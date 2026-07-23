import json
import subprocess
import os
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[5]
SDK_DIR = ROOT / "labs" / "15-pitot" / "pitot-distribution" / "sdk"

class TestLanguageRunners(unittest.TestCase):
    def run_controller_test(self, command: list[str], cwd: Path, extra_env: dict = None):
        request = {
            "pitot_version": "1",
            "type": "control.requested",
            "kind": "test.approval",
            "action_id": "act_test123",
            "data": {"test_field": "value"}
        }
        
        env = os.environ.copy()
        if extra_env:
            env.update(extra_env)
        
        proc = subprocess.Popen(
            command,
            cwd=cwd,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True
        )
        
        stdout, stderr = proc.communicate(json.dumps(request) + "\n")
        self.assertEqual(proc.returncode, 0, f"Process failed: {stderr}")
        
        output = [line for line in stdout.strip().split("\n") if line]
        self.assertEqual(len(output), 1)
        
        response = json.loads(output[0])
        self.assertEqual(response["pitot_version"], "1")
        self.assertEqual(response["type"], "control.response")
        self.assertEqual(response["controller_id"], "test-controller")
        self.assertEqual(response["action_id"], "act_test123")
        self.assertEqual(response["outcome"], "allow")
        self.assertEqual(response["message"], "test approved")

    def test_python_runner(self):
        script_path = SDK_DIR / "tests" / "python_test.py"
        script_path.write_text("""
import sys
from pitot.runner import run_controller, allow
from pitot.types import ControlRequested

def handler(req: ControlRequested):
    return allow("test approved")

if __name__ == "__main__":
    run_controller("test-controller", handler)
""")
        self.run_controller_test(["python3", str(script_path)], SDK_DIR / "python", {"PYTHONPATH": str(SDK_DIR / "python")})

    def test_ts_runner(self):
        script_path = SDK_DIR / "tests" / "ts_test.ts"
        script_path.write_text("""
import { runController, allow } from '../typescript/src/runner';
import { ControlRequested } from '../typescript/src/pitot';

runController("test-controller", async (req: ControlRequested) => {
    return allow("test approved");
});
""")
        self.run_controller_test(["npx", "tsx", str(script_path)], SDK_DIR / "typescript")

    def test_go_runner(self):
        script_path = SDK_DIR / "tests" / "go_test_runner.go"
        script_path.write_text("""
package main

import (
    "github.com/operatorstack/pitot/sdk"
    "github.com/operatorstack/pitot/schema"
)

func main() {
    sdk.RunController("test-controller", func(req schema.ControlRequested) sdk.Outcome {
        return sdk.Allow("test approved")
    })
}
""")
        self.run_controller_test(["go", "run", str(script_path)], ROOT / "labs" / "15-pitot" / "pitot")

if __name__ == "__main__":
    unittest.main()
