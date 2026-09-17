#!/usr/bin/env bash
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'macOS native tests require macOS; skipped on this host'
  exit 0
fi
root="$(cd "$(dirname "$0")" && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/chora-native-tests.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
xcrun swiftc -module-cache-path "$fixture/cache" "$root/RuntimeIntegrity.swift" "$root/RuntimeIntegrityTests.swift" -o "$fixture/test"
"$fixture/test"
xcrun swiftc -module-cache-path "$fixture/cache" "$root/AuthenticationChoices.swift" "$root/AuthenticationChoicesTests.swift" -o "$fixture/auth-test"
"$fixture/auth-test"
