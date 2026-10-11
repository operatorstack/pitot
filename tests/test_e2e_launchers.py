"""Exercise agent wrappers with a non-executable shared Bash runner."""

from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


TESTS = Path(__file__).resolve().parent


class AgentLauncherTests(unittest.TestCase):
    def test_wrappers_forward_agent_and_exit_without_executable_runner(self):
        bash = shutil.which("bash")
        self.assertIsNotNone(bash, "agent E2E launchers require Bash")
        wrappers = sorted(
            path for path in TESTS.glob("e2e_*_cli_test.sh")
            if "e2e_unified_runner.sh" in path.read_text(encoding="utf-8")
        )
        self.assertEqual(len(wrappers), 10)
        with tempfile.TemporaryDirectory(prefix="pitot launchers ") as temporary:
            directory = Path(temporary)
            runner = directory / "e2e_unified_runner.sh"
            runner.write_text('printf "agent=%s\\n" "$1"\nexit 23\n', encoding="utf-8")
            runner.chmod(0o644)
            for source in wrappers:
                with self.subTest(wrapper=source.name):
                    wrapper = directory / source.name
                    shutil.copyfile(source, wrapper)
                    completed = subprocess.run(
                        [bash, str(wrapper)], cwd=TESTS.parent,
                        text=True, capture_output=True, timeout=10,
                    )
                    agent = source.name.removeprefix("e2e_").removesuffix("_cli_test.sh")
                    self.assertEqual(completed.returncode, 23, completed.stderr)
                    self.assertEqual(completed.stdout, f"agent={agent}\n")


if __name__ == "__main__":
    unittest.main()
