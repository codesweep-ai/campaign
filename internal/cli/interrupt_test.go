package cli

// The interrupt teardown's one hard rule, and the test that holds it.
//
// A live run is killed often — a `timeout` around it, a lost terminal, an
// operator who has seen enough — and the campaign it started keeps its
// machines until something reclaims them. So the live driver catches SIGINT
// and tears down before it exits. What it must not do is reach for the
// campaign lock on the way out.
//
// The lock is exclusive, per open file description, and create holds it for
// its whole duration (executeCreate). An interrupt lands inside that window by
// definition — that is when a run is long enough to want interrupting. A
// teardown that opened the lock file again would produce a second description
// and block against its own process's hold: a deadlock no signal can clear,
// because the holder is the waiter. The observable symptom is a run that stops
// answering Ctrl+C altogether.
//
// This file is untagged on purpose. The rule is about locking, not about
// machines, so it is proved in the unit tier where it runs on every push
// rather than only in the live tiers that own the handler.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// interruptedGroup reads the campaign's group off the record without taking
// the campaign lock.
//
// Unlocked reading is safe because Save publishes by atomic rename: a create
// running alongside this has either not written the group yet or has written
// the whole record, never half of one. An empty answer means create had not
// reached its first save — which is also the only moment at which there is no
// machine to reclaim.
func (a *app) interruptedGroup(name string) string {
	campaign, err := a.store.Load(name)
	if err != nil {
		return ""
	}
	return campaign.Group
}

func TestInterruptedGroupReadsThroughAHeldLock(t *testing.T) {
	dir := t.TempDir()
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}}
	if err := a.store.Save(&model.Campaign{Name: "csrint", Group: "csrint-1a2b3c4d"}); err != nil {
		t.Fatal(err)
	}

	// Exactly what executeCreate is holding when the interrupt arrives.
	unlock, err := a.store.Lock("csrint")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()

	got := make(chan string, 1)
	go func() { got <- a.interruptedGroup("csrint") }()
	select {
	case group := <-got:
		if group != "csrint-1a2b3c4d" {
			t.Fatalf("group = %q, want csrint-1a2b3c4d", group)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interruptedGroup blocked on the campaign lock — the interrupt teardown would deadlock against create")
	}
}

// A record create has not saved yet names no group, and there is nothing to
// reclaim. The handler must read that as "exit", not as an error worth
// retrying or a group literally named "".
func TestInterruptedGroupIsEmptyBeforeTheFirstSave(t *testing.T) {
	a := &app{store: store.Store{Dir: filepath.Join(t.TempDir(), "state")}}
	if group := a.interruptedGroup("never-created"); group != "" {
		t.Fatalf("group = %q, want empty", group)
	}
}
