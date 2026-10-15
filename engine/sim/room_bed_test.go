package sim

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_bed_test.go — LLM-782. A home has its house type's plan: people inside
// get squares as in the Tavern; someone going to sleep takes a free bed and is
// drawn there; past the beds they sleep in the loft (no square); waking, they
// get up onto the floor. A lodger in a private room holds no bed.

const homeID StructureID = "home-cole"

// homeWorld: a world with one home placed as a Red House (Medium) — a bed and
// a straw pallet.
func homeWorld(actors ...*Actor) (*World, *[]Event) {
	w, events := squareWorld(actors...)
	w.Assets = map[AssetID]*Asset{"red-medium": {ID: "red-medium", Name: "Red House (Medium)"}}
	w.VillageObjects = map[VillageObjectID]*VillageObject{VillageObjectID(homeID): {ID: VillageObjectID(homeID), AssetID: "red-medium"}}
	w.Structures = map[StructureID]*Structure{homeID: {ID: homeID, DisplayName: "Cole Residence"}}
	return w, events
}

func bedSquares(plan *roomlayout.Layout) map[roomlayout.Square]bool {
	beds := map[roomlayout.Square]bool{}
	for _, st := range plan.Stations {
		for _, u := range st.Use {
			if u == roomlayout.UseSleep {
				beds[st.Square] = true
			}
		}
	}
	return beds
}

func TestHomeSleepersTakeBedsThenTheLoft(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	family := []*Actor{outdoors("silence", KindNPCStateful), outdoors("lewis", KindNPCStateful), outdoors("anne", KindNPCStateful)}
	w, events := homeWorld(family...)
	plan := roomPlan(w, homeID)
	if plan == nil || plan.Art != "home_red_medium" {
		t.Fatalf("the home resolves to %v, want the Red House (Medium) plan", plan)
	}
	beds := bedSquares(plan)
	if len(beds) != 2 {
		t.Fatalf("harness: the plan has %d beds, want 2", len(beds))
	}
	for _, a := range family {
		setActorInsideStructure(w, a, homeID)
		if a.RoomSquare == nil || beds[*a.RoomSquare] {
			t.Fatalf("%s awake at %v, want a square that is not a bed", a.ID, a.RoomSquare)
		}
	}

	// The first two to sleep take the two beds, and the frames say so.
	held := map[roomlayout.Square]bool{}
	for _, a := range family[:2] {
		*events = nil
		if !executeNPCSleep(w, a, now) {
			t.Fatalf("%s did not go to sleep", a.ID)
		}
		if a.RoomSquare == nil || !beds[*a.RoomSquare] || held[*a.RoomSquare] {
			t.Fatalf("%s asleep at %v, want a free bed", a.ID, a.RoomSquare)
		}
		held[*a.RoomSquare] = true
		if !hasSquareChange(*events, a.ID, a.RoomSquare) {
			t.Errorf("no bed frame for %s: %#v", a.ID, *events)
		}
	}
	// The third finds both beds taken: the loft, no square.
	executeNPCSleep(w, family[2], now)
	if family[2].RoomSquare != nil {
		t.Errorf("the third sleeper holds %v, want none (the loft)", family[2].RoomSquare)
	}

	// Waking, a sleeper gets up out of bed onto the floor.
	*events = nil
	wakeNPC(w, family[0])
	if family[0].RoomSquare == nil || beds[*family[0].RoomSquare] {
		t.Errorf("woken onto %v, want the floor", family[0].RoomSquare)
	}
	if !hasSquareChange(*events, family[0].ID, family[0].RoomSquare) {
		t.Errorf("no get-up frame: %#v", *events)
	}
	// The one from the loft comes down onto the floor too.
	wakeNPC(w, family[2])
	if family[2].RoomSquare == nil || beds[*family[2].RoomSquare] {
		t.Errorf("down from the loft onto %v, want the floor", family[2].RoomSquare)
	}

	// Nobody awake is ever put on a bed, however full the room.
	for i := 0; i < 12; i++ {
		a := outdoors(ActorID("guest-"+string(rune('a'+i))), KindNPCStateful)
		w.Actors[a.ID] = a
		w.outdoorActors[a.ID] = struct{}{}
		setActorInsideStructure(w, a, homeID)
		if a.RoomSquare != nil && beds[*a.RoomSquare] {
			t.Fatalf("awake %s put on the bed %v", a.ID, *a.RoomSquare)
		}
	}
}

// Someone asleep in a private room (a lodger, a keeper in their quarters) is
// upstairs: no bed in the room below, even when one is free.
func TestPrivateRoomSleeperHoldsNoBed(t *testing.T) {
	a := outdoors("ruth", KindNPCStateful)
	w, _ := homeWorld(a)
	setActorInsideStructure(w, a, homeID)
	a.State = StateSleeping
	a.InsideRoomID = 7
	if sq := pickRoomSquare(w, a, homeID); sq != nil {
		t.Errorf("a sleeper in a private room got %v", *sq)
	}
}

func TestPlaceRoomSquaresOnLoadPutsSleepersInBed(t *testing.T) {
	sleeper := &Actor{ID: "anne", Kind: KindNPCStateful, InsideStructureID: homeID, State: StateSleeping}
	awake := &Actor{ID: "lewis", Kind: KindNPCStateful, InsideStructureID: homeID}
	w, _ := homeWorld(sleeper, awake)
	w.actorsByStructure[homeID] = map[ActorID]struct{}{"anne": {}, "lewis": {}}
	w.placeRoomSquaresOnLoad()
	beds := bedSquares(roomPlan(w, homeID))
	if sleeper.RoomSquare == nil || !beds[*sleeper.RoomSquare] {
		t.Errorf("after a restart the sleeper is at %v, want a bed", sleeper.RoomSquare)
	}
	if awake.RoomSquare == nil || beds[*awake.RoomSquare] {
		t.Errorf("after a restart the waker is at %v, want the floor", awake.RoomSquare)
	}
}

// The clients load each structure's plan: the Tavern its own, a home its house
// type's carrying the home's id and name. A structure with neither is left out.
func TestRoomPlansListsHomes(t *testing.T) {
	w, _ := homeWorld()
	w.Structures[squareTavern] = &Structure{ID: squareTavern, DisplayName: "Tavern"}
	w.Structures["well"] = &Structure{ID: "well", DisplayName: "Well"}
	snap := &Snapshot{Structures: w.Structures, VillageObjects: w.VillageObjects, Assets: w.Assets}
	plans := RoomPlans(snap)
	got := map[string]*roomlayout.Layout{}
	for _, p := range plans {
		got[p.StructureID] = p
	}
	if len(plans) != 2 || got[string(squareTavern)] == nil || got[string(homeID)] == nil {
		t.Fatalf("plans for %v, want the Tavern and the home", got)
	}
	home := got[string(homeID)]
	if home.Name != "Cole Residence" || home.Art != "home_red_medium" {
		t.Errorf("the home's plan is named %q with art %q", home.Name, home.Art)
	}
	// The shared plan itself is not changed by naming it for one home.
	if shared := roomlayout.Resolve("another-home", "Red House (Medium)"); shared.StructureID != "" || shared.Name == "Cole Residence" {
		t.Errorf("the shared plan was changed: %q %q", shared.StructureID, shared.Name)
	}
}
