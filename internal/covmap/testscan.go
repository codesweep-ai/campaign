package covmap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnumerateTests parses *_test.go files in the given directories and returns
// the sorted, de-duplicated names of top-level Test* functions. Used to
// prune results whose proving test no longer exists.
func EnumerateTests(dirs ...string) ([]string, error) {
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					seen[fn.Name.Name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// CampaignTestDirs are the packages whose tests may emit proofs, under root.
func CampaignTestDirs(root string) []string {
	return []string{
		filepath.Join(root, "internal", "cli"),
		filepath.Join(root, "internal", "store"),
	}
}

// SandboxRoot resolves the sibling cs-sandbox checkout ($CS_SANDBOX_REPO
// override) whose contract tests emit scripts-tier proofs.
func SandboxRoot(root string) string {
	if v := os.Getenv("CS_SANDBOX_REPO"); v != "" {
		return v
	}
	return filepath.Join(root, "..", "sandbox")
}

// SandboxTestDirs are the sandbox packages that emit proofs.
func SandboxTestDirs(sandboxRoot string) []string {
	return []string{
		filepath.Join(sandboxRoot, "internal", "cli"),
		filepath.Join(sandboxRoot, "internal", "seed"),
	}
}

// ExistsSets builds the per-repo test-existence sets Fold prunes against.
// The sandbox set is nil (unverifiable, keep records) when the checkout is
// absent.
func ExistsSets(root string) (map[string]map[string]bool, error) {
	campaign, err := EnumerateTests(CampaignTestDirs(root)...)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{"campaign": toSet(campaign)}
	sb := SandboxRoot(root)
	if _, err := os.Stat(filepath.Join(sb, "internal", "cli")); err == nil {
		sandbox, err := EnumerateTests(SandboxTestDirs(sb)...)
		if err != nil {
			return nil, err
		}
		out["sandbox"] = toSet(sandbox)
	}
	return out, nil
}

func toSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}
