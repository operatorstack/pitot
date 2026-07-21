// Package windtunnel holds Pitot's integrated HEAD-to-HEAD check, which drives a
// host hook event through the sensor and a control request through the bridge to
// prove the two planes agree at workspace HEAD.
//
// The check itself lives in windtunnel_test.go behind the `windtunnel` build tag
// so it runs only as its dedicated target:
//
//	go test -tags windtunnel ./windtunnel/
//
// This file carries no tag so the package always compiles under `go test ./...`.
package windtunnel
