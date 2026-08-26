#!/usr/bin/env python3
"""Build deterministic Netspeed source and supported-client release archives."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import tempfile
import zipfile

MODULE = "github.com/yellowman/netspeed"
BUILDINFO = f"{MODULE}/internal/buildinfo"
PROGRAMS = ("netspeed", "netspeedd")
DEFAULT_PLATFORMS = (
    "linux/amd64",
    "linux/arm64",
    "openbsd/amd64",
    "openbsd/arm64",
    "windows/amd64",
    "windows/arm64",
)
VERSION_RE = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$")
BINARY_PAYLOAD = (
    "README.md",
    "LICENSE",
    "C_CLIENT_PARITY.md",
    "RELEASE_QUALIFICATION.md",
    "HTTP_DEPLOYMENT.md",
    "SERVICE_HARDENING.md",
    "MEASUREMENT_PROTOCOL_V2.md",
    "WEBRTC_LIFECYCLE.md",
    "configs/netspeedd.env.example",
    "locations.json",
)


@dataclass(frozen=True)
class ReleaseMetadata:
    version: str
    commit: str
    source_date_epoch: int
    date: str
    go_version: str


def run(root: Path, args: list[str], *, env: dict[str, str] | None = None, capture: bool = False) -> str:
    result = subprocess.run(
        args,
        cwd=root,
        env=env,
        check=True,
        text=True,
        capture_output=capture,
    )
    return result.stdout.strip() if capture else ""


def git(root: Path, *args: str) -> str:
    return run(root, ["git", *args], capture=True)


def exact_tag(root: Path) -> str | None:
    result = subprocess.run(
        ["git", "describe", "--tags", "--exact-match", "HEAD"],
        cwd=root,
        text=True,
        capture_output=True,
    )
    return result.stdout.strip() if result.returncode == 0 else None


def validate_source_tree(root: Path) -> None:
    env = os.environ.copy()
    env["PYTHONDONTWRITEBYTECODE"] = "1"
    for script in ("check_source_hygiene.py", "check_markdown_links.py"):
        run(root, [sys.executable, str(root / "scripts" / script), "--root", str(root)], env=env)
    run(root, ["git", "diff", "--check"])
    run(root, ["git", "diff", "--cached", "--check"])


def metadata(root: Path, requested_version: str | None, allow_dirty: bool) -> ReleaseMetadata:
    status = git(root, "status", "--porcelain")
    if status and not allow_dirty:
        raise SystemExit("release builds require a clean Git working tree (or --allow-dirty)")

    version = requested_version or exact_tag(root)
    if not version:
        raise SystemExit("--version is required when HEAD is not exactly tagged")
    if not version.startswith("v"):
        version = "v" + version
    if not VERSION_RE.fullmatch(version):
        raise SystemExit(f"invalid release version: {version!r}")

    commit = git(root, "rev-parse", "HEAD")
    if status:
        commit += "-dirty"
    epoch_text = os.environ.get("SOURCE_DATE_EPOCH") or git(root, "show", "-s", "--format=%ct", "HEAD")
    try:
        epoch = int(epoch_text)
    except ValueError as exc:
        raise SystemExit(f"invalid SOURCE_DATE_EPOCH: {epoch_text!r}") from exc
    if epoch < 0:
        raise SystemExit("SOURCE_DATE_EPOCH cannot be negative")
    date = datetime.fromtimestamp(epoch, timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    go_version = run(root, ["go", "version"], capture=True)
    return ReleaseMetadata(
        version=version,
        commit=commit,
        source_date_epoch=epoch,
        date=date,
        go_version=go_version,
    )


def tracked_files(root: Path) -> list[Path]:
    output = subprocess.run(
        ["git", "ls-files", "-z"], cwd=root, check=True, capture_output=True
    ).stdout
    paths = [root / os.fsdecode(value) for value in output.split(b"\0") if value]
    return sorted(path for path in paths if path.is_file() or path.is_symlink())


def zip_datetime(epoch: int) -> tuple[int, int, int, int, int, int]:
    dt = datetime.fromtimestamp(epoch, timezone.utc)
    if dt.year < 1980:
        dt = dt.replace(year=1980, month=1, day=1, hour=0, minute=0, second=0)
    # ZIP timestamps have two-second resolution.
    return (dt.year, dt.month, dt.day, dt.hour, dt.minute, dt.second - dt.second % 2)


def add_bytes(archive: zipfile.ZipFile, name: str, data: bytes, epoch: int, mode: int = 0o644) -> None:
    info = zipfile.ZipInfo(PurePosixPath(name).as_posix(), zip_datetime(epoch))
    info.create_system = 3
    info.external_attr = (stat.S_IFREG | mode) << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    archive.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)


def add_path(archive: zipfile.ZipFile, source: Path, name: str, epoch: int, mode: int | None = None) -> None:
    if source.is_symlink():
        raise SystemExit(f"release archives do not support symlinks: {source}")
    actual_mode = mode if mode is not None else (0o755 if source.stat().st_mode & stat.S_IXUSR else 0o644)
    add_bytes(archive, name, source.read_bytes(), epoch, actual_mode)


def write_source_archive(root: Path, output: Path, meta: ReleaseMetadata) -> None:
    prefix = f"netspeed-{meta.version.removeprefix('v')}-source"
    with zipfile.ZipFile(output, "w") as archive:
        for source in tracked_files(root):
            rel = source.relative_to(root).as_posix()
            add_path(archive, source, f"{prefix}/{rel}", meta.source_date_epoch)


def build_program(root: Path, destination: Path, program: str, goos: str, goarch: str, meta: ReleaseMetadata) -> None:
    suffix = ".exe" if goos == "windows" else ""
    output = destination / f"{program}{suffix}"
    ldflags = " ".join((
        "-s",
        "-w",
        "-buildid=",
        f"-X {BUILDINFO}.Version={meta.version}",
        f"-X {BUILDINFO}.Commit={meta.commit}",
        f"-X {BUILDINFO}.Date={meta.date}",
    ))
    env = os.environ.copy()
    env.update({
        "CGO_ENABLED": "0",
        "GOOS": goos,
        "GOARCH": goarch,
        "SOURCE_DATE_EPOCH": str(meta.source_date_epoch),
    })
    run(root, [
        "go", "build",
        "-mod=readonly",
        "-trimpath",
        "-buildvcs=false",
        "-ldflags", ldflags,
        "-o", str(output),
        f"./cmd/{program}",
    ], env=env)


def write_binary_archive(
    root: Path,
    output: Path,
    stage: Path,
    goos: str,
    goarch: str,
    meta: ReleaseMetadata,
    c_binary: Path | None = None,
) -> None:
    prefix = f"netspeed-{meta.version.removeprefix('v')}-{goos}-{goarch}"
    build_text = (
        f"version={meta.version}\n"
        f"commit={meta.commit}\n"
        f"source_date_epoch={meta.source_date_epoch}\n"
        f"date={meta.date}\n"
        f"goos={goos}\n"
        f"goarch={goarch}\n"
        f"go_version={meta.go_version}\n"
        f"c_client={'included' if c_binary else 'not-published-for-platform'}\n"
    ).encode()
    with zipfile.ZipFile(output, "w") as archive:
        for program in PROGRAMS:
            suffix = ".exe" if goos == "windows" else ""
            add_path(archive, stage / f"{program}{suffix}", f"{prefix}/{program}{suffix}", meta.source_date_epoch, 0o755)
        if c_binary is not None:
            c_suffix = ".exe" if goos == "windows" else ""
            add_path(
                archive,
                c_binary,
                f"{prefix}/netspeed-c{c_suffix}",
                meta.source_date_epoch,
                0o755,
            )
        for rel in BINARY_PAYLOAD:
            source = root / rel
            if not source.exists():
                raise SystemExit(f"release payload is missing {rel}")
            add_path(archive, source, f"{prefix}/{rel}", meta.source_date_epoch, 0o644)
        for source in sorted((root / "web").rglob("*")):
            if source.is_file():
                rel = source.relative_to(root).as_posix()
                add_path(archive, source, f"{prefix}/{rel}", meta.source_date_epoch, 0o644)
        add_bytes(archive, f"{prefix}/BUILDINFO.txt", build_text, meta.source_date_epoch)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_platform(value: str) -> tuple[str, str]:
    parts = value.split("/")
    if len(parts) != 2 or not all(parts):
        raise argparse.ArgumentTypeError("platform must be GOOS/GOARCH")
    return parts[0], parts[1]


def parse_c_binary(value: str) -> tuple[tuple[str, str], Path]:
    platform_text, separator, path_text = value.partition("=")
    if not separator or not path_text:
        raise argparse.ArgumentTypeError("C binary must be GOOS/GOARCH=PATH")
    return parse_platform(platform_text), Path(path_text)


def resolve_c_binaries(
    root: Path,
    values: list[tuple[tuple[str, str], Path]] | None,
) -> dict[tuple[str, str], Path]:
    result: dict[tuple[str, str], Path] = {}
    for platform, supplied_path in values or []:
        if platform in result:
            raise SystemExit(f"duplicate C binary for {platform[0]}/{platform[1]}")
        path = supplied_path if supplied_path.is_absolute() else root / supplied_path
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"C binary is not a regular file: {path}")
        result[platform] = path.resolve()
    return result


def prepare_output(root: Path, output: Path) -> None:
    if output.is_symlink():
        raise SystemExit(f"refusing symlink release output: {output}")
    resolved_root = root.resolve()
    resolved_output = output.resolve()
    if resolved_output == resolved_root or resolved_output in resolved_root.parents:
        raise SystemExit(f"refusing unsafe release output: {resolved_output}")
    if resolved_output.exists() and not resolved_output.is_dir():
        raise SystemExit(f"release output is not a directory: {resolved_output}")

    resolved_output.mkdir(parents=True, exist_ok=True)
    for child in resolved_output.iterdir():
        safe_file = (
            not child.is_symlink()
            and child.is_file()
            and (
                child.name in {"SHA256SUMS", "release-manifest.json"}
                or (child.name.startswith("netspeed-") and child.suffix == ".zip")
            )
        )
        if not safe_file:
            raise SystemExit(
                f"refusing to remove unknown release-output entry: {child}"
            )
    for child in resolved_output.iterdir():
        child.unlink()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", help="release version, such as v1.2.3; defaults to the exact Git tag")
    parser.add_argument("--output", type=Path, default=Path("dist"))
    parser.add_argument("--source-only", action="store_true")
    parser.add_argument("--allow-dirty", action="store_true", help="developer-only; official releases must be clean")
    parser.add_argument("--platform", action="append", type=parse_platform, metavar="GOOS/GOARCH")
    parser.add_argument(
        "--c-binary",
        action="append",
        type=parse_c_binary,
        metavar="GOOS/GOARCH=PATH",
        help="include a prequalified native C client in the matching platform archive",
    )
    parser.add_argument(
        "--require-c-platform",
        action="append",
        type=parse_platform,
        metavar="GOOS/GOARCH",
        help="fail unless --c-binary supplies this platform",
    )
    args = parser.parse_args()

    root = Path(__file__).resolve().parents[1]
    output = args.output if args.output.is_absolute() else root / args.output
    meta = metadata(root, args.version, args.allow_dirty)
    validate_source_tree(root)
    platforms = tuple(args.platform or (parse_platform(value) for value in DEFAULT_PLATFORMS))
    c_binaries = resolve_c_binaries(root, args.c_binary)
    requested_platforms = set(platforms)
    unknown_c_platforms = set(c_binaries) - requested_platforms
    if unknown_c_platforms:
        formatted = ", ".join(f"{goos}/{goarch}" for goos, goarch in sorted(unknown_c_platforms))
        raise SystemExit(f"C binaries supplied for platforms not being released: {formatted}")
    missing_required = set(args.require_c_platform or []) - set(c_binaries)
    if missing_required:
        formatted = ", ".join(f"{goos}/{goarch}" for goos, goarch in sorted(missing_required))
        raise SystemExit(f"required C binaries were not supplied: {formatted}")

    prepare_output(root, output)

    source_archive = output / f"netspeed-{meta.version.removeprefix('v')}-source.zip"
    write_source_archive(root, source_archive, meta)
    artifacts = [source_archive]

    if not args.source_only:
        with tempfile.TemporaryDirectory(prefix="netspeed-release-") as temporary:
            stage_root = Path(temporary)
            for goos, goarch in platforms:
                stage = stage_root / f"{goos}-{goarch}"
                stage.mkdir(parents=True)
                for program in PROGRAMS:
                    build_program(root, stage, program, goos, goarch, meta)
                archive = output / f"netspeed-{meta.version.removeprefix('v')}-{goos}-{goarch}.zip"
                write_binary_archive(
                    root,
                    archive,
                    stage,
                    goos,
                    goarch,
                    meta,
                    c_binaries.get((goos, goarch)),
                )
                artifacts.append(archive)

    artifact_records = [
        {"name": path.name, "sha256": sha256(path), "bytes": path.stat().st_size}
        for path in sorted(artifacts)
    ]
    manifest = {
        "schemaVersion": 1,
        "version": meta.version,
        "commit": meta.commit,
        "sourceDateEpoch": meta.source_date_epoch,
        "date": meta.date,
        "goVersion": meta.go_version,
        "cClientPlatforms": [
            f"{goos}/{goarch}" for goos, goarch in sorted(c_binaries)
        ],
        "artifacts": artifact_records,
    }
    manifest_path = output / "release-manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    sums_path = output / "SHA256SUMS"
    sums_path.write_text(
        "".join(f"{record['sha256']}  {record['name']}\n" for record in artifact_records),
        encoding="utf-8",
    )
    print(f"release artifacts written to {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
