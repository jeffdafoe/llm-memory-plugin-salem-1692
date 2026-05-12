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
// DwellAmount is POSITIVE magnitude (matches legacy item_satisfies
// column convention); UpsertItemDwellCredits flips it to a negative
// delta on the credit row.
type ItemSatisfaction struct {
	Attribute          NeedKey
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

// UpsertItemDwellCredits stamps source="item" dwell credit rows on the
// actor for any satisfaction with a complete dwell triple, pinned to
// the supplied structureID. Empty structureID is a silent skip —
// eating-while-walking gets only the immediate hit, not the per-tick
// payoff.
//
// On re-consume of the same item at the same structure (eating a
// second bowl of stew while the first is still credited), the existing
// row's LastCreditedAt is reset to now and RemainingTicks resets to
// DwellTotalTicks — a fresh meal restarts the timer rather than
// stacking. Stacking would let an actor double-up by paying twice in
// quick succession.
func UpsertItemDwellCredits(actor *Actor, satisfactions []ItemSatisfaction, structureID VillageObjectID, now time.Time) {
	if actor == nil || structureID == "" {
		return
	}
	if actor.DwellCredits == nil {
		actor.DwellCredits = make(map[DwellCreditKey]*DwellCredit)
	}
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
			Attribute:          s.Attribute,
			Source:             DwellSourceItem,
			LastCreditedAt:     now,
			RemainingTicks:     &totalTicks,
			DwellDelta:         -s.DwellAmount,
			DwellPeriodMinutes: s.DwellPeriodMinutes,
		}
	}
}

// DwellCompletionNarration returns the felt-language line for a dwell
// completion event. Item-exhausted takes precedence over floor-hit
// (more specific). Returns "" for unhandled combinations — callers
// silently skip the broadcast.
//
// Vocabulary mirrors legacy dwellCompletionNarration.
func DwellCompletionNarration(attribute NeedKey, source DwellCreditSource, itemExhausted, floorHit bool) string {
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
	return ""
}
