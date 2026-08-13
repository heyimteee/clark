#!/usr/bin/env bash
# Installs a keep-awake launchd agent on macOS so clark's daemons (iMessage
# bridge + Kokoro TTS) keep running with the lid closed.
#
# Uses a dedicated `caffeinate -s -i` process (no child utility) so the
# PreventSystemSleep + PreventUserIdleSystemSleep assertions live for the
# agent's lifetime. Wrapping each daemon in `caffeinate <daemon>` instead risks
# orphaning the daemon on unload (killing caffeinate leaves the child bound to
# its port), so a standalone agent is safer.
#
# IMPORTANT: caffeinate -s only works on AC power. On battery, closing the lid
# sleeps the Mac regardless. For a bulletproof lid-close override (both AC and
# battery) use instead, once, as admin:
#   sudo pmset -a disablesleep 1
#
# Creates: ~/Library/LaunchAgents/com.clark.caffeinate.plist
set -euo pipefail

PLIST="$HOME/Library/LaunchAgents/com.clark.caffeinate.plist"

mkdir -p "$HOME/Library/LaunchAgents"
cat > "$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.clark.caffeinate</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/caffeinate</string>
		<string>-s</string>
		<string>-i</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
PLIST

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

echo "==> Done. com.clark.caffeinate installed."
echo "    Verify: pmset -g assertions | grep PreventSystemSleep"
