package cli

// Both doctors print in the shape `cs-sandbox doctor` prints: a header, titled
// groups, and one badged line per check. An operator runs the two back to back
// when something is wrong, and two different renderings of the same kind of
// answer make them read the second one twice.
//
// Progressive rather than buffered, which is where this parts company with
// cs-sandbox's renderer. That one diagnoses a local host in milliseconds and
// can afford to assemble the whole report before printing it. A campaign doctor
// ssh's into every member, so the same choice here would leave the operator
// watching nothing for minutes and unable to tell a slow probe from a hang.
//
// A section is therefore announced lazily, on its first check: the group title
// and the line under it appear together, and a group whose checks all turned
// out to be inapplicable prints no empty heading.

import (
	"errors"
	"fmt"
	"io"
)

// errChecksFailed is what doctor returns once it has printed its report. The
// report IS the message, so this stays terse rather than restating it.
var errChecksFailed = errors.New("host checks failed")

const (
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

// doctorPrinter renders a grouped report as it is measured, and counts the
// checks that failed so the caller can set its exit status from one place.
type doctorPrinter struct {
	w io.Writer
	// pending is a section announced but not yet printed, waiting for a check
	// to prove it has something in it.
	pending string
	// open reports whether a section heading is on screen, so the blank line
	// between groups is written before the next heading rather than after the
	// last, which would leave a trailing gap above the summary.
	open   bool
	issues int
}

func newDoctorPrinter(w io.Writer, header string) *doctorPrinter {
	fmt.Fprintf(w, "%s\n\n", header)
	return &doctorPrinter{w: w}
}

// section names the group the following checks belong to.
func (p *doctorPrinter) section(title string) { p.pending = title }

func (p *doctorPrinter) heading() {
	if p.pending == "" {
		return
	}
	if p.open {
		fmt.Fprintln(p.w)
	}
	fmt.Fprintf(p.w, "%s:\n", p.pending)
	p.pending, p.open = "", true
}

// ok, warn and bad are the three gradings, matching cs-sandbox's. Only bad
// counts against the host: a warning is a true finding that stops nothing, and
// counting it would make a doctor fail over a tool no campaign needs.
func (p *doctorPrinter) ok(format string, args ...any)   { p.line(ansiGreen, "ok", format, args...) }
func (p *doctorPrinter) warn(format string, args ...any) { p.line(ansiYellow, "??", format, args...) }

func (p *doctorPrinter) bad(format string, args ...any) {
	p.issues++
	p.line(ansiRed, "NO", format, args...)
}

func (p *doctorPrinter) line(color, badge, format string, args ...any) {
	p.heading()
	fmt.Fprintf(p.w, "  %s%s%s  %s\n", color, badge, ansiReset, fmt.Sprintf(format, args...))
}

// summary closes the report with the next thing to do, or the count to fix.
// nextStep is shown only when nothing failed.
func (p *doctorPrinter) summary(nextStep string) {
	if p.open {
		fmt.Fprintln(p.w)
	}
	if p.issues == 0 {
		fmt.Fprintf(p.w, "All good — %s\n", nextStep)
		return
	}
	fmt.Fprintf(p.w, "%d issue(s) to fix above.\n", p.issues)
}
