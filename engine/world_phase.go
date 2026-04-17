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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	phaseDay   = "day"
	phaseNight = "night"

	tagDayActive   = "day-active"
	tagNightActive = "night-active"

	tickerInterval = 60 * time.Second

	defaultTimezone = "America/New_York"
	defaultDawn     = "07:00"
	defaultDusk     = "19:00"
)

// worldConfig bundles the runtime state plus the admin-tunable settings.
type worldConfig struct {
	Phase            string
	LastTransitionAt time.Time
	DawnTime         string // "HH:MM"
	DuskTime         string
	Timezone         string
	Location         *time.Location
}

// loadWorldConfig reads the world_phase row and the three world_* settings.
// Missing settings fall back to defaults so a fresh deploy doesn't crash the
// ticker before an admin has set anything.
func (app *App) loadWorldConfig(ctx context.Context) (*worldConfig, error) {
	cfg := &worldConfig{
		DawnTime: defaultDawn,
		DuskTime: defaultDusk,
		Timezone: defaultTimezone,
	}

	err := app.DB.QueryRow(ctx,
		`SELECT phase, last_transition_at FROM world_phase WHERE id = 1`,
	).Scan(&cfg.Phase, &cfg.LastTransitionAt)
	if err != nil {
		return nil, fmt.Errorf("load world_phase: %w", err)
	}

	rows, err := app.DB.Query(ctx,
		`SELECT key, value FROM setting WHERE key IN ('world_dawn_time', 'world_dusk_time', 'world_timezone')`,
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
		case "world_timezone":
			cfg.Timezone = *value
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

// applyTransition moves the world to the given phase: bulk-updates every
// village_object that has a state tagged for the new phase, broadcasts one
// object_state_changed event per affected row, and stamps world_phase with
// the new phase + last_transition_at.
//
// Safe to call even when the current phase already matches — the UPDATE just
// produces zero rows, but last_transition_at still advances.
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

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// DISTINCT ON picks a single target state per asset deterministically,
	// even if the catalog ever ends up with multiple states under the same
	// tag for one asset. Lowest state id wins — matches insertion order.
	rows, err := tx.Query(ctx,
		`WITH target_states AS (
		    SELECT DISTINCT ON (s.asset_id) s.asset_id, s.state AS target_state
		    FROM asset_state s
		    JOIN asset_state_tag t ON t.state_id = s.id
		    WHERE t.tag = $1
		    ORDER BY s.asset_id, s.id
		)
		UPDATE village_object o
		SET current_state = ts.target_state
		FROM target_states ts
		WHERE o.asset_id = ts.asset_id
		  AND o.current_state IS DISTINCT FROM ts.target_state
		RETURNING o.id, o.current_state`,
		tag,
	)
	if err != nil {
		return 0, err
	}

	type change struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	var changes []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.ID, &c.State); err != nil {
			rows.Close()
			return 0, err
		}
		changes = append(changes, c)
	}
	rows.Close()

	if _, err := tx.Exec(ctx,
		`UPDATE world_phase SET phase = $1, last_transition_at = NOW() WHERE id = 1`,
		newPhase,
	); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	for _, c := range changes {
		app.Hub.Broadcast(WorldEvent{
			Type: "object_state_changed",
			Data: map[string]string{"id": c.ID, "state": c.State},
		})
	}

	// Tell every connected client the world clock moved. Clients use this to
	// tween CanvasModulate between day and night color. Emit after the
	// per-object events so the darken/brighten runs alongside the state flips.
	app.Hub.Broadcast(WorldEvent{
		Type: "world_phase_changed",
		Data: map[string]interface{}{
			"phase":              newPhase,
			"last_transition_at": time.Now().UTC().Format(time.RFC3339),
		},
	})

	log.Printf("world_phase: transitioned to %s (%d objects flipped)", newPhase, len(changes))
	return len(changes), nil
}

// runPhaseTicker is the background loop that fires scheduled transitions.
// Wakes every tickerInterval, reads the latest config (so live setting edits
// take effect without a restart), and transitions if a boundary has been
// crossed since last_transition_at.
func (app *App) runPhaseTicker(ctx context.Context) {
	log.Printf("world_phase: ticker started (%s interval)", tickerInterval)
	ticker := time.NewTicker(tickerInterval)
	defer ticker.Stop()

	// Kick once at startup so a server that came up mid-phase catches up
	// without waiting for the first tick.
	app.checkAndTransition(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("world_phase: ticker stopping")
			return
		case <-ticker.C:
			app.checkAndTransition(ctx)
		}
	}
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

	now := time.Now().In(cfg.Location)
	nextPhase, nextAt := nextBoundary(now, dawnH, dawnM, duskH, duskM)

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"phase":                cfg.Phase,
		"last_transition_at":   cfg.LastTransitionAt.UTC().Format(time.RFC3339),
		"dawn_time":            cfg.DawnTime,
		"dusk_time":            cfg.DuskTime,
		"timezone":             cfg.Timezone,
		"server_time":          now.Format(time.RFC3339),
		"next_transition_at":   nextAt.UTC().Format(time.RFC3339),
		"next_transition_phase": nextPhase,
	})
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
		Phase string `json:"phase"` // "day", "night", or "toggle"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	target := req.Phase
	if target == "toggle" {
		var current string
		if err := app.DB.QueryRow(r.Context(),
			`SELECT phase FROM world_phase WHERE id = 1`,
		).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				jsonError(w, "World phase not initialized", http.StatusInternalServerError)
			} else {
				jsonError(w, "Internal server error", http.StatusInternalServerError)
			}
			return
		}
		if current == phaseDay {
			target = phaseNight
		} else {
			target = phaseDay
		}
	}

	if target != phaseDay && target != phaseNight {
		jsonError(w, `phase must be "day", "night", or "toggle"`, http.StatusBadRequest)
		return
	}

	affected, err := app.applyTransition(r.Context(), target)
	if err != nil {
		log.Printf("world_phase: force-phase failed: %v", err)
		jsonError(w, "Failed to apply transition", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"phase":            target,
		"objects_affected": affected,
	})
}
