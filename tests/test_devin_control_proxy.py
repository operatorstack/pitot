"""Unit tests for the deterministic Devin Connect/protobuf proxy's HTTP
behavior — the properties the Windows CI cells depend on (keep-alive reuse,
chunked draining, burst-tolerant backlog, the request journal, and the
fail-closed 409 state machine) exercised without a released Devin binary."""

from __future__ import annotations

import http.client
import json
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

LAB = Path(__file__).resolve().parents[1]
PROXY = LAB / "tests" / "devin_control_proxy.py"
NONCE = "cafe" * 8


class ProxyHarness:
    def __init__(self, response_fault: str = "none", request_log: bool = True):
        self.workdir = Path(tempfile.mkdtemp(prefix="devin-proxy-test-"))
        self.receipt = self.workdir / "receipt.json"
        self.ready = self.workdir / "ready.txt"
        self.log = self.workdir / "requests.log" if request_log else None
        command = [
            sys.executable, str(PROXY),
            "--nonce", NONCE,
            "--receipt", str(self.receipt),
            "--ready-file", str(self.ready),
            "--canary-command", "echo PITOT_CANARY",
            "--response-fault", response_fault,
        ]
        if self.log is not None:
            command += ["--request-log", str(self.log)]
        self.process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + 10
        while time.time() < deadline:
            if self.ready.is_file() and self.ready.read_text(encoding="utf-8").strip():
                break
            time.sleep(0.02)
        else:
            raise RuntimeError("proxy did not become ready")
        url = self.ready.read_text(encoding="utf-8").strip()
        self.host, self.port = url.split("//", 1)[1].split(":")
        self.port = int(self.port)

    def receipt_json(self) -> dict:
        return json.loads(self.receipt.read_text(encoding="utf-8"))

    def close(self) -> None:
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()


class DevinProxyHTTPTests(unittest.TestCase):
    def setUp(self):
        self.proxy = ProxyHarness()
        self.addCleanup(self.proxy.close)

    def test_keepalive_serves_sequential_requests_on_one_connection(self):
        connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=5)
        self.addCleanup(connection.close)
        for i in range(3):
            connection.request(
                "POST", "/exa.seat_management_pb.SeatManagementService/GetCliTeamSettings",
                body=b"x", headers={"Content-Type": "application/proto"},
            )
            response = connection.getresponse()
            self.assertEqual(response.status, 200, f"request {i}")
            self.assertEqual(response.read(), b"")
            self.assertEqual(response.version, 11, "proxy must speak HTTP/1.1")
            self.assertNotEqual(
                (response.getheader("Connection") or "").lower(), "close",
                "keep-alive must not be refused on the auxiliary path",
            )
        self.assertEqual(self.proxy.receipt_json()["auxiliary_requests"], 3)

    def test_chunked_request_body_is_drained_exactly(self):
        connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=5)
        self.addCleanup(connection.close)
        connection.putrequest("POST", "/aux.Service/Chunked")
        connection.putheader("Transfer-Encoding", "chunked")
        connection.endheaders()
        for chunk in (b"hello ", b"chunked ", b"world"):
            connection.send(b"%x\r\n%s\r\n" % (len(chunk), chunk))
        connection.send(b"0\r\n\r\n")
        response = connection.getresponse()
        self.assertEqual(response.status, 200)
        response.read()
        # The connection must stay usable: an undrained body would desync it.
        connection.request("POST", "/aux.Service/After", body=b"y")
        after = connection.getresponse()
        self.assertEqual(after.status, 200)
        after.read()
        self.assertEqual(self.proxy.receipt_json()["auxiliary_requests"], 2)

    def test_connection_burst_within_backlog_all_served(self):
        errors: list[str] = []
        def one(i: int) -> None:
            try:
                connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=10)
                connection.request("POST", "/aux.Service/Burst", body=b"b")
                response = connection.getresponse()
                if response.status != 200:
                    errors.append(f"{i}: status {response.status}")
                response.read()
                connection.close()
            except Exception as error:  # noqa: BLE001 - the failure IS the signal
                errors.append(f"{i}: {error}")
        threads = [threading.Thread(target=one, args=(i,)) for i in range(64)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join(timeout=15)
        self.assertEqual(errors, [], "burst connections must all be served (backlog 128)")
        self.assertEqual(self.proxy.receipt_json()["auxiliary_requests"], 64)

    def test_request_journal_records_connections_and_requests(self):
        connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=5)
        connection.request("POST", "/aux.Service/Journal", body=b"j")
        connection.getresponse().read()
        connection.close()
        time.sleep(0.2)
        journal = self.proxy.log.read_text(encoding="utf-8")
        self.assertIn("CONN open", journal)
        self.assertIn("REQ  ", journal)
        self.assertIn("/aux.Service/Journal", journal)
        self.assertIn("RES  ", journal)

    def test_get_is_404_with_explicit_length(self):
        connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=5)
        self.addCleanup(connection.close)
        connection.request("GET", "/anything")
        response = connection.getresponse()
        self.assertEqual(response.status, 404)
        self.assertEqual(response.getheader("content-length"), "0")
        response.read()
        # keep-alive survives the 404
        connection.request("POST", "/aux.Service/AfterGet", body=b"z")
        self.assertEqual(connection.getresponse().status, 200)

    def test_off_trajectory_chat_request_is_409(self):
        connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=5)
        self.addCleanup(connection.close)
        # A GetChatMessage without the nonce is off the control trajectory.
        connection.request(
            "POST", "/exa.api_server_pb.ApiServerService/GetChatMessage",
            body=b"no nonce here", headers={"Content-Type": "application/connect+proto"},
        )
        response = connection.getresponse()
        self.assertEqual(response.status, 409)
        response.read()
        receipt = self.proxy.receipt_json()
        self.assertIn("unexpected_request", receipt)
        self.assertFalse(receipt["initial_prompt_observed"])

    def test_initial_prompt_with_nonce_advances_trajectory(self):
        connection = http.client.HTTPConnection(self.proxy.host, self.proxy.port, timeout=5)
        self.addCleanup(connection.close)
        connection.request(
            "POST", "/exa.api_server_pb.ApiServerService/GetChatMessage",
            body=NONCE.encode(), headers={"Content-Type": "application/connect+proto"},
        )
        response = connection.getresponse()
        self.assertEqual(response.status, 200)
        body = response.read()
        self.assertGreater(len(body), 5, "expected a Connect envelope payload")
        receipt = self.proxy.receipt_json()
        self.assertTrue(receipt["initial_prompt_observed"])
        self.assertTrue(receipt["tool_call_response_emitted"])
        self.assertEqual(receipt["endpoint_observed"]["framing"], "connect_envelope")


class DevinProxyFaultModeTests(unittest.TestCase):
    def test_response_fault_text_emits_text_instead_of_tool_call(self):
        proxy = ProxyHarness(response_fault="text")
        self.addCleanup(proxy.close)
        connection = http.client.HTTPConnection(proxy.host, proxy.port, timeout=5)
        self.addCleanup(connection.close)
        connection.request(
            "POST", "/exa.api_server_pb.ApiServerService/GetChatMessage",
            body=NONCE.encode(), headers={"Content-Type": "application/connect+proto"},
        )
        response = connection.getresponse()
        self.assertEqual(response.status, 200)
        response.read()
        receipt = proxy.receipt_json()
        self.assertEqual(receipt["fault_response_emitted"], "text")
        self.assertFalse(receipt["tool_call_response_emitted"])


if __name__ == "__main__":
    unittest.main()
