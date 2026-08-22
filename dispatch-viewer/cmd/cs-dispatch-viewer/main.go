package main

import (
	"os"

	"github.com/codesweep-ai/campaign/dispatch-viewer/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr)) }
