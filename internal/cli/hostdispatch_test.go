package cli

// The host's create-time dispatch loop, and the one thing it must not do:
// answer with a different dispatch's reply.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/codesweep-ai/campaign/internal/store"
)

// racedApp fakes a member that has moved on: d001 is replied to AND d002 has
// been opened and replied to, which is what the host finds when the
// orchestrator dispatches faster than create's readback polls.
func racedApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  exec)
    case "$5" in
      *d001.json*) printf '{"dispatch":"d001","phase":"done","note":"the briefing"}' ;;
      *d002.json*) printf '{"dispatch":"d002","phase":"done","note":"# Dispatch d002 summary"}' ;;
      *) printf 'MSG 100 d001.md\nMSG 200 d002.md\nREPLY d001\nREPLY d002\nDRIVERS 0\n' ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
}

// A wait for d001 answers with d001's reply, even though the node is on d002.
//
// The readback opens d001 and waits. A fast orchestrator opens d002 before the
// next poll, so the node's CURRENT dispatch is no longer the one being awaited
// — and reading "the current dispatch's reply" hands back a work summary where
// a briefing confirmation belongs. It failed a member that had answered
// correctly, with "replied but readback is not valid JSON".
func TestAwaitReplyAnswersWithTheDispatchItWasAskedFor(t *testing.T) {
	a := racedApp(t)
	pol := protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1000, BlindProbes: 3, PollSeconds: 1, SettlingSeconds: 1}

	reply, err := a.awaitReply(context.Background(), io.Discard, model.Member{Name: "dev", Role: "agent", CLI: "claude", Sandbox: "box", Ref: "dev.g"}, "d001", pol, 5*time.Second)
	if err != nil {
		t.Fatalf("await d001: %v", err)
	}
	if reply.Dispatch != "d001" {
		t.Fatalf("awaited d001 and got %s's reply: %+v", reply.Dispatch, reply)
	}
	if reply.Note != "the briefing" {
		t.Fatalf("note = %q, want the briefing", reply.Note)
	}

	// And the newer one is still readable on its own terms, so this is a fix
	// to which dispatch is read rather than to what the node reports.
	later, err := a.awaitReply(context.Background(), io.Discard, model.Member{Name: "dev", Role: "agent", CLI: "claude", Sandbox: "box", Ref: "dev.g"}, "d002", pol, 5*time.Second)
	if err != nil {
		t.Fatalf("await d002: %v", err)
	}
	if later.Dispatch != "d002" {
		t.Fatalf("awaited d002 and got %s's reply", later.Dispatch)
	}
}

// probeOnlyApp fakes a member whose probe answers with the given lines and
// records every other command the host runs against it.
func probeOnlyApp(t *testing.T, probe string) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	tool := filepath.Join(dir, "fake-sandbox")
	body := "#!/bin/sh\ncase \"$5\" in\n  *DRIVERS*) printf '" + probe + "' ;;\n  *) echo \"$*\" >> " + calls + " ;;\nesac\n"
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}, calls
}

// At create, a credential the provider rejects used to read as a member that
// would not answer its briefing: the ladder ran, and the failure named silence.
// The readback now fails on the first look, names the credential, and sends
// nothing into it.
func TestAwaitReplyNamesARejectedCredentialAtOnce(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d d001.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 5 unauthorized - turn failed: Incorrect API key provided\n`, now-20, now-10)
	a, calls := probeOnlyApp(t, probe)
	pol := protocol.Policy{PollSeconds: 1, SettlingSeconds: 1}

	start := time.Now()
	_, err := a.awaitReply(context.Background(), io.Discard, model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g"}, "d001", pol, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "credential") || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("the readback must fail naming the credential and the provider's words; got: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the readback took %s to report a credential the first look could see", time.Since(start))
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Fatalf("the host acted on a member whose credential was rejected:\n%s", b)
	}
}

// A throttled member at create is waited for, out loud, and nothing is sent
// while the wait its provider asked for is still running.
func TestAwaitReplyWaitsOutARefusalAndSaysSo(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d d001.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 5 throttled 600 turn failed: Rate limit reached\n`, now-20, now-5)
	a, calls := probeOnlyApp(t, probe)
	pol := protocol.Policy{PollSeconds: 1, SettlingSeconds: 1}

	var out strings.Builder
	_, err := a.awaitReply(context.Background(), &out, model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g"}, "d001", pol, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "node-refused") {
		t.Fatalf("the bound should end this wait, naming the refusal; got: %v", err)
	}
	if n := strings.Count(out.String(), "provider refused"); n != 1 {
		t.Fatalf("a refusal is said once, not once per look; said %d times:\n%s", n, out.String())
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Fatalf("the host sent something into a wait the provider asked for:\n%s", b)
	}
}
