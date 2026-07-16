package sim

import (
	"errors"
	"fmt"
	"time"
)

// stoke.go — LLM-412. The stoke command: a timed source-activity that feeds a
// structure's hearth fire with firewood. The repair shape (source_activity.go
// StartRepair) with firewood for nails: validate responsibility + co-location
// + that the fire actually wants wood + that the actor carries enough,
// consume the fuel up front, open the window; the fire-extension lands at
// completion, bound to the object the window began at.

// StartStoke begins a timed stoke of the hearth the actor is responsible for
// keeping in — their own (owner), or their employer's while Working a hired
// job there (HearthToStoke; "tend the fire" is the labor design's own worked
// example, and stoking is work, not leaving). Firewood is consumed at START
// with no refund on an abandoned stoke, exactly the nails posture: the
// move-cancel belt clears the window if the stoker walks off, and the lost
// sticks are the cost of starting and walking away.
//
// The stoke tool is gated to be advertised only in exactly this situation,
// but every gate is re-validated here because the substrate stays
// authoritative.
func StartStoke(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StartStoke: actor %q not in world", actorID)
			}
			if actor.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — arrive before tending the fire.",
				)
			}
			now := time.Now().UTC()
			// Land a finished-but-not-yet-swept window first so a stale activity
			// doesn't spuriously read as "still busy".
			completeIfDue(w, actorID, actor, now)
			if actor.SourceActivity != nil {
				return nil, errors.New(
					"you are already busy — finish what you're doing before tending the fire.",
				)
			}
			// Resolve the hearth the actor may stoke — the same resolver and
			// order the perception cue advertises from, so tool and command
			// can't diverge on who may stoke or which fire.
			hearth, _ := HearthToStoke(w.VillageObjects, w.LaborLedger, actorID)
			if hearth == nil {
				return nil, errors.New("there's no hearth of yours to tend here.")
			}
			// A fire is indoors by nature: the stoker must be INSIDE the
			// hearth's structure (structure-backed objects share their id).
			if string(actor.InsideStructureID) != string(hearth.ID) {
				return nil, errors.New("step inside before tending the fire.")
			}
			if !HearthNeedsStoking(hearth, now, w.Settings.HearthLowMinutes) {
				return nil, errors.New("the fire is already burning well — no need for more wood yet.")
			}
			wood := w.Settings.StokeWoodPerStoke
			if wood <= 0 {
				wood = DefaultStokeWoodPerStoke
			}
			if actor.Inventory[FirewoodItemKind] < wood {
				return nil, fmt.Errorf(
					"feeding the fire takes %d firewood but you have %d — you'll need more first.",
					wood, actor.Inventory[FirewoodItemKind],
				)
			}
			// Consume the firewood up front (delete-on-zero inventory invariant).
			actor.Inventory[FirewoodItemKind] -= wood
			if actor.Inventory[FirewoodItemKind] == 0 {
				delete(actor.Inventory, FirewoodItemKind)
			}
			duration := time.Duration(w.Settings.StokeDurationSeconds) * time.Second
			if duration <= 0 {
				duration = DefaultStokeDurationSeconds * time.Second
			}
			actor.SourceActivity = &SourceActivity{
				Kind:      SourceActivityStoke,
				ObjectID:  hearth.ID,
				StartedAt: now,
				Until:     now.Add(duration),
				Qty:       wood, // sticks consumed — completion extends the fire by this many
			}
			w.emit(&SourceActivityStarted{
				ActorID:  actorID,
				ObjectID: hearth.ID,
				Kind:     SourceActivityStoke,
				Until:    actor.SourceActivity.Until,
				At:       now,
			})
			return SourceActivityStartResult{
				Started:    true,
				Kind:       SourceActivityStoke,
				ObjectID:   hearth.ID,
				SourceName: sourceActivityObjectName(w, hearth),
				Until:      actor.SourceActivity.Until,
			}, nil
		},
	}
}
