#!/usr/bin/env python3
# SPDX-License-Identifier: MIT

"""Release provenance manifest tooling for the opencode-session-mapper plugin.

`build` records per-target build metadata; `assemble` validates the five
release archives plus the sanitized official-Host integration report and
writes the immutable release manifest. No network access, no `gh` calls.
"""

import argparse
import hashlib
import json
import re
import zipfile
from pathlib import Path

PLUGIN_ID = "opencode-session-mapper"
REPOSITORY = "https://github.com/ahoo/cpa-plugin-opencode-session-mapper"
ABI_VERSION = 1
EXPECTED_TARGETS = (
    ("linux", "amd64", "so"),
    ("linux", "arm64", "so"),
    ("darwin", "amd64", "dylib"),
    ("darwin", "arm64", "dylib"),
    ("windows", "amd64", "dll"),
)
MAX_ENVELOPE_BYTES = 67108864
OFFICIAL_HOST_VERSION = "v7.3.4"
OFFICIAL_HOST_COMMIT = "8335eac731946bd4eff18f500653f93736df53d6"
OFFICIAL_HOST_BUILD_DATE = "2026-09-15T14:07:07Z"
OFFICIAL_HOST_IMAGE_REPOSITORY = "eceasy/cli-proxy-api"
OFFICIAL_HOST_IMAGE_DIGEST = (
    "sha256:97825da3009f98acf78b5c172fde650a5fbe7a690950a69ce6d7b535d77d4266"
)

INTEGRATION_SCHEMA_VERSION = 1
INTEGRATION_TOP_LEVEL_KEYS = frozenset(
    (
        "schema_version",
        "source_revision",
        "official_host",
        "plugin",
        "assertions",
    )
)
INTEGRATION_PLUGIN_KEYS = frozenset(("id", "version", "library_sha256"))
REQUIRED_ASSERTIONS = frozenset(
    (
        "large_envelope_forwarded",
        "session_header_mapped",
        "client_header_mapped",
        "mock_request_count_one",
        "interceptor_errors_zero",
        "recovered_panics_zero",
        "synthetic_marker_absent_from_host_logs",
    )
)

ARCHIVE_EPOCH = (1980, 1, 1, 0, 0, 0)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def canonical_library(goos: str, extension: str) -> str:
    expected = {"linux": "so", "darwin": "dylib", "windows": "dll"}
    if expected.get(goos) != extension:
        raise SystemExit(f"invalid extension {extension!r} for {goos}")
    return f"{PLUGIN_ID}.{extension}"


def require_version(value: str) -> None:
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", value):
        raise SystemExit("release version must be exact major.minor.patch")


def require_revision(value: str) -> None:
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise SystemExit("source revision must be a lowercase 40-character commit")


def write_json_exclusive(path: Path, value) -> None:
    data = (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()
    try:
        with path.open("xb") as stream:
            stream.write(data)
    except FileExistsError as error:
        raise SystemExit(f"refusing to replace existing file: {path}") from error


def build_metadata(args) -> None:
    require_version(args.version)
    require_revision(args.revision)
    if re.fullmatch(r"go version go1\.26(?:\.[0-9]+)? [^/\s]+/[^\s]+", args.go_version) is None:
        raise SystemExit("unexpected Go toolchain version")
    target = (args.goos, args.goarch, args.extension)
    if target not in EXPECTED_TARGETS:
        raise SystemExit(f"unexpected release target: {target}")
    archive = args.archive.resolve()
    library = args.library.resolve()
    if not archive.is_file() or not library.is_file():
        raise SystemExit("archive and library must be regular files")
    expected_archive = f"{PLUGIN_ID}_{args.version}_{args.goos}_{args.goarch}.zip"
    if archive.name != expected_archive:
        raise SystemExit(f"archive name mismatch: {archive.name}")
    library_name = canonical_library(args.goos, args.extension)
    if library.name != library_name:
        raise SystemExit(f"library name mismatch: {library.name}")
    metadata = {
        "schema_version": 1,
        "plugin": {
            "id": PLUGIN_ID,
            "version": args.version,
            "abi_version": ABI_VERSION,
        },
        "source": {"repository": REPOSITORY, "commit": args.revision},
        "target": {"goos": args.goos, "goarch": args.goarch},
        "toolchain": {
            "go_version": args.go_version,
            "runner_image": args.runner_image,
            "builder_image": args.builder_image,
        },
        "artifact": {
            "archive": {"name": archive.name, "sha256": sha256_file(archive)},
            "library": {"name": library_name, "sha256": sha256_file(library)},
        },
        "verification": {
            "registration": True,
            "source_gates_dependency": True,
        },
    }
    write_json_exclusive(args.output, metadata)


def _is_sha256_hex(value) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def validate_integration_report(report, version: str, revision: str, library_sha256: str):
    if not isinstance(report, dict) or set(report.keys()) != set(INTEGRATION_TOP_LEVEL_KEYS):
        raise SystemExit("integration report shape mismatch")
    if report.get("schema_version") != INTEGRATION_SCHEMA_VERSION:
        raise SystemExit("integration report schema mismatch")
    if report.get("source_revision") != revision:
        raise SystemExit("integration report source revision mismatch")

    official_host = report.get("official_host")
    if official_host != {
        "version": OFFICIAL_HOST_VERSION,
        "commit": OFFICIAL_HOST_COMMIT,
        "build_date": OFFICIAL_HOST_BUILD_DATE,
        "image_repository": OFFICIAL_HOST_IMAGE_REPOSITORY,
        "image_digest": OFFICIAL_HOST_IMAGE_DIGEST,
    }:
        raise SystemExit("integration report Host identity mismatch")

    plugin = report.get("plugin")
    if not isinstance(plugin, dict) or set(plugin.keys()) != set(INTEGRATION_PLUGIN_KEYS):
        raise SystemExit("integration report plugin identity mismatch")
    if plugin.get("id") != PLUGIN_ID or plugin.get("version") != version:
        raise SystemExit("integration report plugin identity mismatch")
    if not _is_sha256_hex(plugin.get("library_sha256")):
        raise SystemExit("integration report plugin identity mismatch")
    if plugin.get("library_sha256") != library_sha256:
        raise SystemExit("integration report library checksum mismatch")

    assertions = report.get("assertions")
    if not isinstance(assertions, dict) or set(assertions) != set(REQUIRED_ASSERTIONS):
        raise SystemExit("integration report assertion shape mismatch")
    for item in assertions.values():
        if item is not True:
            raise SystemExit("integration report assertion failed")

    return {
        "schema_version": INTEGRATION_SCHEMA_VERSION,
        "source_revision": revision,
        "official_host": {
            "version": official_host["version"],
            "commit": official_host["commit"],
            "build_date": official_host["build_date"],
            "image_repository": official_host["image_repository"],
            "image_digest": official_host["image_digest"],
        },
        "plugin": {
            "id": PLUGIN_ID,
            "version": version,
            "library_sha256": library_sha256,
        },
        "assertions": {key: True for key in sorted(assertions.keys())},
    }


def load_integration_report(path: Path, version: str, revision: str, library_sha256: str):
    try:
        report = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as error:
        raise SystemExit("integration report is missing") from error
    except (OSError, json.JSONDecodeError, UnicodeDecodeError) as error:
        raise SystemExit("integration report is malformed") from error
    return validate_integration_report(report, version, revision, library_sha256)


def validate_archive(input_dir: Path, build) -> None:
    artifact = build["artifact"]
    archive_info = artifact["archive"]
    library_info = artifact["library"]
    archive = input_dir / archive_info["name"]
    if not archive.is_file() or sha256_file(archive) != archive_info["sha256"]:
        raise SystemExit(f"archive checksum mismatch: {archive.name}")
    with zipfile.ZipFile(archive) as bundle:
        entries = bundle.infolist()
        if len(entries) != 1 or entries[0].filename != library_info["name"]:
            raise SystemExit(f"archive layout mismatch: {archive.name}")
        entry = entries[0]
        if entry.date_time != ARCHIVE_EPOCH:
            raise SystemExit(f"archive timestamp mismatch: {archive.name}")
        if (entry.external_attr >> 16) & 0o777 != 0o755:
            raise SystemExit(f"archive mode mismatch: {archive.name}")
        data = bundle.read(entry)
        if hashlib.sha256(data).hexdigest() != library_info["sha256"]:
            raise SystemExit(f"library checksum mismatch: {archive.name}")


def assemble_manifest(args) -> None:
    require_version(args.version)
    require_revision(args.revision)
    input_dir = args.input_dir.resolve()
    paths = sorted(input_dir.glob("build-metadata_*.json"))
    if len(paths) != len(EXPECTED_TARGETS):
        raise SystemExit(f"expected {len(EXPECTED_TARGETS)} build metadata files, found {len(paths)}")
    builds = [json.loads(path.read_text(encoding="utf-8")) for path in paths]
    expected_pairs = {(goos, goarch) for goos, goarch, _extension in EXPECTED_TARGETS}
    actual_pairs = {(item["target"]["goos"], item["target"]["goarch"]) for item in builds}
    if actual_pairs != expected_pairs or len(actual_pairs) != len(builds):
        raise SystemExit("release target set mismatch")

    for build in builds:
        if build.get("schema_version") != 1:
            raise SystemExit("build metadata schema mismatch")
        if build.get("plugin") != {
            "id": PLUGIN_ID,
            "version": args.version,
            "abi_version": ABI_VERSION,
        }:
            raise SystemExit("plugin identity mismatch across build metadata")
        if build.get("source") != {"repository": REPOSITORY, "commit": args.revision}:
            raise SystemExit("source identity mismatch across build metadata")
        target = build.get("target")
        extensions = [
            extension
            for goos, goarch, extension in EXPECTED_TARGETS
            if target == {"goos": goos, "goarch": goarch}
        ]
        library = build.get("artifact", {}).get("library", {})
        if len(extensions) != 1 or library.get("name") != canonical_library(
            target["goos"], extensions[0]
        ):
            raise SystemExit("release target library mismatch")
        if build.get("verification") != {"registration": True, "source_gates_dependency": True}:
            raise SystemExit("build verification declaration mismatch")
        validate_archive(input_dir, build)

    linux_builds = [
        build
        for build in builds
        if build.get("target") == {"goos": "linux", "goarch": "amd64"}
    ]
    if len(linux_builds) != 1:
        raise SystemExit("linux-amd64 build metadata is missing")
    linux_library_sha256 = linux_builds[0]["artifact"]["library"]["sha256"]
    if not _is_sha256_hex(linux_library_sha256):
        raise SystemExit("linux-amd64 library checksum mismatch")
    integration_path = getattr(args, "integration_report", None)
    if integration_path is None:
        raise SystemExit("integration report is missing")
    integration = load_integration_report(
        Path(integration_path), args.version, args.revision, linux_library_sha256
    )

    builds.sort(key=lambda item: (item["target"]["goos"], item["target"]["goarch"]))
    manifest = {
        "schema_version": 1,
        "plugin": {
            "id": PLUGIN_ID,
            "version": args.version,
            "abi_version": ABI_VERSION,
        },
        "source": {"repository": REPOSITORY, "commit": args.revision},
        "builds": [
            {
                "target": build["target"],
                "toolchain": build["toolchain"],
                "artifact": build["artifact"],
                "verification": build["verification"],
            }
            for build in builds
        ],
        "max_envelope_bytes": MAX_ENVELOPE_BYTES,
        "official_host_integration": integration,
    }
    write_json_exclusive(args.output, manifest)


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)

    build = commands.add_parser("build")
    build.add_argument("--version", required=True)
    build.add_argument("--revision", required=True)
    build.add_argument("--goos", required=True)
    build.add_argument("--goarch", required=True)
    build.add_argument("--extension", required=True)
    build.add_argument("--archive", required=True, type=Path)
    build.add_argument("--library", required=True, type=Path)
    build.add_argument("--go-version", required=True)
    build.add_argument("--runner-image", required=True)
    build.add_argument("--builder-image", required=True)
    build.add_argument("--output", required=True, type=Path)
    build.set_defaults(handler=build_metadata)

    assemble = commands.add_parser("assemble")
    assemble.add_argument("--version", required=True)
    assemble.add_argument("--revision", required=True)
    assemble.add_argument("--input-dir", required=True, type=Path)
    assemble.add_argument("--integration-report", required=True, type=Path)
    assemble.add_argument("--output", required=True, type=Path)
    assemble.set_defaults(handler=assemble_manifest)
    return root


def main() -> None:
    args = parser().parse_args()
    args.handler(args)


if __name__ == "__main__":
    main()
