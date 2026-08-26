package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// A member whose probe never returns must let the observation finish, and must
// read as unreachable rather than gone.
//
// Before the bound, one wedged member blocked observeCampaign itself, and with
// it every state computed from it — measured on a smoke run that sat in a
// single call for seven minutes and then failed at the archive, the only path
// here that already had a deadline.
//
// node-unreachable, not node-stuck: the bound alone turned that hang into a run
// of failures reading "machine gone" about a member that was starved but
// working, which a later smoke run then reproduced. Silence is not an answer,
// and only answers are evidence a machine has died.
func TestObserveBoundsAHangingProbe(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nsleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{
		Bin:        tool,
		WaitDelay:  100 * time.Millisecond,
		ProbeBound: 150 * time.Millisecond,
	}}
	campaign := &model.Campaign{
		Name: "c", Policy: protocol.Policy{BlindProbes: 2},
		Members: []model.Member{
			{Name: "orch", Role: "orchestrator", CLI: "claude", Sandbox: "box0"},
			{Name: "dev", Role: "agent", CLI: "opencode", Sandbox: "box1"},
		},
	}

	type result struct {
		obs observation
		err error
	}
	done := make(chan result, 1)
	go func() {
		obs, err := a.observeCampaign(t.Context(), campaign)
		done <- result{obs, err}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("observeCampaign did not return: the probe bound is not being applied")
	}
	if got.err != nil {
		t.Fatalf("observeCampaign returned an error rather than states: %v", got.err)
	}
	if len(got.obs.Derived) != 2 {
		t.Fatalf("expected a state per member, got %d", len(got.obs.Derived))
	}
	for _, n := range got.obs.Derived {
		if n.State != string(protocol.StateUnreachable) {
			t.Errorf("%s: a member that never answers its probe must read as %s, got %s (%s)",
				n.Name, protocol.StateUnreachable, n.State, n.Detail)
		}
	}
}
