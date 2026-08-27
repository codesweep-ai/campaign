package cli

// The renderer is what makes a doctor readable, and it has two properties worth
// holding: a group heading appears only when the group has something in it, and
// only a failed check counts against the host. Getting either wrong is invisible
// in a passing run and misleading in the run that matters.

import (
	"strings"
	"testing"
)

// plain strips the colour so an assertion reads the words rather than escapes.
func plain(s string) string {
	for _, code := range []string{ansiGreen, ansiRed, ansiYellow, ansiReset} {
		s = strings.ReplaceAll(s, code, "")
	}
	return s
}

func TestDoctorPrinterGroupsAndGrades(t *testing.T) {
	var out strings.Builder
	p := newDoctorPrinter(&out, "cs-campaign doctor")
	p.section("first")
	p.ok("all is well")
	p.section("second")
	p.warn("worth knowing")
	p.bad("actually broken")
	p.summary("try: cs-campaign init <name>")

	got := plain(out.String())
	want := "cs-campaign doctor\n\nfirst:\n  ok  all is well\n\nsecond:\n  ??  worth knowing\n  NO  actually broken\n\n1 issue(s) to fix above.\n"
	if got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
	// A warning is a true finding that stops nothing. Counting it would fail a
	// doctor over a tool no campaign needs, which is exactly the refusal the
	// sibling checks were designed not to make.
	if p.issues != 1 {
		t.Errorf("issues = %d, want 1 (the warning must not count)", p.issues)
	}
}

// A section announced and never used prints nothing. Doctor names groups before
// it knows whether they apply, so an eager heading would leave empty titles on
// a host where a probe was skipped.
func TestDoctorPrinterSkipsAnEmptySection(t *testing.T) {
	var out strings.Builder
	p := newDoctorPrinter(&out, "cs-campaign doctor")
	p.section("present")
	p.ok("something")
	p.section("never used")
	p.summary("try: cs-campaign init <name>")

	got := plain(out.String())
	if strings.Contains(got, "never used") {
		t.Errorf("an empty section must not print its heading:\n%s", got)
	}
	if !strings.HasSuffix(got, "All good — try: cs-campaign init <name>\n") {
		t.Errorf("a clean report must end with the next step:\n%s", got)
	}
}
