package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// arrival_narration_test.go — ZBBS-WORK-422 coverage of the observer-facing
// arrival narration: ArrivalDestinationName (the place resolver shared with the
// action-log backload) and emitArrivalNarration's gating (narrate only when a
// co-present PC is there to read it).

// TestArrivalDestinationName covers the shared resolver each arrival path uses to
// name the destination — the structure walked to, the village object visited, or
// the structure physically ended inside, else empty.
func TestArrivalDestinationName(t *testing.T) {
	w := &sim.World{
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": {ID: "tavern", DisplayName: "the Tavern"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"well": {ID: "well", DisplayName: "the Well"},
		},
	}
	cases := []struct {
		name string
		e    *sim.ActorArrived
		want string
	}{
		{"dest structure", &sim.ActorArrived{DestStructureID: "tavern"}, "the Tavern"},
		{"dest object", &sim.ActorArrived{DestObjectID: "well"}, "the Well"},
		{"final-structure fallback", &sim.ActorArrived{FinalStructureID: "tavern"}, "the Tavern"},
		{"open ground", &sim.ActorArrived{}, ""},
		{"unknown structure", &sim.ActorArrived{DestStructureID: "ghost"}, ""},
	}
	for _, c := range cases {
		if got := sim.ArrivalDestinationName(w, c.e); got != c.want {
			t.Errorf("%s: ArrivalDestinationName = %q, want %q", c.name, got, c.want)
		}
	}
}

// buildArrivalNarrationWorld seeds a running world with a "cottage" structure, a
// conversational NPC "walker" parked outside, and — when withPC is true — a PC
// already standing inside the cottage (the co-present observer). Mirrors
// buildLocomotionTestWorld; the eventRec captures every emitted event.
func buildArrivalNarrationWorld(t *testing.T, withPC bool) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"cottage-asset": {ID: "cottage-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"cottage": {ID: "cottage", AssetID: "cottage-asset", Pos: sim.WorldPos{X: 320, Y: 320}, LoiterOffsetX: intp(0), LoiterOffsetY: intp(5)},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"cottage": {ID: "cottage", DisplayName: "Cottage"},
	})
	actors := map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", DisplayName: "Walker", Kind: sim.KindNPCShared, Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5}},
	}
	if withPC {
		// A PC already inside the cottage, standing on the door tile (the 1x1
		// footprint) so InsideStructureID resolves to "cottage" — the co-present
		// observer whose talk panel should receive the arrival line.
		actors["pc-1"] = &sim.Actor{
			ID: "pc-1", DisplayName: "Player One", Kind: sim.KindPC,
			Pos:               sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			InsideStructureID: "cottage",
		}
	}
	handles.Actors.Seed(actors)
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

// TestArrivalNarration_EmitsToCoPresentPC: an NPC entering a structure where a PC
// stands emits ActorArrivalNarrated with the "<name> arrives at <place>." line —
// the same phrasing the action-log backload uses.
func TestArrivalNarration_EmitsToCoPresentPC(t *testing.T) {
	w, cancel, rec := buildArrivalNarrationWorld(t, true)
	defer cancel()
	now := time.Now().UTC()
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)

	var text string
	n := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrivalNarrated)
		if ok && a.ActorID == "walker" {
			text = a.Text
			return true
		}
		return false
	})
	if n != 1 {
		t.Fatalf("ActorArrivalNarrated count = %d, want 1", n)
	}
	if want := "Walker arrives at the Cottage."; text != want {
		t.Errorf("narration text = %q, want %q", text, want)
	}
}

// TestDepartureNarration_EmitsToCoPresentPC: an NPC walking OUT of a structure
// where a PC stands emits ActorDepartureNarrated with the "<name> leaves the
// <place>." line — the mirror of the arrival narration. The walker is first walked
// IN (the known-good arrival path) so it genuinely crosses the footprint boundary
// on the way out; counting by event type isolates the departure from the arrival.
func TestDepartureNarration_EmitsToCoPresentPC(t *testing.T) {
	w, cancel, rec := buildArrivalNarrationWorld(t, true)
	defer cancel()
	now := time.Now().UTC()
	// Walk in first — walker ends inside "cottage".
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor in: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)
	// Walk back out to the original outdoor start tile — crosses out of the footprint.
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}), false, now)); err != nil {
		t.Fatalf("MoveActor out: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)

	var text string
	n := rec.countEvents(func(e sim.Event) bool {
		d, ok := e.(*sim.ActorDepartureNarrated)
		if ok && d.ActorID == "walker" {
			text = d.Text
			return true
		}
		return false
	})
	if n != 1 {
		t.Fatalf("ActorDepartureNarrated count = %d, want 1", n)
	}
	if want := "Walker leaves the Cottage."; text != want {
		t.Errorf("narration text = %q, want %q", text, want)
	}
}

// TestDepartureNarration_SkippedWithNoPC: the same walk-out with no PC present
// emits no narration — the line would reach no one.
func TestDepartureNarration_SkippedWithNoPC(t *testing.T) {
	w, cancel, rec := buildArrivalNarrationWorld(t, false)
	defer cancel()
	now := time.Now().UTC()
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor in: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}), false, now)); err != nil {
		t.Fatalf("MoveActor out: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)

	if n := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorDepartureNarrated)
		return ok
	}); n != 0 {
		t.Errorf("ActorDepartureNarrated count = %d, want 0 (no PC in earshot)", n)
	}
}

// TestArrivalNarration_SkippedWithNoPC: the same arrival with no PC present emits
// no narration — the line would reach no one.
func TestArrivalNarration_SkippedWithNoPC(t *testing.T) {
	w, cancel, rec := buildArrivalNarrationWorld(t, false)
	defer cancel()
	now := time.Now().UTC()
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "walker", now, 40)

	if n := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorArrivalNarrated)
		return ok
	}); n != 0 {
		t.Errorf("ActorArrivalNarrated count = %d, want 0 (no PC in earshot)", n)
	}
}
