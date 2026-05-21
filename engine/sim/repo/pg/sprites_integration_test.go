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
		INSERT INTO npc_sprite (id, name, sheet, frame_width, frame_height, pack_id)
		VALUES ($1, 'Woman A v00', 'npc/woman_A_v00.png', 64, 64, 'mana-seed')`, spriteUUIDFull); err != nil {
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
}
