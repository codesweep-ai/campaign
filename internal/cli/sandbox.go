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
}

// defaultWaitDelay is how long a cancelled cs-sandbox call may hold its pipes
// open before they are closed out from under whatever still owns them.
const defaultWaitDelay = 5 * time.Second

func (s sandboxCLI) waitDelay() time.Duration {
	if s.WaitDelay != 0 {
		return s.WaitDelay
	}
	return defaultWaitDelay
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
func (s sandboxCLI) probeMember(ctx context.Context, member model.Member) (protocol.Facts, bool) {
	out, err := s.memberOutput(ctx, member, protocol.ProbeScript(member.CLI))
	if err != nil {
		return protocol.Facts{}, true
	}
	return protocol.ParseProbe(string(out)), false
}

// putMemberFile materialises one $HOME-relative file inside a member.
func (s sandboxCLI) putMemberFile(ctx context.Context, member model.Member, path, content string) error {
	return s.memberRun(ctx, member.Ref, protocol.PutFileScript(path, content))
}

func selectedAPIKey(member model.Member) string {
	for _, key := range member.Profile.Auth.APIKeyFromEnv {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			return key
		}
	}
	return ""
}

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

// observedTurnConfig reads the model and reasoning effort a member's CLI
// actually answered on, out of the transcript that CLI wrote for its most
// recent session — the evidence half of a declaration, riding the readback
// turn that already ran.
func (s sandboxCLI) observedTurnConfig(ctx context.Context, member model.Member) (models, efforts []string, supported bool, err error) {
	if !turnConfigReadable(member.CLI) {
		return nil, nil, false, nil
	}
	var glob, effortKey string
	switch member.CLI {
	case "claude":
		glob, effortKey = `"$HOME"/.cs-claude/projects/*/*.jsonl`, "effort"
	default:
		// Codex rollouts sit one directory level deeper than claude transcripts.
		glob, effortKey = `"$HOME"/.cs-codex/sessions/*/*/*/*.jsonl`, "reasoning_effort"
	}
	command := fmt.Sprintf(`f=$(ls -t %s 2>/dev/null | head -1); [ -n "$f" ] || exit 0; `+
		`grep -ho '"model":"[^"]*"' "$f" | sed 's/.*:"//;s/"$//' | sort -u | sed 's/^/model=/'; `+
		`grep -ho '"%s":"[^"]*"' "$f" | sed 's/.*:"//;s/"$//' | sort -u | sed 's/^/effort=/'`, glob, effortKey)
	out, err := s.memberOutput(ctx, member, command)
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
	parts := []string{
		mkGuestDirs(guestConfigDir, guestInputDir, guestOutputDir, guestRepliesDir, guestSourceDir, protocol.GuestBinDir),
		putGuestFile(guestMemberJSON, string(encodedDoc)+"\n"),
		putGuestFile(guestOrientationFile, orientation),
	}
	if seed := inputs.seedCommand(member); seed != "" {
		parts = append(parts, seed)
	}
	if err := s.memberRun(ctx, member.Ref, strings.Join(parts, " && ")); err != nil {
		return err
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
	command := putGuestFile(guestManifestJSON, string(encoded)+"\n") + " && " + guardInstallLoop
	return s.memberRun(ctx, orchestrator.Ref, command)
}

// guardInstallLoop moves each real cs-*-remote tool aside and symlinks its
// name to the guest binary's guard face. Idempotent; -status is no
// longer guarded because nothing ships or consumes it in this design.
var guardInstallLoop = `for f in claude codex opencode; do for s in "" -forget -output -sessions; do t="cs-$f-remote$s"; ` +
	`if [ -e "$HOME/` + guestBinDir + `/$t" ] && [ ! -L "$HOME/` + guestBinDir + `/$t" ]; ` +
	`then mkdir -p "$HOME/` + guestRealToolsDir + `" && mv "$HOME/` + guestBinDir + `/$t" "$HOME/` + guestRealToolsDir + `/$t"; fi; ` +
	`if [ -e "$HOME/` + guestRealToolsDir + `/$t" ]; then ln -sf cs-campaign-member "$HOME/` + guestBinDir + `/$t"; fi; done; done`

func (s sandboxCLI) run(ctx context.Context, args ...string) error {
	if s.Dry {
		return nil
	}
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	cmd.Stdin = os.Stdin
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
	Gateway int    `json:"gateway,omitempty"`
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
	// Declared environment first, so a --env the profile asked for is present
	// whichever way the auth branch below goes.
	for _, e := range member.Profile.Env {
		args = append(args, "--env", e)
	}
	if key := selectedAPIKey(member); key != "" {
		args = append(args, "--env", key)
	} else {
		for _, login := range member.Profile.Auth.InheritAgentLogin {
			args = append(args, "--inherit-agent-login", login)
		}
	}
	return args
}
