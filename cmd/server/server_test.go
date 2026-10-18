package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"davcloud/internal/store"
)

const testICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//davcloud//test//EN\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:e1\r\n" +
	"DTSTAMP:20260101T000000Z\r\n" +
	"DTSTART:20260102T100000Z\r\n" +
	"DTEND:20260102T110000Z\r\n" +
	"SUMMARY:prueba\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

const testVCF = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"UID:c1\r\n" +
	"FN:Ana Prueba\r\n" +
	"N:Prueba;Ana;;;\r\n" +
	"TEL;TYPE=CELL:600000000\r\n" +
	"END:VCARD\r\n"

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:displayname/><d:getetag/></d:prop></d:propfind>`

// newTestServer builds the real handler (same routing as production) in dev
// mode, so requests do not need the X-Remote-User header.
func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	st := store.New(dir)
	srv := httptest.NewServer(newHandler(st, filepath.Join(dir, "missing-ui"), "alice", false, defaultLimits()))
	t.Cleanup(srv.Close)
	return srv, dir
}

func do(t *testing.T, method, url string, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestCalDAVSmokeAndConditionals(t *testing.T) {
	srv, dataDir := newTestServer(t)

	// PROPFIND on the calendar home set: must be a well-formed 207.
	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/", propfindBody, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND /caldav/ = %d, want 207. body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "multistatus") {
		t.Fatalf("PROPFIND body is not a multistatus: %s", body)
	}

	// A PROPFIND without a body means allprop and must not need a
	// Content-Type header at all (some lightweight clients do this).
	resp, body = do(t, "PROPFIND", srv.URL+"/caldav/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("bodyless PROPFIND /caldav/ = %d, want 207. body: %s", resp.StatusCode, body)
	}
	// Discovery must have created the Radicale-compatible default collections.
	calDir := filepath.Join(dataDir, "collection-root", "alice", "def-calendar")
	if _, err := os.Stat(filepath.Join(calDir, ".Radicale.props")); err != nil {
		t.Fatalf("default calendar props missing after PROPFIND: %v", err)
	}

	objURL := srv.URL + "/caldav/alice/def-calendar/e1.ics"

	// Create with If-None-Match: * (what DAVx⁵ sends for a new item).
	resp, body = do(t, "PUT", objURL, testICS, map[string]string{
		"Content-Type":  "text/calendar",
		"If-None-Match": "*",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT (create) = %d, want 201. body: %s", resp.StatusCode, body)
	}

	// The same conditional PUT must now fail: the item exists.
	resp, body = do(t, "PUT", objURL, testICS, map[string]string{
		"Content-Type":  "text/calendar",
		"If-None-Match": "*",
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("PUT with If-None-Match:* over existing item = %d, want 412. body: %s", resp.StatusCode, body)
	}

	// GET returns the stored object with a strong ETag.
	resp, body = do(t, "GET", objURL, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "UID:e1") {
		t.Errorf("GET body does not contain the event: %s", body)
	}
	etag := resp.Header.Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("GET ETag = %q, want a quoted ETag", etag)
	}

	// If-Match with a stale ETag must fail.
	resp, body = do(t, "PUT", objURL, strings.Replace(testICS, "SUMMARY:prueba", "SUMMARY:otro", 1), map[string]string{
		"Content-Type": "text/calendar",
		"If-Match":     `"deadbeef"`,
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("PUT with stale If-Match = %d, want 412. body: %s", resp.StatusCode, body)
	}

	// If-Match with the current ETag must succeed.
	resp, body = do(t, "PUT", objURL, strings.Replace(testICS, "SUMMARY:prueba", "SUMMARY:otro", 1), map[string]string{
		"Content-Type": "text/calendar",
		"If-Match":     etag,
	})
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT with fresh If-Match = %d, want success. body: %s", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", objURL, "", nil)
	if !strings.Contains(body, "SUMMARY:otro") {
		t.Errorf("update did not persist: %s", body)
	}

	// Cross-user access is rejected.
	resp, _ = do(t, "GET", srv.URL+"/caldav/bob/def-calendar/e1.ics", "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-user GET = %d, want 403", resp.StatusCode)
	}

	// DELETE and confirm it is gone.
	resp, body = do(t, "DELETE", objURL, "", nil)
	if resp.StatusCode >= 300 {
		t.Fatalf("DELETE = %d, want success. body: %s", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", objURL, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404", resp.StatusCode)
	}
}

func TestCardDAVSmokeAndConditionals(t *testing.T) {
	srv, dataDir := newTestServer(t)

	resp, body := do(t, "PROPFIND", srv.URL+"/carddav/", propfindBody, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND /carddav/ = %d, want 207. body: %s", resp.StatusCode, body)
	}
	bookDir := filepath.Join(dataDir, "collection-root", "alice", "def-addressbook")
	if _, err := os.Stat(filepath.Join(bookDir, ".Radicale.props")); err != nil {
		t.Fatalf("default address book props missing after PROPFIND: %v", err)
	}

	objURL := srv.URL + "/carddav/alice/def-addressbook/c1.vcf"

	resp, body = do(t, "PUT", objURL, testVCF, map[string]string{
		"Content-Type":  "text/vcard",
		"If-None-Match": "*",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT (create) = %d, want 201. body: %s", resp.StatusCode, body)
	}

	resp, body = do(t, "PUT", objURL, testVCF, map[string]string{
		"Content-Type":  "text/vcard",
		"If-None-Match": "*",
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("PUT with If-None-Match:* over existing card = %d, want 412. body: %s", resp.StatusCode, body)
	}

	resp, body = do(t, "GET", objURL, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", resp.StatusCode)
	}
	// Data fidelity: vCard 3.0 and its properties must survive the round trip
	// (no silent rewrite to 4.0, no dropped TEL).
	if !strings.Contains(body, "VERSION:3.0") {
		t.Errorf("GET body lost the vCard 3.0 version: %s", body)
	}
	if !strings.Contains(body, "TEL") || !strings.Contains(body, "600000000") {
		t.Errorf("GET body lost the TEL property: %s", body)
	}

	resp, _ = do(t, "GET", srv.URL+"/carddav/bob/def-addressbook/c1.vcf", "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-user GET = %d, want 403", resp.StatusCode)
	}

	resp, body = do(t, "DELETE", objURL, "", nil)
	if resp.StatusCode >= 300 {
		t.Fatalf("DELETE = %d, want success. body: %s", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", objURL, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404", resp.StatusCode)
	}
}

// TestIdentityIsRequired proves the auth contract: without the OpenCloud proxy
// (or dev mode) there is no identity, so DAV endpoints must refuse to serve.
func TestIdentityIsRequired(t *testing.T) {
	dir := t.TempDir()
	st := store.New(dir)
	srv := httptest.NewServer(newHandler(st, filepath.Join(dir, "missing-ui"), "", false, defaultLimits()))
	t.Cleanup(srv.Close)

	resp, _ := do(t, "PROPFIND", srv.URL+"/caldav/", propfindBody, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("PROPFIND without X-Remote-User = %d, want 401", resp.StatusCode)
	}

	// With the header injected (as the proxy does) the same request works.
	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/", propfindBody, map[string]string{
		"Depth":         "1",
		"Content-Type":  "application/xml; charset=utf-8",
		"X-Remote-User": "alice",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("PROPFIND with X-Remote-User = %d, want 207. body: %s", resp.StatusCode, body)
	}
}

// TestMissingParentCollectionIsConflict documents the behaviour for a PUT
// into a calendar that does not exist: 409, never a stray directory.
func TestMissingParentCollectionIsConflict(t *testing.T) {
	srv, dataDir := newTestServer(t)
	resp, body := do(t, "PUT", srv.URL+"/caldav/alice/no-such-cal/e1.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("PUT into missing calendar = %d, want 409. body: %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "collection-root", "alice", "no-such-cal")); !os.IsNotExist(err) {
		t.Error("server created a directory for a missing collection")
	}
}
