# Portable GNU make / BSD make build entry point.
SHELL=/bin/sh
GO?=go
MAKE?=make
PREFIX?=/usr/local
DESTDIR?=
BINDIR?=${PREFIX}/bin
BUILD_DIR?=build
NETSPEED_DEBUG?=no
WEBRTC?=auto
WEBRTC_STATIC?=no
VERSION?=dev
COMMIT?=unknown
SOURCE_DATE?=unknown

# DEBUG is reserved by BSD sys.mk and is included in its default CFLAGS and
# LDFLAGS. Keep it empty; use NETSPEED_DEBUG for this project.
DEBUG=

all: go c-client

go:
	mkdir -p ${BUILD_DIR}
	${GO} build -trimpath -o ${BUILD_DIR}/netspeed ./cmd/netspeed
	${GO} build -trimpath -o ${BUILD_DIR}/netspeedd ./cmd/netspeedd

c-client:
	mkdir -p ${BUILD_DIR}
	cd netspeed.c && ${MAKE} TARGET=../${BUILD_DIR}/netspeed-c NETSPEED_DEBUG=${NETSPEED_DEBUG} WEBRTC=${WEBRTC} WEBRTC_STATIC=${WEBRTC_STATIC} VERSION=${VERSION} COMMIT=${COMMIT} SOURCE_DATE=${SOURCE_DATE} build

test: test-go test-c test-web

test-go:
	${GO} test ./...

test-race:
	${GO} test -race ./...

test-c:
	cd netspeed.c && ${MAKE} NETSPEED_DEBUG=${NETSPEED_DEBUG} WEBRTC=${WEBRTC} WEBRTC_STATIC=${WEBRTC_STATIC} test

test-web:
	@set -e; found=no; for f in web/js/*_test.js web/js/*.test.js tests/browser/*.js; do if test -f "$$f"; then found=yes; node "$$f"; fi; done; if test "$$found" = no; then echo "no browser unit tests found"; fi

install: all
	install -d ${DESTDIR}${BINDIR}
	install -m 0755 ${BUILD_DIR}/netspeed ${DESTDIR}${BINDIR}/netspeed
	install -m 0755 ${BUILD_DIR}/netspeedd ${DESTDIR}${BINDIR}/netspeedd
	install -m 0755 ${BUILD_DIR}/netspeed-c ${DESTDIR}${BINDIR}/netspeed-c

clean:
	rm -rf ${BUILD_DIR}
	cd netspeed.c && ${MAKE} clean

help:
	@echo "Targets: all go c-client test test-go test-race test-c test-web install clean"
	@echo "Options: NETSPEED_DEBUG=yes WEBRTC=auto|yes|no WEBRTC_STATIC=yes|no"

.PHONY: all go c-client test test-go test-race test-c test-web install clean help
