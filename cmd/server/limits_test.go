package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"davcloud/internal/store"
)

// newLimitTestServer builds the real handler with explicit limits, so the caps
// can be exercised without sending real 100 MiB bodies.
func newLimitTestServer(t *testing.T, lim limits) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(newHandler(store.New(dir), filepath.Join(dir, "missing-ui"), "alice", false, lim))
	t.Cleanup(srv.Close)
	return srv, dir
}

// icsWithDescription builds a valid calendar object with a body of the wanted
// size: valid, so a rejection can only come from a limit and not from parsing.
func icsWithDescription(uid string, size int) string {
	return "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//davcloud//limits//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260102T100000Z\r\n" +
		"DESCRIPTION:" + strings.Repeat("a", size) + "\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

// TestPutOverItemSizeIsRejected covers the object cap of issue #39: an object
// bigger than the limit is refused with 413 and leaves nothing behind.
func TestPutOverItemSizeIsRejected(t *testing.T) {
	const itemSize = 4096
	srv, dir := newLimitTestServer(t, limits{RequestSize: 1 << 20, ItemSize: itemSize, Expand: defaultMaxExpand})

	resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/grande.ics", icsWithDescription("grande", 8192), map[string]string{
		"Content-Type": "text/calendar",
	})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT over the object limit = %d, want 413: %s", resp.StatusCode, body)
	}
	// The oversized body was rejected while being read, so no partial file was
	// written in the collection.
	stored := filepath.Join(dir, "collection-root", "alice", "def-calendar", "grande.ics")
	if _, err := os.Stat(stored); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the oversized object left something on disk: %v", err)
	}

	// An object that fits is still accepted and served byte for byte.
	small := icsWithDescription("grande", 64)
	resp, body = do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/grande.ics", small, map[string]string{
		"Content-Type": "text/calendar",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT within the object limit = %d, want 201: %s", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", srv.URL+"/caldav/alice/def-calendar/grande.ics", "", nil)
	if resp.StatusCode != http.StatusOK || body != small {
		t.Errorf("GET after PUT = %d with %d bytes, want 200 with %d bytes", resp.StatusCode, len(body), len(small))
	}
}

// TestOversizedRequestIsRejected covers the request cap: every request body is
// bounded before any handler sees it.
func TestOversizedRequestIsRejected(t *testing.T) {
	srv, _ := newLimitTestServer(t, limits{RequestSize: 2048, ItemSize: 1 << 20, Expand: defaultMaxExpand})

	body := `<d:propfind xmlns:d="DAV:"><d:prop>` +
		strings.Repeat("<d:displayname/>", 200) +
		`</d:prop></d:propfind>`
	resp, out := do(t, "PROPFIND", srv.URL+"/caldav/alice/", body, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("PROPFIND with an oversized body = %d, want 413: %s", resp.StatusCode, out)
	}

	// The same request within the limit still works.
	ok := `<d:propfind xmlns:d="DAV:"><d:prop><d:displayname/></d:prop></d:propfind>`
	resp, out = do(t, "PROPFIND", srv.URL+"/caldav/alice/", ok, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND within the limit = %d, want 207: %s", resp.StatusCode, out)
	}
}

// TestPropfindAdvertisesMaxResourceSize covers the announcement: a client that
// asks for the property learns the cap before sending anything.
func TestPropfindAdvertisesMaxResourceSize(t *testing.T) {
	const itemSize = 4096
	srv, _ := newLimitTestServer(t, limits{RequestSize: 1 << 20, ItemSize: itemSize, Expand: defaultMaxExpand})

	cal := `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:max-resource-size/></d:prop></d:propfind>`
	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/alice/def-calendar/", cal, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND max-resource-size = %d, want 207: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, fmt.Sprintf(">%d<", itemSize)) {
		t.Errorf("the CalDAV answer does not carry the limit %d: %s", itemSize, body)
	}

	card := `<d:propfind xmlns:d="DAV:" xmlns:cr="urn:ietf:params:xml:ns:carddav"><d:prop><cr:max-resource-size/></d:prop></d:propfind>`
	resp, body = do(t, "PROPFIND", srv.URL+"/carddav/alice/def-addressbook/", card, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND max-resource-size on the address book = %d, want 207: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, fmt.Sprintf(">%d<", itemSize)) {
		t.Errorf("the CardDAV answer does not carry the limit %d: %s", itemSize, body)
	}
}

// TestServerHasTimeouts is the regression gate for the Slowloris surface: the
// server is built explicitly, with every timeout set.
func TestServerHasTimeouts(t *testing.T) {
	srv := newServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is not set")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout is not set")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout is not set")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout is not set")
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes is not set")
	}
}

// TestPathologicalRecurrenceQueryIsBounded is the regression gate for the
// expansion cap: a secondly recurrence queried over a decade would make the
// library expand hundreds of millions of instances, so the object is matched on
// its plain dates instead. Without the cap this test does not finish.
func TestPathologicalRecurrenceQueryIsBounded(t *testing.T) {
	srv, _ := newTestServer(t)

	pathological := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//davcloud//limits//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:pathological\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260102T100000Z\r\n" +
		"RRULE:FREQ=SECONDLY\r\n" +
		"SUMMARY:cada segundo\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/pathological.ics", pathological, map[string]string{
		"Content-Type": "text/calendar",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT of the pathological object = %d, want 201: %s", resp.StatusCode, body)
	}

	query := `<?xml version="1.0" encoding="utf-8"?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/></d:prop>
  <c:filter>
    <c:comp-filter name="VCALENDAR">
      <c:comp-filter name="VEVENT">
        <c:time-range start="20260101T000000Z" end="20360101T000000Z"/>
      </c:comp-filter>
    </c:comp-filter>
  </c:filter>
</c:calendar-query>`
	resp, body = do(t, "REPORT", srv.URL+"/caldav/alice/def-calendar/", query, map[string]string{
		"Content-Type": "application/xml",
		"Depth":        "1",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("calendar-query with a pathological recurrence = %d, want 207: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "pathological.ics") {
		t.Errorf("the object itself was not returned: %s", body)
	}
}
