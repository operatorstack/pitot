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

## Adding a host adapter

A new host is admissible only if its blocking boundary can complete Pitot's
causal loop: a proposed command reaches a Controller, allow/deny applies before
execution, and a denied action returns to the model so the agent continues. If
the host can only abort on denial, it is not yet supervisable — say so in the
research note and stop there (see `docs/devin-adapter-research.md` for a worked
rejection-then-acceptance across two of the same host's surfaces).

Adapters come in two transport classes:

- **One-shot hook** (Claude, Codex, Cursor, Copilot, Gemini, Kimi, OpenCode,
  Pi, Qwen): the host invokes `pitot hook HOST` at a synchronous pre-execution
  boundary, Pitot answers by exit code or a native block envelope, and the
  process ends. Add a decoder that normalizes the host payload to a `shell`
  action and an encoder for the host's native deny shape.
- **Stateful transport** (Devin, over ACP): Pitot launches and drives a
  long-lived host process, correlates each command to a permission request, and
  maps allow/deny onto the host's typed one-shot permission options. Never
  select persistent or bypass options.

Either way: add positive and negative conformance fixtures, prove passive
Consumers cannot reach the response channel, and register the adapter in the
supervised inventory so it joins the cross-platform E2E matrix (each adapter is
verified allow-and-deny on Ubuntu, macOS, and Windows before it ships).

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

## Formatting

Formatting is checked in CI. Install the Go toolchain from `go.mod`, Python
3.11 or newer, Node.js 22 or newer, and Rust 1.97.1 with its rustfmt component.
From the repository root:

```bash
python3 -m venv .venv
source .venv/bin/activate
python -m pip install -r requirements-format.txt
npm ci --ignore-scripts
go install mvdan.cc/sh/v3/cmd/shfmt@v3.14.1
export PATH="$(go env GOPATH)/bin:$PATH"
npm run format        # write formatting changes
npm run format:check  # fail on drift without writing
```

The commands use gofmt, Ruff 0.17.0, Prettier 3.9.10, rustfmt 1.9.0, and shfmt
3.14.1 on tracked and unignored authored Go, Python, JS/TS, Rust, shell,
JSON/YAML, and Markdown files. The Python environment must be active when
running the npm commands. On Windows, activate `.venv/Scripts/activate` from
Git Bash and ensure `python3` resolves to that environment.

Generated SDK types and exports, the generated E2E workflow, README's generated
supervisor block (and therefore the whole README), captured JSON evidence, and
the npm lockfile retain their existing generator or capture ownership. The
exclusions are explicit in `scripts/format.py`; generation stability remains a
separate CI check. Rust formatting uses stdin so formatting an authored module
cannot traverse into generated types. TOML manifests and JSON Lines conformance
fixtures are outside this initial formatting gate.

## Refreshing agent endpoint provenance

Protocol documentation is not a verification authority. When a pinned agent
version changes, dispatch the existing **Pitot E2E** workflow with
`capture_provenance=true`. Download all `pitot-endpoint-capture-*` artifacts,
then merge the complete set locally:

```bash
python3 scripts/pitot_adapter_supervisor.py capture-merge \
  --captures /path/to/downloaded-artifacts \
  --output tests/endpoint-provenance.json
python3 scripts/pitot_adapter_supervisor.py check
```

The merge refuses partial, duplicate, mixed-version, unsuccessful, or
fabricated captures. Review the redacted 30-cell wire diff before committing
it; ordinary CI verifies the committed fixtures and never rewrites them.

## Repository ownership

This repository owns Pitot source, maintenance tooling, CI, and releases.
`UPSTREAM.json` records the historical import; it is not an active sync contract.
Changes are made directly here.

Maintenance commands run from this repository root:

```bash
bash scripts/check_import_boundary.sh
bash scripts/generate_types.sh
python3 scripts/pitot_adapter_supervisor.py render
python3 scripts/pitot_adapter_supervisor.py check
python3 -m unittest discover -s tests -p 'test_*.py'
python3 -m unittest discover -s sdk/tests -p 'test_*.py'
```

The Verify workflow installs the Go, Python, Node, and Rust toolchains for these
checks. Pitot E2E verifies real pinned agents across the supported platforms.
The release workflow continues using the existing repository variables.
