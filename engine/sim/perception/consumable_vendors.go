package perception

import (
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// consumable_vendors.go — the shared "who sells a consumable that eases need N"
// finder behind two perception surfaces: the recovery-options remedy arm
// (tiredness, ZBBS-HOME-299) and the satiation seller cues (hunger/thirst,
// ZBBS-HOME-304). Both surfaces frame the result differently, but the scan is
// identical, so it lives here rather than being duplicated.

// vendorConsumable is one (vendor, item) sale opportunity for a given need —
// the neutral shape the two consuming surfaces map into their own bullets.
type vendorConsumable struct {
	StructureLabel string      // the vendor's workplace, where the buyer walks to
	ItemLabel      string      // the consumable's display label
	Magnitude      int         // immediate need eased per unit (positive)
	CostText       string      // per-buyer last-paid, or the caller's fallback
	VendorID       sim.ActorID // for the caller's deterministic sourceKey
	ItemKind       sim.ItemKind
}

// findVendorConsumables scans for sellers of an item that eases `need` and
// returns one entry per (vendor, item), sorted deterministically by
// (StructureLabel, VendorID, ItemKind) so callers get a stable order without
// re-sorting (snap.Actors / Inventory are maps).
//
// Vendorship is inferred STRUCTURALLY — v2 has no standing "vendor" capability
// (v1's serve-tool attribute is gone; sales run through the buyer's
// pay_with_item against a co-present seller). A vendor is a non-PC actor
// stationed at a resolvable WorkStructureID who holds, qty>0, an item the
// catalog says eases `need` on the immediate hit. The cue is surfaced at that
// WORKPLACE, not the vendor's current location (ZBBS-HOME-299 decision): a
// stable "this is where it's sold" signal, so it carries NO transient
// break/sleep/shift gate — availability is resolved on arrival by the
// transaction layer (pay_with_item co-presence + AcceptPay's seller-break gate).
//
// Excluded: the buyer themselves, PCs (they don't sell through the NPC commerce
// path), vendors with no workplace, and vendors whose WorkStructureID doesn't
// resolve to a structure in the snapshot (the "buy at X" cue would name an
// unactionable destination). costFallback is the cost text when the buyer has
// no prior purchase of this item from this seller in the PriceBook.
func findVendorConsumables(snap *sim.Snapshot, buyerID sim.ActorID, need sim.NeedKey, costFallback string) []vendorConsumable {
	if snap == nil || len(snap.ItemKinds) == 0 {
		return nil
	}
	var out []vendorConsumable
	for vendorID, vendor := range snap.Actors {
		if vendor == nil || vendorID == buyerID || vendor.Kind == sim.KindPC {
			continue
		}
		if vendor.WorkStructureID == "" {
			continue
		}
		st := snap.Structures[vendor.WorkStructureID]
		if st == nil {
			continue
		}
		for kind, qty := range vendor.Inventory {
			if qty <= 0 {
				continue
			}
			mag := itemNeedMagnitude(snap, kind, need)
			if mag <= 0 {
				continue
			}
			out = append(out, vendorConsumable{
				StructureLabel: vendorStructureLabel(st),
				ItemLabel:      itemDisplayLabel(snap, kind),
				Magnitude:      mag,
				CostText:       buyerLastPaidText(snap, buyerID, vendorID, kind, costFallback),
				VendorID:       vendorID,
				ItemKind:       kind,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StructureLabel != out[j].StructureLabel {
			return out[i].StructureLabel < out[j].StructureLabel
		}
		if out[i].VendorID != out[j].VendorID {
			return out[i].VendorID < out[j].VendorID
		}
		return out[i].ItemKind < out[j].ItemKind
	})
	return out
}

// itemNeedMagnitude returns the immediate `need` a unit of kind eases per the
// item catalog, or 0 when the kind is unknown or eases no `need` on the
// immediate hit. Pure slow-burn items (Immediate==0, dwell-only) return 0 —
// they aren't "buy and consume now" satisfiers in the MVP.
//
// First-match is correct: ItemKindDef.Satisfies holds at most one entry per
// attribute (the v1 item_satisfies PK is (item_kind, attribute), enforced at
// load — see ItemKindDef.Satisfies), so there is no second entry for `need` to
// stack or out-rank.
func itemNeedMagnitude(snap *sim.Snapshot, kind sim.ItemKind, need sim.NeedKey) int {
	def := snap.ItemKinds[kind]
	if def == nil {
		return 0
	}
	for _, s := range def.Satisfies {
		if s.Attribute == need {
			return s.Immediate
		}
	}
	return 0
}

// itemDisplayLabel resolves a consumable's human label from the catalog,
// falling back to the raw kind when unknown or unlabeled.
func itemDisplayLabel(snap *sim.Snapshot, kind sim.ItemKind) string {
	if def := snap.ItemKinds[kind]; def != nil && def.DisplayLabel != "" {
		return def.DisplayLabel
	}
	return string(kind)
}

// vendorStructureLabel names the workplace where a consumable is bought, with a
// generic fallback when the structure has no display name.
func vendorStructureLabel(s *sim.Structure) string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return "the shop"
}
