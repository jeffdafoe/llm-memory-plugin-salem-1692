package sim

import "time"

// events_dwell.go — Phase 3 dwell perception PR. Event family for the
// three lifecycle moments of a dwell credit. Subscribers in
// engine/sim/handlers/dwell_reactor.go translate these into reactor
// warrants that surface as LLM-perception cues on the eating/resting
// actor's next tick. Hub layer (when ported) subscribes to the same
// events to fan them out as PC HUD broadcasts.
//
// Lifecycle:
//
//   1. DwellStarted — one-shot at consume (or commitPayTransfer's
//      consume_now path), fires when UpsertItemDwellCredits stamps at
//      least one credit. Carries the per-item ConsumeDwellNarration
//      hint and a snapshot of every credit that just landed so a
//      perception cue ("this stew looks really good — you'll need some
//      time to enjoy it properly") can render on the actor's NEXT
//      tick.
//
//   2. DwellTickApplied — fires per applied credit in ApplyDwellTick,
//      once a period has elapsed since LastCreditedAt and the actor is
//      still at the pin. Carries the post-clamp need delta and the
//      remaining tick countdown so subscribers can render the per-tick
//      payoff ("you take another bite — the gnawing ebbs") AND let Hub
//      clients keep a status-bar countdown current.
//
//   3. DwellEnded — fires per credit termination (item exhausted,
//      floor-hit, walked away, or catalog-unknown defense). Carries a
//      DwellEndReason discriminator so subscribers can render the
//      terminal narration ("you finish the last bite, satisfied" /
//      "you feel full" / "you walk away from your meal").
//
// All three skip the actor.LoginUsername PC-only gate that the
// pre-substrate DwellCompletion result-struct narration used —
// LLM-driven NPCs need the perception cues too, and Hub broadcast
// fan-out is subscriber-side filtering, not emit-side.

// DwellCreditSnapshot is a value-typed view of one dwell credit at the
// moment of an event emission. Used inside DwellStarted to carry the
// snapshot of every credit Consume just stamped, so subscribers can
// render the started-dwell perception cue (and Hub clients can compute
// the countdown end time as `event.At + RemainingTicks *
// PeriodMinutes`) without dereferencing the live actor map. Pointer
// for RemainingTicks because nil is valid (source=object credits
// don't decrement).
type DwellCreditSnapshot struct {
	Attribute      NeedKey
	DwellDelta     int  // negative — applied per period
	PeriodMinutes  int  // 0 = no dwell (would-be skipped by HasDwell)
	RemainingTicks *int // nil for source=object; >0 for source=item
}

// DwellStarted fires when at least one dwell credit lands as part of a
// Consume (or pay-with-item consume_now) — i.e. an actor begins a
// slow-burn meal/rest at a pin. Subscribers stamp a perception cue on
// the eater's next reactor tick so the LLM knows "this is starting,
// stay put."
//
// StructureID is the dwell pin (the village_object the actor is
// loitering at — usually the structure/tavern where they're eating).
//
// Credits carries one entry per (Attribute, Source=item) credit just
// stamped — usually one for stew (hunger), could be multiple for an
// item with several need-effects (e.g. a hearty stew with a tiredness
// component).
//
// NarrationText is the pre-rendered consume-time hint from
// ItemKindDef.ConsumeDwellNarration. Empty when the item has no hint
// configured — subscribers render only when non-empty.
//
// At mirrors the LastCreditedAt anchor stamped on every credit in
// Credits, so the dwell timer and the engine log align exactly.
type DwellStarted struct {
	EventBase
	ActorID       ActorID
	Kind          ItemKind
	StructureID   VillageObjectID
	Credits       []DwellCreditSnapshot
	NarrationText string
	At            time.Time
}

func (DwellStarted) isSimEvent() {}

// DwellTickApplied fires per applied credit in ApplyDwellTick — once
// the period has elapsed since LastCreditedAt and the actor is still
// at the pin. One event per credit per tick.
//
// Source / Kind disambiguate item-source (eating stew) from object-
// source (resting under a tree). Kind is empty for source=object.
//
// NeedDelta is the actual signed delta applied to actor.Needs after
// the ClampNeed cap (so the magnitude can be less than the credit's
// DwellDelta when the floor is in play). NewNeedValue is the post-
// clamp value — together they give subscribers everything for "you
// take another bite, the gnawing ebbs" plus countdown math.
//
// RemainingTicks is the POST-decrement count for source=item credits
// (so it reaches 0 only after the final tick fires; the terminating
// DwellEnded{ItemExhausted} fires on the same tick). Nil for
// source=object credits.
//
// PeriodMinutes lets Hub clients compute the next-tick wall-clock as
// At + PeriodMinutes minutes without tracking prior state.
type DwellTickApplied struct {
	EventBase
	ActorID        ActorID
	ObjectID       VillageObjectID
	Source         DwellCreditSource
	Kind           ItemKind
	Attribute      NeedKey
	NeedDelta      int
	NewNeedValue   int
	RemainingTicks *int
	PeriodMinutes  int
	At             time.Time
}

func (DwellTickApplied) isSimEvent() {}

// DwellEndReason discriminates the four ways a dwell credit can
// terminate. Subscribers switch on Reason to pick the terminal
// narration ("you finish the last bite" vs "you feel full" vs "you
// walk away from your meal").
type DwellEndReason int

const (
	// DwellEndUnknown is the zero value — a defensive marker. Never
	// emitted by ApplyDwellTick today; callers reading the field can
	// trust a non-zero value.
	DwellEndUnknown DwellEndReason = iota

	// DwellEndItemExhausted — source=item credit's RemainingTicks
	// decremented to zero on the final applied tick. Item is finished.
	DwellEndItemExhausted

	// DwellEndFloorHit — the applied delta drove the actor's need
	// value to zero (pre>0, post==0). The actor is fully satisfied;
	// the credit terminates regardless of remaining ticks (parity with
	// v1's "you feel full → meal done" narration intent).
	DwellEndFloorHit

	// DwellEndWalkedAway — the actor is no longer at the pin during
	// ApplyDwellTick. The credit is abandoned without applying its
	// payoff.
	DwellEndWalkedAway

	// DwellEndCatalogUnknown — defense-in-depth: the credit's
	// Attribute is unknown to the Needs registry (catalog edit
	// post-stamp). Audit-only; no perception narration.
	DwellEndCatalogUnknown

	// DwellEndStaleAtFloor — defense-in-depth (LLM-376): an object-source
	// credit found on a dwell tick with its need already at the floor.
	// DwellEndFloorHit fires only on a preNeed>0 -> postNeed==0 transition,
	// so a credit born (or persisted in actor_dwell_credit) at the floor
	// never self-terminates and pins the actor with a permanent "you are
	// drinking … until quenched" cue. This retires it. Audit-only; no
	// perception narration — nothing was recovered, so it is not a "finish".
	DwellEndStaleAtFloor
)

// String returns the stable lowercase label for the reason — used in
// logs, telemetry, and tests.
func (r DwellEndReason) String() string {
	switch r {
	case DwellEndItemExhausted:
		return "item_exhausted"
	case DwellEndFloorHit:
		return "floor_hit"
	case DwellEndWalkedAway:
		return "walked_away"
	case DwellEndCatalogUnknown:
		return "catalog_unknown"
	case DwellEndStaleAtFloor:
		return "stale_at_floor"
	default:
		return "unknown"
	}
}

// DwellEnded fires when a dwell credit terminates for any reason. One
// event per credit per termination. Subscribers gate narration on
// Reason and render the appropriate terminal line; CatalogUnknown is
// audit-only with no narration.
type DwellEnded struct {
	EventBase
	ActorID   ActorID
	ObjectID  VillageObjectID
	Source    DwellCreditSource
	Kind      ItemKind // empty for source=object
	Attribute NeedKey
	Reason    DwellEndReason
	At        time.Time
}

func (DwellEnded) isSimEvent() {}
