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
