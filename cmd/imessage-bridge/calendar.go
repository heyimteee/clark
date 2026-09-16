package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type calendarEvent struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"allDay,omitempty"`
	Location string `json:"location,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

// Calendar access goes through EventKit via JXA (osascript -l JavaScript)
// instead of the Calendar.app AppleScript dictionary. The AppleScript
// `every event ... whose start date` queries silently return incomplete
// or empty result sets on modern macOS; EventKit is the same framework the
// Calendar UI uses and expands recurring occurrences correctly. All dates
// are passed as absolute Unix epochs, which removes both the locale
// dependency and the UTC-epoch-constant timezone shift of the old script.

const jxaHelpers = `
function toStr(v) {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  try { if (v.isNil()) return '' } catch (e) {}
  try { const s = ObjC.unwrap(v); if (s !== undefined && s !== null) return String(s) } catch (e) {}
  return String(v)
}
`

const calendarListJXA = `ObjC.import('Foundation')
ObjC.import('EventKit')
` + jxaHelpers + `
function run() {
  const params = __PARAMS__
  try {
    const store = $.EKEventStore.alloc.init
    const nsStart = $.NSDate.alloc.initWithTimeIntervalSince1970(params.from)
    const nsEnd = $.NSDate.alloc.initWithTimeIntervalSince1970(params.to)
    const pred = store.predicateForEventsWithStartDateEndDateCalendars(nsStart, nsEnd, $())
    const evs = ObjC.unwrap(store.eventsMatchingPredicate(pred)) || []
    const events = []
    for (const e of evs) {
      let cal = ''
      try { if (e.calendar && !e.calendar.isNil()) cal = toStr(e.calendar.title) } catch (x) {}
      events.push({
        id: toStr(e.eventIdentifier) || toStr(e.calendarItemExternalIdentifier),
        title: toStr(e.title),
        start: e.startDate.timeIntervalSince1970,
        end: e.endDate.timeIntervalSince1970,
        allDay: !!e.allDay,
        location: toStr(e.location),
        notes: toStr(e.notes),
        calendar: cal,
      })
    }
    return JSON.stringify({events: events})
  } catch (err) {
    return JSON.stringify({error: String(err)})
  }
}
`

const calendarCreateJXA = `ObjC.import('Foundation')
ObjC.import('EventKit')
` + jxaHelpers + `
function run() {
  const params = __PARAMS__
  try {
    const store = $.EKEventStore.alloc.init
    const e = $.EKEvent.eventWithEventStore(store)
    e.title = params.title
    if (params.location) e.location = params.location
    if (params.notes) e.notes = params.notes
    e.startDate = $.NSDate.alloc.initWithTimeIntervalSince1970(params.start)
    e.endDate = $.NSDate.alloc.initWithTimeIntervalSince1970(params.end)
    const cal = store.defaultCalendarForNewEvents
    if (!cal || cal.isNil()) return JSON.stringify({error: 'no default calendar configured'})
    e.calendar = cal
    const err = Ref()
    const ok = store.saveEventSpanError(e, 0, err)
    if (!ok) {
      let msg = 'EventKit refused to save the event'
      try { if (err[0] && !err[0].isNil()) msg = toStr(err[0].localizedDescription) } catch (x) {}
      return JSON.stringify({error: msg})
    }
    return JSON.stringify({id: toStr(e.eventIdentifier) || toStr(e.calendarItemExternalIdentifier)})
  } catch (err) {
    return JSON.stringify({error: String(err)})
  }
}
`

const calendarDeleteJXA = `ObjC.import('Foundation')
ObjC.import('EventKit')
` + jxaHelpers + `
function run() {
  const params = __PARAMS__
  try {
    const store = $.EKEventStore.alloc.init
    const targets = []
    if (params.id) {
      try {
        const e = store.eventWithIdentifier(params.id)
        if (e && !e.isNil()) targets.push(e)
      } catch (x) {}
    }
    if (targets.length === 0) {
      const nsStart = $.NSDate.alloc.initWithTimeIntervalSince1970(params.from)
      const nsEnd = $.NSDate.alloc.initWithTimeIntervalSince1970(params.to)
      const pred = store.predicateForEventsWithStartDateEndDateCalendars(nsStart, nsEnd, $())
      const evs = ObjC.unwrap(store.eventsMatchingPredicate(pred)) || []
      for (const e of evs) {
        if (params.title && toStr(e.title) === params.title) { targets.push(e); continue }
        if (params.id && (toStr(e.eventIdentifier) === params.id || toStr(e.calendarItemExternalIdentifier) === params.id)) targets.push(e)
      }
    }
    if (targets.length === 0) return JSON.stringify({deleted: 0, error: 'event not found'})
    const err = Ref()
    let deleted = 0
    for (const e of targets) {
      if (store.removeEventSpanError(e, 0, err)) deleted++
    }
    return JSON.stringify({deleted: deleted})
  } catch (err) {
    return JSON.stringify({error: String(err)})
  }
}
`

// calendarAccessSentinel prefixes every JXA-side access failure so Go can
// tell "macOS refused consent" apart from query errors.
const calendarAccessSentinel = "CALENDAR_ACCESS_"

// withCalendarAuth inserts the consent-ensure block after the EventKit store
// creation. needRead selects the access bar: listing needs Full Access while
// create/delete also accept Write-Only (macOS 14+ status 4).
func withCalendarAuth(script string, needRead bool) string {
	need := "false"
	if needRead {
		need = "true"
	}
	snippet := strings.Replace(calendarAuthJS, "__NEED_READ__", need, 1)
	return strings.Replace(script, "const store = $.EKEventStore.alloc.init",
		"const store = $.EKEventStore.alloc.init\n"+snippet, 1)
}

const calendarAuthJS = `
  var __calSt = Number($.EKEventStore.authorizationStatusForEntityType(0));
  var __calAuth = (function() {
    if (__calSt === 3) return 'ok';
    if (__calSt === 4) return __NEED_READ__ ? 'write-only' : 'ok';
    if (__calSt !== 0) return 'denied';
    var done = false, granted = false;
    try {
      var cb = function(g, e) { granted = !!g; done = true; };
      try { store.requestFullAccessToEventsWithCompletion(cb); }
      catch (x) { store.requestAccessToEntityTypeCompletion(0, cb); }
    } catch (y) { return 'denied'; }
    var loop = $.NSRunLoop.currentRunLoop;
    var end = Date.now() + 30000;
    while (!done && Date.now() < end) { loop.runUntilDate($.NSDate.dateWithTimeIntervalSinceNow(0.2)); }
    if (!done) return 'timeout';
    return granted ? 'ok' : 'denied';
  })();
  if (__calAuth !== 'ok') return JSON.stringify({error: 'CALENDAR_ACCESS_' + __calAuth});
`

// mapCalendarAccessError translates a JXA access sentinel into a human
// remediation message. ok is false for ordinary query errors.
func mapCalendarAccessError(payloadErr string) (msg string, ok bool) {
	code, found := strings.CutPrefix(payloadErr, calendarAccessSentinel)
	if !found {
		return "", false
	}
	switch code {
	case "write-only":
		return "Calendar access is Write-Only: listing events needs Full Access — change imessage-bridge to Full Access in System Settings → Privacy & Security → Calendars", true
	case "timeout":
		return "calendar consent timed out after 30s: approve the access prompt on the Mac, then ask again", true
	default:
		return "calendar access denied for imessage-bridge: choose Allow in System Settings → Privacy & Security → Calendars", true
	}
}

// calendarAuthStatus reports the current EventKit authorization for /status:
// authorized, write_only, not_determined, restricted, denied, or unknown when
// the probe itself fails.
func calendarAuthStatus() string {
	out, err := runJXA(map[string]any{}, `ObjC.import('EventKit')
function run() { return JSON.stringify({status: Number($.EKEventStore.authorizationStatusForEntityType(0))}); }`)
	if err != nil {
		return "unknown"
	}
	var payload struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		return "unknown"
	}
	switch payload.Status {
	case 3:
		return "authorized"
	case 4:
		return "write_only"
	case 0:
		return "not_determined"
	case 1:
		return "restricted"
	case 2:
		return "denied"
	default:
		return "unknown"
	}
}

// noteCalendarDenied opens the Calendars Settings pane once per episode when
// the user must act there (denied or write-only; not on timeout where the
// system prompt may still be on screen).
func noteCalendarDenied(code string) {
	if code == "timeout" {
		return
	}
	maybeOpenSettingsPane(stateDirForMarkers(), settingsPaneCalendar)
}

// runJXA embeds params as JSON into the script (injection-safe), writes it to
// a temp file and executes osascript. It returns stdout, with stderr folded
// into the error for diagnostics.
func runJXA(params any, script string) (string, error) {
	blob, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode params: %w", err)
	}
	js := strings.Replace(script, "__PARAMS__", string(blob), 1)

	f, err := os.CreateTemp("", "clark-calendar-*.js")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(js); err != nil {
		f.Close()
		return "", fmt.Errorf("write temp: %w", err)
	}
	f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", f.Name())
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func handleCalendarList(w http.ResponseWriter, r *http.Request) {
	from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if from.IsZero() {
		from = time.Now()
	}
	if to.IsZero() {
		to = from.Add(7 * 24 * time.Hour)
	}

	out, err := runJXA(map[string]int64{"from": from.Unix(), "to": to.Unix()}, withCalendarAuth(calendarListJXA, true))
	if err != nil {
		http.Error(w, fmt.Sprintf("calendar list failed: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	var payload struct {
		Events []struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			Start    float64 `json:"start"`
			End      float64 `json:"end"`
			AllDay   bool    `json:"allDay"`
			Location string  `json:"location"`
			Notes    string  `json:"notes"`
		} `json:"events"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		http.Error(w, fmt.Sprintf("calendar list: cannot parse output: %v: %q", err, out), http.StatusInternalServerError)
		return
	}
	if payload.Error != "" {
		if msg, ok := mapCalendarAccessError(payload.Error); ok {
			noteCalendarDenied(strings.TrimPrefix(payload.Error, calendarAccessSentinel))
			http.Error(w, "calendar list failed: "+msg, http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "calendar list failed: "+payload.Error, http.StatusInternalServerError)
		return
	}
	events := make([]calendarEvent, 0, len(payload.Events))
	for _, e := range payload.Events {
		events = append(events, calendarEvent{
			ID:       e.ID,
			Title:    e.Title,
			Start:    time.Unix(int64(e.Start), 0).Format(time.RFC3339),
			End:      time.Unix(int64(e.End), 0).Format(time.RFC3339),
			AllDay:   e.AllDay,
			Location: e.Location,
			Notes:    e.Notes,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"events": events})
	clearSettingsPaneMarker(stateDirForMarkers(), settingsPaneCalendar)
}

func handleCalendarCreate(w http.ResponseWriter, r *http.Request) {
	var e calendarEvent
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	start, err := time.Parse(time.RFC3339, e.Start)
	if err != nil {
		http.Error(w, "invalid start time", http.StatusBadRequest)
		return
	}
	end, err := time.Parse(time.RFC3339, e.End)
	if err != nil {
		http.Error(w, "invalid end time", http.StatusBadRequest)
		return
	}

	out, err := runJXA(map[string]any{
		"title":    e.Title,
		"start":    start.Unix(),
		"end":      end.Unix(),
		"location": e.Location,
		"notes":    e.Notes,
	}, withCalendarAuth(calendarCreateJXA, false))
	if err != nil {
		http.Error(w, fmt.Sprintf("calendar create failed: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	var payload struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		http.Error(w, fmt.Sprintf("calendar create: cannot parse output: %v: %q", err, out), http.StatusInternalServerError)
		return
	}
	if payload.Error != "" {
		if msg, ok := mapCalendarAccessError(payload.Error); ok {
			noteCalendarDenied(strings.TrimPrefix(payload.Error, calendarAccessSentinel))
			http.Error(w, "calendar create failed: "+msg, http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "calendar create failed: "+payload.Error, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": payload.ID})
	clearSettingsPaneMarker(stateDirForMarkers(), settingsPaneCalendar)
}

func handleCalendarDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/calendars/events/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	now := time.Now()
	out, err := runJXA(map[string]any{
		"id":    id,
		"title": id,
		"from":  now.Add(-90 * 24 * time.Hour).Unix(),
		"to":    now.Add(270 * 24 * time.Hour).Unix(),
	}, withCalendarAuth(calendarDeleteJXA, false))
	if err != nil {
		http.Error(w, fmt.Sprintf("calendar delete failed: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	var payload struct {
		Deleted int    `json:"deleted"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		http.Error(w, fmt.Sprintf("calendar delete: cannot parse output: %v: %q", err, out), http.StatusInternalServerError)
		return
	}
	if msg, ok := mapCalendarAccessError(payload.Error); ok {
		noteCalendarDenied(strings.TrimPrefix(payload.Error, calendarAccessSentinel))
		http.Error(w, "calendar delete failed: "+msg, http.StatusServiceUnavailable)
		return
	}
	if payload.Error != "" || payload.Deleted == 0 {
		http.Error(w, "calendar delete failed: "+payload.Error, http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	clearSettingsPaneMarker(stateDirForMarkers(), settingsPaneCalendar)
}
