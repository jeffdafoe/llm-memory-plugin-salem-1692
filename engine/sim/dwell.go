package sim

import "time"

// Dwell mechanic (ZBBS-172) — in-memory port of engine/dwell.go +
// dwell_tick.go.
//
// Sister to the one-shot ApplyConsumption / ApplyObjectRefreshAtArrival:
// where those apply an immediate need delta, dwell CREDITS the actor
// with additional recovery for time spent in place. Two sources, both
// keyed on (object, attribute, source) per-actor:
//
//   - source="object" — sitting under a tree, drinking at a well.
//     RemainingTicks=nil. The per-minute tick deletes the credit only
//     when the actor walks off the loiter pin.
//   - source="item"   — eating a meal at a structure. RemainingTicks is
//     the countdown of ticks left; the credit deletes when it hits
//     zero (meal done) or when the actor walks away (meal abandoned).
//
// OBJECT-SIDE UPSERT lives in object_refresh.go's
// ApplyObjectRefreshAtArrival. ITEM-SIDE UPSERT lives here — shared
// by consume paths (inventory.ExecuteConsume, order fulfillment,
// pay consume_now, serve.go) once those port.
//
// Both flow through ApplyDwellTick — applies dwell_delta via
// ClampNeed-bounded mutation, advances anchor by exactly the period
// (NOT to now), decrements item countdowns, deletes departed/exhausted.

// ItemSatisfaction is one entry from the item_satisfies catalog — the
// effect of consuming a unit of an item on one need.
//
// Immediate and DwellAmount are both POSITIVE magnitudes (matches legacy
// item_satisfies column convention — `amount` for immediate, `dwell_amount`
// for per-tick). Consume negates Immediate to subtract from Needs;
// UpsertItemDwellCredits negates DwellAmount on the credit row.
//
// Immediate = 0 with a complete dwell triple is legal (rare — a pure
// slow-burn item). HasDwell() requires all three dwell fields > 0; the
// upsert silent-skips entries that fail HasDwell.
type ItemSatisfaction struct {
	Attribute          NeedKey
	Immediate          int // positive magnitude; 0 = no immediate hit
	DwellAmount        int // positive magnitude; 0 = no dwell effect
	DwellPeriodMinutes int // 0 = no dwell effect
	DwellTotalTicks    int // 0 = no dwell effect
}

// HasDwell reports whether this satisfaction has a complete dwell
// triple — all three of DwellAmount, DwellPeriodMinutes, DwellTotalTicks
// must be positive for a dwell credit to land.
func (s ItemSatisfaction) HasDwell() bool {
	return s.DwellAmount > 0 && s.DwellPeriodMinutes > 0 && s.DwellTotalTicks > 0
}

// HasActiveItemDwell reports whether the credits map holds a live item-source
// dwell credit — i.e. the actor is mid-meal/mid-drink, finishing a consumed item
// whose slow-burn eases a need only while it stays put. Exhausted credits are
// deleted by ApplyDwellTick, so a present item credit is by definition still
// active. Shared by the engine shift-duty producer (shiftDutyTarget) and the
// perception wind-down cue (buildDutySteer) so both suppress the off-shift "head
// home" duty while a meal is in progress — the meal is finite and both producers
// are level-triggered, so the duty re-fires once it ends. ZBBS-WORK-386.
func HasActiveItemDwell(credits map[DwellCreditKey]*DwellCredit) bool {
	for k := range credits {
		if k.Source == DwellSourceItem {
			return true
		}
	}
	return false
}

// UpsertItemDwellCredits stamps source="item" dwell credit rows on the
// actor for any satisfaction with a complete dwell triple, pinned to
// the supplied structureID. Returns the list of credits that were
// stamped (or refreshed) so callers can emit a DwellStarted event off
// the same write — empty when nothing landed (no dwell triples in the
// satisfactions, empty structureID, or nil actor).
//
// kind labels every stamped credit so perception ("you are eating
// stew") and downstream events can identify the meal without a separate
// catalog lookup. Pass the ItemKind whose Satisfies slice is being
// applied; an empty kind is allowed but yields generic narration.
//
// Empty structureID is a silent skip — eating-while-walking gets only
// the immediate hit, not the per-tick payoff.
//
// On re-consume of the same item at the same structure (eating a
// second bowl of stew while the first is still credited), the existing
// row's LastCreditedAt is reset to now and RemainingTicks resets to
// DwellTotalTicks — a fresh meal restarts the timer rather than
// stacking. Stacking would let an actor double-up by paying twice in
// quick succession.
func UpsertItemDwellCredits(actor *Actor, kind ItemKind, satisfactions []ItemSatisfaction, structureID VillageObjectID, now time.Time) []DwellCreditSnapshot {
	if actor == nil || structureID == "" {
		return nil
	}
	if actor.DwellCredits == nil {
		actor.DwellCredits = make(map[DwellCreditKey]*DwellCredit)
	}
	var stamped []DwellCreditSnapshot
	for _, s := range satisfactions {
		if !s.HasDwell() {
			continue
		}
		totalTicks := s.DwellTotalTicks
		actor.DwellCredits[DwellCreditKey{
			ObjectID:  structureID,
			Attribute: s.Attribute,
			Source:    DwellSourceItem,
		}] = &DwellCredit{
			ObjectID:           structureID,
			Kind:               kind,
			Attribute:          s.Attribute,
			Source:             DwellSourceItem,
			LastCreditedAt:     now,
			RemainingTicks:     &totalTicks,
			DwellDelta:         -s.DwellAmount,
			DwellPeriodMinutes: s.DwellPeriodMinutes,
		}
		ticks := totalTicks
		stamped = append(stamped, DwellCreditSnapshot{
			Attribute:      s.Attribute,
			DwellDelta:     -s.DwellAmount,
			PeriodMinutes:  s.DwellPeriodMinutes,
			RemainingTicks: &ticks,
		})
	}
	return stamped
}

// DwellCompletionNarration returns the felt-language line for a dwell
// completion event. Precedence: item-exhausted → floor-hit → walked-
// away. Returns "" for unhandled combinations — callers silently skip
// the broadcast.
//
// Vocabulary for exhausted/floor-hit mirrors legacy
// dwellCompletionNarration. Walked-away is v2-new — v1 never narrated
// abandoned meals because the credit was deleted silently; v2 promotes
// the abandonment to a DwellEnded event so the LLM can perceive its own
// abandonment ("you walk away from your meal").
func DwellCompletionNarration(attribute NeedKey, source DwellCreditSource, itemExhausted, floorHit, walkedAway bool) string {
	if itemExhausted {
		switch attribute {
		case "hunger":
			return "You finish the last bite, satisfied."
		case "thirst":
			return "You drain the last drop."
		case "tiredness":
			return "You feel a little less tired than before."
		default:
			return "You finish what you had."
		}
	}
	if floorHit {
		switch attribute {
		case "hunger":
			return "You feel full."
		case "thirst":
			return "Your thirst is quenched."
		case "tiredness":
			return "You feel rested."
		}
	}
	if walkedAway {
		switch attribute {
		case "hunger":
			if source == DwellSourceItem {
				return "You walk away from your meal, leaving it half-eaten."
			}
		case "thirst":
			if source == DwellSourceItem {
				return "You walk away from your drink."
			}
		case "tiredness":
			return "You stop resting and move on."
		}
	}
	return ""
}

// DwellTickNarration returns the per-tick felt-language line for an
// applied dwell credit ("you eat — the gnawing ebbs"). Attribute +
// source keyed; no item-Kind variation — v1's per-tick payoff narration
// didn't differentiate per item either. Returns "" for unhandled
// combinations.
func DwellTickNarration(attribute NeedKey, source DwellCreditSource) string {
	if source == DwellSourceItem {
		switch attribute {
		case "hunger":
			return "You take another bite, the gnawing ebbs."
		case "thirst":
			return "You drink; the dryness fades."
		case "tiredness":
			return "You rest a moment; the weariness eases."
		}
		return ""
	}
	if source == DwellSourceObject {
		switch attribute {
		case "hunger":
			return "You pick at what's here; the gnawing eases."
		case "thirst":
			return "You sip from the source; the dryness fades."
		case "tiredness":
			return "You linger here; the weariness eases."
		}
	}
	return ""
}

// itemNeedEaseFragment is the shared felt-language clause for a need easing
// from an item-sourced beat ("the gnawing ebbs"). Single source for the
// immediate consume line (ConsumeNarration) so it reads consistently with the
// item branch of DwellTickNarration above (which uses the same phrasing).
// Returns "" for an unhandled attribute.
func itemNeedEaseFragment(attribute NeedKey) string {
	switch attribute {
	case "hunger":
		return "the gnawing ebbs"
	case "thirst":
		return "the dryness fades"
	case "tiredness":
		return "the weariness eases"
	}
	return ""
}

// consumeVerb picks the second-person verb for the consume beat by the need
// the item primarily eased: eat for hunger, drink for thirst, take for
// anything else (a tiredness remedy like coca tea, or an unhandled need).
func consumeVerb(attribute NeedKey) string {
	switch attribute {
	case "hunger":
		return "eat"
	case "thirst":
		return "drink"
	default:
		return "take"
	}
}

// consumeNeedOrder is the stable tiebreak order when several needs moved on one
// consume — the canonical need ordering, so primaryEasedNeed is deterministic
// regardless of the Applied map's iteration order.
var consumeNeedOrder = []NeedKey{"hunger", "thirst", "tiredness"}

// primaryEasedNeed returns the need a consume eased most (largest Applied
// magnitude; ties broken by consumeNeedOrder). applied carries POSITIVE
// reduction magnitudes (pre-post) for needs that actually moved. Returns "" on
// an empty map.
func primaryEasedNeed(applied map[NeedKey]int) NeedKey {
	best := NeedKey("")
	bestVal := 0
	bestRank := len(consumeNeedOrder) + 1
	for attr, v := range applied {
		if v <= 0 {
			continue
		}
		rank := len(consumeNeedOrder)
		for i, k := range consumeNeedOrder {
			if k == attr {
				rank = i
				break
			}
		}
		if v > bestVal || (v == bestVal && rank < bestRank) {
			best, bestVal, bestRank = attr, v, rank
		}
	}
	return best
}

// ConsumeNarration returns the immediate second-person felt-language beat for
// an actor consuming an item that actually moved a need ("You eat the bread;
// the gnawing ebbs."). The v2 port of v1's narrateConsumeSelf. Composed from
// the item Kind and the primary need that dropped, reusing the dwell ease-
// fragment vocab so the immediate beat and the per-tick dwell payoff read
// consistently. Returns "" when no handled need moved (the caller gates on
// len(applied) > 0; an unhandled need still yields "" → no beat).
func ConsumeNarration(kind ItemKind, applied map[NeedKey]int) string {
	attr := primaryEasedNeed(applied)
	frag := itemNeedEaseFragment(attr)
	if frag == "" {
		return ""
	}
	verb := consumeVerb(attr)
	if kind == "" {
		return "You " + verb + "; " + frag + "."
	}
	return "You " + verb + " the " + string(kind) + "; " + frag + "."
}
