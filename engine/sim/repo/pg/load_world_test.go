package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LoadWorld tests use hand-rolled fake sub-repos rather than pgxmock —
// this layer tests orchestration (dep order, notImpl tolerance, cross-
// aggregate checks). Individual repo SQL semantics are covered by each
// per-repo *_test.go. Mocking SQL here would duplicate that without
// adding signal.

// --- fakes -----------------------------------------------------------------

type fakeActors struct {
	out map[sim.ActorID]*sim.Actor
	err error
}

func (f fakeActors) LoadAll(_ context.Context) (map[sim.ActorID]*sim.Actor, error) {
	return f.out, f.err
}
func (fakeActors) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.ActorID]*sim.Actor) error {
	return nil
}

type fakeStructures struct {
	out map[sim.StructureID]*sim.Structure
	err error
}

func (f fakeStructures) LoadAll(_ context.Context) (map[sim.StructureID]*sim.Structure, error) {
	return f.out, f.err
}
func (fakeStructures) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.StructureID]*sim.Structure) error {
	return nil
}

type fakeHuddles struct {
	out map[sim.HuddleID]*sim.Huddle
	err error
}

func (f fakeHuddles) LoadAll(_ context.Context) (map[sim.HuddleID]*sim.Huddle, error) {
	return f.out, f.err
}
func (fakeHuddles) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.HuddleID]*sim.Huddle) error {
	return nil
}

type fakeScenes struct {
	out map[sim.SceneID]*sim.Scene
	err error
}

func (f fakeScenes) LoadAll(_ context.Context) (map[sim.SceneID]*sim.Scene, error) {
	return f.out, f.err
}
func (fakeScenes) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.SceneID]*sim.Scene) error {
	return nil
}

type fakeOrders struct {
	out map[sim.OrderID]*sim.Order
	err error
}

func (f fakeOrders) LoadAll(_ context.Context) (map[sim.OrderID]*sim.Order, error) {
	return f.out, f.err
}
func (fakeOrders) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.OrderID]*sim.Order) error {
	return nil
}
func (fakeOrders) LoadRecentPrices(_ context.Context, _ time.Time, _ int) ([]sim.PriceBookSeedRecord, error) {
	return nil, nil
}
func (fakeOrders) WriteTerminal(_ context.Context, _ *sim.Order) error {
	return nil
}

type fakeEnvironment struct {
	env      sim.WorldEnvironment
	phase    sim.Phase
	settings sim.WorldSettings
	err      error
}

func (f fakeEnvironment) Load(_ context.Context) (sim.WorldEnvironment, sim.Phase, sim.WorldSettings, error) {
	return f.env, f.phase, f.settings, f.err
}
func (fakeEnvironment) SaveSnapshot(_ context.Context, _ sim.Tx, _ sim.WorldEnvironment, _ sim.Phase) error {
	return nil
}

type fakeAssets struct {
	out map[sim.AssetID]*sim.Asset
	err error
}

func (f fakeAssets) LoadAll(_ context.Context) (map[sim.AssetID]*sim.Asset, error) {
	return f.out, f.err
}

type fakeSprites struct {
	out map[sim.SpriteID]*sim.Sprite
	err error
}

func (f fakeSprites) LoadAll(_ context.Context) (map[sim.SpriteID]*sim.Sprite, error) {
	return f.out, f.err
}

type fakeRecipes struct {
	out map[sim.ItemKind]*sim.ItemRecipe
	err error
}

func (f fakeRecipes) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemRecipe, error) {
	return f.out, f.err
}

type fakeItemKinds struct {
	out map[sim.ItemKind]*sim.ItemKindDef
	err error
}

func (f fakeItemKinds) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemKindDef, error) {
	return f.out, f.err
}

type fakeAttributeDefinitions struct {
	out map[string]*sim.AttributeDefinition
	err error
}

func (f fakeAttributeDefinitions) LoadAll(_ context.Context) (map[string]*sim.AttributeDefinition, error) {
	return f.out, f.err
}

type fakeTerrain struct {
	out *sim.Terrain
	err error
}

func (f fakeTerrain) Load(_ context.Context) (*sim.Terrain, error) {
	return f.out, f.err
}

type fakeVillageObjects struct {
	out map[sim.VillageObjectID]*sim.VillageObject
	err error
}

func (f fakeVillageObjects) LoadAll(_ context.Context) (map[sim.VillageObjectID]*sim.VillageObject, error) {
	return f.out, f.err
}
func (fakeVillageObjects) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.VillageObjectID]*sim.VillageObject) error {
	return nil
}

type fakeActionLog struct{}

func (fakeActionLog) Append(_ context.Context, _ sim.ActionLogEntry) error { return nil }

type fakeTickTelemetry struct{}

func (fakeTickTelemetry) WriteTickTelemetry(_ sim.TickTelemetryRecord) {}

// fakeRepoOpts assembles a sim.Repository where every sub-repo is an
// impl'd fake. Test cases override individual fields to inject scenarios.
type fakeRepoOpts struct {
	actors         sim.ActorsRepo
	structures     sim.StructuresRepo
	huddles        sim.HuddlesRepo
	scenes         sim.ScenesRepo
	orders         sim.OrdersRepo
	environment    sim.EnvironmentRepo
	assets         sim.AssetsRepo
	sprites        sim.SpritesRepo
	attributeDefs  sim.AttributeDefinitionsRepo
	recipes        sim.RecipesRepo
	itemKinds      sim.ItemKindsRepo
	terrain        sim.TerrainRepo
	villageObjects sim.VillageObjectsRepo
}

func (o fakeRepoOpts) build() sim.Repository {
	pick := func(actual, fallback interface{}) interface{} {
		if actual == nil {
			return fallback
		}
		return actual
	}
	return sim.Repository{
		Actors:               pick(o.actors, fakeActors{out: map[sim.ActorID]*sim.Actor{}}).(sim.ActorsRepo),
		Structures:           pick(o.structures, fakeStructures{out: map[sim.StructureID]*sim.Structure{}}).(sim.StructuresRepo),
		Huddles:              pick(o.huddles, fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{}}).(sim.HuddlesRepo),
		Scenes:               pick(o.scenes, fakeScenes{out: map[sim.SceneID]*sim.Scene{}}).(sim.ScenesRepo),
		Orders:               pick(o.orders, fakeOrders{out: map[sim.OrderID]*sim.Order{}}).(sim.OrdersRepo),
		Environment:          pick(o.environment, fakeEnvironment{}).(sim.EnvironmentRepo),
		Assets:               pick(o.assets, fakeAssets{out: map[sim.AssetID]*sim.Asset{}}).(sim.AssetsRepo),
		Sprites:              pick(o.sprites, fakeSprites{out: map[sim.SpriteID]*sim.Sprite{}}).(sim.SpritesRepo),
		AttributeDefinitions: pick(o.attributeDefs, fakeAttributeDefinitions{out: map[string]*sim.AttributeDefinition{}}).(sim.AttributeDefinitionsRepo),
		Recipes:              pick(o.recipes, fakeRecipes{out: map[sim.ItemKind]*sim.ItemRecipe{}}).(sim.RecipesRepo),
		ItemKinds:            pick(o.itemKinds, fakeItemKinds{out: map[sim.ItemKind]*sim.ItemKindDef{}}).(sim.ItemKindsRepo),
		Terrain:              pick(o.terrain, fakeTerrain{out: &sim.Terrain{}}).(sim.TerrainRepo),
		VillageObjects:       pick(o.villageObjects, fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}}).(sim.VillageObjectsRepo),
		ActionLog:            fakeActionLog{},
		TickTelemetry:        fakeTickTelemetry{},
	}
}

// --- LoadWorld -------------------------------------------------------------

const (
	bldgA = "11111111-1111-1111-1111-aaaaaaaaaaaa"
	bldgB = "11111111-1111-1111-1111-bbbbbbbbbbbb"
)

// TestLoadWorld_HappyPath — full impl'd set, every check passes.
// Verifies primary state lands in the World and the checks don't trip.
func TestLoadWorld_HappyPath(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	huddleA := &sim.Huddle{ID: "h-a", Members: map[sim.ActorID]struct{}{"act-1": {}}, StructureID: bldgA, StartedAt: startedAt}
	structA := &sim.Structure{ID: bldgA, DisplayName: "Tavern", Tags: []string{}}
	voA := &sim.VillageObject{ID: bldgA}
	sceA := &sim.Scene{
		ID:         "sc-a",
		OriginAt:   startedAt,
		OriginKind: "pc_speak",
		Bound:      sim.NewStructureBound(bldgA),
		Huddles:    map[sim.HuddleID]struct{}{"h-a": {}},
	}
	// act-1 is huddleA's member — Slice 1 reconciliation requires the
	// actor to be in the loaded set or the load hard-errors as
	// "missing actor referenced by huddle member".
	actA1 := &sim.Actor{
		ID: "act-1", DisplayName: "Anya", State: "working",
		StateEnteredAt: startedAt,
	}

	repo := fakeRepoOpts{
		actors:         fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actA1}},
		structures:     fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{bldgA: voA}},
		huddles:        fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{"h-a": huddleA}},
		scenes:         fakeScenes{out: map[sim.SceneID]*sim.Scene{"sc-a": sceA}},
	}.build()

	w, err := LoadWorld(context.Background(), repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if len(w.Structures) != 1 || w.Structures[bldgA] == nil {
		t.Errorf("Structures = %v", w.Structures)
	}
	if len(w.VillageObjects) != 1 {
		t.Errorf("VillageObjects len = %d", len(w.VillageObjects))
	}
	if len(w.Huddles) != 1 {
		t.Errorf("Huddles len = %d", len(w.Huddles))
	}
	if len(w.Scenes) != 1 {
		t.Errorf("Scenes len = %d (orphan-drop falsely fired?)", len(w.Scenes))
	}
}

// TestLoadWorld_NotImpl_Tolerated — pg.NewRepository's actual wiring
// (notImpl stubs) with requireAllImpl=false succeeds: each notImpl
// repo's error is swallowed, World keeps NewWorld-empty defaults.
func TestLoadWorld_NotImpl_Tolerated(t *testing.T) {
	repo := sim.Repository{
		Actors:               notImplActors{},
		Structures:           fakeStructures{out: map[sim.StructureID]*sim.Structure{}},
		Huddles:              fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{}},
		Scenes:               fakeScenes{out: map[sim.SceneID]*sim.Scene{}},
		Orders:               fakeOrders{out: map[sim.OrderID]*sim.Order{}},
		Environment:          fakeEnvironment{err: errNotImpl},
		Assets:               notImplAssets{},
		Sprites:              notImplSprites{},
		AttributeDefinitions: notImplAttributeDefinitions{},
		Recipes:              notImplRecipes{},
		ItemKinds:            notImplItemKinds{},
		Terrain:              notImplTerrain{},
		VillageObjects:       fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}},
		ActionLog:            notImplActionLog{},
		TickTelemetry:        notImplTickTelemetry{},
	}

	w, err := LoadWorld(context.Background(), repo, false /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if w == nil {
		t.Fatal("World nil")
	}
	if len(w.Actors) != 0 {
		t.Errorf("Actors should stay empty when notImpl; got len=%d", len(w.Actors))
	}
}

// TestLoadWorld_NotImpl_Required_HardFails — requireAllImpl=true with
// any notImpl repo trips a hard error naming the sub-repo.
func TestLoadWorld_NotImpl_Required_HardFails(t *testing.T) {
	repo := sim.Repository{
		Actors:               notImplActors{},
		Structures:           fakeStructures{out: map[sim.StructureID]*sim.Structure{}},
		Huddles:              fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{}},
		Scenes:               fakeScenes{out: map[sim.SceneID]*sim.Scene{}},
		Orders:               fakeOrders{out: map[sim.OrderID]*sim.Order{}},
		Environment:          fakeEnvironment{},
		Assets:               fakeAssets{out: map[sim.AssetID]*sim.Asset{}},
		Sprites:              fakeSprites{out: map[sim.SpriteID]*sim.Sprite{}},
		AttributeDefinitions: fakeAttributeDefinitions{out: map[string]*sim.AttributeDefinition{}},
		Recipes:              fakeRecipes{out: map[sim.ItemKind]*sim.ItemRecipe{}},
		ItemKinds:            fakeItemKinds{out: map[sim.ItemKind]*sim.ItemKindDef{}},
		Terrain:              fakeTerrain{out: &sim.Terrain{}},
		VillageObjects:       fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}},
		ActionLog:            fakeActionLog{},
		TickTelemetry:        fakeTickTelemetry{},
	}

	_, err := LoadWorld(context.Background(), repo, true /*requireAllImpl*/)
	if err == nil {
		t.Fatal("expected error for notImpl Actors with requireAllImpl=true")
	}
	if !strings.Contains(err.Error(), "Actors") {
		t.Errorf("error should name Actors: %v", err)
	}
	if !errors.Is(err, errNotImpl) {
		t.Errorf("error should wrap errNotImpl: %v", err)
	}
}

// TestLoadWorld_NilRefCatalog_Tolerated (ZBBS-HOME-295, code_review #1): a NIL
// reference-catalog sub-repo (an unset field in a partial repo construction) is
// treated as notImpl, not a panic — loadOptionalCatalog nil-guards before the
// LoadAll call. With requireAllImpl=false the load succeeds and the catalog
// stays at its NewWorld-empty default.
func TestLoadWorld_NilRefCatalog_Tolerated(t *testing.T) {
	repo := fakeRepoOpts{}.build()
	repo.AttributeDefinitions = nil // unset field — pre-fix this panicked

	w, err := LoadWorld(context.Background(), repo, false /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld with nil AttributeDefinitions should tolerate: %v", err)
	}
	if len(w.AttributeDefinitions) != 0 {
		t.Errorf("AttributeDefinitions should stay empty when nil; got len=%d", len(w.AttributeDefinitions))
	}
}

// TestLoadWorld_NilRefCatalog_RequiredHardFails: with requireAllImpl=true a nil
// reference catalog hard-errors (naming the sub-repo + wrapping errNotImpl)
// rather than panicking.
func TestLoadWorld_NilRefCatalog_RequiredHardFails(t *testing.T) {
	repo := fakeRepoOpts{}.build()
	repo.Assets = nil

	_, err := LoadWorld(context.Background(), repo, true /*requireAllImpl*/)
	if err == nil {
		t.Fatal("expected error for nil Assets with requireAllImpl=true")
	}
	if !strings.Contains(err.Error(), "Assets") {
		t.Errorf("error should name Assets: %v", err)
	}
	if !errors.Is(err, errNotImpl) {
		t.Errorf("error should wrap errNotImpl: %v", err)
	}
}

// TestLoadWorld_MissingHuddleRef_HardFails — a Scene.Huddles set
// references a HuddleID that's not in the loaded Huddles map.
func TestLoadWorld_MissingHuddleRef_HardFails(t *testing.T) {
	sceA := &sim.Scene{
		ID:         "sc-a",
		OriginAt:   time.Now(),
		OriginKind: "pc_speak",
		Bound:      sim.NewAreaBound(sim.Position{X: 0, Y: 0}, 3),
		// References huddle "h-ghost" which is NOT in the Huddles map.
		Huddles: map[sim.HuddleID]struct{}{"h-ghost": {}},
	}
	repo := fakeRepoOpts{
		scenes:  fakeScenes{out: map[sim.SceneID]*sim.Scene{"sc-a": sceA}},
		huddles: fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, false)
	if err == nil {
		t.Fatal("expected error for missing huddle ref")
	}
	if !strings.Contains(err.Error(), "h-ghost") {
		t.Errorf("error should name the missing huddle id: %v", err)
	}
}

// TestLoadWorld_OrphanStructureBoundScene_WarnsAndDrops — a structure-
// bound scene whose bound_structure_id isn't in the loaded Structures
// map is dropped (NOT hard error). Verify the orphan scene is absent
// from w.Scenes and a sibling (non-orphan) scene survives.
func TestLoadWorld_OrphanStructureBoundScene_WarnsAndDrops(t *testing.T) {
	structA := &sim.Structure{ID: bldgA, DisplayName: "Tavern", Tags: []string{}}
	voA := &sim.VillageObject{ID: bldgA}
	sceLive := &sim.Scene{
		ID:         "sc-live",
		OriginAt:   time.Now(),
		OriginKind: "pc_speak",
		Bound:      sim.NewStructureBound(bldgA),
		Huddles:    map[sim.HuddleID]struct{}{},
	}
	sceOrphan := &sim.Scene{
		ID:         "sc-orphan",
		OriginAt:   time.Now(),
		OriginKind: "pc_speak",
		// References bldgB which doesn't exist in the Structures map.
		Bound:   sim.NewStructureBound(bldgB),
		Huddles: map[sim.HuddleID]struct{}{},
	}
	repo := fakeRepoOpts{
		structures:     fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{bldgA: voA}},
		scenes:         fakeScenes{out: map[sim.SceneID]*sim.Scene{"sc-live": sceLive, "sc-orphan": sceOrphan}},
	}.build()

	w, err := LoadWorld(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("LoadWorld: %v (orphan-drop should not hard-fail)", err)
	}
	if _, ok := w.Scenes["sc-live"]; !ok {
		t.Error("sc-live (non-orphan) should have survived")
	}
	if _, ok := w.Scenes["sc-orphan"]; ok {
		t.Error("sc-orphan should have been dropped")
	}
}

// TestLoadWorld_OrphanAreaBoundScene_NotDropped — area-bound scenes
// have no structure ref so the orphan-drop check doesn't apply.
// Sanity-check the kind switch doesn't accidentally drop them.
func TestLoadWorld_OrphanAreaBoundScene_NotDropped(t *testing.T) {
	sceArea := &sim.Scene{
		ID:         "sc-area",
		OriginAt:   time.Now(),
		OriginKind: "idle_backstop",
		Bound:      sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3),
		Huddles:    map[sim.HuddleID]struct{}{},
	}
	repo := fakeRepoOpts{
		scenes: fakeScenes{out: map[sim.SceneID]*sim.Scene{"sc-area": sceArea}},
	}.build()

	w, err := LoadWorld(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if _, ok := w.Scenes["sc-area"]; !ok {
		t.Error("area-bound scene should not be dropped")
	}
}

// TestLoadWorld_BridgeMismatch_HardFails — a Structure exists with no
// matching VillageObject (Slice 12 shared-identity bridge violation).
func TestLoadWorld_BridgeMismatch_HardFails(t *testing.T) {
	structA := &sim.Structure{ID: bldgA, DisplayName: "Tavern", Tags: []string{}}
	// No matching VillageObject for bldgA.
	repo := fakeRepoOpts{
		structures:     fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, false)
	if err == nil {
		t.Fatal("expected error for bridge violation")
	}
	if !strings.Contains(err.Error(), bldgA) {
		t.Errorf("error should name the unmatched structure id: %v", err)
	}
	if !strings.Contains(err.Error(), "village_object") {
		t.Errorf("error should reference the bridge / village_object: %v", err)
	}
}

// TestLoadWorld_BridgeCheck_MapKeyMismatch_HardFails — a Structure
// stored at map key bldgA but with s.ID == bldgB is internally
// inconsistent. The bridge check treats the map key as authoritative
// and surfaces the mismatch loudly. Defends against future non-pg
// loaders building maps inconsistently.
func TestLoadWorld_BridgeCheck_MapKeyMismatch_HardFails(t *testing.T) {
	// Map key is bldgA but the Structure carries ID=bldgB.
	struMismatch := &sim.Structure{ID: bldgB, DisplayName: "Tavern", Tags: []string{}}
	voA := &sim.VillageObject{ID: bldgA}
	voB := &sim.VillageObject{ID: bldgB}
	repo := fakeRepoOpts{
		structures: fakeStructures{out: map[sim.StructureID]*sim.Structure{
			bldgA: struMismatch,
		}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{
			bldgA: voA,
			bldgB: voB,
		}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, false)
	if err == nil {
		t.Fatal("expected error for map-key / Structure.ID mismatch")
	}
	if !strings.Contains(err.Error(), "mismatched") {
		t.Errorf("error should name the mismatch: %v", err)
	}
}

// TestLoadWorld_StructureBoundScene_NilStructureID_HardFails — a
// SceneBoundStructure with a nil StructureID is internal corruption
// (validateBoundShape + scanBound reject this at the per-repo
// boundaries; pg.LoadWorld is the last line of defense for non-pg
// loaders).
func TestLoadWorld_StructureBoundScene_NilStructureID_HardFails(t *testing.T) {
	// Construct a corrupt bound directly (NewStructureBound would
	// always populate StructureID, so bypass the constructor).
	sceCorrupt := &sim.Scene{
		ID:         "sc-corrupt",
		OriginAt:   time.Now(),
		OriginKind: "pc_speak",
		Bound:      sim.SceneBound{Kind: sim.SceneBoundStructure /* StructureID intentionally nil */},
		Huddles:    map[sim.HuddleID]struct{}{},
	}
	repo := fakeRepoOpts{
		scenes: fakeScenes{out: map[sim.SceneID]*sim.Scene{"sc-corrupt": sceCorrupt}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, false)
	if err == nil {
		t.Fatal("expected error for structure-bound scene with nil StructureID")
	}
	if !strings.Contains(err.Error(), "sc-corrupt") {
		t.Errorf("error should name the corrupt scene: %v", err)
	}
	if !strings.Contains(err.Error(), "nil StructureID") {
		t.Errorf("error should describe the nil StructureID: %v", err)
	}
}

// TestLoadWorld_SubRepoError_Wraps — a real (non-notImpl) SQL-style
// failure from any sub-repo wraps + propagates.
func TestLoadWorld_SubRepoError_Wraps(t *testing.T) {
	cases := []struct {
		name string
		opts fakeRepoOpts
		want string
	}{
		{
			name: "VillageObjects",
			opts: fakeRepoOpts{villageObjects: fakeVillageObjects{err: errors.New("vo boom")}},
			want: "VillageObjects",
		},
		{
			name: "Structures",
			opts: fakeRepoOpts{structures: fakeStructures{err: errors.New("st boom")}},
			want: "Structures",
		},
		{
			name: "Huddles",
			opts: fakeRepoOpts{huddles: fakeHuddles{err: errors.New("hd boom")}},
			want: "Huddles",
		},
		{
			name: "Scenes",
			opts: fakeRepoOpts{scenes: fakeScenes{err: errors.New("sc boom")}},
			want: "Scenes",
		},
		{
			name: "Orders",
			opts: fakeRepoOpts{orders: fakeOrders{err: errors.New("or boom")}},
			want: "Orders",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadWorld(context.Background(), tc.opts.build(), false)
			if err == nil {
				t.Fatalf("expected error for %s sub-repo failure", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name %s sub-repo: %v", tc.want, err)
			}
		})
	}
}

// TestLoadWorld_NotImplLoaders_NonErrorPassesThrough — when a notImpl
// loader path is replaced with a fake that succeeds, the World fields
// pick up the loaded data. Catches the bug where the "ignore notImpl"
// branch accidentally drops successful loads.
func TestLoadWorld_NotImplLoaders_NonErrorPassesThrough(t *testing.T) {
	terrain := &sim.Terrain{}
	repo := fakeRepoOpts{
		terrain: fakeTerrain{out: terrain},
	}.build()

	w, err := LoadWorld(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if w.Terrain != terrain {
		t.Errorf("Terrain not propagated; got %v", w.Terrain)
	}
}

// --- Slice 1 / ZBBS-WORK-243 actor carry-forwards ---

// TestLoadWorld_ActorHuddleReconciliation_StampsAndClears — Slice 11
// reconciliation rebuilds actor.CurrentHuddleID from canonical
// Huddle.Members. Validates two paths: (a) an actor referenced by a
// huddle's Members has CurrentHuddleID stamped to that huddle's ID
// regardless of what was loaded from the actor row; (b) an actor with
// a stale CurrentHuddleID but no membership has it cleared.
func TestLoadWorld_ActorHuddleReconciliation_StampsAndClears(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	huddle := &sim.Huddle{
		ID:        "h-a",
		Members:   map[sim.ActorID]struct{}{"act-member": {}},
		StartedAt: startedAt,
	}
	// act-member should pick up CurrentHuddleID="h-a" from canonical
	// Members regardless of any prior column value.
	actorMember := &sim.Actor{
		ID:              "act-member",
		DisplayName:     "M",
		State:           "idle",
		StateEnteredAt:  startedAt,
		CurrentHuddleID: "old-stale-huddle-from-actor-row",
	}
	// act-stale claims membership in a huddle but isn't in any
	// Members set — reconciliation clears the field.
	actorStale := &sim.Actor{
		ID:              "act-stale",
		DisplayName:     "S",
		State:           "idle",
		StateEnteredAt:  startedAt,
		CurrentHuddleID: "h-a", // stale — Members doesn't include it
	}

	repo := fakeRepoOpts{
		actors: fakeActors{out: map[sim.ActorID]*sim.Actor{
			"act-member": actorMember,
			"act-stale":  actorStale,
		}},
		huddles: fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{"h-a": huddle}},
	}.build()

	w, err := LoadWorld(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if w.Actors["act-member"].CurrentHuddleID != "h-a" {
		t.Errorf("act-member CurrentHuddleID = %q, want h-a", w.Actors["act-member"].CurrentHuddleID)
	}
	if w.Actors["act-stale"].CurrentHuddleID != "" {
		t.Errorf("act-stale CurrentHuddleID = %q, want empty (cache cleared)", w.Actors["act-stale"].CurrentHuddleID)
	}
}

// TestLoadWorld_ActorHuddleReconciliation_MissingActor_HardFails — a
// huddle referencing a missing actor is substrate corruption. FK
// CASCADE should make this unreachable from valid writes.
func TestLoadWorld_ActorHuddleReconciliation_MissingActor_HardFails(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	huddle := &sim.Huddle{
		ID: "h-a", Members: map[sim.ActorID]struct{}{"act-ghost": {}}, StartedAt: startedAt,
	}
	repo := fakeRepoOpts{
		actors:  fakeActors{out: map[sim.ActorID]*sim.Actor{}}, // ghost not present
		huddles: fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{"h-a": huddle}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, true)
	if err == nil || !strings.Contains(err.Error(), "missing actor id=act-ghost") {
		t.Errorf("err = %v, want substring 'missing actor id=act-ghost'", err)
	}
}

// TestLoadWorld_ActorHuddleReconciliation_DoubleMembership_HardFails — an
// actor in two huddles' Members sets violates the single-active-huddle
// invariant.
func TestLoadWorld_ActorHuddleReconciliation_DoubleMembership_HardFails(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	h1 := &sim.Huddle{ID: "h-1", Members: map[sim.ActorID]struct{}{"act-doubled": {}}, StartedAt: startedAt}
	h2 := &sim.Huddle{ID: "h-2", Members: map[sim.ActorID]struct{}{"act-doubled": {}}, StartedAt: startedAt}
	actor := &sim.Actor{ID: "act-doubled", DisplayName: "D", State: "idle", StateEnteredAt: startedAt}

	repo := fakeRepoOpts{
		actors:  fakeActors{out: map[sim.ActorID]*sim.Actor{"act-doubled": actor}},
		huddles: fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{"h-1": h1, "h-2": h2}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, true)
	if err == nil || !strings.Contains(err.Error(), "appears in two huddles") {
		t.Errorf("err = %v, want substring 'appears in two huddles'", err)
	}
}

// TestLoadWorld_ActorStructureRefs_HappyPath — actor with all three
// structure refs + a room ref to a loaded room loads cleanly.
func TestLoadWorld_ActorStructureRefs_HappyPath(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	structA := &sim.Structure{
		ID: bldgA, DisplayName: "Tavern",
		Tags: []string{},
		Rooms: []*sim.Room{
			{ID: 42, StructureID: bldgA, Kind: "common", Name: "common"},
		},
	}
	voA := &sim.VillageObject{ID: bldgA}
	actor := &sim.Actor{
		ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
		HomeStructureID:   bldgA,
		WorkStructureID:   bldgA,
		InsideStructureID: bldgA,
		InsideRoomID:      42,
	}

	repo := fakeRepoOpts{
		actors:         fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
		structures:     fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{bldgA: voA}},
	}.build()

	if _, err := LoadWorld(context.Background(), repo, true); err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
}

// TestLoadWorld_ActorStructureRefs_MissingStructure — actor refs a
// structure that didn't load — hard error per substrate invariant.
// Drives all four ref columns through table-driven cases.
func TestLoadWorld_ActorStructureRefs_MissingStructure(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	bogusStr := sim.StructureID("11111111-1111-1111-1111-deadbeefdead")
	structA := &sim.Structure{
		ID: bldgA, DisplayName: "Tavern", Tags: []string{},
	}
	voA := &sim.VillageObject{ID: bldgA}

	cases := []struct {
		name string
		mut  func(*sim.Actor)
		want string
	}{
		{
			name: "home",
			mut:  func(a *sim.Actor) { a.HomeStructureID = bogusStr },
			want: "HomeStructureID",
		},
		{
			name: "work",
			mut:  func(a *sim.Actor) { a.WorkStructureID = bogusStr },
			want: "WorkStructureID",
		},
		{
			name: "inside",
			mut:  func(a *sim.Actor) { a.InsideStructureID = bogusStr },
			want: "InsideStructureID",
		},
		{
			name: "room",
			mut:  func(a *sim.Actor) { a.InsideRoomID = 999 }, // no Rooms loaded
			want: "InsideRoomID=999",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actor := &sim.Actor{
				ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
			}
			tc.mut(actor)

			repo := fakeRepoOpts{
				actors:         fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
				structures:     fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA}},
				villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{bldgA: voA}},
			}.build()

			_, err := LoadWorld(context.Background(), repo, true)
			if err == nil {
				t.Fatalf("expected error for missing %s ref", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestLoadWorld_ActorStructureRefs_RoomStructureMismatch — InsideRoomID
// resolves to a room that belongs to structure X, but InsideStructureID
// is Y. Locomotion / room-access invariant violated — hard error.
func TestLoadWorld_ActorStructureRefs_RoomStructureMismatch(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	structA := &sim.Structure{
		ID: bldgA, DisplayName: "Tavern", Tags: []string{},
		Rooms: []*sim.Room{
			{ID: 42, StructureID: bldgA, Kind: "common", Name: "common"},
		},
	}
	structB := &sim.Structure{
		ID: bldgB, DisplayName: "Smithy", Tags: []string{},
	}
	voA := &sim.VillageObject{ID: bldgA}
	voB := &sim.VillageObject{ID: bldgB}
	actor := &sim.Actor{
		ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
		InsideStructureID: bldgB, // wrong — room 42 is in bldgA
		InsideRoomID:      42,
	}

	repo := fakeRepoOpts{
		actors:     fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
		structures: fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA, bldgB: structB}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{
			bldgA: voA, bldgB: voB,
		}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, true)
	if err == nil || !strings.Contains(err.Error(), "belongs to structure") {
		t.Errorf("err = %v, want substring 'belongs to structure'", err)
	}
}

// TestLoadWorld_ActorStructureRefs_DuplicateRoomID — index-build
// detects RoomID collisions across structures (last-writer-wins would
// silently misroute actor refs).
func TestLoadWorld_ActorStructureRefs_DuplicateRoomID(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	structA := &sim.Structure{
		ID: bldgA, DisplayName: "Tavern", Tags: []string{},
		Rooms: []*sim.Room{{ID: 42, StructureID: bldgA, Kind: "common", Name: "common"}},
	}
	structB := &sim.Structure{
		ID: bldgB, DisplayName: "Smithy", Tags: []string{},
		Rooms: []*sim.Room{{ID: 42, StructureID: bldgB, Kind: "common", Name: "common"}}, // dup
	}
	voA := &sim.VillageObject{ID: bldgA}
	voB := &sim.VillageObject{ID: bldgB}
	actor := &sim.Actor{
		ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
	}

	repo := fakeRepoOpts{
		actors:     fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
		structures: fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA, bldgB: structB}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{
			bldgA: voA, bldgB: voB,
		}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, true)
	if err == nil || !strings.Contains(err.Error(), "duplicate RoomID=42") {
		t.Errorf("err = %v, want substring 'duplicate RoomID=42'", err)
	}
}

// TestLoadWorld_ActorStructureRefs_RoomNestingMismatch — index-build
// rejects rooms whose own StructureID disagrees with the parent
// structure they're nested under in the loaded map.
func TestLoadWorld_ActorStructureRefs_RoomNestingMismatch(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	structA := &sim.Structure{
		ID: bldgA, DisplayName: "Tavern", Tags: []string{},
		// Room claims to belong to bldgB but is nested under bldgA in the map.
		Rooms: []*sim.Room{{ID: 7, StructureID: bldgB, Kind: "common", Name: "weird"}},
	}
	voA := &sim.VillageObject{ID: bldgA}
	actor := &sim.Actor{
		ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
	}

	repo := fakeRepoOpts{
		actors:         fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
		structures:     fakeStructures{out: map[sim.StructureID]*sim.Structure{bldgA: structA}},
		villageObjects: fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{bldgA: voA}},
	}.build()

	_, err := LoadWorld(context.Background(), repo, true)
	if err == nil || !strings.Contains(err.Error(), "nested under structure") {
		t.Errorf("err = %v, want substring 'nested under structure'", err)
	}
}

// TestLoadWorld_ActorHuddleReconciliation_SkippedWhenHuddlesNotImpl —
// gating must NOT clear actor.CurrentHuddleID when Huddles failed to
// load via notImpl tolerance. Without the peer gating, the
// reconciliation would silently wipe every actor's huddle cache.
func TestLoadWorld_ActorHuddleReconciliation_SkippedWhenHuddlesNotImpl(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	actor := &sim.Actor{
		ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
		CurrentHuddleID: "h-prev", // cached from previous run; Huddles can't be loaded
	}
	repo := sim.Repository{
		Actors:               fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
		Structures:           fakeStructures{out: map[sim.StructureID]*sim.Structure{}},
		Huddles:              fakeHuddles{err: errNotImpl}, // <-- notImpl
		Scenes:               fakeScenes{out: map[sim.SceneID]*sim.Scene{}},
		Orders:               fakeOrders{out: map[sim.OrderID]*sim.Order{}},
		Environment:          fakeEnvironment{},
		Assets:               fakeAssets{out: map[sim.AssetID]*sim.Asset{}},
		Sprites:              fakeSprites{out: map[sim.SpriteID]*sim.Sprite{}},
		AttributeDefinitions: fakeAttributeDefinitions{out: map[string]*sim.AttributeDefinition{}},
		Recipes:              fakeRecipes{out: map[sim.ItemKind]*sim.ItemRecipe{}},
		ItemKinds:            fakeItemKinds{out: map[sim.ItemKind]*sim.ItemKindDef{}},
		Terrain:              fakeTerrain{out: &sim.Terrain{}},
		VillageObjects:       fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}},
		ActionLog:            fakeActionLog{},
		TickTelemetry:        fakeTickTelemetry{},
	}
	w, err := LoadWorld(context.Background(), repo, false /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if w.Actors["act-1"].CurrentHuddleID != "h-prev" {
		t.Errorf("CurrentHuddleID = %q, want preserved 'h-prev' (Huddles notImpl — must skip reconciliation)",
			w.Actors["act-1"].CurrentHuddleID)
	}
}

// TestLoadWorld_ActorStructureRefs_SkippedWhenStructuresNotImpl — same
// idea for Slice 12 carry-forward. validateActorStructureRefs would
// otherwise hard-error any actor with structure refs the moment
// Structures was tolerated empty.
func TestLoadWorld_ActorStructureRefs_SkippedWhenStructuresNotImpl(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	actor := &sim.Actor{
		ID: "act-1", DisplayName: "A", State: "idle", StateEnteredAt: startedAt,
		HomeStructureID:   bldgA,
		WorkStructureID:   bldgA,
		InsideStructureID: bldgA,
		InsideRoomID:      42,
	}
	repo := sim.Repository{
		Actors:               fakeActors{out: map[sim.ActorID]*sim.Actor{"act-1": actor}},
		Structures:           fakeStructures{err: errNotImpl}, // <-- notImpl
		Huddles:              fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{}},
		Scenes:               fakeScenes{out: map[sim.SceneID]*sim.Scene{}},
		Orders:               fakeOrders{out: map[sim.OrderID]*sim.Order{}},
		Environment:          fakeEnvironment{},
		Assets:               fakeAssets{out: map[sim.AssetID]*sim.Asset{}},
		Sprites:              fakeSprites{out: map[sim.SpriteID]*sim.Sprite{}},
		AttributeDefinitions: fakeAttributeDefinitions{out: map[string]*sim.AttributeDefinition{}},
		Recipes:              fakeRecipes{out: map[sim.ItemKind]*sim.ItemRecipe{}},
		ItemKinds:            fakeItemKinds{out: map[sim.ItemKind]*sim.ItemKindDef{}},
		Terrain:              fakeTerrain{out: &sim.Terrain{}},
		VillageObjects:       fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}},
		ActionLog:            fakeActionLog{},
		TickTelemetry:        fakeTickTelemetry{},
	}
	if _, err := LoadWorld(context.Background(), repo, false /*requireAllImpl*/); err != nil {
		t.Errorf("LoadWorld should tolerate actor structure refs when Structures notImpl: %v", err)
	}
}

// TestLoadWorld_ActorReconciliation_SkippedWhenActorsNotImpl — when
// Actors is notImpl-tolerated, the reconciliation does NOT run. A
// Huddles.Members pointing at a "missing" actor isn't an error because
// the actors aren't really missing — they just didn't load.
func TestLoadWorld_ActorReconciliation_SkippedWhenActorsNotImpl(t *testing.T) {
	startedAt := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	huddle := &sim.Huddle{
		ID: "h-a", Members: map[sim.ActorID]struct{}{"act-ghost": {}}, StartedAt: startedAt,
	}
	repo := sim.Repository{
		Actors:               notImplActors{}, // <-- notImpl, reconciliation must skip
		Structures:           fakeStructures{out: map[sim.StructureID]*sim.Structure{}},
		Huddles:              fakeHuddles{out: map[sim.HuddleID]*sim.Huddle{"h-a": huddle}},
		Scenes:               fakeScenes{out: map[sim.SceneID]*sim.Scene{}},
		Orders:               fakeOrders{out: map[sim.OrderID]*sim.Order{}},
		Environment:          fakeEnvironment{},
		Assets:               fakeAssets{out: map[sim.AssetID]*sim.Asset{}},
		Sprites:              fakeSprites{out: map[sim.SpriteID]*sim.Sprite{}},
		AttributeDefinitions: fakeAttributeDefinitions{out: map[string]*sim.AttributeDefinition{}},
		Recipes:              fakeRecipes{out: map[sim.ItemKind]*sim.ItemRecipe{}},
		ItemKinds:            fakeItemKinds{out: map[sim.ItemKind]*sim.ItemKindDef{}},
		Terrain:              fakeTerrain{out: &sim.Terrain{}},
		VillageObjects:       fakeVillageObjects{out: map[sim.VillageObjectID]*sim.VillageObject{}},
		ActionLog:            fakeActionLog{},
		TickTelemetry:        fakeTickTelemetry{},
	}

	if _, err := LoadWorld(context.Background(), repo, false); err != nil {
		t.Errorf("LoadWorld with notImpl Actors should tolerate Huddles.Members: %v", err)
	}
}

// --- Slice 3 (ZBBS-WORK-245): actor_attribute projection rebuild ----------

// TestRebuildActorAttributeProjections covers the businessowner + restock
// projections the ActorsRepo deliberately leaves to LoadWorld: keeper iff
// businessowner flavor non-empty; restock union across attributes in slug
// order with first-listed-wins on item ties; lenient skip of unparseable
// params and unknown-source entries.
func TestRebuildActorAttributeProjections(t *testing.T) {
	const (
		aKeeper  = "00000000-0000-0000-0000-00000000aaa1"
		aBlank   = "00000000-0000-0000-0000-00000000bbb2"
		aStock   = "00000000-0000-0000-0000-00000000ccc3"
		aBad     = "00000000-0000-0000-0000-00000000ddd4"
		aUnknown = "00000000-0000-0000-0000-00000000eee5"
	)
	actors := map[sim.ActorID]*sim.Actor{
		// Keeper: businessowner flavor set → BusinessownerState.
		aKeeper: {ID: aKeeper, Attributes: map[string][]byte{
			"businessowner": []byte(`{"flavor":"flamboyant"}`),
		}},
		// Empty flavor → NOT a keeper (v1 parity).
		aBlank: {ID: aBlank, Attributes: map[string][]byte{
			"businessowner": []byte(`{"flavor":""}`),
		}},
		// Restock union: "worker" lists bread(produce); "tavernkeeper"
		// re-lists bread(buy) + adds ale(buy). Slug order is
		// tavernkeeper < worker, so tavernkeeper's bread(buy) wins the tie.
		aStock: {ID: aStock, Attributes: map[string][]byte{
			"tavernkeeper": []byte(`{"restock":[{"item":"bread","source":"buy","max":4},{"item":"ale","source":"buy","target":6}]}`),
			"worker":       []byte(`{"restock":[{"item":"bread","source":"produce","max":9}]}`),
		}},
		// Unparseable params → skipped, no projection, no panic.
		aBad: {ID: aBad, Attributes: map[string][]byte{
			"businessowner": []byte("{broken"),
			"worker":        []byte("also broken"),
		}},
		// Unknown source → entry dropped.
		aUnknown: {ID: aUnknown, Attributes: map[string][]byte{
			"worker": []byte(`{"restock":[{"item":"gold","source":"mine","max":1}]}`),
		}},
	}

	rebuildActorAttributeProjections(actors)

	if bo := actors[aKeeper].BusinessownerState; bo == nil || bo.Flavor != "flamboyant" {
		t.Errorf("keeper BusinessownerState = %+v, want flavor flamboyant", bo)
	}
	if bo := actors[aBlank].BusinessownerState; bo != nil {
		t.Errorf("blank-flavor BusinessownerState = %+v, want nil", bo)
	}

	rp := actors[aStock].RestockPolicy
	if rp == nil {
		t.Fatal("stock RestockPolicy nil")
	}
	if len(rp.Restock) != 2 {
		t.Fatalf("stock entries = %d, want 2", len(rp.Restock))
	}
	byItem := map[sim.ItemKind]sim.RestockEntry{}
	for _, e := range rp.Restock {
		byItem[e.Item] = e
	}
	// bread: tavernkeeper (slug-first) wins → buy, max 4.
	if e := byItem["bread"]; e.Source != sim.RestockSourceBuy || e.Max != 4 {
		t.Errorf("bread entry = %+v, want buy/max4 (first-listed wins)", e)
	}
	if e := byItem["ale"]; e.Source != sim.RestockSourceBuy || e.Target != 6 {
		t.Errorf("ale entry = %+v, want buy/target6", e)
	}

	if actors[aBad].BusinessownerState != nil || actors[aBad].RestockPolicy != nil {
		t.Errorf("bad-params actor got projections: bo=%+v rp=%+v", actors[aBad].BusinessownerState, actors[aBad].RestockPolicy)
	}
	if rp := actors[aUnknown].RestockPolicy; rp != nil {
		t.Errorf("unknown-source RestockPolicy = %+v, want nil (entry dropped)", rp)
	}
}
