package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pgxmock-based tests for StructuresRepo (Slice 12). Asserts SQL shape +
// arg bindings + scan mapping. Real-pg behaviors (CHECK constraints,
// FK CASCADE, advisory lock blocking, UNIQUE(structure_id, name)) land
// with the testcontainers smoke slice (deferred — 8 flags accumulating).
//
// Structure IDs follow the v1-UUID-as-TEXT shape per the shared-identity
// bridge — pgxmock doesn't enforce CHECKs, but fixtures match the
// strict-bridge contract.

// Predictable structure IDs — distinct leading bytes for readability.
const (
	strA = "00000000-0000-0000-0000-aaaaaaaaaaaa"
	strB = "00000000-0000-0000-0000-bbbbbbbbbbbb"
	strC = "00000000-0000-0000-0000-cccccccccccc"
)

func newMockPoolS(t *testing.T) (pgxmock.PgxPoolIface, *StructuresRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewStructuresRepo(mock)
}

// expectStructureSaveSnapshotPrelude programs the common advisory lock +
// parent nextval expectations.
func expectStructureSaveSnapshotPrelude(mock pgxmock.PgxPoolIface, structGen int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\('structure_snapshot'`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('structure_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(structGen))
}

// expectStructureSaveSnapshotRoomTail programs the child-side nextval +
// trailing delete-stale. UPSERTs against structure_room are programmed
// per-test.
func expectStructureSaveSnapshotRoomTail(mock pgxmock.PgxPoolIface, roomGen int64) {
	mock.ExpectQuery(`SELECT nextval\('structure_room_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(roomGen))
	mock.ExpectExec(`DELETE FROM structure_room WHERE snapshot_gen < \$1`).
		WithArgs(roomGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
}

// emptyRoomRows returns a no-row pgxmock row set for the structure_room
// LoadAll query.
func emptyRoomRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "structure_id", "kind", "name"})
}

// --- LoadAll happy path ---------------------------------------------------

// TestStructuresRepo_LoadAll_HappyPath — parents + room stitching across
// multiple structures. Covers a tavern with rooms (common + private) and
// a smithy with no rooms.
func TestStructuresRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPoolS(t)
	mock.MatchExpectationsInOrder(false)

	mock.ExpectQuery(`FROM structure\b`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "tags", "leads_to_realm"}).
			AddRow(strA, "The Crow's Foot", []string{"tavern", "lodging"}, "").
			AddRow(strB, "Smithy", []string{}, ""))

	mock.ExpectQuery(`FROM structure_room\b`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "structure_id", "kind", "name"}).
			AddRow(int64(1), strA, "common", "common").
			AddRow(int64(2), strA, "private", "bedroom_1"))

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	a := got[strA]
	if a == nil {
		t.Fatal("strA missing")
	}
	if a.DisplayName != "The Crow's Foot" {
		t.Errorf("strA DisplayName = %q", a.DisplayName)
	}
	if len(a.Tags) != 2 || a.Tags[0] != "tavern" || a.Tags[1] != "lodging" {
		t.Errorf("strA Tags = %v", a.Tags)
	}
	if len(a.Rooms) != 2 {
		t.Errorf("strA Rooms len = %d, want 2", len(a.Rooms))
	}
	b := got[strB]
	if b == nil {
		t.Fatal("strB missing")
	}
	if len(b.Tags) != 0 {
		t.Errorf("strB Tags = %v, want empty", b.Tags)
	}
	if len(b.Rooms) != 0 {
		t.Errorf("strB Rooms = %v, want empty", b.Rooms)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestStructuresRepo_LoadAll_Empty — both queries return no rows.
func TestStructuresRepo_LoadAll_Empty(t *testing.T) {
	mock, repo := newMockPoolS(t)

	mock.ExpectQuery(`FROM structure\b`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "tags", "leads_to_realm"}))
	mock.ExpectQuery(`FROM structure_room\b`).
		WillReturnRows(emptyRoomRows())

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestStructuresRepo_LoadAll_OrphanRoomRejected — room with no parent
// surfaces an error (FK should prevent this, but the guard makes drift
// loud).
func TestStructuresRepo_LoadAll_OrphanRoomRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)

	mock.ExpectQuery(`FROM structure\b`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "tags", "leads_to_realm"}).
			AddRow(strA, "Tavern", []string{}, ""))
	mock.ExpectQuery(`FROM structure_room\b`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "structure_id", "kind", "name"}).
			AddRow(int64(1), strB, "common", "common")) // strB not loaded

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for orphan room row")
	}
}

// TestStructuresRepo_LoadAll_ParentQueryError — parent query errors.
func TestStructuresRepo_LoadAll_ParentQueryError(t *testing.T) {
	mock, repo := newMockPoolS(t)

	mock.ExpectQuery(`FROM structure\b`).WillReturnError(errors.New("connection lost"))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from parent query")
	}
}

// TestStructuresRepo_LoadAll_RoomQueryError — child query errors.
func TestStructuresRepo_LoadAll_RoomQueryError(t *testing.T) {
	mock, repo := newMockPoolS(t)

	mock.ExpectQuery(`FROM structure\b`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "tags", "leads_to_realm"}).
			AddRow(strA, "Tavern", []string{}, ""))
	mock.ExpectQuery(`FROM structure_room\b`).WillReturnError(errors.New("connection lost"))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from room query")
	}
}

// --- SaveSnapshot happy path ---------------------------------------------

// TestStructuresRepo_SaveSnapshot_HappyPath — 2 structures, one with
// rooms, one without. Lifecycle: lock → struct gen → 2 upserts → struct
// delete-stale → room gen → 2 room upserts → room delete-stale.
func TestStructuresRepo_SaveSnapshot_HappyPath(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}
	mock.MatchExpectationsInOrder(false)

	expectStructureSaveSnapshotPrelude(mock, 7)

	mock.ExpectExec(`INSERT INTO structure\b`).
		WithArgs(strA, "Tavern", []string{"tavern"}, "", int64(7)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`INSERT INTO structure\b`).
		WithArgs(strB, "Smithy", []string{}, "", int64(7)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WithArgs(int64(7)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('structure_room_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(70)))

	mock.ExpectExec(`INSERT INTO structure_room\b`).
		WithArgs(int64(1), strA, "common", "common", int64(70)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`INSERT INTO structure_room\b`).
		WithArgs(int64(2), strA, "private", "bedroom_1", int64(70)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM structure_room WHERE snapshot_gen < \$1`).
		WithArgs(int64(70)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	structures := map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Tags:        []string{"tavern"},
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: sim.StructureID(strA), Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		},
		sim.StructureID(strB): {
			ID:          sim.StructureID(strB),
			DisplayName: "Smithy",
			Tags:        []string{},
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, structures); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestStructuresRepo_SaveSnapshot_EmptyMap — both gens still bump, both
// DELETEs sweep.
func TestStructuresRepo_SaveSnapshot_EmptyMap(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 7)
	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WithArgs(int64(7)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectStructureSaveSnapshotRoomTail(mock, 70)

	if err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestStructuresRepo_SaveSnapshot_NilTx — substrate-boundary nil check.
func TestStructuresRepo_SaveSnapshot_NilTx(t *testing.T) {
	_, repo := newMockPoolS(t)
	err := repo.SaveSnapshot(context.Background(), nil, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {ID: sim.StructureID(strA), DisplayName: "Tavern"},
	})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}
}

// --- SaveSnapshot validation tests (substrate boundary) ------------------
//
// Each programs the advisory lock + parent nextval prelude. Validation
// runs in the pre-pass after that, before any upsert. Per design_review
// 2026-05-19 #5, nil entries are rejected as substrate errors (NOT
// silently skipped).

// TestStructuresRepo_SaveSnapshot_NilStructureRejected — design_review #5.
func TestStructuresRepo_SaveSnapshot_NilStructureRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): nil,
	})
	if err == nil {
		t.Fatal("expected error for nil structure entry")
	}
}

// TestStructuresRepo_SaveSnapshot_EmptyStructureIDRejected.
func TestStructuresRepo_SaveSnapshot_EmptyStructureIDRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		"": {ID: "", DisplayName: "Empty"},
	})
	if err == nil {
		t.Fatal("expected error for empty StructureID")
	}
}

// TestStructuresRepo_SaveSnapshot_KeyMismatchRejected.
func TestStructuresRepo_SaveSnapshot_KeyMismatchRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {ID: sim.StructureID(strB), DisplayName: "Tavern"},
	})
	if err == nil {
		t.Fatal("expected error for map-key vs s.ID mismatch")
	}
}

// TestStructuresRepo_SaveSnapshot_EmptyDisplayNameRejected — load-bearing
// for LLM prompts.
func TestStructuresRepo_SaveSnapshot_EmptyDisplayNameRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {ID: sim.StructureID(strA), DisplayName: ""},
	})
	if err == nil {
		t.Fatal("expected error for empty DisplayName")
	}
}

// TestStructuresRepo_SaveSnapshot_WhitespaceStructureIDRejected — repo
// validation must match the DB btrim CHECK (code_review R1).
func TestStructuresRepo_SaveSnapshot_WhitespaceStructureIDRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		"   ": {ID: "   ", DisplayName: "Tavern"},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only StructureID")
	}
}

// TestStructuresRepo_SaveSnapshot_WhitespaceDisplayNameRejected — repo
// validation must match the DB btrim CHECK (code_review R1).
func TestStructuresRepo_SaveSnapshot_WhitespaceDisplayNameRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {ID: sim.StructureID(strA), DisplayName: "  \t  "},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only DisplayName")
	}
}

// TestStructuresRepo_SaveSnapshot_WhitespaceRoomNameRejected — repo
// validation must match the DB btrim CHECK (code_review R1).
func TestStructuresRepo_SaveSnapshot_WhitespaceRoomNameRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "  "},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only room Name")
	}
}

// TestStructuresRepo_SaveSnapshot_EmptyTagRejected — repo enforces
// tag-element nonemptiness (replacing the dropped array_position CHECK
// per code_review R1).
func TestStructuresRepo_SaveSnapshot_EmptyTagRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Tags:        []string{"tavern", "", "lodging"},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty tag element")
	}
}

// TestStructuresRepo_SaveSnapshot_NilRoomRejected — design_review #5.
func TestStructuresRepo_SaveSnapshot_NilRoomRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms:       []*sim.Room{nil},
		},
	})
	if err == nil {
		t.Fatal("expected error for nil room in Rooms slice")
	}
}

// TestStructuresRepo_SaveSnapshot_RoomIDNonPositiveRejected — design_review #4.
func TestStructuresRepo_SaveSnapshot_RoomIDNonPositiveRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms: []*sim.Room{
				{ID: 0, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for RoomID == 0")
	}
}

// TestStructuresRepo_SaveSnapshot_RoomStructureIDMismatchRejected.
func TestStructuresRepo_SaveSnapshot_RoomStructureIDMismatchRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strB), Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for room with mismatched StructureID")
	}
}

// TestStructuresRepo_SaveSnapshot_EmptyRoomNameRejected.
func TestStructuresRepo_SaveSnapshot_EmptyRoomNameRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: ""},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty room name")
	}
}

// TestStructuresRepo_SaveSnapshot_DuplicateRoomIDsRejected — design_review #4.
// Two structures, each claiming RoomID=1.
func TestStructuresRepo_SaveSnapshot_DuplicateRoomIDsRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
		sim.StructureID(strB): {
			ID:          sim.StructureID(strB),
			DisplayName: "Smithy",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strB), Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for duplicate RoomID across snapshot")
	}
}

// TestStructuresRepo_SaveSnapshot_DuplicateRoomNamePerStructureRejected.
func TestStructuresRepo_SaveSnapshot_DuplicateRoomNamePerStructureRejected(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:          sim.StructureID(strA),
			DisplayName: "Tavern",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for duplicate room name within structure")
	}
}

// TestStructuresRepo_SaveSnapshot_LeadsToRealmEmpty — empty string
// roundtrips correctly through the default-empty column.
func TestStructuresRepo_SaveSnapshot_LeadsToRealmEmpty(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO structure\b`).
		WithArgs(strA, "Tavern", []string{}, "", int64(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectStructureSaveSnapshotRoomTail(mock, 10)

	structures := map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID:           sim.StructureID(strA),
			DisplayName:  "Tavern",
			Tags:         []string{},
			LeadsToRealm: "", // intentionally empty
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, structures); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// --- SaveSnapshot SQL error paths ----------------------------------------

func TestStructuresRepo_SaveSnapshot_AdvisoryLockError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{})
	if err == nil {
		t.Fatal("expected error from advisory lock")
	}
}

func TestStructuresRepo_SaveSnapshot_StructureNextvalError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('structure_snapshot_gen_seq`).
		WillReturnError(errors.New("sequence broken"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{})
	if err == nil {
		t.Fatal("expected error from structure nextval")
	}
}

func TestStructuresRepo_SaveSnapshot_StructureUpsertError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO structure\b`).
		WillReturnError(errors.New("check constraint violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {ID: sim.StructureID(strA), DisplayName: "Tavern", Tags: []string{}},
	})
	if err == nil {
		t.Fatal("expected error from structure upsert")
	}
}

func TestStructuresRepo_SaveSnapshot_RoomNextvalError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('structure_room_snapshot_gen_seq`).
		WillReturnError(errors.New("sequence broken"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{})
	if err == nil {
		t.Fatal("expected error from room nextval")
	}
}

func TestStructuresRepo_SaveSnapshot_RoomUpsertError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO structure\b`).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('structure_room_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(10)))
	mock.ExpectExec(`INSERT INTO structure_room\b`).
		WillReturnError(errors.New("unique violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{
		sim.StructureID(strA): {
			ID: sim.StructureID(strA), DisplayName: "Tavern", Tags: []string{},
			Rooms: []*sim.Room{
				{ID: 1, StructureID: sim.StructureID(strA), Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error from room upsert")
	}
}

func TestStructuresRepo_SaveSnapshot_StructureDeleteStaleError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{})
	if err == nil {
		t.Fatal("expected error from structure delete-stale")
	}
}

func TestStructuresRepo_SaveSnapshot_RoomDeleteStaleError(t *testing.T) {
	mock, repo := newMockPoolS(t)
	tx := fakeTx{mock: mock}

	expectStructureSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM structure WHERE snapshot_gen < \$1`).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('structure_room_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(10)))
	mock.ExpectExec(`DELETE FROM structure_room WHERE snapshot_gen < \$1`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.StructureID]*sim.Structure{})
	if err == nil {
		t.Fatal("expected error from room delete-stale")
	}
}
