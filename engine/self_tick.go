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
		// Self-tick is a cascade origin — mint a fresh scene per fire
		// so the resulting chat rows carry scene_id (MEM-132's read path
		// filters shared-VA history by scene_id; rows written NULL are
		// orphaned and the next tick reads blank, producing the meta-
		// prose "I need a perception update" loop).
		app.triggerImmediateTick(ctx, f.id, "self:"+reason, true, app.newScene(ctx, ""), "")
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
//   - No need is at mild tier or above (≥1). Any felt discomfort
//     legitimately overrides the duty signal so the NPC has time to
//     actually act on it — e.g. dwell at a Shade Tree long enough for
//     the 10-min tiredness credit to fire. Pre-WORK-234 this gated on
//     red+ only, which let the nudge yank an "amber"-tired NPC back to
//     work within ~30-60s of arriving at a rest spot, defeating the
//     dwell-recovery mechanic (ZBBS-172) for the whole amber tier.
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
	if needLabelTier(r.Hunger, hungerT) >= 1 {
		return false
	}
	if needLabelTier(r.Thirst, thirstT) >= 1 {
		return false
	}
	if needLabelTier(r.Tiredness, tiredT) >= 1 {
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

// shouldNudgeReturnHome (ZBBS-HOME-252) is the symmetric end-of-shift
// counterpart to shouldNudgeReturnToWork. True iff:
//   - NPC has both a work and a home assignment.
//   - The current minute-of-day falls OUTSIDE the NPC's shift window
//     (same window resolution as shouldNudgeReturnToWork).
//   - NPC is still inside their work structure or loitering at it.
//
// No need-tier gate: an off-shift NPC at red tiredness should head home
// (where their bed is), not stay at work waiting for the need to clear.
func shouldNudgeReturnHome(
	r *agentNPCRow,
	insideID sql.NullString,
	loiteringAtID string,
	nowMinuteOfDay, dawnMin, duskMin int,
) bool {
	if !r.WorkStructureID.Valid || !r.HomeStructureID.Valid {
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
		onShift = nowMinuteOfDay >= startMin || nowMinuteOfDay < endMin
	}
	if onShift {
		return false
	}
	workID := r.WorkStructureID.String
	atWork := (insideID.Valid && insideID.String == workID) || loiteringAtID == workID
	return atWork
}

// returnHomePerceptionLine renders the end-of-shift nudge. Mirror of
// returnToWorkPerceptionLine; home label names the destination so the
// LLM can use the same vocabulary the prompt already exposes.
func returnHomePerceptionLine(homeLabel string) string {
	if homeLabel == "" {
		return "Your shift has ended. Time to close up and head home."
	}
	return "Your shift has ended. Time to close up and head home to " + homeLabel + "."
}

// Defaults for the return_to_work_delay_seconds setting (ZBBS-111).
// 30s floor gives the conversation a beat to land before the NPC
// re-decides; 60s ceiling keeps the rhythm from dragging. Operators
// override at runtime by writing a JSON int array to the setting key
// (e.g. '[45,90]'). The (eventual) settings UI will surface a
// "min,max" input that translates to the JSON shape on save.
const (
	defaultReturnToWorkMinDelaySeconds = 30
	defaultReturnToWorkMaxDelaySeconds = 60
)

// returnToWorkMaxReasonableSeconds caps the configured maximum so a
// fat-fingered setting can't push the next tick past a reasonable
// human conversation beat. 1 hour is conservatively wide — a real
// "give me a beat" delay should be tens of seconds. Anything beyond
// this triggers the defaults-fallback path with a log line.
const returnToWorkMaxReasonableSeconds = 3600

// scheduleReturnToWorkFollowup schedules a self-tick at now + jittered
// delay so the LLM gets another turn after the conversation has had a
// beat to breathe. Caller already verified the nudge condition is still
// true post-commit. Range comes from the return_to_work_delay_seconds
// setting; defaults applied when the setting row is missing, malformed,
// negative, max < min, or beyond the reasonable upper bound — anything
// that would produce an immediate-tick storm or a multi-hour silence.
func (app *App) scheduleReturnToWorkFollowup(ctx context.Context, npcID string) {
	minSec, maxSec := app.loadIntRange(ctx, "return_to_work_delay_seconds",
		defaultReturnToWorkMinDelaySeconds, defaultReturnToWorkMaxDelaySeconds)
	if minSec < 0 || maxSec < 0 || maxSec < minSec || maxSec > returnToWorkMaxReasonableSeconds {
		log.Printf("scheduleReturnToWorkFollowup: invalid range [%d,%d] (negatives, max<min, or >%ds), using defaults",
			minSec, maxSec, returnToWorkMaxReasonableSeconds)
		minSec = defaultReturnToWorkMinDelaySeconds
		maxSec = defaultReturnToWorkMaxDelaySeconds
	}
	minDelay := time.Duration(minSec) * time.Second
	maxDelay := time.Duration(maxSec) * time.Second
	delay := minDelay
	if maxDelay > minDelay {
		delay += time.Duration(rand.Int63n(int64(maxDelay - minDelay)))
	}
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
		`SELECT id, COALESCE(role, ''),
		        inside_structure_id, current_x, current_y,
		        work_structure_id,
		        schedule_start_minute, schedule_end_minute,
		        COALESCE(wo.display_name, wa.name) AS work_label
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
	); err != nil {
		return
	}
	// Need values come from actor_need rows (ZBBS-121 commit 4). Best-
	// effort like the rest of the function: a failed read leaves
	// Hunger/Thirst/Tiredness at zero, which the downstream
	// shouldNudgeReturnToWork predicate treats as "no pressing need"
	// — same effect as if the actor was actually silent on all needs.
	// Log on failure so silent skips are observable rather than
	// invisible.
	needs, err := app.needsSnapshot(ctx, npcID)
	if err != nil {
		log.Printf("maybeScheduleReturnToWork needs lookup for %s: %v", npcID, err)
		return
	}
	r.Hunger = needs.Get("hunger")
	r.Thirst = needs.Get("thirst")
	r.Tiredness = needs.Get("tiredness")

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

// scheduleReturnHomeFollowup (ZBBS-HOME-252) schedules a self-tick at
// now + jittered delay so an off-shift NPC stuck at work gets a turn
// to consider walking home. Reuses the return_to_work_delay_seconds
// setting so operators have one knob, not two; the human-conversation-
// beat shape applies equally to "give them a moment before nudging
// again." Caller already verified the predicate is still true.
func (app *App) scheduleReturnHomeFollowup(ctx context.Context, npcID string) {
	minSec, maxSec := app.loadIntRange(ctx, "return_to_work_delay_seconds",
		defaultReturnToWorkMinDelaySeconds, defaultReturnToWorkMaxDelaySeconds)
	if minSec < 0 || maxSec < 0 || maxSec < minSec || maxSec > returnToWorkMaxReasonableSeconds {
		log.Printf("scheduleReturnHomeFollowup: invalid range [%d,%d] (negatives, max<min, or >%ds), using defaults",
			minSec, maxSec, returnToWorkMaxReasonableSeconds)
		minSec = defaultReturnToWorkMinDelaySeconds
		maxSec = defaultReturnToWorkMaxDelaySeconds
	}
	minDelay := time.Duration(minSec) * time.Second
	maxDelay := time.Duration(maxSec) * time.Second
	delay := minDelay
	if maxDelay > minDelay {
		delay += time.Duration(rand.Int63n(int64(maxDelay - minDelay)))
	}
	app.scheduleSelfTick(ctx, npcID, time.Now().Add(delay), "return_home")
}

// maybeScheduleReturnHome is the post-harness companion to the
// return-home perception nudge. Symmetric to maybeScheduleReturnToWork:
// re-queries the NPC's current location, and if shouldNudgeReturnHome
// is still true (e.g. the LLM didn't actually walk home this tick),
// schedules a follow-up self-tick so the NPC keeps getting turns until
// they leave. No-op once the NPC is no longer at work or has come on
// shift again.
func (app *App) maybeScheduleReturnHome(ctx context.Context, npcID string) {
	var r agentNPCRow
	if err := app.DB.QueryRow(ctx,
		`SELECT id, COALESCE(role, ''),
		        inside_structure_id, current_x, current_y,
		        work_structure_id, home_structure_id,
		        schedule_start_minute, schedule_end_minute,
		        COALESCE(wo.display_name, wa.name) AS work_label,
		        COALESCE(ho.display_name, ha.name) AS home_label
		 FROM actor n
		 LEFT JOIN village_object wo ON wo.id = n.work_structure_id
		 LEFT JOIN asset wa ON wa.id = wo.asset_id
		 LEFT JOIN village_object ho ON ho.id = n.home_structure_id
		 LEFT JOIN asset ha ON ha.id = ho.asset_id
		 WHERE n.id = $1 AND n.llm_memory_agent IS NOT NULL`,
		npcID,
	).Scan(&r.ID, &r.Role,
		&r.InsideStructureID, &r.CurrentX, &r.CurrentY,
		&r.WorkStructureID, &r.HomeStructureID,
		&r.ScheduleStartMinute, &r.ScheduleEndMinute,
		&r.WorkLabel, &r.HomeLabel,
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

	loiteringAtID, _ := app.resolveLoiteringStructure(ctx, r.CurrentX, r.CurrentY)

	if !shouldNudgeReturnHome(&r, r.InsideStructureID, loiteringAtID,
		nowMinuteOfDay, dawnMin, duskMin) {
		return
	}

	app.scheduleReturnHomeFollowup(ctx, r.ID)
}
