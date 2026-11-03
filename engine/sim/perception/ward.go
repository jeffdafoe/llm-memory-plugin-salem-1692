package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ward.go — LLM-897. A household's want for a ward after an omen, as two views
// under one "## A ward for the house" heading:
//
//   - WardWantView: the felt line and the buy errand (a horseshoe from the
//     smith), for a resident of a household that wants a ward and holds none.
//   - WardPlaceView: the place choice, for a resident inside that home with a
//     horseshoe in the pack. Its presence is the SINGLE gate on the place_ward
//     tool (handlers/tool_gating.go), so cue and tool cannot drift; the sim
//     command PlaceHomeWard re-checks the same conditions.
//
// A household wants a ward while the latest omen that happened to any of its
// residents is inside the want window (sim.OmenWardWantWindow) and its house
// carries no ward. One ward is enough (D2 on LLM-897): the want ends the moment
// any ward is up. The buy errand also ends, for the whole household, while any
// resident holds a horseshoe or has an offer standing for one, so no NEW buy cue
// sends a second person out. It cannot cancel a purchase already in flight:
// two housemates whose turns read the cue before either offer existed can each
// buy one, and the spare stays in a pack (an accepted limit on LLM-897; a
// household reservation would have to live in the pay_with_item money path).
// The game never says what a ward does.

// wardWant is a household's live want for a ward: what went wrong (a clause, ""
// when the omen predates the kind record) and the household's residents.
type wardWant struct {
	Clause  string
	Members []sim.ActorID
}

// WardWantView is the felt line and the buy errand. Non-nil only with an
// actionable buy path (a co-present seller or a walk-to supplier).
type WardWantView struct {
	Clause          string            // "the milk turned sour in the pail"; "" → the plain line
	Item            sim.ItemKind      // the good to buy (sim.HorseshoeKind)
	Vendors         []RestockVendor   // where to buy it while away — the move_to destination(s)
	CoPresentSeller string            // a seller sharing the actor's huddle right now; "" when none
	SellerStock     int               // what CoPresentSeller holds (0 when none present)
	Block           copresentBuyBlock // goad or hold off, from classifyCoPresentBuy
}

// WardPlaceView is the place choice: the ward goods the actor holds that the
// house does not carry, in sim.VillagerWardKinds order.
type WardPlaceView struct {
	Clause string
	Items  []sim.ItemKind
}

// wardResident reports whether a is a resident villager: an NPC with a home,
// not a visitor and not a player (players place wards from Your Pack).
func wardResident(a *sim.ActorSnapshot) bool {
	if a == nil || a.VisitorState != nil || a.HomeStructureID == "" {
		return false
	}
	return a.Kind == sim.KindNPCStateful || a.Kind == sim.KindNPCShared
}

// buildHouseholdWardWant returns the subject's household's live want, or nil:
// the house exists and carries no ward, and some resident's latest omen happened
// inside the want window. The clause is the most recent such omen's.
func buildHouseholdWardWant(snap *sim.Snapshot, a *sim.ActorSnapshot) *wardWant {
	if snap == nil || !wardResident(a) {
		return nil
	}
	home := a.HomeStructureID
	house := snap.VillageObjects[sim.VillageObjectID(home)]
	if house == nil || sim.HomeWarded(house) {
		return nil
	}
	want := &wardWant{}
	for id, m := range snap.Actors {
		if wardResident(m) && m.HomeStructureID == home {
			want.Members = append(want.Members, id)
		}
	}
	// The same core PlaceHomeWard reads (tool-cue lockstep).
	latestID, ok := sim.LatestHouseholdOmen(snap.Environment.Omens.LastAt, want.Members, snap.PublishedAt,
		sim.OmenWardWantWindow(snap.OmenWardWantDays))
	if !ok {
		return nil
	}
	want.Clause = sim.OmenHouseholdClause(snap.Environment.Omens.LastKind[latestID])
	return want
}

// householdHoldsOrSeeksWard reports whether any resident of the household holds
// a villager ward good or has a buy offer standing for one.
func householdHoldsOrSeeksWard(snap *sim.Snapshot, members []sim.ActorID) bool {
	for _, id := range members {
		m := snap.Actors[id]
		if m == nil {
			continue
		}
		for _, k := range sim.VillagerWardKinds {
			if m.Inventory[k] > 0 {
				return true
			}
		}
	}
	now := snap.PublishedAt
	for _, e := range snap.PayLedger {
		if e == nil || e.State != sim.PayLedgerStatePending || !sim.IsVillagerWardKind(e.ItemKind) {
			continue
		}
		if !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt) {
			continue
		}
		for _, id := range members {
			if e.BuyerID == id {
				return true
			}
		}
	}
	return false
}

// buildWardWant returns the buy errand, or nil. Off-shift only: a keeper on
// shift belongs at the post, and the duty steer is not argued with. Nil under a
// pressing need (see to that first), asleep, or with no actionable buy path
// (the LLM-216 dead-end drop the other buy errands make).
func buildWardWant(snap *sim.Snapshot, actorID sim.ActorID, a *sim.ActorSnapshot, want *wardWant) *WardWantView {
	if want == nil || a == nil || a.State == sim.StateSleeping {
		return nil
	}
	if subjectOnShift(snap, a) || hasRedNeed(a, snap) {
		return nil
	}
	if householdHoldsOrSeeksWard(snap, want.Members) {
		return nil
	}
	item := sim.VillagerWardKinds[0]
	coName, coID := coPresentSellerForItem(snap, actorID, a, item)
	vendors, _ := findItemVendors(snap, actorID, a, item)
	if coName == "" && len(vendors) == 0 {
		return nil
	}
	view := &WardWantView{Clause: want.Clause, Item: item, Vendors: vendors, CoPresentSeller: coName}
	if coID != "" {
		view.SellerStock, view.Block = classifyCoPresentBuy(snap, actorID, a, coID, item)
	}
	return view
}

// buildWardPlaceChoice returns the place choice, or nil: an awake resident,
// inside its own home, not walking and not mid-activity, holding a ward good the
// house does not carry. Mirrors sim.PlaceHomeWard's checks.
func buildWardPlaceChoice(a *sim.ActorSnapshot, want *wardWant, snap *sim.Snapshot) *WardPlaceView {
	if want == nil || a == nil || a.State == sim.StateSleeping {
		return nil
	}
	if a.InsideStructureID != a.HomeStructureID || a.MoveDestKind != "" || actorMidSourceActivity(a) {
		return nil
	}
	house := snap.VillageObjects[sim.VillageObjectID(a.HomeStructureID)]
	if house == nil {
		return nil
	}
	var items []sim.ItemKind
	for _, k := range sim.VillagerWardKinds {
		if a.Inventory[k] > 0 && !house.HasTag(sim.HomeWardTag(k)) {
			items = append(items, k)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &WardPlaceView{Clause: want.Clause, Items: items}
}

// renderWardSince writes the opening clause both views share.
func renderWardSince(b *strings.Builder, clause string) {
	if clause != "" {
		fmt.Fprintf(b, "Since %s, there has been talk in your household of a horseshoe over the door. ", clause)
		return
	}
	b.WriteString("Since the trouble at home this week, there has been talk in your household of a horseshoe over the door. ")
}

// renderWard writes "## A ward for the house". The place choice wins when both
// would show (it cannot: holding the good ends the buy errand), so the section
// carries at most one action.
func renderWard(b *strings.Builder, want *WardWantView, place *WardPlaceView) {
	switch {
	case place != nil:
		b.WriteString("## A ward for the house\n")
		renderWardSince(b, place.Clause)
		item := place.Items[0]
		fmt.Fprintf(b, "You have a %s with you, and you are at home. You could nail it up over the door now — call place_ward with item \"%s\".\n\n",
			sanitizeInline(string(item)), sanitizeInline(string(item)))
	case want != nil:
		b.WriteString("## A ward for the house\n")
		renderWardSince(b, want.Clause)
		label := string(want.Item)
		if want.CoPresentSeller != "" {
			switch want.Block {
			case copresentBuyBlockedNoStock, copresentBuyBlockedCoin, copresentBuyBlockedTerms:
				renderCoPresentBuySoften(b, want.CoPresentSeller, label+"s", want.Block)
			default:
				renderCoPresentBuy(b, want.CoPresentSeller, label+"s", want.Item, 1)
			}
			b.WriteString("\n")
			return
		}
		b.WriteString("You could buy one and put it up at home. When it suits you, use move_to to reach a seller, then pay_with_item once you arrive:\n")
		renderWalkToVendors(b, want.Vendors)
		b.WriteString("\n")
	}
}
