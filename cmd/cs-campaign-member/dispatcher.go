package main

// Dispatcher verbs: the orchestrator's side of the protocol. Every address
// here is a BARE in-group name — the group's DNS serves bare names only, and
// the guest ssh config offers the tier key only to undotted hosts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

func agentRec(env *envState, name string) (protocol.AgentRecord, error) {
	rec, ok := env.Manifest.Agents[name]
	if !ok {
		known := make([]string, 0, len(env.Manifest.Agents))
		for n := range env.Manifest.Agents {
			known = append(known, n)
		}
		sort.Strings(known)
		return rec, fmt.Errorf("unknown campaign agent %q (roster: %s)", name, strings.Join(known, ", "))
	}
	return rec, nil
}

// sshOut, startTurn and runSessionCmd are the dispatcher's only three routes
// out of this process. They are variables so the wait ladder — otherwise
// reachable only live — can run against a scripted world in unit tests.
// memberCmdBound is how long a round trip to a teammate's machine may take.
//
// Deliberately under the shortest turn-tool timeout an adapter imposes, which
// is 120s, so that what the model reads is this command failing and saying so
// rather than its own tool reporting that something was killed. A campaign
// lost its cassette to the difference: `fetch dev` hung, the shell tool killed
// it at 120s with no output, and the orchestrator judged a branch it had never
// fetched, against a ref that did not exist.
var memberCmdBound = 90 * time.Second

// memberBoundForTest shortens the bound so a test can watch it fire.
func memberBoundForTest(d time.Duration) func() {
	prev := memberCmdBound
	memberCmdBound = d
	return func() { memberCmdBound = prev }
}

// gitSSH is what git is told to reach a teammate with. BatchMode so a fetch
// fails rather than waiting on a prompt nobody can answer, and a connect
// timeout so an unreachable machine is an error rather than a wait. The direct
// ssh route below has always said BatchMode; git was left with the default.
const gitSSH = "ssh -o BatchMode=yes -o ConnectTimeout=10"

// payload, when non-empty, is fed to the remote command on stdin. A dispatch
// body travels that way rather than inside command: Linux caps one argv entry
// at MAX_ARG_STRLEN, far below the total argument space, so an embedded body
// puts a ceiling on how long a message may be.
var sshOut = func(host, command, payload string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), memberCmdBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, command)
	if payload != "" {
		cmd.Stdin = strings.NewReader(payload)
	}
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return out, fmt.Errorf("gave up after %s: %w", memberCmdBound, errMissedBound)
	}
	return out, err
}

// errMissedBound marks a round trip that ran out of time rather than failed. An
// error comes back from a machine that answered; a missed bound is silence,
// which a machine under load produces as readily as one that is gone (R65).
var errMissedBound = errors.New("no answer within the bound")

// gitCmd runs one git command against a teammate's machine, bounded, with the
// ssh options git does not set for itself.
func gitCmd(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), memberCmdBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+gitSSH)
	// Cancelling kills git and not the ssh it spawned, and CombinedOutput waits
	// on the pipe rather than on the process, so without this the bound above
	// does nothing and the wait runs on until ssh gives up by itself.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("gave up after %s: %w", memberCmdBound, ctx.Err())
	}
	return out, err
}

var runSessionCmd = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// clockNow and clockSleep are the dispatcher's only two readings of time. They
// are variables for the reason the three routes above are: a wait that backs
// off for minutes can then be tested against a scripted clock, in no time.
var (
	clockNow   = time.Now
	clockSleep = time.Sleep
)

// probeAgent is the one round trip of PROTOCOL.md §6: every fact about one
// node, or a probe failure — which is a fact about the observation, not the
// node.
func probeAgent(rec protocol.AgentRecord) (protocol.Facts, bool) {
	facts, failed, _ := probeAgentBound(rec)
	return facts, failed
}

// probeAgentBound also says whether a failed probe merely ran out of time.
func probeAgentBound(rec protocol.AgentRecord) (facts protocol.Facts, failed, missedBound bool) {
	out, err := sshOut(rec.Sandbox, protocol.ProbeScript(rec.CLI), "")
	if err != nil {
		return protocol.Facts{}, true, errors.Is(err, errMissedBound)
	}
	return protocol.ParseProbe(string(out)), false, false
}

func (e *envState) logEntries() []protocol.Entry {
	b, err := os.ReadFile(filepath.Join(e.Home, protocol.LogFile))
	if err != nil {
		return nil
	}
	return protocol.ParseLog(b)
}

func (e *envState) policy() protocol.Policy { return e.Manifest.Policy.Resolve() }

func cmdList(env *envState) {
	names := sortedAgents(env)
	for _, n := range names {
		rec := env.Manifest.Agents[n]
		repos := make([]string, 0, len(rec.Repos))
		for r := range rec.Repos {
			repos = append(repos, r)
		}
		sort.Strings(repos)
		fmt.Printf("%s\t%s\t%s\trepos=%s\n", n, rec.CLI, rec.Sandbox, strings.Join(repos, ","))
	}
}

func sortedAgents(env *envState) []string {
	names := make([]string, 0, len(env.Manifest.Agents))
	for n := range env.Manifest.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// snapshot computes every agent's state before anything acts on any of them
// — the observation-order rule the simulator surfaced.
// nodeLook is one node's place in a snapshot: the computed observation AND
// the facts it was computed from, so mechanical moves act on exactly what was
// seen — never on a re-probe.
type nodeLook struct {
	Obs   protocol.Observation
	Facts protocol.Facts
}

func snapshot(env *envState) map[string]nodeLook {
	entries := env.logEntries()
	pol := env.policy()
	now := clockNow().Unix()
	type probed struct {
		facts          protocol.Facts
		failed, missed bool
	}
	looks, anySeen := map[string]probed{}, false
	for name, rec := range env.Manifest.Agents {
		facts, failed, missed := probeAgentBound(rec)
		looks[name] = probed{facts, failed, missed}
		anySeen = anySeen || !failed
	}
	mem := loadObserver(env.Home)
	// What this look adds to a node's unseen time. Capped, so a gap between
	// two waits — the model thinking — is not counted as time spent looking.
	step := int64(0)
	if mem.LastLook > 0 {
		step = min(now-mem.LastLook, 2*int64(pol.PollSeconds))
	}
	mem.LastLook = now
	// When no node answers, the observer has learned about itself: its own
	// network, or a host too slow to answer in time. No node moves toward
	// "machine gone" on such a look. A fleet of one cannot tell the two apart,
	// and an outage longer than the provider wait bound is counted after all, so
	// a fabric that is truly gone is still concluded.
	selfBlind := !anySeen && len(looks) > 1
	if selfBlind {
		mem.AllBlind += step
	} else {
		mem.AllBlind = 0
	}
	count := !selfBlind || mem.AllBlind >= int64(pol.ProviderWaitSeconds)
	out := map[string]nodeLook{}
	for name, l := range looks {
		switch {
		case !l.failed:
			delete(mem.Unseen, name)
		case l.missed:
			// Silence within the bound is what a machine under load produces.
			// It moves no node toward "gone" (R65).
		case count:
			mem.Unseen[name] += max(step, 1)
		}
		out[name] = nodeLook{
			Obs:   protocol.Compute(l.facts, l.failed, protocol.Blind{Seconds: mem.Unseen[name]}, protocol.AcceptedFor(entries, name), pol, now),
			Facts: l.facts,
		}
	}
	mem.save(env.Home)
	return out
}

// observerMemory is what this observer knows about its own looks: when it last
// looked, and how long each node has gone unseen. It is not node state. It is
// the one thing that cannot be recomputed by looking, because its subject is a
// machine that does not answer, and it has to outlive a wait call because a
// wait is chunked to fit a tool call (PROTOCOL.md §8) and a lost machine stays
// lost for longer than that. Losing the file costs nothing but time: the count
// starts again.
type observerMemory struct {
	LastLook int64            `json:"lastLook"`
	AllBlind int64            `json:"allBlind,omitempty"`
	Unseen   map[string]int64 `json:"unseen,omitempty"`
}

func observerPath(home string) string {
	return filepath.Join(home, protocol.ChannelsDir, "locks", "observer.json")
}

func loadObserver(home string) *observerMemory {
	mem := &observerMemory{}
	if b, err := os.ReadFile(observerPath(home)); err == nil {
		_ = json.Unmarshal(b, mem)
	}
	if mem.Unseen == nil {
		mem.Unseen = map[string]int64{}
	}
	return mem
}

func (m *observerMemory) save(home string) {
	path := observerPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func printSnapshot(obs map[string]nodeLook, names []string) {
	fmt.Printf("%-14s %-16s %-8s %s\n", "node", "state", "dispatch", "detail")
	for _, n := range names {
		o := obs[n].Obs
		fmt.Printf("%-14s %-16s %-8s %s\n", n, o.State, o.Dispatch, o.Detail)
	}
}

func cmdObserve(env *envState) {
	printSnapshot(snapshot(env), sortedAgents(env))
}

// sendResult is what one send did: the dispatch it landed in, whether it
// opened it, whether it lost a mint race on the way (audit needs to tell a
// deliberate continuation from a repaired collision), and whether a turn was
// started (a continuation into a running turn starts nothing).
type sendResult struct {
	ID      string
	Opened  bool
	Raced   bool
	Started bool
}

// sendBody delivers one message to one agent: opens if closed, continues if
// open — the classification is computed, never chosen. A restart re-anchor is
// not sent from here; deliverPrepared marks that one.
//
// The sender says which of the two it expects, cont being a continuation, and
// a send the computation would land on the other side is refused with nothing
// delivered. Only the word "continued" or "opened", printed after delivery,
// used to tell the two apart. Seen live: a task meant as a new dispatch joined
// the one a seat was still working on, and a ruling meant for a dispatch
// opened a new one because the seat had replied a minute before. The id is the
// token an approval cites, so either way a step carried the wrong one.
func sendBody(env *envState, name, body string, cont bool) (sendResult, error) {
	var res sendResult
	rec, err := agentRec(env, name)
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(body) == "" {
		return res, errors.New("an empty dispatch would spend the agent's turn on nothing")
	}
	for attempt := 0; ; attempt++ {
		facts, failed := probeAgent(rec)
		if failed {
			return res, fmt.Errorf("cannot reach %s to deliver", name)
		}
		if res.ID, res.Opened, err = protocol.SendTarget(facts); err != nil {
			return res, err
		}
		if res.Opened == cont {
			return res, unexpectedSend(env, name, facts, res.ID, cont, res.Raced)
		}
		msgName := protocol.NextMsgName(facts.Msgs, res.ID, false)
		msgPath := protocol.InputDir + "/" + msgName
		out, putErr := sshOut(rec.Sandbox, protocol.PutMsgScript(msgPath), protocol.PutPayload(body))
		if putErr != nil {
			// A collision means a concurrent send claimed the name between our
			// listing and our write (the ID-mint TOCTOU). The winner's message
			// is in the listing now, so one re-probe reclassifies this send the
			// ordinary way. A continuation takes the next name; a send meant to
			// open a dispatch finds the winner's open and is refused above.
			if protocol.IsDeliveryCollision(out) && attempt == 0 {
				res.Raced = true
				continue
			}
			return res, fmt.Errorf("deliver %s to %s: %v", msgName, name, putErr)
		}
		// PROTOCOL.md §5: there is no "deliver into a running turn" case — the
		// move on node-working is nothing. A continuation delivered while this probe
		// saw a live driver therefore starts NO second turn: the running turn
		// (or, if it stops without replying, the wait ladder) picks the
		// message up. A newly opened dispatch always starts its turn — the
		// probe cannot have seen a driver for work that did not exist yet,
		// and skipping there would strand fresh work behind the ladder.
		if !res.Opened && facts.Drivers > 0 {
			return res, nil
		}
		if err = startTurn(env.Home, rec, msgPath, res.ID); err != nil {
			return res, err
		}
		res.Started = true
		return res, nil
	}
}

// unexpectedSend refuses a send that would land on the other side of the one
// rule from where its sender expects. Nothing has been delivered, and the
// message says where the send would have gone and what to do instead.
func unexpectedSend(env *envState, name string, facts protocol.Facts, id string, cont, raced bool) error {
	if !cont {
		why := fmt.Sprintf("a concurrent send opened %s on %s first", id, name)
		if !raced {
			o := protocol.Compute(facts, false, protocol.Blind{}, protocol.AcceptedFor(env.logEntries(), name), env.policy(), clockNow().Unix())
			why = fmt.Sprintf("%s is %s on %s, which is still open", name, o.State, id)
		}
		return fmt.Errorf("%s: this message would continue %s, not open a new dispatch. Nothing was delivered. Wait for its reply, or pass --continue to add this message to %s", why, id, id)
	}
	d := protocol.Current(facts.Msgs)
	if d == nil {
		return fmt.Errorf("%s has no dispatch to continue: this message would open %s. Nothing was delivered. Send it without --continue to open %s", name, id, id)
	}
	return fmt.Errorf("%s replied to %s, which closed it: this message would open %s, not continue %s. Nothing was delivered. Read the reply with `read %s`, then send without --continue to open a new dispatch", name, d.ID, id, d.ID, name)
}

// startTurn starts (or resumes) the agent's turn on the delivered message.
// Background (-b) with setsid so the runner survives this tool call, and
// --turn-timeout 0 so the only watchdog is the stall detector — the wall
// clock stops the watcher, never the turn, and a footer about a dead watcher
// is exactly the evidence this design refuses to consume.
var startTurn = func(home string, rec protocol.AgentRecord, msgPath, id string) error {
	tool := "cs-" + rec.CLI + "-remote"
	// Serialize turn starts per session. Two sends racing one agent both run
	// HERE, on the dispatcher's own machine, so a local flock is a complete
	// fence: the loser blocks until the winner's tool has registered the
	// session, then reads the marker and takes --resume instead of spawning a
	// duplicate --new session (seen live: raced sends accumulated one extra
	// guest session per round). Cross-process by construction — the racing
	// sends are separate processes.
	unlock, err := lockSession(home, rec.Session)
	if err != nil {
		return err
	}
	defer unlock()
	fresh := true
	for _, marker := range []string{rec.Session, rec.Session + ".token"} {
		if _, err := os.Stat(filepath.Join(home, ".cs-"+rec.CLI+"-remote-sessions", marker)); err == nil {
			fresh = false
			break
		}
	}
	// No -d, for the reason the host side gives: /workspace is a directory no member has.
	args := []string{tool, "-H", rec.Sandbox, "-b", "--turn-timeout", "0"}
	if fresh {
		args = append(args, "--new", "--name", rec.Session)
	} else {
		args = append(args, "--resume", rec.Session)
	}
	args = append(args, protocol.Trigger(msgPath, id, fresh))
	// Bounded like every other route to a teammate's machine. The launcher
	// crosses the network and waits for a CLI to come up, and one that hangs
	// would hang wait, and with it every node behind this one in the loop. A
	// launch that fails costs the node nothing: no turn ran, so no rung is
	// spent, and the next look carries the dispatch on (protocol.Charged).
	ctx, cancel := context.WithTimeout(context.Background(), turnStartBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "setsid", args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("start turn on %s: gave up after %s", rec.Sandbox, turnStartBound)
	}
	if err != nil {
		return fmt.Errorf("start turn on %s: %v: %s", rec.Sandbox, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// turnStartBound is how long starting one turn may take. The launcher returns
// once the turn is running in the background, which takes seconds on a healthy
// member and up to a cold CLI start on a slow one.
var turnStartBound = 3 * time.Minute

// lockSession takes an exclusive cross-process lock for one session's turn
// starts. The lock file lives under the campaign's own channels root (like
// the guard's real/ dir), never inside the adapter's sessions dir, whose
// contents belong to the tool.
func lockSession(home, session string) (func(), error) {
	dir := filepath.Join(home, protocol.ChannelsDir, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, session+".turnlock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock turn starts for %s: %v", session, err)
	}
	return func() { f.Close() }, nil // closing the fd releases the flock
}

func cmdSend(env *envState, args []string) error {
	body, flagged, err := readBody(args)
	if err != nil {
		return err
	}
	cont := false
	var rest []string
	for _, a := range flagged {
		if a == "--continue" {
			cont = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) < 1 {
		return errors.New("send needs an agent name")
	}
	if body == "" && len(rest) >= 2 {
		body = strings.Join(rest[1:], " ") // one-liner convenience
		rest = rest[:1]
	}
	res, err := sendBody(env, rest[0], body, cont)
	if err != nil {
		return err
	}
	line := rest[0] + "/" + res.ID
	if res.Opened {
		line += " opened"
	} else {
		line += " continued"
	}
	if res.Raced {
		line += " (a concurrent send took the message name first — delivered as the next message)"
	}
	if !res.Opened && !res.Started {
		line += "; a turn is already running — it, or the wait ladder, picks this up"
	}
	fmt.Println(line)
	return nil
}

func cmdRead(env *envState, args []string) error {
	if len(args) < 1 {
		return errors.New("read needs an agent name")
	}
	rec, err := agentRec(env, args[0])
	if err != nil {
		return err
	}
	var path string
	if len(args) > 1 {
		// The path is model-supplied and reaches a remote login shell: it must
		// be inert, not merely traversal-free (adversarial review, finding 3).
		if !safeReadPath(args[1]) {
			return errors.New("path may contain only letters, digits, . _ / - and no \"..\" segment — it must stay inside the output channel")
		}
		path = protocol.OutputDir + "/" + args[1]
	} else {
		facts, failed := probeAgent(rec)
		if failed {
			return fmt.Errorf("cannot reach %s", args[0])
		}
		d := protocol.Current(facts.Msgs)
		if d == nil {
			return fmt.Errorf("%s has no dispatch to have replied to", args[0])
		}
		if !facts.Replies[d.ID] {
			return fmt.Errorf("%s has not replied to %s yet", args[0], d.ID)
		}
		path = protocol.ReplyPath(d.ID)
	}
	out, err := sshOut(rec.Sandbox, "cat ~/"+path, "")
	if err != nil {
		return fmt.Errorf("read ~/%s on %s: %v", path, args[0], err)
	}
	os.Stdout.Write(out)
	return nil
}

func cmdAccept(env *envState, args []string) error {
	if len(args) != 1 {
		return errors.New("accept needs exactly one agent name")
	}
	rec, err := agentRec(env, args[0])
	if err != nil {
		return err
	}
	facts, failed := probeAgent(rec)
	if failed {
		return fmt.Errorf("cannot reach %s", args[0])
	}
	d := protocol.Current(facts.Msgs)
	if d == nil || !facts.Replies[d.ID] {
		return fmt.Errorf("%s has no reply to accept — accepting work that has not arrived is how backlogs get blamed on the fleet", args[0])
	}
	if protocol.AcceptedFor(env.logEntries(), args[0])[d.ID] {
		fmt.Printf("%s/%s was already accepted\n", args[0], d.ID)
		return nil
	}
	// Node-qualified: dispatch IDs are per-node sequences, so a bare "d002"
	// names a different dispatch on every agent (adversarial review, finding 2).
	if err := protocol.AppendLogLocal(env.Home, protocol.Entry{At: clockNow().UTC(), Kind: "accepted", Text: protocol.AcceptanceText(args[0], d.ID)}); err != nil {
		return err
	}
	fmt.Printf("accepted %s from %s — the agent is free for its next dispatch\n", d.ID, args[0])
	return nil
}

func cmdNote(env *envState, args []string) error {
	if len(args) < 1 || !protocol.LogKinds[args[0]] || args[0] == "accepted" || args[0] == "reported" {
		return errors.New("note needs a kind: plan or assessment (acceptances come from `accept`)")
	}
	body, _, err := readBody(args[1:])
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("an empty %s records nothing: pass --file <path> (\"-\" reads stdin)", args[0])
	}
	if err := protocol.AppendLogLocal(env.Home, protocol.Entry{At: clockNow().UTC(), Kind: args[0], Text: body}); err != nil {
		return err
	}
	fmt.Printf("recorded %s\n", args[0])
	return nil
}

func cmdRestart(env *envState, args []string) error {
	if len(args) != 1 {
		return errors.New("restart needs exactly one agent name")
	}
	msg, err := doRestart(env, args[0])
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// doRestart is rung two of the ladder: drop the session, forget it, and
// re-anchor a fresh one against the still-open dispatch by mechanical replay.
func doRestart(env *envState, name string) (string, error) {
	rec, err := agentRec(env, name)
	if err != nil {
		return "", err
	}
	facts, failed := probeAgent(rec)
	if failed {
		return "", fmt.Errorf("cannot reach %s — no recovery instrument reaches a machine that cannot be reached at all", name)
	}
	return doRestartPrepared(env, name, rec, facts)
}

// doRestartPrepared restarts against facts already observed — the form wait
// uses, so the move acts on the snapshot it decided from.
func doRestartPrepared(env *envState, name string, rec protocol.AgentRecord, facts protocol.Facts) (string, error) {
	d := protocol.Current(facts.Msgs)
	if d == nil || facts.Replies[d.ID] {
		return "", fmt.Errorf("%s has no open dispatch; a restart re-anchors an open dispatch, it does not assign work", name)
	}
	tool := "cs-" + rec.CLI + "-remote"
	// Kill the wedged session, then forget it so the next turn is --new.
	// Errors here are tolerated: a session that is already gone is the goal.
	_ = runSessionCmd(tool, "-H", rec.Sandbox, "--kill", rec.Session)
	_ = runSessionCmd(tool+"-forget", rec.Session)

	var names []string
	var mine []protocol.Msg
	for _, m := range facts.Msgs {
		if m.ID == d.ID {
			mine = append(mine, m)
		}
	}
	protocol.SortMsgs(mine)
	for _, m := range mine {
		names = append(names, m.Name)
	}
	if _, _, err := sendRestartPrepared(env, rec, facts, d.ID, protocol.RestartBody(d.ID, names)); err != nil {
		return "", err
	}
	return fmt.Sprintf("restarted %s — session dropped, re-anchored against %s", name, d.ID), nil
}

// sendRestartPrepared delivers a restart re-anchor (a .restart continuation)
// using facts already probed.
func sendRestartPrepared(env *envState, rec protocol.AgentRecord, facts protocol.Facts, id, body string) (string, bool, error) {
	return deliverPrepared(env, rec, facts, protocol.NextMsgName(facts.Msgs, id, true), id, body)
}

// sendResumePrepared carries a dispatch on without spending a rung: a .resume
// continuation, for a node whose provider refused its last turn or whose last
// turn never ran.
func sendResumePrepared(env *envState, rec protocol.AgentRecord, facts protocol.Facts, id, body string) (string, bool, error) {
	return deliverPrepared(env, rec, facts, protocol.NextResumeName(facts.Msgs, id), id, body)
}

// sendBodyPrepared delivers a plain continuation into a known dispatch using
// facts already probed.
func sendBodyPrepared(env *envState, rec protocol.AgentRecord, facts protocol.Facts, id, body string) (string, bool, error) {
	return deliverPrepared(env, rec, facts, protocol.NextMsgName(facts.Msgs, id, false), id, body)
}

func deliverPrepared(env *envState, rec protocol.AgentRecord, _ protocol.Facts, msgName, id, body string) (string, bool, error) {
	msgPath := protocol.InputDir + "/" + msgName
	if out, err := sshOut(rec.Sandbox, protocol.PutMsgScript(msgPath), protocol.PutPayload(body)); err != nil {
		// No retry here: this path acts on a wait snapshot, and a collision
		// means the world moved under it — the next poll recomputes.
		if protocol.IsDeliveryCollision(out) {
			return "", false, fmt.Errorf("deliver %s: the name already exists — a concurrent send won it; the next look reclassifies", msgName)
		}
		return "", false, fmt.Errorf("deliver %s: %v", msgName, err)
	}
	if err := startTurn(env.Home, rec, msgPath, id); err != nil {
		return "", false, err
	}
	return id, false, nil
}

// safeReadPath admits only inert path characters: the read path is
// model-supplied and reaches a remote login shell.
func safeReadPath(p string) bool {
	if p == "" || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '/', r == '-':
		default:
			return false
		}
	}
	return true
}

// cmdWait is the shape PROTOCOL.md §8 gives the orchestrator's wait, with the
// mechanical ladder of PROTOCOL.md §5 run inside it: block, poll every node
// into one snapshot, perform the moves in code, and return only when a
// judgment is due.
//
// A judgment is a WORLD EVENT that needs a decision code cannot make:
// node-replied and node-stuck, and those two only (SPEC.md R125). Both become
// true on their own — PROTOCOL.md §5's "world events", against which
// `dispatch`, `continue` and `accept` are dispatcher actions — and neither has
// a mechanical move left to run.
//
// node-free is deliberately NOT one, though it once was. The only arrows into
// it are `accept` and campaign start: a dispatcher action and an initial
// condition, both the orchestrator's own. Returning for it woke the model to
// report what the model itself had just done. Worse, it could not be waited
// out — a phased fleet parks seats on purpose, nothing on a node ever clears
// the state, and no instrument says "I am leaving this one free" — so wait
// returned on its first snapshot, before sleeping once, for the rest of the
// campaign. Live, a model met that, reasoned correctly that looping on it
// would spend a turn per poll, backgrounded a poller and ended its turn, which
// nothing can wake and the host reads as node-stopped.
//
// Nothing is hidden by leaving it out: printSnapshot names every node's state
// on every return, the elapsed chunk included, and the elapsed line names the
// free nodes outright. A free seat is reported exactly as often as before —
// only the short-circuit is gone.
func cmdWait(env *envState, args []string) error {
	chunk := protocol.DefaultWaitSeconds
	for i := 0; i < len(args); i++ {
		if args[i] == "--for" && i+1 < len(args) {
			_, _ = fmt.Sscanf(args[i+1], "%d", &chunk)
			i++
		}
	}
	chunk = protocol.WaitChunk(chunk)
	pol := env.policy()
	deadline := clockNow().Add(time.Duration(chunk) * time.Second)
	names := sortedAgents(env)
	var acted []string
	for {
		obs := snapshot(env)
		// Mechanical moves act on THIS snapshot's facts — no re-probe, no
		// reclassification. Re-probing let a reply landing mid-cycle turn a
		// continue into a fabricated new dispatch (adversarial review, finding
		// 4). Delivering into the snapshot's known-open dispatch is safe either
		// way: a reply that raced us has closed it, and the reply check
		// precedes everything on the next look.
		var judgment, free, known, refused []string
		resumed := false
		entries := env.logEntries()
		for _, n := range names {
			o := obs[n].Obs
			switch o.State {
			case protocol.StateRefused:
				refused = append(refused, n)
				// Members refused together must not come back together: that is
				// the load that was refused. One per look, so a fleet returns over
				// minutes. The rest are due and are taken on the looks that follow.
				if o.NextMove != "resume" || resumed {
					continue
				}
				resumed = true
				rec := env.Manifest.Agents[n]
				if _, _, err := sendResumePrepared(env, rec, obs[n].Facts, o.Dispatch, protocol.ResumeBody(o.Dispatch, true)); err != nil {
					acted = append(acted, fmt.Sprintf("resume %s FAILED: %v", n, err))
				} else {
					acted = append(acted, fmt.Sprintf("resumed %s (%s) after its provider's refusal, no rung spent", n, o.Dispatch))
				}
			case protocol.StateStopped:
				rec := env.Manifest.Agents[n]
				switch o.NextMove {
				case "resume":
					if _, _, err := sendResumePrepared(env, rec, obs[n].Facts, o.Dispatch, protocol.ResumeBody(o.Dispatch, false)); err != nil {
						acted = append(acted, fmt.Sprintf("resume %s FAILED: %v", n, err))
					} else {
						acted = append(acted, fmt.Sprintf("resumed %s (%s): its last turn never ran, no rung spent", n, o.Dispatch))
					}
				case "continue":
					if _, _, err := sendBodyPrepared(env, rec, obs[n].Facts, o.Dispatch, protocol.ContinueBody(o.Dispatch)); err != nil {
						acted = append(acted, fmt.Sprintf("continue %s FAILED: %v", n, err))
					} else {
						acted = append(acted, fmt.Sprintf("continued %s (%s)", n, o.Dispatch))
					}
				case "restart":
					if msg, err := doRestartPrepared(env, n, rec, obs[n].Facts); err != nil {
						acted = append(acted, fmt.Sprintf("restart %s FAILED: %v", n, err))
					} else {
						acted = append(acted, msg)
					}
				}
			case protocol.StateReplied:
				judgment = append(judgment, n)
			case protocol.StateStuck:
				// A judgment ends a wait once. A stuck node this orchestrator
				// has been told about stays in every snapshot and ends no wait.
				if protocol.ReportedFor(entries, n)[protocol.ReportedKey(o.Dispatch)] {
					known = append(known, n)
				} else {
					judgment = append(judgment, n)
				}
			case protocol.StateFree:
				// Named on the elapsed line, never a reason to return.
				free = append(free, n)
			}
		}
		if len(judgment) > 0 {
			fmt.Printf("wait returned — a judgment is due.\n\n")
			printSnapshot(obs, names)
			if len(acted) > 0 {
				fmt.Printf("\nrecovery performed while waiting:\n  %s\n", strings.Join(acted, "\n  "))
			}
			fmt.Println()
			for _, n := range judgment {
				switch obs[n].Obs.State {
				case protocol.StateReplied:
					fmt.Printf("%s replied to %s: read it (`cs-campaign-member read %s`), then `accept %s` or send rework with `send %s --file <path>`.\n", n, obs[n].Obs.Dispatch, n, n, n)
				case protocol.StateStuck:
					fmt.Printf("%s is stuck (%s): it can take no further work — every item assigned to it is unreachable. Decide what becomes of its queue, and record an assessment. You are told this once: later waits show %s as stuck and do not return for it.\n", n, obs[n].Obs.Detail, n)
					_ = protocol.AppendLogLocal(env.Home, protocol.Entry{At: clockNow().UTC(), Kind: "reported",
						Text: protocol.AcceptanceText(n, protocol.ReportedKey(obs[n].Obs.Dispatch))})
				}
			}
			return nil
		}
		if clockNow().After(deadline) {
			// "nothing actionable" would be a lie in a chunk that ran the
			// ladder (seen live: it printed above its own recovery report).
			what := "nothing actionable"
			if len(acted) > 0 {
				what = "recovery performed, no judgment due yet"
			}
			// Same lie, the other way: an idle teammate under "nothing
			// actionable" reads as a fleet with nothing left to give. A free
			// node is assignable — it is just not a judgment, because the
			// orchestrator is what freed it. Name it here, do not return for it.
			if len(refused) > 0 {
				// The orchestrator is a model, and this line is all it is told. A
				// refused node looks like lost time, and the costly mistake is to
				// give its work to someone else while it waits.
				what += fmt.Sprintf("; %s: the provider refused the last turn, and wait is holding and will carry the same session on — leave it, and do not reassign its work", strings.Join(refused, ", "))
			}
			if len(known) > 0 {
				what += fmt.Sprintf("; %s still stuck, as already reported", strings.Join(known, ", "))
			}
			if len(free) > 0 {
				is := "is"
				if len(free) > 1 {
					is = "are"
				}
				what += fmt.Sprintf("; %s %s free (assign with `send`, or leave free)", strings.Join(free, ", "), is)
			}
			fmt.Printf("wait chunk elapsed (%ds) — %s; call `wait` again.\n\n", chunk, what)
			printSnapshot(obs, names)
			if len(acted) > 0 {
				fmt.Printf("\nrecovery performed while waiting:\n  %s\n", strings.Join(acted, "\n  "))
			}
			return nil
		}
		clockSleep(protocol.PollInterval(pol))
	}
}

func cmdFetch(env *envState, args []string, push bool) error {
	if len(args) < 1 {
		return errors.New("need an agent name")
	}
	rec, err := agentRec(env, args[0])
	if err != nil {
		return err
	}
	var repo string
	if len(args) > 1 {
		repo = args[1]
	} else {
		repos := make([]string, 0, len(rec.Repos))
		for r := range rec.Repos {
			repos = append(repos, r)
		}
		sort.Strings(repos)
		if len(repos) == 0 {
			return fmt.Errorf("%s has no repository", args[0])
		}
		repo = repos[0]
	}
	branch, ok := rec.Repos[repo]
	if !ok {
		return fmt.Errorf("%s does not hold repo %q", args[0], repo)
	}
	dir := filepath.Join(env.Home, repo)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("you have no local clone of %q to %s against — judging a teammate's repo requires the campaign profile to give the orchestrator that repo too; until then, `read %s` shows its reply and output channel only", repo, map[bool]string{true: "push", false: "fetch"}[push], args[0])
	}
	if push {
		out, err := gitCmd(dir, "push", rec.Sandbox+":"+repo, "HEAD:refs/campaign/orchestrator")
		if err != nil {
			return fmt.Errorf("push: %v: %s", err, strings.TrimSpace(string(out)))
		}
		fmt.Println("refs/campaign/orchestrator")
		return nil
	}
	ref := fmt.Sprintf("refs/remotes/campaign/%s/%s", args[0], repo)
	out, err := gitCmd(dir, "fetch", rec.Sandbox+":"+repo, branch+":"+ref)
	if err != nil {
		return fmt.Errorf("fetch: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Tree-differs-from-base, printed with the ref: an empty branch presented
	// as delivered work buys a wrong acceptance.
	if base, ok := rec.Bases[repo]; ok && base != "" {
		bt, e1 := gitOut(dir, "rev-parse", base+"^{tree}")
		ht, e2 := gitOut(dir, "rev-parse", ref+"^{tree}")
		if e1 == nil && e2 == nil {
			if bt == ht {
				fmt.Printf("%s — WARNING: tree identical to base; whatever this branch claims, it delivers no change\n", ref)
				return nil
			}
			fmt.Printf("%s — tree differs from base (real changes present)\n", ref)
			return nil
		}
	}
	fmt.Println(ref)
	return nil
}
