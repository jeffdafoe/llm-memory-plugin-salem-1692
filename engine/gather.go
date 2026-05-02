package main

// Gather tool — produce a portable item from a refresh-bearing source
// the actor is loitering at. Sister mechanism to object_refresh's
// arrival-driven need decrement: object_refresh drops a need (drink at
// the well to slake thirst), gather credits inventory (fill a pail to
// carry water back). Both share the same source object so the world
// stays consistent — a real 1692 tavernkeeper visits the well to BOTH
// drink AND fill pails.
//
// Source mapping is tag-driven: the village_object's category tag
// determines what item the gather produces (well → water). The mapping
// is hardcoded for now; promotion to a metadata column is the right
// long-term path once a second gatherable lands.
//
// Bounded vs unbounded sources:
//   - Wells are unbounded (object_refresh has NULL available_quantity).
//     Gather always succeeds; draws are not tracked.
//   - Bounded sources (future: orchards, fishing spots) carry an
//     available_quantity that gather decrements. Empty bounds reject
//     with "the source is depleted right now". Continuous-refresh
//     mode replenishes over time via the existing refresh tick.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// gatherTagToItem maps a village_object_tag to the item_kind name a
// successful gather produces from objects bearing that tag. Single
// source of truth — the tool description, validation, and dispatch all
// read from here. Adding a new gatherable means adding one row.
var gatherTagToItem = map[string]string{
	"well": "water",
}

// gatherToleranceSq matches object_refresh's tolerance: the actor must
// be loitering at the source's anchor (within ~2 tiles) for the gather
// to apply. The walk system parks them at the source's loiter offset
// on chore=well, which is well within tolerance.
const gatherToleranceSq = 4096.0

// gatherResult captures the outcome of an attempted gather so the
// dispatcher can build the audit row and tool-result message without
// duplicating logic.
type gatherResult struct {
	Result string // "ok" | "rejected" | "failed"
	Err    string // human-readable, empty when ok
	// Resolved fields used for narration / audit. Empty on rejected /
	// failed.
	Item       string
	Qty        int
	SourceID   string
	SourceName string
}

// executeGather walks the validation chain: locate a nearby gatherable
// source, look up its product, decrement bounded quantity if any,
// credit the actor's inventory. Single transaction so a partial
// failure rolls back cleanly.
func (app *App) executeGather(ctx context.Context, actor *agentNPCRow, qty int) gatherResult {
	if qty <= 0 {
		qty = 1
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return gatherResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Find the nearest village_object the actor is loitering at that
	// (a) carries a gatherable tag and (b) has an object_refresh row.
	// Bounding-box pre-filter on x/y matches the pattern in
	// applyObjectRefreshAtArrival so the planner can prune at scale.
	// Box is generous (192px) to cover the case where the actor is at
	// the source's loiter offset, which can put the anchor several tiles
	// away on each axis. The squared-distance check below uses the
	// loiter point and a tight gatherToleranceSq for actual eligibility.
	const tolerancePx = 192.0
	tagList := make([]string, 0, len(gatherTagToItem))
	for tag := range gatherTagToItem {
		tagList = append(tagList, tag)
	}

	// Distance is computed to the loiter point (anchor + loiter_offset *
	// 32) when set, falling back to the anchor when not. Wells, market
	// stalls, and similar entry_policy='none' sources park visitors at
	// the loiter offset; checking against the anchor would put a
	// correctly-arrived NPC outside the tolerance window. Tile size is
	// 32px (matches the rest of the engine's positioning math).
	const tileSize = 32.0
	var (
		objectID   string
		objectName string
		objectTag  string
		distSq     float64
	)
	err = tx.QueryRow(ctx,
		`SELECT o.id::text,
		        COALESCE(o.display_name, a.name),
		        vot.tag,
		        (o.x + COALESCE(o.loiter_offset_x, 0) * $5 - $1) *
		        (o.x + COALESCE(o.loiter_offset_x, 0) * $5 - $1) +
		        (o.y + COALESCE(o.loiter_offset_y, 0) * $5 - $2) *
		        (o.y + COALESCE(o.loiter_offset_y, 0) * $5 - $2) AS dist_sq
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 JOIN village_object_tag vot ON vot.object_id = o.id
		 WHERE o.x BETWEEN $1 - $3 AND $1 + $3
		   AND o.y BETWEEN $2 - $3 AND $2 + $3
		   AND vot.tag = ANY($4)
		   AND EXISTS (SELECT 1 FROM object_refresh r WHERE r.object_id = o.id)
		 ORDER BY dist_sq
		 LIMIT 1`,
		actor.CurrentX, actor.CurrentY, tolerancePx, tagList, tileSize,
	).Scan(&objectID, &objectName, &objectTag, &distSq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gatherResult{Result: "rejected", Err: "no source to gather from here — walk to a well or other source first"}
		}
		return gatherResult{Result: "failed", Err: fmt.Sprintf("locate source: %v", err)}
	}
	if distSq > gatherToleranceSq {
		return gatherResult{Result: "rejected", Err: "you're not close enough to the source — loiter at it first"}
	}

	produces, ok := gatherTagToItem[objectTag]
	if !ok {
		return gatherResult{Result: "rejected", Err: fmt.Sprintf("no known product from %s", objectName)}
	}

	// Bounded-quantity check. Wells today are unbounded
	// (available_quantity NULL). When a source is bounded we lock the
	// row and decrement, returning a clean rejection if it's empty.
	var available, maxQty sql.NullInt32
	if err := tx.QueryRow(ctx,
		`SELECT available_quantity, max_quantity
		   FROM object_refresh
		  WHERE object_id = $1
		  ORDER BY attribute LIMIT 1
		  FOR UPDATE`,
		objectID,
	).Scan(&available, &maxQty); err != nil {
		return gatherResult{Result: "failed", Err: fmt.Sprintf("lock refresh: %v", err)}
	}
	if available.Valid {
		if int(available.Int32) < qty {
			return gatherResult{Result: "rejected", Err: fmt.Sprintf("%s is depleted right now", objectName)}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE object_refresh
			    SET available_quantity = available_quantity - $2
			  WHERE object_id = $1`,
			objectID, qty,
		); err != nil {
			return gatherResult{Result: "failed", Err: fmt.Sprintf("decrement quantity: %v", err)}
		}
	}

	// Credit the actor's inventory.
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (actor_id, item_kind)
		 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
		actor.ID, produces, qty,
	); err != nil {
		return gatherResult{Result: "failed", Err: fmt.Sprintf("credit inventory: %v", err)}
	}

	if err := tx.Commit(ctx); err != nil {
		return gatherResult{Result: "failed", Err: fmt.Sprintf("commit: %v", err)}
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  actor.ID,
			"item_kind": produces,
		},
	})

	return gatherResult{
		Result:     "ok",
		Item:       produces,
		Qty:        qty,
		SourceID:   objectID,
		SourceName: objectName,
	}
}

// gatherableHereForActor returns a perception line announcing the
// gatherable source the actor is currently loitering at, or "" when
// they aren't standing at one. Used by buildAgentPerception to surface
// the affordance — without this hint the model often stands at a
// source and doesn't connect "I'm here" to "I should call gather()".
//
// Source matching mirrors executeGather: bounding-box prefilter on the
// actor's position, then squared distance against the loiter point
// (anchor + loiter_offset * tile) compared to gatherToleranceSq.
func (app *App) gatherableHereForActor(ctx context.Context, actorX, actorY float64) string {
	const tolerancePx = 192.0
	const tileSize = 32.0
	tagList := make([]string, 0, len(gatherTagToItem))
	for tag := range gatherTagToItem {
		tagList = append(tagList, tag)
	}
	var (
		objectName string
		objectTag  string
		distSq     float64
	)
	err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.display_name, a.name),
		        vot.tag,
		        (o.x + COALESCE(o.loiter_offset_x, 0) * $5 - $1) *
		        (o.x + COALESCE(o.loiter_offset_x, 0) * $5 - $1) +
		        (o.y + COALESCE(o.loiter_offset_y, 0) * $5 - $2) *
		        (o.y + COALESCE(o.loiter_offset_y, 0) * $5 - $2) AS dist_sq
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 JOIN village_object_tag vot ON vot.object_id = o.id
		 WHERE o.x BETWEEN $1 - $3 AND $1 + $3
		   AND o.y BETWEEN $2 - $3 AND $2 + $3
		   AND vot.tag = ANY($4)
		   AND EXISTS (SELECT 1 FROM object_refresh r WHERE r.object_id = o.id)
		 ORDER BY dist_sq
		 LIMIT 1`,
		actorX, actorY, tolerancePx, tagList, tileSize,
	).Scan(&objectName, &objectTag, &distSq)
	if err != nil || distSq > gatherToleranceSq {
		return ""
	}
	produces, ok := gatherTagToItem[objectTag]
	if !ok {
		return ""
	}
	return fmt.Sprintf("You are loitering at the %s — call gather to fill your inventory with %s here.", objectName, produces)
}

// gatherToolSourceLine renders a one-line summary of where each
// gatherable tag's product comes from, used in the tool description so
// the model knows what to expect. Sorted for stability so test/snapshot
// diffs are clean if we add a second source later.
func gatherToolSourceLine() string {
	parts := make([]string, 0, len(gatherTagToItem))
	for tag, item := range gatherTagToItem {
		parts = append(parts, fmt.Sprintf("%s → %s", tag, item))
	}
	// Stable order by tag.
	for i := 1; i < len(parts); i++ {
		for j := i; j > 0 && parts[j-1] > parts[j]; j-- {
			parts[j-1], parts[j] = parts[j], parts[j-1]
		}
	}
	return strings.Join(parts, ", ")
}

