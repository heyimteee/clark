#!/usr/bin/env bash
# Installs clark's Kokoro TTS server on macOS as a launchd agent, mirroring
# install-bridge.sh. The server runs on the Mac so TTS synthesis happens on
# Apple Silicon via Apple's MLX framework (native Metal GPU/ANE) instead of the
# i5 box; clark calls it over Tailscale.
#
# Usage:
#   ./scripts/install-kokoro-tts.sh [KOKORO_TOKEN]
#
#   KOKORO_TOKEN  shared secret, must equal TTS_REMOTE_TOKEN on the server.
#
# Creates:
#   ~/.clark/kokoro/venv                    python venv + mlx-audio + kokoro-onnx
#   ~/.clark/kokoro/mlx-model               mlx-community/Kokoro-82M-8bit (MLX)
#   ~/.clark/kokoro/server                  a copy of the server
#   ~/Library/LaunchAgents/com.clark.kokoro-tts.plist
# Logs: /usr/local/var/log/kokoro-tts.log
set -euo pipefail

TOKEN="${1:-${KOKORO_TOKEN:-}}"
if [[ -z "$TOKEN" ]]; then
	echo "usage: $0 <shared-token>" >&2
	exit 1
fi

DIR="$HOME/.clark/kokoro"
MODEL_DIR="$DIR/mlx-model"
PLIST="$HOME/Library/LaunchAgents/com.clark.kokoro-tts.plist"
LOG_DIR=/usr/local/var/log
PORT=8790
VOICE=am_michael
HF_MODEL="mlx-community/Kokoro-82M-8bit"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_SRC="$SCRIPT_DIR/../tools/kokoro_mac_server.py"

# Pick a python in mlx-audio's supported range (>=3.10, <3.14). Prefer 3.12:
# the spacy->thinc->blis chain has macOS arm64 wheels for 3.12 but not always
# for 3.13 (Cython build fails there).
find_python() {
	for c in python3.12 python3.11 python3.13; do
		if command -v "$c" >/dev/null 2>&1; then echo "$c"; return 0; fi
	done
	if command -v python3 >/dev/null 2>&1; then
		v=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])' 2>/dev/null || true)
		case "$v" in
			3.1[0-3]) echo "python3"; return 0 ;;
		esac
	fi
	return 1
}

PY=$(find_python || true)
if [[ -z "$PY" ]]; then
	echo "No suitable python (need 3.10-3.13). Install one with: brew install python@3.12" >&2
	exit 1
fi
echo "==> Using $($PY --version 2>&1)"

echo "==> Creating venv at $DIR/venv"
mkdir -p "$DIR"
$PY -m venv "$DIR/venv"
"$DIR/venv/bin/pip" install --upgrade pip >/dev/null
"$DIR/venv/bin/pip" install mlx-audio "misaki[en]"
echo "==> Installing espeak-ng (best effort, for OOD fallback)"
command -v brew >/dev/null 2>&1 && (brew list espeak-ng >/dev/null 2>&1 || brew install espeak-ng >/dev/null 2>&1 || true) || true

echo "==> Downloading Kokoro MLX model ($HF_MODEL, 8-bit)"
"$DIR/venv/bin/python" - "$MODEL_DIR" "$HF_MODEL" <<'PYEOF'
import sys
from huggingface_hub import snapshot_download
snapshot_download(
    sys.argv[2],
    local_dir=sys.argv[1],
    allow_patterns=["*.safetensors", "*.json", "voices/*"],
)
PYEOF

echo "==> Copying server script (stable path, independent of the repo)"
cp "$SERVER_SRC" "$DIR/kokoro_mac_server.py"

echo "==> Installing daemon wrapper (proper name in macOS notifications)"
cat > /usr/local/bin/kokoro-tts-daemon <<'WRAPPER'
#!/bin/bash
DIR="$HOME/.clark/kokoro"
exec "$DIR/venv/bin/python" "$DIR/kokoro_mac_server.py" "$@"
WRAPPER
chmod +x /usr/local/bin/kokoro-tts-daemon

echo "==> Installing launchd plist"
mkdir -p "$LOG_DIR"
cat > "$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.clark.kokoro-tts</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/kokoro-tts-daemon</string>
		<string>--port</string>
		<string>$PORT</string>
		<string>--model</string>
		<string>$MODEL_DIR</string>
		<string>--voice</string>
		<string>$VOICE</string>
		<string>--token</string>
		<string>$TOKEN</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>$LOG_DIR/kokoro-tts.log</string>
	<key>StandardErrorPath</key>
	<string>$LOG_DIR/kokoro-tts.log</string>
</dict>
</plist>
PLIST

echo "==> (Re)loading launchd agent"
launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

echo "==> Done."
echo "    Logs: $LOG_DIR/kokoro-tts.log"
echo "    Troubleshoot: launchctl list | grep clark"
echo "    Set on the server (.env):"
echo "      TTS_ENGINE=kokoro-remote"
echo "      TTS_REMOTE_URL=http://$(tailscale ip -4 2>/dev/null | head -1 || echo '<mac-tailnet-ip>'):$PORT"
echo "      TTS_REMOTE_TOKEN=$TOKEN"
