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
	"strings"
	"time"
	"unicode"

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

	// Pull all refresh rows for the matched object. FOR UPDATE locks the
	// rows so concurrent arrivals can't double-spend a finite supply (two
	// NPCs arriving in the same tick at a well with available_quantity=1
	// must not both successfully drink). available_quantity NULL means
	// infinite — no decrement, no skip.
	//
	// A multi-attribute object (shaded oak with both tiredness from shade
	// and hunger from acorns) returns multiple rows; each carries its own
	// supply pool. Per-row supply gating is independent: an oak whose
	// acorn supply is empty can still offer shade.
	rows, err := tx.Query(ctx,
		`SELECT attribute, amount, available_quantity
		   FROM object_refresh
		  WHERE object_id = $1
		  FOR UPDATE`,
		objectID,
	)
	if err != nil {
		return nil, fmt.Errorf("load refresh rows: %w", err)
	}
	type rowSpec struct {
		attr   string
		amount int
		avail  *int // nil = infinite
	}
	var specs []rowSpec
	for rows.Next() {
		var rs rowSpec
		if err := rows.Scan(&rs.attr, &rs.amount, &rs.avail); err != nil {
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

	// Aggregate per-attribute deltas. (object_id, attribute) is the PK so
	// each attribute appears at most once. The FK to refresh_attribute
	// (ZBBS-090) restricts to known names; unknown values from a future
	// attribute that engine code doesn't yet handle land in the default
	// branch with a log warning — defense in depth.
	//
	// Supply gating: an empty finite supply (available_quantity = 0) skips
	// the row entirely — no delta contribution, no decrement. NULL supply
	// means infinite (the well-never-dries default). Non-zero finite
	// supply contributes the delta and queues a decrement.
	delta := consumptionDelta{}
	appliedSpecs := make([]rowSpec, 0, len(specs))
	for _, rs := range specs {
		if rs.avail != nil && *rs.avail <= 0 {
			// Dry well, empty bush — object exists with this attribute
			// configured but the supply is exhausted. Silent skip; the
			// regen tick will refill it eventually if a refresh schedule
			// is configured.
			continue
		}
		switch rs.attr {
		case "hunger":
			delta.Hunger += rs.amount
		case "thirst":
			delta.Thirst += rs.amount
		case "tiredness":
			delta.Tiredness += rs.amount
		default:
			log.Printf("object_refresh: %s has unknown attribute %q (skipped)", objectID, rs.attr)
			continue
		}
		if rs.amount != 0 {
			appliedSpecs = append(appliedSpecs, rs)
		}
	}
	if len(appliedSpecs) == 0 {
		// Every row was either depleted, an unknown attribute, or zero
		// amount. Skip the consumption call so we don't lock the actor row
		// for a guaranteed no-op, and don't insert an audit row for
		// nothing-happened.
		return nil, nil
	}

	// Decrement the finite supplies for rows that actually contributed.
	// One unit per arrival per row — a well at 10 supports 10 thirst
	// quenchings; a bush at 5 berries gives 5 hunger refreshes; a shaded
	// oak's shade and acorns deplete independently. NULL-supply rows
	// (infinite) skip the UPDATE.
	for _, rs := range appliedSpecs {
		if rs.avail == nil {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE object_refresh
			    SET available_quantity = available_quantity - 1
			  WHERE object_id = $1 AND attribute = $2`,
			objectID, rs.attr,
		); err != nil {
			return nil, fmt.Errorf("decrement supply for %s: %w", rs.attr, err)
		}
	}

	// applyConsumption clamps, runs the UPDATE, and enqueues a chronicler
	// needs_resolved event when an agent NPC's need crosses below the
	// red threshold during this drop. That last bit is the whole reason
	// for routing through this helper rather than the inline UPDATE the
	// pre-ZBBS-needs-resolved code used: a thirsty NPC who walked off
	// the job to drink at the well now gets nudged back to work the
	// same tick instead of staying parked at the well.
	source := refreshSource(objectName)
	result, err := app.applyConsumption(ctx, tx, actorID, delta, source)
	if err != nil {
		return nil, fmt.Errorf("apply consumption: %w", err)
	}

	// Build the hits list for the audit-log payload + Hub broadcast.
	// NewValue mirrors result, indexed by attribute name; preserves the
	// original DB-order so consumers see rows consistently. Skipped rows
	// (empty supply, unknown attribute) don't surface as hits — silent
	// dry-well behavior, no audit noise.
	hits := make([]refreshHit, 0, len(appliedSpecs))
	for _, rs := range appliedSpecs {
		var newVal int
		switch rs.attr {
		case "hunger":
			newVal = result.Hunger
		case "thirst":
			newVal = result.Thirst
		case "tiredness":
			newVal = result.Tiredness
		default:
			continue
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

	// Mirror the post-update need values to the editor panel via the same
	// channel admin reset uses. Listeners patch their local NPC metas off
	// this event; without it, the panel's NEEDS readout would stay stale
	// after a well drink or other refresh-tagged-object arrival until a
	// fresh selection or full roster refresh.
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_needs_changed",
		Data: map[string]any{
			"id":        actorID,
			"hunger":    result.Hunger,
			"thirst":    result.Thirst,
			"tiredness": result.Tiredness,
		},
	})

	return hits, nil
}

// refreshSource maps an object's display name (or asset name fallback)
// to the source token that applyConsumption surfaces to the chronicler
// perception via sourceHint. Match is case-insensitive whole-word on
// the human-readable name — robust to operator renames ("Well" /
// "Well (Roofed)" / "The Old Well" all map to "well") without hard-
// coding object UUIDs and without false-positives on names that merely
// contain the substring (e.g. "Farewell Gate", "Well-Fed Shrine").
//
// Tokenization: split on whitespace, trim leading/trailing non-letter
// runes (parens, commas) per token, then exact-match. Hyphenated and
// apostrophized tokens stay intact, so "Well-Fed" doesn't classify as
// a well; "Well's End" similarly stays unmatched (admins can rename
// to a non-possessive form if they want it recognized).
//
// Unknown objects collapse to the empty string. sourceHint is also
// whitelisted (only "well" and "meal_or_drink" surface today), so a
// new refresh-tagged object that isn't yet recognized here renders
// silently in the chronicler's perception rather than echoing arbitrary
// object names. Add a case here when a new source needs a phrase. A
// long-term cleaner home would be a `source` column on object_refresh,
// but the v1 well case doesn't justify a schema migration yet.
func refreshSource(objectName string) string {
	for _, token := range strings.Fields(strings.ToLower(objectName)) {
		token = strings.TrimFunc(token, func(r rune) bool {
			return !unicode.IsLetter(r)
		})
		if token == "well" {
			return "well"
		}
	}
	return ""
}
