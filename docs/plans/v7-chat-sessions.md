# v7.0.0 — Chat Sessions (30-day span, “hop on chats”)

> Feasibility: **HIGH + FREE** — `timestamp` column already exists in `chat_history`, storage is WAL SQLite, UI history tile already has scope tabs to host the switcher.

## Objective

Extend retention from **30 messages per `jid`** to **30 days** and add a session switcher so the Master can browse/rejoin any chat from the last month.

## Today vs Needed

| Aspect | Today | Needed for 30-day hop |
|---|---|---|
| Retention | `history.go:25-34` `DELETE … WHERE id NOT IN (SELECT id … ORDER BY id DESC LIMIT 30)` — per-jid 30 cap | `DELETE WHERE timestamp < datetime('now','-30 days')` (keep `idx_chat_history_jid` and add timestamp indexes; don't evict by count) |
| Query shape | `Messages(jid)`, `RecentMessages(jid,limit)`, `AllRecentMessages(limit)` select only `role,content` (`history.go:52,83,117`); `Message` `store.go:17-20` lacks `Time` | Extend `Message`/`HistoryEntry` with `Time time.Time`, return `timestamp` from queries; add `MessagesSince(jid,since,until,limit)` (+ global variant) |
| Indexes | `idx_chat_history_jid ON chat_history(jid,id)` `store.go:162` | Add `idx_chat_history_timestamp ON chat_history(timestamp)` + `idx_chat_history_jid_ts ON chat_history(jid,timestamp)` for `jid+range` |
| UI | `app.js:258-272` `tile-history` (scope tabs `global/vip/web`), `refreshHistory:576-612` flat `hist-row` grid `120px 52px 1fr` (`app.css:603-637`), auto-refresh `setInterval 15s:1655`, keep scroll `list.scrollTop:596` | Keep flat list for MVP; add `scope=vip` date pagination (switcher) or infinite scroll (“load more” cursor). Grouped `sessions` table is polish. |
| Auth | `handleHistory` `rest.go:98-138` `scope=global\|vip\|web&jid=&limit=` up to 200 (`103`) | Add `since/until` RFC3339 params; reuse `requireAuth` + `decodeBody` audit `server.go:198-211` pattern |
| Sessions table | None — `grep "session"` hits only web auth (`server.go:27-51` sessionManager) and store `whatsmeow keys` | Optional for true session grouping (see below) |

## Retention flip (MVP)

`history.go:25-34` → time-bounded purge. 30 days @ ~10 VIPs × ~5 msg/day + ~25 web msgs/day ≈ 1–6k rows, ~5 MB (`docs/requirements.md:67`). SQLite trivial; add `Message.Time` and expose `e.time` at `app.js:601` (currently empty).

## Grouped sessions (optional polish, copy `DigestDocument` map-reduce feel)

- Either **A (Retention-only + date filter)** — 0.5–1 day: +1 index, change `DELETE`, add `Time`, add `since/until` params, surface `e.time`.
- Or **B1 (True sessions)** — 3–5 days: new `sessions(id,jid,created_at,title,msg_count)` + `chat_history.session_id FK`; `SaveMessage` assigns session by idle gap >4h or midnight; APIs `ListSessions(jid,days)` + `SessionMessages(id)`; switcher sidebar in History tile + Chat `select session` dropdown; optionally Ollama titles via same `DigestDocument` map-reduce already in `internal/assistant` (free local summarization, no Tavily).
- Or **B2 (+ auto-titles)**: background summarizer calls `DigestDocument` on session chunk set.

## Storage & cost

**FREE.** Local WAL SQLite (`store.go:89-94`), no cloud, no token growth beyond the normal `historyLimit=10` injection window (`assistant.go:45-47` default 10, max 50 `723-725`). Additional history beyond injection is browse-only unless the user resumes a session (inject that session’s transcript instead of flat recent).

## Tests & files

- `store/history.go:9,52,83,117` queries + `store.go:71-78` `HistoryStore` iface + `155-162` schema; add `MessagesSince` + indices
- `store/store_test.go:186-534` as table-driven template
- `web/rest.go:98-138,202-215` handlers; `internal/web/server.go:79` mounts
- `web/static/app.js:576-612` + `app.css:546-643`
- `web/static/app.css:295-306` bento grid, `app.js:4,7,157,178` `SESSION_KEY` web auth

## Recommendation

Start with **A2** (30-day `timestamp` retention + date-filter + `limit/offset` pagination) — 1–2 days, 80% value, surfaces `Time`. Graduate to **B1** only if resumable session context becomes a must.
