// Package carddav implements the go-webdav v0.7 CardDAV backend on top of
// the Radicale-compatible store. Same URL layout as the caldav backend:
//
//	/<user>/                    -> address book home set
//	/<user>/<addressbook>/      -> address book collection
//	/<user>/<addressbook>/x.vcf -> address object
package carddav

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	webdavcarddav "github.com/emersion/go-webdav/carddav"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// Backend implements webdavcarddav.BackEnd.
type Backend struct {
	st *store.Store
}

// New creates the backend.
func New(st *store.Store) *Backend { return &Backend{st: st} }

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

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

// AddressBookHomeSetPath implements the v0.7 backend interface.
func (b *Backend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return "/" + auth.User(ctx) + "/", nil
}

// ListAddressBooks implements the backend interface.
func (b *Backend) ListAddressBooks(ctx context.Context) ([]webdavcarddav.AddressBook, error) {
	user := auth.User(ctx)
	if err := b.st.EnsureUser(user); err != nil {
		return nil, err
	}
	cols, err := b.st.ListCollections(user)
	if err != nil {
		return nil, err
	}
	var out []webdavcarddav.AddressBook
	for _, c := range cols {
		if c.Tag != "VADDRESSBOOK" {
			continue
		}
		out = append(out, webdavcarddav.AddressBook{
			Path: "/" + user + "/" + c.ID,
			Name: c.DisplayName,
		})
	}
	return out, nil
}

// GetAddressBook implements the backend interface.
func (b *Backend) GetAddressBook(ctx context.Context, path string) (*webdavcarddav.AddressBook, error) {
	segs := splitPath(path)
	if len(segs) != 2 {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	c, err := b.st.GetCollection(user, segs[1])
	if err != nil || c.Tag != "VADDRESSBOOK" {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	return &webdavcarddav.AddressBook{Path: path, Name: c.DisplayName}, nil
}

// CreateAddressBook implements the backend interface (path from ab.Path).
func (b *Backend) CreateAddressBook(ctx context.Context, ab *webdavcarddav.AddressBook) error {
	segs := splitPath(ab.Path)
	if len(segs) != 2 {
		return &webdav.HTTPError{Code: http.StatusBadRequest}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	return b.st.CreateCollection(user, store.Collection{
		ID:          segs[1],
		Tag:         "VADDRESSBOOK",
		DisplayName: ab.Name,
	})
}

// DeleteAddressBook implements the backend interface.
func (b *Backend) DeleteAddressBook(ctx context.Context, path string) error {
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

func decodeVCF(data []byte) (vcard.Card, error) {
	card, err := vcard.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return nil, err
	}
	return vcard.ToV4(card), nil
}

func (b *Backend) loadObject(ctx context.Context, collPath, name string) (*webdavcarddav.AddressObject, error) {
	segs := splitPath(collPath)
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	data, err := b.st.ReadItem(user, segs[1], name)
	if err != nil {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	card, err := decodeVCF(data)
	if err != nil {
		return nil, &webdav.HTTPError{Code: http.StatusInternalServerError}
	}
	return &webdavcarddav.AddressObject{
		Path: collPath + "/" + name,
		Data: card,
		ETag: store.ETag(data),
	}, nil
}

// GetAddressObject implements the backend interface.
func (b *Backend) GetAddressObject(ctx context.Context, path string, req *webdavcarddav.AddressDataRequest) (*webdavcarddav.AddressObject, error) {
	segs := splitPath(path)
	if len(segs) != 3 {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	return b.loadObject(ctx, "/"+segs[0]+"/"+segs[1], segs[2])
}

func (b *Backend) loadObjects(ctx context.Context, collPath string) ([]webdavcarddav.AddressObject, error) {
	segs := splitPath(collPath)
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	names, err := b.st.ListItems(user, segs[1])
	if err != nil {
		return nil, &webdav.HTTPError{Code: http.StatusNotFound}
	}
	var out []webdavcarddav.AddressObject
	for _, n := range names {
		obj, err := b.loadObject(ctx, collPath, n)
		if err != nil {
			continue
		}
		out = append(out, *obj)
	}
	return out, nil
}

// ListAddressObjects implements the backend interface.
func (b *Backend) ListAddressObjects(ctx context.Context, path string, req *webdavcarddav.AddressDataRequest) ([]webdavcarddav.AddressObject, error) {
	return b.loadObjects(ctx, path)
}

// QueryAddressObjects implements the backend interface using the library's
// own filter evaluation when available (carddav.Filter), falling back to a
// naive whole-card text match.
func (b *Backend) QueryAddressObjects(ctx context.Context, path string, query *webdavcarddav.AddressBookQuery) ([]webdavcarddav.AddressObject, error) {
	objs, err := b.loadObjects(ctx, path)
	if err != nil {
		return nil, err
	}
	if query == nil {
		return objs, nil
	}
	return webdavcarddav.Filter(query, objs)
}

func encodeVCF(card vcard.Card) ([]byte, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PutAddressObject implements the unified create/update of the v0.7 API.
// TODO: honour conditional headers from PutAddressObjectOptions.
func (b *Backend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *webdavcarddav.PutAddressObjectOptions) (*webdavcarddav.AddressObject, error) {
	segs := splitPath(path)
	if len(segs) != 3 {
		return nil, &webdav.HTTPError{Code: http.StatusBadRequest}
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	data, err := encodeVCF(card)
	if err != nil {
		return nil, err
	}
	etag, err := b.st.WriteItem(user, segs[1], segs[2], data)
	if err != nil {
		return nil, err
	}
	return &webdavcarddav.AddressObject{Path: path, Data: card, ETag: etag}, nil
}

// DeleteAddressObject implements the backend interface.
func (b *Backend) DeleteAddressObject(ctx context.Context, path string) error {
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
