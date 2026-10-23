package sim

import (
	"encoding/json"
	"os"
	"testing"
)

// fallenTreesArt is the part of testdata/fallen-trees-snapped.json (the tools/
// fallen-trees output in llm-memory-village-tiles) the engine depends on.
type fallenTreesArt struct {
	Tops []struct {
		Species     string `json:"species"`
		Pose        string `json:"pose"`
		StumpTileDX int    `json:"stump_tile_dx"`
		StumpTileDY int    `json:"stump_tile_dy"`
		Frames      []struct {
			State        string  `json:"state"`
			StumpState   string  `json:"stump_state"`
			StumpWorldDX float64 `json:"stump_world_dx"`
			StumpWorldDY float64 `json:"stump_world_dy"`
		} `json:"frames"`
	} `json:"tops"`
}

// TestRoadVariantsMatchTheArt (LLM-789) — every snapped variant's stump state
// and offset is the art's, and every top/state in the art has its variant. The
// offsets are copied by hand from the tool's JSON; this keeps the copy honest
// (two offsets can share a stump tile, so the placement tests alone would not
// see a stump drawn apart from its butt).
func TestRoadVariantsMatchTheArt(t *testing.T) {
	raw, err := os.ReadFile("testdata/fallen-trees-snapped.json")
	if err != nil {
		t.Fatal(err)
	}
	var art fallenTreesArt
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatal(err)
	}
	assetOf := map[[2]string]AssetID{
		{"maple", "up"}:      FallenMapleAssetID,
		{"chestnut", "up"}:   FallenChestnutAssetID,
		{"birch", "up"}:      FallenBirchAssetID,
		{"maple", "down"}:    FallenMapleSouthAssetID,
		{"chestnut", "down"}: FallenChestnutSouthAssetID,
		{"birch", "down"}:    FallenBirchSouthAssetID,
	}
	seen := map[roadObstacleVariant]bool{}
	for _, top := range art.Tops {
		asset, ok := assetOf[[2]string{top.Species, top.Pose}]
		if !ok {
			t.Errorf("art has %s %s, which no asset id names", top.Species, top.Pose)
			continue
		}
		for _, fr := range top.Frames {
			var found *roadObstacleVariant
			for i := range roadObstacleVariants {
				if v := &roadObstacleVariants[i]; v.Asset == asset && v.State == fr.State {
					found = v
				}
			}
			if found == nil {
				t.Errorf("%s %s %s: no road variant", top.Species, top.Pose, fr.State)
				continue
			}
			seen[*found] = true
			if found.StumpState != fr.StumpState || found.StumpDX != fr.StumpWorldDX || found.StumpDY != fr.StumpWorldDY {
				t.Errorf("%s %s %s: variant stump %s (%v,%v), art %s (%v,%v)", top.Species, top.Pose, fr.State,
					found.StumpState, found.StumpDX, found.StumpDY, fr.StumpState, fr.StumpWorldDX, fr.StumpWorldDY)
			}
			if off, _ := found.stumpOffset(); off.X != top.StumpTileDX || off.Y != top.StumpTileDY {
				t.Errorf("%s %s %s: engine stump tile %v, art (%d,%d)", top.Species, top.Pose, fr.State,
					off, top.StumpTileDX, top.StumpTileDY)
			}
		}
	}
	for _, v := range roadObstacleVariants {
		if v.StumpState != "" && !seen[v] {
			t.Errorf("snapped variant %s %s is not in the art", v.Asset, v.State)
		}
	}
}
