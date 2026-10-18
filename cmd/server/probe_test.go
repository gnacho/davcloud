package main

import (
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"davcloud/internal/store"
)

// TestProbeCollectionListingDepth is a characterization probe: it measures
// which Depth value actually makes the server list the user's collections.
func TestProbeCollectionListingDepth(t *testing.T) {
	dir := t.TempDir()
	st := store.New(dir)
	srv := httptest.NewServer(newHandler(st, filepath.Join(dir, "missing-ui"), "alice", false, defaultLimits()))
	defer srv.Close()

	cases := []struct {
		name string
		path string
	}{
		{"home-set no slash Depth 1", "/caldav/alice"},
		{"home-set slash Depth 1", "/caldav/alice/"},
		{"home-set no slash Depth infinity", "/caldav/alice"},
		{"home-set slash Depth infinity", "/caldav/alice/"},
		{"calendar Depth 1", "/caldav/alice/def-calendar"},
		{"calendar slash Depth 1", "/caldav/alice/def-calendar/"},
	}
	for _, c := range cases {
		depth := "1"
		if strings.Contains(c.name, "infinity") {
			depth = "infinity"
		}
		resp, body := do(t, "PROPFIND", srv.URL+c.path, propfindBody, map[string]string{
			"Depth":        depth,
			"Content-Type": "application/xml; charset=utf-8",
		})
		hrefs := regexp.MustCompile(`<[^>]*href>([^<]*)<`).FindAllStringSubmatch(body, -1)
		var got []string
		for _, h := range hrefs {
			got = append(got, h[1])
		}
		t.Logf("%-38s Depth=%-8s -> %d, hrefs=%v", c.name, depth, resp.StatusCode, got)
	}
}
