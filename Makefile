# Portable GNU make / BSD make build and qualification entry point.
SHELL=/bin/sh
GO?=go
NODE?=node
PYTHON?=python3
MAKE?=make
PREFIX?=/usr/local
DESTDIR?=
BINDIR?=${PREFIX}/bin
BIN_DIR?=bin
NETSPEED_DEBUG?=no
WEBRTC?=auto
WEBRTC_STATIC?=no
VERSION?=dev
COMMIT?=unknown
SOURCE_DATE?=unknown
GO_BUILD_FLAGS?=-mod=readonly -trimpath
GO_LDFLAGS?=-X github.com/yellowman/netspeed/internal/buildinfo.Version=${VERSION} -X github.com/yellowman/netspeed/internal/buildinfo.Commit=${COMMIT} -X github.com/yellowman/netspeed/internal/buildinfo.Date=${SOURCE_DATE}

# OpenBSD sys.mk expands DEBUG into CFLAGS and LDFLAGS. Keep the system
# variable empty; NETSPEED_DEBUG is the project-specific yes/no switch.
DEBUG=

all: build

build: go c-client

go:
	mkdir -p "${BIN_DIR}"
	${GO} build ${GO_BUILD_FLAGS} -ldflags "${GO_LDFLAGS}" -o "${BIN_DIR}/netspeed" ./cmd/netspeed
	${GO} build ${GO_BUILD_FLAGS} -ldflags "${GO_LDFLAGS}" -o "${BIN_DIR}/netspeedd" ./cmd/netspeedd

c-client:
	mkdir -p "${BIN_DIR}"
	cd netspeed.c && ${MAKE} C_TARGET=../${BIN_DIR}/netspeed-c NETSPEED_DEBUG=${NETSPEED_DEBUG} WEBRTC=${WEBRTC} WEBRTC_STATIC=${WEBRTC_STATIC} VERSION=${VERSION} COMMIT=${COMMIT} SOURCE_DATE=${SOURCE_DATE} build

install: build
	install -d "${DESTDIR}${BINDIR}"
	install -m 0755 "${BIN_DIR}/netspeed" "${DESTDIR}${BINDIR}/netspeed"
	install -m 0755 "${BIN_DIR}/netspeedd" "${DESTDIR}${BINDIR}/netspeedd"
	install -m 0755 "${BIN_DIR}/netspeed-c" "${DESTDIR}${BINDIR}/netspeed-c"

fmt:
	@files="$$(find cmd internal tests -name '*.go' -type f -print)"; if test -n "$$files"; then gofmt -w $$files; fi

fmt-check:
	@files="$$(gofmt -l $$(find cmd internal tests -name '*.go' -type f -print))"; if test -n "$$files"; then printf '%s\n' "$$files"; exit 1; fi

mod-tidy-check:
	${GO} mod tidy
	git diff --exit-code -- go.mod go.sum

test: test-go c-test web-test

test-go:
	${GO} test ./...

test-race:
	${GO} test -race ./...

race: test-race

vet:
	${GO} vet ./...

staticcheck:
	staticcheck ./...

vuln:
	govulncheck ./...

web-test:
	@set -e; found=no; for test_file in tests/web/*.test.js web/js/*_test.js web/js/*.test.js; do if test -f "$$test_file"; then found=yes; echo "${NODE} $$test_file"; ${NODE} "$$test_file"; fi; done; if test "$$found" = no; then echo "no browser unit tests found"; fi

test-web: web-test

release-tools:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} -m unittest discover -s tests/release -v

c-test:
	cd netspeed.c && ${MAKE} NETSPEED_DEBUG=${NETSPEED_DEBUG} WEBRTC=${WEBRTC} WEBRTC_STATIC=${WEBRTC_STATIC} test

c-check:
	cd netspeed.c && ${MAKE} NETSPEED_DEBUG=${NETSPEED_DEBUG} check-compilers

c-sanitize:
	cd netspeed.c && ${MAKE} NETSPEED_DEBUG=${NETSPEED_DEBUG} sanitize

c-protocol-check:
	cd netspeed.c && ${MAKE} NETSPEED_DEBUG=${NETSPEED_DEBUG} WEBRTC=yes WEBRTC_STATIC=${WEBRTC_STATIC} protocol-check

test-parity: go c-client
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} tests/client_parity.py "${BIN_DIR}/netspeed" "${BIN_DIR}/netspeed-c"

hygiene:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_source_hygiene.py

docs-check:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_markdown_links.py

make-portability-check:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_make_portability.py

workflow-contract-check:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_workflow_contract.py

integration:
	${GO} test -tags=integration -count=1 -timeout=3m ./tests/integration

integration-turn:
	NETSPEED_E2E_TURN=1 ${GO} test -tags=integration -run TestEmbeddedTURNPacketLoss -count=1 -timeout=2m ./tests/integration

integration-c-turn:
	@test -x "$${NETSPEED_C_CLIENT:-$$(pwd)/${BIN_DIR}/netspeed-c}" || { echo "WEBRTC=yes netspeed-c is required" >&2; exit 1; }
	NETSPEED_E2E_TURN=1 NETSPEED_C_CLIENT="$${NETSPEED_C_CLIENT:-$$(pwd)/${BIN_DIR}/netspeed-c}" ${GO} test -tags=integration -run TestCClientEmbeddedTURNPacketLoss -count=1 -timeout=2m ./tests/integration

browser-smoke:
	npx playwright test --config tests/browser/playwright.config.js

release-reproducibility:
	/bin/sh scripts/check_release_reproducibility.sh

# This is the complete non-platform-specific release gate. It intentionally
# requires the analyzers, Chromium, libdatachannel, and the real TURN fixtures;
# use `make test` for the smaller everyday unit suite.
ci:
	${MAKE} fmt-check
	${MAKE} hygiene
	${MAKE} docs-check
	${MAKE} make-portability-check
	${MAKE} workflow-contract-check
	${MAKE} release-tools
	${MAKE} mod-tidy-check
	${MAKE} test-go
	${MAKE} test-race
	${MAKE} vet
	${MAKE} staticcheck
	${MAKE} vuln
	${MAKE} web-test
	${MAKE} c-check
	${MAKE} c-sanitize
	${MAKE} test-parity WEBRTC=no
	${MAKE} integration
	${MAKE} integration-turn
	${MAKE} browser-smoke
	${MAKE} c-protocol-check WEBRTC_STATIC=yes
	${MAKE} c-client WEBRTC=yes WEBRTC_STATIC=yes
	${MAKE} integration-c-turn
	${MAKE} release-reproducibility

release:
	${PYTHON} scripts/release.py

clean:
	rm -rf "${BIN_DIR}" dist test-results playwright-report
	cd netspeed.c && ${MAKE} clean

help:
	@echo "Build: all build go c-client install clean"
	@echo "Tests: test test-go test-race race vet web-test c-test c-check c-sanitize c-protocol-check test-parity integration integration-turn integration-c-turn browser-smoke"
	@echo "Release checks: fmt-check mod-tidy-check staticcheck vuln hygiene docs-check make-portability-check workflow-contract-check release-tools release-reproducibility ci release"
	@echo "Options: BIN_DIR=bin NETSPEED_DEBUG=yes WEBRTC=auto|yes|no WEBRTC_STATIC=yes|no"

.PHONY: all build go c-client install fmt fmt-check mod-tidy-check test test-go \
	test-race race vet staticcheck vuln web-test test-web release-tools c-test \
	c-check c-sanitize c-protocol-check test-parity hygiene docs-check \
	make-portability-check workflow-contract-check integration integration-turn \
	integration-c-turn browser-smoke release-reproducibility ci release clean help
