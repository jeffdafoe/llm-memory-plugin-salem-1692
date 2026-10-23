package sim

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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
	// For a guest who holds another room here (LLM-847, roomRefusedLineFor).
	roomRefusedNotYoursHeld = "That room is not yours."
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
					return nil, &RoomRefusedError{Line: roomRefusedLineFor(w, a, rm, now)}
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
			fromRoom := a.InsideRoomID
			a.InsideRoomID = target
			tracePCRoomChange(w, a, fromRoom, pcRoomGoRoom, now)
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

// roomRefusedLineFor is roomRefusedLine for a player who may hold a room here
// themselves (LLM-847): a guest tapping a bedroom door that is not theirs keeps
// the reason and is told which one is — "That room is let to another guest.
// Yours is number 1." — by the number plate the halls carry over each guest
// door, the N of "bedroom_N". A vacant room's "Ask the keeper for a room" is
// dropped for them: they have one. The keeper's door and the stairs keep their
// own lines.
func roomRefusedLineFor(w *World, a *Actor, rm *Room, now time.Time) string {
	line := roomRefusedLine(w, rm, now)
	if rm.Kind != RoomKindPrivate {
		return line
	}
	own, ok := lodgerRoomAt(w, a, rm.StructureID, now)
	if !ok || own == rm.ID {
		return line
	}
	reason := roomRefusedLet
	if line != roomRefusedLet {
		reason = roomRefusedNotYoursHeld
	}
	ownRoom := findRoom(w, own)
	if ownRoom == nil {
		return reason
	}
	if n, found := strings.CutPrefix(ownRoom.Name, "bedroom_"); found && n != "" {
		return reason + " Yours is number " + n + "."
	}
	return reason
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

// HeldRoomName names the bedroom of the structure the actor holds a live paid
// grant on, "" when none — the door the client points out upstairs (LLM-847).
// The lowest room id wins on the can't-normally-happen second grant, as
// lodgerRoomAt does. Read over a published snapshot.
func HeldRoomName(snap *Snapshot, a *ActorSnapshot, structureID StructureID) string {
	st := snap.Structures[structureID]
	if st == nil || a == nil {
		return ""
	}
	name := ""
	best := RoomID(0)
	for _, rm := range st.Rooms {
		if rm == nil || rm.Kind != RoomKindPrivate {
			continue
		}
		ra := a.RoomAccess[RoomAccessKey{RoomID: rm.ID, Source: AccessSourceLedger}]
		if !IsActiveLedgerGrant(ra, snap.PublishedAt) {
			continue
		}
		if best == 0 || rm.ID < best {
			best, name = rm.ID, rm.Name
		}
	}
	return name
}

// IsRoomRefused reports whether err is a refused door or stair, and its line.
func IsRoomRefused(err error) (string, bool) {
	var r *RoomRefusedError
	if errors.As(err, &r) {
		return r.Line, true
	}
	return "", false
}
