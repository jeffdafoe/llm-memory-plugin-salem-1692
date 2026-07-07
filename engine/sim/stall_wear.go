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

// OwnedWearableStall returns the wearable business owned by sellerID, or nil when
// the seller owns none. Takes the object map so it serves both the live World
// (w.VillageObjects) and a perception Snapshot (snap.VillageObjects).
//
// ASSUMES one wearable business per owner (data convention): the first match
// wins, so with two owned businesses the result is map-iteration-arbitrary. This
// convention predates LLM-247 (it held trivially when scope was market stalls)
// and still holds — every live business has a distinct owner. The accrual seam,
// the degrade sale-block, and the repair cue all resolve the owner's business
// through here, so if an owner is ever given a second wearable business, those
// paths need to key off the sale/stand-at location instead. See the codebase note
// [[shared/notes/codebase/salem-engine-v2/stall-wear-repair]].
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

// WearableStallToMend returns the wearable business the actor is responsible for
// mending, and whether they reach it through a hire rather than ownership
// (LLM-271). The owner's own business wins first; failing that, a worker actively
// Working a hired job mends the business their EMPLOYER owns — the hired hand at
// the worn stall may lift the hammer too, not just the owner. Only the Working
// state qualifies: an EnRoute worker hasn't reached the post yet (they get the
// relocation cue instead), and a Pending offer isn't a hire. Shared by the
// perception cue (snap.LaborLedger) and the StartRepair command (w.LaborLedger) so
// the two can't drift on who may mend. hired is always false when stall is nil.
//
// A worker holds at most one live job (AcceptWork forbids double-booking), so this
// scan normally finds one match — but it picks the LOWEST LaborID rather than the
// map-iteration-first, so the result is deterministic even if that invariant is
// ever broken (a bad migration, overlapping test setup). Determinism is load-bearing
// here: the cue, StartRepair, and the completion sweep all call this, and two of
// them resolving different employer stalls would advertise one and mend another.
// Mirrors the lowest-LaborID tie-break in laboringOfferFor / workerPendingLaborOffer.
func WearableStallToMend(objects map[VillageObjectID]*VillageObject, ledger map[LaborID]*LaborOffer, actorID ActorID) (stall *VillageObject, hired bool) {
	if own := OwnedWearableStall(objects, actorID); own != nil {
		return own, false
	}
	var best *LaborOffer
	for _, o := range ledger {
		if o == nil || o.State != LaborStateWorking || o.WorkerID != actorID {
			continue
		}
		if best == nil || o.ID < best.ID {
			best = o
		}
	}
	if best == nil {
		return nil, false
	}
	if employerStall := OwnedWearableStall(objects, best.EmployerID); employerStall != nil {
		return employerStall, true
	}
	return nil, false
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

// AtBusiness reports whether an actor is co-located with a business for the
// wear/repair surfaces — the shared location predicate so the "## Your business"
// cue, the co-present condition line, and the StartRepair command can't drift on
// what "at the business" means (the location twin of StallRepairable's wear
// predicate). True when the actor stands INSIDE the business structure
// (structure-backed businesses share their id with the village_object, and
// keepers work indoors — LLM-266) OR at its outdoor loiter pin (the v1
// open-market-stall case, where a keeper stands at the pin, not inside). The
// caller resolves and passes the loiter pin — perception via objectLoiterPin, the
// command via effectiveObjectLoiterTile — passing pinValid=false when none
// resolves, so an inside keeper still passes.
//
// This decides LOCATION only: it trusts the caller to have already validated that
// businessID refers to the relevant wearable business (all current callers resolve
// it through OwnedWearableStall / IsWearableStall). It does not itself re-check
// business-hood, so don't hand it an arbitrary object id and read the result as
// "that object is a business."
func AtBusiness(actorPos TilePos, insideStructureID StructureID, businessID VillageObjectID, pin TilePos, pinValid bool) bool {
	if insideStructureID != "" && string(insideStructureID) == string(businessID) {
		return true
	}
	return pinValid && actorPos.Chebyshev(pin) <= LoiterAttributionTiles
}

// ownerStallDegraded reports whether the actor owns a market stall worn past the
// degrade threshold — shut for restock/production until mended (LLM-118, LLM-304).
// The refill-blocking gate: it suppresses the produce tick (produce_tick.go) and
// the restock warrant (restock_tick.go) and freezes further wear accrual
// (accrueStallWear). Selling from remaining stock is NOT gated — a degraded shop
// draws down what's on hand and reopens its refill on repair. (LLM-304 replaced the
// original LLM-118 sale-block, which trapped a broke keeper who could no longer earn
// the coin to buy the nails.) nil-safe: an actor who owns no stall is never degraded.
func ownerStallDegraded(w *World, actorID ActorID) bool {
	if w == nil {
		return false
	}
	return StallDegraded(OwnedWearableStall(w.VillageObjects, actorID), w.Settings.StallWearDegradeThreshold)
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

// StallRepairHiredWarrantReason is the hired-worker twin of StallRepairWarrantReason
// (LLM-271). It is stamped on a worker the moment they start a hired job
// (startLaborWork) at an employer whose business is ALREADY worn to the repair
// threshold — so unlike the owner's warrant there is no wear-crossing edge to ride
// (the stall wore before the hire). It carries its OWN kind for two reasons: the
// reactor's laboring shelve-gate singles it out as an interrupt (a StateLaboring
// worker is otherwise shelved — hasHiredRepairWarrant), and the warrant line renders
// hired-framed ("the business you're working at") rather than the owner's "your
// business." StallID is the employer's worn business. DedupDiscriminator returns 0,
// matching the owner's warrant — it's a state condition, not an event.
type StallRepairHiredWarrantReason struct {
	StallID VillageObjectID
}

func (StallRepairHiredWarrantReason) isWarrantReason()           {}
func (StallRepairHiredWarrantReason) Kind() WarrantKind          { return WarrantKindStallRepairHired }
func (StallRepairHiredWarrantReason) DedupDiscriminator() uint64 { return 0 }

// maybeStampHiredRepairWarrant wakes a just-hired worker to mend their employer's
// business when it is already worn (LLM-271) — the hired twin of accrueStallWear's
// owner wake. Called from startLaborWork once the worker is on-post. A no-op unless
// the employer owns a wearable business worn to the repair threshold (or degraded).
// One-shot, like the owner's edge-triggered warrant: the worker is woken at the
// moment work begins, hammer-ready at the post; if they decline to mend, the engine
// does not nag — the standing "## The business you're working at" cue still reminds
// them on any later tick they draw. World-goroutine-only.
func maybeStampHiredRepairWarrant(w *World, worker, employer *Actor, at time.Time) {
	if w == nil || worker == nil || employer == nil {
		return
	}
	stall := OwnedWearableStall(w.VillageObjects, employer.ID)
	if !StallRepairable(stall, w.Settings.StallWearRepairThreshold, w.Settings.StallWearDegradeThreshold) {
		return
	}
	tryStampWarrant(w, worker, WarrantMeta{
		TriggerActorID: worker.ID,
		Reason:         StallRepairHiredWarrantReason{StallID: stall.ID},
	}, at)
}

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
	// LLM-304: a degraded stall is shut for restock/production, so it draws down
	// toward empty rather than refilling — freeze its wear here so continued
	// sell-down of remaining stock doesn't pile wear on past the degrade line.
	// Repair zeroes Wear regardless; this just keeps the number stable at degrade.
	if StallDegraded(stall, w.Settings.StallWearDegradeThreshold) {
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
		text = fmt.Sprintf("The %s here is battered and badly in need of repair.", name)
	}
	w.emit(&StallConditionNarrated{
		ActorID:     actor.ID,
		StructureID: conversationalScopeStructure(w, actor),
		Text:        text,
		At:          now,
	})
}
