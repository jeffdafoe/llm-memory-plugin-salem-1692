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
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// refreshHit captures one applied attribute drop for the action-log
// payload and Hub broadcast. amount is the configured signed delta;
// newValue is the post-clamp result.
type refreshHit struct {
	Attribute string `json:"attribute"`
	Amount    int    `json:"amount"`
	NewValue  int    `json:"new_value"`
}

// applyObjectRefreshAtArrival looks up which structure owns the
// arrival tile (via the loiter-pin reverse lookup the perception
// builder also uses) and applies that structure's configured
// attribute drops to the actor. Returns the list of attribute hits
// (empty when no refresh-tagged structure owns this tile) plus an
// error. Errors are logged by the caller; arrival completion
// proceeds either way — refresh is a side-effect, not the primary
// purpose of the arrival.
//
// All attribute updates and the audit row are committed atomically
// so either everything lands or nothing does.
//
// Lookup model (ZBBS-127): the actor's tile is reverse-resolved via
// resolveLoiteringStructure, which finds the structure whose loiter
// pin (or door+1 fallback) covers the tile or any of its 8 king's-
// move slots. Replaces an earlier fixed-radius-from-object-center
// query whose 64-pixel tolerance silently dropped refresh applications
// when the visitor-slot picker scooted the actor to the far edge of
// the loiter cluster (e.g. PC clicking a well, landing 65 px from
// center, getting no refresh). Loiter-based lookup mirrors the model
// players already see in the perception text — "You are at the Well"
// is the same condition as "you get the well's refresh."
func (app *App) applyObjectRefreshAtArrival(ctx context.Context, actorID string, arrivalX, arrivalY float64) ([]refreshHit, error) {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Reverse-resolve the arrival tile to a named structure. Returns
	// ("", "") when the actor isn't standing at any structure's loiter
	// zone — i.e. mid-village or at an unnamed placement. Skip refresh
	// in that case rather than fall back to a coord query: the loiter
	// model is the source of truth for "at" semantics, and a fallback
	// would re-introduce the tolerance bug for any structure whose
	// pin happens to be within 64 px of the arrival.
	objectID, objectName := app.resolveLoiteringStructure(ctx, arrivalX, arrivalY)
	if objectID == "" {
		return nil, nil
	}

	// Confirm the resolved structure has refresh rows. resolveLoiteringStructure
	// only filters on display_name (so it picks up named buildings, not
	// decorative tiles) — an arrival at the Tavern's loiter pin shouldn't
	// fire a refresh because the Tavern doesn't have object_refresh rows.
	// The early return keeps the audit log clean for non-refresh structures.
	var hasRefresh bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM object_refresh WHERE object_id = $1)`,
		objectID,
	).Scan(&hasRefresh); err != nil {
		return nil, fmt.Errorf("check object_refresh existence: %w", err)
	}
	if !hasRefresh {
		return nil, nil
	}

	// Lock the actor row so a concurrent attribute-tick (the hourly
	// hunger/thirst/tiredness increment) can't race the GREATEST clamp
	// and leave needs in an inconsistent state. Pull display_name in the
	// same round-trip for the audit row + Hub broadcast. Also pull
	// inside_structure_id so the private narration room_event can
	// target the right room scope, and login_username so the engine
	// knows whether this actor is a PC (only PCs get the private
	// felt-language broadcast — NPCs perceive the refresh through their
	// next perception build).
	var (
		displayName       string
		insideStructureID sql.NullString
		loginUsername     sql.NullString
	)
	if err := tx.QueryRow(ctx,
		`SELECT display_name, inside_structure_id, login_username FROM actor WHERE id = $1 FOR UPDATE`,
		actorID,
	).Scan(&displayName, &insideStructureID, &loginUsername); err != nil {
		return nil, fmt.Errorf("lock actor: %w", err)
	}

	// Snapshot pre-refresh need values for narration. applyConsumption
	// returns the post-clamp values, but the felt-language clauses need
	// the pre-value to know how severe the need was — clamping makes
	// pre = post - amount unreliable when an actor at thirst=10 takes a
	// well's amount=-24 hit (post is 0, not -14). Read from actor_need
	// inside the same tx so we see the same row applyConsumption is
	// about to update.
	preNeeds := map[string]int{}
	preRows, err := tx.Query(ctx,
		`SELECT key, value FROM actor_need WHERE actor_id = $1`,
		actorID,
	)
	if err != nil {
		return nil, fmt.Errorf("snapshot pre-needs: %w", err)
	}
	for preRows.Next() {
		var k string
		var v int
		if err := preRows.Scan(&k, &v); err != nil {
			preRows.Close()
			return nil, fmt.Errorf("scan pre-need: %w", err)
		}
		preNeeds[k] = v
	}
	preRows.Close()
	if err := preRows.Err(); err != nil {
		return nil, fmt.Errorf("iter pre-needs: %w", err)
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
		`SELECT attribute, amount, available_quantity, dwell_amount, dwell_period_minutes
		   FROM object_refresh
		  WHERE object_id = $1
		  FOR UPDATE`,
		objectID,
	)
	if err != nil {
		return nil, fmt.Errorf("load refresh rows: %w", err)
	}
	// dwellAmount / dwellPeriod are paired by the schema's CHECK
	// constraint — both null (legacy one-shot) or both set (per-tick
	// credit while present). Tracked here per-row so the credit
	// upsert below can fire only on rows that opted in.
	type rowSpec struct {
		attr        string
		amount      int
		avail       *int // nil = infinite
		dwellAmount *int
		dwellPeriod *int
	}
	var specs []rowSpec
	for rows.Next() {
		var rs rowSpec
		if err := rows.Scan(&rs.attr, &rs.amount, &rs.avail, &rs.dwellAmount, &rs.dwellPeriod); err != nil {
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

	// applyConsumption clamps and runs the UPDATE in one place. (Pre-
	// ZBBS-WORK-202 it also enqueued a chronicler needs_resolved event;
	// the chronicler dispatch surface is now gone.)
	result, err := app.applyConsumption(ctx, tx, actorID, delta)
	if err != nil {
		return nil, fmt.Errorf("apply consumption: %w", err)
	}

	// Dwell credit upsert (ZBBS-172). Rows with dwell_amount set
	// reward continued presence: the per-minute dwell tick reads
	// actor_dwell_credit, applies dwell_amount via applyConsumption,
	// and advances last_credited_at by exactly dwell_period_minutes.
	// Stamp/refresh the anchor here so the first dwell credit fires
	// dwell_period_minutes after this arrival rather than from a stale
	// timestamp. ON CONFLICT updates last_credited_at on re-arrival —
	// an actor who leaves and returns starts a fresh dwell window
	// rather than instantly collecting time accumulated while away.
	for _, rs := range appliedSpecs {
		if rs.dwellAmount == nil {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO actor_dwell_credit
			    (actor_id, object_id, attribute, source, last_credited_at, remaining_ticks,
			     dwell_delta, dwell_period_minutes)
			 VALUES ($1, $2, $3, 'object', NOW(), NULL, $4, $5)
			 ON CONFLICT (actor_id, object_id, attribute, source)
			 DO UPDATE SET last_credited_at     = EXCLUDED.last_credited_at,
			               dwell_delta          = EXCLUDED.dwell_delta,
			               dwell_period_minutes = EXCLUDED.dwell_period_minutes`,
			actorID, objectID, rs.attr, *rs.dwellAmount, *rs.dwellPeriod,
		); err != nil {
			return nil, fmt.Errorf("upsert dwell credit for %s: %w", rs.attr, err)
		}
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
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, error, huddle_id)
		 VALUES ($1, $2, 'engine', 'object_refresh', $3, 'ok', NULL,
		         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
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

	// Private felt-language narration (ZBBS-128). PCs see "You drink
	// at the Well — the parching ebbs." in their own talk panel; NPCs
	// don't broadcast at all (no UI; their perception build covers
	// the same ground via the audit log + needs snapshot they read on
	// the next tick). The room shouldn't see this — the felt
	// experience is the actor's, not a public event — so the broadcast
	// carries `private: true` + actor_id and the talk panel filters to
	// only render private events when the matching actor_id is its
	// own PC.
	if loginUsername.Valid {
		if text := narrateRefreshAtSourceSelf(objectName, hits, preNeeds); text != "" {
			structureScope := ""
			if insideStructureID.Valid {
				structureScope = insideStructureID.String
			}
			app.Hub.Broadcast(WorldEvent{
				Type: "room_event",
				Data: map[string]interface{}{
					"actor_id":     actorID,
					"actor_name":   displayName,
					"kind":         "refresh",
					"text":         text,
					"structure_id": structureScope,
					"private":      true,
					"at":           time.Now().UTC().Format(time.RFC3339),
				},
			})
		}
	}

	return hits, nil
}

