package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/dispatch-viewer/internal/frames"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args            []string
		dir, file, site string
		err             error
		fails           bool
	}{
		{args: []string{"run01"}, dir: "run01", file: "viewer.html"},
		{args: []string{"run01", "--file", "x.html"}, dir: "run01", file: "x.html"},
		{args: []string{"--site", "s", "run01"}, dir: "run01", site: "s"},
		{args: []string{"run01", "--file", "x.html", "--site", "s"}, fails: true},
		{args: []string{"run01", "-o", "x.html"}, fails: true},
		{args: []string{"run01", "--no-traces"}, fails: true},
		{args: []string{"run01", "--site"}, fails: true},
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
			if err != nil || o.dir != c.dir || o.file != c.file || o.site != c.site {
				t.Errorf("%v: got (%q, %q, %q, %v)", c.args, o.dir, o.file, o.site, err)
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

// siteArchive writes a two-member archive and a fake cs-tracer that gives the
// orchestrator one session, and puts the tracer first on PATH.
func siteArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("archive/campaign.json", `{"name":"site1","id":"site1-1","members":[
	  {"name":"orch","role":"orchestrator","cli":"claude-code"},{"name":"dev","role":"agent","cli":"opencode"}]}`)
	write("archive/orchestrator/input/m1.md", "# mission")
	write("archive/agents/dev/input/d001.md", "# readback")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "s.jsonl", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("{}\n"))
	_ = tw.Close()
	_ = gz.Close()
	write("archive/orchestrator/transcript/cli-evidence.tgz", buf.String())
	bin := filepath.Join(root, "bin")
	write("bin/cs-tracer", `#!/bin/sh
case "$1" in
version) echo fake-tracer 1 ;;
help) echo "normalize --split" ;;
normalize)
  mkdir -p "$4/s1/chunks"
  echo '{"schemaVersion":3,"trajectories":[{"id":"s1","path":"s1"}]}' > "$4/index.json"
  echo '{"schemaVersion":3,"meta":{"source":"claude-code"},"totals":{"events":1,"output":5,"time":{"workMs":1000}},"strip":[{"i":0,"kind":"user","ts":"2026-09-22T00:00:00Z"}]}' > "$4/s1/summary.json"
  echo '{"events":[{"i":0,"kind":"user","text":"Dispatch ID: m1."}]}' > "$4/s1/chunks/000.json" ;;
*) mkdir -p "$4/traces" && echo '{"files":["traces/s1.html"]}' > "$4/.cs-tracer.json" && echo page > "$4/traces/s1.html" ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "cs-tracer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root
}

// A site holds the page, the data it embeds, the facts, the kept
// trajectories and the agent's page, and marks itself so it can be replaced.
func TestSiteWritesItsFiles(t *testing.T) {
	root := siteArchive(t)
	site := filepath.Join(root, "site")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{root, "--site", site}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, f := range []string{"index.html", "run-data.json", "facts.json", "AGENTS.md", "CLAUDE.md",
		"traces/s1.json", "tracer/traces/s1.html", siteMarker} {
		if _, err := os.Stat(filepath.Join(site, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	// run-data.json is the document the page embeds.
	var run frames.Run
	raw, _ := os.ReadFile(filepath.Join(site, "run-data.json"))
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.Campaign.Name != "site1" || run.Traces == nil || run.Traces.Sessions[0].Strip[0].Addr != "traces/e/"+strconv.Itoa(len(run.Events)) {
		t.Errorf("run-data: %+v", run.Traces)
	}
	guide, _ := os.ReadFile(filepath.Join(site, "AGENTS.md"))
	for _, want := range []string{"campaign run site1", "| dev | agent | opencode |", "`recovery.events[].turnError`", "fake-tracer 1"} {
		if !strings.Contains(string(guide), want) {
			t.Errorf("AGENTS.md lacks %q", want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(site, "CLAUDE.md")); string(b) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q", b)
	}
}

// A second build replaces the site it wrote, and never a folder it did not.
func TestSiteReplacesOnlyASite(t *testing.T) {
	root := siteArchive(t)
	site := filepath.Join(root, "site")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{root, "--site", site}, &stdout, &stderr); code != 0 {
		t.Fatalf("first build: exit %d: %s", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(site, "traces", "stale.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{root, "--site", site}, &stdout, &stderr); code != 0 {
		t.Fatalf("rebuild: exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(site, "traces", "stale.json")); err == nil {
		t.Error("the rebuild kept a file the previous site held under traces/")
	}

	foreign := filepath.Join(root, "notes")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "mine.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := Main([]string{root, "--site", foreign}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "holds files a site did not write") {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if entries, _ := os.ReadDir(foreign); len(entries) != 1 {
		t.Errorf("the refused folder now holds %d entries", len(entries))
	}
}

// Every field of both files is described in the site's AGENTS.md, and no
// description outlives its field.
func TestReferencesCoverTheTypes(t *testing.T) {
	if _, err := references(); err != nil {
		t.Fatal(err)
	}
}
