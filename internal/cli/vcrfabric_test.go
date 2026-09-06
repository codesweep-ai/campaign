//go:build integration || smoke

package cli

// One cs-vcr for the run, as a plain process on this host.
//
// This is cs-sandbox's own recipe for recording a sandbox, and it is followed
// rather than invented because the credential loan depends on it. A shared
// sandbox holds its credential and dials the recorder itself, so it needs the
// name a guest reaches this host under. A LENT one holds a token worth nothing
// and is handed the lender, which runs here — so the recorder is dialled from
// this host, on loopback, and never has to be reachable from a guest at all.
//
// One listener on 0.0.0.0 serves both, which is why there is no container, no
// fabric network to join and no published port. A container would buy
// isolation between concurrent runs, and this tier is serial.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Where the recorder listens. Fixed rather than drawn from the ephemeral range:
// the port travels in the profile, the campaign ID is the sha256 of the
// resolved profile, and a value that moved between a recording and its replay
// would move every name derived from it.
const (
	vcrPort     = "8080"
	vcrListen   = "0.0.0.0:" + vcrPort
	vcrAdmin    = "127.0.0.1:8081"
	vcrLoopback = "127.0.0.1:" + vcrPort
)

// vcrHost is the name a guest resolves to reach this host, which is how a
// member that dials the recorder itself finds it. cs-sandbox puts it in every
// guest's hosts file; the literal is that tool's engine.HostReachableName.
const vcrHost = "host.containers.internal"

// vcrURL is where this scenario's members reach their model traffic. A lent
// scenario names the loopback the LENDER dials; a copying one names the host as
// its own members reach it.
func vcrURL(sc scenario) string {
	if sc.lends() {
		return "http://" + vcrLoopback
	}
	return "http://" + vcrHost + ":" + vcrPort
}

// vcrProxy is the running recorder, and the knowledge of how to stop it.
type vcrProxy struct {
	cmd   *exec.Cmd
	out   *syncBuffer
	mode  string
	store string
	// diag is this run's diagnostics directory: in replay, the requests it
	// could not serve. Outside the test's t.TempDir, so it outlives the run
	// that needs explaining.
	diag string
	once sync.Once
	log  string
}

// syncBuffer collects the recorder's output so it can be read while it runs.
// os/exec copies a child's output on a goroutine of its own, so a plain
// bytes.Buffer is only safe to read once Wait has returned.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// errVCRUnavailable says this host cannot run the proxy at all, which is a
// skip rather than a failure: a tier that could not start has found nothing.
var errVCRUnavailable = errors.New("cs-vcr cannot run on this host")

// startVCR launches the recorder in record or replay mode, with store as the
// cassette directory, and returns once it answers.
func startVCR(t *testing.T, sc scenario, group, mode, store, configDir string) (*vcrProxy, error) {
	bin, err := exec.LookPath("cs-vcr")
	if err != nil {
		return nil, fmt.Errorf("%w: cs-vcr is not on PATH (go install github.com/codesweep-ai/vcr/cmd/cs-vcr@latest)", errVCRUnavailable)
	}
	if err := os.MkdirAll(store, 0o750); err != nil {
		return nil, err
	}
	config, err := writeVCRConfig(sc, configDir)
	if err != nil {
		return nil, err
	}
	diag, err := filepath.Abs(filepath.Join("..", "..", ".tmp", "live-proxy", group))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(diag, 0o700); err != nil {
		return nil, err
	}
	args := []string{mode, "--config", config, "--cassettes", store,
		"--listen", vcrListen, "--admin", vcrAdmin}
	if mode == "replay" {
		// A replay that misses names the step it expected only as far as a log
		// line fits. The dumped request is the whole one, and `cs-vcr
		// calibrate` reads a directory of them to propose the rules that would
		// have matched.
		args = append(args, "--dump-misses", diag)
	}
	p := &vcrProxy{cmd: exec.Command(bin, args...), out: &syncBuffer{}, mode: mode, store: store, diag: diag}
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cs-vcr %s: %w", mode, err)
	}
	t.Cleanup(func() { p.stop() })
	if err := waitForVCR(p); err != nil {
		return nil, err
	}
	t.Logf("cs-vcr %s on %s, cassettes in %s", mode, vcrListen, store)
	return p, nil
}

// waitForVCR blocks until the recorder is accepting connections, so a member
// created immediately after does not race it.
func waitForVCR(p *vcrProxy) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if p.cmd.ProcessState != nil {
			return fmt.Errorf("cs-vcr %s exited before it served:\n%s", p.mode, tail(p.out.String(), 20))
		}
		conn, err := net.DialTimeout("tcp", vcrLoopback, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("cs-vcr %s never answered on %s. Port %s in use?\n%s",
		p.mode, vcrLoopback, vcrPort, tail(p.out.String(), 20))
}

// logs stops the recorder gracefully and returns everything it printed.
//
// Graceful matters: cs-vcr writes its accounting — how many steps it served and
// how many upstream calls it made — when it is asked to shut down. A kill takes
// that away, and the tier's central assertion, that a replay spent nothing,
// then has nothing to read. Idempotent, so the assertion and the teardown can
// both ask.
func (p *vcrProxy) logs() string {
	p.once.Do(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { _ = p.cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = p.cmd.Process.Kill()
				<-done
			}
		}
		p.log = p.out.String()
	})
	return p.log
}

// stop ends the recorder, keeping what it served.
func (p *vcrProxy) stop() {
	if out := p.logs(); out != "" {
		// The whole log to a file, the tail to the terminal. The tail is the
		// wrong 20 lines for every question worth asking: which steps were
		// served, in what order, and where the session stopped matching.
		if p.diag != "" {
			if err := os.WriteFile(filepath.Join(p.diag, "proxy.log"), []byte(out), 0o600); err == nil {
				fmt.Printf("cs-vcr full log: %s\n", filepath.Join(p.diag, "proxy.log"))
			}
		}
		fmt.Printf("cs-vcr summary:\n%s\n", tail(out, 20))
	}
	if dumped, err := filepath.Glob(filepath.Join(p.diag, "[0-9]*.json")); err == nil && len(dumped) > 0 {
		fmt.Printf("cs-vcr dumped %d missed request(s) in %s\n", len(dumped), p.diag)
	}
}

// writeVCRConfig writes the proxy's configuration into a scratch directory —
// never beside the cassette store, which is committed, and never shared between
// scenarios, whose provider blocks differ.
//
// It is separate from the developer's own ~/.config/cs-vcr/config.yaml on
// purpose: that one points the openai provider at the ChatGPT backend, which is
// right for codex signed in with a subscription and wrong for an API key.
//
// The capture rules matter more than they look. A campaign's ID is the sha256
// of its resolved profile, so the group, sandbox, branch and session names all
// derive from it — and those names reach the wire inside tool-call arguments,
// which the shipped ruleset rightly treats as exact. capture blanks each match
// for comparison and restores this run's value on the way out, so a replayed
// agent is handed its own names rather than the recording's. Longest first: an
// 8-hex group pattern would otherwise eat half of a 16-hex campaign ID.
//
// The name part is [a-z0-9]+ and not [a-z]+[0-9]+. Requiring a digit matched
// the clock-derived names (`csmixed12345-…`) and nothing a recorded scenario is
// ever called: replayName mints fixed names, so `csrclaude-ab8f9de5` matched no
// rule and the campaign and group IDs went through unnormalized. The replay
// then handed the agent the recording's branch, and the readback caught it —
// "believes its branch is …csrclaude-ab8f9de5; it is …csrclaude-4fc1ce22".
//
// One rule per member, not one for both. capture numbers a rule's matches by
// order of first appearance WITHIN a request, so a single rule covering both
// sandboxes makes `<SANDBOX:1>` mean whichever member that request happened to
// mention first — the orchestrator here, dev there. The restored response then
// carries the other member's branch, and the agent writes it into its own
// summary. Measured: an orchestrator confirming its branch as dev's. A rule
// that can only ever match one value cannot be numbered wrong.
func writeVCRConfig(sc scenario, dir string) (string, error) {
	path := filepath.Join(dir, "vcr-config.yaml")
	// The member's home carries the host account's name: cs-sandbox gives the
	// guest the uid and the name of whoever launched it, so a cassette recorded
	// here says /home/<whoever recorded it> in every request that mentions a
	// path. Blanking it on both sides is what makes a cassette committable at
	// all — and what lets CI, running as a different account, replay one.
	//
	// Three spellings: a path, the flattened `-home-<user>` slug an agent's own
	// tooling derives from one, and the doubled owner-and-group column an
	// `ls -l` prints. The literal name rather than a shape: this
	// is the one account the run
	// actually has, and a pattern like /home/[a-z]+ would blank a path a
	// mission legitimately talks about.
	me, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve the account the guest will mirror: %w", err)
	}
	return path, os.WriteFile(path, fmt.Appendf(nil, `listen: 0.0.0.0:8080
admin: 127.0.0.1:8081
cassettes: /cassettes
# Agents fire auxiliary calls — a title generation, a summary — alongside the
# main turn, and the two arrive in a nondeterministic order. A lookahead window
# absorbs that.
#
# The shipped default, deliberately. Widening it to 24 while chasing a stuck
# cursor made things worse, not better: a window is only safe while alignment
# is exact, and alignment is NOT exact here — the volatile paths tolerate a
# changed tool result, which is what lets one turn of a campaign look like
# another. With 24 the window reached the whole cassette, and dev was served its
# d002 summary when the harness had asked for the d001 readback, which arrived
# as "replied but readback is not valid JSON".
lookahead: 8
# The lookahead absorbs the ORDER those auxiliary calls arrive in. cs-vcr's
# auxiliary_turns, on by default, absorbs the call itself changing: Claude
# Code's title generation picks a model per run, so a cassette that recorded it
# on haiku is replayed by a session taking it on sonnet.
# Where the real provider lives, which is the recording half's business alone —
# a replay serves the cassette without resolving any of this. It is per scenario
# because the client's own surface does not reveal it: codex on a subscription
# talks to the ChatGPT backend, and the same client with a key talks to the API.
#
# One entry, named the way this scenario's base URL names it. The prefix carries
# that name, so nothing has to be inferred from the request.
providers:
  %s:
    base_url: %s
normalize:
  capture:
    - {pattern: "cs[a-z0-9]+-[0-9a-f]{16}", as: "<CAMPAIGN_ID>"}
    - {pattern: "orchestrator-[0-9a-f]{8}", as: "<ORCHESTRATOR>"}
    - {pattern: "dev-[0-9a-f]{8}", as: "<DEV>"}
    - {pattern: "cs[a-z0-9]+-[0-9a-f]{8}", as: "<GROUP>"}
    - {pattern: "(?:/home/|-home-)(%[3]s)", as: "<USER>"}
    - {pattern: "(%[3]s %[3]s)", as: "<USER_GROUP>"}
`, sc.vcrProvider, sc.vcrUpstream, regexp.QuoteMeta(me.Username)), 0o600)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
