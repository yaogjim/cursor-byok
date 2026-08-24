#!/bin/sh
# Example docker run for 198. Fill proxy/endpoint as needed.
# Do not add -p 7681:7681. Do not put tokens in this file.
set -eu

IMAGE="${IMAGE:-cursor-cli-runtime:wetty}"
NAME="${NAME:-cursor-cli}"
DATA="${DATA:-/usr/local/cursor-cli}"
PROXY="${PROXY:-http://172.16.137.80:8118}"
NO_PROXY_VAL="${NO_PROXY_VAL:-127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16}"

docker rm -f "$NAME" 2>/dev/null || true
docker run -d --name "$NAME" --restart unless-stopped --network host --init \
  -v "${DATA}/root:/root" \
  -v "${DATA}/workspace:/workspace" \
  -v "${DATA}/home:/home/bun" \
  -w /workspace \
  -e HOME=/home/bun \
  -e AGENT_CLI_CREDENTIAL_STORE=file \
  -e NO_OPEN_BROWSER=1 \
  -e CURSOR_API_ENDPOINT=http://127.0.0.1:18090 \
  -e http_proxy="$PROXY" \
  -e https_proxy="$PROXY" \
  -e HTTP_PROXY="$PROXY" \
  -e HTTPS_PROXY="$PROXY" \
  -e ALL_PROXY="$PROXY" \
  -e GLOBAL_AGENT_HTTP_PROXY="$PROXY" \
  -e GLOBAL_AGENT_HTTPS_PROXY="$PROXY" \
  -e no_proxy="$NO_PROXY_VAL" \
  -e NO_PROXY="$NO_PROXY_VAL" \
  "$IMAGE"