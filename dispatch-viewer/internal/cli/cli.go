// Package cli is the cs-dispatch-viewer command: parse args, load one run
// archive through frames, and write a self-contained viewer.html. The arg
// parser is hand-rolled with injected writers, after tracer's cli.go.
package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

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
  cs-dispatch-viewer <run-dir> [-o out.html | -o site-dir/] [--tracer <bin>] [--no-traces]
  cs-dispatch-viewer manual | version | help

<run-dir> is a campaign archive directory (holding campaign.json) or a run
directory holding archive/. Output defaults to viewer.html in the current
directory.

When cs-tracer is on PATH, or named by --tracer, every member's transcript is
normalized and drawn inside its dispatches. An -o that names a directory, or
ends in a slash, writes a site instead of one file: index.html beside a
tracer/ directory holding the tracer's export, and every step on the page
links to its event there. --no-traces skips the tracer altogether.
`

type options struct {
	dir      string
	out      string
	tracer   string
	noTraces bool
}

var errHelp = errors.New("help")
var errVersion = errors.New("version")
var errManual = errors.New("manual")

func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "help", a == "--help", a == "-h":
			return o, errHelp
		case a == "version", a == "--version":
			return o, errVersion
		case a == "manual":
			return o, errManual
		case a == "-o", a == "--out":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a path", a)
			}
			i++
			o.out = args[i]
		case a == "--tracer":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a path", a)
			}
			i++
			o.tracer = args[i]
		case a == "--no-traces":
			o.noTraces = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		case o.dir != "":
			return o, fmt.Errorf("one run directory only (got %q and %q)", o.dir, a)
		default:
			o.dir = a
		}
	}
	if o.dir == "" {
		return o, errors.New("a run directory is required")
	}
	if o.out == "" {
		o.out = "viewer.html"
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
	run, err := frames.Load(o.dir)
	if err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
		return 1
	}
	// A site is a directory: an existing one, or a path given with a
	// trailing slash. The page is its index.html and the traces sit beside it.
	site := ""
	out := o.out
	if info, err := os.Stat(o.out); strings.HasSuffix(o.out, "/") || (err == nil && info.IsDir()) {
		site = filepath.Clean(o.out)
		out = filepath.Join(site, "index.html")
		if err := os.MkdirAll(site, 0o755); err != nil {
			fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
			return 1
		}
	}
	if !o.noTraces {
		err := frames.AttachTraces(context.Background(), run, o.dir, frames.TraceOptions{Tracer: o.tracer, Site: site, Stderr: stderr})
		if err != nil {
			fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
			return 1
		}
	}
	page, err := assemble(run)
	if err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
		return 1
	}
	if err := os.WriteFile(out, page, 0o644); err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
		return 1
	}
	traced := ""
	if run.Traces != nil {
		traced = fmt.Sprintf(", %d sessions, %d anchors", len(run.Traces.Sessions), len(run.Traces.Anchors))
	}
	fmt.Fprintf(stdout, "%s (%d bytes, %d events, %d issues%s)\n", out, len(page), len(run.Events), len(run.Issues), traced)
	return 0
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
