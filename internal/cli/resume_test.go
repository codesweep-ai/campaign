package cli

// The host's one no-rung instrument for the orchestrator. The orchestrator
// resumes a refused agent from its wait loop; nothing above the orchestrator
// did the same for it, so a refused orchestrator sat until a person sent a
// continue that spent a rung (SAC-054). `resume` performs the move the state
// computation derives, and refuses every other move by name.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// resumeWorld fakes a campaign whose orchestrator's probe answers with the
// given lines. It returns the app, the file where the fake sandbox records
// every non-probe command, and the file where a stubbed claude remote tool
// records the turn it was asked to start. The orchestrator's session markers
// exist, so a turn the host starts must resume that session, never open one.
func resumeWorld(t *testing.T, probe string) (*app, string, string) {
	t.Helper()
	a, calls := probeOnlyApp(t, probe)
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".cs-claude-remote-sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"camp-orch", "camp-orch.token"} {
		if err := os.WriteFile(filepath.Join(sessions, f), []byte("live\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	turns := filepath.Join(bin, "turns")
	stub := "#!/bin/sh\necho \"$@\" >> " + turns + "\n"
	if err := os.WriteFile(filepath.Join(bin, "cs-claude-remote"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	// Only the stub's directory and the system's: a real remote tool may be installed.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	campaign := &model.Campaign{Name: "camp", Group: "camp-grp",
		Policy: protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1},
		Members: []model.Member{
			{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.camp-grp", Session: model.Session{Name: "camp-orch"}},
			{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "boxA", Ref: "dev.camp-grp", Session: model.Session{Name: "camp-dev"}},
		}}
	if err := a.store.Save(campaign); err != nil {
		t.Fatal(err)
	}
	return a, calls, turns
}

func runResume(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	cmd := a.root()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"resume"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func readOr(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// The provider refused the orchestrator's mission turn and the wait is over:
// one resume goes into the still-open mission, on the same session, and no
// rung is spent. This is the move `observe` derived on every look for three
// hours while nothing performed it.
func TestResumeCarriesARefusedOrchestratorOn(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 5 capacity - turn failed: API Error: 529 Overloaded\n`, now-700, now-660)
	a, calls, turns := resumeWorld(t, probe)

	out, err := runResume(t, a, "camp")
	if err != nil {
		t.Fatalf("resume must perform the derived move; got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "m1") || !strings.Contains(out, "resumed") || !strings.Contains(out, "no rung spent") {
		t.Fatalf("the host must say which dispatch it resumed and that no rung was spent; said:\n%s", out)
	}
	sent := readOr(t, calls)
	if !strings.Contains(sent, "m1.001.resume.md") {
		t.Fatalf("the resume must be the next resume message of the open mission, m1.001.resume.md; the host wrote:\n%s", sent)
	}
	if strings.Contains(sent, "m1.001.md") || strings.Contains(sent, "m2") {
		t.Fatalf("a resume must not continue the dispatch or open a new one:\n%s", sent)
	}
	turn := readOr(t, turns)
	if !strings.Contains(turn, "--resume camp-orch") || strings.Contains(turn, "--new") {
		t.Fatalf("a resume carries the same session on; the turn was started with:\n%s", turn)
	}
	if !strings.Contains(turn, "Dispatch ID: m1") {
		t.Fatalf("the turn must be anchored to the open mission:\n%s", turn)
	}
}

// The turn for the newest message never ran: the same instrument, and the
// message says why, because the two cases read differently to the model.
func TestResumeCarriesOnAnOrchestratorWhoseTurnNeverRan(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 0 - - -\n`, now-300, now-900)
	a, calls, _ := resumeWorld(t, probe)

	out, err := runResume(t, a, "camp")
	if err != nil {
		t.Fatalf("a turn that never ran is resumed for free; got: %v\n%s", err, out)
	}
	if !strings.Contains(readOr(t, calls), "m1.001.resume.md") {
		t.Fatalf("the host must write the mission's resume message:\n%s", readOr(t, calls))
	}
}

// No resume before the provider's wait has run. The command refuses, says how
// long is left, and sends nothing: a script that calls it early cannot make
// the load that was refused come back early.
func TestResumeRefusesWhileTheProviderWaitRuns(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 5 throttled 600 turn failed: Rate limit reached\n`, now-20, now-5)
	a, calls, turns := resumeWorld(t, probe)

	out, err := runResume(t, a, "camp")
	if err == nil || !strings.Contains(err.Error(), "resuming in") {
		t.Fatalf("the command must refuse and name the wait that is still running; got: %v\n%s", err, out)
	}
	if s := readOr(t, calls) + readOr(t, turns); s != "" {
		t.Fatalf("the host sent something into a wait the provider asked for:\n%s", s)
	}
}

// Past providerWaitSeconds the refusal is a judgment, not a move. The command
// refuses, names the bound, and sends nothing; ending the campaign is not its
// job.
func TestResumeRefusesOnceTheProviderWaitBoundTripped(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 5 capacity - turn failed: API Error: 529 Overloaded\n`, now-5000, now-4000)
	a, calls, turns := resumeWorld(t, probe)

	out, err := runResume(t, a, "camp")
	if err == nil || !strings.Contains(err.Error(), "node-stuck") || !strings.Contains(err.Error(), "provider wait bound tripped") {
		t.Fatalf("the command must refuse a stuck orchestrator naming the bound; got: %v\n%s", err, out)
	}
	if s := readOr(t, calls) + readOr(t, turns); s != "" {
		t.Fatalf("the host acted on a node whose wait bound has tripped:\n%s", s)
	}
}

// A stopped orchestrator whose next move is a continue is a person's decision
// (PROTOCOL.md section 8). The command refuses and names `send`, so a loop
// built on it can never spend a rung by accident.
func TestResumeIsNeverAContinue(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 1 other - turn failed: the model ended its turn without replying\n`, now-700, now-660)
	a, calls, turns := resumeWorld(t, probe)

	out, err := runResume(t, a, "camp")
	if err == nil || !strings.Contains(err.Error(), "node-stopped") || !strings.Contains(err.Error(), "send") {
		t.Fatalf("a continue is not the host's mechanical move; the refusal must say so and name send; got: %v\n%s", err, out)
	}
	if s := readOr(t, calls) + readOr(t, turns); s != "" {
		t.Fatalf("the host continued the orchestrator under the name of a resume:\n%s", s)
	}
}

// The host resumes the orchestrator alone. An agent's refusal belongs to the
// orchestrator's wait loop, and two resumers of one node would race.
func TestResumeAddressesTheOrchestratorAlone(t *testing.T) {
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d d003.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 5 throttled - turn failed: Rate limit reached\n`, now-700, now-660)
	a, calls, turns := resumeWorld(t, probe)

	out, err := runResume(t, a, "camp/dev")
	if err == nil || !strings.Contains(err.Error(), "orchestrator") {
		t.Fatalf("resuming an agent must be refused, naming whose ladder it is; got: %v\n%s", err, out)
	}
	if s := readOr(t, calls) + readOr(t, turns); s != "" {
		t.Fatalf("the host resumed an agent:\n%s", s)
	}
}
