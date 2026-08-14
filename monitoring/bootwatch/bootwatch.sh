#!/usr/bin/env bash
# Reports a reboot/crash to clark on every boot (fired by the systemd oneshot).
# Includes how long the previous boot lasted and when, so the Master can spot
# the power-loss pattern that killed earlier runs.
set -u

ALERT_TOKEN_FILE=/home/tristan/monitoring/bootwatch/.alert_token
URL="https://clark.studio.lab/web/api/notify"
TOKEN=""

[ -f "$ALERT_TOKEN_FILE" ] && TOKEN=$(cat "$ALERT_TOKEN_FILE")

# Walk the boot list for the previous boot's duration (best effort).
PREV=$(journalctl --list-boots 2>/dev/null | tail -n 2 | head -n 1)
PREV_BOOT=$(echo "$PREV" | awk '{print $1}')
PREV_INFO=$(journalctl -b "$PREV_BOOT" 2>/dev/null | head -n 3 | tr '\n' ' ')
BOOT_ID=$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)
UPTIME=$(awk '{printf "%d", $1}' /proc/uptime 2>/dev/null)

MSG="Server booted (boot_id=${BOOT_ID}, uptime=${UPTIME}s, previous=${PREV_INFO:-unknown})"

if [ -z "$TOKEN" ]; then
  logger -t bootwatch "no alert token; skipping (${MSG})"
  exit 0
fi

curl -ksS -o /dev/null -w "%{http_code}" \
  --max-time 15 \
  -X POST \
  -H "Content-Type: application/json" \
  -H "X-Clark-Alert-Token: $TOKEN" \
  -d "{\"kind\":\"reboot\",\"title\":\"Server booted\",\"body\":\"$MSG\"}" \
  "$URL" 2>/dev/null | logger -t bootwatch "clark notify status"

logger -t bootwatch "reported boot: $MSG"
