#!/usr/bin/env bash
set -euo pipefail

repetitions="${1:-1}"
root="$(mktemp -d "$PWD/.chora-joined-build.XXXXXX")"

cleanup() {
  rm -rf "$root"
}
trap cleanup EXIT INT TERM

npm --workspace web run build -- --outDir "${CHORA_WEB_BUILD_DIR:-dist}"
go build -tags chora_e2e -o "$root/chora-e2e" ./tools/e2eserver

for ((iteration=1; iteration<=repetitions; iteration++)); do
  data="$root/run-$iteration"
  mkdir -p "$data"
  chmod 700 "$data"
  port=$((18886 + iteration))
  CHORA_JOINED_BIN="$root/chora-e2e" \
  CHORA_JOINED_DB="$data/chora.db" \
  CHORA_JOINED_WEB="${CHORA_WEB_BUILD_DIR:-$PWD/web/dist}" \
  CHORA_JOINED_PORT="$port" \
  CHORA_JOINED_BASE_URL="http://127.0.0.1:$port" \
    npx playwright test --config playwright.joined.config.ts
done
