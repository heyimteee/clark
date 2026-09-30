// Package calendar gives Clark read, create, and delete access to a CalDAV
// server. The primary target is iCloud, which is free and needs no Apple
// developer account — only an Apple ID and an app-specific password.
//
// iCloud's CalDAV server is not a well-behaved one. Three of its quirks shape
// this client:
//
//   - There is no primary-calendar alias. The collection URL must be discovered
//     with PROPFIND, and it is region-redirected (caldav.icloud.com →
//     pNN-caldav.icloud.com), so the discovered URL is the one that must be used.
//   - Property elements are not consistently ordered and are sometimes CDATA
//     wrapped, so responses are scanned tolerantly rather than decoded into a
//     rigid struct that would fail on a different account's server.
//   - Reads are capped at one year per request, so a wide window is chunked.
//
// The WebDAV transport is hand-rolled because CalDAV needs only four verbs —
// PROPFIND, REPORT, PUT, DELETE — and the genuinely fiddly part is the
// iCalendar layer, handled by a real RFC 5545 implementation.
package calendar

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CalDAVCalendar is one discovered collection.
type CalDAVCalendar struct {
	// Href is the absolute collection URL new events are written to.
	Href string `json:"href"`
	// Name is the human-readable display name.
	Name string `json:"name"`
	// Description is the optional iCalendar calendar description.
	Description string `json:"description,omitempty"`
	// Color is the Apple colour hint, when the server exposes one.
	Color string `json:"color,omitempty"`
}

// CalDAVOptions configures a CalDAV client.
type CalDAVOptions struct {
	// BaseURL is the server root, e.g. https://caldav.icloud.com.
	BaseURL string
	// User is the Apple ID or account username.
	User string
	// Password is the app-specific password.
	Password string
	// DefaultCalendarHref pins the write target. When empty, the first discovered
	// calendar is used.
	DefaultCalendarHref string
	// Timeout bounds each request.
	Timeout time.Duration
	// InsecureSkipVerify is for a self-signed reverse proxy in front of the
	// server. Never set this against iCloud itself.
	InsecureSkipVerify bool
}

// defaultTimeout is generous: Apple's server is slow, especially on REPORT.
const defaultTimeout = 30 * time.Second

// CalDAVClient is a Client backed by a CalDAV server.
type CalDAVClient struct {
	baseURL  string
	user     string
	password string
	http     *http.Client

	// defaultHref pins writes to a specific collection when configured.
	defaultHref string
	// discovered caches the collection list. Discovery is a round trip and the
	// answers are stable, so a list followed by a create should not repeat it.
	discovered   []CalDAVCalendar
	discoveredOK bool
}

// NewCalDAVClient builds a CalDAV-backed Client.
func NewCalDAVClient(opts CalDAVOptions) *CalDAVClient {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &CalDAVClient{
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		user:         opts.User,
		password:     opts.Password,
		defaultHref:  opts.DefaultCalendarHref,
		discoveredOK: false,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion:         tls.VersionTLS12,
					InsecureSkipVerify: opts.InsecureSkipVerify, //nolint:gosec // opt-in and documented
				},
			},
		},
	}
}

// --- WebDAV plumbing --------------------------------------------------------

// calendarQueryBody is the RFC 4791 §7.8 REPORT payload: every VEVENT
// overlapping the window, with its full iCalendar body.
const calendarQueryBody = `<?xml version="1.0" encoding="UTF-8" ?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <c:filter>
    <c:comp-filter name="VCALENDAR">
      <c:comp-filter name="VEVENT">
        <c:time-range start="%s" end="%s"/>
      </c:comp-filter>
    </c:comp-filter>
  </c:filter>
</c:calendar-query>`

const propfindBody = `<?xml version="1.0" encoding="UTF-8" ?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"
           xmlns:ical="http://apple.com/ns/ical/">
  <d:prop>
    <d:current-user-principal/>
    <d:resourcetype/>
    <d:displayname/>
    <c:calendar-description/>
    <ical:calendar-color/>
  </d:prop>
</d:propfind>`

// do issues an authenticated WebDAV request. Some servers require Depth as a
// request header rather than a URL segment, so it is set explicitly.
func (c *CalDAVClient) do(ctx context.Context, method, target, contentType, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	return c.http.Do(req)
}

// readBody drains and closes a response, returning its contents.
func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// --- discovery --------------------------------------------------------------

// Calendars lists the collections available to the account.
func (c *CalDAVClient) Calendars(ctx context.Context) ([]CalDAVCalendar, error) {
	if err := c.discover(ctx); err != nil {
		return nil, err
	}
	return c.discovered, nil
}

// discover resolves the principal and collection list, caching the result.
func (c *CalDAVClient) discover(ctx context.Context) error {
	if c.discoveredOK {
		return nil
	}
	principal, err := c.principalHref(ctx)
	if err != nil {
		return err
	}
	cals, err := c.calendarHrefs(ctx, principal)
	if err != nil {
		return err
	}
	if len(cals) == 0 {
		return fmt.Errorf("no calendars found at %s for %s", c.baseURL, c.user)
	}
	c.discovered = cals
	c.discoveredOK = true
	return nil
}

// principalHref PROPFINDs the server root for current-user-principal.
func (c *CalDAVClient) principalHref(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, "PROPFIND", c.baseURL+"/", "application/xml; charset=utf-8", propfindBody)
	if err != nil {
		return "", fmt.Errorf("calendar discovery failed: %w", err)
	}
	raw, err := readBody(resp)
	if err != nil {
		return "", fmt.Errorf("calendar discovery unreadable: %w", err)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return "", fmt.Errorf("calendar discovery returned %s", resp.Status)
	}
	href := hrefAfterProp(string(raw), "current-user-principal")
	if href == "" {
		return "", fmt.Errorf("calendar discovery returned no principal")
	}
	return resolveHref(c.baseURL, href)
}

// calendarHrefs PROPFINDs the principal home for calendar collections.
func (c *CalDAVClient) calendarHrefs(ctx context.Context, principal string) ([]CalDAVCalendar, error) {
	resp, err := c.do(ctx, "PROPFIND", principal, "application/xml; charset=utf-8", propfindBody)
	if err != nil {
		return nil, fmt.Errorf("calendar list failed: %w", err)
	}
	raw, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("calendar list unreadable: %w", err)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("calendar list returned %s", resp.Status)
	}
	return parseCalendarResponses(c.baseURL, string(raw)), nil
}

// parseCalendarResponses splits a multistatus body into per-collection records.
//
// A tolerant scan is used rather than encoding/xml into a fixed struct: iCloud
// varies element order between accounts and versions, and a strict decode that
// fails on an unexpected ordering would take the whole calendar down.
func parseCalendarResponses(base, body string) []CalDAVCalendar {
	var out []CalDAVCalendar
	for _, chunk := range splitElements(body, "<d:response", "</d:response>") {
		href := firstTag(chunk, "d:href")
		if href == "" {
			continue
		}
		// A calendar is a collection whose resourcetype names the calendar
		// resource type. The self-closing form is matched exactly, because
		// `<c:calendar-description>` also begins with `<c:calendar` and would
		// otherwise make a description-bearing property look like a collection.
		if !isSelfClosing(chunk, "c:calendar") || !containsTag(chunk, "d:collection") {
			continue
		}
		abs, err := resolveHref(base, unescapeXML(href))
		if err != nil {
			continue
		}
		out = append(out, CalDAVCalendar{
			Href:        abs,
			Name:        unescapeXML(firstTag(chunk, "d:displayname")),
			Description: unescapeXML(firstTag(chunk, "c:calendar-description")),
			Color:       firstTag(chunk, "ical:calendar-color"),
		})
	}
	return out
}

// splitElements yields the inner text of every element bounded by open/close.
func splitElements(body, open, close string) []string {
	var out []string
	rest := body
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, close)
		if j < 0 {
			return out
		}
		out = append(out, rest[:j+len(close)])
		rest = rest[j+len(close):]
	}
}

// firstTag returns the text content of the first occurrence of a named element.
func firstTag(chunk, tag string) string {
	open := "<" + tag
	i := strings.Index(chunk, open)
	if i < 0 {
		return ""
	}
	rest := chunk[i+len(open):]
	// Skip any attributes up to the closing bracket.
	if gt := strings.Index(rest, ">"); gt >= 0 {
		if q := strings.IndexByte(rest[:gt], ' '); q >= 0 {
			rest = rest[gt+1:]
		} else {
			rest = rest[gt+1:]
		}
	} else {
		return ""
	}
	end := strings.Index(rest, "</"+tag+">")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// containsTag reports whether an element is present at all.
func containsTag(chunk, needle string) bool {
	return strings.Contains(chunk, needle)
}

// isSelfClosing reports whether chunk declares the given element in empty-element
// form (`<tag/>` or `<tag />`). Matching the closing bracket is what
// distinguishes `<c:calendar/>` from `<c:calendar-description>`.
func isSelfClosing(chunk, tag string) bool {
	open := "<" + tag
	i := strings.Index(chunk, open)
	if i < 0 {
		return false
	}
	rest := chunk[i+len(open):]
	// Skip any attributes before the bracket.
	if sp := strings.IndexAny(rest, " \t\n"); sp >= 0 {
		if gt := strings.Index(rest, ">"); gt >= 0 && sp < gt {
			rest = rest[sp:]
		}
	}
	return strings.HasPrefix(rest, "/>")
}

// unescapeXML reverses the five predefined entities. CDATA content arrives
// verbatim, so this is only applied to ordinary text nodes.
func unescapeXML(s string) string {
	r := strings.NewReplacer(
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&amp;", "&",
	)
	return r.Replace(s)
}

// hrefAfterProp finds the href following a named property element.
func hrefAfterProp(body, prop string) string {
	idx := strings.Index(body, prop)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(prop):]
	h := strings.Index(rest, "href")
	if h < 0 {
		return ""
	}
	rest = rest[h+len("href"):]
	open := strings.Index(rest, ">")
	if open < 0 {
		return ""
	}
	rest = rest[open+1:]
	end := strings.Index(rest, "<")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// resolveHref turns a possibly-relative href into an absolute URL.
func resolveHref(base, href string) (string, error) {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href, nil
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// stripElement removes the opening and closing tags of a named element.
func stripElement(chunk, tag string) string {
	open := "<" + tag
	i := strings.Index(chunk, open)
	if i < 0 {
		return ""
	}
	rest := chunk[i+len(open):]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		return ""
	}
	rest = rest[gt+1:]
	end := strings.Index(rest, "</"+tag+">")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// drain closes a response and returns the connection to the pool.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// icalPathEscape escapes a UID for use as a URL path segment.
func icalPathEscape(s string) string { return url.PathEscape(s) }
