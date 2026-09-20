package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// A turn launcher that hangs has to fail, and say so. wait starts turns inside
// its loop, so one launcher that never returns stops the looks at every node,
// and the orchestrator's tool call is then killed from above with no output.
func TestStartTurnGivesUpOnALauncherThatHangs(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	bin := t.TempDir()
	launcher := filepath.Join(bin, "cs-codex-remote")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nsleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	prev := turnStartBound
	turnStartBound = 2 * time.Second
	defer func() { turnStartBound = prev }()

	start := time.Now()
	err := startTurn(t.TempDir(), protocol.AgentRecord{CLI: "codex", Sandbox: "dev-box", Session: "sess-dev"},
		protocol.InputDir+"/d001.md", "d001")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a launcher that never returned was reported as a started turn")
	}
	if limit := turnStartBound + 4*time.Second; elapsed > limit {
		t.Fatalf("startTurn ran for %s, past %s: the bound is not what ended it", elapsed, limit)
	}
	if !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("the failure does not name the bound that ended it: %v", err)
	}
}
