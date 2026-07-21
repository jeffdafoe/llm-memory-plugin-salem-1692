package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// consolidation.go — per-relationship salient-fact consolidation
// substrate (Phase 3 PR C1). The sweep worker that drives this lives
// off-world in engine/sim/cascade/consolidation.go because it issues
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
//   2. Worker (cascade/) calls the LLM with the prompt, receives a
//      new SummaryText.
//
//   3. ApplyConsolidation runs back on the world goroutine: verify
//      that the snapshot facts still match the live SalientFacts
//      prefix (defends against FIFO cap eviction during the LLM
//      call); on match, replace Relationship.SummaryText, prune
//      the prefix, stamp LastConsolidatedAt; on mismatch, return
//      ErrStaleConsolidationSnapshot — no writes, next sweep
//      re-snapshots from current state and retries.
//
// Selection rules (3 OR branches):
//
//   - Ceiling: len(SalientFacts) >= ConsolidationCeiling. A pre-eviction
//     backstop, not the routine cadence — forces an early pass when a
//     pair out-runs a full day's interactions, giving the sweep a chance
//     to consolidate before the FIFO cap evicts facts off the front. Not
//     a hard guarantee: a pair waiting behind others in the sweep, or
//     gaining > cap-ceiling facts during the LLM call, can still evict.
//   - First-time: LastConsolidatedAt == nil AND len(SalientFacts) >=
//     ConsolidationFirstMinFacts. The minimum-facts gate prevents
//     fake-deep "coherent impressions" distilled from one "Good
//     morrow" line — observed bug WORK-233.
//   - Floor: LastConsolidatedAt < now - ConsolidationFloor. The primary
//     cadence — every active pair gets one pass per 24h regardless of
//     fact count.
//
// Dealing gate (LLM-434): the three branches above are ANDed with a
// precondition — the pair must carry at least one dealing-relevant
// (transactional) fact, OR already have a SummaryText. The prompt asks for a
// judgment about DEALING with the peer (LLM-426); a pure-social pair (only
// spoke/heard facts) can only ever answer "nothing notable", which prunes the
// pair — and because a pruned row is deleted (ClearConsolidation) and
// re-created as a first-timer on the next interaction, that pair would
// otherwise churn one LLM call every ConsolidationFirstMinFacts. See
// relHasDealingFact.
//
// Substrate gating: only actors with Kind == KindNPCShared have
// Relationships populated by RecordInteraction (gated there). The
// scan filter on Kind here is belt-and-braces — even if a stateful
// actor somehow has a Relationship row, we skip them. Per-actor
// narrative consolidation (rewrite NarrativeState.EvolvingSummary from
// the actor's recent World.ActionLog + per-peer SummaryText impressions)
// is the sibling slice — narrative_consolidation.go (primitives) +
// cascade/narrative_consolidation.go (worker), Phase 3 PR C2.

// ConsolidationCeiling is a pre-eviction backstop, not the routine
// cadence. The daily floor (ConsolidationFloor) is the normal trigger;
// the ceiling only forces an early pass when a pair out-runs a full
// day's interactions, giving the sweep a chance to consolidate before
// the FIFO cap (MaxSalientFactsPerRelationship) evicts facts. Kept below
// the cap for headroom — not a hard guarantee (a pair behind others in
// the sweep, or gaining > cap-ceiling facts mid-LLM-call, can still
// evict). (v1 used this as a routine ~hourly trigger for chatty pairs;
// v2 makes the floor the cadence so a pair re-opines at most once/day.)
const ConsolidationCeiling = 150

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
// ApplyConsolidation, passing Facts through as the snapshot that the
// apply path verifies against the live slice's prefix.
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
	LastConsolidated *time.Time
}

// relHasDealingFact reports whether the relationship carries at least one
// dealing-relevant salient fact — a transactional kind (paid/paid_by/delivered/
// received/served/gave/worked/hired/kept_deposit/…), i.e. anything that is NOT
// bare speech. InteractionSpoke and InteractionHeard are the only two
// non-dealing kinds, so "not speech" is an exhaustive test: a pair with none of
// the transactional kinds cannot yield the dealing judgment the consolidation
// prompt asks for (LLM-426) and would only ever answer "nothing notable".
// FindConsolidationCandidates uses it to keep pure-social pairs out of the
// sweep entirely (LLM-434).
//
// A NEW non-speech InteractionKind is treated as dealing-relevant by default —
// the safe direction, since a new transactional kind should qualify a pair. If
// a future kind is speech-like (a bare utterance carrying no dealing content),
// add it to the exclusion here, mirroring the attribution guard in
// cascade/consolidation.go's renderConsolidationFactLine.
func relHasDealingFact(rel *Relationship) bool {
	if rel == nil {
		return false
	}
	for _, f := range rel.SalientFacts {
		if f.Kind != InteractionSpoke && f.Kind != InteractionHeard {
			return true
		}
	}
	return false
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
				// Transient-visitor skip: visitors are KindNPCShared but
				// stateless by design — they don't accumulate relationship
				// state (RecordInteraction skips when VisitorState != nil
				// on either side). Belt-and-braces gate here: even if a
				// stray Relationship row exists on a visitor somehow, the
				// consolidation cascade skips it. See
				// shared/notes/codebase/salem-engine-v2/visitor.
				if actor.VisitorState != nil {
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
					// Dealing gate (LLM-434): skip a pair that has never carried a
					// dealing-relevant fact and holds no summary yet — a pure-social
					// (spoke/heard-only) pair can only answer "nothing notable", and
					// that prunes/re-creates the row into an LLM-call churn loop. A pair
					// with a SummaryText stays eligible so an established relationship
					// keeps being re-judged if its dealings dry up (a no-update reply
					// retains the summary rather than pruning it — LLM-497).
					if rel.SummaryText == "" && !relHasDealingFact(rel) {
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
					LastConsolidated: lastCons,
				})
			}
			return out, nil
		},
	}
}

// ApplyConsolidation returns a Command that atomically replaces the
// relationship's SummaryText, prunes the consolidated facts, and
// stamps LastConsolidatedAt — but only after verifying that the
// snapshot the worker took at scan time still matches the live
// slice's prefix.
//
// Race-safety: between snapshot and apply, RecordInteraction may
// have appended new facts AND the FIFO cap eviction in
// relationship_commands.go may have dropped some of the snapshotted
// facts off the front. We CANNOT prune by raw length — that drops
// post-snapshot appends from the prefix once eviction has shifted
// the slice. Instead, we verify that rel.SalientFacts[:len(snapshot)]
// equals the snapshot value-wise, then prune that prefix.
//
// Match: install summary, prune the prefix, stamp LastConsolidatedAt.
//
// Mismatch (the slice's prefix no longer equals the snapshot —
// eviction has happened or some other mutation): return a typed
// ErrStaleConsolidationSnapshot error. NO writes anywhere — summary
// is not installed, prune is not done, LastConsolidatedAt is not
// stamped. The next sweep re-snapshots from the new live state and
// retries. We lose one LLM call's work; the row stays consistent.
//
// Empty snapshot (no facts to prune): install summary + stamp. This
// is a legitimate edge case if a relationship has facts at scan
// time but they all evict before apply; the worker still has a
// non-empty Facts slice but we treat zero facts as "nothing to
// verify, nothing to prune."
//
// Errors:
//   - empty or whitespace-only newSummary (trimmed at the boundary)
//   - actor not found
//   - actor is not KindNPCShared (substrate invariant violation)
//   - relationship not found
//   - stale snapshot (ErrStaleConsolidationSnapshot)
//
// On any error the relationship is left untouched.
func ApplyConsolidation(actorID, peerID ActorID, newSummary string, snapshotFacts []SalientFact, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Trim at the substrate boundary — the cascade driver
			// already trims, but the Command is public (callable from
			// tests / admin paths / future code) and the invariant
			// "SummaryText is never set to whitespace-only via this
			// path" belongs here, not just in the cascade. Mirrors
			// AppendActionLogEntry's rune-truncate posture +
			// ApplyNarrativeConsolidation's trim posture (PR C2).
			newSummary = strings.TrimSpace(newSummary)
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
			if actor.VisitorState != nil {
				return nil, fmt.Errorf("ApplyConsolidation: actor %q is a transient visitor", actorID)
			}
			if actor.Relationships == nil {
				return nil, fmt.Errorf("ApplyConsolidation: actor %q has no Relationships", actorID)
			}
			rel, ok := actor.Relationships[peerID]
			if !ok || rel == nil {
				return nil, fmt.Errorf("ApplyConsolidation: relationship %q→%q not found", actorID, peerID)
			}
			n := len(snapshotFacts)
			if n == 0 {
				// No facts to verify / prune. Still install summary +
				// stamp — empty-snapshot apply is benign.
				rel.SummaryText = newSummary
				t := at
				rel.LastConsolidatedAt = &t
				rel.UpdatedAt = t
				return nil, nil
			}
			if n > len(rel.SalientFacts) || !salientFactsPrefixEqual(rel.SalientFacts[:n], snapshotFacts) {
				return nil, ErrStaleConsolidationSnapshot
			}
			rel.SummaryText = newSummary
			rel.SalientFacts = rel.SalientFacts[n:]
			t := at
			rel.LastConsolidatedAt = &t
			rel.UpdatedAt = t
			return nil, nil
		},
	}
}

// ClearConsolidation returns a Command for the "nothing notable" outcome
// (LLM-426): the actor has formed no dealing-relevant judgment about the peer,
// so the consolidated summary is cleared and the pair is PRUNED rather than
// storing filler prose. The cascade routes here only when the candidate
// carried NO prior summary — a no-update reply on an established pair
// retains the summary via ApplyConsolidation instead (LLM-497), so an
// accumulated judgment is never one quiet day away from deletion. Same
// snapshot race-safety contract as ApplyConsolidation — the snapshot's
// prefix must still equal the live slice.
//
//   - Match, nothing left after pruning the consolidated facts → DELETE the
//     relationship row entirely, so the relationship graph keeps only edges
//     that carry a judgment (a chatty-but-unremarkable pair stops accreting a
//     row). RecordInteraction re-creates the row if they interact again.
//   - Match, but facts arrived during the LLM call → keep the row for the next
//     sweep to judge those, clear SummaryText, and stamp LastConsolidatedAt so
//     it isn't re-selected before the daily floor.
//   - Mismatch → ErrStaleConsolidationSnapshot, no writes (next sweep retries),
//     exactly as ApplyConsolidation.
//
// Unlike ApplyConsolidation this does NOT reject an empty summary — clearing is
// the whole point. The caller (cascade) routes here only on an explicit
// "nothing notable" reply, never on an empty/garbage one (that keeps the
// reject-and-retry posture in consolidateOne).
func ClearConsolidation(actorID, peerID ActorID, snapshotFacts []SalientFact, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("ClearConsolidation: actor %q not found", actorID)
			}
			if actor.Kind != KindNPCShared {
				return nil, fmt.Errorf("ClearConsolidation: actor %q is not KindNPCShared", actorID)
			}
			if actor.VisitorState != nil {
				return nil, fmt.Errorf("ClearConsolidation: actor %q is a transient visitor", actorID)
			}
			if actor.Relationships == nil {
				return nil, fmt.Errorf("ClearConsolidation: actor %q has no Relationships", actorID)
			}
			rel, ok := actor.Relationships[peerID]
			if !ok || rel == nil {
				return nil, fmt.Errorf("ClearConsolidation: relationship %q→%q not found", actorID, peerID)
			}
			n := len(snapshotFacts)
			if n > len(rel.SalientFacts) || !salientFactsPrefixEqual(rel.SalientFacts[:n], snapshotFacts) {
				return nil, ErrStaleConsolidationSnapshot
			}
			rel.SalientFacts = rel.SalientFacts[n:]
			if len(rel.SalientFacts) == 0 {
				// No judgment, no fresh facts — drop the edge entirely.
				delete(actor.Relationships, peerID)
				return nil, nil
			}
			// Facts landed during the LLM call: keep the row for the next
			// sweep to judge, but clear the (now-absent) summary and stamp.
			rel.SummaryText = ""
			t := at
			rel.LastConsolidatedAt = &t
			rel.UpdatedAt = t
			return nil, nil
		},
	}
}

// ErrStaleConsolidationSnapshot is returned by ApplyConsolidation
// when the live SalientFacts slice's prefix no longer matches the
// snapshot the worker took at scan time — typically because the
// FIFO cap eviction in RecordInteraction fired during the LLM call.
// The sweep worker logs and skips; the next sweep retries from a
// fresh snapshot.
var ErrStaleConsolidationSnapshot = fmt.Errorf("ApplyConsolidation: snapshot stale (FIFO eviction or unexpected mutation)")

// salientFactsPrefixEqual reports whether `live` and `snap` have the
// same length and equal SalientFact values element-wise. Comparison
// is by-value on (At, Kind, Text) — the only fields a SalientFact
// carries (no engine-minted ID on this type as of PR C1; future
// work may add one if salvaging-on-eviction becomes necessary).
//
// time.Time.Equal is used instead of == to avoid wall/monotonic
// clock mismatches; in practice all SalientFact.At values come from
// the same time.Now() call path and don't carry monotonic readings,
// but Equal is the correct comparator regardless.
func salientFactsPrefixEqual(live, snap []SalientFact) bool {
	if len(live) != len(snap) {
		return false
	}
	for i := range live {
		if live[i].Kind != snap[i].Kind {
			return false
		}
		if live[i].Text != snap[i].Text {
			return false
		}
		if !live[i].At.Equal(snap[i].At) {
			return false
		}
	}
	return true
}
