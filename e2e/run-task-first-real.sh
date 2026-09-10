#!/usr/bin/env bash
set -euo pipefail

# Builds and runs the task-first real-Pi acceptance journey in a canonical
# disposable workspace. It never reads from or writes to web/dist and never
# reuses a running development server.

source_root="$(cd "$(dirname "$0")/.." && pwd -P)"
root="$(mktemp -d "${TMPDIR:-/tmp}/chora-task-first-real.XXXXXX")"
root="$(cd "$root" && pwd -P)"
web_root="$source_root/output/task-first-real-web"
data_root="$root/data"
repo_a="$root/repositories/task-first-alpha"
repo_b="$root/repositories/task-first-beta"
config="$root/playwright.task-first-real.config.cjs"
port="${CHORA_TASK_FIRST_PORT:-18913}"
evidence="${CHORA_TASK_FIRST_EVIDENCE:-$source_root/output/task-first-real-evidence.json}"
artifacts="${CHORA_TASK_FIRST_ARTIFACTS:-$source_root/output/task-first-real-artifacts}"

cleanup() {
  local result=$?
  if [[ "$result" != "0" ]]; then
    mkdir -p "$artifacts"
    [[ ! -d "$data_root" ]] || cp -R "$data_root" "$artifacts/failed-data"
    [[ ! -d "$root/repositories" ]] || cp -R "$root/repositories" "$artifacts/failed-repositories"
  fi
  rm -rf "$root"
}
trap cleanup EXIT INT TERM

mkdir -p "$data_root" "$root/repositories" "$web_root" "$(dirname "$evidence")" "$artifacts"
chmod 700 "$data_root"

node - "$config" "$source_root" "$artifacts" <<'NODE'
const { writeFileSync } = require('node:fs')
const { join } = require('node:path')
const [config, source, artifacts] = process.argv.slice(2)
const playwright = join(source, 'node_modules', '@playwright', 'test')
const body = `const { defineConfig } = require(${JSON.stringify(playwright)})
module.exports = defineConfig({
  testDir: ${JSON.stringify(join(source, 'e2e'))},
  testMatch: 'task-first-real.spec.ts',
  timeout: 900000,
  expect: { timeout: 20000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'line',
  outputDir: ${JSON.stringify(artifacts)},
  use: { baseURL: process.env.CHORA_WORKBENCH_BASE_URL, trace: 'retain-on-failure' },
})
`
writeFileSync(config, body)
NODE

if [[ "${1:-}" == "--list" ]]; then
  CHORA_WORKBENCH_BIN="$root/not-run-chora" \
  CHORA_WORKBENCH_SOURCE="$source_root" \
  CHORA_WORKBENCH_DATA="$data_root" \
  CHORA_WORKBENCH_WEB="$web_root" \
  CHORA_WORKBENCH_PORT="$port" \
  CHORA_WORKBENCH_BASE_URL="http://127.0.0.1:$port" \
  CHORA_TASK_FIRST_REPO_A="$repo_a" \
  CHORA_TASK_FIRST_REPO_B="$repo_b" \
  CHORA_TASK_FIRST_EVIDENCE="$evidence" \
    "$source_root/node_modules/.bin/playwright" test --config "$config" --list
  exit 0
fi

if [[ "$port" == "8789" ]]; then
  echo "Refusing port 8789: this acceptance run must not reuse the development instance." >&2
  exit 2
fi
if curl --silent --show-error --fail --max-time 1 "http://127.0.0.1:$port/api/status" >/dev/null 2>&1; then
  echo "Port $port already serves a process; choose an unused CHORA_TASK_FIRST_PORT." >&2
  exit 2
fi
if ! command -v pi >/dev/null 2>&1; then
  echo "BLOCKED: no native pi executable is discoverable on PATH; no fake provider will be used." >&2
  exit 78
fi

create_fixture() {
  local repository="$1"
  local package_name="$2"
  mkdir -p "$repository/src" "$repository/test"
  git -C "$repository" init -b main >/dev/null
  git -C "$repository" config user.email e2e@chora.local
  git -C "$repository" config user.name "Chora E2E"
  cat >"$repository/package.json" <<EOF
{
  "name": "$package_name",
  "private": true,
  "version": "1.0.0",
  "type": "module",
  "scripts": { "test": "node --test" }
}
EOF
  cat >"$repository/src/shared.js" <<'EOF'
export function normalizeLabel(value) {
  return String(value).trim()
}
EOF
  cat >"$repository/test/shared.test.js" <<'EOF'
import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeLabel } from '../src/shared.js'

test('normalizeLabel trims user input', () => {
  assert.equal(normalizeLabel('  useful package  '), 'useful package')
})
EOF
  dd if=/dev/zero of="$repository/fixture.bin" bs=1048576 count=9 status=none
  npm test --prefix "$repository" >/dev/null
  git -C "$repository" add package.json src/shared.js test/shared.test.js fixture.bin
  git -C "$repository" commit -m "initial useful node package" >/dev/null
}

create_fixture "$repo_a" "@chora/task-first-alpha"
create_fixture "$repo_b" "@chora/task-first-beta"

npm --prefix "$source_root" --workspace web run build -- --outDir "$web_root"
go -C "$source_root" build -o "$root/chora" ./cmd/chora

CHORA_WORKBENCH_BIN="$root/chora" \
CHORA_WORKBENCH_SOURCE="$source_root" \
CHORA_WORKBENCH_DATA="$data_root" \
CHORA_WORKBENCH_WEB="$web_root" \
CHORA_WORKBENCH_PORT="$port" \
CHORA_WORKBENCH_BASE_URL="http://127.0.0.1:$port" \
CHORA_TASK_FIRST_REPO_A="$repo_a" \
CHORA_TASK_FIRST_REPO_B="$repo_b" \
CHORA_TASK_FIRST_EVIDENCE="$evidence" \
  "$source_root/node_modules/.bin/playwright" test --config "$config" "$@"
