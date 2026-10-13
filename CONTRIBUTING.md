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

Runtime benchmarks cover 1 KiB, 64 KiB and 1 MiB content under each projection
policy, with one, four and sixteen consumers:

```bash
go test ./runtime -run '^$' -bench 'Benchmark(ProjectEvent|DeliverEvent)' -benchmem -count 3
```

Compare revisions with the same Go version, machine, workload and benchmark
settings. Delivery benchmarks measure validation and successful queue admission;
controller cases also include a real supervised subprocess round trip. They do
not measure Consumer application processing, HTTP transport or child-process
allocations. Retain regressions alongside gains; timing is diagnostic rather
than a portable CI pass threshold.

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

## Generation and static checks

After installing the locked npm dependencies and the pinned Python tools above:

```bash
npm run generate        # regenerate SDK types using the locked quicktype tool
npm run generate:check  # compare with staged output without writing SDK files
npm run check:static    # Go vet, Python correctness, shell and JavaScript syntax
```

Generation uses quicktype 26.0.0 and its locked dependency graph. It does not
resolve packages or download tools. All language outputs are staged successfully
before the write command copies them into the SDKs; check mode reports missing
or changed outputs without modifying them. The schema and generated SDK bytes
remain the contract; regeneration is checked in CI.

The static gate checks authored files. Python checks select syntax and unresolved
name/export errors (E9, F821, F822, F823); formatting remains a separate gate.

## Refreshing agent endpoint provenance

Codex, Qwen and DeepSeek install into fresh, owned npm prefixes. Their receipts
bind the package tree (names, types, bytes and platform metadata), the selected
Node binary and version, and the package's JavaScript entrypoint. Version probes
and verification sessions invoke that absolute Node/JavaScript pair. Qwen's
managed-update pin keeps execution in the recorded package; a seeded external
update proves the selection test can detect an unpinned launch.

The driver validates the receipt before creating session fixtures and rechecks
installation identity around execution. A changed package or Node binary,
malformed receipt, unexpected version banner, or failed probe rejects admission.
Bounded failed-probe streams remain local; public diagnostics contain only
failure reasons, byte counts and digests. Other agents retain entrypoint-only
identity checks.

These are observations on a trusted runner, not publisher authentication or an
atomic filesystem snapshot. POSIX mode bits and Windows readonly attributes are
recorded; Windows ACLs, operating-system libraries and changes between checks
are outside the guarantee. Historical captures retain their original scope.

Protocol documentation is not a verification authority. When a pinned agent
version changes, dispatch the existing **Pitot E2E** workflow with
`capture_provenance=true` on the reviewed producer commit. Wait for the entire
workflow to succeed, then select that exact run and source commit explicitly:

Capture dispatch schedules candidate agent and runtime tests from the validated
catalog even when the previous captures are stale. This does not accept those
captures or bypass ordinary verification. Render the generated surfaces after
the new record has been acquired:

```bash
python3 scripts/pitot_adapter_supervisor.py capture-merge \
  --repository operatorstack/pitot \
  --run-id RUN_ID \
  --source-sha FULL_REVIEWED_COMMIT_SHA \
  --output tests/endpoint-provenance.json
python3 scripts/pitot_adapter_supervisor.py render
python3 scripts/pitot_adapter_supervisor.py check
```

The merger uses `GITHUB_TOKEN` or your authenticated GitHub CLI session. It
fetches the source files, current-attempt jobs and artifact metadata through
GitHub, checks downloaded archive digests, and correlates each passing result
with its capture and successful agent/platform job. All cells must come from
the selected current attempt; a partial rerun cannot borrow earlier cells.
Local JSON files cannot authorize a capture refresh. Acquisition or validation
failure preserves the accepted file; success replaces it atomically.

The schema5 record adds portable installation identity to the wire cells and
retains the
`capture-direct/v1` producer profile to the source run, job/check-run IDs and
artifact digests. Its sorted file hashes cover the capture orchestration,
proxies, witness and canary helpers, catalog-declared integrations, agent
test wrappers, and `.gitattributes`. Acquisition checks these files against
regular blobs in the approved Git tree and checks the local bytes again before publication. Ordinary
verification compares the saved hashes with the supplied checkout; a later
helper edit requires fresh capture evidence. JSON-only validation checks record
structure and does not establish current checkout identity.

The versioned checkout policy keeps text files at LF on all platforms so local
bytes match the approved Git blobs, including Windows checkouts with
`core.autocrlf=true`. Binary files retain Git's automatic detection. Identity
checks compare raw bytes; they never normalize line endings to hide a mismatch.
Conflicting local attributes or filters can still cause a refusal.

Paths must be canonical relative paths. Local file or parent symlinks, Windows
reparse points, nonregular files, and approved-tree symlinks or submodules are
refused. Source files are limited to 1 MB and the selected tree listing to 1,000
entries; truncated listings are refused. These checks cover file bytes, not
executable permissions or a filesystem frozen against concurrent changes.

Review the redacted wire diff, source index and producer hashes before committing.
Ordinary CI never rewrites the record. Historical schema2 and schema3 captures
remain readable at their original scopes; they gain no retrospective source or
current-helper authentication.

Source acquisition trusts GitHub and the reviewed producer workflow on its
runner. Artifact metadata does not cryptographically attest the producing
job: the job association also depends on that workflow's unique artifact names
and host-assigned check-run context. This does not authenticate agent
publishers, transitive compiled Go dependencies, or every program loaded by an
agent. A copied source index cannot
authenticate itself; fresh acquisition requires the selected run to remain
available through GitHub.

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
