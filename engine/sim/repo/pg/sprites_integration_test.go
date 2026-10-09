package pg

// Real-pg integration tests for the read-side SpritesRepo (ZBBS-WORK-256).
// Run against an embedded Postgres with the full prod-baseline schema
// applied; skipped under `go test -short`.
//
// SpritesRepo is a read-only multi-table assembly (tileset_pack + npc_sprite
// + npc_sprite_animation) — no SaveSnapshot, no gen-marker, no Tx. The
// substrate facts worth exercising against real pg: the uuid keying, the
// Pack pointer attach, deterministic animation ordering, and nullable pack_id
// round-trip. Dangling-ref tolerance is NOT tested: npc_sprite.pack_id FKs
// tileset_pack and npc_sprite_animation.sprite_id FKs npc_sprite ON DELETE
// CASCADE, so neither a dangling pack ref nor an orphan animation is
// reachable in valid schema (the skip-and-log guards in the repo are
// defensive-only against schema drift).

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

const (
	spriteUUIDFull  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	spriteUUIDPlain = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// S1 happy path — a fully-populated sprite assembles its whole graph: the
// Pack pointer, its scalar fields, and both animation rows in deterministic
// (sprite_id, direction, animation) order.
func TestIntegration_Sprites_LoadAllHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO tileset_pack (id, name, url) VALUES ('mana-seed', 'Mana Seed', 'http://example/mana-seed.png')`); err != nil {
		t.Fatalf("seed tileset_pack: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale, anchor_y)
		VALUES ($1, 'Woman A v00', 'npc/woman_A_v00.png', 64, 64, 'mana-seed', '["waterfowl"]', 1.0, 0.71875)`, spriteUUIDFull); err != nil {
		t.Fatalf("seed npc_sprite: %v", err)
	}
	// Insert south/walk before south/idle to prove the ORDER BY re-sorts
	// (idle < walk lexically) rather than preserving insert order.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate)
		VALUES ($1, 'south', 'walk', 1, 4, 8.0),
		       ($1, 'south', 'idle', 0, 1, 6.0)`, spriteUUIDFull); err != nil {
		t.Fatalf("seed npc_sprite_animation: %v", err)
	}

	got, err := NewSpritesRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	s := got[sim.SpriteID(spriteUUIDFull)]
	if s == nil {
		t.Fatalf("sprite %s missing", spriteUUIDFull)
	}

	if s.ID != sim.SpriteID(spriteUUIDFull) {
		t.Errorf("ID = %q, want the uuid", s.ID)
	}
	if s.Name != "Woman A v00" || s.Sheet != "npc/woman_A_v00.png" {
		t.Errorf("scalar string fields: %+v", s)
	}
	if s.FrameWidth != 64 || s.FrameHeight != 64 {
		t.Errorf("frame dims = %dx%d, want 64x64", s.FrameWidth, s.FrameHeight)
	}
	if s.PackID == nil || *s.PackID != "mana-seed" {
		t.Errorf("pack_id: %v", s.PackID)
	}
	if s.Pack == nil {
		t.Fatal("Pack pointer should be set")
	}
	if s.Pack.Name != "Mana Seed" || s.Pack.URL == nil || *s.Pack.URL != "http://example/mana-seed.png" {
		t.Errorf("pack fields: %+v", s.Pack)
	}
	// LLM-579/580: the behavior slugs and render scale round-trip.
	if len(s.Behaviors) != 1 || s.Behaviors[0] != sim.BehaviorWaterfowl {
		t.Errorf("Behaviors = %v, want [waterfowl]", s.Behaviors)
	}
	if s.RenderScale != 1.0 {
		t.Errorf("RenderScale = %v, want 1.0", s.RenderScale)
	}
	if s.AnchorY != 0.71875 {
		t.Errorf("AnchorY = %v, want 0.71875", s.AnchorY)
	}

	// Deterministic order: (south, idle) before (south, walk).
	if len(s.Animations) != 2 {
		t.Fatalf("animations len=%d want 2: %+v", len(s.Animations), s.Animations)
	}
	idle := s.Animations[0]
	if idle.Direction != "south" || idle.Animation != "idle" || idle.RowIndex != 0 || idle.FrameCount != 1 || idle.FrameRate != 6.0 {
		t.Errorf("idle animation: %+v", idle)
	}
	walk := s.Animations[1]
	if walk.Direction != "south" || walk.Animation != "walk" || walk.RowIndex != 1 || walk.FrameCount != 4 || walk.FrameRate != 8.0 {
		t.Errorf("walk animation: %+v", walk)
	}
}

// S2 nullable / minimal sprite — pack_id NULL comes back as nil PackID/Pack;
// a sprite with no animation rows has a non-nil empty Animations slice.
func TestIntegration_Sprites_NullablesAndNoPack(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO npc_sprite (id, name, sheet) VALUES ($1, 'Old Man B v02', 'npc/old_man_B_v02.png')`,
		spriteUUIDPlain); err != nil {
		t.Fatalf("seed npc_sprite: %v", err)
	}

	got, err := NewSpritesRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	s := got[sim.SpriteID(spriteUUIDPlain)]
	if s == nil {
		t.Fatalf("sprite %s missing", spriteUUIDPlain)
	}
	if s.PackID != nil || s.Pack != nil {
		t.Errorf("expected no pack, got PackID=%v Pack=%v", s.PackID, s.Pack)
	}
	// frame_width/height have schema defaults of 32.
	if s.FrameWidth != 32 || s.FrameHeight != 32 {
		t.Errorf("frame dims = %dx%d, want schema-default 32x32", s.FrameWidth, s.FrameHeight)
	}
	if s.Animations == nil || len(s.Animations) != 0 {
		t.Errorf("no-animation sprite should have empty (non-nil) Animations, got %#v", s.Animations)
	}
	// Schema defaults for the LLM-579/580 columns: behaviors '[]' (empty,
	// non-nil after unmarshal), render_scale 2.0.
	if s.Behaviors == nil || len(s.Behaviors) != 0 {
		t.Errorf("default behaviors should be empty non-nil, got %#v", s.Behaviors)
	}
	if s.RenderScale != 2.0 {
		t.Errorf("default RenderScale = %v, want 2.0", s.RenderScale)
	}
	// LLM-742: a sprite without a measured feet line stands at the villager 0.9.
	if s.AnchorY != 0.9 {
		t.Errorf("default AnchorY = %v, want 0.9", s.AnchorY)
	}
	// LLM-691: a one-sheet sprite has no rig, and its default '[]' layers
	// stay off the model so they stay off the payload.
	if s.Rig != "" || s.Layers != nil {
		t.Errorf("one-sheet sprite: Rig=%q Layers=%s, want empty", s.Rig, s.Layers)
	}
}

// S3 rig sprite (LLM-691) — rig and the layers array round-trip, layers
// verbatim as JSON; the CHECKs reject an unknown rig and a non-array layers.
func TestIntegration_Sprites_RigLayers(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	const layers = `[{"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 2}}, {"sheet": "/s.png", "behind": true}]`
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO npc_sprite (id, name, sheet, frame_width, frame_height, rig, layers)
		VALUES ($1, 'Farmer', '/b.png', 64, 64, 'farmer_base', $2)`, spriteUUIDFull, layers); err != nil {
		t.Fatalf("seed rig sprite: %v", err)
	}

	got, err := NewSpritesRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	s := got[sim.SpriteID(spriteUUIDFull)]
	if s == nil {
		t.Fatalf("sprite %s missing", spriteUUIDFull)
	}
	if s.Rig != "farmer_base" {
		t.Errorf("Rig = %q, want farmer_base", s.Rig)
	}
	var gotLayers, wantLayers any
	if err := json.Unmarshal(s.Layers, &gotLayers); err != nil {
		t.Fatalf("Layers not JSON: %v (%s)", err, s.Layers)
	}
	_ = json.Unmarshal([]byte(layers), &wantLayers)
	if !reflect.DeepEqual(gotLayers, wantLayers) {
		t.Errorf("Layers = %s, want %s", s.Layers, layers)
	}

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO npc_sprite (id, name, sheet, rig) VALUES ($1, 'Bad rig', '/x.png', 'paper')`, spriteUUIDPlain); err == nil {
		t.Error("unknown rig accepted, want npc_sprite_rig_check to reject it")
	}
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO npc_sprite (id, name, sheet, rig, layers) VALUES ($1, 'Bad layers', '/x.png', 'farmer_base', '{}')`, spriteUUIDPlain); err == nil {
		t.Error("object layers accepted, want npc_sprite_layers_check to reject it")
	}
}

// S4 a player's outfit (LLM-691; the fixture's migrations seed the farmer pack) — UpsertRigSprite inserts the rig sprite,
// a second save of the same id replaces its layers in place, and LoadAll
// reads it back as a rig sprite.
func TestIntegration_Sprites_UpsertRigSprite(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	repo := NewSpritesRepo(f.Pool)
	first, err := sim.NewOutfitSprite("pc-1", "Tess", json.RawMessage(`[{"sheet":"/b.png","ramps":{"skin":1}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertRigSprite(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	afterFirst, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	second, _ := sim.NewOutfitSprite("pc-1", "Tess Renamed", json.RawMessage(`[{"sheet":"/b.png","ramps":{"skin":7}}]`))
	if err := repo.UpsertRigSprite(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != len(afterFirst) {
		t.Fatalf("rows = %d after the re-save, want %d (updated in place)", len(got), len(afterFirst))
	}
	s := got[sim.OutfitSpriteID("pc-1")]
	if s == nil {
		t.Fatalf("outfit sprite missing: %v", got)
	}
	var layers []map[string]any
	if err := json.Unmarshal(s.Layers, &layers); err != nil || len(layers) != 1 {
		t.Fatalf("layers = %s", s.Layers)
	}
	if s.Rig != "farmer_base" || s.Name != "Tess Renamed" || s.FrameWidth != 64 || s.Pack == nil ||
		layers[0]["ramps"].(map[string]any)["skin"] != float64(7) {
		t.Errorf("sprite = %+v layers=%s", s, s.Layers)
	}
}

// S5 feet line (LLM-742) — the migration gives every livestock sprite the feet
// line measured off its sheet (the fixture replays the seeding migrations, so
// this also pins the sprite ids the UPDATEs name), and the CHECK keeps the
// value a fraction of the frame.
func TestIntegration_Sprites_LivestockAnchorY(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	got, err := NewSpritesRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	const cattle, chicken, sheep = 0.71875, 0.703125, 0.65625
	want := map[sim.SpriteID]float64{
		"639d0c01-0000-4000-8000-000000000001": cattle,  // Cow (white)
		"639d0c02-0000-4000-8000-000000000002": cattle,  // Cow (golden)
		"639d0c03-0000-4000-8000-000000000003": cattle,  // Cow (brown)
		"639d0c04-0000-4000-8000-000000000004": cattle,  // Cow (grey)
		"639d0c05-0000-4000-8000-000000000005": cattle,  // Bull (white)
		"639d0c06-0000-4000-8000-000000000006": cattle,  // Bull (brown)
		"639d0c07-0000-4000-8000-000000000007": cattle,  // Heifer (golden)
		"639d0c08-0000-4000-8000-000000000008": cattle,  // Heifer (russet)
		"641c0001-0000-4000-8000-000000000001": chicken, // Hen (golden)
		"641c0002-0000-4000-8000-000000000002": chicken, // Hen (rust)
		"641c0003-0000-4000-8000-000000000003": chicken, // Hen (cream)
		"641c0004-0000-4000-8000-000000000004": chicken, // Rooster (golden)
		"641c0005-0000-4000-8000-000000000005": chicken, // Rooster (slate)
		"641c0006-0000-4000-8000-000000000006": chicken, // Rooster (speckled)
		"689d0c01-0000-4000-8000-000000000001": sheep,   // Sheep
	}
	for id, a := range want {
		s := got[id]
		if s == nil {
			t.Errorf("sprite %s missing from the seeded catalog", id)
			continue
		}
		if s.AnchorY != a {
			t.Errorf("%s (%s) AnchorY = %v, want %v", s.Name, id, s.AnchorY, a)
		}
	}
	// Every grazer in the catalog is one of the measured sprites — a new
	// livestock sheet needs its own feet line.
	for id, s := range got {
		if _, ok := want[id]; !ok && s.HasBehavior(sim.BehaviorGrazer) {
			t.Errorf("grazer sprite %s (%s) has no measured feet line", s.Name, id)
		}
	}

	for _, bad := range []string{"0", "1.5", "-0.2"} {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO npc_sprite (id, name, sheet, anchor_y) VALUES ($1, 'Bad anchor', '/x.png', `+bad+`)`, spriteUUIDPlain); err == nil {
			t.Errorf("anchor_y %s accepted, want npc_sprite_anchor_y_check to reject it", bad)
		}
	}
}
