package cli

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// profileLevel is one place in a profile that holds keys. where is the label
// MANUAL.md's key table uses, and phrase is how an error names the level.
type profileLevel struct {
	where, phrase string
	t             reflect.Type
}

// profileLevels is every struct a profile decodes into. The decoder, the
// unknown-key message and the manual's key table all read the same yaml tags,
// so a key added to a struct is in the message at once, and a test holds the
// table to it.
var profileLevels = []profileLevel{
	{"profile", "the profile's top level", reflect.TypeFor[model.Profile]()},
	{"defaults", "defaults", reflect.TypeFor[model.Defaults]()},
	{"seat", "a seat", reflect.TypeFor[model.MemberProfile]()},
	{"policy", "a policy block", reflect.TypeFor[protocol.Policy]()},
	{"resources", "resources", reflect.TypeFor[model.Resources]()},
	{"repos[]", "a repos entry", reflect.TypeFor[model.Repo]()},
	{"snapshots[]", "a snapshots entry", reflect.TypeFor[model.Snapshot]()},
	{"auth", "auth", reflect.TypeFor[model.Auth]()},
}

// yamlKeys is every key a struct decodes, in declaration order.
func yamlKeys(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

var unknownFieldLine = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)

// explainProfileError rewrites the decoder's unknown-field lines, which name a
// Go type, into the level the key sat at, the nearest key that level takes,
// and every key it takes. Every other decode error passes through unchanged.
//
// It matters because an operator who guesses a key learns the real ones from
// this message or not at all: the binary is often all they have.
func explainProfileError(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err
	}
	lines := make([]string, 0, len(te.Errors))
	explained := false
	for _, e := range te.Errors {
		m := unknownFieldLine.FindStringSubmatch(e)
		if m == nil {
			lines = append(lines, e)
			continue
		}
		msg, ok := unknownKey(m[1], m[2], m[3])
		if !ok {
			lines = append(lines, e)
			continue
		}
		lines = append(lines, msg)
		explained = true
	}
	if !explained {
		return err
	}
	return errors.New(strings.Join(lines, "\n") +
		"\nEvery key is listed in `cs-campaign manual`, under Configuration, Every profile key.")
}

func unknownKey(line, key, goType string) (string, bool) {
	i := slices.IndexFunc(profileLevels, func(l profileLevel) bool { return l.t.String() == goType })
	if i < 0 {
		return "", false
	}
	level := profileLevels[i]
	keys := yamlKeys(level.t)
	msg := fmt.Sprintf("line %s: %s has no key %q", line, level.phrase, key)
	if near := nearestKey(key, keys); near != "" {
		msg += fmt.Sprintf(" (did you mean %q?)", near)
	} else if elsewhere := levelsWith(key); len(elsewhere) > 0 {
		msg += fmt.Sprintf(" (%q goes in %s)", key, strings.Join(elsewhere, " or "))
	}
	return msg + fmt.Sprintf("; %s takes %s", level.phrase, strings.Join(keys, ", ")), true
}

// levelsWith names the other levels that take key, for a key put at the wrong
// depth: handoffTokens on a seat rather than in its policy block.
func levelsWith(key string) []string {
	var out []string
	for _, l := range profileLevels {
		if slices.Contains(yamlKeys(l.t), key) {
			out = append(out, l.phrase)
		}
	}
	return out
}

// nearestKey is the one key within a small edit distance of key, or one that
// differs only in case. A short key gets no guess, since two edits turn any
// three-letter key into most others.
func nearestKey(key string, keys []string) string {
	best, bestD := "", 3
	for _, k := range keys {
		if strings.EqualFold(k, key) {
			return k
		}
		if d := editDistance(strings.ToLower(key), strings.ToLower(k)); d < bestD && d*3 <= len(k) {
			best, bestD = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
