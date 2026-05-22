package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

func intPtr(v int) *int { return &v }

// seededWorld stands up a running mem-backed world and applies a command that
// sets world state + a couple of actors and an object. Because Run republishes
// before replying to a command, world.Published() reflects these once Send
// returns, so the handlers (which read Published()) see them.
func seededWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Phase = sim.PhaseNight
		world.Environment.Weather = "clear"
		world.Environment.Atmosphere = "a hush over the square"
		world.Actors["hannah"] = &sim.Actor{
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Role: "innkeeper", CurrentX: 3, CurrentY: 4,
			InsideStructureID: "tavern",
			LLMAgent:          "hannah-va",
			SpriteID:          "sprite-1", Facing: "east",
		}
		world.Actors["bram"] = &sim.Actor{
			ID: "bram", DisplayName: "Bram", Kind: sim.KindPC,
			State: sim.StateWalking, CurrentX: 1, CurrentY: 1,
		}
		world.VillageObjects["obj1"] = &sim.VillageObject{
			ID: "obj1", AssetID: "asset-x", X: 5.5, Y: 6.5,
			CurrentState: "lit", DisplayName: "Tavern", Tags: []string{"vendor"},
		}
		// Reference state — read by the terrain/assets handlers directly off
		// *sim.World (not the published snapshot). Set here so the post-Send
		// happens-before makes them visible to the test goroutine.
		grid := make([]byte, sim.MapW*sim.MapH)
		grid[0] = sim.TerrainDirt                 // tile (0,0)
		grid[1*sim.MapW+2] = sim.TerrainDeepWater // tile (2,1) = y*MapW+x
		world.Terrain = &sim.Terrain{Data: grid}
		packURL := "https://cdn.example/tavern.png"
		world.Assets = map[sim.AssetID]*sim.Asset{
			"asset-x": {
				ID: "asset-x", Name: "Tavern", Category: "structure",
				DefaultState: "unlit", AnchorX: 1.5, AnchorY: 2, Layer: "objects",
				ZIndex: 3, VisibleWhenInside: false,
				FootprintLeft: 1, FootprintRight: 1, FootprintTop: 0, FootprintBottom: 2,
				DoorOffsetX: intPtr(1), DoorOffsetY: intPtr(2),
				Pack: &sim.TilesetPack{ID: "pack1", Name: "Town", URL: &packURL},
				States: []sim.AssetState{
					{ID: 1, State: "unlit", Sheet: "town.png", SrcX: 0, SrcY: 0, SrcW: 64, SrcH: 96, FrameCount: 1, FrameRate: 0},
					{
						ID: 2, State: "lit", Sheet: "town.png", SrcX: 64, SrcY: 0, SrcW: 64, SrcH: 96,
						FrameCount: 2, FrameRate: 4, Tags: []string{"night-active"},
						Light: &sim.AssetLight{Color: "#ffaa33", Radius: 80, Energy: 1.2, OffsetX: 0, OffsetY: -16, FlickerAmplitude: 0.1, FlickerPeriodMs: 600},
					},
				},
				Slots: []sim.AssetSlot{{SlotName: "sign", OffsetX: 4, OffsetY: -8}},
			},
			// Engine-only fields populated to prove they DON'T leak to the wire.
			"asset-y": {
				ID: "asset-y", Name: "Bush", Category: "nature", DefaultState: "default",
				Layer: "objects", IsObstacle: true, IsPassage: true,
				RotationAlgo: "deterministic", TransitionSpreadSeconds: 5,
				OccupiedMinCount: 2, OccupiedNightOnly: true,
				States: []sim.AssetState{{ID: 3, State: "default", Sheet: "nature.png", SrcW: 32, SrcH: 32, FrameCount: 1}},
			},
		}
		spritePackURL := "https://cdn.example/npc/woman_A.png"
		world.Sprites = map[sim.SpriteID]*sim.Sprite{
			"sprite-1": {
				ID: "sprite-1", Name: "Woman A v00", Sheet: "npc/woman_A_v00.png",
				FrameWidth: 64, FrameHeight: 64,
				Pack: &sim.TilesetPack{ID: "mana-seed", Name: "Mana Seed", URL: &spritePackURL},
				Animations: []sim.SpriteAnimation{
					{Direction: "south", Animation: "idle", RowIndex: 0, FrameCount: 1, FrameRate: 6},
					{Direction: "south", Animation: "walk", RowIndex: 1, FrameCount: 4, FrameRate: 8},
				},
			},
			"sprite-2": {
				ID: "sprite-2", Name: "Old Man B v02", Sheet: "npc/old_man_B_v02.png",
				FrameWidth: 64, FrameHeight: 64,
			},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed command: %v", err)
	}
	return w
}

// okAuth is a test Authenticator: any non-empty token is a valid salem user;
// an empty token is rejected (exercises the missing-token path). Shared with
// hub_test.go (same package).
type okAuth struct{}

func (okAuth) Verify(token string) VerifyResult {
	if token == "" {
		return VerifyResult{Reason: "missing"}
	}
	return VerifyResult{Valid: true, User: &AuthUser{Username: "tester", Realms: []string{"salem"}}}
}

const testToken = "test-token"

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func TestHandleWorld(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := get(t, srv, "/api/village/world")

	var dto WorldStateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", dto.ContractVersion, ContractVersion)
	}
	if dto.Phase != "night" {
		t.Errorf("phase = %q, want night", dto.Phase)
	}
	if dto.Weather != "clear" {
		t.Errorf("weather = %q, want clear", dto.Weather)
	}
	if dto.Atmosphere != "a hush over the square" {
		t.Errorf("atmosphere = %q", dto.Atmosphere)
	}
}

func TestHandleAgents(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := get(t, srv, "/api/village/agents")

	var agents []AgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(agents))
	}
	// Sorted by ID: bram < hannah.
	if agents[0].ID != "bram" || agents[1].ID != "hannah" {
		t.Fatalf("order = [%s %s], want [bram hannah]", agents[0].ID, agents[1].ID)
	}
	bram, hannah := agents[0], agents[1]
	if bram.Kind != "pc" {
		t.Errorf("bram.kind = %q, want pc", bram.Kind)
	}
	if bram.State != "walking" || bram.X != 1 || bram.Y != 1 {
		t.Errorf("bram state/pos = %q (%d,%d)", bram.State, bram.X, bram.Y)
	}
	if hannah.Kind != "npc_shared" {
		t.Errorf("hannah.kind = %q, want npc_shared", hannah.Kind)
	}
	if hannah.Role != "innkeeper" || hannah.X != 3 || hannah.Y != 4 || hannah.InsideStructureID != "tavern" {
		t.Errorf("hannah fields wrong: %+v", hannah)
	}
	if hannah.LLMAgent != "hannah-va" {
		t.Errorf("hannah.llm_memory_agent = %q, want hannah-va", hannah.LLMAgent)
	}
	// bram has no LLMAgent → omitted from the wire (editor picker skips it).
	if bram.LLMAgent != "" {
		t.Errorf("bram.llm_memory_agent = %q, want empty", bram.LLMAgent)
	}
	// hannah has a sprite_id that resolves against the seeded catalog: the
	// inline sprite carries the render subset (no pack) and the animation rows.
	if hannah.Facing != "east" {
		t.Errorf("hannah.facing = %q, want east", hannah.Facing)
	}
	if hannah.Sprite == nil {
		t.Fatal("hannah.sprite should be resolved")
	}
	if hannah.Sprite.ID != "sprite-1" || hannah.Sprite.Sheet != "npc/woman_A_v00.png" || hannah.Sprite.FrameWidth != 64 {
		t.Errorf("hannah sprite fields wrong: %+v", hannah.Sprite)
	}
	if len(hannah.Sprite.Animations) != 2 || hannah.Sprite.Animations[0].Animation != "idle" {
		t.Errorf("hannah sprite animations wrong: %+v", hannah.Sprite.Animations)
	}
	// bram has no sprite_id → Sprite omitted; facing normalizes to "south"
	// (always present so pg-loaded and in-memory actors share a wire shape).
	if bram.Sprite != nil {
		t.Errorf("bram.sprite should be nil, got %+v", bram.Sprite)
	}
	if bram.Facing != "south" {
		t.Errorf("bram.facing = %q, want normalized south", bram.Facing)
	}
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	// raw[0] is bram (sorted): sprite key omitted, facing always present.
	if _, present := raw[0]["sprite"]; present {
		t.Errorf("bram sprite should be omitted, got present")
	}
	if f, _ := raw[0]["facing"].(string); f != "south" {
		t.Errorf("bram facing should be present and 'south', got %q", f)
	}
	// inline sprite must NOT carry pack (that's only on the raw catalog).
	hannahRaw := raw[1]["sprite"].(map[string]any)
	if _, present := hannahRaw["pack"]; present {
		t.Errorf("inline agent sprite should not carry pack, got present")
	}
}

func TestHandleObjects(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := get(t, srv, "/api/village/objects")

	var objs []ObjectDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &objs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("len(objects) = %d, want 1", len(objs))
	}
	o := objs[0]
	if o.ID != "obj1" || o.AssetID != "asset-x" || o.X != 5.5 || o.Y != 6.5 {
		t.Errorf("object identity/pos wrong: %+v", o)
	}
	if o.CurrentState != "lit" || o.DisplayName != "Tavern" || len(o.Tags) != 1 || o.Tags[0] != "vendor" {
		t.Errorf("object fields wrong: %+v", o)
	}
	// obj1 has no loiter override and asset-x has door_offset (1,2) +
	// footprint_bottom 2, so the effective offset is the door fallback: door +
	// 1 tile south = (1, 3). Raw override stays null; owner/placed_by/entry are
	// unset on the seed (ZBBS-HOME-289).
	if o.LoiterOffsetX != nil || o.LoiterOffsetY != nil {
		t.Errorf("raw loiter offset = (%v,%v), want null (no override)", o.LoiterOffsetX, o.LoiterOffsetY)
	}
	if o.EffectiveLoiterOffsetX != 1 || o.EffectiveLoiterOffsetY != 3 {
		t.Errorf("effective loiter offset = (%d,%d), want (1,3) door fallback", o.EffectiveLoiterOffsetX, o.EffectiveLoiterOffsetY)
	}
	if o.Owner != "" || o.PlacedBy != "" || o.EntryPolicy != "" {
		t.Errorf("owner/placed_by/entry_policy = %q/%q/%q, want all empty", o.Owner, o.PlacedBy, o.EntryPolicy)
	}
}

// TestHandleObjects_LoiterOverrideAndDanglingAsset covers the metadata-read
// paths beyond the seeded obj1: a per-instance loiter override (effective ==
// override), a dangling asset_id with an override (falls back to the override,
// no panic), and a dangling asset_id with no override (effective zero). Also
// checks owner/entry_policy surface. ZBBS-HOME-289.
func TestHandleObjects_LoiterOverrideAndDanglingAsset(t *testing.T) {
	w := seededWorld(t)
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// Override + owner + entry policy, real asset.
		world.VillageObjects["obj2"] = &sim.VillageObject{
			ID: "obj2", AssetID: "asset-x", X: 5.5, Y: 6.5,
			LoiterOffsetX: intPtr(4), LoiterOffsetY: intPtr(-3),
			OwnerActorID: "hannah", EntryPolicy: sim.EntryPolicyOwner, PlacedBy: "home",
		}
		// Dangling asset_id + override → effective falls back to the override.
		world.VillageObjects["obj3"] = &sim.VillageObject{
			ID: "obj3", AssetID: "ghost-asset", X: 0, Y: 0,
			LoiterOffsetX: intPtr(2), LoiterOffsetY: intPtr(2),
		}
		// Dangling asset_id, no override → effective zero, no panic.
		world.VillageObjects["obj4"] = &sim.VillageObject{
			ID: "obj4", AssetID: "ghost-asset", X: 0, Y: 0,
		}
		// Dangling asset_id + ONE-AXIS-ONLY override → treated as no override
		// (mirrors computeLoiterTile's both-or-nothing gate), so effective is
		// (0,0), NOT a per-axis blend. Not reachable via the route (both-or-
		// neither), only via direct world state.
		world.VillageObjects["obj5"] = &sim.VillageObject{
			ID: "obj5", AssetID: "ghost-asset", X: 0, Y: 0,
			LoiterOffsetX: intPtr(7),
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed objects: %v", err)
	}

	srv := NewServer(w, okAuth{})
	rec := get(t, srv, "/api/village/objects")
	var objs []ObjectDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &objs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]ObjectDTO{}
	for _, o := range objs {
		byID[o.ID] = o
	}

	o2 := byID["obj2"]
	if o2.LoiterOffsetX == nil || *o2.LoiterOffsetX != 4 || o2.EffectiveLoiterOffsetX != 4 || o2.EffectiveLoiterOffsetY != -3 {
		t.Errorf("obj2 loiter = raw(%v,%v) eff(%d,%d), want raw(4,-3) eff(4,-3)", o2.LoiterOffsetX, o2.LoiterOffsetY, o2.EffectiveLoiterOffsetX, o2.EffectiveLoiterOffsetY)
	}
	if o2.Owner != "hannah" || o2.EntryPolicy != "owner-only" || o2.PlacedBy != "home" {
		t.Errorf("obj2 owner/entry/placed = %q/%q/%q, want hannah/owner-only/home", o2.Owner, o2.EntryPolicy, o2.PlacedBy)
	}

	o3 := byID["obj3"]
	if o3.EffectiveLoiterOffsetX != 2 || o3.EffectiveLoiterOffsetY != 2 {
		t.Errorf("obj3 (dangling asset + override) effective = (%d,%d), want (2,2)", o3.EffectiveLoiterOffsetX, o3.EffectiveLoiterOffsetY)
	}

	o4 := byID["obj4"]
	if o4.EffectiveLoiterOffsetX != 0 || o4.EffectiveLoiterOffsetY != 0 {
		t.Errorf("obj4 (dangling asset, no override) effective = (%d,%d), want (0,0)", o4.EffectiveLoiterOffsetX, o4.EffectiveLoiterOffsetY)
	}

	o5 := byID["obj5"]
	if o5.EffectiveLoiterOffsetX != 0 || o5.EffectiveLoiterOffsetY != 0 {
		t.Errorf("obj5 (dangling asset, one-axis override) effective = (%d,%d), want (0,0) — partial override is not honored", o5.EffectiveLoiterOffsetX, o5.EffectiveLoiterOffsetY)
	}
}

func TestHandleTerrain(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := get(t, srv, "/api/village/terrain")

	var dto TerrainDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", dto.ContractVersion, ContractVersion)
	}
	if dto.MapW != sim.MapW || dto.MapH != sim.MapH {
		t.Errorf("dims = %dx%d, want %dx%d", dto.MapW, dto.MapH, sim.MapW, sim.MapH)
	}
	if dto.PadX != sim.PadX || dto.PadY != sim.PadY || dto.TileSize != int(sim.TileSize) {
		t.Errorf("pad/tile = (%d,%d) %d, want (%d,%d) %d", dto.PadX, dto.PadY, dto.TileSize, sim.PadX, sim.PadY, int(sim.TileSize))
	}
	grid, err := base64.StdEncoding.DecodeString(dto.Data)
	if err != nil {
		t.Fatalf("base64 decode data: %v", err)
	}
	if len(grid) != sim.MapW*sim.MapH {
		t.Fatalf("decoded grid len = %d, want %d", len(grid), sim.MapW*sim.MapH)
	}
	// Row-major: client indexes data[y*map_w + x].
	if grid[0] != sim.TerrainDirt {
		t.Errorf("tile (0,0) = %d, want dirt %d", grid[0], sim.TerrainDirt)
	}
	if grid[1*sim.MapW+2] != sim.TerrainDeepWater {
		t.Errorf("tile (2,1) = %d, want deep-water %d", grid[1*sim.MapW+2], sim.TerrainDeepWater)
	}
}

func TestHandleTerrain_NilTerrain(t *testing.T) {
	// A world with no terrain loaded still answers with the metadata header and
	// an empty data string (decodes to a zero-length grid client-side).
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Terrain = nil
	srv := NewServer(w, okAuth{})
	rec := get(t, srv, "/api/village/terrain")

	var dto TerrainDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.MapW != sim.MapW || dto.MapH != sim.MapH || dto.Data != "" {
		t.Errorf("nil-terrain response wrong: %+v", dto)
	}
}

func TestHandleAssets(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := get(t, srv, "/api/village/assets")

	var assets []AssetDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &assets); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("len(assets) = %d, want 2", len(assets))
	}
	// Sorted by ID: asset-x < asset-y.
	if assets[0].ID != "asset-x" || assets[1].ID != "asset-y" {
		t.Fatalf("order = [%s %s], want [asset-x asset-y]", assets[0].ID, assets[1].ID)
	}
	tavern := assets[0]
	if tavern.Name != "Tavern" || tavern.Category != "structure" || tavern.DefaultState != "unlit" {
		t.Errorf("tavern scalars wrong: %+v", tavern)
	}
	if tavern.AnchorX != 1.5 || tavern.AnchorY != 2 || tavern.Layer != "objects" || tavern.ZIndex != 3 {
		t.Errorf("tavern anchor/layer/z wrong: %+v", tavern)
	}
	if tavern.Footprint != (FootprintDTO{Left: 1, Right: 1, Top: 0, Bottom: 2}) {
		t.Errorf("tavern footprint = %+v", tavern.Footprint)
	}
	if tavern.DoorOffsetX == nil || *tavern.DoorOffsetX != 1 || tavern.DoorOffsetY == nil || *tavern.DoorOffsetY != 2 {
		t.Errorf("tavern door offset wrong: %+v", tavern)
	}
	if tavern.Pack == nil || tavern.Pack.ID != "pack1" || tavern.Pack.Name != "Town" || tavern.Pack.URL == nil || *tavern.Pack.URL != "https://cdn.example/tavern.png" {
		t.Errorf("tavern pack wrong: %+v", tavern.Pack)
	}
	if len(tavern.States) != 2 {
		t.Fatalf("tavern states = %d, want 2", len(tavern.States))
	}
	lit := tavern.States[1]
	if lit.State != "lit" || lit.SrcX != 64 || lit.FrameCount != 2 || lit.FrameRate != 4 {
		t.Errorf("lit state wrong: %+v", lit)
	}
	if len(lit.Tags) != 1 || lit.Tags[0] != "night-active" {
		t.Errorf("lit tags = %v", lit.Tags)
	}
	if lit.Light == nil || lit.Light.Color != "#ffaa33" || lit.Light.Radius != 80 || lit.Light.FlickerPeriodMs != 600 {
		t.Errorf("lit light wrong: %+v", lit.Light)
	}
	if tavern.States[0].Light != nil {
		t.Errorf("unlit state should have no light, got %+v", tavern.States[0].Light)
	}
	if len(tavern.Slots) != 1 || tavern.Slots[0].SlotName != "sign" || tavern.Slots[0].OffsetX != 4 || tavern.Slots[0].OffsetY != -8 {
		t.Errorf("tavern slots wrong: %+v", tavern.Slots)
	}

	// Engine-only fields must NOT appear on the wire. Re-decode asset-y into a
	// permissive map and assert the dropped keys are absent.
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	bush := raw[1]
	for _, k := range []string{"rotation_algo", "transition_spread_seconds", "occupied_min_count", "occupied_night_only", "is_obstacle", "is_passage"} {
		if _, present := bush[k]; present {
			t.Errorf("engine-only key %q leaked to the wire", k)
		}
	}
	// asset-y has no slots and no door offset → those keys are omitted entirely.
	if _, present := bush["slots"]; present {
		t.Errorf("empty slots should be omitted, got present")
	}
	if _, present := bush["door_offset_x"]; present {
		t.Errorf("nil door_offset_x should be omitted, got present")
	}
}

func TestHandleSprites(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := get(t, srv, "/api/village/sprites")

	var sprites []SpriteDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &sprites); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sprites) != 2 {
		t.Fatalf("len(sprites) = %d, want 2", len(sprites))
	}
	// Sorted by ID: sprite-1 < sprite-2.
	if sprites[0].ID != "sprite-1" || sprites[1].ID != "sprite-2" {
		t.Fatalf("order = [%s %s], want [sprite-1 sprite-2]", sprites[0].ID, sprites[1].ID)
	}
	woman := sprites[0]
	if woman.Name != "Woman A v00" || woman.Sheet != "npc/woman_A_v00.png" {
		t.Errorf("woman scalars wrong: %+v", woman)
	}
	if woman.FrameWidth != 64 || woman.FrameHeight != 64 {
		t.Errorf("woman frame dims = %dx%d, want 64x64", woman.FrameWidth, woman.FrameHeight)
	}
	if woman.Pack == nil || woman.Pack.ID != "mana-seed" || woman.Pack.URL == nil || *woman.Pack.URL != "https://cdn.example/npc/woman_A.png" {
		t.Errorf("woman pack wrong: %+v", woman.Pack)
	}
	if len(woman.Animations) != 2 {
		t.Fatalf("woman animations = %d, want 2", len(woman.Animations))
	}
	walk := woman.Animations[1]
	if walk.Direction != "south" || walk.Animation != "walk" || walk.RowIndex != 1 || walk.FrameCount != 4 || walk.FrameRate != 8 {
		t.Errorf("walk animation wrong: %+v", walk)
	}

	// A sprite with no pack and no animations: pack omitted, animations is [].
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	oldMan := raw[1]
	if _, present := oldMan["pack"]; present {
		t.Errorf("nil pack should be omitted, got present")
	}
	anims, ok := oldMan["animations"].([]any)
	if !ok {
		t.Fatalf("animations not an array: %T", oldMan["animations"])
	}
	if len(anims) != 0 {
		t.Errorf("empty animations should serialize as [], got %v", anims)
	}
}

func TestNewServer_NilWorldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil world")
		}
	}()
	NewServer(nil, okAuth{})
}

func TestNewServer_NilAuthPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil authenticator")
		}
	}()
	NewServer(seededWorld(t), nil)
}
