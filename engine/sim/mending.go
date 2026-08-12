package sim

// mending.go — LLM-625. Clothing repair: a "mending"-capability service item
// that, when bought, restores the BUYER's worn garment units instead of
// transferring goods — the lodging pattern (a service whose delivery routes to
// an effect) applied to garment wear (garment_wear.go / LLM-422).
//
// Why a service and not a recipe: wear is not a property of the garment — it
// is a per-actor counter on the wearer's IN-USE unit (Actor.GarmentWear), and
// nothing in commerce moves that counter, so a garment on a shelf is pristine
// by definition. The loss the village feels is the in-use unit being DESTROYED
// at 0 worked minutes on the wearer's back. Repair therefore has to touch the
// wearer, which is exactly what a bought service delivering an effect does.
//
// The model:
//   - The mender is resolved from WHERE THE SELLER WORKS: a structure carrying
//     TagMending (operator-assignable via /object/add-tag, the TagDistributor /
//     TagWholesaler vocabulary — no actor is named in code).
//   - Each mend consumes MendThreadPerMend of the seller's thread
//     (factorThreadKind, an imported factor ware — LLM-442's iron pattern), so
//     mending keeps the wholesale factor load-bearing for clothing: he shifts
//     from selling garment bales to selling material.
//   - One mend restores EVERY worn garment unit the buyer carries, work and
//     warms kinds alike (Jeff, 2026-08-12): the buyer brings their mending to
//     the shop, not one sleeve at a time. Restoring = deleting the GarmentWear
//     entry — the canonical "no entry = fresh" posture of applyGarmentWear.
//   - Mending conserves garments; it cannot create them. An actor holding no
//     garment at all has nothing to mend and is rejected at the accept gate.

// TagMending marks a structure whose keeper offers clothing repair — the
// seller-eligibility anchor for the "mending" service item, resolved from
// where the seller WORKS (the TagDistributor posture). Operator-assignable
// live via /object/add-tag.
const TagMending = "mending"

// CapabilityMending is the item-capability token that routes a bought service
// to garment repair at delivery (transferOrderGoods) — the "lodging" idiom.
// A mending item must also carry "service" (no inventory backing).
const CapabilityMending = "mending"

// MendThreadPerMend is how much of the seller's thread one mend consumes.
// One spool mends a whole wardrobe visit: the price of the visit (the mending
// item's retail) is the economic lever, not the material burn.
const MendThreadPerMend = 1

// MendThreadKind is the imported material a mend consumes — the factor-borne
// supply line (visitor.go factorThreadKind names the same kind for the pack).
const MendThreadKind = ItemKind("thread")

// IsMendingStructure reports whether obj carries the mending tag. Nil-safe,
// no-owner-required — the IsDistributorStructure posture.
func IsMendingStructure(obj *VillageObject) bool {
	return obj != nil && obj.HasTag(TagMending)
}

// ActorIsMender reports whether the actor stationed at workStructureID offers
// mending — their workplace carries TagMending. Takes the object map so it
// serves both the live World and a perception Snapshot (structure and object
// share the same id). An actor with no workplace is never a mender.
func ActorIsMender(objects map[VillageObjectID]*VillageObject, workStructureID StructureID) bool {
	if workStructureID == "" {
		return false
	}
	return IsMendingStructure(objects[VillageObjectID(workStructureID)])
}

// WornGarmentKinds lists the wearable kinds whose in-use unit the actor has
// partially worn — a GarmentWear entry inside (0, budget) for a kind the actor
// still holds. These are exactly the units a mend restores. Sorted-stable is
// not needed: callers either test emptiness (the accept gate) or delete every
// listed entry (delivery).
func WornGarmentKinds(kinds map[ItemKind]*ItemKindDef, inventory, garmentWear map[ItemKind]int) []ItemKind {
	var worn []ItemKind
	for kind, left := range garmentWear {
		budget := GarmentWearMinutes(kinds, kind)
		if budget <= 0 || inventory[kind] <= 0 {
			continue // not a garment, or an unbacked stale entry — nothing to mend
		}
		if left > 0 && left < budget {
			worn = append(worn, kind)
		}
	}
	return worn
}

// MendGarments restores every worn garment unit the actor carries: each listed
// kind's GarmentWear entry is deleted, so the in-use unit reads fresh (full
// budget) to the wear sweep and both clothing tiers (ResolveWorkGarmentTier /
// ResolveWarmGarmentTier). Returns the kinds mended, empty when the actor had
// nothing worn.
func MendGarments(kinds map[ItemKind]*ItemKindDef, a *Actor) []ItemKind {
	if a == nil {
		return nil
	}
	worn := WornGarmentKinds(kinds, a.Inventory, a.GarmentWear)
	for _, kind := range worn {
		delete(a.GarmentWear, kind)
	}
	return worn
}
