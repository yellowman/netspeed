from pathlib import Path
import importlib.util
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("rtc_metadata", ROOT / "scripts/write_libdatachannel_pc.py")
METADATA = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(METADATA)


class MetadataTests(unittest.TestCase):
    def fixture(self, root: Path, runtime: str = "stdc++") -> tuple[Path, Path]:
        prefix, build = root / "rtc", root / "build"
        (prefix / "include/rtc").mkdir(parents=True)
        (prefix / "include/rtc/rtc.h").write_text("/* C API */\n")
        (prefix / "lib/cmake/LibDataChannel").mkdir(parents=True)
        for name in ("datachannel", "juice", "usrsctp"):
            (prefix / f"lib/lib{name}.a").write_bytes(b"archive fixture")
        (prefix / "lib/cmake/LibDataChannel/LibDataChannelConfigVersion.cmake").write_text('set(PACKAGE_VERSION "0.23.2")\n')
        (build / "CMakeFiles/4.0").mkdir(parents=True)
        (build / "CMakeFiles/4.0/CMakeCXXCompiler.cmake").write_text(f'set(CMAKE_CXX_IMPLICIT_LINK_LIBRARIES "{runtime};m;c")\n')
        return prefix, build

    def test_static_dependencies_and_runtime(self):
        for runtime in ("stdc++", "c++"):
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                prefix, build = self.fixture(Path(directory), runtime)
                content = METADATA.write_metadata(prefix, build).read_text()
                self.assertIn(f"-l{runtime}", content)
                self.assertIn("-ljuice -lusrsctp -lssl -lcrypto", content)
                self.assertIn("-DRTC_STATIC=1", content)
                self.assertIn("Version: 0.23.2", content)

    def test_preserves_existing_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            prefix, build = self.fixture(Path(directory))
            output = prefix / "lib/pkgconfig/libdatachannel.pc"
            output.parent.mkdir()
            output.write_text("upstream metadata")
            self.assertEqual(METADATA.write_metadata(prefix, build).read_text(), "upstream metadata")

    def test_rejects_missing_static_dependency(self):
        with tempfile.TemporaryDirectory() as directory:
            prefix, build = self.fixture(Path(directory))
            (prefix / "lib/libjuice.a").unlink()
            with self.assertRaisesRegex(ValueError, "archives are missing"):
                METADATA.write_metadata(prefix, build)
