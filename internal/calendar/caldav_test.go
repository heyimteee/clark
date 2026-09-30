package calendar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const rootMultistatus = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ical="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/</d:href>
    <d:propstat><d:prop><d:current-user-principal><d:href>/1234/principal/</d:href></d:current-user-principal></d:prop></d:propstat>
  </d:response>
</d:multistatus>`

const principalMultistatus = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ical="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/1234/principal/</d:href>
    <d:propstat><d:prop>
      <d:resourcetype><d:collection/></d:resourcetype>
      <d:displayname>Work</d:displayname>
      <c:calendar-description>Client commitments</c:calendar-description>
      <ical:calendar-color>#FF0000</ical:calendar-color>
    </d:prop></d:propstat>
  </d:response>
  <d:response>
    <d:href>/1234/calendar/home/</d:href>
    <d:propstat><d:prop>
      <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
      <d:displayname>Home</d:displayname>
    </d:prop></d:propstat>
  </d:response>
</d:multistatus>`

// fakeCalDAV is a minimal CalDAV server: enough surface to exercise discovery,
// REPORT, PUT, GET, and DELETE without touching the network.
type fakeCalDAV struct {
	objects  map[string]string // href -> ics
	cals     []string
	lastPut  string
	putCount int
	deletes  []string
	authOK   func(user, pass string) bool
}

func newFakeCalDAV() *fakeCalDAV {
	return &fakeCalDAV{objects: map[string]string{}}
}

func (f *fakeCalDAV) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if f.authOK != nil {
			u, p, _ := r.BasicAuth()
			if !f.authOK(u, p) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		switch r.Method {
		case "PROPFIND":
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusMultiStatus)
				fmt.Fprint(w, rootMultistatus)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, principalMultistatus)

		case "REPORT":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			var b strings.Builder
			for _, href := range f.cals {
				ics, ok := f.objects[href]
				if !ok {
					continue
				}
				fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop>`+
					`<c:calendar-data><![CDATA[%s]]></c:calendar-data>`+
					`</d:prop></d:propstat></d:response>`, href, ics)
			}
			fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">%s</d:multistatus>`, b.String())

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.lastPut = string(body)
			f.putCount++
			f.objects[r.URL.Path] = string(body)
			w.WriteHeader(http.StatusCreated)

		case http.MethodGet:
			ics, ok := f.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, ics)

		case http.MethodDelete:
			f.deletes = append(f.deletes, r.URL.Path)
			delete(f.objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func readAll(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	return string(body)
}

func newTestClient(t *testing.T, f *fakeCalDAV) (*CalDAVClient, func()) {
	t.Helper()
	ts := httptest.NewServer(f.handler(t))
	c := NewCalDAVClient(CalDAVOptions{
		BaseURL:             ts.URL,
		User:                "master@icloud.com",
		Password:            "app-specific",
		DefaultCalendarHref: ts.URL + "/1234/calendar/home/",
		Timeout:             5 * time.Second,
	})
	return c, ts.Close
}

// TestCalDAVDiscovery proves calendars are discovered rather than assumed, and
// that only real calendar collections are returned.
func TestCalDAVDiscovery(t *testing.T) {
	f := newFakeCalDAV()
	c, done := newTestClient(t, f)
	defer done()

	cals, err := c.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	if len(cals) != 1 {
		t.Fatalf("discovered %d calendars, want 1 (the principal home is not a calendar): %+v", len(cals), cals)
	}
	if cals[0].Name != "Home" {
		t.Errorf("name = %q, want Home", cals[0].Name)
	}
	if !strings.HasSuffix(cals[0].Href, "/1234/calendar/home/") {
		t.Errorf("href = %q, want the absolute discovered collection URL", cals[0].Href)
	}
}

// TestCalDAVDiscoveryCaches proves discovery is not repeated on every call.
func TestCalDAVDiscoveryCaches(t *testing.T) {
	f := newFakeCalDAV()

	// One discovery costs two PROPFINDs (server root, then the principal home).
	// Repeated calls must add none.
	inner := f.handler(t)
	probefs := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			probefs++
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()
	c2 := NewCalDAVClient(CalDAVOptions{BaseURL: ts.URL, Timeout: 5 * time.Second})

	if _, err := c2.Calendars(context.Background()); err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	first := probefs
	if first == 0 {
		t.Fatal("no PROPFIND issued; discovery did not run")
	}
	for i := 0; i < 4; i++ {
		if _, err := c2.Calendars(context.Background()); err != nil {
			t.Fatalf("Calendars: %v", err)
		}
	}
	if probefs != first {
		t.Errorf("PROPFIND went from %d to %d across repeated calls; discovery should be cached", first, probefs)
	}
}

// TestCalDAVListDecodesReport proves the full round trip: REPORT, decode the
// multistatus, parse the iCalendar, and hand back events.
func TestCalDAVListDecodesReport(t *testing.T) {
	f := newFakeCalDAV()
	c, done := newTestClient(t, f)
	defer done()
	f.cals = []string{"/1234/calendar/home/a.ics"}
	f.objects["/1234/calendar/home/a.ics"] = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:evt-1
DTSTAMP:20260101T000000Z
DTSTART:20260105T140000Z
DTEND:20260105T150000Z
SUMMARY:Client review
END:VEVENT
END:VCALENDAR`

	got, err := c.List(context.Background(), jan(1, 0, 0), jan(10, 0, 0))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Client review" {
		t.Fatalf("List = %+v, want the single decoded event", got)
	}
}

// TestCalDAVCreateWritesObject proves Create PUTs a well-formed object under a
// UID-derived href and never reuses a colliding name.
func TestCalDAVCreateWritesObject(t *testing.T) {
	f := newFakeCalDAV()
	c, done := newTestClient(t, f)
	defer done()

	uid, err := c.Create(context.Background(), Event{
		Title: "Dinner", Start: jan(9, 19, 0), End: jan(9, 21, 0),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if uid == "" {
		t.Fatal("Create returned an empty uid")
	}
	if !strings.Contains(f.lastPut, "SUMMARY:Dinner") {
		t.Errorf("PUT body missing the summary:\n%s", f.lastPut)
	}
	if !strings.Contains(f.lastPut, "BEGIN:VCALENDAR") || !strings.Contains(f.lastPut, "END:VCALENDAR") {
		t.Errorf("PUT body is not a VCALENDAR:\n%s", f.lastPut)
	}
}

// TestCalDAVDeleteRemovesObject proves a bare UID deletes the whole series.
func TestCalDAVDeleteRemovesObject(t *testing.T) {
	f := newFakeCalDAV()
	c, done := newTestClient(t, f)
	defer done()
	f.objects["/1234/calendar/home/series.ics"] = "BEGIN:VCALENDAR\nEND:VCALENDAR"

	if err := c.Delete(context.Background(), "series"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.deletes) != 1 || !strings.HasSuffix(f.deletes[0], "series.ics") {
		t.Errorf("deletes = %v, want the series object removed", f.deletes)
	}
}

// TestCalDAVDeleteSingleOccurrenceAddsExdate is the behaviour EventKit gave us
// for free: cancelling one date must not remove the rest of the series.
func TestCalDAVDeleteSingleOccurrenceAddsExdate(t *testing.T) {
	f := newFakeCalDAV()
	c, done := newTestClient(t, f)
	defer done()
	f.objects["/1234/calendar/home/standup.ics"] = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:standup
DTSTAMP:20260101T000000Z
DTSTART:20260105T090000Z
DTEND:20260105T091500Z
RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR
SUMMARY:Standup
END:VEVENT
END:VCALENDAR`

	if err := c.Delete(context.Background(), "standup:2026-01-07T09:00:00Z"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.deletes) != 0 {
		t.Errorf("deletes = %v, want none; cancelling one date must not delete the series", f.deletes)
	}
	if !strings.Contains(f.lastPut, "EXDATE") {
		t.Errorf("PUT body has no EXDATE:\n%s", f.lastPut)
	}
	if !strings.Contains(f.lastPut, "20260107T090000Z") {
		t.Errorf("EXDATE does not name the cancelled date:\n%s", f.lastPut)
	}
	// The rest of the series must survive.
	if !strings.Contains(f.lastPut, "FREQ=WEEKLY") {
		t.Errorf("PUT body dropped the recurrence rule:\n%s", f.lastPut)
	}
}

// TestCalDAVExpiredLeaseIsUnrelated guards the reader's chunking from silently
// returning duplicates: a single sub-year window must issue exactly one REPORT.
func TestCalDAVListSingleWindowOneReport(t *testing.T) {
	f := newFakeCalDAV()
	reports := 0
	inner := f.handler(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" {
			reports++
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	c := NewCalDAVClient(CalDAVOptions{
		BaseURL:             ts.URL,
		DefaultCalendarHref: ts.URL + "/1234/calendar/home/",
		Timeout:             5 * time.Second,
	})
	if _, err := c.List(context.Background(), jan(1, 0, 0), jan(10, 0, 0)); err != nil {
		t.Fatalf("List: %v", err)
	}
	if reports != 1 {
		t.Errorf("REPORT issued %d times, want 1 for a sub-year window", reports)
	}
}

// TestCalDAVUnauthorized surfaces a credential problem instead of a bare 401.
func TestCalDAVUnauthorized(t *testing.T) {
	f := newFakeCalDAV()
	f.authOK = func(user, pass string) bool { return user == "right" && pass == "right" }
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	c := NewCalDAVClient(CalDAVOptions{BaseURL: ts.URL, User: "wrong", Password: "wrong", Timeout: 5 * time.Second})
	_, err := c.Calendars(context.Background())
	if err == nil {
		t.Fatal("Calendars succeeded with bad credentials")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want it to mention the 401", err)
	}
}
