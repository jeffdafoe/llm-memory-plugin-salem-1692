// Package roomlayout holds the floor plans of the rooms people are drawn in
// (LLM-768): for each structure with a room window, a grid of squares — one
// per tile of the building's footprint — saying which squares can be stood
// on, and its stations: the door, the keeper's place, and the seats and
// standing places a guest is put at. The plans are a seed file compiled into
// the engine (layouts.json); the client reads them from the engine and draws a
// person at the station's art point, or a plain square's centre.
//
// A leaf package: the sim imports it, never the other way round. Placement is
// pure — the caller says which squares are taken.
package roomlayout

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
)

//go:embed layouts.json
var seed []byte

// SquarePx is the art size of one square: the room art is drawn one 16 px
// tile per footprint tile.
const SquarePx = 16

// Square is a square of a room's grid: X across from the left, Y down from
// the top. Written as [x, y] in the seed and on the wire.
type Square struct {
	X int
	Y int
}

func (s Square) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]int{s.X, s.Y})
}

func (s *Square) UnmarshalJSON(b []byte) error {
	var xy []int
	if err := json.Unmarshal(b, &xy); err != nil {
		return err
	}
	if len(xy) != 2 {
		return fmt.Errorf("a square is [x, y], got %s", b)
	}
	s.X, s.Y = xy[0], xy[1]
	return nil
}

// Point is a room art pixel, written as [x, y].
type Point = Square

// Station is a square with a purpose and where a person there is drawn: At is
// where their feet go in room art pixels, Facing and Pose how they stand or
// sit, Serve (seats at a table) where a meal set before them lands. Use names
// the work done there ("stoke": the hearth), which sends a worker to it.
type Station struct {
	ID     string   `json:"id,omitempty"`
	Kind   string   `json:"kind,omitempty"`
	Square Square   `json:"square"`
	At     Point    `json:"at"`
	Facing string   `json:"facing,omitempty"`
	Pose   string   `json:"pose,omitempty"`
	Serve  *Point   `json:"serve,omitempty"`
	Use    []string `json:"use,omitempty"`
}

// UseStoke: the station where the room's fire is fed.
const UseStoke = "stoke"

// Layout is one room's plan. Grid rows are top to bottom, '.' a square that
// can be stood on, 'k' the keeper's way out from behind their counter (theirs
// alone), anything else blocked (walls, furniture). KeeperDoor, when set, is
// where the keeper comes and goes (the Inn's stairs).
type Layout struct {
	StructureID string    `json:"structure_id"`
	Name        string    `json:"name"`
	Grid        []string  `json:"grid"`
	Door        Station   `json:"door"`
	Keeper      Station   `json:"keeper"`
	KeeperDoor  *Station  `json:"keeper_door,omitempty"`
	Stations    []Station `json:"stations"`
}

// Width and Height of the grid in squares.
func (l *Layout) Width() int  { return len(l.Grid[0]) }
func (l *Layout) Height() int { return len(l.Grid) }

// Open reports whether s is on the grid and can be stood on.
func (l *Layout) Open(s Square) bool {
	return s.Y >= 0 && s.Y < len(l.Grid) && s.X >= 0 && s.X < len(l.Grid[s.Y]) && l.Grid[s.Y][s.X] == '.'
}

// reserved: squares a guest is never put on as plain floor — the door, the
// keeper's place, the keeper's way in, and every station.
func (l *Layout) reserved() map[Square]bool {
	out := map[Square]bool{l.Door.Square: true, l.Keeper.Square: true}
	if l.KeeperDoor != nil {
		out[l.KeeperDoor.Square] = true
	}
	for _, st := range l.Stations {
		out[st.Square] = true
	}
	return out
}

// Walkable reports whether a guest or a player can walk on or to s: an open
// square that is not the keeper's place or the keeper's way in.
func (l *Layout) Walkable(s Square) bool {
	if !l.Open(s) || s == l.Keeper.Square {
		return false
	}
	return l.KeeperDoor == nil || s != l.KeeperDoor.Square
}

// Path is the shortest way from one square to another, stepping up, down,
// left or right over walkable squares, both ends included; nil when `to` is not
// walkable or cannot be reached. `from` may be any square (where the walker
// stands now). People are walked past, so who holds a square is the caller's
// concern for `to` only.
func (l *Layout) Path(from, to Square) []Square {
	return l.path(from, to, l.Walkable)
}

// keeperWalkable: what the room's keeper may walk on — any walkable square,
// their own place, and the way out from behind their counter ('k').
func (l *Layout) keeperWalkable(s Square) bool {
	if l.Walkable(s) || s == l.Keeper.Square {
		return true
	}
	return s.Y >= 0 && s.Y < len(l.Grid) && s.X >= 0 && s.X < len(l.Grid[s.Y]) && l.Grid[s.Y][s.X] == 'k'
}

// KeeperPath is Path for the room's keeper, who may also use their own place
// and the way out from behind their counter.
func (l *Layout) KeeperPath(from, to Square) []Square {
	return l.path(from, to, l.keeperWalkable)
}

// StationFor returns the station whose Use names `use`, or nil.
func (l *Layout) StationFor(use string) *Station {
	for i := range l.Stations {
		for _, u := range l.Stations[i].Use {
			if u == use {
				return &l.Stations[i]
			}
		}
	}
	return nil
}

func (l *Layout) path(from, to Square, walkable func(Square) bool) []Square {
	if !walkable(to) {
		return nil
	}
	if from == to {
		return []Square{from}
	}
	prev := map[Square]Square{from: from}
	queue := []Square{from}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		for _, d := range [4]Square{{0, -1}, {1, 0}, {0, 1}, {-1, 0}} {
			next := Square{at.X + d.X, at.Y + d.Y}
			if _, seen := prev[next]; seen || !walkable(next) {
				continue
			}
			prev[next] = at
			if next == to {
				path := []Square{to}
				for s := at; s != from; s = prev[s] {
					path = append(path, s)
				}
				path = append(path, from)
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path
			}
			queue = append(queue, next)
		}
	}
	return nil
}

// Role says which place a person is put at first.
type Role int

const (
	RoleGuest  Role = iota
	RoleKeeper      // the structure's own keeper: their place behind the bar or desk
	RolePC          // a player's character: the door
)

// Place picks the square for a person arriving (or waking) in the room, or
// false when every square is taken. The keeper takes the keeper's place and
// a player's character the door, when free; otherwise — and for everyone
// else — a free station, starting from the one the id hashes to so people do
// not all pile onto the first, then the first free plain square in reading
// order. Guests are never put on the door, the keeper's place or the keeper's
// way in.
func (l *Layout) Place(role Role, id string, taken map[Square]bool) (Square, bool) {
	switch role {
	case RoleKeeper:
		if !taken[l.Keeper.Square] {
			return l.Keeper.Square, true
		}
	case RolePC:
		if !taken[l.Door.Square] {
			return l.Door.Square, true
		}
	}
	if n := len(l.Stations); n > 0 {
		h := fnv.New32a()
		h.Write([]byte(id))
		start := int(h.Sum32() % uint32(n))
		for i := 0; i < n; i++ {
			st := l.Stations[(start+i)%n]
			if !taken[st.Square] {
				return st.Square, true
			}
		}
	}
	reserved := l.reserved()
	for y := range l.Grid {
		for x := range l.Grid[y] {
			s := Square{x, y}
			if l.Open(s) && !reserved[s] && !taken[s] {
				return s, true
			}
		}
	}
	return Square{}, false
}

func (l *Layout) validate() error {
	if len(l.Grid) == 0 || len(l.Grid[0]) == 0 {
		return fmt.Errorf("room %s: empty grid", l.StructureID)
	}
	for i, row := range l.Grid {
		if len(row) != len(l.Grid[0]) {
			return fmt.Errorf("room %s: grid row %d is %d wide, row 0 is %d", l.StructureID, i, len(row), len(l.Grid[0]))
		}
	}
	all := []Station{l.Door, l.Keeper}
	if l.KeeperDoor != nil {
		all = append(all, *l.KeeperDoor)
	}
	all = append(all, l.Stations...)
	seen := map[Square]string{}
	for _, st := range all {
		if !l.Open(st.Square) {
			return fmt.Errorf("room %s: station %q at %v is not an open square", l.StructureID, st.ID, st.Square)
		}
		if other, dup := seen[st.Square]; dup {
			return fmt.Errorf("room %s: stations %q and %q share square %v", l.StructureID, other, st.ID, st.Square)
		}
		seen[st.Square] = st.ID
		if st.At.X < 0 || st.At.Y < 0 || st.At.X >= l.Width()*SquarePx || st.At.Y >= l.Height()*SquarePx {
			return fmt.Errorf("room %s: station %q is drawn at %v, outside the room", l.StructureID, st.ID, st.At)
		}
	}
	// A player walks from the door to any seat or standing place.
	for _, st := range l.Stations {
		if l.Path(l.Door.Square, st.Square) == nil {
			return fmt.Errorf("room %s: station %q at %v cannot be walked to from the door", l.StructureID, st.ID, st.Square)
		}
		// A station with work to do there is the keeper's too: they walk to it
		// from behind their counter.
		if len(st.Use) > 0 && l.KeeperPath(l.Keeper.Square, st.Square) == nil {
			return fmt.Errorf("room %s: station %q at %v cannot be walked to from the keeper's place", l.StructureID, st.ID, st.Square)
		}
	}
	return nil
}

// Parse reads and checks a seed: every row as wide as the first, every
// station on its own open square, drawn inside the room and reachable on foot
// from the door.
func Parse(b []byte) (map[string]*Layout, error) {
	var doc struct {
		Rooms []*Layout `json:"rooms"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("roomlayout: %w", err)
	}
	out := make(map[string]*Layout, len(doc.Rooms))
	for _, l := range doc.Rooms {
		if err := l.validate(); err != nil {
			return nil, fmt.Errorf("roomlayout: %w", err)
		}
		if _, dup := out[l.StructureID]; dup {
			return nil, fmt.Errorf("roomlayout: room %s listed twice", l.StructureID)
		}
		out[l.StructureID] = l
	}
	return out, nil
}

// The compiled-in plans. A bad seed fails the build's tests and the engine's
// start, never a live placement.
var layouts = func() map[string]*Layout {
	l, err := Parse(seed)
	if err != nil {
		panic(err)
	}
	return l
}()

// For returns the plan of the structure's room, or nil when it has none.
func For(structureID string) *Layout {
	return layouts[structureID]
}

// All returns every plan, by structure id order.
func All() []*Layout {
	out := make([]*Layout, 0, len(layouts))
	for _, l := range layouts {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StructureID < out[j].StructureID })
	return out
}
