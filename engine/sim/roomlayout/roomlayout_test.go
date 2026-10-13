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

func TestParseRejectsBadPlans(t *testing.T) {
	good := `{"structure_id": "s", "grid": ["..", ".."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 0], "at": [24, 14]}, "stations": [%s]}`
	cases := map[string]string{
		"ragged grid":       `{"structure_id": "s", "grid": ["..", "."], "door": {"square": [0, 1], "at": [8, 30]}, "keeper": {"square": [1, 0], "at": [24, 14]}, "stations": []}`,
		"station on a wall": strings.Replace(good, `[%s]`, `[{"id": "x", "square": [0, 0], "at": [8, 14]}]`, 1),
		"shared square":     strings.Replace(good, `[%s]`, `[{"id": "x", "square": [0, 1], "at": [8, 14]}]`, 1),
		"drawn outside":     strings.Replace(good, `[%s]`, `[{"id": "x", "square": [1, 1], "at": [40, 14]}]`, 1),
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
