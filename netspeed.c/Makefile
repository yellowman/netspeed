# Portable native C client build. This file intentionally stays within the
# common GNU make / BSD pmake subset; platform and dependency detection lives
# in POSIX shell scripts rather than make-specific conditionals.

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
DEBUG?=no
VERSION?=dev
COMMIT?=unknown
SOURCE_DATE?=unknown
PREFIX?=/usr/local
DESTDIR?=
BINDIR?=${PREFIX}/bin

.PHONY: all build test unit-test integration-test clean install format check-build \
	check-compilers sanitize protocol-check

all: build

build:
	env CC="${CC}" CPPFLAGS="${CPPFLAGS}" CFLAGS="${CFLAGS}" \
		WARNFLAGS="${WARNFLAGS}" CSTD="${CSTD}" LDFLAGS="${LDFLAGS}" \
		LDLIBS="${LDLIBS}" PKG_CONFIG="${PKG_CONFIG}" TARGET="${C_TARGET}" \
		WEBRTC="${WEBRTC}" WEBRTC_STATIC="${WEBRTC_STATIC}" DEBUG="${DEBUG}" VERSION="${VERSION}" \
		COMMIT="${COMMIT}" SOURCE_DATE="${SOURCE_DATE}" \
		${SH} scripts/build.sh

unit-test: build
	env CC="${CC}" CPPFLAGS="${CPPFLAGS}" CFLAGS="${CFLAGS}" \
		WARNFLAGS="${WARNFLAGS}" CSTD="${CSTD}" LDFLAGS="${LDFLAGS}" \
		LDLIBS="${LDLIBS}" PKG_CONFIG="${PKG_CONFIG}" WEBRTC="${WEBRTC}" WEBRTC_STATIC="${WEBRTC_STATIC}" \
		DEBUG="${DEBUG}" VERSION="${VERSION}" COMMIT="${COMMIT}" \
		SOURCE_DATE="${SOURCE_DATE}" TARGET="tests/test_protocol" \
		NETSPEED_TEST_SOURCE="tests/test_protocol.c" ${SH} scripts/build.sh
	./tests/test_protocol
	rm -f tests/test_protocol

integration-test: build
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} tests/test_client.py ./"${C_TARGET}"

test: unit-test integration-test

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
	env ASAN_OPTIONS="detect_leaks=1:halt_on_error=1" UBSAN_OPTIONS="halt_on_error=1" \
		${MAKE} CC=clang WEBRTC=no \
		CFLAGS="-O1 -g -fsanitize=address,undefined -fno-omit-frame-pointer" \
		LDFLAGS="-fsanitize=address,undefined" test
	${MAKE} clean

# Require libdatachannel and compile/run the exact-frame unit suite. The
# process-level WebRTC/TURN fixture runs in repository CI with a real daemon.
protocol-check:
	${MAKE} clean
	${MAKE} WEBRTC=yes unit-test
	${MAKE} clean

install: build
	install -d "${DESTDIR}${BINDIR}"
	install -m 755 "${C_TARGET}" "${DESTDIR}${BINDIR}/netspeed-c"

format:
	@if command -v clang-format >/dev/null 2>&1; then \
		find src include tests -type f \( -name '*.c' -o -name '*.h' \) -exec clang-format -i {} +; \
	else \
		echo 'clang-format is not installed' >&2; exit 1; \
	fi

clean:
	rm -rf obj "${C_TARGET}" tests/test_protocol
