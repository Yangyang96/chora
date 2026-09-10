#!/usr/bin/env bash
set -euo pipefail

# Workbench project-entry bootstrap. Boots the real `chora workbench` server
# (no Docker, no OAuth, no Preflight) against an owner-private data root that
# lives OUTSIDE the source checkout. Symlinks are resolved so the workbench's
# SQLite managed-path anchor never walks a symlinked ancestor (e.g. /tmp).

root="$(mktemp -d "${TMPDIR:-/tmp}/chora-project-entry.XXXXXX")"
root="$(cd "$root" && pwd -P)"

cleanup() {
  local result=$?
  if [[ "$result" == "0" ]]; then
    rm -rf "$root"
  else
    echo "Failed project-entry fixture retained at $root" >&2
  fi
}
trap cleanup EXIT INT TERM

npm --workspace web run build -- --outDir "${CHORA_WEB_BUILD_DIR:-dist}"
go build -o "$root/chora" ./cmd/chora

source_root="$(cd "$PWD" && pwd -P)"
data="$root/data"
mkdir -p "$data"
chmod 700 "$data"

port="${CHORA_WORKBENCH_PORT:-18910}"

CHORA_WORKBENCH_BIN="$root/chora" \
CHORA_WORKBENCH_SOURCE="$source_root" \
CHORA_WORKBENCH_DATA="$data" \
CHORA_WORKBENCH_WEB="${CHORA_WEB_BUILD_DIR:-$source_root/web/dist}" \
CHORA_WORKBENCH_PORT="$port" \
CHORA_WORKBENCH_BASE_URL="http://127.0.0.1:$port" \
  npx playwright test --config playwright.project-entry.config.ts
