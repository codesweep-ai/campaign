package cli

// The host's dispatcher verbs. The host is a dispatcher to exactly one
// worker — the orchestrator — so send and restart address it implicitly.
// Both are operator instruments, never a loop: a human who sees node-stopped
// on the orchestrator decides whether to continue it (PROTOCOL.md §8: an
// orchestrator that stops is a defect to be made visible, not repaired
// automatically).

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// sendText resolves message text from the positional argument or --file;
// exactly one source. Files are the safe path: positional prose has already
// been through the operator's shell.
func sendText(args []string, file string) (string, error) {
	hasArg := len(args) == 2
	if hasArg && file != "" {
		return "", errors.New("pass the message either as an argument or via --file, not both")
	}
	if hasArg {
		return args[1], nil
	}
	if file == "" {
		return "", errors.New("a message is required: positional text or --file")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("message file %s is empty", file)
	}
	return string(b), nil
}

func (a *app) sendCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "send <campaign> [text]", Short: "Send to the orchestrator: opens a dispatch if closed, continues it if open", Args: cobra.RangeArgs(1, 2), RunE: func(c *cobra.Command, args []string) error {
		text, err := sendText(args, file)
		if err != nil {
			return err
		}
		member, err := parseMember(a, args[0])
		if err != nil {
			return err
		}
		if member.Role != "orchestrator" {
			return fmt.Errorf("the host dispatches to the orchestrator alone; %s's work comes from the orchestrator", member.Name)
		}
		id, opened, err := a.hostSend(c.Context(), *member, text, false)
		if err != nil {
			return err
		}
		if opened {
			fmt.Fprintf(c.OutOrStdout(), "%s opened\n", id)
		} else {
			fmt.Fprintf(c.OutOrStdout(), "%s continued\n", id)
		}
		return nil
	}}
	cmd.Flags().StringVar(&file, "file", "", "read the message text from a file instead of the positional argument")
	return cmd
}

func (a *app) restartCmd() *cobra.Command {
	return &cobra.Command{Use: "restart <campaign>", Short: "Restart the orchestrator's session and re-anchor it (operator instrument, rung 2)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		member, err := parseMember(a, args[0])
		if err != nil {
			return err
		}
		if member.Role != "orchestrator" {
			return errors.New("the host restarts the orchestrator alone; agent recovery is the orchestrator's ladder")
		}
		if err := a.hostRestart(c.Context(), *member); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "restarted — session dropped, re-anchored against the open dispatch")
		return nil
	}}
}

func (a *app) transcriptCmd() *cobra.Command {
	return &cobra.Command{Use: "transcript <campaign[/member]>", Short: "The raw model-session transcript — human forensics only, never a state input", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		member, err := parseMember(a, args[0])
		if err != nil {
			return err
		}
		return a.sandbox.sessionLog(c.Context(), c.OutOrStdout(), *member)
	}}
}
