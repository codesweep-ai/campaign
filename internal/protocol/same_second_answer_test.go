package protocol

import "testing"

// A hand-off answered in the same second as the turn end it answers: the
// long session that message starts must not read as 0s long.
func TestScratchSameSecondAnswer(t *testing.T) {
	pol := boundPolicy
	pol.Handoffs = 2
	f, now := handedOffAfter(0, 1200, session{"handoff", 1200}, session{"handoff", 60})
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State == StateStuck {
		t.Errorf("a twenty-minute session answered within its second read as rapid: %+v", o)
	}
}
