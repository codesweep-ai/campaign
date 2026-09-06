package cli

// The one host condition the replay tier cannot work around, checked before it
// costs anything.

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// errRecorderPortBusy marks a host that cannot carry the tier, as distinct from
// a tier that failed.
var errRecorderPortBusy = errors.New("the recorder's port is in use")

// requirePortFree refuses the tier when something else already holds the
// recorder's port.
//
// The port cannot move. It travels in the profile, the campaign ID is the
// sha256 of that profile, and every name in every committed cassette derives
// from it — so a port drawn from the ephemeral range would replay nothing. What
// can move is how a collision is reported.
//
// It has to be checked by BINDING rather than by dialling. The readiness check
// that follows startVCR dials the port and accepts whatever answers, so a
// foreign listener reads there as a healthy recorder: the members then send
// their model calls to a stranger, get nothing they can use, and sit idle until
// the readback gives up fifteen minutes later. The bind error that says what
// really happened reaches only a proxy log nobody opens. Measured on a host
// running an unrelated development server on the same port: five scenarios, one
// bind failure, and a diagnosis that named the members.
func requirePortFree(addr, port string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %s is already held, so the recorder cannot serve this tier.\n"+
			"Find what holds it with: ss -tlnp 'sport = :%s'", errRecorderPortBusy, addr, port)
	}
	return ln.Close()
}

// A foreign listener must be refused. This is the case that cost a run: the
// dial-based readiness check accepts anything answering on the port, so without
// a bind check the tier proceeds against a stranger.
func TestRecorderPortRefusesAForeignListener(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	addr := held.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	err = requirePortFree(addr, port)
	if err == nil {
		t.Fatal("a port held by another process must be refused before the tier boots anything")
	}
	if !errors.Is(err, errRecorderPortBusy) {
		t.Errorf("the refusal must be recognisable as a busy port, got %v", err)
	}
	// The message has to name the port and how to find the holder: the failure
	// it replaces named the members instead, which is what made it expensive.
	for _, want := range []string{port, "ss -tlnp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not carry %q: %v", want, err)
		}
	}

	// And the dial the readiness check makes would have SUCCEEDED here, which
	// is precisely why dialling cannot be the test.
	conn, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
	if dialErr != nil {
		t.Fatalf("expected the foreign listener to accept a dial: %v", dialErr)
	}
	_ = conn.Close()
}

// A free port must pass, and must be left free for cs-vcr to take.
func TestRecorderPortAcceptsAFreePort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	if err := requirePortFree(addr, port); err != nil {
		t.Fatalf("a free port must be accepted: %v", err)
	}
	// Released rather than held: cs-vcr binds it a moment later.
	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the check kept the port instead of releasing it: %v", err)
	}
	_ = again.Close()
}
