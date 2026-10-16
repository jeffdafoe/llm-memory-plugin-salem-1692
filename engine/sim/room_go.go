package sim

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_go.go — LLM-801. A player's character goes from one room of a building
// to another through a door or up the stairs: from the Inn's entry hall up to
// the hall, from the hall into their own room and back. The plan of the room
// they are in says where its doors and stairs lead (roomlayout.Station.Leads);
// canEnterRoom says whether they may go in. Upstairs is out of the building's
// conversation: speech in the hall or a bedroom stays there (RoomKindScoped),
// and they leave whatever huddle they were in downstairs.

// HallRoomName is the name of an inn's upstairs hall room (structure_room).
const HallRoomName = "hall"

// RoomRefusedError is a door or stair that will not let the player through,
// with the line the player is told.
type RoomRefusedError struct {
	Line string
}

func (e *RoomRefusedError) Error() string { return e.Line }

// The lines a refused door or stair says.
const (
	roomRefusedHall     = "The rooms upstairs are for guests."
	roomRefusedLet      = "That room is let to another guest."
	roomRefusedNotYours = "That room is not yours. Ask the keeper for a room."
	roomRefusedKeeper   = "That is the keeper's own room."
)

// RoomGoResult answers MovePCToRoom: the room the player is in now ("" for the
// common room) and their square there (nil when it is full).
type RoomGoResult struct {
	Room   string
	Square *roomlayout.Square
}

// MovePCToRoom takes a player's character through the door or stairs of the
// room they are in that leads to `to` ("common", "hall", "bedroom_2"): awake,
// inside, not on their way out, the way there in their room's plan, the room
// one they may enter and one with a plan to stand in. They arrive at the way
// back to the room they left — the hall's stairhead, a bedroom's door — or,
// when someone stands there, wherever the room puts a player.
func MovePCToRoom(actorID ActorID, to string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a := w.Actors[actorID]
			if a == nil || a.Kind != KindPC {
				return nil, fmt.Errorf("MovePCToRoom: %q is not a player's character", actorID)
			}
			if a.InsideStructureID == "" || a.State == StateSleeping || a.MoveIntent != nil {
				return nil, ErrRoomSquareRefused
			}
			from := actorPlanRoom(w, a)
			here := roomPlanIn(w, a.InsideStructureID, from)
			if here == nil || here.Way(to) == nil {
				return nil, ErrRoomSquareRefused
			}
			target := RoomID(0)
			name := ""
			if to != roomlayout.CommonRoom {
				rm := roomByName(w, a.InsideStructureID, to)
				if rm == nil || rm.Kind == RoomKindCommon {
					return nil, ErrRoomSquareRefused
				}
				if !canEnterRoom(w, a, rm.ID, now) {
					return nil, &RoomRefusedError{Line: roomRefusedLine(w, rm, now)}
				}
				target, name = rm.ID, rm.Name
			}
			there := roomPlanIn(w, a.InsideStructureID, name)
			if there == nil {
				return nil, ErrRoomSquareRefused
			}
			// Out of the conversation downstairs (or upstairs, coming down): the
			// structure's huddle is the common room's, and nobody upstairs is in
			// one (EnsureColocatedHuddle).
			if a.CurrentHuddleID != "" {
				leaveCurrentHuddle(w, a, now)
			}
			a.InsideRoomID = target
			back := from
			if back == "" {
				back = roomlayout.CommonRoom
			}
			taken := takenRoomSquares(w, a.InsideStructureID, name, a.ID)
			var sq *roomlayout.Square
			if way := there.Way(back); way != nil && !taken[way.Square] {
				s := way.Square
				sq = &s
			} else if s, ok := there.Place(roomlayout.RolePC, string(a.ID), taken); ok {
				sq = &s
			}
			a.RoomDriftAt = time.Time{}
			setRoomSquare(w, a, sq)
			return RoomGoResult{Room: name, Square: copyRoomSquare(sq)}, nil
		},
	}
}

// roomRefusedLine is what a door or stair the player may not use says.
func roomRefusedLine(w *World, rm *Room, now time.Time) string {
	switch rm.Kind {
	case RoomKindHall:
		return roomRefusedHall
	case RoomKindStaff:
		return roomRefusedKeeper
	case RoomKindPrivate:
		if roomHeld(w, rm.ID, now) {
			return roomRefusedLet
		}
		return roomRefusedNotYours
	}
	return ErrRoomSquareRefused.Error()
}

// roomHeld reports whether anyone holds a live paid grant on the room.
func roomHeld(w *World, roomID RoomID, now time.Time) bool {
	for _, a := range w.Actors {
		if ra := a.RoomAccess[RoomAccessKey{RoomID: roomID, Source: AccessSourceLedger}]; IsActiveLedgerGrant(ra, now) {
			return true
		}
	}
	return false
}

// VacantRooms lists, by name, the bedrooms of the structure nobody holds a
// live paid grant on — the doors that stand open in its upstairs hall
// (LLM-801). Read over a published snapshot.
func VacantRooms(snap *Snapshot, structureID StructureID) []string {
	st := snap.Structures[structureID]
	if st == nil {
		return nil
	}
	held := map[RoomID]bool{}
	for _, a := range snap.Actors {
		for key, ra := range a.RoomAccess {
			if key.Source == AccessSourceLedger && IsActiveLedgerGrant(ra, snap.PublishedAt) {
				held[key.RoomID] = true
			}
		}
	}
	var out []string
	for _, rm := range st.Rooms {
		if rm != nil && rm.Kind == RoomKindPrivate && !held[rm.ID] {
			out = append(out, rm.Name)
		}
	}
	sort.Strings(out)
	return out
}

// IsRoomRefused reports whether err is a refused door or stair, and its line.
func IsRoomRefused(err error) (string, bool) {
	var r *RoomRefusedError
	if errors.As(err, &r) {
		return r.Line, true
	}
	return "", false
}
