package perception

import (
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// perceived_places_test.go — ZBBS-HOME-389 / LLM-142. CollectPerceivedPlaces
// walks every move-target cue in a Payload and returns the deduped, sorted OBJECT
// id set. Structures are NOT collected — village geography is common knowledge
// and resolves by name directly (LLM-142).

func TestCollectPerceivedPlaces(t *testing.T) {
	p := Payload{
		// Anchors + vendor workplaces are structures — common-knowledge geography,
		// so they are NOT collected here.
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
				{StructureID: "inn"},     // structure — not collected
				{ObjectID: "shade_tree"}, // free object — collected
				{ObjectID: "well"},       // dup of the satiation free source — must collapse
			},
		},
		Restocking: &RestockingView{
			Items: []RestockItemView{
				{Vendors: []RestockVendor{{StructureID: "farm"}, {StructureID: "store"}}}, // structures — not collected
			},
		},
	}
	got := CollectPerceivedPlaces(p)

	wantObjects := []sim.VillageObjectID{"shade_tree", "well"}
	if !reflect.DeepEqual(got.ObjectIDs, wantObjects) {
		t.Errorf("ObjectIDs = %v, want %v (sorted + deduped, objects only)", got.ObjectIDs, wantObjects)
	}
}

func TestCollectPerceivedPlaces_EmptyPayloadYieldsNil(t *testing.T) {
	got := CollectPerceivedPlaces(Payload{})
	if got.ObjectIDs != nil {
		t.Errorf("an empty payload must yield a nil ObjectIDs slice, got %+v", got)
	}
}

// A payload whose only move targets are structures yields no objects — structures
// resolve by village name, not through this shown set (LLM-142).
func TestCollectPerceivedPlaces_StructureOnlyYieldsNil(t *testing.T) {
	p := Payload{
		Anchors:         &AnchorsView{WorkID: "work", HomeID: "home"},
		RecoveryOptions: &RecoveryOptionsView{Options: []RecoveryOption{{StructureID: "inn"}}},
	}
	got := CollectPerceivedPlaces(p)
	if got.ObjectIDs != nil {
		t.Errorf("a structure-only payload must yield nil ObjectIDs, got %v", got.ObjectIDs)
	}
}

// Empty-string object ids (an unset cue field) must be skipped, not collected as "".
func TestCollectPerceivedPlaces_SkipsEmptyIDs(t *testing.T) {
	p := Payload{
		RecoveryOptions: &RecoveryOptionsView{
			Options: []RecoveryOption{{StructureID: "inn", ObjectID: ""}, {ObjectID: "shade_tree"}},
		},
	}
	got := CollectPerceivedPlaces(p)
	wantObjects := []sim.VillageObjectID{"shade_tree"}
	if !reflect.DeepEqual(got.ObjectIDs, wantObjects) {
		t.Errorf("ObjectIDs = %v, want %v (empty ids skipped)", got.ObjectIDs, wantObjects)
	}
}
