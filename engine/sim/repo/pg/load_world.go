package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LoadWorld orchestrates the pg-backed cold-start of a sim.World. Calls
// each sub-repo's LoadAll in dependency order, validates cross-aggregate
// invariants against the loaded set, runs the actor carry-forwards, then
// finalizes via World.FinalizeLoad and returns a runnable World.
//
// Post-load housekeeping (index rebuild, restart expiry/re-stamp passes,
// sequence-counter floors, price-book seed, snapshot publish) is shared
// with sim.LoadWorld through World.FinalizeLoad, so both orchestrators
// finalize identically. What still stands between this and a running
// server is the engine entrypoint wiring (command channel, HTTP handlers,
// checkpoint writer) plus Quotes/PayLedger load — each its own slice.
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
//  6. notImpl-tolerant loaders (Actors / Environment / Assets /
//     Sprites / AttributeDefinitions / Recipes / ItemKinds / Terrain) —
//     order doesn't matter, they all either load, no-op-with-warning, or
//     hard-fail uniformly via handleNotImpl.
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
// # Actor carry-forwards (Slice 1 / ZBBS-WORK-243)
//
// After cross-aggregate consistency runs (and only if Actors actually
// loaded — notImpl tolerance), two actor-side reconciliations enforce
// invariants that couldn't be checked at single-aggregate granularity:
//
//   - reconcileActorHuddleMembership rebuilds actor.CurrentHuddleID
//     from canonical Huddle.Members (Slice 11 carry-forward).
//   - validateActorStructureRefs enforces that actor.{Home,Work,Inside}
//     StructureID + actor.InsideRoomID resolve against loaded
//     Structures (Slice 12 carry-forward; FKs dropped, invariant
//     stays).
//   - rebuildActorAttributeProjections materializes Actor.Businessowner
//     State + RestockPolicy from the raw actor_attribute rows the
//     ActorsRepo loaded into Actor.Attributes (Slice 3 carry-forward;
//     the pg layer stays a dumb mirror, projection logic lives here).
//
// # Out of scope
//
//   - Engine entrypoint wiring (command channel, HTTP handlers,
//     checkpoint writer) — its own slice.
//
//   - Quotes / PayLedger load — no repo in the facade yet, so their
//     restart passes inside FinalizeLoad iterate empty maps until those
//     slices land.
//
//   - Snapshot-isolation Tx wrapping the multi-query load. Today's
//     single-pool READ COMMITTED is safe because LoadWorld runs
//     before the world goroutine starts and before any checkpoint
//     writer can mutate these tables. Multi-process scenarios are a
//     later concern.
func LoadWorld(ctx context.Context, repo sim.Repository, requireAllImpl bool) (*sim.World, error) {
	w := sim.NewWorld(repo)

	// Step 1: VillageObjects (no peer deps; first because Structures
	// bridge check depends on the loaded set).
	villageObjects, err := repo.VillageObjects.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg LoadWorld: VillageObjects.LoadAll: %w", err)
	}
	w.VillageObjects = villageObjects

	// Step 2: Structures. Loaded through handleNotImpl so the carry-
	// forward gating below can tell "loaded successfully" from "notImpl
	// tolerated" — without this distinction, a notImpl Structures with
	// requireAllImpl=false would silently make validateActorStructureRefs
	// false-positive every actor with non-empty structure refs.
	structures, structuresErr := repo.Structures.LoadAll(ctx)
	structuresLoaded, err := handleNotImpl("Structures", structuresErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if structuresLoaded {
		w.Structures = structures
	}

	// Step 3: Huddles (no peer deps; loaded before Scenes for the
	// Scene.Huddles ref check). Same handleNotImpl posture as Structures
	// — gating the Slice 11 reconciliation below requires knowing
	// "really loaded" vs "tolerated empty."
	huddles, huddlesErr := repo.Huddles.LoadAll(ctx)
	huddlesLoaded, err := handleNotImpl("Huddles", huddlesErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if huddlesLoaded {
		w.Huddles = huddles
	}

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
	actorsLoaded, err := handleNotImpl("Actors", actorsErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if actorsLoaded {
		w.Actors = actors
	}

	env, phase, settings, envErr := repo.Environment.Load(ctx)
	loaded, err := handleNotImpl("Environment", envErr, requireAllImpl)
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

	sprites, spritesErr := repo.Sprites.LoadAll(ctx)
	loaded, err = handleNotImpl("Sprites", spritesErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.Sprites = sprites
	}

	attributeDefinitions, attributeDefinitionsErr := repo.AttributeDefinitions.LoadAll(ctx)
	loaded, err = handleNotImpl("AttributeDefinitions", attributeDefinitionsErr, requireAllImpl)
	if err != nil {
		return nil, err
	}
	if loaded {
		w.AttributeDefinitions = attributeDefinitions
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

	// Slice 1 / ZBBS-WORK-243 — actor carry-forwards. Each reconciliation
	// is gated on BOTH actors and its peer aggregate loading successfully:
	//
	//   - reconcileActorHuddleMembership requires Huddles.Members as the
	//     canonical source. If Huddles is notImpl-tolerated (w.Huddles
	//     empty), this would otherwise clear every actor's
	//     CurrentHuddleID — silently corrupting the cache.
	//   - validateActorStructureRefs would otherwise hard-error any
	//     actor with structure refs the moment Structures is tolerated
	//     empty.
	if actorsLoaded && huddlesLoaded {
		// Slice 11 carry-forward: rebuild actor.CurrentHuddleID from
		// canonical Huddle.Members.
		if err := reconcileActorHuddleMembership(w.Actors, w.Huddles); err != nil {
			return nil, err
		}
	}
	if actorsLoaded && structuresLoaded {
		// Slice 12 carry-forward: validate actor.{Home,Work,Inside}
		// StructureID and actor.InsideRoomID against loaded Structures.
		// Slice 12 dropped the FKs (CASCADE pathology) but left the
		// columns; substrate consistency is enforced here.
		if err := validateActorStructureRefs(w.Actors, w.Structures); err != nil {
			return nil, err
		}
	}
	if actorsLoaded {
		// Slice 3 / ZBBS-WORK-245 carry-forward: rebuild the businessowner
		// + restock projections from each actor's raw actor_attribute rows.
		// No peer aggregate needed (works from each actor's own Attributes);
		// best-effort by design — never fails the load.
		rebuildActorAttributeProjections(w.Actors)
	}

	// Post-load housekeeping shared with sim.LoadWorld (index rebuild,
	// restart expiry/re-stamp passes, sequence-counter floors, price-book
	// seed, snapshot publish). Runs last on purpose: rebuildIndices inside
	// FinalizeLoad reads actor.CurrentHuddleID, which the carry-forwards
	// above have just reconciled from canonical Huddle.Members.
	w.FinalizeLoad(ctx)

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

// reconcileActorHuddleMembership rebuilds actor.CurrentHuddleID from
// the canonical Huddle.Members set. v1 stored membership both on the
// actor row and in huddle_member; v2 elects Huddle.Members as the
// canonical direction (Slice 11) — actor.CurrentHuddleID is a
// denormalized cache. At LoadWorld time we OVERWRITE the cache with
// whatever Members says, discarding any drift the actor row had.
//
// Substrate violations:
//
//   - Member references a missing actor — hard error. FK CASCADE
//     should make this unreachable from valid writes.
//   - An actor appears in two huddles' Members sets — hard error.
//     The single-active-huddle-per-actor invariant is enforced by a
//     UNIQUE(actor_id) on huddle_member; a duplicate here means
//     schema drift or out-of-band INSERT.
//
// Empty Actors map: no-op. Empty Huddles map: clears every actor's
// CurrentHuddleID (correct — no huddles, no memberships).
func reconcileActorHuddleMembership(actors map[sim.ActorID]*sim.Actor, huddles map[sim.HuddleID]*sim.Huddle) error {
	// Clear every actor's cached huddle first; we'll re-stamp from
	// Members. This makes the canonical direction explicit and avoids
	// preserving stale values for actors no huddle claims.
	for _, a := range actors {
		if a == nil {
			continue
		}
		a.CurrentHuddleID = ""
	}
	// Re-stamp from canonical Members. Track per-actor sightings so we
	// can hard-error on duplicate memberships.
	claimed := make(map[sim.ActorID]sim.HuddleID)
	for hid, h := range huddles {
		if h == nil {
			continue
		}
		for actorID := range h.Members {
			a, ok := actors[actorID]
			if !ok {
				return fmt.Errorf("pg LoadWorld: actor-huddle reconciliation: huddle id=%s lists missing actor id=%s (FK CASCADE should make this unreachable — schema drift or out-of-band write)",
					hid, actorID)
			}
			if prior, dup := claimed[actorID]; dup {
				return fmt.Errorf("pg LoadWorld: actor-huddle reconciliation: actor id=%s appears in two huddles' Members (%s and %s) — single-active-huddle invariant violated",
					actorID, prior, hid)
			}
			claimed[actorID] = hid
			a.CurrentHuddleID = hid
		}
	}
	return nil
}

// validateActorStructureRefs enforces the substrate invariant that
// every non-empty actor.{Home,Work,Inside}StructureID resolves against
// the loaded Structures map and every non-zero actor.InsideRoomID
// resolves against some loaded Structure's Rooms. Slice 12 dropped the
// FKs from these columns (the CASCADE pathology bit the load path),
// but the consistency contract still holds — v2 owns the integrity
// check here at LoadWorld.
//
// Hard error on any unresolved ref. Out-of-band structure deletion is
// NOT a legitimate cause for drift on actor refs (unlike the Scene
// orphan-drop case, where admin tools may legitimately remove a
// structure mid-edit); actor refs are engine-authored exclusively.
//
// Empty Actors map: no-op. Empty Structures map with actors that have
// non-empty refs: hard error (every ref unresolved).
func validateActorStructureRefs(actors map[sim.ActorID]*sim.Actor, structures map[sim.StructureID]*sim.Structure) error {
	// Build a one-shot RoomID → Structure index. Each Structure carries
	// a Rooms slice; a flat map lets InsideRoomID resolve in one lookup.
	// Index-build also validates two invariants that a malformed loaded
	// set could otherwise smuggle through:
	//   - Nested room's StructureID must match its parent (otherwise
	//     the room belongs to one structure in the slice but reports
	//     membership in another — substrate corruption).
	//   - RoomID uniqueness across all structures (last-writer-wins
	//     would silently let an actor's ref resolve against the wrong
	//     structure).
	roomIndex := make(map[sim.RoomID]sim.StructureID)
	for sid, s := range structures {
		if s == nil {
			continue
		}
		for _, r := range s.Rooms {
			if r == nil {
				continue
			}
			if r.StructureID != "" && r.StructureID != sid {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: room id=%d is nested under structure %s but has StructureID=%s (substrate corruption)",
					r.ID, sid, r.StructureID)
			}
			if prior, dup := roomIndex[r.ID]; dup {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: duplicate RoomID=%d (appears under structures %s and %s)",
					r.ID, prior, sid)
			}
			roomIndex[r.ID] = sid
		}
	}
	for aid, a := range actors {
		if a == nil {
			continue
		}
		if a.HomeStructureID != "" {
			if _, ok := structures[a.HomeStructureID]; !ok {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: actor id=%s HomeStructureID=%s missing from loaded Structures",
					aid, a.HomeStructureID)
			}
		}
		if a.WorkStructureID != "" {
			if _, ok := structures[a.WorkStructureID]; !ok {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: actor id=%s WorkStructureID=%s missing from loaded Structures",
					aid, a.WorkStructureID)
			}
		}
		if a.InsideStructureID != "" {
			if _, ok := structures[a.InsideStructureID]; !ok {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: actor id=%s InsideStructureID=%s missing from loaded Structures",
					aid, a.InsideStructureID)
			}
		}
		if a.InsideRoomID != 0 {
			owner, ok := roomIndex[a.InsideRoomID]
			if !ok {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: actor id=%s InsideRoomID=%d missing from any loaded Structure's Rooms",
					aid, a.InsideRoomID)
			}
			// Room exists. If the actor also claims InsideStructureID,
			// the room must belong to that structure — otherwise
			// the locomotion / room-access invariants are broken.
			if a.InsideStructureID != "" && owner != a.InsideStructureID {
				return fmt.Errorf("pg LoadWorld: actor structure ref check: actor id=%s InsideRoomID=%d belongs to structure %s but InsideStructureID=%s",
					aid, a.InsideRoomID, owner, a.InsideStructureID)
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

// businessownerSlug is the actor_attribute slug that marks a hospitality
// keeper. Mirrors engine/businessowner.go's businessownerSlug constant.
const businessownerSlug = "businessowner"

// businessownerParamsRow is the JSONB shape read for the keeper flavor.
// Repo-local DTO — persistence detail kept out of the sim domain types.
type businessownerParamsRow struct {
	Flavor string `json:"flavor"`
}

// restockParamsRow / restockEntryRow mirror the v1 stored shape of an
// actor_attribute.params blob's restock array ({item, source, max,
// target}) so a v1-written or hand-seeded row round-trips. Repo-local.
type restockParamsRow struct {
	Restock []restockEntryRow `json:"restock"`
}

type restockEntryRow struct {
	Item   string `json:"item"`
	Source string `json:"source"`
	Max    int    `json:"max,omitempty"`
	Target int    `json:"target,omitempty"`
}

// rebuildActorAttributeProjections reconstructs the two derived views that
// Slice 3 deliberately keeps OUT of the pg layer: Actor.BusinessownerState
// (from the `businessowner` attribute's params.flavor) and
// Actor.RestockPolicy (unioned from every attribute's params.restock). The
// ActorsRepo loads actor_attribute rows as raw params bytes into
// Actor.Attributes; this pass walks those raw rows and materializes the
// projections, mirroring v1's loadBusinessownerFlavor +
// loadActorRestockPolicy exactly:
//
//   - Keeper iff the businessowner attribute is present AND its
//     params.flavor is non-empty (v1 skips a missing/empty flavor).
//   - Restock entries union across ALL attributes in slug order;
//     first-listed wins on item ties; unparseable params and
//     unknown-source entries are skipped.
//
// Best-effort by design — it never fails the load. A malformed params blob
// is logged (businessowner) or silently skipped (restock), exactly as v1
// did: operator config is best-effort and other roles on the same actor
// may still be valid. Gated by the caller on actorsLoaded.
//
// nil RestockPolicy / nil BusinessownerState (rather than empty structs)
// marks "not a restocker / not a keeper", matching the sim field
// semantics (RestockPolicy.ProduceEntries and the businessowner triggers
// both treat nil as "skip").
func rebuildActorAttributeProjections(actors map[sim.ActorID]*sim.Actor) {
	for aid, a := range actors {
		if a == nil {
			continue
		}
		// Idempotent: clear any prior projection before rebuilding so a
		// re-run on an actor whose attributes no longer yield a keeper /
		// restocker doesn't leave stale derived state behind.
		a.BusinessownerState = nil
		a.RestockPolicy = nil
		// BusinessownerState — from the businessowner attribute's flavor.
		if raw, ok := a.Attributes[businessownerSlug]; ok && len(raw) > 0 {
			var bo businessownerParamsRow
			if err := json.Unmarshal(raw, &bo); err != nil {
				log.Printf("pg LoadWorld: actor id=%s businessowner params unparseable — skipping keeper projection: %v", aid, err)
			} else if bo.Flavor != "" {
				a.BusinessownerState = &sim.BusinessownerState{Flavor: bo.Flavor}
			}
		}
		// RestockPolicy — union across all attributes in slug order so the
		// first-listed-wins tiebreak is deterministic (v1's ORDER BY slug).
		var entries []sim.RestockEntry
		seen := make(map[sim.ItemKind]bool)
		for _, slug := range sortedAttributeSlugs(a.Attributes) {
			raw := a.Attributes[slug]
			if len(raw) == 0 {
				continue
			}
			var params restockParamsRow
			if err := json.Unmarshal(raw, &params); err != nil {
				continue // unparseable — other roles may still be valid (v1 parity)
			}
			for _, e := range params.Restock {
				item := sim.ItemKind(e.Item)
				if item == "" || seen[item] {
					continue
				}
				source := sim.RestockSource(e.Source)
				if source != sim.RestockSourceProduce && source != sim.RestockSourceBuy {
					continue // unknown source mode — skip (v1 parity)
				}
				seen[item] = true
				entries = append(entries, sim.RestockEntry{
					Item:   item,
					Source: source,
					Max:    e.Max,
					Target: e.Target,
				})
			}
		}
		if len(entries) > 0 {
			a.RestockPolicy = &sim.RestockPolicy{Restock: entries}
		}
	}
}

// sortedAttributeSlugs returns the attribute slugs in deterministic
// ascending order, matching v1's `ORDER BY slug` so the restock union's
// first-listed-wins tiebreak is stable across loads.
func sortedAttributeSlugs(attrs map[string][]byte) []string {
	slugs := make([]string, 0, len(attrs))
	for slug := range attrs {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}
