package main

// The protocol conformance suite: PROTOCOL.md's recovery rules, run against a
// simulated world on a scripted clock, and judged by invariants rather than by
// what any one fix prints.
//
// wait_test.go proves single moves of the ladder. This file proves what a
// whole campaign does over hours when the world misbehaves: a provider that
// throttles, a credential it rejects, an observer that loses its network, a
// turn that never starts. A scenario is a timeline; the real snapshot, Compute
// and cmdWait run against it; and the history is then checked against
// statements the protocol makes, phrased with the protocol's own public
// functions. An invariant names no message text and no file of this
// implementation, so it outlives the fix that first made it pass.
//
// Nothing here sleeps. clockSleep advances the simulated clock and fires the
// events that came due, so an hour of backoff costs microseconds.

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// Failure classes a turn driver reports (sandbox's `failure class=` line).
const (
	classThrottled    = "throttled"
	classCapacity     = "capacity"
	classUnauthorized = "unauthorized"
	classContext      = "context"
)

// turnOutcome is what one started turn will do.
type turnOutcome struct {
	runs       int64  // seconds until the turn ends
	reply      bool   // it writes the open dispatch's reply
	class      string // "" for a turn the model ended by itself
	retryAfter int64  // what the provider asked for, 0 when it named no wait
}

// turnEnd is one line of the node's own turn log.
type turnEnd struct {
	at         int64
	exit       int
	class      string
	retryAfter int64
}

type simNode struct {
	name, host, cli string
	msgs            map[string]int64
	replies         map[string]bool
	drivers         int
	busy            bool
	ends            []turnEnd
	// script decides each started turn's outcome from the clock.
	script func(now int64) turnOutcome
	// startFails says the turn launcher fails at this instant: the message
	// was delivered and no turn ran.
	startFails func(now int64) bool
	// gen invalidates a running turn's scheduled end when the session is killed.
	gen int
	// oldTools is a node whose image predates the turn log and --state: its
	// probe says nothing about either.
	oldTools bool
}

type simEvent struct {
	at  int64
	seq int
	fn  func()
}

type turnStart struct {
	at   int64
	node string
}

type simWorld struct {
	t      *testing.T
	now    int64
	seq    int
	events []simEvent
	nodes  map[string]*simNode // by host
	// blind says the observer cannot reach any node at this instant.
	blind func(now int64) bool

	starts     []turnStart
	kills      []turnStart
	deliveries []delivery
	// looks records every computed state, so an invariant can ask what any
	// observer was ever told.
	looks []simLook
	// spins counts waits that returned without the clock moving.
	spins int
}

type simLook struct {
	at    int64
	node  string
	state protocol.State
}

func newSimWorld(t *testing.T, nodes ...*simNode) *simWorld {
	w := &simWorld{t: t, now: 1_800_000_000, nodes: map[string]*simNode{}}
	for _, n := range nodes {
		n.host = n.name + "-box"
		if n.cli == "" {
			n.cli = "codex"
		}
		n.msgs, n.replies = map[string]int64{}, map[string]bool{}
		w.nodes[n.host] = n
	}
	origSSH, origStart, origRun, origNow, origSleep := sshOut, startTurn, runSessionCmd, clockNow, clockSleep
	t.Cleanup(func() {
		sshOut, startTurn, runSessionCmd, clockNow, clockSleep = origSSH, origStart, origRun, origNow, origSleep
	})
	sshOut, startTurn, runSessionCmd = w.sshOut, w.startTurn, w.sessionCmd
	clockNow = func() time.Time { return time.Unix(w.now, 0) }
	clockSleep = func(d time.Duration) { w.advance(int64(d / time.Second)) }
	return w
}

func (w *simWorld) at(when int64, fn func()) {
	w.seq++
	w.events = append(w.events, simEvent{at: when, seq: w.seq, fn: fn})
}

// advance moves the clock, firing every event that comes due, in order.
func (w *simWorld) advance(secs int64) {
	target := w.now + secs
	for {
		sort.Slice(w.events, func(i, j int) bool {
			if w.events[i].at != w.events[j].at {
				return w.events[i].at < w.events[j].at
			}
			return w.events[i].seq < w.events[j].seq
		})
		if len(w.events) == 0 || w.events[0].at > target {
			break
		}
		ev := w.events[0]
		w.events = w.events[1:]
		w.now = ev.at
		ev.fn()
	}
	w.now = target
}

func (w *simWorld) sshOut(host, command, payload string) ([]byte, error) {
	n, ok := w.nodes[host]
	if !ok {
		return nil, fmt.Errorf("unscripted host %q", host)
	}
	// A round trip takes time. With none, exactly ten looks fit inside the
	// default wait, which is the default threshold, and the simulation would
	// conclude what no real observer can.
	w.advance(1)
	if w.blind != nil && w.blind(w.now) {
		return nil, fmt.Errorf("ssh: connect to host %s: network is unreachable", host)
	}
	if command == protocol.ProbeScript(n.cli) {
		return []byte(w.probeOutput(n)), nil
	}
	if m := putRe.FindStringSubmatch(command); m != nil {
		body, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(m[1], protocol.InputDir+"/")
		n.msgs[name] = w.now
		w.deliveries = append(w.deliveries, delivery{host: host, name: name, body: string(body)})
		return nil, nil
	}
	return nil, fmt.Errorf("unscripted command for %s: %s", host, command)
}

// probeOutput is what the node's machine says about itself. STATE and TURNEND
// are the two facts the sandbox's turn drivers can supply; a parser that does
// not know them ignores them, which is how versions mix.
func (w *simWorld) probeOutput(n *simNode) string {
	var b strings.Builder
	for name, mt := range n.msgs {
		fmt.Fprintf(&b, "MSG %d %s\n", mt, name)
	}
	for id := range n.replies {
		fmt.Fprintf(&b, "REPLY %s\n", id)
	}
	fmt.Fprintf(&b, "DRIVERS %d\n", n.drivers)
	if n.oldTools {
		fmt.Fprintf(&b, "AGENT \n")
		return b.String()
	}
	state := "idle"
	if n.busy {
		state = "busy"
	}
	fmt.Fprintf(&b, "AGENT %s\n", state)
	from := max(0, len(n.ends)-8)
	for _, e := range n.ends[from:] {
		class, ra := e.class, "-"
		if class == "" {
			class = "-"
		}
		if e.retryAfter > 0 {
			ra = strconv.FormatInt(e.retryAfter, 10)
		}
		fmt.Fprintf(&b, "TURNEND %d %d %s %s\n", e.at, e.exit, class, ra)
	}
	return b.String()
}

func (w *simWorld) startTurn(_ string, rec protocol.AgentRecord, _, id string) error {
	n := w.nodes[rec.Sandbox]
	if n.startFails != nil && n.startFails(w.now) {
		return fmt.Errorf("start turn on %s: connection timed out", rec.Sandbox)
	}
	w.starts = append(w.starts, turnStart{at: w.now, node: n.name})
	out := n.script(w.now)
	n.drivers, n.busy = 1, true
	n.gen++
	gen := n.gen
	w.at(w.now+out.runs, func() {
		if n.gen != gen {
			return // the session was killed under this turn
		}
		n.drivers, n.busy = 0, false
		exit := 0
		if out.class != "" {
			exit = 5
		}
		n.ends = append(n.ends, turnEnd{at: w.now, exit: exit, class: out.class, retryAfter: out.retryAfter})
		if out.reply {
			n.replies[id] = true
		}
	})
	return nil
}

func (w *simWorld) sessionCmd(name string, args ...string) error {
	for i, a := range args {
		if a == "--kill" && i > 0 {
			for _, n := range w.nodes {
				if n.host == args[i-1] {
					n.gen++
					n.drivers, n.busy = 0, false
					w.kills = append(w.kills, turnStart{at: w.now, node: n.name})
				}
			}
		}
	}
	return nil
}

func (w *simWorld) env() *envState {
	agents := map[string]protocol.AgentRecord{}
	for host, n := range w.nodes {
		agents[n.name] = protocol.AgentRecord{CLI: n.cli, Sandbox: host, Session: "sess-" + n.name}
	}
	return &envState{
		Home:     w.t.TempDir(),
		Member:   protocol.Member{Role: "orchestrator"},
		Manifest: &protocol.Manifest{Policy: protocol.DefaultPolicy(), Agents: agents},
	}
}

// campaign is the smallest honest orchestrator: dispatch once to every node,
// then wait, accept what comes back, and write off what gets stuck. It stops
// when every node is settled or the horizon passes, and returns each node's
// last computed state.
func (w *simWorld) campaign(env *envState, horizon int64) map[string]protocol.State {
	w.t.Helper()
	for _, name := range sortedAgents(env) {
		if _, err := sendBody(env, name, "do the work"); err != nil {
			w.t.Fatalf("dispatch to %s: %v", name, err)
		}
	}
	end := w.now + horizon
	final := map[string]protocol.State{}
	settled := map[string]bool{}
	for calls := 0; w.now < end && len(settled) < len(w.nodes); calls++ {
		before, settledBefore := w.now, len(settled)
		out, err := captureStdout(w.t, func() error { return cmdWait(env, nil) })
		if err != nil {
			w.t.Fatalf("wait: %v", err)
		}
		// What wait told the orchestrator is what the orchestrator acts on. A
		// model that is told a node is stuck writes its queue off, and no later
		// look gives that work back.
		for name := range env.Manifest.Agents {
			if strings.Contains(out, name+" is stuck") && !settled[name] {
				final[name] = protocol.StateStuck
				settled[name] = true
				w.looks = append(w.looks, simLook{at: w.now, node: name, state: protocol.StateStuck})
			}
		}
		obs := snapshot(env)
		for name, look := range obs {
			if settled[name] {
				continue
			}
			final[name] = look.Obs.State
			w.looks = append(w.looks, simLook{at: w.now, node: name, state: look.Obs.State})
			switch look.Obs.State {
			case protocol.StateReplied:
				_, _ = captureStdout(w.t, func() error { return cmdAccept(env, []string{name}) })
				final[name] = protocol.StateFree
				settled[name] = true
			case protocol.StateStuck:
				settled[name] = true
			}
		}
		// A wait that returns without sleeping once, while nothing new is
		// settled, is a wait that cannot be waited out. A model pays a turn for
		// each such call. A handful is a defect; the loop guard keeps the test
		// from spinning on it.
		if w.now-before < int64(protocol.DefaultPolicy().PollSeconds) && len(settled) == settledBefore {
			w.spins++
			if w.spins > 50 {
				break
			}
		}
	}
	return final
}

// ─── invariants ────────────────────────────────────────────────────────────

// refusedOnly says every turn the node ever ended was refused by the provider
// for a reason waiting cures.
func refusedOnly(n *simNode) bool {
	for _, e := range n.ends {
		if e.class != classThrottled && e.class != classCapacity {
			return false
		}
	}
	return len(n.ends) > 0
}

func (w *simWorld) current(n *simNode) *protocol.Dispatch {
	var msgs []protocol.Msg
	for name, mt := range n.msgs {
		if m, ok := protocol.ParseMsgName(name, mt); ok {
			msgs = append(msgs, m)
		}
	}
	return protocol.Current(msgs)
}

// I1: a provider refusal never charges a rung and never reaches restart.
func (w *simWorld) checkNoRungForRefusal(n *simNode) {
	w.t.Helper()
	d := w.current(n)
	if d == nil {
		return
	}
	if d.Continues != 0 || d.Restarts != 0 {
		w.t.Errorf("I1: %s was only ever refused by its provider, and the ladder charged %d continues and %d restarts",
			n.name, d.Continues, d.Restarts)
	}
	for _, k := range w.kills {
		if k.node == n.name {
			w.t.Errorf("I1: %s had its session killed at +%ds while the provider was refusing: a restart re-sends the whole context into the same limit",
				n.name, k.at-w.looks0())
		}
	}
}

func (w *simWorld) looks0() int64 { return 1_800_000_000 }

// I2: no turn is started on a refused node before the provider's own wait is over.
func (w *simWorld) checkWaitsHonoured(n *simNode) {
	w.t.Helper()
	for _, e := range n.ends {
		if e.retryAfter == 0 || (e.class != classThrottled && e.class != classCapacity) {
			continue
		}
		for _, s := range w.starts {
			if s.node == n.name && s.at >= e.at && s.at < e.at+e.retryAfter {
				w.t.Errorf("I2: %s was resumed %ds after a refusal that asked for %ds", n.name, s.at-e.at, e.retryAfter)
			}
		}
	}
}

// I5: a rung is charged only for a turn that started. The count is the
// protocol's own, computed from what the node's machine says.
func (w *simWorld) checkRungsAreTurns(n *simNode) {
	w.t.Helper()
	facts := protocol.ParseProbe(w.probeOutput(n))
	d := protocol.Current(facts.Msgs)
	if d == nil {
		return
	}
	started := 0
	for _, s := range w.starts {
		if s.node == n.name {
			started++
		}
	}
	continues, restarts := protocol.Charged(d, facts)
	// The opening turn is not a rung.
	if rungs := continues + restarts; rungs > max(0, started-1) {
		w.t.Errorf("I5: %s was charged %d rungs and only %d recovery turns ever started", n.name, rungs, max(0, started-1))
	}
}

// ─── scenarios ─────────────────────────────────────────────────────────────

// throttledUntil is a provider that refuses every turn before clear, asking
// for wait seconds, and lets the work finish afterwards.
func throttledUntil(clear, wait int64) func(int64) turnOutcome {
	return func(now int64) turnOutcome {
		if now < clear {
			return turnOutcome{runs: 120, class: classThrottled, retryAfter: wait}
		}
		return turnOutcome{runs: 300, reply: true}
	}
}

func healthy(now int64) turnOutcome { return turnOutcome{runs: 300, reply: true} }

// A provider throttles one member for forty minutes and then clears. The
// member is healthy throughout, so it must end with its work delivered, no
// rung charged, and never resumed inside a wait the provider asked for.
func TestConformanceThrottleThatClears(t *testing.T) {
	dev := &simNode{name: "dev"}
	w := newSimWorld(t, dev)
	dev.script = throttledUntil(w.now+2400, 15)
	final := w.campaign(w.env(), 4*3600)

	if final["dev"] != protocol.StateFree {
		t.Errorf("a member throttled for forty minutes and then served must deliver; it ended %s", final["dev"])
	}
	w.checkNoRungForRefusal(dev)
	w.checkWaitsHonoured(dev)
}

// I3: members refused together do not come back together.
func TestConformanceFleetDoesNotResumeInStep(t *testing.T) {
	var nodes []*simNode
	for i := range 6 {
		nodes = append(nodes, &simNode{name: fmt.Sprintf("m%d", i)})
	}
	w := newSimWorld(t, nodes...)
	for _, n := range nodes {
		n.script = throttledUntil(w.now+1800, 20)
	}
	w.campaign(w.env(), 3*3600)

	perSecond := map[int64]int{}
	for _, s := range w.starts[len(nodes):] { // the opening dispatches are the orchestrator's own
		perSecond[s.at]++
	}
	for at, n := range perSecond {
		if n > len(nodes)/2 {
			t.Errorf("I3: %d of %d throttled members were resumed in the same second (+%ds): that is the load that caused the throttle",
				n, len(nodes), at-w.looks0())
		}
	}
	for _, n := range nodes {
		w.checkNoRungForRefusal(n)
	}
}

// I4: a rejected credential is the operator's repair. The first look after it
// is a judgment, and nothing is sent into it.
func TestConformanceRejectedCredentialStopsAtOnce(t *testing.T) {
	dev := &simNode{name: "dev", script: func(int64) turnOutcome {
		return turnOutcome{runs: 5, class: classUnauthorized}
	}}
	w := newSimWorld(t, dev)
	w.campaign(w.env(), 3600)

	if len(w.starts) != 1 {
		t.Errorf("I4: %d turns were started against a credential the provider rejects; only the opening one can be excused", len(w.starts))
	}
	if len(w.kills) != 0 {
		t.Errorf("I4: a session was restarted into a rejected credential")
	}
}

// I5 and the connectivity half of the ladder: the launcher fails for ten
// minutes. Messages land, no turn runs, and the node must not be written off
// for turns it was never given.
func TestConformanceRungsAreOnlyChargedForTurnsThatStarted(t *testing.T) {
	dev := &simNode{name: "dev", script: func(now int64) turnOutcome { return turnOutcome{runs: 60} }}
	w := newSimWorld(t, dev)
	outage := w.now + 30
	dev.startFails = func(now int64) bool { return now > outage && now < outage+1800 }
	// The opening turn stops without replying, so the ladder has work to do.
	first := true
	dev.script = func(int64) turnOutcome {
		if first {
			first = false
			return turnOutcome{runs: 60}
		}
		return turnOutcome{runs: 300, reply: true}
	}
	final := w.campaign(w.env(), 3*3600)

	w.checkRungsAreTurns(dev)
	if final["dev"] == protocol.StateStuck {
		t.Errorf("I5: dev was declared stuck, and every recovery turn it was charged for failed to launch")
	}
}

// I6: when the observer loses every node at once it has learned about itself.
// No machine is gone, and the campaign must come through the outage.
func TestConformanceObserverOutageCondemnsNoNode(t *testing.T) {
	for _, chunk := range []string{"", "840"} {
		t.Run("chunk="+chunk, func(t *testing.T) {
			if chunk != "" {
				// The manual's advice for a replay of real work. Ten looks now fit
				// inside one wait, which is what let the count reach its threshold.
				t.Setenv("CS_CAMPAIGN_WAIT_SECONDS", chunk)
			}
			observerOutage(t)
		})
	}
}

func observerOutage(t *testing.T) {
	a, b := &simNode{name: "a", script: healthy}, &simNode{name: "b", script: healthy}
	for _, n := range []*simNode{a, b} {
		n.script = func(int64) turnOutcome { return turnOutcome{runs: 3600, reply: true} }
	}
	w := newSimWorld(t, a, b)
	start := w.now
	w.blind = func(now int64) bool { return now > start+300 && now < start+1500 } // a twenty minute flap
	final := w.campaign(w.env(), 3*3600)

	for _, l := range w.looks {
		if l.state == protocol.StateStuck {
			t.Errorf("I6: %s was declared gone at +%ds, during an outage that hid every node from the observer at once", l.node, l.at-start)
		}
	}
	for name, s := range final {
		if s != protocol.StateFree {
			t.Errorf("I6: %s ended %s; both machines were healthy for the whole campaign", name, s)
		}
	}
}

// I11: a machine that is really gone is concluded gone, under the default
// policy, while its neighbour stays reachable. The conclusion must not depend
// on how many looks happen to fit inside one wait.
func TestConformanceLostMachineIsConcluded(t *testing.T) {
	gone := &simNode{name: "gone", script: func(int64) turnOutcome { return turnOutcome{runs: 9 * 3600, reply: true} }}
	fine := &simNode{name: "fine", script: func(int64) turnOutcome { return turnOutcome{runs: 9 * 3600, reply: true} }}
	w := newSimWorld(t, fine, gone)
	start := w.now
	inner := w.sshOut
	sshOut = func(host, command, payload string) ([]byte, error) {
		if host == gone.host && w.now > start+60 {
			return nil, fmt.Errorf("ssh: connect to host %s: no route to host", host)
		}
		return inner(host, command, payload)
	}
	final := w.campaign(w.env(), 2*3600)
	if final["gone"] != protocol.StateStuck {
		t.Errorf("I11: a machine unreachable for two hours, beside one that answered every look, ended %s and was never concluded gone", final["gone"])
	}
}

// I10: a judgment ends a wait once. With one node stuck for good and another
// still working, the orchestrator must still be able to block.
func TestConformanceStuckNodeDoesNotEndEveryWait(t *testing.T) {
	dead := &simNode{name: "dead", script: func(int64) turnOutcome { return turnOutcome{runs: 30} }}
	slow := &simNode{name: "slow", script: func(int64) turnOutcome { return turnOutcome{runs: 3 * 3600, reply: true} }}
	w := newSimWorld(t, dead, slow)
	w.campaign(w.env(), 5*3600)

	if w.spins > 3 {
		t.Errorf("I10: wait returned %d times without the clock moving: after one node is stuck the orchestrator can no longer block, and pays a model turn per call", w.spins)
	}
}

// I7 and I8 over generated worlds: whatever the provider does, state is a pure
// function of what is on the machines, and every campaign ends.
func TestConformanceGeneratedWorlds(t *testing.T) {
	for seed := range int64(200) {
		rng := rand.New(rand.NewSource(seed))
		var nodes []*simNode
		for i := range 1 + rng.Intn(4) {
			nodes = append(nodes, &simNode{name: fmt.Sprintf("n%d", i)})
		}
		w := newSimWorld(t, nodes...)
		for _, n := range nodes {
			clear := w.now + int64(rng.Intn(5400))
			wait := int64(5 + rng.Intn(40))
			n.script = throttledUntil(clear, wait)
		}
		env := w.env()
		w.campaign(env, 6*3600)

		for _, n := range nodes {
			if refusedOnly(n) || len(n.ends) > 0 && n.ends[len(n.ends)-1].class == "" {
				w.checkWaitsHonoured(n)
			}
			w.checkRungsAreTurns(n)
		}
		// I7: two observers with no memory agree.
		one, two := snapshot(env), snapshot(env)
		for name := range one {
			if one[name].Obs.State != two[name].Obs.State {
				t.Errorf("I7 (seed %d): two looks at %s in the same instant disagree: %s and %s", seed, name, one[name].Obs.State, two[name].Obs.State)
			}
		}
		if t.Failed() {
			t.Logf("first failing seed: %d", seed)
			return
		}
	}
}

// I8: a provider that never relents does not hold a node for ever. The wait
// ends in node-stuck within the policy bound, the reason is named, and even
// then no rung was spent and no session was restarted into the refusal.
func TestConformanceProviderWaitIsBounded(t *testing.T) {
	dev := &simNode{name: "dev", script: func(int64) turnOutcome {
		return turnOutcome{runs: 120, class: classCapacity}
	}}
	w := newSimWorld(t, dev)
	start := w.now
	env := w.env()
	final := w.campaign(env, 6*3600)

	if final["dev"] != protocol.StateStuck {
		t.Fatalf("I8: a node refused for six hours ended %s", final["dev"])
	}
	var stuckAt int64
	for _, l := range w.looks {
		if l.node == "dev" && l.state == protocol.StateStuck {
			stuckAt = l.at - start
			break
		}
	}
	bound := int64(protocol.DefaultPolicy().ProviderWaitSeconds)
	if stuckAt < bound || stuckAt > bound+900 {
		t.Errorf("I8: the provider wait bound is %ds and the node was given up at +%ds", bound, stuckAt)
	}
	if obs := snapshot(env)["dev"].Obs; !strings.Contains(obs.Detail, classCapacity) {
		t.Errorf("I8: a node given up on a provider's refusal must say so; detail: %q", obs.Detail)
	}
	w.checkNoRungForRefusal(dev)
	w.checkWaitsHonoured(dev)
}

// A context the model cannot take is not cured by a continue, which sends the
// same context again. The restart is the only rung that shortens it, so it is
// the first rung spent.
func TestConformanceContextTooLongGoesStraightToRestart(t *testing.T) {
	turns := 0
	dev := &simNode{name: "dev", script: func(int64) turnOutcome {
		turns++
		if turns == 1 {
			return turnOutcome{runs: 60, class: classContext}
		}
		return turnOutcome{runs: 300, reply: true}
	}}
	w := newSimWorld(t, dev)
	final := w.campaign(w.env(), 3*3600)

	d := w.current(dev)
	if d.Continues != 0 || d.Restarts != 1 {
		t.Errorf("a context that is too long cost %d continues and %d restarts; want none and one", d.Continues, d.Restarts)
	}
	if final["dev"] != protocol.StateFree {
		t.Errorf("the restarted node delivered, and ended %s", final["dev"])
	}
}

// An agent can start a turn of its own, which no driver wraps, and an agent can
// be killed while its driver lives. The agent's own word decides both: a busy
// agent is working whoever started the turn, and is never nudged.
func TestConformanceTheAgentsOwnWordDecidesWorking(t *testing.T) {
	dev := &simNode{name: "dev", script: func(int64) turnOutcome { return turnOutcome{runs: 60} }}
	w := newSimWorld(t, dev)
	env := w.env()
	if _, err := sendBody(env, "dev", "do the work"); err != nil {
		t.Fatal(err)
	}
	// The host's turn ends with no reply. Ten minutes on, a background task
	// the agent left running hands it a turn of its own, for two hours.
	w.advance(90)
	w.at(w.now+600, func() { dev.busy = true })
	w.at(w.now+600+7200, func() { dev.busy = false; dev.replies["d001"] = true })
	w.advance(660)

	sent := len(w.deliveries)
	for w.now < 1_800_000_000+7000 {
		if _, err := captureStdout(t, func() error { return cmdWait(env, nil) }); err != nil {
			t.Fatal(err)
		}
		if o := snapshot(env)["dev"].Obs; dev.busy && o.State != protocol.StateWorking {
			t.Fatalf("an agent in a turn of its own read %s (%s)", o.State, o.Detail)
		}
	}
	if len(w.deliveries) != sent {
		t.Errorf("%d message(s) were sent into a turn the agent had started itself", len(w.deliveries)-sent)
	}
}

// I9: a node whose tools predate the turn log is computed exactly as before.
// The ladder runs, rungs are counted from the messages, and nothing about the
// new facts is assumed from their absence.
func TestConformanceOldToolsKeepTheOldLadder(t *testing.T) {
	dev := &simNode{name: "dev", oldTools: true, script: func(int64) turnOutcome { return turnOutcome{runs: 60} }}
	w := newSimWorld(t, dev)
	final := w.campaign(w.env(), 4*3600)

	d := w.current(dev)
	pol := protocol.DefaultPolicy()
	if final["dev"] != protocol.StateStuck || d.Continues != pol.ContinueAttempts || d.Restarts != pol.Restarts || d.Resumes != 0 {
		t.Errorf("a node with old tools that never replies must spend the ladder as before; ended %s with %d continues, %d restarts, %d resumes",
			final["dev"], d.Continues, d.Restarts, d.Resumes)
	}
}

// A machine starved of CPU misses the probe's bound while it works steadily. A
// missed bound is silence and not an error, and no amount of it concludes that
// the machine is gone (SPEC R65).
func TestConformanceASlowMachineIsNotALostMachine(t *testing.T) {
	slow := &simNode{name: "slow", script: func(int64) turnOutcome { return turnOutcome{runs: 2 * 3600, reply: true} }}
	fine := &simNode{name: "fine", script: func(int64) turnOutcome { return turnOutcome{runs: 2 * 3600, reply: true} }}
	w := newSimWorld(t, fine, slow)
	start := w.now
	inner := w.sshOut
	sshOut = func(host, command, payload string) ([]byte, error) {
		if host == slow.host && w.now > start+60 && w.now < start+2*3600 {
			w.advance(int64(memberCmdBound / time.Second))
			return nil, fmt.Errorf("gave up after %s: %w", memberCmdBound, errMissedBound)
		}
		return inner(host, command, payload)
	}
	final := w.campaign(w.env(), 4*3600)
	for name, s := range final {
		if s != protocol.StateFree {
			t.Errorf("%s ended %s; it was slow to answer for two hours and never gone", name, s)
		}
	}
}
