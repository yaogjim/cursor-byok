#!/bin/sh
# Start WeTTY on loopback only. TLS and identity auth terminate at the host proxy.
set -eu

export HOME="${HOME:-/home/bun}"
# Keep /usr/bin before bun's node fallback so `#!/usr/bin/env node` hits real Node
# (WeTTY's node-pty native addon does not run under bun).
export PATH="/home/bun/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bun-node-fallback-bin"

HOST="${WETTY_HOST:-127.0.0.1}"
PORT="${WETTY_PORT:-7681}"
BASE="${WETTY_BASE:-/}"
COMMAND="${WETTY_COMMAND:-/usr/local/bin/cursor-cli-shell}"
TITLE="${WETTY_TITLE:-cursor-cli}"
LOG_LEVEL="${WETTY_LOG_LEVEL:-info}"

cd /workspace 2>/dev/null || cd "$HOME" || cd /

if ! command -v wetty >/dev/null 2>&1; then
  echo "wetty not found in PATH" >&2
  exit 1
fi

if [ -f /usr/local/lib/cursor-cli-docker/patch-wetty.cjs ]; then
  node /usr/local/lib/cursor-cli-docker/patch-wetty.cjs || true
fi

if [ "$#" -gt 0 ]; then
  exec wetty "$@"
fi

set -- wetty \
  --host "$HOST" \
  --port "$PORT" \
  --base "$BASE" \
  --command "$COMMAND" \
  --title "$TITLE"

if wetty --help 2>&1 | grep -q -- '--log-level'; then
  set -- "$@" --log-level "$LOG_LEVEL"
fi

exec "$@"