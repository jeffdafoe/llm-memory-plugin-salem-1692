package sim

import (
	"reflect"
	"testing"
	"time"
)

// move_to_keeper_name_test.go — LLM-868: a place named by its keeper. The arrival
// line says "You arrived at Ezekiel Crane's Blacksmith", so that phrasing — and a
// model's own "Crane's Forge" — must lead to the keeper's work structure, and the
// unknown-place error must list the keeper's place first.

// keeperNameWorld is the live shape: Ezekiel Crane at the Blacksmith, the two
// Ellises at different places, and the constable whose display name carries a
// title. The Blacksmith is the farthest business, as it was from Gideon's door.
func keeperNameWorld() *World {
	w := bnWorld(5)
	bnBusiness(w, "mill", "Mill", 1, EntryPolicyOpen, false)
	bnBusiness(w, "farm", "Ellis Farm", 2, EntryPolicyOwner, false)
	bnBusiness(w, "store", "General Store", 3, EntryPolicyOwner, false)
	bnBusiness(w, "tavern", "Tavern", 4, EntryPolicyOpen, false)
	bnBusiness(w, "apothecary", "PW Apothecary", 5, EntryPolicyOwner, false)
	bnBusiness(w, "meeting", "Meeting House", 6, EntryPolicyOpen, false)
	bnBusiness(w, "smithy", "Blacksmith", 9, EntryPolicyOwner, false)
	for _, k := range []struct {
		id, name string
		work     StructureID
	}{
		{"ezekiel", "Ezekiel Crane", "smithy"},
		{"john", "John Ellis", "tavern"},
		{"elizabeth", "Elizabeth Ellis", "farm"},
		{"gideon", "Constable Gideon Marsh", "meeting"},
		{"silas", "Silas Withrow the bar iron-peddler", ""},
	} {
		w.Actors[ActorID(k.id)] = &Actor{ID: ActorID(k.id), DisplayName: k.name, WorkStructureID: k.work}
	}
	return w
}

func TestResolveStructureByKeeperName(t *testing.T) {
	cases := []struct {
		name string
		want StructureID
	}{
		{"Ezekiel Crane's Blacksmith", "smithy"},
		{"ezekiel crane’s forge", "smithy"},
		{"Crane's Forge", "smithy"},
		{"the Crane's", "smithy"},
		{"Gideon Marsh's Meeting House", "meeting"},
		{"John Ellis's Tavern", "tavern"},
		{"Elizabeth Ellis's farm", "farm"},
		{"Ellis's", ""},            // two Ellises, two places
		{"Ellis's Farm", ""},       // still the shared surname
		{"Marshall's shop", ""},    // whole words only
		{"Blacksmith", ""},         // no possessive
		{"Crane'sForge", ""},       // the 's must end a word
		{"Withrow's pack", ""},     // a visitor works nowhere
		{"Hannah Boggs's Inn", ""}, // no such keeper
	}
	w := keeperNameWorld()
	for _, c := range cases {
		got, ok := resolveStructureByKeeperName(w, c.name)
		if c.want == "" {
			if ok {
				t.Errorf("%q resolved to %q, want no match", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%q = %q ok=%v, want %q", c.name, got, ok, c.want)
		}
	}
}

// Two keepers who share a surname but work the SAME place still resolve.
func TestResolveStructureByKeeperName_SharedSurnameSamePlace(t *testing.T) {
	w := keeperNameWorld()
	w.Actors["anne"] = &Actor{ID: "anne", DisplayName: "Anne Walker", WorkStructureID: "mill"}
	w.Actors["lewis"] = &Actor{ID: "lewis", DisplayName: "Lewis Walker", WorkStructureID: "mill"}
	if got, ok := resolveStructureByKeeperName(w, "Walker's"); !ok || got != "mill" {
		t.Errorf("Walker's = %q ok=%v, want mill", got, ok)
	}
}

func TestNamedVillageDestinations_KeeperNamedFirst(t *testing.T) {
	w := keeperNameWorld()
	a := bnActor("", "")
	now := time.Now()

	names, more := namedVillageDestinationsFor(w, a, "Crane's Forge", moveToDestinationNameCap, now)
	if want := []string{"Blacksmith", "Mill", "Ellis Farm", "General Store", "Tavern"}; !reflect.DeepEqual(names, want) || !more {
		t.Errorf("Crane's Forge: %v more=%v, want %v more=true", names, more, want)
	}
	// A shared surname ranks both keepers' places, nearest first between them.
	names, _ = namedVillageDestinationsFor(w, a, "the Ellis place", moveToDestinationNameCap, now)
	if want := []string{"Ellis Farm", "Tavern", "Mill", "General Store", "PW Apothecary"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Ellis place: %v, want %v", names, want)
	}
	// No keeper named: the nearest-first list, as before.
	plain, _ := namedVillageDestinations(w, a, moveToDestinationNameCap, now)
	names, _ = namedVillageDestinationsFor(w, a, "the Smithy", moveToDestinationNameCap, now)
	if !reflect.DeepEqual(names, plain) {
		t.Errorf("no keeper named: %v, want the plain list %v", names, plain)
	}
}
