package sim

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"
)

// Object-refresh — in-memory port of engine/object_refresh.go and
// engine/object_refresh_tick.go.
//
// Two mechanisms work together:
//
//  1. ApplyObjectRefreshAtArrival — when an actor arrives at a village
//     object within tolerance of a refresh-tagged object, the object's
//     configured attribute drops apply to the actor. Multi-attribute
//     objects (shaded oak: tiredness + hunger) apply all rows. Finite-
//     supply rows (well with available_quantity=10) decrement; depleted
//     rows are silent skips. Per-row dwell config (ZBBS-172) seeds an
//     actor_dwell_credit upsert for the per-minute dwell tick to honor.
//
//  2. RunObjectRefreshRegen — wakes every minute. continuous-mode rows
//     accrue one unit every (period_hours / max_quantity) hours; periodic-
//     mode rows jump to max_quantity once period_hours have elapsed since
//     the anchor. Both clamp at max_quantity.
//
// SPATIAL LOOKUP STUB. Legacy applyObjectRefreshAtArrival uses
// resolveLoiteringStructure (loiter-pin reverse lookup with king's-move
// slots). The full loiter-pin model isn't in sim yet (it depends on
// structure/room geometry that's not ported). For now, this uses a
// simple Euclidean-distance match within ObjectRefreshArrivalTolerance
// pixels. When loitering ports, swap the helper without changing the
// command signature.

// ObjectRefreshArrivalTolerance is the pixel radius around a village
// object that counts as "arrived at" for refresh purposes. Two tiles
// (64 px) matches the legacy bounding-box pre-filter; the loiter-pin
// version will replace this with the proper geometry.
const ObjectRefreshArrivalTolerance = 64.0

// RefreshMode controls how a finite-supply refresh row replenishes.
type RefreshMode string

const (
	// RefreshModeContinuous accrues one unit every (RefreshPeriodHours /
	// MaxQuantity) hours. Wells, berry bushes — smooth recharge.
	RefreshModeContinuous RefreshMode = "continuous"
	// RefreshModePeriodic jumps to MaxQuantity once RefreshPeriodHours
	// have elapsed since LastRefreshAt. Crops, harvests — discrete refill.
	RefreshModePeriodic RefreshMode = "periodic"
)

// ObjectRefresh is one (object, attribute) refresh policy. The legacy
// schema uses (object_id, attribute) as the composite key; in sim the
// owning VillageObject carries them as a slice (per-aggregate model).
type ObjectRefresh struct {
	Attribute NeedKey
	Amount    int // negative — the decrement applied to the actor on arrival

	// Optional finite supply. AvailableQuantity == nil means infinite —
	// no decrement, no regen, the well never runs dry. When non-nil,
	// MaxQuantity must also be non-nil (paired by the legacy CHECK
	// constraint).
	AvailableQuantity  *int
	MaxQuantity        *int
	RefreshMode        RefreshMode
	RefreshPeriodHours *int
	LastRefreshAt      *time.Time // regen anchor; nil = stamp on first tick

	// Optional dwell-credit config (ZBBS-172). When set, the arrival also
	// upserts an actor_dwell_credit row so subsequent dwell ticks credit
	// the actor while they stay at the object.
	DwellDelta         *int // negative
	DwellPeriodMinutes *int
}

// IsFinite reports whether this refresh row has a tracked supply.
func (r *ObjectRefresh) IsFinite() bool {
	return r.AvailableQuantity != nil
}

// HasDwell reports whether this row also credits per-minute dwell.
func (r *ObjectRefresh) HasDwell() bool {
	return r.DwellDelta != nil && r.DwellPeriodMinutes != nil
}

// RefreshHit is one applied attribute drop returned by
// ApplyObjectRefreshAtArrival — used by audit logging and (when ported)
// the Hub broadcast.
type RefreshHit struct {
	ObjectID  VillageObjectID
	Attribute NeedKey
	Amount    int // amount actually applied (matches the row Amount)
	NewValue  int // post-clamp actor need value
}

// ArrivalRefreshResult is the command-reply payload — hits applied plus
// any dwell credits that were stamped/refreshed.
type ArrivalRefreshResult struct {
	ObjectID VillageObjectID
	Hits     []RefreshHit
}

// ApplyObjectRefreshAtArrival returns a Command that resolves the nearest
// refresh-tagged village object to (x, y), applies its refresh rows to
// the actor's needs, decrements finite supplies, and upserts dwell credits
// for any rows with dwell config.
//
// Returns ArrivalRefreshResult with empty Hits if no refresh-tagged object
// is within tolerance, or if every row was depleted / unknown attribute.
// Errors on missing actor.
//
// TODO(rewrite): replace the simple-tolerance lookup with the loiter-pin
// resolver once the loitering geometry is ported.
//
// TODO(rewrite): when Hub/WS layer ports, broadcast object_state_changed
// (for supply UI updates) and a per-actor refresh-event line.
//
// TODO(rewrite): when agent_action_log sink is wired in, append an
// 'engine'-source log row capturing the hits.
func ApplyObjectRefreshAtArrival(actorID ActorID, x, y float64) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("actor %q not found", actorID)
			}

			objID, obj := findRefreshObjectNear(w, x, y)
			if obj == nil {
				return ArrivalRefreshResult{}, nil
			}

			if actor.Needs == nil {
				actor.Needs = make(map[NeedKey]int)
			}
			if actor.DwellCredits == nil {
				actor.DwellCredits = make(map[DwellCreditKey]*DwellCredit)
			}

			var hits []RefreshHit
			now := time.Now().UTC()
			for _, r := range obj.Refreshes {
				if r.IsFinite() && *r.AvailableQuantity <= 0 {
					continue // dry well, empty bush
				}
				if _, known := FindNeed(r.Attribute); !known {
					log.Printf("sim/object_refresh: %s has unknown attribute %q (skipped)",
						objID, r.Attribute)
					continue
				}
				if r.Amount == 0 {
					continue // misconfigured zero-amount row; silent skip
				}
				newValue := ClampNeed(actor.Needs[r.Attribute] + r.Amount)
				actor.Needs[r.Attribute] = newValue

				if r.IsFinite() {
					next := *r.AvailableQuantity - 1
					r.AvailableQuantity = &next
				}

				hits = append(hits, RefreshHit{
					ObjectID:  objID,
					Attribute: r.Attribute,
					Amount:    r.Amount,
					NewValue:  newValue,
				})

				if r.HasDwell() {
					key := DwellCreditKey{
						ObjectID:  objID,
						Attribute: r.Attribute,
						Source:    DwellSourceObject,
					}
					actor.DwellCredits[key] = &DwellCredit{
						ObjectID:           objID,
						Attribute:          r.Attribute,
						Source:             DwellSourceObject,
						LastCreditedAt:     now,
						RemainingTicks:     nil, // source=object: open-ended
						DwellDelta:         *r.DwellDelta,
						DwellPeriodMinutes: *r.DwellPeriodMinutes,
					}
				}
			}
			return ArrivalRefreshResult{ObjectID: objID, Hits: hits}, nil
		},
	}
}

// findRefreshObjectNear returns the nearest refresh-tagged village object
// within ObjectRefreshArrivalTolerance pixels of (x, y), or nil if none.
// Linear scan — fine for v1; spatial index lands with the loiter-pin port.
func findRefreshObjectNear(w *World, x, y float64) (VillageObjectID, *VillageObject) {
	var bestID VillageObjectID
	var bestObj *VillageObject
	bestDist2 := ObjectRefreshArrivalTolerance * ObjectRefreshArrivalTolerance
	for id, obj := range w.VillageObjects {
		if len(obj.Refreshes) == 0 {
			continue
		}
		dx := obj.X - x
		dy := obj.Y - y
		d2 := dx*dx + dy*dy
		if d2 > bestDist2 {
			continue
		}
		bestDist2 = d2
		bestID = id
		bestObj = obj
	}
	return bestID, bestObj
}

// RegenObjectRefresh applies one regen step to all refresh rows in the
// world. Continuous-mode rows accrue units since LastRefreshAt; periodic-
// mode rows jump to MaxQuantity once enough time has elapsed. Returns the
// count of rows whose AvailableQuantity changed.
//
// Exposed as a free function (not a Command) so RunObjectRefreshRegen
// can call it inside a single command — touching many rows at once is
// fine within the world goroutine.
func RegenObjectRefresh(w *World, now time.Time) int {
	touched := 0
	for _, obj := range w.VillageObjects {
		for _, r := range obj.Refreshes {
			if !r.IsFinite() {
				continue
			}
			if r.RefreshPeriodHours == nil || *r.RefreshPeriodHours <= 0 {
				continue
			}
			if r.LastRefreshAt == nil {
				// Just-edited or freshly-loaded — stamp the anchor on
				// this pass, no regen until next.
				t := now
				r.LastRefreshAt = &t
				continue
			}
			if *r.AvailableQuantity >= *r.MaxQuantity {
				continue // already full
			}
			elapsed := now.Sub(*r.LastRefreshAt)
			if elapsed <= 0 {
				continue
			}
			switch r.RefreshMode {
			case RefreshModeContinuous:
				// One unit every (period_hours / max_quantity) hours.
				unitSeconds := float64(*r.RefreshPeriodHours) * 3600.0 / float64(*r.MaxQuantity)
				if unitSeconds <= 0 {
					continue
				}
				units := int(math.Floor(elapsed.Seconds() / unitSeconds))
				if units <= 0 {
					continue
				}
				room := *r.MaxQuantity - *r.AvailableQuantity
				if units > room {
					units = room
				}
				next := *r.AvailableQuantity + units
				r.AvailableQuantity = &next
				// Advance anchor by exactly the consumed time, keeping
				// the sub-unit residual for the next tick.
				adv := time.Duration(float64(units) * unitSeconds * float64(time.Second))
				newAnchor := r.LastRefreshAt.Add(adv)
				r.LastRefreshAt = &newAnchor
				touched++
			case RefreshModePeriodic:
				if elapsed < time.Duration(*r.RefreshPeriodHours)*time.Hour {
					continue
				}
				next := *r.MaxQuantity
				r.AvailableQuantity = &next
				// Advance anchor by exactly period_hours so a missed
				// tick during a long outage doesn't compound multiple
				// harvests.
				newAnchor := r.LastRefreshAt.Add(time.Duration(*r.RefreshPeriodHours) * time.Hour)
				r.LastRefreshAt = &newAnchor
				touched++
			default:
				log.Printf("sim/object_refresh_regen: unknown mode %q on %s (skipped)",
					r.RefreshMode, r.Attribute)
			}
		}
	}
	return touched
}

// ObjectRefreshRegenInterval is how often RunObjectRefreshRegen wakes.
// Matches legacy cadence — one minute, fine-grained enough that
// continuous-mode regen lands close to the unit boundary in most cases.
const ObjectRefreshRegenInterval = time.Minute

// RunObjectRefreshRegen owns the regen ticker goroutine. Wakes every
// ObjectRefreshRegenInterval, submits a regen command that scans every
// refresh row and applies regen per mode.
func RunObjectRefreshRegen(ctx context.Context, w *World) {
	t := time.NewTicker(ObjectRefreshRegenInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, err := w.Send(Command{
				Fn: func(world *World) (any, error) {
					return RegenObjectRefresh(world, time.Now().UTC()), nil
				},
			})
			if err != nil {
				log.Printf("sim/object_refresh_regen: tick failed: %v", err)
			}
		}
	}
}
