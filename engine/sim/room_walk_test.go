package sim

import (
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_walk_test.go — LLM-768. Villagers walk inside a room to what they do:
// the stoker to the hearth (the keeper back after, by the keeper's way), a
// diner to a free seat, an idle guest a step or three now and then.

const squareInn StructureID = "019d98af-ac9b-7833-8e03-5a7015bb5b0c"

// lastWalk is the last square change for id, or nil.
func lastWalk(events []Event, id ActorID) *ActorRoomSquareChanged {
	var out *ActorRoomSquareChanged
	for _, e := range events {
		if c, ok := e.(*ActorRoomSquareChanged); ok && c.ActorID == id {
			out = c
		}
	}
	return out
}

func walkCount(events []Event) int {
	n := 0
	for _, e := range events {
		if _, ok := e.(*ActorRoomSquareChanged); ok {
			n++
		}
	}
	return n
}

func onSquare(a *Actor, s roomlayout.Square) bool {
	return a.RoomSquare != nil && *a.RoomSquare == s
}

func TestStokerWalksToTheHearthAndTheKeeperBack(t *testing.T) {
	for _, sid := range []StructureID{squareTavern, squareInn} {
		keeper := outdoors("keeper", KindNPCStateful)
		keeper.WorkStructureID = sid
		w, events := squareWorld(keeper)
		RegisterRoomWalkSubscriber(w)
		plan := roomlayout.For(string(sid))
		hearth := plan.StationFor(roomlayout.UseStoke).Square
		setActorInsideStructure(w, keeper, sid)
		*events = nil

		w.emit(&SourceActivityStarted{ActorID: keeper.ID, Kind: SourceActivityStoke})
		walk := lastWalk(*events, keeper.ID)
		if !onSquare(keeper, hearth) || walk == nil || walk.Path[0] != plan.Keeper.Square || walk.Path[len(walk.Path)-1] != hearth {
			t.Fatalf("%s: stoke start: keeper on %v, walk %+v; want the keeper's place to the hearth %v", plan.Name, keeper.RoomSquare, walk, hearth)
		}
		w.emit(&SourceActivityCompleted{ActorID: keeper.ID, Kind: SourceActivityStoke})
		if !onSquare(keeper, plan.Keeper.Square) {
			t.Errorf("%s: stoke done: keeper on %v, want back at their place", plan.Name, keeper.RoomSquare)
		}

		// A stoke cancelled by walking out: no walk back inside.
		w.emit(&SourceActivityStarted{ActorID: keeper.ID, Kind: SourceActivityStoke})
		keeper.MoveIntent = &MoveIntent{}
		*events = nil
		w.emit(&SourceActivityCancelled{ActorID: keeper.ID, Kind: SourceActivityStoke})
		if walkCount(*events) != 0 || !onSquare(keeper, hearth) {
			t.Errorf("%s: cancelled on the way out: %d walks, on %v", plan.Name, walkCount(*events), keeper.RoomSquare)
		}
		keeper.MoveIntent = nil
		w.emit(&SourceActivityCancelled{ActorID: keeper.ID, Kind: SourceActivityStoke})
		if !onSquare(keeper, plan.Keeper.Square) {
			t.Errorf("%s: cancelled in place: keeper on %v, want back at their place", plan.Name, keeper.RoomSquare)
		}
	}
}

func TestStokeWalkEdges(t *testing.T) {
	hand := outdoors("hand", KindNPCStateful)
	guest := outdoors("guest", KindNPCStateful)
	w, events := squareWorld(hand, guest)
	RegisterRoomWalkSubscriber(w)
	plan := roomlayout.For(string(squareTavern))
	hearth := plan.StationFor(roomlayout.UseStoke).Square
	setActorInsideStructure(w, hand, squareTavern)
	setActorInsideStructure(w, guest, squareTavern)

	// A hired hand (not the keeper) walks to the fire and stays there after.
	if onSquare(guest, hearth) {
		guest.RoomSquare = &roomlayout.Square{X: 0, Y: 4}
	}
	w.emit(&SourceActivityStarted{ActorID: hand.ID, Kind: SourceActivityStoke})
	if !onSquare(hand, hearth) {
		t.Fatalf("hand on %v, want the hearth", hand.RoomSquare)
	}
	w.emit(&SourceActivityCompleted{ActorID: hand.ID, Kind: SourceActivityStoke})
	if !onSquare(hand, hearth) {
		t.Errorf("after stoking the hand went to %v; only the keeper goes back", hand.RoomSquare)
	}

	// Someone already at the hearth keeps it: the stoker stays put.
	*events = nil
	w.emit(&SourceActivityStarted{ActorID: guest.ID, Kind: SourceActivityStoke})
	if walkCount(*events) != 0 {
		t.Errorf("the hearth is held, yet %d walks", walkCount(*events))
	}
	// Other activities send nobody anywhere.
	w.emit(&SourceActivityStarted{ActorID: guest.ID, Kind: SourceActivityRepair})
	if walkCount(*events) != 0 {
		t.Errorf("a repair start walked someone")
	}
}

func TestDinerWalksToAFreeSeat(t *testing.T) {
	diner := outdoors("diner", KindNPCStateful)
	pc := outdoors("pc", KindPC)
	keeper := outdoors("keeper", KindNPCStateful)
	keeper.WorkStructureID = squareTavern
	w, events := squareWorld(diner, pc, keeper)
	RegisterRoomWalkSubscriber(w)
	for _, a := range []*Actor{diner, pc, keeper} {
		setActorInsideStructure(w, a, squareTavern)
	}
	// Standing at the window: the nearest seat is the east one.
	diner.RoomSquare = &roomlayout.Square{X: 4, Y: 4}
	*events = nil
	w.emit(&DwellStarted{ActorID: diner.ID, Kind: "stew"})
	if !onSquare(diner, roomlayout.Square{X: 3, Y: 6}) {
		t.Errorf("diner on %v, want the nearest seat [3,6]", diner.RoomSquare)
	}
	// Already seated: no walk.
	*events = nil
	w.emit(&DwellStarted{ActorID: diner.ID, Kind: "stew"})
	if walkCount(*events) != 0 {
		t.Errorf("a seated diner walked")
	}
	// Players and keepers are not moved to eat.
	w.emit(&DwellStarted{ActorID: pc.ID, Kind: "stew"})
	w.emit(&DwellStarted{ActorID: keeper.ID, Kind: "stew"})
	if walkCount(*events) != 0 || onSquare(pc, roomlayout.Square{X: 1, Y: 6}) {
		t.Errorf("a player or keeper was walked to a seat")
	}
	// Every seat held: the diner eats where they stand.
	other := outdoors("other", KindNPCStateful)
	w.Actors[other.ID] = other
	w.outdoorActors[other.ID] = struct{}{}
	setActorInsideStructure(w, other, squareTavern)
	other.RoomSquare = &roomlayout.Square{X: 1, Y: 6}
	diner.RoomSquare = &roomlayout.Square{X: 4, Y: 4}
	pc.RoomSquare = &roomlayout.Square{X: 3, Y: 6}
	*events = nil
	w.emit(&DwellStarted{ActorID: diner.ID, Kind: "stew"})
	if walkCount(*events) != 0 {
		t.Errorf("no seat free, yet the diner walked")
	}
}

func TestIdleGuestsDrift(t *testing.T) {
	guest := outdoors("guest", KindNPCStateful)
	eater := outdoors("eater", KindNPCStateful)
	busy := outdoors("busy", KindNPCStateful)
	pc := outdoors("pc", KindPC)
	keeper := outdoors("keeper", KindNPCStateful)
	keeper.WorkStructureID = squareTavern
	w, events := squareWorld(guest, eater, busy, pc, keeper)
	plan := roomlayout.For(string(squareTavern))
	for _, a := range []*Actor{guest, eater, busy, pc, keeper} {
		setActorInsideStructure(w, a, squareTavern)
	}
	eater.DwellCredits = map[DwellCreditKey]*DwellCredit{{Source: DwellSourceItem}: {}}
	busy.SourceActivity = &SourceActivity{Kind: SourceActivityRepair}
	rng := rand.New(rand.NewSource(1))
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	*events = nil

	// The first pass only sets each idle guest's time.
	if res, _ := DriftInRooms(start, rng).Fn(w); res.(RoomDriftResult).Drifted != 0 || walkCount(*events) != 0 {
		t.Fatalf("the first pass moved someone")
	}
	if wait := guest.RoomDriftAt.Sub(start); wait < roomDriftMin || wait > roomDriftMax {
		t.Errorf("first wait %v, want between %v and %v", wait, roomDriftMin, roomDriftMax)
	}
	for _, a := range []*Actor{eater, busy, pc, keeper} {
		if !a.RoomDriftAt.IsZero() {
			t.Errorf("%s got a drift time; only idle guests drift", a.ID)
		}
	}
	// Before the time: nothing.
	DriftInRooms(guest.RoomDriftAt.Add(-time.Second), rng).Fn(w)
	if walkCount(*events) != 0 {
		t.Errorf("drifted early")
	}
	// Over many turns: only the guest moves, a step to three each time, never
	// onto the door or someone else's square.
	at := guest.RoomDriftAt
	for i := 0; i < 40; i++ {
		from := *guest.RoomSquare
		*events = nil
		DriftInRooms(at, rng).Fn(w)
		if walkCount(*events) != 1 {
			t.Fatalf("turn %d: %d walks, want the guest's one", i, walkCount(*events))
		}
		walk := lastWalk(*events, guest.ID)
		if walk == nil || walk.Path[0] != from || len(walk.Path) < 2 || len(walk.Path)-1 > roomDriftSteps {
			t.Fatalf("turn %d: walk %+v from %v, want 1 to %d steps", i, walk, from, roomDriftSteps)
		}
		to := *guest.RoomSquare
		if to == plan.Door.Square || !plan.Walkable(to) {
			t.Fatalf("turn %d: drifted onto %v", i, to)
		}
		for _, other := range []*Actor{eater, busy, pc, keeper} {
			if other.RoomSquare != nil && *other.RoomSquare == to {
				t.Fatalf("turn %d: drifted onto %s's square", i, other.ID)
			}
		}
		if wait := guest.RoomDriftAt.Sub(at); wait < roomDriftMin || wait > roomDriftMax {
			t.Fatalf("turn %d: next wait %v", i, wait)
		}
		at = guest.RoomDriftAt
	}
	// Going out clears the time; coming back starts afresh.
	setActorInsideStructure(w, guest, "")
	setActorInsideStructure(w, guest, squareTavern)
	if !guest.RoomDriftAt.IsZero() {
		t.Errorf("drift time kept across an inside change: %v", guest.RoomDriftAt)
	}
}

// code_review: someone with no square starts from the door but does not own
// it — a walk to a door someone else holds is refused, nothing changes.
func TestWalkInRoomNoSquareDoesNotTakeAHeldDoor(t *testing.T) {
	pc := outdoors("pc", KindPC)
	other := outdoors("other", KindPC)
	w, events := squareWorld(pc, other)
	plan := roomlayout.For(string(squareTavern))
	setActorInsideStructure(w, other, squareTavern)
	setActorInsideStructure(w, pc, squareTavern)
	other.RoomSquare = &roomlayout.Square{X: plan.Door.Square.X, Y: plan.Door.Square.Y}
	pc.RoomSquare = nil
	*events = nil
	if path := walkInRoom(w, pc, plan.Door.Square); path != nil {
		t.Errorf("walked onto a held door: %v", path)
	}
	if pc.RoomSquare != nil || *other.RoomSquare != plan.Door.Square || walkCount(*events) != 0 {
		t.Errorf("after the refused walk: pc %v, other %v, %d walks", pc.RoomSquare, other.RoomSquare, walkCount(*events))
	}
}

// code_review: someone on their way out is never walked inside — not to the
// hearth, not to a seat.
func TestNoRoomWalkOnTheWayOut(t *testing.T) {
	keeper := outdoors("keeper", KindNPCStateful)
	keeper.WorkStructureID = squareTavern
	diner := outdoors("diner", KindNPCStateful)
	w, events := squareWorld(keeper, diner)
	RegisterRoomWalkSubscriber(w)
	setActorInsideStructure(w, keeper, squareTavern)
	setActorInsideStructure(w, diner, squareTavern)
	diner.RoomSquare = &roomlayout.Square{X: 4, Y: 4}
	keeperAt, dinerAt := *keeper.RoomSquare, *diner.RoomSquare
	keeper.MoveIntent = &MoveIntent{}
	diner.MoveIntent = &MoveIntent{}
	*events = nil
	w.emit(&SourceActivityStarted{ActorID: keeper.ID, Kind: SourceActivityStoke})
	w.emit(&DwellStarted{ActorID: diner.ID, Kind: "stew"})
	if walkCount(*events) != 0 || *keeper.RoomSquare != keeperAt || *diner.RoomSquare != dinerAt {
		t.Errorf("walked while leaving: %d walks, keeper %v, diner %v", walkCount(*events), keeper.RoomSquare, diner.RoomSquare)
	}
}

// LLM-788 (live: Silence and Patience Walker one square apart in a column):
// idle drift never steps up beside someone else — a step goes to a square with
// nobody on the eight round it, or, for someone already crowded, to more room
// than they had.
func TestDriftNeverStepsBesideSomeone(t *testing.T) {
	silence := outdoors("silence", KindNPCStateful)
	patience := outdoors("patience", KindNPCStateful)
	anne := outdoors("anne", KindNPCStateful)
	w, events := homeWorld(silence, patience, anne)
	family := []*Actor{silence, patience, anne}
	for _, a := range family {
		setActorInsideStructure(w, a, homeID)
	}
	rng := rand.New(rand.NewSource(7))
	at := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	DriftInRooms(at, rng).Fn(w) // sets each one's time
	walks := 0
	for i := 0; i < 80; i++ {
		at = at.Add(roomDriftMax + time.Second)
		before := map[ActorID]roomlayout.Square{}
		for _, a := range family {
			before[a.ID] = *a.RoomSquare
		}
		*events = nil
		DriftInRooms(at, rng).Fn(w)
		for _, a := range family {
			if *a.RoomSquare == before[a.ID] {
				continue
			}
			walks++
			others := takenRoomSquares(w, homeID, "", a.ID)
			had := map[roomlayout.Square]bool{}
			for id, s := range before {
				if id != a.ID {
					had[s] = true
				}
			}
			now, was := roomlayout.Room(*a.RoomSquare, others), roomlayout.Room(before[a.ID], had)
			if now <= 1 && now <= was {
				t.Fatalf("turn %d: %s stepped from %v to %v, beside someone (room %d, had %d)", i, a.ID, before[a.ID], *a.RoomSquare, now, was)
			}
		}
	}
	if walks == 0 {
		t.Fatal("harness: nobody ever drifted")
	}
}
