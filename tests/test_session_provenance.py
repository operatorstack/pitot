from __future__ import annotations

import copy
import hashlib
import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "session_driver", ROOT / "tests/real_agent_driver.py"
)
driver = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(driver)


def delivery(host="claude", session="native-session"):
    witnesses, requests, consumers = [], [], []
    for index in range(2):
        action = f"act_{index}"
        event = {
            "type": "action.requested",
            "host": {"name": host},
            "action": {"id": action, "kind": "shell"},
            "observation": {
                "source": "host_event" if host == "devin" else "host_hook",
                "fidelity": "direct",
            },
        }
        witness = {"host": host, "action_id": action, "valid": True}
        if session:
            event["session_id"] = session
            witness.update(
                session_id_state="supplied",
                session_id_sha256=hashlib.sha256(session.encode()).hexdigest(),
            )
        else:
            witness["session_id_state"] = "unavailable"
        if host == "deepseek":
            witness = {
                "host": host,
                "action_id": action,
                "valid": True,
                "session_id": session,
            }
        witnesses.append(witness)
        requests.append({"action_id": action, "data": event})
        consumers.append(copy.deepcopy(event))
    return [] if host == "devin" else witnesses, requests, consumers


class SessionDeliveryVerificationTests(unittest.TestCase):
    def test_supplied_and_unavailable_identities(self):
        for host in ("claude", "cursor", "pi", "opencode", "devin", "deepseek"):
            with self.subTest(host=host):
                proofs = driver.validate_session_provenance(host, *delivery(host))
                self.assertEqual([proof["state"] for proof in proofs], ["supplied"] * 2)
        proofs = driver.validate_session_provenance("claude", *delivery(session=""))
        self.assertEqual([proof["state"] for proof in proofs], ["unavailable"] * 2)

    def test_native_plugin_identity_cannot_be_unavailable(self):
        for host in ("pi", "opencode"):
            with self.subTest(host=host), self.assertRaises(RuntimeError):
                driver.validate_session_provenance(host, *delivery(host, ""))

    def test_dropped_or_substituted_delivery_fails(self):
        for layer in ("controller", "consumer"):
            for value in (None, "", "tool-call-is-not-session", 7):
                with self.subTest(layer=layer, value=value):
                    witnesses, requests, consumers = delivery()
                    event = (
                        requests[0]["data"] if layer == "controller" else consumers[0]
                    )
                    event["session_id"] = value
                    with self.assertRaises(RuntimeError):
                        driver.validate_session_provenance(
                            "claude", witnesses, requests, consumers
                        )
            witnesses, requests, consumers = delivery()
            event = requests[0]["data"] if layer == "controller" else consumers[0]
            del event["session_id"]
            with self.assertRaises(RuntimeError):
                driver.validate_session_provenance(
                    "claude", witnesses, requests, consumers
                )

    def test_unavailable_identity_is_omitted(self):
        for layer in ("controller", "consumer"):
            for value in (None, "", "generated-action-id"):
                witnesses, requests, consumers = delivery(session="")
                event = requests[0]["data"] if layer == "controller" else consumers[0]
                event["session_id"] = value
                with (
                    self.subTest(layer=layer, value=value),
                    self.assertRaises(RuntimeError),
                ):
                    driver.validate_session_provenance(
                        "claude", witnesses, requests, consumers
                    )

    def test_witness_is_correlated_and_contains_only_session_hash(self):
        for field, value in (
            ("session_id_sha256", "0" * 64),
            ("session_id_state", "unavailable"),
            ("host", "different-host"),
            ("action_id", "act_other"),
            ("session_id", "native-session"),
        ):
            witnesses, requests, consumers = delivery()
            witnesses[0][field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                driver.validate_session_provenance(
                    "claude", witnesses, requests, consumers
                )

    def test_host_observation_and_action_correlation_are_preserved(self):
        for layer in ("controller", "consumer"):
            for field, value in (
                ("host", {"name": "other"}),
                ("observation", {"source": "host_event", "fidelity": "inferred"}),
                ("action", {"id": "act_other"}),
                ("host", None),
            ):
                witnesses, requests, consumers = delivery()
                event = requests[0]["data"] if layer == "controller" else consumers[0]
                event[field] = value
                with (
                    self.subTest(layer=layer, field=field),
                    self.assertRaises(RuntimeError),
                ):
                    driver.validate_session_provenance(
                        "claude", witnesses, requests, consumers
                    )


if __name__ == "__main__":
    unittest.main()
