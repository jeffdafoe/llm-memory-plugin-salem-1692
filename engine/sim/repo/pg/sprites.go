package pg

import (
	"context"
	"fmt"
	"log"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// SpritesRepo loads the character-sprite catalog — npc_sprite rows plus
// their tileset packs and the per-(direction, animation) animation rows —
// flattened into sim.Sprite aggregates keyed by the npc_sprite.id UUID.
// Reference state: read-only, no checkpoint path. Admin edits write directly
// to the underlying tables through the editor port; the world rebuilds the
// map wholesale via LoadAll at startup (and on SIGHUP when that lands).
//
// Port of v1's loadNPCSprites catalog assembly (engine/npcs.go). Parallel to
// AssetsRepo but a distinct catalog — character sprites use a row-indexed
// directional animation model that the asset src-rect model can't represent.
type SpritesRepo struct {
	pool Pool
}

// NewSpritesRepo constructs a SpritesRepo against the given pool. Normal
// wiring path is pg.NewRepository.
func NewSpritesRepo(pool Pool) *SpritesRepo {
	return &SpritesRepo{pool: pool}
}

// loadAllSpritePacksSQL reads the tileset packs that npc_sprite.pack_id
// points at. Same source table as the asset catalog's packs; pack_group /
// pack_source are not modeled on sim.TilesetPack.
const loadAllSpritePacksSQL = `
SELECT id, name, url
  FROM tileset_pack
 ORDER BY name`

// loadAllSpritesSQL reads the sprite rows. pack_id is nullable (varchar, no
// FK) so it scans into a *string; a dangling pack ref is tolerated (see
// LoadAll's orphan note).
const loadAllSpritesSQL = `
SELECT id::text, name, sheet, frame_width, frame_height, pack_id
  FROM npc_sprite
 ORDER BY name`

// loadAllSpriteAnimationsSQL pulls every animation row in one pass. The
// ORDER BY makes each sprite's aggregated Animations slice deterministic
// across reloads (the query plan would otherwise be free to reorder), so
// anything that serializes / compares the slice stays stable.
const loadAllSpriteAnimationsSQL = `
SELECT sprite_id::text, direction, animation, row_index, frame_count, frame_rate
  FROM npc_sprite_animation
 ORDER BY sprite_id, direction, animation`

// LoadAll assembles the sprite catalog: load packs, load the sprite rows
// (attaching the Pack pointer), then attach animation rows to their parent.
// Three queries, no N+1.
//
// Runs against the pool directly (no Tx) — read-only restart path, same
// posture as the other read-side repos.
//
// Orphan handling differs from the asset catalog: npc_sprite has tighter
// referential integrity. Both npc_sprite.pack_id (FK to tileset_pack) and
// npc_sprite_animation.sprite_id (FK to npc_sprite ON DELETE CASCADE) are
// enforced, so a dangling pack ref or orphan animation row is unreachable in
// valid schema. The skip-and-log guards below are therefore defensive only
// (schema-drift insurance, matching the asset repo's shape) rather than
// handling a reachable prod case. The sprite map keeps a loud duplicate-key
// guard (id is the PK).
func (r *SpritesRepo) LoadAll(ctx context.Context) (map[sim.SpriteID]*sim.Sprite, error) {
	packs, err := r.loadPacks(ctx)
	if err != nil {
		return nil, err
	}
	sprites, err := r.loadSprites(ctx, packs)
	if err != nil {
		return nil, err
	}
	if err := r.attachAnimations(ctx, sprites); err != nil {
		return nil, err
	}
	return sprites, nil
}

// loadPacks reads tileset_pack into TilesetPack values keyed by id.
func (r *SpritesRepo) loadPacks(ctx context.Context) (map[string]*sim.TilesetPack, error) {
	rows, err := r.pool.Query(ctx, loadAllSpritePacksSQL)
	if err != nil {
		return nil, fmt.Errorf("pg sprites LoadAll: tileset_pack query: %w", err)
	}
	defer rows.Close()

	packs := make(map[string]*sim.TilesetPack)
	for rows.Next() {
		var p sim.TilesetPack
		if err := rows.Scan(&p.ID, &p.Name, &p.URL); err != nil {
			return nil, fmt.Errorf("pg sprites LoadAll: tileset_pack scan: %w", err)
		}
		if _, exists := packs[p.ID]; exists {
			return nil, fmt.Errorf("pg sprites LoadAll: duplicate tileset_pack %q", p.ID)
		}
		packs[p.ID] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg sprites LoadAll: tileset_pack iter: %w", err)
	}
	return packs, nil
}

// loadSprites reads the npc_sprite rows into the keyed map, attaching the
// Pack pointer when pack_id resolves. The nullable pack_id scans straight
// into the struct's *string field.
func (r *SpritesRepo) loadSprites(ctx context.Context, packs map[string]*sim.TilesetPack) (map[sim.SpriteID]*sim.Sprite, error) {
	rows, err := r.pool.Query(ctx, loadAllSpritesSQL)
	if err != nil {
		return nil, fmt.Errorf("pg sprites LoadAll: npc_sprite query: %w", err)
	}
	defer rows.Close()

	sprites := make(map[sim.SpriteID]*sim.Sprite)
	for rows.Next() {
		var (
			id string
			s  sim.Sprite
		)
		if err := rows.Scan(&id, &s.Name, &s.Sheet, &s.FrameWidth, &s.FrameHeight, &s.PackID); err != nil {
			return nil, fmt.Errorf("pg sprites LoadAll: npc_sprite scan: %w", err)
		}
		s.ID = sim.SpriteID(id)
		s.Animations = []sim.SpriteAnimation{}
		if s.PackID != nil {
			if pack, ok := packs[*s.PackID]; ok {
				s.Pack = pack
			} else {
				// No FK on npc_sprite.pack_id; a dangling ref is reachable.
				// Leave Pack nil (the in-memory model tolerates it) and keep
				// PackID as stored, but log the drift.
				log.Printf("pg sprites LoadAll: npc_sprite %s references unknown tileset_pack %q — leaving Pack unset", id, *s.PackID)
			}
		}
		// id is the npc_sprite PK so a duplicate is unreachable in valid
		// data; guard loudly against schema drift rather than letting a
		// later row silently win.
		if _, exists := sprites[s.ID]; exists {
			return nil, fmt.Errorf("pg sprites LoadAll: duplicate npc_sprite %q", id)
		}
		sprites[s.ID] = &s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg sprites LoadAll: npc_sprite iter: %w", err)
	}
	return sprites, nil
}

// attachAnimations reads npc_sprite_animation and appends each row to its
// parent sprite. Orphan rows (sprite_id not in the map) are skipped with a
// warning — see LoadAll's orphan note.
func (r *SpritesRepo) attachAnimations(ctx context.Context, sprites map[sim.SpriteID]*sim.Sprite) error {
	rows, err := r.pool.Query(ctx, loadAllSpriteAnimationsSQL)
	if err != nil {
		return fmt.Errorf("pg sprites LoadAll: npc_sprite_animation query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			spriteID string
			a        sim.SpriteAnimation
		)
		if err := rows.Scan(&spriteID, &a.Direction, &a.Animation, &a.RowIndex, &a.FrameCount, &a.FrameRate); err != nil {
			return fmt.Errorf("pg sprites LoadAll: npc_sprite_animation scan: %w", err)
		}
		s, ok := sprites[sim.SpriteID(spriteID)]
		if !ok {
			log.Printf("pg sprites LoadAll: npc_sprite_animation (%s, %s) references unknown npc_sprite %s — skipping", a.Direction, a.Animation, spriteID)
			continue
		}
		s.Animations = append(s.Animations, a)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg sprites LoadAll: npc_sprite_animation iter: %w", err)
	}
	return nil
}
