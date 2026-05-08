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
// Felt-language framing: no raw integers, no exact prices. Severity
// of the need surfaces as a tier-aware lead phrase ("Hunger is
// gnawing — a hearty meal would clear it"). Each item carries its
// own felt-magnitude qualifier ("berries (a small bite)", "stew (a
// hearty meal)") so the model can weigh "settle from own stock vs
// walk to a heartier meal nearby." Non-portable items get an
// "eaten there" hint so the model knows stew at the tavern can't
// be carried home — must consume on the spot.
//
// Scope: hunger and thirst only — these have entries in the item
// catalog with satisfies_attribute set. Tiredness has no consumable
// satisfier yet (sleep handling comes later) so it's silently
// skipped here even when pressing.

import (
	"context"
	"log"
	"math"
	"strings"
)

// satiationItem is one entry in either own-stock or a vendor's stock,
// carrying enough info for the prose builder to render its felt
// magnitude phrase and any portability caveat.
type satiationItem struct {
	Label    string // lowercased display_label, e.g. "berries"
	Amount   int    // item_kind.satisfies_amount, drives felt phrase
	Portable bool   // 'portable' in item_kind.capabilities array
}

// satiationVendor is one nearby place + the items its on-station
// vendor has that would settle the pressing need. StructureName is
// COALESCE(display_name, asset.name) — same labeling the destinations
// section uses, so prose references resolve via move_to consistently.
//
// DistanceTiles is Euclidean from the perceiver's current position to
// the structure's anchor, in tile units (pixels / 32). Drives the
// felt proximity phrase ("close at hand" / "a fair walk away") so
// the model can weigh own-stock vs travel-and-buy against the
// movement fatigue cost (ZBBS-123) every walk now charges.
type satiationVendor struct {
	StructureName string
	VendorName    string
	DistanceTiles float64
	Items         []satiationItem
}

// buildSatiationLines composes one prose block per pressing consumable
// need. Empty when no consumable need is pressing or no satisfiers
// exist anywhere visible. Each block is multi-line — lead phrase,
// own-stock line, nearby-satisfiers line — joined with newlines so
// the perception assembler can drop them into the section list as a
// single section.
//
// Order: hunger first, then thirst. Mirrors the order pressing needs
// appear in the body line for consistency.
//
// The pressingTiers map carries the tier (NeedRed or NeedPeak) per
// pressing need so the lead phrase can scale severity — "gnawing"
// vs "starving" — without the model reading a raw integer.
func (app *App) buildSatiationLines(ctx context.Context, perceiverID string, perceiverX, perceiverY float64, pressingTiers map[string]NeedTier) []string {
	if len(pressingTiers) == 0 {
		return nil
	}
	var blocks []string
	for _, need := range []string{"hunger", "thirst"} {
		tier, ok := pressingTiers[need]
		if !ok {
			continue
		}
		own := app.satiationOwnInventory(ctx, perceiverID, need)
		nearby := app.satiationNearbyVendors(ctx, perceiverID, need, perceiverX, perceiverY)
		if len(own) == 0 && len(nearby) == 0 {
			continue
		}

		verb := "eat"
		if need == "thirst" {
			verb = "drink"
		}

		var lines []string
		if lead := needLeadPhrase(need, tier); lead != "" {
			lines = append(lines, lead)
		}
		if len(own) > 0 {
			lines = append(lines, "You have "+renderItemList(own, need, false)+
				" in your own stock — consume to "+verb+".")
		}
		if len(nearby) > 0 {
			var bullets []string
			for _, v := range nearby {
				bullets = append(bullets,
					"— At "+v.StructureName+" ("+proximityPhrase(v.DistanceTiles)+"), "+
						v.VendorName+" has "+renderItemList(v.Items, need, true)+".")
			}
			lines = append(lines, "Nearby satisfiers:")
			lines = append(lines, bullets...)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return blocks
}

// renderItemList formats a list of satiationItems with per-item felt
// qualifiers, using Oxford-comma joining. With showPortability=true
// (vendor lists), non-portable items pick up an "eaten there" /
// "drunk there" suffix inside their parens so the model knows they
// can't be carried home from a purchase — only consumed on site.
// With showPortability=false (own-stock lists) the hint is suppressed
// because consume() works from inventory regardless of capabilities;
// portability is a purchase-flow concern, not a consumption one.
//
//	"berries (a small bite)"
//	"berries (a small bite) and meat (a hearty meal)"
//	"stew (a hearty meal — eaten there), bread (a meal), and ale (a drink)"
func renderItemList(items []satiationItem, need string, showPortability bool) string {
	rendered := make([]string, len(items))
	for i, it := range items {
		phrase := itemFeltAmount(it.Amount, need)
		if showPortability && !it.Portable {
			suffix := "eaten there"
			if need == "thirst" {
				suffix = "drunk there"
			}
			phrase = phrase + " — " + suffix
		}
		rendered[i] = it.Label + " (" + phrase + ")"
	}
	switch len(rendered) {
	case 0:
		return ""
	case 1:
		return rendered[0]
	case 2:
		return rendered[0] + " and " + rendered[1]
	default:
		return strings.Join(rendered[:len(rendered)-1], ", ") + ", and " + rendered[len(rendered)-1]
	}
}

// proximityPhrase maps Euclidean tile distance to a felt-language
// phrase. Bands sized so an in-village hop reads as "close at hand"
// (negligible fatigue under the default per-tile cost), an adjacent
// district is "a short walk", and the map's edge feels real.
//
// Calibrated against the default movement_fatigue_per_tile_x100=12:
// a 6-tile hop costs 0 (floors), a 20-tile walk costs ~2, a 60-tile
// trek costs ~7 — enough that the "long walk" framing is honest by
// the time the model reads it.
func proximityPhrase(tiles float64) string {
	switch {
	case tiles <= 6:
		return "close at hand"
	case tiles <= 20:
		return "a short walk"
	case tiles <= 50:
		return "a fair walk"
	default:
		return "a long walk"
	}
}

// itemFeltAmount returns the felt-language phrase for an item's
// satisfies_amount magnitude. Hunger and thirst use different scales
// — "a hearty meal" reads naturally for stew (food) but not ale,
// where "a deep drink" fits. Calibrated against the seed item
// catalog (ZBBS-091 / ZBBS-093):
//
//	hunger: berries 4, cheese 6, bread 8, meat 10, stew 12
//	thirst: water 4, milk 6, ale 8
//
// Bands sized so each item lands in a distinct phrase.
func itemFeltAmount(amount int, need string) string {
	if need == "thirst" {
		switch {
		case amount <= 4:
			return "a sip"
		case amount <= 7:
			return "a drink"
		default:
			return "a deep drink"
		}
	}
	switch {
	case amount <= 4:
		return "a small bite"
	case amount <= 7:
		return "a meal"
	case amount <= 11:
		return "a hearty meal"
	default:
		return "a feast"
	}
}

// needLeadPhrase composes the tier-aware lead-in for the satiation
// block. Empty for tiers below NeedRed — the satiation block doesn't
// fire there, so this only ever returns a phrase for red or peak.
//
// The "would clear it" / "you need..." framing tells the model what
// scale of resolution to aim for — at red, a hearty meal closes it;
// at peak, anything will help but a full meal is still better.
func needLeadPhrase(need string, tier NeedTier) string {
	if need == "thirst" {
		switch tier {
		case NeedRed:
			return "Thirst is pressing — a real drink would clear it."
		case NeedPeak:
			return "You're parched — you need to drink, anything will help."
		}
		return ""
	}
	switch tier {
	case NeedRed:
		return "Hunger is gnawing — a hearty meal would clear it."
	case NeedPeak:
		return "You're starving — you need food, anything will help."
	}
	return ""
}

// satiationOwnInventory returns items in the perceiver's inventory
// that satisfy the given need (via item_satisfies), ordered by the
// per-need amount descending so the strongest satisfier reads first.
// With multi-effect items (ZBBS-125), an item like ale is returned
// for both 'thirst' and 'hunger' queries, but with the per-attribute
// amount each time — ale shows as a 4-thirst satisfier under 'thirst'
// and a 2-hunger one under 'hunger'. Errors log and surface as empty
// so a transient DB hiccup doesn't break the perception build.
func (app *App) satiationOwnInventory(ctx context.Context, actorID, need string) []satiationItem {
	rows, err := app.DB.Query(ctx, `
		SELECT ik.display_label, isf.amount,
		       COALESCE('portable' = ANY(ik.capabilities), false) AS portable
		  FROM actor_inventory inv
		  JOIN item_kind ik ON ik.name = inv.item_kind
		  JOIN item_satisfies isf ON isf.item_kind = ik.name
		 WHERE inv.actor_id = $1::uuid
		   AND inv.quantity > 0
		   AND isf.attribute = $2
		 ORDER BY isf.amount DESC, ik.display_label
	`, actorID, need)
	if err != nil {
		log.Printf("satiationOwnInventory %s/%s: %v", actorID, need, err)
		return nil
	}
	defer rows.Close()
	var out []satiationItem
	for rows.Next() {
		var label string
		var amount int
		var portable bool
		if err := rows.Scan(&label, &amount, &portable); err != nil {
			log.Printf("satiationOwnInventory scan: %v", err)
			continue
		}
		out = append(out, satiationItem{
			Label:    strings.ToLower(label),
			Amount:   amount,
			Portable: portable,
		})
	}
	return out
}

// satiationNearbyVendors returns up to four nearest vendor entries
// (one bullet per vendor; if multiple vendors share a structure each
// gets its own bullet) by Euclidean distance from the perceiver's
// current position. A "vendor" here is an NPC whose held attributes
// declare the 'serve' tool — same definition actorIsVendor uses for
// the inventory-line relabel. PCs are filtered out: even if a player
// were to acquire a serve attribute, the satiation block shouldn't
// advertise PC inventory as buyable vendor stock until a real
// PC-to-NPC purchase flow exists.
//
// Excludes the perceiver themselves (so a vendor at their own shop
// doesn't see their own stock listed under "Nearby satisfiers" —
// their own-inventory line above already covered it). Vendors at
// the perceiver's *current* structure are NOT excluded, so a hungry
// villager sitting in the Tavern still reads that John Ellis has
// stew there; proximityPhrase renders the distance honestly as
// "close at hand".
//
// Cap of 4: enough to give the model real choice without ballooning
// the section into a market index. The destinations line ("Other
// places nearby") still surfaces every nearby place for general
// context; the satiation block is the curated subset relevant to
// the pressing need.
//
// Items returned per vendor carry their own satisfies_amount and
// portability so renderItemList can attach per-item felt phrases
// and the "eaten there" hint for non-portables.
func (app *App) satiationNearbyVendors(ctx context.Context, perceiverID, need string, x, y float64) []satiationVendor {
	rows, err := app.DB.Query(ctx, `
		WITH vendors_with_serve AS (
			SELECT DISTINCT aa.actor_id
			  FROM actor_attribute aa
			  JOIN attribute_definition ad ON ad.slug = aa.slug
			 WHERE ad.tools ? 'serve'
		),
		ranked AS (
			SELECT s.id AS structure_id,
			       COALESCE(s.display_name, ass.name) AS structure_name,
			       a.id::text AS vendor_id,
			       a.display_name AS vendor_name,
			       s.x AS sx, s.y AS sy,
			       (s.x - $3) * (s.x - $3) + (s.y - $4) * (s.y - $4) AS dist_sq
			  FROM village_object s
			  JOIN asset ass ON ass.id = s.asset_id
			  JOIN actor a ON a.inside_structure_id = s.id
			  JOIN vendors_with_serve v ON v.actor_id = a.id
			 WHERE a.id != $2::uuid
			   AND a.login_username IS NULL
			   -- Closed-shop suppression: a vendor on take_break shouldn't
			   -- surface in the buyer's "where to find a hearty meal" cue.
			   -- Pairs with executePay's break gate so the LLM doesn't
			   -- walk to a closed shop or pile up paid-but-undelivered
			   -- orders. Vendors with NULL break_until or break_until in
			   -- the past stay visible.
			   AND (a.break_until IS NULL OR a.break_until <= NOW())
			   AND EXISTS (
			       SELECT 1
			         FROM actor_inventory inv
			         JOIN item_satisfies isf ON isf.item_kind = inv.item_kind
			        WHERE inv.actor_id = a.id
			          AND inv.quantity > 0
			          AND isf.attribute = $1
			   )
			 ORDER BY dist_sq
			 LIMIT 4
		)
		SELECT r.structure_id::text, r.structure_name, r.vendor_name,
		       ik.display_label, isf.amount,
		       COALESCE('portable' = ANY(ik.capabilities), false) AS portable,
		       r.dist_sq
		  FROM ranked r
		  JOIN actor_inventory inv ON inv.actor_id::text = r.vendor_id
		  JOIN item_kind ik ON ik.name = inv.item_kind
		  JOIN item_satisfies isf ON isf.item_kind = ik.name
		 WHERE inv.quantity > 0
		   AND isf.attribute = $1
		 ORDER BY r.dist_sq, isf.amount DESC, ik.display_label
	`, need, perceiverID, x, y)
	if err != nil {
		log.Printf("satiationNearbyVendors %s: %v", need, err)
		return nil
	}
	defer rows.Close()
	type vendorKey struct {
		structureID string
		vendorName  string
	}
	bucket := map[vendorKey]*satiationVendor{}
	var order []vendorKey
	for rows.Next() {
		var sid, sname, vname, label string
		var amount int
		var portable bool
		var distSq float64
		if err := rows.Scan(&sid, &sname, &vname, &label, &amount, &portable, &distSq); err != nil {
			log.Printf("satiationNearbyVendors scan: %v", err)
			continue
		}
		key := vendorKey{structureID: sid, vendorName: vname}
		v, ok := bucket[key]
		if !ok {
			// dist_sq is in pixel² (world coords are pixels with
			// tileSize=32); convert to tiles for the felt phrase.
			const tileSize = 32.0
			tiles := math.Sqrt(distSq) / tileSize
			v = &satiationVendor{
				StructureName: sname,
				VendorName:    vname,
				DistanceTiles: tiles,
			}
			bucket[key] = v
			order = append(order, key)
		}
		v.Items = append(v.Items, satiationItem{
			Label:    strings.ToLower(label),
			Amount:   amount,
			Portable: portable,
		})
	}
	out := make([]satiationVendor, 0, len(order))
	for _, k := range order {
		out = append(out, *bucket[k])
	}
	return out
}
