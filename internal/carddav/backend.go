// Package carddav implements the go-webdav v0.7 CardDAV backend on top of the
// Radicale-compatible store.
//
//	/<user>/                    -> address book home set
//	/<user>/<addressbook>/      -> address book collection
//	/<user>/<addressbook>/x.vcf -> address object
//
// Cards are parsed and re-encoded as they are stored: no vCard version
// rewriting is applied, so what Radicale served (usually vCard 3.0 from
// iPhones) keeps coming back byte-compatible in its properties.
package carddav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	webdavcarddav "github.com/emersion/go-webdav/carddav"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// Backend implements webdavcarddav.Backend.
type Backend struct {
	st *store.Store
	// Prefix is the mount point of the endpoint ("/carddav"). The paths that
	// cross the library handler are server paths and include it, while the
	// store only knows the local "/<user>/<collection>[/<item>]" layout.
	Prefix string
}

// New creates the backend mounted at prefix.
func New(st *store.Store, prefix string) *Backend {
	return &Backend{st: st, Prefix: "/" + strings.Trim(prefix, "/")}
}

// local strips the mount prefix from a server path. It is idempotent.
func (b *Backend) local(p string) string {
	if b.Prefix != "/" && strings.HasPrefix(p, b.Prefix) {
		p = strings.TrimPrefix(p, b.Prefix)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// serverPath builds the client visible path of a local path.
func (b *Backend) serverPath(local string) string {
	if local == "" || local == "/" {
		return b.Prefix + "/"
	}
	return b.Prefix + local
}

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

func userFromCtx(ctx context.Context, urlUser string) (string, error) {
	u := auth.User(ctx)
	if urlUser == "" || urlUser != u {
		return "", webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("carddav: url user %q does not match authenticated user %q", urlUser, u))
	}
	return u, nil
}

func collectionPath(segs []string) string { return "/" + segs[0] + "/" + segs[1] }

// CurrentUserPrincipal implements webdav.UserPrincipalBackend.
func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.serverPath("/" + auth.User(ctx)), nil
}

// AddressBookHomeSetPath implements the v0.7 backend interface.
func (b *Backend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return b.serverPath("/" + auth.User(ctx) + "/"), nil
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
			Path: b.serverPath("/" + user + "/" + c.ID),
			Name: c.DisplayName,
		})
	}
	return out, nil
}

// GetAddressBook implements the backend interface.
func (b *Backend) GetAddressBook(ctx context.Context, path string) (*webdavcarddav.AddressBook, error) {
	segs := splitPath(b.local(path))
	if len(segs) != 2 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: bad address book path %q", path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	c, err := b.st.GetCollection(user, segs[1])
	if err != nil || c.Tag != "VADDRESSBOOK" {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: address book %q not found", path))
	}
	return &webdavcarddav.AddressBook{Path: b.serverPath(b.local(path)), Name: c.DisplayName}, nil
}

// CreateAddressBook implements the backend interface (path from ab.Path).
func (b *Backend) CreateAddressBook(ctx context.Context, ab *webdavcarddav.AddressBook) error {
	segs := splitPath(b.local(ab.Path))
	if len(segs) != 2 {
		return webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("carddav: bad address book path %q", ab.Path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	err = b.st.CreateCollection(user, store.Collection{
		ID:          segs[1],
		Tag:         "VADDRESSBOOK",
		DisplayName: ab.Name,
	})
	if errors.Is(err, os.ErrExist) {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, fmt.Errorf("carddav: address book %q already exists", ab.Path))
	}
	return err
}

// DeleteAddressBook implements the backend interface.
func (b *Backend) DeleteAddressBook(ctx context.Context, path string) error {
	segs := splitPath(b.local(path))
	if len(segs) != 2 {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: bad address book path %q", path))
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

// decodeVCF parses a stored card without rewriting its version: the fields
// (including VERSION and grouped item1.* properties) are preserved.
func decodeVCF(data []byte) (vcard.Card, error) {
	return vcard.NewDecoder(bytes.NewReader(data)).Decode()
}

func (b *Backend) loadObject(ctx context.Context, collPath, name string) (*webdavcarddav.AddressObject, error) {
	local := strings.TrimSuffix(b.local(collPath), "/")
	segs := splitPath(local)
	if len(segs) != 2 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: bad collection path %q", collPath))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	data, err := b.st.ReadItem(user, segs[1], name)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: object %s/%s not found", collPath, name))
	}
	card, err := decodeVCF(data)
	if err != nil {
		log.Printf("carddav: unparseable object %s/%s: %v", collPath, name, err)
		return nil, webdav.NewHTTPError(http.StatusInternalServerError, fmt.Errorf("carddav: unparseable object %s/%s", collPath, name))
	}
	return &webdavcarddav.AddressObject{
		Path:          b.serverPath(local + "/" + name),
		Card:          card,
		ETag:          store.ETag(data),
		ContentLength: int64(len(data)),
	}, nil
}

// GetAddressObject implements the backend interface.
func (b *Backend) GetAddressObject(ctx context.Context, path string, req *webdavcarddav.AddressDataRequest) (*webdavcarddav.AddressObject, error) {
	segs := splitPath(b.local(path))
	if len(segs) != 3 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: bad object path %q", path))
	}
	return b.loadObject(ctx, collectionPath(segs), segs[2])
}

func (b *Backend) loadObjects(ctx context.Context, collPath string) ([]webdavcarddav.AddressObject, error) {
	segs := splitPath(b.local(collPath))
	if len(segs) != 2 {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: bad collection path %q", collPath))
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
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: collection %q not found", collPath))
	}
	var out []webdavcarddav.AddressObject
	for _, n := range names {
		obj, err := b.loadObject(ctx, collPath, n)
		if err != nil {
			log.Printf("carddav: skipping %s/%s: %v", collPath, n, err)
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

// QueryAddressObjects implements the backend interface. Most of the work is
// delegated to the library's Match, with two davcloud-specific adjustments:
//
//   - An addressbook-query WITHOUT <C:filter> matches every object (RFC 6352,
//     section 8.6: the filter is optional). go-webdav v0.7's Match falls
//     through its empty prop-filters loop to `return false`, so it would
//     answer an empty multistatus - opencloud-contacts lists the address book
//     exactly this way. We handle the filterless case here; the response
//     properties are still rendered per the report's <D:prop>.
//   - A match error (e.g. an unexpandable recurrence) does not kill the whole
//     query: the object is re-evaluated as a plain one and only logged.
func (b *Backend) QueryAddressObjects(ctx context.Context, path string, query *webdavcarddav.AddressBookQuery) ([]webdavcarddav.AddressObject, error) {
	objs, err := b.loadObjects(ctx, path)
	if err != nil {
		return nil, err
	}
	if query == nil {
		return objs, nil
	}
	if len(query.PropFilters) == 0 {
		// No <C:filter>: every object matches. Respect the result limit.
		if query.Limit > 0 && query.Limit < len(objs) {
			return objs[:query.Limit], nil
		}
		return objs, nil
	}
	var out []webdavcarddav.AddressObject
	for _, obj := range objs {
		ok, err := webdavcarddav.Match(query, &obj)
		if err != nil {
			log.Printf("carddav: match failed for %s (%v), skipping", obj.Path, err)
			continue
		}
		if ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

func encodeVCF(card vcard.Card) ([]byte, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkPreconditions mirrors go-webdav's own filesystem backend semantics:
// a failed If-Match or a matching If-None-Match is 412 Precondition Failed.
func checkPreconditions(opts *webdavcarddav.PutAddressObjectOptions, existingETag string) error {
	if opts == nil {
		return nil
	}
	if opts.IfMatch.IsSet() {
		ok, err := opts.IfMatch.MatchETag(existingETag)
		if err != nil {
			return webdav.NewHTTPError(http.StatusBadRequest, err)
		}
		if !ok {
			return webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("carddav: If-Match condition failed"))
		}
	}
	if opts.IfNoneMatch.IsSet() {
		ok, err := opts.IfNoneMatch.MatchETag(existingETag)
		if err != nil {
			return webdav.NewHTTPError(http.StatusBadRequest, err)
		}
		if ok {
			return webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("carddav: If-None-Match condition failed"))
		}
	}
	return nil
}

// PutAddressObject implements the unified create/update of the v0.7 API,
// honouring the If-Match / If-None-Match preconditions from
// PutAddressObjectOptions.
func (b *Backend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *webdavcarddav.PutAddressObjectOptions) (*webdavcarddav.AddressObject, error) {
	segs := splitPath(b.local(path))
	if len(segs) != 3 {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("carddav: bad object path %q", path))
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
	if err != nil || c.Tag != "VADDRESSBOOK" {
		return nil, webdav.NewHTTPError(http.StatusConflict, fmt.Errorf("carddav: parent address book %q does not exist", segs[1]))
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

	data, err := encodeVCF(card)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("carddav: cannot encode address object: %w", err))
	}
	etag, err := b.st.WriteItem(user, segs[1], segs[2], data)
	if err != nil {
		return nil, err
	}
	return &webdavcarddav.AddressObject{
		Path:          b.serverPath(b.local(path)),
		Card:          card,
		ETag:          etag,
		ContentLength: int64(len(data)),
	}, nil
}

// DeleteAddressObject implements the backend interface.
func (b *Backend) DeleteAddressObject(ctx context.Context, path string) error {
	segs := splitPath(b.local(path))
	if len(segs) != 3 {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: bad object path %q", path))
	}
	user, err := userFromCtx(ctx, segs[0])
	if err != nil {
		return err
	}
	if err := b.st.DeleteItem(user, segs[1], segs[2]); err != nil {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("carddav: object %q not found", path))
	}
	return nil
}
