// Package cli is the cs-dispatch-viewer command: parse args, load one run
// archive through frames, and write a self-contained viewer.html. The arg
// parser is hand-rolled with injected writers, after tracer's cli.go.
package cli

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
  cs-dispatch-viewer <run-dir> [-o out.html]
  cs-dispatch-viewer manual | version | help

<run-dir> is a campaign archive directory (holding campaign.json) or a run
directory holding archive/. Output defaults to viewer.html beside the input.
`

type options struct {
	dir string
	out string
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
	page, err := assemble(run)
	if err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
		return 1
	}
	if err := os.WriteFile(o.out, page, 0o644); err != nil {
		fmt.Fprintf(stderr, "cs-dispatch-viewer: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s (%d bytes, %d events, %d issues)\n", o.out, len(page), len(run.Events), len(run.Issues))
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
