"""Exercise generated Rust programs against packaged, tag-addressed SDK bytes."""

from pathlib import Path
import json
import os
import shutil
import tarfile


def check_rust_package(work, root, run, env, version):
    cargo = shutil.which("cargo")
    go = shutil.which("go")
    if not cargo or not go:
        raise RuntimeError(
            "Rust and Go toolchains are required for package verification"
        )
    rust = work / "sdk/rust"
    env["CARGO_TARGET_DIR"] = str(work / "rust-target")
    run([cargo, "package", "--allow-dirty"], rust)
    archive = work / "rust-target/package" / f"pitot-{version}.crate"
    if not archive.is_file():
        raise RuntimeError("Rust package version does not match source")
    # Use exactly the generated archive, in a repository with the public layout.
    repository = work / "rust-git"
    dependency = repository / "sdk/rust"
    with tarfile.open(archive) as package:
        prefix = f"pitot-{version}/"
        for member in package.getmembers():
            if member.isfile() and member.name.startswith(prefix):
                relative = Path(member.name[len(prefix) :])
                if relative.is_absolute() or ".." in relative.parts:
                    raise RuntimeError("Unexpected package path")
                target = dependency / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(package.extractfile(member).read())
    if not (dependency / "src/lib.rs").is_file():
        raise RuntimeError("Rust archive missing library")
    git = [
        "git",
        "-c",
        "user.name=Package Test",
        "-c",
        "user.email=package@example.invalid",
        "-c",
        "commit.gpgsign=false",
        "-c",
        "tag.gpgsign=false",
    ]
    run([*git, "init", "--quiet"], repository)
    run([*git, "add", "."], repository)
    run([*git, "commit", "--quiet", "-m", "Package fixture"], repository)
    # Deliberately distinct from the source fallback, to test stamped CLI output.
    stamped = "1.2.3"
    run([*git, "tag", "v" + stamped], repository)
    binary = work / ("pitot.exe" if os.name == "nt" else "pitot")
    run(
        [
            go,
            "build",
            "-ldflags",
            "-X main.version=" + stamped,
            "-o",
            str(binary),
            "./cmd/pitot",
        ],
        root,
    )
    event = {
        "pitot_version": "1",
        "type": "action.requested",
        "host": {"name": "package-test"},
        "observation": {"source": "host_hook", "fidelity": "direct"},
        "action": {"id": "observed", "kind": "shell"},
    }
    for template in ("blank-controller", "blank-consumer", "shell-policy"):
        project = work / template
        run(
            [
                str(binary),
                "init",
                "--language",
                "rust",
                "--template",
                template,
                "--dir",
                template,
            ],
            work,
        )
        manifest = project / "Cargo.toml"
        original = manifest.read_text()
        expected = f'pitot = {{ git = "https://github.com/operatorstack/pitot", tag = "v{stamped}" }}'
        if expected not in original:
            raise RuntimeError("Generated Rust dependency does not pin the CLI release")
        # Substitute transport only; retain the emitted tag and nested-crate lookup.
        patched = original.replace(
            "https://github.com/operatorstack/pitot", repository.as_uri()
        )
        manifest.write_text(patched)
        if template == "blank-controller":
            manifest.write_text(patched.replace('tag = "v1.2.3"', 'tag = "v9.9.9"'))
            run([cargo, "check", "--quiet"], project, expected_failure=True)
            manifest.write_text(patched)
        run([cargo, "build", "--quiet"], project)
        executable = (
            work
            / "rust-target/debug"
            / ("pitot-project.exe" if os.name == "nt" else "pitot-project")
        )
        if template == "blank-consumer":
            result = run(
                [str(executable)], project, input_text=json.dumps(event) + "\n"
            )
            if result.stdout or "Consumed event: action.requested" not in result.stderr:
                raise RuntimeError("Packaged Rust Consumer did not observe silently")
        else:
            requests = []
            for decision in ("allow", "deny"):
                data = {
                    **event,
                    "content": {
                        "mode": "full",
                        "full": "echo ok" if decision == "allow" else "PITOT_DENY_ME",
                    },
                }
                requests.append(
                    json.dumps(
                        {
                            "pitot_version": "1",
                            "type": "control.requested",
                            "kind": "shell",
                            "action_id": decision,
                            "data": data,
                        }
                    )
                )
            result = run(
                [str(executable)], project, input_text="\n".join(requests) + "\n"
            )
            responses = [json.loads(line) for line in result.stdout.splitlines()]
            outcomes = [response["outcome"] for response in responses]
            expected_outcomes = (
                ["allow", "deny"] if template == "shell-policy" else ["allow", "allow"]
            )
            if (
                outcomes != expected_outcomes
                or [r["action_id"] for r in responses] != ["allow", "deny"]
                or result.stderr
            ):
                raise RuntimeError("Packaged Rust Controller roundtrip differs")
    return archive
