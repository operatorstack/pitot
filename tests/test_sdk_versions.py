"""Lockstep gate: the SDK manifests and the scaffold constant carry ONE version.

control-law: bindings-move-in-lockstep-with-the-CLI — one release tag ships the
binary and every binding at the same version, so the manifests may never
drift from each other or from the version `pitot init` writes into scaffolds.
"""

import json
import pathlib
import re
import unittest

LAB = pathlib.Path(__file__).resolve().parents[1]


class SDKVersionLockstep(unittest.TestCase):
    def test_sdk_manifests_and_scaffold_constant_agree(self):
        pyproject = (LAB / "sdk/python/pyproject.toml").read_text()
        python_version = re.search(r'^version\s*=\s*"([^"]+)"', pyproject, re.M).group(1)

        package_json = json.loads(
            (LAB / "sdk/typescript/package.json").read_text()
        )

        cargo = (LAB / "sdk/rust/Cargo.toml").read_text()
        rust_version = re.search(r'^version\s*=\s*"([^"]+)"', cargo, re.M).group(1)

        workbench = (LAB / "cmd/pitot/workbench.go").read_text()
        scaffold_version = re.search(
            r'pitotPackageVersion = "([^"]+)"', workbench
        ).group(1)

        # The Go binding ships from the pitot module itself (installed with
        # `go install ...@<tag>`), so its version IS the release tag rather than a
        # separate manifest. It is represented here by the scaffold constant
        # `pitotPackageVersion` in workbench.go, which `pitot init` writes into the
        # Go projects it generates — the one Go-side version string that can drift.
        versions = {
            "sdk/python": python_version,
            "sdk/typescript": package_json["version"],
            "sdk/rust": rust_version,
            "sdk/go (scaffold constant workbench.go pitotPackageVersion)": scaffold_version,
        }
        self.assertEqual(
            len(set(versions.values())),
            1,
            f"SDK versions drifted out of lockstep: {versions}",
        )

    def test_preview_sdks_stay_unshippable_or_join_the_gate(self):
        """Java and C# are types-only previews (see each dir's README): no runner,
        no conformance entry, and no build manifest — so they carry no release
        version and are deliberately excluded from the lockstep gate above.

        This guard makes that exclusion honest instead of a silent gap: the moment
        someone adds a build manifest (turning a preview into something that
        installs), this test fails and forces them to give it a real version and
        fold it into the lockstep set — so a shipped-but-ungated SDK can never slip
        through.
        """
        preview_manifests = {
            "sdk/java": [
                "sdk/java/pom.xml",
                "sdk/java/build.gradle",
                "sdk/java/build.gradle.kts",
            ],
            "sdk/csharp": [
                "sdk/csharp/Pitot/Pitot.csproj",
                "sdk/csharp/Pitot.sln",
            ],
        }
        shipped = {
            name: rel
            for name, rels in preview_manifests.items()
            for rel in rels
            if (LAB / rel).exists()
        }
        self.assertEqual(
            shipped,
            {},
            "a preview SDK gained a build manifest and now installs but is not "
            f"version-locked: {shipped}. Add its version to the lockstep gate "
            "above (and a conformance entry) before shipping it.",
        )


if __name__ == "__main__":
    unittest.main()
