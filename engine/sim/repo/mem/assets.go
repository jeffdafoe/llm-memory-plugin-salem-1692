package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// AssetsRepo is an in-memory implementation of sim.AssetsRepo. Reference
// state — no checkpoint save. Tests Seed the catalog; production loads
// from the asset / asset_state / asset_state_tag / asset_state_light /
// asset_slot / tileset_pack tables (pg impl ports later).
type AssetsRepo struct {
	assets map[sim.AssetID]*sim.Asset
}

func NewAssetsRepo() *AssetsRepo {
	return &AssetsRepo{assets: make(map[sim.AssetID]*sim.Asset)}
}

// Seed inserts assets directly. Tests use this to populate the catalog
// before LoadWorld.
func (r *AssetsRepo) Seed(assets map[sim.AssetID]*sim.Asset) {
	for id, a := range assets {
		r.assets[id] = a
	}
}

func (r *AssetsRepo) LoadAll(_ context.Context) (map[sim.AssetID]*sim.Asset, error) {
	out := make(map[sim.AssetID]*sim.Asset, len(r.assets))
	for id, a := range r.assets {
		out[id] = a
	}
	return out, nil
}
