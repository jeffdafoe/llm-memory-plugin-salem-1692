package sim_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildSetPositionTestWorld seeds a running world for SetActorPosition tests:
//
//   - all-grass terrain
//   - "hut": an OBSTACLE structure at world (320,320) — anchor tile
//     (PadX+10, PadY+10), 3x3 footprint, door carved at the footprint's
//     south edge (anchor + (0,1)). Interior tiles are unwalkable; the
//     door tile is walkable and flips inside-structure attribution.
//   - "walker": an actor parked at the pad origin on open ground.
//
// The returned eventRec captures every emitted event.
func buildSetPositionTestWorld(t *testing.T) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"hut-asset": {
			ID: "hut-asset", Category: "structure",
			IsObstacle:    true,
			FootprintLeft: 1, FootprintRight: 1, FootprintTop: 1, FootprintBottom: 1,
			DoorOffsetX: intp(0), DoorOffsetY: intp(1),
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"hut": {ID: "hut", AssetID: "hut-asset", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"hut": {ID: "hut", DisplayName: "Hut"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", DisplayName: "Walker", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
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

// hutAnchor is the hut's anchor tile: WorldToTile floors 320/32 = 10 onto
// the pad origin. The 3x3 footprint covers anchor±1 on both axes; the door
// is carved at anchor + (0,1).
var hutAnchor = sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}

// TestSetActorPosition_TeleportsToOpenGround: the happy path — position
// lands, attribution stays outdoors, ActorTeleported fires once.
func TestSetActorPosition_TeleportsToOpenGround(t *testing.T) {
	w, cancel, rec := buildSetPositionTestWorld(t)
	defer cancel()
	target := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}

	res, err := w.Send(sim.SetActorPosition("walker", target, time.Now()))
	if err != nil {
		t.Fatalf("SetActorPosition: %v", err)
	}
	out, ok := res.(sim.SetActorPositionResult)
	if !ok {
		t.Fatalf("result type = %T, want SetActorPositionResult", res)
	}
	if out.To != target || out.From != (sim.Position{X: sim.PadX, Y: sim.PadY}) {
		t.Errorf("result = %+v, want from pad origin to %v", out, target)
	}
	if out.InsideStructureID != "" || out.MoveCancelled || out.LeftHuddleID != "" {
		t.Errorf("result = %+v, want outdoors / no cancel / no huddle", out)
	}

	snap := w.Published()
	if got := snap.Actors["walker"].Pos; got != target {
		t.Errorf("walker pos = %v, want %v", got, target)
	}
	teleports := rec.countEvents(func(e sim.Event) bool {
		_, is := e.(*sim.ActorTeleported)
		return is
	})
	if teleports != 1 {
		t.Errorf("ActorTeleported count = %d, want 1", teleports)
	}
}

// TestSetActorPosition_RefusesUnwalkableTargets: a footprint-interior tile
// and an out-of-bounds tile are both refused with ErrTileNotWalkable, and
// the actor does not move.
func TestSetActorPosition_RefusesUnwalkableTargets(t *testing.T) {
	w, cancel, rec := buildSetPositionTestWorld(t)
	defer cancel()

	for _, target := range []sim.Position{
		hutAnchor,            // obstacle footprint interior
		{X: -1, Y: sim.PadY}, // out of bounds
	} {
		_, err := w.Send(sim.SetActorPosition("walker", target, time.Now()))
		if !errors.Is(err, sim.ErrTileNotWalkable) {
			t.Errorf("target %v: err = %v, want ErrTileNotWalkable", target, err)
		}
	}

	snap := w.Published()
	if got := snap.Actors["walker"].Pos; got != (sim.Position{X: sim.PadX, Y: sim.PadY}) {
		t.Errorf("walker moved to %v on a refused teleport", got)
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		_, is := e.(*sim.ActorTeleported)
		return is
	}); n != 0 {
		t.Errorf("ActorTeleported count = %d, want 0", n)
	}
}

// TestSetActorPosition_UnknownActor: a missing actor id is ErrActorNotFound.
func TestSetActorPosition_UnknownActor(t *testing.T) {
	w, cancel, _ := buildSetPositionTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.SetActorPosition("ghost", sim.Position{X: sim.PadX, Y: sim.PadY}, time.Now()))
	if !errors.Is(err, sim.ErrActorNotFound) {
		t.Errorf("err = %v, want ErrActorNotFound", err)
	}
}

// TestSetActorPosition_DoorTileAttributesInside: teleporting onto the hut's
// carved door tile reconciles InsideStructureID to the hut (and emits the
// ActorInsideChanged flip); teleporting back out clears it.
func TestSetActorPosition_DoorTileAttributesInside(t *testing.T) {
	w, cancel, rec := buildSetPositionTestWorld(t)
	defer cancel()
	door := sim.Position{X: hutAnchor.X, Y: hutAnchor.Y + 1}

	res, err := w.Send(sim.SetActorPosition("walker", door, time.Now()))
	if err != nil {
		t.Fatalf("teleport to door: %v", err)
	}
	if out := res.(sim.SetActorPositionResult); out.InsideStructureID != "hut" {
		t.Errorf("InsideStructureID = %q, want hut", out.InsideStructureID)
	}
	if got := w.Published().Actors["walker"].InsideStructureID; got != "hut" {
		t.Errorf("walker InsideStructureID = %q, want hut", got)
	}
	insideFlips := rec.countEvents(func(e sim.Event) bool {
		c, is := e.(*sim.ActorInsideChanged)
		return is && c.InsideStructureID == "hut"
	})
	if insideFlips != 1 {
		t.Errorf("ActorInsideChanged{hut} count = %d, want 1", insideFlips)
	}

	res, err = w.Send(sim.SetActorPosition("walker", sim.Position{X: sim.PadX, Y: sim.PadY}, time.Now()))
	if err != nil {
		t.Fatalf("teleport back out: %v", err)
	}
	if out := res.(sim.SetActorPositionResult); out.InsideStructureID != "" {
		t.Errorf("InsideStructureID after exit = %q, want empty", out.InsideStructureID)
	}
}

// TestSetActorPosition_CancelsInFlightWalk: a teleport supersedes an
// in-flight MoveIntent — the intent clears, ActorMoveStopped{cancelled}
// fires, and the result reports the cancellation.
func TestSetActorPosition_CancelsInFlightWalk(t *testing.T) {
	w, cancel, rec := buildSetPositionTestWorld(t)
	defer cancel()

	walkTarget := sim.Position{X: sim.PadX + 20, Y: sim.PadY + 20}
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(walkTarget), false, time.Now())); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	res, err := w.Send(sim.SetActorPosition("walker", sim.Position{X: sim.PadX + 3, Y: sim.PadY + 3}, time.Now()))
	if err != nil {
		t.Fatalf("SetActorPosition: %v", err)
	}
	if out := res.(sim.SetActorPositionResult); !out.MoveCancelled {
		t.Errorf("MoveCancelled = false, want true")
	}

	intent, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["walker"].MoveIntent != nil, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if moving, _ := intent.(bool); moving {
		t.Error("MoveIntent survived the teleport")
	}
	stops := rec.countEvents(func(e sim.Event) bool {
		s, is := e.(*sim.ActorMoveStopped)
		return is && s.Reason == sim.MoveStoppedCancelled
	})
	if stops != 1 {
		t.Errorf("ActorMoveStopped{cancelled} count = %d, want 1", stops)
	}
}

// TestSetActorPosition_DisplacedActorLeavesHuddle: teleporting an actor away
// from its area-bound huddle runs the drift guard — the actor is removed and
// the result names the huddle it left.
func TestSetActorPosition_DisplacedActorLeavesHuddle(t *testing.T) {
	w, cancel, _ := buildSetPositionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	huddleID := sim.HuddleID("h-teleport-1")

	pin := sim.Position{X: sim.PadX + 2, Y: sim.PadY + 2}
	sceneRes, err := w.Send(sim.CreateScene("encounter", sim.NewAreaBound(pin, 3), now))
	if err != nil {
		t.Fatalf("CreateScene: %v", err)
	}
	sceneID := sceneRes.(sim.SceneID)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].Pos = pin
		world.Huddles[huddleID] = &sim.Huddle{
			ID:        huddleID,
			Members:   map[sim.ActorID]struct{}{"walker": {}},
			StartedAt: now,
		}
		world.Actors["walker"].CurrentHuddleID = huddleID
		world.Scenes[sceneID].Huddles[huddleID] = struct{}{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed huddle: %v", err)
	}

	res, err := w.Send(sim.SetActorPosition("walker", sim.Position{X: sim.PadX + 30, Y: sim.PadY + 30}, now.Add(time.Second)))
	if err != nil {
		t.Fatalf("SetActorPosition: %v", err)
	}
	if out := res.(sim.SetActorPositionResult); out.LeftHuddleID != huddleID {
		t.Errorf("LeftHuddleID = %q, want %q", out.LeftHuddleID, huddleID)
	}
	if got := w.Published().Actors["walker"].CurrentHuddleID; got != "" {
		t.Errorf("walker still in huddle %q after teleport", got)
	}
}
