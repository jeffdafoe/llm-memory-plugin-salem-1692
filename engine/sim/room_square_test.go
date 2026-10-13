package sim

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_square_test.go — LLM-768. A square in a room with a floor plan: dealt on
// entering (keeper to the keeper's place, a player to the door, others to a
// free station), dropped on leaving, given up asleep and taken again on
// waking, dealt to everyone inside at boot — and sent with the frames.

const squareTavern StructureID = "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9"

func squareWorld(actors ...*Actor) (*World, *[]Event) {
	w := sleepTestWorld(actors...)
	w.actorsByStructure = map[StructureID]map[ActorID]struct{}{}
	w.outdoorActors = map[ActorID]struct{}{}
	for _, a := range actors {
		w.outdoorActors[a.ID] = struct{}{}
	}
	events := &[]Event{}
	w.Subscribe(SubscriberFunc(func(_ *World, e Event) { *events = append(*events, e) }))
	return w, events
}

func outdoors(id ActorID, kind ActorKind) *Actor {
	return &Actor{ID: id, Kind: kind, Needs: map[NeedKey]int{"tiredness": 20}}
}

func TestRoomSquareOnEnterAndLeave(t *testing.T) {
	keeper := outdoors("john", KindNPCStateful)
	keeper.WorkStructureID = squareTavern
	pc := outdoors("jeff", KindPC)
	guest := outdoors("silas", KindNPCStateful)
	w, events := squareWorld(keeper, pc, guest)
	plan := roomlayout.For(string(squareTavern))

	setActorInsideStructure(w, guest, squareTavern)
	setActorInsideStructure(w, keeper, squareTavern)
	setActorInsideStructure(w, pc, squareTavern)
	if keeper.RoomSquare == nil || *keeper.RoomSquare != plan.Keeper.Square {
		t.Errorf("keeper square = %v, want the keeper's place %v", keeper.RoomSquare, plan.Keeper.Square)
	}
	if pc.RoomSquare == nil || *pc.RoomSquare != plan.Door.Square {
		t.Errorf("player square = %v, want the door %v", pc.RoomSquare, plan.Door.Square)
	}
	stations := map[roomlayout.Square]bool{}
	for _, st := range plan.Stations {
		stations[st.Square] = true
	}
	if guest.RoomSquare == nil || !stations[*guest.RoomSquare] {
		t.Errorf("guest square = %v, want a station", guest.RoomSquare)
	}
	var last *ActorInsideChanged
	for _, e := range *events {
		if ic, ok := e.(*ActorInsideChanged); ok && ic.ActorID == "jeff" {
			last = ic
		}
	}
	if last == nil || last.RoomSquare == nil || *last.RoomSquare != plan.Door.Square {
		t.Errorf("the inside frame carries %v, want the door square", last)
	}

	// Leaving drops the square; the frame carries none.
	setActorInsideStructure(w, pc, "")
	if pc.RoomSquare != nil {
		t.Errorf("outdoors the player still holds %v", pc.RoomSquare)
	}
	if ic, ok := (*events)[len(*events)-1].(*ActorInsideChanged); !ok || ic.RoomSquare != nil {
		t.Errorf("leaving frame = %#v, want an inside change with no square", (*events)[len(*events)-1])
	}
	// A structure without a plan gives no square.
	setActorInsideStructure(w, pc, "cottage")
	if pc.RoomSquare != nil {
		t.Errorf("in a structure without a plan the player holds %v", pc.RoomSquare)
	}
}

func TestRoomSquareTwoGuestsNeverShare(t *testing.T) {
	var actors []*Actor
	for _, id := range []ActorID{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		actors = append(actors, outdoors(id, KindNPCStateful))
	}
	w, _ := squareWorld(actors...)
	held := map[roomlayout.Square]ActorID{}
	for _, a := range actors {
		setActorInsideStructure(w, a, squareTavern)
		if a.RoomSquare == nil {
			continue
		}
		if other, dup := held[*a.RoomSquare]; dup {
			t.Fatalf("%s and %s share %v", a.ID, other, *a.RoomSquare)
		}
		held[*a.RoomSquare] = a.ID
	}
	if len(held) == 0 {
		t.Fatal("nobody was placed")
	}
}

func TestRoomSquareSleepAndWake(t *testing.T) {
	now := time.Date(2026, 10, 13, 4, 0, 0, 0, time.UTC)
	lodger := outdoors("silas", KindNPCStateful)
	w, events := squareWorld(lodger)
	setActorInsideStructure(w, lodger, squareTavern)

	*events = nil
	if !executeNPCSleep(w, lodger, now) {
		t.Fatal("executeNPCSleep should bed the NPC")
	}
	if lodger.RoomSquare != nil {
		t.Errorf("asleep, the lodger still holds %v", lodger.RoomSquare)
	}
	if !hasSquareChange(*events, "silas", nil) {
		t.Errorf("no square-cleared frame on sleeping: %#v", *events)
	}
	*events = nil
	wakeNPC(w, lodger)
	if lodger.RoomSquare == nil {
		t.Fatal("awake, the lodger has no square")
	}
	if !hasSquareChange(*events, "silas", lodger.RoomSquare) {
		t.Errorf("no square frame on waking: %#v", *events)
	}

	// A player asleep gives the square up too, and takes one on waking.
	pc := outdoors("jeff", KindPC)
	w.Actors[pc.ID] = pc
	w.outdoorActors[pc.ID] = struct{}{}
	setActorInsideStructure(w, pc, squareTavern)
	executePCSleep(w, pc, 0, now)
	if pc.RoomSquare != nil {
		t.Errorf("an asleep player still holds %v", pc.RoomSquare)
	}
	wakePCActor(w, pc)
	if pc.RoomSquare == nil {
		t.Error("a woken player has no square")
	}

	// While a player sleeps their square is free: a second player takes the
	// door; the first wakes onto another square — never a shared one.
	plan := roomlayout.For(string(squareTavern))
	if *pc.RoomSquare != plan.Door.Square {
		t.Fatalf("harness: the player woke on %v, not the door", *pc.RoomSquare)
	}
	executePCSleep(w, pc, 0, now)
	second := outdoors("wendy", KindPC)
	w.Actors[second.ID] = second
	w.outdoorActors[second.ID] = struct{}{}
	setActorInsideStructure(w, second, squareTavern)
	if second.RoomSquare == nil || *second.RoomSquare != plan.Door.Square {
		t.Fatalf("the second player took %v, want the door the sleeper gave up", second.RoomSquare)
	}
	wakePCActor(w, pc)
	if pc.RoomSquare == nil || *pc.RoomSquare == *second.RoomSquare {
		t.Errorf("woken onto %v with the door held by %v: want a square of their own", pc.RoomSquare, *second.RoomSquare)
	}
}

func hasSquareChange(events []Event, id ActorID, want *roomlayout.Square) bool {
	for _, e := range events {
		if sc, ok := e.(*ActorRoomSquareChanged); ok && sc.ActorID == id && sameRoomSquare(sc.Square, want) {
			return true
		}
	}
	return false
}

func TestPlaceRoomSquaresOnLoad(t *testing.T) {
	keeper := &Actor{ID: "zz-john", Kind: KindNPCStateful, WorkStructureID: squareTavern, InsideStructureID: squareTavern}
	pc := &Actor{ID: "zz-jeff", Kind: KindPC, InsideStructureID: squareTavern}
	guest := &Actor{ID: "aa-silas", Kind: KindNPCStateful, InsideStructureID: squareTavern}
	sleeper := &Actor{ID: "aa-anne", Kind: KindNPCStateful, InsideStructureID: squareTavern, State: StateSleeping}
	out := &Actor{ID: "aa-ruth", Kind: KindNPCStateful}
	w, _ := squareWorld(keeper, pc, guest, sleeper, out)
	w.actorsByStructure[squareTavern] = map[ActorID]struct{}{"zz-john": {}, "zz-jeff": {}, "aa-silas": {}, "aa-anne": {}}
	w.placeRoomSquaresOnLoad()
	plan := roomlayout.For(string(squareTavern))
	// The keeper sorts last by id but is placed first, so gets their place.
	if keeper.RoomSquare == nil || *keeper.RoomSquare != plan.Keeper.Square {
		t.Errorf("keeper at %v, want the keeper's place", keeper.RoomSquare)
	}
	if pc.RoomSquare == nil || *pc.RoomSquare != plan.Door.Square {
		t.Errorf("player at %v, want the door", pc.RoomSquare)
	}
	if guest.RoomSquare == nil {
		t.Error("the guest was not placed")
	}
	if sleeper.RoomSquare != nil || out.RoomSquare != nil {
		t.Errorf("sleeper %v / outdoors %v placed", sleeper.RoomSquare, out.RoomSquare)
	}
}

// A player walks to a square of the room: the square changes, the frame carries
// the path, and they stay inside with no village move. A square someone else
// holds, the keeper's place, a wall, a sleeper or someone outdoors is refused.
func TestMovePCInRoom(t *testing.T) {
	pc := outdoors("jeff", KindPC)
	guest := outdoors("silas", KindNPCStateful)
	w, events := squareWorld(pc, guest)
	plan := roomlayout.For(string(squareTavern))
	setActorInsideStructure(w, pc, squareTavern)
	setActorInsideStructure(w, guest, squareTavern)
	*events = nil

	west := roomlayout.Square{X: 1, Y: 6}
	if *guest.RoomSquare == west {
		guest.RoomSquare = &roomlayout.Square{X: 3, Y: 6}
	}
	res, err := MovePCInRoom(pc.ID, west).Fn(w)
	if err != nil {
		t.Fatalf("walk to the west seat: %v", err)
	}
	if pc.RoomSquare == nil || *pc.RoomSquare != west || pc.InsideStructureID != squareTavern || pc.MoveIntent != nil {
		t.Errorf("after the walk: square %v inside %q intent %v", pc.RoomSquare, pc.InsideStructureID, pc.MoveIntent)
	}
	path := res.(RoomMoveResult).Path
	if len(path) < 2 || path[0] != plan.Door.Square || path[len(path)-1] != west {
		t.Errorf("path = %v, want door to west seat", path)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %#v, want one square change", *events)
	}
	if e, ok := (*events)[0].(*ActorRoomSquareChanged); !ok || e.Square == nil || *e.Square != west || len(e.Path) != len(path) {
		t.Errorf("frame = %#v, want the west seat with the path", (*events)[0])
	}

	// Staying put: no frame.
	*events = nil
	if _, err := MovePCInRoom(pc.ID, west).Fn(w); err != nil || len(*events) != 0 {
		t.Errorf("a tap on their own square: err %v, events %v", err, *events)
	}

	for name, to := range map[string]roomlayout.Square{
		"someone else's square": *guest.RoomSquare,
		"the keeper's place":    plan.Keeper.Square,
		"the table":             {X: 2, Y: 6},
		"off the grid":          {X: 9, Y: 9},
	} {
		if _, err := MovePCInRoom(pc.ID, to).Fn(w); !errors.Is(err, ErrRoomSquareRefused) {
			t.Errorf("%s: err %v, want refused", name, err)
		}
	}
	if *pc.RoomSquare != west {
		t.Errorf("a refused step moved them to %v", pc.RoomSquare)
	}

	// Asleep, or outdoors: refused.
	pc.State = StateSleeping
	if _, err := MovePCInRoom(pc.ID, plan.Door.Square).Fn(w); !errors.Is(err, ErrRoomSquareRefused) {
		t.Errorf("asleep: err %v, want refused", err)
	}
	pc.State = StateIdle
	setActorInsideStructure(w, pc, "")
	if _, err := MovePCInRoom(pc.ID, plan.Door.Square).Fn(w); !errors.Is(err, ErrRoomSquareRefused) {
		t.Errorf("outdoors: err %v, want refused", err)
	}
	if _, err := MovePCInRoom(guest.ID, plan.Door.Square).Fn(w); err == nil {
		t.Error("a villager was walked by the player's route")
	}
}

// Someone inside with no square (the room was full when they came in) sets
// off from the door.
func TestMovePCInRoomWithNoSquareStartsAtTheDoor(t *testing.T) {
	pc := outdoors("jeff", KindPC)
	w, _ := squareWorld(pc)
	setActorInsideStructure(w, pc, squareTavern)
	pc.RoomSquare = nil
	plan := roomlayout.For(string(squareTavern))
	res, err := MovePCInRoom(pc.ID, roomlayout.Square{X: 3, Y: 6}).Fn(w)
	if err != nil {
		t.Fatal(err)
	}
	if path := res.(RoomMoveResult).Path; path[0] != plan.Door.Square {
		t.Errorf("path = %v, want it to start at the door", path)
	}
}
