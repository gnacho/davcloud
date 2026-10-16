// Package dav implements the Radicale-compatible URL semantics of the DAV
// endpoints.
//
// Why this package exists: go-webdav v0.7's own handler decides the resource
// type by counting path segments (1 = principal, 2 = home set, 3 = collection,
// 4 = object) and offers no way to configure it, so it expects a layout with
// one more level than Radicale's:
//
//	go-webdav : /user/calendars/cal1/x.ics
//	Radicale  : /user/cal1/x.ics
//
// With the flat layout, a PROPFIND with Depth: 1 on the home set returns no
// collections and a PROPFIND on a collection returns an empty multistatus,
// which would break the OpenCloud agenda/contacts extensions, DAVx⁵,
// Thunderbird and iOS.
//
// The PROPFIND, MKCALENDAR/MKCOL and OPTIONS methods are therefore implemented
// here with Radicale's semantics. Everything else (REPORT, GET, HEAD, PUT,
// DELETE) is delegated to the library handler, whose backends already parse
// the flat layout themselves.
package dav

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// Kind is the flavour of DAV collection served by a handler.
type Kind int

const (
	// KindCalendar serves CalDAV.
	KindCalendar Kind = iota
	// KindAddressBook serves CardDAV.
	KindAddressBook
)

// collectionTag is the .Radicale.props tag matching the kind.
func (k Kind) collectionTag() string {
	if k == KindCalendar {
		return "VCALENDAR"
	}
	return "VADDRESSBOOK"
}

// Handler serves one DAV endpoint (CalDAV or CardDAV).
type Handler struct {
	Kind   Kind
	Store  *store.Store
	Prefix string       // mount prefix, e.g. "/caldav"
	Legacy http.Handler // library handler for REPORT/GET/PUT/DELETE/...
}

// resource is the resolved target of a request.
type resource struct {
	kind    Kind
	user    string
	coll    string // "" for the root and the home set
	item    string // "" unless the target is an object
	collObj store.Collection
}

// externalPrefix returns the prefix clients must see in hrefs. OpenCloud's
// proxy may set X-Script-Name when it strips the prefix before forwarding, so
// that header wins when present.
func (h *Handler) externalPrefix(r *http.Request) string {
	if p := r.Header.Get("X-Script-Name"); p != "" {
		return "/" + strings.Trim(p, "/")
	}
	return "/" + strings.Trim(h.Prefix, "/")
}

// internalPath strips both the mount prefix and X-Script-Name, so the handler
// works whether or not the proxy forwards the prefix in the path.
func (h *Handler) internalPath(r *http.Request) string {
	p := r.URL.Path
	for _, pre := range []string{r.Header.Get("X-Script-Name"), h.Prefix} {
		pre = "/" + strings.Trim(pre, "/")
		if pre != "/" && strings.HasPrefix(p, pre) {
			p = strings.TrimPrefix(p, pre)
			break
		}
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// resolve maps a cleaned internal path to a resource. The authenticated user
// owns everything under its own segment; any other user is forbidden.
func (h *Handler) resolve(r *http.Request) (*resource, error) {
	user := auth.User(r.Context())
	segs := splitPath(h.internalPath(r))

	res := &resource{kind: h.Kind, user: user}
	if len(segs) == 0 {
		return res, nil // root
	}
	if segs[0] != user {
		return nil, errForbidden
	}
	switch len(segs) {
	case 1:
	case 2:
		c, err := h.Store.GetCollection(user, segs[1])
		if err != nil || c.Tag != h.Kind.collectionTag() {
			return nil, errNotFound
		}
		res.coll, res.collObj = segs[1], c
	case 3:
		c, err := h.Store.GetCollection(user, segs[1])
		if err != nil || c.Tag != h.Kind.collectionTag() {
			return nil, errNotFound
		}
		if _, err := h.Store.ItemETag(user, segs[1], segs[2]); err != nil {
			return nil, errNotFound
		}
		res.coll, res.collObj, res.item = segs[1], c, segs[2]
	default:
		return nil, errNotFound
	}
	return res, nil
}

// resolveForCreate maps a path to a collection that is about to be created.
// Unlike resolve it does not require the collection to exist yet, but the
// target must be exactly one level below the user's home set.
func (h *Handler) resolveForCreate(r *http.Request) (*resource, error) {
	user := auth.User(r.Context())
	segs := splitPath(h.internalPath(r))
	if len(segs) != 2 || segs[0] != user {
		return nil, &davError{code: http.StatusForbidden, msg: "dav: collections can only be created under the user home set"}
	}
	if _, err := h.Store.GetCollection(user, segs[1]); err == nil {
		return nil, &davError{code: http.StatusMethodNotAllowed, msg: "dav: collection already exists"}
	}
	return &resource{kind: h.Kind, user: user, coll: segs[1]}, nil
}

// href builds the client-visible path of a collection or object.
func (h *Handler) href(r *http.Request, res *resource) string {
	base := h.externalPrefix(r) + "/" + url.PathEscape(res.user)
	switch {
	case res.coll == "":
		return base + "/"
	case res.item == "":
		return base + "/" + url.PathEscape(res.coll) + "/"
	default:
		return base + "/" + url.PathEscape(res.coll) + "/" + url.PathEscape(res.item)
	}
}

// collections lists the user's collections of the handler's kind, creating the
// defaults on first access (Radicale's predefined_collections).
func (h *Handler) collections(user string) ([]store.Collection, error) {
	if err := h.Store.EnsureUser(user); err != nil {
		return nil, err
	}
	all, err := h.Store.ListCollections(user)
	if err != nil {
		return nil, err
	}
	var out []store.Collection
	for _, c := range all {
		if c.Tag == h.Kind.collectionTag() {
			out = append(out, c)
		}
	}
	return out, nil
}

// ctag computes a collection tag: a value that changes whenever the collection
// content changes, so clients can detect changes without listing every item.
func (h *Handler) ctag(user, coll string) string {
	names, err := h.Store.ListItems(user, coll)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, n := range names {
		etag, err := h.Store.ItemETag(user, coll, n)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "%s:%s\n", n, etag)
	}
	return store.ETag([]byte(sb.String()))
}

// ServeHTTP dispatches by method: the ones whose Radicale semantics differ
// from the library are handled here, the rest are delegated.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "PROPFIND":
		err := h.handlePropfind(w, r)
		if err != nil {
			writeError(w, err)
		}
	case "PROPPATCH":
		err := h.handleProppatch(w, r)
		if err != nil {
			writeError(w, err)
		}
	case "MKCOL", "MKCALENDAR":
		err := h.handleMkcol(w, r)
		if err != nil {
			writeError(w, err)
		}
	case "DELETE":
		// Handled here rather than delegated: the library's CardDAV DELETE
		// resolves the resource type by counting path segments (see the
		// package comment) and returns 403 on the flat layout.
		err := h.handleDelete(w, r)
		if err != nil {
			writeError(w, err)
		}
	case "PUT":
		err := h.handlePut(w, r)
		if err != nil {
			writeError(w, err)
		}
	case "GET", "HEAD":
		err := h.handleGet(w, r)
		if err != nil {
			writeError(w, err)
		}
	case "OPTIONS":
		h.setDavHeaders(w)
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT, MKCOL, MKCALENDAR")
		w.WriteHeader(http.StatusOK)
	default:
		h.Legacy.ServeHTTP(w, r)
	}
}

// setDavHeaders advertises the compliance classes of the endpoint.
func (h *Handler) setDavHeaders(w http.ResponseWriter) {
	w.Header().Set("DAV", davHeader(h.Kind))
}

// Errors used by resolution and rendering.
type davError struct {
	code int
	msg  string
}

func (e *davError) Error() string { return e.msg }

var (
	errForbidden = &davError{code: http.StatusForbidden, msg: "dav: access to another user's data is not allowed"}
	errNotFound  = &davError{code: http.StatusNotFound, msg: "dav: resource not found"}
)

func writeError(w http.ResponseWriter, err error) {
	if de, ok := err.(*davError); ok {
		http.Error(w, de.msg, de.code)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleDelete removes a collection or an object. Radicale answers 204 on
// success and 404 when the resource does not exist.
func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) error {
	// Same as PROPFIND: a client may delete a default collection before any
	// listing, so the defaults are ensured first.
	if user := auth.User(r.Context()); user != "" {
		if _, err := h.collections(user); err != nil {
			return err
		}
	}
	res, err := h.resolve(r)
	if err != nil {
		return err
	}
	switch {
	case res.user == "":
		return &davError{code: http.StatusForbidden, msg: "dav: cannot delete the endpoint root"}
	case res.coll == "":
		return &davError{code: http.StatusForbidden, msg: "dav: cannot delete the home set"}
	}

	if res.item != "" {
		if err := h.Store.DeleteItem(res.user, res.coll, res.item); err != nil {
			return &davError{code: http.StatusNotFound, msg: "dav: object not found"}
		}
	} else {
		if err := h.Store.DeleteCollection(res.user, res.coll); err != nil {
			return &davError{code: http.StatusNotFound, msg: "dav: collection not found"}
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// handleMkcol creates a collection. MKCALENDAR always creates a calendar;
// MKCOL takes the type from the body's resourcetype when present, and falls
// back to the type of the endpoint.
func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request) error {
	res, err := h.resolveForCreate(r)
	if err != nil {
		return err
	}

	var body mkcolBody
	if r.Body != nil {
		if err := decodeXMLBody(r, &body); err != nil {
			return &davError{code: http.StatusBadRequest, msg: "dav: malformed request body"}
		}
	}
	kind := h.Kind
	if r.Method == "MKCALENDAR" {
		kind = KindCalendar
	} else if body.Set.Prop.Resourcetype.AddressBook != nil {
		kind = KindAddressBook
	} else if body.Set.Prop.Resourcetype.Calendar != nil {
		kind = KindCalendar
	}

	name := body.Set.Prop.DisplayName
	if name == "" {
		name = "Personal Collection"
	}
	c := store.Collection{
		ID:          res.coll,
		Tag:         kind.collectionTag(),
		DisplayName: name,
	}
	if kind == KindCalendar {
		c.Components = []string{"VEVENT", "VTODO", "VJOURNAL"}
	}
	if err := h.Store.CreateCollection(res.user, c); err != nil {
		return &davError{code: http.StatusMethodNotAllowed, msg: "dav: collection already exists"}
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}
