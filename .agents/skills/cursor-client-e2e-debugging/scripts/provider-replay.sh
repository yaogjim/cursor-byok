#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  DEBUG_DIR=<path/to/conversation/debug> REQUEST_ID=<request-id> MODEL_CALL_ID=<model-call-id> \
    [BASE_URL=<provider-base-url>] [API_KEY=<provider-api-key>] [CONFIG_FILE=<config.yaml>] \
    [OUT_DIR=<output-dir>] [MAX_TIME=240] provider-replay.sh

Required:
  DEBUG_DIR      Path to history/<conversationId>/debug
  REQUEST_ID     Provider request_id to replay
  MODEL_CALL_ID  Provider model_call_id to replay

Optional:
  EXTRACT_ONLY    Set to 1 to only restore request.body.json and replay.meta.json without curl.
  BASE_URL        Provider base URL. Falls back to GLM_BASE_URL or ANTHROPIC_BASE_URL.
  API_KEY         Provider API key. Falls back to ANTHROPIC_API_KEY or GLM_API_KEY.
  CONFIG_FILE     Defaults to ~/.cursor-local-assistant-v2/config.yaml.
  CHANNEL_NAME    Display name to read from config.yaml when BASE_URL/API_KEY is missing. Defaults to GLM.
  OUT_DIR         Output directory. Defaults to /tmp/cursor-provider-replay-<request-id>.
  MAX_TIME        curl max-time seconds. Defaults to 240.
  ENDPOINT_PATH   Provider path. Defaults to /v1/messages.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

DEBUG_DIR="${DEBUG_DIR:-}"
REQUEST_ID="${REQUEST_ID:-}"
MODEL_CALL_ID="${MODEL_CALL_ID:-}"
CONFIG_FILE="${CONFIG_FILE:-$HOME/.cursor-local-assistant-v2/config.yaml}"
CHANNEL_NAME="${CHANNEL_NAME:-GLM}"
BASE_URL="${BASE_URL:-${GLM_BASE_URL:-${ANTHROPIC_BASE_URL:-}}}"
API_KEY="${API_KEY:-${ANTHROPIC_API_KEY:-${GLM_API_KEY:-}}}"
EXTRACT_ONLY="${EXTRACT_ONLY:-0}"
ENDPOINT_PATH="${ENDPOINT_PATH:-/v1/messages}"
MAX_TIME="${MAX_TIME:-240}"
OUT_DIR="${OUT_DIR:-/tmp/cursor-provider-replay-${REQUEST_ID:-unknown}}"
BODY_FILE="$OUT_DIR/request.body.json"
RESP_FILE="$OUT_DIR/response.sse"
HEADER_FILE="$OUT_DIR/response.headers"
META_FILE="$OUT_DIR/replay.meta.json"

require_value() {
  local name="$1"
  local value="$2"
  if [[ -z "$value" ]]; then
    echo "缺少 $name。运行 --help 查看用法。" >&2
    exit 2
  fi
}

read_channel_config() {
  local field="$1"
  python3 - "$CONFIG_FILE" "$CHANNEL_NAME" "$field" <<'PY'
import sys
from pathlib import Path

config_path = Path(sys.argv[1]).expanduser()
channel_name = sys.argv[2]
field = sys.argv[3]
if not config_path.exists():
    raise SystemExit

lines = config_path.read_text(encoding="utf-8").splitlines()
in_channel = False
for line in lines:
    stripped = line.strip()
    if stripped.startswith("- displayName:"):
        in_channel = stripped.split(":", 1)[1].strip().strip('"') == channel_name
        continue
    if in_channel and stripped.startswith(field + ":"):
        print(stripped.split(":", 1)[1].strip().strip('"'))
        raise SystemExit
PY
}

require_value "DEBUG_DIR" "$DEBUG_DIR"
require_value "REQUEST_ID" "$REQUEST_ID"
require_value "MODEL_CALL_ID" "$MODEL_CALL_ID"

if [[ ! -d "$DEBUG_DIR" ]]; then
  echo "DEBUG_DIR 不存在: $DEBUG_DIR" >&2
  exit 2
fi

if [[ ! -d "$DEBUG_DIR/provider" ]]; then
  echo "provider debug 目录不存在: $DEBUG_DIR/provider" >&2
  exit 2
fi

if [[ "$EXTRACT_ONLY" != "1" ]]; then
  if [[ -z "$BASE_URL" ]]; then
    BASE_URL="$(read_channel_config baseURL || true)"
  fi

  if [[ -z "$API_KEY" ]]; then
    API_KEY="$(read_channel_config apiKey || true)"
  fi

  require_value "BASE_URL/GLM_BASE_URL/ANTHROPIC_BASE_URL 或 config[$CHANNEL_NAME].baseURL" "$BASE_URL"
  require_value "API_KEY/ANTHROPIC_API_KEY/GLM_API_KEY 或 config[$CHANNEL_NAME].apiKey" "$API_KEY"
fi

mkdir -p "$OUT_DIR"
if [[ "$EXTRACT_ONLY" != "1" ]]; then
  : > "$HEADER_FILE"
  : > "$RESP_FILE"
fi

python3 - "$DEBUG_DIR" "$REQUEST_ID" "$MODEL_CALL_ID" "$BODY_FILE" "$META_FILE" <<'PY'
import base64
import hashlib
import json
import sys
from pathlib import Path

debug_dir = Path(sys.argv[1]).expanduser().resolve()
request_id = sys.argv[2]
model_call_id = sys.argv[3]
body_path = Path(sys.argv[4])
meta_path = Path(sys.argv[5])


def load_json_lines(path):
    with path.open("rb") as handle:
        for raw in handle:
            if not raw.strip():
                continue
            try:
                yield json.loads(raw)
            except json.JSONDecodeError:
                continue


def resolve_managed_path(relative_path):
    candidate = (debug_dir / relative_path).resolve()
    try:
        candidate.relative_to(debug_dir)
    except ValueError as exc:
        raise SystemExit(f"payload_ref 路径越界: {relative_path}") from exc
    return candidate


def read_chunk(ref):
    path = resolve_managed_path(ref.get("path") or "")
    offset = int(ref.get("offset") or 0)
    length = int(ref.get("length") or 0)
    if length <= 0:
        raise SystemExit(f"payload_ref length 无效: {ref}")
    with path.open("rb") as handle:
        handle.seek(offset)
        raw = handle.read(length)
    if len(raw) != length:
        raise SystemExit(f"payload pack 读取不完整: path={path} offset={offset} length={length}")
    record = json.loads(raw)
    if record.get("encoding") == "base64":
        chunk = base64.b64decode(record.get("data") or "", validate=True)
    elif record.get("encoding") == "json":
        chunk = json.dumps(record.get("data"), ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    else:
        raise SystemExit(f"不支持的 payload encoding: {record.get('encoding')}")
    chunk_digest = record.get("chunk_sha256")
    if chunk_digest and hashlib.sha256(chunk).hexdigest() != chunk_digest:
        raise SystemExit(f"payload chunk SHA-256 不匹配: {path}")
    return chunk, str(path)


def read_payload(ref):
    chunk_refs = ref.get("chunks") or [ref]
    chunks = []
    paths = []
    for chunk_ref in chunk_refs:
        chunk, path = read_chunk(chunk_ref)
        chunks.append(chunk)
        paths.append(path)
    payload = b"".join(chunks)
    digest = ref.get("sha256")
    if digest and hashlib.sha256(payload).hexdigest() != digest:
        raise SystemExit("payload SHA-256 不匹配")
    return payload, paths


event = None
event_path = None
for path in sorted((debug_dir / "provider").glob("event-*.jsonl")):
    for row in load_json_lines(path):
        if row.get("event") != "llm_request":
            continue
        if row.get("request_id") != request_id:
            continue
        if row.get("model_call_id") != model_call_id:
            continue
        event = row
        event_path = path
        break
    if event is not None:
        break

if event is None:
    raise SystemExit(f"未找到 llm_request: request_id={request_id} model_call_id={model_call_id}")

payload_ref = event.get("payload_ref")
if not isinstance(payload_ref, dict):
    raise SystemExit("llm_request 没有 payload_ref；该事件可能来自 basic 模式或旧格式")
payload_raw, pack_paths = read_payload(payload_ref)
payload = json.loads(payload_raw)
body = payload.get("body") if isinstance(payload, dict) else None
if body is None:
    raise SystemExit("llm_request payload 中没有 body")

body_path.write_text(json.dumps(body, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
meta = {
    "at": event.get("at"),
    "event": event.get("event"),
    "conversation_id": event.get("conversation_id"),
    "request_id": event.get("request_id"),
    "model_call_id": event.get("model_call_id"),
    "provider_event": str(event_path),
    "payload_packs": sorted(set(pack_paths)),
    "payload_sha256": payload_ref.get("sha256"),
}
meta_path.write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

if [[ "$EXTRACT_ONLY" == "1" ]]; then
  echo "body=$BODY_FILE"
  echo "meta=$META_FILE"
  exit 0
fi

url="${BASE_URL%/}${ENDPOINT_PATH}"

set +e
curl --no-buffer --silent --show-error \
  --connect-timeout 30 \
  --max-time "$MAX_TIME" \
  --request POST "$url" \
  --header "content-type: application/json" \
  --header "anthropic-version: 2023-06-01" \
  --header "User-Agent: claude-cli/1.0.25" \
  --header "x-api-key: $API_KEY" \
  --header "Authorization: Bearer $API_KEY" \
  --data-binary "@$BODY_FILE" \
  --dump-header "$HEADER_FILE" \
  --output "$RESP_FILE"
code=$?
set -e

echo "curl_exit_code=$code"
echo "body=$BODY_FILE"
echo "headers=$HEADER_FILE"
echo "response=$RESP_FILE"
echo "meta=$META_FILE"

echo "--- response headers ---"
if [[ -f "$HEADER_FILE" ]]; then
  python3 - "$HEADER_FILE" <<'PY'
from pathlib import Path
import sys
for line in Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines()[:40]:
    print(line)
PY
fi

echo "--- response first 120 lines ---"
if [[ -f "$RESP_FILE" ]]; then
  python3 - "$RESP_FILE" <<'PY'
from pathlib import Path
import sys
for line in Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines()[:120]:
    print(line)
PY
else
  echo "响应文件不存在。"
fi

exit "$code"