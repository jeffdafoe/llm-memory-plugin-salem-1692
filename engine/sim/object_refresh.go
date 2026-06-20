package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
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
// SPATIAL LOOKUP. ApplyObjectRefreshAtArrival resolves the loitering
// object via resolveLoiteringObject (the v2 port of v1
// resolveLoiteringStructure: nearest named object whose loiter pin is
// within LoiterAttributionTiles king's-move tiles of the actor), then
// checks that object for refresh rows. This is the faithful v1 reverse
// lookup; the earlier Euclidean-tolerance stub is gone.

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

	// GatherItem, when non-empty, marks this refresh row's source object as
	// HARVESTABLE (ZBBS-WORK-328): an actor loitering at the owning village
	// object can mint GatherItem into their inventory — an NPC via the
	// `gather` tool, a PC via POST /api/village/pc/gather. Both actor kinds
	// draw down the SAME AvailableQuantity counter (one shared stock per
	// source) and the regen tick refills it. Empty = not gatherable (the
	// common case; most refresh rows are consume-in-place only).
	//
	// The yield rides on the arrival-need-drop row by design: a well or a
	// bush is one shared stock — drinking in place and filling a pail both
	// deplete it. Limitation: because the yield lives on a refresh row, a
	// gatherable source must also be a need-bearing source (well=thirst,
	// bush=hunger). A pure-material gatherable with no consume-in-place need
	// (e.g. a wheat field) doesn't fit this row cleanly — revisit if wanted.
	GatherItem ItemKind
}

// IsGatherable reports whether an actor can harvest a portable item from
// this refresh row's source (the `gather` tool / pc/gather). ZBBS-WORK-328.
// Trim-aware: a whitespace-only gather_item (hand/admin-edited) is NOT
// gatherable, so it never advertises the tool or renders a cue only to be
// rejected at command time by resolveItemKind.
func (r *ObjectRefresh) IsGatherable() bool {
	return strings.TrimSpace(string(r.GatherItem)) != ""
}

// IsFinite reports whether this refresh row has a tracked supply.
func (r *ObjectRefresh) IsFinite() bool {
	return r.AvailableQuantity != nil
}

// MaxRefreshAttributeLen caps a refresh row's attribute name, matching the
// object_refresh.attribute varchar(32) column in the prod baseline.
const MaxRefreshAttributeLen = 32

// MaxGatherItemLen caps a refresh row's gather_item name, matching the
// object_refresh.gather_item varchar(32) column (ZBBS-WORK-328). The item is
// validated against the live catalog at gather time, not here — this is just
// the column-width guard so the editor's set-refresh route returns a clean 400
// rather than a Postgres truncation error at the next checkpoint.
const MaxGatherItemLen = 32

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

		if utf8.RuneCountInString(string(r.GatherItem)) > MaxGatherItemLen {
			return fmt.Errorf("%w: gather_item %q for %q exceeds %d characters", ErrInvalidRefresh, r.GatherItem, r.Attribute, MaxGatherItemLen)
		}
	}
	return nil
}

// cloneObjectRefresh returns a deep copy of r so stored world state never
// aliases a caller-owned ObjectRefresh or its pointer fields, and so a result
// snapshot read off the world goroutine can't race the regen tick mutating a
// live row's AvailableQuantity.
func cloneObjectRefresh(r *ObjectRefresh) *ObjectRefresh {
	c := *r // value fields: Attribute, Amount, RefreshMode, GatherItem
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

// ApplyObjectRefreshAtArrival returns a Command that resolves the village
// object the actor is loitering at (resolveLoiteringObject, Chebyshev <= 1
// tile to the loiter pin), and — if that object carries refresh rows —
// applies them to the actor's needs, decrements finite supplies, and
// upserts dwell credits for any rows with dwell config.
//
// Resolves off actor.Pos directly: arrival is "this actor is now standing
// here," so the actor's own tile is the query point (no external x,y).
//
// Returns ArrivalRefreshResult with empty Hits if no named object owns the
// actor's tile, if the resolved object has no refresh rows (resolve-then-
// check, faithful to v1: loitering at a bench next to a well gets you the
// bench, not the well), or if every row was depleted / unknown attribute.
// Errors on missing actor.
//
// TODO(rewrite): the Hub/WS layer now exists (state flips ride
// object_state_changed), but a finite-supply DECREMENT here does not yet
// surface to clients — wire a supply-update broadcast (and a per-actor
// refresh-event line) over the hub when supply UI is needed.
//
// TODO(rewrite): when agent_action_log sink is wired in, append an
// 'engine'-source log row capturing the hits.
func ApplyObjectRefreshAtArrival(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("actor %q not found", actorID)
			}

			objID, obj := findRefreshObjectNear(w, actor.Pos)
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
			// Eating in place may have drained a finite bush — recompute its
			// berries/bare visual so a picked-clean bush goes bare.
			refreshObjectBerryState(w, obj)
			return ArrivalRefreshResult{ObjectID: objID, Hits: hits}, nil
		},
	}
}

// findRefreshObjectNear resolves the named village object the actor is
// loitering at (resolveLoiteringObject, attribution radius) and returns it
// only if it carries refresh rows. Resolve-then-check, faithful to v1's
// object_refresh: a single loitering object owns the tile; if it has no
// refresh rows the actor gets nothing (the resolver does NOT skip past a
// refresh-less object to a refresh-bearing one farther away). Returns
// ("", nil) when no object owns the tile or the resolved one has no rows.
func findRefreshObjectNear(w *World, actorTile TilePos) (VillageObjectID, *VillageObject) {
	id, ok := resolveLoiteringObject(w, actorTile, LoiterAttributionTiles)
	if !ok {
		return "", nil
	}
	obj := w.VillageObjects[id]
	if obj == nil || len(obj.Refreshes) == 0 {
		return "", nil
	}
	return id, obj
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
		// Regrowth may have restocked a bush from empty — recompute its
		// berries/bare visual so berries reappear once supply is back.
		refreshObjectBerryState(w, obj)
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
			w.beatTicker("object_refresh_regen")
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
