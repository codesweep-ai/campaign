package covmap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ResultsPath is the tracked run-results document under root.
func ResultsPath(root string) string {
	return filepath.Join(root, "covmap", "results.json")
}

// LoadResults reads the tracked results (empty when absent), strictly.
func LoadResults(root string) (*Results, error) {
	data, err := os.ReadFile(ResultsPath(root))
	if os.IsNotExist(err) {
		return &Results{}, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var res Results
	if err := dec.Decode(&res); err != nil {
		return nil, fmt.Errorf("results.json: %w", err)
	}
	return &res, nil
}

// ReadBuffer parses an untracked JSONL run buffer (empty when absent).
func ReadBuffer(path string) ([]Record, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// recordKey identifies a proof independent of when it ran: newest wins.
func recordKey(r Record) string {
	return r.Behavior + "|" + r.Adapter + "|" + r.Role + "|" + string(r.Tier) + "|" + r.Repo + "|" + r.Test
}

// Fold merges buffered records into the existing results, validates every
// surviving record against the rubric, and prunes records whose test no
// longer exists (exists reports test existence per repo; a nil set for a
// repo means "unverifiable here — keep"). The result is deterministic:
// sorted, newest record per key.
func Fold(reg *Registry, existing *Results, buffered []Record, exists map[string]map[string]bool) (*Results, error) {
	latest := map[string]Record{}
	keep := func(r Record) error {
		if err := reg.ValidateRecord(r); err != nil {
			return err
		}
		if set, ok := exists[r.Repo]; ok && set != nil && !set[r.Test] {
			return nil // pruned: the proving test is gone
		}
		k := recordKey(r)
		if cur, ok := latest[k]; !ok || r.Time > cur.Time {
			latest[k] = r
		}
		return nil
	}
	for _, r := range existing.Records {
		// A behavior removed from the rubric prunes its records silently;
		// everything else must validate.
		if _, ok := reg.Lookup(r.Behavior); !ok {
			continue
		}
		if err := keep(r); err != nil {
			return nil, fmt.Errorf("tracked results: %w", err)
		}
	}
	for _, r := range buffered {
		if err := keep(r); err != nil {
			return nil, fmt.Errorf("run buffer: %w", err)
		}
	}
	out := &Results{Records: make([]Record, 0, len(latest))}
	for _, r := range latest {
		out.Records = append(out.Records, r)
	}
	sort.Slice(out.Records, func(i, j int) bool {
		return recordKey(out.Records[i]) < recordKey(out.Records[j])
	})
	return out, nil
}

// SaveResults writes the tracked results document.
func SaveResults(root string, res *Results) error {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ResultsPath(root), append(data, '\n'), 0o644)
}

// CellRecords returns the records proving one adapter×role cell (role-less
// adapter records cover both roles), sorted by tier depth then test name.
func (res *Results) CellRecords(behavior, adapter, role string) []Record {
	var out []Record
	for _, r := range res.Records {
		if r.Behavior == behavior && r.Adapter == adapter && (r.Role == "" || r.Role == role) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if tierRank[out[i].Tier] != tierRank[out[j].Tier] {
			return tierRank[out[i].Tier] > tierRank[out[j].Tier]
		}
		return out[i].Test < out[j].Test
	})
	return out
}

// CellTier returns the deepest proven tier for a cell, or "" for a gap.
func (res *Results) CellTier(behavior, adapter, role string) Tier {
	best := Tier("")
	for _, r := range res.CellRecords(behavior, adapter, role) {
		if best == "" || tierRank[r.Tier] > tierRank[best] {
			best = r.Tier
		}
	}
	return best
}
