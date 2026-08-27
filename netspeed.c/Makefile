# Portable native C client build. Platform and dependency discovery lives in
# POSIX shell so this canonical file works with GNU make and BSD pmake.
SHELL=/bin/sh
CC?=cc
CPPFLAGS?=
CFLAGS?=-O2
WARNFLAGS?=-Wall -Wextra -Wpedantic -Werror
CSTD?=-std=c11
LDFLAGS?=
LDLIBS?=
PKG_CONFIG?=pkg-config
PYTHON?=python3
SH?=/bin/sh
C_TARGET?=netspeed
WEBRTC?=auto
WEBRTC_STATIC?=no
NETSPEED_DEBUG?=no
VERSION?=dev
COMMIT?=unknown
SOURCE_DATE?=unknown
PREFIX?=/usr/local
DESTDIR?=
BINDIR?=${PREFIX}/bin

# OpenBSD sys.mk reserves DEBUG and injects it into compiler and linker flags.
DEBUG=

all: build

build:
	env CC="${CC}" CPPFLAGS="${CPPFLAGS}" CFLAGS="${CFLAGS}" WARNFLAGS="${WARNFLAGS}" CSTD="${CSTD}" LDFLAGS="${LDFLAGS}" LDLIBS="${LDLIBS}" PKG_CONFIG="${PKG_CONFIG}" TARGET="${C_TARGET}" WEBRTC="${WEBRTC}" WEBRTC_STATIC="${WEBRTC_STATIC}" NETSPEED_DEBUG="${NETSPEED_DEBUG}" VERSION="${VERSION}" COMMIT="${COMMIT}" SOURCE_DATE="${SOURCE_DATE}" ${SH} scripts/build.sh

unit-test:
	env CC="${CC}" CPPFLAGS="${CPPFLAGS}" CFLAGS="${CFLAGS}" WARNFLAGS="${WARNFLAGS}" CSTD="${CSTD}" LDFLAGS="${LDFLAGS}" LDLIBS="${LDLIBS}" PKG_CONFIG="${PKG_CONFIG}" TARGET="tests/test_protocol" WEBRTC="${WEBRTC}" WEBRTC_STATIC="${WEBRTC_STATIC}" NETSPEED_DEBUG="${NETSPEED_DEBUG}" VERSION="${VERSION}" COMMIT="${COMMIT}" SOURCE_DATE="${SOURCE_DATE}" NETSPEED_TEST_SOURCE="tests/test_protocol.c" ${SH} scripts/build.sh
	./tests/test_protocol
	rm -f tests/test_protocol

integration-test: build
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} tests/test_client.py ./"${C_TARGET}"
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} tests/cloudflare_fixture.py ./"${C_TARGET}"

packet-report-test:
	env CC="${CC}" CPPFLAGS="${CPPFLAGS} -Itests/fake_rtc -DNETSPEED_HAVE_LIBDATACHANNEL=1 -DPACKET_LOSS_COUNT=4 -DPACKET_LOSS_INTERVAL_MS=0 -DPACKET_LOSS_DRAIN_MS=0" CFLAGS="${CFLAGS}" WARNFLAGS="${WARNFLAGS}" CSTD="${CSTD}" LDFLAGS="${LDFLAGS}" LDLIBS="${LDLIBS}" PKG_CONFIG="${PKG_CONFIG}" TARGET="tests/test_packet_report_process" WEBRTC=no WEBRTC_STATIC=no NETSPEED_DEBUG="${NETSPEED_DEBUG}" VERSION="${VERSION}" COMMIT="${COMMIT}" SOURCE_DATE="${SOURCE_DATE}" NETSPEED_TEST_SOURCE="tests/test_packet_report_process.c tests/fake_rtc/fake_rtc.c" ${SH} scripts/build.sh
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} tests/test_packet_report_rejection.py ./tests/test_packet_report_process
	rm -f tests/test_packet_report_process

test: unit-test integration-test packet-report-test

check-build: build
	./"${C_TARGET}" --version

check-compilers:
	${MAKE} clean
	${MAKE} CC=gcc WEBRTC=no test
	${MAKE} clean
	${MAKE} CC=clang WEBRTC=no test
	${MAKE} clean

sanitize:
	${MAKE} clean
	env ASAN_OPTIONS="detect_leaks=1:halt_on_error=1" UBSAN_OPTIONS="halt_on_error=1" ${MAKE} CC=clang WEBRTC=no CFLAGS="-O1 -g -fsanitize=address,undefined -fno-omit-frame-pointer" LDFLAGS="-fsanitize=address,undefined" test
	${MAKE} clean

# Require the real libdatachannel headers and libraries for the protocol build.
protocol-check:
	${MAKE} clean
	${MAKE} WEBRTC=yes WEBRTC_STATIC=${WEBRTC_STATIC} unit-test
	${MAKE} clean

install: build
	install -d "${DESTDIR}${BINDIR}"
	install -m 0755 "${C_TARGET}" "${DESTDIR}${BINDIR}/netspeed-c"

format:
	@if command -v clang-format >/dev/null 2>&1; then find src include tests -type f \( -name '*.c' -o -name '*.h' \) -exec clang-format -i {} +; else echo 'clang-format is not installed' >&2; exit 1; fi

clean:
	rm -f "${C_TARGET}" tests/test_protocol tests/test_packet_report_process *.o src/*.o tests/*.o tests/fake_rtc/*.o

help:
	@echo "Targets: build unit-test integration-test packet-report-test test check-build check-compilers sanitize protocol-check install clean"
	@echo "Options: NETSPEED_DEBUG=yes WEBRTC=auto|yes|no WEBRTC_STATIC=yes|no"

.PHONY: all build unit-test integration-test packet-report-test test check-build \
	check-compilers sanitize protocol-check install format clean help
