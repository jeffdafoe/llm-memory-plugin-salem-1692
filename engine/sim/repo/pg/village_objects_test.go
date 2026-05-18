package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pgxmock-based tests for VillageObjectsRepo. Asserts SQL shape + arg
// bindings + scan mapping. Real-pg smoke lives at cutover (testcontainers
// slice).

func newMockPoolVO(t *testing.T) (pgxmock.PgxPoolIface, *VillageObjectsRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewVillageObjectsRepo(mock)
}

// --- LoadAll happy path ---------------------------------------------------

// TestVillageObjectsRepo_LoadAll_HappyPath — covers the full column set
// including the various nullable combinations: owner_actor_id present
// vs NULL, attached_to NULL (top-level placement) vs present (overlay),
// loiter offsets present vs NULL.
func TestVillageObjectsRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPoolVO(t)

	loiterX := 3
	loiterY := -2
	ownerStr := "alice"
	attachedID := "parent-uuid"

	rows := pgxmock.NewRows([]string{
		"id", "asset_id", "current_state", "x", "y", "placed_by",
		"display_name", "entry_policy", "owner_actor_id", "attached_to",
		"loiter_offset_x", "loiter_offset_y", "available_quantity", "tags",
	}).
		// Top-level placement, owned, with loiter offsets, tags
		AddRow("obj-1", "asset-well", "default", 640.0, 320.0, "admin",
			"Old Well", "closed", &ownerStr, (*string)(nil),
			&loiterX, &loiterY, 10, []string{"vendor", "well"}).
		// Top-level placement, unowned, no loiter, empty tags
		AddRow("obj-2", "asset-bench", "variant-1", 1000.0, 500.0, "",
			"", "open", (*string)(nil), (*string)(nil),
			(*int)(nil), (*int)(nil), 0, []string{}).
		// Overlay attached to obj-1, owner-only, no tags
		AddRow("obj-3", "asset-lamp", "lit", 645.0, 325.0, "admin",
			"Lamp", "owner-only", &ownerStr, &attachedID,
			(*int)(nil), (*int)(nil), 0, []string{})

	mock.ExpectQuery(`SELECT[\s\S]+FROM village_object`).WillReturnRows(rows)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d objects, want 3", len(got))
	}

	o1 := got["obj-1"]
	if o1 == nil {
		t.Fatal("obj-1 missing")
	}
	if o1.AssetID != "asset-well" {
		t.Errorf("o1.AssetID = %q, want asset-well", o1.AssetID)
	}
	if o1.EntryPolicy != sim.EntryPolicyClosed {
		t.Errorf("o1.EntryPolicy = %q, want closed", o1.EntryPolicy)
	}
	if o1.OwnerActorID != "alice" {
		t.Errorf("o1.OwnerActorID = %q, want alice", o1.OwnerActorID)
	}
	if o1.AttachedTo != "" {
		t.Errorf("o1.AttachedTo = %q, want empty (top-level)", o1.AttachedTo)
	}
	if o1.LoiterOffsetX == nil || *o1.LoiterOffsetX != 3 {
		t.Errorf("o1.LoiterOffsetX = %v, want 3", o1.LoiterOffsetX)
	}
	if o1.AvailableQuantity != 10 {
		t.Errorf("o1.AvailableQuantity = %d, want 10", o1.AvailableQuantity)
	}
	if len(o1.Tags) != 2 {
		t.Errorf("o1.Tags = %v, want 2 entries", o1.Tags)
	}
	if o1.Refreshes != nil {
		t.Errorf("o1.Refreshes = %v, want nil (deferred to follow-up slice)", o1.Refreshes)
	}

	o2 := got["obj-2"]
	if o2.OwnerActorID != "" {
		t.Errorf("o2.OwnerActorID = %q, want empty (NULL → empty ActorID)", o2.OwnerActorID)
	}
	if o2.LoiterOffsetX != nil {
		t.Errorf("o2.LoiterOffsetX = %v, want nil (NULL → nil)", o2.LoiterOffsetX)
	}
	if o2.EntryPolicy != sim.EntryPolicyOpen {
		t.Errorf("o2.EntryPolicy = %q, want open", o2.EntryPolicy)
	}

	o3 := got["obj-3"]
	if o3.AttachedTo != "parent-uuid" {
		t.Errorf("o3.AttachedTo = %q, want parent-uuid", o3.AttachedTo)
	}
	if o3.EntryPolicy != sim.EntryPolicyOwner {
		t.Errorf("o3.EntryPolicy = %q, want owner-only", o3.EntryPolicy)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestVillageObjectsRepo_LoadAll_Empty(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	rows := pgxmock.NewRows([]string{
		"id", "asset_id", "current_state", "x", "y", "placed_by",
		"display_name", "entry_policy", "owner_actor_id", "attached_to",
		"loiter_offset_x", "loiter_offset_y", "available_quantity", "tags",
	})
	mock.ExpectQuery(`SELECT[\s\S]+FROM village_object`).WillReturnRows(rows)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty result returned %d objects", len(got))
	}
}

func TestVillageObjectsRepo_LoadAll_QueryError(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	mock.ExpectQuery(`SELECT[\s\S]+FROM village_object`).
		WillReturnError(errors.New("conn closed"))
	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from query failure")
	}
}

// --- SaveSnapshot ---------------------------------------------------------

func TestVillageObjectsRepo_SaveSnapshot_HappyPath(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	// Step 1: nextval returns gen=5.
	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(5)))

	// Step 2: two upserts (order varies due to map iteration).
	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			"obj-1", "asset-well", "default", 640.0, 320.0, "admin",
			"Old Well", "closed", "alice", nil,
			(*int)(nil), (*int)(nil), 0, []string{"vendor"}, int64(5),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			"obj-2", "asset-bench", "variant-1", 1000.0, 500.0, "", "",
			"open", nil, nil,
			(*int)(nil), (*int)(nil), 0, []string{}, int64(5),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	// Step 3: delete stale (snapshot_gen < 5).
	mock.ExpectExec(`DELETE FROM village_object WHERE snapshot_gen < \$1`).
		WithArgs(int64(5)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.MatchExpectationsInOrder(false)

	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"obj-1": {
			ID:           "obj-1",
			AssetID:      "asset-well",
			CurrentState: "default",
			X:            640, Y: 320,
			PlacedBy:     "admin",
			DisplayName:  "Old Well",
			EntryPolicy:  sim.EntryPolicyClosed,
			OwnerActorID: "alice",
			Tags:         []string{"vendor"},
		},
		"obj-2": {
			ID:           "obj-2",
			AssetID:      "asset-bench",
			CurrentState: "variant-1",
			X:            1000, Y: 500,
			EntryPolicy: sim.EntryPolicyOpen,
			// nil tags → empty slice; nil owner → SQL NULL
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, objects); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestVillageObjectsRepo_SaveSnapshot_EmptyMap — gen still bumps + delete
// still runs, removing every row (snapshot semantic: empty = nothing
// should exist).
func TestVillageObjectsRepo_SaveSnapshot_EmptyMap(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(7)))
	mock.ExpectExec(`DELETE FROM village_object WHERE snapshot_gen < \$1`).
		WithArgs(int64(7)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	if err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestVillageObjectsRepo_SaveSnapshot_NilTx — substrate-boundary nil
// check. Symmetric with OrdersRepo's nil-tx guard.
func TestVillageObjectsRepo_SaveSnapshot_NilTx(t *testing.T) {
	_, repo := newMockPoolVO(t)
	err := repo.SaveSnapshot(context.Background(), nil, map[sim.VillageObjectID]*sim.VillageObject{
		"obj-1": {ID: "obj-1", AssetID: "asset-1"},
	})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_NilObjectSkipped — nil entries are
// silently skipped; gen bump + delete still run.
func TestVillageObjectsRepo_SaveSnapshot_NilObjectSkipped(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1)))
	mock.ExpectExec(`DELETE FROM village_object WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		"obj-1": nil,
	})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestVillageObjectsRepo_SaveSnapshot_NextvalError — sequence call
// fails. Surfaces as substrate error; upsert + delete don't run.
func TestVillageObjectsRepo_SaveSnapshot_NextvalError(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectQuery(`SELECT nextval`).WillReturnError(errors.New("sequence unavailable"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		"obj-1": {ID: "obj-1", AssetID: "asset-1"},
	})
	if err == nil {
		t.Fatal("expected error from nextval failure")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_UpsertError — upsert fails after
// gen bump. Delete-stale doesn't run (caller's Tx rolls back).
func TestVillageObjectsRepo_SaveSnapshot_UpsertError(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1)))
	mock.ExpectExec(`INSERT INTO village_object`).
		WillReturnError(errors.New("CHECK constraint violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		"obj-1": {ID: "obj-1", AssetID: "asset-1", EntryPolicy: "bogus"},
	})
	if err == nil {
		t.Fatal("expected error from upsert failure")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_OwnerNullVsValue — verifies
// nullable owner_actor_id binding: empty ActorID → SQL NULL, non-empty
// → string value. Same for attached_to.
func TestVillageObjectsRepo_SaveSnapshot_OwnerNullVsValue(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(2)))

	// Owned overlay (both owner_actor_id + attached_to non-NULL).
	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			"obj-overlay", "asset-lamp", "lit", 100.0, 100.0, "",
			"", "open", "alice", "parent-id",
			(*int)(nil), (*int)(nil), 0, []string{}, int64(2),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM village_object`).
		WithArgs(int64(2)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"obj-overlay": {
			ID: "obj-overlay", AssetID: "asset-lamp", CurrentState: "lit",
			X: 100, Y: 100,
			EntryPolicy:  sim.EntryPolicyOpen,
			OwnerActorID: "alice",
			AttachedTo:   "parent-id",
		},
	}
	if err := repo.SaveSnapshot(context.Background(), tx, objects); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
