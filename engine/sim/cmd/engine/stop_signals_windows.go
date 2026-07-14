//go:build windows

package main

import "os"

// gracefulStopSignals is empty on Windows: SIGWINCH does not exist there, so the
// signal-driven graceful stop is a Unix-only trigger. The engine only runs under
// systemd in production; this file exists so `go build` / `go test` still work on
// a developer's Windows box, where SIGINT/SIGTERM force-stop as they always have.
//
// The stop gate itself (run's graceful path) is portable and is exercised by the
// tests on every platform — they hand run() a stopRequest directly, no signals.
func gracefulStopSignals() []os.Signal { return nil }
