package cli

// Host observe and the "machine gone" conclusion. observe used to pass a
// constant blindRun=1 — always below BlindProbes — so a truly gone machine
// rendered forever as the node-unreachable overlay and could never reach the
// §2.3 "machine gone" line. The scoped burst re-probes only the failing node,
// within the one invocation, storing nothing.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/codesweep-ai/campaign/internal/store"
)

// burstApp builds an app over a fake sandbox whose member exec fails the
// first failN probes (counted in cntFile) and serves an empty channel after.
// The orchestrator-log read is not a probe and always just fails (tolerated).
func burstApp(t *testing.T, failN int) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	cntFile := filepath.Join(dir, "probes")
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  exec)
    case "$5" in *log.jsonl*) exit 1 ;; esac
    n=$(cat "$CNT_FILE" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$CNT_FILE"
    [ "$n" -le "$FAIL_N" ] && exit 1
    echo "DRIVERS 0"
    ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CNT_FILE", cntFile)
	t.Setenv("FAIL_N", strconv.Itoa(failN))
	restore := observeBurstDelay
	observeBurstDelay = 0
	t.Cleanup(func() { observeBurstDelay = restore })
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}, cntFile
}

func burstCampaign() *model.Campaign {
	return &model.Campaign{
		Name:   "gone",
		Policy: protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1000, BlindProbes: 3, SettlingSeconds: 100},
		Members: []model.Member{
			{Name: "orch", Role: "orchestrator", CLI: "codex", Sandbox: "orch", Ref: "orch.gone"},
		},
	}
}

func probeCount(t *testing.T, cntFile string) int {
	t.Helper()
	b, err := os.ReadFile(cntFile)
	if err != nil {
		t.Fatalf("no probe was recorded: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A machine that answers no probe at all must reach the conclusion — the
// burst accumulates the consecutive-failure run inside the one observe call.
func TestObserveConcludesMachineGone(t *testing.T) {
	a, cntFile := burstApp(t, 1000) // never recovers
	obs, err := a.observeCampaign(context.Background(), burstCampaign())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(obs.Derived) != 1 {
		t.Fatalf("derived rows: %+v", obs.Derived)
	}
	row := obs.Derived[0]
	if row.State != string(protocol.StateStuck) || !strings.Contains(row.Detail, "machine gone") {
		t.Fatalf("a gone machine must render as the conclusion, got %s (%s)", row.State, row.Detail)
	}
	if n := probeCount(t, cntFile); n != 3 {
		t.Fatalf("the burst must stop at BlindProbes consecutive failures, probed %d times", n)
	}
}

// A transient failure must NOT read as gone: the burst re-probes and the
// first success ends it with an ordinary computed state.
func TestObserveBurstResolvesTransientFailure(t *testing.T) {
	a, cntFile := burstApp(t, 2) // fails twice, then answers
	obs, err := a.observeCampaign(context.Background(), burstCampaign())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	row := obs.Derived[0]
	if row.State != string(protocol.StateFree) {
		t.Fatalf("a recovered probe must yield the node's real state, got %s (%s)", row.State, row.Detail)
	}
	if n := probeCount(t, cntFile); n != 3 {
		t.Fatalf("the burst must end on the first success, probed %d times", n)
	}
}

// The healthy path pays nothing: one probe, no burst, no delay.
func TestObserveHealthyNodeProbedOnce(t *testing.T) {
	a, cntFile := burstApp(t, 0)
	observeBurstDelay = time.Hour // a burst would hang the test
	obs, err := a.observeCampaign(context.Background(), burstCampaign())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Derived[0].State != string(protocol.StateFree) {
		t.Fatalf("healthy: %+v", obs.Derived[0])
	}
	if n := probeCount(t, cntFile); n != 1 {
		t.Fatalf("a healthy node must be probed exactly once, probed %d times", n)
	}
}

// repliedApp builds an app over a fake sandbox whose orchestrator reports a
// reply to the mission, and answers the reply read with replyBody.
func repliedApp(t *testing.T, replyBody string) *app {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  exec)
    case "$5" in
      *log.jsonl*) exit 1 ;;
      *m1.json*) printf '%s' "$REPLY_BODY" ;;
      *) printf 'MSG 100 m1.md\nREPLY m1\nDRIVERS 0\n' ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPLY_BODY", replyBody)
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
}

// The verdict a readable reply carries reaches the caller.
func TestObserveReadsTheMissionVerdict(t *testing.T) {
	a := repliedApp(t, `{"outcome":"campaign-met","note":"all three satisfied"}`)
	obs, err := a.observeCampaign(context.Background(), burstCampaign())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Mission == nil {
		t.Fatalf("a readable reply must yield the verdict; err=%q rows=%+v", obs.MissionErr, obs.Derived)
	}
	if obs.Mission.Outcome != "campaign-met" {
		t.Fatalf("outcome = %q", obs.Mission.Outcome)
	}
	if obs.MissionErr != "" {
		t.Fatalf("a verdict that was read must report no error, got %q", obs.MissionErr)
	}
}

// A reply that exists but cannot be parsed must be reported, not swallowed.
// Discarding the error left an unreadable verdict indistinguishable from an
// unfinished campaign, so an observer waited out its whole ceiling on a
// campaign that had already concluded.
func TestObserveReportsAnUnreadableMissionReply(t *testing.T) {
	a := repliedApp(t, "not json at all")
	obs, err := a.observeCampaign(context.Background(), burstCampaign())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Mission != nil {
		t.Fatalf("an unparseable reply is not a verdict: %+v", obs.Mission)
	}
	if !strings.Contains(obs.MissionErr, "not valid JSON") {
		t.Fatalf("the reason must reach the observer, got %q", obs.MissionErr)
	}
	var out strings.Builder
	printObservation(&out, obs)
	if !strings.Contains(out.String(), "present, but unreadable") {
		t.Fatalf("the rendered observation must say so:\n%s", out.String())
	}
}
