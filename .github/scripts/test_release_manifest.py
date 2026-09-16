# SPDX-License-Identifier: MIT

"""Unit tests for release-manifest.py (stdlib unittest only)."""

import hashlib
import importlib.util
import json
import shutil
import tempfile
import types
import unittest
import zipfile
from pathlib import Path

SCRIPT = Path(__file__).with_name("release-manifest.py")
SPEC = importlib.util.spec_from_file_location("release_manifest", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)

EPOCH = (1980, 1, 1, 0, 0, 0)

REQUIRED_ASSERTIONS = (
    "large_envelope_forwarded",
    "session_header_mapped",
    "client_header_mapped",
    "mock_request_count_one",
    "interceptor_errors_zero",
    "recovered_panics_zero",
    "synthetic_marker_absent_from_host_logs",
)


class ReleaseManifestTest(unittest.TestCase):
    def create_input(self, root: Path):
        version = "0.3.2"
        revision = "1" * 40
        for goos, goarch, extension in MODULE.EXPECTED_TARGETS:
            library_name = f"opencode-session-mapper.{extension}"
            target_dir = root / f"stage_{goos}_{goarch}"
            target_dir.mkdir(parents=True, exist_ok=True)
            library = target_dir / library_name
            library.write_bytes(f"{goos}/{goarch}".encode())
            archive = root / f"opencode-session-mapper_{version}_{goos}_{goarch}.zip"
            info = zipfile.ZipInfo(library_name, EPOCH)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100755 << 16
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr(info, library.read_bytes())
            args = types.SimpleNamespace(
                version=version,
                revision=revision,
                goos=goos,
                goarch=goarch,
                extension=extension,
                archive=archive,
                library=library,
                go_version="go version go1.26.0 test/test",
                runner_image="test-runner",
                builder_image="test-builder",
                output=root / f"build-metadata_{goos}_{goarch}.json",
            )
            MODULE.build_metadata(args)
        return version, revision

    def create_integration_report(self, root: Path, version: str, revision: str,
                                  name="official-host-integration.json"):
        library_sha256 = hashlib.sha256(b"linux/amd64").hexdigest()
        report = {
            "schema_version": 1,
            "source_revision": revision,
            "official_host": {
                "version": "v7.3.4",
                "commit": "8335eac731946bd4eff18f500653f93736df53d6",
                "build_date": "2026-09-15T14:07:07Z",
                "image_repository": "eceasy/cli-proxy-api",
                "image_digest": (
                    "sha256:"
                    "97825da3009f98acf78b5c172fde650a5fbe7a690950a69ce6d7b535d77d4266"
                ),
            },
            "plugin": {
                "id": "opencode-session-mapper",
                "version": version,
                "library_sha256": library_sha256,
            },
            "assertions": dict.fromkeys(REQUIRED_ASSERTIONS, True),
        }
        path = root / name
        path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return path

    def assemble(self, root: Path, output_name, integration=None):
        version = "0.3.2"
        revision = "1" * 40
        if integration is None:
            integration = self.create_integration_report(root, version, revision)
        MODULE.assemble_manifest(
            types.SimpleNamespace(
                version=version,
                revision=revision,
                input_dir=root,
                integration_report=integration,
                output=root / output_name,
            )
        )
        return root / output_name

    def mutate_integration_report(self, root: Path, version: str, revision: str, mutate,
                                  name="official-host-integration.json"):
        path = self.create_integration_report(root, version, revision, name=name)
        report = json.loads(path.read_text(encoding="utf-8"))
        mutate(report)
        path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return path

    def test_build_then_assemble_is_deterministic(self):
        with tempfile.TemporaryDirectory() as first_name, tempfile.TemporaryDirectory() as second_name:
            first = Path(first_name)
            second = Path(second_name)
            version, revision = self.create_input(first)
            shutil.copytree(first, second, dirs_exist_ok=True)

            first_output = self.assemble(first, "release-manifest.json")
            second_output = self.assemble(second, "release-manifest.json")
            self.assertEqual(first_output.read_bytes(), second_output.read_bytes())
            manifest = json.loads(first_output.read_text(encoding="utf-8"))
            self.assertEqual(manifest["plugin"]["version"], version)
            self.assertEqual(manifest["plugin"]["id"], "opencode-session-mapper")
            self.assertEqual(manifest["plugin"]["abi_version"], 1)
            self.assertEqual(manifest["source"]["commit"], revision)
            self.assertEqual(len(manifest["builds"]), 5)
            self.assertEqual(manifest["max_envelope_bytes"], 67108864)
            targets = sorted(
                (item["target"]["goos"], item["target"]["goarch"]) for item in manifest["builds"]
            )
            self.assertEqual(
                targets,
                [
                    ("darwin", "amd64"),
                    ("darwin", "arm64"),
                    ("linux", "amd64"),
                    ("linux", "arm64"),
                    ("windows", "amd64"),
                ],
            )

    def test_assemble_records_exact_host_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, revision = self.create_input(root)
            output = self.assemble(root, "release-manifest.json")
            manifest = json.loads(output.read_text(encoding="utf-8"))
            integration = manifest["official_host_integration"]
            self.assertEqual(integration["schema_version"], 1)
            self.assertEqual(integration["source_revision"], revision)
            self.assertEqual(integration["official_host"]["version"], "v7.3.4")
            self.assertEqual(
                integration["official_host"]["commit"], MODULE.OFFICIAL_HOST_COMMIT
            )
            self.assertEqual(
                integration["official_host"]["build_date"], MODULE.OFFICIAL_HOST_BUILD_DATE
            )
            self.assertEqual(
                integration["official_host"]["image_repository"],
                MODULE.OFFICIAL_HOST_IMAGE_REPOSITORY,
            )
            self.assertTrue(integration["official_host"]["image_digest"].startswith("sha256:"))
            self.assertEqual(integration["plugin"]["id"], "opencode-session-mapper")
            self.assertEqual(integration["plugin"]["version"], version)
            self.assertEqual(
                integration["plugin"]["library_sha256"], hashlib.sha256(b"linux/amd64").hexdigest()
            )
            for key in REQUIRED_ASSERTIONS:
                self.assertTrue(integration["assertions"][key])

    def test_build_rejects_unexpected_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            library = root / "opencode-session-mapper.so"
            library.write_bytes(b"library")
            archive = root / "opencode-session-mapper_0.3.2_linux_riscv64.zip"
            info = zipfile.ZipInfo("opencode-session-mapper.so", EPOCH)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100755 << 16
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr(info, library.read_bytes())
            with self.assertRaises(SystemExit):
                MODULE.build_metadata(
                    types.SimpleNamespace(
                        version="0.3.2",
                        revision="1" * 40,
                        goos="linux",
                        goarch="riscv64",
                        extension="so",
                        archive=archive,
                        library=library,
                        go_version="go version go1.26.0 test/test",
                        runner_image="test-runner",
                        builder_image="test-builder",
                        output=root / "build-metadata_linux_riscv64.json",
                    )
                )

    def test_assemble_rejects_missing_integration_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.create_input(root)
            with self.assertRaises(SystemExit):
                MODULE.assemble_manifest(
                    types.SimpleNamespace(
                        version="0.3.2",
                        revision="1" * 40,
                        input_dir=root,
                        integration_report=root / "does-not-exist.json",
                        output=root / "release-manifest.json",
                    )
                )

    def test_assemble_rejects_malformed_integration_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.create_input(root)
            path = root / "official-host-integration.json"
            path.write_text("{not-json", encoding="utf-8")
            with self.assertRaises(SystemExit) as context:
                self.assemble(root, "release-manifest.json", integration=path)
            self.assertNotIn("not-json", str(context.exception))

    def test_assemble_rejects_each_false_assertion(self):
        for key in REQUIRED_ASSERTIONS:
            with self.subTest(assertion=key):
                with tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    version, revision = self.create_input(root)
                    path = self.mutate_integration_report(
                        root, version, revision,
                        lambda report, k=key: report["assertions"].__setitem__(k, False),
                    )
                    with self.assertRaises(SystemExit):
                        self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_extra_false_assertion_key(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, revision = self.create_input(root)
            path = self.mutate_integration_report(
                root, version, revision,
                lambda report: report["assertions"].__setitem__("rogue_probe", False),
            )
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_extra_true_assertion_key(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, revision = self.create_input(root)
            path = self.mutate_integration_report(
                root, version, revision,
                lambda report: report["assertions"].__setitem__("extra_probe", True),
            )
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_library_checksum_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, revision = self.create_input(root)
            path = self.mutate_integration_report(
                root, version, revision,
                lambda report: report["plugin"].__setitem__("library_sha256", "e" * 64),
            )
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_source_revision_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, _revision = self.create_input(root)
            path = self.mutate_integration_report(root, version, "2" * 40, lambda report: None)
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_plugin_version_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _version, revision = self.create_input(root)
            path = self.mutate_integration_report(root, "9.9.9", revision, lambda report: None)
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_host_identity_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, revision = self.create_input(root)
            path = self.mutate_integration_report(
                root, version, revision,
                lambda report: report["official_host"].__setitem__(
                    "image_digest", "sha256:" + "f" * 64
                ),
            )
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json", integration=path)

    def test_assemble_rejects_extra_untrusted_shape(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version, revision = self.create_input(root)
            marker = "untrusted-shape-marker"
            path = self.mutate_integration_report(
                root, version, revision, lambda report: report.__setitem__(marker, True)
            )
            with self.assertRaises(SystemExit) as context:
                self.assemble(root, "release-manifest.json", integration=path)
            self.assertNotIn(marker, str(context.exception))

    def test_build_rejects_unexpected_go_toolchain(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            library = root / "opencode-session-mapper.so"
            library.write_bytes(b"library")
            archive = root / "opencode-session-mapper_0.3.2_linux_amd64.zip"
            info = zipfile.ZipInfo(library.name, EPOCH)
            info.external_attr = 0o100755 << 16
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr(info, library.read_bytes())
            with self.assertRaises(SystemExit):
                MODULE.build_metadata(
                    types.SimpleNamespace(
                        version="0.3.2",
                        revision="1" * 40,
                        goos="linux",
                        goarch="amd64",
                        extension="so",
                        archive=archive,
                        library=library,
                        go_version="go version go1.27.0 linux/amd64",
                        runner_image="test-runner",
                        builder_image="test-builder",
                        output=root / "build-metadata_linux_amd64.json",
                    )
                )

    def test_assemble_rejects_target_library_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.create_input(root)
            metadata_path = root / "build-metadata_linux_amd64.json"
            metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
            metadata["artifact"]["library"]["name"] = "opencode-session-mapper.dll"
            metadata_path.write_text(
                json.dumps(metadata, indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json")

    def test_assemble_rejects_wrong_target_set(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.create_input(root)
            stale = sorted(root.glob("build-metadata_*.json"))[0]
            stale.unlink()
            with self.assertRaises(SystemExit):
                self.assemble(root, "release-manifest.json")

    def test_validate_archive_rejects_wrong_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "opencode-session-mapper_0.3.2_linux_amd64.zip"
            info = zipfile.ZipInfo("opencode-session-mapper.so", EPOCH)
            info.external_attr = 0o100644 << 16
            data = b"library"
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr(info, data)
            build = {
                "artifact": {
                    "archive": {"name": archive.name, "sha256": MODULE.sha256_file(archive)},
                    "library": {
                        "name": "opencode-session-mapper.so",
                        "sha256": hashlib.sha256(data).hexdigest(),
                    },
                }
            }
            with self.assertRaises(SystemExit):
                MODULE.validate_archive(root, build)

    def test_validate_archive_rejects_wrong_timestamp(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "opencode-session-mapper_0.3.2_linux_amd64.zip"
            info = zipfile.ZipInfo("opencode-session-mapper.so", (2026, 9, 16, 0, 0, 0))
            info.external_attr = 0o100755 << 16
            data = b"library"
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr(info, data)
            build = {
                "artifact": {
                    "archive": {"name": archive.name, "sha256": MODULE.sha256_file(archive)},
                    "library": {
                        "name": "opencode-session-mapper.so",
                        "sha256": hashlib.sha256(data).hexdigest(),
                    },
                }
            }
            with self.assertRaises(SystemExit):
                MODULE.validate_archive(root, build)


if __name__ == "__main__":
    unittest.main()
