package sim_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitor_dayplan_test.go — LLM-373 behavioral coverage for the traveler day-plan
// through a running world: the daytime spawn gate, spawn-seeded pack, daybreak-
// anchored departure, and the dusk turn to lodging.

func et(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

// seedDayPlanSettings forces chance=1000 (spawn roll always fires) and a
// 06:00–18:00 village day in America/New_York, so the daytime gate + daybreak
// anchoring are exercised deterministically.
func seedDayPlanSettings(t *testing.T, w *sim.World, loc *time.Location) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorSpawnChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 1 // one visitor → deterministic firstVisitor across ticks
		world.Settings.DawnTime = "06:00"
		world.Settings.DuskTime = "18:00"
		world.Settings.Location = loc
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
}

func firstVisitor(t *testing.T, w *sim.World) *sim.ActorSnapshot {
	t.Helper()
	for _, a := range w.Published().Actors {
		if a.VisitorState != nil {
			return a
		}
	}
	return nil
}

func firstVisitorID(t *testing.T, w *sim.World) sim.ActorID {
	t.Helper()
	for id, a := range w.Published().Actors {
		if a.VisitorState != nil {
			return id
		}
	}
	return ""
}

func TestVisitorSpawn_DayPlanSeeds(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	now := time.Date(2026, 7, 12, 15, 0, 0, 0, loc) // 15:00 — daytime
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))}))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 {
		t.Fatalf("Spawned = %d, want 1 (reason=%q)", tm.Spawned, tm.SpawnSkipReason)
	}

	got := firstVisitor(t, w)
	if got == nil {
		t.Fatal("no visitor after daytime spawn")
	}
	if got.VisitorState.Phase != sim.VisitorPhaseArriving {
		t.Errorf("spawn phase = %q, want arriving", got.VisitorState.Phase)
	}
	// Pack: at least one ware seeded, and a purse in the seeded range.
	wares := 0
	for _, q := range got.Inventory {
		wares += q
	}
	if wares == 0 {
		t.Error("spawned traveler carries no wares")
	}
	if got.Coins < 30 || got.Coins > 50 {
		t.Errorf("purse = %d, want [30,50]", got.Coins)
	}
	// Departure anchored to the next daybreak: 2026-07-13 06:00 ET.
	wantDepart := time.Date(2026, 7, 13, 6, 0, 0, 0, loc)
	if !got.VisitorState.ExpiresAt.Equal(wantDepart) {
		t.Errorf("ExpiresAt = %v, want next daybreak %v", got.VisitorState.ExpiresAt, wantDepart)
	}
}

func TestVisitorSpawn_SkippedAtNight(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	night := time.Date(2026, 7, 12, 22, 0, 0, 0, loc) // 22:00 — after dusk
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: night, Rand: rand.New(rand.NewSource(7))}))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.Spawned != 0 {
		t.Errorf("Spawned = %d at night, want 0 (daytime gate)", tm.Spawned)
	}
	if firstVisitor(t, w) != nil {
		t.Error("a visitor spawned at night despite the daytime gate")
	}
}

// seedVisitorSprites seeds one npc_sprite per distinct name referenced by
// VisitorArchetypeSprite, keyed by a synthetic id — mirroring the live catalog
// (id = uuid, Name = display name) so visitorSpriteID resolves whatever archetype
// the spawn rolls. Call before load().
func (vw *visitorWorld) seedVisitorSprites(t *testing.T) {
	t.Helper()
	sprites := map[sim.SpriteID]*sim.Sprite{}
	for _, name := range sim.VisitorArchetypeSprite {
		id := sim.SpriteID("sprite-" + name) // unique per name; stands in for the uuid PK
		sprites[id] = &sim.Sprite{ID: id, Name: name}
	}
	vw.handles.Sprites.Seed(sprites)
}

// TestVisitorSpawn_SetsSprite — LLM-379: a spawned traveler carries a non-empty
// SpriteID resolved from its archetype (and a Facing), so the client draws it
// instead of nothing.
func TestVisitorSpawn_SetsSprite(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	vw.seedTavern(t)
	vw.seedVisitorSprites(t)
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	now := time.Date(2026, 7, 12, 15, 0, 0, 0, loc)
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))})); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got := firstVisitor(t, w)
	if got == nil {
		t.Fatal("no visitor after daytime spawn")
	}
	if got.SpriteID == "" {
		t.Fatalf("spawned traveler has empty SpriteID — renders invisible (archetype=%q)", got.VisitorState.Archetype)
	}
	// The resolved sprite is the one mapped for its archetype.
	wantName := sim.VisitorArchetypeSprite[got.VisitorState.Archetype]
	if want := sim.SpriteID("sprite-" + wantName); got.SpriteID != want {
		t.Errorf("SpriteID = %q, want %q (archetype %q → %q)", got.SpriteID, want, got.VisitorState.Archetype, wantName)
	}
	if got.Facing == "" {
		t.Error("spawned traveler has empty Facing")
	}
}

// TestVisitorSpawn_MissingSpriteCatalog — a spawn with no sprite for the archetype
// (empty catalog) logs and ships the traveler spriteless rather than crashing: a
// missing sheet must never be fatal to the spawn.
func TestVisitorSpawn_MissingSpriteCatalog(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	vw.seedTavern(t)
	// No seedVisitorSprites — the catalog is empty.
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	now := time.Date(2026, 7, 12, 15, 0, 0, 0, loc)
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))}))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 {
		t.Fatalf("Spawned = %d, want 1 despite missing sprite (reason=%q)", tm.Spawned, tm.SpawnSkipReason)
	}
	got := firstVisitor(t, w)
	if got == nil {
		t.Fatal("visitor was dropped when its sprite was missing; want spawned spriteless")
	}
	if got.SpriteID != "" {
		t.Errorf("SpriteID = %q, want empty (no catalog seeded)", got.SpriteID)
	}
}

// seedBusiness places a shop (asset + VillageObject + Structure) with a present
// keeper inside it — the minimum for keeperPresentAt(shop) to read true, so the
// circuit routes a traveler there. Call before load().
func (vw *visitorWorld) seedBusiness(t *testing.T, id sim.StructureID, name string, pos sim.WorldPos) {
	t.Helper()
	assetID := sim.AssetID(string(id) + "-asset")
	vw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		assetID: {ID: assetID, Category: "structure", DoorOffsetX: intpV(1), DoorOffsetY: intpV(2)},
	})
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(id): {
			ID: sim.VillageObjectID(id), AssetID: assetID, Pos: pos, EntryPolicy: sim.EntryPolicyOpen,
		},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		id: {ID: id, DisplayName: name},
	})
	keeperID := sim.ActorID("keeper-" + string(id))
	vw.handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		keeperID: {
			ID:                 keeperID,
			DisplayName:        name + " Keeper",
			Kind:               sim.KindNPCStateful,
			State:              sim.StateIdle,
			WorkStructureID:    id,
			InsideStructureID:  id,
			Pos:                pos.Tile(),
			Needs:              map[sim.NeedKey]int{},
			Inventory:          map[sim.ItemKind]int{},
			BusinessownerState: &sim.BusinessownerState{Flavor: "smith"},
		},
	})
}

// setVisitorState mutates the single in-flight visitor's actor state on the world
// goroutine — used to simulate an arrival (or a failed arrival) without driving the
// real multi-tick walk.
func setVisitorState(t *testing.T, w *sim.World, mutate func(a *sim.Actor)) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, a := range world.Actors {
			if a.VisitorState != nil {
				mutate(a)
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("mutate visitor: %v", err)
	}
}

func tickCircuit(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))})); err != nil {
		t.Fatalf("circuit tick: %v", err)
	}
}

// TestVisitorSpawn_EngineDoesNotPickShop — LLM-379: the engine no longer chooses the
// traveler's stops. With a shop open, a spawned visitor is walked to the neutral village
// anchor (the tavern), never the shop, and the engine marks nothing visited — he chooses
// his own rounds with move_to.
func TestVisitorSpawn_EngineDoesNotPickShop(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	tavern := vw.seedTavern(t)
	const smithy sim.StructureID = "smithy"
	vw.seedBusiness(t, smithy, "Blacksmith", sim.WorldPos{X: 288, Y: 320})
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	tickCircuit(t, w, time.Date(2026, 7, 12, 15, 0, 0, 0, loc))
	got := firstVisitor(t, w)
	if got == nil {
		t.Fatal("no visitor spawned")
	}
	// The engine's one walk is to the anchor, never the shop.
	if got.MoveDestStructureID == smithy {
		t.Errorf("engine routed the visitor to the shop %q; it must not pick his stops", smithy)
	}
	if got.MoveDestStructureID != tavern {
		t.Errorf("spawn move dest = %q, want the neutral anchor %q", got.MoveDestStructureID, tavern)
	}
	if len(got.VisitorState.VisitedBusinesses) != 0 {
		t.Errorf("engine marked %v visited at spawn; recording is arrival-driven only", got.VisitorState.VisitedBusinesses)
	}
}

// TestRecordVisitorArrival — LLM-379: VisitedBusinesses is written only on a genuine
// co-present arrival at a keeper-business, never for a shut shop, the inn, or an evening
// (lodging) arrival. This is the sole writer now that the engine picks no destinations.
func TestRecordVisitorArrival(t *testing.T) {
	loc := et(t)
	day := time.Date(2026, 7, 12, 15, 0, 0, 0, loc)
	const smithy sim.StructureID = "smithy"

	newWorld := func(t *testing.T) (*sim.World, func(), sim.ActorID) {
		vw := newVisitorWorld()
		vw.seedTavern(t)
		vw.seedBusiness(t, smithy, "Blacksmith", sim.WorldPos{X: 288, Y: 320})
		w, cancel := vw.load(t)
		seedDayPlanSettings(t, w, loc)
		tickCircuit(t, w, day) // spawn
		id := firstVisitorID(t, w)
		if id == "" {
			cancel()
			t.Fatal("no visitor spawned")
		}
		return w, cancel, id
	}
	record := func(t *testing.T, w *sim.World, id sim.ActorID, sid sim.StructureID) {
		if _, err := w.Send(sim.RecordVisitorArrival(id, sid)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// atSmithy puts the visitor co-present inside the smithy — the location the recording
	// gate now requires (a shop is marked only when he is actually there).
	atSmithy := func(t *testing.T, w *sim.World) {
		setVisitorState(t, w, func(a *sim.Actor) { a.InsideStructureID = smithy })
	}

	t.Run("co-present keeper-shop is recorded", func(t *testing.T) {
		w, cancel, id := newWorld(t)
		defer cancel()
		atSmithy(t, w)
		record(t, w, id, smithy)
		if got := firstVisitor(t, w); got == nil || len(visitedOf(got)) != 1 || visitedOf(got)[0] != smithy {
			t.Fatalf("visited=%v, want [smithy]", visitedOf(got))
		}
	})

	t.Run("not co-present (elsewhere) is not recorded", func(t *testing.T) {
		w, cancel, id := newWorld(t)
		defer cancel()
		// The visitor is still out by the anchor, NOT at the smithy; a stale / misrouted
		// arrival (or a stray direct call) must record nothing even though the keeper is
		// present — else a shop is "visited" the traveler never reached.
		record(t, w, id, smithy)
		if got := firstVisitor(t, w); got == nil || len(visitedOf(got)) != 0 {
			t.Fatalf("visited=%v, want none (visitor isn't at the shop)", visitedOf(got))
		}
	})

	t.Run("shut shop (keeper away) is not recorded", func(t *testing.T) {
		w, cancel, id := newWorld(t)
		defer cancel()
		atSmithy(t, w)
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			for _, a := range world.Actors {
				if a.WorkStructureID == smithy {
					a.InsideStructureID = ""
					a.Pos = sim.TilePos{X: sim.PadX + 1, Y: sim.PadY + 1} // keeper wandered far off
				}
			}
			return nil, nil
		}}); err != nil {
			t.Fatal(err)
		}
		record(t, w, id, smithy)
		if got := firstVisitor(t, w); got == nil || len(visitedOf(got)) != 0 {
			t.Fatalf("visited=%v, want none (shut shop)", visitedOf(got))
		}
	})

	t.Run("evening arrival is lodging, not a round", func(t *testing.T) {
		w, cancel, id := newWorld(t)
		defer cancel()
		atSmithy(t, w)
		setVisitorState(t, w, func(a *sim.Actor) { a.VisitorState.Phase = sim.VisitorPhaseLodging })
		record(t, w, id, smithy)
		if got := firstVisitor(t, w); got == nil || len(visitedOf(got)) != 0 {
			t.Fatalf("visited=%v, want none (evening — lodging, not rounds)", visitedOf(got))
		}
	})
}

func visitedOf(a *sim.ActorSnapshot) []sim.StructureID {
	if a == nil || a.VisitorState == nil {
		return nil
	}
	return a.VisitorState.VisitedBusinesses
}

func TestVisitorCircuit_DuskTurnsToLodging(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	// Spawn in the afternoon.
	day := time.Date(2026, 7, 12, 15, 0, 0, 0, loc)
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: day, Rand: rand.New(rand.NewSource(7))})); err != nil {
		t.Fatalf("spawn tick: %v", err)
	}
	if got := firstVisitor(t, w); got == nil || got.VisitorState.Phase != sim.VisitorPhaseArriving {
		t.Fatalf("post-spawn phase = %v, want arriving", got)
	}

	// A later tick, now past dusk (no more spawn, so chance stays high but the visitor
	// is at cap): the circuit turns the in-flight traveler to the lodging phase.
	evening := time.Date(2026, 7, 12, 19, 30, 0, 0, loc)
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: evening, Rand: rand.New(rand.NewSource(7))})); err != nil {
		t.Fatalf("evening tick: %v", err)
	}
	got := firstVisitor(t, w)
	if got == nil {
		t.Fatal("visitor vanished")
	}
	if got.VisitorState.Phase != sim.VisitorPhaseLodging {
		t.Errorf("evening phase = %q, want lodging", got.VisitorState.Phase)
	}
}
