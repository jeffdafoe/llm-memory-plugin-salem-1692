//go:build !windows

package main

import (
	"os"
	"syscall"
)

// gracefulStopSignals returns the signals that request an ABORTABLE stop —
// checkpoint first, and refuse to exit if the world cannot be durably saved
// (LLM-404). Everything else the engine traps (SIGINT/SIGTERM) is a force stop.
//
// SIGWINCH, following Apache httpd, where it means exactly this: "graceful-stop"
// — finish what you are doing, then exit. Two properties earn it the job over
// the more obvious SIGUSR1:
//
//   - Its default disposition is IGNORE. An engine binary that predates this
//     handler ignores the signal and keeps running, so a deploy that sends it to
//     an old engine is a harmless no-op. SIGUSR1's default is TERMINATE, which
//     would have killed the old engine outright with no checkpoint at all —
//     precisely the loss this ticket exists to prevent.
//   - It carries no systemd meaning, so the deploy delivers it out-of-band
//     (`systemctl kill -s WINCH`) and the unit never enters its stopping state.
//     That matters: systemd ALWAYS escalates a stop to SIGKILL once
//     TimeoutStopSec expires, and a SIGKILL landing on an engine that is
//     deliberately refusing to exit would destroy the very world the refusal
//     exists to protect. No signal choice can fix that — `systemctl stop` is
//     simply the wrong verb for an abortable stop.
func gracefulStopSignals() []os.Signal {
	return []os.Signal{syscall.SIGWINCH}
}
