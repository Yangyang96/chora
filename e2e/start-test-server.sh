#!/usr/bin/env bash
set -euo pipefail

port="${CHORA_E2E_PORT:-18787}"
document_port="${CHORA_E2E_DOCUMENT_PORT:-$((port + 1))}"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/chora-e2e.XXXXXX")"
tmp_dir="$(cd "$tmp_dir" && pwd -P)"
chmod 700 "$tmp_dir"
child_pid=""
document_pid=""

cleanup() {
  trap - EXIT INT TERM
  if [[ -n "$child_pid" ]] && kill -0 "$child_pid" 2>/dev/null; then
    kill "$child_pid" 2>/dev/null || true
    wait "$child_pid" 2>/dev/null || true
  fi
  if [[ -n "$document_pid" ]] && kill -0 "$document_pid" 2>/dev/null; then
    kill "$document_pid" 2>/dev/null || true
    wait "$document_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp_dir"
}

trap cleanup EXIT
trap 'exit 0' INT TERM

npm --workspace web run build -- --outDir "${CHORA_WEB_BUILD_DIR:-dist}"
GOCACHE="$tmp_dir/go-cache" go build -tags chora_e2e -o "$tmp_dir/chora-e2e" ./tools/e2eserver
server_args=(--db "$tmp_dir/chora.db" --web "${CHORA_WEB_BUILD_DIR:-$PWD/web/dist}" --port "$port")
if [[ -n "${CHORA_E2E_SCENARIO:-}" ]]; then
  server_args+=(--scenario "$CHORA_E2E_SCENARIO")
fi
"$tmp_dir/chora-e2e" "${server_args[@]}" &
child_pid=$!
if [[ -z "${CHORA_E2E_SCENARIO:-}" ]]; then
  mkdir -p "$tmp_dir/document"
  chmod 700 "$tmp_dir/document"
  "$tmp_dir/chora-e2e" --db "$tmp_dir/document/chora.db" --web "${CHORA_WEB_BUILD_DIR:-$PWD/web/dist}" --port "$document_port" --scenario project-document &
  document_pid=$!
  for attempt in {1..100}; do
    if curl --silent --fail "http://127.0.0.1:$document_port/" >/dev/null; then break; fi
    if ! kill -0 "$document_pid" 2>/dev/null; then wait "$document_pid"; exit 1; fi
    sleep 0.1
  done
  curl --silent --fail "http://127.0.0.1:$document_port/" >/dev/null
fi
wait "$child_pid"
