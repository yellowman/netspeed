#!/usr/bin/env python3
"""Supply pkg-config metadata for the pinned static, data-channel-only build."""
from pathlib import Path
import re
import sys


def write_metadata(prefix: Path, build: Path) -> Path:
    prefix = prefix.resolve()
    if any(character.isspace() for character in str(prefix)):
        raise ValueError("libdatachannel prefix must not contain whitespace")
    if not (prefix / "include/rtc/rtc.h").is_file():
        raise ValueError("installed libdatachannel C API header is missing")
    directories = [prefix / "lib", prefix / "lib64"]
    libdir = next((directory for directory in directories if (directory / "libdatachannel.a").is_file()), None)
    if libdir is None or any(not (libdir / f"lib{name}.a").is_file() for name in ("juice", "usrsctp")):
        raise ValueError("installed static datachannel/juice/usrsctp archives are missing")
    output = libdir / "pkgconfig/libdatachannel.pc"
    if output.exists():
        return output
    config = libdir / "cmake/LibDataChannel/LibDataChannelConfigVersion.cmake"
    version = re.search(r'set\(PACKAGE_VERSION "([0-9]+\.[0-9]+\.[0-9]+)"\)', config.read_text())
    if version is None:
        raise ValueError("installed libdatachannel version metadata is missing")
    compilers = list((build / "CMakeFiles").glob("*/CMakeCXXCompiler.cmake"))
    if len(compilers) != 1:
        raise ValueError("CMake C++ compiler metadata is ambiguous or missing")
    implicit = re.search(r'set\(CMAKE_CXX_IMPLICIT_LINK_LIBRARIES "([^"]*)"\)', compilers[0].read_text())
    runtime = next((name for name in implicit.group(1).split(";") if name in ("stdc++", "c++")), None) if implicit else None
    if runtime is None:
        raise ValueError("CMake did not identify a supported C++ runtime")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        f"prefix={prefix}\nlibdir={libdir}\nincludedir=${{prefix}}/include\n\n"
        "Name: libdatachannel\nDescription: Pinned NetSpeed data-channel-only static build\n"
        f"Version: {version.group(1)}\n"
        # Only static archives are installed, so ordinary pkg-config consumers
        # also need the complete link closure, not just --static consumers.
        f"Libs: -L${{libdir}} -ldatachannel -ljuice -lusrsctp -lssl -lcrypto -l{runtime} -pthread -lm\n"
        "Cflags: -I${includedir} -DRTC_STATIC=1 -DRTC_ENABLE_MEDIA=0 -DRTC_ENABLE_WEBSOCKET=0\n"
    )
    return output


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: write_libdatachannel_pc.py PREFIX BUILD_DIRECTORY")
    try:
        print(write_metadata(Path(sys.argv[1]), Path(sys.argv[2])))
    except (OSError, ValueError) as error:
        raise SystemExit(str(error)) from error
