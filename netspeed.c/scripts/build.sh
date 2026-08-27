#!/bin/sh
set -eu

: "${CC:=cc}" "${CPPFLAGS:=}" "${CFLAGS:=-O2}" "${WARNFLAGS:=-Wall -Wextra -Wpedantic -Werror}"
: "${CSTD:=-std=c11}" "${LDFLAGS:=}" "${LDLIBS:=}" "${PKG_CONFIG:=pkg-config}"
: "${TARGET:=netspeed}" "${WEBRTC:=auto}" "${WEBRTC_STATIC:=no}" "${NETSPEED_DEBUG:=no}"
: "${VERSION:=dev}" "${COMMIT:=unknown}" "${SOURCE_DATE:=unknown}"

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

pc() { ${PKG_CONFIG} "$@"; }
CURL_CFLAGS=$(pc --cflags libcurl 2>/dev/null || pc --cflags curl 2>/dev/null || true)
CURL_LIBS=$(pc --libs libcurl 2>/dev/null || pc --libs curl 2>/dev/null || echo -lcurl)
RTC_PKG=
for p in libdatachannel datachannel; do if pc --exists "$p" 2>/dev/null; then RTC_PKG=$p; break; fi; done
HAVE_RTC=no
if test "${WEBRTC}" != no && test -n "${RTC_PKG}"; then HAVE_RTC=yes; fi
if test "${WEBRTC}" = yes && test "${HAVE_RTC}" != yes; then echo "error: WEBRTC=yes but libdatachannel was not found by pkg-config" >&2; exit 2; fi
RTC_CFLAGS= RTC_LIBS= RTC_DEFS=
if test "${HAVE_RTC}" = yes; then
  RTC_CFLAGS=$(pc --cflags "${RTC_PKG}")
  if test "${WEBRTC_STATIC}" = yes; then RTC_LIBS=$(pc --static --libs "${RTC_PKG}"); RTC_DEFS="-DRTC_STATIC=1"; else RTC_LIBS=$(pc --libs "${RTC_PKG}"); fi
  RTC_DEFS="${RTC_DEFS} -DNS_HAVE_LIBDATACHANNEL=1 -DHAVE_LIBDATACHANNEL=1 -DNETSPEED_HAVE_WEBRTC=1"
fi
OPTFLAGS=
if test "${NETSPEED_DEBUG}" = yes; then OPTFLAGS="-O0 -g"; fi
SOURCES=$(find src -type f -name '*.c' -print | LC_ALL=C sort)
DEFS="-D_POSIX_C_SOURCE=200809L -Iinclude -DNETSPEED_VERSION=\"${VERSION}\" -DNETSPEED_COMMIT=\"${COMMIT}\" -DNETSPEED_BUILD_DATE=\"${SOURCE_DATE}\" ${RTC_DEFS}"
set -x
${CC} ${DEFS} ${CPPFLAGS} ${CURL_CFLAGS} ${RTC_CFLAGS} ${CSTD} ${CFLAGS} ${OPTFLAGS} ${WARNFLAGS} ${SOURCES} ${LDFLAGS} ${CURL_LIBS} ${RTC_LIBS} ${LDLIBS} -lpthread -lm -o "${TARGET}"
set +x
echo "built ${TARGET} (WebRTC packet test: ${HAVE_RTC})"
