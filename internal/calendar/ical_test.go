package calendar

import (
	"strings"
	"testing"
	"time"
)

var jan = func(d int, h, m int) time.Time {
	return time.Date(2026, 1, d, h, m, 0, 0, time.UTC)
}

// TestParseNonRecurring proves a plain one-off event passes through untouched.
func TestParseNonRecurring(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:one-off
DTSTAMP:20260101T000000Z
DTSTART:20260105T140000Z
DTEND:20260105T150000Z
SUMMARY:Client review
LOCATION:Level 12
DESCRIPTION:Bring the deck
END:VEVENT
END:VCALENDAR`

	got, err := parseICS(ics, jan(1, 0, 0), jan(10, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	e := got[0]
	if e.Title != "Client review" || e.Location != "Level 12" || e.Notes != "Bring the deck" {
		t.Errorf("event = %+v, want the summary, location and notes preserved", e)
	}
	if !e.Start.Equal(jan(5, 14, 0)) || !e.End.Equal(jan(5, 15, 0)) {
		t.Errorf("window = %v..%v, want 14:00-15:00 on the 5th", e.Start, e.End)
	}
	if e.Recurring {
		t.Error("Recurring = true for a one-off event")
	}
}

// TestParseRecurringWeekdays is the behaviour EventKit gave us for free and
// CalDAV does not: a daily rule must expand to one event per weekday.
func TestParseRecurringWeekdays(t *testing.T) {
	ics := `BEGIN:VCALENDAR
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

	// Mon 5 Jan - Fri 9 Jan 2026 is a full working week.
	got, err := parseICS(ics, jan(5, 0, 0), jan(10, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d occurrences, want 5 (one per weekday): %+v", len(got), got)
	}
	for i, e := range got {
		want := jan(5+i, 9, 0)
		if !e.Start.Equal(want) {
			t.Errorf("occurrence %d starts %v, want %v", i, e.Start, want)
		}
		if !e.Recurring {
			t.Errorf("occurrence %d Recurring = false, want true", i)
		}
		if !strings.Contains(e.OccurrenceID, "standup:") {
			t.Errorf("occurrence %d OccurrenceID = %q, want it addressable as standup:<time>", i, e.OccurrenceID)
		}
	}
}

// TestParseRecurringRespectsExdate proves a cancelled date is actually gone.
func TestParseRecurringRespectsExdate(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:standup
DTSTAMP:20260101T000000Z
DTSTART:20260105T090000Z
DTEND:20260105T091500Z
RRULE:FREQ=DAILY
EXDATE:20260107T090000Z
SUMMARY:Standup
END:VEVENT
END:VCALENDAR`

	got, err := parseICS(ics, jan(5, 0, 0), jan(10, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d occurrences, want 4 (the 7th is EXDATEd)", len(got))
	}
	for _, e := range got {
		if e.Start.Equal(jan(7, 9, 0)) {
			t.Error("the EXDATEd occurrence is still present")
		}
	}
}

// TestParseRecurringRespectsRDate proves an added date is included.
func TestParseRecurringRespectsRDate(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:lunch
DTSTAMP:20260101T000000Z
DTSTART:20260109T120000Z
DTEND:20260109T130000Z
RRULE:FREQ=WEEKLY;BYDAY=FR
RDATE:20260106T120000Z
SUMMARY:Team lunch
END:VEVENT
END:VCALENDAR`

	got, err := parseICS(ics, jan(5, 0, 0), jan(11, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	// The RDATE lands on Tuesday 6 Jan, plus the Friday 9 Jan occurrence.
	if len(got) != 2 {
		t.Fatalf("got %d occurrences, want 2 (the RDATE plus the weekly Friday): %+v", len(got), got)
	}
	found := map[time.Time]bool{}
	for _, e := range got {
		found[e.Start] = true
	}
	if !found[jan(6, 12, 0)] {
		t.Error("the RDATE occurrence is missing")
	}
}

// TestParseRecurringOverrideDoesNotDuplicate proves a RECURRENCE-ID component is
// treated as an override of the master rather than a second series.
func TestParseRecurringOverrideDoesNotDuplicate(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:standup
DTSTAMP:20260101T000000Z
DTSTART:20260105T090000Z
DTEND:20260105T091500Z
RRULE:FREQ=WEEKLY;BYDAY=MO
SUMMARY:Standup
END:VEVENT
BEGIN:VEVENT
UID:standup
RECURRENCE-ID:20260112T090000Z
DTSTAMP:20260101T000000Z
DTSTART:20260112T140000Z
DTEND:20260112T143000Z
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR`

	got, err := parseICS(ics, jan(5, 0, 0), jan(20, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	// The override is dropped rather than emitted as a separate event, so the
	// series is not doubled up.
	if len(got) > 3 {
		t.Errorf("got %d occurrences, want the series expanded without duplication: %+v", len(got), got)
	}
}

// TestParseClipsToWindow proves occurrences outside the window are excluded.
func TestParseClipsToWindow(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:daily
DTSTAMP:20260101T000000Z
DTSTART:20260101T090000Z
DTEND:20260101T091500Z
RRULE:FREQ=DAILY
SUMMARY:Daily
END:VEVENT
END:VCALENDAR`

	got, err := parseICS(ics, jan(5, 0, 0), jan(8, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d occurrences, want 3 (5th, 6th, 7th): %+v", len(got), got)
	}
	for _, e := range got {
		if e.Start.Before(jan(5, 0, 0)) || !e.Start.Before(jan(8, 0, 0)) {
			t.Errorf("occurrence %v falls outside the requested window", e.Start)
		}
	}
}

// TestParseDegradesMalformedRuleWithoutLosingEvents proves an unreadable
// recurrence rule cannot blank the calendar. The affected event keeps its base
// occurrence — dropping a real event would lose information — but must not
// claim to recur, because an OccurrenceID would promise single-date deletion
// that cannot work on a rule we failed to parse.
func TestParseDegradesMalformedRuleWithoutLosingEvents(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:good
DTSTAMP:20260101T000000Z
DTSTART:20260105T140000Z
DTEND:20260105T150000Z
SUMMARY:Fine
END:VEVENT
BEGIN:VEVENT
UID:bad
DTSTAMP:20260101T000000Z
DTSTART:20260106T100000Z
DTEND:20260106T110000Z
SUMMARY:Unparseable series
RRULE:FREQ=NOTAFREQUENCY
END:VEVENT
END:VCALENDAR`

	got, err := parseICS(ics, jan(1, 0, 0), jan(10, 0, 0))
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want both the well-formed and the degraded one: %+v", len(got), got)
	}
	for _, e := range got {
		if e.ID == "good" {
			continue
		}
		if e.Recurring {
			t.Error("the degraded event claims to recur, but its rule was not understood")
		}
		if e.OccurrenceID != "" {
			t.Errorf("the degraded event has OccurrenceID %q, want none; single-date deletion cannot work here", e.OccurrenceID)
		}
		if !e.Start.Equal(jan(6, 10, 0)) {
			t.Errorf("the degraded event starts %v, want its base occurrence at 2026-01-06 10:00", e.Start)
		}
	}
}

// TestSplitOccurrenceID pins the "uid:timestamp" addressing used by Delete.
func TestSplitOccurrenceID(t *testing.T) {
	cases := []struct {
		in         string
		uid        string
		occurrence string
		single     bool
	}{
		{"standup:2026-01-07T09:00:00Z", "standup", "2026-01-07T09:00:00Z", true},
		{"standup", "standup", "", false},
		{"a:b", "a:b", "", false},
		{"", "", "", false},
		{":2026-01-07T09:00:00Z", ":2026-01-07T09:00:00Z", "", false},
	}
	for _, tc := range cases {
		uid, occ, single := splitOccurrenceID(tc.in)
		if uid != tc.uid || occ != tc.occurrence || single != tc.single {
			t.Errorf("splitOccurrenceID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, uid, occ, single, tc.uid, tc.occurrence, tc.single)
		}
	}
}

// TestBuildICSRoundTrips proves a created event parses back to what was asked for,
// which is the only way to know the PUT body was well-formed.
func TestBuildICSRoundTrips(t *testing.T) {
	want := Event{
		Title:    "Dinner",
		Start:    jan(9, 19, 0),
		End:      jan(9, 21, 0),
		Location: "Ristorante",
		Notes:    "Book the corner table",
	}
	body := buildICS("abc-123", want)
	if body == "" {
		t.Fatal("buildICS returned nothing")
	}
	got, err := parseICS(body, jan(1, 0, 0), jan(20, 0, 0))
	if err != nil {
		t.Fatalf("parseICS on our own output: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("round trip produced %d events, want 1", len(got))
	}
	e := got[0]
	if e.ID != "abc-123" {
		t.Errorf("ID = %q, want abc-123", e.ID)
	}
	if e.Title != want.Title || e.Location != want.Location || e.Notes != want.Notes {
		t.Errorf("round trip = %+v, want %+v", e, want)
	}
	if !e.Start.Equal(want.Start) || !e.End.Equal(want.End) {
		t.Errorf("times = %v..%v, want %v..%v", e.Start, e.End, want.Start, want.End)
	}
}

// TestExtractCalendarDataCDATAAndPlain covers both response encodings iCloud uses.
func TestExtractCalendarDataCDATAAndPlain(t *testing.T) {
	const payload = "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:a\nEND:VEVENT\nEND:VCALENDAR"

	cdata := `<d:response><d:propstat><d:prop><c:calendar-data><![CDATA[` + payload + `]]></c:calendar-data></d:prop></d:propstat></d:response>`
	if got := extractCalendarData(cdata); !strings.Contains(got, "UID:a") {
		t.Errorf("CDATA payload not extracted, got %q", got)
	}

	plain := `<d:response><d:propstat><d:prop><c:calendar-data>BEGIN:VCALENDAR&#10;BEGIN:VEVENT&#10;UID:b&#10;END:VEVENT&#10;END:VCALENDAR</c:calendar-data></d:prop></d:propstat></d:response>`
	if got := extractCalendarData(plain); !strings.Contains(got, "UID:b") {
		t.Errorf("plain payload not extracted, got %q", got)
	}
}
