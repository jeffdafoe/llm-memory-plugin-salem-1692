package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pgxmock-based tests for HuddlesRepo (Slice 11). Asserts SQL shape +
// arg bindings + scan mapping. Real-pg behaviors (CHECK constraints,
// UNIQUE(actor_id), FK CASCADE, advisory lock blocking) land with the
// testcontainers smoke slice (deferred — 6 flags accumulating).
//
// Huddle IDs follow the 'hud-<32 hex>' format produced by
// engine/sim/huddle_commands.go::newHuddleID — pgxmock doesn't enforce
// the scene_huddle_id_format CHECK, but the fixtures match it anyway
// so the same tests can be reused against real pg later.

// Predictable huddle IDs — distinct leading bytes for failure readability.
const (
	hudA = "hud-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hudB = "hud-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hudC = "hud-cccccccccccccccccccccccccccccccc"
)

// Sample actor IDs — v2 uses heterogeneous string IDs (Slice 5 pattern).
const (
	actorAlice = "alice"
	actorBob   = "bob"
	actorCarol = "carol"
)

func newMockPoolH(t *testing.T) (pgxmock.PgxPoolIface, *HuddlesRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewHuddlesRepo(mock)
}

// expectHuddleSaveSnapshotPrelude programs the common advisory lock +
// parent nextval expectations.
func expectHuddleSaveSnapshotPrelude(mock pgxmock.PgxPoolIface, huddleGen int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\('huddle_snapshot'`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('huddle_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(huddleGen))
}

// expectHuddleSaveSnapshotMemberTail programs the child-side nextval +
// delete-stale tail. Per-test UPSERTs against huddle_member are
// programmed separately.
func expectHuddleSaveSnapshotMemberTail(mock pgxmock.PgxPoolIface, memberGen int64) {
	mock.ExpectQuery(`SELECT nextval\('huddle_member_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(memberGen))
	mock.ExpectExec(`DELETE FROM huddle_member WHERE snapshot_gen < \$1`).
		WithArgs(memberGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
}

// emptyMemberRows returns a no-row pgxmock row set for the
// huddle_member query in LoadAll.
func emptyMemberRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"huddle_id", "actor_id"})
}

// --- LoadAll happy path ---------------------------------------------------

// TestHuddlesRepo_LoadAll_HappyPath — parents + member stitching across
// multiple huddles. Covers active (concluded_at NULL) and concluded
// (concluded_at set) huddles, indoor (structure_id set) and outdoor
// (structure_id NULL) shapes.
func TestHuddlesRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPoolH(t)

	structureA := "structure-tavern"
	started := time.Date(2026, 5, 18, 19, 0, 0, 0, time.UTC)
	concluded := started.Add(30 * time.Minute)

	parentRows := pgxmock.NewRows([]string{
		"id", "structure_id", "started_at", "concluded_at",
	}).
		// Indoor active huddle.
		AddRow(hudA, &structureA, started, (*time.Time)(nil)).
		// Outdoor active huddle (structure_id NULL).
		AddRow(hudB, (*string)(nil), started.Add(5*time.Minute), (*time.Time)(nil)).
		// Concluded indoor huddle — no member rows expected.
		AddRow(hudC, &structureA, started.Add(-1*time.Hour), &concluded)
	mock.ExpectQuery(`SELECT[\s\S]+FROM scene_huddle`).WillReturnRows(parentRows)

	memberRows := pgxmock.NewRows([]string{"huddle_id", "actor_id"}).
		AddRow(hudA, actorAlice).
		AddRow(hudA, actorBob).
		AddRow(hudB, actorCarol)
	mock.ExpectQuery(`SELECT[\s\S]+FROM huddle_member`).WillReturnRows(memberRows)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d huddles, want 3", len(got))
	}

	a := got[sim.HuddleID(hudA)]
	if a == nil {
		t.Fatal("hudA missing")
	}
	if a.StructureID != sim.StructureID(structureA) {
		t.Errorf("hudA.StructureID = %q, want %q", a.StructureID, structureA)
	}
	if !a.StartedAt.Equal(started) {
		t.Errorf("hudA.StartedAt = %v, want %v", a.StartedAt, started)
	}
	if a.ConcludedAt != nil {
		t.Errorf("hudA.ConcludedAt = %v, want nil (active)", a.ConcludedAt)
	}
	if len(a.Members) != 2 {
		t.Errorf("hudA.Members len=%d, want 2", len(a.Members))
	}
	if _, ok := a.Members[actorAlice]; !ok {
		t.Errorf("hudA missing alice")
	}
	if _, ok := a.Members[actorBob]; !ok {
		t.Errorf("hudA missing bob")
	}

	b := got[sim.HuddleID(hudB)]
	if b == nil {
		t.Fatal("hudB missing")
	}
	if b.StructureID != "" {
		t.Errorf("hudB.StructureID = %q, want empty (outdoor)", b.StructureID)
	}
	if len(b.Members) != 1 {
		t.Errorf("hudB.Members len=%d, want 1", len(b.Members))
	}

	c := got[sim.HuddleID(hudC)]
	if c == nil {
		t.Fatal("hudC missing")
	}
	if c.ConcludedAt == nil || !c.ConcludedAt.Equal(concluded) {
		t.Errorf("hudC.ConcludedAt = %v, want %v", c.ConcludedAt, concluded)
	}
	if len(c.Members) != 0 {
		t.Errorf("hudC concluded — expected 0 members, got %d", len(c.Members))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestHuddlesRepo_LoadAll_Empty covers the empty-pg case — both
// queries return zero rows.
func TestHuddlesRepo_LoadAll_Empty(t *testing.T) {
	mock, repo := newMockPoolH(t)

	parentRows := pgxmock.NewRows([]string{
		"id", "structure_id", "started_at", "concluded_at",
	})
	mock.ExpectQuery(`SELECT[\s\S]+FROM scene_huddle`).WillReturnRows(parentRows)
	mock.ExpectQuery(`SELECT[\s\S]+FROM huddle_member`).WillReturnRows(emptyMemberRows())

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty result returned %d huddles", len(got))
	}
}

// TestHuddlesRepo_LoadAll_OrphanMemberRejected — a huddle_member row
// whose huddle_id isn't in the parent set surfaces as an error. FK
// CASCADE makes this impossible from valid writes, so the guard
// catches schema drift loudly.
func TestHuddlesRepo_LoadAll_OrphanMemberRejected(t *testing.T) {
	mock, repo := newMockPoolH(t)

	parentRows := pgxmock.NewRows([]string{
		"id", "structure_id", "started_at", "concluded_at",
	}).AddRow(hudA, (*string)(nil), time.Now(), (*time.Time)(nil))
	mock.ExpectQuery(`SELECT[\s\S]+FROM scene_huddle`).WillReturnRows(parentRows)

	// hudC is not in the parent set.
	memberRows := pgxmock.NewRows([]string{"huddle_id", "actor_id"}).
		AddRow(hudA, actorAlice).
		AddRow(hudC, actorBob)
	mock.ExpectQuery(`SELECT[\s\S]+FROM huddle_member`).WillReturnRows(memberRows)

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from orphan member row; got nil")
	}
}

// TestHuddlesRepo_LoadAll_ParentQueryError — parent query fails.
func TestHuddlesRepo_LoadAll_ParentQueryError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	mock.ExpectQuery(`SELECT[\s\S]+FROM scene_huddle`).
		WillReturnError(errors.New("conn closed"))
	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from parent query failure")
	}
}

// TestHuddlesRepo_LoadAll_MemberQueryError — member query fails after
// parent succeeded.
func TestHuddlesRepo_LoadAll_MemberQueryError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	parentRows := pgxmock.NewRows([]string{
		"id", "structure_id", "started_at", "concluded_at",
	}).AddRow(hudA, (*string)(nil), time.Now(), (*time.Time)(nil))
	mock.ExpectQuery(`SELECT[\s\S]+FROM scene_huddle`).WillReturnRows(parentRows)
	mock.ExpectQuery(`SELECT[\s\S]+FROM huddle_member`).
		WillReturnError(errors.New("conn closed"))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from member query failure")
	}
}

// --- SaveSnapshot ---------------------------------------------------------

// TestHuddlesRepo_SaveSnapshot_HappyPath — one active indoor huddle
// with two members + one concluded huddle (no members). Full
// lifecycle: lock, both nextvals, parent UPSERTs, parent delete-stale,
// child UPSERTs, child delete-stale.
func TestHuddlesRepo_SaveSnapshot_HappyPath(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 5)

	started := time.Date(2026, 5, 18, 19, 0, 0, 0, time.UTC)
	concluded := started.Add(30 * time.Minute)

	mock.ExpectExec(`INSERT INTO scene_huddle`).
		WithArgs(hudA, "structure-tavern", started, (*time.Time)(nil), int64(5)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`INSERT INTO scene_huddle`).
		WithArgs(hudB, "structure-tavern", started.Add(-1*time.Hour), &concluded, int64(5)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(5)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('huddle_member_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(50)))

	// hudA has two members (order varies by map iteration).
	mock.ExpectExec(`INSERT INTO huddle_member`).
		WithArgs(hudA, actorAlice, int64(50)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`INSERT INTO huddle_member`).
		WithArgs(hudA, actorBob, int64(50)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	// hudB is concluded (empty Members) — no UPSERTs for it.

	mock.ExpectExec(`DELETE FROM huddle_member WHERE snapshot_gen < \$1`).
		WithArgs(int64(50)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Relaxed for map-iteration order on parent UPSERTs + member UPSERTs.
	// Distinct SQL patterns (scene_huddle vs huddle_member vs delete vs
	// nextval) prevent cross-matching. Order-sensitive tests below keep
	// the default in-order matching.
	mock.MatchExpectationsInOrder(false)

	huddles := map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {
			ID:          sim.HuddleID(hudA),
			Members:     map[sim.ActorID]struct{}{actorAlice: {}, actorBob: {}},
			StructureID: sim.StructureID("structure-tavern"),
			StartedAt:   started,
		},
		sim.HuddleID(hudB): {
			ID:          sim.HuddleID(hudB),
			Members:     map[sim.ActorID]struct{}{}, // concluded — empty
			StructureID: sim.StructureID("structure-tavern"),
			StartedAt:   started.Add(-1 * time.Hour),
			ConcludedAt: &concluded,
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, huddles); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestHuddlesRepo_SaveSnapshot_EmptyMap — both gens still bump, both
// DELETEs sweep their tables.
func TestHuddlesRepo_SaveSnapshot_EmptyMap(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 7)
	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(7)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectHuddleSaveSnapshotMemberTail(mock, 70)

	if err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestHuddlesRepo_SaveSnapshot_NilTx — substrate-boundary nil check.
func TestHuddlesRepo_SaveSnapshot_NilTx(t *testing.T) {
	_, repo := newMockPoolH(t)
	err := repo.SaveSnapshot(context.Background(), nil, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {ID: sim.HuddleID(hudA), StartedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}
}

// TestHuddlesRepo_SaveSnapshot_NilHuddleSkipped — nil map entries are
// silently skipped; lifecycle still runs.
func TestHuddlesRepo_SaveSnapshot_NilHuddleSkipped(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectHuddleSaveSnapshotMemberTail(mock, 10)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): nil,
	})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestHuddlesRepo_SaveSnapshot_ZeroStartedAtRejected — substrate-
// boundary validation. Substrate Commands are public-callable, so we
// don't trust the caller to never pass zero StartedAt.
func TestHuddlesRepo_SaveSnapshot_ZeroStartedAtRejected(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {ID: sim.HuddleID(hudA)}, // zero StartedAt
	})
	if err == nil {
		t.Fatal("expected error for zero StartedAt")
	}
}

// TestHuddlesRepo_SaveSnapshot_EmptyHuddleIDRejected — substrate-
// boundary validation. Empty IDs would trip the scene_huddle_id_nonempty
// CHECK mid-Tx; catch them at the repo boundary so the failing
// checkpoint surfaces a clearer error than a CHECK violation buried
// in a partial-Tx rollback.
func TestHuddlesRepo_SaveSnapshot_EmptyHuddleIDRejected(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		"": {ID: "", StartedAt: time.Date(2026, 5, 18, 19, 0, 0, 0, time.UTC)},
	})
	if err == nil {
		t.Fatal("expected error for empty HuddleID")
	}
}

// TestHuddlesRepo_SaveSnapshot_KeyMismatchRejected — substrate-boundary
// validation. Defends against shape bugs in callers that build the map
// with mismatched keys (e.g. `huddles[hudA] = &Huddle{ID: hudB}`),
// which would otherwise stamp the row under h.ID while LoadAll would
// re-key it correctly — but any in-flight reader of `huddles[hudA]`
// would see a Huddle whose persisted shape doesn't match its key.
func TestHuddlesRepo_SaveSnapshot_KeyMismatchRejected(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {
			ID:        sim.HuddleID(hudB),
			StartedAt: time.Date(2026, 5, 18, 19, 0, 0, 0, time.UTC),
		},
	})
	if err == nil {
		t.Fatal("expected error for map-key vs h.ID mismatch")
	}
}

// TestHuddlesRepo_SaveSnapshot_ConcludedWithMembersRejected — substrate-
// boundary validation. v2 invariant: ConcludeHuddle wipes
// Huddle.Members in memory (engine/sim/huddle_commands.go:480), so a
// concluded huddle MUST have zero Members at SaveSnapshot time. A
// concluded huddle arriving with Members non-empty implies a missed
// wipe somewhere; surface it at the boundary.
func TestHuddlesRepo_SaveSnapshot_ConcludedWithMembersRejected(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)

	concluded := time.Date(2026, 5, 18, 19, 30, 0, 0, time.UTC)
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {
			ID:          sim.HuddleID(hudA),
			Members:     map[sim.ActorID]struct{}{actorAlice: {}},
			StartedAt:   time.Date(2026, 5, 18, 19, 0, 0, 0, time.UTC),
			ConcludedAt: &concluded,
		},
	})
	if err == nil {
		t.Fatal("expected error for concluded huddle with non-empty Members")
	}
}

// TestHuddlesRepo_SaveSnapshot_OutdoorHuddleNullStructure — empty
// StructureID → SQL NULL binding (outdoor huddle).
func TestHuddlesRepo_SaveSnapshot_OutdoorHuddleNullStructure(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 2)

	started := time.Date(2026, 5, 18, 19, 0, 0, 0, time.UTC)
	mock.ExpectExec(`INSERT INTO scene_huddle`).
		WithArgs(hudA, nil, started, (*time.Time)(nil), int64(2)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(2)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('huddle_member_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(20)))
	mock.ExpectExec(`INSERT INTO huddle_member`).
		WithArgs(hudA, actorAlice, int64(20)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM huddle_member WHERE snapshot_gen < \$1`).
		WithArgs(int64(20)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	huddles := map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {
			ID:        sim.HuddleID(hudA),
			Members:   map[sim.ActorID]struct{}{actorAlice: {}},
			StartedAt: started,
			// StructureID intentionally empty — outdoor huddle.
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, huddles); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestHuddlesRepo_SaveSnapshot_AdvisoryLockError — lock fails.
func TestHuddlesRepo_SaveSnapshot_AdvisoryLockError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {ID: sim.HuddleID(hudA), StartedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected error from advisory lock failure")
	}
}

// TestHuddlesRepo_SaveSnapshot_HuddleNextvalError — parent nextval fails.
func TestHuddlesRepo_SaveSnapshot_HuddleNextvalError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('huddle_snapshot_gen_seq`).
		WillReturnError(errors.New("sequence unavailable"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {ID: sim.HuddleID(hudA), StartedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected error from parent nextval failure")
	}
}

// TestHuddlesRepo_SaveSnapshot_HuddleUpsertError — parent upsert fails.
func TestHuddlesRepo_SaveSnapshot_HuddleUpsertError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO scene_huddle`).
		WillReturnError(errors.New("CHECK constraint violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {ID: sim.HuddleID(hudA), StartedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected error from parent upsert failure")
	}
}

// TestHuddlesRepo_SaveSnapshot_MemberNextvalError — child nextval fails
// after parent fully written.
func TestHuddlesRepo_SaveSnapshot_MemberNextvalError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO scene_huddle`).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('huddle_member_snapshot_gen_seq`).
		WillReturnError(errors.New("sequence unavailable"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {ID: sim.HuddleID(hudA), StartedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected error from member nextval failure")
	}
}

// TestHuddlesRepo_SaveSnapshot_MemberUpsertError — child upsert fails.
// Verifies the canonical failure mode for the UNIQUE(actor_id)
// invariant (real-pg would surface here as a unique-violation).
func TestHuddlesRepo_SaveSnapshot_MemberUpsertError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)
	started := time.Now()
	mock.ExpectExec(`INSERT INTO scene_huddle`).
		WithArgs(hudA, nil, started, (*time.Time)(nil), int64(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('huddle_member_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(10)))
	mock.ExpectExec(`INSERT INTO huddle_member`).
		WillReturnError(errors.New("unique_violation on uniq_huddle_member_actor"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{
		sim.HuddleID(hudA): {
			ID:        sim.HuddleID(hudA),
			Members:   map[sim.ActorID]struct{}{actorAlice: {}},
			StartedAt: started,
		},
	})
	if err == nil {
		t.Fatal("expected error from member upsert failure")
	}
}

// TestHuddlesRepo_SaveSnapshot_MemberDeleteStaleError — final
// delete-stale step on the child table fails.
func TestHuddlesRepo_SaveSnapshot_MemberDeleteStaleError(t *testing.T) {
	mock, repo := newMockPoolH(t)
	tx := fakeTx{mock: mock}

	expectHuddleSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM scene_huddle WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('huddle_member_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(10)))
	mock.ExpectExec(`DELETE FROM huddle_member WHERE snapshot_gen < \$1`).
		WithArgs(int64(10)).
		WillReturnError(errors.New("disk full"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.HuddleID]*sim.Huddle{})
	if err == nil {
		t.Fatal("expected error from member delete-stale failure")
	}
}
