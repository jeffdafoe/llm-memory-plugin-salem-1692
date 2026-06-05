package perception

import (
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// perceived_places_test.go — ZBBS-HOME-389. CollectPerceivedPlaces walks every
// move-target cue in a Payload and returns the deduped, sorted id sets.

func TestCollectPerceivedPlaces(t *testing.T) {
	p := Payload{
		Anchors: &AnchorsView{WorkID: "work", HomeID: "home"},
		Satiation: &SatiationView{
			Needs: []SatiationNeedView{
				{
					Vendors:     []SatiationVendor{{StructureID: "tavern"}, {StructureID: "store"}},
					FreeSources: []SatiationFreeSource{{ObjectID: "well"}},
				},
			},
		},
		RecoveryOptions: &RecoveryOptionsView{
			Options: []RecoveryOption{
				{StructureID: "inn"},
				{ObjectID: "shade_tree"},
			},
		},
		Restocking: &RestockingView{
			Items: []RestockItemView{
				// "store" is a dup of the satiation vendor — must collapse.
				{Vendors: []RestockVendor{{StructureID: "farm"}, {StructureID: "store"}}},
			},
		},
	}
	got := CollectPerceivedPlaces(p)

	wantStructures := []sim.StructureID{"farm", "home", "inn", "store", "tavern", "work"}
	wantObjects := []sim.VillageObjectID{"shade_tree", "well"}
	if !reflect.DeepEqual(got.StructureIDs, wantStructures) {
		t.Errorf("StructureIDs = %v, want %v (sorted + deduped)", got.StructureIDs, wantStructures)
	}
	if !reflect.DeepEqual(got.ObjectIDs, wantObjects) {
		t.Errorf("ObjectIDs = %v, want %v (sorted + deduped)", got.ObjectIDs, wantObjects)
	}
}

func TestCollectPerceivedPlaces_EmptyPayloadYieldsNil(t *testing.T) {
	got := CollectPerceivedPlaces(Payload{})
	if got.StructureIDs != nil || got.ObjectIDs != nil {
		t.Errorf("an empty payload must yield nil slices, got %+v", got)
	}
}

// Empty-string ids (an unset cue field) must be skipped, not collected as "".
func TestCollectPerceivedPlaces_SkipsEmptyIDs(t *testing.T) {
	p := Payload{
		Anchors: &AnchorsView{WorkID: "work", HomeID: ""}, // no home anchor
		RecoveryOptions: &RecoveryOptionsView{
			Options: []RecoveryOption{{StructureID: "inn", ObjectID: ""}},
		},
	}
	got := CollectPerceivedPlaces(p)
	wantStructures := []sim.StructureID{"inn", "work"}
	if !reflect.DeepEqual(got.StructureIDs, wantStructures) {
		t.Errorf("StructureIDs = %v, want %v", got.StructureIDs, wantStructures)
	}
	if got.ObjectIDs != nil {
		t.Errorf("ObjectIDs = %v, want nil (empty ids skipped)", got.ObjectIDs)
	}
}
