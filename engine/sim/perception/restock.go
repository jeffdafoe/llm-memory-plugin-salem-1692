package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// restock.go — ZBBS-WORK-322. The "## Restocking" perception section: surfaces,
// to a reseller whose bought-in stock is running low, how to replenish it — each
// `buy` RestockEntry below the reorder threshold (on-hand vs cap) and the
// suppliers selling that item (their workplace, its structure_id for move_to,
// and a per-buyer price hint). The reseller's own LLM decides whether, what, and
// how much to restock, then acts via the existing move_to + pay_with_item tools.
//
// This is the buyer-facing affordance half of the buy-side restock feature; the
// restock producer (engine/sim/restock_tick.go) is the half that brings the
// reseller to a reactor tick by warranting it. Both gate on the same reorder
// threshold (restockReorderThresholdMet, surfaced into the snapshot as
// RestockReorderPct) so the section and the warrant never disagree.
//
// Supplier resolution reuses the structural-vendorship model from
// consumable_vendors.go (a vendor is a non-PC actor stationed at a resolvable
// WorkStructureID holding qty>0 of the item) and its shared helpers
// (itemDisplayLabel, vendorStructureLabel, buyerLastPaidText) — same surface the
// satiation/recovery cues use. The difference: this finder keys on a specific
// ItemKind the reseller wants to buy, not on a need a consumable eases.

// RestockingView is the content-gated "## Restocking" section. A nil view (or
// empty Items) means render omits the section.
type RestockingView struct {
	Items []RestockItemView
}

// RestockItemView is one low `buy` item the reseller could replenish: its label,
// current on-hand quantity, the cap it restocks toward, and the suppliers
// selling it. Vendors may be empty — the item still surfaces (the reseller knows
// it's low) but with no actionable "buy at X" destination this tick.
type RestockItemView struct {
	ItemLabel  string
	CurrentQty int
	Cap        int
	Vendors    []RestockVendor
}

// RestockVendor is one (workplace, supplier) buy opportunity for a low item.
// StructureID is the supplier's workplace key — the reseller passes it straight
// to move_to(structure_id), then pay_with_item once co-present.
type RestockVendor struct {
	StructureLabel string // "Thorne's General Store" — where the reseller walks to
	StructureID    sim.StructureID
	CostText       string // per-buyer last-paid "~3 coins", or "ask the supplier"
}

// buildRestocking builds the restock view for actorSnap, or nil when the actor
// holds no `buy` entry below the reorder threshold, restock is disabled
// (RestockReorderPct == 0), or it carries no RestockPolicy. Pure over the
// snapshot.
func buildRestocking(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *RestockingView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil {
		return nil
	}
	pct := snap.RestockReorderPct
	if pct <= 0 {
		return nil // producer/feature disabled
	}
	var items []RestockItemView
	for _, e := range actorSnap.RestockPolicy.BuyEntries() {
		cap := e.Cap()
		current := actorSnap.Inventory[e.Item]
		if !sim.RestockReorderThresholdMet(current, cap, pct) {
			continue
		}
		items = append(items, RestockItemView{
			ItemLabel:  itemDisplayLabel(snap, e.Item),
			CurrentQty: current,
			Cap:        cap,
			Vendors:    findItemVendors(snap, actorID, e.Item),
		})
	}
	if len(items) == 0 {
		return nil
	}
	// Deterministic section order — by item label, then the underlying kind as
	// a tie-break for two kinds sharing a display label (BuyEntries order is
	// stable, but a sort makes the section robust to policy reordering too).
	sort.Slice(items, func(i, j int) bool {
		return items[i].ItemLabel < items[j].ItemLabel
	})
	return &RestockingView{Items: items}
}

// findItemVendors scans for suppliers selling itemKind and returns one entry per
// (workplace, supplier), sorted deterministically by (StructureLabel,
// StructureID). Structural vendorship + workplace surface mirror
// findVendorConsumables: a supplier is a non-PC actor (excluding the reseller
// itself) stationed at a resolvable WorkStructureID who holds qty>0 of itemKind.
// The cue names the WORKPLACE, carrying no transient break/sleep/shift gate —
// availability is resolved on arrival by the transaction layer (pay_with_item
// co-presence + AcceptPay's seller-break gate), the same posture as the
// satiation vendor cues.
func findItemVendors(snap *sim.Snapshot, buyerID sim.ActorID, itemKind sim.ItemKind) []RestockVendor {
	var out []RestockVendor
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
		if vendor.Inventory[itemKind] <= 0 {
			continue
		}
		out = append(out, RestockVendor{
			StructureLabel: vendorStructureLabel(st),
			StructureID:    vendor.WorkStructureID,
			CostText:       buyerLastPaidText(snap, buyerID, vendorID, itemKind, "ask the supplier"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StructureLabel != out[j].StructureLabel {
			return out[i].StructureLabel < out[j].StructureLabel
		}
		return out[i].StructureID < out[j].StructureID
	})
	return out
}

// renderRestocking writes the "## Restocking" section. Content-gated: a
// nil/empty view writes nothing. Each low item leads with its on-hand/cap so the
// reseller can size the buy (it picks its own quantity — the line hints the
// headroom), then lists where to buy with the structure_id for move_to.
func renderRestocking(b *strings.Builder, v *RestockingView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Restocking\n")
	b.WriteString("Your shop stock of these bought-in goods is running low. You may restock by walking to a supplier and paying for more (you choose how much, up to your cap).\n")
	for _, it := range v.Items {
		headroom := it.Cap - it.CurrentQty
		if headroom < 0 {
			headroom = 0
		}
		fmt.Fprintf(b, "- %s: %d on hand of %d cap (room for %d more).",
			sanitizeInline(it.ItemLabel), it.CurrentQty, it.Cap, headroom)
		if len(it.Vendors) == 0 {
			b.WriteString(" No supplier nearby is currently holding stock.\n")
			continue
		}
		b.WriteString("\n")
		for _, vd := range it.Vendors {
			b.WriteString("  - buy from ")
			b.WriteString(sanitizeInline(vd.StructureLabel))
			if vd.StructureID != "" {
				fmt.Fprintf(b, " (structure_id: %s)", vd.StructureID)
			}
			if vd.CostText != "" {
				fmt.Fprintf(b, ", %s", vd.CostText)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}
