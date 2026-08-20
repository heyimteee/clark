# CLI reference

All management goes through the `clark` binary. Commands are Master-only.

```sh
./clark <command> [flags]
```

## `run`

Start the assistant. On first run a QR code is printed — scan it in WhatsApp > Settings > Linked Devices.

```sh
./clark run
```

Inside Docker the QR code appears in `docker compose logs -f`.

## `init` and `view`

```sh
./clark init          # create the database and defaults (run once)
./clark view          # status, context, VIPs, and each VIP's granted tools
./clark help          # usage for every command
```

## `vip` — inner circle

Only VIPs are answered. Delete more sensitive than add; `clear` empties the whole list.

```sh
./clark vip -a "11234567890,John Doe,Colleague"
./clark vip -d 11234567890
./clark vip -clear
./clark vip           # list
```

## `ctx` — master context

Tells the model your current status. This is the most effective way to change how Clark speaks for you.

```sh
./clark ctx -c "In a board meeting until 5 PM."
./clark ctx -clear
```

## `toggle` — global and per-VIP status

When off, Clark stays silent toward VIPs but still answers you in your own chat.

```sh
./clark toggle                                    # flip everyone
./clark toggle -r "Tiara" -set on
./clark toggle -all off                           # silence everyone, wipes personal overrides
```

`-r`/`-recipient` and `-all` are mutually exclusive; `-set` takes `on`/`off`.

## `think` and `history`

```sh
./clark think on          # reasoning mode (off by default, persists)
./clark think off
./clark history 10         # recent turns injected per reply (default 10)
```

`history` controls recall vs. cost per turn.

## `access` — per-VIP tools

A VIP may only hold `web_search` or `view_history`. Master-only tools (`send_message`, `send_imessage`, `set_status`, `set_context`, `add_vip`, `delete_vip`, `set_access`, `get_state`, `view_all_history`, `set_history_limit`) are never grantable.

```sh
./clark access -r "11234567890" -tool web_search -set on
./clark access -r "John Doe"    -tool web_search -set off
```

## Tips

* CLI writes share the same SQLite store as the running `clark run` process. Changes take effect live via a `SIGHUP` reload and a WebSocket state push to the dashboard.
* Prefer `ctx` and `toggle` for one-off changes; use the web console bento tiles for persistent settings.
