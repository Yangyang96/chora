#!/usr/bin/env bash
set -euo pipefail

# The optional evaluator environment is supplied explicitly, never installed or
# activated by Chora. All fixtures and generated exports live in external temp.
: "${CHORA_CERNORA_PYTHON:?Set CHORA_CERNORA_PYTHON to an external Python 3.12/3.13 environment with Cernora inspection support}"
root="$(cd "$(dirname "$0")/.." && pwd -P)"
PYTHONDONTWRITEBYTECODE=1 "$CHORA_CERNORA_PYTHON" -B "$root/tools/cernora/test_adapter.py"
