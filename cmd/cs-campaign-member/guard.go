package main

// The family guard, as a face of the guest binary. The real
// cs-*-remote tools move to ~/.local/share/cs-campaign/real/ and their names
// become symlinks here. A campaign member may only be driven through its
// declared CLI family; the wrong family is refused with the true diagnosis,
// instead of the misleading "not logged in" that sent an orchestrator into a
// credential-copying spiral. Targets that are not campaign members pass
// through untouched.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

const (
	guardExitBroken  = 70
	guardExitRefused = 78
)

func runGuard(toolName string, args []string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cs-campaign guard: %v\n", err)
		return guardExitBroken
	}
	real := filepath.Join(home, protocol.ChannelsDir, "real", toolName)
	if info, err := os.Stat(real); err != nil || info.Mode()&0o111 == 0 {
		fmt.Fprintf(os.Stderr, "cs-campaign guard: real tool %s missing at %s (broken install — report this, do not work around it)\n", toolName, real)
		return guardExitBroken
	}
	family := strings.TrimPrefix(toolName, "cs-")
	if i := strings.Index(family, "-remote"); i >= 0 {
		family = family[:i]
	}

	// Best-effort manifest routing check; a missing manifest passes through,
	// matching the shell guard.
	if b, err := os.ReadFile(filepath.Join(home, protocol.ManifestDoc)); err == nil {
		var m protocol.Manifest
		if json.Unmarshal(b, &m) == nil {
			prev := ""
			for _, arg := range args {
				candidate := ""
				if prev == "-H" || prev == "--host" {
					candidate = arg
				} else if !strings.HasPrefix(arg, "-") {
					candidate = arg
				}
				prev = arg
				if candidate == "" {
					continue
				}
				for member, rec := range m.Agents {
					if rec.Sandbox == candidate || rec.Session == candidate {
						if rec.CLI != family {
							fmt.Fprintf(os.Stderr,
								"cs-campaign guard: REFUSED — %q is campaign member %q, whose declared CLI is %q; you invoked the %q family.\n"+
									"A member driven with the wrong family reports 'not logged in' because it was never provisioned for that CLI. "+
									"This is a routing mistake, not a provisioning gap: do NOT copy credentials or re-provision.\n"+
									"Use: cs-campaign-member <verb> %s    (roster: cs-campaign-member list)\n",
								candidate, member, rec.CLI, family, member)
							return guardExitRefused
						}
					}
				}
			}
		}
	}

	// Right family (or not a campaign member): exec the real tool in place.
	argv := append([]string{real}, args...)
	if err := syscall.Exec(real, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "cs-campaign guard: exec %s: %v\n", real, err)
		return guardExitBroken
	}
	return 0 // unreachable
}
