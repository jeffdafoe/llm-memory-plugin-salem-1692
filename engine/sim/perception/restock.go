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

	// kind is the final sort tie-break so two item kinds sharing a display label
	// order deterministically (BuyEntries order is stable, but the sort makes the
	// section robust to policy reordering too). Unexported — never rendered.
	// Same posture as OwnStockItem.kind (consumable_vendors.go).
	kind sim.ItemKind
}

// RestockVendor is one (workplace, supplier) buy opportunity for a low item.
// StructureID is the supplier's workplace key — the reseller passes it straight
// to move_to(structure_id), then pay_with_item once co-present.
type RestockVendor struct {
	StructureLabel string // "Thorne's General Store" — where the reseller walks to
	StructureID    sim.StructureID
	CostText       string // per-buyer last-paid "~3 coins", or "" when no price is on record

	// Shut is true when the reseller has a live experiential memory of finding
	// this supplier shut (no keeper) within the decay window — render annotates
	// the line so the model deprioritizes the trip. ZBBS-HOME-353.
	Shut bool
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
			Vendors:    findItemVendors(snap, actorID, actorSnap, e.Item),
			kind:       e.Item,
		})
	}
	if len(items) == 0 {
		return nil
	}
	// Deterministic section order — by item label, then the underlying kind as
	// a tie-break for two kinds sharing a display label (BuyEntries order is
	// stable, but the sort makes the section robust to policy reordering too).
	sort.Slice(items, func(i, j int) bool {
		if items[i].ItemLabel != items[j].ItemLabel {
			return items[i].ItemLabel < items[j].ItemLabel
		}
		return items[i].kind < items[j].kind
	})
	return &RestockingView{Items: items}
}

// findItemVendors resolves the suppliers selling itemKind, ONE cue per workplace
// structure, sorted deterministically by (StructureLabel, StructureID). Runs over
// the shared structural-vendorship scan (eachVendorOffer, consumable_vendors.go),
// the same supplier-resolution path the satiation/recovery consumable cues use.
//
// Dedupe-by-structure: the LLM only needs a destination — move_to(structure_id)
// then pay_with_item resolves which co-present seller actually transacts — so two
// NPCs working the same structure and both holding the item collapse to one cue
// (which also kills the duplicate-line + map-order nondeterminism, code_review).
// The representative seller is the lowest VendorID at that structure, picked
// deterministically so the per-buyer CostText (last-paid from that seller) is
// stable across snapshots regardless of map iteration order.
func findItemVendors(snap *sim.Snapshot, buyerID sim.ActorID, buyerSnap *sim.ActorSnapshot, itemKind sim.ItemKind) []RestockVendor {
	type pick struct {
		vendorID  sim.ActorID
		structure *sim.Structure
	}
	best := map[sim.StructureID]pick{}
	eachVendorOffer(snap, buyerID, func(o vendorOffer) {
		if o.Kind != itemKind {
			return
		}
		if cur, ok := best[o.StructureID]; ok && cur.vendorID <= o.VendorID {
			return // keep the lowest VendorID at this structure
		}
		best[o.StructureID] = pick{vendorID: o.VendorID, structure: o.Structure}
	})
	if len(best) == 0 {
		return nil
	}
	out := make([]RestockVendor, 0, len(best))
	for structureID, p := range best {
		out = append(out, RestockVendor{
			StructureLabel: vendorStructureLabel(p.structure),
			StructureID:    structureID,
			// Empty fallback when no price is on record (was "ask the supplier",
			// which invited the reseller to SPEAK a price question instead of
			// calling pay_with_item — ZBBS-HOME-386). With "", renderRestocking
			// omits the cost clause entirely; the header carries the action.
			CostText:       buyerLastPaidText(snap, buyerID, p.vendorID, itemKind, ""),
			Shut:           businessRememberedShut(snap, buyerSnap, structureID),
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
// headroom), then lists where to buy (structure_id for move_to). The header
// names the move_to + pay_with_item path as an explicit two-step sequence and
// deliberately carries neither the word "ask" nor "price" — ZBBS-HOME-386: the
// old prose ("walk to a supplier and pay") plus an "ask the supplier" price hint
// drew the stateful model into SPEAKING price questions on a loop instead of
// calling pay_with_item, and even a negated "do not ask the price" still primes
// that on a weak model (code_review), so the wording avoids both tokens. Same
// actionable-cue treatment WORK-372 gave deliver_order.
func renderRestocking(b *strings.Builder, v *RestockingView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Restocking\n")
	b.WriteString("Your shop stock of these bought-in goods is running low. Restock in two steps: first use move_to for one listed supplier, then when you arrive use pay_with_item to buy more, up to your cap. The supplier is listed below; go there and pay when you arrive.\n")
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
			if vd.Shut {
				b.WriteString(closedBusinessAnnotation)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}
