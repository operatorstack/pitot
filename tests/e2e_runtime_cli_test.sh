#!/usr/bin/env bash
set -euo pipefail

: "${PITOT_E2E_PLATFORM:?PITOT_E2E_PLATFORM is required}"
: "${PITOT_E2E_EVIDENCE:?PITOT_E2E_EVIDENCE is required}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAB_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="${RUNNER_TEMP:-$(mktemp -d)}/pitot-runtime-e2e-bin"
mkdir -p "$BUILD_DIR"
PITOT_BINARY="$BUILD_DIR/pitot"
TESTROLE_BINARY="$BUILD_DIR/pitot-testrole"
if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
  PITOT_BINARY="${PITOT_BINARY}.exe"
  TESTROLE_BINARY="${TESTROLE_BINARY}.exe"
fi
go build -o "$PITOT_BINARY" "$LAB_DIR/pitot/cmd/pitot"
go build -o "$TESTROLE_BINARY" "$LAB_DIR/pitot/internal/testrole"
PYTHON_COMMAND="python3"
if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
  PYTHON_COMMAND="python"
fi
"$PYTHON_COMMAND" "$SCRIPT_DIR/runtime_capability_driver.py" \
  --pitot "$PITOT_BINARY" \
  --test-role "$TESTROLE_BINARY" \
  --platform "$PITOT_E2E_PLATFORM" \
  --evidence "$PITOT_E2E_EVIDENCE"
