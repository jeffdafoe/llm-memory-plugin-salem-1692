package main

// Gatherable nodes — small fixed finds scattered on the map.
//
// ZBBS-155. Schema lives in migrations/ZBBS-155-gatherable-nodes_up.sql.
// The PC walks past, finds an item on the path, gets it added to their
// inventory. Respawns after a cooldown so the village isn't picked
// clean. PC-only by gating in the call site (npc_movement.go's
// applyArrivalSideEffects); NPCs walk past unaffected.
//
// Proximity: a gatherable is "near" the arriver if within
// gatherableProximityTiles tiles of their final position. The map runs
// at tileSize px per tile (32 in current config); the proximity query
// uses a px bounding box for the integer x/y columns and then refines
// with Chebyshev distance.
//
// Pickup contract:
//   - last_picked_at is NULL (never picked) OR < NOW() - respawn_seconds.
//   - Atomic claim: UPDATE...SET last_picked_at = NOW() WHERE id = ...
//     AND (last_picked_at IS NULL OR last_picked_at < NOW() -
//     (respawn_seconds * INTERVAL '1 second')) RETURNING. Two arrivals
//     racing the same node — only one stamps successfully, the other's
//     UPDATE matches zero rows and skips silently.
//   - Per-pickup, we add qty to actor_inventory via the existing ON
//     CONFLICT DO UPDATE pattern, then broadcast room_event for the
//     "Jefferey found berries on the path" line.
//
// No engine-side respawn timer needed — the gating is read-side only.
// A picked node stays "claimed" by virtue of last_picked_at; the next
// pickup attempt past the cooldown picks it up cleanly.

import (
	"context"
	"fmt"
	"log"
	"time"
)

// gatherableProximityTiles is how close (in tiles) a PC has to be to a
// node for it to be picked up on arrival. 3 tiles ≈ 96 px is a generous
// "you walked past it" radius — enough to cover small routing offsets
// without making nodes trivially auto-collected from across the map.
const gatherableProximityTiles = 3

// gatherablePickup describes one node successfully picked up during
// an arrival sweep. Surfaced so the caller can compose narration or
// notification UX in one place.
type gatherablePickup struct {
	NodeID       int64
	ItemKind     string
	Qty          int
	DisplayLabel string
}

// pickupNearbyGatherables runs the PC's arrival-pickup sweep. Caller is
// responsible for gating to PCs — see applyArrivalSideEffects in
// npc_movement.go. Best-effort: errors logged, not propagated; partial
// pickups commit individually so a transient failure on one node
// doesn't abort the rest.
//
// Reads the configured tileSize from the engine's runtime constant so
// the proximity bounding box stays in sync if tile geometry changes
// later.
func (app *App) pickupNearbyGatherables(ctx context.Context, actorID, actorName string, x, y float64) []gatherablePickup {
	radiusPx := float64(gatherableProximityTiles) * tileSize
	minX := int(x - radiusPx - 1)
	maxX := int(x + radiusPx + 1)
	minY := int(y - radiusPx - 1)
	maxY := int(y + radiusPx + 1)

	rows, err := app.DB.Query(ctx,
		`SELECT id, x, y, item_kind, qty, COALESCE(display_label, ''), respawn_seconds
		   FROM gatherable_node
		  WHERE x BETWEEN $1 AND $2
		    AND y BETWEEN $3 AND $4
		    AND (last_picked_at IS NULL
		         OR last_picked_at < NOW() - (respawn_seconds * INTERVAL '1 second'))`,
		minX, maxX, minY, maxY,
	)
	if err != nil {
		log.Printf("gatherable: candidate query for %s: %v", actorID, err)
		return nil
	}
	type cand struct {
		id           int64
		x, y         int
		itemKind     string
		qty          int
		displayLabel string
		respawnSec   int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.x, &c.y, &c.itemKind, &c.qty, &c.displayLabel, &c.respawnSec); err != nil {
			log.Printf("gatherable: scan candidate for %s: %v", actorID, err)
			continue
		}
		cands = append(cands, c)
	}
	rows.Close()

	var picked []gatherablePickup
	for _, c := range cands {
		// Refine with Chebyshev distance (the bounding box was loose).
		if abs(float64(c.x)-x) > radiusPx || abs(float64(c.y)-y) > radiusPx {
			continue
		}
		// Atomic claim. Re-checks the cooldown predicate so a concurrent
		// arrival picking the same node loses gracefully.
		var stamped time.Time
		err := app.DB.QueryRow(ctx,
			`UPDATE gatherable_node
			    SET last_picked_at = NOW()
			  WHERE id = $1
			    AND (last_picked_at IS NULL
			         OR last_picked_at < NOW() - (respawn_seconds * INTERVAL '1 second'))
			 RETURNING last_picked_at`,
			c.id,
		).Scan(&stamped)
		if err != nil {
			// pgx.ErrNoRows when another pickup beat us. Silent skip.
			continue
		}
		// Credit the buyer's inventory. Same ON CONFLICT pattern used by
		// pay_ledger take-home and the lodging foundation seed.
		if _, err := app.DB.Exec(ctx,
			`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
			 VALUES ($1::uuid, $2, $3)
			 ON CONFLICT (actor_id, item_kind)
			 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
			actorID, c.itemKind, c.qty,
		); err != nil {
			log.Printf("gatherable: credit inventory %s += %d %s: %v", actorID, c.qty, c.itemKind, err)
			continue
		}
		// Compose narration text. display_label drives the flavor; falls
		// back to a generic line when not set.
		var text string
		if c.displayLabel != "" {
			text = fmt.Sprintf("%s gathered %s — %s.", actorName, formatQtyItem(c.qty, c.itemKind), c.displayLabel)
		} else {
			text = fmt.Sprintf("%s gathered %s on the path.", actorName, formatQtyItem(c.qty, c.itemKind))
		}
		app.Hub.Broadcast(WorldEvent{
			Type: "room_event",
			Data: map[string]any{
				"actor_id":   actorID,
				"actor_name": actorName,
				"kind":       "gather",
				"text":       text,
				"x":          c.x,
				"y":          c.y,
				"at":         time.Now().UTC().Format(time.RFC3339),
			},
		})
		// Inventory broadcast so any open inventory UI refreshes.
		app.Hub.Broadcast(WorldEvent{
			Type: "actor_inventory_changed",
			Data: map[string]any{
				"actor_id":  actorID,
				"item_kind": c.itemKind,
			},
		})
		log.Printf("gatherable: %s picked node=%d item=%s qty=%d at (%d,%d)",
			actorName, c.id, c.itemKind, c.qty, c.x, c.y)
		picked = append(picked, gatherablePickup{
			NodeID:       c.id,
			ItemKind:     c.itemKind,
			Qty:          c.qty,
			DisplayLabel: c.displayLabel,
		})
	}
	return picked
}

// formatQtyItem renders "berries", "2 berries", etc. — same shape as
// the pay/consume narration uses, but local to this file so future
// gatherable formatting changes don't ripple.
func formatQtyItem(qty int, item string) string {
	if qty <= 1 {
		return item
	}
	return fmt.Sprintf("%d %s", qty, item)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
