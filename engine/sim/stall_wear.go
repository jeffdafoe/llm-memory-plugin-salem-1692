package sim

import (
	"fmt"
	"math"
	"time"
)

// stall_wear.go — LLM-118, generalized in LLM-247. Every owned business (tavern,
// farms, shops, smithy — not just market stalls) accrues Wear as it takes in
// coin and must be repaired (consuming nails) before it degrades and closes for
// trade. This file holds the domain defaults + helpers; the accrual seam is
// commitPayTransfer (pay_with_item_commands.go), the warrant is
// WarrantKindStallRepair (reactor.go), the repair action rides the
// SourceActivity substrate, and the perception cues live under perception/.
// The Stall* identifiers are kept from LLM-118 (they name persisted checkpoint
// columns + the live umbilical knob contract); the scope is now business-wide.

// Default WorldSettings knobs for business wear (LLM-247 recalibration). Tuned
// live via the umbilical. WearPerCoin=1 makes the meter read "wear == coins the
// owner has taken in since the last repair." Calibrated to observed velocity —
// the busiest business earns on the order of ~50 coins/week — so a repair
// threshold of 60 fires roughly weekly on the top earner and rarely on slow
// ones; degrade sits half again higher, a long cued runway past the first nudge.
const (
	DefaultStallWearPerCoin           = 1
	DefaultStallWearRepairThreshold   = 60
	DefaultStallWearDegradeThreshold  = 90
	DefaultStallNailsPerRepair        = 5
	DefaultStallRepairDurationSeconds = 90
)

// TagMarketStall marks a market-stall instance in the tag vocabulary (applied by
// an operator via the editor / umbilical /object/add-tag). As of LLM-247 it is
// NO LONGER the wear/repair gate — accrual widened to every owned business
// (TagBusiness). Kept as a descriptive type tag.
const TagMarketStall = "market_stall"

// NailItemKind is the canonical item a repair consumes — the smith's nail
// (seeded in the item catalog; produced by Ezekiel, LLM-116). Bought from the
// smith and spent mending a stall: the demand half of the nail loop.
const NailItemKind ItemKind = "nail"

// IsWearableStall reports whether obj is an owned business — the scope gate for
// all wear accrual, the repair tool, and the degrade block. Despite the name
// (kept from LLM-118), the gate is the TagBusiness tag, not market_stall
// (widened in LLM-247): every owned business wears — tavern, farms, shops,
// smithy — not just stalls. An object wears only when it is tagged TagBusiness
// AND carries an owner (the owner perceives the need and performs the repair; an
// unowned business never wears). Nil-safe.
func IsWearableStall(obj *VillageObject) bool {
	return obj != nil && obj.OwnerActorID != "" && obj.HasTag(TagBusiness)
}

// OwnedWearableStall returns the market stall owned by sellerID, or nil when the
// seller owns none. Takes the object map so it serves both the live World
// (w.VillageObjects) and a perception Snapshot (snap.VillageObjects). A vendor
// owns at most one stall by data convention; the first match wins.
func OwnedWearableStall(objects map[VillageObjectID]*VillageObject, sellerID ActorID) *VillageObject {
	if sellerID == "" {
		return nil
	}
	for _, obj := range objects {
		if obj.OwnerActorID == sellerID && IsWearableStall(obj) {
			return obj
		}
	}
	return nil
}

// StallNeedsRepair reports whether a wearable stall has worn to or past the
// repair threshold. A non-positive threshold disables the transition.
func StallNeedsRepair(obj *VillageObject, repairThreshold int) bool {
	return IsWearableStall(obj) && repairThreshold > 0 && obj.Wear >= repairThreshold
}

// StallDegraded reports whether a wearable stall has worn to or past the degrade
// threshold — the point at which it closes for trade until mended. A
// non-positive threshold disables the transition (never degrades).
func StallDegraded(obj *VillageObject, degradeThreshold int) bool {
	return IsWearableStall(obj) && degradeThreshold > 0 && obj.Wear >= degradeThreshold
}

// StallRepairable reports whether a wearable stall is in a state its owner can
// (and should) mend — worn to the repair threshold OR already degraded. The
// OR-degraded clause guards against a threshold MISCONFIGURATION (degrade set
// below repair, or repair disabled while degrade is on): a degraded stall is
// always repairable, even if it never crossed the repair line, so a bad config
// can't brick a stall (degraded → sales blocked → wear can never climb to the
// repair threshold). The repair tool, its cue, and the StartRepair gate all key
// off this so the cue and the command can't drift.
func StallRepairable(obj *VillageObject, repairThreshold, degradeThreshold int) bool {
	return StallNeedsRepair(obj, repairThreshold) || StallDegraded(obj, degradeThreshold)
}

// sellerStallDegraded reports whether the seller owns a market stall worn past
// the degrade threshold — closed for trade until mended (LLM-118). The
// sale-blocking gate at quote-post, fast-path take, and slow accept. nil-safe: a
// seller who owns no stall is never degraded.
func sellerStallDegraded(w *World, sellerID ActorID) bool {
	if w == nil {
		return false
	}
	return StallDegraded(OwnedWearableStall(w.VillageObjects, sellerID), w.Settings.StallWearDegradeThreshold)
}

// StallRepairWarrantReason is stamped on a stall owner when their stall's wear
// crosses the repair threshold (edge-triggered at the accrual in
// commitPayTransfer). StallID is the worn stall — carried for telemetry / admin
// replay and so the cue can name the precise object; the deliberation reads the
// live stall + the owner's nail count from perception. DedupDiscriminator
// returns 0: a wear-threshold crossing is a state condition, not an event, so it
// bypasses the substrate's source-key dedup paths — mirrors
// NeedThresholdWarrantReason. The crossing is self-limiting (it fires only on
// the before<threshold && after>=threshold transition), and repair resets wear
// to 0, which re-arms it.
type StallRepairWarrantReason struct {
	StallID VillageObjectID
}

func (StallRepairWarrantReason) isWarrantReason()           {}
func (StallRepairWarrantReason) Kind() WarrantKind          { return WarrantKindStallRepair }
func (StallRepairWarrantReason) DedupDiscriminator() uint64 { return 0 }

// accrueStallWear adds usage-weighted wear to the seller's owned stall on a
// completed sale and, on the upward crossing of the repair threshold, wakes the
// owner to mend it. Called from commitPayTransfer — the single coin-transfer
// chokepoint — so every accepted sale (slow accept, fast quote-take, bundle,
// eat-here) accrues. A seller who owns no market stall, a zero amount, or
// StallWearPerCoin==0 is a no-op (idle stalls never wear; the off-switch
// disables the feature entirely).
//
// The warrant is edge-triggered: stamped only on the before<threshold &&
// after>=threshold transition, so a stall already past the threshold doesn't
// re-stamp every sale. Repair resets Wear to 0, which re-arms the edge. The
// standing arrival cue (perception) keeps reminding the owner after the one-shot
// warrant is consumed, so an ignored warrant doesn't go silent.
func accrueStallWear(w *World, seller *Actor, amount int, at time.Time) {
	if w == nil || seller == nil || amount <= 0 || w.Settings.StallWearPerCoin <= 0 {
		return
	}
	stall := OwnedWearableStall(w.VillageObjects, seller.ID)
	if stall == nil {
		return
	}
	before := stall.Wear
	// int64 saturating add: a large sale amount × StallWearPerCoin (or accrual
	// over a long-lived stall) must never wrap an int negative — that could lower
	// Wear, skip a threshold, or silently un-degrade a stall across a checkpoint.
	delta := int64(amount) * int64(w.Settings.StallWearPerCoin)
	if delta <= 0 {
		return // 0 (guarded above) or an overflowed product — nothing safe to add
	}
	// Saturate (don't wrap) if the add would exceed int range. Headroom is
	// computed as MaxInt − Wear (Wear >= 0, so this never overflows int64), which
	// keeps the comparison correct even for an absurdly large existing Wear.
	if delta > int64(math.MaxInt)-int64(stall.Wear) {
		stall.Wear = math.MaxInt
	} else {
		stall.Wear = int(int64(stall.Wear) + delta)
	}

	threshold := w.Settings.StallWearRepairThreshold
	if threshold > 0 && before < threshold && stall.Wear >= threshold {
		tryStampWarrant(w, seller, WarrantMeta{
			TriggerActorID: seller.ID,
			Reason:         StallRepairWarrantReason{StallID: stall.ID},
		}, at)
	}
}

// StallConditionNarrated is a WIRE-ONLY event (no engine subscriber) carrying the
// PC talk-box atmosphere line for arriving at a worn market stall (LLM-118) — the
// player's twin of the co-present NPC perception cue (StallConditionView).
// emitStallConditionNarration emits it only when a PC arrives at a worn stall;
// TranslateEvent maps it to a PRIVATE room_event the talk panel renders as a
// second-person narration line addressed to that PC (no speaker), the same
// carrier sleep / lodging narrations use.
type StallConditionNarrated struct {
	EventBase
	ActorID     ActorID
	StructureID StructureID
	Text        string
	At          time.Time
}

func (StallConditionNarrated) isSimEvent() {}

// arrivalStall resolves the worn-or-not market stall a just-arrived actor landed
// at — the arrival's destination object, else the stall whose loiter pin the
// actor now stands at — or nil when they didn't arrive at a wearable stall.
func arrivalStall(w *World, actor *Actor, arrivedEvt *ActorArrived) *VillageObject {
	if arrivedEvt != nil && arrivedEvt.DestObjectID != "" {
		if o := w.VillageObjects[arrivedEvt.DestObjectID]; IsWearableStall(o) {
			return o
		}
	}
	if objID, ok := resolveLoiteringObject(w, actor.Pos, LoiterAttributionTiles); ok {
		if o := w.VillageObjects[objID]; IsWearableStall(o) {
			return o
		}
	}
	return nil
}

// businessDisplayName resolves the human label for a worn business — the
// co-located structure's name (structures share the object's id) first, else the
// object's own DisplayName, else "" for the caller to fall back to a generic
// noun. The sim-side twin of perception.resolveDwellPinLabel.
func businessDisplayName(w *World, obj *VillageObject) string {
	if w == nil || obj == nil {
		return ""
	}
	if st := w.Structures[StructureID(obj.ID)]; st != nil && st.DisplayName != "" {
		return st.DisplayName
	}
	return obj.DisplayName
}

// emitStallConditionNarration surfaces a worn business to a PC who just walked up
// to it, as a private talk-box atmosphere line (LLM-118, generalized LLM-247).
// PC-only: NPCs get the pull-side perception cue (StallConditionView) instead.
// Fires once per arrival (a discrete event), so it can't spam; a fresh wear state
// is reflected each time the player returns.
func emitStallConditionNarration(w *World, actor *Actor, arrivedEvt *ActorArrived, now time.Time) {
	if w == nil || actor == nil || actor.Kind != KindPC {
		return
	}
	stall := arrivalStall(w, actor, arrivedEvt)
	if !StallNeedsRepair(stall, w.Settings.StallWearRepairThreshold) {
		return
	}
	name := businessDisplayName(w, stall)
	if name == "" {
		name = "business"
	}
	text := fmt.Sprintf("The %s here looks worn and run-down from hard use.", name)
	if StallDegraded(stall, w.Settings.StallWearDegradeThreshold) {
		text = fmt.Sprintf("The %s here is battered and clearly unfit for trade.", name)
	}
	w.emit(&StallConditionNarrated{
		ActorID:     actor.ID,
		StructureID: conversationalScopeStructure(w, actor),
		Text:        text,
		At:          now,
	})
}
