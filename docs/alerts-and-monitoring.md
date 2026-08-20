# Alerts and monitoring

## Alert modes

* **Voice** — alerts are spoken aloud via the active TTS engine and delivered to WhatsApp, iMessage, and the web console chat.
* **Silent** — no speech. Triggers a FaceTime audio call and a native macOS banner via the bridge action endpoint, plus WhatsApp/iMessage/web.

Toggle from the console or:

```sh
curl -X POST https://clark.studio.lab/web/api/alert-mode \
  -H "Authorization: Bearer <session>" \
  -H "Content-Type: application/json" \
  -d '{"mode":"silent"}'
```

## Trigger sources

* Bypass phrase (`BYPASS_PHRASE`, default `get him to me`) from any channel
* Netdata webhooks (CPU, memory, disk, container health)
* Uptime Kuma webhooks (service reachability)
* Bootwatch systemd oneshot (reboots)

All alert sources POST to `POST /web/api/notify` with `X-Clark-Alert-Token: <ALERT_TOKEN>` and a body `{"kind","title","body"}`. Known kinds use hardcoded templates; unknown kinds get a factual AI summary; when the model is unavailable a generic What/How/When fallback is used.

## Monitoring stack

Runs on the server alongside Clark:

* **Netdata** `:19999` — host and container telemetry, 60-day retention, alarms webhook to Clark
* **Uptime Kuma** `:3001` — reachability probes, notifications webhook to Clark
* **Bootwatch** — systemd oneshot reporting reboots to Clark on boot

```sh
cd monitoring && docker compose up -d
```

See `monitoring/README.md` for compose details.
