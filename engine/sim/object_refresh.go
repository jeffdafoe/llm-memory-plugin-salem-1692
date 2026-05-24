package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"
	"unicode/utf8"
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

// MaxRefreshAttributeLen caps a refresh row's attribute name, matching the
// object_refresh.attribute varchar(32) column in the prod baseline.
const MaxRefreshAttributeLen = 32

// ErrInvalidRefresh is returned by ValidateObjectRefreshes when a proposed
// refresh row set fails validation (→ 400 at the HTTP layer). The detail is
// wrapped via fmt.Errorf("%w: ...") so callers map it by errors.Is while still
// surfacing which row/field was wrong.
var ErrInvalidRefresh = errors.New("invalid object refresh")

// ValidateObjectRefreshes checks a proposed refresh row set against the
// object_refresh CHECK constraints (migrations/schema.sql) and the sim's
// finite/regen/dwell invariants, so the editor's set-refresh route returns a
// clean 400 rather than a generic 500 from a Postgres constraint violation at
// the next checkpoint. Returns ErrInvalidRefresh on the first violation.
//
// Rules:
//   - attribute: non-empty, <= MaxRefreshAttributeLen runes, a known need
//     (FindNeed — mirrors the refresh_attribute FK), and unique within the set
//     (the (object_id, attribute) primary key).
//   - amount: < 0 (object_refresh_amount_negative) — it is the decrement
//     applied to the actor on arrival, not a restoration.
//   - finite pair: available_quantity and max_quantity are both set or both nil
//     (quantity_pair). When set: available >= 0 (quantity_nonneg), max > 0
//     (max_positive), available <= max (available_le_max).
//   - regen only when finite: an infinite row (available_quantity nil) carries
//     no supply to replenish, so it must omit refresh_mode and
//     refresh_period_hours. A finite row's mode must be "continuous" or
//     "periodic" (mode_check); its period, when set, must be > 0
//     (period_positive) — a finite row may legitimately omit the period to mean
//     "depletes and never refills".
//   - dwell pair: dwell_delta and dwell_period_minutes are both set or both nil
//     (dwell_pair). When set: dwell_delta < 0 (dwell_amount_negative),
//     dwell_period_minutes > 0 (dwell_period_positive).
func ValidateObjectRefreshes(rows []*ObjectRefresh) error {
	seen := make(map[NeedKey]bool, len(rows))
	for _, r := range rows {
		if r == nil {
			return fmt.Errorf("%w: nil refresh row", ErrInvalidRefresh)
		}
		if r.Attribute == "" {
			return fmt.Errorf("%w: attribute is required", ErrInvalidRefresh)
		}
		if utf8.RuneCountInString(string(r.Attribute)) > MaxRefreshAttributeLen {
			return fmt.Errorf("%w: attribute %q exceeds %d characters", ErrInvalidRefresh, r.Attribute, MaxRefreshAttributeLen)
		}
		if _, known := FindNeed(r.Attribute); !known {
			return fmt.Errorf("%w: unknown attribute %q", ErrInvalidRefresh, r.Attribute)
		}
		if seen[r.Attribute] {
			return fmt.Errorf("%w: duplicate attribute %q", ErrInvalidRefresh, r.Attribute)
		}
		seen[r.Attribute] = true

		if r.Amount >= 0 {
			return fmt.Errorf("%w: amount for %q must be negative", ErrInvalidRefresh, r.Attribute)
		}

		if (r.AvailableQuantity == nil) != (r.MaxQuantity == nil) {
			return fmt.Errorf("%w: available_quantity and max_quantity for %q must both be set or both omitted", ErrInvalidRefresh, r.Attribute)
		}
		if r.AvailableQuantity != nil {
			if *r.AvailableQuantity < 0 {
				return fmt.Errorf("%w: available_quantity for %q must be >= 0", ErrInvalidRefresh, r.Attribute)
			}
			if *r.MaxQuantity <= 0 {
				return fmt.Errorf("%w: max_quantity for %q must be > 0", ErrInvalidRefresh, r.Attribute)
			}
			if *r.AvailableQuantity > *r.MaxQuantity {
				return fmt.Errorf("%w: available_quantity for %q cannot exceed max_quantity", ErrInvalidRefresh, r.Attribute)
			}
		}

		if r.IsFinite() {
			switch r.RefreshMode {
			case RefreshModeContinuous, RefreshModePeriodic:
			default:
				return fmt.Errorf(`%w: refresh_mode for %q must be "continuous" or "periodic"`, ErrInvalidRefresh, r.Attribute)
			}
			if r.RefreshPeriodHours != nil && *r.RefreshPeriodHours <= 0 {
				return fmt.Errorf("%w: refresh_period_hours for %q must be > 0", ErrInvalidRefresh, r.Attribute)
			}
		} else {
			if r.RefreshMode != "" {
				return fmt.Errorf("%w: refresh_mode for %q is only valid on a finite (available/max) row", ErrInvalidRefresh, r.Attribute)
			}
			if r.RefreshPeriodHours != nil {
				return fmt.Errorf("%w: refresh_period_hours for %q is only valid on a finite (available/max) row", ErrInvalidRefresh, r.Attribute)
			}
		}

		if (r.DwellDelta == nil) != (r.DwellPeriodMinutes == nil) {
			return fmt.Errorf("%w: dwell_delta and dwell_period_minutes for %q must both be set or both omitted", ErrInvalidRefresh, r.Attribute)
		}
		if r.DwellDelta != nil {
			if *r.DwellDelta >= 0 {
				return fmt.Errorf("%w: dwell_delta for %q must be negative", ErrInvalidRefresh, r.Attribute)
			}
			if *r.DwellPeriodMinutes <= 0 {
				return fmt.Errorf("%w: dwell_period_minutes for %q must be > 0", ErrInvalidRefresh, r.Attribute)
			}
		}
	}
	return nil
}

// cloneObjectRefresh returns a deep copy of r so stored world state never
// aliases a caller-owned ObjectRefresh or its pointer fields, and so a result
// snapshot read off the world goroutine can't race the regen tick mutating a
// live row's AvailableQuantity.
func cloneObjectRefresh(r *ObjectRefresh) *ObjectRefresh {
	c := *r // value fields: Attribute, Amount, RefreshMode
	c.AvailableQuantity = copyIntPtr(r.AvailableQuantity)
	c.MaxQuantity = copyIntPtr(r.MaxQuantity)
	c.RefreshPeriodHours = copyIntPtr(r.RefreshPeriodHours)
	c.LastRefreshAt = copyTimePtr(r.LastRefreshAt)
	c.DwellDelta = copyIntPtr(r.DwellDelta)
	c.DwellPeriodMinutes = copyIntPtr(r.DwellPeriodMinutes)
	return &c
}

// copyTimePtr returns a fresh pointer to a copy of *p, or nil when p is nil.
func copyTimePtr(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// intPtrEqual reports whether two *int hold the same value (both nil counts as
// equal). Used to decide whether a refresh row's regen schedule is unchanged.
func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
// TODO(rewrite): the Hub/WS layer now exists (state flips ride
// object_state_changed), but a finite-supply DECREMENT here does not yet
// surface to clients — wire a supply-update broadcast (and a per-actor
// refresh-event line) over the hub when supply UI is needed.
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
		dx := obj.Pos.X - x
		dy := obj.Pos.Y - y
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

// regenObjectRefresh applies one regen step to all refresh rows in the
// world. Continuous-mode rows accrue units since LastRefreshAt; periodic-
// mode rows jump to MaxQuantity once enough time has elapsed. Returns the
// count of rows whose AvailableQuantity changed.
//
// Exposed as a free function (not a Command) so RunObjectRefreshRegen
// can call it inside a single command — touching many rows at once is
// fine within the world goroutine.
//
// Unexported by design (see buildWalkGrid).
func regenObjectRefresh(w *World, now time.Time) int {
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
			_, err := w.SendContext(ctx, Command{
				Fn: func(world *World) (any, error) {
					return regenObjectRefresh(world, time.Now().UTC()), nil
				},
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("sim/object_refresh_regen: tick failed: %v", err)
			}
		}
	}
}
