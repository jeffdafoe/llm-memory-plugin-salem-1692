package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// gatherCueSnapshot builds a snapshot with one gatherable well at world (100,100)
// and the actor placed at actorTile. The well has zero loiter offset, so its
// pin is the well's anchor tile.
func gatherCueSnapshot(actorTile sim.TilePos) *sim.Snapshot {
	zero := 0
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {Pos: actorTile},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"well": {
				ID:            "well",
				DisplayName:   "Old Well",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Attribute: "thirst", Amount: -12, GatherItem: "water"},
				},
			},
			// A non-gatherable refresh object far away — out of attribution range,
			// so it neither cues nor (being distant) suppresses the well.
			"oak": {
				ID:          "oak",
				DisplayName: "Oak",
				Pos:         sim.WorldPos{X: 5000, Y: 5000},
				Refreshes:   []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -4}},
			},
		},
	}
}

func TestBuild_GatherableCue_AtSource(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	p := Build(gatherCueSnapshot(wellPin), "hannah", nil)

	if p.Surroundings.GatherableItem != "water" {
		t.Errorf("GatherableItem=%q, want water", p.Surroundings.GatherableItem)
	}
	if p.Surroundings.GatherableSource != "Old Well" {
		t.Errorf("GatherableSource=%q, want Old Well", p.Surroundings.GatherableSource)
	}
}

func TestBuild_GatherableCue_TooFar_NoCue(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	far := sim.TilePos{X: wellPin.X + 2, Y: wellPin.Y} // Chebyshev 2 > LoiterAttributionTiles (1)
	p := Build(gatherCueSnapshot(far), "hannah", nil)

	if p.Surroundings.GatherableItem != "" {
		t.Errorf("GatherableItem=%q, want empty (out of attribution range)", p.Surroundings.GatherableItem)
	}
}

// TestBuild_GatherableCue_CloserNonGatherableSuppresses — resolve-then-check:
// a non-gatherable refresh object NEARER than the well owns the tile, so no cue
// is offered (matches the sim.Gather Command, which would reject because that
// closer object is what it resolves).
func TestBuild_GatherableCue_CloserNonGatherableSuppresses(t *testing.T) {
	zero := 0
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	// Actor stands on the oak (cheb 0); the well is one tile away (cheb 1).
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {Pos: wellPin},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"oak": {
				ID: "oak", DisplayName: "Oak",
				Pos:           sim.WorldPos{X: 100, Y: 100}, // same tile as the actor
				LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -4}}, // not gatherable
			},
			"well": {
				ID: "well", DisplayName: "Old Well",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: intp(1), LoiterOffsetY: &zero, // pin one tile east → cheb 1
				Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -12, GatherItem: "water"}},
			},
		},
	}
	p := Build(snap, "hannah", nil)
	if p.Surroundings.GatherableItem != "" {
		t.Errorf("GatherableItem=%q, want empty (closer non-gatherable object owns the tile)", p.Surroundings.GatherableItem)
	}
}

// intp returns a pointer to v (loiter-offset fields are *int).
func intp(v int) *int { return &v }

// TestRenderSurroundings_GatherableLine — the cue renders a "gatherable" line;
// absent when no cue.
func TestRenderSurroundings_GatherableLine(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{GatherableItem: "water", GatherableSource: "Old Well"})
	out := b.String()
	if !strings.Contains(out, "gatherable:") || !strings.Contains(out, "gather water here") {
		t.Errorf("render missing gatherable line:\n%s", out)
	}

	var b2 strings.Builder
	renderSurroundings(&b2, SurroundingsView{})
	if strings.Contains(b2.String(), "gatherable:") {
		t.Errorf("render emitted a gatherable line with no cue:\n%s", b2.String())
	}
}
