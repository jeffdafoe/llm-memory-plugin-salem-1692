// Package sim implements the in-memory salem-engine world: World struct,
// command-channel concurrency model, atomic.Pointer[Snapshot] publishing,
// per-aggregate Repository facade.
//
// A single goroutine (started by World.Run) owns all mutable state. All
// mutations go through Commands sent on the cmds channel. Readers consume
// the most recent Snapshot via World.Published — atomic load, zero
// coordination. See state-model-sketch.md and overview.md under
// shared/tasks/engine-in-memory-rewrite/ for the durable design record.
//
// This package is an island in v1 — no production engine code imports it.
// At cutover, the legacy flat engine/*.go files are deleted and main.go is
// rewired to construct a *sim.World and dispatch HTTP handlers as Commands.
package sim
