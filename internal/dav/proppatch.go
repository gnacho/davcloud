package dav

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"davcloud/internal/auth"
	"davcloud/internal/store"
)

// radicalePrefixes maps an XML namespace to the prefix Radicale uses as the
// property key in .Radicale.props. Client properties written via PROPPATCH
// with a known namespace are stored with this canonical prefix, so the file
// davcloud writes is byte-identical to what Radicale writes itself
// (ICAL:calendar-color, C:calendar-description, ...).
var radicalePrefixes = map[string]string{
	nsDAV:                      "D",
	nsCalDAV:                   "C",
	nsCardDAV:                  "CR",
	nsCS:                       "CS",
	nsICal:                     "ICAL",
	"http://inf-it.com/ns/ab/": "INF",
}

// radicaleKey builds the .Radicale.props key of a property. Known namespaces
// get the canonical prefix; unknown ones are stored in Clark notation
// ("{ns}local") so no information is lost, which deviates from Radicale only
// for namespaces no DAV client is known to write (Radicale stores the client
// declared prefix, an inherently ambiguous choice it can only resolve while
// the original request is around).
func radicaleKey(space, local string) string {
	if p, ok := radicalePrefixes[space]; ok {
		return p + ":" + local
	}
	return "{" + space + "}" + local
}

// keyToName is the inverse of radicaleKey.
func keyToName(key string) (space, local string, ok bool) {
	if strings.HasPrefix(key, "{") {
		if i := strings.Index(key, "}"); i > 0 {
			return key[1:i], key[i+1:], true
		}
		return "", "", false
	}
	if i := strings.Index(key, ":"); i > 0 {
		for ns, p := range radicalePrefixes {
			if p == key[:i] {
				return ns, key[i+1:], true
			}
		}
	}
	return "", "", false
}

// isProtectedProp reports whether a property is server managed and a
// PROPPATCH must not change it (RFC 4918, section 9.2: protected properties
// fail with 403 and a cannot-modify-protected-property error).
func isProtectedProp(space, local string) bool {
	switch space {
	case nsDAV:
		switch local {
		case "getetag", "getcontenttype", "getcontentlength", "getlastmodified",
			"creationdate", "resourcetype", "supported-report-set",
			"current-user-principal", "principal-URL", "owner",
			"current-user-privilege-set", "sync-token", "lockdiscovery",
			"supportedlock", "quota-available-bytes", "quota-used-bytes":
			return true
		}
	case nsCalDAV:
		switch local {
		case "calendar-home-set", "supported-calendar-component-set",
			"supported-collation-set", "max-resource-size",
			"max-instances", "max-attendees-per-instance", "calendar-timezone":
			return true
		}
	case nsCardDAV:
		switch local {
		case "addressbook-home-set", "max-resource-size", "supported-collation-set":
			return true
		}
	case nsCS:
		if local == "getctag" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

type propValue struct {
	XMLName xml.Name
	Inner   string `xml:",innerxml"`
}

type propValueList struct {
	Values []propValue `xml:",any"`
}

type propSetRemove struct {
	Prop propValueList `xml:"DAV: prop"`
}

type propUpdateBody struct {
	Sets    []propSetRemove `xml:"DAV: set"`
	Removes []propSetRemove `xml:"DAV: remove"`
}

func parsePropUpdate(r *http.Request) (*propUpdateBody, error) {
	var body propUpdateBody
	if err := decodeXMLBody(r, &body); err != nil {
		return nil, &davError{code: http.StatusBadRequest, msg: "dav: malformed propertyupdate body"}
	}
	return &body, nil
}

// valueText decodes the text of a property value (its inner XML may carry
// entity references; property values used by DAV clients are plain text).
func valueText(inner string) string {
	var v struct {
		Text string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte("<v>"+inner+"</v>"), &v); err != nil {
		return inner
	}
	return v.Text
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type propPatchResult struct {
	space  string
	local  string
	status int
}

// handleProppatch applies a property update to a collection, with RFC 4918
// atomicity: if any property fails, nothing is applied, the failing ones get
// their own status and the rest get 424 Failed Dependency.
func (h *Handler) handleProppatch(w http.ResponseWriter, r *http.Request) error {
	// Same as PROPFIND: a client may patch a default collection before any
	// listing, so the defaults are ensured first.
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
		return &davError{code: http.StatusForbidden, msg: "dav: PROPPATCH is only supported on collections"}
	}
	if res.item != "" {
		return &davError{code: http.StatusForbidden, msg: "dav: PROPPATCH on objects is not supported"}
	}
	body, err := parsePropUpdate(r)
	if err != nil {
		return err
	}
	if len(body.Sets) == 0 && len(body.Removes) == 0 {
		return &davError{code: http.StatusBadRequest, msg: "dav: empty propertyupdate"}
	}

	set := map[string]string{}
	var remove []string
	var results []propPatchResult
	failed := false

	for _, s := range body.Sets {
		for _, v := range s.Prop.Values {
			space, local := v.XMLName.Space, v.XMLName.Local
			if isProtectedProp(space, local) {
				results = append(results, propPatchResult{space, local, http.StatusForbidden})
				failed = true
				continue
			}
			set[radicaleKey(space, local)] = valueText(v.Inner)
			results = append(results, propPatchResult{space, local, http.StatusOK})
		}
	}
	for _, s := range body.Removes {
		for _, v := range s.Prop.Values {
			space, local := v.XMLName.Space, v.XMLName.Local
			if isProtectedProp(space, local) {
				results = append(results, propPatchResult{space, local, http.StatusForbidden})
				failed = true
				continue
			}
			remove = append(remove, radicaleKey(space, local))
			results = append(results, propPatchResult{space, local, http.StatusOK})
		}
	}

	if !failed {
		if err := h.Store.UpdateCollectionProps(res.user, res.coll, set, remove); err != nil {
			return &davError{code: http.StatusInternalServerError, msg: fmt.Sprintf("dav: cannot update collection properties: %v", err)}
		}
	} else {
		// Nothing is applied: mark the would-be successes as failed
		// dependency.
		for i := range results {
			if results[i].status == http.StatusOK {
				results[i].status = http.StatusFailedDependency
			}
		}
	}

	h.renderPropPatchResponse(w, r, res, results, failed)
	return nil
}

// renderPropPatchResponse answers 207 with one propstat per status group.
// Namespaces are collected in a first pass, then declared on the root:
// declaring them while writing the body would produce an unbound prefix.
func (h *Handler) renderPropPatchResponse(w http.ResponseWriter, r *http.Request, res *resource, results []propPatchResult, protected bool) {
	// Group properties by status, keeping the first appearance order.
	groups := map[int][]propPatchResult{}
	var statuses []int
	seen := map[int]bool{}
	for _, pr := range results {
		if !seen[pr.status] {
			seen[pr.status] = true
			statuses = append(statuses, pr.status)
		}
		groups[pr.status] = append(groups[pr.status], pr)
	}

	// Namespace prefix assignment for the properties echoed back.
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

	var body strings.Builder
	body.WriteString("<D:response><D:href>" + esc(h.href(r, res)) + "</D:href>")
	for _, status := range statuses {
		var props strings.Builder
		for _, pr := range groups[status] {
			props.WriteString(element(qname(pr.space, pr.local), ""))
		}
		var inner strings.Builder
		inner.WriteString(element("D:prop", props.String()))
		inner.WriteString(element("D:status", "HTTP/1.1 "+statusText(status)))
		if status == http.StatusForbidden {
			inner.WriteString(element("D:error", element("D:cannot-modify-protected-property", "")))
		}
		body.WriteString(element("D:propstat", inner.String()))
	}
	body.WriteString("</D:response>")

	var sb strings.Builder
	sb.WriteString(`<D:multistatus xmlns:D="DAV:"`)
	for _, space := range order {
		if space == nsDAV {
			continue
		}
		sb.WriteString(fmt.Sprintf(` xmlns:%s="%s"`, prefixes[space], esc(space)))
	}
	sb.WriteString(">" + body.String() + "</D:multistatus>")

	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write([]byte(sb.String()))
}

func statusText(code int) string {
	switch code {
	case http.StatusOK:
		return "200 OK"
	case http.StatusFailedDependency:
		return "424 Failed Dependency"
	case http.StatusForbidden:
		return "403 Forbidden"
	default:
		return fmt.Sprintf("%d %s", code, http.StatusText(code))
	}
}

// extraProps returns the client properties of a collection as supported
// propEntry values, so PROPFIND serves back what PROPPATCH stored.
func extraProps(c store.Collection) []propEntry {
	if len(c.Extra) == 0 {
		return nil
	}
	out := make([]propEntry, 0, len(c.Extra))
	for key, value := range c.Extra {
		space, local, ok := keyToName(key)
		if !ok || !validXMLName(local) {
			continue
		}
		out = append(out, propEntry{space, local, esc(value)})
	}
	return out
}
