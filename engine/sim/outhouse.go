package sim

import (
	"errors"
	"fmt"
	"log"
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
//
// LLM-811: when the stay ends (a timer armed by UseOuthouse) the player is a
// little less weary — OuthouseTirednessRelief, once per player every
// OuthouseReliefCooldown. The last relief per player is transient too
// (outhouseReliefAt): a restart forgets it.

// TagOuthouse is the per-instance tag that makes a placement an outhouse.
const TagOuthouse = "outhouse"

// OuthouseStay is how long a player is inside.
const OuthouseStay = 10 * time.Second

// OuthouseTirednessRelief is how much tiredness (0..NeedMax) the end of a stay
// takes off — about 50 minutes of rest under a shade tree.
const OuthouseTirednessRelief = 2

// OuthouseReliefCooldown is how long a player waits between reliefs. A stay
// inside it still plays out; it just does not ease tiredness.
const OuthouseReliefCooldown = 4 * time.Hour

// outhouseNeed is the need an outhouse stay eases.
const outhouseNeed NeedKey = "tiredness"

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
	Stay  time.Duration
	Until time.Time
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
			until := now.Add(OuthouseStay)
			w.outhouseVisits[objectID] = outhouseVisit{ActorID: actorID, Until: until}
			w.emit(&OuthouseUsed{ObjectID: objectID, ActorID: actorID, Stay: OuthouseStay, At: now})
			armOuthouseStayEnd(w, objectID, actorID, until)
			return OuthouseResult{Stay: OuthouseStay, Until: until}, nil
		},
	}
}

// outhouseAfterFunc schedules a stay's end. time.AfterFunc in production;
// tests swap it to capture the delay instead of arming a real timer.
var outhouseAfterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }

// armOuthouseStayEnd starts the one-shot timer that ends a stay at until (not
// a fresh OuthouseStay from now: the command may run after its now was taken).
// Mirrors armEstablishmentCloseupEviction: the callback hops back onto the
// world goroutine, guarded by LifecycleContext so a shutdown while armed
// aborts. MUST be called from inside a Command.Fn.
func armOuthouseStayEnd(w *World, objectID VillageObjectID, actorID ActorID, until time.Time) {
	outhouseAfterFunc(max(time.Until(until), 0), func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return
		}
		if _, err := w.SendContext(ctx, finishOuthouseStay(objectID, actorID, until, time.Now().UTC())); err != nil && ctx.Err() == nil {
			log.Printf("sim/outhouse: ending the stay of %s in %s failed: %v", actorID, objectID, err)
		}
	})
}

// finishOuthouseStay ends the stay it was armed for — a no-op when that visit
// is gone or has been replaced — and eases the player's tiredness unless they
// had the relief within OuthouseReliefCooldown. Returns whether tiredness moved.
func finishOuthouseStay(objectID VillageObjectID, actorID ActorID, until, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			v, ok := w.outhouseVisits[objectID]
			if !ok || v.ActorID != actorID || !v.Until.Equal(until) {
				return false, nil
			}
			delete(w.outhouseVisits, objectID)
			actor := w.Actors[actorID]
			if actor == nil || actor.Needs[outhouseNeed] <= 0 {
				return false, nil
			}
			if last, ok := w.outhouseReliefAt[actorID]; ok && now.Sub(last) < OuthouseReliefCooldown {
				return false, nil
			}
			actor.Needs[outhouseNeed] = ClampNeed(actor.Needs[outhouseNeed] - OuthouseTirednessRelief)
			if w.outhouseReliefAt == nil {
				w.outhouseReliefAt = make(map[ActorID]time.Time)
			}
			w.outhouseReliefAt[actorID] = now
			// Push the new value to the player's HUD at once, like eating in
			// place (applyObjectRefreshEffect).
			if actor.Kind == KindPC {
				needs := make(map[NeedKey]int, len(actor.Needs))
				for k, n := range actor.Needs {
					needs[k] = n
				}
				w.emit(&PCNeedsChanged{ActorID: actorID, Needs: needs})
			}
			return true, nil
		},
	}
}
