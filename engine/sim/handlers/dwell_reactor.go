package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// dwell_reactor.go — Phase 3 dwell perception PR. Subscribers for the
// three dwell-lifecycle events (DwellStarted / DwellTickApplied /
// DwellEnded). Each stamps a self-perception warrant on the eating
// (or resting) actor so the LLM's next reactor tick perceives the cue
// text — keeping NPCs parked at the structure to finish dwell-bearing
// meals.
//
// Targeting policy: self only. The eater/rester gets the warrant; no
// fan-out to co-located peers in this PR (visible-to-others is a
// follow-up — call 7 in the design pass). PC actors get the warrant
// too (subscriber doesn't gate by ActorKind) — Hub-layer fan-out to a
// PC HUD line lands when Hub ports; for now the same warrant powers
// the LLM cue.
//
// Dedup: every dwell Reason returns DedupDiscriminator=0 (bypass
// dedup, same posture as BasicWarrantReason). Each dwell event is 1:1
// with its triggering moment, so there's no double-stamp risk from
// upstream, and (Kind, 0) collapse is avoided because dedup is
// bypassed for these warrants. See reactor_dwell.go for the rationale.
//
// Force: false. Dwell cues are atmosphere, not emergencies; jitter and
// the per-minute rate gate apply normally.
//
// Wake cadence (ZBBS-WORK-407): the per-minute DwellTickApplied still fires —
// it applies the recovery and updates the client HUD — but its WARRANT (the
// thing that wakes a reactor tick / LLM call) is stamped only when the recovery
// crosses a stat boundary that changes the actor's situation: the driving need
// falling out of its red tier. A mid-meal minute that merely nudges a still-red
// or already-sated need stamps nothing, so a long meal or a rest under the shade
// tree no longer burns an LLM call every minute. Completion is its own beat
// (DwellEnded), so the terminal tick defers to it rather than double-waking.

// handleDwellStartedWarrants is the DwellStarted subscriber. Stamps
// DwellStartedWarrantReason on the eater. Skip emit when the event
// carries no credits (defensive — Consume + commitPayTransfer skip
// emitting DwellStarted with empty credits, but the event arriving
// here with zero would mean an upstream change broke the contract).
func handleDwellStartedWarrants(w *sim.World, evt sim.Event) {
	started, ok := evt.(*sim.DwellStarted)
	if !ok {
		return
	}
	if started.ActorID == "" || len(started.Credits) == 0 {
		return
	}
	actor, ok := w.Actors[started.ActorID]
	if !ok || actor == nil {
		return
	}
	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: started.ActorID,
		Force:          false,
		Reason: sim.DwellStartedWarrantReason{
			ItemKind:      started.Kind,
			StructureID:   started.StructureID,
			Credits:       cloneDwellCreditSnapshots(started.Credits),
			NarrationText: started.NarrationText,
		},
		SourceEventID: started.EventID(),
		RootEventID:   started.RootEventID(),
		SourceActorID: started.ActorID,
		OccurredAt:    started.At,
	}
	if _, err := sim.StampWarrant(started.ActorID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: dwell-reactor StampWarrant for eater %q on DwellStarted (event %d): %v",
			started.ActorID, started.EventID(), err,
		)
	}
}

// handleDwellTickAppliedWarrants is the DwellTickApplied subscriber. It stamps
// DwellTickAppliedWarrantReason on the eater/rester ONLY when this tick crosses
// the recovering need out of its red tier (the boundary wake, ZBBS-WORK-407) and
// the dwell isn't completing this tick (DwellEnded carries the completion beat).
// A non-boundary minute applies its recovery + HUD update via the event but
// stamps no warrant, so it wakes no LLM tick. The per-tick narration is rendered
// at stamp time so perception build doesn't re-run DwellTickNarration.
func handleDwellTickAppliedWarrants(w *sim.World, evt sim.Event) {
	applied, ok := evt.(*sim.DwellTickApplied)
	if !ok {
		return
	}
	if applied.ActorID == "" {
		return
	}
	actor, ok := w.Actors[applied.ActorID]
	if !ok || actor == nil {
		return
	}
	// Boundary-cadenced wake (ZBBS-WORK-407). The recovery is already applied;
	// only wake the LLM when this tick crossed the driving need OUT of its red
	// tier (value was >= threshold, now < threshold) — the moment the actor's
	// options actually change (it can stop eating / get up now). A still-red or
	// already-sated nudge isn't worth a reactor tick. The terminal tick defers to
	// DwellEnded so completion isn't double-stamped.
	threshold := w.Settings.NeedThresholds.Get(applied.Attribute)
	before := applied.NewNeedValue - applied.NeedDelta // NeedDelta is the signed recovery already applied
	crossedOutOfRed := before >= threshold && applied.NewNeedValue < threshold
	terminalTick := applied.RemainingTicks != nil && *applied.RemainingTicks == 0
	if !crossedOutOfRed || terminalTick {
		return
	}
	now := time.Now().UTC()
	var remaining *int
	if applied.RemainingTicks != nil {
		rt := *applied.RemainingTicks
		remaining = &rt
	}
	meta := sim.WarrantMeta{
		TriggerActorID: applied.ActorID,
		Force:          false,
		Reason: sim.DwellTickAppliedWarrantReason{
			ObjectID:       applied.ObjectID,
			Source:         applied.Source,
			ItemKind:       applied.Kind,
			Attribute:      applied.Attribute,
			NeedDelta:      applied.NeedDelta,
			NewNeedValue:   applied.NewNeedValue,
			RemainingTicks: remaining,
			PeriodMinutes:  applied.PeriodMinutes,
			NarrationText:  sim.DwellTickNarration(applied.Attribute, applied.Source),
		},
		SourceEventID: applied.EventID(),
		RootEventID:   applied.RootEventID(),
		SourceActorID: applied.ActorID,
		OccurredAt:    applied.At,
	}
	if _, err := sim.StampWarrant(applied.ActorID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: dwell-reactor StampWarrant for actor %q on DwellTickApplied (event %d): %v",
			applied.ActorID, applied.EventID(), err,
		)
	}
}

// handleDwellEndedWarrants is the DwellEnded subscriber. Stamps
// DwellEndedWarrantReason on the eater/rester with the pre-rendered
// terminal narration (item-exhausted / floor-hit / walked-away).
// CatalogUnknown produces a warrant with empty NarrationText — the
// event remains for audit/replay but no perception line is rendered.
func handleDwellEndedWarrants(w *sim.World, evt sim.Event) {
	ended, ok := evt.(*sim.DwellEnded)
	if !ok {
		return
	}
	if ended.ActorID == "" {
		return
	}
	actor, ok := w.Actors[ended.ActorID]
	if !ok || actor == nil {
		return
	}
	now := time.Now().UTC()
	narration := sim.DwellCompletionNarration(
		ended.Attribute, ended.Source,
		ended.Reason == sim.DwellEndItemExhausted,
		ended.Reason == sim.DwellEndFloorHit,
		ended.Reason == sim.DwellEndWalkedAway,
	)
	meta := sim.WarrantMeta{
		TriggerActorID: ended.ActorID,
		Force:          false,
		Reason: sim.DwellEndedWarrantReason{
			ObjectID:      ended.ObjectID,
			Source:        ended.Source,
			ItemKind:      ended.Kind,
			Attribute:     ended.Attribute,
			Reason:        ended.Reason,
			NarrationText: narration,
		},
		SourceEventID: ended.EventID(),
		RootEventID:   ended.RootEventID(),
		SourceActorID: ended.ActorID,
		OccurredAt:    ended.At,
	}
	if _, err := sim.StampWarrant(ended.ActorID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: dwell-reactor StampWarrant for actor %q on DwellEnded (event %d, reason %s): %v",
			ended.ActorID, ended.EventID(), ended.Reason, err,
		)
	}
}

// RegisterDwellHandlers wires the three dwell-lifecycle event
// subscribers into the world (DwellStarted, DwellTickApplied,
// DwellEnded). Separate from RegisterPayHandlers / RegisterSpeechHandlers
// / RegisterSceneQuoteHandlers / RegisterPayWithItemHandlers for the
// same opt-in-piecewise reason — a build that wants commerce but not
// the dwell cues (or vice versa) can compose. Must run on the world
// goroutine — call before World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice would invoke each subscriber twice
// per event. Since dwell warrants bypass dedup (DedupDiscriminator=0),
// the second stamp would land — and the open-cycle warrant list would
// briefly hold two copies of the same Reason until the next tick. This
// is a wedge worth knowing about for tests / admin tooling that
// re-registers; production wiring registers once at world build.
func RegisterDwellHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterDwellHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleDwellStartedWarrants))
	w.Subscribe(sim.SubscriberFunc(handleDwellTickAppliedWarrants))
	w.Subscribe(sim.SubscriberFunc(handleDwellEndedWarrants))
}

// cloneDwellCreditSnapshots returns an independent copy of credits.
// Used by the DwellStarted subscriber to snapshot the event's payload
// onto the warrant Reason — a subsequent mutation of the event's slice
// (none today, but the contract is value semantics) must not reach the
// warrant. RemainingTicks is the only pointer field; deep-copy it.
func cloneDwellCreditSnapshots(src []sim.DwellCreditSnapshot) []sim.DwellCreditSnapshot {
	if len(src) == 0 {
		return nil
	}
	out := make([]sim.DwellCreditSnapshot, len(src))
	for i, c := range src {
		cp := c
		if c.RemainingTicks != nil {
			rt := *c.RemainingTicks
			cp.RemainingTicks = &rt
		}
		out[i] = cp
	}
	return out
}
