#!/bin/sh
set -eu

: "${CC:=cc}" "${CPPFLAGS:=}" "${CFLAGS:=-O2}" "${WARNFLAGS:=-Wall -Wextra -Wpedantic -Werror}"
: "${CSTD:=-std=c11}" "${LDFLAGS:=}" "${LDLIBS:=}" "${PKG_CONFIG:=pkg-config}"
: "${TARGET:=netspeed}" "${WEBRTC:=auto}" "${WEBRTC_STATIC:=no}" "${NETSPEED_DEBUG:=no}"
: "${VERSION:=dev}" "${COMMIT:=unknown}" "${SOURCE_DATE:=unknown}" "${NETSPEED_TEST_SOURCE:=}"

case " ${CPPFLAGS} ${CFLAGS} ${LDFLAGS} ${LDLIBS} " in
  *" no "*)
    echo "error: a compiler or linker flag contains the standalone token 'no'." >&2
    echo "On BSD make, do not use DEBUG=no; use NETSPEED_DEBUG=no." >&2
    exit 2
    ;;
esac
case "${NETSPEED_DEBUG}" in yes|no) ;; *) echo "NETSPEED_DEBUG must be yes or no" >&2; exit 2;; esac
case "${WEBRTC}" in auto|yes|no) ;; *) echo "WEBRTC must be auto, yes, or no" >&2; exit 2;; esac
case "${WEBRTC_STATIC}" in yes|no) ;; *) echo "WEBRTC_STATIC must be yes or no" >&2; exit 2;; esac

platform_cppflags="-D_POSIX_C_SOURCE=200809L"
case "$(uname -s)" in
  Linux) platform_cppflags="$platform_cppflags -D_DEFAULT_SOURCE" ;;
  FreeBSD|OpenBSD) platform_cppflags="$platform_cppflags -I/usr/local/include"; LDFLAGS="$LDFLAGS -L/usr/local/lib" ;;
  NetBSD) platform_cppflags="$platform_cppflags -I/usr/pkg/include"; LDFLAGS="$LDFLAGS -L/usr/pkg/lib" ;;
  Darwin) platform_cppflags="$platform_cppflags -I/usr/local/include -I/opt/homebrew/include"; LDFLAGS="$LDFLAGS -L/usr/local/lib -L/opt/homebrew/lib" ;;
esac

pc() { ${PKG_CONFIG} "$@"; }
if command -v "${PKG_CONFIG}" >/dev/null 2>&1 && pc --exists libcurl 2>/dev/null; then
  curl_cflags=$(pc --cflags libcurl)
  curl_libs=$(pc --libs libcurl)
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
if test "${WEBRTC}" != no; then
  if test -n "${WEBRTC_CFLAGS:-}" || test -n "${WEBRTC_LIBS:-}"; then
    test -n "${WEBRTC_LIBS:-}" || { echo "WEBRTC_LIBS is required with an explicit WebRTC override" >&2; exit 1; }
    webrtc_cflags=${WEBRTC_CFLAGS:-}
    webrtc_libs=${WEBRTC_LIBS:-}
    have_webrtc=yes
  else
    rtc_pkg=""
    for candidate in libdatachannel datachannel; do
      if command -v "${PKG_CONFIG}" >/dev/null 2>&1 && pc --exists "$candidate" 2>/dev/null; then rtc_pkg=$candidate; break; fi
    done
    if test -n "$rtc_pkg"; then
      webrtc_cflags=$(pc --cflags "$rtc_pkg")
      if test "${WEBRTC_STATIC}" = yes; then webrtc_libs=$(pc --static --libs "$rtc_pkg"); else webrtc_libs=$(pc --libs "$rtc_pkg"); fi
      have_webrtc=yes
    else
      for prefix in /usr/local /usr/pkg /opt/homebrew; do
        if test -f "$prefix/include/rtc/rtc.h"; then
          webrtc_cflags="-I$prefix/include"
          webrtc_libs="-L$prefix/lib -ldatachannel"
          have_webrtc=yes
          break
        fi
      done
    fi
  fi
fi
if test "${WEBRTC}" = yes && test "${have_webrtc}" != yes; then
  echo "WEBRTC=yes requires libdatachannel (or WEBRTC_CFLAGS and WEBRTC_LIBS)" >&2
  exit 1
fi
rtc_defs=""
if test "${have_webrtc}" = yes; then
  rtc_defs="-DNS_HAVE_LIBDATACHANNEL=1 -DHAVE_LIBDATACHANNEL=1 -DNETSPEED_HAVE_LIBDATACHANNEL=1 -DNETSPEED_HAVE_WEBRTC=1"
  if test "${WEBRTC_STATIC}" = yes; then rtc_defs="$rtc_defs -DRTC_STATIC=1"; fi
fi

optflags=""
if test "${NETSPEED_DEBUG}" = yes; then optflags="-O0 -g -DDEBUG"; fi
common_sources="src/http.c src/json.c src/output.c src/packet_loss.c src/speedtest.c src/stats.c src/timing.c"
if test -n "${NETSPEED_TEST_SOURCE}"; then
  sources="${NETSPEED_TEST_SOURCE} ${common_sources}"
else
  sources="src/main.c src/cloudflare.c ${common_sources}"
fi
mkdir -p "$(dirname "${TARGET}")"
set -x
${CC} ${platform_cppflags} ${rtc_defs} ${CPPFLAGS} ${curl_cflags} ${webrtc_cflags} ${CSTD} ${CFLAGS} ${optflags} ${WARNFLAGS} -Iinclude \
  "-DNETSPEED_VERSION=\"${VERSION}\"" "-DNETSPEED_COMMIT=\"${COMMIT}\"" "-DNETSPEED_BUILD_DATE=\"${SOURCE_DATE}\"" \
  ${sources} ${LDFLAGS} ${curl_libs} ${webrtc_libs} ${LDLIBS} -lpthread -lm -o "${TARGET}"
set +x
printf 'built %s (WebRTC packet test: %s)\n' "${TARGET}" "${have_webrtc}"
