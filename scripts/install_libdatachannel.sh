#!/bin/sh
# Build the pinned libdatachannel dependency used by the supported C client.
set -eu

prefix=${1:?usage: install_libdatachannel.sh PREFIX [WORKDIR]}
work=${2:-"${TMPDIR:-/tmp}/netspeed-libdatachannel"}
commit=443f6934d9007eb7076ab7825ba330f355fcbead
repository=https://github.com/paullouisageneau/libdatachannel.git

rm -rf "$work"
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
    echo "libdatachannel pkg-config metadata was not installed under $prefix" >&2
    exit 1
fi
printf 'installed libdatachannel %s under %s\n' "$commit" "$prefix"
