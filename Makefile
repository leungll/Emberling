# Repository entry point for development and CI (ENGINEERING §8). CI calls these targets
# instead of duplicating commands, so a green pipeline and a green workstation run the
# same gate.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

# Toolchain baselines (ENGINEERING §1). The authoritative files are backend/go.mod for Go
# and .nvmrc for Node; these variables only exist to verify them.
GO_VERSION   := $(shell sed -n 's/^go \([0-9.]*\)$$/\1/p' backend/go.mod)
NODE_VERSION := $(shell cat .nvmrc)
PNPM_VERSION := 11.25.0

# GOTOOLCHAIN=auto lets a host Go older than the module baseline download and run the
# pinned toolchain instead of failing the build.
GO := GOTOOLCHAIN=auto go

BACKEND := backend
STUDIO  := studio

.PHONY: bootstrap dev test test-integration test-e2e lint check \
        check-go-version check-node-version fmt-check vet

## bootstrap: verify toolchain versions and install dependencies. Starts no service.
bootstrap: check-go-version check-node-version
	@echo "==> backend: downloading Go module dependencies"
	@cd $(BACKEND) && $(GO) mod download
	@if [ -d $(STUDIO) ]; then \
		echo "==> studio: installing dependencies"; \
		corepack enable >/dev/null 2>&1 || true; \
		cd $(STUDIO) && pnpm install --frozen-lockfile; \
	else \
		echo "studio: not present, skipped"; \
	fi

check-go-version:
	@echo "==> toolchain: Go (baseline $(GO_VERSION))"
	@resolved=$$(cd $(BACKEND) && $(GO) version | awk '{print $$3}' | sed 's/^go//'); \
	if [ "$$resolved" != "$(GO_VERSION)" ]; then \
		echo "Go toolchain mismatch: backend/go.mod requires $(GO_VERSION), 'go version' resolved to $$resolved."; \
		echo "Install Go $(GO_VERSION) or allow GOTOOLCHAIN=auto to download it."; \
		exit 1; \
	fi; \
	echo "    go$$resolved"

# The Node.js baseline is verified whenever the toolchain is present. A missing toolchain
# is an error only once studio/ exists, so a backend-only checkout still bootstraps.
check-node-version:
	@echo "==> toolchain: Node.js (baseline $(NODE_VERSION)) and pnpm (baseline $(PNPM_VERSION))"
	@if ! command -v node >/dev/null 2>&1; then \
		if [ -d $(STUDIO) ]; then \
			echo "Node.js is not installed. Install Node.js $(NODE_VERSION) (see .nvmrc)."; \
			exit 1; \
		fi; \
		echo "    node: not installed, studio not present, skipped"; \
		exit 0; \
	fi; \
	node_major=$$(node --version | sed 's/^v//' | cut -d. -f1); \
	baseline_major=$$(echo $(NODE_VERSION) | cut -d. -f1); \
	if [ "$$node_major" -lt "$$baseline_major" ]; then \
		echo "Node.js mismatch: baseline is $(NODE_VERSION) (see .nvmrc), found $$(node --version)."; \
		exit 1; \
	fi; \
	echo "    node $$(node --version)"
	@if ! command -v pnpm >/dev/null 2>&1; then \
		if command -v corepack >/dev/null 2>&1; then corepack enable >/dev/null 2>&1 || true; fi; \
	fi; \
	if ! command -v pnpm >/dev/null 2>&1; then \
		if [ -d $(STUDIO) ]; then \
			echo "pnpm is not installed. Enable it with 'corepack enable' and use pnpm $(PNPM_VERSION)."; \
			exit 1; \
		fi; \
		echo "    pnpm: not installed, studio not present, skipped"; \
		exit 0; \
	fi; \
	pnpm_major=$$(pnpm --version | cut -d. -f1); \
	baseline_pnpm_major=$$(echo $(PNPM_VERSION) | cut -d. -f1); \
	if [ "$$pnpm_major" -lt "$$baseline_pnpm_major" ]; then \
		echo "pnpm mismatch: baseline is $(PNPM_VERSION), found $$(pnpm --version)."; \
		exit 1; \
	fi; \
	echo "    pnpm $$(pnpm --version)"

## dev: start PostgreSQL, the Mock Provider, the Backend and Studio. The Mock Provider sits
## behind the `mock` profile in deploy/compose.yaml, so the profile is selected here.
dev:
	docker compose -f deploy/compose.yaml --profile mock up

## test: Go unit tests and Studio component tests.
test:
	@echo "==> backend: unit tests"
	@cd $(BACKEND) && $(GO) test ./...
	@if [ -d $(STUDIO) ]; then \
		echo "==> studio: component tests"; \
		cd $(STUDIO) && pnpm test --run; \
	else \
		echo "studio: not present, skipped"; \
	fi

## test-integration: PostgreSQL, Runtime, callback and SSE tests. Requires a database and
## fails loudly when one is unavailable; an integration test is never silently skipped.
test-integration:
	@echo "==> backend: integration tests (build tag: integration)"
	@if [ -z "$${EMBERLING_TEST_DATABASE_URL:-}" ] && ! docker info >/dev/null 2>&1; then \
		echo "Integration tests need PostgreSQL 18."; \
		echo "Set EMBERLING_TEST_DATABASE_URL to a reachable database, or start Docker so"; \
		echo "testcontainers can run postgres:18. Refusing to skip."; \
		exit 1; \
	fi
	@cd $(BACKEND) && $(GO) test -tags integration -count=1 ./test/... ./internal/...

## test-e2e: Playwright acceptance of the three MVP scenarios.
test-e2e:
	@if [ -d $(STUDIO) ]; then \
		cd $(STUDIO) && pnpm exec playwright test; \
	else \
		echo "studio: not present, skipped"; \
	fi

## lint: go vet, formatting check and Studio lint.
lint: fmt-check vet
	@if [ -d $(STUDIO) ]; then \
		echo "==> studio: lint"; \
		cd $(STUDIO) && pnpm lint; \
	else \
		echo "studio: not present, skipped"; \
	fi

fmt-check:
	@echo "==> backend: gofmt"
	@unformatted=$$(cd $(BACKEND) && gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt reported unformatted files:"; \
		echo "$$unformatted" | sed 's/^/    backend\//'; \
		echo "Run: cd backend && gofmt -w ."; \
		exit 1; \
	fi

vet:
	@echo "==> backend: go vet"
	@cd $(BACKEND) && $(GO) vet ./...
	@cd $(BACKEND) && $(GO) vet -tags integration ./...

## check: the full repository gate.
check: fmt-check lint test test-integration
	@if [ -d $(STUDIO) ]; then \
		echo "==> studio: build"; \
		cd $(STUDIO) && pnpm build; \
	else \
		echo "studio: not present, skipped"; \
	fi
	@echo "==> git: whitespace check"
	@git diff --check
