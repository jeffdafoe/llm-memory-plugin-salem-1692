package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// SpritesRepo is an in-memory implementation of sim.SpritesRepo. Reference
// state — no checkpoint save. Tests Seed the catalog; production loads from
// the npc_sprite / npc_sprite_animation / tileset_pack tables (pg impl).
type SpritesRepo struct {
	sprites map[sim.SpriteID]*sim.Sprite
}

func NewSpritesRepo() *SpritesRepo {
	return &SpritesRepo{sprites: make(map[sim.SpriteID]*sim.Sprite)}
}

// Seed inserts sprites directly. Tests use this to populate the catalog
// before LoadWorld.
func (r *SpritesRepo) Seed(sprites map[sim.SpriteID]*sim.Sprite) {
	for id, s := range sprites {
		r.sprites[id] = s
	}
}

func (r *SpritesRepo) LoadAll(_ context.Context) (map[sim.SpriteID]*sim.Sprite, error) {
	out := make(map[sim.SpriteID]*sim.Sprite, len(r.sprites))
	for id, s := range r.sprites {
		out[id] = s
	}
	return out, nil
}
