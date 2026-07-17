package sim

// distributor.go — the wholesale tier (LLM-223, generalized from the farm gate
// to the wholesaler tag in LLM-252). Wholesaler-tagged sellers (the farms and
// the mill) sell only to the village distributor (Josiah as the "village Sysco");
// everyone else buys wholesale-origin goods from the distributor, not straight
// from the source. Two enforcement points ride the helpers here: the perception
// filter in perception/consumable_vendors.go (drops wholesale vendors from every
// non-distributor buyer's "who sells X" cues) and the engine backstop below the
// perception layer in pay_with_item_commands.go (rejects a non-distributor's buy
// from a wholesale seller). The wholesaler tag is deliberately independent of
// TagFarm — which now scopes only the farm-upkeep tax — so the gate and the tax
// stay orthogonal: a farm carries both, the mill carries only wholesaler.

// TagDistributor marks the structure whose keeper is the village wholesale
// distributor — the sole buyer the farm-tagged producers may sell to, and the
// standing supplier everyone else restocks from. Operator-assignable live via
// /object/add-tag, mirroring TagFarm / TagMarketStall (the LLM-203 tag vocabulary).
const TagDistributor = "distributor"

// IsDistributorStructure reports whether obj carries the distributor tag. Nil-safe.
// Unlike IsFarmStructure it does NOT require an owner: the tag alone marks the
// wholesale channel, and the distributor's identity as a BUYER is resolved from
// who WORKS there (ActorIsDistributor), not from ownership.
func IsDistributorStructure(obj *VillageObject) bool {
	return obj != nil && obj.HasTag(TagDistributor)
}

// ActorIsDistributor reports whether the actor stationed at workStructureID is the
// village distributor — i.e. their workplace carries TagDistributor. Takes the
// object map so it serves both the live World (w.VillageObjects) and a perception
// Snapshot (snap.VillageObjects); structure and object share the same id, so the
// work anchor keys straight into the object map. An actor with no workplace is
// never the distributor.
func ActorIsDistributor(objects map[VillageObjectID]*VillageObject, workStructureID StructureID) bool {
	if workStructureID == "" {
		return false
	}
	return IsDistributorStructure(objects[VillageObjectID(workStructureID)])
}

// TagWholesaler marks a structure whose sellers are wholesale-tier: they sell
// only to the village distributor, never direct to retail buyers. Generalizes
// the LLM-223 farm gate (LLM-252) so non-farm wholesale sources — the mill —
// join the tier without being farms. Operator-assignable live via
// /object/add-tag, mirroring TagFarm / TagDistributor (the LLM-203 vocabulary).
// Deliberately independent of TagFarm: a farm carries both (wholesaler gate +
// farm-upkeep tax), the mill carries only wholesaler.
const TagWholesaler = "wholesaler"

// IsWholesalerStructure reports whether obj carries the wholesaler tag. Nil-safe.
// Like IsDistributorStructure (and unlike IsFarmStructure) it does NOT require an
// owner: the tag alone marks the wholesale channel, resolved from where the
// seller WORKS, not from ownership.
func IsWholesalerStructure(obj *VillageObject) bool {
	return obj != nil && obj.HasTag(TagWholesaler)
}

// SellerAtWholesaler reports whether a seller stationed at workStructureID sells
// from a wholesaler-tagged structure — the wholesale gate's seller-side test.
// Empty workStructureID (no workplace) is never a wholesaler.
func SellerAtWholesaler(objects map[VillageObjectID]*VillageObject, workStructureID StructureID) bool {
	if workStructureID == "" {
		return false
	}
	return IsWholesalerStructure(objects[VillageObjectID(workStructureID)])
}

// IsOwnProduce reports whether kind is a good this actor makes as wholesale stock —
// it works at a wholesaler-tagged structure AND kind is one of its produce-source
// restock rows. Such goods are stock to sell, not the actor's larder (LLM-267), so
// its owner can never consume them: the Consume guard rejects on this predicate and
// the satiation eat-cue filters on it, keying off the SAME test so cue and block
// can't drift (the StallRepairable pattern). Takes the object map + work anchor +
// policy, like SellerAtWholesaler, so it serves both the live World and a perception
// Snapshot. Nil/empty-safe: no policy, no workplace, or a non-wholesaler workplace
// all read false, so it is a no-op for every non-wholesale actor.
func IsOwnProduce(objects map[VillageObjectID]*VillageObject, workStructureID StructureID, policy *RestockPolicy, kind ItemKind) bool {
	if policy == nil || !SellerAtWholesaler(objects, workStructureID) {
		return false
	}
	for _, e := range policy.ProduceEntries() {
		if e.Item == kind {
			return true
		}
	}
	return false
}

// DistributorSteerLabel names where a wholesale-gated buyer should shop instead — the
// distributor's keeper (owner of the distributor-tagged structure), falling back
// to the structure's own display name, then a generic phrase. Best-effort, for the
// reject steer only: the gate still holds if no distributor is configured (the
// label degrades; the block does not). First match wins — one distributor by data
// convention, so iteration order only matters if an operator tags two, which the
// data model doesn't intend. The fallback is deliberately an in-world phrase, not
// "the village distributor": rendered prose must never hand the NPC's LLM a
// mechanic-role term (LLM-292) — `distributor` stays an engine/tag concept.
func DistributorSteerLabel(objects map[VillageObjectID]*VillageObject, actors map[ActorID]*Actor) string {
	for _, obj := range objects {
		if !IsDistributorStructure(obj) {
			continue
		}
		if obj.OwnerActorID != "" {
			if owner := actors[obj.OwnerActorID]; owner != nil && owner.DisplayName != "" {
				return owner.DisplayName
			}
		}
		if obj.DisplayName != "" {
			return obj.DisplayName
		}
	}
	return "the village storekeeper"
}

// ActorTradeErrand returns a merchant visitor's bound trade errand, or nil (LLM-455) —
// the generalization of the LLM-410 ActorIsFactor predicate. Non-nil only for a transient
// visitor carrying a Trade (a buy or sell bound to a real good + real counterparty); nil
// for a resident keeper, a PC, or a passer-through visitor. Nil-safe.
func ActorTradeErrand(a *Actor) *TradeErrand {
	if a == nil || a.VisitorState == nil {
		return nil
	}
	return a.VisitorState.Trade
}

// ActorHasTradeErrand reports whether the actor is a merchant visitor carrying a bound
// errand (LLM-455). The garment-wear exemption keys on it — a merchant's garments are
// stock to sell, not worn, so they don't wear on him (a passer-through with no errand
// wears his coat normally). Nil-safe.
func ActorHasTradeErrand(a *Actor) bool {
	return ActorTradeErrand(a) != nil
}

// TradeErrandSteer enforces a merchant visitor's errand-confinement rule (LLM-455) — the
// engine backstop beneath the perception layer and the talk-only tool gate, the
// generalization of the LLM-410 FactorTradeSteer (the wholesale factor is now the "sell"
// errand whose Counterparty is the distributor). A merchant visitor's trade goods move
// only between him and his errand Counterparty (the open keeper who sells the good he
// buys, or the distributor who absorbs the imports he sells). So a transaction is rejected
// when exactly one side is a merchant visitor and that visitor's counterparty is not the
// keeper of his errand structure — covering the visitor as buyer AND as seller, and (for a
// factor) both legs of the two-way distributor deal, since the gate keys on the counterparty
// STRUCTURE, not the direction.
//
// His SELF-provisioning is exempt so he still rides the ordinary lodging/eating lifecycle: a
// service (a room — nights_stay carries the "service" capability) or a consumable (a meal, a
// journeycake) that the visitor BUYS is his own bed / supper / road-food, not his errand
// trade, so it is allowed from any keeper. def is the good being bought (nil-safe: an unknown
// kind is treated as errand trade and gated). The visitor-as-seller side is unconditional — a
// merchant holds only his trade wares to sell, never a service/consumable.
//
// Returns "" when the trade is allowed: no merchant visitor involved, the counterparty IS his
// errand keeper, or the visitor is buying self-provisioning. The steer is an in-world line and
// never names a mechanic role (LLM-292).
func TradeErrandSteer(objects map[VillageObjectID]*VillageObject, actors map[ActorID]*Actor, buyer, seller *Actor, def *ItemKindDef) string {
	buyerErrand := ActorTradeErrand(buyer)
	sellerErrand := ActorTradeErrand(seller)
	if buyerErrand == nil && sellerErrand == nil {
		return "" // neither is a merchant visitor — the common path
	}
	if buyerErrand != nil && sellerErrand != nil {
		// Two merchant visitors trading with each other (both carry errands): neither is the
		// other's counterparty keeper, so this trade belongs to neither errand. Reject — each
		// deals only with his own bound keeper, never with another traveler (code_review).
		return "you deal only with the keeper you came to trade with, not with another traveler passing through."
	}
	// Exactly one side carries an errand — check its counterparty against the other party.
	errand := sellerErrand
	counterparty := buyer
	if buyerErrand != nil {
		errand = buyerErrand
		counterparty = seller
	}
	if errand.Counterparty != "" && counterparty != nil && counterparty.WorkStructureID == errand.Counterparty {
		return "" // the merchant's counterparty is his errand keeper — the one trade he may do
	}
	who := errandKeeperLabel(objects, actors, errand.Counterparty)
	if sellerErrand != nil {
		// A villager is trying to buy the visitor's trade goods from him directly.
		return "that trader deals only with " + who + " — the goods he carries go to " + who + ", who supplies the village; buy them from " + who + " instead."
	}
	// The visitor is BUYING from a villager who is not his errand keeper. Let him
	// provision himself — a bed, a meal, road-food — anywhere; only his errand trade is confined.
	if def != nil && (def.HasCapability("service") || def.Consumable()) {
		return ""
	}
	return "you deal only with " + who + " for your trade — take your custom there, not to anyone else in the village."
}

// errandKeeperLabel names the keeper of a merchant visitor's errand structure — the owner
// of the Counterparty structure, falling back to the structure's own display name, then a
// generic in-world phrase (LLM-455). Best-effort, for the reject steer only: the gate holds
// even if the label degrades. Never names a mechanic role (LLM-292).
func errandKeeperLabel(objects map[VillageObjectID]*VillageObject, actors map[ActorID]*Actor, counterparty StructureID) string {
	if counterparty != "" {
		if obj := objects[VillageObjectID(counterparty)]; obj != nil {
			if obj.OwnerActorID != "" {
				if owner := actors[obj.OwnerActorID]; owner != nil && owner.DisplayName != "" {
					return owner.DisplayName
				}
			}
			if obj.DisplayName != "" {
				return obj.DisplayName
			}
		}
	}
	return "the one they came to trade with"
}
