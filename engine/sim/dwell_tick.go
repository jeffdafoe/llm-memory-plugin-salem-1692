package sim

import (
	"context"
	"log"
	"time"
)

// Dwell tick — in-memory port of engine/dwell_tick.go's
// dispatchObjectRefreshDwell + applyDwellCredit.
//
// Per-minute handler converting dwell credits into need recovery for
// actors still present at the pinned object. Algorithm:
//
//   For each actor, for each DwellCredit:
//     1. If not ripe (LastCreditedAt + period > now), skip.
//     2. If actor is no longer at the credit's object (loiter check),
//        delete the credit and continue.
//     3. If credit.Attribute is unknown to the Needs registry, delete
//        the credit (defense in depth) and continue.
//     4. Apply DwellDelta to actor.Needs[attribute] via ClampNeed.
//     5. Compute floor-hit (pre>0 && post==0) and item-exhausted
//        (source=item && RemainingTicks <= 1).
//     6. If item-exhausted, delete the credit. Otherwise advance
//        LastCreditedAt by exactly DwellPeriodMinutes (residual time
//        carries forward; phase doesn't drift) and decrement
//        RemainingTicks for item-source credits.
//     7. Emit dwell-completion narration for PC actors on item-
//        exhausted or floor-hit.
//
// LOITER LOOKUP STUB. Same as ApplyObjectRefreshAtArrival: legacy
// resolveLoiteringStructure isn't ported yet. Using Euclidean distance
// against ObjectRefreshArrivalTolerance until loitering lands.
//
// HUB BROADCAST STUB. Dwell-completion narrations would broadcast a
// private room_event for PCs. Until the Hub port, we collect them in
// the result so callers/tests can observe and a future thin wrapper
// can fan them out.

// DwellCompletion is a per-credit narration emission produced by
// ApplyDwellTick. Hub layer (when ported) translates these to private
// room_event broadcasts; until then they're observable via the
// DwellTickResult.
type DwellCompletion struct {
	ActorID       ActorID
	StructureID   StructureID // actor's InsideStructureID at apply time — scope for room_event broadcast
	Attribute     NeedKey
	Source        DwellCreditSource
	ItemExhausted bool
	FloorHit      bool
	Text          string // pre-rendered narration, "" if no vocab matches
	At            time.Time
}

// DwellTickResult is what ApplyDwellTick returns: number of credits
// that fired plus any completion narrations queued for PCs.
type DwellTickResult struct {
	Applied     int
	Completions []DwellCompletion
}

// ApplyDwellTick walks every actor's DwellCredits, applies ripe ones,
// expires departed/exhausted ones, and stamps completion narrations
// for PC actors when an item finishes or a need crosses the floor.
//
// All work happens inside the world goroutine — the command channel
// serializes against concurrent state changes, so the legacy
// row-locking dance isn't needed here.
func ApplyDwellTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			res := DwellTickResult{}

			for actorID, actor := range w.Actors {
				if actor.DwellCredits == nil {
					continue
				}
				// Two-pass to avoid mutating the map mid-iteration.
				var toExpire []DwellCreditKey
				type fired struct {
					key         DwellCreditKey
					credit      *DwellCredit
					itemExhaust bool
					floorHit    bool
					preNeed     int
				}
				var fires []fired

				for key, credit := range actor.DwellCredits {
					nextAt := credit.LastCreditedAt.Add(time.Duration(credit.DwellPeriodMinutes) * time.Minute)
					if nextAt.After(now) {
						continue // not ripe
					}
					if _, known := FindNeed(credit.Attribute); !known {
						log.Printf("sim/dwell_tick: actor %s credit unknown attribute %q (removing)",
							actorID, credit.Attribute)
						toExpire = append(toExpire, key)
						continue
					}
					if !actorAtCreditObject(w, actor, credit) {
						toExpire = append(toExpire, key)
						continue
					}

					preNeed := actor.Needs[credit.Attribute]
					if actor.Needs == nil {
						actor.Needs = make(map[NeedKey]int)
					}
					actor.Needs[credit.Attribute] = ClampNeed(preNeed + credit.DwellDelta)
					postNeed := actor.Needs[credit.Attribute]

					itemExhaust := credit.Source == DwellSourceItem &&
						credit.RemainingTicks != nil &&
						*credit.RemainingTicks <= 1
					floorHit := preNeed > 0 && postNeed == 0
					fires = append(fires, fired{
						key:         key,
						credit:      credit,
						itemExhaust: itemExhaust,
						floorHit:    floorHit,
						preNeed:     preNeed,
					})
				}

				for _, k := range toExpire {
					delete(actor.DwellCredits, k)
				}

				for _, f := range fires {
					if f.itemExhaust {
						delete(actor.DwellCredits, f.key)
					} else {
						f.credit.LastCreditedAt = f.credit.LastCreditedAt.Add(
							time.Duration(f.credit.DwellPeriodMinutes) * time.Minute)
						if f.credit.Source == DwellSourceItem && f.credit.RemainingTicks != nil {
							rem := *f.credit.RemainingTicks - 1
							f.credit.RemainingTicks = &rem
						}
					}
					res.Applied++

					// Stamp completion narration for PCs only.
					if actor.LoginUsername != "" && (f.itemExhaust || f.floorHit) {
						text := DwellCompletionNarration(f.credit.Attribute, f.credit.Source,
							f.itemExhaust, f.floorHit)
						res.Completions = append(res.Completions, DwellCompletion{
							ActorID:       actorID,
							StructureID:   actor.InsideStructureID,
							Attribute:     f.credit.Attribute,
							Source:        f.credit.Source,
							ItemExhausted: f.itemExhaust,
							FloorHit:      f.floorHit,
							Text:          text,
							At:            now,
						})
					}
				}
			}
			return res, nil
		},
	}
}

// actorAtCreditObject returns whether actor's current position is
// within ObjectRefreshArrivalTolerance of the credit's pinned object.
// Returns false if the object no longer exists.
func actorAtCreditObject(w *World, actor *Actor, credit *DwellCredit) bool {
	obj, ok := w.VillageObjects[credit.ObjectID]
	if !ok {
		return false
	}
	dx := obj.X - float64(actor.CurrentX)
	dy := obj.Y - float64(actor.CurrentY)
	return dx*dx+dy*dy <= ObjectRefreshArrivalTolerance*ObjectRefreshArrivalTolerance
}

// DwellTickerInterval is how often RunDwellTicker wakes. Matches
// legacy 1-min cadence.
const DwellTickerInterval = time.Minute

// RunDwellTicker owns the dwell-tick goroutine. Wakes every
// DwellTickerInterval and submits an ApplyDwellTick command.
func RunDwellTicker(ctx context.Context, w *World) {
	t := time.NewTicker(DwellTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, err := w.SendContext(ctx, ApplyDwellTick(time.Now().UTC()))
			if err != nil && ctx.Err() == nil {
				log.Printf("sim/dwell_ticker: %v", err)
			}
		}
	}
}
