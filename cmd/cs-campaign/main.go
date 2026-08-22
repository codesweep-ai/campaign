package main

import (
	"fmt"
	"os"

	"github.com/codesweep-ai/campaign/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "cs-campaign:", err)
		os.Exit(1)
	}
}
