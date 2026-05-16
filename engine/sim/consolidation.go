package sim

import (
	"fmt"
	"sort"
	"time"
)

// consolidation.go — per-relationship salient-fact consolidation
// substrate (Phase 3 PR C1). The sweep worker that drives this lives
// off-world in engine/sim/handlers/consolidation.go because it issues
// LLM calls; this file is sim-package primitives only.
//
// Mechanism (mirrors v1 engine/actor_narrative_consolidate.go):
//
//   1. FindConsolidationCandidates runs on the world goroutine, scans
//      KindNPCShared actors' Relationships, returns up to `limit`
//      candidates with full snapshot data (prior summary + facts +
//      display names) so the off-world worker can build the prompt
//      without coming back to the world goroutine.
//
//   2. Worker (handlers/) calls the LLM with the prompt, receives a
//      new SummaryText.
//
//   3. ApplyConsolidation runs back on the world goroutine: replace
//      Relationship.SummaryText, prune the first SnapshotLen entries
//      from SalientFacts (anything appended during the LLM call
//      survives — race-safety via prune-by-snapshot-length, same
//      pattern as v1), stamp LastConsolidatedAt.
//
// Selection rules (3 OR branches):
//
//   - Ceiling: len(SalientFacts) >= ConsolidationCeiling. Forces a
//     mid-cycle pass when a chatty pair accumulates faster than the
//     daily floor.
//   - First-time: LastConsolidatedAt == nil AND len(SalientFacts) >=
//     ConsolidationFirstMinFacts. The minimum-facts gate prevents
//     fake-deep "coherent impressions" distilled from one "Good
//     morrow" line — observed bug WORK-233.
//   - Floor: LastConsolidatedAt < now - ConsolidationFloor. Daily
//     cadence ensures every active pair gets at least one pass per
//     24h regardless of fact count.
//
// Substrate gating: only actors with Kind == KindNPCShared have
// Relationships populated by RecordInteraction (gated there). The
// scan filter on Kind here is belt-and-braces — even if a stateful
// actor somehow has a Relationship row, we skip them. Per-actor
// narrative consolidation (v1 Phase 4 — rewrite NarrativeState.
// EvolvingSummary from agent_action_log) is deferred to a follow-up
// PR; v2 doesn't have agent_action_log yet.

// ConsolidationCeiling is the high-water-mark fact count that forces
// a mid-cycle consolidation pass even if the daily floor hasn't
// elapsed. Mirrors v1's consolidationCeiling.
const ConsolidationCeiling = 20

// ConsolidationFloor is the minimum age past which a relationship
// gets re-consolidated regardless of fact count. Daily cadence —
// mirrors v1's 24h consolidationFloor.
const ConsolidationFloor = 24 * time.Hour

// ConsolidationFirstMinFacts is the minimum SalientFacts count
// required to qualify a FIRST consolidation (LastConsolidatedAt ==
// nil). Mirrors v1 WORK-233: pre-fix, any new pair with >= 1 fact
// qualified, producing fake-deep "coherent impressions" distilled
// from a single "Good morrow". Subsequent consolidations still
// qualify via the daily-floor branch regardless of fact count.
const ConsolidationFirstMinFacts = 5

// ConsolidationsPerSweep caps how many relationships can be
// consolidated in one sweep cycle. Combined with
// ConsolidationSweepInterval this is the rate-limit circuit
// breaker against accidental N² blow-up if the relationship table
// ever grows large. Mirrors v1's consolidationsPerSweep.
const ConsolidationsPerSweep = 5

// ConsolidationSweepInterval is the cadence at which the off-world
// worker polls for consolidation candidates. Combined with
// ConsolidationsPerSweep this gives 20 consolidations/hour hard cap.
// Mirrors v1's consolidationSweepInterval.
const ConsolidationSweepInterval = 15 * time.Minute

// ConsolidationCandidate is the snapshot of a (actor, peer) pair the
// off-world worker needs to build a consolidation prompt and apply
// the result. Produced by FindConsolidationCandidates; consumed by
// the worker which builds the LLM Request and then issues
// ApplyConsolidation with the SnapshotLen for race-safe pruning.
//
// All fields are owned by the candidate (deep-copied at scan time)
// so the worker can read them without holding any reference back
// into world state.
type ConsolidationCandidate struct {
	ActorID          ActorID
	PeerID           ActorID
	ActorName        string
	PeerName         string
	ActorLLMAgent    string
	PriorSummary     string
	Facts            []SalientFact
	SnapshotLen      int
	LastConsolidated *time.Time
}

// FindConsolidationCandidates returns a Command that scans the world
// for relationships needing a consolidation pass and returns up to
// `limit` candidates ordered ceiling-overdue first, then NULLS first,
// then oldest-consolidated, then fact-count desc, then by (actor, peer)
// ID for deterministic tiebreaks.
//
// `at` is the "now" reference used to evaluate the daily-floor branch;
// callers pass time.Now() in production and a fixed time in tests for
// determinism.
//
// Returns []ConsolidationCandidate (possibly empty) as the Command's
// Value. Never returns an error.
func FindConsolidationCandidates(at time.Time, limit int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			out := []ConsolidationCandidate{}
			if limit <= 0 {
				return out, nil
			}
			cutoff := at.Add(-ConsolidationFloor)

			type entry struct {
				actor   *Actor
				peerID  ActorID
				rel     *Relationship
				ceiling bool
			}
			var all []entry
			for _, actor := range w.Actors {
				if actor.Kind != KindNPCShared {
					continue
				}
				for peerID, rel := range actor.Relationships {
					if rel == nil {
						continue
					}
					n := len(rel.SalientFacts)
					if n == 0 {
						continue
					}
					ceilingOverdue := n >= ConsolidationCeiling
					firstQualifies := rel.LastConsolidatedAt == nil && n >= ConsolidationFirstMinFacts
					floorOverdue := rel.LastConsolidatedAt != nil && rel.LastConsolidatedAt.Before(cutoff)
					if !ceilingOverdue && !firstQualifies && !floorOverdue {
						continue
					}
					all = append(all, entry{actor: actor, peerID: peerID, rel: rel, ceiling: ceilingOverdue})
				}
			}

			sort.Slice(all, func(i, j int) bool {
				a, b := all[i], all[j]
				if a.ceiling != b.ceiling {
					return a.ceiling
				}
				ai := a.rel.LastConsolidatedAt
				bi := b.rel.LastConsolidatedAt
				if (ai == nil) != (bi == nil) {
					return ai == nil
				}
				if ai != nil && !ai.Equal(*bi) {
					return ai.Before(*bi)
				}
				ali := len(a.rel.SalientFacts)
				bli := len(b.rel.SalientFacts)
				if ali != bli {
					return ali > bli
				}
				if a.actor.ID != b.actor.ID {
					return a.actor.ID < b.actor.ID
				}
				return a.peerID < b.peerID
			})

			if len(all) > limit {
				all = all[:limit]
			}

			for _, e := range all {
				peerName := ""
				if peer, ok := w.Actors[e.peerID]; ok && peer != nil {
					peerName = peer.DisplayName
				}
				factsCopy := make([]SalientFact, len(e.rel.SalientFacts))
				copy(factsCopy, e.rel.SalientFacts)
				var lastCons *time.Time
				if e.rel.LastConsolidatedAt != nil {
					t := *e.rel.LastConsolidatedAt
					lastCons = &t
				}
				out = append(out, ConsolidationCandidate{
					ActorID:          e.actor.ID,
					PeerID:           e.peerID,
					ActorName:        e.actor.DisplayName,
					PeerName:         peerName,
					ActorLLMAgent:    e.actor.LLMAgent,
					PriorSummary:     e.rel.SummaryText,
					Facts:            factsCopy,
					SnapshotLen:      len(factsCopy),
					LastConsolidated: lastCons,
				})
			}
			return out, nil
		},
	}
}

// ApplyConsolidation returns a Command that replaces the relationship's
// SummaryText with newSummary, prunes the first snapshotLen entries
// from SalientFacts (preserving anything appended after the snapshot),
// and stamps LastConsolidatedAt = at.
//
// Race-safety: the worker snapshots facts at length L1, issues an LLM
// call, and submits this Command. Between snapshot and apply, more
// facts may have landed via RecordInteraction. We prune by slicing
// SalientFacts[snapshotLen:], which keeps every post-snapshot
// append. This is the same pattern v1 used with the SQL ORDINALITY
// window.
//
// Defensive: if SalientFacts shrunk below snapshotLen between snapshot
// and apply (no command does this today, but cap eviction in
// RecordInteraction could in principle race the apply), we still
// install the new summary and stamp the marker but skip the prune.
// A subsequent sweep will pick the relationship back up if it's still
// over threshold.
//
// Errors:
//   - empty newSummary
//   - actor not found
//   - actor is not KindNPCShared (substrate invariant violation)
//   - relationship not found
//
// On error the relationship is left untouched; the sweep logs and
// retries next cycle. No partial-state writes.
func ApplyConsolidation(actorID, peerID ActorID, newSummary string, snapshotLen int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if newSummary == "" {
				return nil, fmt.Errorf("ApplyConsolidation: empty new summary for %q→%q", actorID, peerID)
			}
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("ApplyConsolidation: actor %q not found", actorID)
			}
			if actor.Kind != KindNPCShared {
				return nil, fmt.Errorf("ApplyConsolidation: actor %q is not KindNPCShared", actorID)
			}
			if actor.Relationships == nil {
				return nil, fmt.Errorf("ApplyConsolidation: actor %q has no Relationships", actorID)
			}
			rel, ok := actor.Relationships[peerID]
			if !ok || rel == nil {
				return nil, fmt.Errorf("ApplyConsolidation: relationship %q→%q not found", actorID, peerID)
			}
			rel.SummaryText = newSummary
			if snapshotLen > 0 && snapshotLen <= len(rel.SalientFacts) {
				rel.SalientFacts = rel.SalientFacts[snapshotLen:]
			}
			t := at
			rel.LastConsolidatedAt = &t
			rel.UpdatedAt = t
			return nil, nil
		},
	}
}
