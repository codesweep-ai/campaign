# cs-campaign — build, test, gate and release.
#
# `make` with no argument prints the menu. `make check` is the one command to
# run before pushing, and it is what CI runs.

GORELEASER ?= goreleaser
CS_LINT    ?= cs-lint
BIN        := bin/cs-campaign
PKG        := ./cmd/cs-campaign
VIEWERBIN  := bin/cs-dispatch-viewer
VIEWERPKG  := ./dispatch-viewer/cmd/cs-dispatch-viewer
PREFIX     ?= $(HOME)/.local
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w
VIEWERLDFLAGS := -s -w
# Tracked files where git knows them, every .go file where it does not — a
# fresh clone before its first commit has nothing tracked, and an empty list
# makes `gofmt -l` read stdin and hang rather than check anything.
GO_FILES   := $(shell git ls-files '*.go' 2>/dev/null | grep . || find . -name '*.go' -not -path './bin/*' -not -path './dist/*')

# Coverage is not a separate mode: every test target below writes Go binary
# coverage data into its own tier directory under $(COVERDIR), and `make
# coverage` merges whichever tiers are present. That is what lets
# `make test test-smoke` report one aggregate number instead of the last tier
# overwriting the one before it. scripts/coverage.sh documents the layout.
#
# -test.gocoverdir must be absolute: `go test` runs each package's test binary
# with that package's directory as its working directory, so a relative path
# would scatter the data one directory per package.
COVERDIR   ?= .coverage
COVER_ABS  := $(abspath $(COVERDIR))
COVERFLAGS  = -covermode=atomic -coverpkg=$(shell go list ./... | paste -sd, -)

.PHONY: help guestbin build build-go build-go-embedded install uninstall test test-smoke \
        test-integration fixtures fixtures-strict coverage coverage-check coverage-baseline \
        covmap covmap-scripts vet fmt fmt-check lint deadcode prose refs oss \
        fixtures-check surface cs-lint-installed ledger check ci snapshot \
        release release-check clean

.DEFAULT_GOAL := help

## help: list available targets (this menu)
help:
	@echo "cs-campaign make targets:"
	@grep -E '^## [a-z][a-z0-9-]*: ' $(MAKEFILE_LIST) | sed -E 's/^## ([^:]+): (.*)/  \1|\2/' | column -t -s '|'
	@echo ""
	@echo "  PREFIX=$(PREFIX) (install location; override with make install PREFIX=/usr/local)"

GUESTBIN := internal/cli/assets/cs-campaign-member.bin

## guestbin: compile the guest binary for embedding (linux/amd64, static)
##
## Two-stage on purpose: the artifact must exist at compile time but is
## produced by a build step, so a committed placeholder keeps plain
## `go build ./...` and `go test ./...` working. `create` refuses a placeholder
## rather than installing one into a member.
guestbin:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o $(GUESTBIN).tmp ./cmd/cs-campaign-member
	mv $(GUESTBIN).tmp $(GUESTBIN)

## build: bin/cs-campaign (guest binary embedded) and bin/cs-dispatch-viewer via
## goreleaser (single target)
##
## Twice, because --output takes one path and this repo declares two build ids.
## --clean on both: goreleaser refuses a dist directory it did not empty itself,
## and by then --output has already copied the first binary out of it. The
## second skips the before hooks the first just ran — one of them is the guest
## compile, and running it twice is a minute of nothing.
build:
	@mkdir -p $(dir $(BIN))
	@if command -v $(GORELEASER) >/dev/null 2>&1; then \
		VERSION='$(VERSION)' $(GORELEASER) build --single-target --snapshot --clean --id cs-campaign --output $(BIN); \
		VERSION='$(VERSION)' $(GORELEASER) build --single-target --snapshot --clean --skip=before --id cs-dispatch-viewer --output $(VIEWERBIN); \
		git checkout -q -- $(GUESTBIN) 2>/dev/null || true; \
	else \
		echo "goreleaser not found; using go build (run 'make build-go-embedded' explicitly to force)"; \
		$(MAKE) build-go-embedded; \
	fi

## build-go-embedded: the same two binaries, guest binary embedded, without
## goreleaser. The fallback `build` takes when goreleaser is not installed —
## not `build-go`, which would embed the placeholder and be refused by a member.
build-go-embedded: guestbin
	@mkdir -p $(dir $(BIN))
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)
	@git checkout -q -- $(GUESTBIN) 2>/dev/null || true # restore the committed placeholder; the real bytes are in $(BIN)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(VIEWERLDFLAGS)' -o $(VIEWERBIN) $(VIEWERPKG)

## build-go: the same two binaries without the embedded guest binary
##
## What CI builds. It creates no campaign, and skipping the guest compile
## leaves the working tree clean.
build-go:
	@mkdir -p $(dir $(BIN))
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(VIEWERLDFLAGS)' -o $(VIEWERBIN) $(VIEWERPKG)

## versions: what this build is made of — this repo's binary, every pinned tool,
## the Go toolchain, and whether a workspace is overriding the go.mod pins. Each
## line is read by asking that binary its own version. It deliberately depends on
## nothing and runs from source: reporting a version must not trigger a build.
## -buildvcs=true because `go run` leaves out the VCS stamp by default, and that
## stamp is the version now that nothing injects one with -X.
.PHONY: versions
versions:
	@if out="$$(go run -buildvcs=true -ldflags '$(LDFLAGS)' $(PKG) version 2>&1)"; then \
		printf '%-12s %-42s %s\n' '$(notdir $(BIN))' "$$(printf '%s\n' "$$out" | awk 'NR==1{print $$2}')" 'this repo'; \
	else \
		printf '%-12s %s\n' '$(notdir $(BIN))' "FAILED — $$(printf '%s\n' "$$out" | head -1)"; \
	fi
	@for t in $$(go list tool 2>/dev/null); do \
		if out="$$(go tool $$t version 2>&1)"; then \
			printf '%-12s %s\n' "$$(basename $$t)" "$$(printf '%s\n' "$$out" | awk 'NR==1{print $$2}')"; \
		else \
			printf '%-12s %s\n' "$$(basename $$t)" "FAILED — $$(printf '%s\n' "$$out" | head -1)"; \
		fi; \
	done
	@printf '%-12s %s\n' 'go' "$$(go env GOVERSION)"
	@w="$$(go env GOWORK)"; \
	case "$$w" in \
		''|off) printf '%-12s %s\n' 'workspace' 'off — versions above are go.mod pins' ;; \
		*)      printf '%-12s %s\n' 'workspace' "$$w — local checkouts override the go.mod pins" ;; \
	esac

## repin: move every codesweep-ai tool pin to its branch tip, then report. Uses
## GOPROXY=direct because the module proxy caches branch resolution and `@main`
## can come back a commit behind origin/main. Uses GOWORK=off so this edits the
## recorded pins even while a workspace is serving local checkouts.
.PHONY: repin
repin:
	@tools="$$(go list tool 2>/dev/null | grep codesweep-ai || true)"; \
	if [ -z "$$tools" ]; then \
		echo "no codesweep-ai tools declared yet — add the first with:" >&2; \
		echo "  GOPROXY=direct go get -tool github.com/codesweep-ai/lint/cmd/cs-lint@main" >&2; \
		exit 1; \
	fi; \
	GOWORK=off GOPROXY=direct go get -tool $$(echo "$$tools" | sed 's|$$|@main|')
	@GOWORK=off go mod tidy
	@$(MAKE) versions

## install: build, then copy both binaries into $(PREFIX)/bin
##
## A real copy, so the installed command keeps working if the checkout moves.
## Re-run after every rebuild: a stale binary silently runs a fleet without the
## code you think it has.
install: build
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/cs-campaign
	install -m 0755 $(VIEWERBIN) $(PREFIX)/bin/cs-dispatch-viewer
	@echo "installed $(PREFIX)/bin/cs-campaign and $(PREFIX)/bin/cs-dispatch-viewer ($(VERSION))"
	@case ":$(PATH):" in *":$(PREFIX)/bin:"*) : ;; *) echo "note: add $(PREFIX)/bin to PATH" ;; esac

## uninstall: remove the installed binaries
uninstall:
	rm -f $(PREFIX)/bin/cs-campaign $(PREFIX)/bin/cs-dispatch-viewer

## test: the unit tier — no virtual machines, no tokens, the default build
test:
	@scripts/coverage.sh reset unit
	CS_COVERDIR=$(COVER_ABS)/unit go test $(COVERFLAGS) ./... -args -test.gocoverdir=$(COVER_ABS)/unit

## test-smoke: the whole protocol on real VMs, with the model turns replayed
##
## Boots real Firecracker machines and runs a campaign end to end, serving every
## model call from the committed cassette through a cs-vcr on the campaign's own
## fabric. It holds no credential and reaches no provider, which is what makes it
## the tier CI runs on every push. Needs /dev/kvm, podman, a group-aware
## cs-sandbox and cs-vcr; it skips itself, saying which, where one is missing.
##
## -p 1 because the members share one host's memory, one fabric address range
## and one pool of gateway ports. -v because a run boots machines and would
## otherwise print nothing for minutes — and because a tier that skipped
## everything looks exactly like one that passed.
##
## -timeout is a deadlock detector rather than a budget: sized far above the
## real runtime so that when a member wedges it is Go that ends the run and
## names the test, not the CI job timeout above it.
##
## -run is the selector, and it is not optional: a build tag ADDS files to a
## package, it does not hide the rest, so without it this target would re-run
## every unit test in internal/cli — which `make test` already ran, under a
## timeout sized for booting virtual machines.
SMOKE_TESTS ?= TestSmokeReplay

## The wait loop's poll interval, shortened for this tier alone. The campaign's
## own number is sized for turns that take minutes; a replayed turn answers in
## milliseconds, and the interval then becomes most of the campaign — measured
## at 24s of 46s, asleep. At 2 the campaign phase runs in 29s instead of 46s.
##
## `?=` so the environment or the command line still wins:
##   make test-smoke CS_CAMPAIGN_POLL_SECONDS=15
##
## It is deliberately not profile configuration. The campaign ID is the digest
## of the rendered profile and a member reads its policy from a file the model
## is shown, so a number in either would invalidate every committed cassette.
CS_CAMPAIGN_POLL_SECONDS ?= 2

## The chunk one `wait` blocks for, shortened for this tier for the same reason
## and on the same terms. It used to be reached only by a campaign with nothing
## free, because a free node short-circuited the chunk; a wait that returns only
## on a real judgment reaches it on every quiet call instead. The recorded
## orchestrators call bare `wait`, so each replayed campaign would otherwise
## spend the full 240s there — against a campaign phase measured at 29s.
##
## It overrides `--for` too, which is the point: what is replayed is a recorded
## model that asked for the campaign's number.
CS_CAMPAIGN_WAIT_SECONDS ?= 4

test-smoke:
	@scripts/coverage.sh reset smoke
	CS_CAMPAIGN_POLL_SECONDS=$(CS_CAMPAIGN_POLL_SECONDS) \
	  CS_CAMPAIGN_WAIT_SECONDS=$(CS_CAMPAIGN_WAIT_SECONDS) \
	  CS_COVERDIR=$(COVER_ABS)/smoke go test -tags smoke $(COVERFLAGS) \
	  -count=1 -p 1 -v -timeout 2400s -run '$(SMOKE_TESTS)' ./internal/cli \
	  -args -test.gocoverdir=$(COVER_ABS)/smoke

# A credential a live scenario needs, kept out of the tree and out of every
# other scenario. Loaded only by the targets below, and only a scenario that
# declares the variable is granted it — the product passes nothing a profile did
# not ask for. Nothing here reads .env, so a run without it simply skips the
# scenarios whose credential is missing.
DOTENV = set -a; [ -f .env ] && . ./.env; set +a;

## test-integration: LIVE — the backend matrix against real providers
##
## Real machines, real model turns, real money. One campaign per backend:
## claude on a subscription and on an API key, codex on a subscription and on
## an API key, and opencode on a Fireworks key. Every scenario this host cannot
## sign in for skips with the credential it wants, so a run also reports what
## one more login would cover.
##
## -run selects the live members for the same reason test-smoke does. The
## recording test is not among them: it overwrites the committed cassette, so
## it is `make fixtures` and nothing else.
INTEGRATION_TESTS ?= TestLiveMatrix|TestLiveHeterogeneousFleet

test-integration:
	@scripts/coverage.sh reset integration
	$(DOTENV) CS_CAMPAIGN_LIVE=1 CS_COVERDIR=$(COVER_ABS)/integration go test -tags integration $(COVERFLAGS) \
	  -count=1 -p 1 -v -timeout 5400s -run '$(INTEGRATION_TESTS)' ./internal/cli \
	  -args -test.gocoverdir=$(COVER_ABS)/integration

## fixtures: LIVE — record the cassettes test-smoke replays
##
## Runs each scenario against its real provider with a cs-vcr in record mode,
## and writes test/cassettes/<scenario>/. Costs a real campaign per scenario and
## needs that scenario's credential; one it cannot sign in for skips. Commit the
## result with the code: CI replays what this records.
##
## Re-record one at a time when an agent version moves:
##   make fixtures FIXTURE_TESTS='TestLiveRecordsACassette/codex-api-key'
##
## Re-record when an agent version moves, when the profile the driver renders
## changes, or when a replay starts missing. A cassette is bound to the profile
## that recorded it, because the campaign ID is that profile's digest.
FIXTURE_TESTS ?= TestLiveRecordsACassette

fixtures:
	$(DOTENV) CS_CAMPAIGN_LIVE=1 CS_CAMPAIGN_RECORD=1 go test -tags integration -count=1 -p 1 -v -timeout 7200s \
	  ./internal/cli -run '$(FIXTURE_TESTS)'

## fixtures-strict: the same recording, with a skip treated as a failure. For a
## host that holds every credential and means to re-record all five: a missing
## one skips under `fixtures`, and a run that recorded nothing reports the same
## green as one that recorded everything. scripts/record-fixtures.sh runs this.
fixtures-strict:
	$(DOTENV) CS_CAMPAIGN_LIVE=1 CS_CAMPAIGN_RECORD=1 CS_CAMPAIGN_STRICT=1 \
	  go test -tags integration -count=1 -p 1 -v -timeout 7200s ./internal/cli -run '$(FIXTURE_TESTS)'

## coverage: merge every tier present under $(COVERDIR) and print the report
coverage:
	@scripts/coverage.sh report

## coverage-check: report, then fail if a package .coverage-baseline records as
## covered has stopped being reached. It checks presence, never a percentage:
## what it exists to catch is a suite that quietly stopped running.
coverage-check: coverage
	@scripts/coverage.sh check

## coverage-baseline: re-record .coverage-baseline. Records every tier present
## by default; pass BASELINE_TIERS to restrict it to the tiers CI runs, since
## recording a tier CI never runs commits a promise nothing keeps.
coverage-baseline:
	@scripts/coverage.sh baseline $(BASELINE_TIERS)

## covmap: fold the buffered run records into covmap/ and re-render the page
##
## Records are emitted by tests through covmap.Prove into an untracked buffer.
## Folding them into the tracked artifacts is this deliberate act.
covmap:
	go run ./cmd/covmap

## covmap-scripts: fold in a sibling cs-sandbox checkout's contract-test proofs
##
## Optional, and the only target that reaches outside this repository. It fills
## the scripts tier of the behaviour map from the sandbox repo's own suite, and
## the map renders those cells as gaps without it. Point CS_SANDBOX_REPO at a
## checkout of github.com/codesweep-ai/sandbox, then run `make covmap`.
##
## There is no default: a path to somebody else's checkout is not this
## repository's to guess, and a gate that resolves outside the clone is how a
## build stops working for everyone but its author.
CS_SANDBOX_REPO ?=
covmap-scripts:
	@test -n "$(CS_SANDBOX_REPO)" -a -d "$(CS_SANDBOX_REPO)" || { \
		echo "set CS_SANDBOX_REPO to a checkout of github.com/codesweep-ai/sandbox" >&2; \
		exit 2; \
	}
	CS_SANDBOX_COVERAGE_LOG=$(abspath covmap/runs.local.jsonl) go -C $(CS_SANDBOX_REPO) test ./internal/cli ./internal/seed -count=1

## vet: go vet
vet:
	go vet ./...

## fmt: rewrite Go files with gofmt
fmt:
	@test -n "$(GO_FILES)" || { echo "no Go files"; exit 0; }; gofmt -w $(GO_FILES)

## fmt-check: fail if any Go file is unformatted
fmt-check:
	@test -n "$(GO_FILES)" || { echo "no Go files to check"; exit 0; }; \
	unformatted="$$(gofmt -l $(GO_FILES))"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-formatted:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

## lint: the Go rules from .golangci.yml (see that file for what is on and why)
##
## Once per build tag: a tag hides a file from the linter exactly as it hides
## it from the compiler, and the live tiers are entirely behind tags.
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint is not installed; see https://golangci-lint.run/welcome/install/" >&2; \
		exit 2; \
	}
	golangci-lint run
	golangci-lint run --build-tags=integration
	golangci-lint run --build-tags=smoke

## deadcode: functions no entry point reaches
##
## golangci-lint's `unused` cannot see this — it reasons one package at a time,
## so a function whose only caller lives in another package looks used. Drop
## -test and it answers a softer question: what only a test keeps alive?
deadcode:
	@command -v deadcode >/dev/null 2>&1 || { \
		echo "deadcode is not installed: go install golang.org/x/tools/cmd/deadcode@latest" >&2; \
		exit 2; \
	}
	@out="$$(deadcode -test ./...)"; \
	if [ -n "$$out" ]; then echo "$$out"; exit 1; fi
	@out="$$(deadcode -test -tags=integration ./...)"; \
	if [ -n "$$out" ]; then echo "$$out"; exit 1; fi
	@out="$$(deadcode -test -tags=smoke ./...)"; \
	if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

## prose: check how this repository's documents are written
prose: cs-lint-installed
	$(CS_LINT) prose

## refs: check that everything the documents point at is there
refs: cs-lint-installed
	$(CS_LINT) refs

## oss: the rules this repo has to satisfy as a published project
oss: cs-lint-installed
	$(CS_LINT) oss

## surface: check the docs against the binary, the code and the build
surface: build cs-lint-installed
	$(CS_LINT) surface

# The four targets above are one shared tool: github.com/codesweep-ai/lint.
# prose and refs ask for no binary and run first; surface reads the one
# build makes.
# Its knobs for this repo live in .cs-lint.yaml, and `cs-lint <linter> --explain`
# says what each rule wants.
cs-lint-installed:
	@command -v $(CS_LINT) >/dev/null 2>&1 || { \
		echo "cs-lint is not installed: go install github.com/codesweep-ai/lint/cmd/cs-lint@latest" >&2; \
		exit 2; \
	}

## ledger: validate the issue records and prove ledger.html is current
ledger:
	@command -v cs-ledger >/dev/null 2>&1 || { \
		echo "cs-ledger is not installed: go install github.com/codesweep-ai/ledger/cmd/cs-ledger@latest" >&2; \
		exit 2; \
	}
	cs-ledger check ledger

## fixtures-check: prove the committed cassettes replay under this cs-vcr
##
## One process, no machine, no provider, so it runs anywhere — including the
## hosts that cannot run test-smoke at all. test-smoke asks the same question
## per scenario before it provisions, which is where it matters most; this is
## what puts the answer inside `make check`, where a fixture that has rotted
## against a newer cs-vcr is caught by name rather than as a silent hang.
fixtures-check:
	@scripts/fixtures-check.sh

## check: the full local gate — fmt, vet, the linters, and the unit tier
check: fmt-check vet lint deadcode test coverage-check fixtures-check prose refs oss surface

# say prints a heading above each gate, so a long run reads as a list rather
# than as a wall. Bold where a terminal is reading it and plain where a pipe
# is: `make ci > ci.log` should leave a log somebody can read. The escapes are
# the same ones scripts/check.sh uses in tracer, which is where the shape came
# from.
define say
@if [ -t 1 ]; then printf '\n\033[1m==> %s\033[0m\n' "$(1)"; else printf '\n==> %s\n' "$(1)"; fi
endef

## ci: every gate the CI workflow runs, on this machine
##
## One Linux leg of .github/workflows/ci.yml, in the order CI runs it, so a
## red build is something you can see before you push rather than after.
##
## The smoke tier is left out: it boots real machines through cs-sandbox on a
## Firecracker host, and replays a recorded cassette. Run it with
## `make test-smoke` where the host can carry it.
ci:
	$(call say,the gate a contributor runs before pushing)
	@$(MAKE) --no-print-directory check
	$(call say,build)
	@$(MAKE) --no-print-directory build-go
	$(call say,release manifest)
	@$(MAKE) --no-print-directory release-check
	$(call say,ledger)
	@$(MAKE) --no-print-directory ledger
	$(call say,the behaviour map)
	go test ./internal/covmap/... -run TestCoverageHTMLCurrent -count=1
	git diff --exit-code -- covmap/
	@printf '\nci: every gate ran. Not reproduced here: build-test on macOS, the\n'
	@printf 'smoke tier, and the coverage job that merges tiers from other runners.\n'

## snapshot: local release dry-run into dist/ (all platforms, archives, checksums)
##
## Skips the bill of materials and the signature; both need tools a release job
## has and a laptop usually does not.
snapshot:
	VERSION='$(VERSION)' $(GORELEASER) release --snapshot --clean --skip=sbom,sign
	@git checkout -q -- $(GUESTBIN) 2>/dev/null || true # goreleaser's hook built the real one

## release: tagged release (needs a pushed git tag and credentials)
release:
	$(GORELEASER) release --clean
	@git checkout -q -- $(GUESTBIN) 2>/dev/null || true # goreleaser's hook built the real one

## release-check: validate .goreleaser.yaml
release-check:
	$(GORELEASER) check

## clean: remove build output
clean:
	rm -rf bin dist $(COVERDIR)
