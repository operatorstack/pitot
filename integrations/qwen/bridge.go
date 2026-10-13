// Package qwen exposes the shipped hook scripts for read-only setup checks.
package qwen

import _ "embed"

// BashBridge is the shell hook installed on macOS and Linux.
//
//go:embed PreToolUse
var BashBridge string

// NodeBridge is the portable hook installed with Node.js.
//
//go:embed PreToolUse.cjs
var NodeBridge string
