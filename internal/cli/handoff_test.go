package cli

// A seat with a context line hands off when its context crosses it, and the
// host starts it fresh on its own handoff. The line is declared per seat,
// reaches the member's machine as create-time environment, and is explained
// to the member by its orientation. A seat with no line gets none of it.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/codesweep-ai/campaign/internal/store"
)

// A member may declare its own line, as it declares its own stall threshold.
// A negative line, or the campaign's bound or its window declared on one seat,
// is refused at validation rather than written where nothing reads it.
func TestAMemberPolicyMayCarryAHandoffLine(t *testing.T) {
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := p.Agents["worker"]
	worker.Policy.HandoffTokens = 60000
	p.Agents["worker"] = worker
	if err = validateProfile(p); err != nil {
		t.Fatalf("a member's own line is a number it may set: %v", err)
	}

	worker.Policy.HandoffTokens = -1
	p.Agents["worker"] = worker
	if err = validateProfile(p); err == nil || !strings.Contains(err.Error(), "policy.handoffTokens") {
		t.Fatalf("a negative line must be refused by name: %v", err)
	}

	worker.Policy.HandoffTokens = 60000
	worker.Policy.Handoffs = 3
	p.Agents["worker"] = worker
	err = validateProfile(p)
	if err == nil {
		t.Fatal("a per-member handoffs bound must be refused: the campaign's bound is the one computed against")
	}
	for _, want := range []string{"policy.handoffs", "defaults.policy", "stallSeconds, handoffTokens and freshPerDispatch alone"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	// The window that makes a hand-off rapid is the same bound's, and is
	// refused on a seat by name too.
	worker.Policy.Handoffs = 0
	worker.Policy.RapidHandoffSeconds = 120
	p.Agents["worker"] = worker
	err = validateProfile(p)
	if err == nil {
		t.Fatal("a per-member rapidHandoffSeconds must be refused: the campaign's window is the one computed against")
	}
	for _, want := range []string{"policy.rapidHandoffSeconds", "defaults.policy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// The member's own line wins over the campaign's, and a seat neither names
// has none: there is no compiled-in line.
func TestTheHandoffLineResolvesPerSeat(t *testing.T) {
	var p model.Profile
	p.Defaults.Policy.HandoffTokens = 80000
	if got := resolveHandoff(p, model.MemberProfile{Policy: protocol.Policy{HandoffTokens: 60000}}); got != 60000 {
		t.Errorf("the member's own line = %d, want 60000", got)
	}
	if got := resolveHandoff(p, model.MemberProfile{}); got != 80000 {
		t.Errorf("a member with no line of its own takes the campaign's: %d, want 80000", got)
	}
	if got := resolveHandoff(model.Profile{}, model.MemberProfile{}); got != 0 {
		t.Errorf("a seat no profile gives a line has none: %d", got)
	}

	// And the campaign records each seat's resolved line.
	prof, err := profileFromFlags("codex", []string{"worker=codex", "other=codex"}, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := prof.Agents["worker"]
	worker.Policy.HandoffTokens = 60000
	prof.Agents["worker"] = worker
	c := buildCampaign("acme", prof, "", "", time.Unix(1_800_000_000, 0))
	lines := map[string]int{}
	for _, m := range c.Members {
		lines[m.Name] = m.HandoffTokens
	}
	if lines["worker"] != 60000 || lines["other"] != 0 || lines["orchestrator"] != 0 {
		t.Errorf("recorded lines %v, want worker 60000 and none elsewhere", lines)
	}
}

// freshPerDispatch is an agent's: its own, else the campaign's default. The
// orchestrator never has it, its one dispatch being the mission, and a profile
// that gives it one is refused rather than written where nothing reads it.
func TestFreshPerDispatchResolvesForAgentsAlone(t *testing.T) {
	p, err := profileFromFlags("codex", []string{"worker=codex", "other=codex"}, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := p.Agents["worker"]
	worker.Policy.FreshPerDispatch = true
	p.Agents["worker"] = worker
	resolved := func() map[string]bool {
		t.Helper()
		if err := validateProfile(p); err != nil {
			t.Fatalf("an agent's policy and defaults.policy may carry it: %v", err)
		}
		got := map[string]bool{}
		for _, m := range buildCampaign("acme", p, "", "", time.Unix(1_800_000_000, 0)).Members {
			got[m.Name] = m.FreshPerDispatch
		}
		return got
	}
	if got := resolved(); !got["worker"] || got["other"] || got["orchestrator"] {
		t.Errorf("from the agent's own policy: %v, want worker alone", got)
	}
	p.Defaults.Policy.FreshPerDispatch = true
	if got := resolved(); !got["worker"] || !got["other"] || got["orchestrator"] {
		t.Errorf("from defaults.policy: %v, want every agent and not the orchestrator", got)
	}

	p.Orchestrator.Policy.FreshPerDispatch = true
	err = validateProfile(p)
	if err == nil {
		t.Fatal("an orchestrator's freshPerDispatch must be refused: nothing would ever read it")
	}
	for _, want := range []string{"orchestrator: policy.freshPerDispatch", "set it on an agent, or under defaults.policy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// The line reaches the member's machine as create-time environment, with the
// file the handoff goes to and the sentence that asks for it. Claude Code also
// has its background tasks turned off, since a background sub-agent ends with
// the session a hand-off replaces.
func TestTheHandoffLineReachesTheMachine(t *testing.T) {
	c := &model.Campaign{Engine: "firecracker", Group: "grp", Network: groupNetwork("grp")}
	claude := model.Member{Sandbox: "box", CLI: "claude", StallSeconds: 180, HandoffTokens: 60000}
	want := []string{
		"--env", "CS_CLAUDE_STALL_SECS=180",
		"--env", "CS_HANDOFF_TOKENS=60000",
		"--env", "CS_HANDOFF_FILE=~/.local/share/cs-campaign/output/handoff.md",
		"--env", "CS_HANDOFF_ASK=" + handoffAsk,
		"--env", "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1",
	}
	if got := createArgs(c, claude); !containsSeq(got, want) {
		t.Fatalf("got %#v\nwant, in order, %#v", got, want)
	}
	if "~/"+protocol.HandoffFile != "~/.local/share/cs-campaign/output/handoff.md" {
		t.Fatalf("the handoff file moved to %s; the member's machine is told the old path", protocol.HandoffFile)
	}

	codex := model.Member{Sandbox: "box", CLI: "codex", StallSeconds: 180, HandoffTokens: 60000}
	got := createArgs(c, codex)
	if !containsSeq(got, []string{"--env", "CS_CODEX_STALL_SECS=180", "--env", "CS_HANDOFF_TOKENS=60000",
		"--env", "CS_HANDOFF_FILE=~/" + protocol.HandoffFile, "--env", "CS_HANDOFF_ASK=" + handoffAsk}) {
		t.Fatalf("codex gets the line, the file and the ask: %#v", got)
	}
	if slices.Contains(got, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1") {
		t.Errorf("a Claude Code variable reached a codex member: %#v", got)
	}

	// A seat with no line runs as it always has.
	for _, cli := range []string{"claude", "codex", "opencode"} {
		for _, a := range createArgs(c, model.Member{Sandbox: "box", CLI: cli, StallSeconds: 180}) {
			if strings.Contains(a, "HANDOFF") || strings.Contains(a, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS") {
				t.Errorf("a %s seat with no line got %q", cli, a)
			}
		}
	}
}

// The ask states no number: neither the context nor the line, nor a
// placeholder for the hook to fill with one. A member told how far it is from
// the line rations its context against it, and cuts work short to stay under.
func TestTheAskStatesNoNumber(t *testing.T) {
	c := &model.Campaign{Engine: "firecracker", Group: "grp", Network: groupNetwork("grp")}
	found := false
	for _, a := range createArgs(c, model.Member{Sandbox: "box", CLI: "claude", StallSeconds: 180, HandoffTokens: 60000}) {
		if ask, ok := strings.CutPrefix(a, "CS_HANDOFF_ASK="); ok {
			found = true
			if strings.ContainsAny(ask, "0123456789{}") {
				t.Errorf("the ask states a number, or a place for one: %q", ask)
			}
		}
	}
	if !found {
		t.Fatal("a seat with a line was given no ask")
	}
}

// The ask names a heading of the orientation, and the member finds what to do
// under it. The two are written in different files, so this holds them
// together.
func TestTheAskNamesAnOrientationHeading(t *testing.T) {
	const heading = "When you are asked to hand off"
	if !strings.Contains(handoffAsk, "under the heading "+heading) {
		t.Fatalf("the ask does not name the heading %q: %s", heading, handoffAsk)
	}
	text := renderOrientation(t, "dev", 60000)
	if !strings.Contains(text, "## "+heading+"\n") {
		t.Fatalf("the orientation has no heading %q:\n%s", heading, text)
	}
}

// renderOrientation renders one member's orientation in a two-seat campaign,
// with the given seat's line set.
func renderOrientation(t *testing.T, who string, line int) string {
	t.Helper()
	return renderSeat(t, who, func(m *model.Member) { m.HandoffTokens = line })
}

// renderSeat renders one member's orientation in a two-seat campaign, after
// set has changed that seat.
func renderSeat(t *testing.T, who string, set func(*model.Member)) string {
	t.Helper()
	campaign := &model.Campaign{Name: "acme", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "claude"},
		{Name: "dev", Role: "agent", CLI: "claude"},
	}}
	for i := range campaign.Members {
		if campaign.Members[i].Name == who {
			set(&campaign.Members[i])
			text, _, err := buildOrientation(campaign, campaign.Members[i], campaignInputs{})
			if err != nil {
				t.Fatal(err)
			}
			return text
		}
	}
	t.Fatalf("no member %s", who)
	return ""
}

// fold joins a text's lines, so a sentence the template wraps reads whole.
func fold(text string) string { return strings.Join(strings.Fields(text), " ") }

// A seat with a line is told how to work so a hand-off loses nothing, what to
// write when asked, and how to start again. A seat with none reads what it
// always read: none of the three sections, and no mention of a hand-off. The
// orientation tests that pin the rest of the text hold the bytes.
//
// No orientation states the line. A member that knows how far it is from the
// line rations its context against it, and is told instead never to.
func TestTheOrientationExplainsTheLineOnlyToASeatWithOne(t *testing.T) {
	headings := []string{"## Long work", "## When you are asked to hand off", "## When you start fresh"}
	// A line no other number in the text could be mistaken for, and the ways
	// a template could spell it.
	const line = 73519
	spelled := []string{"73519", "73,519", "73 519", "73k", "74k", "73.5k"}
	// What a lined seat must read: never ration, never a second session,
	// commit first and name each commit, and on the fresh side read only
	// what the handoff names, and trust git.
	sentences := []string{
		"Until then, never ration your context, and never cut work short to save it.",
		"Never start a second CLI session yourself, with a `cs-*-remote` tool or any other way",
		"Stop the task at that point, and commit your work on your branch.",
		"the commit each repository you changed is at, by its hash",
		"The harness checks that your work is committed and that the handoff names each commit.",
		"Beyond those, read only what the handoff names as still to read",
		"Where the handoff and git disagree, trust git.",
	}
	for _, who := range []string{"orchestrator", "dev"} {
		without := renderOrientation(t, who, 0)
		for _, h := range append(headings, "hand off", "handoff", "hand-off", "fresh session") {
			if strings.Contains(without, h) {
				t.Errorf("%s has no line and its orientation says %q:\n%s", who, h, without)
			}
		}

		with := renderOrientation(t, who, line)
		for _, n := range spelled {
			if strings.Contains(with, n) {
				t.Errorf("%s's orientation states its line as %q:\n%s", who, n, with)
			}
		}
		for _, want := range append(headings, "~/"+protocol.HandoffFile,
			"setsid nohup bash -c '<command>' > <file> 2>&1 < /dev/null &") {
			if !strings.Contains(with, want) {
				t.Errorf("%s has a line and its orientation does not say %q:\n%s", who, want, with)
			}
		}
		for _, want := range sentences {
			if !strings.Contains(fold(with), want) {
				t.Errorf("%s has a line and its orientation does not say %q:\n%s", who, want, with)
			}
		}
		// The sections sit together, in the order a member meets them.
		at := []int{}
		for _, h := range headings {
			at = append(at, strings.Index(with, h))
		}
		if !slices.IsSorted(at) {
			t.Errorf("%s: the sections are out of order: %v", who, at)
		}
	}
	// The orchestrator's state of the team lives in its log, so its handoff
	// and its fresh start both name the log; an agent keeps none.
	if orch := renderOrientation(t, "orchestrator", 60000); !strings.Contains(orch, "~/"+protocol.LogFile) || !strings.Contains(orch, "your log,") {
		t.Errorf("the orchestrator's orientation does not point it at its log:\n%s", orch)
	}
	if dev := renderOrientation(t, "dev", 60000); strings.Contains(dev, protocol.LogFile) || strings.Contains(dev, "your log") {
		t.Errorf("an agent's orientation names a log it does not keep:\n%s", dev)
	}
}

// An agent that starts each dispatch fresh is told so, and told to leave what
// a later dispatch needs where a fresh session finds it. A seat without the
// policy reads not a word of it. The section sits with the obligations, ahead
// of any a line adds.
func TestTheOrientationExplainsFreshDispatchesOnlyToAnAgentWithThem(t *testing.T) {
	const heading = "## Each dispatch in a fresh session"
	fresh := renderSeat(t, "dev", func(m *model.Member) { m.FreshPerDispatch = true })
	for _, want := range []string{heading, "Each new dispatch starts you in a fresh session.",
		"So before you reply, commit your work, and write anything a later dispatch needs into a file under `~/" + guestOutputDir + "`.",
		"Where what you wrote and git disagree, trust git."} {
		if !strings.Contains(fold(fresh), want) {
			t.Errorf("an agent with the policy is not told %q:\n%s", want, fresh)
		}
	}
	for _, who := range []string{"orchestrator", "dev"} {
		if without := renderSeat(t, who, func(*model.Member) {}); strings.Contains(without, heading) || strings.Contains(without, "fresh session") {
			t.Errorf("%s has no such policy and its orientation describes it:\n%s", who, without)
		}
	}
	both := renderSeat(t, "dev", func(m *model.Member) { m.FreshPerDispatch = true; m.HandoffTokens = 60000 })
	if at, long := strings.Index(both, heading), strings.Index(both, "## Long work"); at < 0 || long < 0 || at > long {
		t.Errorf("the section comes before the hand-off sections: %d, %d\n%s", at, long, both)
	}
}

// handoffApp fakes one member whose probe answers with the given lines. The
// sandbox records every other command in calls and decodes every file it is
// asked to write into payloads. A copy into the kept handoffs goes to remote,
// so its order against the kill can be read. The hand-off check is recorded in
// checks and answered as answerCheck says, by default with nothing: work and
// handoff agree. The member's remote tools are stubs on PATH
// that record into remote, and the forget stub removes the host's session
// record as the real one does, so a turn started after it is a new session.
func handoffApp(t *testing.T, probe string, member model.Member) (a *app, calls, payloads, remote string) {
	t.Helper()
	dir := t.TempDir()
	calls, payloads, remote = filepath.Join(dir, "calls"), filepath.Join(dir, "payloads"), filepath.Join(dir, "remote")
	check := filepath.Join(dir, "check")
	tool := filepath.Join(dir, "fake-sandbox")
	body := "#!/bin/sh\ncase \"$5\" in\n  *DRIVERS*) printf '" + probe + "' ;;\n" +
		"  *NOHANDOFF*) printf '%s\\n' \"$5\" >> " + check + "s; cat " + check + " 2>/dev/null; [ ! -e " + check + ".fails ] ;;\n" +
		"  *'base64 -d'*) echo \"$*\" >> " + calls + "; cat >> " + payloads + "; echo >> " + payloads + " ;;\n" +
		"  */handoffs/*) echo \"keep $5\" >> " + remote + " ;;\n" +
		"  *) echo \"$*\" >> " + calls + " ;;\nesac\n"
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".cs-"+member.CLI+"-remote-sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{member.Session.Name, member.Session.Name + ".token"} {
		if err := os.WriteFile(filepath.Join(sessions, f), []byte("live\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	remoteTool := "cs-" + member.CLI + "-remote"
	stubs := map[string]string{
		// The kill reports what the real tool reports, so a caller can be
		// checked for passing it through.
		remoteTool: "#!/bin/sh\necho \"remote $*\" >> " + remote + "\n" +
			"case \"$*\" in *--kill*) echo \"Killed remote tmux session for '$4' (history kept; next turn resumes).\" ;; esac\n",
		remoteTool + "-forget": "#!/bin/sh\necho \"forget $*\" >> " + remote + "\nrm -f " + sessions + "/\"$1\" " + sessions + "/\"$1\".token\n",
	}
	for name, stub := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Only the stubs and the system's tools: a real remote tool may be installed.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}, calls, payloads, remote
}

// answerCheck makes the hand-off check of a handoffApp member print out, or,
// when it fails, exit as a check that could not run.
func answerCheck(t *testing.T, calls, out string, fails bool) {
	t.Helper()
	check := filepath.Join(filepath.Dir(calls), "check")
	if err := os.WriteFile(check, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if fails {
		if err := os.WriteFile(check+".fails", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// checksRun returns each hand-off check a handoffApp member was asked to run.
func checksRun(t *testing.T, calls string) []string {
	t.Helper()
	s := strings.TrimSuffix(readOr(t, filepath.Join(filepath.Dir(calls), "checks")), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// handoffProbe is a member on dispatch id, opened long ago, whose last turn
// ended in a hand-off five seconds ago.
func handoffProbe(id string) string {
	now := time.Now().Unix()
	return fmt.Sprintf(`MSG %d %s.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 0 handoff - handoff at 70000 tokens, over the line of 60000\n`, now-700, id, now-5)
}

// decoded returns the first file the fake sandbox was asked to write.
func decoded(t *testing.T, payloads string) string {
	t.Helper()
	first, _, _ := strings.Cut(readOr(t, payloads), "\n")
	b, err := base64.StdEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("undecodable payload %q: %v", first, err)
	}
	return string(b)
}

// assertFreshSession checks that the old session was ended and forgotten
// before the next turn began, and that the turn began a new session.
func assertFreshSession(t *testing.T, remote string, member model.Member) {
	t.Helper()
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(readOr(t, remote)), "\n") {
		if !strings.HasPrefix(l, "keep ") {
			lines = append(lines, l)
		}
	}
	if len(lines) < 3 {
		t.Fatalf("want a kill, a forget and a turn; the remote tools saw:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "--kill "+member.Session.Name) || lines[1] != "forget "+member.Session.Name {
		t.Errorf("the old session must be ended, then forgotten:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[2], "--new --name "+member.Session.Name) {
		t.Errorf("the turn after a hand-off is a new session:\n%s", lines[2])
	}
}

// At create the host owns a member's readback, and a member that hands off
// during it is started fresh on its handoff, out loud, rather than continued.
func TestAwaitReplyStartsAHandedOffMemberFresh(t *testing.T) {
	dev := model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g", Session: model.Session{Name: "camp-dev"}}
	a, calls, payloads, remote := handoffApp(t, handoffProbe("d001"), dev)
	pol := protocol.Policy{PollSeconds: 1, SettlingSeconds: 1}

	var out strings.Builder
	_, err := a.awaitReply(context.Background(), &out, dev, "d001", pol, 2*time.Second)
	// The fake member never replies, so the wait ends on its bound.
	if err == nil || !strings.Contains(err.Error(), "did not reply within") {
		t.Fatalf("the wait should end on its bound; got: %v", err)
	}
	if !strings.Contains(out.String(), "dev handed off — started fresh on its handoff, no rung spent") {
		t.Fatalf("the host must say it started the member fresh:\n%s", out.String())
	}
	sent := readOr(t, calls)
	if !strings.Contains(sent, "d001.001.handoff.md") {
		t.Fatalf("the fresh session must read d001.001.handoff.md; the host wrote:\n%s", sent)
	}
	if strings.Contains(sent, "d001.001.md") || strings.Contains(sent, "restart") {
		t.Fatalf("a hand-off is neither a continue nor a restart:\n%s", sent)
	}
	body := decoded(t, payloads)
	if !strings.Contains(body, "fresh session after a hand-off") || strings.Contains(body, protocol.LogFile) {
		t.Errorf("an agent's fresh session is pointed at its handoff and at no log:\n%s", body)
	}
	assertFreshSession(t, remote, dev)
}

// The orchestrator's fresh session is the host's to start, with the same
// instrument as its resume, and it is pointed at its log as well.
func TestResumeStartsAHandedOffOrchestratorFresh(t *testing.T) {
	orch := model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"}}
	a, calls, payloads, remote := handoffApp(t, handoffProbe("m1"), orch)
	var tools bytes.Buffer
	a.sandbox.Out, a.sandbox.Err = &tools, &tools
	pol := protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1}

	said, err := a.hostResumeOrchestrator(context.Background(), orch, pol)
	if err != nil {
		t.Fatalf("a hand-off is the host's move to make: %v", err)
	}
	// resume says what it did in its own words. The kill tool's line says the
	// next turn resumes the session, right above a line saying it is fresh.
	if said != "ended the orchestrator's session; starting it fresh on its handoff\n"+
		"m1 carried on in a fresh session on the orchestrator's handoff, no rung spent" {
		t.Errorf("said %q", said)
	}
	if strings.Contains(tools.String(), "Killed remote tmux session") {
		t.Errorf("resume passed the kill tool's report through:\n%s", tools.String())
	}
	if got := checksRun(t, calls); len(got) != 1 {
		t.Errorf("the work and the handoff are checked once before the swap: %q", got)
	}
	if sent := readOr(t, calls); !strings.Contains(sent, "m1.001.handoff.md") {
		t.Fatalf("the fresh session must read m1.001.handoff.md; the host wrote:\n%s", sent)
	}
	body := decoded(t, payloads)
	for _, want := range []string{"fresh session after a hand-off", "~/" + protocol.LogFile, "~/" + protocol.HandoffFile, "m1.md"} {
		if !strings.Contains(body, want) {
			t.Errorf("the orchestrator's fresh session is not pointed at %q:\n%s", want, body)
		}
	}
	assertFreshSession(t, remote, orch)
}

// The host keeps the orchestrator's handoff before it ends the session that
// wrote it, under this hand-off's ordinal on the dispatch: the second here.
func TestHostHandoffKeepsTheHandoffBeforeTheKill(t *testing.T) {
	orch := model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"}}
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nMSG %d m1.001.handoff.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 0 handoff - handoff at 70000 tokens, over the line of 60000\n`, now-700, now-600, now-5)
	a, calls, _, remote := handoffApp(t, probe, orch)
	pol := protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1, Handoffs: 10}

	if _, err := a.hostResumeOrchestrator(context.Background(), orch, pol); err != nil {
		t.Fatalf("resume: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(readOr(t, remote)), "\n")
	want := "keep mkdir -p ~/" + protocol.HandoffsDir + " && cp ~/" + protocol.HandoffFile + " ~/" + protocol.HandoffsDir + "/m1-2.md"
	if len(lines) < 2 || lines[0] != want || !strings.Contains(lines[1], "--kill camp-orch") {
		t.Fatalf("want the copy to m1-2.md, then the kill; the member saw:\n%s", strings.Join(lines, "\n"))
	}
	if sent := readOr(t, calls); !strings.Contains(sent, "m1.002.handoff.md") {
		t.Fatalf("the fresh session still starts on the next .handoff message:\n%s", sent)
	}
}

// An orchestrator whose work and handoff disagree is not started fresh: its
// session is kept, a .commit message in it asks it to commit, and resume says
// so, and says the next resume makes the swap. The check is asked about the
// orchestrator's own repositories, at their bases.
func TestResumeKeepsAnOrchestratorWhoseWorkIsNotCommitted(t *testing.T) {
	const base = "0123456789abcdef0123456789abcdef01234567"
	orch := model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"},
		Profile: model.MemberProfile{Repos: []model.Repo{{Path: "/src/app", ResolvedCommit: base}}}}
	a, calls, payloads, remote := handoffApp(t, handoffProbe("m1"), orch)
	answerCheck(t, calls, "UNNAMED\tapp\tabc1234\n", false)
	pol := protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1}

	said, err := a.hostResumeOrchestrator(context.Background(), orch, pol)
	if err != nil {
		t.Fatalf("a hand-off not taken is a move, not a failure: %v", err)
	}
	if said != "kept the orchestrator's session: its work and its handoff do not agree (your handoff does not name the commit ~/app is at, abc1234)\n"+
		"m1 carried on in the same session, asked to commit first, no rung spent; resume again when observe says fresh session next" {
		t.Errorf("said %q", said)
	}
	if got := checksRun(t, calls); len(got) != 1 || got[0] != protocol.HandoffCheckScript(map[string]string{"app": base}) {
		t.Errorf("the check must ask about the orchestrator's repository at its base: %q", got)
	}
	sent := readOr(t, calls)
	if !strings.Contains(sent, "m1.001.commit.md") || strings.Contains(sent, "handoff.md") {
		t.Fatalf("the kept session reads m1.001.commit.md, and no fresh session is started; the host wrote:\n%s", sent)
	}
	if body := decoded(t, payloads); body != protocol.CommitBody("m1", []string{"your handoff does not name the commit ~/app is at, abc1234"}) {
		t.Errorf("the ask is not the commit body:\n%s", body)
	}
	r := readOr(t, remote)
	if strings.Contains(r, "--kill") || strings.Contains(r, "forget") || strings.Contains(r, "keep ") || !strings.Contains(r, "--resume camp-orch") {
		t.Errorf("the session is kept and its turn resumes it, and the handoff is not copied aside:\n%s", r)
	}
}

// At create, the host's own wait says when it kept a member's session, and
// why, rather than claiming a fresh start it did not make.
func TestAwaitReplyKeepsAMemberWhoseWorkIsNotCommitted(t *testing.T) {
	dev := model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g", Session: model.Session{Name: "camp-dev"}}
	a, calls, _, remote := handoffApp(t, handoffProbe("d001"), dev)
	answerCheck(t, calls, "DIRTY\tapp\t1\tx.go\n", false)
	pol := protocol.Policy{PollSeconds: 1, SettlingSeconds: 1}

	var out strings.Builder
	if _, err := a.awaitReply(context.Background(), &out, dev, "d001", pol, 2*time.Second); err == nil || !strings.Contains(err.Error(), "did not reply within") {
		t.Fatalf("the wait should end on its bound; got: %v", err)
	}
	if !strings.Contains(out.String(), "dev handed off — session kept and asked to commit first (~/app has work not committed (1 path, the first x.go)), no rung spent") ||
		strings.Contains(out.String(), "started fresh") {
		t.Fatalf("the host must say it kept the session, and why:\n%s", out.String())
	}
	if !strings.Contains(readOr(t, calls), "d001.001.commit.md") {
		t.Errorf("the kept session must read d001.001.commit.md; the host wrote:\n%s", readOr(t, calls))
	}
	if r := readOr(t, remote); strings.Contains(r, "--kill") || strings.Contains(r, "forget") {
		t.Errorf("a kept session is never ended:\n%s", r)
	}
}

// A check that cannot run says nothing about the work. Nothing is ended and
// nothing is sent: resume fails and says which step, and the host's wait says
// it will try again.
func TestAHandoffCheckThatFailsToRunEndsNoSession(t *testing.T) {
	orch := model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"}}
	a, calls, payloads, remote := handoffApp(t, handoffProbe("m1"), orch)
	answerCheck(t, calls, "", true)
	pol := protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1}

	if _, err := a.hostResumeOrchestrator(context.Background(), orch, pol); err == nil || !strings.Contains(err.Error(), "check orchestrator's work against its handoff") {
		t.Fatalf("a check that did not run is an error naming the check: %v", err)
	}
	if r, p := readOr(t, remote), readOr(t, payloads); r != "" || p != "" {
		t.Fatalf("nothing moves on a check that did not run; remote:\n%s\npayloads:\n%s", r, p)
	}

	dev := model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g", Session: model.Session{Name: "camp-dev"}}
	a, calls, _, remote = handoffApp(t, handoffProbe("d001"), dev)
	answerCheck(t, calls, "", true)
	var out strings.Builder
	_, _ = a.awaitReply(context.Background(), &out, dev, "d001", protocol.Policy{PollSeconds: 1, SettlingSeconds: 1}, 2*time.Second)
	if !strings.Contains(out.String(), "dev: the hand-off failed and will be tried again: check dev's work against its handoff") {
		t.Errorf("the host's wait must say the hand-off failed and will be tried again:\n%s", out.String())
	}
	if r := readOr(t, remote); r != "" {
		t.Errorf("nothing moves on a check that did not run:\n%s", r)
	}
}

// observe names the instrument for the orchestrator's hand-off, as it does for
// its resume, because nothing else performs it.
func TestObserveNamesResumeForAHandedOffOrchestrator(t *testing.T) {
	orch := model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"}}
	a, _, _, remote := handoffApp(t, handoffProbe("m1"), orch)
	campaign := &model.Campaign{Name: "camp", Members: []model.Member{orch},
		Policy: protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, BlindProbes: 3, SettlingSeconds: 1}}

	obs, err := a.observeCampaign(context.Background(), campaign)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	row := obs.Derived[0]
	if row.State != string(protocol.StateStopped) || !strings.Contains(row.Detail, "fresh session next") ||
		!strings.Contains(row.Detail, "the host's move: cs-campaign resume") {
		t.Fatalf("observe must name the instrument for the hand-off: %s (%s)", row.State, row.Detail)
	}
	if s := readOr(t, remote); s != "" {
		t.Fatalf("observe looks and never acts:\n%s", s)
	}
}

// planProfile is a campaign of an orchestrator and one agent, worker, whose
// policy a test appends to the end.
const planProfile = "apiVersion: codesweep.ai/v1alpha1\nkind: CampaignProfile\norchestrator:\n  cli: codex\n  repos: [{path: /nonexistent/app}]\nagents:\n  worker:\n    cli: codex\n    repos: [{path: /nonexistent/app}]\n"

// runPlan runs `plan` on a profile and returns what it printed.
func runPlan(t *testing.T, profile string) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "campaign.yaml")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBriefsFor(t, path, "worker")
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}}
	cmd := a.createCmd(true)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"demo", "--profile", path})
	err := cmd.Execute()
	return out.Bytes(), err
}

// A member without a line must read the manifest bytes it read before lines
// existed (SPEC.md R142). So the plan of a profile with no line records no
// handoffs and no rapidHandoffSeconds, and one line on one seat records the
// default bound of ten and window of 300. A window the profile names is kept.
// A profile that sets neither a line nor freshPerDispatch names neither.
func TestThePlanRecordsTheHandoffBoundOnlyWithALine(t *testing.T) {
	line := planProfile + "    policy: {handoffTokens: 60000}\n"
	for _, tc := range []struct {
		name, profile string
		want, window  int
	}{
		{"no line", planProfile, 0, 0},
		{"a line on one seat", line, 10, 300},
		{"a line and a window", line + "defaults:\n  policy: {rapidHandoffSeconds: 120}\n", 10, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runPlan(t, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			var plan struct {
				Policy map[string]int `json:"policy"`
			}
			if err := json.Unmarshal(out, &plan); err != nil {
				t.Fatalf("plan is not JSON: %v\n%s", err, out)
			}
			got, has := plan.Policy["handoffs"]
			if has != (tc.want != 0) || got != tc.want {
				t.Errorf("plan policy %v: want handoffs %d, present %v", plan.Policy, tc.want, tc.want != 0)
			}
			got, has = plan.Policy["rapidHandoffSeconds"]
			if has != (tc.window != 0) || got != tc.window {
				t.Errorf("plan policy %v: want rapidHandoffSeconds %d, present %v", plan.Policy, tc.window, tc.window != 0)
			}
			if tc.want == 0 {
				for _, key := range []string{"handoffTokens", "freshPerDispatch"} {
					if bytes.Contains(out, []byte(key)) {
						t.Errorf("a profile that says nothing of it records %s:\n%s", key, out)
					}
				}
			}
		})
	}
}

// The switch is read from a profile as written, and the plan shows it on the
// seat it resolved for. On the orchestrator it is refused before anything is
// planned.
func TestThePlanCarriesFreshPerDispatchOnTheAgentAlone(t *testing.T) {
	out, err := runPlan(t, planProfile+"    policy: {freshPerDispatch: true}\n")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Members []struct {
			Name             string `json:"name"`
			FreshPerDispatch bool   `json:"freshPerDispatch"`
		} `json:"members"`
	}
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatalf("plan is not JSON: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, m := range plan.Members {
		got[m.Name] = m.FreshPerDispatch
	}
	if len(got) != 2 || !got["worker"] || got["orchestrator"] {
		t.Errorf("plan members %v: want worker fresh at each dispatch, and the orchestrator not", got)
	}

	orch := strings.Replace(planProfile, "orchestrator:\n  cli: codex\n", "orchestrator:\n  cli: codex\n  policy: {freshPerDispatch: true}\n", 1)
	if _, err := runPlan(t, orch); err == nil || !strings.Contains(err.Error(), "orchestrator: policy.freshPerDispatch") {
		t.Errorf("an orchestrator's freshPerDispatch must be refused by name: %v", err)
	}
}

// wait reads from the orchestrator's roster which agents start fresh at each
// dispatch. An agent without the policy has no word of it there, and neither
// has any member.json, so a campaign that never sets it writes the bytes it
// always wrote.
func TestTheManifestCarriesFreshPerDispatchOnlyWhenOn(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		dir := t.TempDir()
		tool, capture := filepath.Join(dir, "fake-sandbox"), filepath.Join(dir, "exec-args")
		body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CAPTURE\"\ncat >> \"$CAPTURE.stdin\"; echo >> \"$CAPTURE.stdin\"\n"
		if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CAPTURE", capture)
		campaign := &model.Campaign{Name: "demo", Group: "demo-grp", Network: groupNetwork("demo-grp"), Members: []model.Member{
			{Name: "orchestrator", Role: "orchestrator", CLI: "codex", Sandbox: "demo-orchestrator", Ref: "demo-orchestrator.demo-grp",
				Session: model.Session{Name: "demo-orchestrator"}},
			{Name: "backend", Role: "agent", CLI: "claude", Sandbox: "demo-backend", Ref: "demo-backend.demo-grp",
				Session: model.Session{Name: "demo-backend"}, FreshPerDispatch: fresh},
			{Name: "frontend", Role: "agent", CLI: "claude", Sandbox: "demo-frontend", Ref: "demo-frontend.demo-grp",
				Session: model.Session{Name: "demo-frontend"}},
		}}
		if err := (sandboxCLI{Bin: tool}).configureOrchestrator(context.Background(), campaign); err != nil {
			t.Fatal(err)
		}
		raw := capturedPayload(t, capture, 0)
		var manifest struct {
			Agents map[string]map[string]any `json:"agents"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("no manifest payload: %v", err)
		}
		if v, has := manifest.Agents["backend"]["freshPerDispatch"]; has != fresh || (fresh && v != true) {
			t.Errorf("backend with the policy %v: manifest record %v", fresh, manifest.Agents["backend"])
		}
		if _, has := manifest.Agents["frontend"]["freshPerDispatch"]; has {
			t.Errorf("an agent without the policy has it named in the manifest: %v", manifest.Agents["frontend"])
		}
		if !fresh && bytes.Contains(raw, []byte("freshPerDispatch")) {
			t.Errorf("a campaign without the policy names it in its manifest:\n%s", raw)
		}
	}

	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c := buildCampaign("acme", p, "", "", time.Unix(1_800_000_000, 0))
	for _, m := range c.Members {
		_, doc, err := buildOrientation(c, m, campaignInputs{})
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(doc); bytes.Contains(b, []byte("freshPerDispatch")) {
			t.Errorf("%s's member.json names a policy nobody set: %s", m.Name, b)
		}
	}
}
