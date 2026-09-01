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
	_ = from
	_ = to
	json.NewEncoder(w).Encode(map[string]any{"events": []calendarEvent{}})
}

func handleCalendarCreate(w http.ResponseWriter, r *http.Request) {
	var e calendarEvent
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	start, _ := time.Parse(time.RFC3339, e.Start)
	end, _ := time.Parse(time.RFC3339, e.End)
	script := fmt.Sprintf(`
		tell application "Calendar"
			tell calendar "Calendar"
				make new event at end with properties {summary:"%s", start date:date "%s", end date:date "%s", location:"%s", description:"%s"}
			end tell
		end tell
	`, escapeAppleScript(e.Title), start.Format("Monday, January 2, 2006 at 3:04:00 PM"), end.Format("Monday, January 2, 2006 at 3:04:00 PM"), escapeAppleScript(e.Location), escapeAppleScript(e.Notes))
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
					if (summary of e) is "%s" then
						delete e
					end if
				end repeat
			end repeat
		end tell
	`, escapeAppleScript(id))
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		http.Error(w, fmt.Sprintf("delete failed: %s: %s", err, string(out)), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func escapeAppleScript(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}
