//go:build smoke

package cli

// The smoke tier: the same campaigns the integration tier runs, with the model
// traffic served from committed cassettes instead of providers.
//
// It boots real virtual machines, so it needs a host with KVM — but it holds no
// credential, reaches no provider, and costs nothing. That is what makes it the
// tier CI runs on every push: the whole dispatch protocol end to end, with the
// one part that costs money replayed.
//
//	make test-smoke
//
// A replay reproduces the model's decisions, not the world's facts. The
// orchestrator's judgement is replayed rather than re-derived, so this proves
// the harness carried a campaign to its verdict. It does not verify the work
// the campaign delivered.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"
)

// fakeKey is what a replayed fleet authenticates with. It says of itself that
// it is fake, which is what lets the leak scan stay strict.
const fakeKey = "not-a-real-token-replay-only"

// TestSmokeReplay replays every scenario that has been recorded.
//
// Serial: each member is a real machine, and the scenarios share one host's
// memory, one fabric address range and one pool of gateway ports.
func TestSmokeReplay(t *testing.T) {
	recorded := 0
	for _, sc := range scenarios() {
		if !hasCassette(t, sc) {
			continue
		}
		recorded++
		t.Run(sc.name, func(t *testing.T) {
			store := cassetteStore(t, sc)
			assertCassetteRuleset(t, sc, store)
			assertRecordingFinished(t, sc, store)
			run := runLiveCampaign(t, sc, runOptions{
				baseURL:    vcrBaseURL,
				ceiling:    20 * time.Minute,
				proxyMode:  "replay",
				proxyStore: store,
				fakeKey:    fakeKey,
				fixedName:  replayName(sc),
			})

			if run.verdict.Outcome != "campaign-met" {
				t.Fatalf("the recorded campaign was met, so its replay must be too; got %s: %s",
					run.verdict.Outcome, run.verdict.Note)
			}
			assertSpentNothing(t, run.proxy)
			proveCampaignBehaviours(t, sc.cli, covmap.TierSmoke)
		})
	}
	if recorded == 0 {
		t.Skip("no cassette under test/cassettes: record one with `make fixtures`")
	}
}

// assertSpentNothing is the assertion that makes this tier worth running. A
// replay that quietly fell through to a provider would pass every other check
// here, and cost money on every push.
//
// Two independent statements, because either alone can be true by accident.
// cs-vcr says at startup that this session will contact no provider, which is a
// property of the mode it was started in; and it accounts for the upstream
// calls it made when it shuts down, which is what actually happened. A run that
// spent nothing shows both.
func assertSpentNothing(t *testing.T, proxy *vcrProxy) {
	t.Helper()
	summary := proxy.logs()
	const promise = "no provider will be contacted this session"
	if !strings.Contains(summary, promise) {
		t.Logf("cs-vcr log:\n%s", tail(summary, 40))
		t.Fatalf("cs-vcr never said %q, so this was not a replay session", promise)
	}
	calls, err := upstreamCalls(summary)
	if err != nil {
		t.Logf("cs-vcr log:\n%s", tail(summary, 40))
		t.Fatalf("cs-vcr printed no upstream accounting, so the replay cannot be shown to have spent nothing: %v", err)
	}
	if calls != 0 {
		t.Logf("cs-vcr log:\n%s", tail(summary, 40))
		t.Fatalf("replay made %d upstream call(s): a cassette miss fell through to a real provider", calls)
	}
}

// upstreamCalls reads the count out of cs-vcr's shutdown accounting, whose line
// is `upstream calls\t0`.
func upstreamCalls(summary string) (int, error) {
	for line := range strings.SplitSeq(summary, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "upstream calls")
		if !ok {
			continue
		}
		return strconv.Atoi(strings.TrimSpace(rest))
	}
	return 0, errors.New("no `upstream calls` line in the log")
}

// assertCassetteRuleset fails a scenario whose cassettes were recorded under a
// normalization ruleset this cs-vcr build no longer applies.
//
// Worth its one process, because the failure it replaces is the worst shape a
// test failure comes in. A cassette keyed under a superseded ruleset does not
// miss a few entries: the keys mean something else now, so every model call
// misses at once. The members then sit through the protocol's entire ladder
// waiting for turns that can never arrive — continue, restart, and finally the
// readback bound fifteen minutes later — while stdout says nothing about why.
// cs-vcr can answer the question outright, in one call that boots no machine
// and contacts no provider, so it is asked before anything is provisioned.
//
// A hard failure rather than a skip. A cassette that cannot be replayed is a
// broken fixture, not an absent one, and this is the tier CI runs on every
// push: skipping would let the fixtures rot silently, which is exactly how
// they got here.
func assertCassetteRuleset(t *testing.T, sc scenario, store string) {
	t.Helper()
	bin, err := exec.LookPath("cs-vcr")
	if err != nil {
		return // the run skips on the missing binary, and that path owns the message
	}
	members, err := filepath.Glob(filepath.Join(store, "*", "index.jsonl"))
	if err != nil || len(members) == 0 {
		return
	}
	config := filepath.Join(t.TempDir(), "verify.yaml")
	if err = os.WriteFile(config, []byte("cassettes: "+store+"\n"), 0o600); err != nil {
		t.Fatalf("write the cs-vcr verify config: %v", err)
	}
	args := []string{"cassette", "verify", "--config", config}
	for _, member := range members {
		args = append(args, filepath.Base(filepath.Dir(member)))
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err == nil {
		return
	}
	t.Fatalf("this cs-vcr build cannot replay the committed cassette:\n%s\nre-record it with `make fixtures FIXTURE_TESTS='TestLiveRecordsACassette/%s'`",
		strings.TrimSpace(string(out)), sc.name)
}

// assertRecordingFinished fails a scenario whose cassette came out of a
// recording that never reached a verdict.
//
// The ruleset check above cannot see this. cs-vcr writes entries as it serves
// them, so a recording cut short — a credential that ran out, a machine that
// died, an interrupt — leaves a cassette that is well-formed and correctly
// keyed and simply stops early. Measured: an Anthropic key ran out of credit
// forty requests into claude-api-key, and the truncated result passed the
// ruleset gate without a word, then cost a full replay to discover.
//
// The recording writes recorded.json before it starts and settles it only once
// the campaign has met its mission, so an unsettled claim is proof rather than
// inference. A cassette with no claim at all predates the file and is left
// alone — refusing those would fail on fixtures that replay perfectly well.
func assertRecordingFinished(t *testing.T, sc scenario, store string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(store, "recorded.json"))
	if err != nil {
		return // recorded before completeness was tracked
	}
	var claim struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &claim); err != nil {
		t.Fatalf("unreadable recorded.json for %s: %v", sc.name, err)
	}
	if claim.Outcome == "campaign-met" {
		return
	}
	t.Fatalf("this cassette came out of a recording that ended %q, and the replay below asserts campaign-met\nre-record it with `make fixtures FIXTURE_TESTS='TestLiveRecordsACassette/%s'`",
		claim.Outcome, sc.name)
}

// hasCassette reports whether this scenario has been recorded. A scenario with
// no cassette is skipped rather than failed: a recording that was never made
// cannot be said to have a broken replay.
func hasCassette(t *testing.T, sc scenario) bool {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(cassetteStore(t, sc), "*", "index.jsonl"))
	return err == nil && len(entries) > 0
}
