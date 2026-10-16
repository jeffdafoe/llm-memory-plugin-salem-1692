package sim

import (
	"errors"
	"fmt"
	"time"
)

// outhouse.go — LLM-806. A player steps into an outhouse: the client walks them
// to it (object_visit) and on arrival posts /pc/outhouse. UseOuthouse checks
// they stand at it and nobody else is inside, holds it for OuthouseStay, and
// emits OuthouseUsed — every client hides the player for the stay and plays the
// outhouse sounds at the outhouse, quieter with distance.
//
// The hold is transient world state (outhouseVisits): a restart loses at most
// one stay. Only the clients hide the player: in the sim they stay at the
// door, so NPCs see them standing there, and the client ignores walk clicks
// for the stay. NPCs do not use outhouses.

// TagOuthouse is the per-instance tag that makes a placement an outhouse.
const TagOuthouse = "outhouse"

// OuthouseStay is how long a player is inside.
const OuthouseStay = 10 * time.Second

var (
	// ErrNotOuthouse — the object is not tagged an outhouse.
	ErrNotOuthouse = errors.New("that is not an outhouse")
	// ErrOuthouseOutOfReach — the player is not at the outhouse door.
	ErrOuthouseOutOfReach = errors.New("you are not close enough — stand at the door first")
	// ErrOuthouseInUse — someone is inside already.
	ErrOuthouseInUse = errors.New("Someone is in there.")
)

// outhouseVisit is one player's stay in an outhouse.
type outhouseVisit struct {
	ActorID ActorID
	Until   time.Time
}

// OuthouseUsed is emitted when a player goes into an outhouse. Wire-only.
type OuthouseUsed struct {
	EventBase
	ObjectID VillageObjectID
	ActorID  ActorID
	Stay     time.Duration
	At       time.Time
}

func (OuthouseUsed) isSimEvent() {}

// OuthouseResult is UseOuthouse's reply.
type OuthouseResult struct {
	Stay time.Duration
}

// UseOuthouse puts actorID into the outhouse objectID for OuthouseStay.
func UseOuthouse(actorID ActorID, objectID VillageObjectID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor := w.Actors[actorID]
			if actor == nil {
				return nil, fmt.Errorf("UseOuthouse: actor %q not in world", actorID)
			}
			obj := w.VillageObjects[objectID]
			if obj == nil || !obj.HasTag(TagOuthouse) {
				return nil, ErrNotOuthouse
			}
			pin, ok := effectiveObjectLoiterTile(w, objectID)
			if !ok || actor.InsideStructureID != "" || pin.Chebyshev(actor.Pos) > LoiterAttributionTiles {
				return nil, ErrOuthouseOutOfReach
			}
			if v, held := w.outhouseVisits[objectID]; held && now.Before(v.Until) {
				return nil, ErrOuthouseInUse
			}
			// One outhouse at a time: a player at the doors of two is still in
			// the first until its stay ends.
			for _, v := range w.outhouseVisits {
				if v.ActorID == actorID && now.Before(v.Until) {
					return nil, ErrOuthouseInUse
				}
			}
			if w.outhouseVisits == nil {
				w.outhouseVisits = make(map[VillageObjectID]outhouseVisit)
			}
			w.outhouseVisits[objectID] = outhouseVisit{ActorID: actorID, Until: now.Add(OuthouseStay)}
			w.emit(&OuthouseUsed{ObjectID: objectID, ActorID: actorID, Stay: OuthouseStay, At: now})
			return OuthouseResult{Stay: OuthouseStay}, nil
		},
	}
}
