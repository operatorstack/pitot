// Package examples documents the two reference programs that demonstrate the
// Consumer/Controller distinction, mirroring the public README:
//
//	token-meter    a passive Consumer that counts tokens without recording prompts
//	local-approval a Controller that returns one correlated response per request
//
// Each is a standalone main package under this directory so it can be built and
// run without a Pitot SDK. A Consumer reads JSON Lines from standard input; a
// Controller returns one schema-valid response on its output channel.
package examples
