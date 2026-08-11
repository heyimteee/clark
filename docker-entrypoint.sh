#!/bin/sh
set -e

# Seed the assistant's default settings. Idempotent, so safe on every boot.
clark init

# Optionally seed a master context from the environment.
if [ -n "${CLARK_CONTEXT:-}" ]; then
  clark ctx -c "$CLARK_CONTEXT"
fi

exec clark "$@"
