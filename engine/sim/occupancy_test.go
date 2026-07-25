package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// occupancyAsset builds an occupancy-tracked asset: an unoccupied + an occupied
// tagged state, plus the min-count / night-only knobs.
func occupancyAsset(id sim.AssetID, minCount int, nightOnly bool) *sim.Asset {
	return &sim.Asset{
		ID: id, Name: string(id), Category: "structure", DefaultState: "unoccupied",
		OccupiedMinCount: minCount, OccupiedNightOnly: nightOnly,
		States: []sim.AssetState{
			{ID: 1, State: "unoccupied", Tags: []string{sim.TagUnoccupied}},
			{ID: 2, State: "occupied", Tags: []string{sim.TagOccupied}},
		},
	}
}

// stallAsset builds the interior-less market-stall shape (LLM-534): occupancy-
// tracked, not night-only, and — the load-bearing part — a footprint whose bottom
// edge sits ABOVE the loiter pin the keeper tends it from. Mirrors the live
// `Market Stall (Fancy)` geometry (footprint 2/2/5/0, door offset -2/-4), so a
// keeper at post is outside the footprint and invisible to a headcount.
func stallAsset(id sim.AssetID) *sim.Asset {
	a := occupancyAsset(id, 1, false)
	a.FootprintLeft, a.FootprintRight, a.FootprintTop, a.FootprintBottom = 2, 2, 5, 0
	doorX, doorY := -2, -4
	a.DoorOffsetX, a.DoorOffsetY = &doorX, &doorY
	return a
}

// stallTiles returns the placement anchor, the loiter pin the keeper tends the
// stall from, and the door tile — derived the same way the engine does, so the
// test can't drift from computeLoiterTile / structureContainingTile.
func stallTiles() (anchor, pin, door sim.TilePos) {
	anchor = stallPos.Tile()
	pin = sim.TilePos{X: anchor.X + stallLoiterOffsetX, Y: anchor.Y + stallLoiterOffsetY}
	door = sim.TilePos{X: anchor.X - 2, Y: anchor.Y - 4}
	return anchor, pin, door
}

// The stall's placement and per-instance loiter offset, matching the Ellis Farm.
// The pin lands one tile BELOW the footprint's bottom edge (FootprintBottom = 0),
// which is the whole defect: it is not a footprint tile.
var stallPos = sim.WorldPos{X: 500, Y: 500}

const (
	stallLoiterOffsetX = -1
	stallLoiterOffsetY = 1
)

// buildOccupancyWorld seeds a world with five structures: a tavern (min 1), a
// workshop (min 2), an inn (night-only), a barn whose asset has no
// occupied/unoccupied states (not occupancy-tracked), and a market stall whose
// keeper's post lies outside its footprint (LLM-534). Each structure's
// placement object shares its id (shared-identity bridge). Initial phase = day.
// A capture subscriber is registered before Run.
func buildOccupancyWorld(t *testing.T) (*sim.World, *objEventCapture) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tavern-a":   occupancyAsset("tavern-a", 1, false),
		"workshop-a": occupancyAsset("workshop-a", 2, false),
		"inn-a":      occupancyAsset("inn-a", 1, true),
		"stall-a":    stallAsset("stall-a"),
		"barn-a": {
			ID: "barn-a", Name: "Barn", Category: "structure", DefaultState: "default",
			States: []sim.AssetState{{ID: 9, State: "default"}},
		},
	})
	loiterX, loiterY := stallLoiterOffsetX, stallLoiterOffsetY
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern":   {ID: "tavern", AssetID: "tavern-a", CurrentState: "unoccupied", Pos: sim.WorldPos{X: 100, Y: 100}},
		"workshop": {ID: "workshop", AssetID: "workshop-a", CurrentState: "unoccupied", Pos: sim.WorldPos{X: 200, Y: 200}},
		"inn":      {ID: "inn", AssetID: "inn-a", CurrentState: "unoccupied", Pos: sim.WorldPos{X: 300, Y: 300}},
		"barn":     {ID: "barn", AssetID: "barn-a", CurrentState: "default", Pos: sim.WorldPos{X: 400, Y: 400}},
		// DisplayName is required: ResolveLoiteringObject skips unnamed objects, so
		// an anonymous stall could never resolve a keeper standing at its pin.
		"stall": {
			ID: "stall", AssetID: "stall-a", DisplayName: "Stall",
			CurrentState: "unoccupied", Pos: stallPos,
			LoiterOffsetX: &loiterX, LoiterOffsetY: &loiterY,
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":   {ID: "tavern", DisplayName: "Tavern"},
		"workshop": {ID: "workshop", DisplayName: "Workshop"},
		"inn":      {ID: "inn", DisplayName: "Inn"},
		"barn":     {ID: "barn", DisplayName: "Barn"},
		"stall":    {ID: "stall", DisplayName: "Stall"},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	cap := &objEventCapture{}
	w.Subscribe(cap) // must precede Run
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	// Start in day so the night-only inn test exercises a real day→night flip.
	// Sent AFTER Run starts — Send blocks on the world goroutine consuming it.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Phase = sim.PhaseDay
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed phase: %v", err)
	}
	return w, cap
}

// seedActorInside adds an actor and places it inside structureID (via the index
// chokepoint, so occupancy recomputes). BreakUntil/SleepingUntil optional.
func seedActorInside(t *testing.T, w *sim.World, id sim.ActorID, structureID sim.StructureID, breakUntil, sleepUntil *time.Time) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{ID: id, DisplayName: string(id), Kind: sim.KindNPCShared, State: sim.StateIdle}
		a.BreakUntil = breakUntil
		a.SleepingUntil = sleepUntil
		world.Actors[id] = a
		sim.SetActorInsideStructure(world, a, structureID)
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedActorInside: %v", err)
	}
}

func moveActorInside(t *testing.T, w *sim.World, id sim.ActorID, structureID sim.StructureID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors[id], structureID)
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("moveActorInside: %v", err)
	}
}

func objState(w *sim.World, id sim.VillageObjectID) string {
	return w.Published().VillageObjects[id].CurrentState
}

// TestOccupancy_TavernEnterLeave: a single arrival flips the tavern to occupied
// (min 1, not night-only); the departure flips it back.
func TestOccupancy_TavernEnterLeave(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	seedActorInside(t, w, "patron", "tavern", nil, nil)
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("after arrival, tavern = %q, want occupied", got)
	}

	moveActorInside(t, w, "patron", "") // leave
	if got := objState(w, "tavern"); got != "unoccupied" {
		t.Fatalf("after departure, tavern = %q, want unoccupied", got)
	}
}

// TestOccupancy_WorkshopMinCount: occupied requires >= 2 inside.
func TestOccupancy_WorkshopMinCount(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	seedActorInside(t, w, "smith-1", "workshop", nil, nil)
	if got := objState(w, "workshop"); got != "unoccupied" {
		t.Fatalf("one worker, workshop = %q, want unoccupied (min 2)", got)
	}
	seedActorInside(t, w, "smith-2", "workshop", nil, nil)
	if got := objState(w, "workshop"); got != "occupied" {
		t.Fatalf("two workers, workshop = %q, want occupied", got)
	}
}

// TestOccupancy_InnNightOnly: a guest inside by day leaves the inn unoccupied;
// the day→night transition flips it occupied via the phase sweep; night→day
// flips it back with no one moving.
func TestOccupancy_InnNightOnly(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	seedActorInside(t, w, "guest", "inn", nil, nil)
	if got := objState(w, "inn"); got != "unoccupied" {
		t.Fatalf("guest inside by day, inn = %q, want unoccupied (night-only)", got)
	}

	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("transition night: %v", err)
	}
	if got := objState(w, "inn"); got != "occupied" {
		t.Fatalf("after dusk, inn = %q, want occupied", got)
	}

	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseDay)); err != nil {
		t.Fatalf("transition day: %v", err)
	}
	if got := objState(w, "inn"); got != "unoccupied" {
		t.Fatalf("after dawn, inn = %q, want unoccupied", got)
	}
}

// TestOccupancy_RestingExcludedNonNightOnly verifies option (b) (ZBBS-HOME-284 #2):
// in a non-night-only structure (tavern = open-for-business), a sleeping or
// on-break keeper does NOT count, so the structure darkens — the home==work
// vendor case. In a night-only structure (inn = guests lodging) everyone counts,
// so a sleeping guest keeps it lit at night.
func TestOccupancy_RestingExcludedNonNightOnly(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	future := time.Now().Add(time.Hour)

	// Tavern (non-night-only, min 1): a sleeping keeper doesn't count → dark.
	seedActorInside(t, w, "keeper", "tavern", nil, &future)
	if got := objState(w, "tavern"); got != "unoccupied" {
		t.Fatalf("sleeping keeper should not count: tavern = %q, want unoccupied", got)
	}

	// On-break also excluded for a non-night-only structure.
	seedActorInside(t, w, "breaker", "workshop", &future, nil)
	if got := objState(w, "workshop"); got != "unoccupied" {
		t.Fatalf("on-break actor should not count: workshop = %q, want unoccupied", got)
	}

	// Inn (night-only): a sleeping guest DOES count. Lit at night.
	seedActorInside(t, w, "guest", "inn", nil, &future)
	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("transition night: %v", err)
	}
	if got := objState(w, "inn"); got != "occupied" {
		t.Fatalf("sleeping guest in night-only inn should count: inn = %q, want occupied", got)
	}
}

// TestOccupancy_HomeWorkKeeperDarkensOnSleep is the end-to-end recompute-trigger
// check: a home==work tavern keeper bedded by the sleep backstop darkens the
// tavern (executeNPCSleep → refresh), and waking re-lights it (wakeNPC → refresh).
func TestOccupancy_HomeWorkKeeperDarkensOnSleep(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	// Awake home==work keeper inside the tavern (unscheduled → always off-shift).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCStateful, State: sim.StateIdle, HomeStructureID: "tavern"}
		world.Actors["keeper"] = a
		sim.SetActorInsideStructure(world, a, "tavern")
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed keeper: %v", err)
	}
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("awake keeper present: tavern = %q, want occupied", got)
	}

	// Backstop beds the off-shift at-home keeper → tavern darkens.
	if _, err := w.Send(sim.AutoBedAtHomeNPCs(time.Now().UTC())); err != nil {
		t.Fatalf("auto-bed: %v", err)
	}
	if got := objState(w, "tavern"); got != "unoccupied" {
		t.Fatalf("keeper asleep: tavern = %q, want unoccupied", got)
	}

	// Expire the sleep cap, run the wake sweep → tavern re-lights.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		past := time.Now().Add(-time.Minute)
		world.Actors["keeper"].SleepingUntil = &past
		return nil, nil
	}}); err != nil {
		t.Fatalf("expire sleep: %v", err)
	}
	if _, err := w.Send(sim.WakeExpiredNPCSleepers(time.Now().UTC())); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("keeper awake again: tavern = %q, want occupied", got)
	}
}

// seedKeeperAtPin adds a keeper who works structureID and stands OUTDOORS at
// pos — the posture a market-stall keeper holds while tending it. Goes through
// the index chokepoint with "" so outdoorActors stays consistent; that
// deliberately does NOT recompute occupancy for the stall, which is the point
// (the position is what changed, not any inside-structure attribution).
func seedKeeperAtPin(t *testing.T, w *sim.World, id sim.ActorID, structureID sim.StructureID, pos sim.TilePos) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{
			ID: id, DisplayName: string(id), Kind: sim.KindNPCShared,
			State: sim.StateIdle, WorkStructureID: structureID, Pos: pos,
		}
		world.Actors[id] = a
		sim.SetActorInsideStructure(world, a, "")
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedKeeperAtPin: %v", err)
	}
}

func sweepBusinessOccupancy(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RefreshActivePresenceOccupancyStates(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("sweepBusinessOccupancy: %v", err)
	}
}

// TestOccupancy_StallKeeperAtPostReadsOpen is the LLM-534 regression: a keeper
// tending an interior-less stall stands at its loiter pin, which is NOT a
// footprint tile, so no headcount can ever see her. The business branch reads
// businessTendedAt instead and the stall renders open while she works it; walking
// off her post shuts it again.
//
// Live shape this reproduces (2026-07-25): Elizabeth Ellis at (41,45) beside the
// Ellis Farm's pin (41,44) → closed; the stall only flickered open for the second
// she crossed its door tile.
func TestOccupancy_StallKeeperAtPostReadsOpen(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	_, pin, _ := stallTiles()

	seedKeeperAtPin(t, w, "keeper", "stall", pin)
	sweepBusinessOccupancy(t, w)
	if got := objState(w, "stall"); got != "occupied" {
		t.Fatalf("keeper at her post: stall = %q, want occupied", got)
	}

	// Confirm the pin really is outside the footprint — if it ever moves inside,
	// this test would pass for the wrong reason (a headcount would suffice).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if world.Actors["keeper"].InsideStructureID != "" {
			t.Errorf("keeper at pin %v resolved INSIDE %q — the pin is a footprint tile, so this test no longer covers the defect",
				pin, world.Actors["keeper"].InsideStructureID)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("assert outdoors: %v", err)
	}

	// Walk her well clear of the pin → nobody minding it → shut.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["keeper"].Pos = sim.TilePos{X: pin.X + 20, Y: pin.Y + 20}
		return nil, nil
	}}); err != nil {
		t.Fatalf("move keeper off post: %v", err)
	}
	sweepBusinessOccupancy(t, w)
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("keeper away: stall = %q, want unoccupied", got)
	}
}

// TestOccupancy_StallTendedPredicateExcludesSleeper: tendedness is gated on being
// awake, so a keeper dozing at her own post reads as not minding the stall.
//
// PREDICATE ONLY — it mutates State and calls the sweep directly. It deliberately
// does NOT cover the executeNPCSleep / wakeNPC hooks: those are guards for a state
// no caller can produce (both bed-down callers gate on npcSleepArmFor, which
// classifies how an actor may sleep in the structure it is currently INSIDE), so
// there is no integration path to drive them through.
func TestOccupancy_StallTendedPredicateExcludesSleeper(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	_, pin, _ := stallTiles()

	seedKeeperAtPin(t, w, "keeper", "stall", pin)
	sweepBusinessOccupancy(t, w)
	if got := objState(w, "stall"); got != "occupied" {
		t.Fatalf("awake keeper at post: stall = %q, want occupied", got)
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["keeper"].State = sim.StateSleeping
		return nil, nil
	}}); err != nil {
		t.Fatalf("sleep keeper: %v", err)
	}
	sweepBusinessOccupancy(t, w)
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("keeper asleep at post: stall = %q, want unoccupied", got)
	}
}

// TestOccupancy_LocomotionTickSweepsBusinesses proves the sweep is wired to the
// locomotion tick — the trigger that matters, because an outdoor→outdoor step
// never reaches setActorInsideStructure. The keeper stands still at her post; a
// DIFFERENT actor's walk is what drives the tick.
func TestOccupancy_LocomotionTickSweepsBusinesses(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	_, pin, _ := stallTiles()

	seedKeeperAtPin(t, w, "keeper", "stall", pin)
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("before any tick, stall = %q, want unoccupied", got)
	}

	// A walker with a live MoveIntent makes the tick do work rather than take its
	// no-movers early return.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		walker := &sim.Actor{
			ID: "walker", DisplayName: "Walker", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Pos: sim.TilePos{X: pin.X + 30, Y: pin.Y + 30},
		}
		world.Actors["walker"] = walker
		sim.SetActorInsideStructure(world, walker, "")
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed walker: %v", err)
	}
	dest := sim.TilePos{X: pin.X + 34, Y: pin.Y + 30}
	moveTo := sim.MoveDestination{Kind: sim.MoveDestinationPosition, Position: &dest}
	if _, err := w.Send(sim.MoveActor("walker", moveTo, false, time.Now())); err != nil {
		t.Fatalf("MoveActor walker: %v", err)
	}
	if _, err := w.Send(sim.EvaluateLocomotion(time.Now())); err != nil {
		t.Fatalf("EvaluateLocomotion: %v", err)
	}
	if got := objState(w, "stall"); got != "occupied" {
		t.Fatalf("after a locomotion tick, stall = %q, want occupied — the tick did not sweep businesses", got)
	}
}

// TestOccupancy_TeleportSweepsBusinesses: an operator set-position between two
// OUTDOOR tiles leaves InsideStructureID unchanged, so the index chokepoint
// early-returns, and the teleported actor has no MoveIntent so the locomotion tick
// takes its no-movers path. The non-walk reconcile has to sweep or dropping a
// keeper on her post leaves the stall shut.
func TestOccupancy_TeleportSweepsBusinesses(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	_, pin, _ := stallTiles()

	// Keeper parked well away from her stall, then teleported onto her post.
	seedKeeperAtPin(t, w, "keeper", "stall", sim.TilePos{X: pin.X + 20, Y: pin.Y + 20})
	sweepBusinessOccupancy(t, w)
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("keeper away: stall = %q, want unoccupied", got)
	}

	if _, err := w.Send(sim.SetActorPosition("keeper", pin, time.Now())); err != nil {
		t.Fatalf("SetActorPosition: %v", err)
	}
	if got := objState(w, "stall"); got != "occupied" {
		t.Fatalf("after teleport onto her post, stall = %q, want occupied — the teleport did not sweep", got)
	}
}

// TestOccupancy_WorkStructureChangeSweepsBusinesses: whether a structure is a
// business at all is decided by who works there, and reassigning a workplace moves
// both ends with nobody stepping anywhere. The stall is only occupancy-read as a
// business while it has a worker — retiring its keeper drops it back to headcount,
// which with an empty footprint means shut.
func TestOccupancy_WorkStructureChangeSweepsBusinesses(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	_, pin, _ := stallTiles()

	// A stateful NPC standing on the pin but NOT yet working the stall: no worker,
	// so the stall is not a business and headcount (zero, the pin is outside the
	// footprint) keeps it shut.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{
			ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: pin,
		}
		world.Actors["keeper"] = a
		sim.SetActorInsideStructure(world, a, "")
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed keeper: %v", err)
	}
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("no worker yet: stall = %q, want unoccupied", got)
	}

	// Take the stall on as her workplace → it becomes a business she is tending.
	if _, err := w.Send(sim.SetActorWorkStructure("keeper", "stall")); err != nil {
		t.Fatalf("SetActorWorkStructure(stall): %v", err)
	}
	if got := objState(w, "stall"); got != "occupied" {
		t.Fatalf("after taking the stall on, stall = %q, want occupied — the assignment did not sweep", got)
	}

	// Give it up again → no worker, back to headcount, shut.
	if _, err := w.Send(sim.SetActorWorkStructure("keeper", "")); err != nil {
		t.Fatalf("SetActorWorkStructure(none): %v", err)
	}
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("after giving up the stall, stall = %q, want unoccupied", got)
	}
}

// TestOccupancy_LaborSettleShutsStall drives the labor hook for real: a hired hand
// working the stall keeps it open while its owner is away (the LLM-527 presence
// rule), and the settle that ends his job shuts it — with him still standing
// there, so no movement follows to sweep it.
func TestOccupancy_LaborSettleShutsStall(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	_, pin, _ := stallTiles()

	// Owner far from her stall; hired hand at its pin on a live job for her.
	seedKeeperAtPin(t, w, "owner", "stall", sim.TilePos{X: pin.X + 25, Y: pin.Y + 25})
	workingUntil := time.Now().Add(-time.Minute) // window already elapsed
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		hand := &sim.Actor{
			ID: "hand", DisplayName: "Hand", Kind: sim.KindNPCShared,
			State: sim.StateLaboring, Pos: pin,
		}
		world.Actors["hand"] = hand
		sim.SetActorInsideStructure(world, hand, "")
		world.LaborLedger[1] = &sim.LaborOffer{
			ID: 1, WorkerID: "hand", EmployerID: "owner",
			Reward: 5, DurationMin: 30, State: sim.LaborStateWorking,
			WorkingUntil: &workingUntil,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed hand + job: %v", err)
	}
	sweepBusinessOccupancy(t, w)
	if got := objState(w, "stall"); got != "occupied" {
		t.Fatalf("hired hand at work, owner away: stall = %q, want occupied", got)
	}

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(time.Now())); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if got := objState(w, "stall"); got != "unoccupied" {
		t.Fatalf("after the job settled, stall = %q, want unoccupied — the settle did not sweep", got)
	}
}

// TestOccupancy_NightOnlyBusinessKeepsHeadcount is the guard on the scope of the
// LLM-534 change: the Tavern's asset is night-only, where occupied means GUESTS
// ARE LODGING, not "the keeper is in". Giving it the tended predicate would light
// it whenever its keeper was awake and dark with a full house of sleepers. A
// worker at a night-only structure must not switch it off headcount.
func TestOccupancy_NightOnlyBusinessKeepsHeadcount(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	future := time.Now().Add(time.Hour)

	// Innkeeper works here and is present and awake — under the tended reading
	// the inn would read occupied by day, which is exactly what must not happen.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{
			ID: "innkeeper", DisplayName: "Innkeeper", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, WorkStructureID: "inn",
		}
		world.Actors["innkeeper"] = a
		sim.SetActorInsideStructure(world, a, "inn")
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed innkeeper: %v", err)
	}
	if got := objState(w, "inn"); got != "unoccupied" {
		t.Fatalf("awake innkeeper by day: inn = %q, want unoccupied (night-only headcount)", got)
	}

	// At night, a SLEEPING guest still lights it — the headcount reading that the
	// tended predicate (which excludes sleepers) would have broken.
	seedActorInside(t, w, "guest", "inn", nil, &future)
	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("transition night: %v", err)
	}
	if got := objState(w, "inn"); got != "occupied" {
		t.Fatalf("sleeping guest at night: inn = %q, want occupied", got)
	}
}

// TestOccupancy_LoadConvergesStaleBusinessState: occupied art is derived but
// STORED on the village object and restored verbatim from the checkpoint, so a
// village that went down with a keeper at her stall came back up showing it open
// with nobody there (observed live on the James Farm). FinalizeLoad sweeps the
// non-night-only tracked structures before the first publish.
func TestOccupancy_LoadConvergesStaleBusinessState(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"stall-a": stallAsset("stall-a")})
	loiterX, loiterY := stallLoiterOffsetX, stallLoiterOffsetY
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"stall": {
			ID: "stall", AssetID: "stall-a", DisplayName: "Stall",
			CurrentState: "occupied", Pos: stallPos,
			LoiterOffsetX: &loiterX, LoiterOffsetY: &loiterY,
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{"stall": {ID: "stall", DisplayName: "Stall"}})
	// The keeper is checkpointed far from her post, so the stored "occupied" is
	// stale the moment the world wakes.
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCShared,
			State: sim.StateIdle, WorkStructureID: "stall",
			Pos: sim.TilePos{X: 10, Y: 10},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if got := w.Published().VillageObjects["stall"].CurrentState; got != "unoccupied" {
		t.Fatalf("first publish after load: stall = %q, want unoccupied — stale checkpointed state was not converged", got)
	}
}

// TestOccupancy_LoadConvergeDoesNotEmit pins the OTHER half of the load
// contract: convergence must write the corrected state WITHOUT emitting
// VillageObjectStateChanged. object_state_changed tells a client to re-render one
// object, and at load there is no client to tell — FinalizeLoad's republish
// carries the converged state in the initial snapshot instead. Routing the load
// path back through refreshStructureOccupancyState would still produce the right
// state and so would slip past TestOccupancy_LoadConvergesStaleBusinessState; this
// is the test that would catch it.
//
// The subscriber is attached BEFORE FinalizeLoad on purpose. That is not the
// production order (pg.LoadWorld runs long before the events hub subscribes), and
// the point is that the load path must not DEPEND on the production order — with a
// listener present, it still must not emit.
func TestOccupancy_LoadConvergeDoesNotEmit(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	loiterX, loiterY := stallLoiterOffsetX, stallLoiterOffsetY
	w.Assets = map[sim.AssetID]*sim.Asset{"stall-a": stallAsset("stall-a")}
	// Checkpointed "occupied" with its keeper parked far from her post, so
	// convergence has a REAL change to make (occupied -> unoccupied). A no-op
	// starting state would not distinguish direct assignment from the emitting
	// wrapper, since the wrapper returns early when nothing changes.
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		"stall": {
			ID: "stall", AssetID: "stall-a", DisplayName: "Stall",
			CurrentState: "occupied", Pos: stallPos,
			LoiterOffsetX: &loiterX, LoiterOffsetY: &loiterY,
		},
	}
	w.Structures = map[sim.StructureID]*sim.Structure{"stall": {ID: "stall", DisplayName: "Stall"}}
	w.Actors = map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCShared,
			State: sim.StateIdle, WorkStructureID: "stall",
			Pos: sim.TilePos{X: 10, Y: 10},
		},
	}

	cap := &objEventCapture{}
	w.Subscribe(cap)

	if err := w.FinalizeLoad(context.Background()); err != nil {
		t.Fatalf("FinalizeLoad: %v", err)
	}

	if got := w.Published().VillageObjects["stall"].CurrentState; got != "unoccupied" {
		t.Fatalf("after load: stall = %q, want unoccupied — convergence did not run, so the no-emit assertion below proves nothing", got)
	}
	for _, evt := range cap.snapshot() {
		if sc, ok := evt.(*sim.VillageObjectStateChanged); ok && sc.ObjectID == "stall" {
			t.Errorf("load convergence emitted VillageObjectStateChanged (%s->%s); the load path must assign CurrentState directly, not go through refreshStructureOccupancyState",
				sc.FromState, sc.ToState)
		}
	}
}

// TestOccupancy_NonTrackedAssetNoOp: a structure whose asset has no
// occupied/unoccupied states is not occupancy-tracked — arrivals don't flip it
// and emit no VillageObjectStateChanged for it.
func TestOccupancy_NonTrackedAssetNoOp(t *testing.T) {
	w, cap := buildOccupancyWorld(t)

	seedActorInside(t, w, "hand", "barn", nil, nil)
	if got := objState(w, "barn"); got != "default" {
		t.Fatalf("barn = %q, want default (not occupancy-tracked)", got)
	}
	for _, evt := range cap.snapshot() {
		if sc, ok := evt.(*sim.VillageObjectStateChanged); ok && sc.ObjectID == "barn" {
			t.Errorf("unexpected VillageObjectStateChanged for non-tracked barn: %+v", sc)
		}
	}
}

// TestOccupancy_EmitsStateChange: a flip emits VillageObjectStateChanged so the
// client gets an object_state_changed frame.
func TestOccupancy_EmitsStateChange(t *testing.T) {
	w, cap := buildOccupancyWorld(t)

	seedActorInside(t, w, "patron", "tavern", nil, nil)

	var found *sim.VillageObjectStateChanged
	for _, evt := range cap.snapshot() {
		if sc, ok := evt.(*sim.VillageObjectStateChanged); ok && sc.ObjectID == "tavern" {
			found = sc
		}
	}
	if found == nil {
		t.Fatal("no VillageObjectStateChanged emitted for the tavern flip")
	}
	if found.ToState != "occupied" || found.FromState != "unoccupied" {
		t.Errorf("event = %s->%s, want unoccupied->occupied", found.FromState, found.ToState)
	}
}
