package sim

import (
	"errors"
	"fmt"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_square.go — LLM-768. Where a person stands inside a room that has a
// floor plan (roomlayout): Actor.RoomSquare, held in memory only. It is set
// when they enter (setActorInsideStructure), changed for a bed when they go
// to sleep in a room that has beds (LLM-782) — a sleeper upstairs or in the
// loft holds none, never a seat — taken again when they wake, and dealt out to everyone already inside at boot. A restart
// loses nothing that matters: everyone is placed afresh.
//
// The client draws each person at their square; the engine owns it so every
// viewer sees the same room, and so later slices can move people square to
// square (a player's tap, a villager walking to the hearth to stoke it).

// roomPlan is the floor plan of the structure's room: its own (the Tavern, the
// Inn), else its house type's — every home placed as one asset shares one plan
// (LLM-782). Nil when it has neither.
func roomPlan(w *World, structureID StructureID) *roomlayout.Layout {
	return resolveRoomPlan(w.VillageObjects, w.Assets, structureID)
}

func resolveRoomPlan(objects map[VillageObjectID]*VillageObject, assets map[AssetID]*Asset, structureID StructureID) *roomlayout.Layout {
	if structureID == "" {
		return nil
	}
	name := ""
	if vobj := objects[VillageObjectID(structureID)]; vobj != nil {
		if asset := assets[vobj.AssetID]; asset != nil {
			name = asset.Name
		}
	}
	return roomlayout.Resolve(string(structureID), name)
}

// planRoomOf names the room roomID is for the plans (LLM-801): "" for none or
// the structure's common room (its own plan), else the room's name.
// A room is looked up among the structure's own (a stale id from another
// building is no room of this one, as audienceRoomScope). A room with no name
// is still not the common room: it gets a name no plan has.
func planRoomOf(w *World, structureID StructureID, roomID RoomID) string {
	if roomID == 0 || w.Structures[structureID] == nil {
		return ""
	}
	for _, rm := range w.Structures[structureID].Rooms {
		if rm == nil || rm.ID != roomID {
			continue
		}
		switch {
		case rm.Kind == RoomKindCommon:
			return ""
		case rm.Name == "":
			return fmt.Sprintf("room-%d", rm.ID)
		}
		return rm.Name
	}
	return ""
}

// actorPlanRoom: the room the actor is in, as planRoomOf names it.
func actorPlanRoom(w *World, a *Actor) string {
	return planRoomOf(w, a.InsideStructureID, a.InsideRoomID)
}

// roomPlanIn is the plan of a room of the structure: the structure's own (or
// its house type's) for the common room "", else that room's — the Inn's hall
// and guest rooms (LLM-801). Nil when it has none (a bedroom upstairs at the
// Tavern, the keeper's quarters): whoever is there is not drawn.
func roomPlanIn(w *World, structureID StructureID, room string) *roomlayout.Layout {
	if room == "" {
		return roomPlan(w, structureID)
	}
	return roomlayout.ForRoom(string(structureID), room)
}

// actorRoomPlan is the plan of the room the actor is in.
func actorRoomPlan(w *World, a *Actor) *roomlayout.Layout {
	return roomPlanIn(w, a.InsideStructureID, actorPlanRoom(w, a))
}

// RoomPlans lists the plan of every structure with a room, each carrying the
// structure's id (a home's carries its house type's plan, named for the home),
// by structure id — what the clients load to draw rooms. After a structure's
// own plan come its rooms' plans, each copy naming its room (LLM-801), by room
// name.
func RoomPlans(snap *Snapshot) []*roomlayout.Layout {
	ids := make([]StructureID, 0, len(snap.Structures))
	for id := range snap.Structures {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*roomlayout.Layout, 0)
	for _, id := range ids {
		st := snap.Structures[id]
		if plan := resolveRoomPlan(snap.VillageObjects, snap.Assets, id); plan != nil {
			out = append(out, plan.ForStructure(string(id), st.DisplayName))
		}
		names := make([]string, 0, len(st.Rooms))
		for _, rm := range st.Rooms {
			if rm != nil && rm.Kind != RoomKindCommon {
				names = append(names, rm.Name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if plan := roomlayout.ForRoom(string(id), name); plan != nil {
				out = append(out, plan.InRoom(name))
			}
		}
	}
	return out
}

// roomRole: the structure's own keeper goes to the keeper's place, a player's
// character to the door, everyone else to a free seat or standing place.
func roomRole(a *Actor, structureID StructureID) roomlayout.Role {
	switch {
	case a.Kind == KindPC:
		return roomlayout.RolePC
	case a.WorkStructureID == structureID:
		return roomlayout.RoleKeeper
	}
	return roomlayout.RoleGuest
}

// takenRoomSquares: the squares held by the others in the same room of the
// structure (LLM-801: the hall's squares are not the common room's).
func takenRoomSquares(w *World, structureID StructureID, room string, except ActorID) map[roomlayout.Square]bool {
	taken := map[roomlayout.Square]bool{}
	for id := range w.actorsByStructure[structureID] {
		if id == except {
			continue
		}
		if other := w.Actors[id]; other != nil && other.RoomSquare != nil && other.RoomSquareRoom == room {
			taken[*other.RoomSquare] = true
		}
	}
	return taken
}

// pickRoomSquare: the square for an actor in the room they are in, or nil when
// the room has no plan or every square is taken. Someone asleep takes a free
// bed (LLM-782) — none when the room has no plan (asleep upstairs at the
// Tavern, a keeper in their quarters) or every bed is taken (the loft); either
// way they are not drawn. Rising — getting up out of a bed — they stand at
// the room's rise place when it is free (LLM-801: beside the guest-room bed,
// not at its door).
func pickRoomSquare(w *World, a *Actor, rising bool) *roomlayout.Square {
	layout := actorRoomPlan(w, a)
	if layout == nil {
		return nil
	}
	taken := takenRoomSquares(w, a.InsideStructureID, actorPlanRoom(w, a), a.ID)
	var sq roomlayout.Square
	var ok bool
	switch {
	case a.State == StateSleeping:
		sq, ok = layout.Bed(string(a.ID), taken)
	case rising && layout.StationFor(roomlayout.UseRise) != nil && !taken[layout.StationFor(roomlayout.UseRise).Square]:
		sq, ok = layout.StationFor(roomlayout.UseRise).Square, true
	default:
		sq, ok = layout.Place(roomRole(a, a.InsideStructureID), string(a.ID), taken)
	}
	if !ok {
		return nil
	}
	return &sq
}

// setRoomSquare changes the actor's square — a square of the room they are in
// now — and tells the clients. A no-op when neither the square nor the room
// changed.
//
// A change of room with no square either side is told only in a structure
// with a room window (a plan for its common room): nobody draws the rest.
func setRoomSquare(w *World, a *Actor, sq *roomlayout.Square) {
	room := actorPlanRoom(w, a)
	if sameRoomSquare(a.RoomSquare, sq) && a.RoomSquareRoom == room {
		return
	}
	squareChanged := !sameRoomSquare(a.RoomSquare, sq)
	a.RoomSquare = sq
	a.RoomSquareRoom = room
	if squareChanged || roomPlan(w, a.InsideStructureID) != nil {
		w.emit(&ActorRoomSquareChanged{ActorID: a.ID, StructureID: a.InsideStructureID, Room: room, Square: copyRoomSquare(sq)})
	}
}

// setActorRoom moves the actor into another room of the structure they are in
// (roomID 0 or the common room's id for the common room) and gives them a
// square there when the room changed (LLM-801). Every room change of an awake
// actor goes through here, so their square never belongs to the room they
// left.
func setActorRoom(w *World, a *Actor, roomID RoomID) {
	a.InsideRoomID = roomID
	if a.RoomSquareRoom != actorPlanRoom(w, a) {
		setRoomSquare(w, a, pickRoomSquare(w, a, false))
	}
}

// bedRoomSquare: going to sleep, the actor gives up their square for a bed in
// the room — or for none (a room with no plan, the loft). Called with State
// already set to sleeping and InsideRoomID set to the room they sleep in.
func bedRoomSquare(w *World, a *Actor) {
	setRoomSquare(w, a, pickRoomSquare(w, a, false))
}

// reclaimRoomSquare: waking, the actor gets up — out of their bed, or into
// the room they now are in when they slept in another — and takes a square
// again. Called with InsideRoomID already set to the room they wake in.
func reclaimRoomSquare(w *World, a *Actor) {
	inBed := false
	if a.RoomSquare != nil && a.RoomSquareRoom == actorPlanRoom(w, a) {
		layout := actorRoomPlan(w, a)
		if layout != nil && !layout.IsBed(*a.RoomSquare) {
			return
		}
		inBed = layout != nil
	}
	setRoomSquare(w, a, pickRoomSquare(w, a, inBed))
}

// placeRoomSquaresOnLoad deals a square to everyone awake inside a room at
// boot: keepers first so they get their place, then players, then the rest,
// each group by id so a boot is repeatable. No frames — the clients' first
// snapshot carries the squares.
func (w *World) placeRoomSquaresOnLoad() {
	ids := make([]ActorID, 0)
	for id, a := range w.Actors {
		a.RoomSquare = nil
		a.RoomSquareRoom = actorPlanRoom(w, a)
		if a.InsideStructureID != "" && actorRoomPlan(w, a) != nil {
			ids = append(ids, id)
		}
	}
	rank := func(a *Actor) roomlayout.Role {
		switch roomRole(a, a.InsideStructureID) {
		case roomlayout.RoleKeeper:
			return 0
		case roomlayout.RolePC:
			return 1
		}
		return 2
	}
	sort.Slice(ids, func(i, j int) bool {
		ri, rj := rank(w.Actors[ids[i]]), rank(w.Actors[ids[j]])
		if ri != rj {
			return ri < rj
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		a := w.Actors[id]
		a.RoomSquare = pickRoomSquare(w, a, false)
	}
}

func sameRoomSquare(a, b *roomlayout.Square) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func copyRoomSquare(s *roomlayout.Square) *roomlayout.Square {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

// ErrRoomSquareRefused: a player's step inside a room cannot happen — they are
// not awake in a room with a floor plan, or the square is not floor they may
// stand on, is held by someone else, or cannot be reached.
var ErrRoomSquareRefused = errors.New("you cannot go there")

// RoomMoveResult answers MovePCInRoom: the square the player now holds and the
// way they walked to it, both ends included.
type RoomMoveResult struct {
	Square roomlayout.Square
	Path   []roomlayout.Square
}

// MovePCInRoom walks a player's character to another square of the room they
// are in — a tap on the floor or on a free seat. It is not village movement:
// they stay inside, keep their conversation and set no MoveIntent; only their
// square changes, and the frame carries the path so every viewer sees them
// walk it. Someone with no square yet (the room was full) sets off from the
// door.
func MovePCInRoom(actorID ActorID, to roomlayout.Square) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a := w.Actors[actorID]
			if a == nil || a.Kind != KindPC {
				return nil, fmt.Errorf("MovePCInRoom: %q is not a player's character", actorID)
			}
			layout := actorRoomPlan(w, a)
			if layout == nil || a.InsideStructureID == "" || a.State == StateSleeping || a.MoveIntent != nil {
				return nil, ErrRoomSquareRefused
			}
			path := walkInRoom(w, a, to)
			if path == nil {
				return nil, ErrRoomSquareRefused
			}
			return RoomMoveResult{Square: to, Path: path}, nil
		},
	}
}
