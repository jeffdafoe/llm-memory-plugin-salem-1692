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

	if err := SaveWorld(ctx, repo, checkpointableWorld(repo).BuildCheckpointSnapshot()); err != nil {
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

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
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
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (two VOs): %v", err)
	}

	// Second checkpoint drops obj2.
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
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

// TestIntegration_SaveWorld_SameWindowOrderAndRoomGrant — ZBBS-HOME-451
// regression. An order minted, accepted, and delivered with a room grant
// all inside ONE checkpoint window means the pay_ledger row and the
// room_access row referencing it (granted_via_ledger_id, the one
// cross-aggregate FK that survived the v2 purge — and it is NOT deferred)
// are both new to the same SaveWorld. Saving Actors before Orders aborts
// the checkpoint at statement time — permanently, since the same snapshot
// re-fails every cycle (the 2026-06-12 live wedge: instant-settle lodging
// hits this in ~3s). SaveWorld must run Orders before Actors so the FK
// target exists when the grant upserts.
func TestIntegration_SaveWorld_SameWindowOrderAndRoomGrant(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	// pay_ledger.item_kind carries a real FK to the item_kind reference
	// table, which the engine loads at startup and never checkpoints.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES ('nights_stay', 'Lodging', 'lodging')`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}

	const (
		lodgerID = sim.ActorID("aaaaaaaa-0000-0000-0000-00000000a451")
		keeperID = sim.ActorID("bbbbbbbb-0000-0000-0000-00000000b451")
		innID    = sim.StructureID("cccccccc-0000-0000-0000-00000000c451")
		roomID   = sim.RoomID(22)
		ledgerID = 259
	)
	ts := time.Date(2026, 6, 12, 22, 1, 49, 0, time.UTC)
	checkout := ts.Add(17 * time.Hour)

	w := checkpointableWorld(repo)
	// A structure shares its id with a village_object (the shared-identity
	// bridge LoadWorld enforces), so the inn needs both rows.
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(innID): {ID: sim.VillageObjectID(innID), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
	}
	w.Structures = map[sim.StructureID]*sim.Structure{
		innID: {ID: innID, DisplayName: "Inn", Rooms: []*sim.Room{
			{ID: roomID, StructureID: innID, Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
		}},
	}
	w.Actors = map[sim.ActorID]*sim.Actor{
		lodgerID: {ID: lodgerID, DisplayName: "Lodger", State: sim.StateIdle, Coins: 65,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: roomID, Source: sim.AccessSourceLedger}: {
					RoomID: roomID, Source: sim.AccessSourceLedger,
					LedgerID: ledgerID, ExpiresAt: &checkout, Active: true, CreatedAt: ts,
				},
			}},
		keeperID: {ID: keeperID, DisplayName: "Keeper", State: sim.StateIdle, Coins: 85},
	}
	delivered := ts.Add(3 * time.Second)
	w.Orders = map[sim.OrderID]*sim.Order{
		ledgerID: {
			ID: ledgerID, LedgerID: ledgerID, State: sim.OrderStateDelivered,
			BuyerID: lodgerID, SellerID: keeperID, Item: "nights_stay", Qty: 1, Amount: 4,
			ConsumerIDs: []sim.ActorID{lodgerID},
			CreatedAt:   ts, DeliveredAt: &delivered, ReadyBy: ts.Truncate(24 * time.Hour), ExpiresAt: checkout,
		},
	}

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (same-window order + room grant): %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld after same-window checkpoint: %v", err)
	}
	lodger := loaded.Actors[lodgerID]
	if lodger == nil {
		t.Fatal("lodger did not round-trip")
	}
	grant := lodger.RoomAccess[sim.RoomAccessKey{RoomID: roomID, Source: sim.AccessSourceLedger}]
	if grant == nil {
		t.Fatal("room grant did not round-trip")
	}
	if grant.LedgerID != ledgerID || !grant.Active {
		t.Errorf("grant = ledger %d active %v, want ledger %d active true", grant.LedgerID, grant.Active, ledgerID)
	}
	// The delivered order is terminal, so LoadWorld doesn't rehydrate it —
	// assert the durable pay_ledger row (the FK target) landed instead.
	var ledgerState string
	if err := f.Pool.QueryRow(ctx,
		`SELECT state FROM pay_ledger WHERE id = $1`, ledgerID).Scan(&ledgerState); err != nil {
		t.Fatalf("pay_ledger row %d did not persist: %v", ledgerID, err)
	}
	if ledgerState != "accepted" {
		t.Errorf("pay_ledger state = %q, want accepted", ledgerState)
	}
}
