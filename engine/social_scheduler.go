package main

// Per-NPC social hour evaluation (ZBBS-068).
//
// Orthogonal to the main `behavior` column: any NPC whose social_* columns
// are fully set participates regardless of their job. Two boundaries per day:
//
//   social-enter: walk from current location to the nearest structure
//     whose asset has any state carrying social_tag. startReturnWalk
//     flips inside_structure_id on arrival.
//
//   social-leave: walk back to the NPC's home door and flip inside=false
//     (startReturnWalk then sets inside=true on arrival at the home
//     structure — matches the worker-leave path).
//
// Idempotency: social_last_boundary_at stamps the processed boundary and is
// kept SEPARATE from last_shift_tick_at so the worker/rotation schedulers
// can't collide with this one. Edits to the social fields should clear
// social_last_boundary_at so the next tick re-evaluates — mirror the
// /schedule PATCH pattern in npcs.go when a UI lands.
//
// Requires home_structure_id — an NPC with no home has nowhere to return
// to when the window ends, so social scheduling is silently skipped.

import (
	"context"
	"database/sql"
	"log"
	"time"
)

type socialRow struct {
	ID                   string
	SocialTag            string
	SocialStartHour      int
	SocialEndHour        int
	SocialLastBoundaryAt sql.NullTime
	InsideStructureID    sql.NullString
	CurrentX             float64
	CurrentY             float64
	HomeStructureID      string
	HomeDoorX            float64
	HomeDoorY            float64
}

// loadSocialRows returns every NPC with a complete social schedule AND a
// home to return to. The all-or-none CHECK on the table guarantees the
// three social_* fields travel together; we additionally require
// home_structure_id since social-leave has no destination without one.
func (app *App) loadSocialRows(ctx context.Context) ([]socialRow, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT n.id, n.social_tag, n.social_start_hour, n.social_end_hour,
		        n.social_last_boundary_at, n.inside_structure_id,
		        n.current_x, n.current_y,
		        n.home_structure_id,
		        COALESCE(hs.x + ha.door_offset_x * 32.0, hs.x),
		        COALESCE(hs.y + ha.door_offset_y * 32.0, hs.y)
		 FROM npc n
		 JOIN village_object hs ON hs.id = n.home_structure_id
		 JOIN asset ha         ON ha.id = hs.asset_id
		 WHERE n.social_tag IS NOT NULL
		   AND n.home_structure_id IS NOT NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []socialRow
	for rows.Next() {
		var s socialRow
		if err := rows.Scan(&s.ID, &s.SocialTag, &s.SocialStartHour, &s.SocialEndHour,
			&s.SocialLastBoundaryAt, &s.InsideStructureID,
			&s.CurrentX, &s.CurrentY,
			&s.HomeStructureID, &s.HomeDoorX, &s.HomeDoorY); err != nil {
			log.Printf("social-scheduler: scan: %v", err)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

type socialBoundaryKind int

const (
	socialEnter socialBoundaryKind = iota
	socialLeave
)

// mostRecentSocialBoundary returns the most recent enter/leave boundary at
// or before now. Window wraps midnight when start > end (e.g. 20-02 for a
// late-night tavern shift). Considers yesterday/today/tomorrow so wrap
// windows resolve correctly near midnight — same approach as
// mostRecentWorkerBoundary / mostRecentRotationFiring.
func mostRecentSocialBoundary(now time.Time, startH, endH int) (time.Time, socialBoundaryKind) {
	loc := now.Location()
	y, mo, d := now.Date()

	type candidate struct {
		t    time.Time
		kind socialBoundaryKind
	}
	var cands [6]candidate
	for i, dayOffset := range []int{-1, 0, 1} {
		base := time.Date(y, mo, d+dayOffset, 0, 0, 0, 0, loc)
		start := base.Add(time.Duration(startH) * time.Hour)
		end := base.Add(time.Duration(endH) * time.Hour)
		if endH <= startH {
			end = end.Add(24 * time.Hour)
		}
		cands[i*2] = candidate{start, socialEnter}
		cands[i*2+1] = candidate{end, socialLeave}
	}

	var latestT time.Time
	var latestKind socialBoundaryKind
	for _, c := range cands {
		if !c.t.After(now) && c.t.After(latestT) {
			latestT = c.t
			latestKind = c.kind
		}
	}
	return latestT, latestKind
}

// findNearestStructureByTag returns the closest village_object carrying
// targetTag (per-instance tag, not asset-state tag), measured by squared
// Euclidean distance from (fromX, fromY). Returns the door-offset-adjusted
// coords so callers can pass them straight into startReturnWalk. Empty id
// + nil error means no match.
func (app *App) findNearestStructureByTag(ctx context.Context, targetTag string, fromX, fromY float64) (string, float64, float64, error) {
	row := app.DB.QueryRow(ctx,
		`SELECT o.id,
		        COALESCE(o.x + a.door_offset_x * 32.0, o.x),
		        COALESCE(o.y + a.door_offset_y * 32.0, o.y)
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 JOIN village_object_tag vot ON vot.object_id = o.id
		 WHERE vot.tag = $1
		 ORDER BY (o.x - $2) * (o.x - $2) + (o.y - $3) * (o.y - $3)
		 LIMIT 1`,
		targetTag, fromX, fromY,
	)
	var id string
	var dx, dy float64
	if err := row.Scan(&id, &dx, &dy); err != nil {
		if err == sql.ErrNoRows {
			return "", 0, 0, nil
		}
		return "", 0, 0, err
	}
	return id, dx, dy, nil
}

// evaluateSocialSchedule dispatches at most one walk per tick for the NPC.
// Stamps the boundary even when the walk is a no-op (already there, or no
// matching structure) so we don't spin against the same boundary every
// tick for the rest of the window.
func (app *App) evaluateSocialSchedule(ctx context.Context, s *socialRow, now time.Time) {
	boundaryAt, kind := mostRecentSocialBoundary(now, s.SocialStartHour, s.SocialEndHour)
	if boundaryAt.IsZero() {
		return
	}
	if s.SocialLastBoundaryAt.Valid && !s.SocialLastBoundaryAt.Time.Before(boundaryAt) {
		return
	}

	// stampBoundary marks this boundary as processed so future ticks skip.
	// Called from every early-exit below so the scheduler doesn't spin on
	// no-op evaluations (e.g. NPC isn't at the tavern, no tagged structure
	// exists, etc.).
	stampBoundary := func() {
		if _, err := app.DB.Exec(ctx,
			`UPDATE npc SET social_last_boundary_at = $2 WHERE id = $1`,
			s.ID, boundaryAt,
		); err != nil {
			log.Printf("social-scheduler: stamp %s: %v", s.ID, err)
		}
	}

	switch kind {
	case socialEnter:
		// Only walk the NPC in if they aren't already inside a tavern-tagged
		// structure. If they are (e.g. admin placed them there manually),
		// just stamp and exit — no redundant walk.
		if s.InsideStructureID.Valid {
			alreadyAtTavern, err := app.structureHasTag(ctx, s.InsideStructureID.String, s.SocialTag)
			if err == nil && alreadyAtTavern {
				stampBoundary()
				return
			}
		}
		id, dx, dy, err := app.findNearestStructureByTag(ctx, s.SocialTag, s.CurrentX, s.CurrentY)
		if err != nil {
			log.Printf("social-scheduler: find %s for %s: %v", s.SocialTag, s.ID, err)
			return
		}
		if id == "" {
			// No tagged structure exists. Stamp so we don't retry every
			// tick until the window ends; admin fixes by tagging a state.
			stampBoundary()
			return
		}
		npc := &behaviorNPC{ID: s.ID, CurX: s.CurrentX, CurY: s.CurrentY}
		app.interpolateCurrentPos(npc)
		if err := app.startReturnWalk(ctx, npc, dx, dy, id, "social-enter"); err != nil {
			log.Printf("social-scheduler: social-enter %s dispatch: %v", s.ID, err)
			return
		}
		stampBoundary()

	case socialLeave:
		// Critical: only walk NPC home if they're CURRENTLY inside a
		// tavern-tagged structure. Earlier versions walked every NPC home
		// at every leave boundary — which meant an admin setting social
		// hours mid-day yanked workers out of their shops because the
		// "most recent boundary ≤ now" was a leave boundary. That isn't
		// the intent: leave is the tavern→home transition, not a policy
		// enforced from any location.
		if !s.InsideStructureID.Valid {
			stampBoundary()
			return
		}
		atTavern, err := app.structureHasTag(ctx, s.InsideStructureID.String, s.SocialTag)
		if err != nil {
			log.Printf("social-scheduler: tag check %s: %v", s.ID, err)
			return
		}
		if !atTavern {
			// NPC is inside a non-tavern structure (likely their work or
			// home). Nothing to leave from. Stamp and exit.
			stampBoundary()
			return
		}
		npc := &behaviorNPC{ID: s.ID, CurX: s.CurrentX, CurY: s.CurrentY}
		app.interpolateCurrentPos(npc)
		if err := app.startReturnWalk(ctx, npc, s.HomeDoorX, s.HomeDoorY, s.HomeStructureID, "social-leave"); err != nil {
			log.Printf("social-scheduler: social-leave %s dispatch: %v", s.ID, err)
			return
		}
		stampBoundary()
	}
}

// structureHasTag reports whether the placed object carries the given
// per-instance tag. Used by the social scheduler to check whether an NPC
// is "at the tavern" before dispatching a tavern-related walk.
func (app *App) structureHasTag(ctx context.Context, objectID, tag string) (bool, error) {
	var present bool
	err := app.DB.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM village_object_tag
		               WHERE object_id = $1 AND tag = $2)`,
		objectID, tag,
	).Scan(&present)
	return present, err
}

// dispatchSocialSchedules is the per-tick entry, called from runServerTick.
// Errors on a single NPC log and continue so one bad row doesn't freeze
// the scheduler for the rest.
func (app *App) dispatchSocialSchedules(ctx context.Context) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("social-scheduler: load config: %v", err)
		return
	}
	now := time.Now().In(cfg.Location)

	rows, err := app.loadSocialRows(ctx)
	if err != nil {
		log.Printf("social-scheduler: load rows: %v", err)
	}
	for i := range rows {
		app.evaluateSocialSchedule(ctx, &rows[i], now)
	}
}
