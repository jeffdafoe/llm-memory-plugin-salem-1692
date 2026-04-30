package main

// Object-refresh — arrival-driven need decrement on village_object.
//
// Sister mechanism to pay.go's consumption side-effect: where pay drops
// hunger/thirst as part of a counterparty transaction (NPC buys ale from
// John the innkeeper), object-refresh drops attributes when an actor
// arrives at an inanimate object configured with refresh rows.
//
// Examples:
//   - Well: a refresh row {thirst, -24} resets thirst on arrival.
//   - Fruit tree: {hunger, -24} (or smaller for berries / less filling).
//   - Shaded oak: two rows — {tiredness, -12} and {hunger, -8} for shade
//     plus acorns. Multiple rows per object are explicitly supported.
//   - Dry well: zero rows. Object exists, no refresh effect.
//
// Trigger: applyArrival in npc_movement.go fires applyObjectRefreshAtArrival
// after the actor's position update is committed and before the cascade
// tick. Spatial lookup (no walk-state plumbing) so PC and NPC arrivals
// share the path. The 2-tile tolerance covers loiter offset + jitter from
// pickWalkTarget; map placement avoids overlapping refresh objects.
//
// Refresh attribute config does NOT surface to the LLM perception. The
// character infers from world knowledge ("a fruit tree" → can be eaten);
// the engine just makes the consequence honest.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// Squared tolerance (in pixel units) for matching arrival point to a
// refresh-tagged object. Visitor walks land at (anchor + loiter*32 +
// jitter); jitter is ±half-tile (~16 px). Two tiles squared = 64*64 =
// 4096 gives comfortable slack without false positives at typical
// inter-object spacing.
const objectRefreshToleranceSq = 4096.0

// refreshHit captures one applied attribute drop for the action-log
// payload and Hub broadcast. amount is the configured signed delta;
// newValue is the post-clamp result.
type refreshHit struct {
	Attribute string `json:"attribute"`
	Amount    int    `json:"amount"`
	NewValue  int    `json:"new_value"`
}

// applyObjectRefreshAtArrival inspects the arrival point for a nearby
// refresh-tagged object and applies its configured attribute drops to
// the actor. Returns the list of attribute hits (empty when no object
// matched) plus an error. Errors are logged by the caller; arrival
// completion proceeds either way — refresh is a side-effect, not the
// primary purpose of the arrival.
//
// All attribute updates and the audit row are committed atomically so
// either everything lands or nothing does.
func (app *App) applyObjectRefreshAtArrival(ctx context.Context, actorID string, arrivalX, arrivalY float64) ([]refreshHit, error) {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Nearest refresh-tagged object — single round-trip for selection.
	// EXISTS over the join table excludes objects with no refresh rows
	// (well that's been dried up, decorative tree). ORDER BY squared
	// distance + LIMIT 1 picks the closest; the tolerance check below
	// drops the row if the closest is still too far (e.g., NPC arrived
	// somewhere with no refresh object nearby).
	var (
		objectID   string
		objectName string
		distSq     float64
	)
	// Bounding box pre-filter on x/y trims the candidate set to objects
	// roughly within the tolerance window before the squared-distance
	// sort. At small object counts it's no different from a full scan;
	// at scale it lets the planner prune via village_object's (x, y)
	// access patterns. Window is the tolerance radius (64 px) — same
	// units as o.x/o.y/arrivalX/arrivalY.
	const tolerancePx = 64.0
	err = tx.QueryRow(ctx,
		`SELECT o.id::text,
		        COALESCE(o.display_name, a.name),
		        (o.x - $1) * (o.x - $1) + (o.y - $2) * (o.y - $2) AS dist_sq
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 WHERE o.x BETWEEN $1 - $3 AND $1 + $3
		   AND o.y BETWEEN $2 - $3 AND $2 + $3
		   AND EXISTS (SELECT 1 FROM object_refresh r WHERE r.object_id = o.id)
		 ORDER BY dist_sq
		 LIMIT 1`,
		arrivalX, arrivalY, tolerancePx,
	).Scan(&objectID, &objectName, &distSq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // no refresh object near the arrival point
		}
		return nil, fmt.Errorf("locate nearest refresh object: %w", err)
	}
	if distSq > objectRefreshToleranceSq {
		return nil, nil // arrival not at a refresh object
	}

	// Lock the actor row so a concurrent attribute-tick (the hourly
	// hunger/thirst/tiredness increment) can't race the GREATEST clamp
	// and leave needs in an inconsistent state. Pull display_name in the
	// same round-trip for the audit row + Hub broadcast.
	var displayName string
	if err := tx.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1 FOR UPDATE`,
		actorID,
	).Scan(&displayName); err != nil {
		return nil, fmt.Errorf("lock actor: %w", err)
	}

	// Pull all refresh rows for the matched object. A multi-attribute
	// object (shaded oak with both tiredness and hunger) returns multiple
	// rows; we apply each one.
	rows, err := tx.Query(ctx,
		`SELECT attribute, amount FROM object_refresh WHERE object_id = $1`,
		objectID,
	)
	if err != nil {
		return nil, fmt.Errorf("load refresh rows: %w", err)
	}
	type rowSpec struct {
		attr   string
		amount int
	}
	var specs []rowSpec
	for rows.Next() {
		var rs rowSpec
		if err := rows.Scan(&rs.attr, &rs.amount); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan refresh row: %w", err)
		}
		specs = append(specs, rs)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate refresh rows: %w", err)
	}
	if len(specs) == 0 {
		// EXISTS matched but no rows — a delete raced us between the two
		// queries. Treat as no-op rather than error.
		return nil, nil
	}

	// Apply each drop. Column whitelist lives in the CHECK constraint on
	// object_refresh.attribute; we still gate here so a drift in the DB
	// constraint can't turn into a SQL injection vector.
	hits := make([]refreshHit, 0, len(specs))
	for _, rs := range specs {
		col, ok := needAttributeColumn(rs.attr)
		if !ok {
			log.Printf("object_refresh: %s has unknown attribute %q (skipped)", objectID, rs.attr)
			continue
		}
		// amount is negative (CHECK constraint on object_refresh.amount).
		// GREATEST(0, x + amount) clamps at 0 so an over-configured drop
		// can't push the value below zero. No upper clamp needed —
		// adding a negative number can only decrease the value.
		var newVal int
		if err := tx.QueryRow(ctx,
			fmt.Sprintf(`UPDATE actor
			             SET %s = GREATEST(0, %s + $1::int)
			             WHERE id = $2
			             RETURNING %s`, col, col, col),
			rs.amount, actorID,
		).Scan(&newVal); err != nil {
			return nil, fmt.Errorf("apply %s: %w", rs.attr, err)
		}
		hits = append(hits, refreshHit{
			Attribute: rs.attr,
			Amount:    rs.amount,
			NewValue:  newVal,
		})
	}

	if len(hits) == 0 {
		return nil, nil
	}

	// Audit row — same shape as the agent_action_log inserts in
	// agent_tick.go (line 1480). source='engine' marks this as an
	// engine-side side effect rather than a tool-call commit.
	payload := map[string]any{
		"object_id":   objectID,
		"object_name": objectName,
		"refreshes":   hits,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return hits, fmt.Errorf("marshal payload: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, error)
		 VALUES ($1, $2, 'engine', 'object_refresh', $3, 'ok', NULL)`,
		actorID, displayName, payloadJSON,
	); err != nil {
		return hits, fmt.Errorf("audit insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return hits, fmt.Errorf("commit tx: %w", err)
	}

	// Post-commit broadcast for admin/dashboard observers. Non-fatal if
	// the Hub isn't listening — the attribute change already landed.
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_object_refresh",
		Data: map[string]any{
			"actor_id":    actorID,
			"actor_name":  displayName,
			"object_id":   objectID,
			"object_name": objectName,
			"refreshes":   hits,
			"at":          time.Now().UTC().Format(time.RFC3339),
		},
	})

	return hits, nil
}

// needAttributeColumn maps a refresh-row attribute name to the actor
// table column name. Whitelist enforced here as a defensive layer atop
// the CHECK constraint on object_refresh.attribute. Returns (col, true)
// for known attributes, ("", false) otherwise.
func needAttributeColumn(attr string) (string, bool) {
	switch attr {
	case "hunger":
		return "hunger", true
	case "thirst":
		return "thirst", true
	case "tiredness":
		return "tiredness", true
	}
	return "", false
}
