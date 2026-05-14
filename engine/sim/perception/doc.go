// Package perception builds an NPC's tick-time view of the world and
// renders it into a prompt.
//
// It is the perception half of the agent-tick execution pipeline (Phase 2
// PR 3c). The pipeline shell — worker pool, admission, the ReactorTickDue
// subscriber, the tickRunner seam — lives in engine/sim/handlers (PR 3b);
// the harness loop, LLM call, and tool dispatch land in PR 3d. PR 3c is
// the pure, off-world middle: turn a published *sim.Snapshot plus the
// actor's consumed warrant batch into a Payload, then render that Payload
// into a prompt string.
//
// # Purity
//
// Build and Render are pure functions. They read only the exported,
// immutable *sim.Snapshot — never *sim.World, never the world goroutine,
// no locks, no mutation. The Snapshot is the deep-cloned lock-free read
// view (sim.World.Published); perception can be run from any worker
// goroutine without coordination. This is why the package imports sim but
// sim never imports perception, and why the package has no init-time
// state.
//
// # Why a separate package
//
// handlers.tickJob is unexported, so Build takes the inputs it needs
// (actor ID + warrant batch) as plain arguments rather than the job
// struct. That keeps perception free of any handlers dependency: in PR 3d
// the harness loop (in handlers) imports perception, not the reverse.
//
// # Staleness
//
// A Snapshot can be stale relative to the live world by the time a worker
// perceives from it — that is correct for perception. The baseline diff
// is computed against the *frozen* Scene.ParticipantStateAtOrigin, and the
// whole point of a per-tick snapshot is a consistent read. Staleness only
// matters for *mutation*: tool execution re-validates against live world
// state via sim.RunTickToolCommand (PR 3b), never against this Snapshot.
package perception
