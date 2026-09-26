package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSandboxImagePicksTheImageThisHostHolds: scripts/sandbox-image.sh names
// the image the Makefile's tiers boot. That is the published name where this
// host holds that image, else the local build's name where it holds that one,
// else the published name, which `cs-sandbox build` then pulls or builds under
// the local name. A cs-sandbox from before the local names prints none, and
// gets the published name whatever is here.
func TestSandboxImagePicksTheImageThisHostHolds(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "sandbox-image.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const pub, loc = "ghcr.io/o/sandbox-slim:v1", "localhost/o/sandbox-slim:v1"
	images := "image              ghcr.io/o/sandbox:v1\nimage-slim         " + pub + "\n" +
		"image-local        localhost/o/sandbox:v1\nimage-slim-local   " + loc + "\n"
	for _, c := range []struct {
		name, images, want string
		held               []string
	}{
		{"the published image here", images, pub, []string{pub, loc}},
		{"only the local build here", images, loc, []string{loc}},
		{"neither here", images, pub, nil},
		{"a cs-sandbox with no local names", "image-slim         " + pub + "\n", pub, []string{loc}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			sandbox := filepath.Join(dir, "cs-sandbox")
			if err := os.WriteFile(sandbox, []byte("#!/bin/sh\nprintf '"+strings.ReplaceAll(c.images, "\n", `\n`)+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			var podman strings.Builder
			podman.WriteString("#!/bin/sh\nfor ref; do :; done\ncase \"$ref\" in\n")
			for _, h := range c.held {
				podman.WriteString("  " + h + ") exit 0;;\n")
			}
			podman.WriteString("esac\nexit 1\n")
			if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(podman.String()), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(script, "slim")
			cmd.Env = append(os.Environ(), "SANDBOX="+sandbox, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("sandbox-image.sh slim: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != c.want {
				t.Errorf("sandbox-image.sh slim = %q, want %q", got, c.want)
			}
		})
	}
}
