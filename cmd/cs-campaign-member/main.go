// cs-campaign-member is the guest-side CLI of the dispatch protocol: the one
// binary installed into every campaign member. Workers use it to read their
// dispatch and reply; the orchestrator additionally drives its agents with it.
//
// Role gating is real here, not doctrine: dispatcher verbs check the role in
// member.json and refuse on an agent — and the binary simply does not contain
// host verbs (create, destroy, archive, pin), which is the stronger half of
// the guarantee.
//
// Invoked under a cs-*-remote name (via the symlinks) it becomes the
// family guard; see guard.go.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

func main() {
	// Guard mode: the move-aside symlinks every cs-*-remote name to
	// this binary; basename decides which face is running.
	if base := filepath.Base(os.Args[0]); strings.HasPrefix(base, "cs-") && base != protocol.GuestBinName {
		os.Exit(runGuard(base, os.Args[1:]))
	}
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(1)
	}
	verb, args := os.Args[1], os.Args[2:]
	switch verb {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	case "guard":
		// Hidden: exercised directly only by tests and the doctor probe.
		if len(args) < 1 {
			fatal("guard needs the tool name it is standing in for")
		}
		os.Exit(runGuard(args[0], args[1:]))
	}

	env, err := loadEnv()
	if err != nil {
		fatal("%v", err)
	}
	switch verb {
	// Worker verbs — every member.
	case "inbox":
		err = cmdInbox(env)
	case "check-inputs":
		cmdCheckInputs(env)
	case "reply":
		err = cmdReply(env, args)
	// Dispatcher verbs — orchestrator only.
	case "list", "observe", "send", "read", "restart", "accept", "note", "wait", "fetch", "push":
		if err := gate(env, verb); err != nil {
			fatal("%v", err)
		}
		switch verb {
		case "list":
			cmdList(env)
		case "observe":
			cmdObserve(env)
		case "send":
			err = cmdSend(env, args)
		case "read":
			err = cmdRead(env, args)
		case "restart":
			err = cmdRestart(env, args)
		case "accept":
			err = cmdAccept(env, args)
		case "note":
			err = cmdNote(env, args)
		case "wait":
			err = cmdWait(env, args)
		case "fetch":
			err = cmdFetch(env, args, false)
		case "push":
			err = cmdFetch(env, args, true)
		}
	default:
		usage(os.Stderr)
		fatal("unknown verb %q", verb)
	}
	if err != nil {
		fatal("%v", err)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `usage: cs-campaign-member <verb> [args]

Every member:
  inbox                         the current open dispatch: its ID and every message, in order
  check-inputs                  verify every file member.json lists under "inputs"; print any that are absent
  reply [--file F|-] [flags]    write the reply that closes your current dispatch (body from F, "-" = stdin)
      mission reply only:       --outcome campaign-met|campaign-converged|campaign-exhausted|campaign-blocked
                                --unmet "<item>" (repeatable; required unless campaign-met)
      any reply:                --needs-input   (the fleet cannot act on this without outside help)

Orchestrator only:
  list                          the roster: every teammate, its CLI, repos
  observe                       every agent's state, computed now, in one snapshot
  send <agent> --file F|-       dispatch or continue: opens if closed, continues if open — you never classify
  read <agent> [path]           the agent's reply to its current dispatch, or a file from its output channel
  restart <agent>               drop its session and re-anchor it against its open dispatch
  accept <agent>                record its current reply as accepted (frees the agent)
  note plan|assessment --file F|-   append to your log; re-planning is another plan entry
  wait [--for SECS]             block; recovery runs itself, returns when a judgment is due
  fetch <agent> [repo]          fetch its branch to refs/remotes/campaign/<agent>/<repo>
  push <agent> [repo]           push HEAD to it at refs/campaign/orchestrator
`)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "cs-campaign-member: "+format+"\n", a...)
	os.Exit(1)
}

// gate enforces the role boundary in code: dispatcher verbs exist only for
// the orchestrator. An agent replies; it does not dispatch.
func gate(env *envState, verb string) error {
	if env.Member.Role != "orchestrator" {
		return fmt.Errorf("%s is a dispatcher verb; this member's role is %q — an agent replies, it does not dispatch", verb, env.Member.Role)
	}
	if env.Manifest == nil {
		return fmt.Errorf("no campaign manifest at ~/%s — this member was not configured as an orchestrator", protocol.ManifestDoc)
	}
	return nil
}

// envState is everything a verb needs: identity, home, and (orchestrator) the
// roster.
type envState struct {
	Home     string
	Member   protocol.Member
	Manifest *protocol.Manifest
}

func loadEnv() (*envState, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	e := &envState{Home: home}
	b, err := os.ReadFile(filepath.Join(home, protocol.MemberDoc))
	if err != nil {
		return nil, fmt.Errorf("no member identity at ~/%s — is this machine a campaign member? (%v)", protocol.MemberDoc, err)
	}
	if err := json.Unmarshal(b, &e.Member); err != nil {
		return nil, fmt.Errorf("member.json unparseable: %v", err)
	}
	if mb, err := os.ReadFile(filepath.Join(home, protocol.ManifestDoc)); err == nil {
		var m protocol.Manifest
		if err := json.Unmarshal(mb, &m); err != nil {
			return nil, fmt.Errorf("manifest.json unparseable: %v", err)
		}
		e.Manifest = &m
	}
	return e, nil
}

// readBody resolves message/reply text from --file (or "-" for stdin),
// mirroring the one lesson the shell helper carried in its bones: positional
// prose has already been through the caller's shell, so files are the only
// path on which bytes arrive intact. A bare positional argument is accepted
// for one-liners but files are the documented form.
func readBody(args []string) (string, []string, error) {
	rest := make([]string, 0, len(args))
	var body string
	for i := 0; i < len(args); i++ {
		if args[i] != "--file" {
			rest = append(rest, args[i])
			continue
		}
		if i+1 >= len(args) {
			return "", nil, errors.New("--file needs a path (\"-\" reads stdin)")
		}
		i++
		if args[i] == "-" {
			b, err := os.ReadFile("/dev/stdin")
			if err != nil {
				return "", nil, err
			}
			body = string(b)
		} else {
			b, err := os.ReadFile(args[i])
			if err != nil {
				return "", nil, fmt.Errorf("cannot read body: %v", err)
			}
			body = string(b)
		}
	}
	return body, rest, nil
}
