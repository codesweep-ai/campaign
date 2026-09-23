// Package cli is the cs-dispatch-viewer command: parse args, load one run
// archive through frames, and write a self-contained viewer.html. The arg
// parser is hand-rolled with injected writers, after tracer's cli.go.
package cli

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"text/template"

	"github.com/codesweep-ai/campaign"
	"github.com/codesweep-ai/campaign/dispatch-viewer/internal/frames"
)

//go:embed shell/viewer.html
var shell string

// devVersion marks a binary that carried no release stamp.
const devVersion = "dev"

var version = devVersion

// buildVersion reports the release stamp when there is one, and otherwise the
// module version the toolchain recorded. A binary installed straight from the
// module path carries no stamp, so without this it would answer "dev" and
// leave you guessing which revision rendered a dispatch.
func buildVersion() string {
	if version != devVersion {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return version
	}
	return info.Main.Version
}

const usage = `cs-dispatch-viewer — render one campaign run archive as a self-contained HTML timeline

usage:
  cs-dispatch-viewer <run-dir> [--file <name.html> | --site <dir>] [--tracer <bin>]
  cs-dispatch-viewer manual | version | help

<run-dir> is a campaign archive directory (holding campaign.json) or a run
directory holding archive/.

With no flag it writes one self-contained viewer.html in the current
directory, and --file names that file. When cs-tracer is on PATH, or named by
--tracer, every member's transcript is drawn inside its dispatches. Without
one, the page draws the dispatches alone and says why.

--site <dir> writes a folder to read the run in, and to ask an agent about it:
index.html, the tracer's pages in tracer/, run-data.json, every session's
trajectory in traces/, facts.json, and an AGENTS.md saying how to use them. A
site needs cs-tracer. <dir> must be new, empty, or a site written before,
which is replaced.
`

type options struct {
	dir    string
	file   string
	site   string
	tracer string
}

var errHelp = errors.New("help")
var errVersion = errors.New("version")
var errManual = errors.New("manual")

func parseArgs(args []string) (options, error) {
	var o options
	value := func(i *int, a string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a path", a)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch {
		case a == "help", a == "--help", a == "-h":
			return o, errHelp
		case a == "version", a == "--version":
			return o, errVersion
		case a == "manual":
			return o, errManual
		case a == "--file":
			o.file, err = value(&i, a)
		case a == "--site":
			o.site, err = value(&i, a)
		case a == "--tracer":
			o.tracer, err = value(&i, a)
		case a == "-o", a == "--out":
			return o, fmt.Errorf("%s is gone: name one file with --file, or a folder with --site", a)
		case a == "--no-traces":
			return o, errors.New("--no-traces is gone: a page draws traces whenever a tracer is found")
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		case o.dir != "":
			return o, fmt.Errorf("one run directory only (got %q and %q)", o.dir, a)
		default:
			o.dir = a
		}
		if err != nil {
			return o, err
		}
	}
	if o.dir == "" {
		return o, errors.New("a run directory is required")
	}
	if o.file != "" && o.site != "" {
		return o, errors.New("--file and --site are two outputs; give one")
	}
	if o.file == "" && o.site == "" {
		o.file = "viewer.html"
	}
	return o, nil
}

func Main(args []string, stdout, stderr io.Writer) int {
	o, err := parseArgs(args)
	if err == errHelp {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err == errVersion {
		fmt.Fprintf(stdout, "cs-dispatch-viewer %s (%s/%s, %s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	}
	if err == errManual {
		fmt.Fprint(stdout, campaign.ManualMD)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n%s", err, usage)
		return 2
	}
	if err := render(o, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
		return 1
	}
	return 0
}

func render(o options, stdout, stderr io.Writer) error {
	run, err := frames.Load(o.dir)
	if err != nil {
		return err
	}
	var previous []string
	if o.site != "" {
		if previous, err = siteFiles(o.site); err != nil {
			return err
		}
		if err := os.MkdirAll(o.site, 0o755); err != nil {
			return err
		}
	}
	err = frames.AttachTraces(context.Background(), run, o.dir, frames.TraceOptions{Tracer: o.tracer, Site: o.site, Stderr: stderr})
	if err != nil {
		return err
	}
	page, err := assemble(run)
	if err != nil {
		return err
	}
	out := o.file
	if o.site != "" {
		out = filepath.Join(o.site, "index.html")
		if err := writeSite(o.site, previous, run, page); err != nil {
			return err
		}
	} else if err := os.WriteFile(out, page, 0o644); err != nil {
		return err
	}
	traced := ""
	if run.Traces != nil {
		traced = fmt.Sprintf(", %d sessions, %d anchors", len(run.Traces.Sessions), len(run.Traces.Anchors))
	}
	fmt.Fprintf(stdout, "%s (%d bytes, %d events, %d issues%s)\n", out, len(page), len(run.Events), len(run.Issues), traced)
	if o.site != "" {
		fmt.Fprintf(stdout, "%s: run-data.json, facts.json, AGENTS.md, and traces/ beside it; start an agent there to ask about the run\n", o.site)
	}
	return nil
}

// siteMarker names a directory this wrote, and lists what it wrote there, so
// a later --site replaces exactly that and refuses anything else.
const siteMarker = ".cs-dispatch-viewer.json"

type marker struct {
	Tool    string   `json:"tool"`
	Version string   `json:"version"`
	Files   []string `json:"files"`
}

// siteFiles says whether dir can take a site, and returns what a previous
// site wrote there. A missing or empty directory can; one holding a marker
// can, and its files go; anything else is someone's and is refused.
func siteFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, siteMarker))
	if err != nil {
		return nil, fmt.Errorf("%s holds files a site did not write; name a new or empty directory", dir)
	}
	var m marker
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Join(dir, siteMarker), err)
	}
	for _, f := range m.Files {
		if f == "" || strings.ContainsAny(f, `/\`) || f == "." || f == ".." {
			return nil, fmt.Errorf("%s names %q, which is not a file in the site", filepath.Join(dir, siteMarker), f)
		}
	}
	return m.Files, nil
}

// writeSite writes everything but tracer/, which AttachTraces wrote. It
// builds every file before it removes what the previous site held, so a
// failure leaves that site as it was.
func writeSite(dir string, previous []string, run *frames.Run, page []byte) error {
	data, err := json.MarshalIndent(run, "", " ")
	if err != nil {
		return err
	}
	facts, err := json.MarshalIndent(frames.ComputeFacts(run), "", " ")
	if err != nil {
		return err
	}
	guide, err := siteGuide(run)
	if err != nil {
		return err
	}
	files := []struct {
		name string
		body []byte
	}{
		{"AGENTS.md", guide},
		// Claude Code reads CLAUDE.md, and imports the file it names.
		{"CLAUDE.md", []byte("@AGENTS.md\n")},
		{"facts.json", append(facts, '\n')},
		{"index.html", page},
		{"run-data.json", append(data, '\n')},
	}
	for _, f := range previous {
		if f == "tracer" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, f)); err != nil {
			return err
		}
	}
	var written []string
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.body, 0o644); err != nil {
			return err
		}
		written = append(written, f.name)
	}
	if run.Traces != nil {
		if err := frames.WriteTrajectories(run.Traces, filepath.Join(dir, "traces")); err != nil {
			return err
		}
		written = append(written, "traces")
		if _, err := os.Stat(filepath.Join(dir, "tracer")); err == nil {
			written = append(written, "tracer")
		}
	}
	m, err := json.MarshalIndent(marker{Tool: "cs-dispatch-viewer", Version: buildVersion(), Files: written}, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, siteMarker), append(m, '\n'), 0o644)
}

//go:embed shell/site-guide.md.tmpl
var guideText string

var guideTmpl = template.Must(template.New("site-guide").Parse(guideText))

// references renders the field lists of both files and of the two shapes
// facts.json repeats, which are described once and not descended into.
func references() (map[string]string, error) {
	stepRef, dur := reflect.TypeFor[frames.StepRef](), reflect.TypeFor[frames.Dur]()
	refs := map[string]string{}
	for _, r := range []struct {
		name string
		t    reflect.Type
		docs map[string]string
		stop []reflect.Type
	}{
		{"Run", reflect.TypeFor[frames.Run](), runDocs, nil},
		{"Facts", reflect.TypeFor[frames.Facts](), factsDocs, []reflect.Type{stepRef, dur}},
		{"StepRef", stepRef, stepRefDocs, nil},
		{"Dur", dur, durDocs, nil},
	} {
		text, err := reference(r.t, r.docs, r.stop...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.name, err)
		}
		refs[r.name] = text
	}
	return refs, nil
}

// siteGuide fills the site's AGENTS.md in for this run. The field lists come
// from the types, so they say what the files hold.
func siteGuide(run *frames.Run) ([]byte, error) {
	refs, err := references()
	if err != nil {
		return nil, err
	}
	facts := frames.ComputeFacts(run)
	severe := 0
	for _, i := range run.Issues {
		if i.Severity == "error" || i.Severity == "severe" {
			severe++
		}
	}
	d := map[string]any{
		"Name": run.Campaign.Name, "ID": run.Campaign.ID, "Version": buildVersion(),
		"Outcome": run.Campaign.Outcome, "Issues": len(run.Issues), "Severe": severe,
		"Elapsed": facts.Elapsed.Duration.HMS, "Events": len(run.Events), "Dispatches": len(facts.Dispatches),
		"Nodes": run.Nodes, "Sessions": 0, "Tracer": "no tracer",
		"Run": refs["Run"], "Facts": refs["Facts"], "StepRef": refs["StepRef"], "Dur": refs["Dur"],
	}
	if run.Traces != nil {
		d["Sessions"], d["Tracer"] = len(run.Traces.Sessions), run.Traces.Tool
	}
	var b bytes.Buffer
	if err := guideTmpl.Execute(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// assemble splices the run payload into the shell as a JSON script block
// before </body>. `<` is escaped so content containing "</script>" cannot
// break out of the block (tracer's writeBlock rule).
func assemble(run *frames.Run) ([]byte, error) {
	data, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	safe := strings.ReplaceAll(string(data), "<", `\u003c`)
	block := `<script type="application/json" id="run-data">` + safe + `</script>`
	// The block replaces a marker at the top of <body>: the app script at the
	// bottom of the page must find the data already parsed into the DOM.
	const marker = "<!--RUN-DATA-->"
	if !strings.Contains(shell, marker) {
		return nil, fmt.Errorf("shell has no %s marker", marker)
	}
	return []byte(strings.Replace(shell, marker, block, 1)), nil
}
