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
	"strconv"
	"strings"
	"time"

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
	// MaxExpandedInstances caps the recurrence instances one object may produce
	// while a time-range query is being matched (0 means
	// defaultMaxExpandedInstances).
	MaxExpandedInstances int
}

// defaultMaxExpandedInstances mirrors Radicale's max_expand_occurrence (10000).
// The expansion itself happens inside the library's caldav.Match, which has no
// bound of its own, so the cap is applied here by estimating the instances an
// object needs before the query is delegated.
const defaultMaxExpandedInstances = 10000

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

// QueryCalendarObjects implements the backend interface. Objects are matched
// one by one with the library's caldav.Match (which expands RRULE/RDATE/EXDATE
// when evaluating time-ranges) instead of the atomic caldav.Filter, so a
// single object with a broken recurrence cannot kill the whole query: it is
// re-evaluated as a plain single event, and only logged.
func (b *Backend) QueryCalendarObjects(ctx context.Context, path string, query *webdavcaldav.CalendarQuery) ([]webdavcaldav.CalendarObject, error) {
	objs, err := b.loadObjects(ctx, path)
	if err != nil {
		return nil, err
	}
	if query == nil {
		return objs, nil
	}
	limit := b.MaxExpandedInstances
	if limit <= 0 {
		limit = defaultMaxExpandedInstances
	}
	ranges := queryRanges(&query.CompFilter)
	var out []webdavcaldav.CalendarObject
	for _, obj := range objs {
		if overInstanceBudget(obj.Data.Component, ranges, limit) {
			log.Printf("caldav: %s expands to more than %d instances in the requested range, matching it as a single event", obj.Path, limit)
			stripRecurrence(obj.Data.Component)
		}
		ok, err := webdavcaldav.Match(query.CompFilter, &obj)
		if err != nil {
			log.Printf("caldav: recurrence expansion failed for %s (%v), matching as a single event", obj.Path, err)
			stripRecurrence(obj.Data.Component)
			ok, err = webdavcaldav.Match(query.CompFilter, &obj)
			if err != nil {
				log.Printf("caldav: skipping unmatchable object %s: %v", obj.Path, err)
				continue
			}
		}
		if ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// stripRecurrence removes RRULE/RDATE/EXDATE from every component, so an
// object whose recurrence data cannot be expanded is matched on its plain
// dates instead of failing the query.
func stripRecurrence(comp *ical.Component) {
	for _, name := range []string{"RRULE", "RDATE", "EXDATE"} {
		comp.Props.Del(name)
	}
	for _, child := range comp.Children {
		stripRecurrence(child)
	}
}

// timeRange is a [start, end] pair taken from a component filter.
type timeRange struct {
	start, end time.Time
}

// queryRanges collects the time ranges of a calendar-query filter, nested ones
// included: clients put the range in the VEVENT/VTODO/VJOURNAL filter, below
// the VCALENDAR one.
func queryRanges(f *webdavcaldav.CompFilter) []timeRange {
	var out []timeRange
	if !f.Start.IsZero() {
		out = append(out, timeRange{f.Start, f.End})
	}
	for i := range f.Comps {
		out = append(out, queryRanges(&f.Comps[i])...)
	}
	return out
}

// overInstanceBudget reports whether expanding the recurrence of any component
// of the object would exceed limit instances for any of the requested ranges.
func overInstanceBudget(comp *ical.Component, ranges []timeRange, limit int) bool {
	for _, child := range comp.Children {
		rule := child.Props.Get("RRULE")
		if rule == nil {
			continue
		}
		for _, r := range ranges {
			if instanceBudget(rule.Value, r.start, r.end) > limit {
				return true
			}
		}
	}
	return false
}

// instanceBudget is a deliberately generous upper bound of the instances a
// recurrence generates inside [start, end]. Its only purpose is to stop a
// client from making the server expand a recurrence pathologically, so
// rounding errors are acceptable as long as the bound is never an underestimate
// that would wrongly let the expansion through.
func instanceBudget(rrule string, start, end time.Time) int {
	perInterval := map[string]int64{
		"SECONDLY": 1,
		"MINUTELY": 60,
		"HOURLY":   3600,
		"DAILY":    86400,
		"WEEKLY":   604800,
		// Average lengths: a smaller step yields a larger bound, which is the
		// safe direction for a cap.
		"MONTHLY": 2629746,
		"YEARLY":  31556952,
	}

	var freq string
	interval := int64(1)
	for _, part := range strings.Split(rrule, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToUpper(strings.TrimSpace(key)) {
		case "FREQ":
			freq = strings.ToUpper(value)
		case "INTERVAL":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
				interval = n
			}
		case "COUNT":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				return int(n)
			}
		case "UNTIL":
			if t, err := parseICalTime(value); err == nil && t.Before(end) {
				end = t
			}
		}
	}

	step, ok := perInterval[freq]
	if !ok || !end.After(start) {
		return 1
	}
	seconds := int64(end.Sub(start) / time.Second)
	return int(seconds/(step*interval)) + 2
}

// parseICalTime parses the value of UNTIL, which is a DATE-TIME (optionally in
// UTC) or a DATE.
func parseICalTime(value string) (time.Time, error) {
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("caldav: cannot parse iCalendar time %q", value)
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
