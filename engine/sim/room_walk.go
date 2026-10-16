package sim

import (
	"math/rand"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// room_walk.go — LLM-768. People walk inside a room with a floor plan to what
// they are doing, with no LLM turn: whoever starts to stoke the fire walks to
// the hearth (the keeper back to their place when done, out from behind their
// counter by the keeper's way), a villager who sits down to a meal walks to a
// free seat, and now and then an idle guest drifts a step or three. Each walk
// is a square change carrying its path — the frame a player's tap sends too
// (MovePCInRoom) — so every viewer sees the same walk.

const (
	// RoomDriftSweepInterval is how often the drift sweep looks at the rooms.
	RoomDriftSweepInterval = 30 * time.Second
	// roomDriftMin..roomDriftMax: the wait between one guest's drifts.
	roomDriftMin = 3 * time.Minute
	roomDriftMax = 8 * time.Minute
	// roomDriftSteps: how far a drift goes, in steps.
	roomDriftSteps = 3
)

// walkInRoom walks a to `to` in the room they are in — the room's keeper by
// the keeper's way, anyone else over the floor — and returns the path, both
// ends included (just their square when already there). Nil when they are not
// awake in a room with a plan, are on their way out (MoveIntent), someone else
// holds `to`, or there is no way.
func walkInRoom(w *World, a *Actor, to roomlayout.Square) []roomlayout.Square {
	layout := actorRoomPlan(w, a)
	if layout == nil || a.InsideStructureID == "" || a.State == StateSleeping || a.MoveIntent != nil {
		return nil
	}
	from := layout.Door.Square
	if a.RoomSquare != nil {
		from = *a.RoomSquare
	}
	// Only their own square is theirs to "walk" to while someone else holds
	// it — not the door they start from when they have no square.
	alreadyThere := a.RoomSquare != nil && *a.RoomSquare == to
	if !alreadyThere && takenRoomSquares(w, a.InsideStructureID, actorPlanRoom(w, a), a.ID)[to] {
		return nil
	}
	var path []roomlayout.Square
	if roomRole(a, a.InsideStructureID) == roomlayout.RoleKeeper {
		path = layout.KeeperPath(from, to)
	} else {
		path = layout.Path(from, to)
	}
	if path == nil {
		return nil
	}
	if a.RoomSquare == nil || *a.RoomSquare != to {
		sq := to
		a.RoomSquare = &sq
		a.RoomSquareRoom = actorPlanRoom(w, a)
		w.emit(&ActorRoomSquareChanged{ActorID: a.ID, StructureID: a.InsideStructureID, Room: a.RoomSquareRoom, Square: copyRoomSquare(&sq), Path: path})
	}
	return path
}

// RegisterRoomWalkSubscriber wires the stoke and meal walks into the world.
// Must run on the world goroutine (before World.Run or inside a Command.Fn).
func RegisterRoomWalkSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterRoomWalkSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleRoomWalk))
}

func handleRoomWalk(w *World, e Event) {
	switch ev := e.(type) {
	case *SourceActivityStarted:
		if ev.Kind == SourceActivityStoke {
			walkToStoke(w, w.Actors[ev.ActorID])
		}
	case *SourceActivityCompleted:
		if ev.Kind == SourceActivityStoke {
			keeperBackToPlace(w, w.Actors[ev.ActorID])
		}
	case *SourceActivityCancelled:
		if ev.Kind == SourceActivityStoke {
			keeperBackToPlace(w, w.Actors[ev.ActorID])
		}
	case *DwellStarted:
		walkToSeat(w, w.Actors[ev.ActorID])
	}
}

// walkToStoke: the stoker walks to the room's hearth. Someone already standing
// there keeps it; the stoker then stokes from where they are.
func walkToStoke(w *World, a *Actor) {
	if a == nil {
		return
	}
	layout := actorRoomPlan(w, a)
	if layout == nil {
		return
	}
	if st := layout.StationFor(roomlayout.UseStoke); st != nil {
		walkInRoom(w, a, st.Square)
	}
}

// keeperBackToPlace: the room's keeper, done at the fire, goes back behind
// their counter. Anyone else stays where they are. Not on the way out of the
// building (a stoke cancelled by walking off).
func keeperBackToPlace(w *World, a *Actor) {
	if a == nil || a.MoveIntent != nil || roomRole(a, a.InsideStructureID) != roomlayout.RoleKeeper {
		return
	}
	if layout := actorRoomPlan(w, a); layout != nil {
		walkInRoom(w, a, layout.Keeper.Square)
	}
}

// walkToSeat: a villager who sits down to a meal in a room walks to the nearest
// free seat, unless already on one. Players choose their own seat; keepers eat
// at their place.
func walkToSeat(w *World, a *Actor) {
	if a == nil || a.Kind == KindPC || a.InsideStructureID == "" || a.RoomSquare == nil ||
		roomRole(a, a.InsideStructureID) == roomlayout.RoleKeeper {
		return
	}
	layout := actorRoomPlan(w, a)
	if layout == nil {
		return
	}
	taken := takenRoomSquares(w, a.InsideStructureID, actorPlanRoom(w, a), a.ID)
	var best []roomlayout.Square
	for _, st := range layout.Stations {
		if st.Kind != "seat" {
			continue
		}
		if st.Square == *a.RoomSquare {
			return
		}
		if taken[st.Square] {
			continue
		}
		if p := layout.Path(*a.RoomSquare, st.Square); p != nil && (best == nil || len(p) < len(best)) {
			best = p
		}
	}
	if best != nil {
		walkInRoom(w, a, best[len(best)-1])
	}
}

// RoomDriftResult reports one drift sweep: how many guests stepped.
type RoomDriftResult struct {
	Drifted int
}

// DriftInRooms is one pass of the idle drift: each idle guest in a room with a
// plan — a villager, not the room's keeper, awake, not eating, not busy at a
// task, not on their way out — steps to a free square one to three steps away
// when their time comes, then waits 3 to 8 minutes for the next. The first
// pass that sees a guest only sets their time. rng picks the square and the
// wait (a seeded one in tests).
func DriftInRooms(now time.Time, rng *rand.Rand) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			res := RoomDriftResult{}
			sids := make([]StructureID, 0, len(w.actorsByStructure))
			for sid := range w.actorsByStructure {
				sids = append(sids, sid)
			}
			sort.Slice(sids, func(i, j int) bool { return sids[i] < sids[j] })
			for _, sid := range sids {
				if roomPlan(w, sid) == nil {
					continue
				}
				ids := make([]ActorID, 0, len(w.actorsByStructure[sid]))
				for id := range w.actorsByStructure[sid] {
					ids = append(ids, id)
				}
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				for _, id := range ids {
					a := w.Actors[id]
					if a == nil || !roomDriftIdle(a, sid) {
						continue
					}
					layout := actorRoomPlan(w, a)
					if layout == nil {
						continue
					}
					if a.RoomDriftAt.IsZero() {
						a.RoomDriftAt = now.Add(roomDriftWait(rng))
						continue
					}
					if now.Before(a.RoomDriftAt) {
						continue
					}
					a.RoomDriftAt = now.Add(roomDriftWait(rng))
					if to, ok := roomDriftSquare(w, a, layout, rng); ok && walkInRoom(w, a, to) != nil {
						res.Drifted++
					}
				}
			}
			return res, nil
		},
	}
}

func roomDriftIdle(a *Actor, sid StructureID) bool {
	if a.Kind == KindPC || a.State == StateSleeping || a.RoomSquare == nil || a.MoveIntent != nil ||
		a.SourceActivity != nil || roomRole(a, sid) == roomlayout.RoleKeeper {
		return false
	}
	for key := range a.DwellCredits {
		if key.Source == DwellSourceItem {
			return false
		}
	}
	return true
}

func roomDriftWait(rng *rand.Rand) time.Duration {
	return roomDriftMin + time.Duration(rng.Int63n(int64(roomDriftMax-roomDriftMin)+1))
}

// roomDriftSquare picks a free square one to roomDriftSteps steps from the
// guest — never the door (stepping onto it reads as leaving), never right
// beside someone else (LLM-788).
func roomDriftSquare(w *World, a *Actor, layout *roomlayout.Layout, rng *rand.Rand) (roomlayout.Square, bool) {
	taken := takenRoomSquares(w, a.InsideStructureID, actorPlanRoom(w, a), a.ID)
	var near []roomlayout.Square
	for y := 0; y < layout.Height(); y++ {
		for x := 0; x < layout.Width(); x++ {
			s := roomlayout.Square{X: x, Y: y}
			if s == *a.RoomSquare || s == layout.Door.Square || taken[s] || !layout.Walkable(s) {
				continue
			}
			if p := layout.Path(*a.RoomSquare, s); p != nil && len(p)-1 <= roomDriftSteps {
				near = append(near, s)
			}
		}
	}
	if len(near) == 0 {
		return roomlayout.Square{}, false
	}
	// Never step up beside someone (LLM-788): a square with nobody on the
	// eight round it, picked at random; with none in reach, stay put — unless
	// they already stand beside someone, then step to the roomiest square that
	// gives them more room than they have.
	var clear []roomlayout.Square
	for _, s := range near {
		if roomlayout.Room(s, taken) > 1 {
			clear = append(clear, s)
		}
	}
	if len(clear) > 0 {
		return clear[rng.Intn(len(clear))], true
	}
	if best := roomlayout.Roomiest(near, taken); roomlayout.Room(best, taken) > roomlayout.Room(*a.RoomSquare, taken) {
		return best, true
	}
	return roomlayout.Square{}, false
}
