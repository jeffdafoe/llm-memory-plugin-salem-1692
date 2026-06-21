package pg

// Real-pg integration tests for VillageObjectsRepo (Slice 14, first
// aggregate). Run against an embedded Postgres with the full prod-baseline
// schema applied. Skipped under `go test -short`.
//
// These exercise the substrate semantics pgxmock can't validate: gen-marker
// resulting state, the safer-DELETE + orphan-check rollback, FK CASCADE on
// the attached_to self-reference, CHECK enforcement (tags null-element,
// entry_policy domain), array round-trip, and the object_refresh child
// co-managed under the parent's checkpoint Tx.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestIntegration_SchemaApplies is the bootstrap sanity check: it forces
// the template database to build (baseline + all post-baseline migrations)
// and confirms a representative v2 table exists. If the migration set
// doesn't apply cleanly on the prod baseline, this fails at fixture setup.
func TestIntegration_SchemaApplies(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	var n int
	if err := f.Pool.QueryRow(ctx, `SELECT 1`).Scan(&n); err != nil {
		t.Fatalf("trivial query: %v", err)
	}
	if n != 1 {
		t.Fatalf("SELECT 1 = %d", n)
	}

	// A handful of v2-substrate columns the post-baseline migrations add
	// on top of the prod baseline — proves the migrations actually ran.
	checks := []struct{ table, column string }{
		{"village_object", "snapshot_gen"},     // ZBBS-WORK-237
		{"actor", "sim_state"},                 // ZBBS-WORK-243
		{"actor_relationship", "snapshot_gen"}, // ZBBS-WORK-244
		{"actor_relationship", "dropped_fact_count"},
		{"npc_acquaintance", "snapshot_gen"},
	}
	for _, c := range checks {
		var exists bool
		if err := f.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2
			)`, c.table, c.column).Scan(&exists); err != nil {
			t.Fatalf("column check %s.%s: %v", c.table, c.column, err)
		}
		if !exists {
			t.Errorf("expected column %s.%s to exist after migrations", c.table, c.column)
		}
	}
}

// --- helpers --------------------------------------------------------------

// voRepo binds a VillageObjectsRepo to the fixture pool. *pgxpool.Pool
// satisfies the package Pool interface directly.
func voRepo(f *integrationFixture) *VillageObjectsRepo {
	return NewVillageObjectsRepo(f.Pool)
}

// saveSnapshotVO drives VillageObjectsRepo.SaveSnapshot inside a real Tx
// (begin → SaveSnapshot → commit). On SaveSnapshot error it rolls back and
// returns the error, so callers can assert the rollback left pg untouched.
func saveSnapshotVO(t *testing.T, f *integrationFixture, repo *VillageObjectsRepo, objects map[sim.VillageObjectID]*sim.VillageObject) error {
	t.Helper()
	ctx := t.Context()
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := repo.SaveSnapshot(ctx, &txAdapter{tx: tx}, objects); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return nil
}

// pgErrCode extracts the SQLSTATE from a (possibly wrapped) pg error, or ""
// if the error isn't a *pgconn.PgError.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// countVO returns the number of village_object rows.
func countVO(t *testing.T, f *integrationFixture) int {
	t.Helper()
	var n int
	if err := f.Pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM village_object`).Scan(&n); err != nil {
		t.Fatalf("count village_object: %v", err)
	}
	return n
}

// sameStringSet compares two string slices order-independently.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, c := range seen {
		if c != 0 {
			return false
		}
	}
	return true
}

func refreshByAttr(refs []*sim.ObjectRefresh) map[sim.NeedKey]*sim.ObjectRefresh {
	m := make(map[sim.NeedKey]*sim.ObjectRefresh, len(refs))
	for _, r := range refs {
		m[r.Attribute] = r
	}
	return m
}

// --- scenarios ------------------------------------------------------------

// #1 LoadAll happy path — seed rows directly (not via SaveSnapshot) so this
// exercises LoadAll in isolation, including nullable owner → "" and tags
// nil→empty-slice normalization.
func TestIntegration_VillageObjects_LoadAllHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object
			(id, asset_id, current_state, x, y, placed_by, display_name,
			 entry_policy, owner_actor_id, loiter_offset_x, loiter_offset_y,
			 available_quantity, tags)
		VALUES
			($1, $2, 'default', 640, 320, '', 'Well', 'open',
			 'actor-7', 3, 4, 5, ARRAY['vendor','well']),
			($3, $4, 'lit', 10, 20, '', 'Lamp', 'closed',
			 NULL, NULL, NULL, 0, '{}')`,
		uuidObj1, uuidAssetWell, uuidObj2, uuidAssetLamp); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := voRepo(f).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadAll len=%d, want 2", len(got))
	}

	well := got[sim.VillageObjectID(uuidObj1)]
	if well == nil {
		t.Fatal("well missing")
	}
	if well.AssetID != sim.AssetID(uuidAssetWell) || well.DisplayName != "Well" ||
		well.EntryPolicy != sim.EntryPolicyOpen || well.OwnerActorID != "actor-7" ||
		well.AvailableQuantity != 5 {
		t.Errorf("well unexpected: %+v", well)
	}
	if well.LoiterOffsetX == nil || *well.LoiterOffsetX != 3 ||
		well.LoiterOffsetY == nil || *well.LoiterOffsetY != 4 {
		t.Errorf("well loiter offsets = %v,%v", well.LoiterOffsetX, well.LoiterOffsetY)
	}
	if !sameStringSet(well.Tags, []string{"vendor", "well"}) {
		t.Errorf("well tags = %#v", well.Tags)
	}

	lamp := got[sim.VillageObjectID(uuidObj2)]
	if lamp == nil {
		t.Fatal("lamp missing")
	}
	if lamp.OwnerActorID != "" {
		t.Errorf("lamp owner should map NULL→\"\", got %q", lamp.OwnerActorID)
	}
	if lamp.LoiterOffsetX != nil || lamp.LoiterOffsetY != nil {
		t.Errorf("lamp loiter offsets should be nil, got %v,%v", lamp.LoiterOffsetX, lamp.LoiterOffsetY)
	}
	if lamp.Tags == nil || len(lamp.Tags) != 0 {
		t.Errorf("lamp tags should be empty non-nil slice, got %#v", lamp.Tags)
	}
}

// #2 SaveSnapshot round-trip — populate a Go map, SaveSnapshot, LoadAll,
// assert the parent fields survive (tags compared as a set).
func TestIntegration_VillageObjects_SaveSnapshotRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := voRepo(f)

	lx, ly := 2, -1
	want := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {
			ID:                sim.VillageObjectID(uuidObj1),
			AssetID:           sim.AssetID(uuidAssetWell),
			CurrentState:      "default",
			Pos:               sim.WorldPos{X: 640, Y: 320},
			PlacedBy:          "system",
			DisplayName:       "Well",
			EntryPolicy:       sim.EntryPolicyOpen,
			OwnerActorID:      "actor-7",
			LoiterOffsetX:     &lx,
			LoiterOffsetY:     &ly,
			Tags:              []string{"vendor", "well"},
			AvailableQuantity: 5,
		},
		sim.VillageObjectID(uuidObj2): {
			ID:           sim.VillageObjectID(uuidObj2),
			AssetID:      sim.AssetID(uuidAssetLamp),
			CurrentState: "lit",
			Pos:          sim.WorldPos{X: 10, Y: 20},
			EntryPolicy:  sim.EntryPolicyClosed,
			Tags:         []string{},
		},
	}
	if err := saveSnapshotVO(t, f, repo, want); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("round-trip len=%d, want 2", len(got))
	}

	g1 := got[sim.VillageObjectID(uuidObj1)]
	if g1 == nil {
		t.Fatal("obj1 missing after round-trip")
	}
	if g1.AssetID != sim.AssetID(uuidAssetWell) || g1.CurrentState != "default" ||
		g1.Pos.X != 640 || g1.Pos.Y != 320 || g1.PlacedBy != "system" ||
		g1.DisplayName != "Well" || g1.EntryPolicy != sim.EntryPolicyOpen ||
		g1.OwnerActorID != "actor-7" || g1.AvailableQuantity != 5 {
		t.Errorf("obj1 round-trip mismatch: %+v", g1)
	}
	if g1.LoiterOffsetX == nil || *g1.LoiterOffsetX != 2 ||
		g1.LoiterOffsetY == nil || *g1.LoiterOffsetY != -1 {
		t.Errorf("obj1 loiter offsets = %v,%v", g1.LoiterOffsetX, g1.LoiterOffsetY)
	}
	if !sameStringSet(g1.Tags, []string{"vendor", "well"}) {
		t.Errorf("obj1 tags = %#v", g1.Tags)
	}

	g2 := got[sim.VillageObjectID(uuidObj2)]
	if g2 == nil {
		t.Fatal("obj2 missing after round-trip")
	}
	if g2.CurrentState != "lit" || g2.EntryPolicy != sim.EntryPolicyClosed ||
		g2.OwnerActorID != "" || len(g2.Tags) != 0 {
		t.Errorf("obj2 round-trip mismatch: %+v", g2)
	}
}

// #3 SaveSnapshot gen-marker pruning — seed two objects, SaveSnapshot only
// one; the other is pruned by the trailing delete-stale.
func TestIntegration_VillageObjects_GenMarkerPrune(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := voRepo(f)

	two := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
		sim.VillageObjectID(uuidObj2): {ID: sim.VillageObjectID(uuidObj2), AssetID: sim.AssetID(uuidAssetLamp), EntryPolicy: sim.EntryPolicyClosed},
	}
	if err := saveSnapshotVO(t, f, repo, two); err != nil {
		t.Fatalf("SaveSnapshot two: %v", err)
	}
	if n := countVO(t, f); n != 2 {
		t.Fatalf("after first snapshot count=%d, want 2", n)
	}

	one := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
	}
	if err := saveSnapshotVO(t, f, repo, one); err != nil {
		t.Fatalf("SaveSnapshot one: %v", err)
	}
	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[sim.VillageObjectID(uuidObj1)] == nil {
		t.Fatalf("after prune got %d rows, want only obj1", len(got))
	}
	if got[sim.VillageObjectID(uuidObj2)] != nil {
		t.Error("obj2 should have been pruned")
	}
}

// #4 SaveSnapshot empty map clears the table.
func TestIntegration_VillageObjects_EmptyMapClears(t *testing.T) {
	f := newFixture(t)
	repo := voRepo(f)

	seed := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {ID: sim.VillageObjectID(uuidObj1), AssetID: sim.AssetID(uuidAssetWell), EntryPolicy: sim.EntryPolicyOpen},
		sim.VillageObjectID(uuidObj2): {ID: sim.VillageObjectID(uuidObj2), AssetID: sim.AssetID(uuidAssetLamp), EntryPolicy: sim.EntryPolicyClosed},
	}
	if err := saveSnapshotVO(t, f, repo, seed); err != nil {
		t.Fatalf("SaveSnapshot seed: %v", err)
	}
	if err := saveSnapshotVO(t, f, repo, map[sim.VillageObjectID]*sim.VillageObject{}); err != nil {
		t.Fatalf("SaveSnapshot empty: %v", err)
	}
	if n := countVO(t, f); n != 0 {
		t.Fatalf("after empty snapshot count=%d, want 0", n)
	}
}

// #5 Safer-DELETE preserves a stale parent that still has a fresh child, and
// the orphan check then errors + rolls back the Tx (cross-tier snapshot is a
// world-invariant violation). Deterministic sequence setup per the design.
func TestIntegration_VillageObjects_OrphanCheckRollback(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// Pin the sequence so the next SaveSnapshot's nextval returns 5.
	if _, err := f.Pool.Exec(ctx, `SELECT setval('village_object_snapshot_gen_seq', 4, true)`); err != nil {
		t.Fatalf("setval: %v", err)
	}
	// Stale parent at gen 4, fresh attached child at gen 5.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, entry_policy, available_quantity, tags, snapshot_gen)
		VALUES ($1, $2, 'default', 0, 0, '', 'closed', 0, '{}', 4)`,
		uuidObj1, uuidAssetWell); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, entry_policy, attached_to, available_quantity, tags, snapshot_gen)
		VALUES ($1, $2, 'default', 0, 0, '', 'closed', $3, 0, '{}', 5)`,
		uuidObj2, uuidAssetLamp, uuidObj1); err != nil {
		t.Fatalf("seed child: %v", err)
	}

	// Empty snapshot: parent (gen4) is stale but kept by the safer-DELETE
	// because the fresh child (gen5) still references it; the orphan check
	// then fires.
	err := saveSnapshotVO(t, f, voRepo(f), map[sim.VillageObjectID]*sim.VillageObject{})
	if err == nil {
		t.Fatal("expected orphan-check error, got nil")
	}

	// Rolled back: both rows remain.
	if n := countVO(t, f); n != 2 {
		t.Fatalf("after rollback count=%d, want 2 (both rows preserved)", n)
	}
}

// #6 tags array null-element CHECK (village_object_tags_no_nulls).
func TestIntegration_VillageObjects_TagsNullElementCheck(t *testing.T) {
	f := newFixture(t)
	_, err := f.Pool.Exec(t.Context(), `
		INSERT INTO village_object (id, asset_id, x, y, placed_by, tags)
		VALUES (gen_random_uuid(), $1, 0, 0, '', ARRAY['ok', NULL]::text[])`,
		uuidAssetWell)
	if got := pgErrCode(err); got != "23514" {
		t.Fatalf("tags null-element: got err=%v (code %q), want check_violation 23514", err, got)
	}
}

// #7 entry_policy domain CHECK — TEXT+CHECK (not enum), so a bad value is a
// check_violation (23514), not invalid_text_representation (22P02).
func TestIntegration_VillageObjects_EntryPolicyCheck(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	for _, ok := range []string{"", "open", "owner-only", "closed"} {
		_, err := f.Pool.Exec(ctx, `
			INSERT INTO village_object (id, asset_id, x, y, placed_by, entry_policy)
			VALUES (gen_random_uuid(), $1, 0, 0, '', $2)`, uuidAssetWell, ok)
		if err != nil {
			t.Errorf("entry_policy %q should be accepted, got: %v", ok, err)
		}
	}

	_, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, x, y, placed_by, entry_policy)
		VALUES (gen_random_uuid(), $1, 0, 0, '', 'bogus')`, uuidAssetWell)
	if got := pgErrCode(err); got != "23514" {
		t.Fatalf("entry_policy 'bogus': got err=%v (code %q), want check_violation 23514", err, got)
	}
}

// #8 FK CASCADE on attached_to — deleting a parent cascades to its overlay.
func TestIntegration_VillageObjects_AttachedToCascade(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, entry_policy, tags)
		VALUES ($1, $2, 'default', 0, 0, '', 'closed', '{}')`,
		uuidObj1, uuidAssetWell); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, entry_policy, attached_to, tags)
		VALUES ($1, $2, 'default', 0, 0, '', 'closed', $3, '{}')`,
		uuidObj2, uuidAssetLamp, uuidObj1); err != nil {
		t.Fatalf("seed overlay: %v", err)
	}

	if _, err := f.Pool.Exec(ctx, `DELETE FROM village_object WHERE id = $1`, uuidObj1); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if n := countVO(t, f); n != 0 {
		t.Fatalf("after parent delete count=%d, want 0 (overlay cascaded)", n)
	}
}

// #9 Refreshes co-managed under the parent Tx — SaveSnapshot writes the
// object_refresh children; LoadAll stitches them back; dropping a refresh
// attribute from the snapshot prunes its row via the child gen-marker.
// Also validates the prod-conformance: an infinite (no-supply) row's
// refresh_mode round-trips as the 'continuous' NOT-NULL default.
func TestIntegration_VillageObjects_RefreshesCoManaged(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := voRepo(f)

	// object_refresh.attribute FKs refresh_attribute(name); the schema-only
	// baseline ships that lookup table empty, so seed the attributes used.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO refresh_attribute (name, display_label)
		VALUES ('thirst', 'Thirst'), ('tiredness', 'Tiredness')`); err != nil {
		t.Fatalf("seed refresh_attribute: %v", err)
	}

	max10, avail3, period12 := 10, 3, 12
	anchor := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	dwellDelta, dwellPeriod := -1, 15

	withBoth := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {
			ID:           sim.VillageObjectID(uuidObj1),
			AssetID:      sim.AssetID(uuidAssetWell),
			CurrentState: "default",
			EntryPolicy:  sim.EntryPolicyOpen,
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "thirst",
					Amount:             -8,
					MaxQuantity:        &max10,
					AvailableQuantity:  &avail3,
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: &period12,
					LastRefreshAt:      &anchor,
				},
				{
					Attribute:          "tiredness",
					Amount:             -1,
					DwellDelta:         &dwellDelta,
					DwellPeriodMinutes: &dwellPeriod,
				},
			},
		},
	}
	if err := saveSnapshotVO(t, f, repo, withBoth); err != nil {
		t.Fatalf("SaveSnapshot with refreshes: %v", err)
	}

	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	obj := got[sim.VillageObjectID(uuidObj1)]
	if obj == nil {
		t.Fatal("obj1 missing")
	}
	if len(obj.Refreshes) != 2 {
		t.Fatalf("obj1 Refreshes len=%d, want 2", len(obj.Refreshes))
	}
	byAttr := refreshByAttr(obj.Refreshes)

	thirst := byAttr["thirst"]
	if thirst == nil || !thirst.IsFinite() || thirst.Amount != -8 ||
		thirst.RefreshMode != sim.RefreshModeContinuous ||
		thirst.MaxQuantity == nil || *thirst.MaxQuantity != 10 ||
		thirst.AvailableQuantity == nil || *thirst.AvailableQuantity != 3 ||
		thirst.RefreshPeriodHours == nil || *thirst.RefreshPeriodHours != 12 {
		t.Errorf("thirst refresh round-trip mismatch: %+v", thirst)
	}
	if thirst.LastRefreshAt == nil || !thirst.LastRefreshAt.Equal(anchor) {
		t.Errorf("thirst LastRefreshAt = %v, want %v", thirst.LastRefreshAt, anchor)
	}

	tiredness := byAttr["tiredness"]
	if tiredness == nil || tiredness.IsFinite() || !tiredness.HasDwell() ||
		tiredness.Amount != -1 ||
		tiredness.DwellDelta == nil || *tiredness.DwellDelta != -1 ||
		tiredness.DwellPeriodMinutes == nil || *tiredness.DwellPeriodMinutes != 15 {
		t.Errorf("tiredness refresh round-trip mismatch: %+v", tiredness)
	}
	// Conformance: infinite rows carry prod's NOT NULL DEFAULT 'continuous'
	// mode even though IsFinite() is false.
	if tiredness.RefreshMode != sim.RefreshModeContinuous {
		t.Errorf("tiredness RefreshMode = %q, want continuous (NOT NULL default)", tiredness.RefreshMode)
	}

	// Drop the tiredness refresh from the snapshot; its row should be pruned
	// by the object_refresh gen-marker delete-stale step.
	obj.Refreshes = []*sim.ObjectRefresh{byAttr["thirst"]}
	resaved := map[sim.VillageObjectID]*sim.VillageObject{sim.VillageObjectID(uuidObj1): obj}
	if err := saveSnapshotVO(t, f, repo, resaved); err != nil {
		t.Fatalf("SaveSnapshot after drop: %v", err)
	}

	var refreshCount int
	if err := f.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM object_refresh WHERE object_id = $1`, uuidObj1).Scan(&refreshCount); err != nil {
		t.Fatalf("count object_refresh: %v", err)
	}
	if refreshCount != 1 {
		t.Fatalf("after dropping tiredness, object_refresh count=%d, want 1", refreshCount)
	}
}

// SaveSnapshot of an overlay (attached_to set) alongside its parent — both
// in one checkpoint Tx. The attached_to self-FK is DEFERRABLE INITIALLY
// DEFERRED (ZBBS-WORK-237) and SaveSnapshot orders roots before overlays,
// so this round-trips regardless of map iteration order. Guards the
// previously-latent ordering hazard.
func TestIntegration_VillageObjects_OverlayRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := voRepo(f)

	objects := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {
			ID:           sim.VillageObjectID(uuidObj1),
			AssetID:      sim.AssetID(uuidAssetWell),
			CurrentState: "default",
			EntryPolicy:  sim.EntryPolicyClosed,
		},
		sim.VillageObjectID(uuidObj2): {
			ID:           sim.VillageObjectID(uuidObj2),
			AssetID:      sim.AssetID(uuidAssetLamp),
			CurrentState: "lit",
			EntryPolicy:  sim.EntryPolicyClosed,
			AttachedTo:   sim.VillageObjectID(uuidObj1),
		},
	}
	if err := saveSnapshotVO(t, f, repo, objects); err != nil {
		t.Fatalf("SaveSnapshot parent+overlay: %v", err)
	}

	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	overlay := got[sim.VillageObjectID(uuidObj2)]
	if overlay == nil {
		t.Fatal("overlay missing after round-trip")
	}
	if overlay.AttachedTo != sim.VillageObjectID(uuidObj1) {
		t.Errorf("overlay.AttachedTo = %q, want %q", overlay.AttachedTo, uuidObj1)
	}
}

// Yield-only (forage-to-sell) refresh round-trip (LLM-24) — a gather row with
// amount=0 and gather_item set persists through the relaxed
// object_refresh_amount_negative CHECK and threads back via LoadAll with its
// finite supply intact. Proves the DB admits the decoupled row and
// IsYieldOnly() reads it back correctly (a unit test can't catch a DB CHECK).
func TestIntegration_VillageObjects_YieldOnlyRefreshRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := voRepo(f)

	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO refresh_attribute (name, display_label) VALUES ('hunger', 'Hunger')`); err != nil {
		t.Fatalf("seed refresh_attribute: %v", err)
	}

	max5, avail5, period6 := 5, 5, 6
	anchor := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(uuidObj1): {
			ID:           sim.VillageObjectID(uuidObj1),
			AssetID:      sim.AssetID(uuidAssetWell),
			CurrentState: "default",
			EntryPolicy:  sim.EntryPolicyOpen,
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             0, // yield-only — forage-to-sell, no consume-in-place need
					MaxQuantity:        &max5,
					AvailableQuantity:  &avail5,
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: &period6,
					LastRefreshAt:      &anchor,
					GatherItem:         "berries",
				},
			},
		},
	}
	if err := saveSnapshotVO(t, f, repo, objects); err != nil {
		t.Fatalf("SaveSnapshot yield-only refresh: %v", err)
	}

	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	obj := got[sim.VillageObjectID(uuidObj1)]
	if obj == nil || len(obj.Refreshes) != 1 {
		t.Fatalf("obj1 refreshes = %+v, want 1 row", obj)
	}
	r := obj.Refreshes[0]
	if r.Amount != 0 || r.GatherItem != "berries" || !r.IsGatherable() || !r.IsYieldOnly() {
		t.Errorf("yield-only refresh round-trip mismatch: %+v (IsYieldOnly=%v)", r, r.IsYieldOnly())
	}
	if !r.IsFinite() || r.AvailableQuantity == nil || *r.AvailableQuantity != 5 {
		t.Errorf("yield-only supply round-trip mismatch: %+v", r)
	}
}

// The relaxed amount CHECK still guards misconfiguration (LLM-24): a
// zero-amount row is admitted only when gather_item is set; amount=0 with a
// NULL gather_item is a check_violation, and amount>0 stays rejected.
func TestIntegration_VillageObjects_YieldOnlyAmountCheck(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO refresh_attribute (name, display_label)
		VALUES ('hunger', 'Hunger'), ('thirst', 'Thirst'), ('tiredness', 'Tiredness')`); err != nil {
		t.Fatalf("seed refresh_attribute: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, entry_policy, tags)
		VALUES ($1, $2, 'default', 0, 0, '', 'open', '{}')`, uuidObj1, uuidAssetWell); err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	// amount=0 + gather_item set → accepted (the forage-to-sell row).
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO object_refresh (object_id, attribute, amount, gather_item)
		VALUES ($1, 'hunger', 0, 'berries')`, uuidObj1); err != nil {
		t.Fatalf("amount=0 + gather_item should be accepted: %v", err)
	}

	// amount=0 + NULL gather_item → check_violation (a needless, yield-less row).
	_, err := f.Pool.Exec(ctx, `
		INSERT INTO object_refresh (object_id, attribute, amount)
		VALUES ($1, 'thirst', 0)`, uuidObj1)
	if got := pgErrCode(err); got != "23514" {
		t.Errorf("amount=0 + NULL gather_item: got err=%v (code %q), want check_violation 23514", err, got)
	}

	// amount=0 + empty/whitespace gather_item → check_violation: the CHECK
	// mirrors Go's trim-aware IsGatherable(), so a blank gather_item is not a
	// legal yield-only row even though it is non-NULL.
	for _, blank := range []string{"", "   "} {
		_, err := f.Pool.Exec(ctx, `
			INSERT INTO object_refresh (object_id, attribute, amount, gather_item)
			VALUES ($1, 'thirst', 0, $2)`, uuidObj1, blank)
		if got := pgErrCode(err); got != "23514" {
			t.Errorf("amount=0 + gather_item=%q: got err=%v (code %q), want check_violation 23514", blank, err, got)
		}
	}

	// amount>0 → still rejected (would RAISE a need), gather_item or not.
	_, err = f.Pool.Exec(ctx, `
		INSERT INTO object_refresh (object_id, attribute, amount, gather_item)
		VALUES ($1, 'tiredness', 5, 'berries')`, uuidObj1)
	if got := pgErrCode(err); got != "23514" {
		t.Errorf("amount>0: got err=%v (code %q), want check_violation 23514", err, got)
	}
}

// LoadAll refuses to start with a precise reason when a required-but-
// nullable column is NULL (legacy/external data). Validates the hardened
// error path; the data fixup is handled out-of-band (home mail 2026-05-20).
func TestIntegration_VillageObjects_LoadAllNullRequiredColumn(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// Valid asset_id + display_name, but placed_by left NULL.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO village_object (id, asset_id, x, y, display_name, tags)
		VALUES ($1, $2, 0, 0, 'Orphan', '{}')`,
		uuidObj1, uuidAssetWell); err != nil {
		t.Fatalf("seed NULL-placed_by row: %v", err)
	}

	_, err := voRepo(f).LoadAll(ctx)
	if err == nil {
		t.Fatal("expected LoadAll to refuse a NULL required column, got nil")
	}
	if !strings.Contains(err.Error(), "placed_by") || !strings.Contains(err.Error(), uuidObj1) {
		t.Errorf("error should name the row id and column, got: %v", err)
	}
}
