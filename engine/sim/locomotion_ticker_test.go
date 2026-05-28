package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildLocomotionTestWorld seeds a running world for ticker tests:
//
//   - all-grass terrain
//   - "cottage": a non-obstacle 1x1 structure at world (320,320), anchor
//     tile (PadX+10, PadY+10). Its asset's door offset is (0,0), so the
//     door tile IS the anchor — standing on it flips InsideStructureID.
//     A loiter offset of (0,5) pushes the visitor-slot ring clear of the
//     footprint so a StructureVisit arrival lands outside the structure.
//   - "walker": an actor parked at (PadX+5, PadY+5) with open grass to
//     every target.
//
// The returned eventRec captures every emitted event.
func buildLocomotionTestWorld(t *testing.T) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"cottage-asset": {ID: "cottage-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"cottage": {
			ID: "cottage", AssetID: "cottage-asset", Pos: sim.WorldPos{X: 320, Y: 320},
			LoiterOffsetX: intp(0), LoiterOffsetY: intp(5),
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"cottage": {ID: "cottage", DisplayName: "Cottage"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", DisplayName: "Walker", Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel, rec
}

// actorSpatial reads an actor's tile + InsideStructureID inside a command.
func actorSpatial(t *testing.T, w *sim.World, id sim.ActorID) (sim.Position, sim.StructureID) {
	t.Helper()
	type st struct {
		pos    sim.Position
		inside sim.StructureID
	}
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors[id]
			return st{pos: sim.Position{X: a.Pos.X, Y: a.Pos.Y}, inside: a.InsideStructureID}, nil
		},
	})
	if err != nil {
		t.Fatalf("actorSpatial: %v", err)
	}
	s := res.(st)
	return s.pos, s.inside
}

// tickLoco runs one locomotion tick through the command channel.
func tickLoco(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.EvaluateLocomotion(now)); err != nil {
		t.Fatalf("EvaluateLocomotion: %v", err)
	}
}

// driveToArrival ticks until the actor's MoveIntent clears or maxTicks is
// hit. Returns the tick count consumed.
func driveToArrival(t *testing.T, w *sim.World, id sim.ActorID, now time.Time, maxTicks int) int {
	t.Helper()
	for i := 1; i <= maxTicks; i++ {
		tickLoco(t, w, now)
		if moveIntentOf(t, w, id) == nil {
			return i
		}
	}
	t.Fatalf("%s did not finish moving within %d ticks", id, maxTicks)
	return 0
}

// TestLocomotion_PositionWalkToArrival covers a Position move end to end:
// the actor advances one tile per tick, emits one ActorMoved per step,
// and on arrival emits ActorArrived, clears MoveIntent, and is stamped an
// arrival warrant.
func TestLocomotion_PositionWalkToArrival(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// (PadX+5,PadY+5) → (PadX+5,PadY+2): 3 tiles straight north.
	dest := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 2}
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(dest), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	ticks := driveToArrival(t, w, "walker", now, 20)
	if ticks != 3 {
		t.Errorf("arrived in %d ticks, want 3", ticks)
	}

	pos, inside := actorSpatial(t, w, "walker")
	if pos != dest {
		t.Errorf("final position = %+v, want %+v", pos, dest)
	}
	if inside != "" {
		t.Errorf("InsideStructureID = %q, want empty (open ground)", inside)
	}

	moved := rec.countEvents(func(e sim.Event) bool {
		m, ok := e.(*sim.ActorMoved)
		return ok && m.ActorID == "walker"
	})
	if moved != 3 {
		t.Errorf("ActorMoved count = %d, want 3", moved)
	}
	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.FinalPosition == dest && a.MovementAttemptID == 1
	})
	if arrived != 1 {
		t.Errorf("matching ActorArrived count = %d, want 1", arrived)
	}
}

// TestLocomotion_StructureEnterArrival covers a StructureEnter move: the
// actor walks onto the structure's door tile, InsideStructureID flips,
// the actorsByStructure index follows, and ActorArrived carries the
// structure.
func TestLocomotion_StructureEnterArrival(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)

	pos, inside := actorSpatial(t, w, "walker")
	doorTile := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}
	if pos != doorTile {
		t.Errorf("final position = %+v, want door tile %+v", pos, doorTile)
	}
	if inside != "cottage" {
		t.Errorf("InsideStructureID = %q, want cottage", inside)
	}

	// The actorsByStructure index must have moved in lockstep.
	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.ActorsInStructure(world, "cottage"), nil
		},
	})
	inIdx := res.([]sim.ActorID)
	if len(inIdx) != 1 || inIdx[0] != "walker" {
		t.Errorf("actorsByStructure[cottage] = %v, want [walker]", inIdx)
	}

	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.FinalStructureID == "cottage"
	})
	if arrived != 1 {
		t.Errorf("ActorArrived{cottage} count = %d, want 1", arrived)
	}
}

// TestLocomotion_StructureVisitArrival covers a StructureVisit move: the
// actor stops at a visitor slot around the loiter pin, outside the
// structure's footprint — so FinalStructureID is empty.
func TestLocomotion_StructureVisitArrival(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureVisitDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)

	pos, inside := actorSpatial(t, w, "walker")
	if inside != "" {
		t.Errorf("InsideStructureID = %q, want empty (visitor slot is outside)", inside)
	}
	// The loiter pin is at (PadX+10, PadY+15); the slot must be one of the
	// eight king's-move tiles around it.
	pin := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 15}
	dx, dy := pos.X-pin.X, pos.Y-pin.Y
	if dx < -1 || dx > 1 || dy < -1 || dy > 1 || (dx == 0 && dy == 0) {
		t.Errorf("final position %+v is not a visitor slot around pin %+v", pos, pin)
	}
	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.FinalStructureID == ""
	})
	if arrived != 1 {
		t.Errorf("ActorArrived (no structure) count = %d, want 1", arrived)
	}
}

// TestLocomotion_BilateralPauseInHuddle covers the bilateral pause: an
// actor that holds a MoveIntent but is also in an active huddle does not
// advance — and resumes once it leaves the huddle.
func TestLocomotion_BilateralPauseInHuddle(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 1}), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	// Pull the walker into a huddle while it has an in-flight MoveIntent.
	if _, err := w.Send(sim.JoinHuddle("walker", "cottage", "", now)); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}
	startPos, _ := actorSpatial(t, w, "walker")

	for i := 0; i < 5; i++ {
		tickLoco(t, w, now)
	}
	pausedPos, _ := actorSpatial(t, w, "walker")
	if pausedPos != startPos {
		t.Errorf("walker moved while in a huddle: %+v -> %+v", startPos, pausedPos)
	}
	if moveIntentOf(t, w, "walker") == nil {
		t.Error("MoveIntent was cleared during the bilateral pause")
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorMoved)
		return ok
	}); n != 0 {
		t.Errorf("ActorMoved fired %d times during the pause, want 0", n)
	}

	// Leave the huddle — locomotion resumes on the next tick.
	if _, err := w.Send(sim.LeaveHuddle("walker", now)); err != nil {
		t.Fatalf("LeaveHuddle: %v", err)
	}
	tickLoco(t, w, now)
	resumedPos, _ := actorSpatial(t, w, "walker")
	if resumedPos == startPos {
		t.Error("walker did not resume moving after leaving the huddle")
	}
}

// TestLocomotion_SoftBlocker_ReroutesAroundOccupant covers the
// re-plan-on-soft-block path (ZBBS-WORK-340): when another actor stands on
// the mover's straight-line next tile but a lateral detour is open, the
// mover takes the detour on the SAME tick instead of stalling.
// MoveIntent.StuckTicks stays at 0.
func TestLocomotion_SoftBlocker_ReroutesAroundOccupant(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// walker at (PadX+5,PadY+5) heading straight north — direct next tile
	// would be (PadX+5,PadY+4). Park a blocker there; the lateral tiles
	// (PadX+4|PadX+6, PadY+5) are open grass, so re-planning with the
	// direct tile masked off yields a viable detour.
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["blocker"] = &sim.Actor{
				ID:          "blocker",
				DisplayName: "Abraham",
				Pos:         sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 4},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	if _, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 1}), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	tickLoco(t, w, now)

	pos, _ := actorSpatial(t, w, "walker")
	if pos == (sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}) {
		t.Errorf("walker stalled on a soft blocker instead of re-routing; at %+v", pos)
	}
	if pos == (sim.Position{X: sim.PadX + 5, Y: sim.PadY + 4}) {
		t.Errorf("walker stepped onto the occupied tile %+v", pos)
	}
	dx, dy := pos.X-(sim.PadX+5), pos.Y-(sim.PadY+5)
	if !((dx == -1 && dy == 0) || (dx == 1 && dy == 0)) {
		t.Errorf("walker took an unexpected detour: at %+v (expected one tile east or west of origin)", pos)
	}
	intent := moveIntentOf(t, w, "walker")
	if intent == nil {
		t.Fatal("MoveIntent was cleared on a re-route step")
	}
	if intent.StuckTicks != 0 {
		t.Errorf("StuckTicks = %d after a successful re-route, want 0", intent.StuckTicks)
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		m, ok := e.(*sim.ActorMoved)
		return ok && m.ActorID == "walker"
	}); n != 1 {
		t.Errorf("ActorMoved count = %d on the re-route tick, want 1", n)
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.ActorMoveStopped)
		return ok && s.ActorID == "walker"
	}); n != 0 {
		t.Errorf("ActorMoveStopped fired on a successful re-route: %d", n)
	}
}

// TestLocomotion_SoftBlocker_HardStopsAfterStuckThreshold covers the
// deadlock hard-stop (ZBBS-WORK-340): when a mover soft-blocks AND the
// re-plan with the occupant tile masked off also finds no detour, the
// stuck counter accumulates and after DeadlockStuckThreshold consecutive
// ticks the mover emits MoveStoppedDeadlocked, the MoveIntent clears, and
// a DeadlockEntry lands on World.DeadlockSnapshot with replan_failed=true
// (the no-detour-exists branch).
func TestLocomotion_SoftBlocker_HardStopsAfterStuckThreshold(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Destination IS the blocker's tile. The original FindPath returns a
	// 2-tile path [start, goal]; path[1] equals the goal, which is the
	// occupied tile. Re-planning with that tile masked off makes the goal
	// itself impassable so FindPath returns nil — exactly the
	// sleeping-Abraham-in-the-only-doorway shape.
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["blocker"] = &sim.Actor{
				ID:          "blocker",
				DisplayName: "Abraham",
				Pos:         sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 4},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	dest := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 4}
	if _, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(dest), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	// Tick up to (threshold - 1) times: the counter should accumulate but no
	// Deadlocked event fires yet.
	for i := 1; i < sim.DeadlockStuckThreshold; i++ {
		tickLoco(t, w, now)
		intent := moveIntentOf(t, w, "walker")
		if intent == nil {
			t.Fatalf("MoveIntent cleared on tick %d (before threshold)", i)
		}
		if intent.StuckTicks != i {
			t.Errorf("StuckTicks after tick %d = %d, want %d", i, intent.StuckTicks, i)
		}
		if n := rec.countEvents(func(e sim.Event) bool {
			s, ok := e.(*sim.ActorMoveStopped)
			return ok && s.ActorID == "walker"
		}); n != 0 {
			t.Errorf("ActorMoveStopped fired before threshold (after tick %d): %d", i, n)
		}
		if got := w.DeadlockSnapshot(); len(got) != 0 {
			t.Errorf("DeadlockSnapshot non-empty before threshold (after tick %d): %d", i, len(got))
		}
	}

	// One more tick trips the threshold: Deadlocked event + ring entry.
	tickLoco(t, w, now)

	if intent := moveIntentOf(t, w, "walker"); intent != nil {
		t.Errorf("MoveIntent survived the deadlock hard-stop: %+v", intent)
	}
	stopped := rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.ActorMoveStopped)
		return ok && s.ActorID == "walker" && s.Reason == sim.MoveStoppedDeadlocked
	})
	if stopped != 1 {
		t.Errorf("ActorMoveStopped{deadlocked} count = %d, want 1", stopped)
	}

	entries := w.DeadlockSnapshot()
	if len(entries) != 1 {
		t.Fatalf("DeadlockSnapshot length = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.MoverID != "walker" {
		t.Errorf("MoverID = %q, want walker", got.MoverID)
	}
	if got.OccupantID != "blocker" {
		t.Errorf("OccupantID = %q, want blocker", got.OccupantID)
	}
	if got.OccupantName != "Abraham" {
		t.Errorf("OccupantName = %q, want Abraham", got.OccupantName)
	}
	if got.OccupantTile != (sim.Position{X: sim.PadX + 5, Y: sim.PadY + 4}) {
		t.Errorf("OccupantTile = %+v, want %+v", got.OccupantTile, sim.Position{X: sim.PadX + 5, Y: sim.PadY + 4})
	}
	if !got.ReplanFailed {
		t.Error("ReplanFailed = false, want true (the goal itself is blocked — no detour exists)")
	}
	if got.DestinationKind != sim.MoveDestinationPosition {
		t.Errorf("DestinationKind = %q, want position", got.DestinationKind)
	}
	if got.DestPosition != dest {
		t.Errorf("DestPosition = %+v, want %+v", got.DestPosition, dest)
	}
}

// TestLocomotion_SoftBlocker_StuckCounterResetsOnSuccessfulStep covers the
// reset rule (ZBBS-WORK-340): once a mover takes a successful one-tile
// step (whether direct or via re-plan), MoveIntent.StuckTicks resets to 0.
// Without the reset, a mover stuck N-1 ticks → advances → restuck-N-1
// would deadlock-stop in 2 ticks instead of N; we accumulate ticks and
// then verify a successful step zeroes the counter directly.
func TestLocomotion_SoftBlocker_StuckCounterResetsOnSuccessfulStep(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Walker at (PadX+5,PadY+5), destination two tiles north (PadX+5,PadY+3).
	// Four actor blockers fence off every cardinal neighbor of the walker
	// — the iterative-mask re-plan widens up to all four and still finds
	// every first-step soft-blocked, so the stuck counter ticks up.
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["north_blocker"] = &sim.Actor{
				ID: "north_blocker", DisplayName: "North",
				Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 4},
			}
			world.Actors["west_blocker"] = &sim.Actor{
				ID: "west_blocker", DisplayName: "West",
				Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY + 5},
			}
			world.Actors["east_blocker"] = &sim.Actor{
				ID: "east_blocker", DisplayName: "East",
				Pos: sim.TilePos{X: sim.PadX + 6, Y: sim.PadY + 5},
			}
			world.Actors["south_blocker"] = &sim.Actor{
				ID: "south_blocker", DisplayName: "South",
				Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 6},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed blockers: %v", err)
	}
	if _, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 3}), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	// Accumulate three ticks of stuck. (Stay under DeadlockStuckThreshold
	// so the walker hasn't hard-stopped yet.)
	for i := 1; i <= 3; i++ {
		tickLoco(t, w, now)
		intent := moveIntentOf(t, w, "walker")
		if intent == nil {
			t.Fatalf("MoveIntent cleared on tick %d", i)
		}
		if intent.StuckTicks != i {
			t.Fatalf("StuckTicks after tick %d = %d, want %d", i, intent.StuckTicks, i)
		}
	}

	// Move east_blocker off the lateral detour. Next tick, the re-plan
	// finds a step via (PadX+6,PadY+5).
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["east_blocker"].Pos.X = sim.PadX + 50
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("move east_blocker: %v", err)
	}

	tickLoco(t, w, now)

	pos, _ := actorSpatial(t, w, "walker")
	if pos != (sim.Position{X: sim.PadX + 6, Y: sim.PadY + 5}) {
		t.Errorf("walker did not take the freed lateral detour: at %+v, want %+v", pos, sim.Position{X: sim.PadX + 6, Y: sim.PadY + 5})
	}
	intent := moveIntentOf(t, w, "walker")
	if intent == nil {
		t.Fatal("MoveIntent cleared on the reset step (walker has not arrived yet)")
	}
	if intent.StuckTicks != 0 {
		t.Errorf("StuckTicks after a successful step = %d, want 0", intent.StuckTicks)
	}
}

// TestLocomotion_InvalidatedWhenStructureRemoved covers the invalidated
// stop: a StructureEnter whose target structure is removed mid-walk emits
// ActorMoveStopped{invalidated} and clears the MoveIntent.
func TestLocomotion_InvalidatedWhenStructureRemoved(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	tickLoco(t, w, now) // one step en route

	// Remove the cottage out from under the walker.
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			delete(world.Structures, "cottage")
			delete(world.VillageObjects, "cottage")
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("remove cottage: %v", err)
	}
	tickLoco(t, w, now)

	if moveIntentOf(t, w, "walker") != nil {
		t.Error("MoveIntent survived destination invalidation")
	}
	stopped := rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.ActorMoveStopped)
		return ok && s.ActorID == "walker" && s.Reason == sim.MoveStoppedInvalidated
	})
	if stopped != 1 {
		t.Errorf("ActorMoveStopped{invalidated} count = %d, want 1", stopped)
	}
}

// TestArrivedAtDestination_StructureVisitRingMembership covers the
// arrival semantics for StructureVisit: ANY of the eight visitor-slot
// ring tiles counts as arrived, not just the one pickVisitorSlot
// currently prefers. The pin tile itself and a non-ring tile do not.
func TestArrivedAtDestination_StructureVisitRingMembership(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()

	// cottage loiter offset is (0,5) → pin at (PadX+10, PadY+15).
	pin := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 15}
	dest := sim.NewStructureVisitDestination("cottage")

	arrivedAt := func(pos sim.Position) bool {
		res, err := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				world.Actors["walker"].Pos.X = pos.X
				world.Actors["walker"].Pos.Y = pos.Y
				return sim.ArrivedAtDestination(world, world.Actors["walker"], dest), nil
			},
		})
		if err != nil {
			t.Fatalf("ArrivedAtDestination: %v", err)
		}
		return res.(bool)
	}

	// All eight ring tiles count as arrived.
	for _, off := range sim.VisitorSlotOffsets {
		ring := sim.Position{X: pin.X + off.X, Y: pin.Y + off.Y}
		if !arrivedAt(ring) {
			t.Errorf("ring tile %+v should count as arrived", ring)
		}
	}
	// The pin tile itself is the gathering centre, not a slot.
	if arrivedAt(pin) {
		t.Error("the loiter pin tile should NOT count as arrived")
	}
	// A tile well clear of the ring is not arrived.
	if arrivedAt(sim.Position{X: pin.X + 5, Y: pin.Y + 5}) {
		t.Error("a tile off the ring should not count as arrived")
	}
}

// TestEvaluateLocomotion_DeterministicContention covers the sorted-iteration
// fix: when two movers contend for the same tile in one tick, the actor
// with the lexicographically smaller ID advances and the other
// soft-blocks — reproducibly, independent of map iteration order.
func TestEvaluateLocomotion_DeterministicContention(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// "aaa" at (PadX+20,PadY+5) and "zzz" at (PadX+20,PadY+3) both target
	// the tile between them, (PadX+20,PadY+4) — a one-tick contention, on
	// an empty column clear of the seeded "walker".
	contended := sim.Position{X: sim.PadX + 20, Y: sim.PadY + 4}
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["aaa"] = &sim.Actor{ID: "aaa", Pos: sim.TilePos{X: sim.PadX + 20, Y: sim.PadY + 5}}
			world.Actors["zzz"] = &sim.Actor{ID: "zzz", Pos: sim.TilePos{X: sim.PadX + 20, Y: sim.PadY + 3}}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed contenders: %v", err)
	}
	if _, err := w.Send(sim.MoveActor("aaa", sim.NewPositionDestination(contended), false, now)); err != nil {
		t.Fatalf("MoveActor aaa: %v", err)
	}
	if _, err := w.Send(sim.MoveActor("zzz", sim.NewPositionDestination(contended), false, now)); err != nil {
		t.Fatalf("MoveActor zzz: %v", err)
	}

	tickLoco(t, w, now)

	// aaa (smaller ID) is processed first, steps onto the contended tile
	// and arrives; zzz then soft-blocks on the now-occupied tile.
	aaaPos, _ := actorSpatial(t, w, "aaa")
	if aaaPos != contended {
		t.Errorf("aaa should have advanced onto %+v, got %+v", contended, aaaPos)
	}
	if moveIntentOf(t, w, "aaa") != nil {
		t.Error("aaa should have arrived (MoveIntent cleared)")
	}
	zzzPos, _ := actorSpatial(t, w, "zzz")
	if zzzPos != (sim.Position{X: sim.PadX + 20, Y: sim.PadY + 3}) {
		t.Errorf("zzz should have soft-blocked at its start tile, got %+v", zzzPos)
	}
	if moveIntentOf(t, w, "zzz") == nil {
		t.Error("zzz should still be moving (soft block preserves MoveIntent)")
	}
}

// TestClassifyTileBlocker covers the hard/soft/clear classification.
func TestClassifyTileBlocker(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()

	// Mark one tile as deep water so it is hard-blocked.
	waterTile := sim.Position{X: sim.PadX + 30, Y: sim.PadY + 30}
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Terrain.Data[waterTile.Y*sim.MapW+waterTile.X] = sim.TerrainDeepWater
			world.Actors["sitter"] = &sim.Actor{ID: "sitter", Pos: sim.TilePos{X: sim.PadX + 7, Y: sim.PadY + 7}}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	type res struct{ hard, soft bool }
	classify := func(tile sim.Position, mover sim.ActorID) res {
		out, err := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				grid, gerr := sim.BuildWalkGrid(world)
				if gerr != nil {
					return nil, gerr
				}
				h, s := sim.ClassifyTileBlocker(grid, world, tile, mover)
				return res{hard: h, soft: s}, nil
			},
		})
		if err != nil {
			t.Fatalf("classify: %v", err)
		}
		return out.(res)
	}

	if r := classify(waterTile, "walker"); !r.hard || r.soft {
		t.Errorf("water tile: got hard=%v soft=%v, want hard=true soft=false", r.hard, r.soft)
	}
	sitterTile := sim.Position{X: sim.PadX + 7, Y: sim.PadY + 7}
	if r := classify(sitterTile, "walker"); r.hard || !r.soft {
		t.Errorf("occupied tile: got hard=%v soft=%v, want hard=false soft=true", r.hard, r.soft)
	}
	// The occupant excepting itself reads the tile as clear.
	if r := classify(sitterTile, "sitter"); r.hard || r.soft {
		t.Errorf("occupant's own tile: got hard=%v soft=%v, want both false", r.hard, r.soft)
	}
	clearTile := sim.Position{X: sim.PadX + 40, Y: sim.PadY + 40}
	if r := classify(clearTile, "walker"); r.hard || r.soft {
		t.Errorf("clear tile: got hard=%v soft=%v, want both false", r.hard, r.soft)
	}
}

// containResult is the flattened return of structureContainingTile, used
// so the test command Fn and its caller agree on a single named type.
type containResult struct {
	sid sim.StructureID
	ok  bool
}

// TestStructureContainingTile covers the footprint-containment lookup.
func TestStructureContainingTile(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()

	check := func(pos sim.Position) (sim.StructureID, bool) {
		out, err := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				sid, ok := sim.StructureContainingTile(world, pos)
				return containResult{sid: sid, ok: ok}, nil
			},
		})
		if err != nil {
			t.Fatalf("StructureContainingTile: %v", err)
		}
		r := out.(containResult)
		return r.sid, r.ok
	}

	// cottage anchor (PadX+10,PadY+10), footprint extents all 0 → the
	// anchor tile is the whole footprint.
	if sid, ok := check(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}); !ok || sid != "cottage" {
		t.Errorf("anchor tile: got %q ok=%v, want cottage true", sid, ok)
	}
	if _, ok := check(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 11}); ok {
		t.Error("tile just south of the 1x1 footprint should not be contained")
	}
	if _, ok := check(sim.Position{X: sim.PadX, Y: sim.PadY}); ok {
		t.Error("far-off tile should not be contained")
	}
}

// TestLocomotionTicker_GoroutineDrivesMovement covers the real AfterFunc
// self-rearm chain end to end: RunLocomotionTicker as a goroutine
// actually advances a moving actor without the test driving ticks.
func TestLocomotionTicker_GoroutineDrivesMovement(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	tickerCtx, tickerCancel := context.WithCancel(context.Background())
	defer tickerCancel()
	go sim.RunLocomotionTicker(tickerCtx, w)

	dest := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 2}
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(dest), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	// Three tiles at a 200ms cadence — give it generous headroom.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			pos, _ := actorSpatial(t, w, "walker")
			t.Fatalf("ticker goroutine did not deliver walker to %+v (stuck at %+v)", dest, pos)
		default:
		}
		if moveIntentOf(t, w, "walker") == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	pos, _ := actorSpatial(t, w, "walker")
	if pos != dest {
		t.Errorf("ticker goroutine left walker at %+v, want %+v", pos, dest)
	}
}
