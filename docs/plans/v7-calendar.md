# v7.0.0 — Calendar support

> Feasibility: **FREE** — three independent free paths exist; recommended path piggybacks the existing Mac `imessage-bridge` (no OAuth, no cloud).

## Objective

Clark can read, create, and delete calendar events. When the calendar shows something planned, Clark proactively asks (WhatsApp + web chat) whether to enter **Protocol Away** — a bundled state: context set to the event, status **ON for everyone** (`SetStatus(true)` clearing per-VIP overrides), thinking **ON**.

## Free paths (ranked)

| Path | Cost | What it requires |
|---|---|---|
| **A — Apple Calendar via Mac bridge (RECOMMENDED)** | Free | Extend `cmd/imessage-bridge` with EventKit REST (4 routes); add `MAC_CALENDAR_URL` env; grant Calendar Automation TCC once. Prior art: `shadowfax92/apple-mcp-api-bridge`, `amargautam/macos-local-mcp-server` (Vapor+EventKit bridge, pure Swift HTTP on `:8080`). `Calendar.app` itself is scriptable via AppleScript `tell application "Calendar" to make new event … end tell` (`developer.apple.com` Calendar Scripting Guide). |
| **B — Google Calendar API** | Free under 1M req/day (`developers.google.com/workspace/calendar/api/guides/quota`, `requestly.com` FAQ) | GCP project + enable `calendar-json.googleapis.com` (no billing for under-threshold). OAuth 2.0 refresh token or Service Account + “share calendar with `…@developer.gserviceaccount.com`” (StackOverflow Q14003203). |
| **C — CalDAV generic (Baikal/Radicale/Nextcloud)** | Free | Self-host `ckulka/baikal:latest` (GPL). Clark side uses `emersion/go-ical` + `emersion/go-webdav`. Config `CALDAV_URL/USER/PASS`. |

**Decision:** ship **A** first (zero secrets, zero Google, inherits the Mac bridge's trusted TCC identity already needed for `chat.db`). Keep `B/C` as interface-compatible fallbacks behind `CALENDAR_PROVIDER=macos|google|caldav` env (default `macos` when `MAC_CALENDAR_URL` set).

## Design

### New interface

```go
// internal/calendar/calendar.go
type Client interface {
  List(ctx context.Context, from, to time.Time) ([]Event, error)
  Create(ctx context.Context, e Event) (id string, err error)
  Delete(ctx context.Context, id string) error
}
type Event struct { ID, CalendarID, Title, Location, Notes string; Start, End time.Time }
```

### Bridge side

Extend `cmd/imessage-bridge/actions.go:17-36` `ActionServer` (already `requireToken` at `37-50`) with:

- `GET /calendars`
- `GET /calendars/:id/events?from=&to=` — `REPORT calendar-query` or `EKEventStore predicateForEvents`
- `POST /calendars/:id/events {title,start,end,location,notes}` — `EKEventStore.save` or AppleScript
- `DELETE /calendars/:id/events/:id`

Protected with same `X-Clark-Bridge-Token` and `validE164`-style input validation (`85-88`).

### In-container client

`internal/calendar/macos.go` — `http.Client{Timeout:10s}` + token header (identical to `app.go:368-393` `macAction` pattern). `google.go` uses `google.golang.org/api/calendar/v3`; `caldav.go` wraps `go-webdav` + `go-ical`. No new system deps.

### Tools (Master-only, reuse `toolParams` at `assistant/tools.go:311-323`)

| Tool | Params | Gate |
|---|---|---|
| `add_calendar_event` | `title*, start*, end*, location, notes` | `masterOnly` |
| `list_calendar_events` | `from, to, limit?` | maybe VIP-readable (share via `defaultVIPGrants`) |
| `delete_calendar_event` | `id*` or `title+date` helper | `masterOnly` |

Prompt auto-injects via `describeTools` into `prompt.md:10-12`; add mapping line at `15`:
`add to calendar / schedule … → add_calendar_event; what's on my calendar → list_calendar_events; cancel my … → delete_calendar_event`.

### Store

Only if caching locally; otherwise pure proxy (no table). If cached, migration in `store.go:130-179`:

```sql
CREATE TABLE IF NOT EXISTS calendar_events(
  id TEXT PRIMARY KEY, calendar_id TEXT, title TEXT,
  start TEXT, end TEXT, location TEXT, notes TEXT,
  source TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### Proactive ask — “Shall I enter Protocol Away?”

A ticker in `internal/app/app.go:Run()` (beside existing `alerts` wiring at `166-183`) lists next-24h events every 15 min. If any exist and the last ask is older than the window, compose a confirmable ask and fan out via `alert.Service.Deliver` (already hits WhatsApp self + iMessage self + `hub.broadcast` + voice/banner per mode):

```go
events, _ := cal.List(ctx, time.Now(), time.Now().Add(24*time.Hour))
if len(events)>0 && since(lastAsk)>24*time.Hour {
  ask := fmt.Sprintf("Sir, you have %d events tomorrow: %s. Shall I enter Protocol Away?",
                     len(events), summarize(events))
  alerts.Deliver(ctx, "calendar", "Protocol Away?", ask)
}
```

User confirms via self-chat (`whatsapp` or `web` `handleSend` `rest.go:355-380`) — new fast-path regex (or tool `enter_protocol_away`) calls a composer:

```go
func (s *Service) EnterProtocolAway(reason string) error {
  if err := s.SetStatus(true); err != nil { return err }   // ON for everyone, wipes vip_status  assistant.go:620-634
  if err := s.SetThinking(true); err != nil { return err } // thinking ON  assistant.go:696-709
  return s.SetContext("Protocol Away — " + reason)          // assistant.go:651-663
}
```

Individual settings already notify via `notifyState` → `web/server.go:118` `hub.broadcast`.

## Files touched

New: `internal/calendar/{calendar.go,macos.go,google.go?,caldav.go?}`, `cmd/imessage-bridge/calendar.go` (or extension to `actions.go`)
Modified: `internal/config/config.go:14-58` (+`CALENDAR_*` vars), `internal/assistant/tools.go:13-264` (+3 tools), `internal/assistant/assistant.go:620-709` (+`EnterProtocolAway`), `internal/assistant/commands.go:372-388` (+ away-confirm regex), `internal/store/store.go:130-179` (optional table), `internal/app/app.go:43-68,117-232,238-279` (wire + ticker), `.env.example` docs, `Dockerfile` only if embedding `go-ical`/`go-webdav` (no system deps).

## Testing & rollout

Table-driven tool tests (master-only gate, happy path, malformed RFC3339), HTTP client httptest for macos/google/caldav, bridge calendar routes 405/401/200, `go test -race`. Manual: add/list/delete via self-chat + toll-free verify at `ollama.com/settings` delta = 0 (local + Apple).

## Cost flag

**FREE.** No paid-only blocker. Google billing only if `>1M/day` after pricing later 2026 + 90-day notice.
