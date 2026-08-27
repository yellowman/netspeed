# Portable GNU make / BSD make Makefile.
SHELL=/bin/sh
CC?=cc
CPPFLAGS?=
CFLAGS?=-O2
WARNFLAGS?=-Wall -Wextra -Wpedantic -Werror
CSTD?=-std=c11
LDFLAGS?=
LDLIBS?=
PKG_CONFIG?=pkg-config
TARGET?=netspeed
WEBRTC?=auto
WEBRTC_STATIC?=no
NETSPEED_DEBUG?=no
VERSION?=dev
COMMIT?=unknown
SOURCE_DATE?=unknown
PREFIX?=/usr/local
DESTDIR?=
BINDIR?=${PREFIX}/bin

# OpenBSD sys.mk treats DEBUG as a compiler/linker flag variable. A value such
# as "no" is therefore passed literally to cc. Keep it empty and use the
# project-specific NETSPEED_DEBUG switch instead.
DEBUG=

all: build

build:
	env CC="${CC}" CPPFLAGS="${CPPFLAGS}" CFLAGS="${CFLAGS}" WARNFLAGS="${WARNFLAGS}" CSTD="${CSTD}" LDFLAGS="${LDFLAGS}" LDLIBS="${LDLIBS}" PKG_CONFIG="${PKG_CONFIG}" TARGET="${TARGET}" WEBRTC="${WEBRTC}" WEBRTC_STATIC="${WEBRTC_STATIC}" NETSPEED_DEBUG="${NETSPEED_DEBUG}" VERSION="${VERSION}" COMMIT="${COMMIT}" SOURCE_DATE="${SOURCE_DATE}" /bin/sh scripts/build.sh

test: build
	/bin/sh scripts/test.sh "${TARGET}"

install: build
	install -d ${DESTDIR}${BINDIR}
	install -m 0755 ${TARGET} ${DESTDIR}${BINDIR}/netspeed-c

clean:
	rm -f ${TARGET} *.o src/*.o tests/*.o

help:
	@echo "Targets: build test install clean"
	@echo "Options: NETSPEED_DEBUG=yes WEBRTC=auto|yes|no WEBRTC_STATIC=yes|no"

.PHONY: all build test install clean help
