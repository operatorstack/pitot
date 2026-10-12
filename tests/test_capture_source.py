"""Controlled API fixtures exercise admission; live capture evidence is separate."""

from __future__ import annotations

import base64
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile
from urllib.parse import unquote

from test_e2e_reporting import _result, reporter

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import pitot_capture_source as source
import pitot_adapter_supervisor as supervisor

REPOSITORY = "operatorstack/pitot"
SHA = "a" * 40


def bundle(frame, result):
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("capture.json", json.dumps(frame))
        archive.writestr("result.json", json.dumps(result))
    return output.getvalue()


class FixtureAPI:
    def __init__(self):
        self.manifest = json.loads((ROOT / "adapter-verification.json").read_text())
        self.run = {
            "id": 1,
            "repository": {"id": 99, "full_name": REPOSITORY},
            "head_repository": {"id": 99},
            "head_sha": SHA,
            "workflow_id": 7,
            "path": source.WORKFLOW,
            "event": "workflow_dispatch",
            "status": "completed",
            "conclusion": "success",
            "run_attempt": 1,
        }
        self.jobs, self.artifacts, self.archives = [], [], {}
        self.frames, self.results = [], []
        for agent in self.manifest["agents"]:
            for platform in self.manifest["platforms"]:
                agent_id, platform_id = agent["id"], platform["id"]
                index = len(self.artifacts)
                check_id = 1000 + index
                result = _result(agent_id, platform_id)
                cell = copy.deepcopy(
                    next(
                        item
                        for item in reporter.ENDPOINT_PROVENANCE["cells"]
                        if item["agent"] == agent_id and item["platform"] == platform_id
                    )
                )
                result["endpoint"]["fixture"] = "candidate"
                frame = {
                    "schema_version": 2,
                    "accepted": True,
                    "cell": cell,
                    "source": {
                        "repo": REPOSITORY,
                        "run_id": 1,
                        "run_attempt": 1,
                        "source_sha": SHA,
                        "checkout_sha": SHA,
                        "check_run_id": check_id,
                    },
                }
                artifact_id = 3000 + index
                self.jobs.append(
                    {
                        "id": 2000 + index,
                        "run_id": 1,
                        "run_attempt": 1,
                        "head_sha": SHA,
                        "check_run_url": f"https://api.github.com/repos/{REPOSITORY}/check-runs/{check_id}",
                        "name": f"verify-agents (cell) / {agent_id} ({platform_id})",
                        "labels": [platform["runner"]],
                        "runner_id": 4000 + index,
                        "runner_name": "controlled runner",
                        "status": "completed",
                        "conclusion": "success",
                        "steps": [
                            {
                                "name": name,
                                "status": "completed",
                                "conclusion": "success",
                            }
                            for name in (
                                f"Run {agent_id} E2E",
                                "Upload endpoint provenance candidate",
                            )
                        ],
                    }
                )
                self.artifacts.append(
                    {
                        "id": artifact_id,
                        "name": f"pitot-endpoint-capture-{agent_id}-{platform_id}-1-{check_id}",
                        "workflow_run": {
                            "id": 1,
                            "repository_id": 99,
                            "head_repository_id": 99,
                            "head_sha": SHA,
                        },
                        "expired": False,
                    }
                )
                self.frames.append(frame)
                self.results.append(result)
                self.repack(index)
        self.calls = 0
        self.post_attempt = None
        self.catalog_changed = False
        self.truncated = False
        self.producers = {
            path: (ROOT / path).read_bytes()
            for path in source.producer_paths(self.manifest)
        }
        self.commit = {"sha": SHA, "tree": {"sha": "b" * 40}}
        self.tree = {
            "sha": "b" * 40,
            "truncated": False,
            "tree": [
                {"path": path, "type": "blob", "mode": "100644", "sha": self.blob(data)}
                for path, data in self.producers.items()
            ],
        }

    @staticmethod
    def blob(data):
        return hashlib.sha1(
            b"blob " + str(len(data)).encode() + b"\0" + data,
            usedforsecurity=False,
        ).hexdigest()

    def repack(self, index):
        data = bundle(self.frames[index], self.results[index])
        artifact = self.artifacts[index]
        self.archives[artifact["id"]] = data
        artifact["digest"] = "sha256:" + hashlib.sha256(data).hexdigest()

    def request(self, method, path):
        if path == f"/git/commits/{SHA}":
            return copy.deepcopy(self.commit)
        if path == "/git/trees/" + "b" * 40 + "?recursive=1":
            return copy.deepcopy(self.tree)
        if path == "":
            return {"id": 99, "full_name": REPOSITORY}
        if path == "/actions/workflows/pitot-e2e.yml":
            return {"id": 7, "path": source.WORKFLOW}
        if path == "/actions/runs/1":
            self.calls += 1
            value = copy.deepcopy(self.run)
            if self.calls > 1 and self.post_attempt is not None:
                value["run_attempt"] = self.post_attempt
            return value
        if path.startswith("/contents/"):
            file = unquote(path.removeprefix("/contents/").split("?", 1)[0])
            data = self.producers[file]
            if self.catalog_changed:
                data = b"changed"
            return {
                "type": "file",
                "encoding": "base64",
                "content": base64.b64encode(data).decode(),
            }
        if path.startswith("/actions/runs/1/attempts/1/jobs?"):
            return {"total_count": len(self.jobs), "jobs": self.jobs}
        if path.startswith("/actions/runs/1/artifacts?"):
            return {
                "total_count": len(self.artifacts) + int(self.truncated),
                "artifacts": self.artifacts,
            }
        raise AssertionError("unexpected API route: " + path)

    def download(self, url):
        prefix = f"https://api.github.com/repos/{REPOSITORY}/actions/artifacts/"
        assert url.startswith(prefix) and url.endswith("/zip")
        return self.archives[int(url[len(prefix) :].removesuffix("/zip"))]


class CaptureSourceTests(unittest.TestCase):
    def promote(self, api, path):
        supervisor.merge_ci_captures(ROOT, path, REPOSITORY, 1, SHA, api)

    def test_complete_set_records_distinct_job_and_check_ids(self):
        api = FixtureAPI()
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "accepted.json"
            self.promote(api, output)
            value = json.loads(output.read_text())
        self.assertEqual(value["schema_version"], 4)
        self.assertEqual(value["source"]["producer_profile"], "capture-direct/v1")
        self.assertEqual(len(value["source"]["producer_files"]), 40)
        self.assertIn(
            "integrations/cursor/beforeShellExecution.ps1",
            [item["path"] for item in value["source"]["producer_files"]],
        )
        self.assertIn(".gitattributes", source.producer_paths(api.manifest))
        self.assertEqual(
            (ROOT / ".gitattributes").read_bytes(), b"* text=auto eol=lf\n"
        )
        self.assertEqual(len(value["cells"]), 33)
        self.assertEqual(len(value["source"]["artifacts"]), 33)
        self.assertNotEqual(
            value["source"]["artifacts"][0]["job_id"],
            value["source"]["artifacts"][0]["check_run_id"],
        )

    def test_source_and_session_mismatches_preserve_prior_output(self):
        def failed_step(api):
            api.jobs[0]["steps"][0]["conclusion"] = "failure"

        def context_mismatch(api):
            api.frames[0]["source"]["checkout_sha"] = "b" * 40
            api.repack(0)

        def result_mismatch(api):
            api.results[0]["cli"]["executable_sha256"] = "0" * 64
            api.repack(0)

        def malformed_result(section, field, value):
            def mutate(api):
                api.results[0][section][field] = value
                api.repack(0)

            return mutate

        mutations = [
            lambda api: api.run.update(event="pull_request"),
            lambda api: api.run.update(head_sha="b" * 40),
            lambda api: api.run.update(conclusion="failure"),
            lambda api: api.artifacts.pop(),
            lambda api: api.artifacts.append(copy.deepcopy(api.artifacts[0])),
            lambda api: api.artifacts[0].update(expired=True),
            lambda api: api.artifacts[0].update(digest="sha256:" + "0" * 64),
            lambda api: api.jobs[0].update(labels=["different-runner"]),
            lambda api: api.jobs[0].update(
                check_run_url="https://api.github.com/repos/operatorstack/pitot/check-runs/999"
            ),
            lambda api: setattr(api, "post_attempt", 2),
            lambda api: setattr(api, "catalog_changed", True),
            lambda api: setattr(api, "truncated", True),
            failed_step,
            context_mismatch,
            result_mismatch,
            malformed_result("runtime", "schema_version", True),
            malformed_result("runtime", "pid", False),
            malformed_result("runtime", "endpoint", {}),
            malformed_result("runtime", "config_sha256", {}),
            malformed_result("cli", "executable", {}),
            malformed_result("canary", "denied_executions", False),
        ]
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index), tempfile.TemporaryDirectory() as directory:
                api = FixtureAPI()
                mutate(api)
                output = Path(directory) / "accepted.json"
                output.write_text("prior accepted state")
                with self.assertRaises(supervisor.ContractError):
                    self.promote(api, output)
                self.assertEqual(output.read_text(), "prior accepted state")

    def test_strict_json_and_archive_members(self):
        for data in (b'{"a":1,"a":2}', b'{"a":NaN}', b'"\xff"', b'{"a":1e400}'):
            with self.subTest(data=data), self.assertRaises(ValueError):
                source.strict_json(data)
        output = io.BytesIO()
        with zipfile.ZipFile(output, "w") as archive:
            link = zipfile.ZipInfo("capture.json")
            link.external_attr = 0o120777 << 16
            archive.writestr(link, "{}")
            archive.writestr("result.json", "{}")
        with self.assertRaisesRegex(ValueError, "unsupported"):
            source.unpack(output.getvalue())

    def test_atomic_replace_failure_keeps_old_record(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "accepted.json"
            output.write_text("old")
            with patch.object(
                source.os, "replace", side_effect=OSError("controlled failure")
            ):
                with self.assertRaises(OSError):
                    source.atomic_json(output, {"new": True})
            self.assertEqual(output.read_text(), "old")
            self.assertEqual(list(Path(directory).iterdir()), [output])

    def test_host_context_requires_matching_checkout_and_positive_check_id(self):
        environment = {
            "GITHUB_REPOSITORY": REPOSITORY,
            "GITHUB_RUN_ID": "1",
            "GITHUB_RUN_ATTEMPT": "2",
            "PITOT_CAPTURE_CHECK_RUN_ID": "55",
            "PITOT_SOURCE_SHA": SHA,
        }
        with patch.object(source.subprocess, "check_output", return_value=SHA + "\n"):
            self.assertEqual(
                source.source_context(ROOT, environment)["check_run_id"], 55
            )
            for invalid in ("", "0", "-1", "true", "1.0"):
                with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                    source.source_context(
                        ROOT, {**environment, "PITOT_CAPTURE_CHECK_RUN_ID": invalid}
                    )
        with patch.object(source.subprocess, "check_output", return_value="b" * 40):
            with self.assertRaisesRegex(ValueError, "checkout differs"):
                source.source_context(ROOT, environment)

    def fixture_root(self, directory, api):
        root = Path(directory) / "repo"
        for path, data in api.producers.items():
            file = root / path
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_bytes(data)
        return root

    def test_each_direct_producer_edit_refuses_acquisition_and_current_record(self):
        api = FixtureAPI()
        with tempfile.TemporaryDirectory() as directory:
            root = self.fixture_root(directory, api)
            output = root / supervisor.ENDPOINT_PROVENANCE
            supervisor.merge_ci_captures(root, output, REPOSITORY, 1, SHA, api)
            accepted = output.read_bytes()
            value = json.loads(accepted)
            with patch.object(
                source,
                "local_producer_bytes",
                side_effect=AssertionError("JSON-only reader touched filesystem"),
            ):
                supervisor.validate_endpoint_value(value, api.manifest)
            for path, data in api.producers.items():
                with self.subTest(path=path):
                    file = root / path
                    file.write_bytes(data + b"\n")
                    with self.assertRaises(supervisor.ContractError):
                        supervisor.validate_endpoint_provenance(root, api.manifest)
                    with self.assertRaises(supervisor.ContractError):
                        supervisor.merge_ci_captures(
                            root, output, REPOSITORY, 1, SHA, api
                        )
                    self.assertEqual(output.read_bytes(), accepted)
                    file.write_bytes(data)
            supervisor.validate_endpoint_provenance(root, api.manifest)

    def test_historical_records_do_not_gain_current_producer_claims(self):
        api = FixtureAPI()
        historical = json.loads((ROOT / supervisor.ENDPOINT_PROVENANCE).read_text())
        # Derive only the original schema3 vocabulary, not a new-profile upgrade.
        historical["schema_version"] = 3
        historical["source"].pop("producer_files", None)
        historical["source"].pop("producer_profile", None)
        with tempfile.TemporaryDirectory() as directory:
            root = self.fixture_root(directory, api)
            (root / supervisor.ENDPOINT_PROVENANCE).write_text(json.dumps(historical))
            (root / "tests/model_control_proxy.py").write_bytes(
                b"changed old-scope helper"
            )
            supervisor.validate_endpoint_provenance(root, api.manifest)
            historical["schema_version"] = 2
            del historical["source"]
            supervisor.validate_endpoint_value(historical, api.manifest)

    def test_producer_binding_is_exact_and_sorted(self):
        api = FixtureAPI()
        value = source.collect(api, ROOT, api.manifest, REPOSITORY, 1, SHA)
        changes = [
            lambda v: v["source"].update(producer_profile="other"),
            lambda v: v["source"]["producer_files"].pop(),
            lambda v: v["source"]["producer_files"].reverse(),
            lambda v: v["source"]["producer_files"].append(
                copy.deepcopy(v["source"]["producer_files"][0])
            ),
            lambda v: v["source"]["producer_files"][0].update(sha256="0"),
            lambda v: v["source"]["producer_files"][0].update(path="../escape"),
        ]
        for change in changes:
            bad = copy.deepcopy(value)
            change(bad)
            with (
                self.subTest(change=change),
                self.assertRaises(supervisor.ContractError),
            ):
                supervisor.validate_endpoint_value(bad, api.manifest)

    def test_git_tree_identity_and_blob_failures_preserve_output(self):
        changes = [
            lambda a: a.commit.update(sha="c" * 40),
            lambda a: a.tree.update(sha="c" * 40),
            lambda a: a.tree.update(truncated=True),
            lambda a: a.tree["tree"].pop(),
            lambda a: a.tree["tree"].append(copy.deepcopy(a.tree["tree"][0])),
            lambda a: a.tree["tree"][0].update(mode="120000"),
            lambda a: a.tree["tree"][0].update(mode="160000", type="commit"),
            lambda a: a.tree["tree"][0].update(sha="0" * 40),
            lambda a: a.tree["tree"].extend(
                {"path": f"unused/{n}"} for n in range(1001)
            ),
        ]
        for change in changes:
            api = FixtureAPI()
            change(api)
            with (
                self.subTest(change=change),
                tempfile.TemporaryDirectory() as directory,
            ):
                output = Path(directory) / "accepted.json"
                output.write_bytes(b"previous accepted state")
                with self.assertRaises(supervisor.ContractError):
                    self.promote(api, output)
                self.assertEqual(output.read_bytes(), b"previous accepted state")

    def test_artifact_paths_cannot_escape_and_url_paths_are_encoded(self):
        for suffix in [
            "../escape",
            "nested/../../escape",
            "./hook",
            "/hook",
            "hook\\file",
            "hook\0file",
        ]:
            manifest = copy.deepcopy(FixtureAPI().manifest)
            manifest["agents"][0]["artifacts"] = [
                f"integrations/{manifest['agents'][0]['id']}/{suffix}"
            ]
            with (
                self.subTest(suffix=suffix),
                self.assertRaises(supervisor.ContractError),
            ):
                supervisor.validate_manifest(manifest)
        client = type("Client", (), {})()
        client.request = lambda method, path: {
            "type": "file",
            "encoding": "base64",
            "content": "",
        }
        with patch.object(client, "request", wraps=client.request) as request:
            source.source_bytes(client, "integrations/opencode/a?b#c%d", SHA)
            request.assert_called_once_with(
                "GET", f"/contents/integrations/opencode/a%3Fb%23c%25d?ref={SHA}"
            )

    @unittest.skipIf(
        os.name == "nt", "Windows reparse points have a native junction gate"
    )
    def test_static_symlink_and_fifo_producers_are_refused_without_read(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "real").write_bytes(b"owned bytes")
            (root / "alias").symlink_to(root / "real")
            with self.assertRaisesRegex(ValueError, "alias"):
                source.local_producer_bytes(root, "alias")
            (root / "folder").mkdir()
            (root / "folder/file").write_bytes(b"bytes")
            (root / "alias-parent").symlink_to(
                root / "folder", target_is_directory=True
            )
            with self.assertRaisesRegex(ValueError, "alias"):
                source.local_producer_bytes(root, "alias-parent/file")
            os.mkfifo(root / "fifo")
            with self.assertRaisesRegex(ValueError, "regular"):
                source.local_producer_bytes(root, "fifo")

    @unittest.skipUnless(os.name == "nt", "requires native Windows junction metadata")
    def test_native_windows_junction_producer_parent_is_refused(self):
        api = FixtureAPI()
        with tempfile.TemporaryDirectory() as directory:
            root = self.fixture_root(directory, api)
            value = source.collect(api, root, api.manifest, REPOSITORY, 1, SHA)
            outside = Path(directory) / "outside-tests"
            (root / "tests").rename(outside)
            result = subprocess.run(
                ["cmd", "/c", "mklink", "/J", str(root / "tests"), str(outside)],
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertTrue((root / "tests").lstat().st_file_attributes & 0x400)
            with self.assertRaisesRegex(ValueError, "alias"):
                source.validate_current_producers(root, value["source"], api.manifest)


if __name__ == "__main__":
    unittest.main()
