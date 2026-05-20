package pg

// Real-pg integration tests for the read-side AssetsRepo (ZBBS-WORK-247).
// Run against an embedded Postgres with the full prod-baseline schema
// applied; skipped under `go test -short`.
//
// AssetsRepo is a read-only multi-table assembly (tileset_pack + asset +
// asset_state + asset_state_light + asset_state_tag + asset_slot) — no
// SaveSnapshot, no gen-marker, no Tx. The substrate facts worth exercising
// against real pg: the uuid keying, the Pack pointer dedup, the
// LEFT JOIN light discriminant (lit states only), the array_agg tag set,
// nullable column round-trip (pack_id / fits_slot / offsets), and the
// no-FK orphan/dangling-ref tolerance (skip + log, no startup failure).

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

const (
	assetUUIDFull  = "11111111-1111-1111-1111-111111111111"
	assetUUIDPlain = "22222222-2222-2222-2222-222222222222"
	assetUUIDGhost = "33333333-3333-3333-3333-333333333333"
	assetUUIDOrphn = "99999999-9999-9999-9999-999999999999"
)

// A1 happy path — a fully-populated asset assembles its whole graph:
// Pack pointer, both states in AssetStateID order, the lit state's Light
// + tags, the unlit state's empty tag slice, the slot, and all the
// scalar fields (including the v2-only rotation/spread and the occupancy
// columns).
func TestIntegration_Assets_LoadAllHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO tileset_pack (id, name, url) VALUES ('pack-a', 'Pack A', 'http://example/pack-a.png')`); err != nil {
		t.Fatalf("seed tileset_pack: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset (
			id, name, category, default_state, anchor_x, anchor_y, layer,
			pack_id, fits_slot, z_index, is_obstacle, is_passage,
			footprint_left, footprint_right, footprint_top, footprint_bottom,
			door_offset_x, door_offset_y, visible_when_inside,
			stand_offset_x, stand_offset_y,
			rotation_algo, transition_spread_seconds,
			occupied_min_count, occupied_night_only)
		VALUES (
			$1, 'Tavern', 'structure', 'default', 0.5, 0.85, 'objects',
			'pack-a', 'top', -1, true, false,
			1, 2, 3, 4,
			5, 6, true,
			7, 8,
			'deterministic', 9,
			2, true)`, assetUUIDFull); err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	// Two states; insert 'default' first so it gets the lower SERIAL id
	// (states come back ordered by id). 'lit' carries a light row + tags.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
		VALUES ($1, 'default', 'tavern.png', 0, 0, 32, 48, 1, 0)`, assetUUIDFull); err != nil {
		t.Fatalf("seed asset_state default: %v", err)
	}
	var litStateID int
	if err := f.Pool.QueryRow(ctx, `
		INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
		VALUES ($1, 'lit', 'tavern.png', 32, 0, 32, 48, 4, 8)
		RETURNING id`, assetUUIDFull).Scan(&litStateID); err != nil {
		t.Fatalf("seed asset_state lit: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_state_light (state_id, color, radius, energy, offset_x, offset_y, flicker_amplitude, flicker_period_ms)
		VALUES ($1, '#ffcc00', 120, 1.5, 1, 2, 0.3, 800)`, litStateID); err != nil {
		t.Fatalf("seed asset_state_light: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_state_tag (state_id, tag) VALUES ($1, 'rotatable'), ($1, 'day-active')`, litStateID); err != nil {
		t.Fatalf("seed asset_state_tag: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
		VALUES ($1, 'top', 5, 6)`, assetUUIDFull); err != nil {
		t.Fatalf("seed asset_slot: %v", err)
	}

	got, err := NewAssetsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	a := got[sim.AssetID(assetUUIDFull)]
	if a == nil {
		t.Fatalf("asset %s missing (keys: %v)", assetUUIDFull, keysOf(got))
	}

	if a.ID != sim.AssetID(assetUUIDFull) {
		t.Errorf("ID = %q, want the uuid", a.ID)
	}
	if a.Name != "Tavern" || a.Category != "structure" || a.Layer != "objects" {
		t.Errorf("scalar string fields: %+v", a)
	}
	if a.ZIndex != -1 || !a.IsObstacle || a.IsPassage {
		t.Errorf("z/obstacle/passage: z=%d obstacle=%v passage=%v", a.ZIndex, a.IsObstacle, a.IsPassage)
	}
	if a.FootprintLeft != 1 || a.FootprintRight != 2 || a.FootprintTop != 3 || a.FootprintBottom != 4 {
		t.Errorf("footprint: %d/%d/%d/%d", a.FootprintLeft, a.FootprintRight, a.FootprintTop, a.FootprintBottom)
	}
	if !a.VisibleWhenInside {
		t.Error("visible_when_inside should be true")
	}
	if a.RotationAlgo != sim.RotationAlgoDeterministic || a.TransitionSpreadSeconds != 9 {
		t.Errorf("rotation/spread: algo=%q spread=%d", a.RotationAlgo, a.TransitionSpreadSeconds)
	}
	if a.OccupiedMinCount != 2 || !a.OccupiedNightOnly {
		t.Errorf("occupancy: min=%d nightOnly=%v", a.OccupiedMinCount, a.OccupiedNightOnly)
	}
	// Nullable pointer fields populated.
	if a.FitsSlot == nil || *a.FitsSlot != "top" {
		t.Errorf("fits_slot: %v", a.FitsSlot)
	}
	if a.DoorOffsetX == nil || *a.DoorOffsetX != 5 || a.DoorOffsetY == nil || *a.DoorOffsetY != 6 {
		t.Errorf("door offsets: %v/%v", a.DoorOffsetX, a.DoorOffsetY)
	}
	if a.StandOffsetX == nil || *a.StandOffsetX != 7 || a.StandOffsetY == nil || *a.StandOffsetY != 8 {
		t.Errorf("stand offsets: %v/%v", a.StandOffsetX, a.StandOffsetY)
	}
	// Pack pointer.
	if a.PackID == nil || *a.PackID != "pack-a" {
		t.Errorf("pack_id: %v", a.PackID)
	}
	if a.Pack == nil {
		t.Fatal("Pack pointer should be set")
	}
	if a.Pack.Name != "Pack A" || a.Pack.URL == nil || *a.Pack.URL != "http://example/pack-a.png" {
		t.Errorf("pack fields: %+v", a.Pack)
	}

	// States in AssetStateID order: default first, lit second.
	if len(a.States) != 2 {
		t.Fatalf("states len=%d want 2: %+v", len(a.States), a.States)
	}
	def := a.States[0]
	if def.State != "default" || def.Light != nil {
		t.Errorf("default state: %+v", def)
	}
	if def.Tags == nil || len(def.Tags) != 0 {
		t.Errorf("default tags should be empty (non-nil), got %#v", def.Tags)
	}
	lit := a.States[1]
	if lit.State != "lit" || lit.FrameCount != 4 || lit.FrameRate != 8 {
		t.Errorf("lit state scalars: %+v", lit)
	}
	if lit.Light == nil {
		t.Fatal("lit state should carry a Light")
	}
	if lit.Light.Color != "#ffcc00" || lit.Light.Radius != 120 || lit.Light.Energy != 1.5 ||
		lit.Light.OffsetX != 1 || lit.Light.OffsetY != 2 ||
		lit.Light.FlickerAmplitude != 0.3 || lit.Light.FlickerPeriodMs != 800 {
		t.Errorf("light params: %+v", lit.Light)
	}
	if len(lit.Tags) != 2 {
		t.Errorf("lit tags len=%d want 2: %v", len(lit.Tags), lit.Tags)
	}
	if !lit.HasTag("rotatable") || !lit.HasTag("day-active") {
		t.Errorf("lit tags missing expected: %v", lit.Tags)
	}

	// Pure in-memory helpers wired off the loaded graph.
	if a.FindState("lit") == nil || a.FindState("missing") != nil {
		t.Error("FindState wiring")
	}
	if pool := a.RotatablePool(); len(pool) != 1 || pool[0].State != "lit" {
		t.Errorf("RotatablePool: %+v", pool)
	}

	// Slots.
	if len(a.Slots) != 1 || a.Slots[0].SlotName != "top" || a.Slots[0].OffsetX != 5 || a.Slots[0].OffsetY != 6 {
		t.Errorf("slots: %+v", a.Slots)
	}
}

// A2 nullable / minimal asset — pack_id, fits_slot, and both offset pairs
// NULL come back as nil pointers; a state with no light and no tags has a
// nil Light and a non-nil empty tag slice.
func TestIntegration_Assets_NullablesAndNoPack(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO asset (id, name, category) VALUES ($1, 'Rock', 'nature')`, assetUUIDPlain); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h)
		VALUES ($1, 'default', 'rock.png', 0, 0, 16, 16)`, assetUUIDPlain); err != nil {
		t.Fatalf("seed asset_state: %v", err)
	}

	got, err := NewAssetsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	a := got[sim.AssetID(assetUUIDPlain)]
	if a == nil {
		t.Fatalf("asset %s missing", assetUUIDPlain)
	}
	if a.PackID != nil || a.Pack != nil {
		t.Errorf("expected no pack, got PackID=%v Pack=%v", a.PackID, a.Pack)
	}
	if a.FitsSlot != nil || a.DoorOffsetX != nil || a.DoorOffsetY != nil ||
		a.StandOffsetX != nil || a.StandOffsetY != nil {
		t.Errorf("expected nil pointer fields: %+v", a)
	}
	if len(a.States) != 1 {
		t.Fatalf("states len=%d want 1", len(a.States))
	}
	if a.States[0].Light != nil {
		t.Errorf("unlit state should have nil Light, got %+v", a.States[0].Light)
	}
	if a.States[0].Tags == nil || len(a.States[0].Tags) != 0 {
		t.Errorf("no-tag state should have empty (non-nil) Tags, got %#v", a.States[0].Tags)
	}
	if len(a.Slots) != 0 {
		t.Errorf("expected no slots, got %+v", a.Slots)
	}
}

// A3 dangling pack ref — asset.pack_id has no FK, so a ref to a missing
// tileset_pack is reachable. LoadAll must not fail: PackID stays as
// stored, Pack stays nil (logged warning).
func TestIntegration_Assets_DanglingPackRefTolerated(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO asset (id, name, category, pack_id) VALUES ($1, 'Ghost', 'prop', 'no-such-pack')`,
		assetUUIDGhost); err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	got, err := NewAssetsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll should tolerate dangling pack ref, got: %v", err)
	}
	a := got[sim.AssetID(assetUUIDGhost)]
	if a == nil {
		t.Fatalf("asset %s missing", assetUUIDGhost)
	}
	if a.PackID == nil || *a.PackID != "no-such-pack" {
		t.Errorf("PackID should stay as stored, got %v", a.PackID)
	}
	if a.Pack != nil {
		t.Errorf("Pack should be nil for a dangling ref, got %+v", a.Pack)
	}
}

// A4 orphan child rows — asset_state / asset_slot reference asset.id by a
// UNIQUE constraint only (no FK, no CASCADE), so a child whose parent was
// deleted is reachable. LoadAll skips orphans without failing and without
// fabricating a phantom asset.
func TestIntegration_Assets_OrphanChildrenSkipped(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// One real asset, plus orphan state + slot pointing at a non-existent
	// asset uuid.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO asset (id, name, category) VALUES ($1, 'Real', 'prop')`, assetUUIDPlain); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h)
		VALUES ($1, 'default', 'real.png', 0, 0, 16, 16)`, assetUUIDPlain); err != nil {
		t.Fatalf("seed real state: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h)
		VALUES ($1, 'orphan', 'ghost.png', 0, 0, 16, 16)`, assetUUIDOrphn); err != nil {
		t.Fatalf("seed orphan state: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
		VALUES ($1, 'ghost-slot', 0, 0)`, assetUUIDOrphn); err != nil {
		t.Fatalf("seed orphan slot: %v", err)
	}

	got, err := NewAssetsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll should skip orphans, got: %v", err)
	}
	if _, phantom := got[sim.AssetID(assetUUIDOrphn)]; phantom {
		t.Error("orphan rows must not fabricate a phantom asset")
	}
	real := got[sim.AssetID(assetUUIDPlain)]
	if real == nil {
		t.Fatalf("real asset missing")
	}
	if len(real.States) != 1 || real.States[0].State != "default" {
		t.Errorf("real asset states should be unaffected by orphans: %+v", real.States)
	}
}

// keysOf is a small debug helper for failure messages.
func keysOf(m map[sim.AssetID]*sim.Asset) []sim.AssetID {
	out := make([]sim.AssetID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
