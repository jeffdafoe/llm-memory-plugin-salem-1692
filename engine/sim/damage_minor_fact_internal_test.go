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
