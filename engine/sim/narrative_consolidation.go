package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// narrative_consolidation.go — per-actor narrative consolidation
// substrate (Phase 3 PR C2). Periodic LLM-driven rewrite of
// Actor.Narrative.EvolvingSummary from the actor's recent ActionLog
// trail + per-peer Relationship.SummaryText impressions. Mirrors v1's
// per-actor branch in engine/actor_narrative_consolidate.go (the Phase 4
// half of the same file that hosted Phase 3's per-pair consolidation).
//
// The off-world sweep worker that drives this lives in
// engine/sim/cascade/narrative_consolidation.go; this file is sim-package
// primitives only.
//
// Mechanism (mirrors the per-relationship consolidation slice shape):
//
//   1. FindNarrativeConsolidationCandidates runs on the world goroutine,
//      scans KindNPCShared actors whose Narrative is nil or whose
//      LastConsolidatedAt is past the daily floor, snapshots their
//      recent ActionLog window + per-peer SummaryText, and returns up
//      to `limit` candidates with everything the off-world worker
//      needs to build the prompt without coming back to the world
//      goroutine.
//
//   2. Worker (cascade/) decides path based on source material:
//        - If events + peers + prior are all empty: send
//          StampNarrativeConsolidated to mark the actor as
//          "checked, nothing to say" without burning an LLM call. The
//          marker prevents the sweep from retrying every cycle.
//        - Else: build the prompt, call the actor's VA, install
//          via ApplyNarrativeConsolidation.
//
//   3. ApplyNarrativeConsolidation runs on the world goroutine: trim,
//      reject empty, auto-create Actor.Narrative if nil, install
//      EvolvingSummary, stamp LastConsolidatedAt + UpdatedAt. Does NOT
//      touch SeedText — that's the dream pipeline's input, not ours.
//
// Selection rules (2 OR branches — no ceiling, daily floor only):
//
//   - First-time: Actor.Narrative == nil OR
//     Actor.Narrative.LastConsolidatedAt == nil.
//   - Floor: Actor.Narrative.LastConsolidatedAt < now - NarrativeConsolidationFloor.
//
// There's no ceiling-trigger because per-actor consolidation has no
// append-pressure equivalent to per-pair SalientFacts — the ActionLog
// is shared infra that grows regardless of consolidation cadence and
// is bounded by retention compaction, not by us.
//
// Substrate gating: only Kind == KindNPCShared actors qualify. Stateful
// NPCs carry self-continuity through their own VA's memory-api session;
// PCs are player-driven.
//
// Race-safety: unlike per-pair consolidation (which prefix-verifies
// SalientFacts against FIFO eviction), per-actor has no analogous
// shifting-prefix concern. ActionLog compaction is age-based GC — if
// entries the prompt saw get compacted off the front during the LLM
// call, the summary the LLM produced already reflects them; whether
// they're still in memory at apply time is irrelevant. Narrative is a
// single struct on Actor; world goroutine is the only writer; last-
// write-wins on EvolvingSummary is fine.

// NarrativeConsolidationFloor is the minimum age past which an actor's
// EvolvingSummary gets re-consolidated. Daily cadence — slower than
// per-pair (also 24h) by intent: per-actor reflection is broader and
// shouldn't churn on every tick-cluster. Mirrors v1's
// narrativeConsolidationFloor.
const NarrativeConsolidationFloor = 24 * time.Hour

// NarrativeConsolidationsPerSweep caps the per-actor narrative pass.
// Daily floor only (no ceiling), so the steady-state load is at most
// one call per shared-VA actor per day. The cap protects against an
// unexpected influx of new shared-VA actors all due at once. Mirrors
// v1's narrativeConsolidationsPerSweep.
const NarrativeConsolidationsPerSweep = 2

// NarrativeConsolidationSweepInterval is the cadence at which the
// off-world worker polls for narrative candidates. Combined with
// NarrativeConsolidationsPerSweep this is a hard cap of 8/hour. Matches
// the per-relationship sweep interval so admin reasoning about
// cascade-tick cost stays simple.
const NarrativeConsolidationSweepInterval = 15 * time.Minute

// NarrativeEventsWindow bounds how far back the consolidation reads
// ActionLog for the actor's "what happened to you recently" prompt
// section. 24h matches NarrativeConsolidationFloor — the prompt window
// is the daily-cadence "today's reflection" view. The action-log
// substrate retains 48h by default, so the window fits comfortably
// within retention without forcing a bump.
const NarrativeEventsWindow = 24 * time.Hour

// NarrativeEventsLimit caps the events the prompt includes. Engine
// logs can be high-volume on busy days; the LLM doesn't need every
// line to synthesize a coherent self-reflection. Mirrors v1's
// narrativeRecentEventsLimit.
const NarrativeEventsLimit = 40

// NarrativePeerSummary is one peer's consolidated impression from the
// perspective of the actor being reflected on. Keyed by PeerID rather
// than DisplayName so duplicate display names (two NPCs both named
// "Mary", say) don't collide and shadow one another in the candidate
// snapshot.
type NarrativePeerSummary struct {
	PeerID  ActorID
	Name    string
	Summary string
}

// NarrativeCandidate is the snapshot the off-world worker needs to
// build a narrative consolidation prompt and apply the result. Produced
// by FindNarrativeConsolidationCandidates; consumed by the worker which
// builds the LLM Request and issues either ApplyNarrativeConsolidation
// (when source material is present) or StampNarrativeConsolidated
// (when everything is empty and the marker is the only update).
//
// All fields are owned by the candidate (deep-copied at scan time) so
// the worker can read them without holding any reference into world
// state. Events is oldest-first within the window; PeerSummaries is a
// fresh slice sorted by peer DisplayName, with PeerID tiebreak so the
// rendering is deterministic even when display names collide.
type NarrativeCandidate struct {
	ActorID          ActorID
	ActorName        string
	ActorLLMAgent    string
	PriorSummary     string
	Events           []ActionLogEntry
	PeerSummaries    []NarrativePeerSummary
	LastConsolidated *time.Time
}

// HasSourceMaterial reports whether the candidate has any input the
// LLM could synthesize from. Drives the worker's decision to call the
// LLM vs stamp-only.
//
// PriorSummary is treated as empty when whitespace-only — the prompt
// build strips trim-only strings too, so an actor with `"   "` for a
// prior would otherwise burn an LLM call with effectively zero source
// material.
func (c NarrativeCandidate) HasSourceMaterial() bool {
	return len(c.Events) > 0 || len(c.PeerSummaries) > 0 || strings.TrimSpace(c.PriorSummary) != ""
}

// FindNarrativeConsolidationCandidates returns a Command that scans
// the world for shared-VA NPC actors due for a narrative consolidation
// and returns up to `limit` candidates with full snapshot data.
//
// `at` is the "now" reference for the daily-floor branch; callers pass
// time.Now() in production and a fixed time in tests for determinism.
//
// Ordering: NULLS first on LastConsolidatedAt (untouched actors run
// before previously-consolidated), then oldest LastConsolidatedAt
// within the touched group, then deterministic tiebreak by ActorID.
//
// Returns []NarrativeCandidate (possibly empty) as the Command's
// Value. Never returns an error.
func FindNarrativeConsolidationCandidates(at time.Time, limit int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			out := []NarrativeCandidate{}
			if limit <= 0 {
				return out, nil
			}
			cutoff := at.Add(-NarrativeConsolidationFloor)
			eventsCutoff := at.Add(-NarrativeEventsWindow)

			type entry struct {
				actor        *Actor
				lastCons     *time.Time
				priorSummary string
			}
			var all []entry
			for _, actor := range w.Actors {
				if actor == nil || actor.Kind != KindNPCShared {
					continue
				}
				// Transient-visitor skip: visitors are KindNPCShared but
				// stateless by design — they don't accumulate Narrative
				// state (the shared salem-visitor VA has dream_mode='none',
				// learning_enabled=false, cache_prompts=false). Identity
				// for each visitor is engine-injected per call from
				// VisitorState; reflection across days would be meaningless
				// for an actor whose existence is bounded in hours. See
				// shared/notes/codebase/salem-engine-v2/visitor.
				if actor.VisitorState != nil {
					continue
				}
				var lastCons *time.Time
				var prior string
				if actor.Narrative != nil {
					if actor.Narrative.LastConsolidatedAt != nil {
						t := *actor.Narrative.LastConsolidatedAt
						lastCons = &t
					}
					prior = actor.Narrative.EvolvingSummary
				}
				// Qualifying: never-consolidated OR past the daily floor.
				firstTime := lastCons == nil
				floorOverdue := lastCons != nil && lastCons.Before(cutoff)
				if !firstTime && !floorOverdue {
					continue
				}
				all = append(all, entry{actor: actor, lastCons: lastCons, priorSummary: prior})
			}

			sort.Slice(all, func(i, j int) bool {
				a, b := all[i], all[j]
				if (a.lastCons == nil) != (b.lastCons == nil) {
					return a.lastCons == nil
				}
				if a.lastCons != nil && !a.lastCons.Equal(*b.lastCons) {
					return a.lastCons.Before(*b.lastCons)
				}
				return a.actor.ID < b.actor.ID
			})

			if len(all) > limit {
				all = all[:limit]
			}

			for _, e := range all {
				events := snapshotEventsForActor(w, e.actor.ID, eventsCutoff)
				peers := snapshotPeerSummaries(w, e.actor)
				out = append(out, NarrativeCandidate{
					ActorID:          e.actor.ID,
					ActorName:        e.actor.DisplayName,
					ActorLLMAgent:    e.actor.LLMAgent,
					PriorSummary:     e.priorSummary,
					Events:           events,
					PeerSummaries:    peers,
					LastConsolidated: e.lastCons,
				})
			}
			return out, nil
		},
	}
}

// snapshotEventsForActor returns the actor's ActionLog entries within
// the window, oldest first, capped at NarrativeEventsLimit. The slice
// is freshly allocated — callers may mutate without affecting world
// state. Returns nil for an empty result so the candidate's field
// semantics match an unset slice exactly.
//
// Filter: ActorID match AND OccurredAt > cutoff (strict After, matching
// v1's `occurred_at > NOW() - interval` semantic). When more than the
// limit qualify, keep the most-recent N (drop oldest) then re-sort
// ascending for the prompt.
func snapshotEventsForActor(w *World, actorID ActorID, cutoff time.Time) []ActionLogEntry {
	if len(w.ActionLog) == 0 {
		return nil
	}
	var all []ActionLogEntry
	for _, e := range w.ActionLog {
		if e.ActorID != actorID {
			continue
		}
		if !e.OccurredAt.After(cutoff) {
			continue
		}
		all = append(all, CloneActionLogEntry(e))
	}
	if len(all) == 0 {
		return nil
	}
	// Sort ascending by OccurredAt (the ActionLog itself is approximately
	// monotonic from world-goroutine ordering, but subscribers can write
	// slightly out-of-band timestamps; we want strict oldest-first for
	// the prompt).
	sort.Slice(all, func(i, j int) bool {
		return all[i].OccurredAt.Before(all[j].OccurredAt)
	})
	// If we exceeded the limit, drop the oldest extras — the prompt
	// prefers recent events over a full week.
	if len(all) > NarrativeEventsLimit {
		all = all[len(all)-NarrativeEventsLimit:]
	}
	return all
}

// snapshotPeerSummaries returns a fresh slice of {PeerID, Name,
// Summary} for peers with a non-empty consolidated impression. Sorted
// by Name ascending with PeerID as tiebreak — deterministic even when
// two peers share a display name. Returns nil for an empty result so
// the candidate's field semantics match an unset slice exactly.
//
// Skips peers without a Relationship row, peers with empty SummaryText
// (no consolidation has run yet for them), and peers whose Actor entry
// is missing or unnamed in world state.
func snapshotPeerSummaries(w *World, actor *Actor) []NarrativePeerSummary {
	if len(actor.Relationships) == 0 {
		return nil
	}
	out := make([]NarrativePeerSummary, 0, len(actor.Relationships))
	for peerID, rel := range actor.Relationships {
		if rel == nil {
			continue
		}
		if rel.SummaryText == "" {
			continue
		}
		peer, ok := w.Actors[peerID]
		if !ok || peer == nil || peer.DisplayName == "" {
			continue
		}
		out = append(out, NarrativePeerSummary{
			PeerID:  peerID,
			Name:    peer.DisplayName,
			Summary: rel.SummaryText,
		})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// ApplyNarrativeConsolidation returns a Command that installs
// `newSummary` as the actor's EvolvingSummary and stamps
// LastConsolidatedAt + UpdatedAt. Auto-creates Actor.Narrative if nil
// (mirrors RecordInteraction's lazy-create posture for Relationships).
//
// Does NOT touch SeedText — that's an external input (dream pipeline,
// admin tool) and is not within this slice's authority to overwrite.
//
// Errors:
//   - empty newSummary (after trim)
//   - actor not found
//   - actor is not KindNPCShared (substrate invariant violation)
//
// On any error the actor's Narrative is left untouched.
func ApplyNarrativeConsolidation(actorID ActorID, newSummary string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Trim at the substrate boundary — the cascade driver already
			// trims, but the Command is public (callable directly from
			// tests / future callers / admin paths) so we defend the
			// invariant here: EvolvingSummary is never set to whitespace-
			// only via this path. Mirrors AppendActionLogEntry's
			// rune-truncate-at-boundary posture.
			newSummary = strings.TrimSpace(newSummary)
			if newSummary == "" {
				return nil, fmt.Errorf("ApplyNarrativeConsolidation: empty new summary for %q", actorID)
			}
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("ApplyNarrativeConsolidation: actor %q not found", actorID)
			}
			if actor.Kind != KindNPCShared {
				return nil, fmt.Errorf("ApplyNarrativeConsolidation: actor %q is not KindNPCShared", actorID)
			}
			if actor.VisitorState != nil {
				return nil, fmt.Errorf("ApplyNarrativeConsolidation: actor %q is a transient visitor", actorID)
			}
			if actor.Narrative == nil {
				actor.Narrative = &NarrativeState{CreatedAt: at}
			}
			actor.Narrative.EvolvingSummary = newSummary
			t := at
			actor.Narrative.LastConsolidatedAt = &t
			actor.Narrative.UpdatedAt = at
			return nil, nil
		},
	}
}

// StampNarrativeConsolidated returns a Command that marks the actor's
// Narrative as just-checked without installing a new summary. Used for
// the "checked, nothing to say" path — the candidate had no events, no
// peer summaries, no prior reflection. Stamping prevents the sweep
// from retrying the empty actor every cycle.
//
// Auto-creates Actor.Narrative if nil (same lazy-create posture as
// ApplyNarrativeConsolidation). Does NOT touch EvolvingSummary or
// SeedText. UpdatedAt is also bumped so any consumer treating it as
// "last touch by the engine" sees the stamp.
//
// Errors:
//   - actor not found
//   - actor is not KindNPCShared
func StampNarrativeConsolidated(actorID ActorID, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("StampNarrativeConsolidated: actor %q not found", actorID)
			}
			if actor.Kind != KindNPCShared {
				return nil, fmt.Errorf("StampNarrativeConsolidated: actor %q is not KindNPCShared", actorID)
			}
			if actor.VisitorState != nil {
				return nil, fmt.Errorf("StampNarrativeConsolidated: actor %q is a transient visitor", actorID)
			}
			if actor.Narrative == nil {
				actor.Narrative = &NarrativeState{CreatedAt: at}
			}
			t := at
			actor.Narrative.LastConsolidatedAt = &t
			actor.Narrative.UpdatedAt = at
			return nil, nil
		},
	}
}
