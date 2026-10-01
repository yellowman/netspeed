#!/bin/sh
# Build the pinned libdatachannel dependency used by the supported C client.
set -eu

prefix=${1:?usage: install_libdatachannel.sh PREFIX [WORKDIR]}
work_root=${2:-"${TMPDIR:-/tmp}/netspeed-libdatachannel"}
script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
commit=443f6934d9007eb7076ab7825ba330f355fcbead
repository=https://github.com/paullouisageneau/libdatachannel.git

mkdir -p "$work_root"
work=$(mktemp -d "$work_root/netspeed-libdatachannel.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work/source" "$work/build" "$prefix"
git -C "$work/source" init -q
git -C "$work/source" remote add origin "$repository"
git -C "$work/source" fetch --depth 1 origin "$commit"
git -C "$work/source" checkout -q --detach FETCH_HEAD
git -C "$work/source" submodule update --init --recursive --depth 1

generator=""
if command -v ninja >/dev/null 2>&1; then
    generator="-G Ninja"
fi
# shellcheck disable=SC2086
cmake -S "$work/source" -B "$work/build" $generator \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="$prefix" \
    -DCMAKE_INSTALL_LIBDIR=lib \
    -DBUILD_SHARED_LIBS=OFF \
    -DBUILD_SHARED_DEPS_LIBS=OFF \
    -DNO_MEDIA=ON \
    -DNO_WEBSOCKET=ON \
    -DNO_EXAMPLES=ON \
    -DNO_TESTS=ON \
    -DWARNINGS_AS_ERRORS=OFF
cmake --build "$work/build" --parallel
cmake --install "$work/build"

pc_path=$(find "$prefix" -type f -name 'libdatachannel.pc' -print -quit)
if [ -z "$pc_path" ]; then
    # This pinned upstream installs CMake exports, not a .pc file. Our C build
    # uses pkg-config; include all static dependencies and the actual C++ runtime.
    python3 "$script_directory/write_libdatachannel_pc.py" "$prefix" "$work/build"
fi
printf 'installed libdatachannel %s under %s\n' "$commit" "$prefix"
