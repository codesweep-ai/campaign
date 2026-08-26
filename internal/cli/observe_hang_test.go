package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// A member whose probe never returns must read as a probe failure and let the
// observation finish. Compute already turns a failed probe into
// node-unreachable, and a run of them into node-stuck — but a probe that hangs
// is not a failure, so before the bound one wedged member blocked
// observeCampaign itself, and with it every state computed from it, including
// the states that say a member is gone.
//
// Measured on a smoke run that sat in one observeCampaign call for seven
// minutes and then failed at the archive, which was the only path here that
// already had a deadline.
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
		if n.State != string(protocol.StateStuck) {
			t.Errorf("%s: a member that never answers its probe must read as %s, got %s (%s)",
				n.Name, protocol.StateStuck, n.State, n.Detail)
		}
	}
}
