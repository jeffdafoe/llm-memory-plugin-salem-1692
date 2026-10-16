package sim

import (
	"testing"
	"time"
)

// TestMinorWorksFactCrateByLandmark: the landmark names where the crate is,
// never who broke it — "knocked loose ... by the Blacksmith" read as the smith
// having done it.
func TestMinorWorksFactCrateByLandmark(t *testing.T) {
	assets := map[AssetID]*Asset{
		"crate-asset": {ID: "crate-asset", DefaultState: "default", States: []AssetState{
			{ID: 200, State: "default"},
			{ID: 201, State: "lid-loose", Tags: []string{"minor-of-default", "minor-form-crate"}},
		}},
	}
	crate := &VillageObject{ID: "crate", AssetID: "crate-asset", CurrentState: "lid-loose", DamagedAt: time.Now(), Pos: TilePos{X: 10, Y: 10}.Center()}
	smithy := &VillageObject{ID: "smithy", DisplayName: "Blacksmith", Pos: TilePos{X: 12, Y: 10}.Center()}
	objects := map[VillageObjectID]*VillageObject{"crate": crate, "smithy": smithy}
	structures := map[StructureID]*Structure{"smithy": {}}

	if got, want := minorWorksFact(objects, structures, assets, crate), "The lid of the crate by the Blacksmith has come loose"; got != want {
		t.Errorf("one crate: %q, want %q", got, want)
	}
	if got, want := minorWorksFactCount(objects, structures, assets, crate, 3), "The lids of three crates by the Blacksmith have come loose"; got != want {
		t.Errorf("three crates: %q, want %q", got, want)
	}
}

// TestArrivalWalkedTo: the object a walk was to, else its structure, else none
// — what the client matches the arrival's repair offer against (LLM-793).
func TestArrivalWalkedTo(t *testing.T) {
	cases := []struct {
		label string
		evt   *ActorArrived
		want  VillageObjectID
	}{
		{"no event", nil, ""},
		{"a walk to a tile", &ActorArrived{}, ""},
		{"a walk to an object", &ActorArrived{DestObjectID: "crate"}, "crate"},
		{"a walk to a structure", &ActorArrived{DestStructureID: "smithy"}, "smithy"},
	}
	for _, c := range cases {
		if got := arrivalWalkedTo(c.evt); got != c.want {
			t.Errorf("%s: %q, want %q", c.label, got, c.want)
		}
	}
}
