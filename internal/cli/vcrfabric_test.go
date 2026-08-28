//go:build integration || smoke

package cli

// One cs-vcr per campaign group, joined to that group's own fabric network.
//
// The alternative is a single host-wide proxy reached through an SSH reverse
// tunnel per member. Both work; this one is better for three reasons. The
// container joins only cs-sandbox-<group>, so one campaign cannot read or
// write another's cassettes. Traffic stays on the podman bridge and never
// reaches the host INPUT chain, so no firewall rule and no privilege are
// involved. And the container takes the network alias `vcr` on its own
// network, so every campaign profile names the same URL regardless of group —
// aliases are network-scoped, while the container name is host-global and
// therefore carries the group.
//
// Guests resolve it through the fabric's own dnsmasq, which forwards what it is
// not authoritative for to the bridge's aardvark, and aardvark answers for
// network aliases.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// vcrImage is a distroless base: the proxy is a static binary bind-mounted in,
// so the image needs nothing but a place to stand.
const vcrImage = "gcr.io/distroless/static-debian12:nonroot"

// vcrBaseURL is what a member's CLI is aimed at. The alias and the port are
// fixed, so this string is the same in every campaign.
const vcrBaseURL = "http://vcr:8080"

// vcrHost is the alias on its own, for the NO_PROXY that keeps a member's model
// calls off the tunnel and pointed straight at the base URL above.
const vcrHost = "vcr"

// vcrProxy is one running proxy, and the knowledge of how to stop it.
type vcrProxy struct {
	name  string
	group string
	store string
	// diag is this run's diagnostics directory: the proxy's whole log, and in
	// replay the requests it could not serve. Outside the test's t.TempDir, so
	// it outlives the run that needs explaining.
	diag string
	// tail streams the container's output while it runs, because --rm takes the
	// container's log away with the container. Guarded: the streamer writes it
	// on its own goroutine.
	// reaper outlives this process just long enough to remove the container,
	// and stdin is the pipe whose closing tells it to.
	reaper *exec.Cmd
	stdin  io.WriteCloser
}

// errVCRUnavailable says this host cannot run the proxy at all, which is a
// skip rather than a failure: a tier that could not start has found nothing.
var errVCRUnavailable = errors.New("cs-vcr cannot run on this host")

// startVCR launches the proxy on a group's network in record or replay mode,
// with store as the cassette directory. It returns once the container is up.
//
// It returns an error rather than failing the test, because the caller runs it
// on its own goroutine — the network it joins does not exist until create has
// made the first member, so the launch has to overlap with create — and
// t.Fatal outside the test's own goroutine does not stop a test.
func startVCR(t *testing.T, sc scenario, group, mode, store, configDir string) (*vcrProxy, error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil, fmt.Errorf("%w: podman is not installed", errVCRUnavailable)
	}
	bin, err := stageVCRBinary(configDir)
	if err != nil {
		return nil, err
	}
	network := "cs-sandbox-" + group
	name := "cs-vcr-" + group
	_ = exec.Command("podman", "rm", "-f", name).Run()

	if err := waitForFabric(network); err != nil {
		return nil, err
	}

	config, err := writeVCRConfig(sc, configDir)
	if err != nil {
		return nil, err
	}
	p := &vcrProxy{name: name, group: group, store: store}
	args := []string{
		"run", "-d", "--name", name,
		"--network", network + ":alias=vcr",
		// keep-id plus this uid runs the proxy as the invoking user, so a
		// cassette written into the bind-mounted store is owned by them on the
		// host. Without it the files land under a subuid and reading them back
		// needs `podman unshare`.
		"--userns=keep-id", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", bin + ":/usr/local/bin/cs-vcr:ro,Z",
		"-v", store + ":/cassettes:Z",
		"-v", config + ":/vcr-config.yaml:ro,Z",
		"-e", "CS_VCR_CASSETTES=/cassettes",
		"-e", "VCR_LISTEN=0.0.0.0:8080",
		"-e", "VCR_ADMIN=127.0.0.1:8081",
	}
	// Not under configDir: that is the test's t.TempDir, and a diagnostic that
	// goes away with the test answers nothing. This sits beside the live tier's
	// kept evidence.
	diag, err := filepath.Abs(filepath.Join("..", "..", ".tmp", "live-proxy", group))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(diag, 0o700); err != nil {
		return nil, err
	}
	p.diag = diag
	// A replay that misses names the step it expected and how the request
	// differed, but only far enough to fit a log line. The dumped request is
	// the whole one, and `cs-vcr calibrate` reads a directory of them to
	// propose the rules that would have matched, which is the documented way
	// to make a real agent run replayable.
	if mode == "replay" {
		args = append(args, "-v", diag+":/misses:Z")
	}
	args = append(args, vcrImage, "/usr/local/bin/cs-vcr", mode, "--config", "/vcr-config.yaml")
	if mode == "replay" {
		args = append(args, "--dump-misses", "/misses")
	}
	if out, err := exec.Command("podman", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start cs-vcr on %s: %w\n%s", network, err, out)
	}
	if err := p.startReaper(); err != nil {
		return nil, err
	}
	t.Cleanup(p.stop)
	time.Sleep(2 * time.Second)
	if out, err := exec.Command("podman", "inspect", name, "--format", "{{.State.Running}}").Output(); err != nil ||
		strings.TrimSpace(string(out)) != "true" {
		logs, _ := exec.Command("podman", "logs", name).CombinedOutput()
		return nil, fmt.Errorf("cs-vcr did not stay up on %s:\n%s", network, logs)
	}
	t.Logf("cs-vcr %s on %s as vcr, store=%s", mode, network, store)
	return p, nil
}

// logs stops the proxy gracefully and returns everything it printed.
//
// Graceful matters: cs-vcr writes its accounting — how many steps it served and
// how many upstream calls it made — when it is asked to shut down. `podman rm
// -f` kills it before it can, and the tier's central assertion, that a replay
// spent nothing, then has nothing to read. Idempotent, so the assertion and
// the teardown can both ask.
func (p *vcrProxy) logs() string {
	_ = exec.Command("podman", "stop", "--time", "10", p.name).Run()
	out, err := exec.Command("podman", "logs", p.name).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

// stop removes the container, keeping what it served.
func (p *vcrProxy) stop() {
	if out := []byte(p.logs()); len(out) > 0 {
		// The whole log to a file, the tail to the terminal. The container is
		// removed a few lines below and takes its log with it, and the tail is
		// the wrong 20 lines for every question worth asking: which steps were
		// served, in what order, and where the session stopped matching. Read
		// as a tail alone it says a replay served two requests when it served
		// sixty.
		if p.diag != "" {
			if err := os.WriteFile(filepath.Join(p.diag, "proxy.log"), out, 0o600); err == nil {
				fmt.Printf("cs-vcr %s full log: %s\n", p.name, filepath.Join(p.diag, "proxy.log"))
			}
		}
		fmt.Printf("cs-vcr %s summary:\n%s\n", p.name, tail(string(out), 20))
	}
	if dumped, err := filepath.Glob(filepath.Join(p.diag, "[0-9]*.json")); err == nil && len(dumped) > 0 {
		fmt.Printf("cs-vcr %s dumped %d missed request(s) in %s\n", p.name, len(dumped), p.diag)
	}
	// The reaper is what removes this container when the run dies without
	// unwinding. Here the run is unwinding, so close its pipe and let it go
	// before doing the removal itself.
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	if p.reaper != nil {
		_ = p.reaper.Wait()
		p.reaper = nil
	}
	_ = exec.Command("podman", "rm", "-f", p.name).Run()
}

// waitForFabric waits for the group's network and for the keepalive container
// that is the last thing brought up for a fabric.
//
// Waiting on the network alone is not enough. cs-campaign is still configuring
// the fabric at that moment — dnsmasq is taking an address on the bridge and
// the gateway container is starting — and a proxy that begins forwarding then
// gets a TLS handshake timeout on every upstream call while resolution
// settles. The symptom arrives fifteen minutes later as a readback timeout,
// which points nowhere near here.
func waitForFabric(network string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if exec.Command("podman", "network", "exists", network).Run() == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if exec.Command("podman", "network", "exists", network).Run() != nil {
		return fmt.Errorf("network %s never appeared", network)
	}
	keepalive := network + "-keepalive"
	for time.Now().Before(deadline) {
		out, err := exec.Command("podman", "inspect", "-f", "{{.State.Running}}", keepalive).Output()
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			break
		}
		time.Sleep(time.Second)
	}
	time.Sleep(5 * time.Second) // let dnsmasq finish claiming its address
	return nil
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

// stageVCRBinary copies the installed cs-vcr into scratch and returns the copy,
// which is what the container mounts and runs.
//
// A copy rather than the installed binary itself, because the mount carries `Z`
// and `Z` relabels the source. On a host with SELinux enforcing, a binary under
// ~/.local/bin is `home_bin_t` and a container cannot exec it at all —
// `exec container process /usr/local/bin/cs-vcr: Permission denied`, with the
// proxy dead before it serves a request. Relabelling a copy fixes that without
// touching the file the developer installed.
//
// Bind-mounted rather than baked into an image, so the proxy under test is the
// one this machine has.
func stageVCRBinary(dir string) (string, error) {
	path, err := exec.LookPath("cs-vcr")
	if err != nil {
		return "", fmt.Errorf("%w: cs-vcr is not on PATH (go install github.com/codesweep-ai/vcr/cmd/cs-vcr@latest)",
			errVCRUnavailable)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	staged := filepath.Join(dir, "cs-vcr")
	if err := os.WriteFile(staged, body, 0o755); err != nil {
		return "", fmt.Errorf("stage cs-vcr: %w", err)
	}
	return staged, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// startReaper leaves a process holding one end of a pipe this test holds the
// other end of, whose only job is to remove the container when that pipe
// closes.
//
// Which is what the kernel does to it whenever this process dies, however it
// dies: exiting, signalled, panicking on the test timeout, or killed outright.
// t.Cleanup covers a pass or a fail and the interrupt path covers a signal;
// neither runs for the last two, and a proxy left by one of those holds a port
// and a network alias until somebody notices.
//
// Here rather than in cs-vcr, which has no business knowing what started it:
// stdin already means something to every other caller of that binary, and to a
// service manager it means a closed /dev/null, which would read as "shut down"
// the moment it started. The lifetime belongs to whoever owns the process.
func (p *vcrProxy) startReaper() error {
	// `cat` blocks until the pipe closes and podman then removes the container.
	// A shell because this runs on the host, where there is one; the proxy's own
	// image is distroless and could not have done this from the inside.
	cmd := exec.Command("sh", "-c", `cat >/dev/null; exec podman rm -f "$1" >/dev/null 2>&1`, "sh", p.name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("hold the reaper's pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the reaper for %s: %w", p.name, err)
	}
	p.reaper, p.stdin = cmd, stdin
	return nil
}
