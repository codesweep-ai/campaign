package withhold

import "testing"

func TestADeclarationWithholdsTheTreeBeneathIt(t *testing.T) {
	cases := []struct {
		decl, path string
		want       bool
	}{
		{"results", "results", true},
		{"results", "results/a.txt", true},
		{"results", "results/deep/b.txt", true},
		{"results", "result", false},
		{"results", "src/results", false},
		{"results/answers.txt", "results/answers.txt", true},
		{"results/answers.txt", "results/answers.txt.bak", false},
		{"results/*", "results/a.txt", true},
		{"results/*", "results/deep/b.txt", true},
		{"results/*", "results", false},
		{"**/golden", "a/b/golden/x", true},
		{"**/golden", "golden/x", true},
		{"**/golden", "a/golden.txt", false},
		{"*.key", "server.key", true},
		{"*.key", "etc/server.key", false},
		{"**/*.key", "etc/server.key", true},
		{"docs/**/private", "docs/private/x", true},
		{"docs/**/private", "docs/a/b/private", true},
		{"docs/**/private", "docs/public/x", false},
	}
	for _, c := range cases {
		if got := Match(c.decl, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.decl, c.path, got, c.want)
		}
	}
}

func TestNormalizeRefusesWhatCannotBeAPath(t *testing.T) {
	for _, bad := range []string{"", "/", "/etc", ".", "..", "a/../b", "a//b", "**", "./a"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) accepted it", bad)
		}
	}
	cases := []struct{ in, want string }{{"results/", "results"}, {" results ", "results"}, {"a/*/b", "a/*/b"}}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if err != nil || got != c.want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestHitNamesTheDeclarationThatMatched(t *testing.T) {
	d, ok := Hit([]string{"src", "results/**"}, "results/a")
	if !ok || d != "results/**" {
		t.Fatalf("Hit = %q, %v", d, ok)
	}
	if _, ok := Hit([]string{"src"}, "docs/x"); ok {
		t.Fatal("a path outside every declaration was hit")
	}
}
