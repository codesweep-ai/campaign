package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// sandboxCLI shells out to a versioned cs-sandbox binary and to the installed
// cs-<cli>-remote tool families. Out/Err, when set, capture subprocess output
// that would otherwise stream to the terminal; interactive commands (ssh)
// always keep the real TTY.
type sandboxCLI struct {
	Bin      string
	Dry      bool
	Out, Err io.Writer
	// WaitDelay bounds the wait for a cancelled command's pipes. Zero means
	// defaultWaitDelay; tests set it small.
	WaitDelay time.Duration
	// ProbeBound bounds one probe round trip. Zero means defaultProbeBound;
	// tests set it small.
	ProbeBound time.Duration
	// ReadyBound bounds the wait for a freshly created guest to accept its
	// first command. Zero means defaultReadyBound; tests set it small.
	ReadyBound time.Duration
	// ReadyInterval spaces those probes. Zero means readyProbeInterval; tests
	// set it small.
	ReadyInterval time.Duration
}

// defaultWaitDelay is how long a cancelled cs-sandbox call may hold its pipes
// open before they are closed out from under whatever still owns them.
const defaultWaitDelay = 5 * time.Second

// defaultProbeBound is how long one probe of one node may take.
//
// A failed probe is already a fact the caller knows what to do with: it counts
// towards blindProbes, and a run of them is the conclusion that the machine is
// gone. A probe that never RETURNS is not a failure, so without a bound it
// defeats that contract completely — the observer blocks on the one wedged
// member it exists to notice, and every rung computed from a state stops being
// reached. Measured on a smoke run that sat in one observeCampaign call for
// seven minutes and then failed at the archive, which is the only path here
// that already had a deadline.
//
// Well under the default pollSeconds: the probe is one round trip running a
// short shell script, so anything near this bound is a member in trouble
// rather than a member being slow.
const defaultProbeBound = 20 * time.Second

// defaultReadyBound is how long a member's guest may take to accept its first
// command, measured from the moment cs-sandbox reports the machine created.
//
// `cs-sandbox create` returns when the machine exists and its port forward is
// up, which is earlier than sshd inside the guest accepting a connection. Every
// provisioning step after it execs into that guest and none of them retries, so
// one refused connection failed the whole create with every machine in the
// fleet already provisioned. The SECOND member is the one that loses that race:
// it boots beside a machine that is already running and taking the host's CPU
// and disk, while the orchestrator booted against an idle host.
//
// Generous on purpose, and bounded on purpose. A guest that is merely slow
// costs a few seconds here. A guest that never arrives is a create the operator
// wants to watch fail rather than watch hang.
const defaultReadyBound = 2 * time.Minute

// readyProbeInterval spaces the readiness probes. Short, because the point is
// to lose as little time as possible once the guest is actually up.
const readyProbeInterval = 2 * time.Second

func (s sandboxCLI) waitDelay() time.Duration {
	if s.WaitDelay != 0 {
		return s.WaitDelay
	}
	return defaultWaitDelay
}

func (s sandboxCLI) probeBound() time.Duration {
	if s.ProbeBound != 0 {
		return s.ProbeBound
	}
	return defaultProbeBound
}

func (s sandboxCLI) readyBound() time.Duration {
	if s.ReadyBound != 0 {
		return s.ReadyBound
	}
	return defaultReadyBound
}

func (s sandboxCLI) readyInterval() time.Duration {
	if s.ReadyInterval != 0 {
		return s.ReadyInterval
	}
	return readyProbeInterval
}

func newSandbox() sandboxCLI {
	bin := os.Getenv("CS_SANDBOX_BIN")
	if bin == "" {
		bin = "cs-sandbox"
	}
	return sandboxCLI{Bin: bin}
}

func (s sandboxCLI) stdout() io.Writer {
	if s.Out != nil {
		return s.Out
	}
	return os.Stdout
}

func (s sandboxCLI) stderr() io.Writer {
	if s.Err != nil {
		return s.Err
	}
	return os.Stderr
}

func agentTool(cli, suffix string) (string, error) {
	if model.ValidAdapterCLI(cli) {
		return "cs-" + cli + "-remote" + suffix, nil
	}
	return "", fmt.Errorf("unsupported CLI %q", cli)
}

func runTool(ctx context.Context, stdout, stderr io.Writer, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}
	return nil
}

// hostSessionFresh reports whether the host has never driven this member's
// session: the remote tools keep session records under
// ~/.cs-<cli>-remote-sessions, and their absence means --new.
func hostSessionFresh(member model.Member) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	for _, marker := range []string{member.Session.Name, member.Session.Name + ".token"} {
		if _, err := os.Stat(filepath.Join(home, ".cs-"+member.CLI+"-remote-sessions", marker)); err == nil {
			return false
		}
	}
	return true
}

// startTurn starts (or resumes) a member's turn on a delivered message, from
// the host. Background with --turn-timeout 0: the drivers' wall clock stops
// the watcher and never the turn, and this design consumes no watcher verdict
// — the reply artifact is the completion signal, the stall watchdog the hang
// detector.
func (s sandboxCLI) startTurn(ctx context.Context, member model.Member, msgPath, id string) error {
	tool, err := agentTool(member.CLI, "")
	if err != nil {
		return err
	}
	// No -d. A turn runs in the member's $HOME, which is where the remote tool lands and
	// where the clone sits one level down at $HOME/<repo-name>. cs-campaign named
	// /workspace here for a directory no member has: tmux starts a session in its own
	// working directory when -c points nowhere, so every turn has always run in $HOME
	// while the argument said otherwise. opencode is the one adapter that binds a session
	// to the path it is handed, and it fails every prompt on that session rather than
	// falling back — so the argument has to be right, not merely tolerated.
	args := []string{"-H", member.Ref, "-b", "--turn-timeout", "0"}
	if hostSessionFresh(member) {
		args = append(args, "--new", "--name", member.Session.Name)
	} else {
		args = append(args, "--resume", member.Session.Name)
	}
	args = append(args, protocol.Trigger(msgPath, id, hostSessionFresh(member)))
	// The tool's launch banner is diagnostics, not command output.
	return runTool(ctx, s.stderr(), s.stderr(), tool, args...)
}

// killSession kills a member's warm session; the first half of a restart.
func (s sandboxCLI) killSession(ctx context.Context, member model.Member) error {
	tool, err := agentTool(member.CLI, "")
	if err != nil {
		return err
	}
	return runTool(ctx, s.stderr(), s.stderr(), tool, "-H", member.Ref, "--kill", member.Session.Name)
}

// forgetSession discards the session record so the next turn is --new; the
// second half of a restart. Tolerant: an already-absent session is the goal.
func (s sandboxCLI) forgetSession(ctx context.Context, member model.Member) {
	tool, err := agentTool(member.CLI, "-forget")
	if err != nil {
		return
	}
	_ = exec.CommandContext(ctx, tool, member.Session.Name).Run()
}

// sessionLog streams the member's raw session transcript — human forensics
// only, never a state input.
func (s sandboxCLI) sessionLog(ctx context.Context, w io.Writer, member model.Member) error {
	tool, err := agentTool(member.CLI, "-output")
	if err != nil {
		return err
	}
	return runTool(ctx, w, s.stderr(), tool, member.Session.Name, "--full")
}

// probeMember is the one round trip of PROTOCOL.md §6, from the host: every
// fact about one node, or a probe failure — a fact about the observation,
// not the node.
//
// Bounded, so a wedged member becomes that probe failure rather than an
// indefinite wait in whatever is observing.
func (s sandboxCLI) probeMember(ctx context.Context, member model.Member) (protocol.Facts, bool) {
	facts, failed, _ := s.probeMemberDeadline(ctx, member)
	return facts, failed
}

// probeMemberDeadline is probeMember, and also says whether the failure was the
// member missing its deadline rather than answering with one.
//
// The two are different facts. An error comes back from a machine that
// answered, and a run of them is the evidence that it is gone. A deadline is
// silence, and silence is what a machine under load produces as readily as a
// machine that has died — a starved guest misses this bound while making steady
// progress. Counting one as the other let a bounded probe report "machine gone"
// about a member that was merely busy, which is the same mistake as calling a
// slow node stalled.
func (s sandboxCLI) probeMemberDeadline(ctx context.Context, member model.Member) (protocol.Facts, bool, bool) {
	pctx, cancel := context.WithTimeout(ctx, s.probeBound())
	defer cancel()
	out, err := s.memberOutput(pctx, member, protocol.ProbeScript(member.CLI))
	if err != nil {
		return protocol.Facts{}, true, errors.Is(pctx.Err(), context.DeadlineExceeded)
	}
	return protocol.ParseProbe(string(out)), false, false
}

// putMemberFile materialises one $HOME-relative file inside a member.
func (s sandboxCLI) putMemberFile(ctx context.Context, member model.Member, path, content string) error {
	return s.memberRunStdin(ctx, member.Ref, protocol.PutFileScript(path), protocol.PutPayload(content))
}

func selectedAPIKey(member model.Member) string {
	for _, key := range member.Profile.Auth.APIKeyEnvs() {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			return key
		}
	}
	return ""
}

// prepareAuth finishes what an environment key cannot do on its own: two
// adapters read their credential from a file rather than the environment, so
// the guest writes one from the variable it was given.
//
// Only apiKeyFromEnv needs it. A lent or copied grant is placed by cs-sandbox
// in the shape that adapter's own sign-in would have written, which is the
// whole point of a loan: the agent runs the code path it takes when signed in.
func (s sandboxCLI) prepareAuth(ctx context.Context, member model.Member) error {
	key := selectedAPIKey(member)
	if key == "" {
		return nil
	}
	// In both branches the key is expanded only inside the guest (it is present
	// there via create's `--env <NAME>`). It never appears in host argv, campaign
	// state, or output.
	switch member.CLI {
	case "codex":
		// Codex stores its isolated login under ~/.cs-codex.
		command := fmt.Sprintf(`printf %%s "${%s}" | CODEX_HOME="$HOME/.cs-codex" codex login --with-api-key`, key)
		return s.memberRun(ctx, member.Ref, command)
	case "opencode":
		// opencode picks provider env vars up directly; the cs-opencode wrapper
		// sources ~/.cs-opencode/env, making the key durable for warm tmux TUIs
		// whose shells do not inherit create-time env.
		command := fmt.Sprintf(`umask 077 && mkdir -p "$HOME/.cs-opencode" && printf 'export %s=%%q\n' "${%s}" > "$HOME/.cs-opencode/env"`, key, key)
		return s.memberRun(ctx, member.Ref, command)
	}
	return nil
}

// applyModelConfig writes the member's declared model and effort into the config
// its own CLI reads. Each adapter differs, and none of them is an argument the
// campaign can pass at dispatch: the turn drivers take a fixed flag set, so the
// guest's own config is the only channel.
func (s sandboxCLI) applyModelConfig(ctx context.Context, member model.Member) error {
	declaredModel, effort := member.Profile.Model, member.Profile.Effort
	if declaredModel == "" && effort == "" {
		return nil
	}
	var command string
	switch member.CLI {
	case "claude":
		// cs-claude sources ~/.cs-claude/env with `set -a`, so plain assignments
		// are exported into the CLI. Rewritten rather than appended: create is
		// resumable, and a second pass must not leave two assignments behind.
		var lines []string
		if declaredModel != "" {
			lines = append(lines, "ANTHROPIC_MODEL="+declaredModel)
		}
		if effort != "" {
			lines = append(lines, "CLAUDE_CODE_EFFORT_LEVEL="+effort)
		}
		command = fmt.Sprintf(`umask 077 && mkdir -p "$HOME/.cs-claude" && touch "$HOME/.cs-claude/env" && sed -i -e '/^ANTHROPIC_MODEL=/d' -e '/^CLAUDE_CODE_EFFORT_LEVEL=/d' "$HOME/.cs-claude/env" && printf '%%s\n' %s >> "$HOME/.cs-claude/env"`, shellArgs(lines))
	case "codex":
		// Prepended, not appended: a bare key after a [table] header belongs to
		// that table, so appending model_reasoning_effort lands it in whatever
		// section codex wrote last and the config fails to load.
		var lines []string
		if declaredModel != "" {
			lines = append(lines, fmt.Sprintf("model = %q", declaredModel))
		}
		if effort != "" {
			lines = append(lines, fmt.Sprintf("model_reasoning_effort = %q", effort))
		}
		command = fmt.Sprintf(`umask 077 && mkdir -p "$HOME/.cs-codex" && cd "$HOME/.cs-codex" && touch config.toml && grep -v -e '^model = ' -e '^model_reasoning_effort = ' config.toml > config.toml.tmp && { printf '%%s\n' %s; cat config.toml.tmp; } > config.toml && rm -f config.toml.tmp`, shellArgs(lines))
	case "opencode":
		// opencode resolves its model from opencode.json; effort attaches to a
		// NAMED MODEL: provider.<provider>.models.<model>.options.reasoningEffort.
		command = fmt.Sprintf(`umask 077 && mkdir -p "$HOME/.cs-opencode" && python3 -c '`+
			`import json,pathlib,sys; p=pathlib.Path.home()/".cs-opencode/opencode.json"; `+
			`c=json.loads(p.read_text()) if p.exists() else {}; slug=sys.argv[1]; effort=sys.argv[2]; `+
			`c["model"]=slug if slug else c.get("model"); `+
			`prov,_,mid=(slug or c.get("model","")).partition("/"); `+
			`m=c.setdefault("provider",{}).setdefault(prov,{}).setdefault("models",{}).setdefault(mid,{}) if effort and mid else None; `+
			`m.setdefault("options",{}).__setitem__("reasoningEffort",effort) if m is not None else None; `+
			`p.write_text(json.dumps(c,indent=2)+"\n")' %s`, shellArgs([]string{declaredModel, effort}))
	default:
		return nil
	}
	return s.memberRun(ctx, member.Ref, command)
}

// shellArgs single-quotes each value for the guest shell. The values are
// modelToken-constrained, so there is no embedded quote to escape.
func shellArgs(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, "'"+v+"'")
	}
	return strings.Join(quoted, " ")
}

// turnConfigReadable reports whether this adapter leaves a transcript naming the
// model it answered on. opencode keeps sessions in a SQLite store instead, so a
// declaration there is applied and recorded but cannot be confirmed.
func turnConfigReadable(cli string) bool {
	return cli == "claude" || cli == "codex"
}

// turnConfigCommand is the shell that reads a member's transcripts, kept apart
// from the call so the one thing that has gone wrong here is testable.
//
// It scans EVERY transcript. `head -1` is the shape to keep out: a member can
// hold more than one session file, and the newest is not always the one that
// answered.
func turnConfigCommand(cli string) string {
	glob, effortKey := `"$HOME"/.cs-codex/sessions/*/*/*/*.jsonl`, "reasoning_effort"
	if cli == "claude" {
		// Codex rollouts sit one directory level deeper than claude transcripts.
		glob, effortKey = `"$HOME"/.cs-claude/projects/*/*.jsonl`, "effort"
	}
	// `set --` rather than `ls`, so a name with a space cannot split, and
	// `[ -e "$1" ]` because an unmatched glob comes back as the pattern itself.
	return fmt.Sprintf(`set -- %s; [ -e "$1" ] || exit 0; `+
		`grep -ho '"model":"[^"]*"' "$@" | sed 's/.*:"//;s/"$//' | sort -u | sed 's/^/model=/'; `+
		`grep -ho '"%s":"[^"]*"' "$@" | sed 's/.*:"//;s/"$//' | sort -u | sed 's/^/effort=/'`, glob, effortKey)
}

// observedTurnConfig reads the model and reasoning effort a member's CLI
// actually answered on, out of the transcripts that CLI wrote — the evidence
// half of a declaration, riding the readback turn that already ran.
//
// EVERY transcript the member has, not the newest one. A CLI can have more than
// one session file open, and the newest is not always the one that answered:
// measured on a CI runner where the member wrote a second session three seconds
// after the first, and the check read that one before it had named its model.
// The turn it was confirming sat in the older file, complete. Reading only the
// newest reported a member answering on no model at all, and failed a readback
// on a campaign that was correctly configured.
//
// Reading them all is safe because a member is built for one campaign and
// destroyed with it, so every transcript under that home belongs to this run.
// It also matches what the caller already tolerates: a declaration has to be
// PRESENT among the models named, never the only one, because a CLI names a
// subagent and a summariser beside the turn.
func (s sandboxCLI) observedTurnConfig(ctx context.Context, member model.Member) (models, efforts []string, supported bool, err error) {
	if !turnConfigReadable(member.CLI) {
		return nil, nil, false, nil
	}
	out, err := s.memberOutput(ctx, member, turnConfigCommand(member.CLI))
	if err != nil {
		return nil, nil, true, err
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		switch value := strings.TrimSpace(line); {
		case strings.HasPrefix(value, "model="):
			models = append(models, strings.TrimPrefix(value, "model="))
		case strings.HasPrefix(value, "effort="):
			efforts = append(efforts, strings.TrimPrefix(value, "effort="))
		}
	}
	return models, efforts, true, nil
}

// configureChannels creates a member's channels, seeds the operator's
// briefing files, writes the rendered orientation and member.json, and
// installs the guest binary. One command per round trip where possible:
// every round trip is an ssh into a booting guest.
func (s sandboxCLI) configureChannels(ctx context.Context, campaign *model.Campaign, member model.Member, inputs campaignInputs) error {
	orientation, memberDoc, err := buildOrientation(campaign, member, inputs)
	if err != nil {
		return err
	}
	encodedDoc, err := json.MarshalIndent(memberDoc, "", "  ")
	if err != nil {
		return err
	}
	dirs := []string{guestConfigDir, guestInputDir, guestOutputDir, guestRepliesDir, guestSourceDir, protocol.GuestBinDir}
	if member.Role == "orchestrator" {
		dirs = append(dirs, guestRolesDir)
	}
	if err := s.memberRun(ctx, member.Ref, mkGuestDirs(dirs...)); err != nil {
		return err
	}
	// One exec per file. The content rides on stdin, so a member's seeded set
	// has no size ceiling: joining these into one script put the whole payload
	// in a single argv entry, which execve refuses past MAX_ARG_STRLEN.
	files := []guestFile{
		{guestMemberJSON, string(encodedDoc) + "\n"},
		{guestOrientationFile, orientation},
	}
	files = append(files, inputs.seedFiles(member)...)
	for _, f := range files {
		if err := s.putMemberFile(ctx, member, f.Path, f.Content); err != nil {
			return fmt.Errorf("seed %s: %w", f.Path, err)
		}
	}
	return s.installGuestBinary(ctx, member.Ref)
}

// installGuestBinary streams the embedded cs-campaign-member over stdin — the
// payload is megabytes, and argv (the base64 path every other file takes)
// caps at ARG_MAX.
func (s sandboxCLI) installGuestBinary(ctx context.Context, ref string) error {
	bin, err := guestBinary()
	if err != nil {
		return err
	}
	if s.Dry {
		return nil
	}
	cmd := exec.CommandContext(ctx, s.Bin, "exec", ref, "sh", "-lc",
		"cat > ~/"+protocol.GuestBinDir+"/"+protocol.GuestBinName+" && chmod 755 ~/"+protocol.GuestBinDir+"/"+protocol.GuestBinName)
	cmd.Stdin = bytes.NewReader(bin)
	cmd.Stdout, cmd.Stderr = s.stdout(), s.stderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install guest binary into %s: %w", ref, err)
	}
	return nil
}

// configureOrchestrator installs the campaign manifest and arms the family
// guard. Every address in the manifest is a GUEST address: the orchestrator
// reaches agents across the group's own network, where members resolve by
// bare name.
func (s sandboxCLI) configureOrchestrator(ctx context.Context, campaign *model.Campaign) error {
	manifest := protocol.Manifest{
		Campaign: campaign.Name, Network: campaign.Network,
		Policy: campaign.Policy,
		Agents: map[string]protocol.AgentRecord{},
	}
	var orchestrator *model.Member
	for i := range campaign.Members {
		member := &campaign.Members[i]
		if member.Role == "orchestrator" {
			orchestrator = member
			continue
		}
		repos := map[string]string{}
		bases := map[string]string{}
		for _, repo := range member.Profile.Repos {
			repos[repoGuestName(repo)] = member.Branch
			if repo.ResolvedCommit != "" {
				bases[repoGuestName(repo)] = repo.ResolvedCommit
			}
		}
		var snapshots []string
		for _, snap := range member.Profile.Snapshots {
			name := snap.Name
			if name == "" {
				name = filepath.Base(snap.Path)
			}
			snapshots = append(snapshots, name)
		}
		manifest.Agents[member.Name] = protocol.AgentRecord{
			CLI: member.CLI, Sandbox: member.Sandbox, Session: member.Session.Name,
			Repos: repos, Bases: bases, Snapshots: snapshots,
		}
	}
	if orchestrator == nil {
		return errors.New("campaign has no orchestrator")
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := s.putMemberFile(ctx, *orchestrator, guestManifestJSON, string(encoded)+"\n"); err != nil {
		return err
	}
	return s.memberRun(ctx, orchestrator.Ref, guardInstallLoop)
}

// guardInstallLoop moves each real cs-*-remote tool aside and symlinks its
// name to the guest binary's guard face. Idempotent; -status is no
// longer guarded because nothing ships or consumes it in this design.
var guardInstallLoop = `for f in claude codex opencode; do for s in "" -forget -output -sessions; do t="cs-$f-remote$s"; ` +
	`if [ -e "$HOME/` + guestBinDir + `/$t" ] && [ ! -L "$HOME/` + guestBinDir + `/$t" ]; ` +
	`then mkdir -p "$HOME/` + guestRealToolsDir + `" && mv "$HOME/` + guestBinDir + `/$t" "$HOME/` + guestRealToolsDir + `/$t"; fi; ` +
	`if [ -e "$HOME/` + guestRealToolsDir + `/$t" ]; then ln -sf cs-campaign-member "$HOME/` + guestBinDir + `/$t"; fi; done; done`

func (s sandboxCLI) run(ctx context.Context, args ...string) error {
	return s.runStdin(ctx, "", args...)
}

// runStdin runs cs-sandbox with payload on stdin, or with the host's own stdin
// when payload is empty — `ssh` with no arguments attaches a terminal and needs
// it.
func (s sandboxCLI) runStdin(ctx context.Context, payload string, args ...string) error {
	if s.Dry {
		return nil
	}
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	cmd.Stdin = os.Stdin
	if payload != "" {
		cmd.Stdin = strings.NewReader(payload)
	}
	cmd.Stdout, cmd.Stderr = s.stdout(), s.stderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", s.Bin, strings.Join(args, " "), err)
	}
	return nil
}

func (s sandboxCLI) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	// Cancelling the context kills cs-sandbox, and nothing else: the ssh it
	// spawned, and the command running inside the member, survive holding the
	// write end of this pipe. Output() waits on the pipe rather than on the
	// process, so without a delay a cancelled call still blocks forever, which
	// is the whole failure a deadline was meant to prevent.
	cmd.WaitDelay = s.waitDelay()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", s.Bin, strings.Join(args, " "), err)
	}
	return out, nil
}

// memberRun, refOutput and memberOutput run one SHELL COMMAND LINE in a member.
// `cs-sandbox exec` takes an argv and execs it the way podman/kubectl do, so
// the shell is asked for rather than assumed; `-l` because these rely on what
// the guest's profile puts on PATH.
func (s sandboxCLI) memberRun(ctx context.Context, ref, command string) error {
	return s.run(ctx, "exec", ref, "sh", "-lc", command)
}

// memberRunStdin is memberRun with a payload the guest command reads from
// stdin. It exists so a file's contents never enter argv, where a single entry
// is capped well below the total argument space.
func (s sandboxCLI) memberRunStdin(ctx context.Context, ref, command, payload string) error {
	return s.runStdin(ctx, payload, "exec", ref, "sh", "-lc", command)
}

// awaitMemberReady blocks until the guest runs a trivial command, or until the
// bound elapses. It is the one retry in the provisioning path, and it is a
// readiness probe rather than a retry of the caller's command: retrying the
// real steps would re-run work whose failure means something else entirely.
//
// The probes are silent. A refused connection during boot is expected rather
// than newsworthy, and printing each one would bury the create's own output.
// A wait long enough to notice says so once, so a slow boot does not read as a
// hang, and the error carries the last failure when the bound runs out.
func (s sandboxCLI) awaitMemberReady(ctx context.Context, ref string) error {
	if s.Dry {
		return nil
	}
	deadline := time.Now().Add(s.readyBound())
	said := false
	for attempt := 1; ; attempt++ {
		probe, cancel := context.WithTimeout(ctx, s.probeBound())
		cmd := exec.CommandContext(probe, s.Bin, "exec", ref, "sh", "-lc", "true")
		cmd.WaitDelay = s.waitDelay()
		err := cmd.Run()
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s accepted no command in %s (%d attempts): %w", ref, s.readyBound(), attempt, err)
		}
		if !said {
			said = true
			fmt.Fprintf(s.stderr(), "waiting for %s to accept commands\n", ref)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.readyInterval()):
		}
	}
}

func (s sandboxCLI) refOutput(ctx context.Context, ref, command string) ([]byte, error) {
	return s.output(ctx, "exec", ref, "sh", "-lc", command)
}

func (s sandboxCLI) memberOutput(ctx context.Context, member model.Member, command string) ([]byte, error) {
	return s.refOutput(ctx, member.Ref, command)
}

// Instance is `cs-sandbox inspect <ref> --json`: the resolved record for one
// sandbox. Only the fields cs-campaign consumes are declared.
type Instance struct {
	Ref   string `json:"ref"`
	Name  string `json:"name"`
	Group string `json:"group"`
	IP    string `json:"ip"`
	Repos []struct {
		Dir    string `json:"dir"`
		Source string `json:"source"`
		Branch string `json:"branch"`
	} `json:"repos"`
}

// inspect reads a member's resolved record: the branch a clone actually got
// is learned rather than predicted.
func (s sandboxCLI) inspect(ctx context.Context, ref string) (Instance, error) {
	var inst Instance
	if s.Dry {
		return inst, nil
	}
	cmd := exec.CommandContext(ctx, s.Bin, "inspect", ref, "--json")
	output, err := cmd.Output()
	if err != nil {
		return inst, fmt.Errorf("%s inspect %s --json: %w", s.Bin, ref, err)
	}
	if err = json.Unmarshal(output, &inst); err != nil {
		return inst, fmt.Errorf("%s inspect %s --json: %w", s.Bin, ref, err)
	}
	return inst, nil
}

// Group is one row of `cs-sandbox group ls --json`.
type Group struct {
	Name    string `json:"name"`
	Network string `json:"network"`
	Members int    `json:"members"`
}

// groups reads the machine-readable group inventory; its existence is the
// capability gate for a group-aware cs-sandbox.
func (s sandboxCLI) groups(ctx context.Context) ([]Group, error) {
	if s.Dry {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, s.Bin, "group", "ls", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s group ls --json: %w", s.Bin, err)
	}
	var groups []Group
	if err = json.Unmarshal(output, &groups); err != nil {
		return nil, fmt.Errorf("%s group ls --json: %w", s.Bin, err)
	}
	return groups, nil
}

// removeGroup reclaims a campaign's group and everything it owns, verified
// from the inventory rather than by matching error prose.
func (s sandboxCLI) removeGroup(ctx context.Context, group string) error {
	if s.Dry {
		return nil
	}
	groups, err := s.groups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name == group {
			cmd := exec.CommandContext(ctx, s.Bin, "group", "rm", group)
			if out, rmErr := cmd.CombinedOutput(); rmErr != nil {
				return fmt.Errorf("%s group rm %s: %w: %s", s.Bin, group, rmErr, strings.TrimSpace(string(out)))
			}
			return nil
		}
	}
	return nil
}

func (s sandboxCLI) list(ctx context.Context) ([]model.Sandbox, error) {
	out, err := s.output(ctx, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var sandboxes []model.Sandbox
	err = json.Unmarshal(out, &sandboxes)
	return sandboxes, err
}

func (s sandboxCLI) version(ctx context.Context) (string, error) {
	out, err := s.output(ctx, "version")
	if err != nil {
		return "", err
	}
	reported := strings.TrimSpace(string(out))
	if reported == "" {
		return "", fmt.Errorf("%s version returned empty output", s.Bin)
	}
	return reported, nil
}

// createArgs builds the cs-sandbox create invocation. The stall threshold —
// the turn drivers' own idle definition — travels here as create-time env,
// landing in the seeded ~/.ssh/environment that sshd applies before every
// command shell: supported surface, fixed at create, per-member.
func createArgs(campaign *model.Campaign, member model.Member) []string {
	args := []string{"create", member.Sandbox, "--engine", campaign.Engine, "--type", "agent", "--group", campaign.Group, "--yolo"}
	if member.Solo {
		args = append(args, "--solo")
	}
	if member.Profile.Resources.CPUS > 0 {
		args = append(args, "--cpus", strconv.Itoa(member.Profile.Resources.CPUS))
	}
	if member.Profile.Resources.MemoryMiB > 0 {
		args = append(args, "--mem", strconv.Itoa(member.Profile.Resources.MemoryMiB))
	}
	for _, repo := range member.Profile.Repos {
		spec := repo.Path
		if repo.ResolvedCommit != "" {
			spec += "@" + repo.ResolvedCommit
		}
		if repo.Name != "" {
			spec += ":" + repo.Name
		}
		args = append(args, "--repo", spec)
	}
	for _, snapshot := range member.Profile.Snapshots {
		spec := snapshot.Path
		if snapshot.Name != "" {
			spec += ":" + snapshot.Name
		}
		args = append(args, "--snapshot", spec)
	}
	if member.StallSeconds > 0 {
		args = append(args, "--env", fmt.Sprintf("CS_%s_STALL_SECS=%d", strings.ToUpper(member.CLI), member.StallSeconds))
	}
	// The poll override travels the same way, when the host has one. It is not
	// profile configuration: it must not reach the campaign ID or a member's
	// recorded policy, or a cassette stops matching. See protocol.PollInterval.
	if v := os.Getenv("CS_CAMPAIGN_POLL_SECONDS"); v != "" {
		args = append(args, "--env", "CS_CAMPAIGN_POLL_SECONDS="+v)
	}
	// And the wait-chunk override, on identical terms — it bounds how long the
	// orchestrator's wait blocks, and the orchestrator is a member, so the
	// number has to be inside the guest to have any effect at all. See
	// protocol.WaitChunk.
	if v := os.Getenv("CS_CAMPAIGN_WAIT_SECONDS"); v != "" {
		args = append(args, "--env", "CS_CAMPAIGN_WAIT_SECONDS="+v)
	}
	// Declared environment first, so a --env the profile asked for is present
	// whichever way the auth branch below goes.
	for _, e := range member.Profile.Env {
		args = append(args, "--env", e)
	}
	return append(args, authArgs(member)...)
}

// authArgs grants the member its one model credential.
//
// A lent grant hands the sandbox a loan token and leaves the credential on the
// host, so nothing inside can read, refresh or revoke it, and destroying the
// member ends the loan. Copying it in is the declared alternative, and the
// campaign says which through the resolved mode rather than through a flag
// name a profile spells.
//
// The ladder is first-available, not additive: an environment key, else a
// host-held key, else a login. Passing a key beside a login would leave the
// agent spending the key while the profile named a subscription.
//
// The verb is read off the member rather than off the spelling it was written
// in: a plain grant takes the campaign's, and both arrive here resolved.
func authArgs(member model.Member) []string {
	if key := selectedAPIKey(member); key != "" {
		return []string{"--env", key}
	}
	auth := member.Profile.Auth
	keyFlag, loginFlag := "--lend-api-key", "--lend-agent-login"
	if auth.Credentials == model.CredentialInherit {
		keyFlag, loginFlag = "--inherit-api-key", "--inherit-agent-login"
	}
	if providers := auth.APIKeys(); len(providers) > 0 {
		return repeatFlag(keyFlag, providers)
	}
	return repeatFlag(loginFlag, auth.AgentLogins())
}

// repeatFlag pairs one flag with each of its values, the form cs-sandbox takes
// for a repeatable one.
func repeatFlag(flag string, values []string) []string {
	var args []string
	for _, v := range values {
		args = append(args, flag, v)
	}
	return args
}
