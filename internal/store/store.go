package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/codesweep-ai/campaign/internal/model"
)

type Store struct{ Dir string }

// CurrentVersion 2 records a campaign's cs-sandbox group and each member's
// qualified ref. Version 1 records cannot be migrated: their members were
// created on a build that addressed sandboxes by bare name on a caller-named
// network, and that layout is not readable by a group-aware cs-sandbox — so
// the sandboxes a v1 record points at are already unreachable, and silently
// adopting one would produce a campaign whose every command missed.
const CurrentVersion = 2

func (s Store) Lock(name string) (func() error, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "."+name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() error {
		e := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		c := f.Close()
		if e != nil {
			return e
		}
		return c
	}, nil
}

func DefaultDir() string {
	if v := os.Getenv("CS_CAMPAIGN_STATE_DIR"); v != "" {
		return v
	}
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "cs-campaign", "campaigns")
}
func (s Store) path(name string) string { return filepath.Join(s.Dir, name+".json") }
func (s Store) Save(c *model.Campaign) error {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported campaign state version %d (current %d)", c.Version, CurrentVersion)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".state-*")
	if err != nil {
		return err
	}
	n := tmp.Name()
	defer os.Remove(n)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(b, '\n'))
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(n, s.path(c.Name))
	}
	return err
}
func (s Store) Load(name string) (*model.Campaign, error) {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("campaign %q not found", name)
		}
		return nil, err
	}
	var c model.Campaign
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return nil, fmt.Errorf("decode campaign state %q: %w", name, err)
	}
	if c.Version < CurrentVersion {
		// Deliberately not migrated. Every pre-group record addresses its
		// members by bare name on a caller-named network; a group-aware
		// cs-sandbox cannot see those sandboxes, so a "migrated" campaign would
		// load cleanly and then miss on every command it ran. Failing here says
		// so once, in the one place that can.
		return nil, fmt.Errorf("campaign %q uses state version %d, which predates sandbox groups (current %d); "+
			"its members are not addressable by this build — archive or destroy it with the previous cs-campaign, then remove %s",
			name, c.Version, CurrentVersion, s.path(name))
	}
	if c.Version != CurrentVersion {
		return nil, fmt.Errorf("campaign %q uses unsupported state version %d (current %d)", name, c.Version, CurrentVersion)
	}
	return &c, nil
}
func (s Store) Delete(name string) error {
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		err = nil
	}
	// The lock file goes with the record. Teardown reclaims everything a campaign
	// created, and a zero-byte .<name>.lock left behind is a small leak that
	// accrues one per campaign forever — the kind that is invisible until a state
	// directory is mostly litter. Best-effort: a stale lock is harmless, and
	// failing a destroy over one would be worse than leaving it.
	_ = os.Remove(filepath.Join(s.Dir, "."+name+".lock"))
	return err
}

// List returns every readable campaign plus a diagnostic line for each state
// file that exists but cannot be read or decoded, so corruption is visible to
// callers instead of silently shrinking the listing.
func (s Store) List() ([]model.Campaign, []string, error) {
	es, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []model.Campaign
	var problems []string
	for _, e := range es {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, er := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if er != nil {
			problems = append(problems, e.Name()+": "+er.Error())
			continue
		}
		var c model.Campaign
		if er = json.Unmarshal(b, &c); er != nil {
			problems = append(problems, e.Name()+": "+er.Error())
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(problems)
	return out, problems, nil
}
