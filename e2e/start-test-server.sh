#!/usr/bin/env bash
set -euo pipefail

port="${CHORA_E2E_PORT:-18787}"
tmp_dir="$(mktemp -d "$PWD/.chora-e2e.XXXXXX")"
chmod 700 "$tmp_dir"
child_pid=""

cleanup() {
  trap - EXIT INT TERM
  if [[ -n "$child_pid" ]] && kill -0 "$child_pid" 2>/dev/null; then
    kill "$child_pid" 2>/dev/null || true
    wait "$child_pid" 2>/dev/null || true
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
wait "$child_pid"
