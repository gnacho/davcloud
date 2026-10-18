package dav

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// PUT and GET of objects are handled here rather than delegated to go-webdav
// so that the store keeps the raw request body byte for byte. Radicale stores
// the item as the client sent it; decoding and re-encoding (what the library
// backend does) reorders properties and changes the file on disk, which would
// make the data volume differ from a Radicale one and complicate the rollback
// drill of the migration plan. REPORT stays delegated: its calendar-data /
// address-data payloads are inherently normalised by the query machinery.

// itemMediaType returns the media type expected for an item of the endpoint.
func (h *Handler) itemMediaType() string {
	if h.Kind == KindCalendar {
		return "text/calendar"
	}
	return "text/vcard"
}

// resolveForWrite resolves the target of a PUT: the parent collection must
// exist (409 otherwise, per RFC 4918), but the object itself may not exist
// yet.
func (h *Handler) resolveForWrite(r *http.Request) (*resource, error) {
	user := auth.User(r.Context())
	segs := splitPath(h.internalPath(r))
	if len(segs) != 3 || segs[0] != user {
		return nil, &davError{code: http.StatusForbidden, msg: "dav: objects live at /<user>/<collection>/<item>"}
	}
	if !store.ValidName(segs[1]) || !store.ValidName(segs[2]) {
		return nil, errReservedName
	}
	c, err := h.Store.GetCollection(user, segs[1])
	if err != nil || c.Tag != h.Kind.collectionTag() {
		return nil, &davError{code: http.StatusConflict, msg: "dav: the parent collection does not exist"}
	}
	return &resource{kind: h.Kind, user: user, coll: segs[1], collObj: c, item: segs[2]}, nil
}

// validateItem parses the raw body and returns the item UID. It rejects
// content that is not a parseable iCalendar or vCard stream, and, like
// Radicale, a PUT whose resource name does not match the UID inside the data.
func (h *Handler) validateItem(name string, raw []byte) (uid string, err error) {
	if h.Kind == KindCalendar {
		cal, err := ical.NewDecoder(bytes.NewReader(raw)).Decode()
		if err != nil {
			return "", fmt.Errorf("dav: the request body is not a valid iCalendar stream")
		}
		// RFC 4791: a calendar object resource holds one component plus, for a
		// recurring event, overrides that share its UID and carry
		// RECURRENCE-ID. Several unrelated events under one href would share a
		// single ETag, so no client could move, edit or delete them on their
		// own, and a component without UID would be unaddressable.
		for _, comp := range cal.Children {
			switch comp.Name {
			case "VEVENT", "VTODO", "VJOURNAL":
				p := comp.Props.Get("UID")
				if p == nil || p.Value == "" {
					return "", fmt.Errorf("dav: the calendar object has a %s without UID", comp.Name)
				}
				if uid == "" {
					uid = p.Value
					continue
				}
				if uid != p.Value {
					return "", fmt.Errorf("dav: the calendar object has components with different UIDs: %q and %q", uid, p.Value)
				}
			}
		}
	} else {
		dec := vcard.NewDecoder(bytes.NewReader(raw))
		card, err := dec.Decode()
		if err != nil {
			return "", fmt.Errorf("dav: the request body is not a valid vCard stream")
		}
		// One vCard per resource, for the same reason as iCalendar. The
		// decoder stops at the end of a card, so a second card that decodes
		// means the body carries one no href could ever address.
		if _, err := dec.Decode(); err == nil {
			return "", fmt.Errorf("dav: the address object has more than one vCard")
		}
		uid = card.Value("UID")
	}
	if uid == "" {
		return "", fmt.Errorf("dav: the calendar or address object has no UID")
	}
	// Radicale requires the resource name (minus extension) to equal the UID,
	// so one UID can never be stored twice under different hrefs.
	base := strings.TrimSuffix(name, ".ics")
	base = strings.TrimSuffix(base, ".vcf")
	if base != uid {
		return "", fmt.Errorf("dav: the resource name %q does not match the UID %q", name, uid)
	}
	return uid, nil
}

// matchPreconditions evaluates If-Match / If-None-Match against the current
// state of the item, with the same semantics as the library filesystem
// backend: a failed If-Match or a matching If-None-Match is 412.
func matchPreconditions(r *http.Request, existingETag string) error {
	ifMatch := webdav.ConditionalMatch(r.Header.Get("If-Match"))
	if ifMatch.IsSet() {
		ok, err := ifMatch.MatchETag(existingETag)
		if err != nil {
			return &davError{code: http.StatusBadRequest, msg: "dav: malformed If-Match header"}
		}
		if !ok {
			return &davError{code: http.StatusPreconditionFailed, msg: "dav: If-Match condition failed"}
		}
	}
	ifNoneMatch := webdav.ConditionalMatch(r.Header.Get("If-None-Match"))
	if ifNoneMatch.IsSet() {
		ok, err := ifNoneMatch.MatchETag(existingETag)
		if err != nil {
			return &davError{code: http.StatusBadRequest, msg: "dav: malformed If-None-Match header"}
		}
		if ok {
			return &davError{code: http.StatusPreconditionFailed, msg: "dav: If-None-Match condition failed"}
		}
	}
	return nil
}

// handlePut stores an object, keeping the raw request body on disk.
func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) error {
	// A client may PUT into a default collection before any listing, same as
	// with PROPFIND and PROPPATCH.
	if user := auth.User(r.Context()); user != "" {
		if _, err := h.collections(user); err != nil {
			return err
		}
	}
	res, err := h.resolveForWrite(r)
	if err != nil {
		return err
	}
	if h.MaxItemSize > 0 {
		// The object cap is applied while reading, so an oversized body is never
		// buffered in full and no partial file is left behind.
		r.Body = http.MaxBytesReader(w, r.Body, h.MaxItemSize)
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return bodyError(err, "dav: cannot read the request body")
	}
	if _, err := h.validateItem(res.item, raw); err != nil {
		return &davError{code: http.StatusBadRequest, msg: err.Error()}
	}

	existingETag := ""
	if e, err := h.Store.ItemETag(res.user, res.coll, res.item); err == nil {
		existingETag = e
	}
	if err := matchPreconditions(r, existingETag); err != nil {
		return err
	}

	etag, err := h.Store.WriteItem(res.user, res.coll, res.item, raw)
	if err != nil {
		return &davError{code: http.StatusInternalServerError, msg: fmt.Sprintf("dav: cannot write the item: %v", err)}
	}
	w.Header().Set("ETag", store.QuoteETag(etag))
	w.Header().Set("Location", h.href(r, res))
	w.WriteHeader(http.StatusCreated)
	return nil
}

// handleGet serves an object exactly as it is stored on disk, with strong
// ETag and 304 handling on If-None-Match and If-Modified-Since.
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) error {
	res, err := h.resolve(r)
	if err != nil {
		return err
	}
	if res.item == "" {
		// GET on a collection or home set is delegated to the library (it
		// answers with a small informational page, like Radicale's).
		h.Legacy.ServeHTTP(w, r)
		return nil
	}
	raw, err := h.Store.ReadItem(res.user, res.coll, res.item)
	if err != nil {
		return &davError{code: http.StatusNotFound, msg: "dav: object not found"}
	}
	etag := store.QuoteETag(store.ETag(raw))

	w.Header().Set("ETag", etag)
	if mod, err := h.Store.ItemModTime(res.user, res.coll, res.item); err == nil {
		lastMod := mod.UTC().Truncate(time.Second)
		w.Header().Set("Last-Modified", lastMod.Format(http.TimeFormat))
		if t, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !lastMod.After(t) {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	if inm := webdav.ConditionalMatch(r.Header.Get("If-None-Match")); inm.IsSet() {
		ok, err := inm.MatchETag(store.ETag(raw))
		if err == nil && ok {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	w.Header().Set("Content-Type", h.itemMediaType()+"; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(raw)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
	return nil
}
