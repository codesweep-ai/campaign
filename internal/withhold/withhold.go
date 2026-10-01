// Package withhold matches repository paths against the paths a profile
// declares a member must never receive.
//
// A declaration is a path relative to the root of a repository or snapshot.
// It names a file, a directory, or a glob: `*` and `?` match within one path
// segment and `**` matches any run of segments. Whatever a declaration
// matches, it also withholds everything beneath it, so `results` and
// `results/**` withhold the same tree.
package withhold

import (
	"fmt"
	"path"
	"strings"
)

// Normalize returns the declaration in the form Match expects, or says why it
// cannot be one. A declaration is relative, has no empty or `.` or `..`
// segments, and is not the whole tree.
func Normalize(decl string) (string, error) {
	d := strings.TrimSpace(decl)
	d = strings.TrimSuffix(d, "/")
	if d == "" || d == "." || d == "**" {
		return "", fmt.Errorf("withhold %q names the whole tree; give the member no copy instead", decl)
	}
	if strings.HasPrefix(d, "/") {
		return "", fmt.Errorf("withhold %q is absolute; declare a path relative to the repository root", decl)
	}
	for seg := range strings.SplitSeq(d, "/") {
		switch seg {
		case "", ".", "..":
			return "", fmt.Errorf("withhold %q has a %q segment; declare a plain relative path", decl, seg)
		}
	}
	return d, nil
}

// Match reports whether p, a slash-separated path relative to the tree's
// root, is withheld by the normalized declaration decl.
func Match(decl, p string) bool {
	return matchSegs(strings.Split(decl, "/"), strings.Split(strings.Trim(p, "/"), "/"))
}

// matchSegs matches declaration segments against a prefix of the path's
// segments. A declaration that runs out has matched a directory, and
// everything beneath it is withheld; a path that runs out first is above the
// declaration and is not.
func matchSegs(decl, segs []string) bool {
	if len(decl) == 0 {
		return true
	}
	if decl[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(decl[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(decl[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegs(decl[1:], segs[1:])
}

// Hit returns the first declaration that withholds p.
func Hit(decls []string, p string) (string, bool) {
	for _, d := range decls {
		if Match(d, p) {
			return d, true
		}
	}
	return "", false
}
