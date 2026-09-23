package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// fieldPaths lists every JSON path a document of type t can hold, in field
// order: a slice element is written `[]`, a map key `<key>`. It stops at raw
// JSON, which a reference describes whole, and at the shapes in stop, which a
// reference describes once. An embedded shape adds no path of its own.
func fieldPaths(t reflect.Type, stop ...reflect.Type) []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	raw := reflect.TypeFor[json.RawMessage]()
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch {
		case t == raw || slices.Contains(stop, t):
			return
		case t.Kind() == reflect.Slice:
			walk(t.Elem(), prefix+"[]")
			return
		case t.Kind() == reflect.Map:
			walk(t.Elem(), prefix+".<"+keyName(prefix)+">")
			return
		case t.Kind() != reflect.Struct || t.PkgPath() == "time":
			return
		}
		for f := range t.Fields() {
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				walk(f.Type, prefix)
				continue
			}
			if name == "" {
				name = f.Name
			}
			p := name
			if prefix != "" {
				p = prefix + "." + name
			}
			out = append(out, p)
			walk(f.Type, p)
		}
	}
	walk(t, "")
	return out
}

// keyName names a map's key in a path, after what the map is keyed by.
func keyName(prefix string) string {
	switch {
	case strings.HasSuffix(prefix, ".<node>"):
		return "dispatch"
	case strings.HasSuffix(prefix, "issueDefs"):
		return "code"
	case strings.HasSuffix(prefix, "failedSteps"):
		return "class"
	}
	return "node"
}

// reference renders paths as a markdown list with each path's description,
// and reports every path that has none, and every description whose path the
// type no longer has, so the reference cannot drift from the code.
func reference(t reflect.Type, docs map[string]string, stop ...reflect.Type) (string, error) {
	paths := fieldPaths(t, stop...)
	var b strings.Builder
	var missing []string
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
		d, ok := docs[p]
		if !ok {
			missing = append(missing, p)
			continue
		}
		if d == "" {
			fmt.Fprintf(&b, "- `%s`\n", p)
			continue
		}
		fmt.Fprintf(&b, "- `%s`: %s\n", p, d)
	}
	for p := range docs {
		if !seen[p] {
			missing = append(missing, p+" (described, but not in the type)")
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return "", fmt.Errorf("field reference out of date: %s", strings.Join(missing, ", "))
	}
	return b.String(), nil
}
