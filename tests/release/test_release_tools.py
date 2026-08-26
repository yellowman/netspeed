from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("netspeed_release", ROOT / "scripts" / "release.py")
assert SPEC and SPEC.loader
release = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = release
SPEC.loader.exec_module(release)


class ReleaseToolTests(unittest.TestCase):
    def test_zip_datetime_has_zip_resolution(self) -> None:
        value = release.zip_datetime(1_787_691_487)
        self.assertGreaterEqual(value[0], 1980)
        self.assertEqual(value[-1] % 2, 0)

    def test_deterministic_zip_entry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            outputs = []
            for index in range(2):
                output = root / f"archive-{index}.zip"
                with zipfile.ZipFile(output, "w") as archive:
                    release.add_bytes(archive, "root/example.txt", b"stable\n", 1_787_691_487)
                outputs.append(output.read_bytes())
            self.assertEqual(outputs[0], outputs[1])

    def test_platform_parser(self) -> None:
        self.assertEqual(release.parse_platform("openbsd/amd64"), ("openbsd", "amd64"))

    def test_c_binary_parser(self) -> None:
        self.assertEqual(
            release.parse_c_binary("linux/amd64=bin/netspeed-c"),
            (("linux", "amd64"), Path("bin/netspeed-c")),
        )

    def test_resolve_c_binaries_rejects_duplicate_platform(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "netspeed-c"
            binary.write_bytes(b"native")
            values = [
                (("linux", "amd64"), Path("netspeed-c")),
                (("linux", "amd64"), Path("netspeed-c")),
            ]
            with self.assertRaises(SystemExit):
                release.resolve_c_binaries(root, values)

    def test_binary_archive_includes_qualified_c_client(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stage = root / "stage"
            stage.mkdir()
            (stage / "netspeed").write_bytes(b"go-client")
            (stage / "netspeedd").write_bytes(b"go-daemon")
            c_binary = root / "netspeed-c"
            c_binary.write_bytes(b"c-client")
            output = root / "release.zip"
            meta = release.ReleaseMetadata(
                version="v1.2.3",
                commit="abc123",
                source_date_epoch=1_787_691_487,
                date="2026-08-25T00:00:00Z",
                go_version="go version go1.27.0 linux/amd64",
            )
            with mock.patch.object(release, "BINARY_PAYLOAD", ()):
                release.write_binary_archive(
                    root,
                    output,
                    stage,
                    "linux",
                    "amd64",
                    meta,
                    c_binary,
                )
            with zipfile.ZipFile(output) as archive:
                prefix = "netspeed-1.2.3-linux-amd64"
                self.assertEqual(archive.read(f"{prefix}/netspeed-c"), b"c-client")
                self.assertIn(
                    b"c_client=included",
                    archive.read(f"{prefix}/BUILDINFO.txt"),
                )

    def test_binary_archive_marks_unpublished_c_platform(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stage = root / "stage"
            stage.mkdir()
            (stage / "netspeed").write_bytes(b"go-client")
            (stage / "netspeedd").write_bytes(b"go-daemon")
            output = root / "release.zip"
            meta = release.ReleaseMetadata(
                version="v1.2.3",
                commit="abc123",
                source_date_epoch=1_787_691_487,
                date="2026-08-25T00:00:00Z",
                go_version="go version go1.27.0 linux/amd64",
            )
            with mock.patch.object(release, "BINARY_PAYLOAD", ()):
                release.write_binary_archive(
                    root,
                    output,
                    stage,
                    "linux",
                    "amd64",
                    meta,
                )
            with zipfile.ZipFile(output) as archive:
                self.assertIn(
                    b"c_client=not-published-for-platform",
                    archive.read("netspeed-1.2.3-linux-amd64/BUILDINFO.txt"),
                )

    def test_release_versions(self) -> None:
        accepted = (
            "v1.2.3",
            "v1.2.3-rc.1",
            "v1.2.3+build.7",
            "v1.2.3-rc.1+build.7",
        )
        for version in accepted:
            with self.subTest(version=version):
                self.assertIsNotNone(release.VERSION_RE.fullmatch(version))
        for version in ("1.2.3", "v1.2", "v1.2.3/unsafe"):
            with self.subTest(version=version):
                self.assertIsNone(release.VERSION_RE.fullmatch(version))

    def test_prepare_output_rejects_repository(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(SystemExit):
                release.prepare_output(root, root)

    def test_prepare_output_rejects_repository_ancestor(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            root = parent / "repo"
            root.mkdir()
            with self.assertRaises(SystemExit):
                release.prepare_output(root, parent)

    def test_prepare_output_rejects_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            root = parent / "repo"
            root.mkdir()
            target = parent / "target"
            target.mkdir()
            output = parent / "release"
            output.symlink_to(target, target_is_directory=True)
            with self.assertRaises(SystemExit):
                release.prepare_output(root, output)

    def test_prepare_output_rejects_unknown_entry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            root.mkdir()
            output = Path(directory) / "release"
            output.mkdir()
            unknown = output / "keep.txt"
            unknown.write_text("do not delete\n", encoding="utf-8")
            with self.assertRaises(SystemExit):
                release.prepare_output(root, output)
            self.assertTrue(unknown.exists())

    def test_prepare_output_removes_only_owned_files(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            root.mkdir()
            output = Path(directory) / "release"
            output.mkdir()
            for name in (
                "netspeed-1.2.3-source.zip",
                "release-manifest.json",
                "SHA256SUMS",
            ):
                (output / name).write_text("old\n", encoding="utf-8")
            release.prepare_output(root, output)
            self.assertEqual(list(output.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
