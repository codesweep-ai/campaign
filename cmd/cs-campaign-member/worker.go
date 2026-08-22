package main

// Worker verbs: what every member — agent and orchestrator alike — can do.
// A worker reads its dispatch and writes its reply; that is the whole
// protocol from a worker's chair.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// localMsgs lists this machine's own input channel as protocol messages.
func localMsgs(home string) ([]protocol.Msg, error) {
	dir := filepath.Join(home, protocol.InputDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no input channel at ~/%s: %v", protocol.InputDir, err)
	}
	var msgs []protocol.Msg
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if m, ok := protocol.ParseMsgName(e.Name(), info.ModTime().Unix()); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

func cmdInbox(env *envState) error {
	msgs, err := localMsgs(env.Home)
	if err != nil {
		return err
	}
	d := protocol.Current(msgs)
	if d == nil {
		fmt.Println("no dispatch has been opened on this member yet")
		return nil
	}
	var mine []protocol.Msg
	for _, m := range msgs {
		if m.ID == d.ID {
			mine = append(mine, m)
		}
	}
	protocol.SortMsgs(mine)
	replied := false
	if _, err := os.Stat(filepath.Join(env.Home, protocol.ReplyPath(d.ID))); err == nil {
		replied = true
	}
	fmt.Printf("dispatch %s — %d message(s)", d.ID, len(mine))
	if replied {
		fmt.Printf(" — REPLIED (closed; the next message you receive opens a new dispatch)\n")
	} else {
		fmt.Printf(" — OPEN\n")
	}
	for _, m := range mine {
		b, err := os.ReadFile(filepath.Join(env.Home, protocol.InputDir, m.Name))
		if err != nil {
			return err
		}
		fmt.Printf("\n--- ~/%s/%s ---\n%s\n", protocol.InputDir, m.Name, strings.TrimRight(string(b), "\n"))
	}
	if !replied {
		fmt.Printf("\nYou owe a reply: when the work is concluded, run\n  cs-campaign-member reply --file <path-to-your-summary>\nEnding your turn without one reads as stopped, and you will be continued.\n")
	}
	return nil
}

func cmdCheckInputs(env *envState) {
	missing := 0
	for _, in := range env.Member.Inputs {
		p := filepath.Join(env.Home, protocol.InputDir, in)
		if _, err := os.Stat(p); err != nil {
			fmt.Printf("MISSING %s\n", in)
			missing++
		}
	}
	if missing == 0 {
		fmt.Printf("all %d listed inputs present\n", len(env.Member.Inputs))
	}
}

func cmdReply(env *envState, args []string) error {
	body, rest, err := readBody(args)
	if err != nil {
		return err
	}
	r := protocol.Reply{Phase: "done", At: time.Now().UTC()}
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--needs-input":
			r.Phase = "needs-input"
		case "--outcome":
			if i+1 >= len(rest) {
				return errors.New("--outcome needs a value")
			}
			i++
			r.Outcome = rest[i]
		case "--unmet":
			if i+1 >= len(rest) {
				return errors.New("--unmet needs a value")
			}
			i++
			r.Unmet = append(r.Unmet, rest[i])
		default:
			if body == "" && !strings.HasPrefix(rest[i], "--") {
				body = rest[i] // one-liner convenience; files are the documented form
			} else {
				return fmt.Errorf("unknown reply flag %q", rest[i])
			}
		}
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("an empty reply says nothing: pass --file <path> (\"-\" reads stdin)")
	}
	r.Note = body

	msgs, err := localMsgs(env.Home)
	if err != nil {
		return err
	}
	d := protocol.Current(msgs)
	if d == nil {
		return errors.New("no dispatch is open on this member — there is nothing to reply to")
	}
	if _, err := os.Stat(filepath.Join(env.Home, protocol.ReplyPath(d.ID))); err == nil {
		return fmt.Errorf("dispatch %s is already closed by its reply; the next message you receive opens a new one", d.ID)
	}
	r.Dispatch = d.ID

	// The mission reply carries the campaign's verdict; the vocabulary is
	// enforced here, in code, not in prose.
	if d.ID == protocol.MissionID {
		if err := protocol.ValidateMissionReply(r); err != nil {
			return err
		}
	} else if r.Outcome != "" {
		return fmt.Errorf("--outcome belongs to the mission reply (%s) only; this dispatch is %s", protocol.MissionID, d.ID)
	}

	// Tool-stamped branch evidence: the orchestrator judges a measurement,
	// not a claim. Failures are recorded, never fatal — a reply must always
	// be writable.
	for _, repo := range env.Member.Repos {
		r.Repos = append(r.Repos, repoState(env.Home, repo))
	}

	if err := protocol.WriteReplyLocal(env.Home, r); err != nil {
		return err
	}
	fmt.Printf("replied to %s — the dispatch is closed. You may finish your turn.\n", d.ID)
	return nil
}

// repoState measures one repository: tree-differs-from-base, never commit
// count.
func repoState(home string, ref protocol.RepoRef) protocol.RepoState {
	dir := filepath.Join(home, ref.Name)
	st := protocol.RepoState{Repo: ref.Name}
	head, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		st.Error = strings.TrimSpace(err.Error())
		return st
	}
	st.Head = head
	if dirty, err := gitOut(dir, "status", "--porcelain"); err == nil && dirty != "" {
		st.Dirty = true
	}
	if ref.Base != "" {
		baseTree, err1 := gitOut(dir, "rev-parse", ref.Base+"^{tree}")
		headTree, err2 := gitOut(dir, "rev-parse", "HEAD^{tree}")
		if err1 == nil && err2 == nil {
			st.TreeDiffersFromBase = baseTree != headTree
		} else {
			st.Error = "base tree unreadable"
		}
	}
	return st
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
