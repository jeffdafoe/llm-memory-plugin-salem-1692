package main

// Per-NPC scheduled behavior evaluation.
//
// As of the chronicler-dispatch redesign (2026-05-01): when the worker
// branch matches an agent-driven NPC (llm_memory_agent IS NOT NULL), this
// scheduler does NOT walk them. Instead, evaluateWorkerSchedule enqueues
// a shift_start / shift_end event on app.ChroniclerDispatchQueue; the
// chronicler picks it up on its next fire and decides whether to attend
// the NPC, who then decides for themselves whether to walk to/from work.
// The boundary is still stamped (last_shift_tick_at) on the agent branch
// so we don't re-enqueue every tick.
//
// Decorative NPCs (NULL llm_memory_agent) continue to use the legacy walk
// path unchanged. Both branches share the same boundary detection +
// stamping logic above the agent-vs-decorative split.
//
// Runs every server tick (via runServerTickOnce) and walks every NPC whose
// behavior opts into per-NPC scheduling:
//
//   worker: schedule_start_minute / schedule_end_minute give an absolute
//     window in minutes-of-day (1-min precision). NPC walks home → work
//     door at the start boundary, work → home door at the end boundary.
//     Wraps midnight when start > end (e.g. tavernkeeper 17:00–05:00).
//     Both NULL = use the global dawn/dusk window — admins get the full
//     daylight shift without re-stamping individual workers when the
//     world's dawn/dusk shifts. Per-NPC; each worker carries their own
//     pair. ZBBS-071 replaced the older schedule_offset_minutes scalar.
//
//   washerwoman / town_crier: when schedule_interval_hours + active_start_hour
//     + active_end_hour are all set on the NPC, fires at active_start_hour,
//     then every schedule_interval_hours, until past active_end_hour. When
//     unset, the NPC falls back to firing at world_rotation_time (legacy
//     path through checkAndRotate + applyRotation). Mixed behavior is
//     impossible: applyRotation checks HasCustomSchedule() and skips the
//     route start for NPCs that own their own schedule.
//
//   lamplighter: ignores all schedule fields. Dawn/dusk only.
//
// Idempotency: each NPC carries last_shift_tick_at stamping the most recent
// boundary (arrive/leave for worker, firing boundary for rotation). A boundary
// older than the stamp is skipped. Editing the schedule clears the stamp so
// the next tick re-evaluates from scratch — avoids up-to-12h lag on config
// changes.
//
// Missed night boundaries (for rotation NPCs whose window ends before midnight
// and start after, leaving night gaps) are skipped AND stamped — no catch-up
// at dawn. Missing a cycle is better than bursting stale rotations after a
// quiet night.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"log"
	"time"
)

// deterministicLatenessMinutes computes a per-boundary lateness offset in
// [0, window) minutes. Asymmetric on purpose — the NPC fires at or after
// the nominal boundary, never before — so the last_shift_tick_at stamp
// (which records the nominal boundary) still monotonically trails the
// actual dispatch.
//
// The offset is a function of (npc_id, boundary_unix) so it's stable
// across ticks and across server restarts. Without stability the NPC
// would re-roll every tick and never cross its own lateness threshold
// as clock time advances.
//
// FNV-1a is plenty for this: not cryptographic, but well-distributed
// enough that "each NPC rolls a different number for each boundary"
// produces a visibly non-clockwork village.
func deterministicLatenessMinutes(npcID string, boundary time.Time, windowMinutes int) int {
	if windowMinutes <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(npcID))
	h.Write([]byte{0})
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(boundary.Unix()))
	h.Write(ts[:])
	return int(h.Sum64() % uint64(windowMinutes))
}

const behaviorWorker = "worker"

// dispatchScheduledBehaviors is the per-tick entry. Errors on a single NPC
// log and continue so one bad row doesn't freeze the scheduler for the rest.
func (app *App) dispatchScheduledBehaviors(ctx context.Context) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("scheduler: load config: %v", err)
		return
	}
	dawnH, dawnM, err := parseHM(cfg.DawnTime)
	if err != nil {
		log.Printf("scheduler: bad dawn time %q: %v", cfg.DawnTime, err)
		return
	}
	duskH, duskM, err := parseHM(cfg.DuskTime)
	if err != nil {
		log.Printf("scheduler: bad dusk time %q: %v", cfg.DuskTime, err)
		return
	}
	// Dawn/dusk in minutes-of-day for the worker scheduler's NULL-fallback.
	dawnMin := dawnH*60 + dawnM
	duskMin := duskH*60 + duskM
	now := time.Now().In(cfg.Location)

	workers, err := app.loadWorkerRows(ctx)
	if err != nil {
		log.Printf("scheduler: load workers: %v", err)
	}
	for i := range workers {
		app.evaluateWorkerSchedule(ctx, &workers[i], now, dawnMin, duskMin)
	}

	rotators, err := app.loadCustomScheduledRotationNPCs(ctx)
	if err != nil {
		log.Printf("scheduler: load rotation NPCs: %v", err)
	}
	for i := range rotators {
		app.evaluateRotationSchedule(ctx, &rotators[i], now)
	}
}

// workerRow bundles everything the worker scheduler needs to decide whether
// to dispatch this tick. Door coords are pre-resolved server-side via the
// same COALESCE chain go-home / go-to-work use.
//
// ScheduleStartMinute / ScheduleEndMinute are the absolute window in
// minutes-of-day. Both NULL together = inherit dawn/dusk (the
// schedule_window_all_or_none CHECK guarantees they travel as a pair).
// LatenessWindow is the ±fuzz window for when the NPC actually fires
// relative to the nominal boundary (ZBBS-067). Social fields travel along
// so the worker scheduler can step aside whenever the NPC has an active
// social window — see evaluateWorkerSchedule for why.
type workerRow struct {
	ID                   string
	ScheduleStartMinute  sql.NullInt64
	ScheduleEndMinute    sql.NullInt64
	LatenessWindow       int
	LastShiftTickAt      sql.NullTime
	InsideStructureID    sql.NullString
	CurrentX             float64
	CurrentY             float64

	HomeStructureID string
	HomeDoorX       float64
	HomeDoorY       float64

	WorkStructureID string
	WorkDoorX       float64
	WorkDoorY       float64

	// Social schedule overlap (ZBBS-068, minute-precision since ZBBS-071).
	// All three are NULL together (the all-or-none CHECK) — Valid==false
	// means no social schedule configured, in which case the worker
	// scheduler runs normally.
	SocialTag         sql.NullString
	SocialStartMinute sql.NullInt64
	SocialEndMinute   sql.NullInt64

	// Agent override (ZBBS-072, M6.1). Valid && in the future = an LLM
	// agent owns the NPC for this window; the scheduler steps aside.
	AgentOverrideUntil sql.NullTime

	// LlmMemoryAgent identifies an LLM-driven NPC. When non-NULL,
	// evaluateWorkerSchedule routes shift boundaries to the chronicler
	// dispatch queue instead of walking the NPC. The override-aside
	// above is no longer load-bearing for these NPCs (no walks happen),
	// but stays as defense in case of operator config changes. Decorative
	// NPCs (NULL) keep the legacy walk path.
	LlmMemoryAgent sql.NullString

	// DisplayName / WorkPlaceName / HomePlaceName are surfaced to the
	// chronicler perception when an agent NPC's shift boundary is
	// enqueued. Pre-resolved here in the scheduler load so the perception
	// render is a pure formatting step at fire time.
	DisplayName    string
	HomePlaceName  string
	WorkPlaceName  string
}

// loadWorkerRows selects every worker NPC with both home and work
// structures assigned. NPCs missing either are silently excluded — they
// can't walk a shift until an admin fills them in.
//
// As of ZBBS-097 the worker filter reads actor_attribute (the worker
// attribute_definition was added in the same migration). The legacy
// actor.behavior = 'worker' column is no longer consulted here.
func (app *App) loadWorkerRows(ctx context.Context) ([]workerRow, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT n.id, n.schedule_start_minute, n.schedule_end_minute,
		        n.lateness_window_minutes, n.last_shift_tick_at,
		        n.inside_structure_id, n.current_x, n.current_y,
		        n.home_structure_id,
		        COALESCE(hs.x + ha.door_offset_x * 32.0, hs.x),
		        COALESCE(hs.y + ha.door_offset_y * 32.0, hs.y),
		        n.work_structure_id,
		        COALESCE(ws.x + wa.door_offset_x * 32.0, ws.x),
		        COALESCE(ws.y + wa.door_offset_y * 32.0, ws.y),
		        n.social_tag, n.social_start_minute, n.social_end_minute,
		        n.agent_override_until,
		        n.llm_memory_agent, n.display_name,
		        COALESCE(hs.display_name, ha.name, ''),
		        COALESCE(ws.display_name, wa.name, '')
		 FROM actor n
		 JOIN actor_attribute aa ON aa.actor_id = n.id
		 JOIN village_object hs ON hs.id = n.home_structure_id
		 JOIN asset ha         ON ha.id = hs.asset_id
		 JOIN village_object ws ON ws.id = n.work_structure_id
		 JOIN asset wa         ON wa.id = ws.asset_id
		 WHERE aa.slug = $1`,
		behaviorWorker,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []workerRow
	for rows.Next() {
		var w workerRow
		if err := rows.Scan(&w.ID, &w.ScheduleStartMinute, &w.ScheduleEndMinute,
			&w.LatenessWindow, &w.LastShiftTickAt,
			&w.InsideStructureID, &w.CurrentX, &w.CurrentY,
			&w.HomeStructureID, &w.HomeDoorX, &w.HomeDoorY,
			&w.WorkStructureID, &w.WorkDoorX, &w.WorkDoorY,
			&w.SocialTag, &w.SocialStartMinute, &w.SocialEndMinute,
			&w.AgentOverrideUntil,
			&w.LlmMemoryAgent, &w.DisplayName,
			&w.HomePlaceName, &w.WorkPlaceName); err != nil {
			log.Printf("scheduler: scan worker row: %v", err)
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

// minuteInSocialWindow reports whether the given minute-of-day falls within
// [start, end). Wraps around midnight when start > end (e.g. a 22:00–02:00
// late-night tavern shift treats 23:00 and 01:00 as in-window).
func minuteInSocialWindow(minute, startMin, endMin int) bool {
	if startMin == endMin {
		return false
	}
	if startMin < endMin {
		return minute >= startMin && minute < endMin
	}
	return minute >= startMin || minute < endMin
}

// workerBoundaryKind is which side of the shift a boundary sits on.
type workerBoundaryKind int

const (
	workerArrive workerBoundaryKind = iota
	workerLeave
)

// resolveWorkerWindow returns the effective (start, end) minute-of-day pair
// for the worker. Per-NPC values win; both NULL falls back to the global
// dawn/dusk pair so admins get a full daylight shift without setting fields.
func resolveWorkerWindow(w *workerRow, dawnMin, duskMin int) (startMin, endMin int) {
	if w.ScheduleStartMinute.Valid && w.ScheduleEndMinute.Valid {
		return int(w.ScheduleStartMinute.Int64), int(w.ScheduleEndMinute.Int64)
	}
	return dawnMin, duskMin
}

// mostRecentWorkerBoundary returns the most recent arrive/leave boundary at
// or before now for a worker with the given absolute window. Window wraps
// midnight when startMin > endMin (e.g. tavernkeeper 17:00–05:00). Considers
// yesterday/today/tomorrow so wrap windows resolve correctly near midnight,
// matching mostRecentRotationFiring.
//
//	arrive = startMin  (NPC walks home → work)
//	leave  = endMin    (NPC walks work → home; on the next calendar day for
//	                    a wrap window so the (arrive, leave) pair belongs to
//	                    the same shift instance)
func mostRecentWorkerBoundary(now time.Time, startMin, endMin int) (time.Time, workerBoundaryKind) {
	loc := now.Location()
	y, mo, d := now.Date()

	type candidate struct {
		t    time.Time
		kind workerBoundaryKind
	}
	var cands [6]candidate
	for i, dayOffset := range []int{-1, 0, 1} {
		base := time.Date(y, mo, d+dayOffset, 0, 0, 0, 0, loc)
		arrive := base.Add(time.Duration(startMin) * time.Minute)
		leave := base.Add(time.Duration(endMin) * time.Minute)
		if endMin <= startMin {
			// Wrap window: leave belongs to the day after arrive.
			leave = leave.Add(24 * time.Hour)
		}
		cands[i*2] = candidate{arrive, workerArrive}
		cands[i*2+1] = candidate{leave, workerLeave}
	}

	var latestT time.Time
	var latestKind workerBoundaryKind
	for _, c := range cands {
		if !c.t.After(now) && c.t.After(latestT) {
			latestT = c.t
			latestKind = c.kind
		}
	}
	return latestT, latestKind
}

// evaluateWorkerSchedule dispatches at most one walk per tick for the NPC.
// The dispatch is skipped (but the boundary is still stamped) when the NPC
// is already inside the target structure — a fresh restart with NPCs
// correctly parked shouldn't walk them in place.
//
// Lateness window (ZBBS-067): when w.LatenessWindow > 0, the NPC waits
// a deterministic offset past the nominal boundary before firing. The
// stamp still records the nominal boundary, so once we've fired we
// stay idempotent exactly like the zero-lateness case.
//
// Window resolution: per-NPC absolute window if both schedule_*_minute are
// set, otherwise the global dawn/dusk pair (same shift NPCs without
// per-NPC overrides used to get via the offset=0 path).
func (app *App) evaluateWorkerSchedule(ctx context.Context, w *workerRow, now time.Time, dawnMin, duskMin int) {
	// Agent override: when an LLM agent owns the NPC, step aside entirely.
	// The /agent/tick endpoint forward-stamps last_shift_tick_at to the
	// override expiry, so the boundary missed during this window is dropped
	// rather than dispatched the moment the override clears.
	if w.AgentOverrideUntil.Valid && now.Before(w.AgentOverrideUntil.Time) {
		return
	}
	startMin, endMin := resolveWorkerWindow(w, dawnMin, duskMin)
	boundaryAt, kind := mostRecentWorkerBoundary(now, startMin, endMin)
	if boundaryAt.IsZero() {
		return
	}
	if w.LastShiftTickAt.Valid && !w.LastShiftTickAt.Time.Before(boundaryAt) {
		return
	}
	// Hold off until the nominal boundary plus the per-NPC lateness
	// offset. Don't stamp yet — we haven't acted on this boundary.
	lateMinutes := deterministicLatenessMinutes(w.ID, boundaryAt, w.LatenessWindow)
	effectiveAt := boundaryAt.Add(time.Duration(lateMinutes) * time.Minute)
	if now.Before(effectiveAt) {
		return
	}

	// Suppress this dispatch when the NPC has an active social schedule
	// covering effectiveAt. The social_scheduler owns the NPC's location
	// during their social window — without this guard, a worker-leave
	// whose lateness pushes it past the social-enter boundary will yank
	// the NPC out of the tavern and walk them home, undoing the social
	// walk that already fired on an earlier tick. Stamp the boundary so
	// we don't keep re-evaluating the same suppressed shift change every
	// tick; the next boundary (the social-leave at end of window, then
	// tomorrow's worker-arrive) will land normally.
	if w.SocialTag.Valid && w.SocialStartMinute.Valid && w.SocialEndMinute.Valid {
		nowMin := effectiveAt.Hour()*60 + effectiveAt.Minute()
		if minuteInSocialWindow(nowMin, int(w.SocialStartMinute.Int64), int(w.SocialEndMinute.Int64)) {
			if _, err := app.DB.Exec(ctx,
				`UPDATE actor SET last_shift_tick_at = $2 WHERE id = $1`,
				w.ID, boundaryAt,
			); err != nil {
				log.Printf("scheduler: stamp (social-suppressed) %s: %v", w.ID, err)
			}
			return
		}
	}

	// Agent-NPC branch (chronicler-dispatch redesign): instead of walking
	// the NPC bodily, enqueue a shift_start / shift_end event for the
	// chronicler. The chronicler's next perception surfaces the boundary
	// and the chronicler decides whether to attend the NPC, who then
	// decides for themselves whether to walk to/from work. Decorative
	// NPCs (NULL llm_memory_agent) fall through to the legacy walk path
	// below. Stamp the boundary either way so we don't re-evaluate every
	// tick — the dispatch is a one-shot per boundary.
	if w.LlmMemoryAgent.Valid {
		eventType := dispatchShiftStart
		if kind == workerLeave {
			eventType = dispatchShiftEnd
		}
		// Resolve current_place for the perception. Inside a structure -> its
		// label; outdoors -> "the open village" matches the NPC tick wording.
		currentPlace := "the open village"
		if w.InsideStructureID.Valid {
			currentPlace = app.lookupStructureName(ctx, w.InsideStructureID.String)
			if currentPlace == "" {
				currentPlace = "the open village"
			}
		}
		startMinResolved, endMinResolved := resolveWorkerWindow(w, dawnMin, duskMin)
		app.ChroniclerDispatchQueue.enqueue(eventType, boundaryAt, chroniclerDispatchAgent{
			ID:           w.ID,
			DisplayName:  w.DisplayName,
			CurrentPlace: currentPlace,
			WorkPlace:    w.WorkPlaceName,
			ShiftStart:   formatMinuteOfDay(startMinResolved),
			ShiftEnd:     formatMinuteOfDay(endMinResolved),
		})
		if _, err := app.DB.Exec(ctx,
			`UPDATE actor SET last_shift_tick_at = $2 WHERE id = $1`,
			w.ID, boundaryAt,
		); err != nil {
			log.Printf("scheduler: stamp (agent-dispatch) %s: %v", w.ID, err)
		}
		return
	}

	var targetStructureID string
	var destX, destY float64
	label := "worker-arrive"
	switch kind {
	case workerArrive:
		targetStructureID = w.WorkStructureID
		destX, destY = w.WorkDoorX, w.WorkDoorY
	case workerLeave:
		targetStructureID = w.HomeStructureID
		destX, destY = w.HomeDoorX, w.HomeDoorY
		label = "worker-leave"
	}

	alreadyThere := w.InsideStructureID.Valid && w.InsideStructureID.String == targetStructureID
	if !alreadyThere {
		npc := &behaviorNPC{ID: w.ID, CurX: w.CurrentX, CurY: w.CurrentY}
		app.interpolateCurrentPos(npc)
		if err := app.startReturnWalk(ctx, npc, destX, destY, targetStructureID, label, true); err != nil {
			log.Printf("scheduler: %s %s dispatch: %v", label, w.ID, err)
			return
		}
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET last_shift_tick_at = $2 WHERE id = $1`,
		w.ID, boundaryAt,
	); err != nil {
		log.Printf("scheduler: stamp %s: %v", w.ID, err)
	}
}

// formatMinuteOfDay renders a minute-of-day integer as "HH:MM" for the
// chronicler perception. Used so the chronicler sees the same shift
// window text the NPCs see in their own perception.
//
// Defensive normalization: clamps to [0, 1440) so a misconfigured
// schedule (1440, negative, beyond one day) renders as a valid clock
// time rather than "24:00" or "-1:-30" — perception is LLM input and
// invalid times confuse shift interpretation.
func formatMinuteOfDay(m int) string {
	m = ((m % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// rotationRow is a washerwoman or town_crier with a fully-configured
// per-NPC schedule. NPCs without a schedule (NULL in any of the three
// fields) aren't returned and fall back to the legacy world_rotation_time
// trigger via applyRotation.
type rotationRow struct {
	ID                 string
	Behavior           string
	ScheduleInterval   int
	ActiveStartHour    int
	ActiveEndHour      int
	LatenessWindow     int
	LastShiftTickAt    sql.NullTime
	AgentOverrideUntil sql.NullTime
}

// loadCustomScheduledRotationNPCs returns every washerwoman / town_crier
// whose schedule fields are all non-null. The DB CHECK constraint
// schedule_all_or_none guarantees these are only set in the complete
// all-three shape.
func (app *App) loadCustomScheduledRotationNPCs(ctx context.Context) ([]rotationRow, error) {
	// ZBBS-096: rotation NPCs are now identified by attribute slug via
	// actor_attribute, not by the legacy actor.behavior column. The
	// schedule fields themselves stay on actor — they're per-actor
	// overrides, not role-shape data.
	rows, err := app.DB.Query(ctx,
		`SELECT n.id, aa.slug, n.schedule_interval_hours, n.active_start_hour,
		        n.active_end_hour, n.lateness_window_minutes, n.last_shift_tick_at,
		        n.agent_override_until
		 FROM actor n
		 JOIN actor_attribute aa ON aa.actor_id = n.id
		 WHERE aa.slug IN ($1, $2)
		   AND n.schedule_interval_hours IS NOT NULL`,
		behaviorWasherwoman, behaviorTownCrier,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []rotationRow
	for rows.Next() {
		var r rotationRow
		if err := rows.Scan(&r.ID, &r.Behavior, &r.ScheduleInterval,
			&r.ActiveStartHour, &r.ActiveEndHour, &r.LatenessWindow,
			&r.LastShiftTickAt, &r.AgentOverrideUntil); err != nil {
			log.Printf("scheduler: scan rotation row: %v", err)
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// mostRecentRotationFiring computes the most recent firing boundary at or
// before now for a per-NPC rotation schedule. Fires at start_hour, then
// every interval_hours, through (but not after) end_hour. When end wraps
// past midnight (start > end), the effective window spans two calendar
// days — yesterday's start through today's end, or today's start through
// tomorrow's end. Returns zero time when no firing boundary sits within
// the last 24h — the NPC is currently outside their active window AND the
// last window ended more than a day ago (unusual configuration).
func mostRecentRotationFiring(now time.Time, startH, endH, intervalH int) time.Time {
	loc := now.Location()
	y, mo, d := now.Date()

	// Build candidate windows for yesterday, today, and tomorrow. The
	// tomorrow case covers start > end wrap where today's "start" is
	// actually yesterday in wall-clock (e.g., tavernkeeper starts 18:00
	// for a 18-06 window means yesterday's 18:00 is still the active
	// window's start at 03:00 today).
	var latest time.Time
	for _, dayOffset := range []int{-1, 0, 1} {
		base := time.Date(y, mo, d+dayOffset, 0, 0, 0, 0, loc)
		start := base.Add(time.Duration(startH) * time.Hour)
		// End is inclusive in user mental model (fires happen up to and
		// including end_hour). time.Date normalizes end < start by
		// pushing end into the next day.
		end := base.Add(time.Duration(endH) * time.Hour)
		if endH <= startH {
			end = end.Add(24 * time.Hour)
		}
		// Iterate firing points in this window. With interval=3 and a
		// 9-18 window, fires are at 9, 12, 15, 18.
		interval := time.Duration(intervalH) * time.Hour
		for t := start; !t.After(end); t = t.Add(interval) {
			if !t.After(now) && t.After(latest) {
				latest = t
			}
		}
	}
	return latest
}

// evaluateRotationSchedule fires the NPC's rotation route if the most
// recent firing boundary within their window is unstamped. Stamps even
// when no route candidates are available (empty laundry set etc.) so the
// scheduler doesn't keep retrying a no-op for the rest of the window.
func (app *App) evaluateRotationSchedule(ctx context.Context, r *rotationRow, now time.Time) {
	// Agent override: see evaluateWorkerSchedule for the model.
	if r.AgentOverrideUntil.Valid && now.Before(r.AgentOverrideUntil.Time) {
		return
	}
	boundaryAt := mostRecentRotationFiring(now, r.ActiveStartHour, r.ActiveEndHour, r.ScheduleInterval)
	if boundaryAt.IsZero() {
		return
	}
	if r.LastShiftTickAt.Valid && !r.LastShiftTickAt.Time.Before(boundaryAt) {
		return
	}
	// Same lateness treatment as worker (ZBBS-067): hold until the
	// nominal firing plus a deterministic per-boundary offset.
	lateMinutes := deterministicLatenessMinutes(r.ID, boundaryAt, r.LatenessWindow)
	effectiveAt := boundaryAt.Add(time.Duration(lateMinutes) * time.Minute)
	if now.Before(effectiveAt) {
		return
	}

	npc, ok := app.loadBehaviorNPCByID(ctx, r.ID)
	if !ok {
		log.Printf("scheduler: rotation NPC %s vanished during tick", r.ID)
		return
	}

	var domainTag, label string
	switch r.Behavior {
	case behaviorWasherwoman:
		domainTag, label = tagLaundry, "washerwoman"
	case behaviorTownCrier:
		domainTag, label = tagNoticeBoard, "town_crier"
	default:
		// Guarded by the query filter, but keep the switch defensive.
		return
	}
	if _, err := app.startRotationRouteForNPC(ctx, npc, domainTag, label); err != nil {
		log.Printf("scheduler: %s route for %s: %v", label, r.ID, err)
		// Stamp anyway — retrying next tick with the same candidates
		// would just re-fail. Admin fixes the root cause, the next
		// firing picks up.
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET last_shift_tick_at = $2 WHERE id = $1`,
		r.ID, boundaryAt,
	); err != nil {
		log.Printf("scheduler: stamp %s: %v", r.ID, err)
	}
}
