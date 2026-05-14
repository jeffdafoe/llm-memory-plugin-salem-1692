// Package handlers is the off-world tick execution pipeline for the v2
// engine (Phase 2 PR 3). It turns a sim.ReactorTickDue event into an NPC
// turn without ever blocking the world goroutine.
//
// PR 3b — this slice — lands the pipeline shell:
//
//   - TickWorkerPool: a fixed pool of worker goroutines draining a bounded
//     job channel. It also implements sim.TickAdmissionController, so the
//     reactor evaluator can check capacity BEFORE consuming an actor's
//     warrants (Option A — admit before consume).
//   - The ReactorTickDue subscriber: builds a tickJob per event and
//     enqueues it (non-blocking; a full channel is an admission-invariant
//     breach and panics).
//   - The worker body: per-tick lifecycle telemetry plus the call back into
//     sim.CompleteReactorTick.
//   - RegisterTickHandlers: the one bootstrap entry point that wires the
//     admission controller and the subscriber together as a unit.
//
// The actual perception build, prompt rendering, LLM call, and tool
// dispatch are PR 3c/3d — reached through the tickRunner seam, which PR 3b
// fills with a stub so the lifecycle is exercisable end-to-end now.
//
// The package depends on sim; sim never depends on it. RegisterTickHandlers
// is the only coupling, and it is called at bootstrap.
package handlers
