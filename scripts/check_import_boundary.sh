#!/usr/bin/env bash
# Enforce Pitot's sensor-innocence invariant at build time: the sensor
# (measurement) package must never import the bridge (control) package.
# Measurement is innocent of control. This is a structural guarantee, not a
# convention, so a refactor cannot silently blur the boundary.
set -euo pipefail

MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE_PATH="github.com/operatorstack/pitot"

cd "$MODULE_DIR"

# Package -> forbidden dependency substring. Extend this table as new innocence
# boundaries are declared.
FORBIDDEN_PKG="${MODULE_PATH}/sensor"
FORBIDDEN_DEP="${MODULE_PATH}/bridge"

echo "check_import_boundary: ${FORBIDDEN_PKG} must not depend on ${FORBIDDEN_DEP}"

DEPS="$(go list -deps "$FORBIDDEN_PKG")"
if echo "$DEPS" | grep -qx "$FORBIDDEN_DEP"; then
  echo "FAIL: ${FORBIDDEN_PKG} imports ${FORBIDDEN_DEP} (directly or transitively)." >&2
  echo "      The sensor observes; it must not reach the control transport." >&2
  exit 1
fi

echo "OK: sensor-innocence boundary holds."
