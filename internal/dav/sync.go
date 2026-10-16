package dav

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"

	"davcloud/internal/auth"
)

// syncTokenPrefix is the URI prefix of the opaque sync tokens, analogous to
// Radicale's "http://radicale.org/ns/sync/<token>".
const syncTokenPrefix = "http://davcloud/ns/sync/"

// syncToken is the current sync token of a collection. The multifilesystem
// store keeps no change history, so the token is derived from the ctag: it
// changes exactly when the collection content changes.
func (h *Handler) syncToken(user, coll string) string {
	return syncTokenPrefix + h.ctag(user, coll)
}

// ---------------------------------------------------------------------------
// REPORT dispatch
// ---------------------------------------------------------------------------

// reportRootName returns the name of the first element of a REPORT body, or
// an empty name when the body is empty or not XML.
func reportRootName(raw []byte) xml.Name {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.Name{}
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name
		}
	}
}

// handleReport routes REPORT: sync-collection is answered here (go-webdav has
// no server side implementation); everything else keeps going to the library
// with the body restored.
func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return &davError{code: http.StatusBadRequest, msg: "dav: cannot read the request body"}
	}
	if name := reportRootName(raw); name.Space == nsDAV && name.Local == "sync-collection" {
		if err := h.handleSyncCollection(w, r, raw); err != nil {
			return err
		}
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.Legacy.ServeHTTP(w, r)
	return nil
}

// ---------------------------------------------------------------------------
// sync-collection (RFC 6578)
// ---------------------------------------------------------------------------

type syncCollectionBody struct {
	SyncToken string    `xml:"DAV: sync-token"`
	SyncLevel string    `xml:"DAV: sync-level"`
	Prop      *propList `xml:"DAV: prop"`
}

// handleSyncCollection answers REPORT sync-collection. The semantics match
// what Radicale's multifilesystem storage can provide: an unknown or stale
// token gets a full sync (every item with its properties), and a current
// token gets an empty response, so a client with no changes pending pays one
// tiny request.
func (h *Handler) handleSyncCollection(w http.ResponseWriter, r *http.Request, raw []byte) error {
	// A client may sync a default collection before any listing.
	if user := auth.User(r.Context()); user != "" {
		if _, err := h.collections(user); err != nil {
			return err
		}
	}
	res, err := h.resolve(r)
	if err != nil {
		return err
	}
	if res.coll == "" {
		return &davError{code: http.StatusForbidden, msg: "dav: sync-collection applies to a collection"}
	}
	if res.item != "" {
		return &davError{code: http.StatusForbidden, msg: "dav: sync-collection applies to a collection"}
	}

	var body syncCollectionBody
	if err := xml.Unmarshal(raw, &body); err != nil {
		return &davError{code: http.StatusBadRequest, msg: "dav: malformed sync-collection body"}
	}

	token := h.syncToken(res.user, res.coll)

	// Only the changes since the client token are reported; without a change
	// history the only honest answer to a stale token is the full listing.
	var targets []*resource
	if body.SyncToken != token {
		names, err := h.Store.ListItems(res.user, res.coll)
		if err != nil {
			return &davError{code: http.StatusInternalServerError, msg: "dav: cannot list the collection"}
		}
		for _, n := range names {
			targets = append(targets, &resource{
				kind: h.Kind, user: res.user, coll: res.coll, collObj: res.collObj, item: n,
			})
		}
	}

	req := &propfindRequest{Prop: body.Prop}
	if body.Prop == nil {
		req.AllProp = true
	}

	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.Header().Set("DAV", davHeader(h.Kind))
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(h.renderMultiStatusWithToken(r, req, targets, token))
	return nil
}
