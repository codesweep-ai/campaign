package protocol

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The probe counts turn drivers from the process table. A driver asked for
// --state is a question and not a turn, and it matches the same pattern. Seen
// live: a loop that polled --state counted itself as a live driver for three
// minutes after the real one had exited. Two observers probe one node, so one
// probe's --state call can be running while the other counts.
func TestTheProbeCountsTurnsAndNotStateQueries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probe runs inside a Linux member")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep is not installed")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ChannelsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	// A family name nothing else on this machine uses, so a real driver
	// elsewhere cannot disturb the count.
	const cli = "zzprobe"
	fake := func(argv0 string) {
		cmd := exec.Command("bash", "-c", `exec -a "$0" sleep 30`, argv0)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	}
	fake("cs-" + cli + "-turn --tmux token --format text")
	fake("cs-" + cli + "-turn --state")
	time.Sleep(300 * time.Millisecond)

	cmd := exec.Command("sh", "-c", ProbeScript(cli))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if f := ParseProbe(string(out)); f.Drivers != 1 {
		t.Fatalf("the probe counted %d drivers with one turn and one --state query running; want 1:\n%s", f.Drivers, out)
	}
	if !strings.Contains(string(out), "DRIVERS 1") {
		t.Fatalf("no DRIVERS line:\n%s", out)
	}
}

// With nothing running the count is a plain 0, and never an empty field.
func TestTheProbeCountsNoDriversAsZero(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probe runs inside a Linux member")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ChannelsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", ProbeScript("zznone"))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "DRIVERS 0\n") {
		t.Fatalf("want a DRIVERS 0 line:\n%s", out)
	}
}
