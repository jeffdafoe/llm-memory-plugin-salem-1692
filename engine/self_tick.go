package main

// Scheduled self-tick mechanism for NPCs (ZBBS-110).
//
// NPC ticks are reactive-only — they fire from cascade origins (PC speech,
// NPC arrival, heard-speech, chronicler dispatch, shift boundary). There
// is no native "tick myself again in N seconds" facility. This file adds
// one: scheduleSelfTick stamps a future fire time on actor.next_self_tick_at,
// and the runServerTick handler dispatchSelfTicks drains entries whose
// time has arrived and force-triggers their harness.
//
// Single slot per actor by design. Scheduling a sooner tick wins; a
// later one is ignored. Cascade origins (other NPCs reacting, PC speech,
// chronicler attend) cancel any pending self-tick — fresh signal beats a
// stale planned one.
//
// First user: return-to-work nudge. After an NPC's harness ends with the
// "you should head back to work" condition still true, schedule a
// follow-up ~30-60s out so the LLM gets another shot to commit move_to.

import (
	"context"
	"database/sql"
	"log"
	"math/rand"
	"time"
)

// scheduleSelfTick records that npcID should be ticked at fireAt with the
// given reason (a short tag used in journalctl, e.g. "return_to_work").
//
// Wins-the-soonest semantics: if a sooner tick is already scheduled, this
// call is a no-op. Otherwise it overwrites. This keeps an unrelated
// scheduling call from pushing back a more urgent one without making the
// caller responsible for ordering.
func (app *App) scheduleSelfTick(ctx context.Context, npcID string, fireAt time.Time, reason string) {
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor
		 SET next_self_tick_at = $2,
		     next_self_tick_reason = $3
		 WHERE id = $1
		   AND (next_self_tick_at IS NULL OR next_self_tick_at > $2)`,
		npcID, fireAt, reason,
	); err != nil {
		log.Printf("scheduleSelfTick %s (%s): %v", npcID, reason, err)
	}
}

// cancelSelfTick clears any pending self-tick for npcID. Called when a
// cascade origin (PC speech, other-NPC arrival, chronicler attend, summon
// delivery) supersedes the planned tick — the cascade is fresher signal
// than whatever the schedule was waiting to deliver.
//
// No-op when no self-tick is scheduled. Cheap to call from any trigger
// path; the WHERE clause filters at the DB.
func (app *App) cancelSelfTick(ctx context.Context, npcID string) {
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor
		 SET next_self_tick_at = NULL,
		     next_self_tick_reason = NULL
		 WHERE id = $1
		   AND next_self_tick_at IS NOT NULL`,
		npcID,
	); err != nil {
		log.Printf("cancelSelfTick %s: %v", npcID, err)
	}
}

// dispatchSelfTicks is the per-server-tick handler. Drains entries whose
// fire time has arrived, clears the slot first (so a slow trigger doesn't
// re-fire on the next server tick), then force-triggers the harness.
//
// Force=true bypasses the agentMinTickGap cost guard — these ticks are
// deliberately scheduled, not cascade-driven, and the schedule's own
// jitter is the rate limiter.
//
// Bounded LIMIT so a backlog never spams a single 60s window — at most
// 32 self-ticks fire per server tick. Realistic load is far below that;
// the cap is purely defensive.
func (app *App) dispatchSelfTicks(ctx context.Context) {
	rows, err := app.DB.Query(ctx,
		`UPDATE actor
		 SET next_self_tick_at = NULL,
		     next_self_tick_reason = NULL
		 WHERE id IN (
		     SELECT id FROM actor
		     WHERE next_self_tick_at IS NOT NULL
		       AND next_self_tick_at <= now()
		     ORDER BY next_self_tick_at ASC
		     LIMIT 32
		 )
		 RETURNING id, COALESCE(next_self_tick_reason, '')`,
	)
	if err != nil {
		log.Printf("dispatchSelfTicks: %v", err)
		return
	}
	defer rows.Close()

	type fire struct{ id, reason string }
	var fires []fire
	for rows.Next() {
		var id, reason string
		if err := rows.Scan(&id, &reason); err != nil {
			continue
		}
		fires = append(fires, fire{id, reason})
	}

	for _, f := range fires {
		reason := f.reason
		if reason == "" {
			reason = "self_tick"
		}
		app.triggerImmediateTick(ctx, f.id, "self:"+reason, true, "", "")
	}
}

// shouldNudgeReturnToWork is the predicate for the return-to-work
// perception nudge AND the matching end-of-harness self-tick scheduling.
// One predicate, two callers, so the line shown to the LLM and the
// follow-up tick stay in lock-step.
//
// True iff:
//   - NPC has a work assignment.
//   - The current minute-of-day falls within the NPC's shift window
//     (per-NPC schedule, falling back to the world dawn/dusk pair —
//     same resolution npc_scheduler.evaluateWorkerSchedule uses).
//   - NPC is neither inside their work structure nor loitering at it
//     (loiteringAtID is the result of resolveLoiteringStructure).
//   - No need is at red tier or above (≥2). A pressing need legitimately
//     overrides the duty signal — Ezekiel doesn't return to the forge
//     while starving.
func shouldNudgeReturnToWork(
	r *agentNPCRow,
	insideID sql.NullString,
	loiteringAtID string,
	nowMinuteOfDay, dawnMin, duskMin int,
	hungerT, thirstT, tiredT int,
) bool {
	if !r.WorkStructureID.Valid {
		return false
	}
	startMin, endMin := dawnMin, duskMin
	if r.ScheduleStartMinute.Valid && r.ScheduleEndMinute.Valid {
		startMin = int(r.ScheduleStartMinute.Int32)
		endMin = int(r.ScheduleEndMinute.Int32)
	}
	var onShift bool
	if startMin <= endMin {
		onShift = nowMinuteOfDay >= startMin && nowMinuteOfDay < endMin
	} else {
		// Wrap window (e.g. 17:00–05:00).
		onShift = nowMinuteOfDay >= startMin || nowMinuteOfDay < endMin
	}
	if !onShift {
		return false
	}
	workID := r.WorkStructureID.String
	if insideID.Valid && insideID.String == workID {
		return false
	}
	if loiteringAtID == workID {
		return false
	}
	if needLabelTier(r.Hunger, hungerT) >= 2 {
		return false
	}
	if needLabelTier(r.Thirst, thirstT) >= 2 {
		return false
	}
	if needLabelTier(r.Tiredness, tiredT) >= 2 {
		return false
	}
	return true
}

// returnToWorkPerceptionLine renders the nudge sentence shown in the
// LLM's perception when shouldNudgeReturnToWork is true. The work label
// names the building so the speaker can ground a departure phrase in the
// same vocabulary the rest of the prompt uses.
func returnToWorkPerceptionLine(workLabel string) string {
	if workLabel == "" {
		return "Your shift continues. You should excuse yourself and return to work."
	}
	return "Your shift at " + workLabel + " continues. You should excuse yourself and return."
}

// returnToWorkSelfTickJitter is the wall-clock window between scheduling
// and firing. 30s floor gives the conversation a beat to land before the
// NPC re-decides; 60s ceiling keeps the rhythm from dragging. Centralized
// here so we can tune it from one place if pacing feels off in the
// wild.
const (
	returnToWorkMinDelay = 30 * time.Second
	returnToWorkMaxDelay = 60 * time.Second
)

// scheduleReturnToWorkFollowup schedules a self-tick at now + jittered
// delay so the LLM gets another turn after the conversation has had a
// beat to breathe. Caller already verified the nudge condition is still
// true post-commit.
func (app *App) scheduleReturnToWorkFollowup(ctx context.Context, npcID string) {
	delay := returnToWorkMinDelay + time.Duration(rand.Int63n(int64(returnToWorkMaxDelay-returnToWorkMinDelay)))
	app.scheduleSelfTick(ctx, npcID, time.Now().Add(delay), "return_to_work")
}

// maybeScheduleReturnToWork is the post-harness companion to the
// perception nudge. Re-queries the NPC's location and needs (the row the
// harness loaded is stale once a commit has run) and, if the
// shouldNudgeReturnToWork predicate is still true, schedules the
// follow-up self-tick. No-op when conditions have changed (NPC moved to
// work, fell into a pressing need, shift ended).
//
// Best-effort: any DB error short-circuits silently. The next reactive
// cascade will still tick the NPC if anything else fires.
func (app *App) maybeScheduleReturnToWork(ctx context.Context, npcID string) {
	var r agentNPCRow
	if err := app.DB.QueryRow(ctx,
		`SELECT id, role,
		        inside_structure_id, current_x, current_y,
		        work_structure_id,
		        schedule_start_minute, schedule_end_minute,
		        COALESCE(wo.display_name, wa.name) AS work_label,
		        hunger, thirst, tiredness
		 FROM actor n
		 LEFT JOIN village_object wo ON wo.id = n.work_structure_id
		 LEFT JOIN asset wa ON wa.id = wo.asset_id
		 WHERE n.id = $1 AND n.llm_memory_agent IS NOT NULL`,
		npcID,
	).Scan(&r.ID, &r.Role,
		&r.InsideStructureID, &r.CurrentX, &r.CurrentY,
		&r.WorkStructureID,
		&r.ScheduleStartMinute, &r.ScheduleEndMinute,
		&r.WorkLabel,
		&r.Hunger, &r.Thirst, &r.Tiredness,
	); err != nil {
		return
	}

	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		return
	}
	dawnMin, duskMin := 6*60, 18*60
	if dh, dm, err := parseHM(cfg.DawnTime); err == nil {
		dawnMin = dh*60 + dm
	}
	if dh, dm, err := parseHM(cfg.DuskTime); err == nil {
		duskMin = dh*60 + dm
	}
	now := time.Now().In(cfg.Location)
	nowMinuteOfDay := now.Hour()*60 + now.Minute()

	hungerT := app.loadNeedThreshold(ctx, "hunger_red_threshold", defaultHungerRedThreshold)
	thirstT := app.loadNeedThreshold(ctx, "thirst_red_threshold", defaultThirstRedThreshold)
	tiredT := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)

	// resolveLoiteringStructure returns "" when the NPC isn't on a
	// recognized loiter slot — that's fine, shouldNudgeReturnToWork only
	// uses it to detect "loitering AT work specifically."
	loiteringAtID, _ := app.resolveLoiteringStructure(ctx, r.CurrentX, r.CurrentY)

	if !shouldNudgeReturnToWork(&r, r.InsideStructureID, loiteringAtID,
		nowMinuteOfDay, dawnMin, duskMin, hungerT, thirstT, tiredT) {
		return
	}

	app.scheduleReturnToWorkFollowup(ctx, r.ID)
}
