// Package caldav implements the go-webdav v0.7 CalDAV backend on top of the
// Radicale-compatible store.
//
// URL layout (after the /caldav prefix is stripped by the mux or by the
// library handler):
//
//	/<user>/                  -> calendar home set
//	/<user>/<calendar>/       -> calendar collection
//	/<user>/<calendar>/x.ics  -> calendar object
//
// The authenticated user always comes from the auth context; any <user>
// segment that does not match it is rejected (no cross-user access yet).
//
// Written against the real go-webdav v0.7 API: caldav.Handler{Backend,
// Prefix}, PutCalendarObject (unified create/update) with preconditions,
// caldav.Filter for queries and webdav.NewHTTPError for status mapping.
package caldav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	webdavcaldav "github.com/emersion/go-webdav/caldav"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// Backend implements webdavcaldav.Backend.
type Backend struct {
	st *store.Store
	// Prefix is the mount point of the endpoint ("/caldav"). The paths that
	// cross the library handler are server paths and include it, while the
	// store only knows the local "/<user>/<collection>[/<item>]" layout.
	Prefix string
}

// New creates the backend mounted at prefix.
func New(st *store.Store, prefix string) *Backend {
	return &Backend{st: st, Prefix: "/" + strings.Trim(prefix, "/")}
}

// local strips the mount prefix from a server path, yielding the path the
// store works with. It is idempotent, so it can be applied to paths that have
// already been normalised.
func (b *Backend) local(p string) string {
	if b.Prefix != "/" && strings.HasPrefix(p, b.Prefix) {
		p = strings.TrimPrefix(p, b.Prefix)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// serverPath is the inverse of local: it builds the client visible path.
func (b *Backend) serverPath(local string) string {
	if local == "" || local == "/" {
		return b.Prefix + "/"
	}
	return b.Prefix + local
}

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

// userFromCtx validates that the URL user segment matches the authenticated
// user and returns it.
func userFromCtx(ctx context.Context, urlUser string) (string, error) {
	u := auth.User(ctx)
	if urlUser == "" || urlUser != u {
		return "", webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("caldav: url user %q does not match authenticated user %q", urlUser, u))
	}
	return u, nil
}

// collectionPath returns "/<user>/<collection>" from a 3-segment object path.
func collectionPath(segs []string) string { return "/" + segs[0] + "/" + segs[1] }

// CurrentUserPrincipal implements webdav.UserPrincipalBackend.
func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.serverPath("/" + auth.User(ctx)), nil
}

// CalendarHomeSetPath implements the v0.7 backend interface (no principalPath).
func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return b.serverPath("/" + auth.User(ctx) + "/"), nil
}

// ListCalendars implements the backend interface.
func (b *Backend) ListCalendars(ctx context.Context) ([]webdavcaldav.Calendar, error) {
	user := auth.User(ctx)
	if err := b.st.EnsureUser(user); err != nil {
		return nil, err
	}
	cols, err := b.st.ListCollections(user)
	if err != nil {
		return nil, err
	}
	var out []webdavcaldav.Calendar
	for _, c := range cols {
		if c.Tag != "VCALENDAR" {
			continue
		}
		out = append(out, webdavcaldav.Calendar{
			Path:                  b.serverPath("/" + user + "/" + c.ID),
			Name:                  c.DisplayName,
			SupportedComponentSet: c.Components,
		})
	}
	return out, nil
}

// GetCalendar implements the backend interface.
func (b *Backend) GetCalendar(ctx context.Context, path string) (*webdavcaldav.Calendar, error) {
	segs := splitPath(b.local(path))
	if len(segs) != 2 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: bad calendar path %q", path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	c, err := b.st.GetCollection(user, segs[1])
	if err != nil || c.Tag != "VCALENDAR" {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: calendar %q not found", path))
	}
	return &webdavcaldav.Calendar{
		Path:                  b.serverPath(b.local(path)),
		Name:                  c.DisplayName,
		SupportedComponentSet: c.Components,
	}, nil
}

// CreateCalendar implements the backend interface (path comes from cal.Path).
func (b *Backend) CreateCalendar(ctx context.Context, cal *webdavcaldav.Calendar) error {
	segs := splitPath(b.local(cal.Path))
	if len(segs) != 2 {
		return webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("caldav: bad calendar path %q", cal.Path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	comps := cal.SupportedComponentSet
	if len(comps) == 0 {
		comps = []string{"VEVENT", "VTODO", "VJOURNAL"}
	}
	err = b.st.CreateCollection(user, store.Collection{
		ID:          segs[1],
		Tag:         "VCALENDAR",
		DisplayName: cal.Name,
		Components:  comps,
	})
	if errors.Is(err, os.ErrExist) {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, fmt.Errorf("caldav: calendar %q already exists", cal.Path))
	}
	return err
}

// DeleteCalendar implements the backend interface.
func (b *Backend) DeleteCalendar(ctx context.Context, path string) error {
	segs := splitPath(b.local(path))
	if len(segs) != 2 {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: bad calendar path %q", path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	if err := b.st.DeleteCollection(user, segs[1]); err != nil {
		return err
	}
	return nil
}

func decodeICS(data []byte) (*ical.Calendar, error) {
	return ical.NewDecoder(bytes.NewReader(data)).Decode()
}

func (b *Backend) loadObject(ctx context.Context, collPath, name string) (*webdavcaldav.CalendarObject, error) {
	local := strings.TrimSuffix(b.local(collPath), "/")
	segs := splitPath(local)
	if len(segs) != 2 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: bad collection path %q", collPath))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	data, err := b.st.ReadItem(user, segs[1], name)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: object %s/%s not found", collPath, name))
	}
	cal, err := decodeICS(data)
	if err != nil {
		// The item exists on disk but is not parseable: report it loudly
		// instead of hiding the data, and still fail the read.
		log.Printf("caldav: unparseable object %s/%s: %v", collPath, name, err)
		return nil, webdav.NewHTTPError(http.StatusInternalServerError, fmt.Errorf("caldav: unparseable object %s/%s", collPath, name))
	}
	return &webdavcaldav.CalendarObject{
		Path:          b.serverPath(local + "/" + name),
		Data:          cal,
		ETag:          store.ETag(data),
		ContentLength: int64(len(data)),
	}, nil
}

// GetCalendarObject implements the backend interface.
func (b *Backend) GetCalendarObject(ctx context.Context, path string, req *webdavcaldav.CalendarCompRequest) (*webdavcaldav.CalendarObject, error) {
	segs := splitPath(b.local(path))
	if len(segs) != 3 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: bad object path %q", path))
	}
	return b.loadObject(ctx, collectionPath(segs), segs[2])
}

func (b *Backend) loadObjects(ctx context.Context, collPath string) ([]webdavcaldav.CalendarObject, error) {
	segs := splitPath(b.local(collPath))
	if len(segs) != 2 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: bad collection path %q", collPath))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	if err := b.st.EnsureUser(user); err != nil {
		return nil, err
	}
	names, err := b.st.ListItems(user, segs[1])
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: collection %q not found", collPath))
	}
	var out []webdavcaldav.CalendarObject
	for _, n := range names {
		obj, err := b.loadObject(ctx, collPath, n)
		if err != nil {
			// A single broken item must not hide the rest of the calendar,
			// but the skip is logged (silent drops are how data disappears).
			log.Printf("caldav: skipping %s/%s: %v", collPath, n, err)
			continue
		}
		out = append(out, *obj)
	}
	return out, nil
}

// ListCalendarObjects implements the backend interface.
func (b *Backend) ListCalendarObjects(ctx context.Context, path string, req *webdavcaldav.CalendarCompRequest) ([]webdavcaldav.CalendarObject, error) {
	return b.loadObjects(ctx, path)
}

// QueryCalendarObjects implements the backend interface. The library's own
// caldav.Filter/caldav.Match evaluate component and time-range filters; the
// time-range evaluation of recurring series (RRULE/RDATE/EXDATE expansion) is
// NOT done here yet, so a recurring master that starts outside the range but
// has occurrences inside it may be missed. Clients expand locally meanwhile.
func (b *Backend) QueryCalendarObjects(ctx context.Context, path string, query *webdavcaldav.CalendarQuery) ([]webdavcaldav.CalendarObject, error) {
	objs, err := b.loadObjects(ctx, path)
	if err != nil {
		return nil, err
	}
	if query == nil {
		return objs, nil
	}
	return webdavcaldav.Filter(query, objs)
}

func encodeICS(cal *ical.Calendar) ([]byte, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkPreconditions implements the same semantics as go-webdav's own
// filesystem backend (fs_local.go): a failed If-Match or a matching
// If-None-Match is a 412 Precondition Failed.
func checkPreconditions(opts *webdavcaldav.PutCalendarObjectOptions, existingETag string) error {
	if opts == nil {
		return nil
	}
	if opts.IfMatch.IsSet() {
		ok, err := opts.IfMatch.MatchETag(existingETag)
		if err != nil {
			return webdav.NewHTTPError(http.StatusBadRequest, err)
		}
		if !ok {
			return webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("caldav: If-Match condition failed"))
		}
	}
	if opts.IfNoneMatch.IsSet() {
		ok, err := opts.IfNoneMatch.MatchETag(existingETag)
		if err != nil {
			return webdav.NewHTTPError(http.StatusBadRequest, err)
		}
		if ok {
			return webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("caldav: If-None-Match condition failed"))
		}
	}
	return nil
}

// PutCalendarObject implements the unified create/update of the v0.7 API,
// honouring the If-Match / If-None-Match preconditions from
// PutCalendarObjectOptions.
func (b *Backend) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *webdavcaldav.PutCalendarObjectOptions) (*webdavcaldav.CalendarObject, error) {
	segs := splitPath(b.local(path))
	if len(segs) != 3 {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("caldav: bad object path %q", path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	// Make sure the user's default collections exist (same as Radicale's
	// predefined_collections) so a PUT arriving before discovery still lands.
	if err := b.st.EnsureUser(user); err != nil {
		return nil, err
	}
	c, err := b.st.GetCollection(user, segs[1])
	if err != nil || c.Tag != "VCALENDAR" {
		return nil, webdav.NewHTTPError(http.StatusConflict, fmt.Errorf("caldav: parent calendar %q does not exist", segs[1]))
	}

	existing := ""
	if etag, err := b.st.ItemETag(user, segs[1], segs[2]); err == nil {
		existing = etag
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := checkPreconditions(opts, existing); err != nil {
		return nil, err
	}

	data, err := encodeICS(cal)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("caldav: cannot encode calendar object: %w", err))
	}
	etag, err := b.st.WriteItem(user, segs[1], segs[2], data)
	if err != nil {
		return nil, err
	}
	return &webdavcaldav.CalendarObject{
		Path:          b.serverPath(b.local(path)),
		Data:          cal,
		ETag:          etag,
		ContentLength: int64(len(data)),
	}, nil
}

// DeleteCalendarObject implements the backend interface.
func (b *Backend) DeleteCalendarObject(ctx context.Context, path string) error {
	segs := splitPath(b.local(path))
	if len(segs) != 3 {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: bad object path %q", path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	if err := b.st.DeleteItem(user, segs[1], segs[2]); err != nil {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("caldav: object %q not found", path))
	}
	return nil
}
