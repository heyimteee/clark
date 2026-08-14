#!/usr/bin/env bash
# Installs the clark iMessage bridge on macOS as a launchd agent.
#
# Prerequisites (one-time, user):
#   * Grant Full Disk Access to /usr/local/bin/imessage-bridge (not just the
#     terminal) so it can read ~/Library/Messages/chat.db.
#   * IMPORTANT: rebuilding the binary invalidates the TCC grant. After every
#     reinstall, remove and re-add the binary in
#     System Settings > Privacy & Security > Full Disk Access, then:
#     launchctl kickstart -k gui/$(id -u)/com.clark.imessage-bridge
#   * The first outbound send will prompt for Automation permission for Messages.
#
# Usage:
#   ./scripts/install-bridge.sh [IMESSAGE_BRIDGE_URL] [IMESSAGE_BRIDGE_TOKEN]
#
#   IMESSAGE_BRIDGE_URL   e.g. https://clark.example.com
#   IMESSAGE_BRIDGE_TOKEN the same value as IMESSAGE_BRIDGE_TOKEN on the server
#
# Optional env overrides: IMESSAGE_OWN_HANDLE (auto-detected otherwise),
# IMESSAGE_TLS_ROOTCA (path to a self-signed root CA, mkcert fallback only).
set -euo pipefail

URL="${1:-${IMESSAGE_BRIDGE_URL:-}}"
TOKEN="${2:-${IMESSAGE_BRIDGE_TOKEN:-}}"

if [[ -z "$URL" || -z "$TOKEN" ]]; then
	echo "usage: $0 <https://clark.YOUR-DOMAIN> <shared-token>" >&2
	exit 1
fi

BIN=/usr/local/bin/imessage-bridge
PLIST="$HOME/Library/LaunchAgents/com.clark.imessage-bridge.plist"
LOG_DIR=/usr/local/var/log

echo "==> Building bridge binary"
go build -o "$BIN" ./cmd/imessage-bridge

echo "==> Installing launchd plist"
mkdir -p "$LOG_DIR"
cat > "$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.clark.imessage-bridge</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>EnvironmentVariables</key>
	<dict>
		<key>IMESSAGE_BRIDGE_URL</key>
		<string>$URL</string>
		<key>IMESSAGE_BRIDGE_TOKEN</key>
		<string>$TOKEN</string>
		<key>IMESSAGE_OWN_HANDLE</key>
		<string>${IMESSAGE_OWN_HANDLE:-}</string>
		<key>IMESSAGE_TLS_ROOTCA</key>
		<string>${IMESSAGE_TLS_ROOTCA:-}</string>
		<key>IMESSAGE_ACTION_LISTEN</key>
		<string>${IMESSAGE_ACTION_LISTEN:-:8791}</string>
	</dict>
	<key>StandardOutPath</key>
	<string>$LOG_DIR/clark-bridge.log</string>
	<key>StandardErrorPath</key>
	<string>$LOG_DIR/clark-bridge.log</string>
</dict>
</plist>
PLIST

echo "==> (Re)loading launchd agent"
launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

echo "==> Done. Logs: $LOG_DIR/clark-bridge.log"
echo "    Troubleshoot with: launchctl list | grep clark"
