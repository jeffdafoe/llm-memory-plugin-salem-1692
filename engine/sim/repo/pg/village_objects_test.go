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
//
// Test IDs use syntactically valid UUID strings so test fixtures match
// the production schema's UUID column types (id, asset_id, attached_to)
// — pgxmock doesn't enforce the ::uuid cast, but real Postgres would.
// Code_review R1 flag.

// Predictable UUID literals — different leading bytes make failures
// easier to read in test output than v4 random UUIDs.
const (
	uuidObj1    = "11111111-1111-1111-1111-111111111111"
	uuidObj2    = "22222222-2222-2222-2222-222222222222"
	uuidObj3    = "33333333-3333-3333-3333-333333333333"
	uuidOverlay = "00000000-0000-0000-0000-000000000099"

	uuidAssetWell  = "a0000000-0000-0000-0000-000000000001"
	uuidAssetBench = "a0000000-0000-0000-0000-000000000002"
	uuidAssetLamp  = "a0000000-0000-0000-0000-000000000003"
)

func newMockPoolVO(t *testing.T) (pgxmock.PgxPoolIface, *VillageObjectsRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewVillageObjectsRepo(mock)
}

// expectSaveSnapshotPrelude programs the common nextval + advisory lock
// expectations on the mock. The lock is matched as Exec (the SELECT
// returns void and the production code uses Exec); nextval as Query.
// MatchExpectationsInOrder must be set by the caller — these two are
// only ordered relative to each other (lock before nextval).
func expectSaveSnapshotPrelude(mock pgxmock.PgxPoolIface, gen int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(gen))
}

// expectSaveSnapshotOrphanCheck programs the orphan-check expectation
// (count = 0 means no invariant violation; happy path).
func expectSaveSnapshotOrphanCheck(mock pgxmock.PgxPoolIface, gen int64, count int64) {
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM village_object fresh`).
		WithArgs(gen).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(count))
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
	parentRef := uuidObj1

	rows := pgxmock.NewRows([]string{
		"id", "asset_id", "current_state", "x", "y", "placed_by",
		"display_name", "entry_policy", "owner_actor_id", "attached_to",
		"loiter_offset_x", "loiter_offset_y", "available_quantity", "tags",
	}).
		// Top-level placement, owned, with loiter offsets, tags.
		AddRow(uuidObj1, uuidAssetWell, "default", 640.0, 320.0, "admin",
			"Old Well", "closed", &ownerStr, (*string)(nil),
			&loiterX, &loiterY, 10, []string{"vendor", "well"}).
		// Top-level placement, unowned, no loiter, empty tags.
		AddRow(uuidObj2, uuidAssetBench, "variant-1", 1000.0, 500.0, "",
			"", "open", (*string)(nil), (*string)(nil),
			(*int)(nil), (*int)(nil), 0, []string{}).
		// Overlay attached to obj-1, owner-only, no tags.
		AddRow(uuidObj3, uuidAssetLamp, "lit", 645.0, 325.0, "admin",
			"Lamp", "owner-only", &ownerStr, &parentRef,
			(*int)(nil), (*int)(nil), 0, []string{})

	mock.ExpectQuery(`SELECT[\s\S]+FROM village_object`).WillReturnRows(rows)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d objects, want 3", len(got))
	}

	o1 := got[sim.VillageObjectID(uuidObj1)]
	if o1 == nil {
		t.Fatal("obj-1 missing")
	}
	if o1.AssetID != sim.AssetID(uuidAssetWell) {
		t.Errorf("o1.AssetID = %q, want %q", o1.AssetID, uuidAssetWell)
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

	o2 := got[sim.VillageObjectID(uuidObj2)]
	if o2.OwnerActorID != "" {
		t.Errorf("o2.OwnerActorID = %q, want empty (NULL → empty ActorID)", o2.OwnerActorID)
	}
	if o2.LoiterOffsetX != nil {
		t.Errorf("o2.LoiterOffsetX = %v, want nil (NULL → nil)", o2.LoiterOffsetX)
	}
	if o2.EntryPolicy != sim.EntryPolicyOpen {
		t.Errorf("o2.EntryPolicy = %q, want open", o2.EntryPolicy)
	}
	// Empty Postgres array '{}' scanned through pgx may produce empty
	// slice or nil depending on the type path. LoadAll normalizes to
	// empty slice so callers don't have to nil-check (Slice 9 R1 fix).
	if o2.Tags == nil {
		t.Errorf("o2.Tags = nil, want empty slice (normalize-nil-tags)")
	}

	o3 := got[sim.VillageObjectID(uuidObj3)]
	if string(o3.AttachedTo) != uuidObj1 {
		t.Errorf("o3.AttachedTo = %q, want %q", o3.AttachedTo, uuidObj1)
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

// TestVillageObjectsRepo_SaveSnapshot_HappyPath — full lifecycle:
// advisory lock → nextval → per-row UPSERTs → safer DELETE → orphan
// check returns 0.
func TestVillageObjectsRepo_SaveSnapshot_HappyPath(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	expectSaveSnapshotPrelude(mock, 5)

	// Two upserts (order varies due to map iteration).
	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			uuidObj1, uuidAssetWell, "default", 640.0, 320.0, "admin",
			"Old Well", "closed", "alice", nil,
			(*int)(nil), (*int)(nil), 0, []string{"vendor"}, int64(5),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			uuidObj2, uuidAssetBench, "variant-1", 1000.0, 500.0, "", "",
			"open", nil, nil,
			(*int)(nil), (*int)(nil), 0, []string{}, int64(5),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	// Safer DELETE — protects fresh children whose parents are stale.
	mock.ExpectExec(`DELETE FROM village_object stale[\s\S]+WHERE stale.snapshot_gen < \$1`).
		WithArgs(int64(5)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	expectSaveSnapshotOrphanCheck(mock, 5, 0)

	mock.MatchExpectationsInOrder(false)

	objects := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {
			ID:           sim.VillageObjectID(uuidObj1),
			AssetID:      sim.AssetID(uuidAssetWell),
			CurrentState: "default",
			X:            640, Y: 320,
			PlacedBy:     "admin",
			DisplayName:  "Old Well",
			EntryPolicy:  sim.EntryPolicyClosed,
			OwnerActorID: "alice",
			Tags:         []string{"vendor"},
		},
		sim.VillageObjectID(uuidObj2): {
			ID:           sim.VillageObjectID(uuidObj2),
			AssetID:      sim.AssetID(uuidAssetBench),
			CurrentState: "variant-1",
			X:            1000, Y: 500,
			EntryPolicy: sim.EntryPolicyOpen,
			// nil tags → empty slice; nil owner → SQL NULL.
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, objects); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestVillageObjectsRepo_SaveSnapshot_EmptyMap — lock + gen still
// happen; safer DELETE runs (removing every row since none has the new
// gen); orphan check returns 0.
func TestVillageObjectsRepo_SaveSnapshot_EmptyMap(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	expectSaveSnapshotPrelude(mock, 7)
	mock.ExpectExec(`DELETE FROM village_object stale`).
		WithArgs(int64(7)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectSaveSnapshotOrphanCheck(mock, 7, 0)

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
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell)},
	})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_NilObjectSkipped — nil entries are
// silently skipped; full lifecycle (lock + gen + delete + orphan check)
// still runs.
func TestVillageObjectsRepo_SaveSnapshot_NilObjectSkipped(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	expectSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM village_object stale`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectSaveSnapshotOrphanCheck(mock, 1, 0)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): nil,
	})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestVillageObjectsRepo_SaveSnapshot_AdvisoryLockError — lock acquisition
// fails (e.g., connection issue). Surfaces as substrate error; nextval +
// upserts + delete + orphan check don't run.
func TestVillageObjectsRepo_SaveSnapshot_AdvisoryLockError(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell)},
	})
	if err == nil {
		t.Fatal("expected error from advisory lock failure")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_NextvalError — sequence call
// fails. Surfaces as substrate error; upsert + delete don't run.
func TestVillageObjectsRepo_SaveSnapshot_NextvalError(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval`).WillReturnError(errors.New("sequence unavailable"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell)},
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

	expectSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO village_object`).
		WillReturnError(errors.New("CHECK constraint violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: "bogus"},
	})
	if err == nil {
		t.Fatal("expected error from upsert failure")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_OrphanCheckViolation — orphan
// check returns >0, meaning a fresh child references a stale parent
// (world-side parent/child-same-gen invariant got broken). SaveSnapshot
// errors out so the violation surfaces loudly. Caller's Tx rolls back.
func TestVillageObjectsRepo_SaveSnapshot_OrphanCheckViolation(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	expectSaveSnapshotPrelude(mock, 3)

	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			uuidObj1, uuidAssetWell, "default", 0.0, 0.0, "", "",
			"open", nil, nil,
			(*int)(nil), (*int)(nil), 0, []string{}, int64(3),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM village_object stale`).
		WithArgs(int64(3)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Orphan check returns 2 — two fresh children reference stale parents.
	expectSaveSnapshotOrphanCheck(mock, 3, 2)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {
			ID:           sim.VillageObjectID(uuidObj1),
			AssetID:      sim.AssetID(uuidAssetWell),
			CurrentState: "default",
			EntryPolicy:  sim.EntryPolicyOpen,
		},
	})
	if err == nil {
		t.Fatal("expected error from orphan-check violation")
	}
}

// TestVillageObjectsRepo_SaveSnapshot_OwnerNullVsValue — verifies
// nullable owner_actor_id binding: empty ActorID → SQL NULL, non-empty
// → string value. Same for attached_to.
func TestVillageObjectsRepo_SaveSnapshot_OwnerNullVsValue(t *testing.T) {
	mock, repo := newMockPoolVO(t)
	tx := fakeTx{mock: mock}

	expectSaveSnapshotPrelude(mock, 2)

	// Owned overlay (both owner_actor_id + attached_to non-NULL).
	mock.ExpectExec(`INSERT INTO village_object`).
		WithArgs(
			uuidOverlay, uuidAssetLamp, "lit", 100.0, 100.0, "",
			"", "open", "alice", uuidObj1,
			(*int)(nil), (*int)(nil), 0, []string{}, int64(2),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM village_object stale`).
		WithArgs(int64(2)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	expectSaveSnapshotOrphanCheck(mock, 2, 0)

	objects := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidOverlay): {
			ID: sim.VillageObjectID(uuidOverlay), AssetID: sim.AssetID(uuidAssetLamp), CurrentState: "lit",
			X: 100, Y: 100,
			EntryPolicy:  sim.EntryPolicyOpen,
			OwnerActorID: "alice",
			AttachedTo:   sim.VillageObjectID(uuidObj1),
		},
	}
	if err := repo.SaveSnapshot(context.Background(), tx, objects); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
