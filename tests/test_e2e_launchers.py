"""Exercise agent wrappers with a non-executable shared Bash runner."""

from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest


TESTS = Path(__file__).resolve().parent


class AgentLauncherTests(unittest.TestCase):
    def test_cursor_runner_uses_native_binaries_and_python_on_every_platform(self):
        bash = shutil.which("bash")
        self.assertIsNotNone(bash)
        for platform in ("Windows", "Linux", "macOS"):
            with (
                self.subTest(platform=platform),
                tempfile.TemporaryDirectory(prefix="pitot native launch ") as temporary,
            ):
                directory = Path(temporary)
                tools = directory / "tools"
                tools.mkdir()
                for name, text in {
                    "go": 'printf "%s\\n" "$@" >> "$GO_LOG"\n',
                    "python3": 'printf "%s\\n" "CALL" "$@" >> "$DRIVER_LOG"\n',
                    "wsl.exe": 'touch "$WSL_LOG"\nexit 79\n',
                }.items():
                    executable = tools / name
                    executable.write_text("#!/usr/bin/env bash\n" + text)
                    executable.chmod(0o755)
                environment = {
                    **os.environ,
                    "PATH": str(tools) + os.pathsep + os.environ.get("PATH", ""),
                    "RUNNER_OS": platform,
                    "RUNNER_TEMP": str(directory),
                    "PITOT_E2E_PLATFORM": platform.lower(),
                    "PITOT_INSTALL_RECEIPT": str(directory / "install.json"),
                    "PITOT_E2E_EVIDENCE": str(directory / "evidence.json"),
                    "GO_LOG": str(directory / "go.log"),
                    "DRIVER_LOG": str(directory / "driver.log"),
                    "WSL_LOG": str(directory / "wsl.log"),
                }
                result = subprocess.run(
                    [bash, str(TESTS / "e2e_unified_runner.sh"), "cursor"],
                    env=environment,
                    capture_output=True,
                    text=True,
                    timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse((directory / "wsl.log").exists())
                build_arguments = (directory / "go.log").read_text().splitlines()
                binaries = [
                    build_arguments[index + 1]
                    for index, value in enumerate(build_arguments)
                    if value == "-o"
                ]
                suffix = ".exe" if platform == "Windows" else ""
                self.assertEqual(
                    [Path(path).name for path in binaries],
                    [
                        "pitot" + suffix,
                        "pitot-witness" + suffix,
                        "pitot-testrole" + suffix,
                    ],
                )
                arguments = (directory / "driver.log").read_text().splitlines()
                self.assertEqual(arguments.count("CALL"), 2)
                self.assertEqual(
                    arguments.count(str(TESTS / "real_agent_driver.py")), 2
                )
                for path in binaries:
                    self.assertEqual(arguments.count(path), 2)
                self.assertIn("--expect-incompatible-response", arguments)

    def test_wrappers_forward_agent_and_exit_without_executable_runner(self):
        bash = shutil.which("bash")
        self.assertIsNotNone(bash, "agent E2E launchers require Bash")
        wrappers = sorted(
            path
            for path in TESTS.glob("e2e_*_cli_test.sh")
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
                        [bash, str(wrapper)],
                        cwd=TESTS.parent,
                        text=True,
                        capture_output=True,
                        timeout=10,
                    )
                    agent = source.name.removeprefix("e2e_").removesuffix(
                        "_cli_test.sh"
                    )
                    self.assertEqual(completed.returncode, 23, completed.stderr)
                    self.assertEqual(completed.stdout, f"agent={agent}\n")


if __name__ == "__main__":
    unittest.main()
