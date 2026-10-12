#!/usr/bin/env bash
# Real released-agent prompt-to-boundary E2E. Direct boundary invocation is forbidden.
set -euo pipefail

HOST="${1:-}"
if [[ -z "$HOST" ]]; then
  echo "ERROR: a supervised agent ID is required" >&2
  exit 2
fi
: "${PITOT_E2E_PLATFORM:?PITOT_E2E_PLATFORM is required}"
: "${PITOT_INSTALL_RECEIPT:?PITOT_INSTALL_RECEIPT is required}"
: "${PITOT_E2E_EVIDENCE:?PITOT_E2E_EVIDENCE is required}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAB_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PITOT_MAIN="$LAB_DIR/cmd/pitot"

BUILD_DIR="${RUNNER_TEMP:-$(mktemp -d)}/pitot-real-e2e-bin"
mkdir -p "$BUILD_DIR"
PITOT_BINARY="$BUILD_DIR/pitot"
WITNESS_BINARY="$BUILD_DIR/pitot-witness"
TESTROLE_BINARY="$BUILD_DIR/pitot-testrole"
if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
  PITOT_BINARY="${PITOT_BINARY}.exe"
  WITNESS_BINARY="${WITNESS_BINARY}.exe"
  TESTROLE_BINARY="${TESTROLE_BINARY}.exe"
fi

go build -o "$PITOT_BINARY" "$PITOT_MAIN"
go build -o "$WITNESS_BINARY" "$SCRIPT_DIR/witness/main.go"
go build -o "$TESTROLE_BINARY" "$LAB_DIR/internal/testrole"

DRIVER_ARGS=(
  --agent "$HOST"
  --platform "$PITOT_E2E_PLATFORM"
  --installation "$PITOT_INSTALL_RECEIPT"
  --evidence "$PITOT_E2E_EVIDENCE"
  --pitot "$PITOT_BINARY"
  --witness "$WITNESS_BINARY"
  --test-role "$TESTROLE_BINARY"
)
if [[ -n "${PITOT_CAPTURE_OUTPUT:-}" ]]; then
  DRIVER_ARGS+=(--capture-output "$PITOT_CAPTURE_OUTPUT")
fi

DRIVER=(python3 "$SCRIPT_DIR/real_agent_driver.py" "${DRIVER_ARGS[@]}")
NEGATIVE_EVIDENCE="${PITOT_E2E_EVIDENCE}.wrong-response"

"${DRIVER[@]}"

"${DRIVER[@]}" \
  --evidence "$NEGATIVE_EVIDENCE" \
  --response-fault text \
  --expect-incompatible-response
echo "PASS: $HOST rejected incompatible proxy response evidence"
