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
	// Wholesale tier (LLM-223, generalized to the wholesaler tag in LLM-252):
	// wholesaler-tagged sellers (farms, mill) sell only to the village
	// distributor. For every other buyer, drop wholesale vendors from this scan so
	// perception never points a non-distributor at a wholesale source — the restock
	// buy directory AND the satiation/consumption cues both ride this shared scan,
	// so a hungry or restocking buyer is routed to the distributor (or nowhere)
	// rather than lured to a source the PayWithItem backstop then rejects. The
	// distributor himself still perceives the wholesale sources as suppliers.
	// Resolved once for the scan; an empty buyerID (an unbuyer-scoped scan, e.g. a
	// min-price venue sweep) reads as non-distributor, which is harmless — such a
	// caller filters the offers it wants and never follows a wholesale source.
	buyerIsDistributor := false
	if buyer := snap.Actors[buyerID]; buyer != nil {
		buyerIsDistributor = sim.ActorIsDistributor(snap.VillageObjects, buyer.WorkStructureID)
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
		if !buyerIsDistributor && sim.SellerAtWholesaler(snap.VillageObjects, vendor.WorkStructureID) {
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
	StructureLabel string          // the vendor's workplace, where the buyer walks to
	StructureID    sim.StructureID // the workplace's key — what the buyer passes to move_to
	ItemLabel      string          // the consumable's display label
	Magnitude      int             // immediate need eased per unit (positive)
	CostText       string          // per-buyer last-paid, or the caller's fallback
	costCoins      int             // per-buyer last-paid as a number, 0 when unknown (LLM-176 affordability)
	VendorID       sim.ActorID     // for the caller's deterministic sourceKey
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
			StructureID:    o.StructureID,
			ItemLabel:      itemDisplayLabel(snap, o.Kind),
			Magnitude:      mag,
			CostText:       buyerLastPaidText(snap, buyerID, o.VendorID, o.Kind, costFallback),
			costCoins:      buyerLastPaidCoins(snap, buyerID, o.VendorID, o.Kind),
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

// findWarmGarmentVendors returns the workplaces selling a warm garment (a
// CapabilityWarms good — coat or cloak) — the vendor-gated destinations for the
// cold "buy a coat" nudge (LLM-410). Built on the shared structural-vendorship
// scan: any non-PC keeper holding a warms-capable good at a resolvable workplace,
// one entry per structure (lowest VendorID the representative), sorted by label
// then structure for a stable cue. Empty when nothing warm is for sale — which is
// exactly what makes the nudge vendor-gated: no supply, no cue, so the steer never
// dangles before the seed stock (or the factor channel) exists.
//
// Buyer-unscoped (empty buyerID): the scan reads the caller as a non-distributor,
// so a wholesale-only warm-garment source would be dropped — correct for an
// ordinary villager — and the self-vendor case can't arise, since a keeper holding
// a coat already reads as warm (actorSnapHasWarmGarment) and never reaches this cue.
func findWarmGarmentVendors(snap *sim.Snapshot) []RestockVendor {
	if snap == nil || len(snap.ItemKinds) == 0 {
		return nil
	}
	type pick struct {
		vendorID  sim.ActorID
		structure *sim.Structure
	}
	best := map[sim.StructureID]pick{}
	eachVendorOffer(snap, "", func(o vendorOffer) {
		def := snap.ItemKinds[o.Kind]
		if def == nil || !def.HasCapability(sim.CapabilityWarms) {
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
			VendorID:       p.vendorID,
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

// OwnStockItem is one satisfier the actor already carries — the consume-first
// half of both the satiation section (hunger/thirst) and the recovery-options
// tiredness own-stock line. Shared so "you carry X — consume" reads identically
// across needs.
type OwnStockItem struct {
	Label     string // "coca tea"
	Magnitude int    // immediate need eased per unit

	// TradeStock is true when this satisfier is one of the actor's own trade
	// goods (in its RestockPolicy) — merchandise/ingredients it produces, buys,
	// or forages, not personal provisions. The satiation own-stock cue uses it
	// to demote trade stock to a desperation-only option (LLM-134) so a producer
	// isn't nudged to graze the goods it sells. The recovery-options tiredness
	// caller ignores the flag.
	TradeStock bool

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
		out = append(out, OwnStockItem{
			Label:     itemDisplayLabel(snap, kind),
			Magnitude: mag,
			// Effective-policy check (LLM-260): a DERIVED buy input — a recipe
			// ingredient of something the actor produces, with no hand-authored
			// entry — is trade stock too, so Hannah's porridge milk demotes from
			// her casual-eating cue the same way John's explicit stew carrots do.
			TradeStock: sim.ManagesEffective(snap.Recipes, actorSnap.RestockPolicy, kind),
			kind:       kind,
		})
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

// renderOwnStockLine renders "<item> (<felt amount>), <item> (<felt amount>)"
// for an own-stock list — e.g. "cheese (a good meal), bread (a small bite)".
// Shared by the satiation section (need = hunger/thirst) and the
// recovery-options tiredness line (need = tiredness). The felt phrase replaces
// the raw "(~N)" magnitude the LLM couldn't calibrate against (ZBBS-HOME-339).
//
// needLevel is the actor's CURRENT level of the need, for the ZBBS-WORK-392
// sufficiency clause (see feltAmountWithSufficiency); the recovery-options
// tiredness caller passes 0 to keep its line bare.
func renderOwnStockLine(items []OwnStockItem, need sim.NeedKey, needLevel int) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("%s (%s)", sanitizeInline(it.Label), feltAmountWithSufficiency(it.Magnitude, need, needLevel))
	}
	return strings.Join(parts, ", ")
}

// feltAmountWithSufficiency renders the felt tier, appending the per-unit
// sufficiency fact when a SINGLE unit would fully zero the actor's current
// need: "a hearty meal — a single one would fully satisfy your hunger"
// (ZBBS-WORK-392, the perception half of the Prudence over-buy fix — the
// WORK-391 clamp makes the waste mechanically impossible; this makes the buy
// decision informed in the first place). Facts only, never a quantity
// recommendation (the c′ intent rule): the clause renders ONLY when one unit
// suffices — "you would need three" reads as a buy-three nudge, and
// under-buying self-corrects (still hungry next tick, cue still present). A
// reseller correctly ignores the fact; a needLevel of 0 (caller without a
// live level, or need not pressing) renders the bare tier.
func feltAmountWithSufficiency(magnitude int, need sim.NeedKey, needLevel int) string {
	felt := itemFeltAmount(magnitude, need)
	if magnitude > 0 && needLevel > 0 && magnitude >= needLevel {
		if phrase := needSufficiencyPhrase(need); phrase != "" {
			felt += " — a single one would fully " + phrase
		}
	}
	return felt
}

// needSufficiencyPhrase is the per-need tail of the sufficiency clause, kept
// in the same felt-language register as itemFeltAmount. Explicitly scoped to
// the needs with authored prose — an unsupported need (e.g. a future caller
// passing a live tiredness level through the shared renderOwnStockLine)
// renders the bare tier rather than accidental bad prose like "satisfy your
// tiredness" (code_review).
func needSufficiencyPhrase(need sim.NeedKey) string {
	switch need {
	case "hunger":
		return "satisfy your hunger"
	case "thirst":
		return "quench your thirst"
	default:
		return ""
	}
}

// itemFeltAmount maps an item's immediate need-satisfaction magnitude to a
// felt-language phrase, per need — the v1 vocabulary restored (the v2 rewrite
// regressed to raw "(~N)"). Bands are calibrated against the live v2 item
// catalog (item_satisfies) so each catalogued satisfier lands in a distinct
// phrase:
//
//	hunger:    ale/berries 2, carrots 3, bread/stew 4, cheese 8, meat 10
//	thirst:    ale 4, milk 6, water 8
//	tiredness: coca tea 12
//
// ZBBS-HOME-339.
func itemFeltAmount(amount int, need sim.NeedKey) string {
	switch need {
	case "thirst":
		switch {
		case amount <= 4:
			return "a sip"
		case amount <= 7:
			return "a drink"
		default:
			return "a deep drink"
		}
	case "tiredness":
		switch {
		case amount <= 4:
			return "a small revival"
		case amount <= 7:
			return "a fair revival"
		case amount <= 11:
			return "a strong revival"
		default:
			return "a thorough waking"
		}
	default: // hunger
		switch {
		case amount <= 2:
			return "a nibble"
		case amount <= 4:
			return "a small bite"
		case amount <= 8:
			return "a good meal"
		default:
			return "a hearty meal"
		}
	}
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

// closedBusinessAnnotation is the in-world suffix appended to a supplier cue
// pointing at a business the subject remembers finding shut (ZBBS-HOME-353).
// Phrased as recalled experience, not a live status read, so it reads as the
// NPC's own knowledge — and it deprioritizes rather than forbids (the memory
// decays, so a flat "it's closed" would be wrong once a keeper returns).
const closedBusinessAnnotation = " — though you went there not long ago and found it shut up, no one tending it"

// businessRememberedShut reports whether the subject has an experiential memory
// (ZBBS-HOME-353) of arriving at structureID and finding it shut, still within
// its TTL of the snapshot clock. Perception uses it to annotate a supplier cue
// pointing at a remembered-shut business so the model deprioritizes the trip.
// The memory is stamped + self-cleared by the arrival subscriber
// (sim/closed_business.go); the TTL decay is applied by Observed.Active at read
// time so a stale "shut" naturally fades (the NPC retries) without the world
// goroutine sweeping the store. False when the subject has no such memory, the
// snapshot has no clock baseline, or the memory has expired.
func businessRememberedShut(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, structureID sim.StructureID) bool {
	if snap == nil || actorSnap == nil {
		return false
	}
	// Don't treat a place as remembered-shut while the actor is actively walking to
	// it (LLM-366): a mid-walk re-tick must not read a stale "shut" label and steer
	// the actor off the destination it just chose (ZBBS-HOME-405). The memory
	// survives for the NEXT decision; arrival re-observes the truth.
	if walkingToStructure(actorSnap, structureID) {
		return false
	}
	return actorSnap.Observed.Active(sim.ObservedStateKey{StructureID: structureID, Condition: sim.ObservedClosed}, snap.PublishedAt)
}

// outOfStockAnnotation is the in-world suffix appended to a buy cue for a
// (vendor, item) the subject remembers finding out of stock (ZBBS-HOME-363).
// Recalled experience, not a live read — it deprioritizes rather than forbids
// (the memory decays, and the vendor may have restocked since).
const outOfStockAnnotation = " — though you went there for it not long ago and found them out"

// businessRememberedOutOfStock reports whether the subject has a live
// experiential memory (ZBBS-HOME-363, within its TTL of the snapshot clock) of
// trying to buy itemKind at structureID and finding it out of stock. The
// per-(structure,item) sibling of businessRememberedShut: stamped + self-cleared
// by the PayWithItemResolved subscriber (sim/out_of_stock.go), TTL decay applied
// by Observed.Active at read time so a stale "dry" fades (the NPC retries).
func businessRememberedOutOfStock(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, structureID sim.StructureID, itemKind sim.ItemKind) bool {
	if snap == nil || actorSnap == nil {
		return false
	}
	// Same in-flight-destination guard as businessRememberedShut (LLM-366 /
	// ZBBS-HOME-405): don't let a stale "out of stock" memory steer the actor off a
	// shop it is currently walking to; arrival re-checks the shelves.
	if walkingToStructure(actorSnap, structureID) {
		return false
	}
	return actorSnap.Observed.Active(sim.ObservedStateKey{StructureID: structureID, ItemKind: itemKind, Condition: sim.ObservedOutOfStock}, snap.PublishedAt)
}

// walkingToStructure reports whether the actor's in-flight move targets
// structureID (an enter or a visit). It is the narrow HOME-405 guard for the
// remembered-shut / remembered-out-of-stock avoidance reads (LLM-366): an NPC
// weighing a cue about the very place it is walking to must not be steered off it
// by a stale memory — it chose to go re-check, and arrival re-observes the truth.
// Replaces the old commit-time memory wipe (move_to.go), which also erased the
// memory across decisions and let a workless NPC re-pick the same shut shop.
func walkingToStructure(actorSnap *sim.ActorSnapshot, structureID sim.StructureID) bool {
	if actorSnap == nil || structureID == "" {
		return false
	}
	return actorSnap.MoveDestStructureID == structureID &&
		(actorSnap.MoveDestKind == sim.MoveDestinationStructureEnter ||
			actorSnap.MoveDestKind == sim.MoveDestinationStructureVisit)
}
