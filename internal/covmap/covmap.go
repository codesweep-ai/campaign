// Package covmap is the execution-gated behavioral coverage system. The
// universe is a checked-in rubric (covmap/behaviors.json, authored and
// design-referenced) crossed with the adapter and role lists derived from
// internal/model. Cells are filled ONLY by run-results: tests call Prove at
// the assertion that demonstrates a behavior, which appends a record to an
// untracked buffer; `make covmap` folds buffers into the tracked
// covmap/results.json (pruning records whose tests no longer exist) and
// renders covmap/covmap.html as a pure function of rubric + results.
// Nothing here is curated coverage: an unexecuted claim is a visible gap.
package covmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/codesweep-ai/campaign/internal/model"
)

// Tier is a verification depth, by how much of the real system executes.
type Tier string

const (
	TierUnit    Tier = "unit"    // pure Go in-process, collaborators faked
	TierScripts Tier = "scripts" // the real shipped shell scripts execute (no VMs/models)
	TierSmoke   Tier = "smoke"   // real VMs and the whole protocol, model turns replayed
	TierLive    Tier = "live"    // real microVMs and real model turns
)

// Smoke ranks below live and above scripts, which is exactly what it proves:
// everything a live run does except the model's own answer, which comes from a
// cassette. A cell filled at smoke has been through real machines and the real
// protocol; only the judgement inside it was decided once, earlier.
var tierRank = map[Tier]int{TierUnit: 1, TierScripts: 2, TierSmoke: 3, TierLive: 4}

// TierLetters render in cells; TierLabels in the legend.
var (
	TierOrder   = []Tier{TierUnit, TierScripts, TierSmoke, TierLive}
	TierLetters = map[Tier]string{TierUnit: "U", TierScripts: "S", TierSmoke: "R", TierLive: "L"}
	TierLabels  = map[Tier]string{
		TierUnit:    "unit — pure Go, collaborators faked",
		TierScripts: "scripts — the real shipped shell scripts execute",
		TierSmoke:   "smoke — real VMs and the whole protocol, model turns replayed",
		TierLive:    "live — real microVMs and model turns",
	}
)

// Behavior is one rubric row. Scope "adapter" rows span the adapter × role
// matrix; scope "core" rows are adapter-agnostic. DesignRef anchors the row
// to the design promise it restates, so review is a two-step check.
type Behavior struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Group     string `json:"group"`
	Scope     string `json:"scope"`
	DesignRef string `json:"design_ref"`
}

// Registry is the parsed rubric.
type Registry struct {
	Behaviors []Behavior `json:"behaviors"`
	byID      map[string]Behavior
}

// Record is one execution-gated proof: emitted by a test's Prove call, at
// the commit the test ran on. Role "" on an adapter-scope behavior means the
// proof is role-agnostic (host-side tooling) and fills both role cells.
type Record struct {
	Behavior string `json:"behavior"`
	Adapter  string `json:"adapter,omitempty"`
	Role     string `json:"role,omitempty"`
	Tier     Tier   `json:"tier"`
	Test     string `json:"test"`
	Repo     string `json:"repo"` // "campaign" | "sandbox"
	Commit   string `json:"commit"`
	Time     string `json:"time"` // RFC3339 UTC
}

// Results is the tracked run-results document.
type Results struct {
	Records []Record `json:"records"`
}

// FindRepoRoot walks up from dir to the campaign module root.
func FindRepoRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		mod, err := os.ReadFile(filepath.Join(abs, "go.mod"))
		if err == nil && bytes.Contains(mod, []byte("module github.com/codesweep-ai/campaign")) {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("cs-campaign repo root not found above %s", dir)
		}
		abs = parent
	}
}

// LoadRegistry reads and validates the rubric at covmap/behaviors.json
// under root. Decoding is strict; ids must be unique; scope and fields are
// mandatory.
func LoadRegistry(root string) (*Registry, error) {
	data, err := os.ReadFile(filepath.Join(root, "covmap", "behaviors.json"))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var reg Registry
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("behaviors.json: %w", err)
	}
	reg.byID = map[string]Behavior{}
	for i, b := range reg.Behaviors {
		where := fmt.Sprintf("behaviors[%d] (%q)", i, b.ID)
		if b.ID == "" || b.Title == "" || b.Group == "" || b.DesignRef == "" {
			return nil, fmt.Errorf("%s: id, title, group, and design_ref are all required", where)
		}
		if b.Scope != "adapter" && b.Scope != "core" {
			return nil, fmt.Errorf("%s: scope must be adapter or core, got %q", where, b.Scope)
		}
		if _, dup := reg.byID[b.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate id", where)
		}
		reg.byID[b.ID] = b
	}
	if len(reg.Behaviors) == 0 {
		return nil, errors.New("behaviors.json: empty rubric")
	}
	return &reg, nil
}

// Lookup returns the behavior for id.
func (r *Registry) Lookup(id string) (Behavior, bool) {
	b, ok := r.byID[id]
	return b, ok
}

// Rows returns behaviors of one scope in rubric order.
func (r *Registry) Rows(scope string) []Behavior {
	var out []Behavior
	for _, b := range r.Behaviors {
		if b.Scope == scope {
			out = append(out, b)
		}
	}
	return out
}

// ValidateRecord checks a record against the rubric and the derived axes.
func (r *Registry) ValidateRecord(rec Record) error {
	b, ok := r.Lookup(rec.Behavior)
	if !ok {
		return fmt.Errorf("unknown behavior %q (register it in covmap/behaviors.json)", rec.Behavior)
	}
	if _, ok := tierRank[rec.Tier]; !ok {
		return fmt.Errorf("unknown tier %q", rec.Tier)
	}
	switch b.Scope {
	case "core":
		if rec.Adapter != "" || rec.Role != "" {
			return fmt.Errorf("behavior %q is core; adapter/role must be empty", rec.Behavior)
		}
	case "adapter":
		if !model.ValidAdapterCLI(rec.Adapter) {
			return fmt.Errorf("behavior %q needs a valid adapter, got %q", rec.Behavior, rec.Adapter)
		}
		if rec.Role != "" && !contains(model.Roles, rec.Role) {
			return fmt.Errorf("unknown role %q", rec.Role)
		}
	}
	if rec.Test == "" {
		return fmt.Errorf("record for %q has no test name", rec.Behavior)
	}
	if rec.Repo != "campaign" && rec.Repo != "sandbox" {
		return fmt.Errorf("record for %q has unknown repo %q", rec.Behavior, rec.Repo)
	}
	return nil
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}
