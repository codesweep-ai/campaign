package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) archiveCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{Use: "archive <campaign>", Short: "Archive campaign state and member evidence", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		campaign, err := a.store.Load(args[0])
		if err != nil {
			return err
		}
		root, err := a.archiveCampaign(c.Context(), campaign, dir)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), root)
		return nil
	}}
	cmd.Flags().StringVar(&dir, "output", "", "archive destination")
	return cmd
}

// archiveCampaign writes the symmetric evidence layout: campaign state and
// source profile at the root, then per-member input/output/transcript/
// source-metadata channels. Every failed collection leaves an INCOMPLETE
// marker so partial archives are detectable rather than silent.
func (a *app) archiveCampaign(ctx context.Context, campaign *model.Campaign, dir string) (string, error) {
	root := dir
	if root == "" {
		root = filepath.Join("archives", campaign.Name+"-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	state, _ := json.MarshalIndent(campaign, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "campaign.json"), append(state, '\n'), 0o600); err != nil {
		return "", err
	}
	// Re-fingerprint at archive time so the evidence records the
	// surface the campaign ENDED on, not just the one it was created on
	// (upstream can move mid-run). Host-surface metadata, not member evidence:
	// a failed probe is recorded inside the file rather than as INCOMPLETE.
	a.archiveUpstreamFingerprint(ctx, root)
	// The host fingerprint above speaks for the host. Re-measure the
	// plane the work happened on, and diff it against what doctor recorded at
	// create, so a harness modified mid-run is evidence rather than folklore.
	a.archiveMemberHarness(ctx, root, campaign)
	// The fleet's own account of what it was there to do, beside the fingerprint
	// and the verdict rather than inside campaign.json. It answers a question an
	// auditor asks about the run as a whole — "did every member understand the
	// same campaign?" — which is a comparison across members, and comparing
	// prose buried one per member under a 200-line state dump is work nobody
	// does. It is a copy: campaign.json remains authoritative.
	archiveReadback(root, campaign)
	profileIncomplete := filepath.Join(root, "INCOMPLETE-profile.txt")
	if campaign.ProfilePath != "" {
		if source, err := os.ReadFile(campaign.ProfilePath); err == nil {
			if err = os.WriteFile(filepath.Join(root, "campaign-profile.yaml"), source, 0o600); err != nil {
				return "", err
			}
			_ = os.Remove(profileIncomplete)
		} else {
			_ = os.WriteFile(profileIncomplete, []byte(err.Error()+"\n"), 0o600)
		}
	}
	for _, member := range campaign.Members {
		if err := a.archiveMember(ctx, root, member); err != nil {
			return "", err
		}
	}
	// Audit the fleet while the VMs still exist — a wrong-family run
	// or a copied credential must be provable from the evidence, not left to
	// the model's final-report candor, and never discoverable only after
	// destroy. Findings are recorded into the archive (loudly, but they do not
	// abort collection: a suspect run is exactly when the evidence matters most).
	a.archiveFleetVerdict(ctx, root, campaign)
	return root, nil
}

// archiveReadback writes every member's restatement as one artifact. Members
// that never answered are named with a reason rather than omitted: an absent row
// and a member that could not state its job must not look the same, which is the
// whole reason `readbackSkipped` is recorded on the campaign too.
func archiveReadback(root string, campaign *model.Campaign) {
	type row struct {
		Member string          `json:"member"`
		Role   string          `json:"role"`
		CLI    string          `json:"cli"`
		Answer *model.Readback `json:"readback,omitempty"`
		Absent string          `json:"absent,omitempty"`
	}
	doc := struct {
		Members []row `json:"members"`
	}{Members: []row{}}
	for _, member := range campaign.Members {
		r := row{Member: member.Name, Role: member.Role, CLI: member.CLI, Answer: member.Readback}
		if member.Readback == nil {
			r.Absent = "no restatement was recorded for this member"
		}
		doc.Members = append(doc.Members, r)
	}
	if encoded, err := json.MarshalIndent(doc, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(root, "readback.json"), append(encoded, '\n'), 0o600)
	}
}

func (a *app) archiveFleetVerdict(ctx context.Context, root string, campaign *model.Campaign) {
	findings := a.verifyFleetLive(ctx, campaign)
	verdict := struct {
		Findings []fleetFinding `json:"findings"`
		Clean    bool           `json:"clean"`
	}{Findings: findings, Clean: len(findings) == 0}
	if encoded, marshalErr := json.MarshalIndent(verdict, "", "  "); marshalErr == nil {
		_ = os.WriteFile(filepath.Join(root, "fleet-verdict.json"), append(encoded, '\n'), 0o600)
	}
	if len(findings) > 0 {
		_ = os.WriteFile(filepath.Join(root, "FLEET-ANOMALY.txt"),
			[]byte(reportFleet(io.Discard, findings).Error()+"\n"), 0o600)
	}
}

// archiveCollectBound is how long one evidence-collection command may run
// inside a member.
//
// Every step below already turns a failure into an INCOMPLETE marker and lets
// the rest of the archive continue. A command that never returns is not a
// failure, so without a bound it defeats that contract completely: the whole
// archive blocks on one wedged member, and archive is the only copy of a
// member's work. The opencode transcript export is the realistic case, because
// it shells out once per recorded session.
const archiveCollectBound = 2 * time.Minute

// collect runs one collection command in a member under that bound, so a hung
// command becomes a marker naming the deadline rather than an indefinite wait.
func (a *app) collect(ctx context.Context, member model.Member, command string) ([]byte, error) {
	bound := a.collectBound
	if bound == 0 {
		bound = archiveCollectBound
	}
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	return a.sandbox.memberOutput(ctx, member, command)
}

func (a *app) archiveMember(ctx context.Context, root string, member model.Member) error {
	base := filepath.Join(root, "agents", member.Name)
	if member.Role == "orchestrator" {
		base = filepath.Join(root, "orchestrator")
	}
	for _, channel := range []string{"input", "output", "transcript", "source-metadata"} {
		if err := os.MkdirAll(filepath.Join(base, channel), 0o700); err != nil {
			return err
		}
	}
	// The input channel IS the dispatch record — one copy, the one the member
	// actually read, with provenance structural (d001 is the readback; after
	// handoff the orchestrator is an agent's only writer).
	a.archiveMemberChannels(ctx, base, member)
	a.archiveMemberConfig(ctx, base, member)
	if err := a.archiveTranscripts(ctx, base, member); err != nil {
		return err
	}
	return a.archiveSourceMetadata(ctx, base, member)
}

// archiveMemberConfig collects the config plane wholesale: everything under
// ~/.config/cs-campaign (identity, not work — member.json, the orchestrator's
// manifest.json, and whatever the guest ABI grows next lands here with no
// change to this file), plus the CS_* lines of the seeded ~/.ssh/environment —
// the stall thresholds actually delivered to the turn drivers. The env file is
// deliberately never taken verbatim: create --env seeds API keys into it, and
// credentials are excluded from archives by rule.
func (a *app) archiveMemberConfig(ctx context.Context, base string, member model.Member) {
	dir := filepath.Join(base, "config")
	_ = os.MkdirAll(dir, 0o700)
	marker := filepath.Join(base, "INCOMPLETE-config.txt")
	data, err := a.collect(ctx, member, "cd ~/"+guestConfigDir+" && tar -czf - .")
	if err != nil {
		_ = os.WriteFile(marker, []byte(err.Error()+"\n"), 0o600)
	} else if err = extractFlatTar(dir, data); err != nil {
		_ = os.WriteFile(marker, []byte(err.Error()+"\n"), 0o600)
	} else {
		_ = os.Remove(marker)
	}
	if env, err := a.collect(ctx, member, "grep '^CS_' ~/.ssh/environment 2>/dev/null || true"); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "stall-env"), env, 0o600)
	}
}

// extractFlatTar unpacks a small guest tar into dir: regular files and
// directories only, paths cleaned, no absolute or parent-escaping entries,
// sizes bounded, guest mtimes preserved.
func extractFlatTar(dir string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open config archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries, total := 0, int64(0)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			return nil
		}
		if nextErr != nil {
			return fmt.Errorf("read config archive: %w", nextErr)
		}
		name := filepath.Clean(header.Name)
		if name == "." {
			continue
		}
		// The guest authors this tar and a guest is an LLM with a shell: cap
		// count and volume, not only per-file size (adversarial review, 5).
		entries++
		if entries > 1000 {
			return errors.New("config archive holds more than 1000 entries")
		}
		total += header.Size
		if total > 32<<20 {
			return errors.New("config archive exceeds 32MiB")
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("unsafe config path %q", header.Name)
		}
		if err := writeTarEntry(reader, header, filepath.Join(dir, name), 8<<20, "config"); err != nil {
			return err
		}
	}
}

// writeTarEntry materializes one allowed tar entry under target: a directory,
// or a regular file bounded at maxFile with the guest's own mtime restored.
// Anything else is refused by name, because a tar an LLM with a shell produced
// is the wrong place to guess. kind names the archive in the diagnostics.
//
// Message-file times are when dispatches opened and continued, so the mtime is
// evidence rather than metadata to discard.
func writeTarEntry(reader io.Reader, header *tar.Header, target string, maxFile int64, kind string) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o700)
	case tar.TypeReg:
		if header.Size > maxFile {
			return fmt.Errorf("%s file too large: %q", kind, header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		file, openErr := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !header.ModTime.IsZero() {
			_ = os.Chtimes(target, header.ModTime, header.ModTime)
		}
		return nil
	default:
		return fmt.Errorf("unsupported %s entry %q (type %d)", kind, header.Name, header.Typeflag)
	}
}

// archiveMemberChannels pulls the member-owned input/output directories from
// the guest through a tar stream with strict extraction rules.
func (a *app) archiveMemberChannels(ctx context.Context, base string, member model.Member) {
	marker := filepath.Join(base, "INCOMPLETE-channels.txt")
	data, err := a.collect(ctx, member, "cd ~/"+guestChannelsDir+" && tar -czf - input output")
	if err != nil {
		_ = os.WriteFile(marker, []byte(err.Error()+"\n"), 0o600)
		return
	}
	if err = extractMemberChannels(base, data); err != nil {
		_ = os.WriteFile(marker, []byte(err.Error()+"\n"), 0o600)
		return
	}
	_ = os.Remove(marker)
}

// archiveTranscripts collects only the adapter-declared CLI evidence paths;
// credentials and unrelated home state are excluded by this allowlist. For
// opencode that means exactly the profile's SQLite trio plus per-session JSON
// exports — never .cs-opencode/env or auth.json, which live beside them.
func (a *app) archiveTranscripts(ctx context.Context, base string, member model.Member) error {
	var allow, pre string
	switch member.CLI {
	case "codex":
		allow = ".cs-codex/sessions .cs-codex/history.jsonl .cs-codex/state_5.sqlite .cs-codex/state_5.sqlite-shm .cs-codex/state_5.sqlite-wal"
	case "claude":
		allow = ".cs-claude/projects"
	case "opencode":
		allow = ".cs-opencode/opencode.db .cs-opencode/opencode.db-wal .cs-opencode/opencode.db-shm .cs-opencode/export"
		// Human-readable transcript layer beside the raw db (which an archive
		// mid-checkpoint copy could tear): export every session through the
		// cs-opencode wrapper so the profile's OPENCODE_DB is used. The export
		// carries prompts, replies, tool calls, and per-message error/completed
		// evidence, and contains no credential material.
		pre = `umask 077; mkdir -p "$HOME/.cs-opencode/export"; for id in $(cs-opencode db "SELECT id FROM session" --format tsv 2>/dev/null); do case "$id" in ses_*) cs-opencode export "$id" > "$HOME/.cs-opencode/export/$id.json" 2>/dev/null || rm -f "$HOME/.cs-opencode/export/$id.json" ;; esac; done; `
	}
	command := fmt.Sprintf("cd \"$HOME\" && %sfiles=; for f in %s; do [ -e \"$f\" ] && files=\"$files $f\"; done; [ -n \"$files\" ] && tar -czf - $files || tar -czf - --files-from /dev/null", pre, allow)
	target := filepath.Join(base, "transcript", "cli-evidence.tgz")
	marker := filepath.Join(base, "transcript", "INCOMPLETE.txt")
	data, err := a.collect(ctx, member, command)
	if err != nil {
		_ = os.Remove(target)
		_ = os.WriteFile(marker, []byte(err.Error()+"\n"), 0o600)
		return nil
	}
	if err = os.WriteFile(target, data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(marker)
	return nil
}

func (a *app) archiveSourceMetadata(ctx context.Context, base string, member model.Member) error {
	for _, repo := range member.Profile.Repos {
		name := repo.Name
		if name == "" {
			name = filepath.Base(repo.Path)
		}
		guestRepo := `"$HOME"/` + shellQuote(name)
		command := fmt.Sprintf("git -C %s status --short && git -C %s log -1 --format='commit=%%H%%nsubject=%%s' && git -C %s diff --binary %s..HEAD", guestRepo, guestRepo, guestRepo, repo.ResolvedCommit)
		data, err := a.collect(ctx, member, command)
		component := archiveComponent(name)
		complete := filepath.Join(base, "source-metadata", component+".txt")
		missing := filepath.Join(base, "source-metadata", component+".INCOMPLETE.txt")
		if err != nil {
			_ = os.Remove(complete)
			_ = os.WriteFile(missing, []byte(err.Error()+"\n"), 0o600)
			continue
		}
		if err = os.WriteFile(complete, data, 0o600); err != nil {
			return err
		}
		_ = os.Remove(missing)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// archiveComponent maps an arbitrary repository name to a safe, collision
// resistant file-name component.
func archiveComponent(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	name := strings.Trim(builder.String(), ".")
	if name == "" {
		name = "repository"
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-%x", name, sum[:3])
}

func archiveIncomplete(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), "INCOMPLETE") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			found = append(found, rel)
		}
		return nil
	})
	sort.Strings(found)
	return found, err
}

// extractMemberChannels unpacks a guest-produced tar stream, allowing only
// regular files and directories under input/ and output/, bounded in size.
func extractMemberChannels(base string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open member channel archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries, total := 0, int64(0)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			return nil
		}
		if nextErr != nil {
			return fmt.Errorf("read member channel archive: %w", nextErr)
		}
		// Same hostile-guest posture as extractFlatTar: bound quantity, not
		// only per-file size.
		entries++
		if entries > 20000 {
			return errors.New("member channel archive holds more than 20000 entries")
		}
		total += header.Size
		if total > 512<<20 {
			return errors.New("member channel archive exceeds 512MiB")
		}
		name := filepath.Clean(header.Name)
		if filepath.IsAbs(name) || name == "." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe member channel path %q", header.Name)
		}
		parts := strings.Split(name, string(filepath.Separator))
		if parts[0] != "input" && parts[0] != "output" {
			return fmt.Errorf("member channel path outside input/output: %q", header.Name)
		}
		if err = writeTarEntry(reader, header, filepath.Join(base, name), 64<<20, "member channel"); err != nil {
			return err
		}
	}
}
