GO ?= go
NODE ?= node
PYTHON ?= python3

.PHONY: all fmt fmt-check mod-tidy-check test race vet staticcheck vuln \
	web-test release-tools c-check hygiene docs-check integration integration-turn \
	browser-smoke ci release clean

all: test

fmt:
	@files="$$(find cmd internal tests -name '*.go' -type f -print)"; \
	if [ -n "$$files" ]; then gofmt -w $$files; fi

fmt-check:
	@files="$$(gofmt -l $$(find cmd internal tests -name '*.go' -type f -print))"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

mod-tidy-check:
	$(GO) mod tidy
	git diff --exit-code -- go.mod go.sum

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

staticcheck:
	staticcheck ./...

vuln:
	govulncheck ./...

web-test:
	@for test_file in tests/web/*.test.js; do \
		echo "$(NODE) $$test_file"; \
		$(NODE) "$$test_file" || exit 1; \
	done

release-tools:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m unittest discover -s tests/release -v

c-check:
	$(MAKE) -C netspeed.c check-compilers

hygiene:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) scripts/check_source_hygiene.py

docs-check:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) scripts/check_markdown_links.py

integration:
	$(GO) test -tags=integration -count=1 -timeout=3m ./tests/integration

integration-turn:
	NETSPEED_E2E_TURN=1 $(GO) test -tags=integration -run TestEmbeddedTURNPacketLoss -count=1 -timeout=2m ./tests/integration

browser-smoke:
	npx playwright test --config tests/browser/playwright.config.js

ci: fmt-check hygiene docs-check release-tools test vet web-test c-check integration

release:
	$(PYTHON) scripts/release.py

clean:
	rm -rf dist test-results playwright-report
	$(MAKE) -C netspeed.c clean
