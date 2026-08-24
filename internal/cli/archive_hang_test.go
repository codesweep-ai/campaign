package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
)

// A member whose collection command never returns must leave a marker and let
// the archive finish. Archive already turns a collection *failure* into an
// INCOMPLETE marker, but a hang is not a failure: before the bound, one wedged
// member blocked the whole archive indefinitely, and CI saw it as an unrelated
// test timeout forty minutes later.
func TestArchiveBoundsAHangingCollection(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nsleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{Bin: tool, WaitDelay: 100 * time.Millisecond}, collectBound: 150 * time.Millisecond}
	root := t.TempDir()
	member := model.Member{Name: "dev", Role: "agent", CLI: "opencode", Sandbox: "box1"}

	done := make(chan error, 1)
	go func() { done <- a.archiveMember(t.Context(), root, member) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("archiveMember returned an error rather than markers: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("archiveMember did not return: the collection bound is not being applied")
	}

	var markers []string
	_ = filepath.Walk(filepath.Join(root, "agents", "dev"), func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasPrefix(info.Name(), "INCOMPLETE") {
			markers = append(markers, p)
		}
		return nil
	})
	if len(markers) == 0 {
		t.Fatal("a hung collection left no INCOMPLETE marker")
	}
}

// R109: source-metadata has to answer "which branch, measured against what?"
// from that file alone. A diff names neither of its endpoints and
// `status --short` prints no branch header, so without these two lines the
// archive records a change against a base nothing in it identifies.
func TestSourceMetadataRecordsBranchAndBase(t *testing.T) {
	covmap.ProveCoreOnPass(t, "archive-safety", covmap.TierUnit)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "product")
	base := initTestRepo(t, repo, "work")

	// A fake sandbox that runs the collection command right here: `exec <ref>
	// sh -lc <command>` is the shape memberOutput calls it with.
	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n[ \"$1\" = exec ] || exit 1\nexec sh -c \"$5\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{Bin: tool}}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "source-metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	member := model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box",
		Profile: model.MemberProfile{Repos: []model.Repo{{Path: "/elsewhere/product", ResolvedCommit: base}}}}
	if err := a.archiveSourceMetadata(t.Context(), root, member); err != nil {
		t.Fatal(err)
	}

	matches, _ := filepath.Glob(filepath.Join(root, "source-metadata", "product-*.txt"))
	if len(matches) != 1 {
		t.Fatalf("expected one source-metadata file, got %v", matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"branch=work\n", "base=" + base + "\n", "commit="} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("source-metadata is missing %q:\n%s", want, body)
		}
	}
}

// initTestRepo makes a one-commit repository on branch and returns that
// commit, which is the base an archive measures against.
func initTestRepo(t *testing.T, dir, branch string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", branch},
		{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "--allow-empty", "-m", "base"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
