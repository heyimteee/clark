package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type calendarEvent struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Location string `json:"location,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

func handleCalendarList(w http.ResponseWriter, r *http.Request) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	from, _ := time.Parse(time.RFC3339, fromStr)
	to, _ := time.Parse(time.RFC3339, toStr)
	if from.IsZero() {
		from = time.Now()
	}
	if to.IsZero() {
		to = from.Add(7 * 24 * time.Hour)
	}
	// Language-agnostic: build dates from current date's components to avoid
	// parsing the English literal "Monday, January 1, 2001 at 12:00:00 AM"
	// which fails on Indonesian locale (Senin, 1 Januari ... pukul ...).
	fromUnix := from.Unix()
	toUnix := to.Unix()
	script := fmt.Sprintf(`
		set epoch to current date
		set year of epoch to 2001
		set month of epoch to 1
		set day of epoch to 1
		set hours of epoch to 0
		set minutes of epoch to 0
		set seconds of epoch to 0
		set fromDate to epoch + (%d - 978307200)
		set toDate to epoch + (%d - 978307200)
		set out to ""
		tell application "Calendar"
			repeat with c in calendars
				repeat with e in (every event of c whose start date >= fromDate and start date <= toDate)
					set out to out & (summary of e) & "||" & ((start date of e) as «class isot» as string) & "||" & ((end date of e) as «class isot» as string) & "||" & (location of e as string) & "||" & (description of e as string) & "||" & (uid of e as string) & "\n"
				end repeat
			end repeat
		end tell
		return out
	`, fromUnix, toUnix)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("calendar list failed: %s: %s", err, string(out)), http.StatusInternalServerError)
		return
	}
	events := parseCalendarListOutput(string(out))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"events": events})
}

func parseCalendarListOutput(out string) []calendarEvent {
	var events []calendarEvent
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "||")
		if len(parts) < 3 {
			continue
		}
		title := strings.TrimSpace(parts[0])
		startStr := strings.TrimSpace(parts[1])
		endStr := strings.TrimSpace(parts[2])
		loc := ""
		notes := ""
		uid := ""
		if len(parts) > 3 {
			loc = strings.TrimSpace(parts[3])
		}
		if len(parts) > 4 {
			notes = strings.TrimSpace(parts[4])
		}
		if len(parts) > 5 {
			uid = strings.TrimSpace(parts[5])
		}
		start, _ := time.Parse("2006-01-02T15:04:05Z07:00", startStr)
		if start.IsZero() {
			start, _ = time.Parse(time.RFC3339, startStr)
		}
		if start.IsZero() {
			start, _ = time.Parse("Monday, January 2, 2006 at 3:04:05 PM", startStr)
		}
		end, _ := time.Parse("2006-01-02T15:04:05Z07:00", endStr)
		if end.IsZero() {
			end, _ = time.Parse(time.RFC3339, endStr)
		}
		if end.IsZero() {
			end, _ = time.Parse("Monday, January 2, 2006 at 3:04:05 PM", endStr)
		}
		id := title + start.Format("20060102T150405")
		if uid != "" {
			id = uid
		}
		events = append(events, calendarEvent{
			ID:       id,
			Title:    title,
			Start:    start.Format(time.RFC3339),
			End:      end.Format(time.RFC3339),
			Location: loc,
			Notes:    notes,
		})
	}
	return events
}

func handleCalendarCreate(w http.ResponseWriter, r *http.Request) {
	var e calendarEvent
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	start, _ := time.Parse(time.RFC3339, e.Start)
	end, _ := time.Parse(time.RFC3339, e.End)
	// Use epoch math for locale-agnostic dates and pick the first writable calendar
	script := fmt.Sprintf(`
		set epoch to current date
		set year of epoch to 2001
		set month of epoch to 1
		set day of epoch to 1
		set hours of epoch to 0
		set minutes of epoch to 0
		set seconds of epoch to 0
		set startDate to epoch + (%d - 978307200)
		set endDate to epoch + (%d - 978307200)
		tell application "Calendar"
			set targetCal to first calendar whose writable is true
			if targetCal is missing value then set targetCal to first calendar
			tell targetCal
				make new event at end with properties {summary:"%s", start date:startDate, end date:endDate, location:"%s", description:"%s"}
			end tell
		end tell
	`, start.Unix(), end.Unix(), escapeAppleScript(e.Title), escapeAppleScript(e.Location), escapeAppleScript(e.Notes))
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("calendar create failed: %s: %s", err, string(out)), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": e.Title + start.Format("20060102T150405")})
}

func handleCalendarDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/calendars/events/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	script := fmt.Sprintf(`
		tell application "Calendar"
			repeat with c in calendars
				repeat with e in (every event of c)
					if (uid of e as string) is "%s" or (summary of e) is "%s" then
						delete e
					end if
				end repeat
			end repeat
		end tell
	`, escapeAppleScript(id), escapeAppleScript(id))
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("delete failed: %s: %s", err, string(out)), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func escapeAppleScript(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}
