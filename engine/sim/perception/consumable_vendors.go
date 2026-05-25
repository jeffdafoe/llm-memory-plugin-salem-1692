package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// consumable_vendors.go — the shared "who sells a consumable that eases need N"
// finder behind two perception surfaces: the recovery-options remedy arm
// (tiredness, ZBBS-HOME-299) and the satiation seller cues (hunger/thirst,
// ZBBS-HOME-304). Both surfaces frame the result differently, but the scan is
// identical, so it lives here rather than being duplicated.

// vendorOffer is one (vendor, item) sale opportunity surfaced by the shared
// structural-vendorship scan (eachVendorOffer) — the neutral tuple every
// consuming finder maps into its own bullet shape. Structure is the resolved
// (non-nil) workplace; StructureID is its key (what a buyer's move_to needs).
type vendorOffer struct {
	VendorID    sim.ActorID
	Structure   *sim.Structure
	StructureID sim.StructureID
	Kind        sim.ItemKind
	Qty         int
}

// eachVendorOffer is the shared structural-vendorship scan behind every "who
// sells X" perception surface: the need-keyed consumable finder
// (findVendorConsumables) and the item-keyed restock supplier finder
// (findItemVendors, restock.go). It calls fn once for every (vendor, item) where
// a non-PC actor OTHER than buyerID, stationed at a resolvable WorkStructureID,
// holds qty>0 of the item. Each caller applies its own match predicate + mapping
// inside fn. Iteration order is snap.Actors / Inventory map order — callers that
// need stable output sort their own result (this scan promises no order).
//
// Vendorship is inferred STRUCTURALLY — v2 has no standing "vendor" capability
// (v1's serve-tool attribute is gone; sales run through the buyer's
// pay_with_item against a co-present seller). The cue names the WORKPLACE, not
// the vendor's current location, and carries NO transient break/sleep/shift gate
// — availability is resolved on arrival by the transaction layer (pay_with_item
// co-presence + AcceptPay's seller-break gate).
func eachVendorOffer(snap *sim.Snapshot, buyerID sim.ActorID, fn func(vendorOffer)) {
	if snap == nil {
		return
	}
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
			fn(vendorOffer{
				VendorID:    vendorID,
				Structure:   st,
				StructureID: vendor.WorkStructureID,
				Kind:        kind,
				Qty:         qty,
			})
		}
	}
}

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
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		mag := itemNeedMagnitude(snap, o.Kind, need)
		if mag <= 0 {
			return
		}
		out = append(out, vendorConsumable{
			StructureLabel: vendorStructureLabel(o.Structure),
			ItemLabel:      itemDisplayLabel(snap, o.Kind),
			Magnitude:      mag,
			CostText:       buyerLastPaidText(snap, buyerID, o.VendorID, o.Kind, costFallback),
			VendorID:       o.VendorID,
			ItemKind:       o.Kind,
		})
	})
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

// OwnStockItem is one satisfier the actor already carries — the consume-first
// half of both the satiation section (hunger/thirst) and the recovery-options
// tiredness own-stock line. Shared so "you carry X — consume" reads identically
// across needs.
type OwnStockItem struct {
	Label     string // "coca tea"
	Magnitude int    // immediate need eased per unit

	// kind is the final sort tie-break so two item kinds that share a display
	// label AND magnitude order deterministically (Inventory is a map).
	// Unexported — never rendered.
	kind sim.ItemKind
}

// gatherOwnStock returns the actor's own inventory items that ease `need` on the
// immediate hit, strongest-first (ties by label, then ItemKind for determinism
// — Inventory is a map). Empty when the actor carries no satisfier.
func gatherOwnStock(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, need sim.NeedKey) []OwnStockItem {
	if snap == nil || actorSnap == nil {
		return nil
	}
	var out []OwnStockItem
	for kind, qty := range actorSnap.Inventory {
		if qty <= 0 {
			continue
		}
		mag := itemNeedMagnitude(snap, kind, need)
		if mag <= 0 {
			continue
		}
		out = append(out, OwnStockItem{Label: itemDisplayLabel(snap, kind), Magnitude: mag, kind: kind})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Magnitude != out[j].Magnitude {
			return out[i].Magnitude > out[j].Magnitude
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].kind < out[j].kind
	})
	return out
}

// renderOwnStockLine renders "<item> (~N), <item> (~N)" for an own-stock list.
// Shared by the satiation section and the recovery-options tiredness line.
func renderOwnStockLine(items []OwnStockItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("%s (~%d)", sanitizeInline(it.Label), it.Magnitude)
	}
	return strings.Join(parts, ", ")
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
