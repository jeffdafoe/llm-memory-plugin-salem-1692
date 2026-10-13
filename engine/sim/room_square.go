package sim

import (
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_square.go — LLM-768. Where a person stands inside a room that has a
// floor plan (roomlayout): Actor.RoomSquare, held in memory only. It is set
// when they enter (setActorInsideStructure), given up when they go to sleep
// (sleepers are upstairs, never drawn, and must not hold a seat), taken again
// when they wake, and dealt out to everyone already inside at boot. A restart
// loses nothing that matters: everyone is placed afresh.
//
// The client draws each person at their square; the engine owns it so every
// viewer sees the same room, and so later slices can move people square to
// square (a player's tap, a villager walking to the hearth to stoke it).

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

// pickRoomSquare: the square for an awake actor inside structureID, or nil when
// the structure has no plan or every square is taken.
func pickRoomSquare(w *World, a *Actor, structureID StructureID) *roomlayout.Square {
	layout := roomlayout.For(string(structureID))
	if layout == nil || structureID == "" || a.State == StateSleeping {
		return nil
	}
	sq, ok := layout.Place(roomRole(a, structureID), string(a.ID), takenRoomSquares(w, structureID, a.ID))
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

// releaseRoomSquare: going to sleep, the actor gives up their square.
func releaseRoomSquare(w *World, a *Actor) {
	setRoomSquare(w, a, nil)
}

// reclaimRoomSquare: waking inside, the actor takes a square again.
func reclaimRoomSquare(w *World, a *Actor) {
	if a.RoomSquare == nil {
		setRoomSquare(w, a, pickRoomSquare(w, a, a.InsideStructureID))
	}
}

// placeRoomSquaresOnLoad deals a square to everyone awake inside a room at
// boot: keepers first so they get their place, then players, then the rest,
// each group by id so a boot is repeatable. No frames — the clients' first
// snapshot carries the squares.
func (w *World) placeRoomSquaresOnLoad() {
	ids := make([]ActorID, 0)
	for id, a := range w.Actors {
		a.RoomSquare = nil
		if a.InsideStructureID != "" && roomlayout.For(string(a.InsideStructureID)) != nil {
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
