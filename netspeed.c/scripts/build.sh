#!/bin/sh
set -eu

: "${CC:=cc}"
: "${CPPFLAGS:=}"
: "${CFLAGS:=-O2}"
: "${WARNFLAGS:=-Wall -Wextra -Wpedantic -Werror}"
: "${CSTD:=-std=c11}"
: "${LDFLAGS:=}"
: "${LDLIBS:=}"
: "${PKG_CONFIG:=pkg-config}"
: "${TARGET:=netspeed}"
: "${WEBRTC:=auto}"
: "${WEBRTC_STATIC:=no}"
: "${DEBUG:=no}"
: "${VERSION:=dev}"
: "${COMMIT:=unknown}"
: "${SOURCE_DATE:=unknown}"
: "${NETSPEED_TEST_SOURCE:=}"

case "$WEBRTC" in
    auto|yes|no) ;;
    *) echo "WEBRTC must be auto, yes, or no" >&2; exit 2 ;;
esac
case "$WEBRTC_STATIC" in
    yes|no) ;;
    *) echo "WEBRTC_STATIC must be yes or no" >&2; exit 2 ;;
esac

platform_cppflags="-D_POSIX_C_SOURCE=200809L"
case "$(uname -s)" in
    Linux) platform_cppflags="$platform_cppflags -D_DEFAULT_SOURCE" ;;
    FreeBSD|OpenBSD) platform_cppflags="$platform_cppflags -I/usr/local/include"; LDFLAGS="$LDFLAGS -L/usr/local/lib" ;;
    NetBSD) platform_cppflags="$platform_cppflags -I/usr/pkg/include"; LDFLAGS="$LDFLAGS -L/usr/pkg/lib" ;;
    Darwin) platform_cppflags="$platform_cppflags -I/usr/local/include -I/opt/homebrew/include"; LDFLAGS="$LDFLAGS -L/usr/local/lib -L/opt/homebrew/lib" ;;
esac

if command -v "$PKG_CONFIG" >/dev/null 2>&1 && "$PKG_CONFIG" --exists libcurl; then
    curl_cflags=$($PKG_CONFIG --cflags libcurl)
    curl_libs=$($PKG_CONFIG --libs libcurl)
elif command -v curl-config >/dev/null 2>&1; then
    curl_cflags=$(curl-config --cflags)
    curl_libs=$(curl-config --libs)
else
    echo "libcurl development files are required (pkg-config libcurl or curl-config)" >&2
    exit 1
fi

webrtc_cflags=""
webrtc_libs=""
have_webrtc=no
if [ "$WEBRTC" != no ]; then
    if [ -n "${WEBRTC_CFLAGS:-}" ] || [ -n "${WEBRTC_LIBS:-}" ]; then
        if [ -z "${WEBRTC_LIBS:-}" ]; then
            echo "WEBRTC_LIBS is required when overriding libdatachannel discovery" >&2
            exit 1
        fi
        webrtc_cflags=${WEBRTC_CFLAGS:-}
        webrtc_libs=${WEBRTC_LIBS:-}
        have_webrtc=yes
    elif command -v "$PKG_CONFIG" >/dev/null 2>&1 && "$PKG_CONFIG" --exists libdatachannel; then
        webrtc_cflags=$($PKG_CONFIG --cflags libdatachannel)
        if [ "$WEBRTC_STATIC" = yes ]; then
            webrtc_libs=$($PKG_CONFIG --static --libs libdatachannel)
        else
            webrtc_libs=$($PKG_CONFIG --libs libdatachannel)
        fi
        have_webrtc=yes
    else
        for prefix in /usr/local /usr/pkg /opt/homebrew; do
            if [ -f "$prefix/include/rtc/rtc.h" ]; then
                webrtc_cflags="-I$prefix/include"
                webrtc_libs="-L$prefix/lib -ldatachannel"
                have_webrtc=yes
                break
            fi
        done
    fi
fi
if [ "$WEBRTC" = yes ] && [ "$have_webrtc" != yes ]; then
    echo "WEBRTC=yes requires libdatachannel (or WEBRTC_CFLAGS and WEBRTC_LIBS)" >&2
    exit 1
fi
if [ "$have_webrtc" = yes ]; then
    platform_cppflags="$platform_cppflags -DNETSPEED_HAVE_LIBDATACHANNEL=1"
fi

if [ "$DEBUG" = yes ]; then
    CFLAGS="$CFLAGS -O0 -g -DDEBUG"
fi

common_sources="src/http.c src/json.c src/output.c src/packet_loss.c src/speedtest.c src/stats.c src/timing.c"
if [ -n "$NETSPEED_TEST_SOURCE" ]; then
    sources="$NETSPEED_TEST_SOURCE $common_sources"
else
    sources="src/main.c $common_sources"
fi

mkdir -p "$(dirname "$TARGET")"
set -x
$CC $CPPFLAGS $platform_cppflags $curl_cflags $webrtc_cflags \
    $CSTD $CFLAGS $WARNFLAGS -Iinclude \
    "-DNETSPEED_VERSION=\"$VERSION\"" \
    "-DNETSPEED_COMMIT=\"$COMMIT\"" \
    "-DNETSPEED_BUILD_DATE=\"$SOURCE_DATE\"" \
    $sources $LDFLAGS $curl_libs $webrtc_libs $LDLIBS -lpthread -lm -o "$TARGET"
set +x
printf 'built %s (WebRTC packet test: %s)\n' "$TARGET" "$have_webrtc"
