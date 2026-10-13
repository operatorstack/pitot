#!/usr/bin/env python3
"""Build local SDK artifacts and exercise their installed public entry points."""

from __future__ import annotations

import argparse
import email
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import venv
import zipfile

from check_release import source_version
from check_rust_package import check_rust_package


ROOT = Path(__file__).resolve().parents[1]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--output", type=Path, help="retain verified packages for publication"
    )
    args = parser.parse_args()
    version = source_version()
    npm = shutil.which("npm")
    node = shutil.which("node")
    if not npm or not node:
        raise SystemExit("Node.js and npm are required")
    with tempfile.TemporaryDirectory(prefix="pitot-sdk-packages-") as temporary:
        work = Path(temporary)
        env = {
            key: value
            for key, value in os.environ.items()
            if not key.upper().startswith(("NPM_CONFIG_", "PIP_", "NODE_"))
            and key.upper() not in {"PYTHONPATH", "PYTHONHOME"}
            and not key.upper().endswith(("_TOKEN", "_SECRET", "_PASSWORD", "_API_KEY"))
        }
        env["PIP_CONFIG_FILE"] = os.devnull
        env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
        env["PIP_CACHE_DIR"] = str(work / "pip-cache")
        env["npm_config_cache"] = str(work / "npm-cache")
        for name in ("npmrc", "npm-globalrc"):
            (work / name).write_text("", encoding="utf-8")
        npm_flags = [
            "--userconfig",
            str(work / "npmrc"),
            "--globalconfig",
            str(work / "npm-globalrc"),
            "--no-audit",
            "--no-fund",
        ]

        def run(arguments, cwd, *, input_text=None, expected_failure=False):
            result = subprocess.run(
                arguments,
                cwd=cwd,
                env=env,
                input=input_text,
                capture_output=True,
                text=True,
                encoding="utf-8",
                timeout=300,
            )
            if (result.returncode == 0) == expected_failure:
                print(result.stdout, file=sys.stderr)
                print(result.stderr, file=sys.stderr)
                raise RuntimeError(f"Unexpected exit {result.returncode}: {arguments}")
            return result

        # Copy tracked source bytes, including current edits, without caches,
        # untracked configuration or an existing dist that could hide a bad pack.
        sources = subprocess.check_output(
            ["git", "ls-files", "-z", "sdk/python", "sdk/typescript", "sdk/rust"],
            cwd=ROOT,
        ).decode()
        for relative in sources.split("\0"):
            if relative:
                target = work / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((ROOT / relative).read_bytes())

        typescript = work / "sdk/typescript"
        run([npm, "install", "--ignore-scripts", *npm_flags], typescript)
        packed = work / "packed"
        packed.mkdir()
        # No manual build: packing itself must supply the advertised entrypoints.
        run([npm, "pack", "--pack-destination", str(packed), *npm_flags], typescript)
        tarballs = list(packed.glob("*.tgz"))
        if len(tarballs) != 1:
            raise RuntimeError("Packing must produce exactly one tarball")
        with tarfile.open(tarballs[0]) as package:
            metadata = json.load(package.extractfile("package/package.json"))
            if (
                metadata["name"] != "@operatorstack/pitot"
                or metadata["version"] != version
            ):
                raise RuntimeError("npm package identity differs from release source")
            names = set(package.getnames())
            for entry in (metadata["main"], metadata["types"]):
                if "package/" + entry.removeprefix("./") not in names:
                    raise RuntimeError(f"Missing packaged entrypoint: {entry}")

        consumer = work / "node-consumer"
        consumer.mkdir()
        (consumer / "package.json").write_text('{"private":true}', encoding="utf-8")
        run(
            [npm, "install", "--ignore-scripts", str(tarballs[0]), *npm_flags],
            consumer,
        )
        for module_mode, import_code in (
            (
                "commonjs",
                "const {runController,runConsumer,allow,deny}=require('@operatorstack/pitot');",
            ),
            (
                "module",
                "import {runController,runConsumer,allow,deny} from '@operatorstack/pitot';",
            ),
        ):
            result = run(
                [
                    node,
                    "--input-type=" + module_mode,
                    "-e",
                    import_code
                    + "if(typeof runController!=='function'||typeof runConsumer!=='function')throw Error('missing runners');"
                    + "console.log(JSON.stringify([allow('allow'),deny('deny')]));",
                ],
                consumer,
            )
            if json.loads(result.stdout) != [
                {"outcome": "allow", "message": "allow"},
                {"outcome": "deny", "message": "deny"},
            ]:
                raise RuntimeError("Installed public helpers differ")

        (consumer / "typecheck.ts").write_text(
            "import {runController,runConsumer,allow,deny,ControlRequested,Event} from '@operatorstack/pitot';\n"
            "runController('artifact', (r:ControlRequested)=>r.action_id==='allow'?allow():deny());\n"
            "runConsumer((e:Event)=>{const id:string|undefined=e.action?.id;void id;});\n",
            encoding="utf-8",
        )
        run(
            [
                node,
                str(typescript / "node_modules/typescript/bin/tsc"),
                "--strict",
                "--noEmit",
                "--target",
                "es2022",
                "--module",
                "commonjs",
                "--moduleResolution",
                "node",
                "typecheck.ts",
            ],
            consumer,
        )
        if (consumer / "node_modules/typescript").exists():
            raise RuntimeError("Consumer unexpectedly requires build tools")

        build_environment = work / "python-build"
        installed_environment = work / "python-installed"
        for location in (build_environment, installed_environment):
            venv.EnvBuilder(with_pip=True).create(location)

        def python_at(location):
            return str(
                location / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
            )

        wheels = work / "wheels"
        run(
            [
                python_at(build_environment),
                "-m",
                "pip",
                "install",
                "--index-url",
                "https://pypi.org/simple",
                "build",
            ],
            work,
        )
        # build creates the wheel from the sdist, checking both distribution paths.
        run(
            [
                python_at(build_environment),
                "-m",
                "build",
                "--outdir",
                str(wheels),
                str(work / "sdk/python"),
            ],
            work,
        )
        wheel_files = list(wheels.glob("*.whl"))
        sdists = list(wheels.glob("*.tar.gz"))
        if len(wheel_files) != 1 or len(sdists) != 1:
            raise RuntimeError("Build must produce exactly one wheel and one sdist")
        with zipfile.ZipFile(wheel_files[0]) as wheel:
            metadata_paths = [
                name
                for name in wheel.namelist()
                if name.endswith(".dist-info/METADATA")
            ]
            if len(metadata_paths) != 1:
                raise RuntimeError("Wheel metadata missing or ambiguous")
            metadata = email.message_from_bytes(wheel.read(metadata_paths[0]))
            if (
                metadata["Name"] != "operatorstack-pitot"
                or metadata["Version"] != version
            ):
                raise RuntimeError("Wheel identity differs from release source")
        with tarfile.open(sdists[0]) as sdist:
            metadata_paths = [
                name
                for name in sdist.getnames()
                if name.count("/") == 1 and name.endswith("/PKG-INFO")
            ]
            if len(metadata_paths) != 1:
                raise RuntimeError("Source distribution metadata missing or ambiguous")
            metadata = email.message_from_bytes(
                sdist.extractfile(metadata_paths[0]).read()
            )
            if (
                metadata["Name"] != "operatorstack-pitot"
                or metadata["Version"] != version
            ):
                raise RuntimeError(
                    "Source distribution identity differs from release source"
                )
        python = python_at(installed_environment)
        run([python, "-m", "pip", "install", "--no-deps", str(wheel_files[0])], work)
        run(
            [
                python,
                "-I",
                "-c",
                "from pitot import run_controller,run_consumer,allow,deny,ControlRequested,Event",
            ],
            work,
        )

        requests = "".join(
            json.dumps(
                {
                    "pitot_version": "1",
                    "type": "control.requested",
                    "kind": "test.approval",
                    "action_id": decision,
                }
            )
            + "\n"
            for decision in ("allow", "deny")
        )
        event = (
            json.dumps(
                {
                    "pitot_version": "1",
                    "type": "action.requested",
                    "host": {"name": "package-test"},
                    "observation": {"source": "host_hook", "fidelity": "direct"},
                    "action": {"id": "observed", "kind": "shell"},
                }
            )
            + "\n"
        )
        for command, cwd, controller_code, consumer_code in (
            (
                [node, "-e"],
                consumer,
                "const p=require('@operatorstack/pitot');p.runController('artifact',r=>r.action_id==='allow'?p.allow():p.deny());",
                "require('@operatorstack/pitot').runConsumer(e=>console.log(e.action.id));",
            ),
            (
                [python, "-I", "-c"],
                work,
                "from pitot import run_controller,allow,deny;run_controller('artifact',lambda r:allow() if r.action_id=='allow' else deny())",
                "from pitot import run_consumer;run_consumer(lambda e:print(e.action.id))",
            ),
        ):
            result = run([*command, controller_code], cwd, input_text=requests)
            responses = [json.loads(line) for line in result.stdout.splitlines()]
            expected = [
                {
                    "pitot_version": "1",
                    "type": "control.response",
                    "controller_id": "artifact",
                    "action_id": decision,
                    "outcome": decision,
                }
                for decision in ("allow", "deny")
            ]
            if responses != expected or result.stderr:
                raise RuntimeError("Installed Controller roundtrip differs")
            result = run([*command, consumer_code], cwd, input_text=event)
            if result.stdout.strip() != "observed" or result.stderr:
                raise RuntimeError("Installed Consumer roundtrip differs")

        crate = check_rust_package(work, ROOT, run, env, version)

        # A failed compiler must stop packaging instead of publishing partial JS.
        (typescript / "src/index.ts").write_text(
            "export const broken: = 1;\n", encoding="utf-8"
        )
        rejected = work / "rejected"
        rejected.mkdir()
        run(
            [npm, "pack", "--pack-destination", str(rejected), *npm_flags],
            typescript,
            expected_failure=True,
        )
        if list(rejected.glob("*.tgz")):
            raise RuntimeError("Failed build produced a tarball")
        if args.output:
            args.output.mkdir(parents=True, exist_ok=False)
            for artifact in [*tarballs, *wheel_files, *sdists, crate]:
                shutil.copyfile(artifact, args.output / artifact.name)
        print(
            "PASS: installed Python/TypeScript/Rust artifacts, generated Rust projects, identities, stdio and failed-build rejection"
        )


if __name__ == "__main__":
    main()
