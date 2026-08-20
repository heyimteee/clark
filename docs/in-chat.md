# In-chat commands

These are typed in your own chat (the self-chat). They are handled instantly without a model call and are Master-only.

| What you type | What it does |
| --- | --- |
| `wake up buddy` / `wake clark` | Turn Clark on for everyone (wipes personal overrides) |
| `silence clark` / `sleep clark` | Turn Clark off for everyone |
| `wake clark for <name>` / `for <name> wake clark` | Turn Clark on just for that person |
| `silence <name>` / `sleep clark for <name>` | Turn Clark off just for that person |
| `wake clark for everyone` / `silence clark for all` | Reset everyone to one status |
| `thinking mode on` / `thinking mode off` / `toggle thinking` | Reasoning mode |
| `set history limit to 10` | Recent turns reviewed per reply |
| `set my context to …` | Update your context |
| `clear context` | Empty your context |
| `add vip <number>, <name>, <relation>` | Admit someone to the inner circle |
| `delete vip <name>` | Remove someone |
| `clear vips` | Empty the whole inner circle |
| `grant <name> access to <tool>` / `revoke <name> access to <tool>` | Manage a VIP's tools |
| `show me everything` | Full report (status, context, VIPs, tools) |
| `help` / `tool guidance` / `show commands` | This manual |

## How status works

There is one default status plus an optional per-VIP override. A personal override wins; the default applies to everyone else. Global commands (`wake up buddy`, `silence clark`, `toggle`, `set_status` without a recipient) set the default and wipe personal overrides, restoring a single known state. Per-VIP commands touch only that person.

## Rate-limit failover

If the model returns HTTP 429, Clark turns himself off (persisted, overrides wiped), messages you in your own chat, and apologizes to the person he was answering. Say `wake up buddy` to resume.
