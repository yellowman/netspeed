# Portable project build. This file intentionally uses the common GNU make / BSD pmake subset.

GO?=go
NODE?=node
PYTHON?=python3
PREFIX?=/usr/local
DESTDIR?=
BINDIR?=${PREFIX}/bin
BUILD_DIR?=bin
VERSION?=dev
COMMIT?=unknown
SOURCE_DATE?=unknown
GO_BUILD_FLAGS?=-trimpath
GO_LDFLAGS?=-X github.com/yellowman/netspeed/internal/buildinfo.Version=${VERSION} -X github.com/yellowman/netspeed/internal/buildinfo.Commit=${COMMIT} -X github.com/yellowman/netspeed/internal/buildinfo.Date=${SOURCE_DATE}
WEBRTC?=auto
WEBRTC_STATIC?=no

.PHONY: all build go go-build c-client c-build install fmt fmt-check mod-tidy-check \
	go-test test test-parity go-race race go-vet vet staticcheck vuln web-test \
	release-tools c-test c-check c-sanitize c-protocol-check hygiene docs-check \
	make-portability-check integration integration-turn integration-c-turn \
	browser-smoke ci release clean

all: build

build: go-build c-build

go: go-build

c-client: c-build

go-build:
	mkdir -p "${BUILD_DIR}"
	${GO} build ${GO_BUILD_FLAGS} -ldflags "${GO_LDFLAGS}" -o "${BUILD_DIR}/netspeed" ./cmd/netspeed
	${GO} build ${GO_BUILD_FLAGS} -ldflags "${GO_LDFLAGS}" -o "${BUILD_DIR}/netspeedd" ./cmd/netspeedd

c-build:
	${MAKE} -C netspeed.c WEBRTC="${WEBRTC}" WEBRTC_STATIC="${WEBRTC_STATIC}" VERSION="${VERSION}" COMMIT="${COMMIT}" SOURCE_DATE="${SOURCE_DATE}" all
	mkdir -p "${BUILD_DIR}"
	cp netspeed.c/netspeed "${BUILD_DIR}/netspeed-c"

install: build
	install -d "${DESTDIR}${BINDIR}"
	install -m 755 "${BUILD_DIR}/netspeed" "${DESTDIR}${BINDIR}/netspeed"
	install -m 755 "${BUILD_DIR}/netspeedd" "${DESTDIR}${BINDIR}/netspeedd"
	install -m 755 "${BUILD_DIR}/netspeed-c" "${DESTDIR}${BINDIR}/netspeed-c"

fmt:
	@files="$$(find cmd internal tests -name '*.go' -type f -print)"; \
	if [ -n "$$files" ]; then gofmt -w $$files; fi
	${MAKE} -C netspeed.c format

fmt-check:
	@files="$$(gofmt -l $$(find cmd internal tests -name '*.go' -type f -print))"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

mod-tidy-check:
	${GO} mod tidy
	git diff --exit-code -- go.mod go.sum

go-test:
	${GO} test ./...

test: go-test c-test

go-race:
	${GO} test -race ./...

race: go-race

go-vet:
	${GO} vet ./...

vet: go-vet

staticcheck:
	staticcheck ./...

vuln:
	govulncheck ./...

web-test:
	@for test_file in tests/web/*.test.js; do \
		echo "${NODE} $$test_file"; \
		${NODE} "$$test_file" || exit 1; \
	done

release-tools:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} -m unittest discover -s tests/release -v

c-test:
	${MAKE} -C netspeed.c test

c-check:
	${MAKE} -C netspeed.c check-compilers

c-sanitize:
	${MAKE} -C netspeed.c sanitize

c-protocol-check:
	${MAKE} -C netspeed.c protocol-check

test-parity: go c-client
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} tests/client_parity.py "${BUILD_DIR}/netspeed" "${BUILD_DIR}/netspeed-c"

hygiene:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_source_hygiene.py

docs-check:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_markdown_links.py

make-portability-check:
	PYTHONDONTWRITEBYTECODE=1 ${PYTHON} scripts/check_make_portability.py

integration:
	${GO} test -tags=integration -count=1 -timeout=3m ./tests/integration

integration-turn:
	NETSPEED_E2E_TURN=1 ${GO} test -tags=integration -run TestEmbeddedTURNPacketLoss -count=1 -timeout=2m ./tests/integration

integration-c-turn:
	NETSPEED_E2E_TURN=1 NETSPEED_C_CLIENT="$${NETSPEED_C_CLIENT:-$$(pwd)/bin/netspeed-c}" ${GO} test -tags=integration -run TestCClientEmbeddedTURNPacketLoss -count=1 -timeout=2m ./tests/integration

browser-smoke:
	npx playwright test --config tests/browser/playwright.config.js

ci: fmt-check hygiene docs-check make-portability-check release-tools go-test go-vet web-test c-check test-parity integration

release:
	${PYTHON} scripts/release.py

clean:
	rm -rf "${BUILD_DIR}" dist test-results playwright-report
	${MAKE} -C netspeed.c clean
