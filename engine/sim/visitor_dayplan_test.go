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

// TestVisitorCircuit_RoutesToOpenBusiness — the spawned traveler heads to the open
// business (keeper present), not the tavern fallback.
func TestVisitorCircuit_RoutesToOpenBusiness(t *testing.T) {
	loc := et(t)
	vw := newVisitorWorld()
	vw.seedTavern(t)
	const smithy sim.StructureID = "smithy"
	vw.seedBusiness(t, smithy, "Blacksmith", sim.WorldPos{X: 288, Y: 320})
	w, cancel := vw.load(t)
	defer cancel()
	seedDayPlanSettings(t, w, loc)

	tickCircuit(t, w, time.Date(2026, 7, 12, 15, 0, 0, 0, loc))
	got := firstVisitor(t, w)
	if got == nil || got.VisitorState.RoundTarget != smithy {
		t.Fatalf("RoundTarget = %v, want the open business %q", got, smithy)
	}
}

// TestVisitorCircuit_VisitCountsOnlyOnInteriorArrival — the code_review #2 guard: a
// round is marked made only when the traveler is confirmed INSIDE the shop. A walk
// that ended outside (StructureVisit fallback) drops the leg without marking it, so
// the shop is retried rather than skipped forever.
func TestVisitorCircuit_VisitCountsOnlyOnInteriorArrival(t *testing.T) {
	loc := et(t)
	day := time.Date(2026, 7, 12, 15, 0, 0, 0, loc)

	// Case A: arrived INSIDE → the round is marked, dwell starts.
	vwA := newVisitorWorld()
	vwA.seedTavern(t)
	const smithy sim.StructureID = "smithy"
	vwA.seedBusiness(t, smithy, "Blacksmith", sim.WorldPos{X: 288, Y: 320})
	wA, cancelA := vwA.load(t)
	defer cancelA()
	seedDayPlanSettings(t, wA, loc)
	tickCircuit(t, wA, day)
	setVisitorState(t, wA, func(a *sim.Actor) {
		a.MoveIntent = nil
		a.InsideStructureID = smithy // arrived inside
	})
	tickCircuit(t, wA, day)
	if got := firstVisitor(t, wA); got == nil ||
		len(got.VisitorState.VisitedBusinesses) != 1 || got.VisitorState.VisitedBusinesses[0] != smithy ||
		got.VisitorState.DwellUntil == nil {
		t.Errorf("interior arrival: visited=%v dwell=%v; want [smithy] + dwell set",
			visitedOf(got), dwellSet(got))
	}

	// Case B: the walk ended OUTSIDE (no interior) → the shop is NOT marked visited.
	vwB := newVisitorWorld()
	vwB.seedTavern(t)
	vwB.seedBusiness(t, smithy, "Blacksmith", sim.WorldPos{X: 288, Y: 320})
	wB, cancelB := vwB.load(t)
	defer cancelB()
	seedDayPlanSettings(t, wB, loc)
	tickCircuit(t, wB, day)
	setVisitorState(t, wB, func(a *sim.Actor) {
		a.MoveIntent = nil
		a.InsideStructureID = "" // ended outside — StructureVisit fallback / failed path
	})
	tickCircuit(t, wB, day)
	if got := firstVisitor(t, wB); got == nil || len(got.VisitorState.VisitedBusinesses) != 0 {
		t.Errorf("outside arrival: visited=%v; want none (the shop must not be skipped)", visitedOf(got))
	}
}

func visitedOf(a *sim.ActorSnapshot) []sim.StructureID {
	if a == nil || a.VisitorState == nil {
		return nil
	}
	return a.VisitorState.VisitedBusinesses
}
func dwellSet(a *sim.ActorSnapshot) bool {
	return a != nil && a.VisitorState != nil && a.VisitorState.DwellUntil != nil
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
