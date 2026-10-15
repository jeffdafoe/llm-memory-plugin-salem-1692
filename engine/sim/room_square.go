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

// RoomPlans lists the plan of every structure with a room, each carrying the
// structure's id (a home's carries its house type's plan, named for the home),
// by structure id — what the clients load to draw rooms.
func RoomPlans(snap *Snapshot) []*roomlayout.Layout {
	ids := make([]StructureID, 0, len(snap.Structures))
	for id := range snap.Structures {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*roomlayout.Layout, 0)
	for _, id := range ids {
		if plan := resolveRoomPlan(snap.VillageObjects, snap.Assets, id); plan != nil {
			out = append(out, plan.ForStructure(string(id), snap.Structures[id].DisplayName))
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

// takenRoomSquares: the squares held by the others inside the structure.
func takenRoomSquares(w *World, structureID StructureID, except ActorID) map[roomlayout.Square]bool {
	taken := map[roomlayout.Square]bool{}
	for id := range w.actorsByStructure[structureID] {
		if id == except {
			continue
		}
		if other := w.Actors[id]; other != nil && other.RoomSquare != nil {
			taken[*other.RoomSquare] = true
		}
	}
	return taken
}

// pickRoomSquare: the square for an actor inside structureID, or nil when the
// structure has no plan or every square is taken. Someone asleep there takes a
// free bed (LLM-782) — unless they sleep in a private room (a lodger, a
// keeper in their quarters: upstairs, not in this room — audienceRoomScope; a
// sleeper in the common room sleeps here) or every bed is taken (the loft);
// either way they are not drawn.
func pickRoomSquare(w *World, a *Actor, structureID StructureID) *roomlayout.Square {
	layout := roomPlan(w, structureID)
	if layout == nil {
		return nil
	}
	taken := takenRoomSquares(w, structureID, a.ID)
	var sq roomlayout.Square
	var ok bool
	if a.State == StateSleeping {
		if audienceRoomScope(w, a) != 0 {
			return nil
		}
		sq, ok = layout.Bed(string(a.ID), taken)
	} else {
		sq, ok = layout.Place(roomRole(a, structureID), string(a.ID), taken)
	}
	if !ok {
		return nil
	}
	return &sq
}

// setRoomSquare changes the actor's square and tells the clients. A no-op
// when it is unchanged.
func setRoomSquare(w *World, a *Actor, sq *roomlayout.Square) {
	if sameRoomSquare(a.RoomSquare, sq) {
		return
	}
	a.RoomSquare = sq
	w.emit(&ActorRoomSquareChanged{ActorID: a.ID, StructureID: a.InsideStructureID, Square: copyRoomSquare(sq)})
}

// bedRoomSquare: going to sleep, the actor gives up their square for a bed in
// the room — or for none (upstairs, the loft). Called with State already set to
// sleeping.
func bedRoomSquare(w *World, a *Actor) {
	setRoomSquare(w, a, pickRoomSquare(w, a, a.InsideStructureID))
}

// reclaimRoomSquare: waking inside, the actor gets up — out of their bed, or
// down from upstairs — and takes a square again.
func reclaimRoomSquare(w *World, a *Actor) {
	if a.RoomSquare != nil {
		layout := roomPlan(w, a.InsideStructureID)
		if layout == nil || !layout.IsBed(*a.RoomSquare) {
			return
		}
	}
	setRoomSquare(w, a, pickRoomSquare(w, a, a.InsideStructureID))
}

// placeRoomSquaresOnLoad deals a square to everyone awake inside a room at
// boot: keepers first so they get their place, then players, then the rest,
// each group by id so a boot is repeatable. No frames — the clients' first
// snapshot carries the squares.
func (w *World) placeRoomSquaresOnLoad() {
	ids := make([]ActorID, 0)
	for id, a := range w.Actors {
		a.RoomSquare = nil
		if a.InsideStructureID != "" && roomPlan(w, a.InsideStructureID) != nil {
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
		a.RoomSquare = pickRoomSquare(w, a, a.InsideStructureID)
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
			layout := roomPlan(w, a.InsideStructureID)
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
