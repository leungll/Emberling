# Repository entry point for development and CI. CI calls these targets
# instead of duplicating commands, so a green pipeline and a green workstation run the
# same gate.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

# Toolchain baselines. The authoritative files are backend/go.mod for Go
# and .nvmrc for Node; these variables only exist to verify them.
GO_VERSION   := $(shell sed -n 's/^go \([0-9.]*\)$$/\1/p' backend/go.mod)
NODE_VERSION := $(shell cat .nvmrc)
PNPM_VERSION := 11.25.0

# GOTOOLCHAIN=auto lets a host Go older than the module baseline download and run the
# pinned toolchain instead of failing the build.
GO := GOTOOLCHAIN=auto go

# golangci-lint is pinned and installed by `make bootstrap` into a repo-local tool
# directory instead of backend/go.mod. It must be built with the module's Go baseline,
# or it refuses to load a module that requires a newer Go.
GOLANGCI_LINT_VERSION := 2.14.0
TOOLS_BIN             := $(abspath .bin)
GOLANGCI_LINT         := $(TOOLS_BIN)/golangci-lint

BACKEND := backend
STUDIO  := studio
MEDIA   := services/media

.PHONY: bootstrap dev test test-integration test-e2e test-ui lint check check-fast \
        check-go-version check-node-version fmt-check lint-go lint-studio install-golangci-lint check-doc-refs \
        check-migrations migrations-checksum check-file-size test-engineering studio-build whitespace-check \
        media-test media-dev

## bootstrap: verify toolchain versions and install dependencies. Starts no service.
bootstrap: check-go-version check-node-version install-golangci-lint
	@echo "==> backend: downloading Go module dependencies"
	@cd $(BACKEND) && $(GO) mod download
	@if [ -d $(STUDIO) ]; then \
		echo "==> studio: installing dependencies"; \
		corepack enable >/dev/null 2>&1 || true; \
		cd $(STUDIO) && pnpm install --frozen-lockfile; \
	else \
		echo "studio: not present, skipped"; \
	fi

install-golangci-lint:
	@echo "==> toolchain: golangci-lint v$(GOLANGCI_LINT_VERSION)"
	@if [ -x $(GOLANGCI_LINT) ] && $(GOLANGCI_LINT) version 2>/dev/null | \
		grep -q "version $(GOLANGCI_LINT_VERSION) built with go$(GO_VERSION) "; then \
		echo "    already installed"; \
	else \
		GOTOOLCHAIN=go$(GO_VERSION) GOBIN=$(TOOLS_BIN) go install \
			github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION); \
		echo "    installed into $(TOOLS_BIN)"; \
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
	@cd $(BACKEND) && $(GO) test -race ./...
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
	@cd $(BACKEND) && $(GO) test -race -timeout 20m -tags integration -count=1 ./test/... ./internal/...

## test-e2e: Playwright acceptance of the three MVP scenarios.
test-e2e:
	@if [ -d $(STUDIO) ]; then \
		cd $(STUDIO) && pnpm exec playwright test; \
	else \
		echo "studio: not present, skipped"; \
	fi

## media-test: the media service's pytest suite. FFmpeg runs for real and transcription
## uses the tiny.en model, which is downloaded on first use. Dependencies come from the
## uv lockfile; MEDIA_FFMPEG_BIN and MEDIA_FFPROBE_BIN select the binaries, as for the
## service itself. Not part of `check`: the Go tests use a fake media service instead.
media-test:
	@echo "==> media: pytest (real FFmpeg, tiny.en)"
	@cd $(MEDIA) && uv run --frozen pytest

## media-dev: run the media service locally from the lockfile. Configure it with
## MEDIA_DATA_DIR, MEDIA_WHISPER_MODEL, MEDIA_FFMPEG_BIN, MEDIA_FFPROBE_BIN, MEDIA_PORT and
## MEDIA_PUBLIC_URL.
media-dev:
	@cd $(MEDIA) && uv run --frozen python -m app

## lint: formatting check, golangci-lint (which includes go vet) and Studio lint.
lint: fmt-check lint-go lint-studio

## test-ui: Studio UI review against recorded Backend responses at 1000 and 1440 px: token,
## truncation, focus, copy and accessibility checks, with screenshots in
## studio/tests/ui/report. Needs the Playwright Chromium download, so it is not part of check.
test-ui:
	@if [ -d $(STUDIO) ]; then \
		cd $(STUDIO) && pnpm test:ui; \
	else \
		echo "studio: not present, skipped"; \
	fi

lint-studio:
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

# Runs backend/.golangci.yml for the default build and for the integration build tag.
lint-go:
	@echo "==> backend: golangci-lint"
	@if ! [ -x $(GOLANGCI_LINT) ]; then \
		echo "golangci-lint is not installed in $(TOOLS_BIN). Run: make bootstrap"; \
		exit 1; \
	fi
	@cd $(BACKEND) && $(GOLANGCI_LINT) run ./...
	@cd $(BACKEND) && $(GOLANGCI_LINT) run --build-tags integration ./...

# Tracked files must state invariants in their own words instead of citing the unpublished
# design documents by number, filename or section, referring to a rule by its number in a
# list, or naming a planning milestone slice. git grep searches tracked files only, so the
# guard works in a public checkout without docs/. The bracketed [G] matches the same text
# as a plain G but keeps this line from matching itself; the rule-number and milestone
# alternatives cannot match their own bracketed spelling. A rule number matches with or
# without its hash, as in "invariant N" or "rule #N". \# keeps make from reading a
# comment. CLAUDE.md and README files are excluded because they describe the rule and the
# docs/ directory. The initial migration is excluded because shared migrations are
# immutable: the historical comments it already shipped with are grandfathered, and any
# new migration is checked like every other file.
DOC_REF_PATTERN := (^|[^[:alnum:]_])[0-9]{2} §|§[0-9]|(^|[^[:alnum:]_])[0-9]{2}-[a-z-]+\.md([^[:alnum:]_]|$$)|(^|[^[:alnum:]_])ENGINEERIN[G](\.md)?([^[:alnum:]_]|$$)|docs/AGENTS\.md|(^|[^[:alnum:]_])([Ii]nvariants?|[Rr]ules?) \#?[0-9]|(^|[^[:alnum:]_])M[0-9]+ slice

check-doc-refs:
	@echo "==> source: design document citations"
	@status=0; \
	git grep -nE '$(DOC_REF_PATTERN)' -- . \
		':(exclude)docs' ':(exclude)CLAUDE.md' ':(exclude,glob)**/README*.md' \
		':(exclude)backend/migrations/00001_initial.sql' \
		|| status=$$?; \
	if [ "$$status" -eq 0 ]; then \
		echo "Tracked files cite design documents or numbered rules. Name the invariant or rule instead."; \
		exit 1; \
	elif [ "$$status" -ne 1 ]; then \
		echo "git grep failed with exit status $$status."; \
		exit "$$status"; \
	fi

## check-migrations: shared migrations are immutable. Every backend/migrations/*.sql file
## must match its checksum in backend/migrations/checksums.sha256.
check-migrations:
	@echo "==> backend: migration checksums"
	@sh scripts/check-migrations.sh check

## migrations-checksum: register new migrations by appending their checksums. Existing
## manifest lines are never rewritten.
migrations-checksum:
	@sh scripts/check-migrations.sh register

## check-file-size: source files stay within the line limit; the baseline may only shrink.
check-file-size:
	@echo "==> source: file size"
	@sh scripts/check-file-size.sh

## test-engineering: prove the guards reject attempts to rewrite protected history.
test-engineering:
	@sh scripts/tests/check-engineering.sh

studio-build:
	@if [ -d $(STUDIO) ]; then \
		echo "==> studio: build"; \
		cd $(STUDIO) && pnpm build; \
	else \
		echo "studio: not present, skipped"; \
	fi

whitespace-check:
	@echo "==> git: whitespace check"
	@git diff --check

# The gate phases in execution order. scripts/run-phases.sh runs each one as its own make
# invocation, stops at the first failure and prints the elapsed seconds of every phase.
CHECK_PHASES := fmt-check lint-go lint-studio check-doc-refs check-migrations check-file-size \
                test-engineering test test-integration studio-build whitespace-check
CHECK_FAST_PHASES := $(filter-out test-integration studio-build,$(CHECK_PHASES))

## check: the full repository gate.
check:
	@MAKE='$(MAKE)' sh scripts/run-phases.sh $(CHECK_PHASES)

## check-fast: the gate without integration tests and the Studio build, for iteration.
## `make check` remains the required gate before handoff and in CI.
check-fast:
	@MAKE='$(MAKE)' sh scripts/run-phases.sh $(CHECK_FAST_PHASES)

.PHONY: evals-test

## evals-test: self-tests of the video-editing evaluation tooling (Python, uv-locked).
## They run against in-process fake services and need neither Emberling nor the media
## service to be running.
evals-test:
	@echo "==> evals: video_editing self-tests"
	@cd evals/video_editing && uv run --frozen pytest

.PHONY: demo demo-studio demo-down

## demo: fault-injection, photo-set, software-delivery and serving demo on the Compose
## stack. Builds and starts postgres, the Mock Provider, the mock production service, the
## sandbox runner and the Backend when needed, then runs each variant in turn. Every
## variant ends with the invariant report and the ledger-vs-record comparison.
## EMBERLING_DEMO_VARIANT=<name> runs one; unset, it runs every variant except the serving
## ones, which run as a group with EMBERLING_DEMO_VARIANT=serving. Variants:
##   held-before-accept    SIGKILL while the image dispatch is held before the Provider
##                         accepts it; the restarted Backend retries under the same key.
##   after-accept          SIGKILL after the Provider accepted the task, so its callback is
##                         lost and Provider polling completes the Attempt.
##   photo-set             the scripted effect-template Agent applies one template to
##                         three photos in two reviewed rounds and renders one video.
##   photo-gate            a video request for an unreviewed asset is rejected before
##                         any dispatch.
##   photo-limit           a generation past the frozen limit is rejected before any
##                         dispatch.
##   photo-sync-crash      SIGKILL while a synchronous generation is held at the Provider;
##                         the restarted Backend reports RESULT_UNCERTAIN to the Agent,
##                         which generates the photo again.
##   photo-video-restart   SIGKILL while the Agent waits for the video callback; the
##                         callback completes the original Action once, nothing re-sent.
##   delivery              the scripted delivery Agent tests a patch in the sandbox
##                         runner and deploys it once to the mock production service.
##   delivery-restart-during-test
##                         SIGKILL while the test waits for its callback; the callback,
##                         delivered twice, completes the original Attempt once.
##   delivery-deploy-lost  SIGKILL while production holds the deployment; the Agent is
##                         told the outcome is unknown and reports instead of redeploying.
##   delivery-runner-redelivery
##                         the restarted runner redelivers a finished test; no new Event.
##   vuln-verify           the scripted repair Agent, bound to a workspace of the example
##                         repository, upgrades the vulnerable dependency without adapting
##                         the code, fails that verification, adapts it, passes the second.
##   vuln-scope-denied     a write outside the allowed paths is refused before any
##                         Attempt; the Agent continues inside them.
##   vuln-stale-evidence   a verification of a digest the workspace moved past is
##                         refused; the latest digest is verified instead.
##   vuln-restart-during-verify
##                         SIGKILL while the verification callback is held; after the
##                         restart no Decision is asked again, no verification runs twice.
##   vuln-incomplete-final the Agent declares completion before verifying its latest patch
##                         or recording a change request; judged INCOMPLETE, both listed.
##   vuln-main             the verified patch is described in a change request, a person
##                         approves its release and production deploys it once.
##   vuln-restart-during-approval
##                         SIGKILL while the release waits for approval; still waiting
##                         after, the Agent clock still paused, no Decision asked again.
##   vuln-duplicate-approval
##                         the same approval submitted twice at once takes effect once.
##   vuln-reject-resubmit  the person rejects with a reason; the Agent changes the code,
##                         verifies, records a new change request and asks again.
##   vuln-release-crash-deployed
##                         production drops its answer and the Backend is killed; the
##                         restarted Backend queries and confirms instead of resending.
##   vuln-release-crash-voided
##                         SIGKILL while production holds the release; the unknown
##                         operation is voided and released again under the next key.
##   vuln-void-exhausted   both operations voided; the target is freed, nothing deployed.
##   vuln-target-occupied  a second task's release to an occupied target is refused
##                         before any approval.
##   vuln-release-refused  production refuses; the target is freed, the reason goes back
##                         to the Agent.
##   vuln-unhealthy        deployed but unhealthy, reported as a failed check.
##   vuln-prepare-only     a PREPARE_ONLY task's release request is refused by its goal.
##   vuln-release-unreachable-stop
##                         production unreachable past the confirm deadline; a person stops
##                         waiting, the task ends RELEASE_UNKNOWN with the target held and a
##                         second task is refused; once production is back the operation is
##                         voided, the target freed and a third task releases to it.
##   vuln-stop-then-deployed
##                         production deploys after a person stopped waiting; the target is
##                         freed with the version recorded as live but unchecked.
##   After the vuln variants, deploy/.demo/evidence/vuln-evidence.md (or
##   EMBERLING_DEMO_EVIDENCE_DIR) summarises their evidence in five aspects with the source
##   Runs, and lists the versions confirmed live only after a stop; see
##   scripts/demo/vuln-evidence-report.sh to render it by hand.
##   Every release variant but vuln-prepare-only decides an approval and runs only with
##   EMBERLING_OPERATOR_TOKEN (at least 32 bytes) set; EMBERLING_DEMO_APPROVAL=manual
##   makes vuln-main wait for a person on the Studio approval page.
##   serving-burst         twenty users submit the serving template at once.
##   serving-crash-held    SIGKILL while dispatches are held before acceptance.
##   serving-crash-waiting SIGKILL while every task waits for its callback; callbacks
##                         and polls complete them.
##   serving-duplicate     every callback delivered three times; no extra Event.
##   serving-version-switch
##                         a new version saved while Runs are in flight; each finishes
##                         on the version it was bound to.
##   serving-client-retry  concurrent resubmissions under one Idempotency-Key per user.
##   serving-response-lost the Provider drops each acceptance answer; the keyed retry
##                         takes over the original task.
##   serving-rollout       traffic-split.sh sends 10% of keyed requests to a slower
##                         version 2, then the ratio goes back to 0.
##   serving-capacity      the Backend restarted with two slots on the image Provider;
##                         at most two image tasks in flight, the rest queue as READY.
## EMBERLING_DEMO_STUDIO=1 turns on the Studio demo mode described under demo-studio.
## EMBERLING_DEMO_MODEL=<model id> runs vuln-verify or vuln-main against a real model. The
## Backend registers it from EMBERLING_MODELS, EMBERLING_MODEL_BASE_URL and
## EMBERLING_MODEL_API_KEY (optionally EMBERLING_MODEL_TIMEOUT) in the shell environment;
## set nothing else (an image key without image models stops the Backend). The Agent gets
## the EMBERLING_DEMO_MODEL_TIMEOUT_MS budget, thirty minutes by default.
## EMBERLING_DEMO_RUNNER=docker starts the docker sandbox runner (profile sandbox-docker,
## needs SANDBOXRUNNER_VERIFY_IMAGE) instead of the mock runner.
## Needs Docker, git, curl and jq; deliberately not part of `check`.
demo:
	@bash scripts/demo/demo.sh run

## demo-studio: `make demo` with EMBERLING_DEMO_STUDIO=1, to watch it in Studio. Also builds
## and starts Studio on http://localhost:5173, prints a Studio link to every Run, opens it in
## the browser (EMBERLING_DEMO_OPEN=0 only prints it) and pauses
## for Enter before each fault, hold release and next variant; each pause names any
## deadline a longer look would exceed. Without a terminal it prints the pause and goes on;
## EMBERLING_DEMO_PAUSE=0 skips the pauses. EMBERLING_DEMO_VARIANT selects one variant as
## for demo. The stack and Studio keep running afterwards; stop them with `make demo-down`.
demo-studio:
	@EMBERLING_DEMO_STUDIO=1 bash scripts/demo/demo.sh run

## demo-down: stop the demo stack, Studio included, and remove the request-record volumes of
## the three mock services.
demo-down:
	@bash scripts/demo/demo.sh down
