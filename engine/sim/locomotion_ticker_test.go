package sim_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestLocomotionPace_MatchesV1Speed pins the engine's visible walk speed at
// v1's 48 world-pixels/sec (the v1 const `defaultNPCSpeed = 48.0` in the
// deleted-but-still-in-tree engine/npc_movement.go). v2's pacing model
// ties speed to cadence — speed = TileSize / LocomotionTickInterval — so
// changing either constant changes the speed; PR-4 (the rewrite) silently
// did exactly that, picking 200ms on architectural symmetry without
// back-checking v1's effective pace, and the 3.33x speed-up went unnoticed
// for months because no player was at the keyboard. ZBBS-WORK-341 reset
// the constant; this regression guard catches a future re-introduction.
func TestLocomotionPace_MatchesV1Speed(t *testing.T) {
	const v1NPCSpeedPxPerSec = 48.0
	got := float64(sim.TileSize) / sim.LocomotionTickInterval.Seconds()
	if math.Abs(got-v1NPCSpeedPxPerSec) > 0.001 {
		t.Fatalf("visible walk speed = %.4f px/s (TileSize=%v, LocomotionTickInterval=%v), want %.1f px/s — v1's defaultNPCSpeed (engine/npc_movement.go). If this is intentional, update the constants in lockstep with the client's village_api.gd LOCOMOTION_TICK_SECONDS.",
			got, sim.TileSize, sim.LocomotionTickInterval, v1NPCSpeedPxPerSec)
	}
}

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

// TestLocomotion_InsideChangeEmitsBroadcast covers the ZBBS-WORK-373 inside-state
// push end to end: entering the cottage emits ActorInsideChanged{cottage}, and
// walking back out to open ground emits ActorInsideChanged{} (empty). The leave
// case is the Finding-6 fix — the client needs this frame to un-stick a keeper
// from "inside the structure" the instant they walk away, since the v2 engine
// otherwise pushes no inside flip between the walk-start and arrival brackets.
func TestLocomotion_InsideChangeEmitsBroadcast(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Enter the cottage: the ""→cottage flip emits exactly one inside-change.
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor enter: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)
	if _, inside := actorSpatial(t, w, "walker"); inside != "cottage" {
		t.Fatalf("after enter: InsideStructureID = %q, want cottage", inside)
	}
	enteredCottage := rec.countEvents(func(e sim.Event) bool {
		c, ok := e.(*sim.ActorInsideChanged)
		return ok && c.ActorID == "walker" && c.InsideStructureID == "cottage"
	})
	if enteredCottage != 1 {
		t.Errorf("ActorInsideChanged{cottage} count = %d, want 1", enteredCottage)
	}

	// Walk back out to open ground: the cottage→"" flip is the leave the client
	// must hear to render the keeper walking away rather than stuck inside.
	out := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(out), false, now)); err != nil {
		t.Fatalf("MoveActor leave: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)
	if _, inside := actorSpatial(t, w, "walker"); inside != "" {
		t.Fatalf("after leave: InsideStructureID = %q, want empty", inside)
	}
	leftStructure := rec.countEvents(func(e sim.Event) bool {
		c, ok := e.(*sim.ActorInsideChanged)
		return ok && c.ActorID == "walker" && c.InsideStructureID == ""
	})
	if leftStructure != 1 {
		t.Errorf("ActorInsideChanged{} (leave) count = %d, want 1", leftStructure)
	}
}

// TestLocomotion_StructureEnter_WalkThroughOccupiedDoor covers the
// last-resort door walk-through (ZBBS-HOME-348). A member entering a
// structure whose single door tile is occupied by another actor — the
// household-lockout shape, a resident parked or sleeping on the one
// reachable interior tile — steps onto the door anyway after the reroute
// exhausts, arrives (InsideStructureID flips), and records NO deadlock.
// This recovers v1's overlap-on-the-door behavior that v2's soft-block
// collision (ZBBS-WORK-340) regressed, scoped to StructureEnter only.
func TestLocomotion_StructureEnter_WalkThroughOccupiedDoor(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Park a blocker ON the cottage door tile (PadX+10,PadY+10) — the single
	// reachable interior tile. Without the walk-through the entering walker
	// would deadlock against it forever.
	doorTile := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			// Make the walker a RESIDENT of the cottage — the walk-through is
			// gated on membership (structureMembershipAllows), so a non-member
			// would (correctly) deadlock instead. See the negative test below.
			world.Actors["walker"].HomeStructureID = "cottage"
			world.Actors["blocker"] = &sim.Actor{
				ID:          "blocker",
				DisplayName: "Hope",
				Pos:         sim.TilePos{X: doorTile.X, Y: doorTile.Y},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 60)

	pos, inside := actorSpatial(t, w, "walker")
	if pos != doorTile {
		t.Errorf("final position = %+v, want door tile %+v (walked through onto it)", pos, doorTile)
	}
	if inside != "cottage" {
		t.Errorf("InsideStructureID = %q, want cottage (arrived via walk-through)", inside)
	}

	// The overlap is intentional: the blocker stays put on the door and both
	// actors now share that tile (recovering v1's actors-may-overlap behavior).
	blockerPos, _ := actorSpatial(t, w, "blocker")
	if blockerPos != doorTile {
		t.Errorf("blocker moved off the door tile: at %+v, want %+v", blockerPos, doorTile)
	}
	if pos != blockerPos {
		t.Errorf("walker (%+v) and blocker (%+v) should share the door tile", pos, blockerPos)
	}

	// A walk-through is a successful entry, not a wedge: no deadlock entry,
	// no Deadlocked stop event.
	if got := w.DeadlockSnapshot(); len(got) != 0 {
		t.Errorf("DeadlockSnapshot = %d entries, want 0 (walk-through is not a deadlock)", len(got))
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.ActorMoveStopped)
		return ok && s.ActorID == "walker" && s.Reason == sim.MoveStoppedDeadlocked
	}); n != 0 {
		t.Errorf("ActorMoveStopped{deadlocked} fired on a walk-through entry: %d", n)
	}
	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.FinalStructureID == "cottage"
	})
	if arrived != 1 {
		t.Errorf("ActorArrived{cottage} count = %d, want 1", arrived)
	}
}

// TestLocomotion_StructureEnter_NonMemberWalksThroughOpenDoorAfterWindow covers
// the generalized walk-through (ZBBS-HOME-327): a NON-member entering an OPEN
// structure whose single door tile is occupied no longer deadlocks. HOME-348's
// immediate walk-through is still member-gated and does not fire, so the
// non-member instead waits out the stuck window and then forces through the
// blocker onto the door — arriving inside. This is the live Josiah→Tavern
// shape: a customer (non-member) at an open business whose keeper sleeps across
// the door. Entry policy is NOT bypassed: the cottage is open, so the entrant is
// authorized (see TestLocomotion_StructureEnter_NonMemberCannotWalkThroughOwnerOnly
// for the owner-only guard).
func TestLocomotion_StructureEnter_NonMemberWalksThroughOpenDoorAfterWindow(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Walker is left a NON-member (no HomeStructureID/WorkStructureID/ownership
	// for "cottage"). The cottage is open, so resolvePathTarget hands out the
	// door tile as the goal and the entrant is authorized to be there.
	doorTile := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["blocker"] = &sim.Actor{
				ID:          "blocker",
				DisplayName: "Hope",
				Pos:         sim.TilePos{X: doorTile.X, Y: doorTile.Y},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 60)

	pos, inside := actorSpatial(t, w, "walker")
	if inside != "cottage" {
		t.Errorf("non-member did not walk through into the open cottage (InsideStructureID=%q, want cottage)", inside)
	}
	if pos != doorTile {
		t.Errorf("non-member final position = %+v, want the door tile %+v (stepped through the blocker)", pos, doorTile)
	}
	// Walk-through, not give-up: arrival fires, no deadlock stop event.
	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.FinalStructureID == "cottage"
	})
	if arrived != 1 {
		t.Errorf("ActorArrived{cottage} count = %d, want 1", arrived)
	}
	stopped := rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.ActorMoveStopped)
		return ok && s.ActorID == "walker" && s.Reason == sim.MoveStoppedDeadlocked
	})
	if stopped != 0 {
		t.Errorf("ActorMoveStopped{deadlocked} count = %d, want 0 (walks through, does not give up)", stopped)
	}
	// The stable block was recorded as the contention canary before the walk-through.
	if got := w.DeadlockSnapshot(); len(got) == 0 {
		t.Error("expected a DeadlockSnapshot canary entry for the stable door block")
	}
}

// TestLocomotion_StructureEnter_NonMemberCannotWalkThroughOwnerOnly is the
// entry-policy guard that survives ZBBS-HOME-327: the walk-through can only fire
// for an actor that already holds a valid MoveIntent toward an authorized
// target, and a non-member NEVER gets one for an owner-only structure —
// MoveActor rejects the StructureEnter at command time (resolvePathTarget also
// rejects it independently). So there is no MoveIntent for the locomotion ticker
// to walk through, and the non-member can never be dropped inside a private home
// via the occupied-door fallback.
func TestLocomotion_StructureEnter_NonMemberCannotWalkThroughOwnerOnly(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Make the cottage owner-only and park a blocker on its door. The walker
	// remains a non-member.
	doorTile := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.VillageObjects["cottage"].EntryPolicy = sim.EntryPolicyOwner
			world.Actors["blocker"] = &sim.Actor{
				ID:          "blocker",
				DisplayName: "Hope",
				Pos:         sim.TilePos{X: doorTile.X, Y: doorTile.Y},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed owner-only cottage + blocker: %v", err)
	}

	// MoveActor must reject the non-member's StructureEnter outright.
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err == nil {
		t.Fatal("MoveActor accepted a non-member StructureEnter into an owner-only structure; want rejection")
	}

	// No MoveIntent was stamped, so nothing for the ticker to walk through.
	if intent := moveIntentOf(t, w, "walker"); intent != nil {
		t.Errorf("MoveIntent stamped despite rejected owner-only entry: %+v", intent)
	}
	// Tick a few times anyway — the walker must never end up inside the cottage.
	for i := 0; i < sim.DeadlockStuckThreshold+2; i++ {
		tickLoco(t, w, now)
	}
	if _, inside := actorSpatial(t, w, "walker"); inside == "cottage" {
		t.Errorf("non-member ended up inside owner-only cottage (InsideStructureID=%q)", inside)
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

// TestLocomotion_MoverInHuddleStillAdvances pins the post-ZBBS-HOME-340
// invariant: the old "bilateral pause" is gone, so an actor that holds a
// MoveIntent keeps advancing even if it is also in an active huddle. The
// pause used to freeze such an actor until it left the huddle, which
// permanently wedged a player (who never re-ticks to re-decide) once a
// passerby was pulled into a huddle mid-walk. Encounters no longer pull a
// mover into a huddle at all; this test guards the locomotion side of the
// same invariant — a mover is never suspended.
func TestLocomotion_MoverInHuddleStillAdvances(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 1}), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	// Force the walker into a huddle while it has an in-flight MoveIntent —
	// the residual moving-while-huddled state the old pause used to freeze.
	if _, err := w.Send(sim.JoinHuddle("walker", "cottage", "", now)); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}
	startPos, _ := actorSpatial(t, w, "walker")

	tickLoco(t, w, now)

	movedPos, _ := actorSpatial(t, w, "walker")
	if movedPos == startPos {
		t.Errorf("walker did not advance while in a huddle: still at %+v (bilateral pause should be gone)", startPos)
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorMoved)
		return ok
	}); n == 0 {
		t.Error("ActorMoved did not fire — the mover was suspended instead of advancing")
	}
	// The ticker leaves the huddle unconditionally before advancing a mover,
	// so the walker is no longer huddled.
	if huddleID := huddleIDOf(t, w, "walker"); huddleID != "" {
		t.Errorf("walker should have left the huddle on the advancing tick, still in %q", huddleID)
	}
}

// TestArrivalEncounter_RealWalkToArrivalFormsHuddle drives a real walk to
// arrival next to a stationary actor and asserts the arrival encounter
// huddle forms. It locks the ordering invariant ZBBS-HOME-340 depends on:
// finishArrival clears the arriver's MoveIntent BEFORE emitting
// ActorArrived. If that order ever flips, the arriver would still hold a
// MoveIntent when handleArrivalEncounter runs, outdoorEncounterExcludesActor
// would drop it as the initiator, and the greeting would silently never
// form. (The other arrival-encounter tests synthesize ActorArrived and so
// cannot catch this.)
func TestArrivalEncounter_RealWalkToArrivalFormsHuddle(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		// walker arrives one tile north of the stationary witness.
		"walker":  {ID: "walker", DisplayName: "Walker", Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5}},
		"witness": {ID: "witness", DisplayName: "Witness", Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 9}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterEncounter(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("RegisterEncounter: %v", err)
	}

	now := time.Now().UTC()
	if _, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 8}), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 10)

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("expected the arrival to form 1 huddle with the stationary witness, got %d "+
			"(a zero count means the arriver still held a MoveIntent when ActorArrived fired)",
			st.activeHuddleCount)
	}
	if h := st.memberToHuddleIDs["walker"]; h == "" || h != st.memberToHuddleIDs["witness"] {
		t.Errorf("walker and witness should share the arrival huddle, got walker=%q witness=%q",
			st.memberToHuddleIDs["walker"], st.memberToHuddleIDs["witness"])
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

// TestLocomotion_SoftBlocker_WalksThroughAfterStuckThreshold covers the
// last-resort walk-through (ZBBS-HOME-327, replacing the ZBBS-WORK-340 hard-
// stop): when a mover soft-blocks AND the re-plan with the occupant tile
// masked off also finds no detour, the stuck counter accumulates; after
// DeadlockStuckThreshold consecutive ticks the mover records a DeadlockEntry
// (replan_failed=true — the no-detour-exists branch, kept as the contention
// canary) and then steps THROUGH the blocker onto the goal tile rather than
// freezing. Here the destination IS the blocker's tile, so the walk-through
// step also completes arrival: ActorArrived fires, MoveIntent clears via
// arrival (not a hard-stop), and NO MoveStoppedDeadlocked is emitted.
func TestLocomotion_SoftBlocker_WalksThroughAfterStuckThreshold(t *testing.T) {
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

	// One more tick trips the threshold: the canary entry is recorded and the
	// mover walks through the blocker. Since the blocker's tile IS the goal,
	// the walk-through step lands the mover on the destination and arrival
	// fires in the same tick.
	tickLoco(t, w, now)

	// The mover reached the goal — it did NOT freeze. MoveIntent clears via
	// arrival, not a hard-stop.
	if intent := moveIntentOf(t, w, "walker"); intent != nil {
		t.Errorf("MoveIntent survived arrival via walk-through: %+v", intent)
	}
	pos, _ := actorSpatial(t, w, "walker")
	if pos != dest {
		t.Errorf("walker did not walk through onto the goal: at %+v, want %+v", pos, dest)
	}
	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.FinalPosition == dest
	})
	if arrived != 1 {
		t.Errorf("ActorArrived count = %d, want 1 (walk-through completed arrival)", arrived)
	}
	// No deadlock hard-stop event — the walk-through replaced it.
	stopped := rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.ActorMoveStopped)
		return ok && s.ActorID == "walker" && s.Reason == sim.MoveStoppedDeadlocked
	})
	if stopped != 0 {
		t.Errorf("ActorMoveStopped{deadlocked} count = %d, want 0 (walk-through, not give-up)", stopped)
	}

	// The deadlock entry is still recorded as the contention canary.
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

// TestLocomotion_SoftBlocker_DeadlockEntryReplanFailedFalse_OnMutualBlockShape
// covers the ReplanFailed classification on the mutual-block / clogged-
// corridor flavor (ZBBS-WORK-340). When every cardinal neighbor is
// soft-blocked by a separate actor, the iterative-mask re-plan finds
// detours on early passes (each with a soft-blocked first step) and
// only returns nil once the mask saturates the neighborhood — that nil
// is "no detour after also masking everywhere else," NOT "no detour
// exists at all." The recorded entry must read replan_failed=false so
// operators can distinguish "relocate a sleeper" from "the corridor's
// just clogged."
func TestLocomotion_SoftBlocker_DeadlockEntryReplanFailedFalse_OnMutualBlockShape(t *testing.T) {
	w, cancel, _ := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Walker at (PadX+5,PadY+5), destination two tiles north (PadX+5,PadY+3).
	// All four cardinal neighbors occupied by distinct actors — the goal
	// itself is OPEN grass, so on attempt 0 FindPathBlocking (with just
	// north masked) returns a valid path via a lateral. The first-step
	// soft-block then widens the mask through all four neighbors before
	// the planner runs out.
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

	// Drive past the threshold.
	for i := 0; i < sim.DeadlockStuckThreshold; i++ {
		tickLoco(t, w, now)
	}

	entries := w.DeadlockSnapshot()
	if len(entries) != 1 {
		t.Fatalf("DeadlockSnapshot length = %d, want 1", len(entries))
	}
	if entries[0].ReplanFailed {
		t.Error("ReplanFailed = true, want false — detours existed (first FindPathBlocking returned a non-nil path), they just all had occupied first steps")
	}

	// ZBBS-HOME-327: the boxed-in mover did not freeze. At the threshold it
	// walked THROUGH the straight-line blocker (north, the path[1] toward the
	// goal) and kept its MoveIntent — the goal (two tiles north) is not yet
	// reached, so the walk continues next tick.
	pos, _ := actorSpatial(t, w, "walker")
	overlapTile := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 4}
	if pos != overlapTile {
		t.Errorf("walker did not walk through onto the north blocker tile: at %+v, want %+v", pos, overlapTile)
	}
	if moveIntentOf(t, w, "walker") == nil {
		t.Error("MoveIntent cleared — the walk-through must preserve it (goal not yet reached)")
	}

	// One more tick: the mover must step OFF the overlapped tile and continue
	// toward the goal — not stay stacked on the blocker or oscillate. The goal
	// tile (one north of the overlap) is clear, so it advances straight onto it
	// and arrives. This is the next-tick recovery from a forced overlap.
	tickLoco(t, w, now)
	goal := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 3}
	pos, _ = actorSpatial(t, w, "walker")
	if pos != goal {
		t.Errorf("walker did not continue off the overlap to the goal: at %+v, want %+v", pos, goal)
	}
	if moveIntentOf(t, w, "walker") != nil {
		t.Error("MoveIntent should clear on arrival at the goal after the walk-through")
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

// TestArrivedAtDestination_StructureVisit covers the arrival semantics for
// StructureVisit: a visitor is arrived when within LoiterAttributionTiles
// (Chebyshev <= 1) of the loiter pin — the pin tile itself (Chebyshev 0) and
// all eight visitor-slot ring tiles (Chebyshev 1). A tile beyond the ring is
// not arrived.
//
// ZBBS-HOME-329: this arm previously checked ring membership ONLY and rejected
// the pin tile, but pickVisitorSlotAtPin returns the pin tile as the
// all-slots-blocked last resort. A visitor parked there never registered
// arrival → finishArrival never ran → the mover looped on the pin forever. The
// arm now mirrors the ObjectVisit arm (Chebyshev <= LoiterAttributionTiles), so
// the pin-tile case below = arrived (this is the assertion that flipped).
func TestArrivedAtDestination_StructureVisit(t *testing.T) {
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

	// All eight ring tiles (Chebyshev 1) count as arrived.
	for _, off := range sim.VisitorSlotOffsets {
		ring := sim.Position{X: pin.X + off.X, Y: pin.Y + off.Y}
		if !arrivedAt(ring) {
			t.Errorf("ring tile %+v should count as arrived", ring)
		}
	}
	// The pin tile itself (Chebyshev 0) — the all-slots-blocked last-resort slot
	// pickVisitorSlotAtPin can return — now counts as arrived. ZBBS-HOME-329.
	if !arrivedAt(pin) {
		t.Error("the loiter pin tile (Chebyshev 0) should count as arrived (329 fix)")
	}
	// A tile beyond the ring (Chebyshev 2) is not arrived.
	if arrivedAt(sim.Position{X: pin.X + 2, Y: pin.Y}) {
		t.Error("a tile beyond the ring (Chebyshev 2) should not count as arrived")
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

	// Three tiles at the LocomotionTickInterval cadence — give it generous
	// headroom (5s covers v1's 2/3-sec/tile + arming + scheduler jitter).
	deadline := time.After(5 * time.Second)
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
