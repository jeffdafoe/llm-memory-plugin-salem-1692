package main

// Daily rotation for assets whose states carry the 'rotatable' tag.
//
// At the configured world_rotation_time (default midnight in world_timezone),
// the ticker calls checkAndRotate. Every village_object whose current_state is
// 'rotatable'-tagged gets a new state picked from that asset's pool of
// rotatable states. The selection algorithm per asset is stored on asset.rotation_algo:
//
//   - random_per_object: each placed instance picks independently. Max variety
//     per visual sweep (every laundry line different).
//   - random_per_asset:  all placed instances of this asset flip to the same
//     newly chosen state. Visually uniform ("the town crier posted X today").
//   - deterministic:     cycle through rotatable states in asset_state.id
//     order, wrapping. Predictable progression.
//
// Flips are scheduled via scheduleFlips, so asset.transition_spread_seconds
// spreads the per-object updates over a window (laundry over 30 min, notices
// over 5 min, etc.).

import (
	"context"
	"encoding/json"
	"log"
	mathrand "math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const tagRotatable = "rotatable"

// applyRotation computes the per-object flips for a single rotation pass,
// stamps world_phase.last_rotation_at, and hands the flips to scheduleFlips.
// Returns the number of objects scheduled to rotate.
//
// If a washerwoman or town_crier NPC is on duty, their domain (laundry or
// notice-board tagged states) is excluded from the bulk flip. Whether we
// also fire their route here depends on schedule ownership:
//
//   - Legacy NPC (schedule fields NULL): fire the route from applyRotation,
//     anchored to world_rotation_time like every other global cycle.
//   - Custom-scheduled NPC: skip firing. The per-NPC scheduler
//     (dispatchScheduledBehaviors) owns their cadence. Laundry still
//     gets excluded from the bulk flip so the custom schedule's route is
//     the sole mutator.
func (app *App) applyRotation(ctx context.Context) (int, error) {
	var exclude []string
	washerwoman, hasWasherwoman := app.findNPCWithBehavior(ctx, behaviorWasherwoman)
	if hasWasherwoman {
		exclude = append(exclude, tagLaundry)
	}
	crier, hasCrier := app.findNPCWithBehavior(ctx, behaviorTownCrier)
	if hasCrier {
		exclude = append(exclude, tagNoticeBoard)
	}

	flips, err := app.determineRotationFlipsScoped(ctx, rotationScope{ExcludeTags: exclude})
	if err != nil {
		return 0, err
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE world_phase SET last_rotation_at = NOW() WHERE id = 1`,
	); err != nil {
		return 0, err
	}

	gen := app.WorldEventGen.Add(1)
	for i := range flips {
		flips[i].Gen = gen
	}
	app.scheduleFlips(flips)

	// Dispatch per-object NPC routes for legacy-scheduled domains we
	// excluded above. Custom-scheduled NPCs are left alone so their
	// per-NPC scheduler is the sole trigger.
	var washerStops, crierStops int
	if hasWasherwoman && !washerwoman.HasCustomSchedule {
		n, err := app.startRotationRouteForNPC(ctx, washerwoman, tagLaundry, "washerwoman")
		if err != nil {
			log.Printf("world_rotation: washerwoman route failed: %v", err)
		}
		washerStops = n
	}
	if hasCrier && !crier.HasCustomSchedule {
		n, err := app.startRotationRouteForNPC(ctx, crier, tagNoticeBoard, "town_crier")
		if err != nil {
			log.Printf("world_rotation: town_crier route failed: %v", err)
		}
		crierStops = n
	}

	log.Printf("world_rotation: %d bulk flips, %d washerwoman stops, %d town_crier stops",
		len(flips), washerStops, crierStops)
	return len(flips) + washerStops + crierStops, nil
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// rotationScope narrows the candidate set for a rotation pass. Tag scopes
// down to objects whose current state carries that tag; ExcludeTags removes
// objects whose current state carries any listed tag. Both empty = full
// rotatable pool.
type rotationScope struct {
	Tag         string
	ExcludeTags []string
}

// determineRotationFlips is the unscoped variant — used by code paths that
// just want all rotatable flips without NPC-domain filtering.
func (app *App) determineRotationFlips(ctx context.Context) ([]pendingFlip, error) {
	return app.determineRotationFlipsScoped(ctx, rotationScope{})
}

// determineRotationFlipsForTag returns flips only for objects whose current
// state carries the given tag. Used by washerwoman / town_crier to build
// their per-domain route.
func (app *App) determineRotationFlipsForTag(ctx context.Context, tag string) ([]pendingFlip, error) {
	return app.determineRotationFlipsScoped(ctx, rotationScope{Tag: tag})
}

// rotationCandidate holds one object that currently sits in a rotatable state,
// along with its asset's rotation metadata.
type rotationCandidate struct {
	ObjectID      string
	AssetID       string
	CurrentState  string
	Algo          string
	SpreadSeconds int
}

// determineRotationFlipsScoped loads every village_object currently in a
// rotatable state (optionally narrowed by scope), groups them by asset, and
// picks a new state per object according to the asset's rotation_algo.
// Emits one pendingFlip per object whose new state differs from its current
// state (single-state pools are a no-op for random_per_object — they've
// nowhere else to go).
func (app *App) determineRotationFlipsScoped(ctx context.Context, scope rotationScope) ([]pendingFlip, error) {
	// 1. Candidates: objects currently in a rotatable-tagged state, further
	//    filtered by scope. The candidate's current state must have the
	//    rotatable tag AND (if Tag set) also the scope tag AND (if
	//    ExcludeTags set) none of the excluded tags.
	query := `SELECT o.id, o.asset_id, o.current_state, a.rotation_algo, a.transition_spread_seconds
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 JOIN asset_state s ON s.asset_id = o.asset_id AND s.state = o.current_state
		 JOIN asset_state_tag t ON t.state_id = s.id
		 WHERE t.tag = $1`
	args := []interface{}{tagRotatable}
	if scope.Tag != "" {
		args = append(args, scope.Tag)
		query += ` AND EXISTS (SELECT 1 FROM asset_state_tag t2 WHERE t2.state_id = s.id AND t2.tag = $2)`
	}
	if len(scope.ExcludeTags) > 0 {
		args = append(args, scope.ExcludeTags)
		query += ` AND NOT EXISTS (SELECT 1 FROM asset_state_tag t2 WHERE t2.state_id = s.id AND t2.tag = ANY($` +
			strconv.Itoa(len(args)) + `))`
	}
	candRows, err := app.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var candidates []rotationCandidate
	for candRows.Next() {
		var c rotationCandidate
		if err := candRows.Scan(&c.ObjectID, &c.AssetID, &c.CurrentState, &c.Algo, &c.SpreadSeconds); err != nil {
			candRows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	candRows.Close()

	if len(candidates) == 0 {
		return nil, nil
	}

	// 2. Per-asset rotatable-state pools, ordered by asset_state.id so the
	//    deterministic algo has a stable sequence.
	poolRows, err := app.DB.Query(ctx,
		`SELECT s.asset_id, s.state
		 FROM asset_state s
		 JOIN asset_state_tag t ON t.state_id = s.id
		 WHERE t.tag = $1
		 ORDER BY s.asset_id, s.id`,
		tagRotatable,
	)
	if err != nil {
		return nil, err
	}
	pools := map[string][]string{}
	for poolRows.Next() {
		var assetID, state string
		if err := poolRows.Scan(&assetID, &state); err != nil {
			poolRows.Close()
			return nil, err
		}
		pools[assetID] = append(pools[assetID], state)
	}
	poolRows.Close()

	// 3. For random_per_asset, decide the single target state per touched asset
	//    before iterating. Every candidate from that asset lands on the same
	//    target.
	assetTarget := map[string]string{}
	for _, c := range candidates {
		if c.Algo != "random_per_asset" {
			continue
		}
		if _, ok := assetTarget[c.AssetID]; ok {
			continue
		}
		assetTarget[c.AssetID] = pickRandomExcluding(pools[c.AssetID], c.CurrentState)
	}

	// 4. Build flips. Skip any flip where the pool was empty or the chosen
	//    target equals the current state (single-state pools, or a random pick
	//    that happens to match — pickRandomExcluding avoids the latter unless
	//    the pool truly has one member).
	var flips []pendingFlip
	for _, c := range candidates {
		pool := pools[c.AssetID]
		if len(pool) == 0 {
			continue
		}
		var newState string
		switch c.Algo {
		case "random_per_object":
			newState = pickRandomExcluding(pool, c.CurrentState)
		case "random_per_asset":
			newState = assetTarget[c.AssetID]
		case "deterministic":
			newState = pickDeterministicNext(pool, c.CurrentState)
		default:
			// Unknown algo — skip. Shouldn't happen given the CHECK constraint.
			log.Printf("world_rotation: unknown rotation_algo %q for asset %s", c.Algo, c.AssetID)
			continue
		}
		if newState == c.CurrentState {
			continue
		}
		flips = append(flips, pendingFlip{
			ObjectID:      c.ObjectID,
			NewState:      newState,
			SpreadSeconds: c.SpreadSeconds,
		})
	}
	return flips, nil
}

// pickRandomExcluding returns a random state from pool that is not `current`.
// Falls back to pool[0] when the pool has a single member (no alternative).
func pickRandomExcluding(pool []string, current string) string {
	if len(pool) == 0 {
		return current
	}
	if len(pool) == 1 {
		return pool[0]
	}
	for {
		idx := mathrand.IntN(len(pool))
		if pool[idx] != current {
			return pool[idx]
		}
	}
}

// pickDeterministicNext returns the state after `current` in the pool,
// wrapping back to pool[0] after the last entry. If current isn't in the pool
// (unexpected — the candidate query should guarantee it is), returns pool[0].
func pickDeterministicNext(pool []string, current string) string {
	for i, s := range pool {
		if s == current {
			return pool[(i+1)%len(pool)]
		}
	}
	return pool[0]
}

// checkAndRotate is the rotation-side counterpart to checkAndTransition. One
// boundary per day at world_rotation_time. Safe to call every tick — it only
// acts when last_rotation_at sits before the most recent boundary.
func (app *App) checkAndRotate(ctx context.Context) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("world_rotation: failed to load config: %v", err)
		return
	}
	h, m, err := parseHM(cfg.RotationTime)
	if err != nil {
		log.Printf("world_rotation: bad rotation time %q: %v", cfg.RotationTime, err)
		return
	}

	now := time.Now().In(cfg.Location)
	boundary := mostRecentRotationBoundary(now, h, m)
	if !cfg.LastRotationAt.Before(boundary) {
		return
	}
	if _, err := app.applyRotation(ctx); err != nil {
		log.Printf("world_rotation: apply failed: %v", err)
	}
}

// mostRecentRotationBoundary returns the timestamp of the most recent daily
// rotation boundary at or before now. Rotation happens once per day at h:m.
func mostRecentRotationBoundary(now time.Time, h, m int) time.Time {
	loc := now.Location()
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, h, m, 0, 0, loc)
	if today.After(now) {
		return today.Add(-24 * time.Hour)
	}
	return today
}

// nextRotationBoundary returns the next daily rotation after now.
func nextRotationBoundary(now time.Time, h, m int) time.Time {
	loc := now.Location()
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, h, m, 0, 0, loc)
	if today.After(now) {
		return today
	}
	return today.Add(24 * time.Hour)
}

// handleForceRotate lets an admin trigger an immediate rotation pass without
// waiting for the scheduled boundary. Updates last_rotation_at so the ticker
// treats this as the latest processed boundary.
func (app *App) handleForceRotate(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	// No request body needed, but accept an empty JSON object gracefully.
	if r.Body != nil {
		var ignored struct{}
		_ = json.NewDecoder(r.Body).Decode(&ignored)
	}

	affected, err := app.applyRotation(r.Context())
	if err != nil {
		log.Printf("world_rotation: force-rotate failed: %v", err)
		jsonError(w, "Failed to apply rotation", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"objects_affected": affected,
	})
}

