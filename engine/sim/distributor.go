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

// ActorIsFactor reports whether an actor is a wholesale factor (LLM-410) — a transient
// visitor carrying the DistributorOnly flag, who trades ONLY with the village distributor.
// Nil-safe.
func ActorIsFactor(a *Actor) bool {
	return a != nil && a.VisitorState != nil && a.VisitorState.DistributorOnly
}

// FactorTradeSteer enforces the wholesale factor's distributor-only TRADE rule (LLM-410) —
// the engine backstop beneath the perception steer, the mirror of the wholesale gate above.
// A factor's trade goods move only between him and the village distributor, in EITHER
// direction: he sells his imported cloth/charms into the village and buys the village's
// surplus, both through the distributor alone. So a transaction is rejected when exactly one
// side is a factor and that factor's counterparty is not the distributor — symmetric, covering
// the factor as seller (a non-distributor buying his cloth) AND as buyer (the factor buying a
// trade good from anyone else).
//
// His SELF-provisioning is exempt so he still rides the ordinary lodging/eating lifecycle: a
// service (a room — nights_stay carries the "service" capability) or a consumable (a meal) that
// the factor BUYS is his own bed or supper, not wholesale trade, so it is allowed from any
// keeper. def is the good being bought (nil-safe: an unknown kind is treated as a trade good and
// gated). The sell side is unconditional — the factor holds only his trade wares, never a
// service/consumable to sell.
//
// Returns "" when the trade is allowed: no factor involved, the factor's counterparty IS the
// distributor, or the factor is buying self-provisioning. The steer is an in-world line and never
// names the mechanic role (LLM-292) — the distributor is named by DistributorSteerLabel.
func FactorTradeSteer(objects map[VillageObjectID]*VillageObject, actors map[ActorID]*Actor, buyer, seller *Actor, def *ItemKindDef) string {
	buyerFactor := ActorIsFactor(buyer)
	sellerFactor := ActorIsFactor(seller)
	if buyerFactor == sellerFactor {
		// Neither is a factor (the common path), or — vacuously — both are: only one
		// visitor spawns as a factor at a time, so two factors trading never arises. Allow.
		return ""
	}
	counterparty := seller
	if sellerFactor {
		counterparty = buyer
	}
	if counterparty != nil && ActorIsDistributor(objects, counterparty.WorkStructureID) {
		return "" // the factor's counterparty is the distributor — the one trade he may do
	}
	who := DistributorSteerLabel(objects, actors)
	if sellerFactor {
		// A non-distributor is trying to buy the factor's goods.
		return "that trader deals only with " + who + " — his cloth and wares go to " + who + ", who supplies the village; buy them from " + who + " instead."
	}
	// The factor is BUYING from a non-distributor. Let him provision himself — a bed or a
	// meal — anywhere; only his wholesale trade goods are distributor-only.
	if def != nil && (def.HasCapability("service") || def.Consumable()) {
		return ""
	}
	return "you deal only with " + who + " here — buy the goods you carry home from " + who + ", not from anyone else in the village."
}
