package main

// World day/night cycle.
//
// A singleton row in world_phase tracks the current phase and the timestamp
// of the last transition. A background goroutine wakes once a minute, figures
// out which scheduled boundary (dawn or dusk) is most recent, and — if we
// haven't already processed it — fires a transition.
//
// A transition is a bulk UPDATE over village_object.current_state driven by
// asset_state_tag ('day-active' at dawn, 'night-active' at dusk). Each
// affected object gets an individual object_state_changed event so every
// connected client updates the same way it would for a manual state change.
//
// Admins can force a phase via POST /api/village/world/force-phase. Forcing
// updates last_transition_at, so the ticker treats the force as the most
// recently processed boundary and doesn't immediately revert it until the
// next real boundary rolls around.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	mathrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	phaseDay   = "day"
	phaseNight = "night"

	tagDayActive   = "day-active"
	tagNightActive = "night-active"

	defaultTimezone     = "America/New_York"
	defaultDawn         = "07:00"
	defaultDusk         = "19:00"
	defaultRotationTime = "00:00"

	// Zoom floors applied client-side — admins can see further out than
	// regular users. Stored in the setting table so an admin can retune
	// without a redeploy.
	defaultZoomMinAdmin   = 0.1
	defaultZoomMinRegular = 0.3
)

// worldConfig bundles the runtime state plus the admin-tunable settings.
type worldConfig struct {
	Phase            string
	LastTransitionAt time.Time
	LastRotationAt   time.Time
	DawnTime         string // "HH:MM"
	DuskTime         string
	RotationTime     string // "HH:MM"
	Timezone         string
	Location         *time.Location
	ZoomMinAdmin     float64
	ZoomMinRegular   float64
}

// pendingFlip is one scheduled village_object.current_state change. Applied
// by scheduleFlips — either immediately (SpreadSeconds=0) or at a random
// offset uniformly in [0, SpreadSeconds) seconds into the future.
//
// Gen captures App.WorldEventGen at schedule time so applyFlip can detect
// "my transition has been superseded" and bail without overwriting a newer
// transition's target state.
type pendingFlip struct {
	ObjectID      string
	NewState      string
	SpreadSeconds int
	Gen           uint64
}

// loadWorldConfig reads the world_phase row and the three world_* settings.
// Missing settings fall back to defaults so a fresh deploy doesn't crash the
// ticker before an admin has set anything.
func (app *App) loadWorldConfig(ctx context.Context) (*worldConfig, error) {
	cfg := &worldConfig{
		DawnTime:       defaultDawn,
		DuskTime:       defaultDusk,
		RotationTime:   defaultRotationTime,
		Timezone:       defaultTimezone,
		ZoomMinAdmin:   defaultZoomMinAdmin,
		ZoomMinRegular: defaultZoomMinRegular,
	}

	err := app.DB.QueryRow(ctx,
		`SELECT phase, last_transition_at, last_rotation_at FROM world_phase WHERE id = 1`,
	).Scan(&cfg.Phase, &cfg.LastTransitionAt, &cfg.LastRotationAt)
	if err != nil {
		return nil, fmt.Errorf("load world_phase: %w", err)
	}

	rows, err := app.DB.Query(ctx,
		`SELECT key, value FROM setting
		 WHERE key IN ('world_dawn_time', 'world_dusk_time', 'world_rotation_time',
		               'world_timezone', 'world_zoom_min_admin', 'world_zoom_min_regular')`,
	)
	if err != nil {
		return nil, fmt.Errorf("load world settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value *string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		if value == nil {
			continue
		}
		switch key {
		case "world_dawn_time":
			cfg.DawnTime = *value
		case "world_dusk_time":
			cfg.DuskTime = *value
		case "world_rotation_time":
			cfg.RotationTime = *value
		case "world_timezone":
			cfg.Timezone = *value
		case "world_zoom_min_admin":
			if f, err := strconv.ParseFloat(*value, 64); err == nil {
				cfg.ZoomMinAdmin = f
			}
		case "world_zoom_min_regular":
			if f, err := strconv.ParseFloat(*value, 64); err == nil {
				cfg.ZoomMinRegular = f
			}
		}
	}

	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Printf("world_phase: timezone %q invalid, falling back to %s: %v", cfg.Timezone, defaultTimezone, err)
		loc, _ = time.LoadLocation(defaultTimezone)
		cfg.Timezone = defaultTimezone
	}
	cfg.Location = loc

	return cfg, nil
}

// parseHM splits "HH:MM" into hour and minute integers.
func parseHM(s string) (hour, minute int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return hour, minute, nil
}

// mostRecentBoundary returns the phase and wall-clock time of the most recent
// dawn/dusk boundary at or before now. The search window is the last 24 hours,
// which always contains at least two boundaries.
func mostRecentBoundary(now time.Time, dawnH, dawnM, duskH, duskM int) (phase string, at time.Time) {
	loc := now.Location()
	y, mo, d := now.Date()
	todayDawn := time.Date(y, mo, d, dawnH, dawnM, 0, 0, loc)
	todayDusk := time.Date(y, mo, d, duskH, duskM, 0, 0, loc)

	candidates := []struct {
		t     time.Time
		phase string
	}{
		{todayDawn.Add(-24 * time.Hour), phaseDay},
		{todayDusk.Add(-24 * time.Hour), phaseNight},
		{todayDawn, phaseDay},
		{todayDusk, phaseNight},
	}

	var latestT time.Time
	var latestPhase string
	for _, c := range candidates {
		if !c.t.After(now) && c.t.After(latestT) {
			latestT = c.t
			latestPhase = c.phase
		}
	}
	return latestPhase, latestT
}

// nextBoundary returns the next dawn/dusk after now, plus the phase that
// boundary sets. Used by the GET /api/village/world endpoint for UI countdowns.
func nextBoundary(now time.Time, dawnH, dawnM, duskH, duskM int) (phase string, at time.Time) {
	loc := now.Location()
	y, mo, d := now.Date()
	todayDawn := time.Date(y, mo, d, dawnH, dawnM, 0, 0, loc)
	todayDusk := time.Date(y, mo, d, duskH, duskM, 0, 0, loc)

	candidates := []struct {
		t     time.Time
		phase string
	}{
		{todayDawn, phaseDay},
		{todayDusk, phaseNight},
		{todayDawn.Add(24 * time.Hour), phaseDay},
		{todayDusk.Add(24 * time.Hour), phaseNight},
	}

	for _, c := range candidates {
		if c.t.After(now) {
			return c.phase, c.t
		}
	}
	// Unreachable given 48h window, but keep the compiler happy.
	return phaseDay, now
}

// applyTransition moves the world to the given phase. Resolves the target
// state per asset (DISTINCT ON picks the lowest-id tagged state deterministically
// if an asset ever has multiple states under one tag), stamps world_phase
// synchronously, then hands per-object flips off to scheduleFlips — which may
// spread them over time per asset.transition_spread_seconds.
//
// The world_phase_changed broadcast fires immediately so clients start the
// CanvasModulate tween right at the boundary. Lamp/torch/campfire glows
// trickle in as scheduled flips land over the spread window.
//
// Safe to call when the current phase already matches — determineTransitionFlips
// filters rows whose current_state already equals the target, so scheduleFlips
// gets an empty list.
func (app *App) applyTransition(ctx context.Context, newPhase string) (int, error) {
	var tag string
	switch newPhase {
	case phaseDay:
		tag = tagDayActive
	case phaseNight:
		tag = tagNightActive
	default:
		return 0, fmt.Errorf("invalid phase %q", newPhase)
	}

	// If the village has a lamplighter on duty, he takes over the per-object
	// flips for lamplighter-target objects — walks the route, lights or
	// extinguishes each one individually on arrival. Non-lamp day/night
	// objects (campfires) still flip in the bulk pass.
	_, hasLamplighter := app.findNPCWithBehavior(ctx, behaviorLamplighter)

	var excludeTag string
	if hasLamplighter {
		excludeTag = tagLamplighterTarget
	}
	flips, err := app.determineTransitionFlips(ctx, tag, excludeTag)
	if err != nil {
		return 0, err
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE world_phase SET phase = $1, last_transition_at = NOW() WHERE id = 1`,
		newPhase,
	); err != nil {
		return 0, err
	}

	// Bump generation AFTER the DB write so anything racing against it sees a
	// consistent snapshot. Flips scheduled below carry this gen; any older
	// pending flips are now stale.
	gen := app.WorldEventGen.Add(1)
	for i := range flips {
		flips[i].Gen = gen
	}
	app.scheduleFlips(flips)

	// CanvasModulate tween starts immediately at the boundary; per-object
	// flips land via scheduleFlips (or the lamplighter's route) as their
	// windows elapse.
	app.Hub.Broadcast(WorldEvent{
		Type: "world_phase_changed",
		Data: map[string]interface{}{
			"phase":              newPhase,
			"last_transition_at": time.Now().UTC().Format(time.RFC3339),
		},
	})

	var lamplighterStops int
	if hasLamplighter {
		stops, err := app.startLamplighterRoute(ctx, tag)
		if err != nil {
			log.Printf("world_phase: lamplighter route failed: %v", err)
		}
		lamplighterStops = stops
	}

	log.Printf("world_phase: transitioned to %s (%d bulk flips, %d lamplighter stops)",
		newPhase, len(flips), lamplighterStops)
	return len(flips) + lamplighterStops, nil
}

// determineTransitionFlips returns the per-object flips needed to move every
// village_object into the target state for the given tag ('day-active' or
// 'night-active'). Each flip carries the owning asset's transition_spread_seconds
// so scheduleFlips can spread them individually.
//
// When excludeTag is non-empty, objects whose asset has a target_state also
// carrying excludeTag are dropped from the bulk flip — they're expected to
// be handled by an NPC route (e.g. lamplighter-target).
func (app *App) determineTransitionFlips(ctx context.Context, tag, excludeTag string) ([]pendingFlip, error) {
	query := `WITH target_states AS (
		    SELECT DISTINCT ON (s.asset_id) s.asset_id, s.id AS target_state_id, s.state AS target_state
		    FROM asset_state s
		    JOIN asset_state_tag t ON t.state_id = s.id
		    WHERE t.tag = $1
		    ORDER BY s.asset_id, s.id
		)
		SELECT o.id, ts.target_state, a.transition_spread_seconds
		FROM village_object o
		JOIN target_states ts ON ts.asset_id = o.asset_id
		JOIN asset a ON a.id = o.asset_id
		WHERE o.current_state IS DISTINCT FROM ts.target_state`
	args := []interface{}{tag}
	if excludeTag != "" {
		args = append(args, excludeTag)
		query += ` AND NOT EXISTS (
		    SELECT 1 FROM asset_state_tag t2 WHERE t2.state_id = ts.target_state_id AND t2.tag = $2
		)`
	}
	rows, err := app.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flips []pendingFlip
	for rows.Next() {
		var f pendingFlip
		if err := rows.Scan(&f.ObjectID, &f.NewState, &f.SpreadSeconds); err != nil {
			return nil, err
		}
		flips = append(flips, f)
	}
	return flips, nil
}

// scheduleFlips queues each flip via time.AfterFunc. Flips with SpreadSeconds > 0
// fire at a uniform-random offset in [0, SpreadSeconds) seconds; flips with
// SpreadSeconds == 0 fire immediately on a fresh goroutine (still async, but
// at ~zero delay).
//
// Each fired flip does its own idempotent UPDATE + broadcast, so flips from
// a given transition are independent — if the engine restarts mid-window, the
// startup catch-up (applyTransition with the current phase) will re-schedule
// any objects still in the wrong state.
func (app *App) scheduleFlips(flips []pendingFlip) {
	for _, f := range flips {
		flip := f
		var delay time.Duration
		if flip.SpreadSeconds > 0 {
			delay = time.Duration(mathrand.IntN(flip.SpreadSeconds)) * time.Second
		}
		time.AfterFunc(delay, func() {
			app.applyFlip(flip)
		})
	}
}

// applyFlip performs a single pending state change. Uses a fresh background
// context since this runs on an independent timer long after the originating
// request is gone. The IS DISTINCT FROM guard keeps it idempotent.
//
// Drops the flip if a newer world event (transition or rotation) has fired
// since scheduling. Without this, rapid Force Night → Force Day would let
// stale "turn on" flips land after the "turn off" transition, briefly
// re-lighting objects that should stay dark.
func (app *App) applyFlip(flip pendingFlip) {
	if flip.Gen != app.WorldEventGen.Load() {
		return
	}
	ctx := context.Background()
	_, err := app.DB.Exec(ctx,
		`UPDATE village_object SET current_state = $2
		 WHERE id = $1 AND current_state IS DISTINCT FROM $2`,
		flip.ObjectID, flip.NewState,
	)
	if err != nil {
		log.Printf("flip: object %s -> %s failed: %v", flip.ObjectID, flip.NewState, err)
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "object_state_changed",
		Data: map[string]string{"id": flip.ObjectID, "state": flip.NewState},
	})
}

// checkAndTransition does one iteration of the scheduler loop. It's safe to
// call at any time; it's a no-op when no boundary has been crossed since the
// last transition.
func (app *App) checkAndTransition(ctx context.Context) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("world_phase: failed to load config: %v", err)
		return
	}

	dawnH, dawnM, err := parseHM(cfg.DawnTime)
	if err != nil {
		log.Printf("world_phase: bad dawn time %q: %v", cfg.DawnTime, err)
		return
	}
	duskH, duskM, err := parseHM(cfg.DuskTime)
	if err != nil {
		log.Printf("world_phase: bad dusk time %q: %v", cfg.DuskTime, err)
		return
	}

	now := time.Now().In(cfg.Location)
	targetPhase, boundaryAt := mostRecentBoundary(now, dawnH, dawnM, duskH, duskM)

	// If we've already processed this boundary (or anything after it),
	// there's nothing to do. last_transition_at in the DB is stored as UTC;
	// boundaryAt is in cfg.Location — comparison still works because Go
	// normalizes both to instants.
	if !cfg.LastTransitionAt.Before(boundaryAt) {
		return
	}

	if _, err := app.applyTransition(ctx, targetPhase); err != nil {
		log.Printf("world_phase: transition to %s failed: %v", targetPhase, err)
	}
}

// handleGetWorldState returns the current phase plus timing info the client
// uses to render the config panel (last transition, next boundary, tunables).
// Also carries rotation state so the panel can show a "next rotation in Xh Ym"
// countdown alongside the phase countdown.
func (app *App) handleGetWorldState(w http.ResponseWriter, r *http.Request) {
	cfg, err := app.loadWorldConfig(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	dawnH, dawnM, err := parseHM(cfg.DawnTime)
	if err != nil {
		jsonError(w, "Invalid dawn time setting", http.StatusInternalServerError)
		return
	}
	duskH, duskM, err := parseHM(cfg.DuskTime)
	if err != nil {
		jsonError(w, "Invalid dusk time setting", http.StatusInternalServerError)
		return
	}
	rotH, rotM, err := parseHM(cfg.RotationTime)
	if err != nil {
		jsonError(w, "Invalid rotation time setting", http.StatusInternalServerError)
		return
	}

	now := time.Now().In(cfg.Location)
	nextPhase, nextAt := nextBoundary(now, dawnH, dawnM, duskH, duskM)
	nextRotationAt := nextRotationBoundary(now, rotH, rotM)

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"phase":                 cfg.Phase,
		"last_transition_at":    cfg.LastTransitionAt.UTC().Format(time.RFC3339),
		"dawn_time":             cfg.DawnTime,
		"dusk_time":             cfg.DuskTime,
		"timezone":              cfg.Timezone,
		"server_time":           now.Format(time.RFC3339),
		"next_transition_at":    nextAt.UTC().Format(time.RFC3339),
		"next_transition_phase": nextPhase,
		"rotation_time":         cfg.RotationTime,
		"last_rotation_at":      cfg.LastRotationAt.UTC().Format(time.RFC3339),
		"next_rotation_at":      nextRotationAt.UTC().Format(time.RFC3339),
		"zoom_min_admin":        cfg.ZoomMinAdmin,
		"zoom_min_regular":      cfg.ZoomMinRegular,
	})
}

// handleSetZoomSettings lets an admin retune the two client-side zoom floors.
// Both values are optional in the request; omitted fields are left alone.
func (app *App) handleSetZoomSettings(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	var req struct {
		ZoomMinAdmin   *float64 `json:"zoom_min_admin"`
		ZoomMinRegular *float64 `json:"zoom_min_regular"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ZoomMinAdmin == nil && req.ZoomMinRegular == nil {
		jsonError(w, "Provide zoom_min_admin and/or zoom_min_regular", http.StatusBadRequest)
		return
	}
	// Sanity floor/ceiling. 0.01 is already absurdly far out, 10.0 gigantic close.
	for _, v := range []*float64{req.ZoomMinAdmin, req.ZoomMinRegular} {
		if v == nil {
			continue
		}
		if *v < 0.01 || *v > 10.0 {
			jsonError(w, "zoom value out of range", http.StatusBadRequest)
			return
		}
	}

	if req.ZoomMinAdmin != nil {
		if err := app.upsertSetting(r.Context(), "world_zoom_min_admin", strconv.FormatFloat(*req.ZoomMinAdmin, 'f', -1, 64)); err != nil {
			log.Printf("set zoom_min_admin: %v", err)
			jsonError(w, "Failed to save zoom_min_admin", http.StatusInternalServerError)
			return
		}
	}
	if req.ZoomMinRegular != nil {
		if err := app.upsertSetting(r.Context(), "world_zoom_min_regular", strconv.FormatFloat(*req.ZoomMinRegular, 'f', -1, 64)); err != nil {
			log.Printf("set zoom_min_regular: %v", err)
			jsonError(w, "Failed to save zoom_min_regular", http.StatusInternalServerError)
			return
		}
	}

	// Broadcast so every connected client reloads its zoom floor without
	// having to refresh. Payload carries the freshly-persisted values
	// (load from DB so we emit truth, not the request).
	cfg, err := app.loadWorldConfig(r.Context())
	if err != nil {
		log.Printf("reload after zoom save: %v", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "zoom_settings_changed",
		Data: map[string]float64{
			"zoom_min_admin":   cfg.ZoomMinAdmin,
			"zoom_min_regular": cfg.ZoomMinRegular,
		},
	})
	w.WriteHeader(http.StatusNoContent)
}

// upsertSetting writes a single key/value to the setting table.
func (app *App) upsertSetting(ctx context.Context, key, value string) error {
	_, err := app.DB.Exec(ctx,
		`INSERT INTO setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = $2`,
		key, value,
	)
	return err
}

// handleForcePhase lets an admin jump the world to a specific phase (or toggle)
// without waiting for the scheduled boundary. Updates last_transition_at so the
// ticker treats this as the latest processed boundary.
func (app *App) handleForcePhase(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	var req struct {
		Phase string `json:"phase"` // "day" or "night"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Phase != phaseDay && req.Phase != phaseNight {
		jsonError(w, `phase must be "day" or "night"`, http.StatusBadRequest)
		return
	}

	affected, err := app.applyTransition(r.Context(), req.Phase)
	if err != nil {
		log.Printf("world_phase: force-phase failed: %v", err)
		jsonError(w, "Failed to apply transition", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"phase":            req.Phase,
		"objects_affected": affected,
	})
}
