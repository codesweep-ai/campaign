# cs-campaign — build, test, gate and release.
#
# `make` with no argument prints the menu. `make check` is the one command to
# run before pushing, and it is what CI runs.

GORELEASER ?= goreleaser
CS_LINT    ?= go tool cs-lint
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

.PHONY: help guestbin build build-go build-go-embedded install uninstall test tools setup-smoke test-smoke \
        test-integration fixtures fixtures-strict coverage coverage-check coverage-baseline \
        covmap covmap-scripts vet fmt fmt-check lint deadcode prose refs oss \
        fixtures-check surface ledger check ci snapshot \
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

# The tool surface every tier that boots a machine runs on.
#
# cs-sandbox boots the members and cs-vcr serves the cassette, and a campaign
# resolves both from PATH at run time. "Whatever is installed" is not good
# enough: `create` refuses a cs-sandbox that is not the one this build names, so
# a tier needs the go.mod pins specifically. These targets put them in a
# directory of this repository's own and run against that — a fresh machine
# needs no `make install` in a sibling checkout, and the developer's
# ~/.local/bin is left as they arranged it.
#
# bin/tools rather than bin: cs-sandbox reads dir(dir(argv0)) and treats it as a
# sandbox checkout when it holds a go.mod, and bin/cs-sandbox would point that
# at THIS repository's root. It falls back to its embedded assets when it finds
# no image/ tree there, so bin would work today — by the fallback rather than by
# design, which is not a thing to build a tier on. bin is also where
# cs-campaign itself is built.
TOOLSDIR   := $(abspath bin/tools)
SANDBOX    := $(TOOLSDIR)/cs-sandbox
WITH_TOOLS := PATH="$(TOOLSDIR):$$PATH"

## tools: cs-sandbox, cs-vcr and the agent tools in bin/tools, at the go.mod pins
##
## `go install` with no @version resolves through go.mod, so what lands here is
## the pin by construction and `make repin` moves it — there is no second place
## recording a version, and nothing to keep in step by hand. Measured at about a
## second with a warm module cache and near nothing when the binaries are
## current, which is what lets it be a prerequisite rather than a step somebody
## has to remember.
##
## CGO_ENABLED=0 for cs-vcr is load-bearing. The proxy is bind-mounted into
## gcr.io/distroless/static-debian12 — no libc, no dynamic loader — so a cgo
## build (the default wherever there is a gcc) dies at exec with
## "/usr/local/bin/cs-vcr: No such file or directory": the kernel reporting the
## missing ELF interpreter, not a missing binary. Every model call then fails,
## each scenario burns its ceiling, and the tier times out rather than saying
## what broke.
##
## The agent tools come from cs-sandbox because cs-sandbox is what ships them,
## and they are installed beside it so the whole directory is one function of
## one pin. Run with bin/tools already on PATH so the command does not offer to
## add it to a shell profile, which is the opposite of the point here.
tools:
	@mkdir -p $(TOOLSDIR)
	@GOBIN=$(TOOLSDIR) go install github.com/codesweep-ai/sandbox/cmd/cs-sandbox
	@CGO_ENABLED=0 GOBIN=$(TOOLSDIR) go install github.com/codesweep-ai/vcr/cmd/cs-vcr
	@out="$$($(WITH_TOOLS) $(SANDBOX) install-agent-tools $(TOOLSDIR))" && printf '%s\n' "$$out" | head -1

## What `cs-sandbox build` is asked for when this host has no image yet. The
## default builds the image a real campaign uses, which is the one `create`
## looks for when CS_SANDBOX_IMAGE is unset. CI overrides it with
## `--slim --with-agents`, because the shipped image is 9.3 GB and a hosted
## runner does not have the disk — and CI sets CS_SANDBOX_IMAGE to match, since
## each variant has a package of its own.
SANDBOX_BUILD_FLAGS ?= --engine firecracker

## setup-smoke: the host state a tier that boots machines needs, at the pinned versions
##
## A prerequisite of test-smoke rather than a paragraph in a document, so a
## machine that has never built a sibling project reaches a passing tier with one
## command. The live tiers below take the same setup and the same prerequisite;
## the name is the tier that always needs it, not the only one that does.
##
## Cheap when the host is already set up, which is what makes that safe: podman
## is asked whether the image a run would boot is there, and only its absence
## runs the build. That guard is not decoration — `cs-sandbox build` asks the
## registry for the image before it considers building one, so running it
## unconditionally would put a network round trip in front of every smoke run
## and a full image build in front of every offline one.
##
## The question goes to podman rather than to `cs-sandbox doctor`, which is the
## obvious place to put it and the wrong one: doctor reports an unbuilt image as
## a warning and still exits 0, so a probe reading its exit code would call every
## fresh machine ready and build nothing — which is the one case this target
## exists for. Measured, on a host with the image renamed away.
##
## The image asked about is the one `create` will boot: CS_SANDBOX_IMAGE where it
## is set, and the reference cs-sandbox names for itself otherwise. Both carry
## the pinned version, so `make repin` renames the image this host is asked for
## and the rebuild happens without anybody deciding to. Nothing here asks after
## the Firecracker artifacts: the same build makes them, and cs-sandbox fetches
## what is missing on the first `create`.
##
## A host that boots no microVMs is reported and passed over rather than failed.
## test-smoke skips itself, saying which, on such a machine, and a setup step
## that turned that skip into a failure would take the tier away from every
## machine that cannot run it — which is most of them. The two doctors report on
## the same terms: the tier decides what it can run. A build that was actually
## attempted and then failed does fail the target, because a host that got that
## far has a fault rather than a limitation.
setup-smoke: tools
	@if ! command -v podman >/dev/null 2>&1 || [ ! -w /dev/kvm ]; then \
		echo "setup-smoke: this host boots no microVMs (needs podman and a writable /dev/kvm) — test-smoke will skip"; \
	else \
		image=$${CS_SANDBOX_IMAGE:-$$($(SANDBOX) version --images | awk '$$1=="image"{print $$2}')}; \
		if podman image exists "$$image"; then \
			echo "setup-smoke: $$image is built"; \
		else \
			$(WITH_TOOLS) $(SANDBOX) build $(SANDBOX_BUILD_FLAGS); \
		fi; \
	fi
	@$(WITH_TOOLS) $(SANDBOX) doctor --engine firecracker || \
		echo "setup-smoke: cs-sandbox doctor says this host is not ready; test-smoke will skip what it cannot run"
	@$(WITH_TOOLS) go run ./cmd/cs-campaign doctor || \
		echo "setup-smoke: cs-campaign doctor says the surface is not the one this tree names"

## test-smoke: the whole protocol on real VMs, with the model turns replayed
##
## Boots real Firecracker machines and runs a campaign end to end, serving every
## model call from the committed cassette through a cs-vcr on the campaign's own
## fabric. It holds no credential and reaches no provider, which is what makes it
## the tier CI runs on every push. Needs /dev/kvm and podman; `setup-smoke`
## supplies cs-sandbox, cs-vcr and the agent tools, and the tier skips itself,
## saying which, where the host cannot carry it.
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

test-smoke: setup-smoke
	@scripts/coverage.sh reset smoke
	$(WITH_TOOLS) \
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

test-integration: setup-smoke
	@scripts/coverage.sh reset integration
	$(DOTENV) $(WITH_TOOLS) CS_CAMPAIGN_LIVE=1 CS_COVERDIR=$(COVER_ABS)/integration go test -tags integration $(COVERFLAGS) \
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

fixtures: setup-smoke
	$(DOTENV) $(WITH_TOOLS) CS_CAMPAIGN_LIVE=1 CS_CAMPAIGN_RECORD=1 go test -tags integration -count=1 -p 1 -v -timeout 7200s \
	  ./internal/cli -run '$(FIXTURE_TESTS)'

## fixtures-strict: the same recording, with a skip treated as a failure. For a
## host that holds every credential and means to re-record all five: a missing
## one skips under `fixtures`, and a run that recorded nothing reports the same
## green as one that recorded everything. scripts/record-fixtures.sh runs this.
fixtures-strict: setup-smoke
	$(DOTENV) $(WITH_TOOLS) CS_CAMPAIGN_LIVE=1 CS_CAMPAIGN_RECORD=1 CS_CAMPAIGN_STRICT=1 \
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
prose:
	$(CS_LINT) prose

## refs: check that everything the documents point at is there
refs:
	$(CS_LINT) refs

## oss: the rules this repo has to satisfy as a published project
oss:
	$(CS_LINT) oss

## surface: check the docs against the binary, the code and the build
surface: build
	$(CS_LINT) surface

# The four targets above are one shared tool: github.com/codesweep-ai/lint.
# prose and refs ask for no binary and run first; surface reads the one
# build makes.
# Its knobs for this repo live in .cs-lint.yaml, and `cs-lint <linter> --explain`
# says what each rule wants.
## ledger: validate the issue records and prove ledger.html is current
ledger:
	go tool cs-ledger check ledger

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
## The smoke tier is in, because CI runs it and this target exists to be what CI
## is. It provisions itself — setup-smoke supplies the pinned tools and builds
## the guest image where the host has none — so it costs about ten minutes on a
## Firecracker host and rather more on a cold one. Where the host cannot carry
## it the tier skips itself, and this stays green: a pass here is the gates,
## plus as much of the smoke tier as this machine can run.
##
## It runs last. Everything above it answers in seconds to a couple of minutes,
## and a run that is going to fail on gofmt should say so before it boots a
## virtual machine.
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
	$(call say,the whole protocol on real machines with the turns replayed)
	@$(MAKE) --no-print-directory test-smoke
	@printf '\nci: every gate ran. Not reproduced here: build-test on macOS, and\n'
	@printf 'the coverage job that merges tiers from other runners.\n'

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
