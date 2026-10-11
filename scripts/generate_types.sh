#!/bin/bash
set -euo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"
MODE=${1:-write}
if [[ "$#" -gt 1 || ("$MODE" != write && "$MODE" != --check) ]]; then
  echo 'Usage: bash scripts/generate_types.sh [--check]' >&2
  exit 2
fi

# Resolve only the repository's installed, locked generator. Never download a
# generator while producing or checking SDK source.
node - << 'JS'
const fs = require('node:fs');
const expected = require('./package.json').devDependencies.quicktype;
const path = './node_modules/quicktype/package.json';
if (!fs.existsSync(path) || JSON.parse(fs.readFileSync(path)).version !== expected) {
  console.error(`Install locked quicktype ${expected} with npm ci --ignore-scripts first.`);
  process.exit(1);
}
JS
QUICKTYPE="$REPO_ROOT/node_modules/quicktype/dist/index.js"
STAGING=$(mktemp -d)
trap 'rm -rf "$STAGING"' EXIT
GOWORK=off go run ./cmd/generate-schema > "$STAGING/schema.json"

# Generate every output successfully before replacing any repository file.
mkdir -p "$STAGING/typescript" "$STAGING/python" "$STAGING/rust" "$STAGING/csharp" "$STAGING/java"
node "$QUICKTYPE" -s schema "$STAGING/schema.json" -o "$STAGING/typescript/pitot.ts" --just-types
printf 'export * from "./pitot";\nexport * from "./runner";\n' > "$STAGING/typescript/index.ts"
node "$QUICKTYPE" -s schema "$STAGING/schema.json" -o "$STAGING/python/types.py"
node "$QUICKTYPE" -s schema "$STAGING/schema.json" -o "$STAGING/rust/types.rs"
node "$QUICKTYPE" -s schema "$STAGING/schema.json" -o "$STAGING/csharp/Types.cs" --namespace Pitot
node "$QUICKTYPE" -s schema "$STAGING/schema.json" -o "$STAGING/java/Types.java" --package com.operatorstack.pitot

SOURCES=(typescript/pitot.ts typescript/index.ts python/types.py rust/types.rs csharp/Types.cs)
TARGETS=(sdk/typescript/src/pitot.ts sdk/typescript/src/index.ts sdk/python/pitot/types.py sdk/rust/src/types.rs sdk/csharp/Pitot/Types.cs)
# Java emits a separate source file for each reflected type.
JAVA_DIR=sdk/java/src/main/java/com/operatorstack/pitot
for file in "$STAGING/java/"*.java; do
  name=$(basename "$file")
  SOURCES+=("java/$name")
  TARGETS+=("$JAVA_DIR/$name")
done
FAILED=0
# This directory is owned entirely by the Java type generator.
for file in "$JAVA_DIR/"*.java; do
  [[ -e "$file" ]] || continue
  if [[ ! -e "$STAGING/java/$(basename "$file")" ]]; then
    if [[ "$MODE" == --check ]]; then
      echo "Stale generated SDK file: $file" >&2
      FAILED=1
    else
      rm "$file"
    fi
  fi
done
for i in "${!SOURCES[@]}"; do
  if [[ "$MODE" == --check ]]; then
    if ! cmp -s "$STAGING/${SOURCES[$i]}" "${TARGETS[$i]}"; then
      echo "Generated SDK drift: ${TARGETS[$i]}" >&2
      FAILED=1
    fi
  else
    mkdir -p "$(dirname "${TARGETS[$i]}")"
    cp "$STAGING/${SOURCES[$i]}" "${TARGETS[$i]}"
  fi
done
if [[ "$MODE" == write ]]; then
  touch sdk/python/pitot/__init__.py
fi
exit "$FAILED"
