package cli

// The host holds and tries again a member whose token budget is spent, as the
// orchestrator's wait does for an agent: `resume` for the orchestrator, and
// the create-time readback for every member (SPEC.md R151, R78a).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// spentProbe is a member on dispatch id, opened ninety minutes ago, whose last
// turn ended spent ago seconds back, with the given exit. Its driver answers
// --budget with reading, or prints nothing when reading is "", as a driver too
// old to know the flag does.
func spentProbe(id string, exit int, ago int64, reading ...string) string {
	now := time.Now().Unix()
	budget := ""
	if len(reading) > 0 {
		budget = reading[0]
	}
	return fmt.Sprintf(`MSG %d %s.md\nDRIVERS 0\nAGENT idle\nBUDGET %s\nSPENT %d\nTURNEND %d %d spent - token budget spent: output 5.0M of 5M\n`,
		now-5400, id, budget, now-ago, now-ago, exit)
}

var spentOrch = model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"}}

var spentHostPolicy = protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1}

// Once the wait is over, a refused orchestrator is tried again in its own
// session, on a resume that names the message its refused turn was given.
func TestResumeTriesASpentOrchestratorAgainInItsSession(t *testing.T) {
	a, calls, payloads, remote := handoffApp(t, spentProbe("m1", 5, 1900), spentOrch)

	said, err := a.hostResumeOrchestrator(context.Background(), spentOrch, spentHostPolicy)
	if err != nil {
		t.Fatalf("a due try is the host's move: %v", err)
	}
	if said != "m1 tried again after the orchestrator's token budget was spent, no rung spent" {
		t.Errorf("said %q", said)
	}
	if sent := readOr(t, calls); !strings.Contains(sent, "m1.001.resume.md") {
		t.Fatalf("the try is the mission's next resume message; the host wrote:\n%s", sent)
	}
	if body := decoded(t, payloads); body != protocol.SpentResumeBody("m1", "m1.md") {
		t.Errorf("the resume must name the message the refused turn was given:\n%s", body)
	}
	ran := readOr(t, remote)
	if strings.Contains(ran, "--kill") || strings.Contains(ran, "-forget") || !strings.Contains(ran, "--resume camp-orch") {
		t.Errorf("a refused turn's session is carried on, never ended:\n%s", ran)
	}
}

// Before the try is due, resume refuses, says why, and sends nothing. Calling
// it early cannot bring the try forward.
func TestResumeRefusesASpentOrchestratorBeforeItsTry(t *testing.T) {
	a, calls, payloads, remote := handoffApp(t, spentProbe("m1", 5, 60), spentOrch)

	_, err := a.hostResumeOrchestrator(context.Background(), spentOrch, spentHostPolicy)
	if err == nil || !strings.Contains(err.Error(), "node-refused") || !strings.Contains(err.Error(), "token budget spent (output)") ||
		!strings.Contains(err.Error(), "the next try is not due yet, and nothing was sent") {
		t.Fatalf("resume must refuse a hold, naming it: %v", err)
	}
	if s := readOr(t, calls) + readOr(t, payloads) + readOr(t, remote); s != "" {
		t.Fatalf("the host sent something into a hold:\n%s", s)
	}
}

// An orchestrator that handed off at its budget's line is started fresh on its
// handoff once the wait is over, as after a hand-off at its context line.
func TestResumeStartsASpentHandedOffOrchestratorFresh(t *testing.T) {
	a, calls, _, remote := handoffApp(t, spentProbe("m1", 0, 1900), spentOrch)

	said, err := a.hostResumeOrchestrator(context.Background(), spentOrch, spentHostPolicy)
	if err != nil {
		t.Fatalf("a due hand-off is the host's move: %v", err)
	}
	if !strings.HasPrefix(said, "ended the orchestrator's session; starting it fresh on its handoff") {
		t.Errorf("said %q", said)
	}
	if sent := readOr(t, calls); !strings.Contains(sent, "m1.001.handoff.md") {
		t.Fatalf("the fresh session must read m1.001.handoff.md; the host wrote:\n%s", sent)
	}
	assertFreshSession(t, remote, spentOrch)
}

// At create, a member whose budget is spent is waited for out loud, once, in
// words that do not blame its provider, and nothing is sent into the hold.
func TestAwaitReplyHoldsASpentMemberAndSaysSo(t *testing.T) {
	dev := model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g", Session: model.Session{Name: "camp-dev"}}
	a, calls := probeOnlyApp(t, spentProbe("d001", 5, 5))

	var out strings.Builder
	_, err := a.awaitReply(context.Background(), &out, dev, "d001", protocol.Policy{PollSeconds: 1, SettlingSeconds: 1}, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "node-refused") || !strings.Contains(err.Error(), "token budget spent") {
		t.Fatalf("the bound should end this wait, naming the spent budget; got: %v", err)
	}
	if n := strings.Count(out.String(), "its token budget is spent, and only the operator can raise it — waiting"); n != 1 {
		t.Fatalf("a hold is said once, not once per look; said %d times:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), "provider") {
		t.Errorf("a spent budget is not the provider's doing:\n%s", out.String())
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Fatalf("the host sent something into a hold:\n%s", b)
	}
}

// The operator raised the budget a minute after the turn was refused. The
// reading says a turn would be sent, so resume tries at once, not half an
// hour later, and a node that handed off starts fresh at once.
func TestResumeTriesASpentOrchestratorAtOnceWhenItsBudgetHasRoom(t *testing.T) {
	a, calls, payloads, _ := handoffApp(t, spentProbe("m1", 5, 60, "ok"), spentOrch)
	said, err := a.hostResumeOrchestrator(context.Background(), spentOrch, spentHostPolicy)
	if err != nil || said != "m1 tried again after the orchestrator's token budget was spent, no rung spent" {
		t.Fatalf("a reading with room makes the try due now: %q, %v", said, err)
	}
	if !strings.Contains(readOr(t, calls), "m1.001.resume.md") || decoded(t, payloads) != protocol.SpentResumeBody("m1", "m1.md") {
		t.Errorf("the try is the spent resume:\n%s", readOr(t, calls))
	}

	a, calls, _, remote := handoffApp(t, spentProbe("m1", 0, 60, "ok"), spentOrch)
	said, err = a.hostResumeOrchestrator(context.Background(), spentOrch, spentHostPolicy)
	if err != nil || !strings.HasPrefix(said, "ended the orchestrator's session; starting it fresh on its handoff") {
		t.Fatalf("a node that handed off starts fresh at once: %q, %v", said, err)
	}
	if !strings.Contains(readOr(t, calls), "m1.001.handoff.md") {
		t.Errorf("the fresh session reads m1.001.handoff.md:\n%s", readOr(t, calls))
	}
	assertFreshSession(t, remote, spentOrch)
}

// While the reading still asks, the top-up has not landed. resume refuses,
// says which types still ask, and sends nothing. A driver too old to give a
// reading leaves the wait as it was, and resume says only that the try is not
// due.
func TestResumeSaysWhichTypesStillAsk(t *testing.T) {
	for reading, want := range map[string]string{
		"ask cached,output": "the budget has not been raised: cached, output still at the ask line on the latest reading, and nothing was sent",
		"none":              "the next try is not due yet, and nothing was sent",
		"off":               "the next try is not due yet, and nothing was sent",
		"":                  "the next try is not due yet, and nothing was sent",
	} {
		a, calls, payloads, remote := handoffApp(t, spentProbe("m1", 5, 60, reading), spentOrch)
		_, err := a.hostResumeOrchestrator(context.Background(), spentOrch, spentHostPolicy)
		if err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("reading %q: want a refusal ending %q, got %v", reading, want, err)
		}
		if s := readOr(t, calls) + readOr(t, payloads) + readOr(t, remote); s != "" {
			t.Errorf("reading %q: the host sent something into a hold:\n%s", reading, s)
		}
	}
}
