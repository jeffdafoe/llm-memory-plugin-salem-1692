package main

// Consumption — the unified path for reducing villager needs.
//
// Three callers feed into this:
//  1. pay.go's food/drink side effect (a tavern meal drops hunger/thirst).
//  2. The admin "reset needs" action triggered from the editor panel.
//  3. The (future) well-drink mechanic when an NPC interacts with a well.
//
// Centralizing the path means each of these clamps the actor's needs into
// [0, needMax] uniformly. Threshold-crossing detection and chronicler
// dispatch were removed in ZBBS-WORK-202 along with the rest of the
// chronicler dispatch surface.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// consumptionDelta describes how much each need should change. Negative
// values reduce, positive values increase. Zero leaves the need alone.
// The UPDATE clamps the result into [0, needMax]. Callers that want
// "fully zero this need" pass -needMax (the clamp handles overflow).
type consumptionDelta struct {
	Hunger    int
	Thirst    int
	Tiredness int
}

// consumptionResult bundles the post-update need values. Returned by
// applyConsumption so callers (in particular handleResetNPCNeeds) can
// render the actual persisted values instead of assuming what the clamp
// produced — guards against drift if needMax or DB constraints change.
type consumptionResult struct {
	Hunger    int
	Thirst    int
	Tiredness int
}

// applyConsumption is the single domain entry point for any reduction in
// villager needs. The caller supplies a transaction (so the consumption
// can compose with surrounding work like pay.go's coin transfer).
//
// SELECT FOR UPDATE locks the actor row before the UPDATE so concurrent
// callers (a chore tool finishing at the same time as an admin reset)
// serialize on the same row rather than racing.
//
// On success: the actor's needs are clamped into [0, needMax] and the
// post-update values are returned for the caller to render.
func (app *App) applyConsumption(ctx context.Context, tx pgx.Tx, actorID string, delta consumptionDelta) (consumptionResult, error) {
	// 1. Lock the actor row + read current need values via JOIN to
	//    actor_need (ZBBS-121 commit 4). FOR UPDATE OF a locks just
	//    the actor row — same lock target as the pre-conversion code,
	//    so concurrent applyConsumption / dispatchNeedsTick still
	//    serialize correctly. LEFT JOIN means an actor with no
	//    actor_need rows still produces one row (with NULL key/value),
	//    while a non-existent actor produces zero rows — checked via
	//    foundActor below to preserve the pre-conversion ErrNoRows
	//    semantics.
	preNeeds := NeedSet{}
	preRows, err := tx.Query(ctx,
		`SELECT n.key, n.value
		   FROM actor a
		   LEFT JOIN actor_need n ON n.actor_id = a.id
		  WHERE a.id = $1
		  FOR UPDATE OF a`,
		actorID,
	)
	if err != nil {
		return consumptionResult{}, fmt.Errorf("applyConsumption: lock and read needs: %w", err)
	}
	defer preRows.Close()
	foundActor := false
	for preRows.Next() {
		foundActor = true
		var key sql.NullString
		var value sql.NullInt64
		if err := preRows.Scan(&key, &value); err != nil {
			return consumptionResult{}, fmt.Errorf("applyConsumption: scan need row: %w", err)
		}
		if key.Valid && value.Valid {
			preNeeds[key.String] = int(value.Int64)
		}
	}
	if err := preRows.Err(); err != nil {
		return consumptionResult{}, fmt.Errorf("applyConsumption: iterate needs: %w", err)
	}
	if !foundActor {
		return consumptionResult{}, fmt.Errorf("applyConsumption: actor %s not found", actorID)
	}
	// Hard-fail on missing need rows rather than silently defaulting
	// to 0. Defaulting could over-decrement against a real value if a
	// row was somehow deleted post-backfill (writeNeedRows is UPSERT
	// so it should always restore them, but this is a write-path
	// safety net). Once the legacy columns drop in commit 6, this is
	// the only signal an operator gets that backfill broke.
	oldH, okH := preNeeds.GetOK("hunger")
	oldT, okT := preNeeds.GetOK("thirst")
	oldTi, okTi := preNeeds.GetOK("tiredness")
	if !okH || !okT || !okTi {
		return consumptionResult{}, fmt.Errorf(
			"applyConsumption: missing actor_need rows for actor %s: hunger=%t thirst=%t tiredness=%t",
			actorID, okH, okT, okTi,
		)
	}

	// 2. Compute clamped new values in code so we can detect crossings
	// without a second read. Clamp into [0, needMax] in both
	// directions — positive deltas (a future "make them hungrier" admin
	// tool) get the same protection.
	newH := clampNeed(oldH + delta.Hunger)
	newT := clampNeed(oldT + delta.Thirst)
	newTi := clampNeed(oldTi + delta.Tiredness)

	// 3. Write to actor_need rows if anything actually changed
	//    (ZBBS-121 commit 5: dual-write era ends; rows are now the
	//    sole write target). Skipping no-op writes preserves the same
	//    short-circuit the legacy column UPDATE used.
	if newH != oldH || newT != oldT || newTi != oldTi {
		if err := app.writeNeedRows(ctx, tx, actorID, map[string]int{
			"hunger":    newH,
			"thirst":    newT,
			"tiredness": newTi,
		}); err != nil {
			return consumptionResult{}, fmt.Errorf("applyConsumption: write rows: %w", err)
		}
	}

	return consumptionResult{Hunger: newH, Thirst: newT, Tiredness: newTi}, nil
}

// clampNeed bounds a need value into the [0, needMax] band that the
// SQL CHECK and the LEAST/GREATEST clamps elsewhere already enforce.
// Centralized here so the consumption path matches the attribute-tick
// path's invariants by construction.
func clampNeed(v int) int {
	if v < 0 {
		return 0
	}
	if v > needMax {
		return needMax
	}
	return v
}

