#!/bin/sh
# Interactive shell for WeTTY.
# WeTTY only execs a local command (instead of ssh) when the server uid is 0.
# This wrapper drops the PTY to the non-root bun user and starts /bin/sh.
set -eu

cd /workspace 2>/dev/null || cd /home/bun 2>/dev/null || cd /

if [ "$(id -u)" -eq 0 ]; then
  export HOME=/home/bun
  export USER=bun
  export LOGNAME=bun
  export SHELL=/bin/sh
  exec setpriv --reuid=bun --regid=bun --init-groups -- /bin/sh -i
fi

export HOME="${HOME:-/home/bun}"
export USER="${USER:-bun}"
export LOGNAME="${LOGNAME:-bun}"
export SHELL=/bin/sh
exec /bin/sh -i