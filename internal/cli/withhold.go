package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/withhold"
)

// validateWithhold refuses a declaration that cannot name a path, before
// anything on disk is read. The history checks are checkWithheld's.
func validateWithhold(m model.MemberProfile) error {
	for _, r := range m.Repos {
		for _, d := range r.Withhold {
			if _, err := withhold.Normalize(d); err != nil {
				return fmt.Errorf("repo %s: %w", r.Path, err)
			}
		}
	}
	for _, s := range m.Snapshots {
		for _, d := range s.Withhold {
			if _, err := withhold.Normalize(d); err != nil {
				return fmt.Errorf("snapshot %s: %w", s.Path, err)
			}
		}
	}
	return nil
}

// checkWithheld holds every withhold declaration against what the member's
// copy would carry. A member's repository is a clone of the whole host
// repository, every branch included, so a withheld path anywhere in its
// history reaches the member at create, whatever ref the member is checked
// out at. A snapshot is the directory as it stands.
//
// The clone is made from a local path, which copies the object store rather
// than the objects a ref reaches, so an object no ref reaches travels too and
// no ref walk can see it. A repository with a declaration is therefore also
// refused while it holds one, and the message names the command that removes
// them.
func checkWithheld(p model.Profile) error {
	members := []struct {
		name string
		m    model.MemberProfile
	}{{"orchestrator", p.Orchestrator}}
	for _, name := range sortedNames(p.Agents) {
		members = append(members, struct {
			name string
			m    model.MemberProfile
		}{"agent " + name, p.Agents[name]})
	}
	for _, mem := range members {
		for _, r := range mem.m.Repos {
			if len(r.Withhold) == 0 || r.Initialize {
				continue
			}
			decls, err := normalizedDecls(r.Withhold)
			if err != nil {
				return fmt.Errorf("%s: repo %s: %w", mem.name, r.Path, err)
			}
			if err := withheldInHistory(mem.name, r.Path, decls); err != nil {
				return err
			}
			if err := withheldStoreIsClean(mem.name, r.Path); err != nil {
				return err
			}
		}
		for _, s := range mem.m.Snapshots {
			if len(s.Withhold) == 0 {
				continue
			}
			decls, err := normalizedDecls(s.Withhold)
			if err != nil {
				return fmt.Errorf("%s: snapshot %s: %w", mem.name, s.Path, err)
			}
			if err := withheldInSnapshot(mem.name, s.Path, decls); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizedDecls(decls []string) ([]string, error) {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		n, err := withhold.Normalize(d)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// withheldInHistory walks every object reachable from every ref of the
// repository, HEAD included, and refuses on the first path a declaration
// withholds. A path added and later deleted is still in the clone, so the
// walk is over objects and never over a tip's tree.
func withheldInHistory(member, repo string, decls []string) error {
	out, err := exec.Command("git", "-C", repo, "rev-list", "--objects", "--all").Output()
	if err != nil {
		return fmt.Errorf("%s: repo %s: reading its history for withheld paths: %w", member, repo, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var path, decl string
	for sc.Scan() {
		_, p, ok := strings.Cut(sc.Text(), " ")
		if !ok || p == "" {
			continue
		}
		if d, hit := withhold.Hit(decls, p); hit {
			path, decl = deeperHit(path, decl, p, d)
		}
	}
	if path == "" {
		return nil
	}
	added := "a commit"
	if b, err := exec.Command("git", "-C", repo, "log", "--all", "--format=%h", "--diff-filter=A", "--", path).Output(); err == nil {
		if lines := strings.Fields(string(b)); len(lines) > 0 {
			added = "commit " + lines[len(lines)-1]
		}
	}
	return fmt.Errorf("%s: repo %s withholds %s, and %s on its history adds %s\n\n"+
		"A member's clone carries every branch of the repository, so a withheld path anywhere\n"+
		"in its history reaches the member at create. Keep the hold-out in a repository or\n"+
		"snapshot this member is not given, and rewrite the history that holds it",
		member, repo, decl, added, path)
}

// deeperHit keeps the first withheld path seen, and then a path beneath it:
// rev-list names a directory before its files, and a message that names a
// file says more than one that names the directory.
func deeperHit(path, decl, p, d string) (string, string) {
	if path == "" || strings.HasPrefix(p, path+"/") {
		return p, d
	}
	return path, decl
}

// withheldStoreIsClean refuses a repository whose object store holds objects
// no ref reaches. The reflog does not count as reaching one: the clone copies
// the store and not the reflog, so an object only the reflog names arrives
// with nothing to say where it came from.
func withheldStoreIsClean(member, repo string) error {
	cmd := exec.Command("git", "-C", repo, "fsck", "--unreachable", "--no-reflogs", "--connectivity-only", "--no-progress")
	out, _ := cmd.Output()
	n := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "unreachable ") {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%s: repo %s withholds paths, and the repository holds %d objects no ref reaches\n\n"+
		"A member's clone copies the whole object store, so an object the reflog alone names, or\n"+
		"one left by a rebase or an amend, arrives where no history check can see it. Run\n\n"+
		"  git -C %s reflog expire --expire=now --all && git -C %s gc --prune=now\n\n"+
		"and validate again", member, repo, n, repo, repo)
}

// withheldInSnapshot walks the frozen tree and refuses on the first path a
// declaration withholds.
func withheldInSnapshot(member, snap string, decls []string) error {
	return filepath.WalkDir(snap, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%s: snapshot %s: %w", member, snap, err)
		}
		rel, relErr := filepath.Rel(snap, p)
		if relErr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if decl, hit := withhold.Hit(decls, rel); hit {
			return fmt.Errorf("%s: snapshot %s withholds %s, and the tree holds %s\n\n"+
				"A snapshot arrives as the directory stands. Remove the path, or give this member a\n"+
				"snapshot that does not hold it", member, snap, decl, rel)
		}
		return nil
	})
}
