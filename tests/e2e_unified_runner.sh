#!/usr/bin/env bash
# Real released-agent prompt-to-hook E2E. Direct hook invocation is forbidden.
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
PITOT_MAIN="$LAB_DIR/pitot/cmd/pitot"
if [[ ! -d "$PITOT_MAIN" ]]; then
  PITOT_MAIN="$LAB_DIR/cmd/pitot"
fi

BUILD_DIR="${RUNNER_TEMP:-$(mktemp -d)}/pitot-real-e2e-bin"
mkdir -p "$BUILD_DIR"
PITOT_BINARY="$BUILD_DIR/pitot"
WITNESS_BINARY="$BUILD_DIR/pitot-witness"
TESTROLE_BINARY="$BUILD_DIR/pitot-testrole"
if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
  PITOT_BINARY="${PITOT_BINARY}.exe"
  WITNESS_BINARY="${WITNESS_BINARY}.exe"
  TESTROLE_BINARY="${TESTROLE_BINARY}.exe"
  if [[ "$HOST" == "cursor" ]]; then
    # Cursor's supported Windows runtime is WSL. Keep both supervised
    # executables native to that runtime; crossing back into a Windows Pitot
    # process makes Cursor's hook pipe remain open after Pitot has returned.
    PITOT_BINARY="$BUILD_DIR/pitot-linux"
    WITNESS_BINARY="$BUILD_DIR/pitot-witness-linux"
    TESTROLE_BINARY="$BUILD_DIR/pitot-testrole-linux"
  fi
fi

if [[ "${RUNNER_OS:-}" == "Windows" && "$HOST" == "cursor" ]]; then
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$PITOT_BINARY" "$PITOT_MAIN"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WITNESS_BINARY" "$SCRIPT_DIR/witness/main.go"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$TESTROLE_BINARY" "$LAB_DIR/pitot/internal/testrole"
else
  go build -o "$PITOT_BINARY" "$PITOT_MAIN"
  go build -o "$WITNESS_BINARY" "$SCRIPT_DIR/witness/main.go"
  go build -o "$TESTROLE_BINARY" "$LAB_DIR/pitot/internal/testrole"
fi

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
if [[ "${RUNNER_OS:-}" == "Windows" && "$HOST" == "cursor" ]]; then
  # Run the complete controller in WSL so its temporary home, project, proxy,
  # hook, and child processes use the same native filesystem/process boundary.
  to_wsl_path() {
    local windows_path drive tail
    windows_path="$(cygpath -am "$1")"
    if [[ ! "$windows_path" =~ ^[A-Za-z]:/ ]]; then
      echo "ERROR: cannot map path into WSL: $windows_path" >&2
      return 1
    fi
    drive="${windows_path:0:1}"
    tail="${windows_path:2}"
    printf '/mnt/%s%s' "${drive,,}" "$tail"
  }
  WSL_ARGS=(
    --agent "$HOST"
    --platform "$PITOT_E2E_PLATFORM"
    --installation "$(to_wsl_path "$PITOT_INSTALL_RECEIPT")"
    --evidence "$(to_wsl_path "$PITOT_E2E_EVIDENCE")"
    --pitot "$(to_wsl_path "$PITOT_BINARY")"
    --witness "$(to_wsl_path "$WITNESS_BINARY")"
    --test-role "$(to_wsl_path "$TESTROLE_BINARY")"
  )
  if [[ -n "${PITOT_CAPTURE_OUTPUT:-}" ]]; then
    WSL_ARGS+=(--capture-output "$(to_wsl_path "$PITOT_CAPTURE_OUTPUT")")
  fi
  # Git Bash otherwise rewrites /mnt/<drive> arguments as paths beneath its
  # own installation before wsl.exe can receive them.
  export MSYS2_ARG_CONV_EXCL='*'
  DRIVER=(
    wsl.exe --distribution Ubuntu -- env
    PITOT_SOURCE_SHA="${PITOT_SOURCE_SHA:-}"
    python3 "$(to_wsl_path "$SCRIPT_DIR/real_agent_driver.py")" "${WSL_ARGS[@]}"
  )
  NEGATIVE_EVIDENCE="$(to_wsl_path "${PITOT_E2E_EVIDENCE}.wrong-response")"
fi

"${DRIVER[@]}"

"${DRIVER[@]}" \
  --evidence "$NEGATIVE_EVIDENCE" \
  --response-fault text \
  --expect-incompatible-response
echo "PASS: $HOST rejected incompatible proxy response evidence"
