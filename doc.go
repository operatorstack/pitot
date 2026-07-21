// Package pitot is the module root for the Pitot sensor and control transport.
//
// Pitot converts host-specific coding-agent activity into a stable, language
// neutral event stream and carries correlated responses from a statically
// registered controller when a host is waiting synchronously. It reports what
// happened; the controller decides what it means.
//
// Module layout (see the public README for the full contract):
//
//	schema/       public event and response types + versioned constants
//	protocol/     newline-delimited JSON framing and state-machine helpers
//	adapters/     Claude Code, Cursor, and Codex host boundaries
//	sensor/       normalization and observation pipeline (decoder)
//	bridge/       controller routing and single-response transport
//	projection/   full, sha256, and omit content policies
//	conformance/  language-neutral fixtures and negative controls
//	examples/     token-meter and local-approval reference programs
//	cmd/pitot/    reference Go executable
//
// Architectural invariant: sensor/ must never import bridge/. Measurement is
// innocent of control. The invariant is enforced at build time by
// scripts/check_import_boundary.sh.
package pitot
