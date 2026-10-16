// Package store implements a filesystem backend with the Radicale
// "multifilesystem" layout:
//
//	<root>/collection-root/<user>/<collection>/.Radicale.props
//	<root>/collection-root/<user>/<collection>/<item>.ics|vcf
//
// Because the layout is identical, the existing radicale-data volume can be
// mounted here and all data shows up unchanged.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PropsFile is the per-collection metadata file used by Radicale.
const PropsFile = ".Radicale.props"

// Collection is the JSON shape of .Radicale.props (keys kept compatible).
type Collection struct {
	ID          string   `json:"-"`
	Tag         string   `json:"tag"`                                    // VCALENDAR | VADDRESSBOOK
	DisplayName string   `json:"D:displayname"`                          //
	Components  []string `json:"C:supported-calendar-component-set,omitempty"`
}

// Store is a root directory containing the Radicale collection tree.
type Store struct {
	Root string
}

// New creates a Store, ensuring the root exists.
func New(root string) *Store {
	_ = os.MkdirAll(filepath.Join(root, "collection-root"), 0o755)
	return &Store{Root: root}
}

func cleanSeg(s string) (string, error) {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
		return "", fmt.Errorf("store: invalid path segment %q", s)
	}
	return s, nil
}

func (s *Store) userDir(user string) (string, error) {
	u, err := cleanSeg(user)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Root, "collection-root", u), nil
}

// EnsureUser creates the user directory plus the default collections,
// mirroring Radicale's predefined_collections.
func (s *Store) EnsureUser(user string) error {
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defaults := []Collection{
		{ID: "def-calendar", Tag: "VCALENDAR", DisplayName: "Personal Calendar", Components: []string{"VEVENT", "VTODO", "VJOURNAL"}},
		{ID: "def-addressbook", Tag: "VADDRESSBOOK", DisplayName: "Personal Address Book"},
	}
	for _, c := range defaults {
		p := filepath.Join(dir, c.ID)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			if err := s.writeProps(p, c); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) writeProps(dir string, c Collection) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, PropsFile), b, 0o644)
}

func (s *Store) readCollection(dir, id string) (Collection, error) {
	b, err := os.ReadFile(filepath.Join(dir, PropsFile))
	if err != nil {
		return Collection{}, err
	}
	var c Collection
	if err := json.Unmarshal(b, &c); err != nil {
		return Collection{}, err
	}
	c.ID = id
	return c, nil
}

// ListCollections returns all collections of a user (any tag).
func (s *Store) ListCollections(user string) ([]Collection, error) {
	dir, err := s.userDir(user)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Collection
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := s.readCollection(filepath.Join(dir, e.Name()), e.Name())
		if err != nil {
			continue // skip collections without props
		}
		out = append(out, c)
	}
	return out, nil
}

// GetCollection returns one collection by id.
func (s *Store) GetCollection(user, id string) (Collection, error) {
	if _, err := cleanSeg(id); err != nil {
		return Collection{}, err
	}
	dir, err := s.userDir(user)
	if err != nil {
		return Collection{}, err
	}
	return s.readCollection(filepath.Join(dir, id), id)
}

// CreateCollection creates a collection directory with props.
func (s *Store) CreateCollection(user string, c Collection) error {
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, c.ID)
	if _, err := os.Stat(p); err == nil {
		return os.ErrExist
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	return s.writeProps(p, c)
}

// DeleteCollection removes a collection directory entirely.
func (s *Store) DeleteCollection(user, id string) error {
	if _, err := cleanSeg(id); err != nil {
		return err
	}
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, id))
}

func (s *Store) collDir(user, coll string) (string, error) {
	if _, err := cleanSeg(coll); err != nil {
		return "", err
	}
	dir, err := s.userDir(user)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, coll), nil
}

// ListItems returns item file names of a collection.
func (s *Store) ListItems(user, coll string) ([]string, error) {
	dir, err := s.collDir(user, coll)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == PropsFile {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// ReadItem returns the raw content of an item.
func (s *Store) ReadItem(user, coll, name string) ([]byte, error) {
	if _, err := cleanSeg(name); err != nil {
		return nil, err
	}
	dir, err := s.collDir(user, coll)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, name))
}

// WriteItem writes an item and returns its new ETag (sha256).
func (s *Store) WriteItem(user, coll, name string, data []byte) (string, error) {
	if _, err := cleanSeg(name); err != nil {
		return "", err
	}
	dir, err := s.collDir(user, coll)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	return ETag(data), nil
}

// DeleteItem removes an item. Returns os.ErrNotExist if missing.
func (s *Store) DeleteItem(user, coll, name string) error {
	if _, err := cleanSeg(name); err != nil {
		return err
	}
	dir, err := s.collDir(user, coll)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, name))
}

// ETag computes a strong ETag for raw content.
func ETag(data []byte) string {
	h := sha256.Sum256(data)
	return `"` + hex.EncodeToString(h[:]) + `"`
}
