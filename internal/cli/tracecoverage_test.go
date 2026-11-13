package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unreadSessions reads one store's `cs-tracer normalize` output and returns a
// finding for each session the tracer could not read: one where more than one
// event in fifty is a record type it does not recognize. That is the bound the
// tracer holds its own fixtures to. It returns how many sessions the store
// yielded too, because a store with none is the same fault at its worst: a
// CLI whose files the tracer no longer takes for sessions at all.
//
// It reads the tracer's documents rather than its viewer, so it holds for any
// tracer that writes the trajectory format the dispatch viewer reads.
func unreadSessions(norm string) (int, []string, error) {
	raw, err := os.ReadFile(filepath.Join(norm, "index.json"))
	if errors.Is(err, fs.ErrNotExist) {
		// The tracer writes no index when it normalized nothing.
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	var idx struct {
		Trajectories []struct{ ID, Path string } `json:"trajectories"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return 0, nil, fmt.Errorf("index.json: %w", err)
	}
	var findings []string
	for _, tr := range idx.Trajectories {
		raw, err := os.ReadFile(filepath.Join(norm, tr.Path, "summary.json"))
		if err != nil {
			return 0, nil, err
		}
		var sum struct {
			Totals struct{ Events int } `json:"totals"`
			Parse  struct {
				Adapter      string `json:"adapter"`
				Unrecognized int    `json:"unrecognized"`
				Warnings     []struct {
					RawType string `json:"rawType"`
				} `json:"warnings"`
			} `json:"parse"`
		}
		if err := json.Unmarshal(raw, &sum); err != nil {
			return 0, nil, fmt.Errorf("%s/summary.json: %w", tr.Path, err)
		}
		if sum.Parse.Unrecognized*50 <= sum.Totals.Events {
			continue
		}
		var types []string
		for _, w := range sum.Parse.Warnings {
			if w.RawType != "" && w.RawType != "unreadable" && !slices.Contains(types, w.RawType) {
				types = append(types, w.RawType)
			}
		}
		slices.Sort(types)
		findings = append(findings, fmt.Sprintf("session %s (%s): %d of %d events are records the tracer does not recognize: %s",
			tr.ID, sum.Parse.Adapter, sum.Parse.Unrecognized, sum.Totals.Events, strings.Join(types, ", ")))
	}
	return len(idx.Trajectories), findings, nil
}

// writeNormalized lays out a store's normalized output the way cs-tracer
// writes it: an index naming each trajectory, and a summary in each one's
// directory.
func writeNormalized(t *testing.T, sessions map[string]string) string {
	t.Helper()
	norm := t.TempDir()
	type entry struct{ ID, Path string }
	var index struct {
		SchemaVersion int     `json:"schemaVersion"`
		Trajectories  []entry `json:"trajectories"`
	}
	index.SchemaVersion = 3
	for _, id := range slices.Sorted(maps.Keys(sessions)) {
		index.Trajectories = append(index.Trajectories, entry{id, id})
		if err := os.MkdirAll(filepath.Join(norm, id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(norm, id, "summary.json"), []byte(sessions[id]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(norm, "index.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return norm
}

func summaryWith(adapter string, events, unrecognized int, types ...string) string {
	var warnings []string
	for _, ty := range types {
		warnings = append(warnings, fmt.Sprintf(`{"rawType":%q}`, ty))
	}
	return fmt.Sprintf(`{"schemaVersion":3,"totals":{"events":%d},"parse":{"adapter":%q,"unrecognized":%d,"warnings":[%s]}}`,
		events, adapter, unrecognized, strings.Join(warnings, ","))
}

// The bound is the tracer's own: one unrecognized event in fifty passes, and
// one more fails. A finding names the session and the record types, because
// those are what a tracer change has to classify.
func TestUnreadSessionsHoldsTheTracersBound(t *testing.T) {
	norm := writeNormalized(t, map[string]string{
		"at-the-bound": summaryWith("claude-code", 50, 1, "user-content:text"),
		"clean":        summaryWith("opencode", 12, 0),
		"over":         summaryWith("codex", 50, 2, "token_usage_record", "token_usage_record", "unreadable"),
	})
	sessions, findings, err := unreadSessions(norm)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 3 {
		t.Errorf("read %d sessions, want 3", sessions)
	}
	want := []string{"session over (codex): 2 of 50 events are records the tracer does not recognize: token_usage_record"}
	if !slices.Equal(findings, want) {
		t.Errorf("findings = %q, want %q", findings, want)
	}
}

// A store the tracer took nothing from reports zero sessions rather than a
// clean bill, so the caller can tell "read, and all recognized" from "not read".
// The tracer writes no index at all then, which is the case that matters.
func TestUnreadSessionsCountsAStoreWithNoSession(t *testing.T) {
	for name, norm := range map[string]string{
		"no index":    t.TempDir(),
		"empty index": writeNormalized(t, nil),
	} {
		sessions, findings, err := unreadSessions(norm)
		if err != nil || sessions != 0 || len(findings) != 0 {
			t.Errorf("%s: got %d sessions, findings %q, err %v; want 0, none, nil", name, sessions, findings, err)
		}
	}
	broken := writeNormalized(t, map[string]string{"s": "{"})
	if _, _, err := unreadSessions(broken); err == nil {
		t.Error("an unreadable summary.json passed; want an error naming it")
	}
}
