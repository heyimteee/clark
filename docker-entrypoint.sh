#!/bin/sh
set -e

# Seed the assistant's default settings. Idempotent, so safe on every boot.
clark init

# Seed the affirmation volume from the baked piper fallback clips on first
# boot. Michael clips synced from the Mac (which overwrite these) take
# priority; piper is only the last-resort fallback.
if [ -d /opt/affirmations-fallback ] && [ -d /opt/affirmations ] && \
   ! ls /opt/affirmations/*.wav >/dev/null 2>&1; then
  cp /opt/affirmations-fallback/*.wav /opt/affirmations/
fi

# The idle tone is a voice-agnostic generated sine, not a speech clip, so it
# must always exist even after Michael clips replace the speech fallbacks.
if [ -d /opt/affirmations-fallback ] && [ -d /opt/affirmations ] && \
   [ ! -f /opt/affirmations/idle.wav ]; then
  cp /opt/affirmations-fallback/idle.wav /opt/affirmations/
fi

# Optionally seed a master context from the environment.
if [ -n "${CLARK_CONTEXT:-}" ]; then
  clark ctx -c "$CLARK_CONTEXT"
fi

exec clark "$@"
