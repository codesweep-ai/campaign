package cli

import (
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/dispatch-viewer/internal/frames"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args     []string
		dir, out string
		err      error
		fails    bool
	}{
		{args: []string{"run01"}, dir: "run01", out: "viewer.html"},
		{args: []string{"run01", "-o", "x.html"}, dir: "run01", out: "x.html"},
		{args: []string{"--out", "x.html", "run01"}, dir: "run01", out: "x.html"},
		{args: []string{"help"}, err: errHelp},
		{args: []string{"--help", "run01"}, err: errHelp},
		{args: []string{"version"}, err: errVersion},
		{args: []string{"manual"}, err: errManual},
		{args: []string{}, fails: true},
		{args: []string{"-o"}, fails: true},
		{args: []string{"a", "b"}, fails: true},
		{args: []string{"--bogus"}, fails: true},
	}
	for _, c := range cases {
		o, err := parseArgs(c.args)
		switch {
		case c.err != nil:
			if err != c.err {
				t.Errorf("%v: want %v, got %v", c.args, c.err, err)
			}
		case c.fails:
			if err == nil {
				t.Errorf("%v: expected an error", c.args)
			}
		default:
			if err != nil || o.dir != c.dir || o.out != c.out {
				t.Errorf("%v: got (%q, %q, %v)", c.args, o.dir, o.out, err)
			}
		}
	}
}

// A note containing </script> must not be able to break out of the injected
// data block, and the block must land where the app script can find it.
func TestAssembleEscapesAndPlaces(t *testing.T) {
	run := &frames.Run{
		SchemaVersion: frames.SchemaVersion,
		Replies: map[string]map[string]*frames.Reply{
			"dev": {"d002": {Dispatch: "d002", Note: "evil </script><script>alert(1)</script>"}},
		},
	}
	page, err := assemble(run)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Count(html, `id="run-data"`) != 1 {
		t.Fatal("exactly one data block expected")
	}
	before, rest, found := strings.Cut(html, `id="run-data"`)
	if !found {
		t.Fatal("no data block in the page")
	}
	if strings.Contains(before, "renderTimeline") {
		t.Fatal("data block must precede the app script")
	}
	block, _, closed := strings.Cut(rest, "</script>")
	if !closed {
		t.Fatal("the data block is never closed")
	}
	if strings.Contains(block, "<script>alert") {
		t.Fatal("note content escaped the data block")
	}
	if strings.Contains(html, "<!--RUN-DATA-->") {
		t.Fatal("marker survived injection")
	}
}
