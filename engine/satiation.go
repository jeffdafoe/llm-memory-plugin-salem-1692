package main

// Satiation perception block (ZBBS-123).
//
// When an NPC has a tier ≥ 2 pressing need (Address now: hunger /
// thirst), this builder surfaces a short readout that bridges the
// need-pressure signal with the resolution paths the engine actually
// supports: consume something from your own stock, or walk to a
// nearby vendor stocked with satisfiers.
//
// Diagnosed from a 2026-05-05 chat scene where Prudence Ward — a
// herbalist hungry on shift at her own apothecary, holding 50
// berries (food / hunger / +4) — committed move_to(General Store)
// instead of consume(berries). The body line said "Address now:
// hunger" and the inventory line said "Items you can sell: berries
// x50" but those two facts never connected in the model's choice.
// The vendor relabel from ZBBS-114 ("Items you can sell") primed
// the inventory as merchandise, not food, and the action menu in
// the decision prompt didn't list `consume` so the LLM picked a
// move-shaped tool from the explicit options.
//
// This block doesn't replace the existing inventory line — that
// remains the canonical "what you carry" readout. It augments it
// with an analytical layer ("of those, these would settle the
// pressing need; alternatives are at these nearby vendors").
//
// Felt-language framing: no raw integers, no price quotes (the item
// catalog dropped price in ZBBS-092 and didn't restore it; vendors
// negotiate in conversation). Item display labels are downcased to
// read naturally inside prose ("berries" not "Berries").
//
// Scope: hunger and thirst only — these have entries in the item
// catalog with satisfies_attribute set. Tiredness has no consumable
// satisfier yet (sleep handling comes later) so it's silently
// skipped here even when pressing.

import (
	"context"
	"log"
	"strings"
)

// satiationVendor is one nearby place + the items its on-station
// vendor has that would settle the pressing need. StructureName is
// COALESCE(display_name, asset.name) — same labeling the destinations
// section uses, so prose references resolve via move_to consistently.
type satiationVendor struct {
	StructureName string
	VendorName    string
	Items         []string
}

// buildSatiationLines composes one prose line per pressing consumable
// need. Empty when no consumable need is pressing or no satisfiers
// exist anywhere visible. Each line is self-contained — own stock,
// then nearby vendors — so the perception assembler can drop them
// into the section list without further conditioning.
//
// Order: hunger first, then thirst. Mirrors the order pressing needs
// appear in the body line for consistency.
func (app *App) buildSatiationLines(ctx context.Context, perceiverID string, perceiverX, perceiverY float64, currentStructureID string, pressingNeeds []string) []string {
	if len(pressingNeeds) == 0 {
		return nil
	}
	var lines []string
	for _, need := range orderedConsumableNeeds(pressingNeeds) {
		own := app.satiationOwnInventory(ctx, perceiverID, need)
		nearby := app.satiationNearbyVendors(ctx, perceiverID, need, perceiverX, perceiverY, currentStructureID)
		if len(own) == 0 && len(nearby) == 0 {
			continue
		}

		verb := "eat"
		if need == "thirst" {
			verb = "drink"
		}

		var parts []string
		if len(own) > 0 {
			parts = append(parts, "You have "+joinWithAnd(own)+" in your own stock — consume to "+verb+" and settle "+need+".")
		}
		if len(nearby) > 0 {
			var clauses []string
			for _, v := range nearby {
				clauses = append(clauses, "at "+v.StructureName+", "+v.VendorName+" has "+joinWithAnd(v.Items))
			}
			parts = append(parts, "Nearby: "+strings.Join(clauses, "; ")+".")
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return lines
}

// orderedConsumableNeeds filters pressingNeeds down to those with a
// satisfier in the item catalog (hunger, thirst) and returns them in
// a stable order (hunger then thirst). Tiredness is silently dropped
// — surfacing it here would be noise; the body line already names it
// and "Address now" still flags it for the model.
func orderedConsumableNeeds(pressing []string) []string {
	have := map[string]bool{}
	for _, n := range pressing {
		have[n] = true
	}
	var out []string
	for _, n := range []string{"hunger", "thirst"} {
		if have[n] {
			out = append(out, n)
		}
	}
	return out
}

// satiationOwnInventory returns the display labels (lowercased) of
// items in the perceiver's inventory whose satisfies_attribute matches
// the given need, ordered by satisfies_amount descending so the
// strongest satisfier reads first ("stew and bread" rather than
// "bread and stew"). Errors log and surface as empty so a transient
// DB hiccup doesn't break the perception build.
func (app *App) satiationOwnInventory(ctx context.Context, actorID, need string) []string {
	rows, err := app.DB.Query(ctx, `
        SELECT ik.display_label
          FROM actor_inventory inv
          JOIN item_kind ik ON ik.name = inv.item_kind
         WHERE inv.actor_id = $1::uuid
           AND inv.quantity > 0
           AND ik.satisfies_attribute = $2
         ORDER BY ik.satisfies_amount DESC, ik.display_label
    `, actorID, need)
	if err != nil {
		log.Printf("satiationOwnInventory %s/%s: %v", actorID, need, err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			log.Printf("satiationOwnInventory scan: %v", err)
			continue
		}
		out = append(out, strings.ToLower(label))
	}
	return out
}

// satiationNearbyVendors returns up to four nearest structures (by
// Euclidean distance from the perceiver's current position) that have
// a vendor currently inside whose inventory contains satisfiers for
// the pressing need. "Vendor" is anyone whose held attributes declare
// the 'serve' tool — same definition actorIsVendor uses for the
// inventory-line relabel.
//
// Excludes the perceiver themselves (so a vendor at their own shop
// doesn't see "at PW Apothecary, Prudence Ward has water" in their
// own perception — they're already aware of their own stock via the
// own-inventory line above).
//
// Cap of 4: enough to give the model real choice without ballooning
// the section into a market index. The destinations line ("Other
// places nearby") still surfaces every nearby place for general
// context; the satiation block is the curated subset relevant to
// the pressing need.
func (app *App) satiationNearbyVendors(ctx context.Context, perceiverID, need string, x, y float64, currentStructureID string) []satiationVendor {
	rows, err := app.DB.Query(ctx, `
        WITH vendors_with_serve AS (
            SELECT DISTINCT aa.actor_id
              FROM actor_attribute aa
              JOIN attribute_definition ad ON ad.slug = aa.slug
             WHERE ad.tools ? 'serve'
        ),
        candidates AS (
            SELECT s.id AS structure_id,
                   COALESCE(s.display_name, ass.name) AS structure_name,
                   a.id::text AS vendor_id,
                   a.display_name AS vendor_name,
                   ik.display_label,
                   ik.satisfies_amount,
                   s.x AS sx, s.y AS sy
              FROM village_object s
              JOIN asset ass ON ass.id = s.asset_id
              JOIN actor a ON a.inside_structure_id = s.id
              JOIN vendors_with_serve v ON v.actor_id = a.id
              JOIN actor_inventory inv ON inv.actor_id = a.id AND inv.quantity > 0
              JOIN item_kind ik ON ik.name = inv.item_kind
             WHERE ik.satisfies_attribute = $1
               AND a.id != $2::uuid
               AND ($3 = '' OR s.id::text != $3)
        )
        SELECT structure_id::text, structure_name, vendor_name,
               array_agg(display_label ORDER BY satisfies_amount DESC, display_label) AS items,
               sx, sy
          FROM candidates
         GROUP BY structure_id, structure_name, vendor_id, vendor_name, sx, sy
         ORDER BY (sx - $4) * (sx - $4) + (sy - $5) * (sy - $5)
         LIMIT 4
    `, need, perceiverID, currentStructureID, x, y)
	if err != nil {
		log.Printf("satiationNearbyVendors %s: %v", need, err)
		return nil
	}
	defer rows.Close()
	var out []satiationVendor
	for rows.Next() {
		var sid, sname, vname string
		var items []string
		var sx, sy float64
		if err := rows.Scan(&sid, &sname, &vname, &items, &sx, &sy); err != nil {
			log.Printf("satiationNearbyVendors scan: %v", err)
			continue
		}
		for i, it := range items {
			items[i] = strings.ToLower(it)
		}
		out = append(out, satiationVendor{
			StructureName: sname,
			VendorName:    vname,
			Items:         items,
		})
	}
	return out
}

// joinWithAnd renders a list of strings as natural English: "" / "a" /
// "a and b" / "a, b, and c" (Oxford comma). Used by the satiation
// readout for both own-stock and per-vendor item lists.
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}
