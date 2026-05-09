package main

// Dwell mechanic (ZBBS-172).
//
// Sister to the one-shot consume / object_refresh arrival path: where
// those apply an immediate need delta, dwell credits the actor with
// additional recovery for time spent in place.
//
// Two sources, both keyed on (actor, object, attribute, source) in
// actor_dwell_credit:
//
//   - source='object'  — sitting under a tree, drinking at a well.
//                        remaining_ticks = NULL. The dwell tick deletes
//                        the row only when the actor walks off the
//                        loiter pin.
//   - source='item'    — eating a meal at a structure. remaining_ticks
//                        is the countdown of dwell ticks left; the row
//                        deletes when the count hits zero (meal done)
//                        or when the actor walks away (meal abandoned).
//
// Object-side upsert lives in object_refresh.go's
// applyObjectRefreshAtArrival. Item-side upsert lives below — it's
// shared by inventory.go (executeConsume), order_fulfillment.go,
// pay.go (consume_now), and serve.go because every consume site that
// applies an item satisfaction must also stamp the corresponding
// dwell credit.
//
// Both sources flow through the per-minute dispatchObjectRefreshDwell
// handler, which applies dwell_amount via applyConsumption (so the
// audit trail, threshold-crossing chronicler dispatch, and Hub
// broadcast all reuse the existing one-shot machinery).

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// resolveLoiterStructureLocked locks the actor row, reads its current
// position, and resolves the loiter structure from those locked
// coordinates. Used by consume / serve paths that need to pin item
// dwell to the actor's CURRENT structure (not the perception-time
// snapshot in agentNPCRow.CurrentX/Y, which can be stale if the
// actor moved between perception build and the consume tx). Returns
// "" when the actor isn't standing at any named structure.
//
// resolveLoiteringStructure itself reads village_object data from
// app.DB (committed state); that's fine because village_object is
// operator-edited, not actor-driven. The freshness that matters is
// the actor's coordinates, which we get from the locked row.
func (app *App) resolveLoiterStructureLocked(ctx context.Context, tx pgx.Tx, actorID string) (string, error) {
	var x, y float64
	if err := tx.QueryRow(ctx,
		`SELECT current_x, current_y FROM actor WHERE id = $1 FOR UPDATE`,
		actorID,
	).Scan(&x, &y); err != nil {
		return "", fmt.Errorf("lock actor for loiter resolve: %w", err)
	}
	id, _ := app.resolveLoiteringStructure(ctx, x, y)
	return id, nil
}

// upsertItemDwellCredits stamps actor_dwell_credit rows for any
// satisfaction with a complete dwell triple set, pinned to the given
// structure. structureID == "" causes a silent skip — eating-while-
// walking gets only the immediate hit, not the per-tick payoff. The
// immediate satisfaction applies separately via applyConsumption
// regardless of dwell.
//
// On re-consume of the same item at the same structure (eating a
// second bowl of stew while the first is still credited), the
// existing row's last_credited_at is reset to NOW and remaining_ticks
// resets to dwell_total_ticks — a fresh meal restarts the timer
// rather than stacking ticks. Stacking would let an actor double-up
// dwell credits by paying twice in quick succession.
//
// Callers resolve the structure differently:
//   - executeConsume (self-consume): resolveLoiterStructureLocked
//     from the buyer's locked actor row.
//   - executeDeliverOrder (at-source pay+consume): the seller's
//     work_structure_id, since at-source delivery co-locates
//     consumer with seller via the huddle co-location gate.
//   - executeServe (gift): resolveLoiterStructureLocked from the
//     server's locked actor row.
func (app *App) upsertItemDwellCredits(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	satisfactions []itemSatisfaction,
	structureID string,
) error {
	if structureID == "" {
		return nil
	}
	for _, s := range satisfactions {
		if s.DwellAmount <= 0 || s.DwellPeriodMinutes <= 0 || s.DwellTotalTicks <= 0 {
			continue
		}
		// item_satisfies stores DwellAmount as positive magnitude; the
		// credit row stores the apply-ready negative delta so the tick
		// handler doesn't have to know which source convention applies.
		dwellDelta := -s.DwellAmount
		if _, err := tx.Exec(ctx,
			`INSERT INTO actor_dwell_credit
			    (actor_id, object_id, attribute, source, last_credited_at, remaining_ticks,
			     dwell_delta, dwell_period_minutes)
			 VALUES ($1, $2, $3, 'item', NOW(), $4, $5, $6)
			 ON CONFLICT (actor_id, object_id, attribute, source)
			 DO UPDATE SET last_credited_at     = EXCLUDED.last_credited_at,
			               remaining_ticks      = EXCLUDED.remaining_ticks,
			               dwell_delta          = EXCLUDED.dwell_delta,
			               dwell_period_minutes = EXCLUDED.dwell_period_minutes`,
			actorID, structureID, s.Attribute, s.DwellTotalTicks, dwellDelta, s.DwellPeriodMinutes,
		); err != nil {
			return fmt.Errorf("upsert item dwell credit for %s: %w", s.Attribute, err)
		}
	}
	return nil
}

// buildConsumeDwellHintEvent builds the one-time period-flavored hint
// at consume time when the consumed item has a dwell narration set
// (item_kind.consume_dwell_narration) AND the consumer is a PC. The
// hint tells the player there's a lasting effect to stay for —
// without it, the player only learns the dwell mechanic exists by
// noticing a need column tick down post-hoc, which is too late if
// they've already walked off.
//
// Returns *WorldEvent so the caller can defer the broadcast through
// the agent tick's deferred-broadcast queue (consume narrations land
// AFTER the keeper's "here you are" speak in iter N+1, not interleaved
// with the deliver_order's synchronous handover narration).
//
// Silent skip (returns nil) when:
//   - itemKind has no narration configured
//   - actor has no login_username (NPCs don't see HUD narration)
//   - actor has no satisfaction with a dwell triple (caller should
//     gate this — defensive, but a stew row with all-zero dwell would
//     still get the hint here if we relied only on item_kind)
//
// ZBBS-HOME-220.
func (app *App) buildConsumeDwellHintEvent(ctx context.Context, actorID, itemKind, structureID string) *WorldEvent {
	var narration sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT consume_dwell_narration FROM item_kind WHERE name = $1`,
		itemKind,
	).Scan(&narration); err != nil {
		log.Printf("dwell-hint narration lookup for %s: %v", itemKind, err)
		return nil
	}
	if !narration.Valid || narration.String == "" {
		return nil
	}
	var loginUsername sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT login_username FROM actor WHERE id = $1::uuid`,
		actorID,
	).Scan(&loginUsername); err != nil {
		log.Printf("dwell-hint pc check for %s: %v", actorID, err)
		return nil
	}
	if !loginUsername.Valid {
		return nil
	}

	data := map[string]interface{}{
		"actor_id":   actorID,
		"actor_name": "",
		"kind":       "consume",
		"text":       narration.String,
		"private":    true,
		"at":         time.Now().UTC().Format(time.RFC3339),
	}
	if structureID != "" {
		data["structure_id"] = structureID
	}
	return &WorldEvent{Type: "room_event", Data: data}
}
