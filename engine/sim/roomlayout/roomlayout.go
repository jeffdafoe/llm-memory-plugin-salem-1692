// Package roomlayout holds the floor plans of the rooms people are drawn in
// (LLM-768): for each structure with a room window — or, for homes, each house
// type (LLM-782) — a grid of squares — one
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
// sit, Serve (a seat at a table, a place at the bar) where an order set before
// them lands. Use names the work done there ("stoke": the hearth), which sends
// a worker to it, or "drink": a place at the bar.
//
// Leads names the room a step through the station takes you to (LLM-801): a
// door or a staircase — "common", "hall", "bedroom_2". Tap is the art rect
// [x, y, w, h] a tap on means the station, for a door or stairs drawn far
// bigger than a person standing at them.
type Station struct {
	ID     string   `json:"id,omitempty"`
	Kind   string   `json:"kind,omitempty"`
	Square Square   `json:"square"`
	At     Point    `json:"at"`
	Facing string   `json:"facing,omitempty"`
	Pose   string   `json:"pose,omitempty"`
	Serve  *Point   `json:"serve,omitempty"`
	Use    []string `json:"use,omitempty"`
	Leads  string   `json:"leads,omitempty"`
	Tap    *[4]int  `json:"tap,omitempty"`
}

// CommonRoom is what Leads calls a structure's common room — the room a plan
// keyed by structure alone draws.
const CommonRoom = "common"

// UseStoke: the station where the room's fire is fed.
const UseStoke = "stoke"

// UseDrink: a place to stand at the bar (LLM-781). It carries a Serve point
// on the bar top, so an order is set down in front of the guest there.
const UseDrink = "drink"

// UseSleep: a bed (LLM-782). It stands on a bed square ('b' on the grid),
// which nobody walks on; only someone asleep in the room is put there.
const UseSleep = "sleep"

// PoseSleep: how a bed's sleeper is drawn — the client's mark of a bed.
const PoseSleep = "sleep"

// UseRise: where someone getting out of the room's bed stands (LLM-801) — by
// the bed, not at the door they would otherwise be put at.
const UseRise = "rise"

// has reports whether the station's Use names use.
func (st *Station) has(use string) bool {
	for _, u := range st.Use {
		if u == use {
			return true
		}
	}
	return false
}

// Layout is one room's plan. Grid rows are top to bottom, '.' a square that
// can be stood on, 'k' the keeper's way out from behind their counter (theirs
// alone), 'b' a bed (a sleeper's alone), anything else blocked (walls,
// furniture). KeeperDoor, when set, is where the keeper comes and goes (the
// Inn's stairs).
//
// A plan is for one structure (StructureID) or for every structure placed as
// one of Assets — a house type, so the homes of one size share a plan
// (LLM-782). Art names the client's picture of the room when it is not keyed
// by the structure.
//
// A plan with Rooms draws those rooms of its structure (LLM-801) — the Inn's
// upstairs hall, or its four guest rooms on one plan — instead of the
// structure's common room. Room is set only on the wire: which one of them a
// copy is for (ForRoom). Such a plan may have no Keeper.
type Layout struct {
	StructureID string    `json:"structure_id"`
	Rooms       []string  `json:"rooms,omitempty"`
	Room        string    `json:"room,omitempty"`
	Assets      []string  `json:"assets,omitempty"`
	Art         string    `json:"art,omitempty"`
	Name        string    `json:"name"`
	Grid        []string  `json:"grid"`
	Door        Station   `json:"door"`
	Keeper      *Station  `json:"keeper,omitempty"`
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
	out := map[Square]bool{l.Door.Square: true}
	if l.Keeper != nil {
		out[l.Keeper.Square] = true
	}
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
	if !l.Open(s) || l.keeperPlace(s) {
		return false
	}
	return l.KeeperDoor == nil || s != l.KeeperDoor.Square
}

// keeperPlace reports whether s is the keeper's place; a room with no keeper
// has none.
func (l *Layout) keeperPlace(s Square) bool {
	return l.Keeper != nil && s == l.Keeper.Square
}

// Path is the shortest way from one square to another, stepping up, down,
// left or right over walkable squares, both ends included; nil when `to` is not
// walkable or cannot be reached. `from` may be any square (where the walker
// stands now). People are walked past, so who holds a square is the caller's
// concern for `to` only.
func (l *Layout) Path(from, to Square) []Square {
	return l.path(from, to, l.Walkable, nil)
}

func (l *Layout) keeperWay(s Square) bool {
	return s.Y >= 0 && s.Y < len(l.Grid) && s.X >= 0 && s.X < len(l.Grid[s.Y]) && l.Grid[s.Y][s.X] == 'k'
}

// keeperWalkable: what the room's keeper may walk on — any walkable square,
// their own place, and the way out from behind their counter ('k').
func (l *Layout) keeperWalkable(s Square) bool {
	return l.Walkable(s) || l.keeperPlace(s) || l.keeperWay(s)
}

// keeperStep: in a room with a keeper's way, the keeper leaves and comes back
// to their place only by it — never straight through the counter onto the
// floor in front of it (the Inn's [2,5] below Hannah's place, behind the desk).
func (l *Layout) keeperStep(at, next Square) bool {
	if !l.keeperPlace(at) && !l.keeperPlace(next) {
		return true
	}
	other := next
	if next == l.Keeper.Square {
		other = at
	}
	if l.keeperWay(other) {
		return true
	}
	for _, row := range l.Grid {
		for _, c := range row {
			if c == 'k' {
				return false
			}
		}
	}
	return true
}

// KeeperPath is Path for the room's keeper, who may also use their own place
// and the way out from behind their counter, and leaves their place only by
// that way when the room has one.
func (l *Layout) KeeperPath(from, to Square) []Square {
	return l.path(from, to, l.keeperWalkable, l.keeperStep)
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

// Way returns the door or station that leads to the room named `to`, or nil —
// the stairs up from the Inn's entry hall, a bedroom's door off the hall.
func (l *Layout) Way(to string) *Station {
	if to == "" {
		return nil
	}
	if l.Door.Leads == to {
		return &l.Door
	}
	for i := range l.Stations {
		if l.Stations[i].Leads == to {
			return &l.Stations[i]
		}
	}
	return nil
}

// path is the BFS behind Path and KeeperPath; step, when set, may also refuse a
// single step between two walkable squares.
func (l *Layout) path(from, to Square, walkable func(Square) bool, step func(at, next Square) bool) []Square {
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
			if _, seen := prev[next]; seen || !walkable(next) || (step != nil && !step(at, next)) {
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
// not all pile onto the first, then the free plain square with the most room
// round it (Roomiest, LLM-788), so people do not stand on top of each other.
// A station where the keeper works (the hearth they stoke) is taken only when
// every other station is full, so guests sit or stand at the bar
// before they stand in the middle of the room (LLM-781). Guests are never put
// on the door, the keeper's place or the keeper's way in.
func (l *Layout) Place(role Role, id string, taken map[Square]bool) (Square, bool) {
	switch role {
	case RoleKeeper:
		if l.Keeper != nil && !taken[l.Keeper.Square] {
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
		for _, work := range []bool{false, true} {
			for i := 0; i < n; i++ {
				st := &l.Stations[(start+i)%n]
				if st.has(UseSleep) {
					continue
				}
				if st.has(UseStoke) == work && !taken[st.Square] {
					return st.Square, true
				}
			}
		}
	}
	// Plain floor: the free square farthest from everyone already in the room,
	// so people do not stand on top of each other (LLM-788); ties go to the
	// first in reading order.
	reserved := l.reserved()
	var free []Square
	for y := range l.Grid {
		for x := range l.Grid[y] {
			s := Square{x, y}
			if l.Open(s) && !reserved[s] && !taken[s] {
				free = append(free, s)
			}
		}
	}
	if len(free) == 0 {
		return Square{}, false
	}
	return Roomiest(free, taken), true
}

// Roomiest picks, of candidates, the square whose nearest taken square is
// farthest away (Chebyshev: a diagonal neighbour is as close as a side one) —
// the one with the most room round it. Ties go to the earliest candidate.
// With nothing taken, the first candidate.
func Roomiest(candidates []Square, taken map[Square]bool) Square {
	best := candidates[0]
	bestRoom := -1
	for _, s := range candidates {
		if room := Room(s, taken); room > bestRoom {
			best, bestRoom = s, room
		}
	}
	return best
}

// Room is how far s is from the nearest taken square (Chebyshev): 1 when
// someone stands right beside it, diagonals included. Large when nothing is
// taken.
func Room(s Square, taken map[Square]bool) int {
	room := 1 << 30
	for t, held := range taken {
		if held && t != s {
			if d := chebyshev(s, t); d < room {
				room = d
			}
		}
	}
	return room
}

func chebyshev(a, b Square) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

// Bed picks a free bed for someone going to sleep in the room, starting from
// the one their id hashes to, or false when the room has no free bed (they
// sleep in the loft, out of sight).
func (l *Layout) Bed(id string, taken map[Square]bool) (Square, bool) {
	var beds []Square
	for _, st := range l.Stations {
		if st.has(UseSleep) {
			beds = append(beds, st.Square)
		}
	}
	if len(beds) == 0 {
		return Square{}, false
	}
	h := fnv.New32a()
	h.Write([]byte(id))
	start := int(h.Sum32() % uint32(len(beds)))
	for i := range beds {
		if s := beds[(start+i)%len(beds)]; !taken[s] {
			return s, true
		}
	}
	return Square{}, false
}

// IsBed reports whether s is one of the room's beds.
func (l *Layout) IsBed(s Square) bool {
	for _, st := range l.Stations {
		if st.Square == s && st.has(UseSleep) {
			return true
		}
	}
	return false
}

func (l *Layout) bedSquare(s Square) bool {
	return s.Y >= 0 && s.Y < len(l.Grid) && s.X >= 0 && s.X < len(l.Grid[s.Y]) && l.Grid[s.Y][s.X] == 'b'
}

// label names the plan in errors: its structure (and rooms), or its house
// types.
func (l *Layout) label() string {
	if l.StructureID != "" && len(l.Rooms) > 0 {
		return fmt.Sprint(l.StructureID, l.Rooms)
	}
	if l.StructureID != "" {
		return l.StructureID
	}
	return fmt.Sprint(l.Assets)
}

func (l *Layout) validate() error {
	if len(l.Grid) == 0 || len(l.Grid[0]) == 0 {
		return fmt.Errorf("room %s: empty grid", l.label())
	}
	if (l.StructureID == "") == (len(l.Assets) == 0) {
		return fmt.Errorf("room %s: a plan is for one structure or for house types, not both or neither", l.label())
	}
	if len(l.Assets) > 0 && l.Art == "" {
		return fmt.Errorf("room %s: a house-type plan names its art", l.label())
	}
	if len(l.Rooms) > 0 && (len(l.Assets) > 0 || l.Art == "") {
		return fmt.Errorf("room %s: a plan for rooms is one structure's and names its art", l.label())
	}
	for _, r := range l.Rooms {
		if r == "" || r == CommonRoom {
			return fmt.Errorf("room %s: %q is not a room a plan can be for — the common room is the structure's own plan", l.label(), r)
		}
	}
	// A structure's common room and a home always have a keeper's place; an
	// upstairs room may not.
	if len(l.Rooms) == 0 && l.Keeper == nil {
		return fmt.Errorf("room %s: no keeper's place", l.label())
	}
	if l.Room != "" {
		return fmt.Errorf("room %s: room is set on the wire, not in the seed", l.label())
	}
	for i, row := range l.Grid {
		if len(row) != len(l.Grid[0]) {
			return fmt.Errorf("room %s: grid row %d is %d wide, row 0 is %d", l.label(), i, len(row), len(l.Grid[0]))
		}
	}
	all := []Station{l.Door}
	if l.Keeper != nil {
		all = append(all, *l.Keeper)
	}
	if l.KeeperDoor != nil {
		all = append(all, *l.KeeperDoor)
	}
	all = append(all, l.Stations...)
	seen := map[Square]string{}
	for _, st := range all {
		// A bed stands on a bed square; everything else on open floor. The
		// client knows a bed by its "sleep" pose and finds its cover by its id,
		// so a bed carries both and nothing else carries the pose.
		if st.has(UseSleep) {
			if !l.bedSquare(st.Square) {
				return fmt.Errorf("room %s: bed %q at %v is not a bed square", l.label(), st.ID, st.Square)
			}
			if st.ID == "" || st.Pose != PoseSleep {
				return fmt.Errorf("room %s: bed at %v needs an id and pose %q", l.label(), st.Square, PoseSleep)
			}
		} else if st.Pose == PoseSleep {
			return fmt.Errorf("room %s: station %q is posed %q but is not a bed", l.label(), st.ID, PoseSleep)
		} else if !l.Open(st.Square) {
			return fmt.Errorf("room %s: station %q at %v is not an open square", l.label(), st.ID, st.Square)
		}
		if other, dup := seen[st.Square]; dup {
			return fmt.Errorf("room %s: stations %q and %q share square %v", l.label(), other, st.ID, st.Square)
		}
		seen[st.Square] = st.ID
		if st.At.X < 0 || st.At.Y < 0 || st.At.X >= l.Width()*SquarePx || st.At.Y >= l.Height()*SquarePx {
			return fmt.Errorf("room %s: station %q is drawn at %v, outside the room", l.label(), st.ID, st.At)
		}
	}
	// A player walks from the door to any seat or standing place. Beds are
	// never walked to: a sleeper is put there, and gets up from there.
	for _, st := range l.Stations {
		if st.has(UseSleep) {
			continue
		}
		if st.has(UseDrink) && st.Serve == nil {
			return fmt.Errorf("room %s: bar place %q has no serve point on the bar", l.label(), st.ID)
		}
		if l.Path(l.Door.Square, st.Square) == nil {
			return fmt.Errorf("room %s: station %q at %v cannot be walked to from the door", l.label(), st.ID, st.Square)
		}
		// A station with work to do there is the keeper's too: they walk to it
		// from behind their counter.
		if len(st.Use) > 0 && l.Keeper != nil && l.KeeperPath(l.Keeper.Square, st.Square) == nil {
			return fmt.Errorf("room %s: station %q at %v cannot be walked to from the keeper's place", l.label(), st.ID, st.Square)
		}
	}
	return nil
}

// assetKey is the key a house-type plan is kept under, beside the structure
// ids.
func assetKey(name string) string {
	return "asset:" + name
}

// roomKey is the key a plan for one of a structure's rooms is kept under.
func roomKey(structureID, room string) string {
	return "room:" + structureID + "/" + room
}

// Parse reads and checks a seed: every row as wide as the first, every
// station on its own open square (a bed on its bed square), drawn inside the
// room and reachable on foot from the door. Plans are keyed by structure id,
// house-type plans by assetKey of each house type.
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
		keys := []string{l.StructureID}
		switch {
		case len(l.Rooms) > 0:
			keys = keys[:0]
			for _, r := range l.Rooms {
				keys = append(keys, roomKey(l.StructureID, r))
			}
		case l.StructureID == "":
			keys = keys[:0]
			for _, a := range l.Assets {
				keys = append(keys, assetKey(a))
			}
		}
		for _, k := range keys {
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("roomlayout: room %s listed twice", k)
			}
			out[k] = l
		}
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

// For returns the plan of the structure's own room, or nil when it has none.
// A home's plan is its house type's: see Resolve.
func For(structureID string) *Layout {
	if l := layouts[structureID]; l != nil && l.StructureID == structureID && len(l.Rooms) == 0 {
		return l
	}
	return nil
}

// ForRoom returns the plan of one of a structure's rooms by its name — the
// Inn's "hall", "bedroom_2" — or nil when that room has none (LLM-801). The
// common room is the structure's own plan (For, Resolve), not this.
func ForRoom(structureID, room string) *Layout {
	if structureID == "" || room == "" {
		return nil
	}
	return layouts[roomKey(structureID, room)]
}

// InRoom is a room plan as one room's, for the wire: a copy naming the room.
func (l *Layout) InRoom(room string) *Layout {
	c := *l
	c.Room = room
	return &c
}

// Resolve returns the plan of a structure placed as house type `asset`: its
// own plan when it has one, else its house type's, else nil.
func Resolve(structureID, asset string) *Layout {
	if structureID == "" {
		return nil
	}
	if l := For(structureID); l != nil {
		return l
	}
	if asset == "" {
		return nil
	}
	return layouts[assetKey(asset)]
}

// ForStructure is a house-type plan as one structure's: a copy carrying the
// structure's id and name, for the wire. A structure's own plan comes back as
// it is.
func (l *Layout) ForStructure(structureID, name string) *Layout {
	if l.StructureID != "" {
		return l
	}
	c := *l
	c.StructureID = structureID
	c.Name = name
	return &c
}

// All returns every structure's own plan, by structure id order. House-type
// plans are not in it: each structure resolves its own (Resolve).
func All() []*Layout {
	out := make([]*Layout, 0, len(layouts))
	for k, l := range layouts {
		if k == l.StructureID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StructureID < out[j].StructureID })
	return out
}
