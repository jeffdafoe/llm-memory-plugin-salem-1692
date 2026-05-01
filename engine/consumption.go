package main

// Consumption — the unified path for reducing villager needs.
//
// Three callers feed into this:
//  1. pay.go's food/drink side effect (a tavern meal drops hunger/thirst).
//  2. The admin "reset needs" action triggered from the editor panel.
//  3. The (future) well-drink mechanic when an NPC interacts with a well.
//
// Centralizing the path means each of these gets the same domain effect:
// clamp at 0, run the chronicler trigger when a need crosses out of the
// red threshold band, and enqueue a needs_resolved event so the chronicler
// can attend to NPCs who were too needy to stay at work and now aren't.
//
// Rationale: villagers leaving their post when distress crosses the red
// threshold is fine — the worker scheduler, the chronicler, and the
// villagers' own ticks all handle that direction. The reverse direction
// ("X drank from the well, they're fine now, the chronicler should attend
// to them so they walk back to work") didn't exist before this — needs
// would just silently drop and the chronicler had no signal to nudge them
// back. The dispatch queue already knows how to surface "interesting NPC
// state changes" to the chronicler; this just adds one more event source.
//
// Threshold semantics: a need "crosses" when it was at or above the red
// threshold before the call and is below it after. Crossing only the
// peak→red boundary doesn't fire (still in distress, no nudge needed);
// crossing only the mild→silent boundary doesn't fire (was never in
// distress, no work was abandoned). The red threshold is the one that
// matters because it matches the existing chronicler distress filter
// in buildChroniclerDistressList — same source of truth.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

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

// needCross records a single need that transitioned from at-or-above the
// red threshold to below it during applyConsumption. Returned to the
// caller for logging; also used to populate the chronicler dispatch
// event's ResolvedNeeds list.
type needCross struct {
	Need     string // "hunger" / "thirst" / "tiredness"
	OldValue int
	NewValue int
}

// consumptionResult bundles the post-update need values and any threshold
// crossings that fired. Returned by applyConsumption so callers (in
// particular handleResetNPCNeeds) can render the actual persisted
// values instead of assuming what the clamp produced — guards against
// drift if needMax or DB constraints change.
type consumptionResult struct {
	Hunger    int
	Thirst    int
	Tiredness int
	Crosses   []needCross
}

// applyConsumption is the single domain entry point for any reduction in
// villager needs. The caller supplies a transaction (so the consumption
// can compose with surrounding work like pay.go's coin transfer) and a
// source label that the chronicler perception will surface.
//
// SELECT FOR UPDATE locks the actor row before the UPDATE so concurrent
// callers (a chore tool finishing at the same time as an admin reset)
// serialize on the same row rather than racing.
//
// On success: the actor's needs are clamped into [0, needMax] and a
// needs_resolved event is enqueued on app.ChroniclerDispatchQueue if any
// red-threshold crossings occurred AND the actor is an agent NPC. The
// chronicler dispatch is meaningless for decorative NPCs (no llm_memory_
// agent → no tick to attend) and PCs (login_username set), so we skip
// the enqueue for those — the SQL still runs (a PC eating in the tavern
// is a real consumption, just not a chronicler-attention event).
//
// Returns the post-update values + the list of crossings for the
// caller's use. An empty Crosses slice is fine — a non-crossing
// reduction (e.g. dropping hunger from 8 to 5, well below the default
// red threshold of 18) is still a real effect on the actor's row.
func (app *App) applyConsumption(ctx context.Context, tx pgx.Tx, actorID string, delta consumptionDelta, source string) (consumptionResult, error) {
	// 1. Lock + read current values.
	var oldH, oldT, oldTi int
	err := tx.QueryRow(ctx,
		`SELECT hunger, thirst, tiredness FROM actor WHERE id = $1 FOR UPDATE`,
		actorID,
	).Scan(&oldH, &oldT, &oldTi)
	if err != nil {
		return consumptionResult{}, fmt.Errorf("applyConsumption: read needs: %w", err)
	}

	// 2. Compute clamped new values in code so we can detect crossings
	// without a second read. Clamp into [0, needMax] in both
	// directions — positive deltas (a future "make them hungrier" admin
	// tool) get the same protection.
	newH := clampNeed(oldH + delta.Hunger)
	newT := clampNeed(oldT + delta.Thirst)
	newTi := clampNeed(oldTi + delta.Tiredness)

	// 3. Single UPDATE if anything actually changed.
	if newH != oldH || newT != oldT || newTi != oldTi {
		if _, err := tx.Exec(ctx,
			`UPDATE actor SET hunger = $1, thirst = $2, tiredness = $3 WHERE id = $4`,
			newH, newT, newTi, actorID,
		); err != nil {
			return consumptionResult{}, fmt.Errorf("applyConsumption: update needs: %w", err)
		}
	}

	result := consumptionResult{Hunger: newH, Thirst: newT, Tiredness: newTi}

	// 4. Detect threshold crossings. Same red-threshold settings the
	// chronicler distress section uses, so an NPC newly absent from
	// that section is exactly an NPC who fired a needs_resolved event.
	hungerT := app.loadNeedThreshold(ctx, "hunger_red_threshold", defaultHungerRedThreshold)
	thirstT := app.loadNeedThreshold(ctx, "thirst_red_threshold", defaultThirstRedThreshold)
	tiredT := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)

	if oldH >= hungerT && newH < hungerT {
		result.Crosses = append(result.Crosses, needCross{Need: "hunger", OldValue: oldH, NewValue: newH})
	}
	if oldT >= thirstT && newT < thirstT {
		result.Crosses = append(result.Crosses, needCross{Need: "thirst", OldValue: oldT, NewValue: newT})
	}
	if oldTi >= tiredT && newTi < tiredT {
		result.Crosses = append(result.Crosses, needCross{Need: "tiredness", OldValue: oldTi, NewValue: newTi})
	}
	if len(result.Crosses) == 0 {
		return result, nil
	}

	// 5. Resolve agent details for the chronicler perception. Done as
	// part of this txn so the read is consistent with the UPDATE we
	// just made (current_place reflects post-update inside_structure_id
	// in the same snapshot). Skips non-agent rows — see top-of-file
	// comment for rationale.
	agent, ok, err := app.loadDispatchAgentForActor(ctx, tx, actorID)
	if err != nil {
		// Don't fail the consumption — the SQL is committed (or will be
		// when the caller commits the surrounding txn) and the only
		// loss is the chronicler nudge. Log and return so the caller
		// still sees what happened.
		log.Printf("applyConsumption: load agent details for %s: %v", actorID, err)
		return result, nil
	}
	if !ok {
		// Decorative NPC or PC. Crossings are real for the row, but no
		// chronicler attention is warranted.
		return result, nil
	}

	resolvedNeeds := make([]string, 0, len(result.Crosses))
	for _, c := range result.Crosses {
		resolvedNeeds = append(resolvedNeeds, c.Need)
	}
	agent.ResolvedNeeds = resolvedNeeds
	agent.Source = source

	// Enqueue at "now" — needs_resolved events don't fold by minute
	// the way shift boundaries do (no scheduled boundary minute to
	// align on), but the queue's batching by (event_type, unix_minute)
	// still gives us free coalescing when several NPCs drink from the
	// same well in the same tick.
	app.ChroniclerDispatchQueue.enqueue(dispatchNeedsResolved, time.Now().UTC(), agent)

	return result, nil
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

// loadDispatchAgentForActor resolves the chroniclerDispatchAgent fields
// for an actor: display_name, current place, work place, shift window.
// Returns (_, false, nil) when the actor isn't an agent NPC (NULL
// llm_memory_agent or non-NULL login_username) — the consumption itself
// still applies but no chronicler dispatch is warranted.
//
// Reads from the same transaction as the UPDATE so a concurrent walk
// completion (which would change inside_structure_id) doesn't slip in
// between our UPDATE and our place lookup.
func (app *App) loadDispatchAgentForActor(ctx context.Context, tx pgx.Tx, actorID string) (chroniclerDispatchAgent, bool, error) {
	var (
		displayName       string
		insideStructureID sql.NullString
		workStructureID   sql.NullString
		scheduleStartMin  sql.NullInt64
		scheduleEndMin    sql.NullInt64
		llmMemoryAgent    sql.NullString
		loginUsername     sql.NullString
	)
	err := tx.QueryRow(ctx, `
		SELECT display_name,
		       inside_structure_id,
		       work_structure_id,
		       schedule_start_minute,
		       schedule_end_minute,
		       llm_memory_agent,
		       login_username
		FROM actor
		WHERE id = $1
	`, actorID).Scan(&displayName, &insideStructureID, &workStructureID,
		&scheduleStartMin, &scheduleEndMin, &llmMemoryAgent, &loginUsername)
	if err != nil {
		return chroniclerDispatchAgent{}, false, err
	}
	if !llmMemoryAgent.Valid || loginUsername.Valid {
		return chroniclerDispatchAgent{}, false, nil
	}

	currentPlace := "the open village"
	if insideStructureID.Valid {
		if name := app.lookupStructureName(ctx, insideStructureID.String); name != "" {
			currentPlace = name
		}
	}
	workPlace := ""
	if workStructureID.Valid {
		workPlace = app.lookupStructureName(ctx, workStructureID.String)
	}

	// Shift window — fall back to dawn/dusk when the per-NPC override is
	// NULL, matching the worker scheduler's resolution. Errors loading
	// world config drop us through with empty shift strings (the render
	// just skips the time annotation), preferring a slightly thinner
	// chronicler line over swallowing the whole event.
	shiftStart, shiftEnd := "", ""
	if workPlace != "" {
		startMin, endMin, ok := app.resolveActorShiftWindow(ctx, scheduleStartMin, scheduleEndMin)
		if ok {
			shiftStart = formatMinuteOfDay(startMin)
			shiftEnd = formatMinuteOfDay(endMin)
		}
	}

	return chroniclerDispatchAgent{
		ID:           actorID,
		DisplayName:  displayName,
		CurrentPlace: currentPlace,
		WorkPlace:    workPlace,
		ShiftStart:   shiftStart,
		ShiftEnd:     shiftEnd,
	}, true, nil
}

// resolveActorShiftWindow returns the (start, end) minute-of-day pair for
// an actor based on their per-NPC schedule_start_minute / _end_minute,
// falling back to world dawn/dusk when one or both are NULL. Mirrors the
// resolveWorkerWindow helper in npc_scheduler.go but takes the nullable
// columns directly so callers outside the worker scheduler don't need a
// workerRow.
func (app *App) resolveActorShiftWindow(ctx context.Context, scheduleStart, scheduleEnd sql.NullInt64) (int, int, bool) {
	if scheduleStart.Valid && scheduleEnd.Valid {
		return int(scheduleStart.Int64), int(scheduleEnd.Int64), true
	}
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		return 0, 0, false
	}
	dawnH, dawnM, err := parseHM(cfg.DawnTime)
	if err != nil {
		return 0, 0, false
	}
	duskH, duskM, err := parseHM(cfg.DuskTime)
	if err != nil {
		return 0, 0, false
	}
	return dawnH*60 + dawnM, duskH*60 + duskM, true
}
