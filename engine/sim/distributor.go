package sim

// distributor.go — the LLM-223 farm wholesale tier. Farm-tagged producers sell
// only to the village distributor (Josiah as the "village Sysco"); everyone else
// buys farm-origin goods from the distributor, not straight from the farm. Two
// enforcement points ride the helpers here: the perception filter in
// perception/consumable_vendors.go (drops farm vendors from every non-distributor
// buyer's "who sells X" cues) and the engine backstop below the perception layer
// in pay_with_item_commands.go (rejects a non-distributor's buy from a farm
// seller). Shares TagFarm / IsFarmStructure with the farm-upkeep tax so the two
// features can never disagree on "what is a farm".

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

// SellerAtFarm reports whether a seller stationed at workStructureID sells from a
// farm-tagged structure — the wholesale gate's seller-side test. Reuses
// IsFarmStructure so "what counts as a farm" stays shared with the upkeep tax.
// Empty workStructureID (no workplace) is never a farm.
func SellerAtFarm(objects map[VillageObjectID]*VillageObject, workStructureID StructureID) bool {
	if workStructureID == "" {
		return false
	}
	return IsFarmStructure(objects[VillageObjectID(workStructureID)])
}

// DistributorSteerLabel names where a farm-gated buyer should shop instead — the
// distributor's keeper (owner of the distributor-tagged structure), falling back
// to the structure's own display name, then a generic phrase. Best-effort, for the
// reject steer only: the gate still holds if no distributor is configured (the
// label degrades; the block does not). First match wins — one distributor by data
// convention, so iteration order only matters if an operator tags two, which the
// data model doesn't intend.
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
	return "the village distributor"
}
