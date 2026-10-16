package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-798: the camp scene line opens on the owner's name rune-aware, so a name
// starting with a multi-byte letter is not split; an empty label reads
// "Someone's".
func TestCampSightProse_Names(t *testing.T) {
	if got, want := campSightProse(CampSight{OwnerLabel: "élodie", Fire: sim.CampFireStateBurning}),
		"Élodie's tent is pitched nearby, a fire burning before it."; got != want {
		t.Errorf("multi-byte name = %q, want %q", got, want)
	}
	if got, want := campSightProse(CampSight{OwnerLabel: "a stranger"}),
		"A stranger's tent is pitched nearby, its fire ring cold."; got != want {
		t.Errorf("stranger = %q, want %q", got, want)
	}
	if got, want := campSightProse(CampSight{Fire: sim.CampFireStateDying}),
		"Someone's tent is pitched nearby, the fire before it burning low."; got != want {
		t.Errorf("no label = %q, want %q", got, want)
	}
}

// Three camps in sight: the nearest first, a tie broken by the lower tent id,
// and only two told of.
func TestBuildCampSightings_NearestTwo(t *testing.T) {
	at := func(x, y int) sim.WorldPos {
		return sim.WorldPos{X: float64(x-sim.PadX) * sim.TileSize, Y: float64(y-sim.PadY) * sim.TileSize}
	}
	subject := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Hannah", Pos: sim.TilePos{X: 100, Y: 100},
		Acquaintances: map[string]sim.Acquaintance{"Bea": {}, "Cal": {}, "Abe": {}}}
	snap := &sim.Snapshot{
		PublishedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"h":   subject,
			"abe": {Kind: sim.KindPC, DisplayName: "Abe"},
			"bea": {Kind: sim.KindPC, DisplayName: "Bea"},
			"cal": {Kind: sim.KindPC, DisplayName: "Cal"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			// Abe's camp is farthest (6); Cal's and Bea's tie at 4, and Bea's
			// tent id sorts first.
			"tent-c": {ID: "tent-c", AssetID: sim.TentAssetID, CurrentState: sim.TentStateClosed, OwnerActorID: "abe", Pos: at(106, 100)},
			"tent-b": {ID: "tent-b", AssetID: sim.TentAssetID, CurrentState: sim.TentStateClosed, OwnerActorID: "cal", Pos: at(96, 100)},
			"tent-a": {ID: "tent-a", AssetID: sim.TentAssetID, CurrentState: sim.TentStateClosed, OwnerActorID: "bea", Pos: at(104, 100)},
		},
	}
	got := buildCampSightings(snap, "h", subject)
	if len(got) != 2 || got[0].OwnerLabel != "Bea" || got[1].OwnerLabel != "Cal" {
		t.Fatalf("sightings = %+v, want Bea then Cal (nearest two, tie by tent id)", got)
	}
}
