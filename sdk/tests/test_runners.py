"""Shared Consumer/Controller conformance suite for the Pitot SDK runners.

Every first-class language (Python, TypeScript, Go, Rust) is driven through the
same fixture cases so their observable protocol behavior stays identical:

  * controller stream  - N requests produce N responses, in request order
  * malformed input    - a bad line is reported and skipped, stream continues
  * deny outcome       - the handler's deny is faithfully serialized
  * consumer stream    - events are delivered and stdout stays protocol-clean

Paths are resolved relative to this file (no hardcoded monorepo depth), and each
language is skipped cleanly when its toolchain is unavailable.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SDK_DIR = Path(__file__).resolve().parent.parent
FIXTURES_DIR = SDK_DIR / "tests" / "_fixtures"


def go_module_dir() -> Path | None:
    """Locate the Go module root in either the monorepo or flattened layout."""
    for candidate in (SDK_DIR.parent, SDK_DIR.parent.parent / "pitot"):
        if (candidate / "go.mod").exists():
            return candidate
    return None


def control_request(action_id: str) -> str:
    return json.dumps(
        {
            "pitot_version": "1",
            "type": "control.requested",
            "kind": "test.approval",
            "action_id": action_id,
            "data": {"test_field": "value"},
        }
    )


def action_event(action_id: str) -> str:
    return json.dumps(
        {
            "pitot_version": "1",
            "type": "action.requested",
            "host": {"name": "test-host"},
            "observation": {"fidelity": "direct", "source": "host_hook"},
            "action": {"id": action_id, "kind": "shell"},
        }
    )


def run_lines(command, cwd, env, lines):
    proc = subprocess.Popen(
        command,
        cwd=str(cwd),
        env=env,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    stdout, stderr = proc.communicate("\n".join(lines) + "\n", timeout=120)
    return proc.returncode, stdout, stderr


def wire_rows():
    path = SDK_DIR.parent / "conformance" / "fixtures" / "wire.jsonl"
    return [json.loads(line) for line in path.read_text().splitlines()]


def wire_bytes(row):
    return row["raw"].encode("utf-8") if "raw" in row else bytes.fromhex(row["raw_hex"])


def run_bytes(command, cwd, env, payload):
    result = subprocess.run(
        command, cwd=str(cwd), env=env, input=payload, capture_output=True, timeout=120
    )
    return (
        result.returncode,
        result.stdout.decode("utf-8", errors="replace"),
        result.stderr.decode("utf-8", errors="replace"),
    )


# Fixture sources. Handlers key their outcome off PITOT_TEST_OUTCOME so a single
# fixture serves both allow and deny cases.

PY_CONTROLLER = """import os
from pitot.runner import run_controller, allow, deny, Outcome
from pitot.types import ControlRequested

def handler(req: ControlRequested):
    print("handled", file=__import__("sys").stderr)
    if os.environ.get("PITOT_TEST_OUTCOME") == "invalid":
        return Outcome("invalid")
    if os.environ.get("PITOT_TEST_OUTCOME") == "oversize":
        return allow("x" * (4 * 1024 * 1024))
    if os.environ.get("PITOT_TEST_OUTCOME") == "no-message":
        return allow()
    if os.environ.get("PITOT_TEST_OUTCOME") == "deny":
        return deny("test denied")
    return allow("test approved")

if __name__ == "__main__":
    run_controller("test-controller", handler)
"""

PY_CONSUMER = """import sys
from pitot.runner import run_consumer
from pitot.types import Event

def handler(event: Event):
    print("consumed", event.type, file=sys.stderr)

if __name__ == "__main__":
    run_consumer(handler)
"""

TS_CONTROLLER = """import { runController, allow, deny } from '../../typescript/src/runner';
import { ControlRequested } from '../../typescript/src/pitot';

runController("test-controller", async (req: ControlRequested) => {
    console.error("handled");
    if (process.env.PITOT_TEST_OUTCOME === "invalid") return { outcome: "invalid" } as any;
    if (process.env.PITOT_TEST_OUTCOME === "oversize") return allow("x".repeat(4 * 1024 * 1024));
    if (process.env.PITOT_TEST_OUTCOME === "no-message") return allow();
    if (process.env.PITOT_TEST_OUTCOME === "deny") return deny("test denied");
    return allow("test approved");
});
"""

TS_CONSUMER = """import { runConsumer } from '../../typescript/src/runner';
import { Event } from '../../typescript/src/pitot';

runConsumer(async (event: Event) => {
    console.error("consumed", event.type);
});
"""

GO_CONTROLLER = """package main

import (
\t"os"
\t"fmt"
\t"strings"

\t"github.com/operatorstack/pitot/schema"
\t"github.com/operatorstack/pitot/sdk"
)

func main() {
\tsdk.RunController("test-controller", func(req schema.ControlRequested) sdk.Outcome {
\t\tfmt.Fprintln(os.Stderr, "handled")
\t\tif os.Getenv("PITOT_TEST_OUTCOME") == "invalid" { return sdk.Outcome{Outcome:"invalid"} }
\t\tif os.Getenv("PITOT_TEST_OUTCOME") == "oversize" { return sdk.Allow(strings.Repeat("x", 4 * 1024 * 1024)) }
\t\tif os.Getenv("PITOT_TEST_OUTCOME") == "no-message" { return sdk.Allow("") }
\t\tif os.Getenv("PITOT_TEST_OUTCOME") == "deny" {
\t\t\treturn sdk.Deny("test denied")
\t\t}
\t\treturn sdk.Allow("test approved")
\t})
}
"""

GO_CONSUMER = """package main

import (
\t"fmt"
\t"os"

\t"github.com/operatorstack/pitot/schema"
\t"github.com/operatorstack/pitot/sdk"
)

func main() {
\tsdk.RunConsumer(func(event schema.Event) {
\t\tfmt.Fprintf(os.Stderr, "consumed %s\\n", event.Type)
\t})
}
"""

RUST_CONTROLLER = """use pitot::{run_controller, allow, deny, ControlRequested, Outcome};

fn handler(_req: ControlRequested) -> Outcome {
    eprintln!("handled");
    if std::env::var("PITOT_TEST_OUTCOME").as_deref() == Ok("invalid") {
        return Outcome{outcome: "invalid".to_string(),message:None};
    }
    if std::env::var("PITOT_TEST_OUTCOME").as_deref() == Ok("oversize") { return allow(Some("x".repeat(4 * 1024 * 1024))); }
    if std::env::var("PITOT_TEST_OUTCOME").as_deref() == Ok("no-message") { return allow(None); }
    if std::env::var("PITOT_TEST_OUTCOME").as_deref() == Ok("deny") {
        return deny(Some("test denied".to_string()));
    }
    allow(Some("test approved".to_string()))
}

fn main() {
    run_controller("test-controller", Box::new(handler));
}
"""

RUST_CONSUMER = """use pitot::{run_consumer, Event};

fn handler(event: Event) {
    eprintln!("consumed {}", event.event_type);
}

fn main() {
    run_consumer(Box::new(handler));
}
"""


class RunnerContract:
    """Mixin defining the shared conformance cases.

    Subclasses provide controller_command()/consumer_command() returning
    (command, cwd, env) and available() returning (bool, reason).
    """

    def setUp(self):
        ok, reason = self.available()
        if not ok:
            self.skipTest(reason)
        FIXTURES_DIR.mkdir(parents=True, exist_ok=True)

    @classmethod
    def tearDownClass(cls):
        # Generated fixtures must never linger under the published sdk/ tree, or
        # they would show up as projection drift in UPSTREAM.json.
        shutil.rmtree(FIXTURES_DIR, ignore_errors=True)

    def _wire_cases(self, envelope):
        return [row for row in wire_rows() if row["envelope"] == envelope]

    def test_raw_consumer_admission(self):
        cmd, cwd, env = self.consumer_command()
        for accepted in (False, True):
            rows = [
                row for row in self._wire_cases("event") if row["valid"] == accepted
            ]
            payload = b"\n".join(wire_bytes(row) for row in rows)
            payload += b"\n" + action_event("sentinel").encode() + b"\n"
            code, stdout, stderr = run_bytes(cmd, cwd, env, payload)
            self.assertEqual(code, 0, stderr)
            self.assertEqual(stdout, "", stderr)
            self.assertEqual(
                stderr.count("consumed"), len(rows) + 1 if accepted else 1, stderr
            )
            self.assertNotIn("sample command", stderr)

    def test_raw_controller_admission(self):
        cmd, cwd, env = self.controller_command()
        for accepted in (False, True):
            rows = [
                row for row in self._wire_cases("request") if row["valid"] == accepted
            ]
            payload = b"\n".join(wire_bytes(row) for row in rows)
            payload += b"\n" + control_request("sentinel").encode() + b"\n"
            code, stdout, stderr = run_bytes(cmd, cwd, env, payload)
            self.assertEqual(code, 0, stderr)
            expected = len(rows) + 1 if accepted else 1
            self.assertEqual(stderr.count("handled"), expected, stderr)
            self.assertEqual(len(stdout.splitlines()), expected, stdout + stderr)
            self.assertEqual(
                json.loads(stdout.splitlines()[-1])["action_id"], "sentinel"
            )

    def test_invalid_decision_never_reaches_stdout(self):
        cmd, cwd, env = self.controller_command()
        env = dict(env, PITOT_TEST_OUTCOME="invalid")
        code, stdout, stderr = run_lines(cmd, cwd, env, [control_request("test")])
        self.assertEqual(code, 0, stderr)
        self.assertEqual(stderr.count("handled"), 1, stderr)
        self.assertEqual(stdout, "", stdout)
        self.assertIn("error", stderr)

    def test_oversized_response_never_reaches_stdout(self):
        cmd, cwd, env = self.controller_command()
        env = dict(env, PITOT_TEST_OUTCOME="oversize")
        code, stdout, stderr = run_lines(cmd, cwd, env, [control_request("test")])
        self.assertEqual(code, 0, stderr)
        self.assertEqual(stderr.count("handled"), 1, stderr)
        self.assertEqual(stdout, "")
        self.assertIn("error", stderr)

    def test_absent_response_message_is_omitted(self):
        cmd, cwd, env = self.controller_command()
        env = dict(env, PITOT_TEST_OUTCOME="no-message")
        code, stdout, stderr = run_lines(cmd, cwd, env, [control_request("test")])
        self.assertEqual(code, 0, stderr)
        self.assertNotIn("message", json.loads(stdout))

    def test_frame_limits_and_final_eof(self):
        cmd, cwd, env = self.controller_command()
        prefix = b'{"pitot_version":"1","type":"control.requested","kind":"test","action_id":"boundary","data":"'
        suffix = b'"}'
        boundary = (
            prefix + b"x" * (4 * 1024 * 1024 - len(prefix) - len(suffix)) + suffix
        )
        payload = (
            boundary + b"\n" + boundary + b" \n" + control_request("sentinel").encode()
        )
        code, stdout, stderr = run_bytes(cmd, cwd, env, payload)
        self.assertEqual(code, 0, stderr)
        self.assertEqual(stderr.count("handled"), 2, stderr)
        self.assertEqual(
            [json.loads(line)["action_id"] for line in stdout.splitlines()],
            ["boundary", "sentinel"],
        )

    # --- Controller cases -------------------------------------------------

    def test_controller_stream_preserves_order(self):
        cmd, cwd, env = self.controller_command()
        ids = ["act_1", "act_2", "act_3"]
        code, stdout, stderr = run_lines(
            cmd, cwd, env, [control_request(i) for i in ids]
        )
        self.assertEqual(code, 0, f"{self.language}: process failed: {stderr}")
        lines = [l for l in stdout.strip().split("\n") if l]
        self.assertEqual(
            len(lines),
            len(ids),
            f"{self.language}: expected one response per request:\n{stdout}\n{stderr}",
        )
        for expected, raw in zip(ids, lines):
            resp = json.loads(raw)
            self.assertEqual(resp["pitot_version"], "1")
            self.assertEqual(resp["type"], "control.response")
            self.assertEqual(resp["controller_id"], "test-controller")
            self.assertEqual(
                resp["action_id"], expected, f"{self.language}: responses out of order"
            )
            self.assertEqual(resp["outcome"], "allow")
            self.assertEqual(resp["message"], "test approved")

    def test_controller_skips_malformed_line(self):
        cmd, cwd, env = self.controller_command()
        lines = [
            control_request("act_1"),
            "{ this is not valid json",
            control_request("act_2"),
        ]
        code, stdout, stderr = run_lines(cmd, cwd, env, lines)
        self.assertEqual(
            code,
            0,
            f"{self.language}: malformed input must not crash the runner: {stderr}",
        )
        responses = [json.loads(l) for l in stdout.strip().split("\n") if l]
        self.assertEqual(
            [r["action_id"] for r in responses],
            ["act_1", "act_2"],
            f"{self.language}: malformed line should be skipped, not emit a response:\n{stdout}",
        )

    def test_controller_deny(self):
        cmd, cwd, env = self.controller_command()
        env = dict(env)
        env["PITOT_TEST_OUTCOME"] = "deny"
        code, stdout, stderr = run_lines(cmd, cwd, env, [control_request("act_1")])
        self.assertEqual(code, 0, f"{self.language}: {stderr}")
        responses = [json.loads(l) for l in stdout.strip().split("\n") if l]
        self.assertEqual(len(responses), 1)
        self.assertEqual(responses[0]["outcome"], "deny")
        self.assertEqual(responses[0]["message"], "test denied")

    # --- Consumer cases ---------------------------------------------------

    def test_consumer_stream_keeps_stdout_clean(self):
        cmd, cwd, env = self.consumer_command()
        code, stdout, stderr = run_lines(
            cmd, cwd, env, [action_event("act_1"), action_event("act_2")]
        )
        self.assertEqual(code, 0, f"{self.language}: {stderr}")
        self.assertEqual(
            stdout.strip(),
            "",
            f"{self.language}: consumer must not write to stdout:\n{stdout}",
        )
        self.assertEqual(
            stderr.count("consumed"),
            2,
            f"{self.language}: expected two delivered events:\n{stderr}",
        )


def write_fixture(name: str, contents: str) -> Path:
    FIXTURES_DIR.mkdir(parents=True, exist_ok=True)
    path = FIXTURES_DIR / name
    path.write_text(contents)
    return path


class TestPythonRunner(RunnerContract, unittest.TestCase):
    language = "python"

    def available(self):
        return (shutil.which("python3") is not None, "python3 not available")

    def _env(self):
        env = os.environ.copy()
        env["PYTHONPATH"] = str(SDK_DIR / "python")
        return env

    def controller_command(self):
        path = write_fixture("py_controller.py", PY_CONTROLLER)
        return ["python3", str(path)], FIXTURES_DIR, self._env()

    def consumer_command(self):
        path = write_fixture("py_consumer.py", PY_CONSUMER)
        return ["python3", str(path)], FIXTURES_DIR, self._env()


class TestOptimizedPythonRunner(TestPythonRunner):
    language = "python-optimized"

    def controller_command(self):
        cmd, cwd, env = super().controller_command()
        return [cmd[0], "-O", *cmd[1:]], cwd, env

    def consumer_command(self):
        cmd, cwd, env = super().consumer_command()
        return [cmd[0], "-O", *cmd[1:]], cwd, env


class TestTypeScriptRunner(RunnerContract, unittest.TestCase):
    language = "typescript"

    def available(self):
        return (shutil.which("npx") is not None, "npx not available")

    def controller_command(self):
        path = write_fixture("ts_controller.ts", TS_CONTROLLER)
        return ["npx", "tsx", str(path)], SDK_DIR / "typescript", os.environ.copy()

    def consumer_command(self):
        path = write_fixture("ts_consumer.ts", TS_CONSUMER)
        return ["npx", "tsx", str(path)], SDK_DIR / "typescript", os.environ.copy()


class TestGoRunner(RunnerContract, unittest.TestCase):
    language = "go"

    def available(self):
        if shutil.which("go") is None:
            return (False, "go not available")
        if go_module_dir() is None:
            return (False, "go module not found in expected layout")
        return (True, "")

    def controller_command(self):
        path = write_fixture("go_controller.go", GO_CONTROLLER)
        return ["go", "run", str(path)], go_module_dir(), os.environ.copy()

    def consumer_command(self):
        path = write_fixture("go_consumer.go", GO_CONSUMER)
        return ["go", "run", str(path)], go_module_dir(), os.environ.copy()


class TestRustRunner(RunnerContract, unittest.TestCase):
    language = "rust"

    def available(self):
        if shutil.which("cargo") is None:
            return (False, "cargo not available")
        if not (SDK_DIR / "rust" / "Cargo.toml").exists():
            return (False, "rust SDK crate missing Cargo.toml")
        return (True, "")

    def _build(self, fixture_name: str, contents: str) -> Path:
        # Build a throwaway crate that path-depends on the Rust SDK, then run
        # its compiled binary. Cached across cases within the process.
        project = Path(tempfile.mkdtemp(prefix="pitot-rust-conf-"))
        self.addCleanup(shutil.rmtree, project, ignore_errors=True)
        crate = (SDK_DIR / "rust").resolve()
        (project / "src").mkdir(parents=True)
        (project / "src" / "main.rs").write_text(contents)
        (project / "Cargo.toml").write_text(
            '[package]\nname = "conf"\nversion = "0.0.0"\nedition = "2021"\n\n'
            f'[dependencies]\npitot = {{ path = "{crate}" }}\n'
        )
        build = subprocess.run(
            ["cargo", "build", "--quiet"],
            cwd=str(project),
            capture_output=True,
            text=True,
            timeout=300,
        )
        if build.returncode != 0:
            self.fail(f"rust build failed: {build.stderr}")
        return project / "target" / "debug" / "conf"

    def controller_command(self):
        binary = self._build("controller", RUST_CONTROLLER)
        return [str(binary)], SDK_DIR / "rust", os.environ.copy()

    def consumer_command(self):
        binary = self._build("consumer", RUST_CONSUMER)
        return [str(binary)], SDK_DIR / "rust", os.environ.copy()


if __name__ == "__main__":
    unittest.main()
