// Package caldav implements the go-webdav v0.7 CalDAV backend on top of the
// Radicale-compatible store.
//
// URL layout (after the /caldav prefix is stripped by the mux):
//
//	/<user>/                  -> calendar home set
//	/<user>/<calendar>/       -> calendar collection
//	/<user>/<calendar>/x.ics  -> calendar object
//
// The authenticated user always comes from the auth context; any <user>
// segment that does not match it is rejected (no cross-user access yet).
//
// Written against the v0.7 API: caldav.Handler{Backend, Prefix},
// PutCalendarObject (unified create/update) and caldav.Filter for queries.
package caldav

import (
	"bytes"
	"context"
	"net/http"
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
}

// New creates the backend.
func New(st *store.Store) *Backend { return &Backend{st: st} }

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

// userFromCtx validates that the URL user segment matches the authenticated
// user and returns it.
func userFromCtx(ctx context.Context, urlUser string) (string, error) {
	u := auth.User(ctx)
	if urlUser == "" || urlUser != u {
		return "", &webdav.HTTPError{Code: http.StatusForbidden}
	}
	return u, nil
}

// CurrentUserPrincipal implements webdav.UserPrincipalBackend.
func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/" + auth.User(ctx), nil
}

// CalendarHomeSetPath implements the v0.7 backend interface (no principalPath).
func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return "/" + auth.User(ctx) + "/", nil
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
			Path:                  "/" + user + "/" + c.ID,
			Name:                  c.DisplayName,
			SupportedComponentSet: c.Components,
		})
	}
	return out, nil
}

// GetCalendar implements the backend interface.
func (b *Backend) GetCalendar(ctx context.Context, path string) (*webdavcaldav.Calendar, error) {
	segs := splitPath(path)
	if len(segs) != 2 {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	c, err := b.st.GetCollection(user, segs[1])
	if err != nil || c.Tag != "VCALENDAR" {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	return &webdavcaldav.Calendar{
		Path:                  path,
		Name:                  c.DisplayName,
		SupportedComponentSet: c.Components,
	}, nil
}

// CreateCalendar implements the backend interface (path comes from cal.Path).
func (b *Backend) CreateCalendar(ctx context.Context, cal *webdavcaldav.Calendar) error {
	segs := splitPath(cal.Path)
	if len(segs) != 2 {
		return &webdav.HTTPError{Code: http.StatusBadRequest}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	comps := cal.SupportedComponentSet
	if len(comps) == 0 {
		comps = []string{"VEVENT", "VTODO", "VJOURNAL"}
	}
	return b.st.CreateCollection(user, store.Collection{
		ID:          segs[1],
		Tag:         "VCALENDAR",
		DisplayName: cal.Name,
		Components:  comps,
	})
}

// DeleteCalendar implements the backend interface.
func (b *Backend) DeleteCalendar(ctx context.Context, path string) error {
	segs := splitPath(path)
	if len(segs) != 2 {
		return &webdav.HTTPError{Code: http.StatusNotFound}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	return b.st.DeleteCollection(user, segs[1])
}

func decodeICS(data []byte) (*ical.Calendar, error) {
	return ical.NewDecoder(bytes.NewReader(data)).Decode()
}

func (b *Backend) loadObject(ctx context.Context, collPath, name string) (*webdavcaldav.CalendarObject, error) {
	segs := splitPath(collPath)
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	data, err := b.st.ReadItem(user, segs[1], name)
	if err != nil {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	cal, err := decodeICS(data)
	if err != nil {
		return nil, &webdav.HTTPError{Code: http.StatusInternalServerError}
	}
	return &webdavcaldav.CalendarObject{
		Path: collPath + "/" + name,
		Data: cal,
		ETag: store.ETag(data),
	}, nil
}

// GetCalendarObject implements the backend interface.
func (b *Backend) GetCalendarObject(ctx context.Context, path string, req *webdavcaldav.CalendarCompRequest) (*webdavcaldav.CalendarObject, error) {
	segs := splitPath(path)
	if len(segs) != 3 {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	return b.loadObject(ctx, "/"+segs[0]+"/"+segs[1], segs[2])
}

func (b *Backend) loadObjects(ctx context.Context, collPath string) ([]webdavcaldav.CalendarObject, error) {
	segs := splitPath(collPath)
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	names, err := b.st.ListItems(user, segs[1])
	if err != nil {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	var out []webdavcaldav.CalendarObject
	for _, n := range names {
		obj, err := b.loadObject(ctx, collPath, n)
		if err != nil {
			continue // skip unreadable items
		}
		out = append(out, *obj)
	}
	return out, nil
}

// ListCalendarObjects implements the backend interface.
func (b *Backend) ListCalendarObjects(ctx context.Context, path string, req *webdavcaldav.CalendarCompRequest) ([]webdavcaldav.CalendarObject, error) {
	return b.loadObjects(ctx, path)
}

// QueryCalendarObjects implements the backend interface using the library's
// own filter evaluation (caldav.Filter / caldav.Match handle time-range and
// component filters in v0.7).
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

// PutCalendarObject implements the unified create/update of the v0.7 API.
// TODO: honour opts (If-Match / If-None-Match conditions) from
// PutCalendarObjectOptions once confirmed against pkg.go.dev.
func (b *Backend) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *webdavcaldav.PutCalendarObjectOptions) (*webdavcaldav.CalendarObject, error) {
	segs := splitPath(path)
	if len(segs) != 3 {
		return nil, &webdav.HTTPError{Code: http.StatusBadRequest}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	data, err := encodeICS(cal)
	if err != nil {
		return nil, err
	}
	etag, err := b.st.WriteItem(user, segs[1], segs[2], data)
	if err != nil {
		return nil, err
	}
	return &webdavcaldav.CalendarObject{Path: path, Data: cal, ETag: etag}, nil
}

// DeleteCalendarObject implements the backend interface.
func (b *Backend) DeleteCalendarObject(ctx context.Context, path string) error {
	segs := splitPath(path)
	if len(segs) != 3 {
		return &webdav.HTTPError{Code: http.StatusNotFound}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	if err := b.st.DeleteItem(user, segs[1], segs[2]); err != nil {
		return &webdav.HTTPError{Code: http.StatusNotFound}
	}
	return nil
}
