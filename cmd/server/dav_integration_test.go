package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
	SyncToken string        `xml:"sync-token"`
}

func parseMultiStatus(t *testing.T, body string) multistatusDoc {
	t.Helper()
	var doc multistatusDoc
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("multistatus is not valid XML: %v\nbody: %s", err, body)
	}
	assertDeclaredPrefixes(t, body)
	return doc
}

// assertDeclaredPrefixes checks that every element prefix used in the
// document is declared on the root element. Go's decoder is lenient with
// unbound prefixes, so without this a multistatus that a strict parser (and
// some DAV clients) would reject slips through the tests.
func assertDeclaredPrefixes(t *testing.T, body string) {
	t.Helper()
	declared := map[string]bool{}
	for _, m := range declaredRe.FindAllStringSubmatch(body, -1) {
		declared[m[1]] = true
	}
	for _, m := range usedRe.FindAllStringSubmatch(body, -1) {
		if !declared[m[1]] {
			t.Errorf("prefix %q is used but not declared on the root element", m[1])
		}
	}
}

var (
	declaredRe = regexp.MustCompile(`xmlns:([A-Za-z0-9]+)=`)
	usedRe     = regexp.MustCompile(`</?([A-Za-z0-9]+):[A-Za-z0-9]`)
)

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

// TestPutGetAreByteIdentical is the core of issue #6: the file on disk must
// be exactly the request body (like Radicale, which stores the item as sent)
// and GET must serve exactly those bytes. That is what keeps the data volume
// interchangeable with Radicale for the rollback drill.
func TestPutGetAreByteIdentical(t *testing.T) {
	srv, dataDir := newTestServer(t)

	raw := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//odd order//test//ES\r\n" +
		"X-WR-CALNAME:Raw\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:raw-1\r\n" +
		"DTSTAMP:20261016T120000Z\r\n" +
		"DTSTART:20261020T090000Z\r\n" +
		"DTEND:20261020T100000Z\r\n" +
		"SUMMARY:Orden no alfabetico, con acentos: ñ\r\n" +
		"X-ODD-PROP;PARAM=uno:valor\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	if resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/raw-1.ics", raw, map[string]string{
		"Content-Type": "text/calendar; charset=utf-8",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	onDisk, err := os.ReadFile(filepath.Join(dataDir, "collection-root", "alice", "def-calendar", "raw-1.ics"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != raw {
		t.Errorf("the file on disk is not byte identical to the request body:\n--- disk ---\n%q\n--- sent ---\n%q", onDisk, raw)
	}

	resp, body := do(t, "GET", srv.URL+"/caldav/alice/def-calendar/raw-1.ics", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	if body != raw {
		t.Errorf("GET body is not byte identical to what was PUT:\n%q", body)
	}
}

// TestPutRejectsMismatchedUID mirrors Radicale: the resource name must match
// the UID inside the data, so one UID cannot live under two hrefs.
func TestPutRejectsMismatchedUID(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/otro-nombre.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT with a name not matching the UID = %d, want 400: %s", resp.StatusCode, body)
	}
}

// TestPutAcceptsLaxContentType documents that PUT validates the content, not
// the Content-Type header (issue #5): a parseable stream is accepted even
// with a lax media type, an unparseable one is rejected.
func TestPutAcceptsLaxContentType(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/e1.ics", testICS, map[string]string{
		"Content-Type": "text/plain",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("PUT text/plain with a valid stream = %d, want 201: %s", resp.StatusCode, body)
	}

	resp, body = do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/basura.ics", "esto no es ical", map[string]string{
		"Content-Type": "text/calendar",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT with an unparseable stream = %d, want 400: %s", resp.StatusCode, body)
	}
}

// TestGetConditionalRequests covers the 304 handling of GET: If-None-Match
// with the current ETag and If-Modified-Since both answer 304.
func TestGetConditionalRequests(t *testing.T) {
	srv, _ := newTestServer(t)
	url := srv.URL + "/caldav/alice/def-calendar/e1.ics"

	if resp, body := do(t, "PUT", url, testICS, map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	resp, _ := do(t, "GET", url, "", nil)
	etag := resp.Header.Get("ETag")

	resp, _ = do(t, "GET", url, "", map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("GET with If-None-Match = %d, want 304", resp.StatusCode)
	}

	resp, _ = do(t, "GET", url, "", map[string]string{"If-Modified-Since": "Wed, 01 Jan 2042 00:00:00 GMT"})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("GET with a future If-Modified-Since = %d, want 304", resp.StatusCode)
	}
}

// TestSyncCollection covers RFC 6578 with the semantics the multifilesystem
// store can honestly provide: a stale or empty token gets the full listing,
// a current token gets an empty response.
func TestSyncCollection(t *testing.T) {
	srv, _ := newTestServer(t)
	coll := srv.URL + "/caldav/alice/def-calendar/"

	if resp, body := do(t, "PUT", coll+"e1.ics", testICS, map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	sync := func(token string) multistatusDoc {
		t.Helper()
		body := `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token>` + token + `</D:sync-token><D:prop><D:getetag/></D:prop></D:sync-collection>`
		resp, out := do(t, "REPORT", coll, body, map[string]string{"Content-Type": "application/xml"})
		if resp.StatusCode != http.StatusMultiStatus {
			t.Fatalf("REPORT sync-collection = %d, want 207: %s", resp.StatusCode, out)
		}
		return parseMultiStatus(t, out)
	}

	// Empty token: full sync with every item and a token to remember.
	first := sync("")
	if len(first.Responses) != 1 || first.Responses[0].Href != "/caldav/alice/def-calendar/e1.ics" {
		t.Fatalf("full sync hrefs = %v, want the one item", hrefsOf(first))
	}
	token := first.SyncToken
	if !strings.HasPrefix(token, "http://davcloud/ns/sync/") {
		t.Fatalf("sync token = %q, want a davcloud token", token)
	}

	// Current token: nothing changed, so an empty response is enough.
	again := sync(token)
	if len(again.Responses) != 0 {
		t.Errorf("sync with the current token returned %d responses, want 0", len(again.Responses))
	}
	if again.SyncToken != token {
		t.Errorf("sync token changed without content changes: %q != %q", again.SyncToken, token)
	}

	// After a change, the stale token gets the full listing and a new token.
	if resp, body := do(t, "PUT", coll+"e2.ics", strings.Replace(testICS, "UID:e1", "UID:e2", 1), map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	third := sync(token)
	if len(third.Responses) != 2 {
		t.Errorf("sync with a stale token returned %d responses, want the full listing (2)", len(third.Responses))
	}
	if third.SyncToken == token {
		t.Error("sync token did not change after a content change")
	}
}

// TestSyncCollectionIsAdvertised checks that PROPFIND announces the report
// and the token, so clients actually use this code path.
func TestSyncCollectionIsAdvertised(t *testing.T) {
	srv, _ := newTestServer(t)
	coll := srv.URL + "/caldav/alice/def-calendar/"

	req := `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:sync-token/><D:supported-report-set/></D:prop></D:propfind>`
	resp, body := do(t, "PROPFIND", coll, req, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d: %s", resp.StatusCode, body)
	}
	props := propsOf(parseMultiStatus(t, body), "/caldav/alice/def-calendar/")
	if !strings.HasPrefix(textOf(props["DAV: sync-token"]), "http://davcloud/ns/sync/") {
		t.Errorf("collection has no sync-token: %q", textOf(props["DAV: sync-token"]))
	}
	if !strings.Contains(props["DAV: supported-report-set"].Inner, "sync-collection") {
		t.Errorf("supported-report-set does not include sync-collection: %q", props["DAV: supported-report-set"].Inner)
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

// TestProppatchStoresClientProperties covers the issue #3 behaviour: renaming
// a collection and setting a client property (calendar-color) via PROPPATCH
// must be persisted in .Radicale.props with the canonical Radicale keys and
// served back by PROPFIND.
func TestProppatchStoresClientProperties(t *testing.T) {
	srv, dataDir := newTestServer(t)
	coll := srv.URL + "/caldav/alice/def-calendar/"

	patch := `<?xml version="1.0" encoding="utf-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:IC="http://apple.com/ns/ical/">
  <D:set><D:prop>
    <D:displayname>Calendario renombrado</D:displayname>
    <IC:calendar-color>#3498db</IC:calendar-color>
  </D:prop></D:set>
</D:propertyupdate>`
	resp, body := do(t, "PROPPATCH", coll, patch, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH = %d, want 207: %s", resp.StatusCode, body)
	}
	ms := parseMultiStatus(t, body)
	if got := propsOf(ms, "/caldav/alice/def-calendar/"); len(got) != 2 {
		t.Errorf("PROPPATCH answered 200 for %d properties, want 2: %v", len(got), keysOf(got))
	}

	// Persisted with the canonical Radicale keys, no invented prefix.
	b, err := os.ReadFile(filepath.Join(dataDir, "collection-root", "alice", "def-calendar", ".Radicale.props"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"D:displayname": "Calendario renombrado"`) {
		t.Errorf("props file missing the renamed displayname: %s", b)
	}
	if !strings.Contains(string(b), `"ICAL:calendar-color": "#3498db"`) {
		t.Errorf("props file missing the canonical ICAL key: %s", b)
	}

	// And served back by PROPFIND, in the right namespaces.
	req := `<?xml version="1.0"?>
<D:propfind xmlns:D="DAV:" xmlns:IC="http://apple.com/ns/ical/">
  <D:prop><D:displayname/><IC:calendar-color/></D:prop>
</D:propfind>`
	resp, body = do(t, "PROPFIND", coll, req, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d: %s", resp.StatusCode, body)
	}
	props := propsOf(parseMultiStatus(t, body), "/caldav/alice/def-calendar/")
	if got := textOf(props["DAV: displayname"]); got != "Calendario renombrado" {
		t.Errorf("PROPFIND displayname = %q", got)
	}
	if got := textOf(props["http://apple.com/ns/ical/ calendar-color"]); got != "#3498db" {
		t.Errorf("PROPFIND calendar-color = %q", got)
	}
}

// TestProppatchProtectedPropertyIsAtomic checks RFC 4918 atomicity: a patch
// touching a protected property must fail with 403 for that property, 424
// for the rest, and apply nothing.
func TestProppatchProtectedPropertyIsAtomic(t *testing.T) {
	srv, dataDir := newTestServer(t)
	coll := srv.URL + "/caldav/alice/def-calendar/"

	patch := `<?xml version="1.0" encoding="utf-8"?>
<D:propertyupdate xmlns:D="DAV:">
  <D:set><D:prop>
    <D:getetag>"hacked"</D:getetag>
    <D:displayname>No se aplica</D:displayname>
  </D:prop></D:set>
</D:propertyupdate>`
	resp, body := do(t, "PROPPATCH", coll, patch, map[string]string{
		"Content-Type": "application/xml; charset=utf-8",
	})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH = %d, want 207: %s", resp.StatusCode, body)
	}
	ms := parseMultiStatus(t, body)
	resp0 := ms.Responses[0]
	var status []string
	for _, ps := range resp0.Propstats {
		status = append(status, ps.Status)
	}
	joined := strings.Join(status, ",")
	if !strings.Contains(joined, "403") || !strings.Contains(joined, "424") {
		t.Errorf("propstat statuses = %q, want 403 (protected) and 424 (dependency)", joined)
	}

	b, err := os.ReadFile(filepath.Join(dataDir, "collection-root", "alice", "def-calendar", ".Radicale.props"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"D:displayname": "Personal Calendar"`) {
		t.Errorf("atomicity broken: the displayname changed despite the failed patch: %s", b)
	}
}

// TestProppatchRemove deletes a client property.
func TestProppatchRemove(t *testing.T) {
	srv, _ := newTestServer(t)
	coll := srv.URL + "/caldav/alice/def-calendar/"

	setPatch := `<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:" xmlns:IC="http://apple.com/ns/ical/">
  <D:set><D:prop><IC:calendar-color>#000000</IC:calendar-color></D:prop></D:set></D:propertyupdate>`
	if resp, body := do(t, "PROPPATCH", coll, setPatch, map[string]string{"Content-Type": "application/xml"}); resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("set = %d: %s", resp.StatusCode, body)
	}
	removePatch := `<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:" xmlns:IC="http://apple.com/ns/ical/">
  <D:remove><D:prop><IC:calendar-color/></D:prop></D:remove></D:propertyupdate>`
	if resp, body := do(t, "PROPPATCH", coll, removePatch, map[string]string{"Content-Type": "application/xml"}); resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("remove = %d: %s", resp.StatusCode, body)
	}

	req := `<?xml version="1.0"?><D:propfind xmlns:D="DAV:" xmlns:IC="http://apple.com/ns/ical/">
  <D:prop><IC:calendar-color/></D:prop></D:propfind>`
	resp, body := do(t, "PROPFIND", coll, req, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d: %s", resp.StatusCode, body)
	}
	if missing := strings.Join(notFoundOf(parseMultiStatus(t, body), "/caldav/alice/def-calendar/"), ","); !strings.Contains(missing, "calendar-color") {
		t.Errorf("removed property did not come back as 404: %q", missing)
	}
}

// TestProppatchErrors covers the error cases: missing collection and
// unsupported targets.
func TestProppatchErrors(t *testing.T) {
	srv, _ := newTestServer(t)
	patch := `<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:">
  <D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`

	resp, _ := do(t, "PROPPATCH", srv.URL+"/caldav/alice/no-existe/", patch, map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PROPPATCH on missing collection = %d, want 404", resp.StatusCode)
	}
	resp, _ = do(t, "PROPPATCH", srv.URL+"/caldav/alice/", patch, map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("PROPPATCH on the home set = %d, want 403", resp.StatusCode)
	}
}

// TestETagIsCanonicalAndUsableForPreconditions is a regression guard for a
// subtle interoperability bug: the store used to return a pre-quoted ETag and
// go-webdav quotes again, so the server emitted ETag: "\"hex\"" and If-Match
// only worked with that same double-quoted value. A client using the canonical
// quoted-string (what Radicale sends) would have failed.
func TestETagIsCanonicalAndUsableForPreconditions(t *testing.T) {
	srv, _ := newTestServer(t)
	url := srv.URL + "/caldav/alice/def-calendar/e1.ics"

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
	got := textOf(propsOf(parseMultiStatus(t, body), "/caldav/alice/def-calendar/e1.ics")["DAV: getetag"])
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
		{"/caldav/alice/def-calendar/e1.ics", testICS, "text/calendar"},
		{"/carddav/alice/def-addressbook/c1.vcf", testVCF, "text/vcard"},
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

	if resp, body := do(t, "PUT", srv.URL+"/caldav/alice/def-calendar/e1.ics", testICS, map[string]string{
		"Content-Type": "text/calendar",
	}); resp.StatusCode >= 300 {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}

	resp, body := do(t, "PROPFIND", srv.URL+"/caldav/", propfindBody, map[string]string{"Depth": "infinity"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND infinity = %d, want 207: %s", resp.StatusCode, body)
	}
	hrefs := strings.Join(hrefsOf(parseMultiStatus(t, body)), " ")
	for _, want := range []string{"/caldav/alice/", "/caldav/alice/def-calendar/", "/caldav/alice/def-calendar/e1.ics"} {
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
