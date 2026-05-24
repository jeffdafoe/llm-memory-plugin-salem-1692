package cascade

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildArrivalRefreshWorld seeds a world with one named, asset-backed refresh
// object — an oak that eases tiredness (-8, infinite supply, no dwell) — and a
// tired actor at the tile origin. The oak has a zero loiter offset so its pin
// lands on its anchor tile; placeOnPin moves the actor there.
func buildArrivalRefreshWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tree-oak": {ID: "tree-oak"},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"oak": {
			ID: "oak", DisplayName: "Oak", AssetID: "tree-oak", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 500, Y: 500},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "tiredness", Amount: -8},
			},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"weary": {
			ID:          "weary",
			DisplayName: "Weary Traveller",
			Needs:       map[sim.NeedKey]int{"tiredness": 14},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// placeOnPin teleports the actor onto objID's loiter pin (the object's anchor
// tile, given the zero loiter offset) so an arrival there resolves to it.
func placeOnPin(t *testing.T, w *sim.World, actorID sim.ActorID, objID sim.VillageObjectID) sim.TilePos {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[objID]
		actor := world.Actors[actorID]
		actor.Pos = obj.Pos.Tile()
		return actor.Pos, nil
	}})
	if err != nil {
		t.Fatalf("placeOnPin: %v", err)
	}
	return res.(sim.TilePos)
}

// dispatchArrival invokes the subscriber directly on the world goroutine with
// a synthesized ActorArrived — the white-box convention this package's
// subscriber tests use (driving the locomotion ticker tile-by-tile is out of
// scope for a wiring test).
func dispatchArrival(t *testing.T, w *sim.World, evt *sim.ActorArrived) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleObjectRefreshArrival(world, evt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("dispatchArrival: %v", err)
	}
}

func tirednessOf(w *sim.World, actorID sim.ActorID) int {
	return w.Published().Actors[actorID].Needs["tiredness"]
}

// TestObjectRefreshArrival_AppliesOnArrival: a fresh ActorArrived whose
// FinalPosition matches the actor's tile on the oak pin applies the refresh
// through the subscriber.
func TestObjectRefreshArrival_AppliesOnArrival(t *testing.T) {
	w, cancel := buildArrivalRefreshWorld(t)
	defer cancel()

	pin := placeOnPin(t, w, "weary", "oak")
	dispatchArrival(t, w, &sim.ActorArrived{
		ActorID:       "weary",
		FinalPosition: sim.Position{X: pin.X, Y: pin.Y},
		At:            time.Now(),
	})

	if got := tirednessOf(w, "weary"); got != 6 { // 14 - 8
		t.Errorf("tiredness = %d, want 6 (refresh applied on arrival)", got)
	}
}

// TestObjectRefreshArrival_SkipsStaleEvent: the actor stands on the oak pin,
// but the ActorArrived event reports a different FinalPosition (a same-tick
// subscriber moved the actor after the event was stamped). The freshness
// guard skips — no refresh — so the engine never keys a refresh off the
// wrong tile.
func TestObjectRefreshArrival_SkipsStaleEvent(t *testing.T) {
	w, cancel := buildArrivalRefreshWorld(t)
	defer cancel()

	pin := placeOnPin(t, w, "weary", "oak")
	dispatchArrival(t, w, &sim.ActorArrived{
		ActorID:       "weary",
		FinalPosition: sim.Position{X: pin.X + 5, Y: pin.Y + 5}, // stale: != actor.Pos
		At:            time.Now(),
	})

	if got := tirednessOf(w, "weary"); got != 14 {
		t.Errorf("tiredness = %d, want 14 (stale event must not apply)", got)
	}
}

// TestObjectRefreshArrival_NoOpAwayFromObject: a fresh arrival far from any
// refresh object resolves to nothing — the command self-filters, so a blanket
// subscribe is harmless.
func TestObjectRefreshArrival_NoOpAwayFromObject(t *testing.T) {
	w, cancel := buildArrivalRefreshWorld(t)
	defer cancel()

	// Actor stays at the tile origin, far from the oak.
	dispatchArrival(t, w, &sim.ActorArrived{
		ActorID:       "weary",
		FinalPosition: sim.Position{X: 0, Y: 0},
		At:            time.Now(),
	})

	if got := tirednessOf(w, "weary"); got != 14 {
		t.Errorf("tiredness = %d, want 14 (no object nearby)", got)
	}
}

// TestRegisterObjectRefreshArrival_NilWorldPanics is the wiring guard
// regression, mirroring the other cascade Register* helpers.
func TestRegisterObjectRefreshArrival_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterObjectRefreshArrival(nil) did not panic")
		}
	}()
	RegisterObjectRefreshArrival(nil)
}
