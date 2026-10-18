package dav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// XML namespaces used by the DAV properties.
const (
	nsDAV     = "DAV:"
	nsCalDAV  = "urn:ietf:params:xml:ns:caldav"
	nsCardDAV = "urn:ietf:params:xml:ns:carddav"
	nsCS      = "http://calendarserver.org/ns/"
	nsICal    = "http://apple.com/ns/ical/"
)

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

// propfindRequest is the parsed PROPFIND body. All three modes are optional: a
// request without a body is an allprop request (RFC 4918, section 9.1).
type propfindRequest struct {
	Prop     *propList
	AllProp  bool
	PropName bool
}

// propList holds the property names of a <prop> or <include> element.
type propList struct {
	Names []xml.Name
}

func (l *propList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			l.Names = append(l.Names, tok.Name)
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

type propfindBody struct {
	Prop     *propList `xml:"DAV: prop"`
	AllProp  *struct{} `xml:"DAV: allprop"`
	PropName *struct{} `xml:"DAV: propname"`
}

func parsePropfind(r *http.Request) (*propfindRequest, error) {
	req := &propfindRequest{}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, bodyError(err, "dav: cannot read request body")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		req.AllProp = true // an empty body means allprop
		return req, nil
	}
	var body propfindBody
	if err := xml.Unmarshal(data, &body); err != nil {
		return nil, &davError{code: http.StatusBadRequest, msg: "dav: malformed propfind body"}
	}
	switch {
	case body.Prop != nil:
		req.Prop = body.Prop
	case body.PropName != nil:
		req.PropName = true
	default:
		req.AllProp = true
	}
	return req, nil
}

// mkcolBody is the body of MKCOL / MKCALENDAR. It deliberately has no XMLName:
// MKCALENDAR uses its own root element and both are decoded with this struct.
type mkcolBody struct {
	Set struct {
		Prop struct {
			Resourcetype struct {
				Collection  *struct{} `xml:"DAV: collection"`
				Calendar    *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
				AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
			} `xml:"DAV: resourcetype"`
			DisplayName string `xml:"DAV: displayname"`
		} `xml:"DAV: prop"`
	} `xml:"DAV: set"`
}

// decodeXMLBody decodes an XML request body, treating an empty body as a
// valid, empty document.
func decodeXMLBody(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := xml.NewDecoder(r.Body).Decode(v)
	if err == io.EOF {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// Depth
// ---------------------------------------------------------------------------

type depth int

const (
	depthZero depth = iota
	depthOne
	depthInfinity
)

func parseDepth(s string) (depth, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0":
		return depthZero, nil
	case "1":
		return depthOne, nil
	case "infinity":
		return depthInfinity, nil
	default:
		return 0, &davError{code: http.StatusBadRequest, msg: "dav: invalid Depth header"}
	}
}

// ---------------------------------------------------------------------------
// XML helpers
// ---------------------------------------------------------------------------

// propEntry is one supported property with its value already serialised as the
// inner XML of the element.
type propEntry struct {
	space string
	name  string
	inner string
}

func propKey(space, name string) string { return space + " " + name }

// esc escapes text for use as XML content.
func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// element renders an element, name being already qualified ("D:href"). An
// empty inner value yields a self-closing element.
func element(name, inner string) string {
	if inner == "" {
		return "<" + name + "/>"
	}
	return "<" + name + ">" + inner + "</" + name + ">"
}

// attrElement renders an element carrying a single attribute.
func attrElement(name, attr, value string) string {
	return "<" + name + " " + attr + `="` + esc(value) + `"/>`
}

// validXMLName guards against building malformed markup out of a client
// supplied name. The XML decoder already rejects invalid names, so this is
// belt and braces.
func validXMLName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return true
}

// escapeSegment percent-encodes one path segment.
func escapeSegment(s string) string { return url.PathEscape(s) }

// ---------------------------------------------------------------------------
// Property builders
// ---------------------------------------------------------------------------

// hrefElement renders a DAV href.
func hrefElement(path string) string {
	return element("D:href", esc(path))
}

// privileges is the current-user-privilege-set value: the authenticated user
// has full access to its own home set.
func privileges() string {
	names := []string{
		"read", "write", "write-content", "write-properties",
		"bind", "unbind", "read-current-user-privilege-set",
	}
	var b strings.Builder
	for _, p := range names {
		b.WriteString(element("D:privilege", element("D:"+p, "")))
	}
	return b.String()
}

// supportedReports lists the REPORTs the server answers: the queries are
// delegated to go-webdav, sync-collection is implemented in internal/dav
// (RFC 6578, full sync when the token does not match, like Radicale's
// multifilesystem storage).
func supportedReports(kind Kind) string {
	reports := []string{"calendar-query", "calendar-multiget", "free-busy-query"}
	ns := "C:"
	if kind == KindAddressBook {
		reports = []string{"addressbook-query", "addressbook-multiget"}
		ns = "CR:"
	}
	var b strings.Builder
	for _, rep := range reports {
		b.WriteString(element("D:supported-report", element("D:report", element(ns+rep, ""))))
	}
	b.WriteString(element("D:supported-report", element("D:report", element("D:sync-collection", ""))))
	return b.String()
}

// supportedComponents is the supported-calendar-component-set value.
func supportedComponents(comps []string) string {
	if len(comps) == 0 {
		comps = []string{"VEVENT", "VTODO", "VJOURNAL"}
	}
	var b strings.Builder
	for _, c := range comps {
		b.WriteString(attrElement("C:comp", "name", c))
	}
	return b.String()
}

// homeHref is the client visible URL of the user's home set.
func (h *Handler) homeHref(r *http.Request) string {
	return h.externalPrefix(r) + "/" + escapeSegment(auth.User(r.Context())) + "/"
}

// propsForResource builds the supported properties of one resource.
func (h *Handler) propsForResource(r *http.Request, res *resource) []propEntry {
	home := h.homeHref(r)

	common := []propEntry{
		{nsDAV, "current-user-principal", hrefElement(home)},
		{nsDAV, "principal-URL", hrefElement(home)},
		{nsDAV, "owner", hrefElement(home)},
		{nsDAV, "current-user-privilege-set", privileges()},
		{nsDAV, "supported-report-set", supportedReports(h.Kind)},
	}

	homeSet := propEntry{nsCalDAV, "calendar-home-set", hrefElement(home)}
	if h.Kind == KindAddressBook {
		homeSet = propEntry{nsCardDAV, "addressbook-home-set", hrefElement(home)}
	}

	collectionRT := "D:collection"
	if h.Kind == KindCalendar {
		collectionRT += "|C:calendar"
	} else {
		collectionRT += "|CR:addressbook"
	}

	switch {
	case res.user == "":
		// Root of the endpoint: it only knows about the principal.
		return append([]propEntry{
			{nsDAV, "resourcetype", element("D:collection", "")},
			{nsDAV, "displayname", ""},
		}, append(common, homeSet)...)

	case res.coll == "":
		// Home set, which is also the principal in the flat Radicale layout.
		return append([]propEntry{
			{nsDAV, "resourcetype", element("D:collection", "")},
			{nsDAV, "displayname", esc(res.user)},
		}, append(common, homeSet)...)

	case res.item == "":
		// Collection.
		var rt strings.Builder
		for _, part := range strings.Split(collectionRT, "|") {
			rt.WriteString(element(part, ""))
		}
		props := []propEntry{
			{nsDAV, "resourcetype", rt.String()},
			{nsDAV, "displayname", esc(res.collObj.DisplayName)},
			{nsDAV, "getcontenttype", esc(h.contentType())},
			{nsCS, "getctag", esc(h.ctag(res.user, res.coll))},
			{nsDAV, "sync-token", esc(h.syncToken(res.user, res.coll))},
		}
		if h.MaxItemSize > 0 {
			// The cap the endpoint applies to a single object, announced so a
			// well-behaved client can refuse to send what will not be stored.
			ns := nsCalDAV
			if h.Kind == KindAddressBook {
				ns = nsCardDAV
			}
			props = append(props, propEntry{ns, "max-resource-size", strconv.FormatInt(h.MaxItemSize, 10)})
		}
		if h.Kind == KindCalendar {
			props = append(props, propEntry{
				nsCalDAV,
				"supported-calendar-component-set",
				supportedComponents(res.collObj.Components),
			})
		}
		// Client properties written via PROPPATCH (e.g. ICAL:calendar-color).
		props = append(props, extraProps(res.collObj)...)
		return append(props, append(common, homeSet)...)

	default:
		// Calendar or address object.
		etag := ""
		if e, err := h.Store.ItemETag(res.user, res.coll, res.item); err == nil {
			etag = store.QuoteETag(e)
		}
		return append([]propEntry{
			{nsDAV, "resourcetype", ""},
			{nsDAV, "getetag", esc(etag)},
			{nsDAV, "getcontenttype", esc(h.contentType())},
		}, common...)
	}
}

// contentType is the media type of the items of the endpoint.
func (h *Handler) contentType() string {
	if h.Kind == KindCalendar {
		return "text/calendar"
	}
	return "text/vcard"
}

// ---------------------------------------------------------------------------
// Depth expansion
// ---------------------------------------------------------------------------

// expand returns the target resource plus its descendants, parents first.
// children is set for Depth: 1 (direct children only) and full for Depth:
// infinity (all the way down; the tree is at most three levels deep).
func (h *Handler) expand(r *http.Request, res *resource, children, full bool) ([]*resource, error) {
	out := []*resource{res}

	switch {
	case res.user == "":
		home := &resource{kind: h.Kind, user: auth.User(r.Context())}
		sub, err := h.expand(r, home, children, full)
		if err != nil {
			return nil, err
		}
		return append(out, sub...), nil

	case res.coll == "":
		if !children {
			return out, nil
		}
		colls, err := h.collections(res.user)
		if err != nil {
			return nil, err
		}
		for _, c := range colls {
			child := &resource{kind: h.Kind, user: res.user, coll: c.ID, collObj: c}
			sub, err := h.expand(r, child, true, full)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		}
		return out, nil

	case res.item == "":
		if !children {
			return out, nil
		}
		names, err := h.Store.ListItems(res.user, res.coll)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, &resource{
				kind: h.Kind, user: res.user, coll: res.coll, collObj: res.collObj, item: n,
			})
		}
		return out, nil
	}
	return out, nil
}

// userOr returns the resource user, falling back to the authenticated one for
// the endpoint root.
func (res *resource) userOr(r *http.Request) string {
	if res.user != "" {
		return res.user
	}
	return auth.User(r.Context())
}

// ---------------------------------------------------------------------------
// PROPFIND handler
// ---------------------------------------------------------------------------

// handlePropfind answers PROPFIND with the Radicale URL semantics.
func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) error {
	depth, err := parseDepth(r.Header.Get("Depth"))
	if err != nil {
		return err
	}
	req, err := parsePropfind(r)
	if err != nil {
		return err
	}
	// The root and the home set create the user's default collections on first
	// access, exactly like Radicale's predefined_collections. A client that
	// already knows the URL of a default collection may PROPFIND it directly
	// before any listing, so the defaults are ensured before resolving.
	if user := auth.User(r.Context()); user != "" {
		if _, err := h.collections(user); err != nil {
			return err
		}
	}
	res, err := h.resolve(r)
	if err != nil {
		return err
	}
	targets, err := h.expand(r, res, depth != depthZero, depth == depthInfinity)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.Header().Set("DAV", davHeader(h.Kind))
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(h.renderMultiStatus(r, req, targets))
	return nil
}

// renderMultiStatus builds the multistatus document. Namespaces are declared
// on the root element: DAV: is always D, the other known namespaces get
// readable prefixes, and anything else gets nsN in order of appearance.
func (h *Handler) renderMultiStatus(r *http.Request, req *propfindRequest, targets []*resource) []byte {
	return h.renderMultiStatusWithToken(r, req, targets, "")
}

// renderMultiStatusWithToken is renderMultiStatus plus a trailing sync-token,
// used by REPORT sync-collection (RFC 6578).
func (h *Handler) renderMultiStatusWithToken(r *http.Request, req *propfindRequest, targets []*resource, syncToken string) []byte {
	prefixes := map[string]string{nsDAV: "D"}
	var order []string
	next := 0
	qname := func(space, local string) string {
		p, ok := prefixes[space]
		if !ok {
			switch space {
			case nsCalDAV:
				p = "C"
			case nsCardDAV:
				p = "CR"
			case nsCS:
				p = "CS"
			case nsICal:
				p = "IC"
			default:
				p = fmt.Sprintf("ns%d", next)
				next++
			}
			prefixes[space] = p
			order = append(order, space)
		}
		return p + ":" + local
	}

	responses := make([]string, 0, len(targets))
	for _, res := range targets {
		supported := h.propsForResource(r, res)
		index := make(map[string]propEntry, len(supported))
		for _, p := range supported {
			index[propKey(p.space, p.name)] = p
		}

		var found, missing []string
		switch {
		case req.PropName:
			for _, p := range supported {
				found = append(found, element(qname(p.space, p.name), ""))
			}
		case req.Prop != nil:
			for _, name := range req.Prop.Names {
				if !validXMLName(name.Local) {
					continue
				}
				if p, ok := index[propKey(name.Space, name.Local)]; ok {
					found = append(found, element(qname(p.space, p.name), p.inner))
					continue
				}
				if name.Space != "" {
					missing = append(missing, element(qname(name.Space, name.Local), ""))
				}
			}
		default: // allprop
			for _, p := range supported {
				found = append(found, element(qname(p.space, p.name), p.inner))
			}
		}

		var sb strings.Builder
		sb.WriteString("<D:response><D:href>" + esc(h.href(r, res)) + "</D:href>")
		if len(found) > 0 {
			sb.WriteString(element("D:propstat",
				element("D:prop", strings.Join(found, ""))+
					element("D:status", "HTTP/1.1 200 OK")))
		}
		if len(missing) > 0 {
			sb.WriteString(element("D:propstat",
				element("D:prop", strings.Join(missing, ""))+
					element("D:status", "HTTP/1.1 404 Not Found")))
		}
		sb.WriteString("</D:response>")
		responses = append(responses, sb.String())
	}

	decl := []string{`xmlns:D="DAV:"`}
	// The resourcetype and supported-report-set values embed the endpoint
	// namespace prefix directly (C:calendar / CR:addressbook), so that
	// namespace is always declared, used or not.
	if h.Kind == KindCalendar {
		decl = append(decl, fmt.Sprintf(`xmlns:C="%s"`, esc(nsCalDAV)))
	} else {
		decl = append(decl, fmt.Sprintf(`xmlns:CR="%s"`, esc(nsCardDAV)))
	}
	seenDecl := map[string]bool{nsDAV: true, nsCalDAV: true, nsCardDAV: true}
	for _, space := range order {
		if seenDecl[space] {
			continue
		}
		decl = append(decl, fmt.Sprintf(`xmlns:%s="%s"`, prefixes[space], esc(space)))
	}

	var sb strings.Builder
	sb.WriteString("<D:multistatus " + strings.Join(decl, " ") + ">")
	sb.WriteString(strings.Join(responses, ""))
	if syncToken != "" {
		sb.WriteString(element("D:sync-token", esc(syncToken)))
	}
	sb.WriteString("</D:multistatus>")
	return []byte(sb.String())
}

// davHeader is the DAV compliance class header of an endpoint.
func davHeader(kind Kind) string {
	if kind == KindCalendar {
		return "1, 2, 3, calendar-access"
	}
	return "1, 3, addressbook"
}
