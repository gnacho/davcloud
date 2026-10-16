package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"davcloud/internal/store"
)

// --- multistatus parsing helpers -------------------------------------------

type propElt struct {
	XMLName xml.Name
	Inner   string `xml:",innerxml"`
}

// textOf returns the text content of a property. encoding/xml does not fill a
// chardata field on a struct that also captures innerxml, so the escaped text
// is decoded here.
func textOf(p propElt) string {
	var v struct {
		Text string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte("<v>"+p.Inner+"</v>"), &v); err != nil {
		return p.Inner
	}
	return v.Text
}

type propContainer struct {
	Props []propElt `xml:",any"`
}

type propstatElt struct {
	Prop   propContainer `xml:"prop"`
	Status string        `xml:"status"`
}

type responseElt struct {
	Href      string        `xml:"href"`
	Propstats []propstatElt `xml:"propstat"`
}

type multistatusDoc struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []responseElt `xml:"response"`
}

func parseMultiStatus(t *testing.T, body string) multistatusDoc {
	t.Helper()
	var doc multistatusDoc
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("multistatus is not valid XML: %v\nbody: %s", err, body)
	}
	return doc
}

// propsOf returns the properties answered with 200 for one href, keyed by
// "space local".
func propsOf(ms multistatusDoc, href string) map[string]propElt {
	out := map[string]propElt{}
	for _, resp := range ms.Responses {
		if resp.Href != href {
			continue
		}
		for _, ps := range resp.Propstats {
			if !strings.Contains(ps.Status, "200") {
				continue
			}
			for _, p := range ps.Prop.Props {
				out[p.XMLName.Space+" "+p.XMLName.Local] = p
			}
		}
	}
	return out
}

// notFoundOf returns the property names answered with 404 for one href.
func notFoundOf(ms multistatusDoc, href string) []string {
	var out []string
	for _, resp := range ms.Responses {
		if resp.Href != href {
			continue
		}
		for _, ps := range resp.Propstats {
			if !strings.Contains(ps.Status, "404") {
				continue
			}
			for _, p := range ps.Prop.Props {
				out = append(out, p.XMLName.Space+" "+p.XMLName.Local)
			}
		}
	}
	return out
}

func hrefsOf(ms multistatusDoc) []string {
	out := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		out = append(out, r.Href)
	}
	return out
}

// keysOf lists the property names of a parsed resource, for diagnostics.
func keysOf(props map[string]propElt) []string {
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- tests -----------------------------------------------------------------

// TestPropfindHomeSetListsFlatCollections is the core parity test: a PROPFIND
// with Depth: 1 on the home set must list the user's collections with the same
// hrefs Radicale produces, one level flatter than go-webdav's own layout.
func TestPropfindHomeSetListsFlatCollections(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/">
  <d:prop><d:resourcetype/><d:displayname/><cs:getctag/></d:prop>
</d:propfind>`
	resp, out := do(t, "PROPFIND", srv.URL+"/caldav/alice/", body, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND home set = %d, want 207: %s", resp.StatusCode, out)
	}
	ms := parseMultiStatus(t, out)
	hrefs := hrefsOf(ms)
	if len(hrefs) < 2 {
		t.Fatalf("home set listing returned %d responses, want the home set plus the default calendar: %v", len(hrefs), hrefs)
	}
	if hrefs[0] != "/caldav/alice/" {
		t.Errorf("first href = %q, want /caldav/alice/", hrefs[0])
	}
	cal := propsOf(ms, "/caldav/alice/def-calendar/")
	if len(cal) == 0 {
		t.Fatalf("default calendar not listed under the home set: %v", hrefs)
	}
	if !strings.Contains(cal["DAV: resourcetype"].Inner, "calendar") {
		t.Errorf("calendar resourcetype = %q, want a caldav calendar element", cal["DAV: resourcetype"].Inner)
	}
	if !strings.Contains(textOf(cal["DAV: displayname"]), "Personal Calendar") {
		t.Errorf("calendar displayname = %q, want Personal Calendar\ndoc: %s", textOf(cal["DAV: displayname"]), out)
	}
	if textOf(cal["http://calendarserver.org/ns/ getctag"]) == "" {
		t.Errorf("calendar has no getctag\nprops sent: %v\ndoc: %s", keysOf(cal), out)
	}
}

// TestPropfindCollectionListsItems checks the Depth: 1 listing of a calendar
// itself: the objects, with their ETags and the flat hrefs.
func TestPropfindCollectionListsItems(t *testing.T) {
	srv, _ := newTestServer(t)

	if resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/e1.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	}); resp.StatusCode >= 300 {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	req := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:getcontenttype/><d:displayname/></d:prop></d:propfind>`
	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/alice/def-calendar/", req, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND collection = %d, want 207: %s", resp.StatusCode, body)
	}
	ms := parseMultiStatus(t, body)
	itemHref := "/caldav/alice/def-calendar/e1.ics"
	item := propsOf(ms, itemHref)
	if len(item) == 0 {
		t.Fatalf("item %s not listed: %v", itemHref, hrefsOf(ms))
	}
	if !strings.HasPrefix(textOf(item["DAV: getetag"]), `"`) {
		t.Errorf("item getetag = %q, want a quoted ETag", textOf(item["DAV: getetag"]))
	}
	if !strings.Contains(textOf(item["DAV: getcontenttype"]), "text/calendar") {
		t.Errorf("item getcontenttype = %q", textOf(item["DAV: getcontenttype"]))
	}
}

// TestPropfindUnknownPropertyIs404 checks the RFC 4918 requirement that
// unsupported properties are answered in a separate 404 propstat instead of
// being silently dropped.
func TestPropfindUnknownPropertyIs404(t *testing.T) {
	srv, _ := newTestServer(t)

	req := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:prop><d:displayname/><d:quota-available-bytes/><ic:calendar-color/></d:prop>
</d:propfind>`
	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/alice/def-calendar/", req, map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d, want 207: %s", resp.StatusCode, body)
	}
	ms := parseMultiStatus(t, body)
	href := "/caldav/alice/def-calendar/"
	if got := propsOf(ms, href); len(got) == 0 {
		t.Error("displayname was not answered with 200")
	}
	missing := strings.Join(notFoundOf(ms, href), ",")
	if !strings.Contains(missing, "quota-available-bytes") || !strings.Contains(missing, "calendar-color") {
		t.Errorf("404 propstat = %q, want quota-available-bytes and calendar-color", missing)
	}
}

// TestPropfindCtagChangesWithContent checks that the collection tag changes
// when the collection content changes: without sync-collection this is what
// lets clients detect changes cheaply.
func TestPropfindCtagChangesWithContent(t *testing.T) {
	srv, _ := newTestServer(t)

	req := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/">
  <d:prop><cs:getctag/></d:prop></d:propfind>`
	getCtag := func() string {
		t.Helper()
		resp, body := do(t, "PROPFIND", srv.URL+"/caldav/alice/def-calendar/", req, map[string]string{"Depth": "0"})
		if resp.StatusCode != http.StatusMultiStatus {
			t.Fatalf("PROPFIND = %d: %s", resp.StatusCode, body)
		}
		return textOf(propsOf(parseMultiStatus(t, body), "/caldav/alice/def-calendar/")["http://calendarserver.org/ns/ getctag"])
	}

	before := getCtag()
	if before == "" {
		t.Fatal("no ctag")
	}
	if resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/e1.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	}); resp.StatusCode >= 300 {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	if after := getCtag(); after == before {
		t.Errorf("ctag did not change after adding an item: %q", after)
	}
}

// TestMkcalendarCreatesCollection covers collection creation, which the
// library handler refuses on the flat layout.
func TestMkcalendarCreatesCollection(t *testing.T) {
	srv, dataDir := newTestServer(t)

	body := `<?xml version="1.0" encoding="utf-8"?>
<cal:mkcalendar xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:d="DAV:">
  <d:set><d:prop><d:displayname>Trabajo</d:displayname></d:prop></d:set>
</cal:mkcalendar>`
	resp, out := do(t, "MKCALENDAR", srv.URL+"/caldav/alice/trabajo", body, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCALENDAR = %d, want 201: %s", resp.StatusCode, out)
	}
	props := filepath.Join(dataDir, "collection-root", "alice", "trabajo", ".Radicale.props")
	b, err := os.ReadFile(props)
	if err != nil {
		t.Fatalf("collection props missing: %v", err)
	}
	if !strings.Contains(string(b), "Trabajo") || !strings.Contains(string(b), "VCALENDAR") {
		t.Errorf("collection props = %s, want the displayname and the VCALENDAR tag", b)
	}

	// Recreating it is a conflict, not a silent overwrite.
	resp, _ = do(t, "MKCALENDAR", srv.URL+"/caldav/alice/trabajo", body, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("second MKCALENDAR = %d, want 405", resp.StatusCode)
	}
}

// TestWellKnownRedirects implements RFC 6764 discovery.
func TestWellKnownRedirects(t *testing.T) {
	srv, _ := newTestServer(t)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, tc := range []struct{ path, want string }{
		{"/.well-known/caldav", "/caldav/alice/"},
		{"/.well-known/carddav", "/carddav/alice/"},
	} {
		resp, err := client.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMovedPermanently {
			t.Errorf("GET %s = %d, want 301", tc.path, resp.StatusCode)
		}
		if got := resp.Header.Get("Location"); got != tc.want {
			t.Errorf("GET %s redirected to %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestReportCalendarQueryAndMultiget measures what the delegated REPORT
// handler does on the flat layout: it must answer 207 with hrefs that keep the
// mount prefix, which is what the OpenCloud extensions resolve against.
func TestReportCalendarQueryAndMultiget(t *testing.T) {
	srv, _ := newTestServer(t)

	if resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/e1.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	}); resp.StatusCode >= 300 {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	query := `<?xml version="1.0" encoding="utf-8"?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"/></c:comp-filter></c:filter>
</c:calendar-query>`
	resp, body := do(t, "REPORT", srv.URL+"/caldav/alice/def-calendar/", query, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
		"Depth":        "1",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT calendar-query = %d, want 207: %s", resp.StatusCode, body)
	}
	ms := parseMultiStatus(t, body)
	hrefs := hrefsOf(ms)
	if len(hrefs) != 1 || hrefs[0] != "/caldav/alice/def-calendar/e1.ics" {
		t.Fatalf("calendar-query hrefs = %v, want [/caldav/alice/def-calendar/e1.ics]", hrefs)
	}
	if !strings.Contains(body, "UID:e1") {
		t.Error("calendar-query did not return the calendar data")
	}

	multiget := `<?xml version="1.0" encoding="utf-8"?>
<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <d:href>/caldav/alice/def-calendar/e1.ics</d:href>
</c:calendar-multiget>`
	resp, body = do(t, "REPORT", srv.URL+"/caldav/alice/def-calendar/", multiget, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT calendar-multiget = %d, want 207: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "UID:e1") {
		t.Errorf("calendar-multiget did not return the object: %s", body)
	}
}

// TestETagIsCanonicalAndUsableForPreconditions is a regression guard for a
// subtle interoperability bug: the store used to return a pre-quoted ETag and
// go-webdav quotes again, so the server emitted ETag: "\"hex\"" and If-Match
// only worked with that same double-quoted value. A client using the canonical
// quoted-string (what Radicale sends) would have failed.
func TestETagIsCanonicalAndUsableForPreconditions(t *testing.T) {
	srv, _ := newTestServer(t)
	url := srv.URL + "/caldav/alice/def-calendar/etag.ics"

	if resp, body := do(t, "PUT", url, testICS, map[string]string{
		"Content-Type": "text/calendar",
	}); resp.StatusCode >= 300 {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	resp, _ := do(t, "GET", url, "", nil)
	etag := resp.Header.Get("ETag")
	if len(etag) != 66 || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || strings.Contains(etag, `\"`) {
		t.Fatalf("ETag header = %s, want a canonical quoted 64 hex digit ETag", etag)
	}

	// The PROPFIND getetag must be the very same string.
	req := `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`
	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/alice/def-calendar/", req, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d: %s", resp.StatusCode, body)
	}
	got := textOf(propsOf(parseMultiStatus(t, body), "/caldav/alice/def-calendar/etag.ics")["DAV: getetag"])
	if got != etag {
		t.Errorf("PROPFIND getetag = %s, GET ETag = %s: they must match", got, etag)
	}

	// A conditional update with that ETag must succeed...
	resp, body = do(t, "PUT", url, strings.Replace(testICS, "SUMMARY:prueba", "SUMMARY:revisado", 1), map[string]string{
		"Content-Type": "text/calendar",
		"If-Match":     etag,
	})
	if resp.StatusCode >= 300 {
		t.Errorf("PUT with the canonical ETag = %d, want success: %s", resp.StatusCode, body)
	}
	// ...and with a stale one must fail.
	resp, body = do(t, "PUT", url, testICS, map[string]string{
		"Content-Type": "text/calendar",
		"If-Match":     etag,
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("PUT with a stale ETag = %d, want 412: %s", resp.StatusCode, body)
	}
}

// TestDeleteIsAvailableOnBothEndpoints is a regression guard: the library's
// CardDAV DELETE rejects the flat layout with 403, so DELETE is handled by
// davcloud itself on both endpoints.
func TestDeleteIsAvailableOnBothEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, tc := range []struct{ url, body, ctype string }{
		{"/caldav/alice/def-calendar/d1.ics", testICS, "text/calendar"},
		{"/carddav/alice/def-addressbook/d1.vcf", testVCF, "text/vcard"},
	} {
		if resp, body := do(t, "PUT", srv.URL+tc.url, tc.body, map[string]string{
			"Content-Type": tc.ctype,
		}); resp.StatusCode >= 300 {
			t.Fatalf("PUT %s = %d: %s", tc.url, resp.StatusCode, body)
		}
		resp, body := do(t, "DELETE", srv.URL+tc.url, "", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("DELETE %s = %d, want 204: %s", tc.url, resp.StatusCode, body)
		}
		resp, _ = do(t, "GET", srv.URL+tc.url, "", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET after DELETE %s = %d, want 404", tc.url, resp.StatusCode)
		}
	}
}

// TestPropfindRejectsCrossUserAccess makes sure the flat layout does not open
// a hole: another user's segment is forbidden, never served.
func TestPropfindRejectsCrossUserAccess(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/bob/", propfindBody, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("PROPFIND on another user = %d, want 403: %s", resp.StatusCode, body)
	}
}

// TestPropfindInfinityDepth covers the recursive listing: the tree is shallow,
// so Depth: infinity is answered in full instead of being refused.
func TestPropfindInfinityDepth(t *testing.T) {
	srv, _ := newTestServer(t)

	if resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/i1.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	}); resp.StatusCode >= 300 {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/", propfindBody, map[string]string{"Depth": "infinity"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND infinity = %d, want 207: %s", resp.StatusCode, body)
	}
	hrefs := strings.Join(hrefsOf(parseMultiStatus(t, body)), " ")
	for _, want := range []string{"/caldav/alice/", "/caldav/alice/def-calendar/", "/caldav/alice/def-calendar/i1.ics"} {
		if !strings.Contains(hrefs, want) {
			t.Errorf("infinity listing is missing %s: %v", want, hrefs)
		}
	}
}

// TestSpaFallbackStillWorks guards the non-DAV routes after the DAV wiring
// change.
func TestSpaFallbackStillWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newHandler(store.New(t.TempDir()), dir, "alice"))
	t.Cleanup(srv.Close)

	resp, body := do(t, "GET", srv.URL+"/some/client/route", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET spa route = %d, want 200: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "spa") {
		t.Errorf("spa fallback body = %q", body)
	}
}
