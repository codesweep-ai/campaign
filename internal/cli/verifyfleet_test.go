package cli

// Verify-fleet must FLAG the ctv4 signatures (declared-CLI evidence
// empty, foreign session state, copied credentials) and PASS a fleet
// whose work matches its declaration — in both live and archive modes. The
// archive-mode acceptance test runs against the preserved ctv4 archive itself
// when it is present.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
)

func TestFleetProbeClassifiesEvidenceAndForeignState(t *testing.T) {
	covmap.ProveCoreOnPass(t, "fleet-conformance", covmap.TierUnit)
	// The ctv4 fixer shape from its probe output: declared codex, no codex
	// evidence, a populated claude session dir, a copied claude credential.
	out := "EVIDENCE 0\nFOREIGN-SESSION claude 37\nFOREIGN-SESSION opencode 0\nFOREIGN-CRED claude .cs-claude/.credentials.json\n"
	findings := classifyFleetProbe("fixer", "codex", out)
	var joined strings.Builder
	for _, f := range findings {
		joined.WriteString(f.Problem + "\n")
	}
	for _, want := range []string{"produced NO evidence", "carries claude session evidence (37 files)", "carries claude credential state"} {
		if !strings.Contains(joined.String(), want) {
			t.Fatalf("classify missing %q in:\n%s", want, joined.String())
		}
	}
	// A healthy codex member: evidence present, no foreign state.
	if got := classifyFleetProbe("fixer", "codex", "EVIDENCE 9\nFOREIGN-SESSION claude 0\nFOREIGN-SESSION opencode 0\n"); len(got) != 0 {
		t.Fatalf("healthy member produced findings: %+v", got)
	}
}

func TestVerifyFleetLiveFlagsWrongFamilyMember(t *testing.T) {
	covmap.ProveCoreOnPass(t, "fleet-conformance", covmap.TierUnit)
	// A fake guest whose codex member did its work on claude.
	home := t.TempDir()
	t.Setenv("FAKE_HOME", home)
	mustDir(t, filepath.Join(home, ".cs-claude", "projects", "p"))
	mustFile(t, filepath.Join(home, ".cs-claude", "projects", "p", "session.jsonl"), "{}\n")
	mustFile(t, filepath.Join(home, ".cs-claude", ".credentials.json"), "token\n")
	// The declared codex stream is deliberately empty.
	mustDir(t, filepath.Join(home, ".cs-codex", "sessions"))

	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	mustFile(t, tool, "#!/bin/sh\ncase \"$1\" in exec) HOME=\"$FAKE_HOME\" sh -c \"$5\" ;; esac\n")
	if err := os.Chmod(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{Bin: tool}}
	campaign := &model.Campaign{Name: "c", Members: []model.Member{{Name: "fixer", Role: "agent", CLI: "codex", Sandbox: "box1"}}}
	findings := a.verifyFleetLive(t.Context(), campaign)
	var joined strings.Builder
	for _, f := range findings {
		joined.WriteString(f.Problem + "\n")
	}
	for _, want := range []string{"produced NO evidence", "carries claude session evidence", "carries claude credential state"} {
		if !strings.Contains(joined.String(), want) {
			t.Fatalf("live audit missed %q:\n%s", want, joined.String())
		}
	}
}

func TestVerifyFleetLivePassesAHealthyFleet(t *testing.T) {
	covmap.ProveCoreOnPass(t, "fleet-conformance", covmap.TierUnit)
	home := t.TempDir()
	t.Setenv("FAKE_HOME", home)
	mustDir(t, filepath.Join(home, ".cs-codex", "sessions"))
	mustFile(t, filepath.Join(home, ".cs-codex", "sessions", "rollout.jsonl"), "{}\n")
	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	mustFile(t, tool, "#!/bin/sh\ncase \"$1\" in exec) HOME=\"$FAKE_HOME\" sh -c \"$5\" ;; esac\n")
	if err := os.Chmod(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{Bin: tool}}
	healthy := &model.Campaign{Name: "c", Members: []model.Member{{Name: "fixer", Role: "agent", CLI: "codex", Sandbox: "box1"}}}
	if findings := a.verifyFleetLive(t.Context(), healthy); len(findings) != 0 {
		t.Fatalf("healthy fleet flagged: %+v", findings)
	}
}

// A shared login is not a fleet-level finding. The audit used to refuse more
// than one member inheriting the host's Claude login, on the theory that a
// cloned OAuth credential rotates out from under its copies. Sharing one login
// across members is no different from sharing one API key across them, and the
// same is true of codex — so the fleet check is gone, and what remains is
// per-member evidence, which is what the audit is actually for.
func TestVerifyFleetLiveAllowsAFleetSharingOneLogin(t *testing.T) {
	home := t.TempDir()
	tool := filepath.Join(home, "fake-sandbox")
	mustFile(t, tool, "#!/bin/sh\ncase \"$1\" in exec) echo 'EVIDENCE 3' ;; esac\n")
	if err := os.Chmod(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{Bin: tool}}
	for _, family := range []string{"claude", "codex"} {
		t.Run(family, func(t *testing.T) {
			shared := &model.Campaign{Name: "c", Members: []model.Member{
				inheriting("a", "orchestrator", "box0", family),
				inheriting("b", "agent", "box1", family),
			}}
			if findings := a.verifyFleetLive(t.Context(), shared); len(findings) != 0 {
				t.Fatalf("two members may share one %s login: %+v", family, findings)
			}
		})
	}
}

func inheriting(name, role, box, family string) model.Member {
	m := model.Member{Name: name, Role: role, CLI: family, Sandbox: box}
	m.Profile.Auth.InheritAgentLogin = []string{family}
	return m
}

// TestVerifyFleetArchiveFlagsCtv4 is the retroactive acceptance test named in
// The preserved ctv4 archive must be flagged, and a healthy run
// must pass. Skips cleanly when the archives are absent (they are gitignored
// evidence, present only on the host that ran the campaigns).
func TestVerifyFleetArchiveFlagsCtv4(t *testing.T) {
	covmap.ProveCoreOnPass(t, "fleet-conformance", covmap.TierUnit)
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Skip("repo root not found")
	}
	ctv4, _ := filepath.Glob(filepath.Join(root, ".tmp", "ctv4-*"))
	if len(ctv4) == 0 {
		t.Skip("ctv4 archive not present (gitignored evidence); acceptance test runs only on the host that has it")
	}
	findings, err := verifyFleetArchive(ctv4[0])
	if err != nil {
		t.Fatal(err)
	}
	flagged := false
	for _, f := range findings {
		if f.Member == "fixer" && f.Declared == "codex" && strings.Contains(f.Problem, "EMPTY codex evidence") {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("verify-fleet must flag the ctv4 codex fixer's empty evidence; got %+v", findings)
	}
	if ctv3, _ := filepath.Glob(filepath.Join(root, ".tmp", "ctv3-*")); len(ctv3) > 0 {
		if healthy, err := verifyFleetArchive(ctv3[0]); err != nil || len(healthy) != 0 {
			t.Fatalf("healthy ctv3 archive must pass: %+v, %v", healthy, err)
		}
	}
}

// TestVerifyFleetArchiveSynthetic covers the empty-tarball flag without
// depending on the gitignored real archives.
func TestVerifyFleetArchiveSynthetic(t *testing.T) {
	covmap.ProveCoreOnPass(t, "fleet-conformance", covmap.TierUnit)
	dir := t.TempDir()
	campaign := model.Campaign{Name: "syn", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "claude"},
		{Name: "fixer", Role: "agent", CLI: "codex"},
	}}
	raw, _ := json.MarshalIndent(campaign, "", "  ")
	mustFile(t, filepath.Join(dir, "campaign.json"), string(raw))
	writeTgz(t, filepath.Join(dir, "orchestrator", "transcript", "cli-evidence.tgz"), map[string]string{".cs-claude/projects/p/s.jsonl": "{}"})
	writeTgz(t, filepath.Join(dir, "agents", "fixer", "transcript", "cli-evidence.tgz"), nil) // empty: the ctv4 shape

	findings, err := verifyFleetArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Member != "fixer" || !strings.Contains(findings[0].Problem, "EMPTY codex evidence") {
		t.Fatalf("expected exactly the fixer flagged, got %+v", findings)
	}
}

func mustDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustFile(t *testing.T, path, content string) {
	t.Helper()
	mustDir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTgz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	mustDir(t, filepath.Dir(path))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
