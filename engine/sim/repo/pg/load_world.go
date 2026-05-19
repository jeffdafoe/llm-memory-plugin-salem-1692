package pg

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LoadWorld orchestrates the pg-backed cold-start of a sim.World. Calls
// each sub-repo's LoadAll in dependency order, validates cross-aggregate
// invariants against the loaded set, and returns the populated World.
//
// The function is intentionally INCOMPLETE today (Slice 14): it loads
// primary state and runs the consistency checks, but does NOT perform
// the post-load housekeeping that sim.LoadWorld does (rebuildIndices,
// LoadedAt, restartExpire* passes, SeedPriceBook, republish). That
// wiring lands at the main.go cutover slice. Until then this is the
// canonical orchestration shape for cutover-prep tests; nobody Runs the
// returned World yet.
//
// # notImpl tolerance
//
// Not every sub-repo has a pg implementation yet. pg.NewRepository
// wires Actors / Environment / Assets / Recipes / ItemKinds / Terrain /
// ActionLog as notImpl stubs that return errNotImpl from every method.
// LoadWorld tolerates that during the cutover-prep period:
//
//   - requireAllImpl=false (default for tests + interim wiring): each
//     notImpl LoadAll/Load returns errNotImpl, the loader logs a
//     warning naming the sub-repo, and the World's corresponding field
//     stays at its NewWorld-initialized empty default (empty map for
//     map-typed fields, zero-value for env/phase/settings, nil for
//     terrain).
//
//   - requireAllImpl=true (production cutover): a notImpl repo is a
//     hard error. main.go flips this at cutover time, the moment every
//     sub-repo is expected to have a real pg-impl.
//
// Non-notImpl errors (real SQL failures, schema drift, scan errors)
// always surface as hard errors regardless of the flag.
//
// # Dependency order
//
// Sub-repos load in an order that lets the cross-aggregate checks see
// the right peer state:
//
//  1. VillageObjects — needed by the bridge check (Slice 12).
//  2. Structures — needed by the structure-bound scene orphan check
//     and the bridge check.
//  3. Huddles — needed by the Scene.Huddles existence check.
//  4. Scenes — depends on Structures + Huddles.
//  5. Orders — independent; loaded last for symmetry with sim.LoadWorld.
//  6. notImpl loaders (Actors / Environment / Assets / Recipes /
//     ItemKinds / Terrain) — order doesn't matter, they all either
//     no-op-with-warning or hard-fail uniformly.
//
// # Cross-aggregate consistency checks
//
// Run after every sub-repo has loaded, in this order (most fundamental
// to most tolerant of out-of-band edits):
//
//  1. structure.id ↔ village_object.id::text bridge — HARD ERROR.
//     Slice 12 contract: every Structure shares its ID with a
//     VillageObject (single source of truth for door / loiter pin /
//     footprint anchors). Bridge violations are deploy-time migration
//     corruption.
//
//  2. Scene.Huddles → Huddle existence — HARD ERROR. Scene.Huddles is
//     canonical in v2 (Slice 13 R1: persisted exactly via
//     scene_huddle_ref). A reference to a missing huddle is substrate
//     corruption.
//
//  3. Structure-bound scene `bound_structure_id` → Structure existence
//     — WARN-AND-DROP. Admin / dev tooling may legitimately delete a
//     structure without cascading to scenes (structure deletion is
//     out-of-band by design; the scene→structure ref isn't an FK).
//     The orphan scene is dropped from w.Scenes with a warning;
//     cascade lifetime cleans up naturally on next checkpoint. A
//     structure-bound scene with nil StructureID is a separate
//     corruption case and is a HARD ERROR (not warn-and-drop).
//
// # Out of scope (Slice 14)
//
//   - Actor reconciliations from loaded peer state (Slice 11
//     carry-forward: actor.current_huddle_id from huddle_member;
//     Slice 12 carry-forward: actor.{home,work,inside}_structure_id +
//     actor.inside_room_id). Blocked on Actors-pg-impl. Hook stubs +
//     TODO comments mark the call sites.
//
//   - main.go wiring.
//
//   - Snapshot-isolation Tx wrapping the multi-query load. Today's
//     single-pool READ COMMITTED is safe because LoadWorld runs
//     before the world goroutine starts and before any checkpoint
//     writer can mutate these tables. Multi-process scenarios are
//     post-cutover.
func LoadWorld(ctx context.Context, repo sim.Repository, requireAllImpl bool) (*sim.World, error) {
	w := sim.NewWorld(repo)

	// Step 1: VillageObjects (no peer deps; first because Structures
	// bridge check depends on the loaded set).
	villageObjects, err := repo.VillageObjects.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg LoadWorld: VillageObjects.LoadAll: %w", err)
	}
	w.VillageObjects = villageObjects

	// Step 2: Structures.
	structures, err := repo.Structures.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg LoadWorld: Structures.LoadAll: %w", err)
	}
	w.Structures = structures

	// Step 3: Huddles (no peer deps; loaded before Scenes for the
	// Scene.Huddles ref check).
	huddles, err := repo.Huddles.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg LoadWorld: Huddles.LoadAll: %w", err)
	}
	w.Huddles = huddles

	// Step 4: Scenes (depends on Structures + Huddles for the checks).
	scenes, err := repo.Scenes.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg LoadWorld: Scenes.LoadAll: %w", err)
	}
	w.Scenes = scenes

	// Step 5: Orders.
	orders, err := repo.Orders.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg LoadWorld: Orders.LoadAll: %w", err)
	}
	w.Orders = orders

	// Step 6: notImpl-tolerant loaders. Each call site uses the
	// (loaded bool, err error) shape from handleNotImpl: `loaded` is
	// true iff the sub-repo's load succeeded (err was nil before
	// notImpl detection); `err` carries any hard failure (real SQL
	// error, or notImpl-with-requireAllImpl=true). ActionLog has no
	// LoadAll (Append-only sink), so it's NOT part of this loop;
	// cutover-time main.go wiring is where ActionLog gains a pg
	// projection.
	actors, actorsErr := repo.Actors.LoadAll(ctx)
	loaded, err := handleNotImpl("Actors", actorsErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.Actors = actors
	}

	env, phase, settings, envErr := repo.Environment.Load(ctx)
	loaded, err = handleNotImpl("Environment", envErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.Environment = env
		w.Phase = phase
		w.Settings = settings
	}

	assets, assetsErr := repo.Assets.LoadAll(ctx)
	loaded, err = handleNotImpl("Assets", assetsErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.Assets = assets
	}

	recipes, recipesErr := repo.Recipes.LoadAll(ctx)
	loaded, err = handleNotImpl("Recipes", recipesErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.Recipes = recipes
	}

	itemKinds, itemKindsErr := repo.ItemKinds.LoadAll(ctx)
	loaded, err = handleNotImpl("ItemKinds", itemKindsErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.ItemKinds = itemKinds
	}

	terrain, terrainErr := repo.Terrain.Load(ctx)
	loaded, err = handleNotImpl("Terrain", terrainErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.Terrain = terrain
	}

	// Cross-aggregate consistency checks. Order: bridge first (most
	// fundamental — corrupt deploy state), then huddle refs (substrate
	// invariant), then structure-bound orphan scenes (legitimate drift,
	// warn-and-drop). The orphan-scene pass also hard-errors on
	// internally-malformed scenes (Bound.Kind=structure with nil
	// StructureID) — that's corruption, not a legitimate orphan.
	if err := checkBridgeInvariant(w.Structures, w.VillageObjects); err != nil {
		return nil, err
	}
	if err := checkSceneHuddleRefs(w.Scenes, w.Huddles); err != nil {
		return nil, err
	}
	if err := dropStructureBoundOrphanScenes(w.Scenes, w.Structures); err != nil {
		return nil, err
	}

	// TODO(Slice 11 carry-forward): once Actors-pg-impl lands, reconcile
	// actor.CurrentHuddleID from Huddle.Members. Each actor referenced
	// in any loaded huddle's Members set gets its CurrentHuddleID
	// stamped to that huddle's ID. Conflicting memberships (an actor in
	// two huddles' Members sets) are substrate corruption — hard error.

	// TODO(Slice 12 carry-forward): once Actors-pg-impl lands, populate
	// actor.{Home,Work,Inside}StructureID and actor.InsideRoomID from
	// their actor-table columns (loaded via Actors.LoadAll). Slice 12
	// dropped the FKs (CASCADE pathology) but left the columns in
	// place; the substrate invariant that those refs resolve against
	// loaded Structures is enforced here at LoadWorld time.

	return w, nil
}

// handleNotImpl translates a sub-repo LoadAll/Load error into the
// notImpl tolerance contract. Returns (loaded, error):
//
//   - (true, nil): the call succeeded — caller MUST write the loaded
//     value into the World.
//   - (false, nil): the call returned errNotImpl with
//     requireAllImpl=false. Logs a warning and lets the caller skip
//     the World field write so the NewWorld empty default is preserved.
//   - (false, wrapped): real SQL failure, or errNotImpl with
//     requireAllImpl=true. Caller propagates the error.
//
// Returning a bool (rather than relying on the caller to re-check err
// at the outer scope) removes the shadowed-err / outer-err pattern that
// would otherwise repeat at every call site.
func handleNotImpl(repoName string, err error, requireAllImpl bool) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errNotImpl) {
		if requireAllImpl {
			return false, fmt.Errorf("pg LoadWorld: %s sub-repo is notImpl and requireAllImpl=true: %w", repoName, err)
		}
		log.Printf("pg LoadWorld: %s sub-repo is notImpl — skipping (set requireAllImpl=true at cutover)", repoName)
		return false, nil
	}
	return false, fmt.Errorf("pg LoadWorld: %s sub-repo load: %w", repoName, err)
}

// checkBridgeInvariant verifies Slice 12's shared-identity bridge: every
// Structure.ID must equal some VillageObject.ID. Violations indicate
// migration corruption — the migration's backfill step is the one and
// only authorized writer of new structure rows pre-cutover. Hard error.
//
// The map key is the authoritative loaded identity (it's what every
// other check + every consumer of w.Structures looks up by). The
// function also rejects map-key / s.ID disagreement as an internal-
// consistency failure — pg.LoadAll keys by the row's id column, so a
// mismatch indicates the loader returned an inconsistent map.
//
// Empty Structures map: trivially passes (no entries to validate).
func checkBridgeInvariant(structures map[sim.StructureID]*sim.Structure, villageObjects map[sim.VillageObjectID]*sim.VillageObject) error {
	for sid, s := range structures {
		if s == nil {
			return fmt.Errorf("pg LoadWorld: bridge check: nil Structure at map key=%s", sid)
		}
		if s.ID != sid {
			return fmt.Errorf("pg LoadWorld: bridge check: Structure at map key=%s has mismatched s.ID=%s (loader returned inconsistent map)", sid, s.ID)
		}
		if _, ok := villageObjects[sim.VillageObjectID(sid)]; !ok {
			return fmt.Errorf("pg LoadWorld: bridge check: structure.id=%s has no matching village_object row (shared-identity bridge violation — migration corruption or out-of-band write)", sid)
		}
	}
	return nil
}

// checkSceneHuddleRefs verifies that every HuddleID referenced by a
// Scene.Huddles set exists in the loaded Huddles map. Scene.Huddles is
// canonical (Slice 13 R1 — persisted exactly via scene_huddle_ref);
// a reference to a missing huddle is substrate corruption.
// Hard error.
//
// Same map-key authoritative posture as checkBridgeInvariant: the
// function uses the map key for identity and rejects map-key / s.ID
// disagreement defensively (loader returned inconsistent map).
//
// Empty Scenes map: trivially passes.
func checkSceneHuddleRefs(scenes map[sim.SceneID]*sim.Scene, huddles map[sim.HuddleID]*sim.Huddle) error {
	for sid, s := range scenes {
		if s == nil {
			return fmt.Errorf("pg LoadWorld: scene huddle ref check: nil Scene at map key=%s", sid)
		}
		if s.ID != sid {
			return fmt.Errorf("pg LoadWorld: scene huddle ref check: Scene at map key=%s has mismatched s.ID=%s (loader returned inconsistent map)", sid, s.ID)
		}
		for hid := range s.Huddles {
			if _, ok := huddles[hid]; !ok {
				return fmt.Errorf("pg LoadWorld: scene huddle ref check: scene id=%s references missing huddle id=%s (Scene.Huddles is canonical — substrate corruption)", sid, hid)
			}
		}
	}
	return nil
}

// dropStructureBoundOrphanScenes removes from the scenes map any
// structure-bound scene whose bound_structure_id is absent from the
// loaded Structures map. Warn-and-drop (NOT hard error): structure
// deletion is out-of-band by design — admin/dev tools may delete a
// structure without cascading to scenes, and the scene→structure ref
// isn't an FK. Slice 13 design_review #7 settled this posture.
//
// A structure-bound scene with nil StructureID is NOT a legitimate
// orphan — it's internal corruption (validateBoundShape rejects this
// at SaveSnapshot and scanBound rejects it at LoadAll). This function
// hard-errors on it rather than silently dropping it.
//
// Mutates scenes in place; caller's w.Scenes is the same map. Logs an
// aggregate "N dropped" line if any were dropped, in addition to the
// per-scene warning.
//
// Empty scenes map: no-op.
func dropStructureBoundOrphanScenes(scenes map[sim.SceneID]*sim.Scene, structures map[sim.StructureID]*sim.Structure) error {
	var dropped int
	for sid, s := range scenes {
		if s == nil {
			continue
		}
		if s.Bound.Kind != sim.SceneBoundStructure {
			continue
		}
		if s.Bound.StructureID == nil {
			return fmt.Errorf("pg LoadWorld: structure-bound scene id=%s has nil StructureID (corrupt Bound — should have been rejected at LoadAll)", sid)
		}
		if _, ok := structures[*s.Bound.StructureID]; !ok {
			log.Printf("pg LoadWorld: dropping structure-bound scene id=%s — referenced structure id=%s missing (admin out-of-band delete; cascade lifetime will clean on next checkpoint)", sid, *s.Bound.StructureID)
			delete(scenes, sid)
			dropped++
		}
	}
	if dropped > 0 {
		log.Printf("pg LoadWorld: dropped %d structure-bound orphan scene(s)", dropped)
	}
	return nil
}
