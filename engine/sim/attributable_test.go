package sim

import (
	"testing"
	"time"
)

// TestAttributableGatesTheSimSideDoors — LLM-682. The engine-side doors toward a
// source share the resolvers' rule: an unnamed owned bush is not seeded as a known
// gather place, does not wake its owner to harvest, and an unnamed free source
// does not count as one for the seek-work backstop. The resolver itself refuses it
// too, which is the half every door has to agree with.
func TestAttributableGatesTheSimSideDoors(t *testing.T) {
	var nilObj *VillageObject
	if nilObj.Attributable() || (&VillageObject{}).Attributable() || !(&VillageObject{DisplayName: "Well"}).Attributable() {
		t.Fatal("Attributable: want false for nil and unnamed, true for named")
	}

	// Seeding.
	owner := &Actor{ID: "prudence", Kind: KindNPCStateful}
	bush := forageBushObj("prudence", "raspberries", 10)
	bush.DisplayName = ""
	SeedOwnedKnownPlaces(map[ActorID]*Actor{"prudence": owner}, map[VillageObjectID]*VillageObject{"bushA": bush}, time.Now())
	if kp := owner.KnownPlaces["bushA"]; kp != nil {
		t.Errorf("an unnamed owned bush was seeded as a known place: %+v", kp)
	}

	// The forage restock wake.
	a := &Actor{ID: "prudence"}
	rememberForageBush(a, "raspberries", "bushA")
	w := &World{VillageObjects: map[VillageObjectID]*VillageObject{"bushA": bush}}
	if actorRemembersForageSource(a, w, "raspberries") {
		t.Error("an unnamed owned bush counts as a remembered forage source")
	}
	bush.DisplayName = "Raspberry Bush"
	if !actorRemembersForageSource(a, w, "raspberries") {
		t.Error("the same bush with a name does not count")
	}

	// The seek-work backstop's free-source check.
	free := &VillageObject{Refreshes: []*ObjectRefresh{{Attribute: "hunger", Amount: -5}}}
	w = &World{VillageObjects: map[VillageObjectID]*VillageObject{"src": free}}
	if freeConsumableSourceExists(w, a, "hunger") {
		t.Error("an unnamed free source counts as one")
	}
	free.DisplayName = "Berry Bush"
	if !freeConsumableSourceExists(w, a, "hunger") {
		t.Error("the same source with a name does not count")
	}
}
