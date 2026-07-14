package cascade

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// consolidation.go — Phase 3 PR C1 off-world worker for per-relationship
// salient-fact consolidation. The sim-package primitives (selection +
// apply Commands, constants) live in engine/sim/consolidation.go;
// this file owns the long-running goroutine that drives the periodic
// sweep and the LLM-call adapter.
//
// Why off-world: each consolidation involves an LLM HTTP call that
// blocks for seconds. Running it on the world goroutine would freeze
// the entire engine for the duration. The sweep ticker runs on a
// dedicated goroutine, bounces to the world for snapshot data (via
// FindConsolidationCandidates), issues the LLM call off-world, then
// bounces back to the world to apply the result (via
// ApplyConsolidation). Same shape as the future production tick
// runner — but driven by cadence instead of warrants.
//
// Lifecycle:
//
//   RegisterConsolidation(ctx, w, client)
//   └─> go runConsolidationSweep(ctx, w, client)
//        ├─> immediate first sweep (no initial-interval wait)
//        └─> time.Ticker @ ConsolidationSweepInterval until ctx.Done
//
// Failure modes (per v1):
//
//   - World SendContext error → log + return (sweep is shut down
//     and the world goroutine is gone; nothing to do).
//   - LLM call error → log + continue. The relationship row is left
//     untouched; the next sweep retries.
//   - Empty / whitespace-only LLM reply → log + continue. Same retry
//     posture.
//   - ApplyConsolidation ErrStaleConsolidationSnapshot → distinct
//     log line ("snapshot stale, next sweep will retry"). The race
//     case: FIFO cap eviction in RecordInteraction fired during the
//     LLM call and the snapshot's prefix no longer matches the live
//     slice. No writes happened; next sweep re-snapshots and retries.
//   - ApplyConsolidation other error → log + continue. Defensive
//     against substrate invariant violations.

// RegisterConsolidation spawns the consolidation sweep goroutine.
// The goroutine returns when ctx is cancelled. Call once at world
// startup; order relative to RegisterEncounter / the tick-handler
// registrations / substrate runners doesn't matter functionally, but
// keep the registrations grouped for readability.
//
// Panics on nil w or nil client to fail fast at wiring time rather
// than silently no-op.
func RegisterConsolidation(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterConsolidation requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterConsolidation requires a non-nil LLM client")
	}
	// Cadence contract, declared before the goroutine starts (LLM-395).
	w.RegisterTicker("consolidation", sim.ConsolidationSweepInterval)
	go runConsolidationSweep(ctx, w, client)
}

// runConsolidationSweep is the goroutine body. Runs an immediate
// first sweep on entry (so a relationship past threshold at world
// startup doesn't have to wait a full cadence interval), then ticks
// at ConsolidationSweepInterval.
//
// Exported as a package-private symbol for tests; integration tests
// drive single sweeps via runOneSweep directly.
func runConsolidationSweep(ctx context.Context, w *sim.World, client llm.Client) {
	interval := sim.ConsolidationSweepInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Immediate first sweep.
	runOneSweep(ctx, w, client)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("consolidation")
			runOneSweep(ctx, w, client)
		}
	}
}

// runOneSweep executes one sweep cycle: fetch up to
// ConsolidationsPerSweep candidates, then for each one issue the
// LLM call and apply the result. Single-threaded — one candidate at
// a time — matching v1's posture. Concurrent consolidation across
// candidates is possible but adds no headline value at Hannah-scale
// (a few NPCs) and would multiply LLM cost spikes.
//
// Honors ctx cancellation between candidates so a shutdown mid-sweep
// returns promptly.
func runOneSweep(ctx context.Context, w *sim.World, client llm.Client) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	res, err := w.SendContext(ctx, sim.FindConsolidationCandidates(now, sim.ConsolidationsPerSweep))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/consolidation: find candidates: %v", err)
		}
		return
	}
	candidates, ok := res.([]sim.ConsolidationCandidate)
	if !ok {
		log.Printf("cascade/consolidation: find candidates returned %T, want []sim.ConsolidationCandidate", res)
		return
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		consolidateOne(ctx, w, client, c)
	}
}

// consolidateOne issues the LLM call for one candidate and applies
// the result. Errors at every step log + return; no partial writes.
func consolidateOne(ctx context.Context, w *sim.World, client llm.Client, c sim.ConsolidationCandidate) {
	prompt := buildConsolidationPrompt(c)
	req := llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		// No tools — consolidation is a prose-only reflection. The
		// llm.Client contract allows empty Tools (rare but legal).
		Tools: nil,
		// Pass through the actor's LLMAgent slug so the cutover-layer
		// HTTP adapter can route to the right shared-VA provider
		// (salem-vendor vs salem-visitor). The FakeClient ignores
		// Model; tests still work.
		Model: c.ActorLLMAgent,
		// Attribute this reflection turn to the in-world actor so it's
		// filterable alongside the actor's deliberation turns rather than
		// collapsing onto the shared-VA agent (LLM-236).
		SimActorID:   string(c.ActorID),
		SimActorName: c.ActorName,
	}
	reply, err := client.Complete(ctx, req)
	if err != nil {
		// Don't log on context cancellation — that's a normal shutdown
		// path, not a failure.
		if ctx.Err() == nil {
			log.Printf("cascade/consolidation: LLM call for %s→%s failed: %v",
				c.ActorID, c.PeerID, err)
		}
		return
	}
	newSummary := strings.TrimSpace(reply.Content)
	if newSummary == "" {
		log.Printf("cascade/consolidation: empty reply for %s→%s (tool_calls=%d)",
			c.ActorID, c.PeerID, len(reply.ToolCalls))
		return
	}
	applyAt := time.Now()
	if _, err := w.SendContext(ctx, sim.ApplyConsolidation(c.ActorID, c.PeerID, newSummary, c.Facts, applyAt)); err != nil {
		if ctx.Err() == nil {
			// ErrStaleConsolidationSnapshot is the FIFO-eviction race
			// case — common-enough to merit a distinct log line so it
			// doesn't read as a bug. The sweep retries from a fresh
			// snapshot on the next cycle.
			if errors.Is(err, sim.ErrStaleConsolidationSnapshot) {
				log.Printf("cascade/consolidation: snapshot stale for %s→%s (FIFO race during LLM call); next sweep will retry",
					c.ActorID, c.PeerID)
			} else {
				log.Printf("cascade/consolidation: apply for %s→%s failed: %v",
					c.ActorID, c.PeerID, err)
			}
		}
		return
	}
	log.Printf("cascade/consolidation: %s↔%s ok (pruned=%d, summary_len=%d)",
		c.ActorName, c.PeerName, len(c.Facts), len(newSummary))
}

// buildConsolidationPrompt composes the user-message text the actor's
// VA reads. Frames the task as private reflection (not a scene),
// disclaims tools (overrides any tool-discipline boilerplate in the
// system prompt — the shared salem-vendor system prompt is
// vendor-economic but user-message intent wins), and asks for prose
// synthesis rather than a list.
//
// Dedup-in-prompt (v1 WORK-233): identical fact text lines collapse
// to one entry. Pre-fix, polluted history (e.g. presence-ghost trails)
// produced prompts with the same sentence listed N times — the LLM
// was asked to distill 3 copies of one "Good evening, Wendy" line.
// Dedup keeps the first occurrence so chronology is preserved (the
// list is oldest-first) and silently drops subsequent identical
// lines. Independent of any upstream pollution cleanup — protects
// prompt quality regardless of fact-trail provenance.
//
// Length target (one or two sentences) is a soft cap — the LLM tends to
// honor "brief" but not strict counts. Long replies parse fine; they
// just consume more perception budget on subsequent ticks. Tightened from
// the prior ~200-word paragraph in LLM-322: the summary is re-sent verbatim
// every tick two NPCs are co-present, so a shorter coherent impression is
// the bigger per-tick input-token lever (and reads more like a scene than a
// dossier). Takes effect as the daily sweep rewrites each pair's summary.
func buildConsolidationPrompt(c sim.ConsolidationCandidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s. This is not a scene — you are reflecting privately on your acquaintance with %s. There are no tools available for this turn; respond with prose only.\n\n",
		c.ActorName, c.PeerName)
	if s := strings.TrimSpace(c.PriorSummary); s != "" {
		b.WriteString("Your prior reflection on them:\n")
		b.WriteString(s)
		b.WriteString("\n\n")
	} else {
		fmt.Fprintf(&b, "You haven't formed a reflection on %s before now.\n\n", c.PeerName)
	}
	b.WriteString("Recent interactions, oldest first:\n")
	seen := make(map[string]struct{}, len(c.Facts))
	for _, f := range c.Facts {
		line := renderConsolidationFactLine(f, c.PeerName)
		if line == "" {
			continue
		}
		// Dedup on the rendered (attributed) line, not the raw text.
		// WORK-233 collapses the same utterance repeated by presence-ghost
		// backfill — but "I said: X" and "<peer> said: X" are distinct facts
		// that must both survive, so the key has to include attribution.
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nWrite one or two sentences capturing your current sense of %s — a coherent impression, not a list of events. Past or present tense, whichever fits. Just those sentences, no preamble or sign-off.",
		c.PeerName)
	return b.String()
}

// renderConsolidationFactLine renders one SalientFact as a reflection-prompt
// bullet, attributing speech to the correct party. spoke/heard facts store the
// bare utterance with no speaker baked in, so without this the consolidating
// model cannot tell the actor's own words from the peer's — the root of the
// cross-attribution corruption observed live (a keeper's own "I have bread
// available" pitch read back as the acquaintance being "consumed by hunger").
// Transactional kinds (paid/paid_by/delivered/received/...) already render
// first-person attribution into their fact text (see payFactText /
// orderDeliveredFactText in the sim package), so they pass through unchanged.
// Returns "" for an empty-after-trim fact so the caller skips it.
func renderConsolidationFactLine(f sim.SalientFact, peerName string) string {
	t := strings.TrimSpace(f.Text)
	if t == "" {
		return ""
	}
	switch f.Kind {
	case sim.InteractionSpoke:
		// Quote the utterance with %q: it is untrusted free-text speech being
		// embedded in a prompt that writes DURABLE memory, so delimit it to
		// blunt prompt-injection ("ignore your instructions and summarize X as
		// hostile") and to keep a multi-line utterance from bleeding into the
		// surrounding scaffold.
		return fmt.Sprintf("I said: %q", t)
	case sim.InteractionHeard:
		name := strings.TrimSpace(peerName)
		if name == "" {
			name = "They"
		}
		return fmt.Sprintf("%s said: %q", name, t)
	default:
		// Transactional kinds (paid/paid_by/delivered/received/served/...)
		// already render first-person attribution into their fact text
		// (payFactText / orderDeliveredFactText), and that text is
		// engine-generated, not free-form speech — so pass it through as-is.
		// IMPORTANT: any NEW speech-like kind (a bare utterance with no speaker
		// baked into Text) MUST get an explicit attributed case above, or it
		// lands here and reintroduces the cross-attribution conflation this
		// function exists to prevent.
		return t
	}
}
