package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
