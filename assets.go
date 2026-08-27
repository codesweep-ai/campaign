// Package campaign exposes assets embedded at build time.
//
// It exists so files at the repository root can be embedded: //go:embed cannot
// reference a parent directory, and the manual belongs at the root where a reader
// finds it rather than buried in internal/.
package campaign

import _ "embed"

// ManualMD is the user-facing manual, embedded so `cs-campaign manual` carries it
// inside the binary. Someone who has only the executable still has the
// documentation — which matters here, because this CLI is routinely installed to
// ~/.local/bin far from its checkout, and is also shipped into guest VMs.
//
// Embedded as a FILE rather than a Go string literal on purpose: the same bytes
// are reviewable as markdown in the repository and shipped in the binary, so the
// two cannot drift and no test is needed to check that they agree. A unit test
// asserts the weaker, useful thing instead — that the manual names every command
// the CLI actually registers.
//
//go:embed MANUAL.md
var ManualMD string

// GoMod is this binary's own module manifest, embedded so a built cs-campaign
// carries the versions it was built against.
//
// It is the reference every upstream check compares to, and it has to travel
// inside the executable because that is the only place it survives. `make
// install` copies the binary to ~/.local/bin and a release ships a tarball, so
// by the time `doctor` runs there is usually no checkout on the host at all,
// and any go.mod found on disk would belong to some other tree at some other
// revision.
//
// Embedded rather than stamped in with -ldflags for the same reason ManualMD is
// a file: the bytes reviewed in the repository and the bytes in the binary are
// the same bytes, so the two cannot drift and no generator sits between them.
//
//go:embed go.mod
var GoMod string
