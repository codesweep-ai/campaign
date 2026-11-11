package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
)

// fanoutText is the instruction a codex or opencode seat reads when its fanout
// is native, from the file its CLI loads with every session. It says the same
// as the orientation's "Parallel work" section, where the CLI's configuration
// has no switch for the behaviour and only its instructions can ask for it.
// Claude Code has the switch, `ultracode`, and reads no copy of this. The text
// carries no apostrophe: it travels to the guest inside single quotes.
const fanoutText = "## Parallel work\n\n" +
	"When a task has independent parts, such as several files to read or several claims to check, " +
	"run one sub-agent per part at the same time, then combine what they return. " +
	"Prefer a read-only sub-agent for reading and checking. " +
	"Run the sub-agents as one foreground batch, and wait for the whole batch inside your turn. " +
	"Never run one in the background, and never end your turn while one runs."

// fanoutCapText is the one sentence a cap adds to the instruction, and the
// whole of it for an opencode seat with a cap and no fanout: OpenCode has no
// setting that bounds how many sub-agents run at once.
func fanoutCapText(limit int) string {
	return fmt.Sprintf("Run at most %d sub-agents at once.", limit)
}

// fanoutInstruction is the text a seat's instruction file holds.
func fanoutInstruction(p model.MemberProfile) string {
	switch {
	case p.Fanout == model.FanoutNative && p.FanoutMax > 0:
		return fanoutText + " " + fanoutCapText(p.FanoutMax) + "\n"
	case p.Fanout == model.FanoutNative:
		return fanoutText + "\n"
	case p.FanoutMax > 0:
		return "## Parallel work\n\n" + fanoutCapText(p.FanoutMax) + "\n"
	}
	return ""
}

// The markers around the block applyFanoutConfig writes into a codex seat's
// AGENTS.md, so a resumed create replaces the block rather than stacking one.
const (
	fanoutBlockOpen  = "<!-- cs-campaign fanout -->"
	fanoutBlockClose = "<!-- /cs-campaign fanout -->"
)

// applyFanoutConfig writes the member's declared fanout into the configuration
// its own CLI reads, beside applyModelConfig and for the same reason: the turn
// drivers take a fixed flag set, so the guest's own config is the only channel.
// A seat with nothing declared is sent no command at all.
//
// Each CLI keeps the switch somewhere else. Claude Code reads `ultracode` and
// permission rules from its settings file and two concurrency caps from its
// environment. Codex spawns on an instruction in its AGENTS.md and reads
// `agents.*` from config.toml. OpenCode reads `permission.task` and an
// `instructions` file from opencode.json. The opencode write stays in the
// dialect the seeded file uses, with `permission` singular: OpenCode 2 reads a
// file in that dialect whole, and drops its keys once a key from its own
// dialect appears beside them.
func (s sandboxCLI) applyFanoutConfig(ctx context.Context, member model.Member) error {
	p := member.Profile
	if p.Fanout == "" && p.FanoutMax == 0 {
		return nil
	}
	var steps []string
	switch member.CLI {
	case "claude":
		if p.Fanout != "" {
			// Merged into the settings file rather than written over it: the
			// image seeds the permissions and hooks the seat runs under, and
			// `ultracode` is one key among them. A deny rule is added once,
			// so a resumed create converges.
			script := `import json,pathlib,sys; p=pathlib.Path.home()/".cs-claude/settings.json"; ` +
				`c=json.loads(p.read_text()) if p.exists() else {}; mode=sys.argv[1]; ` +
				`c.__setitem__("ultracode",True) if mode=="native" else None; ` +
				`d=c.setdefault("permissions",{}).setdefault("deny",[]) if mode=="none" else []; ` +
				`[d.append(t) for t in ("Agent","Workflow") if t not in d]; ` +
				`p.write_text(json.dumps(c,indent=2)+"\n")`
			steps = append(steps, fmt.Sprintf(`mkdir -p "$HOME/.cs-claude" && python3 -c '%s' %s`, script, shellArgs([]string{p.Fanout})))
		}
		if p.FanoutMax > 0 {
			// The same file and the same rewrite as the model and effort: two
			// assignments of one name would leave the last one to win.
			n := strconv.Itoa(p.FanoutMax)
			lines := []string{"CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS=" + n, "CLAUDE_CODE_WORKFLOW_MAX_CONCURRENT_AGENTS=" + n}
			steps = append(steps, fmt.Sprintf(`mkdir -p "$HOME/.cs-claude" && touch "$HOME/.cs-claude/env" && sed -i -e '/^CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS=/d' -e '/^CLAUDE_CODE_WORKFLOW_MAX_CONCURRENT_AGENTS=/d' "$HOME/.cs-claude/env" && printf '%%s\n' %s >> "$HOME/.cs-claude/env"`, shellArgs(lines)))
		}
	case "codex":
		if p.Fanout == model.FanoutNative {
			// Codex spawns sub-agents only when asked by the user, a skill or
			// an AGENTS.md, and the one in CODEX_HOME is read by every session.
			// The block is replaced, not appended, between its markers.
			script := `import pathlib,re,sys; p=pathlib.Path.home()/".cs-codex/AGENTS.md"; t=p.read_text() if p.exists() else ""; ` +
				`t=re.sub(r"\n*` + fanoutBlockOpen + `.*?` + fanoutBlockClose + `\n?","",t,flags=re.S); ` +
				`p.write_text((t.rstrip("\n")+"\n\n" if t.strip() else "")+"` + fanoutBlockOpen + `\n"+sys.argv[1]+"` + fanoutBlockClose + `\n")`
			steps = append(steps, fmt.Sprintf(`mkdir -p "$HOME/.cs-codex" && python3 -c '%s' %s`, script, shellArgs([]string{fanoutInstruction(p)})))
		}
		var lines []string
		if p.Fanout == model.FanoutNone {
			// Both generations of the feature, since the model catalog turns
			// the second on by itself for some models.
			lines = append(lines, "agents.enabled = false", "features.multi_agent_v2.enabled = false")
		}
		if p.FanoutMax > 0 {
			lines = append(lines, fmt.Sprintf("agents.max_concurrent_threads_per_session = %d", p.FanoutMax))
		}
		if len(lines) > 0 {
			// Dotted keys at the top of the file, for the reason applyModelConfig
			// gives: a key after a [table] header belongs to that table.
			steps = append(steps, fmt.Sprintf(`mkdir -p "$HOME/.cs-codex" && cd "$HOME/.cs-codex" && touch config.toml && { grep -v -e '^agents\.' -e '^features\.multi_agent_v2\.' config.toml || :; } > config.toml.tmp && { printf '%%s\n' %s; cat config.toml.tmp; } > config.toml && rm -f config.toml.tmp`, shellArgs(lines)))
		}
	case "opencode":
		script := `import json,pathlib,sys; p=pathlib.Path.home()/".cs-opencode/opencode.json"; ` +
			`c=json.loads(p.read_text()) if p.exists() else {}; mode=sys.argv[1]; text=sys.argv[2]; ` +
			`f=pathlib.Path.home()/".cs-opencode/fanout.md"; ` +
			`f.write_text(text) if text else None; ` +
			`i=c.setdefault("instructions",[]) if text else []; ` +
			`i.append(str(f)) if text and str(f) not in i else None; ` +
			`c.setdefault("permission",{}).__setitem__("task","deny") if mode=="none" else None; ` +
			`p.write_text(json.dumps(c,indent=2)+"\n")`
		steps = append(steps, fmt.Sprintf(`mkdir -p "$HOME/.cs-opencode" && python3 -c '%s' %s`, script, shellArgs([]string{p.Fanout, fanoutInstruction(p)})))
	default:
		return nil
	}
	return s.memberRun(ctx, member.Ref, "umask 077 && "+strings.Join(steps, " && "))
}
