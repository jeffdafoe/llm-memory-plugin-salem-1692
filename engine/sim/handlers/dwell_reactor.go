package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// dwell_reactor.go — Phase 3 dwell perception PR. Subscribers for the
// dwell-lifecycle decision points (DwellTickApplied / DwellEnded).
// Each stamps a self-perception warrant on the eating (or resting)
// actor so the LLM's next reactor tick perceives the cue text.
//
// DwellStarted deliberately mints NO warrant (LLM-316): the start of a
// meal is the actor's own just-committed action — the consume tool
// result already reports the outcome in-tick, and the standing `## You`
// dwell line ("You are eating stew …, ~N minute(s) remaining") carries
// the keep-eating signal every tick after. Echoing it as a next-tick
// wake burned an LLM call to contemplate a meal already underway and
// pushed PC-HUD flavor copy ("this stew looks really good…") into NPC
// perception. The DwellStarted event itself still fires for the
// Hub/HUD layer and audit.
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
// terminal narration (item-exhausted / floor-hit / item-source walk-away).
// When the narration is empty — object-source walk-aways and the defensive
// Unknown/CatalogUnknown reasons — no warrant is stamped (the DwellEnded
// event still stands for audit/replay); an empty-text warrant would render
// the vague "Something happened nearby" fallback (LLM-65).
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
	// LLM-65: an empty terminal narration means this DwellEnded is silent —
	// keep the event for audit/replay, but don't mint a perception warrant (an
	// empty-text DwellEndedWarrantReason renders the vague "Something happened
	// nearby" fallback). Breadcrumb only for an *unintended* empty so a real
	// narration-coverage gap stays observable. By-design silent (no log): the
	// defensive Unknown/CatalogUnknown reasons, and object-source walk-aways
	// (free, resumable — the common live case, so logging it would be noise).
	// Anything else empty is an unhandled combination worth flagging — e.g. a
	// future item-source rest's walk-away, or a new attribute with no
	// exhausted/floor line.
	if narration == "" {
		byDesignSilent := ended.Reason == sim.DwellEndUnknown ||
			ended.Reason == sim.DwellEndCatalogUnknown ||
			(ended.Reason == sim.DwellEndWalkedAway && ended.Source == sim.DwellSourceObject)
		if !byDesignSilent {
			log.Printf(
				"handlers: dwell-reactor skipping empty-narration DwellEnded for actor %q (event %d, reason %s, attr %q, source %q) — no narration coverage",
				ended.ActorID, ended.EventID(), ended.Reason, ended.Attribute, ended.Source,
			)
		}
		return
	}
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

// RegisterDwellHandlers wires the dwell-lifecycle event subscribers
// into the world (DwellTickApplied, DwellEnded).
// Separate from RegisterPayHandlers / RegisterSpeechHandlers
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
	w.Subscribe(sim.SubscriberFunc(handleDwellTickAppliedWarrants))
	w.Subscribe(sim.SubscriberFunc(handleDwellEndedWarrants))
}
