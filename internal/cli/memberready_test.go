package cli

// The one retry in the provisioning path. cs-sandbox create returns before the
// guest accepts connections, and every step after it execs into that guest, so
// a member one second from ready used to fail a create whose machines were all
// provisioned (SAC-007).

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// refusingSandbox is a cs-sandbox stand-in whose `exec` fails the first n times
// and succeeds afterwards, which is how a guest behaves while sshd is coming
// up. It counts attempts in a file so the count survives each process.
//
// Exit 255 is what ssh returns for its own failures, and it is what the real
// failure looked like: `Connection closed by 127.0.0.1 port <gateway>`.
func refusingSandbox(counter string, n int) string {
	return fmt.Sprintf(`
case "$1" in
  exec)
    c=0; [ -f %[1]s ] && c=$(cat %[1]s)
    c=$((c+1)); echo $c > %[1]s
    if [ $c -le %[2]d ]; then
      echo "Connection closed by 127.0.0.1 port 2305" >&2
      exit 255
    fi
    exit 0;;
  *) exit 0;;
esac
`, counter, n)
}

// neverReadySandbox is a guest that never accepts, which is a machine that
// failed to boot rather than one that is slow.
const neverReadySandbox = `
case "$1" in
  exec) echo "Connection closed by 127.0.0.1 port 2305" >&2; exit 255;;
  *) exit 0;;
esac
`

func readySandbox(t *testing.T, body string) sandboxCLI {
	t.Helper()
	dir := installFakeTool(t, "fake-sandbox", body)
	return sandboxCLI{
		Bin:           filepath.Join(dir, "fake-sandbox"),
		ReadyBound:    5 * time.Second,
		ReadyInterval: 10 * time.Millisecond,
		ProbeBound:    2 * time.Second,
		WaitDelay:     100 * time.Millisecond,
	}
}

// A guest that refuses and then accepts must not fail the create. This is the
// whole defect: the old path took the first refusal as the answer.
func TestReadyWaitOutlastsABootingGuest(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "n")
	s := readySandbox(t, refusingSandbox(counter, 3))
	var errOut bytes.Buffer
	s.Err = &errOut

	if err := s.awaitMemberReady(context.Background(), "dev.group"); err != nil {
		t.Fatalf("a guest that accepted on the fourth attempt should be ready: %v", err)
	}
	// Said once, not once per probe: a create's own output must stay readable.
	if got := strings.Count(errOut.String(), "waiting for dev.group"); got != 1 {
		t.Errorf("expected exactly one waiting line, got %d:\n%s", got, errOut.String())
	}
}

// A guest that never arrives must fail, and say so with the last refusal. A
// wait without a bound would turn a dead machine into a hung create, which is
// the failure the operator can do least about.
func TestReadyWaitIsBounded(t *testing.T) {
	s := readySandbox(t, neverReadySandbox)
	s.ReadyBound = 300 * time.Millisecond

	start := time.Now()
	err := s.awaitMemberReady(context.Background(), "dev.group")
	if err == nil {
		t.Fatal("a guest that never accepts must fail the create")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("wait ran %s, far past its bound", elapsed)
	}
	for _, want := range []string{"dev.group", "accepted no command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q: %v", want, err)
		}
	}
}

// A cancelled create must stop waiting, rather than hold its bound out.
func TestReadyWaitHonoursCancellation(t *testing.T) {
	s := readySandbox(t, neverReadySandbox)
	s.ReadyBound = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	if err := s.awaitMemberReady(ctx, "dev.group"); err == nil {
		t.Fatal("expected cancellation to end the wait")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("cancellation took %s to take effect", elapsed)
	}
}
