// Package deepseek contains the native DeepSeek Harness shell integration.
package deepseek

import _ "embed"

const Version = "0.2.0-rc.2"

// Plugin is installed into the project's owned integration directory.
//
//go:embed pitot.mjs
var Plugin string
