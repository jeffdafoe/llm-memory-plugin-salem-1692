package sim

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_go_test.go — LLM-801. The Inn's upstairs: a lodger climbs from the entry
// hall to the hall and into their own room; nobody else may go up; doors to
// rooms that are not theirs are refused with the right line; upstairs is out
// of the building's conversation; going to bed — by hand or idle — puts them
// in their bed upstairs, and they wake there.

const upInn StructureID = "019d98af-ac9b-7833-8e03-5a7015bb5b0c"

const (
	upCommon   RoomID = 1
	upBedroom1 RoomID = 11
	upBedroom2 RoomID = 12
	upBedroom3 RoomID = 13
	upBedroom4 RoomID = 14
	upQuarters RoomID = 15
	upHall     RoomID = 16
)

// upstairsWorld: the Inn with its rooms as the live seed has them, the hall
// included, and the actors outdoors.
func upstairsWorld(actors ...*Actor) (*World, *[]Event) {
	w, events := squareWorld(actors...)
	w.Structures = map[StructureID]*Structure{upInn: {ID: upInn, DisplayName: "Inn", Rooms: []*Room{
		{ID: upCommon, StructureID: upInn, Kind: RoomKindCommon, Name: "common"},
		{ID: upBedroom1, StructureID: upInn, Kind: RoomKindPrivate, Name: "bedroom_1"},
		{ID: upBedroom2, StructureID: upInn, Kind: RoomKindPrivate, Name: "bedroom_2"},
		{ID: upBedroom3, StructureID: upInn, Kind: RoomKindPrivate, Name: "bedroom_3"},
		{ID: upBedroom4, StructureID: upInn, Kind: RoomKindPrivate, Name: "bedroom_4"},
		{ID: upQuarters, StructureID: upInn, Kind: RoomKindStaff, Name: "keeper_quarters"},
		{ID: upHall, StructureID: upInn, Kind: RoomKindHall, Name: "hall"},
	}}}
	return w, events
}

// upLodger: a player who holds bedroom 2 until `until`, outdoors.
func upLodger(id ActorID, room RoomID, until time.Time) *Actor {
	a := outdoors(id, KindPC)
	a.LoginUsername = string(id)
	a.RoomAccess = map[RoomAccessKey]*RoomAccess{
		{RoomID: room, Source: AccessSourceLedger}: {RoomID: room, Source: AccessSourceLedger, Active: true, ExpiresAt: &until},
	}
	return a
}

func goRoom(t *testing.T, w *World, id ActorID, to string, now time.Time) (RoomGoResult, error) {
	t.Helper()
	res, err := MovePCToRoom(id, to, now).Fn(w)
	if err != nil {
		return RoomGoResult{}, err
	}
	return res.(RoomGoResult), nil
}

func refusedLine(err error) string {
	line, _ := IsRoomRefused(err)
	return line
}

func TestUpstairsPlansParse(t *testing.T) {
	hall := roomlayout.ForRoom(string(upInn), "hall")
	if hall == nil || hall.Way("common") == nil {
		t.Fatal("the Inn's hall has no plan, or no stairs down")
	}
	for _, name := range []string{"bedroom_1", "bedroom_2", "bedroom_3", "bedroom_4", "keeper_quarters"} {
		if hall.Way(name) == nil {
			t.Errorf("the hall has no door to %s", name)
		}
	}
	for _, name := range []string{"bedroom_1", "bedroom_2", "bedroom_3", "bedroom_4"} {
		room := roomlayout.ForRoom(string(upInn), name)
		if room == nil || room.Way("hall") == nil || room.StationFor(roomlayout.UseSleep) == nil || room.StationFor(roomlayout.UseRise) == nil {
			t.Errorf("%s: no plan, or no door, bed or bedside", name)
		}
	}
	if roomlayout.For(string(upInn)).Way("hall") == nil {
		t.Error("the entry hall has no stairs up")
	}
	if roomlayout.ForRoom(string(upInn), "keeper_quarters") != nil {
		t.Error("the keeper's quarters have a plan: nobody but the keeper is ever drawn there")
	}
}

func TestLodgerGoesUpstairsAndIntoTheirRoom(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(24*time.Hour))
	other := upLodger("silas", upBedroom3, now.Add(24*time.Hour))
	other.Kind = KindNPCStateful
	w, events := upstairsWorld(jeff, other)
	setActorInsideStructure(w, jeff, upInn)

	res, err := goRoom(t, w, "jeff", "hall", now)
	hall := roomlayout.ForRoom(string(upInn), "hall")
	if err != nil || res.Room != "hall" || jeff.InsideRoomID != upHall {
		t.Fatalf("up the stairs: %+v %v, room %d", res, err, jeff.InsideRoomID)
	}
	if res.Square == nil || *res.Square != hall.Door.Square {
		t.Errorf("arrived at %v, want the stairhead %v", res.Square, hall.Door.Square)
	}
	last := (*events)[len(*events)-1].(*ActorRoomSquareChanged)
	if last.Room != "hall" || last.Square == nil || *last.Square != hall.Door.Square {
		t.Errorf("the frame says room %q square %v", last.Room, last.Square)
	}

	// Doors that are not theirs.
	for _, c := range []struct{ to, line string }{
		{"bedroom_3", roomRefusedLet},
		{"bedroom_1", roomRefusedNotYours},
		{"keeper_quarters", roomRefusedKeeper},
	} {
		if _, err := goRoom(t, w, "jeff", c.to, now); refusedLine(err) != c.line {
			t.Errorf("%s: %v, want %q", c.to, err, c.line)
		}
	}
	if jeff.InsideRoomID != upHall {
		t.Fatalf("a refused door moved them to room %d", jeff.InsideRoomID)
	}

	res, err = goRoom(t, w, "jeff", "bedroom_2", now)
	guest := roomlayout.ForRoom(string(upInn), "bedroom_2")
	if err != nil || jeff.InsideRoomID != upBedroom2 || res.Square == nil || *res.Square != guest.Door.Square {
		t.Fatalf("into bedroom 2: %+v %v", res, err)
	}
	// A bedroom leads only back to the hall.
	if _, err := goRoom(t, w, "jeff", "common", now); !errors.Is(err, ErrRoomSquareRefused) {
		t.Errorf("bedroom straight to the common room: %v", err)
	}

	res, err = goRoom(t, w, "jeff", "hall", now)
	if err != nil || res.Square == nil || *res.Square != hall.Way("bedroom_2").Square {
		t.Errorf("out of bedroom 2: %+v %v, want their door %v", res, err, hall.Way("bedroom_2").Square)
	}
	res, err = goRoom(t, w, "jeff", "common", now)
	stairFoot := roomlayout.For(string(upInn)).Way("hall").Square
	if err != nil || jeff.InsideRoomID != 0 || res.Room != "" || res.Square == nil || *res.Square != stairFoot {
		t.Errorf("down the stairs: %+v %v, room %d, want the stair foot %v", res, err, jeff.InsideRoomID, stairFoot)
	}
}

func TestOnlyGuestsAndTheKeeperGoUpstairs(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	visitor := outdoors("wendy", KindPC)
	lapsed := upLodger("abe", upBedroom1, now.Add(-time.Hour))
	w, _ := upstairsWorld(visitor, lapsed)
	setActorInsideStructure(w, visitor, upInn)
	setActorInsideStructure(w, lapsed, upInn)
	for _, id := range []ActorID{"wendy", "abe"} {
		if _, err := goRoom(t, w, id, "hall", now); refusedLine(err) != roomRefusedHall {
			t.Errorf("%s up the stairs: %v, want %q", id, err, roomRefusedHall)
		}
	}
	keeper := outdoors("hannah", KindNPCShared)
	keeper.WorkStructureID = upInn
	w.Actors["hannah"] = keeper
	if !canEnterRoom(w, keeper, upHall, now) {
		t.Error("the keeper may not go up her own stairs")
	}

	// The keeper upstairs, done stoking: the hall has no keeper's place to go
	// back to, and nothing breaks for it (code_review).
	setActorInsideStructure(w, keeper, upInn)
	setActorRoom(w, keeper, upHall)
	keeperBackToPlace(w, keeper)
	if keeper.RoomSquareRoom != "hall" || keeper.RoomSquare == nil {
		t.Errorf("the keeper in the hall is on %v in %q", keeper.RoomSquare, keeper.RoomSquareRoom)
	}
}

func TestUpstairsIsOutOfTheConversation(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(24*time.Hour))
	jeff.LastPCSeenAt = &now
	hannah := outdoors("hannah", KindNPCShared)
	hannah.WorkStructureID = upInn
	silas := outdoors("silas", KindNPCStateful)
	// A whole world, for huddles and the scene a building's conversation is
	// anchored to (which needs the structure's placement — the Shared-Identity
	// Bridge).
	w, _ := upstairsWorld(jeff, hannah, silas)
	w.Huddles = map[HuddleID]*Huddle{}
	w.Scenes = map[SceneID]*Scene{}
	w.actorsByHuddle = map[HuddleID]map[ActorID]struct{}{}
	w.carryoverByStructure = map[StructureID]*conversationCarryover{}
	w.VillageObjects = map[VillageObjectID]*VillageObject{VillageObjectID(upInn): {ID: VillageObjectID(upInn)}}
	for _, a := range []*Actor{jeff, hannah, silas} {
		setActorInsideStructure(w, a, upInn)
	}
	if _, err := EnsureColocatedHuddle("hannah", now).Fn(w); err != nil || jeff.CurrentHuddleID == "" {
		t.Fatalf("downstairs, Hannah's talk does not take Jeff in: %v", err)
	}
	if _, err := goRoom(t, w, "jeff", "hall", now); err != nil {
		t.Fatal(err)
	}
	if jeff.CurrentHuddleID != "" {
		t.Error("going upstairs left them in the conversation downstairs")
	}
	for _, id := range colocatedConversationalActors(w, silas, upInn, now) {
		if id == "jeff" {
			t.Error("talk downstairs would draw in Jeff upstairs")
		}
	}
	for _, id := range colocatedAudienceIDs(w, silas, now) {
		if id == "jeff" {
			t.Error("Silas downstairs counts Jeff upstairs as someone to hear him")
		}
	}
	if _, err := EnsureColocatedHuddle("jeff", now).Fn(w); err != nil || jeff.CurrentHuddleID != "" {
		t.Errorf("speaking in the hall joined the building's conversation: %v", err)
	}
	if ids := colocatedAudienceIDs(w, jeff, now); len(ids) != 0 {
		t.Errorf("in the hall Jeff would be heard by %v", ids)
	}
}

func TestGoingToBedUpstairsAndWakingThere(t *testing.T) {
	now := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(24*time.Hour))
	w, _ := upstairsWorld(jeff)
	setActorInsideStructure(w, jeff, upInn)
	guest := roomlayout.ForRoom(string(upInn), "bedroom_2")

	// From the common room, as the bed tap and the idle arm both do.
	if _, err := SleepPC("jeff", now).Fn(w); err != nil {
		t.Fatal(err)
	}
	if jeff.InsideRoomID != upBedroom2 || jeff.RoomSquareRoom != "bedroom_2" || jeff.RoomSquare == nil || !guest.IsBed(*jeff.RoomSquare) {
		t.Fatalf("asleep in room %d %q on %v, want their bed in bedroom 2", jeff.InsideRoomID, jeff.RoomSquareRoom, jeff.RoomSquare)
	}

	if _, err := WakePC("jeff", now.Add(time.Hour)).Fn(w); err != nil {
		t.Fatal(err)
	}
	bedside := guest.StationFor(roomlayout.UseRise).Square
	if jeff.InsideRoomID != upBedroom2 || jeff.RoomSquare == nil || *jeff.RoomSquare != bedside {
		t.Errorf("woke in room %d on %v, want bedroom 2 beside the bed %v", jeff.InsideRoomID, jeff.RoomSquare, bedside)
	}
}

func TestIdleLodgerIsBeddedUpstairsAndWakesThere(t *testing.T) {
	now := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(24*time.Hour))
	jeff.Needs["tiredness"] = 60
	idleSince := now.Add(-time.Hour)
	jeff.LastPCInputAt = &idleSince
	jeff.LastPCSeenAt = &now
	w, _ := upstairsWorld(jeff)
	RegisterLodgingMorningDescentSubscriber(w)
	setActorInsideStructure(w, jeff, upInn)

	if _, err := AutoBedIdleLodgerPCs(now).Fn(w); err != nil {
		t.Fatal(err)
	}
	guest := roomlayout.ForRoom(string(upInn), "bedroom_2")
	if jeff.State != StateSleeping || jeff.RoomSquare == nil || !guest.IsBed(*jeff.RoomSquare) {
		t.Fatalf("idle in the common room: state %s, square %v %q — want asleep in their bed", jeff.State, jeff.RoomSquare, jeff.RoomSquareRoom)
	}

	// Rested by morning: the sweep wakes them, and they stay in their room —
	// no descent to the common room.
	jeff.Needs["tiredness"] = 0
	later := now.Add(8 * time.Hour)
	jeff.LastPCSeenAt = &later
	if _, err := WakeExpiredPCSleepers(later).Fn(w); err != nil {
		t.Fatal(err)
	}
	if jeff.State == StateSleeping || jeff.InsideRoomID != upBedroom2 {
		t.Errorf("morning: state %s, room %d — want awake in bedroom 2", jeff.State, jeff.InsideRoomID)
	}
}

func TestStayRunsOutUpstairs(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(time.Hour))
	w, _ := upstairsWorld(jeff)
	setActorInsideStructure(w, jeff, upInn)
	if _, err := goRoom(t, w, "jeff", "hall", now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Hour)
	if _, err := ExpireRoomAccess(later).Fn(w); err != nil {
		t.Fatal(err)
	}
	if _, err := EvictExpiredOccupants(later).Fn(w); err != nil {
		t.Fatal(err)
	}
	if jeff.InsideRoomID != upCommon || jeff.RoomSquareRoom != "" || jeff.RoomSquare == nil {
		t.Errorf("after the stay ran out: room %d %q square %v, want the common room", jeff.InsideRoomID, jeff.RoomSquareRoom, jeff.RoomSquare)
	}
}

func TestUpstairsSquaresAfterARestart(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(24*time.Hour))
	silas := outdoors("silas", KindNPCStateful)
	w, _ := upstairsWorld(jeff, silas)
	setActorInsideStructure(w, jeff, upInn)
	setActorInsideStructure(w, silas, upInn)
	if _, err := goRoom(t, w, "jeff", "hall", now); err != nil {
		t.Fatal(err)
	}
	if _, err := goRoom(t, w, "jeff", "bedroom_2", now); err != nil {
		t.Fatal(err)
	}
	w.placeRoomSquaresOnLoad()
	guest := roomlayout.ForRoom(string(upInn), "bedroom_2")
	if jeff.RoomSquareRoom != "bedroom_2" || jeff.RoomSquare == nil || *jeff.RoomSquare != guest.Door.Square {
		t.Errorf("after a restart Jeff is on %v in %q, want bedroom 2's door", jeff.RoomSquare, jeff.RoomSquareRoom)
	}
	if silas.RoomSquareRoom != "" || silas.RoomSquare == nil {
		t.Errorf("after a restart Silas is on %v in %q, want the common room", silas.RoomSquare, silas.RoomSquareRoom)
	}
}

func TestVacantRooms(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 0, 0, 0, time.UTC)
	jeff := upLodger("jeff", upBedroom2, now.Add(time.Hour))
	gone := upLodger("abe", upBedroom4, now.Add(-time.Hour))
	w, _ := upstairsWorld(jeff, gone)
	snap := &Snapshot{PublishedAt: now, Structures: map[StructureID]*Structure{upInn: w.Structures[upInn]},
		Actors: map[ActorID]*ActorSnapshot{"jeff": {RoomAccess: jeff.RoomAccess}, "abe": {RoomAccess: gone.RoomAccess}}}
	got := VacantRooms(snap, upInn)
	want := []string{"bedroom_1", "bedroom_3", "bedroom_4"}
	if len(got) != len(want) {
		t.Fatalf("vacant %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("vacant %v, want %v", got, want)
		}
	}
}
