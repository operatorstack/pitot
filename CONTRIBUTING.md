# Contributing to Pitot

Pitot is protocol-first infrastructure. Contributions should preserve the
separation between observation, controller authority, and host transport.

Before proposing an adapter or protocol change:

1. state the host capability or interoperability gap;
2. add or update language-neutral conformance fixtures;
3. preserve direct, estimated, and unavailable observation provenance;
4. keep content out of fault codes and diagnostics;
5. prove passive Consumers cannot reach the response channel; and
6. document any change to request correlation or timeout behavior.

## Development

The reference implementation is a Go module (`go 1.26`). From the repository
root:

```bash
go test ./...                          # unit and conformance suites
go build ./cmd/pitot                   # the reference executable
go test -tags windtunnel ./windtunnel/ # integrated sensor + bridge check
go test -run x -fuzz=FuzzDecode ./sensor
```

Conformance fixtures live under `conformance/fixtures/` as JSON Lines: add a
positive vector for every new adapter behavior and a negative control for every
boundary fault. The sensor package must never import the bridge package —
measurement stays innocent of control.

