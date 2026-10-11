#!/bin/bash
set -euo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

go run ./cmd/generate-schema > schema.json

# TypeScript
TS_DIR="sdk/typescript/src"
mkdir -p "$TS_DIR"
npx -y quicktype -s schema schema.json -o "$TS_DIR/pitot.ts" --just-types
echo 'export * from "./pitot";
export * from "./runner";' > "$TS_DIR/index.ts"
echo "Generated TypeScript types in $TS_DIR"

# Python
PY_DIR="sdk/python/pitot"
mkdir -p "$PY_DIR"
npx -y quicktype -s schema schema.json -o "$PY_DIR/types.py"
# Create empty __init__.py if it doesn't exist
touch "$PY_DIR/__init__.py"
echo "Generated Python types in $PY_DIR"

# Rust
RS_DIR="sdk/rust/src"
mkdir -p "$RS_DIR"
npx -y quicktype -s schema schema.json -o "$RS_DIR/types.rs"
echo "Generated Rust types in $RS_DIR"

# C#
CS_DIR="sdk/csharp/Pitot"
mkdir -p "$CS_DIR"
npx -y quicktype -s schema schema.json -o "$CS_DIR/Types.cs" --namespace Pitot
echo "Generated C# types in $CS_DIR"

# Java
JAVA_DIR="sdk/java/src/main/java/com/operatorstack/pitot"
mkdir -p "$JAVA_DIR"
npx -y quicktype -s schema schema.json -o "$JAVA_DIR/Types.java" --package com.operatorstack.pitot
echo "Generated Java types in $JAVA_DIR"

rm schema.json
