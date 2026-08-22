package main

// Dispatcher verbs: the orchestrator's side of the protocol. Every address
// here is a BARE in-group name — the group's DNS serves bare names only, and
// the guest ssh config offers the tier key only to undotted hosts.

import (
	"context"
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

var sshOut = func(host, command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), memberCmdBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, command)
	cmd.WaitDelay = 2 * time.Second
	return cmd.Output()
}

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

// probeAgent is the one round trip of §6: every fact about one node, or a
// probe failure — which is a fact about the observation, not the node.
func probeAgent(rec protocol.AgentRecord) (protocol.Facts, bool) {
	out, err := sshOut(rec.Sandbox, protocol.ProbeScript(rec.CLI))
	if err != nil {
		return protocol.Facts{}, true
	}
	return protocol.ParseProbe(string(out)), false
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

func snapshot(env *envState, blind map[string]int) map[string]nodeLook {
	entries := env.logEntries()
	pol := env.policy()
	now := time.Now().Unix()
	out := map[string]nodeLook{}
	for name, rec := range env.Manifest.Agents {
		facts, failed := probeAgent(rec)
		if failed {
			blind[name]++
		} else {
			blind[name] = 0
		}
		out[name] = nodeLook{
			Obs:   protocol.Compute(facts, failed, blind[name], protocol.AcceptedFor(entries, name), pol, now),
			Facts: facts,
		}
	}
	return out
}

func printSnapshot(obs map[string]nodeLook, names []string) {
	fmt.Printf("%-14s %-16s %-8s %s\n", "node", "state", "dispatch", "detail")
	for _, n := range names {
		o := obs[n].Obs
		fmt.Printf("%-14s %-16s %-8s %s\n", n, o.State, o.Dispatch, o.Detail)
	}
}

func cmdObserve(env *envState) {
	blind := map[string]int{}
	obs := snapshot(env, blind)
	printSnapshot(obs, sortedAgents(env))
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
func sendBody(env *envState, name, body string) (sendResult, error) {
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
		d := protocol.Current(facts.Msgs)
		res.Opened = false
		switch {
		case d == nil || facts.Replies[d.ID]:
			if res.ID, err = protocol.NextDispatchID(facts.Msgs); err != nil {
				return res, err
			}
			res.Opened = true
		default:
			res.ID = d.ID
		}
		msgName := protocol.NextMsgName(facts.Msgs, res.ID, false)
		msgPath := protocol.InputDir + "/" + msgName
		out, putErr := sshOut(rec.Sandbox, protocol.PutMsgScript(msgPath, body))
		if putErr != nil {
			// A collision means a concurrent send claimed the name between our
			// listing and our write (the ID-mint TOCTOU). The winner's message
			// is in the listing now, so one re-probe reclassifies this send the
			// ordinary way — usually into a continuation of the winner's
			// dispatch, which is exactly what the one rule says a send while
			// open is.
			if protocol.IsDeliveryCollision(out) && attempt == 0 {
				res.Raced = true
				continue
			}
			return res, fmt.Errorf("deliver %s to %s: %v", msgName, name, putErr)
		}
		// §1.3: there is no "deliver into a running turn" case — the move on
		// node-working is nothing. A continuation delivered while this probe
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
	args := []string{tool, "-H", rec.Sandbox, "-d", "/workspace", "-b", "--turn-timeout", "0"}
	if fresh {
		args = append(args, "--new", "--name", rec.Session)
	} else {
		args = append(args, "--resume", rec.Session)
	}
	args = append(args, protocol.Trigger(msgPath, id, fresh))
	cmd := exec.Command("setsid", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start turn on %s: %v: %s", rec.Sandbox, err, strings.TrimSpace(string(out)))
	}
	return nil
}

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
	body, rest, err := readBody(args)
	if err != nil {
		return err
	}
	if len(rest) < 1 {
		return errors.New("send needs an agent name")
	}
	if body == "" && len(rest) >= 2 {
		body = strings.Join(rest[1:], " ") // one-liner convenience
		rest = rest[:1]
	}
	res, err := sendBody(env, rest[0], body)
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
		line += " (lost a mint race — delivered into the winner's open dispatch)"
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
	out, err := sshOut(rec.Sandbox, "cat ~/"+path)
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
	if err := protocol.AppendLogLocal(env.Home, protocol.Entry{At: time.Now().UTC(), Kind: "accepted", Text: protocol.AcceptanceText(args[0], d.ID)}); err != nil {
		return err
	}
	fmt.Printf("accepted %s from %s — the agent is free for its next dispatch\n", d.ID, args[0])
	return nil
}

func cmdNote(env *envState, args []string) error {
	if len(args) < 1 || !protocol.LogKinds[args[0]] || args[0] == "accepted" {
		return errors.New("note needs a kind: plan or assessment (acceptances come from `accept`)")
	}
	body, _, err := readBody(args[1:])
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("an empty %s records nothing: pass --file <path> (\"-\" reads stdin)", args[0])
	}
	if err := protocol.AppendLogLocal(env.Home, protocol.Entry{At: time.Now().UTC(), Kind: args[0], Text: body}); err != nil {
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
	return deliverPrepared(env, rec, facts, id, body, true)
}

// sendBodyPrepared delivers a plain continuation into a known dispatch using
// facts already probed.
func sendBodyPrepared(env *envState, rec protocol.AgentRecord, facts protocol.Facts, id, body string) (string, bool, error) {
	return deliverPrepared(env, rec, facts, id, body, false)
}

func deliverPrepared(env *envState, rec protocol.AgentRecord, facts protocol.Facts, id, body string, restart bool) (string, bool, error) {
	msgName := protocol.NextMsgName(facts.Msgs, id, restart)
	msgPath := protocol.InputDir + "/" + msgName
	if out, err := sshOut(rec.Sandbox, protocol.PutMsgScript(msgPath, body)); err != nil {
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

// cmdWait is §8's shape with §3.6's extension: block, poll every node into
// one snapshot, perform the mechanical moves in code, and return only when
// the next arrow is a judgment — node-replied, node-stuck, or node-free.
func cmdWait(env *envState, args []string) error {
	chunk := 240
	for i := 0; i < len(args); i++ {
		if args[i] == "--for" && i+1 < len(args) {
			_, _ = fmt.Sscanf(args[i+1], "%d", &chunk)
			i++
		}
	}
	pol := env.policy()
	deadline := time.Now().Add(time.Duration(chunk) * time.Second)
	blind := map[string]int{}
	names := sortedAgents(env)
	var acted []string
	for {
		obs := snapshot(env, blind)
		// Mechanical moves act on THIS snapshot's facts — no re-probe, no
		// reclassification. Re-probing let a reply landing mid-cycle turn a
		// continue into a fabricated new dispatch (adversarial review, finding
		// 4). Delivering into the snapshot's known-open dispatch is safe either
		// way: a reply that raced us has closed it, and the reply check
		// precedes everything on the next look.
		var judgment []string
		for _, n := range names {
			o := obs[n].Obs
			switch o.State {
			case protocol.StateStopped:
				rec := env.Manifest.Agents[n]
				switch o.NextMove {
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
			case protocol.StateReplied, protocol.StateStuck, protocol.StateFree:
				judgment = append(judgment, n)
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
					fmt.Printf("%s is stuck (%s): it can take no further work — every item assigned to it is unreachable. Decide what becomes of its queue, and record an assessment.\n", n, obs[n].Obs.Detail)
				case protocol.StateFree:
					fmt.Printf("%s is free: dispatch its next task with `send %s --file <path>`, or leave it free if nothing remains.\n", n, n)
				}
			}
			return nil
		}
		if time.Now().After(deadline) {
			// "nothing actionable" would be a lie in a chunk that ran the
			// ladder (seen live: it printed above its own recovery report).
			if len(acted) > 0 {
				fmt.Printf("wait chunk elapsed (%ds) — recovery performed, no judgment due yet; call `wait` again.\n\n", chunk)
			} else {
				fmt.Printf("wait chunk elapsed (%ds) — nothing actionable; call `wait` again.\n\n", chunk)
			}
			printSnapshot(obs, names)
			if len(acted) > 0 {
				fmt.Printf("\nrecovery performed while waiting:\n  %s\n", strings.Join(acted, "\n  "))
			}
			return nil
		}
		time.Sleep(protocol.PollInterval(pol))
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
