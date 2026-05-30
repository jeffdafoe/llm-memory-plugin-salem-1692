package sim

import (
	"testing"
	"time"
)

// closed_business_test.go — ZBBS-HOME-353 capture subscriber. White-box (package
// sim) so it can reach the unexported helpers and drive handleClosedBusinessOnArrival
// directly. The inside-structure arrival path (FinalStructureID set) needs no
// loiter geometry; one case exercises the loiter-visit path with the standard
// loiterObj fixture.

func cbWorld() *World {
	return &World{
		Actors:         make(map[ActorID]*Actor),
		Structures:     make(map[StructureID]*Structure),
		VillageObjects: make(map[VillageObjectID]*VillageObject),
		Assets:         map[AssetID]*Asset{"a": {ID: "a"}},
	}
}

func cbAgent(id ActorID, work StructureID, inside StructureID) *Actor {
	return &Actor{ID: id, Kind: KindNPCStateful, WorkStructureID: work, InsideStructureID: inside}
}

func arrivedInside(id ActorID, structure StructureID, at time.Time) *ActorArrived {
	return &ActorArrived{ActorID: id, FinalStructureID: structure, At: at}
}

func TestClosedBusiness_RecordsWhenKeeperAbsent(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["farm"] = &Structure{ID: "farm", DisplayName: "Ellis Farm"}
	// The farm has a worker (so it's a business) — but the worker is elsewhere.
	w.Actors["dairyer"] = cbAgent("dairyer", "farm", "general_store")
	// John arrives INSIDE the farm; no keeper present → remembered shut.
	john := cbAgent("john", "tavern", "farm")
	w.Actors["john"] = john

	handleClosedBusinessOnArrival(w, arrivedInside("john", "farm", now))

	if _, ok := john.ClosedBusinessObs["farm"]; !ok {
		t.Fatalf("expected john to remember Ellis Farm shut, got %v", john.ClosedBusinessObs)
	}
}

func TestClosedBusiness_NoRecordWhenKeeperPresent(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["farm"] = &Structure{ID: "farm", DisplayName: "Ellis Farm"}
	// The dairyer IS inside the farm → keeper present → not shut.
	w.Actors["dairyer"] = cbAgent("dairyer", "farm", "farm")
	john := cbAgent("john", "tavern", "farm")
	w.Actors["john"] = john

	handleClosedBusinessOnArrival(w, arrivedInside("john", "farm", now))

	if len(john.ClosedBusinessObs) != 0 {
		t.Fatalf("keeper present → no shut memory, got %v", john.ClosedBusinessObs)
	}
}

func TestClosedBusiness_KeeperPresentClearsStaleMemory(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["farm"] = &Structure{ID: "farm", DisplayName: "Ellis Farm"}
	w.Actors["dairyer"] = cbAgent("dairyer", "farm", "farm") // present this time
	john := cbAgent("john", "tavern", "farm")
	john.ClosedBusinessObs = map[StructureID]time.Time{"farm": now.Add(-time.Hour)} // stale memory
	w.Actors["john"] = john

	handleClosedBusinessOnArrival(w, arrivedInside("john", "farm", now))

	if _, ok := john.ClosedBusinessObs["farm"]; ok {
		t.Fatalf("re-observing the farm attended must clear the stale shut memory, got %v", john.ClosedBusinessObs)
	}
}

func TestClosedBusiness_OwnWorkplaceExcluded(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["tavern"] = &Structure{ID: "tavern", DisplayName: "Tavern"}
	john := cbAgent("john", "tavern", "tavern") // arriving at his OWN workplace
	w.Actors["john"] = john

	handleClosedBusinessOnArrival(w, arrivedInside("john", "tavern", now))

	if len(john.ClosedBusinessObs) != 0 {
		t.Fatalf("an actor never records its own workplace as shut, got %v", john.ClosedBusinessObs)
	}
}

func TestClosedBusiness_NonBusinessIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	// A residence: a structure with NO worker → not a business → never "shut".
	w.Structures["cottage"] = &Structure{ID: "cottage", DisplayName: "A Cottage"}
	john := cbAgent("john", "tavern", "cottage")
	w.Actors["john"] = john

	handleClosedBusinessOnArrival(w, arrivedInside("john", "cottage", now))

	if len(john.ClosedBusinessObs) != 0 {
		t.Fatalf("a workerless residence is not a business, got %v", john.ClosedBusinessObs)
	}
}

func TestClosedBusiness_NonAgentIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["farm"] = &Structure{ID: "farm", DisplayName: "Ellis Farm"}
	w.Actors["dairyer"] = cbAgent("dairyer", "farm", "general_store")
	pc := &Actor{ID: "pc", Kind: KindPC, InsideStructureID: "farm"}
	w.Actors["pc"] = pc

	handleClosedBusinessOnArrival(w, arrivedInside("pc", "farm", now))

	if len(pc.ClosedBusinessObs) != 0 {
		t.Fatalf("PCs don't accrue shut memory, got %v", pc.ClosedBusinessObs)
	}
}

func TestClosedBusiness_StaleArrivalIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["farm"] = &Structure{ID: "farm", DisplayName: "Ellis Farm"}
	w.Actors["dairyer"] = cbAgent("dairyer", "farm", "general_store")
	// Event says farm, but john has already moved on (InsideStructureID != farm).
	john := cbAgent("john", "tavern", "tavern")
	w.Actors["john"] = john

	handleClosedBusinessOnArrival(w, arrivedInside("john", "farm", now))

	if len(john.ClosedBusinessObs) != 0 {
		t.Fatalf("a superseded arrival (current structure != event) must not record, got %v", john.ClosedBusinessObs)
	}
}

// TestClosedBusiness_VisitPathResolvesViaLoiter covers the StructureVisit arrival
// (FinalStructureID empty, actor stands at the loiter slot outside). Structures
// share their id with their village_object placement, so the loiter-resolved
// object id is the structure id — the John-Ellis owner-only-farm path.
func TestClosedBusiness_VisitPathResolvesViaLoiter(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	at := WorldToTile(0, 0)
	x, y := 0, 0
	// Object "farm" placed so its loiter pin sits on the arrival tile.
	w.VillageObjects["farm"] = &VillageObject{
		ID: "farm", DisplayName: "Ellis Farm", AssetID: "a",
		Pos: WorldPos{X: 0, Y: 0}, LoiterOffsetX: &x, LoiterOffsetY: &y,
	}
	w.Structures["farm"] = &Structure{ID: "farm", DisplayName: "Ellis Farm"}
	w.Actors["dairyer"] = cbAgent("dairyer", "farm", "general_store") // worker elsewhere
	john := cbAgent("john", "tavern", "")                             // outdoors at the slot
	john.Pos = at
	w.Actors["john"] = john

	// Visit arrival: FinalStructureID empty, FinalPosition at the loiter slot.
	handleClosedBusinessOnArrival(w, &ActorArrived{ActorID: "john", FinalPosition: at, At: now})

	if _, ok := john.ClosedBusinessObs["farm"]; !ok {
		t.Fatalf("visit to the keeperless farm (via loiter resolution) must record shut, got %v", john.ClosedBusinessObs)
	}
}
