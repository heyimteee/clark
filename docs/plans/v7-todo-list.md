# v7.0.0 — Todo List

> Feasibility: **trivial + FREE** — green field in code, SQLite migration ~15 lines.

## Objective

Master (and optionally VIP-gated) CRUD for a persistent todo list, reachable via natural chat (“add todo …”, “list my todos”, …) and `POST /web/api/todos` for scripting, optionally as a bento tile.

## Data

```sql
CREATE TABLE IF NOT EXISTS todos(
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  jid        TEXT NOT NULL,         -- owner: "web" or VIP JID, like chat_history.jid
  text       TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'open',  -- open|done
  priority   INTEGER DEFAULT 0,
  due_at     DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_todos_jid ON todos(jid, id);
CREATE INDEX IF NOT EXISTS idx_todos_status ON todos(status);
```

Mirrors `migrate()` `store.go:130-179` with 30s ctx `131` and `execContext` `173-177`. Per-row few KB — identical storage discipline to `chat_history` WAL/`busy_timeout=5000`/`foreign_keys=on`/`0600` `store.go:89-94`.

## Tools (Master-only; per-conversation isolation via `tools.Sender`)

| Tool | Params | Handler |
|---|---|---|
| `add_todo` | `text*` string, `priority?` int, `due?` RFC3339 | `store.AddTodo(jid,text,…)` |
| `list_todos` | `status?` (`open`/`done`/all), `limit?` int | `store.ListTodos(jid, status, limit)` |
| `complete_todo` | `id*` int | `store.CompleteTodo(id)` |
| `delete_todo` | `id*` int | `store.DeleteTodo(id)` |
| `clear_todos` (optional) | none | `store.ClearTodos(jid)` |

Each tool is ~20 lines in `assistant/tools.go:13-263` (`RegisterFunc:46`, `toolParams:311-323`, `StringArg/IntArg/BoolArg` `102-124`, `masterOnly:304`). Follows `add_vip:88-111`, `view_history:196-217` patterns. `toolsForSender` `1563-1586` allowlist decides VIP visibility — keep Master-only for v1 (aligns with calendar `add/delete`).

## Fast path

Add `todo` regexes to `commands.go:44-149` only if desired; **not required** — tools alone suffice (LLM infers `add todo buy milk` → `add_todo`). If added, share the Master-only guard `!isSelf → false` at `53`.

## Web

| Route | Verb | Handler | Notes |
|---|---|---|---|
| `/web/api/todos` | `GET` `?status=&limit=` | `handleTodos` | like `handleHistory` `rest.go:98-138` |
| `/web/api/todos` | `POST` `{text,priority,due}` | `handleTodoAdd` | `decodeBody` audit `server.go:198-211` |
| `/web/api/todos/:id/complete` | `POST` | `handleTodoComplete` | upsert `completed_at` |
| `/web/api/todos/:id` | `DELETE` | `handleTodoDelete` | |

`server.go:79,123-143` mounts + `Subscribe(broadcast) 118-120` pattern usable for live `hub.broadcast` push.

**UI (optional polish, copy `history` tile):** `app.js:187-272` bento grid `12 col` (`app.css:295-306`, `tile-config/voice/access span 4`, `tile-vips/history span 12`), handlers `bindBento:348-435`, `renderVips:498-531`. New `tile-todos` (span 12 or 6), `renderTodos()/refreshTodos()/bindTodos()`, `state.todos` exposed from `rest.go:18-34` `state()`.

## Effort

| Scope | Days | Storage impact |
|---|---|---|
| Chat-only (DB+4 tools+REST+CLI, no tile) | 1–2 | <1 MB / 1k todos |
| + bento tile (JS/CSS polish, `help` manual `commands.go:561`) | +0.5–1 | same |

## Files touched

New: `internal/store/todos.go` (+ tests `store_test.go:186-534` pattern)
Modified: `store.go:30-78` `HistoryStore` grouping + `130-179` migration; `internal/assistant/tools.go:12-263`; `internal/tools/tools.go:46`; `internal/web/rest.go:18,98`, `internal/web/server.go:79,123`; `internal/web/static/app.js:187,348,498,576`; `internal/web/static/app.css:295`.

## Testing

`store` table-driven (copy `store_test.go` history/VIP suites); tool master-only gate (`vip_status` permutations); `rest_test.go` for `GET/POST/complete/delete`; `handler_test.go` fakeButler allowlist.
