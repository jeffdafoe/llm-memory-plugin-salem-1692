package roomlayout

import (
	"strings"
	"testing"
)

const tavern = "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9"
const inn = "019d98af-ac9b-7833-8e03-5a7015bb5b0c"

func TestSeedParses(t *testing.T) {
	for _, id := range []string{tavern, inn} {
		l := For(id)
		if l == nil {
			t.Fatalf("no plan for %s", id)
		}
		if l.Width() != 8 || l.Height() != 7 {
			t.Errorf("%s: grid %dx%d, want 8x7 (the footprint)", l.Name, l.Width(), l.Height())
		}
	}
	if For("nowhere") != nil {
		t.Error("a structure without a room has a plan")
	}
	if got := len(All()); got != 2 {
		t.Errorf("All() = %d plans, want 2", got)
	}
}

func TestPlaceKeeperPCAndGuests(t *testing.T) {
	l := For(tavern)
	taken := map[Square]bool{}
	if s, ok := l.Place(RoleKeeper, "john", taken); !ok || s != l.Keeper.Square {
		t.Errorf("keeper placed at %v %v, want the keeper's place %v", s, ok, l.Keeper.Square)
	}
	if s, ok := l.Place(RolePC, "jeff", taken); !ok || s != l.Door.Square {
		t.Errorf("player placed at %v %v, want the door %v", s, ok, l.Door.Square)
	}
	stations := map[Square]bool{}
	for _, st := range l.Stations {
		stations[st.Square] = true
	}
	// Guests fill the stations first — each its own — then plain floor.
	for i := 0; i < len(l.Stations); i++ {
		s, ok := l.Place(RoleGuest, "guest"+string(rune('a'+i)), taken)
		if !ok || !stations[s] || taken[s] {
			t.Fatalf("guest %d placed at %v %v: want a free station", i, s, ok)
		}
		taken[s] = true
	}
	floor, ok := l.Place(RoleGuest, "late", taken)
	if !ok || stations[floor] || floor == l.Door.Square || floor == l.Keeper.Square || !l.Open(floor) {
		t.Fatalf("with the stations full a guest went to %v %v: want plain open floor", floor, ok)
	}
	// A guest is never put on the door or the keeper's place, even free.
	for {
		s, ok := l.Place(RoleGuest, "crowd", taken)
		if !ok {
			break
		}
		if s == l.Door.Square || s == l.Keeper.Square {
			t.Fatalf("a guest was put on %v", s)
		}
		taken[s] = true
	}
	// The keeper's place taken: the keeper is placed like a guest.
	taken = map[Square]bool{l.Keeper.Square: true}
	if s, ok := l.Place(RoleKeeper, "john", taken); !ok || s == l.Keeper.Square {
		t.Errorf("keeper's place taken: placed at %v %v", s, ok)
	}
}

func TestPlaceSpreadsGuests(t *testing.T) {
	l := For(inn)
	first := map[Square]bool{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		s, _ := l.Place(RoleGuest, id, map[Square]bool{})
		first[s] = true
	}
	if len(first) < 2 {
		t.Errorf("eight guests alone in the room all start on %v: the hash should spread them", first)
	}
}

// LLM-781: guests take the Tavern's seats and its two bar places before the
// hearth (the keeper's work place, in the middle of the room) — whatever their
// id hashes to — and the hearth only once those are full.
func TestPlaceGuestsSeatsAndBarBeforeTheHearth(t *testing.T) {
	l := For(tavern)
	hearth := l.StationFor(UseStoke)
	if hearth == nil {
		t.Fatal("the Tavern has no hearth station")
	}
	bar := 0
	for _, st := range l.Stations {
		if st.has(UseDrink) {
			bar++
			if st.Serve == nil || st.Facing != "east" {
				t.Errorf("bar place %q: serve %v facing %q, want a serve point and facing the bar (east)", st.ID, st.Serve, st.Facing)
			}
		}
	}
	if bar != 2 {
		t.Fatalf("the Tavern has %d bar places, want 2", bar)
	}
	for i := 0; i < 50; i++ {
		id := "guest" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if s, _ := l.Place(RoleGuest, id, map[Square]bool{}); s == hearth.Square {
			t.Fatalf("guest %q alone in the room was put at the hearth", id)
		}
	}
	taken := map[Square]bool{}
	for i := 0; i < len(l.Stations)-1; i++ {
		s, ok := l.Place(RoleGuest, "g"+string(rune('a'+i)), taken)
		if !ok || s == hearth.Square {
			t.Fatalf("guest %d went to %v %v before the other stations were full", i, s, ok)
		}
		taken[s] = true
	}
	if s, ok := l.Place(RoleGuest, "last", taken); !ok || s != hearth.Square {
		t.Errorf("the other stations full: a guest went to %v %v, want the hearth %v", s, ok, hearth.Square)
	}
}

func TestParseRejectsBadPlans(t *testing.T) {
	good := `{"structure_id": "s", "grid": ["..", ".."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 0], "at": [24, 14]}, "stations": [%s]}`
	cases := map[string]string{
		"ragged grid":        `{"structure_id": "s", "grid": ["..", "."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 0], "at": [24, 14]}, "stations": []}`,
		"station on a wall":  strings.Replace(good, `[%s]`, `[{"id": "x", "square": [0, 0], "at": [8, 14]}]`, 1),
		"shared square":      strings.Replace(good, `[%s]`, `[{"id": "x", "square": [0, 1], "at": [8, 14]}]`, 1),
		"drawn outside":      strings.Replace(good, `[%s]`, `[{"id": "x", "square": [1, 1], "at": [40, 14]}]`, 1),
		"bar place unserved": strings.Replace(good, `[%s]`, `[{"id": "x", "square": [1, 1], "at": [24, 30], "use": ["drink"]}]`, 1),
	}
	cases["station on a wall"] = strings.Replace(cases["station on a wall"], `["..", ".."]`, `["#.", ".."]`, 1)
	for name, room := range cases {
		if _, err := Parse([]byte(`{"rooms": [` + room + `]}`)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	ok := strings.Replace(good, `[%s]`, `[{"id": "x", "square": [1, 1], "at": [24, 30]}]`, 1)
	if _, err := Parse([]byte(`{"rooms": [` + ok + `]}`)); err != nil {
		t.Errorf("a good plan was rejected: %v", err)
	}
}

func TestPathWalksTheFloor(t *testing.T) {
	l := For(tavern)
	// Door to the west seat: up the gap by the bar, behind the table, down
	// the west wall — one step at a time, never through a wall or the table.
	path := l.Path(l.Door.Square, Square{1, 6})
	if path == nil || path[0] != l.Door.Square || path[len(path)-1] != (Square{1, 6}) {
		t.Fatalf("door to west seat = %v", path)
	}
	for i := 1; i < len(path); i++ {
		dx, dy := path[i].X-path[i-1].X, path[i].Y-path[i-1].Y
		if dx*dx+dy*dy != 1 || !l.Walkable(path[i]) {
			t.Fatalf("step %v -> %v in %v is not one walkable square", path[i-1], path[i], path)
		}
	}
	if len(path) != 10 {
		t.Errorf("door to west seat took %d squares, want the shortest (10): %v", len(path), path)
	}
	if got := l.Path(l.Door.Square, l.Door.Square); len(got) != 1 {
		t.Errorf("staying put = %v, want just the square", got)
	}
	// Nobody but the keeper walks to the keeper's place, or onto a wall.
	if got := l.Path(l.Door.Square, l.Keeper.Square); got != nil {
		t.Errorf("a path to the keeper's place: %v", got)
	}
	if got := l.Path(l.Door.Square, Square{2, 6}); got != nil {
		t.Errorf("a path onto the table: %v", got)
	}
	inn := For(inn)
	if got := inn.Path(inn.Door.Square, inn.KeeperDoor.Square); got != nil {
		t.Errorf("a path up the keeper's stairs: %v", got)
	}
}

func TestParseRejectsACutOffStation(t *testing.T) {
	room := `{"structure_id": "s", "grid": [".#.", "..."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 1], "at": [24, 30]}, "stations": [{"id": "x", "square": [2, 0], "at": [40, 14]}]}`
	if _, err := Parse([]byte(`{"rooms": [` + room + `]}`)); err == nil || !strings.Contains(err.Error(), "cannot be walked to") {
		t.Errorf("a station cut off by the keeper's place parsed: %v", err)
	}
}

// The keeper walks out from behind the counter by squares nobody else may use,
// to the station where the fire is fed.
func TestKeeperPathToTheHearth(t *testing.T) {
	for _, id := range []string{tavern, inn} {
		l := For(id)
		hearth := l.StationFor(UseStoke)
		if hearth == nil {
			t.Fatalf("%s: no stoke station", l.Name)
		}
		path := l.KeeperPath(l.Keeper.Square, hearth.Square)
		if path == nil || path[0] != l.Keeper.Square || path[len(path)-1] != hearth.Square {
			t.Fatalf("%s: keeper to hearth = %v", l.Name, path)
		}
		back := l.KeeperPath(hearth.Square, l.Keeper.Square)
		if len(back) != len(path) {
			t.Errorf("%s: the way back %v is not the way there %v", l.Name, back, path)
		}
		usedKeeperWay := false
		for _, s := range path {
			if l.Grid[s.Y][s.X] == 'k' {
				usedKeeperWay = true
				if l.Walkable(s) || l.Path(l.Door.Square, s) != nil {
					t.Errorf("%s: a guest may walk on the keeper's way %v", l.Name, s)
				}
			}
		}
		if !usedKeeperWay {
			t.Errorf("%s: keeper to hearth %v did not come out by the keeper's way", l.Name, path)
		}
	}
	if For(tavern).StationFor("nothing") != nil {
		t.Error("StationFor found a station for an unknown use")
	}
}

func TestParseRejectsAWorkStationTheKeeperCannotReach(t *testing.T) {
	room := `{"structure_id": "s", "grid": ["#.", ".."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 0], "at": [24, 14]}, "stations": [{"id": "x", "square": [1, 1], "at": [24, 30], "use": ["stoke"]}]}`
	if _, err := Parse([]byte(`{"rooms": [` + room + `]}`)); err != nil {
		t.Fatalf("a reachable work station was rejected: %v", err)
	}
	// The keeper boxed into a corner: the work station is walkable from the door
	// but not from the keeper's place.
	boxed := `{"structure_id": "s", "grid": [".#.", "#.."], "door": {"square": [1, 1], "at": [24, 30]}, "keeper": {"square": [0, 0], "at": [8, 14]}, "stations": [{"id": "x", "square": [2, 0], "at": [40, 14], "use": ["stoke"]}]}`
	if _, err := Parse([]byte(`{"rooms": [` + boxed + `]}`)); err == nil || !strings.Contains(err.Error(), "keeper's place") {
		t.Errorf("a work station the keeper cannot reach parsed: %v", err)
	}
}

// The keeper leaves and returns to their place only by the keeper's way when
// the room has one — at the Inn, never from Hannah's place [2,4] straight down
// through the desk to [2,5].
func TestKeeperLeavesOnlyByTheKeepersWay(t *testing.T) {
	l := For(inn)
	chair := Square{X: 0, Y: 6}
	for _, path := range [][]Square{l.KeeperPath(l.Keeper.Square, chair), l.KeeperPath(chair, l.Keeper.Square)} {
		if path == nil {
			t.Fatal("no keeper path between the keeper's place and the rocking chair")
		}
		for i := 1; i < len(path); i++ {
			a, b := path[i-1], path[i]
			if a == l.Keeper.Square || b == l.Keeper.Square {
				other := a
				if a == l.Keeper.Square {
					other = b
				}
				if l.Grid[other.Y][other.X] != 'k' {
					t.Errorf("keeper path %v steps %v -> %v, not by the keeper's way", path, a, b)
				}
			}
		}
	}
	// A room with no keeper's way: the keeper steps off their place freely.
	room := `{"structure_id": "s", "grid": ["..", ".."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 0], "at": [24, 14]}, "stations": []}`
	plans, err := Parse([]byte(`{"rooms": [` + room + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := plans["s"].KeeperPath(Square{X: 1, Y: 0}, Square{X: 1, Y: 1}); len(p) != 2 {
		t.Errorf("no keeper's way: path %v, want one step", p)
	}
}
