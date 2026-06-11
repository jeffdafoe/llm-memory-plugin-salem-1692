package cascade

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// register_production.go — composition helper that wires every production
// cascade subscriber + sweeper for a salem-engine instance. Pulled out
// of main.go so cutover assembly is one call; tests that need a partial
// composition (a single subsystem under examination, or a subset
// without LLM-dependent cascades) keep calling the per-subsystem
// Register* helpers directly.
//
// This is the "all of production" choice — distinct from the handlers
// package's deliberate refusal of a canonical RegisterAllProductionTools
// (see handlers/register_speak.go). Tools have many sensible production
// compositions (PR-by-PR enable/disable, dev-vs-prod surface trimming).
// Cascades do not: in production they all run, full stop. The risk a
// handlers-style "no canonical helper" would protect against — silently
// activating a subsystem the cutover isn't ready for — doesn't apply
// here because cascade Register* helpers are wired into the engine's
// runtime contract from the moment they ship; adding a new cascade
// is itself the activation decision.
//
// Wiring contract: call AFTER LoadWorld (subscribers can run on the
// initial republish; sweep goroutines spawned here capture w.LifecycleContext
// so they unblock on Run's ctx cancel), and BEFORE World.Run starts
// processing commands. The cascade Register* helpers all advertise
// "before Run starts, or from inside a Command.Fn" as the safe entry
// condition; calling this from main.go's startup sequence between
// LoadWorld and `go w.Run(ctx)` is the canonical fit.

// RegisterProductionCascades wires the full production cascade set onto
// w. Composition order is not load-bearing — subscribers register
// independently into w.subscribers, and each cascade is isolated by
// event-type dispatch — but the order below tracks the conceptual
// dependency graph (substrates first, then cascades that consume them)
// to keep the file readable.
//
// ctx is the engine lifecycle context — the same one main.go will
// later pass to World.Run. Sweep goroutines launched inside each
// Register* helper key off w.LifecycleContext (which Run stamps on
// entry), so the ctx passed here is used only for the initial readback
// of settings via SendContext; the in-progress shutdown signal flows
// through w.LifecycleContext after Run starts.
//
// client is the production LLM adapter (engine/sim/llm/memapi.Client).
// The LLM-bearing cascades (atmosphere, consolidation, narrative
// consolidation, noticeboard) call client.Chat with their own scene-
// scoped Requests; the non-LLM cascades ignore it.
//
// Panics on nil w (via each Register*'s wiring guard). Nil client is
// rejected here rather than later: a non-nil client is required by
// production; tests that need a partial composition without LLM
// cascades should call the non-LLM Register* helpers directly instead.
func RegisterProductionCascades(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterProductionCascades requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterProductionCascades requires a non-nil LLM client " +
			"(tests wanting a partial composition should call the per-subsystem " +
			"Register* helpers directly)")
	}

	// Engine-internal substrates and observers — no LLM dependency.
	// These cover the audit trail, encounter detection, and the
	// per-actor reactive primitives.
	RegisterActionLog(ctx, w)
	RegisterEncounter(w)
	RegisterObjectRefreshArrival(w)
	RegisterIdleBackstop(ctx, w)
	RegisterRedNeedBackstop(ctx, w)
	RegisterPriceBook(w)
	RegisterNPCRoutes(ctx, w)

	// Single-subscriber engine cascades that drive engine-authored
	// speech / state, no LLM call but a richer-than-substrate role.
	RegisterBusinessowner(ctx, w)
	// After RegisterBusinessowner in reading order because its whole
	// purpose is feeding that greet path (ZBBS-HOME-425); registration
	// order itself is not load-bearing.
	RegisterBusinessArrival(w)
	RegisterVisitor(ctx, w)

	// LLM-bearing cascades — atmosphere prose, per-pair consolidation,
	// per-actor narrative consolidation, and noticeboard authoring.
	// Each fires LLM calls through the supplied client. Order among
	// them is irrelevant; alphabetical for predictable reading.
	RegisterAtmosphere(ctx, w, client)
	RegisterConsolidation(ctx, w, client)
	RegisterNarrativeConsolidation(ctx, w, client)
	RegisterNoticeboard(ctx, w, client)
}
