# Monitoring

Telemetry + alerting for the clark server and every service, delivered to the
Master via clark itself (WhatsApp, web console, and spoken voice).

## What's covered

- **Netdata** (`:19999`) — full host + container telemetry: CPU, memory, disk,
  network, load, and **hardware temperatures** (CPU package via coretemp, NVMe
  SSD, PCH) at 1-second granularity, 60-day retention. Monitors every container
  (clark, NPM, adguard, portainer) via the docker socket.
- **Uptime Kuma** (`:3001`) — external reachability probes of the services and
  a status page.
- **Alerting to clark** — Netdata alarms and Kuma failures POST to clark's
  `/web/api/notify`, which delivers to WhatsApp (self-chat), the web console
  chat, and **spoken voice** (auto-toggle voice on → speak → restore).
- **Bootwatch** (systemd) — reports every boot/reboot to clark, including how
  long the previous boot lasted, so power-loss/crash patterns are visible.

## Layout

```
monitoring/
  docker-compose.yml          # netdata + uptime-kuma
  netdata/netdata.conf        # 60-day dbengine, hostname, listener
  netdata/health_alarm_notify.conf.example  # webhook -> clark (set ALERT_TOKEN)
  bootwatch/bootwatch.sh      # reboot reporter (needs systemd install)
```

## Deploy

```sh
# 1. On the server:
mkdir -p /home/tristan/monitoring
scp -r monitoring/ 3studio-server-tail:/home/tristan/monitoring/

# 2. Set the alert token:
cp /home/tristan/monitoring/netdata/health_alarm_notify.conf.example \
   /home/tristan/monitoring/netdata/health_alarm_notify.conf
# ... replace REPLACE_WITH_ALERT_TOKEN with the server's ALERT_TOKEN.

# 3. Start the stack:
cd /home/tristan/monitoring && docker compose up -d
```

## Privileged / one-time setup (manual, requires sudo or the NPM UI)

1. **Bootwatch** (needs root):
   ```sh
   sudo cp /home/tristan/monitoring/bootwatch/bootwatch.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now bootwatch
   ```
2. **NPM proxy hosts** (via the NPM admin UI at `npm.studio.lab`): add
   `netdata.studio.lab` → `http://host.docker.internal:19999` and
   `kuma.studio.lab` → `http://uptime-kuma:3001` (or the host IP).
3. **Uptime Kuma**: first visit `http://SERVER:3001` to create the admin user,
   then add monitors (clark, NPM, adguard, portainer, the Mac's Tailscale IP)
   and a Webhook notification to `https://clark.studio.lab/web/api/notify`
   with header `X-Clark-Alert-Token`.

## Note on DNS

Docker's embedded resolver (`127.0.0.11`) cannot forward through Tailscale
MagicDNS alone (`server misbehaving`). clark fixes this with explicit
`dns:` entries in `docker-compose.yml`. The daemon-wide equivalent is:

```json
{ "dns": ["100.100.100.100", "1.1.1.1"] }
```
in `/etc/docker/daemon.json`, followed by `systemctl restart docker`.

## Alert kinds

`/web/api/notify` accepts `{"kind","title","body"}`. Known kinds map to
hardcoded Clark-voiced templates: `overheat`, `disk_high`, `memory_high`,
`cpu_high`, `container_down`, `service_down`, `reboot`, `dns_failure`,
`bypass`. Unknown kinds get a factual AI summary; if the model is unavailable
during the alert, a generic What/How/When fallback is used.
