package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureUserCreatesDefaultsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	if err := s.EnsureUser("alice"); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	// Writing custom props must survive a second EnsureUser call.
	custom := []byte(`{"tag":"VCALENDAR","D:displayname":"Trabajo"}`)
	calDir := filepath.Join(root, "collection-root", "alice", "def-calendar")
	if err := os.WriteFile(filepath.Join(calDir, PropsFile), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureUser("alice"); err != nil {
		t.Fatalf("EnsureUser (2nd): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(calDir, PropsFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Errorf("props were overwritten: got %s, want %s", got, custom)
	}

	cols, err := s.ListCollections("alice")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("got %d collections, want 2: %+v", len(cols), cols)
	}
	tags := map[string]string{}
	for _, c := range cols {
		tags[c.ID] = c.Tag
	}
	if tags["def-calendar"] != "VCALENDAR" || tags["def-addressbook"] != "VADDRESSBOOK" {
		t.Errorf("unexpected tags: %+v", tags)
	}
}

func TestLayoutMatchesRadicale(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.EnsureUser("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteItem("alice", "def-calendar", "e1.ics", []byte("BEGIN:VCALENDAR\nEND:VCALENDAR\n")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "collection-root", "alice", "def-calendar", "e1.ics")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("item not at Radicale-compatible path %s: %v", want, err)
	}
}

func TestRejectsInvalidSegments(t *testing.T) {
	s := New(t.TempDir())
	bad := []string{"", ".", "..", "a/b", `a\b`}
	for _, b := range bad {
		if _, err := s.GetCollection("alice", b); err == nil {
			t.Errorf("GetCollection accepted %q", b)
		}
		if _, err := s.WriteItem("alice", b, "x.ics", []byte("x")); err == nil {
			t.Errorf("WriteItem accepted collection %q", b)
		}
		if _, err := s.WriteItem("alice", "def-calendar", b, []byte("x")); err == nil {
			t.Errorf("WriteItem accepted item %q", b)
		}
		if err := s.EnsureUser(b); err == nil {
			t.Errorf("EnsureUser accepted user %q", b)
		}
		if err := s.DeleteItem("alice", "def-calendar", b); err == nil {
			t.Errorf("DeleteItem accepted item %q", b)
		}
	}
}

func TestWriteItemIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.EnsureUser("alice"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.WriteItem("alice", "def-calendar", "e1.ics", []byte(strings.Repeat("x", 100+i))); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "collection-root", "alice", "def-calendar")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s after atomic write", e.Name())
		}
	}
	if len(entries) != 2 { // .Radicale.props + e1.ics
		t.Errorf("unexpected directory content: %v", entries)
	}

	// A stray temp file (e.g. from a crashed writer) must not be served as an
	// item and must not be deleted by the server.
	if err := os.WriteFile(filepath.Join(dir, ".tmp-orphan"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := s.ListItems("alice", "def-calendar")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "e1.ics" {
		t.Errorf("ListItems returned %v, want [e1.ics]", names)
	}
}

func TestETagIsDeterministicAndContentAddressed(t *testing.T) {
	a := ETag([]byte("hola"))
	b := ETag([]byte("hola"))
	c := ETag([]byte("hola!"))
	if a != b {
		t.Errorf("ETag is not deterministic: %s != %s", a, b)
	}
	if a == c {
		t.Error("ETag did not change with content")
	}
	// The logical ETag is unquoted: the DAV layer quotes it when it puts it on
	// the wire, and quoting it here too would emit "\"hex\"".
	if strings.ContainsAny(a, `"`) || len(a) != 64 {
		t.Errorf("ETag %s is not an unquoted sha256", a)
	}
	if got, want := QuotedETag([]byte("hola")), `"`+a+`"`; got != want {
		t.Errorf("QuotedETag = %s, want %s", got, want)
	}
}

func TestItemETagAndWriteItemErrors(t *testing.T) {
	s := New(t.TempDir())
	if err := s.EnsureUser("alice"); err != nil {
		t.Fatal(err)
	}
	content := []byte("BEGIN:VCALENDAR\nEND:VCALENDAR\n")
	if _, err := s.WriteItem("alice", "def-calendar", "e1.ics", content); err != nil {
		t.Fatal(err)
	}
	etag, err := s.ItemETag("alice", "def-calendar", "e1.ics")
	if err != nil {
		t.Fatalf("ItemETag: %v", err)
	}
	if etag != ETag(content) {
		t.Errorf("ItemETag = %s, want %s", etag, ETag(content))
	}

	if _, err := s.ItemETag("alice", "def-calendar", "missing.ics"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ItemETag on missing item = %v, want os.ErrNotExist", err)
	}

	// Writing into a collection that does not exist must fail instead of
	// creating a stray directory.
	if _, err := s.WriteItem("alice", "no-such-collection", "e1.ics", content); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("WriteItem into missing collection = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "collection-root", "alice", "no-such-collection")); !errors.Is(err, os.ErrNotExist) {
		t.Error("WriteItem created a directory for a missing collection")
	}

	if err := s.DeleteItem("alice", "def-calendar", "missing.ics"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DeleteItem on missing item = %v, want os.ErrNotExist", err)
	}
}

func TestCreateCollectionRejectsExisting(t *testing.T) {
	s := New(t.TempDir())
	c := Collection{ID: "trabajo", Tag: "VCALENDAR", DisplayName: "Trabajo"}
	if err := s.CreateCollection("alice", c); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCollection("alice", c); !errors.Is(err, os.ErrExist) {
		t.Errorf("second CreateCollection = %v, want os.ErrExist", err)
	}
	got, err := s.GetCollection("alice", "trabajo")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Trabajo" || got.Tag != "VCALENDAR" {
		t.Errorf("round-tripped collection is %+v", got)
	}
	if err := s.DeleteCollection("alice", "trabajo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCollection("alice", "trabajo"); err == nil {
		t.Error("collection still readable after delete")
	}
}
