package calendar

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	ics "github.com/arran4/golang-ical"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/teambition/rrule-go"
)

// icalUID builds a unique identifier for a new event.
func icalUID() string {
	return fmt.Sprintf("%d-clark", time.Now().UTC().UnixNano())
}

// List returns every event occurrence overlapping [from, to).
//
// iCloud caps a single query at one year, so a wider window is chunked. The
// 1-year boundary is applied conservatively (minus a day) so a request never
// straddles the cap and gets rejected outright.
func (c *CalDAVClient) List(ctx context.Context, from, to time.Time) ([]Event, error) {
	if err := c.discover(ctx); err != nil {
		return nil, err
	}
	target, err := c.writeCalendar()
	if err != nil {
		return nil, err
	}

	var out []Event
	const maxWindow = 364 * 24 * time.Hour

	chunkStart, chunkEnd := from.UTC(), to.UTC()
	if chunkEnd.Sub(chunkStart) > maxWindow {
		chunkEnd = chunkStart.Add(maxWindow)
	}

	for chunkStart.Before(to.UTC()) {
		body := fmt.Sprintf(calendarQueryBody,
			chunkStart.Format("20060102T150405Z"),
			chunkEnd.Format("20060102T150405Z"))

		resp, err := c.do(ctx, "REPORT", target, "application/xml; charset=utf-8", body)
		if err != nil {
			return nil, fmt.Errorf("calendar list failed: %w", err)
		}
		raw, err := readBody(resp)
		if err != nil {
			return nil, fmt.Errorf("calendar list unreadable: %w", err)
		}
		if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("calendar list returned %s", resp.Status)
		}

		icsText := extractCalendarData(string(raw))
		events, err := parseICS(icsText, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)

		if !chunkEnd.Before(to.UTC()) {
			break
		}
		chunkStart = chunkEnd
		chunkEnd = chunkStart.Add(maxWindow)
	}
	return out, nil
}

// extractCalendarData pulls every calendar-data payload out of a multistatus
// body. Responses may be CDATA wrapped or plain, and one response can carry more
// than one VCALENDAR, so both forms are collected.
func extractCalendarData(body string) string {
	var b strings.Builder
	for _, chunk := range splitElements(body, "<c:calendar-data", "</c:calendar-data>") {
		inner := chunk
		if cd := strings.Index(inner, "]]>"); cd >= 0 {
			// CDATA form: everything between the opening bracket and the
			// terminator, verbatim. The `<![CDATA[` marker itself must be
			// dropped or the iCalendar parser chokes on the leading '['.
			inner = inner[strings.Index(inner, ">")+1 : cd]
			inner = strings.TrimPrefix(inner, "<![CDATA[")
		} else {
			inner = unescapeXML(stripElement(inner, "c:calendar-data"))
		}
		if strings.TrimSpace(inner) == "" {
			continue
		}
		b.WriteString(inner)
		if !strings.HasSuffix(inner, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// parseICS converts iCalendar text into Events, expanding recurrences.
//
// Grouping by UID matters: a recurring master and its RECURRENCE-ID overrides
// arrive in the same VCALENDAR, and expanding them independently would produce
// duplicate occurrences for every overridden date.
func parseICS(text string, from, to time.Time) ([]Event, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	cal, err := ics.ParseCalendar(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("calendar data could not be parsed: %w", err)
	}

	masters := map[string]*ics.VEvent{}
	var order []string
	for _, ev := range cal.Events() {
		uid := uidOf(ev)
		if uid == "" {
			continue
		}
		if _, seen := masters[uid]; !seen {
			order = append(order, uid)
		}
		// A component carrying a RECURRENCE-ID is an override of the master, so
		// the master must win to keep the series authoritative.
		if ev.GetProperty(ics.ComponentPropertyRecurrenceId) != nil {
			continue
		}
		masters[uid] = ev
	}

	var out []Event
	for _, uid := range order {
		// One malformed series must not blank the whole calendar.
		if events, err := expandEvent(masters[uid], from, to); err == nil {
			out = append(out, events...)
		}
	}
	return out, nil
}

// uidOf reads a component's UID.
func uidOf(ev *ics.VEvent) string {
	p := ev.GetProperty(ics.ComponentPropertyUniqueId)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.Value)
}

// expandEvent turns one VEVENT — recurring or not — into concrete occurrences.
func expandEvent(ev *ics.VEvent, from, to time.Time) ([]Event, error) {
	if ev == nil {
		return nil, nil
	}
	start, err := ev.GetStartAt()
	if err != nil {
		return nil, fmt.Errorf("event has no usable start: %w", err)
	}
	end, err := ev.GetEndAt()
	if err != nil || end.Before(start) {
		end = start.Add(time.Hour)
	}
	duration := end.Sub(start)

	rules := rawRRulesOrNil(ev)
	if len(rules) == 0 {
		e := toEvent(ev, uidOf(ev), start, duration)
		if e.Start.Before(to) && e.End.After(from) {
			return []Event{e}, nil
		}
		return nil, nil
	}

	set, err := buildRRuleSet(ev, start, rules)
	if err != nil {
		// The series shape is not understood. Emitting the base occurrence is
		// more useful than dropping the event, but it must not claim to recur:
		// an OccurrenceID would promise single-date deletion that cannot work
		// on a rule we failed to parse.
		logOnce("calendar: falling back to a single occurrence for %q: %v", uidOf(ev), err)
		e := toEvent(ev, uidOf(ev), start, duration)
		e.Recurring = false
		e.OccurrenceID = ""
		if e.Start.Before(to) && e.End.After(from) {
			return []Event{e}, nil
		}
		return nil, nil
	}
	// Widen the lower bound by the event duration so an occurrence that starts
	// just before the window but overlaps it is not dropped.
	occurrences := set.Between(from.Add(-duration), to, true)

	out := make([]Event, 0, len(occurrences))
	for _, occ := range occurrences {
		out = append(out, toEvent(ev, uidOf(ev), occ, duration))
	}
	return out, nil
}

// buildRRuleSet assembles the RRULEs plus EXDATE exclusions and RDATE additions
// for a recurring event, so a cancelled or added date is honoured.
//
// The rule is read as raw text rather than reassembled from the parsed struct:
// reconstructing FREQ/INTERVAL/BYDAY/COUNT/UNTIL by hand is exactly the kind of
// detail this code exists to get right.
func buildRRuleSet(ev *ics.VEvent, start time.Time, raw []string) (*rrule.Set, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("event has no recurrence rule")
	}

	set := &rrule.Set{}
	for _, value := range raw {
		rr, err := rrule.StrToRRule(value + ";DTSTART=" + start.Format("20060102T150405Z"))
		if err != nil {
			return nil, fmt.Errorf("recurrence rule %q is not understood: %w", value, err)
		}
		set.RRule(rr)
	}
	if ex, err := ev.GetExDates(); err == nil {
		for _, d := range ex {
			set.ExDate(d)
		}
	}
	if rd, err := ev.GetRDates(); err == nil {
		for _, d := range rd {
			set.RDate(d)
		}
	}
	return set, nil
}

// rawRRules returns the RRULE property values verbatim.
func rawRRules(ev *ics.VEvent) ([]string, error) {
	var out []string
	for _, p := range ev.Properties {
		if p.IANAToken == string(ics.ComponentPropertyRrule) {
			if v := strings.TrimSpace(p.Value); v != "" {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

// toEvent maps a component plus one occurrence start to our Event.
func toEvent(ev *ics.VEvent, uid string, start time.Time, duration time.Duration) Event {
	e := Event{
		ID:        uid,
		Title:     propValue(ev, ics.ComponentPropertySummary),
		Start:     start.UTC(),
		End:       start.Add(duration).UTC(),
		Location:  propValue(ev, ics.ComponentPropertyLocation),
		Notes:     propValue(ev, ics.ComponentPropertyDescription),
		Recurring: len(rawRRulesOrNil(ev)) > 0,
	}
	if start.Location() == time.UTC && start.Hour() == 0 && start.Minute() == 0 &&
		duration%(24*time.Hour) == 0 {
		e.AllDay = true
	}
	if e.Recurring {
		// A per-date address so Delete can cancel exactly one occurrence.
		e.OccurrenceID = uid + ":" + e.Start.Format(time.RFC3339)
	}
	return e
}

// rawRRulesOrNil reports whether this event recurs.
func rawRRulesOrNil(ev *ics.VEvent) []string {
	r, _ := rawRRules(ev)
	return r
}

// logOnce records a degraded-parse notice without a logging dependency in this
// package. Failures here are per-event and self-limiting, so a one-line note is
// the right amount of noise.
func logOnce(format string, args ...any) {
	if warned.CompareAndSwap(false, true) {
		logging.Log("CALENDAR", logging.SevWarn, "RECUR", fmt.Sprintf(format, args...))
	}
}

// warned keeps a single malformed event from flooding the log on every list.
var warned atomic.Bool

// propValue reads a component property, returning "" when absent.
func propValue(ev *ics.VEvent, prop ics.ComponentProperty) string {
	p := ev.GetProperty(prop)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.Value)
}

// Create adds an event to a target calendar and returns its UID. The
// destination comes from the event's CalendarHref when set (the console picker),
// otherwise from the configured default, otherwise the first collection found.
func (c *CalDAVClient) Create(ctx context.Context, e Event) (string, error) {
	if err := c.discover(ctx); err != nil {
		return "", err
	}
	target, err := c.writeCalendar()
	if href := strings.TrimSpace(e.CalendarHref); err == nil && href != "" {
		target = href
	}
	if err != nil || target == "" {
		return "", fmt.Errorf("no calendar available for writing")
	}

	uid := icalUID()
	href := strings.TrimRight(target, "/") + "/" + icalPathEscape(uid) + ".ics"

	req, err := c.newPutRequest(ctx, href, buildICS(uid, e))
	if err != nil {
		return "", err
	}
	// Never silently overwrite an existing object.
	req.Header.Set("If-None-Match", "*")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("calendar create failed: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("calendar create returned %s", resp.Status)
	}
	return uid, nil
}

// buildICS renders an Event as a single-occurrence VCALENDAR.
func buildICS(uid string, e Event) string {
	cal := ics.NewCalendar()
	cal.SetProductId("-//clark//butler//EN")
	cal.SetVersion("2.0")

	ev := ics.NewEvent(uid)
	ev.SetSummary(e.Title)
	ev.SetDescription(e.Notes)
	ev.SetLocation(e.Location)
	ev.SetDtStampTime(time.Now().UTC())
	ev.SetStartAt(e.Start)
	ev.SetEndAt(e.End)
	cal.AddVEvent(ev)

	var buf strings.Builder
	if err := cal.SerializeTo(&buf); err != nil {
		return ""
	}
	return buf.String()
}

// Delete removes an event. An id of "<uid>:<RFC3339>" cancels a single
// occurrence of a recurring series via EXDATE; a bare uid removes the whole
// series.
func (c *CalDAVClient) Delete(ctx context.Context, id string) error {
	if err := c.discover(ctx); err != nil {
		return err
	}
	uid, occurrence, single := splitOccurrenceID(id)
	if single {
		return c.cancelOccurrence(ctx, uid, occurrence)
	}
	return c.deleteObject(ctx, strings.TrimRight(mustWriteCalendar(c), "/")+"/"+icalPathEscape(uid)+".ics")
}

// splitOccurrenceID separates "uid" from "uid:2026-01-02T09:00:00Z".
//
// A timestamp contains colons of its own, so the separator is found by asking
// which colon leaves a parseable RFC 3339 suffix — not by taking the last one,
// which would split inside the time.
func splitOccurrenceID(id string) (uid, occurrence string, single bool) {
	for i := 0; i < len(id); i++ {
		if id[i] != ':' || i == 0 || i == len(id)-1 {
			continue
		}
		if _, err := time.Parse(time.RFC3339, id[i+1:]); err == nil {
			return id[:i], id[i+1:], true
		}
	}
	return id, "", false
}

// cancelOccurrence adds an EXDATE to the recurring master, which is how CalDAV
// cancels exactly one date without disturbing the rest of the series.
func (c *CalDAVClient) cancelOccurrence(ctx context.Context, uid, occurrence string) error {
	href := strings.TrimRight(mustWriteCalendar(c), "/") + "/" + icalPathEscape(uid) + ".ics"
	body, err := c.get(ctx, href)
	if err != nil {
		return err
	}
	cal, err := ics.ParseCalendar(strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("cannot cancel one occurrence: the event could not be read back: %w", err)
	}
	var master *ics.VEvent
	for _, ev := range cal.Events() {
		if uidOf(ev) == uid {
			master = ev
			break
		}
	}
	if master == nil {
		return fmt.Errorf("cannot cancel one occurrence: event %q not found", uid)
	}
	ts, err := time.Parse(time.RFC3339, occurrence)
	if err != nil {
		return fmt.Errorf("cannot cancel one occurrence: bad occurrence id: %w", err)
	}
	// Seconds are dropped so the EXDATE matches the recurrence expansion exactly.
	master.AddProperty(ics.ComponentPropertyExdate, ts.UTC().Format("20060102T150405Z"))

	var buf strings.Builder
	if err := cal.SerializeTo(&buf); err != nil {
		return fmt.Errorf("cannot cancel one occurrence: %w", err)
	}
	return c.put(ctx, href, buf.String())
}

// deleteObject removes a calendar object outright.
func (c *CalDAVClient) deleteObject(ctx context.Context, href string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, href, nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calendar delete failed: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("calendar delete returned %s", resp.Status)
	}
	return nil
}

// mustWriteCalendar returns the write target, falling back to the first
// discovered collection. Callers have already run discover.
func mustWriteCalendar(c *CalDAVClient) string {
	if c.defaultHref != "" {
		return c.defaultHref
	}
	if len(c.discovered) == 0 {
		return ""
	}
	return c.discovered[0].Href
}

// writeCalendar picks the collection new events are written to.
func (c *CalDAVClient) writeCalendar() (string, error) {
	if h := mustWriteCalendar(c); h != "" {
		return h, nil
	}
	return "", fmt.Errorf("no calendar available for writing")
}
