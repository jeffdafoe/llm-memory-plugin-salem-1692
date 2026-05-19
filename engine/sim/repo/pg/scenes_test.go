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

// pgxmock-based tests for ScenesRepo (Slice 13). Asserts SQL shape +
// arg bindings + scan mapping. Real-pg behaviors (CHECK rejection, FK
// CASCADE, advisory lock blocking) land with the testcontainers smoke
// slice (deferred — 9 flags now).

const (
	sceA = "00000000-0000-0000-0000-aaaaaaaaaaaa"
	sceB = "00000000-0000-0000-0000-bbbbbbbbbbbb"
)

func newMockPoolSc(t *testing.T) (pgxmock.PgxPoolIface, *ScenesRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewScenesRepo(mock)
}

func expectSceneSaveSnapshotPrelude(mock pgxmock.PgxPoolIface, sceneGen int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\('scene_snapshot'`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('scene_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(sceneGen))
}

func expectSceneSaveSnapshotRefTail(mock pgxmock.PgxPoolIface, refGen int64) {
	mock.ExpectQuery(`SELECT nextval\('scene_huddle_ref_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(refGen))
	mock.ExpectExec(`DELETE FROM scene_huddle_ref WHERE snapshot_gen < \$1`).
		WithArgs(refGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
}

func emptySceneRefRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"scene_id", "huddle_id"})
}

// --- LoadAll ---------------------------------------------------------------

// TestScenesRepo_LoadAll_HappyPath — one structure-bound scene with two
// huddles + one area-bound scene with one huddle. Validates parent
// scan + Bound reconstruction for both variants + huddle ref stitching.
func TestScenesRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	mock.MatchExpectationsInOrder(false)

	structureID := strA
	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC), "pc_speak", "structure",
				&structureID, (*int)(nil), (*int)(nil), (*int)(nil), 5, 10).
			AddRow(sceB, time.Date(2026, 5, 19, 11, 0, 0, 0, time.UTC), "idle_backstop", "area",
				(*string)(nil), ptrInt(8), ptrInt(12), ptrInt(3), 8, 12))

	mock.ExpectQuery(`FROM scene_huddle_ref\b`).
		WillReturnRows(pgxmock.NewRows([]string{"scene_id", "huddle_id"}).
			AddRow(sceA, hudA).
			AddRow(sceA, hudB).
			AddRow(sceB, hudC))

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	a := got[sceA]
	if a == nil {
		t.Fatal("sceA missing")
	}
	if a.OriginKind != "pc_speak" {
		t.Errorf("sceA OriginKind = %q", a.OriginKind)
	}
	if a.Bound.Kind != sim.SceneBoundStructure {
		t.Errorf("sceA Bound.Kind = %q, want structure", a.Bound.Kind)
	}
	if a.Bound.StructureID == nil || *a.Bound.StructureID != sim.StructureID(strA) {
		t.Errorf("sceA Bound.StructureID = %v", a.Bound.StructureID)
	}
	if len(a.Huddles) != 2 {
		t.Errorf("sceA Huddles len = %d, want 2", len(a.Huddles))
	}

	b := got[sceB]
	if b.Bound.Kind != sim.SceneBoundArea {
		t.Errorf("sceB Bound.Kind = %q, want area", b.Bound.Kind)
	}
	if b.Bound.Anchor == nil || *b.Bound.Radius != 3 {
		t.Errorf("sceB Bound = %v", b.Bound)
	}
	if len(b.Huddles) != 1 {
		t.Errorf("sceB Huddles len = %d, want 1", len(b.Huddles))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func ptrInt(i int) *int { return &i }

// TestScenesRepo_LoadAll_Empty — both queries return no rows.
func TestScenesRepo_LoadAll_Empty(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}))
	mock.ExpectQuery(`FROM scene_huddle_ref\b`).
		WillReturnRows(emptySceneRefRows())

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

// TestScenesRepo_LoadAll_OrphanRefRejected — ref row with no parent
// → error.
func TestScenesRepo_LoadAll_OrphanRefRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	structureID := strA
	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Now(), "pc_speak", "structure",
				&structureID, (*int)(nil), (*int)(nil), (*int)(nil), 5, 10))

	mock.ExpectQuery(`FROM scene_huddle_ref\b`).
		WillReturnRows(pgxmock.NewRows([]string{"scene_id", "huddle_id"}).
			AddRow(sceB, hudA)) // sceB not loaded

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for orphan ref row")
	}
}

// TestScenesRepo_LoadAll_DuplicateSceneIDRejected — defensive against
// admin-direct writes / schema drift (code_review R1). PK prevents
// duplicates in valid DB state; this matches the loud-drift guards on
// orphan rows + unknown bound_kind.
func TestScenesRepo_LoadAll_DuplicateSceneIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	structureID := strA
	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Now(), "pc_speak", "structure",
				&structureID, (*int)(nil), (*int)(nil), (*int)(nil), 5, 10).
			AddRow(sceA, time.Now(), "idle_backstop", "structure",
				&structureID, (*int)(nil), (*int)(nil), (*int)(nil), 5, 10))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for duplicate scene id in result set")
	}
}

// TestScenesRepo_LoadAll_StructureBoundWithAreaFieldsRejected —
// scanBound tightening (code_review R1). A corrupt row with bound_kind=
// structure but anchor/radius columns populated must error loudly
// rather than silently load as structure ignoring the area payload.
func TestScenesRepo_LoadAll_StructureBoundWithAreaFieldsRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	structureID := strA
	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Now(), "pc_speak", "structure",
				&structureID, ptrInt(0), ptrInt(0), ptrInt(3), 5, 10))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for structure-bound row with area columns populated")
	}
}

// TestScenesRepo_LoadAll_AreaBoundWithStructureIDRejected.
func TestScenesRepo_LoadAll_AreaBoundWithStructureIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	structureID := strA
	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Now(), "pc_speak", "area",
				&structureID, ptrInt(8), ptrInt(12), ptrInt(3), 5, 10))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for area-bound row with bound_structure_id populated")
	}
}

// TestScenesRepo_LoadAll_AreaBoundNegativeRadiusRejected — NewAreaBound
// clamps negative radii to 0, which would hide DB corruption. scanBound
// must catch it explicitly. (code_review R1.)
func TestScenesRepo_LoadAll_AreaBoundNegativeRadiusRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Now(), "pc_speak", "area",
				(*string)(nil), ptrInt(8), ptrInt(12), ptrInt(-1), 5, 10))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for area-bound row with negative radius")
	}
}

// TestScenesRepo_SaveSnapshot_UnboundedWithPayloadRejected — code_review
// R1: Unbounded scenes must have all variant fields nil. An in-memory
// Unbounded scene with populated StructureID/Anchor/Radius is corrupt
// state; lock down today, loosen via migration if future world-scope
// variants need payload.
func TestScenesRepo_SaveSnapshot_UnboundedWithPayloadRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	id := sim.StructureID(strA)
	badBound := sim.SceneBound{Kind: sim.SceneBoundUnbounded, StructureID: &id}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {
			ID:         sim.SceneID(sceA),
			OriginKind: "atmosphere_refresh",
			OriginAt:   time.Now(),
			Bound:      badBound,
		},
	})
	if err == nil {
		t.Fatal("expected error for Unbounded scene with payload")
	}
}

// TestScenesRepo_LoadAll_UnknownBoundKindRejected — defensive against
// schema drift.
func TestScenesRepo_LoadAll_UnknownBoundKindRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}).
			AddRow(sceA, time.Now(), "pc_speak", "future_variant",
				(*string)(nil), (*int)(nil), (*int)(nil), (*int)(nil), 5, 10))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown bound_kind")
	}
}

// TestScenesRepo_LoadAll_ParentQueryError.
func TestScenesRepo_LoadAll_ParentQueryError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	mock.ExpectQuery(`FROM scene\b`).WillReturnError(errors.New("connection lost"))
	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from parent query")
	}
}

// TestScenesRepo_LoadAll_RefQueryError.
func TestScenesRepo_LoadAll_RefQueryError(t *testing.T) {
	mock, repo := newMockPoolSc(t)

	mock.ExpectQuery(`FROM scene\b`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "origin_at", "origin_kind", "bound_kind",
			"bound_structure_id", "bound_anchor_x", "bound_anchor_y", "bound_radius",
			"origin_position_x", "origin_position_y",
		}))
	mock.ExpectQuery(`FROM scene_huddle_ref\b`).WillReturnError(errors.New("connection lost"))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error from ref query")
	}
}

// --- SaveSnapshot happy path -----------------------------------------------

// TestScenesRepo_SaveSnapshot_HappyPath — 1 structure-bound + 1 area-bound +
// 1 unbounded scene. Unbounded is filtered from upsert but identity is
// still validated (verified by lack of error). Refs upsert only for
// persisted scenes.
func TestScenesRepo_SaveSnapshot_HappyPath(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}
	mock.MatchExpectationsInOrder(false)

	expectSceneSaveSnapshotPrelude(mock, 5)

	originAt := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)

	mock.ExpectExec(`INSERT INTO scene\b`).
		WithArgs(sceA, originAt, "pc_speak", "structure",
			strA, nil, nil, nil, 5, 10, int64(5)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`INSERT INTO scene\b`).
		WithArgs(sceB, originAt, "idle_backstop", "area",
			nil, 8, 12, 3, 8, 12, int64(5)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM scene WHERE snapshot_gen < \$1`).
		WithArgs(int64(5)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('scene_huddle_ref_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(50)))

	mock.ExpectExec(`INSERT INTO scene_huddle_ref\b`).
		WithArgs(sceA, hudA, int64(50)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`INSERT INTO scene_huddle_ref\b`).
		WithArgs(sceB, hudB, int64(50)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM scene_huddle_ref WHERE snapshot_gen < \$1`).
		WithArgs(int64(50)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	unboundedID := sim.SceneID("00000000-0000-0000-0000-cccccccccccc")
	scenes := map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {
			ID:             sim.SceneID(sceA),
			OriginAt:       originAt,
			OriginKind:     "pc_speak",
			Bound:          sim.NewStructureBound(sim.StructureID(strA)),
			OriginPosition: sim.Position{X: 5, Y: 10},
			Huddles:        map[sim.HuddleID]struct{}{sim.HuddleID(hudA): {}},
		},
		sim.SceneID(sceB): {
			ID:             sim.SceneID(sceB),
			OriginAt:       originAt,
			OriginKind:     "idle_backstop",
			Bound:          sim.NewAreaBound(sim.Position{X: 8, Y: 12}, 3),
			OriginPosition: sim.Position{X: 8, Y: 12},
			Huddles:        map[sim.HuddleID]struct{}{sim.HuddleID(hudB): {}},
		},
		unboundedID: {
			ID:             unboundedID,
			OriginAt:       originAt,
			OriginKind:     "atmosphere_refresh",
			Bound:          sim.NewUnboundedBound(),
			OriginPosition: sim.Position{X: 0, Y: 0},
			// Huddles silently discarded — unbounded scene is filtered.
			Huddles: map[sim.HuddleID]struct{}{sim.HuddleID(hudC): {}},
		},
	}

	if err := repo.SaveSnapshot(context.Background(), tx, scenes); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestScenesRepo_SaveSnapshot_EmptyMap — both gens still bump, both
// DELETEs sweep.
func TestScenesRepo_SaveSnapshot_EmptyMap(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM scene WHERE snapshot_gen < \$1`).
		WithArgs(int64(1)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectSceneSaveSnapshotRefTail(mock, 10)

	if err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestScenesRepo_SaveSnapshot_NilTx.
func TestScenesRepo_SaveSnapshot_NilTx(t *testing.T) {
	_, repo := newMockPoolSc(t)
	err := repo.SaveSnapshot(context.Background(), nil, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}
}

// --- SaveSnapshot validation tests (substrate boundary) --------------------

func TestScenesRepo_SaveSnapshot_NilSceneRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): nil,
	})
	if err == nil {
		t.Fatal("expected error for nil scene entry")
	}
}

func TestScenesRepo_SaveSnapshot_EmptySceneIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		"": {ID: "", OriginKind: "pc_speak", OriginAt: time.Now(), Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for empty SceneID")
	}
}

func TestScenesRepo_SaveSnapshot_WhitespaceSceneIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		"   ": {ID: "   ", OriginKind: "pc_speak", OriginAt: time.Now(), Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only SceneID")
	}
}

func TestScenesRepo_SaveSnapshot_KeyMismatchRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceB), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for map-key vs s.ID mismatch")
	}
}

func TestScenesRepo_SaveSnapshot_EmptyOriginKindRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "", OriginAt: time.Now(), Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for empty OriginKind")
	}
}

func TestScenesRepo_SaveSnapshot_WhitespaceOriginKindRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "  \t  ", OriginAt: time.Now(), Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only OriginKind")
	}
}

func TestScenesRepo_SaveSnapshot_ZeroOriginAtRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", Bound: sim.NewStructureBound(sim.StructureID(strA))},
	})
	if err == nil {
		t.Fatal("expected error for zero OriginAt")
	}
}

// --- Bound shape validation -------------------------------------------------

func TestScenesRepo_SaveSnapshot_StructureBoundMissingStructureIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	badBound := sim.SceneBound{Kind: sim.SceneBoundStructure} // StructureID nil
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for structure bound with nil StructureID")
	}
}

func TestScenesRepo_SaveSnapshot_StructureBoundEmptyStructureIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	emptyID := sim.StructureID("   ")
	badBound := sim.SceneBound{Kind: sim.SceneBoundStructure, StructureID: &emptyID}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only StructureID in bound")
	}
}

func TestScenesRepo_SaveSnapshot_StructureBoundWithAnchorRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	id := sim.StructureID(strA)
	anchor := sim.Position{X: 0, Y: 0}
	badBound := sim.SceneBound{Kind: sim.SceneBoundStructure, StructureID: &id, Anchor: &anchor}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for structure bound with Anchor set")
	}
}

func TestScenesRepo_SaveSnapshot_AreaBoundMissingAnchorRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	radius := 3
	badBound := sim.SceneBound{Kind: sim.SceneBoundArea, Radius: &radius} // Anchor nil
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for area bound with nil Anchor")
	}
}

func TestScenesRepo_SaveSnapshot_AreaBoundWithStructureIDRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	id := sim.StructureID(strA)
	anchor := sim.Position{X: 0, Y: 0}
	radius := 3
	badBound := sim.SceneBound{
		Kind: sim.SceneBoundArea, StructureID: &id, Anchor: &anchor, Radius: &radius,
	}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for area bound with StructureID set")
	}
}

func TestScenesRepo_SaveSnapshot_AreaBoundNegativeRadiusRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	// NewAreaBound clamps negative radii to 0, so we have to construct
	// the bad bound directly to test substrate validation.
	anchor := sim.Position{X: 0, Y: 0}
	radius := -1
	badBound := sim.SceneBound{Kind: sim.SceneBoundArea, Anchor: &anchor, Radius: &radius}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for area bound with negative Radius")
	}
}

func TestScenesRepo_SaveSnapshot_UnknownBoundKindRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	badBound := sim.SceneBound{Kind: sim.SceneBoundKind("future_variant")}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {ID: sim.SceneID(sceA), OriginKind: "pc_speak", OriginAt: time.Now(), Bound: badBound},
	})
	if err == nil {
		t.Fatal("expected error for unknown Bound.Kind")
	}
}

func TestScenesRepo_SaveSnapshot_EmptyHuddleIDInSetRejected(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {
			ID:         sim.SceneID(sceA),
			OriginKind: "pc_speak",
			OriginAt:   time.Now(),
			Bound:      sim.NewStructureBound(sim.StructureID(strA)),
			Huddles:    map[sim.HuddleID]struct{}{"   ": {}},
		},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only HuddleID in Huddles set")
	}
}

// TestScenesRepo_SaveSnapshot_UnboundedSceneStillValidatesIdentity —
// design_review #8: an Unbounded scene with a corrupt identity (zero
// OriginAt) should error even though it would be skipped from upsert.
func TestScenesRepo_SaveSnapshot_UnboundedSceneStillValidatesIdentity(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {
			ID:         sim.SceneID(sceA),
			OriginKind: "atmosphere_refresh",
			// OriginAt zero — should still error even though Unbounded
			// is filtered from upsert.
			Bound: sim.NewUnboundedBound(),
		},
	})
	if err == nil {
		t.Fatal("expected identity validation error on Unbounded scene with zero OriginAt")
	}
}

// --- SaveSnapshot SQL error paths ------------------------------------------

func TestScenesRepo_SaveSnapshot_AdvisoryLockError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{})
	if err == nil {
		t.Fatal("expected error from advisory lock")
	}
}

func TestScenesRepo_SaveSnapshot_SceneNextvalError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('scene_snapshot_gen_seq`).
		WillReturnError(errors.New("sequence broken"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{})
	if err == nil {
		t.Fatal("expected error from scene nextval")
	}
}

func TestScenesRepo_SaveSnapshot_SceneUpsertError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO scene\b`).WillReturnError(errors.New("check constraint violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {
			ID:         sim.SceneID(sceA),
			OriginKind: "pc_speak",
			OriginAt:   time.Now(),
			Bound:      sim.NewStructureBound(sim.StructureID(strA)),
		},
	})
	if err == nil {
		t.Fatal("expected error from scene upsert")
	}
}

func TestScenesRepo_SaveSnapshot_RefNextvalError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM scene WHERE snapshot_gen < \$1`).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('scene_huddle_ref_snapshot_gen_seq`).
		WillReturnError(errors.New("sequence broken"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{})
	if err == nil {
		t.Fatal("expected error from ref nextval")
	}
}

func TestScenesRepo_SaveSnapshot_RefUpsertError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`INSERT INTO scene\b`).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM scene WHERE snapshot_gen < \$1`).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('scene_huddle_ref_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(10)))
	mock.ExpectExec(`INSERT INTO scene_huddle_ref\b`).
		WillReturnError(errors.New("FK violation"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{
		sim.SceneID(sceA): {
			ID:         sim.SceneID(sceA),
			OriginKind: "pc_speak",
			OriginAt:   time.Now(),
			Bound:      sim.NewStructureBound(sim.StructureID(strA)),
			Huddles:    map[sim.HuddleID]struct{}{sim.HuddleID(hudA): {}},
		},
	})
	if err == nil {
		t.Fatal("expected error from ref upsert")
	}
}

func TestScenesRepo_SaveSnapshot_SceneDeleteStaleError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM scene WHERE snapshot_gen < \$1`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{})
	if err == nil {
		t.Fatal("expected error from scene delete-stale")
	}
}

func TestScenesRepo_SaveSnapshot_RefDeleteStaleError(t *testing.T) {
	mock, repo := newMockPoolSc(t)
	tx := fakeTx{mock: mock}

	expectSceneSaveSnapshotPrelude(mock, 1)
	mock.ExpectExec(`DELETE FROM scene WHERE snapshot_gen < \$1`).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('scene_huddle_ref_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(10)))
	mock.ExpectExec(`DELETE FROM scene_huddle_ref WHERE snapshot_gen < \$1`).
		WillReturnError(errors.New("connection lost"))

	err := repo.SaveSnapshot(context.Background(), tx, map[sim.SceneID]*sim.Scene{})
	if err == nil {
		t.Fatal("expected error from ref delete-stale")
	}
}
