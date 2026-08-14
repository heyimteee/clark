#!/usr/bin/env bash
# Syncs Michael-rendered wake/processing clips from the Mac to the server's
# affirmation volume. The Mac renders with MLX (am_michael); the server serves
# them so wake + "Processing, Sir." match the reply voice (piper/Ryan remains
# only as the image-baked last-resort fallback before the first sync).
#
# Usage:
#   On the Mac:  tools/render_affirmations.py --model ~/.clark/kokoro/mlx-model \
#                   --voice am_michael --out /tmp/clark-affirmations
#   Then from anywhere:  ./scripts/sync-affirmations.sh /tmp/clark-affirmations
set -euo pipefail

SRC="${1:?usage: sync-affirmations.sh <local-affirmation-dir>}"
SERVER="${2:-3studio-server-tail}"
DEST="/home/tristan/clark/affirmations"

echo "==> Syncing affirmations -> $SERVER:$DEST"
ssh "$SERVER" "mkdir -p $DEST"
scp -q "$SRC"/*.wav "$SERVER:$DEST/"
echo "==> Done. Restart clark to pick up: docker restart clark"
