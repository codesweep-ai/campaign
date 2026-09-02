//go:build ignore

// consumer-conventions — the house rules a @codesweep-ai/ui consumer must keep.
//
// Vendored byte-identical into ledger, tracer and campaign. It takes no
// configuration: every path is derived, so the same file works in all three
// despite their different layouts. Keep the copies identical; the three repos'
// previous checks were three separate implementations and the gaps below are
// exactly what that divergence hid.
//
// Run as:  go run scripts/consumer-conventions.go
//
// It is a standalone file, not a package: `//go:build ignore` keeps it out of
// ./... so vet, deadcode, golangci-lint and the coverage baseline ignore it.
//
// THE PIN. The ui dependency is a committed tarball whose filename carries the
// ui commit's short SHA, because npm keys a file: dependency on the specifier
// string and silently reuses a tarball swapped underneath an unchanged name.
// The filename is therefore the pin, and these rules keep every part of the
// repo agreeing about it:
//
//  1. exactly one committed tarball
//  2. every @codesweep-ai/ui specifier names that tarball
//  3. every lockfile entry resolves to it AND carries an integrity
//  4. the installed marker's SHA starts with the filename's short SHA
//
// Rule 3's integrity half is the one a human keeps losing: `npm install` after
// a tarball swap leaves the field absent and says nothing, and `npm ci` then
// installs without verifying the bytes — which is the very hole the SHA in the
// filename exists to plug. Restore it with the explicit form:
//
//	rm -rf node_modules && npm install ./<dir>/ui-tarball/<the tarball>
//
// Rule 4 is the only one needing an install, so it is the only one that skips.
// Rules 1-3 read committed files and always run, including on a Go-only clone.
//
// NAMING. A target that re-records committed files must say so in its name: a
// destructive target wearing an innocuous name gets run by someone expecting a
// test. `make fixtures` once meant a read-only oracle in two of these repos and
// live recording in the third.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const pkg = "@codesweep-ai/ui"

var problems []string

func fail(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

// walk visits every file under root, skipping node_modules, .git and dist.
func walk(fn func(path string, d os.DirEntry)) {
	filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		fn(path, d)
		return nil
	})
}

func readJSON(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// A pin is one of two things, and exactly one field is set.
type pin struct {
	tarball string // basename of the committed .tgz, e.g. codesweep-ai-ui-0.2.0+f6252d4.tgz
	version string // exact registry version, e.g. 0.2.1-dev.20260831063919.0d88493
}

func (p pin) String() string {
	if p.tarball != "" {
		return p.tarball
	}
	return p.version
}

func (p pin) isTarball() bool { return p.tarball != "" }

func main() {
	p, ok := findPin()
	if ok {
		checkSpecifiers(p)
		checkLocks(p)
		checkInstalled(p)
	}
	checkNaming()

	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "conventions: FAIL")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		os.Exit(1)
	}
	fmt.Println("conventions: ok")
}

// 1. the repo pins one way, and says which. A committed tarball wins where one
// exists, because a repo carrying one is choosing to install without a
// registry; otherwise the specifiers have to agree on one exact version.
func findPin() (pin, bool) {
	var found []string
	walk(func(path string, d os.DirEntry) {
		if filepath.Base(filepath.Dir(path)) == "ui-tarball" && strings.HasSuffix(path, ".tgz") {
			found = append(found, path)
		}
	})
	if len(found) > 1 {
		fail("%d committed ui tarballs, expected exactly one: %s", len(found), strings.Join(found, ", "))
		return pin{}, false
	}
	if len(found) == 1 {
		base := filepath.Base(found[0])
		if shortSHA(base) == "" {
			fail("%s does not carry a 7-character short SHA (want codesweep-ai-ui-<version>+<sha7>.tgz)", base)
			return pin{}, false
		}
		fmt.Printf("  pin        tarball %s\n", found[0])
		return pin{tarball: base}, true
	}

	// No tarball, so the pin is whatever the specifiers say -- and they have to
	// say one thing. A range here would let two clones install two different
	// builds from the same commit, which is the whole point of pinning at all.
	versions := map[string]string{}
	eachSpecifier(func(path, spec string) {
		versions[spec] = path
	})
	if len(versions) == 0 {
		fail("nothing pins %s: no committed */ui-tarball/*.tgz and no package.json depends on it", pkg)
		return pin{}, false
	}
	if len(versions) > 1 {
		var parts []string
		for spec, path := range versions {
			parts = append(parts, fmt.Sprintf("%s in %s", spec, path))
		}
		sort.Strings(parts)
		fail("%s is pinned %d different ways: %s", pkg, len(versions), strings.Join(parts, ", "))
		return pin{}, false
	}
	var spec string
	for v := range versions {
		spec = v
	}
	if !exactRe.MatchString(spec) {
		fail("%s: %s is %q, which is a range rather than one version. Pin an exact version, so a\n"+
			"      reinstall cannot move the package underneath a committed page", versions[spec], pkg, spec)
		return pin{}, false
	}
	fmt.Printf("  pin        registry %s\n", spec)
	return pin{version: spec}, true
}

var (
	shaRe   = regexp.MustCompile(`\+([0-9a-f]{7})\.tgz$`)
	exactRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.\-+]+)?$`)
)

func shortSHA(base string) string {
	if m := shaRe.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return ""
}

// eachSpecifier visits every @codesweep-ai/ui specifier in every package.json.
func eachSpecifier(fn func(path, spec string)) {
	walk(func(path string, d os.DirEntry) {
		if d.Name() != "package.json" {
			return
		}
		var m struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if readJSON(path, &m) != nil {
			return
		}
		for _, deps := range []map[string]string{m.Dependencies, m.DevDependencies} {
			if spec, ok := deps[pkg]; ok {
				fn(path, spec)
			}
		}
	})
}

// 2. every specifier names the pin.
func checkSpecifiers(p pin) {
	before := len(problems)
	n := 0
	eachSpecifier(func(path, spec string) {
		n++
		if !p.isTarball() {
			if spec != p.version {
				fail("%s: %s is %q but the pin is %q", path, pkg, spec, p.version)
			}
			return
		}
		if !strings.HasPrefix(spec, "file:") {
			fail("%s: %s is %q, not a file: tarball", path, pkg, spec)
			return
		}
		if got := filepath.Base(spec); got != p.tarball {
			fail("%s: %s names %s but the committed tarball is %s", path, pkg, got, p.tarball)
		}
	})
	if n == 0 {
		fail("no package.json declares %s, but a tarball is committed", pkg)
		return
	}
	if len(problems) == before {
		fmt.Printf("  specifiers %d, all naming %s\n", n, p)
	}
}

// 3. every lock entry resolves to the pin and carries an integrity.
//
// The integrity half is the one a human keeps losing on a tarball pin: `npm
// install` after a swap leaves the field absent and says nothing, and `npm ci`
// then installs without verifying the bytes. On a registry pin npm writes it
// itself, and this rule is what notices if it ever stops.
func checkLocks(p pin) {
	before := len(problems)
	n := 0
	walk(func(path string, d os.DirEntry) {
		if d.Name() != "package-lock.json" {
			return
		}
		var m struct {
			Packages map[string]struct {
				Version   string `json:"version"`
				Resolved  string `json:"resolved"`
				Integrity string `json:"integrity"`
			} `json:"packages"`
		}
		if readJSON(path, &m) != nil {
			return
		}
		e, ok := m.Packages["node_modules/"+pkg]
		if !ok {
			return
		}
		n++
		if p.isTarball() {
			if got := filepath.Base(e.Resolved); got != p.tarball {
				fail("%s: lock resolves %s to %s, but the committed tarball is %s", path, pkg, got, p.tarball)
			}
		} else if e.Version != p.version {
			fail("%s: lock holds %s %s, but the pin is %s", path, pkg, e.Version, p.version)
		}
		if strings.TrimSpace(e.Integrity) == "" {
			fail("%s: lock entry for %s has NO integrity — npm ci will not verify its bytes.\n"+
				"      restore it with: rm -rf node_modules package-lock.json && npm install", path, pkg)
		}
	})
	if n == 0 {
		fail("no package-lock.json carries an entry for %s", pkg)
		return
	}
	if len(problems) == before {
		fmt.Printf("  lockfiles  %d, resolved and with integrity\n", n)
	}
}

// 4. the installed copy matches the pin. The only rule that needs an install,
// so the only one that skips.
func checkInstalled(p pin) {
	before := len(problems)
	if !p.isTarball() {
		checkInstalledVersion(p)
		return
	}
	want := shortSHA(p.tarball)
	var markers []string
	filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "BUILD.json" && strings.Contains(filepath.ToSlash(path), "node_modules/"+pkg+"/dist/") {
			markers = append(markers, path)
		}
		return nil
	})
	if len(markers) == 0 {
		fmt.Printf("  installed  SKIP (no install found; run npm ci to check the installed package)\n")
		return
	}
	for _, m := range markers {
		var b struct {
			SHA string `json:"sha"`
		}
		if readJSON(m, &b) != nil {
			fail("%s: unreadable install marker", m)
			continue
		}
		if !strings.HasPrefix(b.SHA, want) {
			fail("%s: installed ui is %s but the committed tarball is %s.\n"+
				"      the install is stale or the tarball was swapped; reinstall with:\n"+
				"      rm -rf node_modules && npm install ./<dir>/ui-tarball/%s", m, b.SHA, p.tarball, p.tarball)
		}
	}
	if len(problems) == before {
		fmt.Printf("  installed  %d marker(s) matching %s\n", len(markers), want)
	}
}

// A registry install carries its version in its own package.json, which is what
// npm resolved rather than what the lockfile asked for.
func checkInstalledVersion(p pin) {
	before := len(problems)
	var installed []string
	filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "package.json" && strings.HasSuffix(filepath.ToSlash(filepath.Dir(path)), "node_modules/"+pkg) {
			installed = append(installed, path)
		}
		return nil
	})
	if len(installed) == 0 {
		fmt.Printf("  installed  SKIP (no install found; run npm ci to check the installed package)\n")
		return
	}
	for _, path := range installed {
		var m struct {
			Version string `json:"version"`
		}
		if readJSON(path, &m) != nil {
			fail("%s: unreadable installed package.json", path)
			continue
		}
		if m.Version != p.version {
			fail("%s: installed %s is %s but the pin is %s. Reinstall with: npm ci", path, pkg, m.Version, p.version)
		}
	}
	if len(problems) == before {
		fmt.Printf("  installed  %d, matching %s\n", len(installed), p.version)
	}
}

// NAMING: a target that re-records committed files must be named record-*.
var (
	targetRe = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9_.\-]*)\s*:(?:[^=]|$)`)
	recordRe = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*RECORD[A-Z0-9_]*=1\b`)
)

func checkNaming() {
	b, err := os.ReadFile("Makefile")
	if err != nil {
		fmt.Println("  naming     SKIP (no Makefile)")
		return
	}
	var target string
	var recipe strings.Builder
	checked, flagged := 0, 0
	finish := func() {
		if target == "" {
			return
		}
		checked++
		if recordRe.MatchString(recipe.String()) && !strings.HasPrefix(target, "record-") {
			flagged++
			fail("Makefile: target %q re-records committed files but is not named record-*.\n"+
				"      a destructive target under an innocuous name gets run by someone expecting a test", target)
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "\t") {
			recipe.WriteString(line)
			recipe.WriteString("\n")
			continue
		}
		if m := targetRe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, ".") {
			finish()
			target, recipe = m[1], strings.Builder{}
		}
	}
	finish()
	if flagged == 0 {
		fmt.Printf("  naming     %d targets, no unnamed recording target\n", checked)
	}
}
