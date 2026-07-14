package cascade

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// returner_consolidation.go — off-world worker that folds a returner's per-PC
// episodic memory at visit-end (LLM-383). Structurally identical to
// consolidation.go's sweep, but drives the returner primitives
// (FindReturnerConsolidationCandidates / ApplyReturnerConsolidation) instead of
// the actor-relationship ones. Reuses buildConsolidationPrompt VERBATIM, so the
// returner fold inherits the same speaker attribution + dedup + scene register the
// persistent fold has (and, critically, the same cross-attribution guard). See
// engine/sim/returner_consolidation.go for the cadence rationale.
//
// Failure modes mirror consolidation.go: world SendContext error → log + return;
// LLM error / empty reply → log + continue (row untouched, next sweep retries);
// ErrStaleConsolidationSnapshot → distinct log line (capture raced the LLM call in
// a mid-visit ceiling fold), no writes, retried next sweep.

// RegisterReturnerConsolidation spawns the returner-consolidation sweep goroutine.
// The goroutine returns when ctx is cancelled. Call once at world startup. Panics
// on nil w or nil client to fail fast at wiring time.
func RegisterReturnerConsolidation(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterReturnerConsolidation requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterReturnerConsolidation requires a non-nil LLM client")
	}
	// Cadence contract, declared before the goroutine starts (LLM-395). Shares the
	// consolidation sweep's interval, as the goroutine's own ticker does.
	w.RegisterTicker("returner_consolidation", sim.ConsolidationSweepInterval)
	go runReturnerConsolidationSweep(ctx, w, client)
}

// runReturnerConsolidationSweep runs an immediate first sweep (so a returner that
// departed before a restart is folded at boot rather than waiting a full cadence),
// then ticks at ConsolidationSweepInterval — the same cadence as the persistent
// sweep; a returner's fold latency past departure is bounded by one interval,
// which is immaterial given the weeks-long dormancy before its next visit.
func runReturnerConsolidationSweep(ctx context.Context, w *sim.World, client llm.Client) {
	ticker := time.NewTicker(sim.ConsolidationSweepInterval)
	defer ticker.Stop()

	runOneReturnerSweep(ctx, w, client)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("returner_consolidation")
			runOneReturnerSweep(ctx, w, client)
		}
	}
}

// runOneReturnerSweep fetches up to ConsolidationsPerSweep returner candidates and
// folds each in turn. Honors ctx cancellation between candidates.
func runOneReturnerSweep(ctx context.Context, w *sim.World, client llm.Client) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	res, err := w.SendContext(ctx, sim.FindReturnerConsolidationCandidates(now, sim.ConsolidationsPerSweep))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/returner_consolidation: find candidates: %v", err)
		}
		return
	}
	candidates, ok := res.([]sim.ConsolidationCandidate)
	if !ok {
		log.Printf("cascade/returner_consolidation: find candidates returned %T, want []sim.ConsolidationCandidate", res)
		return
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		consolidateOneReturner(ctx, w, client, c)
	}
}

// consolidateOneReturner issues the LLM fold for one returner acquaintance and
// applies it. Errors at every step log + return; no partial writes.
func consolidateOneReturner(ctx context.Context, w *sim.World, client llm.Client, c sim.ConsolidationCandidate) {
	prompt := buildConsolidationPrompt(c)
	req := llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		// Prose-only reflection, no tools (llm.Client contract allows empty Tools).
		Tools: nil,
		// The shared salem-visitor slug routes to the returner's VA. FakeClient
		// ignores Model; tests still work.
		Model: c.ActorLLMAgent,
		// Attribute the reflection turn to the returner identity (rvis- id) so it's
		// filterable alongside its deliberation turns rather than collapsing onto
		// the shared VA.
		SimActorID:   string(c.ActorID),
		SimActorName: c.ActorName,
	}
	reply, err := client.Complete(ctx, req)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/returner_consolidation: LLM call for %s→%s failed: %v", c.ActorID, c.PeerID, err)
		}
		return
	}
	newSummary := strings.TrimSpace(reply.Content)
	if newSummary == "" {
		log.Printf("cascade/returner_consolidation: empty reply for %s→%s (tool_calls=%d)", c.ActorID, c.PeerID, len(reply.ToolCalls))
		return
	}
	applyAt := time.Now()
	if _, err := w.SendContext(ctx, sim.ApplyReturnerConsolidation(c.ActorID, c.PeerID, newSummary, c.Facts, applyAt)); err != nil {
		if ctx.Err() == nil {
			if errors.Is(err, sim.ErrStaleConsolidationSnapshot) {
				log.Printf("cascade/returner_consolidation: snapshot stale for %s→%s (capture race during LLM call); next sweep will retry",
					c.ActorID, c.PeerID)
			} else {
				log.Printf("cascade/returner_consolidation: apply for %s→%s failed: %v", c.ActorID, c.PeerID, err)
			}
		}
		return
	}
	log.Printf("cascade/returner_consolidation: %s↔%s ok (pruned=%d, summary_len=%d)",
		c.ActorName, c.PeerName, len(c.Facts), len(newSummary))
}
