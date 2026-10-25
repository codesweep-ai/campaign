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
// --state or --context is a question and not a turn, and it matches the same
// pattern. Seen
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
	fake("cs-" + cli + "-turn --context")
	time.Sleep(300 * time.Millisecond)

	cmd := exec.Command("sh", "-c", ProbeScript(cli))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if f := ParseProbe(string(out)); f.Drivers != 1 {
		t.Fatalf("the probe counted %d drivers with one turn, one --state query and one --context query running; want 1:\n%s", f.Drivers, out)
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

// The probe asks the family's driver how full the agent's context is, the way it
// asks whether the agent is in a turn. A driver from before --context refuses the
// flag with nothing on stdout, and that must read as not known rather than as a
// reading or a failed look.
func TestTheProbeReadsTheDriversContextAnswer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probe runs inside a Linux member")
	}
	for _, tc := range []struct {
		name, driver string
		want         *ContextReading
	}{
		{"a driver that knows --context", `case "$1" in
  --state) echo busy ;;
  --context) echo "tokens=62221 window=- compactions=2 compacted=1792267563 model=claude-opus-5" ;;
esac`, &ContextReading{Tokens: 62221, Compactions: 2, Compacted: 1792267563, Model: "claude-opus-5"}},
		{"a driver from before --context", `case "$1" in
  --state) echo busy ;;
  *) echo "cs-zzctx-turn: unknown argument: $1" >&2; exit 1 ;;
esac`, nil},
		{"a driver with no record yet", `case "$1" in
  --state) echo absent ;;
  --context) echo absent ;;
esac`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			for _, d := range []string{filepath.Join(home, ChannelsDir), bin} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "cs-zzctx-turn"), []byte("#!/bin/sh\n"+tc.driver+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", ProbeScript("zzctx"))
			cmd.Env = append(os.Environ(), "HOME="+home)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			f := ParseProbe(string(out))
			if f.Agent == "" {
				t.Fatalf("the stand-in driver was not asked for --state:\n%s", out)
			}
			switch {
			case tc.want == nil && f.Context != nil:
				t.Errorf("Context = %+v, want not known:\n%s", *f.Context, out)
			case tc.want != nil && (f.Context == nil || *f.Context != *tc.want):
				t.Errorf("Context = %+v, want %+v:\n%s", f.Context, *tc.want, out)
			}
		})
	}
}
