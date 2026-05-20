package pg

// Real-pg integration tests for SaveWorld (ZBBS-WORK-249). Run against an
// embedded Postgres with the full prod-baseline schema applied; skipped
// under `go test -short`.
//
// The SaveWorld unit tests (save_world_test.go) prove orchestration —
// call order, atomic commit, rollback-on-error — with spy repos. These
// tests prove the part spies can't: that the seven REAL SaveSnapshots
// compose inside one genuine Tx (gen-marker bumps + delete-stale sweeps
// against the real schema and its constraints), commit, and reload via the
// real LoadWorld. This is the end-to-end checkpoint roundtrip.

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// checkpointableWorld builds a NewWorld with the minimum world-level state a
// real checkpoint requires: a valid phase and non-zero durable scheduler
// timestamps. NewWorld leaves these zero-value, which Environment.SaveSnapshot
// correctly rejects (a real running world always has them set by the time it
// checkpoints). Aggregate maps start empty; callers populate as needed.
func checkpointableWorld(repo sim.Repository) *sim.World {
	w := sim.NewWorld(repo)
	ts := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	w.Phase = sim.PhaseDay
	w.Environment = sim.WorldEnvironment{
		Now:              ts,
		LastTransitionAt: ts,
		LastRotationAt:   ts,
		LastNeedsTickAt:  ts,
	}
	return w
}

// TestIntegration_SaveWorld_EmptyRoundTrip — checkpoint a World with no
// aggregates, then cold-load it back with requireAllImpl=true. Proves all
// seven SaveSnapshots run + commit in one Tx against the real schema (every
// gen sequence bumps, every delete-stale sweeps its empty table, the
// environment singleton is seeded) and that LoadWorld then reads a clean
// empty World with no errNotImpl from any sub-repo.
func TestIntegration_SaveWorld_EmptyRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	if err := SaveWorld(ctx, repo, checkpointableWorld(repo)); err != nil {
		t.Fatalf("SaveWorld (empty): %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld after empty checkpoint: %v", err)
	}
	if len(loaded.VillageObjects) != 0 {
		t.Errorf("VillageObjects = %d, want 0", len(loaded.VillageObjects))
	}
	if len(loaded.Structures) != 0 {
		t.Errorf("Structures = %d, want 0", len(loaded.Structures))
	}
	if len(loaded.Huddles) != 0 {
		t.Errorf("Huddles = %d, want 0", len(loaded.Huddles))
	}
	if len(loaded.Scenes) != 0 {
		t.Errorf("Scenes = %d, want 0", len(loaded.Scenes))
	}
	if len(loaded.Actors) != 0 {
		t.Errorf("Actors = %d, want 0", len(loaded.Actors))
	}
	if len(loaded.Orders) != 0 {
		t.Errorf("Orders = %d, want 0", len(loaded.Orders))
	}
	if loaded.Phase != sim.PhaseDay {
		t.Errorf("Phase = %q, want %q (environment singleton should round-trip)", loaded.Phase, sim.PhaseDay)
	}
}

// TestIntegration_SaveWorld_PopulatedRoundTrip — checkpoint a World holding
// a couple of village_objects (the one checkpoint aggregate with a real-pg
// builder to borrow), reload, and confirm they survive. A standalone VO
// that isn't also a structure is valid (the shared-identity bridge is
// one-way: every structure needs a VO, not vice-versa).
func TestIntegration_SaveWorld_PopulatedRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	w := checkpointableWorld(repo)
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
		sim.VillageObjectID(uuidObj2): {ID: sim.VillageObjectID(uuidObj2), AssetID: sim.AssetID(uuidAssetLamp), EntryPolicy: sim.EntryPolicyClosed},
	}

	if err := SaveWorld(ctx, repo, w); err != nil {
		t.Fatalf("SaveWorld (populated): %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld after populated checkpoint: %v", err)
	}
	if len(loaded.VillageObjects) != 2 {
		t.Fatalf("VillageObjects = %d, want 2", len(loaded.VillageObjects))
	}
	if loaded.VillageObjects[sim.VillageObjectID(uuidObj1)] == nil ||
		loaded.VillageObjects[sim.VillageObjectID(uuidObj2)] == nil {
		t.Errorf("expected both village_objects to round-trip, got %v", loaded.VillageObjects)
	}
}

// TestIntegration_SaveWorld_DeleteStaleAcrossCheckpoints — a second
// checkpoint with a smaller set must prune rows the first one wrote, end
// to end through SaveWorld. This proves the per-aggregate gen-marker
// delete-stale sweep fires when driven by the orchestrator (the "an actor
// who left is removed, not leaked" property at the SaveWorld level).
func TestIntegration_SaveWorld_DeleteStaleAcrossCheckpoints(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	w := checkpointableWorld(repo)
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
		sim.VillageObjectID(uuidObj2): {ID: sim.VillageObjectID(uuidObj2), AssetID: sim.AssetID(uuidAssetLamp), EntryPolicy: sim.EntryPolicyClosed},
	}
	if err := SaveWorld(ctx, repo, w); err != nil {
		t.Fatalf("SaveWorld (two VOs): %v", err)
	}

	// Second checkpoint drops obj2.
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
	}
	if err := SaveWorld(ctx, repo, w); err != nil {
		t.Fatalf("SaveWorld (one VO): %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld after prune: %v", err)
	}
	if len(loaded.VillageObjects) != 1 {
		t.Fatalf("VillageObjects = %d, want 1 (obj2 should be swept)", len(loaded.VillageObjects))
	}
	if loaded.VillageObjects[sim.VillageObjectID(uuidObj1)] == nil {
		t.Error("obj1 should survive the second checkpoint")
	}
	if _, gone := loaded.VillageObjects[sim.VillageObjectID(uuidObj2)]; gone {
		t.Error("obj2 should have been pruned by the delete-stale sweep")
	}
}
