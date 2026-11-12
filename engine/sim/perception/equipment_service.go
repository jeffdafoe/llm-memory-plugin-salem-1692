package perception

import (
	"fmt"
	"math"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// equipment_service.go — LLM-648 perception, both sides of the wright's trade.
//
// OWNER side ("## Your equipment"): a keeper whose owned business has accrued
// EquipmentUse past the due threshold gets a tiered scene line — due, then
// long-past-due at the accrual cap — and, when a wright shares the huddle, the
// act-now imperative naming pay_with_item. Silence below the threshold is the
// common case (the felt-needs posture: the quietest tier is still a line, but
// not-due is not a tier).
//
// WRIGHT side ("## Your trade"): the specialist's rounds steer — resupply
// first when he holds no whetstone, else the due business with the MOST
// accrued use, as a walk-to with a bearing or, co-present with its owner, the
// speak imperative to offer the service. The owner's own cue carries the
// pay_with_item; the wright's job is to show up and name his rate — the two
// cues split the deal the way the trader-visit cue splits the factor's.
//
// The RATE is the wright's to name (LLM-984). His cue carries what the stone
// cost him and a cost-anchored ask; the owner's cue names no figure and pays
// what he names. Before LLM-984 the owner's page quoted the catalog retail
// ("his rate is about 10") while the wright's page gave no number at all, so
// the owner set the price and the wright accepted it — live, Lewis sold the
// service at or under the price of the stone it consumed and reached 0 coins.
//
// Slice 1 gates nothing: no penalty rides the counter, so both cues are pure
// steering (the LLM-648 observe-compliance decision).

// EquipmentServiceView is the owner-side cue: the subject's own business is
// due for the wright's service.
type EquipmentServiceView struct {
	// Business is the owned structure's display name; Gear the per-business
	// diegetic phrase ("the millstones", "the bellows and anvil").
	Business string
	Gear     string
	// Overdue is true at the accrual cap — the counter has stopped counting
	// and the scene should stop being polite.
	Overdue bool
	// WrightName is the co-present wright's display name; "" = no wright in
	// the huddle, so the cue stays a standing fact with no imperative.
	// WrightID is the same actor by id, for the labor cues that step aside
	// for him (LLM-651).
	WrightName string
	WrightID   sim.ActorID
	// ServiceItem is the catalog kind the imperative names (resolved by
	// capability, not hardcoded). The view carries no price on purpose
	// (LLM-984): the rate is the wright's to name, and a catalog figure here
	// had the owner offering it before the wright spoke.
	ServiceItem sim.ItemKind
}

// WrightRoundsView is the wright-side cue: where the trade wants him next.
type WrightRoundsView struct {
	// NoStone: he holds no whetstone — resupply before the rounds.
	NoStone bool
	// The due call (zero-valued when NoStone or nothing is due): the owner,
	// their business, its gear phrase, and how to get there.
	OwnerName string
	Business  string
	Gear      string
	Overdue   bool
	// CoPresent: the owner shares the huddle — offer now (speak) instead of
	// walking.
	CoPresent bool
	// Walk is the bearing phrase ("a fair walk east"); "" when co-present or
	// coincident.
	Walk string

	// The cost-anchored ask (LLM-984), all three 0 when there is nothing to say
	// (see wrightServiceAsk). StoneCost is what the stones for one service
	// lately cost him, RecentRate what a service has lately brought him (0 with
	// no coin sale on record), Ask the rate the cue tells him to name.
	StoneCost  int
	RecentRate int
	Ask        int

	// The NoStone resupply (LLM-717), resolved by the same seller rule the
	// nail and shovel buy cues and "## Restocking" run (findItemVendors /
	// coPresentSellerForItem), so this line can never name a shop Restocking
	// would not. StonesShort is what one service still lacks. StoneBlocked:
	// suppliers exist but none is a destination this tick (shut, standoff,
	// sold-to, no means). The co-present fields mirror StallRepairBuyView.
	StonesShort     int
	StoneVendors    []RestockVendor
	StoneBlocked    bool
	CoPresentSeller string
	PendingOffer    bool
	SellerStock     int
	Block           copresentBuyBlock
	// TraderHasStone: a selling trader co-present at his shop carries
	// whetstones; "## A trader's come to deal" holds the pay_with_item. Set in
	// Build once ErrandVisit exists.
	TraderHasStone bool
	// RestockingBuysHere: "## Restocking" renders the co-present buy from the
	// same CoPresentSeller, so this line names the need without its own
	// pay_with_item. Set in Build after Restocking settles.
	RestockingBuysHere bool
}

// HasWalkToSupplier reports whether the rounds cue will render a walk-to
// whetstone supplier (LLM-491 at-post reconciliation). Mirrors
// renderWrightStoneBuy: a co-present seller or trader returns before the
// vendor list.
func (v *WrightRoundsView) HasWalkToSupplier() bool {
	if v == nil || !v.NoStone || v.CoPresentSeller != "" || v.TraderHasStone {
		return false
	}
	return len(v.StoneVendors) > 0
}

// equipmentGearPhrase maps a business's tags to its diegetic gear — a bare
// noun phrase (no article; the templates supply "the"/"The"). Checked in
// specificity order — the Tavern carries lodging AND tavern, the farms carry
// wholesaler AND farm — so the more telling tag wins. The default keeps the
// cue rendering for an untagged-but-wearable business rather than going
// silent on a phrase lookup.
func equipmentGearPhrase(obj *sim.VillageObject) string {
	switch {
	case obj == nil:
		return "tools of the trade"
	case obj.HasTag("smithy"):
		return "bellows and anvil"
	case obj.HasTag("farm"):
		return "blades and barrow"
	case obj.HasTag(sim.VisitorTagTavern):
		return "coppers and taps"
	case obj.HasTag("lodging"):
		return "coppers and hearth-irons"
	case obj.HasTag("shop"), obj.HasTag(sim.TagMarketStall):
		return "scales and fittings"
	case obj.HasTag(sim.TagWholesaler):
		return "millstones"
	default:
		return "tools of the trade"
	}
}

// equipmentServiceItemKind resolves the catalog kind carrying the
// equipment-service capability — the exact name the pay_with_item imperative
// must use. Catalog-driven (no hardcoded name, the delivery arms' posture);
// deterministic on the pathological many-kinds catalog by lowest name.
func equipmentServiceItemKind(snap *sim.Snapshot) (sim.ItemKind, int) {
	var kind sim.ItemKind
	price := 0
	for name, def := range snap.ItemKinds {
		if def == nil || !def.HasCapability(sim.CapabilityEquipmentService) {
			continue
		}
		if kind != "" && name >= kind {
			continue
		}
		kind = name
		if r := snap.Recipes[name]; r != nil {
			price = r.RetailPrice
		}
	}
	return kind, price
}

// equipmentOverdue reports whether a due business has hit the accrual cap.
// Mirrors AccrueEquipmentUse's cap arithmetic; threshold is already known > 0
// at both call sites (due-ness gated first).
func equipmentOverdue(use, threshold int) bool {
	return use >= threshold*2
}

// buildEquipmentService returns the owner-side cue when the subject's owned
// business is due for the wright's service, else nil. Resident keepers only —
// a visitor owns nothing here.
func buildEquipmentService(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, members []HuddleMember) *EquipmentServiceView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState != nil {
		return nil
	}
	threshold := snap.EquipmentServiceDueThreshold
	stall := sim.DueOwnedBusiness(snap.VillageObjects, actorID, threshold)
	if stall == nil {
		return nil
	}
	kind, _ := equipmentServiceItemKind(snap)
	if kind == "" {
		return nil // no service item in the catalog — nothing purchasable to cue
	}
	label, ok := resolveStructureLabel(snap, sim.StructureID(stall.ID))
	if !ok || label == "" {
		label = stall.DisplayName
	}
	v := &EquipmentServiceView{
		Business:    label,
		Gear:        equipmentGearPhrase(stall),
		Overdue:     equipmentOverdue(stall.EquipmentUse, threshold),
		ServiceItem: kind,
	}
	for _, m := range members {
		ms := snap.Actors[m.ID]
		if ms == nil || m.ID == actorID {
			continue
		}
		if !sim.ActorIsWright(snap.VillageObjects, ms.WorkStructureID, m.ID) {
			continue
		}
		// A wright already on a job for this owner is not here to be paid for
		// the service — the cue stays the standing fact (LLM-651).
		if liveLaborBetween(snap, m.ID, actorID) {
			continue
		}
		v.WrightName = m.DisplayName
		v.WrightID = m.ID
		break
	}
	return v
}

// liveLaborBetween reports whether worker holds a live labor offer with
// employer — pending and unexpired, en route, or working. While one stands
// the two already have a deal on the table, and both service cues yield until
// it resolves: the owner's pay arm and the wright's rounds step back, so the
// owner's page never carries "don't pay again by hand" beside "have him see to
// it", nor the wright's "you are working a job for them" beside "offer your
// service". A due business keeps until the contract settles (LLM-651,
// code_review).
func liveLaborBetween(snap *sim.Snapshot, worker, employer sim.ActorID) bool {
	for _, o := range snap.LaborLedger {
		if o == nil || o.WorkerID != worker || o.EmployerID != employer {
			continue
		}
		switch o.State {
		case sim.LaborStateEnRoute, sim.LaborStateWorking:
			return true
		case sim.LaborStatePending:
			if laborOfferLivePending(o, snap.PublishedAt) {
				return true
			}
		}
	}
	return false
}

// wrightOfferingServiceHere reports whether the subject is a wright standing
// with the owner of a business due for his service — the rounds cue is on its
// co-present arm. The solicit affordance reads this to step aside (LLM-651).
func wrightOfferingServiceHere(v *WrightRoundsView) bool {
	return v != nil && v.CoPresent
}

// withoutActor returns ids with every occurrence of drop removed; nil when
// nothing is left, so the slice keeps reading as "no one" (LLM-651).
func withoutActor(ids []sim.ActorID, drop sim.ActorID) []sim.ActorID {
	var out []sim.ActorID
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

// renderEquipmentService writes the owner-side "## Your equipment" cue.
// Content-gated.
func renderEquipmentService(b *strings.Builder, v *EquipmentServiceView) {
	if v == nil {
		return
	}
	business := sanitizeInline(v.Business)
	gear := sanitizeInline(v.Gear)
	b.WriteString("## Your equipment\n")
	if v.Overdue {
		fmt.Fprintf(b, "The %s at %s are long past due — dull, dragging, working against you every batch.", gear, business)
	} else {
		fmt.Fprintf(b, "The %s at %s have seen a season's hard use and want the wright's attention.", gear, business)
	}
	if v.WrightName != "" {
		name := sanitizeInline(v.WrightName)
		// No figure (LLM-984): the wright names his own rate, and the owner
		// pays what he names. The speak step covers the tick where the owner's
		// page renders before the wright has spoken.
		fmt.Fprintf(b, " %s is here now with his stones — have him see to it. The rate is his to name: if he has not said it yet, ask him (speak). Then pay it: pay_with_item (seller \"%s\", item \"%s\", qty 1, consume_now false, the rate he named as the coins in amount).\n\n", name, name, v.ServiceItem)
		return
	}
	b.WriteString(" It will keep until the wright calls.\n\n")
}

// buildWrightRounds returns the wright-side cue for a subject who works the
// trade, else nil. NoStone preempts the rounds — no service without a stone.
// The due call is the business with the MOST accrued use (tie: lowest object
// id), never the wright's own shop.
func buildWrightRounds(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, members []HuddleMember) *WrightRoundsView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState != nil {
		return nil
	}
	if !sim.ActorIsWright(snap.VillageObjects, actorSnap.WorkStructureID, actorID) {
		return nil
	}
	if held := actorSnap.Inventory[sim.WhetstoneKind]; held < sim.WhetstonesPerService {
		return buildWrightStoneBuy(snap, actorID, actorSnap, sim.WhetstonesPerService-held)
	}
	threshold := snap.EquipmentServiceDueThreshold
	var best *sim.VillageObject
	for _, obj := range snap.VillageObjects {
		if obj == nil || obj.OwnerActorID == actorID {
			continue
		}
		// The same wearable-business predicate the accrual seam, the owner cue,
		// and the delivery gate run (code_review): a non-business owned object
		// with a stray persisted EquipmentUse must never become a rounds
		// destination — ServiceEquipment could not reset it.
		if !sim.IsWearableStall(obj) {
			continue
		}
		if !sim.EquipmentServiceDue(obj, threshold) {
			continue
		}
		// An owner the wright already has a job with is off his rounds until
		// that contract settles — the mirror of the owner-side skip (LLM-651).
		if liveLaborBetween(snap, actorID, obj.OwnerActorID) {
			continue
		}
		if best == nil || obj.EquipmentUse > best.EquipmentUse ||
			(obj.EquipmentUse == best.EquipmentUse && obj.ID < best.ID) {
			best = obj
		}
	}
	if best == nil {
		return nil
	}
	owner := snap.Actors[best.OwnerActorID]
	if owner == nil {
		return nil
	}
	label, ok := resolveStructureLabel(snap, sim.StructureID(best.ID))
	if !ok || label == "" {
		label = best.DisplayName
	}
	v := &WrightRoundsView{
		OwnerName: owner.DisplayName,
		Business:  label,
		Gear:      equipmentGearPhrase(best),
		Overdue:   equipmentOverdue(best.EquipmentUse, threshold),
	}
	v.StoneCost, v.RecentRate, v.Ask = wrightServiceAsk(snap, actorID)
	for _, m := range members {
		if m.ID == best.OwnerActorID {
			v.CoPresent = true
			return v
		}
	}
	toTile := best.Pos.Tile()
	dist := math.Max(
		math.Abs(float64(toTile.X-actorSnap.Pos.X)),
		math.Abs(float64(toTile.Y-actorSnap.Pos.Y)),
	)
	dir := cardinalDirection(float64(actorSnap.Pos.X), float64(actorSnap.Pos.Y), float64(toTile.X), float64(toTile.Y))
	if dir != "" {
		v.Walk = qualitativeDistance(dist) + " " + dir
	}
	return v
}

// wrightServiceAsk is the cost-anchored ask for one service (LLM-984): what the
// stones for it lately cost the wright, what a service has lately brought him,
// and the rate he should name. All three are 0 when the cue should stay silent.
//
// The ask is stone cost PLUS THE CATALOG'S OWN MARGIN — service retail less
// whetstone retail (10 − 4 = 6 as seeded) — never below service retail. It is
// not the reseller's cost-plus-a-quarter (TradeValueItem.AskUnit): a reseller
// sells the good it bought, the wright sells his labour, and a quarter of a
// stone does not pay for a call. Keeping the designed margin means a rise in
// the stone's price passes through to the owners, who are the coin pools the
// service exists to tap (LLM-648). The margin is read from the catalog, so an
// umbilical recipe/set reprice of either good moves it with no code change.
//
// The service recipe has no inputs (the stone is drawn at delivery, not by a
// batch), so the makings ask (MakingsAskUnit) never sees this cost; and the
// stone is a reserved makings line with no price (LLM-720), so the resale ask
// never sees it either. This is the one place the wright's margin is stated.
//
// Silent when: the catalog gives no positive margin; he has no coin purchase
// of a stone in the window (a barter leg files no price observation, so it is
// neither a cost nor a dilution); or his realized rate already covers the ask
// — the LLM-475/627 rule that the phrase's presence is itself the signal, so a
// winning line carries no nudge. The cover test divides, as AskUnit's does.
func wrightServiceAsk(snap *sim.Snapshot, actorID sim.ActorID) (stoneCost, recentRate, ask int) {
	kind, retail := equipmentServiceItemKind(snap)
	stone := snap.Recipes[sim.WhetstoneKind]
	if kind == "" || retail <= 0 || stone == nil || stone.RetailPrice <= 0 {
		return 0, 0, 0
	}
	margin := retail - stone.RetailPrice*sim.WhetstonesPerService
	if margin <= 0 {
		return 0, 0, 0
	}
	units, coins := buyerRecentPurchases(snap, actorID, sim.WhetstoneKind, restockSalesWindow)
	if units <= 0 || coins <= 0 {
		return 0, 0, 0
	}
	// Nearest coin, the rounding the wares cue displays, so the figure he is
	// shown and the sum built on it agree (7 + 6 = 13 on his own page).
	stoneCost = (coins + units/2) / units * sim.WhetstonesPerService
	ask = stoneCost + margin
	if ask < retail {
		ask = retail
	}
	if soldUnits, soldCoins := sellerRecentSales(snap, actorID, kind, restockSalesWindow); soldUnits > 0 {
		if soldCoins/soldUnits >= ask {
			return 0, 0, 0
		}
		recentRate = (soldCoins + soldUnits/2) / soldUnits
	}
	return stoneCost, recentRate, ask
}

// wrightAskClause is the render half of wrightServiceAsk: one sentence carrying
// the cost, the recent rate when there is one, and the ask last — the directive
// with a number is the final word against the trailing-rate self-anchor
// (LLM-627). "" when the view carries no ask.
func wrightAskClause(v *WrightRoundsView) string {
	if v == nil || v.Ask <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The stone for the work cost you about %s", coinsPhrase(v.StoneCost))
	if v.RecentRate > 0 {
		fmt.Fprintf(&b, ", and of late the work has brought you about %s", coinsPhrase(v.RecentRate))
	}
	fmt.Fprintf(&b, " — ask %s or more.", coinsPhrase(v.Ask))
	return b.String()
}

// buildWrightStoneBuy is the NoStone arm of the rounds cue (LLM-717). A bare
// "Buy one" with no supplier was a want with no outlet: live, Lewis filled it
// with ~19 recall calls a day asking who sells whetstones, every one in a turn
// where Restocking named no seller. Same resolution as buildStallRepairBuy.
func buildWrightStoneBuy(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, short int) *WrightRoundsView {
	coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, sim.WhetstoneKind)
	vendors, blocked := findItemVendors(snap, actorID, actorSnap, sim.WhetstoneKind)
	v := &WrightRoundsView{
		NoStone:         true,
		StonesShort:     short,
		StoneVendors:    vendors,
		StoneBlocked:    len(blocked) > 0,
		CoPresentSeller: coName,
		PendingOffer:    coID != "" && hasPendingOfferTo(snap, actorID, coID, sim.WhetstoneKind),
	}
	if coID != "" && !v.PendingOffer {
		v.SellerStock, v.Block = classifyCoPresentBuy(snap, actorID, actorSnap, coID, sim.WhetstoneKind)
	}
	return v
}

// renderWrightStoneBuy writes the NoStone line. With no destination to give it
// says so and names nobody: a named smith or "the maker" with no move_to target
// reads as an errand (LLM-301), and a dangling want gets filled with invented
// ones (LLM-298).
func renderWrightStoneBuy(b *strings.Builder, v *WrightRoundsView) {
	b.WriteString("You're out of whetstones — no service without a stone. ")
	switch {
	case v.CoPresentSeller != "":
		switch {
		case v.PendingOffer:
			renderCoPresentBuyPending(b, v.CoPresentSeller, "whetstone")
		case v.Block == copresentBuyBlockedNoStock, v.Block == copresentBuyBlockedCoin, v.Block == copresentBuyBlockedTerms:
			renderCoPresentBuySoften(b, v.CoPresentSeller, "whetstones", v.Block)
		case v.RestockingBuysHere:
			fmt.Fprintf(b, "%s is here with you and sells them — you need one before you make your rounds.\n", sanitizeInline(v.CoPresentSeller))
		case v.SellerStock > 0 && v.SellerStock < v.StonesShort:
			renderCoPresentBuyCapped(b, v.CoPresentSeller, "whetstones", sim.WhetstoneKind, v.SellerStock)
		default:
			b.WriteString("Buy one before you make your rounds. ")
			renderCoPresentBuy(b, v.CoPresentSeller, "whetstones", sim.WhetstoneKind, v.StonesShort)
		}
	case v.TraderHasStone:
		b.WriteString("The trader here has whetstones — buy one from him before you make your rounds.\n")
	case len(v.StoneVendors) > 0:
		b.WriteString("Buy one before you make your rounds. Use move_to to reach a supplier, then pay_with_item once you arrive:\n")
		renderWalkToVendors(b, v.StoneVendors)
	case v.StoneBlocked:
		b.WriteString("You have no way to buy one just now — your rounds wait until you can.\n")
	default:
		b.WriteString("Nobody in the village has a whetstone to sell just now — your rounds wait until one comes in.\n")
	}
	b.WriteString("\n")
}

// renderWrightRounds writes the wright-side "## Your trade" cue.
// Content-gated.
func renderWrightRounds(b *strings.Builder, v *WrightRoundsView) {
	if v == nil {
		return
	}
	// "## The wright's rounds", not "## Your trade" — that header belongs to
	// the production-choice cue and the golden invariants police it.
	b.WriteString("## The wright's rounds\n")
	if v.NoStone {
		renderWrightStoneBuy(b, v)
		return
	}
	owner := sanitizeInline(v.OwnerName)
	business := sanitizeInline(v.Business)
	gear := sanitizeInline(v.Gear)
	urgency := "due for your attention"
	if v.Overdue {
		urgency = "long past due — they'll be feeling it in every batch"
	}
	// The ask rides every arm (LLM-984): before the offer imperative when he is
	// with the owner, after the walk steer when he is not, so he arrives with
	// his rate already in mind. "" when wrightServiceAsk stayed silent.
	ask := wrightAskClause(v)
	if v.CoPresent {
		fmt.Fprintf(b, "You're with %s now, and the %s at %s are %s. ", owner, gear, business, urgency)
		if ask != "" {
			b.WriteString(ask + " ")
		}
		b.WriteString("Offer your service — tell them your rate and that your stone is ready (speak).\n\n")
		return
	}
	if v.Walk != "" {
		fmt.Fprintf(b, "The %s at %s are %s — %s. Head there (move_to \"%s\") and offer %s your service.", gear, business, urgency, v.Walk, business, owner)
	} else {
		fmt.Fprintf(b, "The %s at %s are %s. Head there (move_to \"%s\") and offer %s your service.", gear, business, urgency, business, owner)
	}
	if ask != "" {
		b.WriteString(" " + ask)
	}
	b.WriteString("\n\n")
}
