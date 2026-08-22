package cli

// The evidence audit that caught ctv4, as a tool. The shims
// guard the tool boundary; they cannot see a credential copied over peer ssh
// or a tool invoked by absolute path. verify-fleet asks the evidence instead
// of the declaration: did each member's DECLARED CLI actually do the work, and
// does any member carry a foreign family's session or credential state? It runs
// automatically inside archive (before destroy — a violation must never be
// discoverable only after the evidence is gone) and on demand, live or over a
// preserved archive. Disclosure becomes structural, not a matter of the model's
// candor.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/spf13/cobra"
)

// evidenceGlob is the declared-CLI evidence stream per family — the same
// adapter-declared paths archiveTranscripts collects. A member whose declared
// CLI did the work has a non-empty stream here.
var evidenceGlob = map[string]string{
	"claude":   ".cs-claude/projects",
	"codex":    ".cs-codex/sessions",
	"opencode": ".cs-opencode/opencode.db",
}

// foreignSessionGlob is where a family's SESSION evidence lands. A member
// carrying another family's populated session dir is the ctv4 signature: the
// declared codex fixer had .cs-claude/projects full of Claude transcripts.
var foreignSessionGlob = map[string][]string{
	"claude":   {".cs-claude/projects", ".claude/projects"},
	"codex":    {".cs-codex/sessions"},
	"opencode": {".cs-opencode/export"},
}

// foreignCredLeaf is a family's credential/token file. A member carrying a
// family's creds it was not declared for is the copy signature — the second
// half of ctv4 (credentials pushed to the fixer to make the wrong family work).
var foreignCredLeaf = map[string][]string{
	"claude":   {".cs-claude/.credentials.json", ".claude/.credentials.json"},
	"codex":    {".cs-codex/auth.json", ".codex/auth.json"},
	"opencode": {".cs-opencode/auth.json"},
}

type fleetFinding struct {
	Member   string `json:"member"`
	Declared string `json:"declaredCli"`
	Problem  string `json:"problem"`
}

func (a *app) auditCmd() *cobra.Command {
	var archiveDir string
	cmd := &cobra.Command{Use: "audit [campaign]", Short: "Audit the evidence: each member's declared CLI did the work", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		var findings []fleetFinding
		var err error
		switch {
		case archiveDir != "":
			findings, err = verifyFleetArchive(archiveDir)
		case len(args) == 1:
			var campaign *model.Campaign
			if campaign, err = a.store.Load(args[0]); err == nil {
				findings = a.verifyFleetLive(c.Context(), campaign)
			}
		default:
			return errors.New("audit needs a campaign name or --archive <dir>")
		}
		if err != nil {
			return err
		}
		return reportFleet(c.OutOrStdout(), findings)
	}}
	cmd.Flags().StringVar(&archiveDir, "archive", "", "audit a preserved archive directory instead of live VMs")
	return cmd
}

func reportFleet(out io.Writer, findings []fleetFinding) error {
	if len(findings) == 0 {
		fmt.Fprintln(out, "ok  audit: every member's declared CLI matches its evidence; no foreign-family state")
		return nil
	}
	var lines []string
	for _, finding := range findings {
		lines = append(lines, fmt.Sprintf("%s (declared %s): %s", finding.Member, finding.Declared, finding.Problem))
	}
	return fmt.Errorf("audit FAILED (%d findings) — a member's work did not match its declaration:\n  %s",
		len(findings), strings.Join(lines, "\n  "))
}

// verifyFleetLive probes each member VM: the declared CLI's evidence stream is
// non-empty, and no other family's session or credential state is present. A
// member it cannot reach is a finding rather than an error, because an audit
// that stops at the first unreachable member reports the rest as clean.
func (a *app) verifyFleetLive(ctx context.Context, campaign *model.Campaign) []fleetFinding {
	var findings []fleetFinding
	for _, member := range campaign.Members {
		declared := member.CLI
		out, err := a.sandbox.memberOutput(ctx, member, fleetProbe(declared))
		if err != nil {
			findings = append(findings, fleetFinding{member.Name, declared, fmt.Sprintf("unreachable for audit: %v", err)})
			continue
		}
		findings = append(findings, classifyFleetProbe(member.Name, declared, string(out))...)
	}
	return findings
}

// fleetProbe emits one line per checked path: "EVIDENCE <n>", "FOREIGN-SESSION
// <family> <n>", "FOREIGN-CRED <family> <path>". Counts are file counts (or 1
// for a non-empty single file), so an empty stream reads as 0 and never as
// absence-is-fine.
func fleetProbe(declared string) string {
	var b strings.Builder
	b.WriteString(`cd "$HOME" || exit 0; count(){ if [ -d "$1" ]; then find "$1" -type f 2>/dev/null | head -1000 | wc -l; elif [ -s "$1" ]; then echo 1; else echo 0; fi; }; `)
	fmt.Fprintf(&b, `printf 'EVIDENCE %%s\n' "$(count %q)"; `, evidenceGlob[declared])
	for family, globs := range foreignSessionGlob {
		if family == declared {
			continue
		}
		for _, g := range globs {
			fmt.Fprintf(&b, `printf 'FOREIGN-SESSION %s %%s\n' "$(count %q)"; `, family, g)
		}
	}
	for family, leaves := range foreignCredLeaf {
		if family == declared {
			continue
		}
		for _, leaf := range leaves {
			fmt.Fprintf(&b, `[ -e %q ] && printf 'FOREIGN-CRED %s %s\n'; `, leaf, family, leaf)
		}
	}
	// A trailing false `[ -e ]` test would make the whole probe exit nonzero on
	// a perfectly healthy member; end on success so exit status means "the
	// probe ran", not "the last checked file happened to exist".
	b.WriteString("true")
	return b.String()
}

func classifyFleetProbe(member, declared, out string) []fleetFinding {
	var findings []fleetFinding
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[0] == "EVIDENCE":
			if fields[1] == "0" {
				findings = append(findings, fleetFinding{member, declared, fmt.Sprintf("declared CLI %q produced NO evidence (its %s stream is empty) — did another CLI do the work?", declared, evidenceGlob[declared])})
			}
		case len(fields) == 3 && fields[0] == "FOREIGN-SESSION":
			if fields[2] != "0" {
				findings = append(findings, fleetFinding{member, declared, fmt.Sprintf("carries %s session evidence (%s files) but was declared %s — the ctv4 wrong-family signature", fields[1], fields[2], declared)})
			}
		case len(fields) >= 3 && fields[0] == "FOREIGN-CRED":
			findings = append(findings, fleetFinding{member, declared, fmt.Sprintf("carries %s credential state at %s but was declared %s — a copied-credential signature", fields[1], fields[2], declared)})
		}
	}
	return findings
}

// verifyFleetArchive is the retroactive audit: read the declared CLIs from
// campaign.json and confirm each member's transcript/cli-evidence.tgz carries
// its declared-CLI evidence. This is what flags the preserved ctv4 archive (the
// codex fixer's tarball is empty because Claude did the work).
func verifyFleetArchive(dir string) ([]fleetFinding, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "campaign.json"))
	if err != nil {
		return nil, fmt.Errorf("read archive campaign.json: %w", err)
	}
	var campaign model.Campaign
	if err := json.Unmarshal(raw, &campaign); err != nil {
		return nil, fmt.Errorf("parse archive campaign.json: %w", err)
	}
	var findings []fleetFinding
	for _, member := range campaign.Members {
		group := "agents"
		if member.Role == "orchestrator" {
			group = "orchestrator"
		}
		// Members live under agents/<name>/ or orchestrator/; try both shapes.
		candidates := []string{
			filepath.Join(dir, group, member.Name, "transcript", "cli-evidence.tgz"),
			filepath.Join(dir, "orchestrator", "transcript", "cli-evidence.tgz"),
		}
		var tgz string
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				tgz = candidate
				break
			}
		}
		if tgz == "" {
			findings = append(findings, fleetFinding{member.Name, member.CLI, "no transcript/cli-evidence.tgz in the archive — cannot confirm the declared CLI did the work"})
			continue
		}
		entries, err := tgzEntryCount(tgz)
		if err != nil {
			findings = append(findings, fleetFinding{member.Name, member.CLI, fmt.Sprintf("unreadable evidence tarball: %v", err)})
			continue
		}
		if entries == 0 {
			findings = append(findings, fleetFinding{member.Name, member.CLI, fmt.Sprintf("EMPTY %s evidence tarball — declared %s but produced no transcript; another CLI did the work (the ctv4 signature)", member.CLI, member.CLI)})
		}
	}
	return findings, nil
}

func tgzEntryCount(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	count := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
		if header.Typeflag == tar.TypeReg {
			count++
		}
	}
	return count, nil
}
