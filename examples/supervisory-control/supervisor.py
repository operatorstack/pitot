"""Application-owned plant and supervisor; independent of agent protocols."""

from __future__ import annotations

import hashlib
from pathlib import Path
import threading
import time


class Supervisor:
    """One delivery session. Permission and completed transitions are separate."""

    def __init__(self, project: Path, worker: str, commands: dict[str, str]):
        self.project = project
        self.worker = worker
        self.commands = commands
        self.state = "draft"
        self.revision = 0
        self.tested_revision: int | None = None
        self.tested_digest: str | None = None
        # A changed state invalidates permissions issued against older state.
        self.epoch = 0
        self.dispatched = False
        self.pending: dict[tuple[str, str], tuple[int, float]] = {}
        self.events: list[dict] = []
        self.lock = threading.RLock()

    def record(self, event: str, **details) -> None:
        self.events.append({"event": event, "state": self.state, **details})

    def permitted(self, actor: str, action: str) -> bool:
        if action == "dispatch":
            return actor == "codex" and not self.dispatched and self.state == "draft"
        if actor != self.worker:
            return False
        if action == "build":
            return self.state != "released"
        if action == "release":
            return self.state == "verified" and self.tested_revision == self.revision
        return False

    def admit(self, actor: str, command: str, action_id: str) -> tuple[bool, str]:
        with self.lock:
            action = next((a for a, c in self.commands.items() if c == command), None)
            allowed = bool(action and self.permitted(actor, action))
            reason = (
                f"SUPERVISOR_ALLOW {action} state={self.state}"
                if allowed
                else f"SUPERVISOR_DENY {action or 'unknown'} state={self.state}; "
                "build and obtain a current external test result before release"
            )
            self.record(
                "admission",
                actor=actor,
                action=action,
                allowed=allowed,
                action_id=action_id,
                reason=reason,
            )
            if allowed:
                # A short-lived, one-use reservation. A later event revokes it.
                self.pending[(actor, action)] = (self.epoch, time.monotonic() + 30)
            return allowed, reason

    def execute(self, actor: str, action: str) -> dict:
        with self.lock:
            # Consume permission once and check again: conditions may have changed
            # since the agent received an allow response.
            grant = self.pending.pop((actor, action), None)
            if (
                not grant
                or grant[0] != self.epoch
                or grant[1] < time.monotonic()
                or not self.permitted(actor, action)
            ):
                raise ValueError(
                    "No current admission reservation; propose the action again"
                )
            if action == "dispatch":
                self.dispatched = True
                self.record("dispatch_completed", actor=actor, worker=self.worker)
                return {"result": "DISPATCH_ACCEPTED"}
            if action == "build":
                # This is the actual toy build, not a state update at admission.
                (self.project / "artifact.txt").write_text("release candidate\n")
                self.revision += 1
                self.epoch += 1
                self.state = "built"
                self.tested_revision = self.tested_digest = None
                self.record("build_completed", actor=actor, revision=self.revision)
                return {"result": "BUILD_OK", "revision": self.revision}
            artifact = (self.project / "artifact.txt").read_bytes()
            # A matching revision alone is insufficient if the bytes were edited.
            if hashlib.sha256(artifact).hexdigest() != self.tested_digest:
                self.invalidate("artifact changed since test")
                raise ValueError("Tested artifact changed; release refused")
            (self.project / "released.txt").write_bytes(artifact)
            self.state = "released"
            self.epoch += 1
            self.record("release_completed", actor=actor, revision=self.revision)
            return {"result": "RELEASE_OK", "revision": self.revision}

    def observe_tests(self, revision: int, artifact: bytes) -> bool:
        """Only the application tester calls this; no agent event endpoint."""
        with self.lock:
            if self.state != "built" or revision != self.revision:
                self.record("stale_test_ignored", revision=revision)
                return False
            if (
                artifact != b"release candidate\n"
                or artifact != (self.project / "artifact.txt").read_bytes()
            ):
                self.invalidate("tests failed")
                return False
            self.tested_revision = revision
            self.tested_digest = hashlib.sha256(artifact).hexdigest()
            self.state = "verified"
            self.epoch += 1
            self.record("tests_passed", revision=revision)
            return True

    def invalidate(self, reason: str) -> None:
        with self.lock:
            if self.state in {"built", "verified"}:
                self.state = "built"
                self.tested_revision = self.tested_digest = None
                self.epoch += 1
                self.record("tests_invalidated", reason=reason)

    def test_artifact(self, revision: int) -> dict:
        """External tester runs after build, independently of the agent policy."""
        artifact = (self.project / "artifact.txt").read_bytes()
        passed = self.observe_tests(revision, artifact)
        if passed and revision == 1:
            # Reproducible environment change, outside either agent's control.
            self.invalidate("test environment changed")
        with self.lock:
            return {"result": "BUILD_OK", "revision": revision, "state": self.state}
