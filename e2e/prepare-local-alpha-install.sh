#!/bin/sh
set -eu

: "${CHORA_INSTALL_ROOT:?}"
: "${CHORA_DATA_ROOT:?}"
: "${CHORA_SOURCE_ROOT:?}"
: "${CHORA_BUNDLE_AGGREGATE:?}"

go run ./tools/localalpha install --bundle "$CHORA_SOURCE_ROOT" --root "$CHORA_INSTALL_ROOT"
go run ./tools/localalpha stage-web \
  --root "$CHORA_INSTALL_ROOT" \
  --stage "$CHORA_INSTALL_ROOT/build/web-workspace" \
  --expected-aggregate "$CHORA_BUNDLE_AGGREGATE"

(
  cd "$CHORA_INSTALL_ROOT/build/web-workspace"
  npm ci --ignore-scripts
  npm run web:build
)

mkdir -p \
  "$CHORA_INSTALL_ROOT/bin" \
  "$CHORA_INSTALL_ROOT/build/go-cache" \
  "$CHORA_INSTALL_ROOT/build/go-tmp"
(
  cd "$CHORA_INSTALL_ROOT/source"
  GOCACHE="$CHORA_INSTALL_ROOT/build/go-cache" \
  GOTMPDIR="$CHORA_INSTALL_ROOT/build/go-tmp" \
  go build -o "$CHORA_INSTALL_ROOT/bin/chora" ./cmd/chora
)

go run ./tools/localalpha init-data \
  --root "$CHORA_INSTALL_ROOT" \
  --data "$CHORA_DATA_ROOT" \
  --expected-aggregate "$CHORA_BUNDLE_AGGREGATE"
go run ./tools/localalpha verify --bundle "$CHORA_INSTALL_ROOT"
