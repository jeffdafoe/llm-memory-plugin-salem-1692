package sim

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// checkpoint_clamp.go — keep ONE out-of-range number from taking the whole
// village's durability down with it (LLM-392).
//
// # The outage this exists to prevent
//
// On 2026-07-12 a single unpersistable pay_ledger row made SaveWorld roll back
// its transaction. The next checkpoint built the same snapshot and failed the
// same way, 60 seconds later, and again, for 17.5 hours. The village ran with
// zero durability the whole time; the shutdown checkpoint failed too, so the
// restart loaded a 17.5-hour-old image and silently rolled back every actor's
// position, coins, inventory and orders.
//
// The transaction is all-or-nothing on purpose — a half-written checkpoint is
// far worse than a missing one (shared GUIDELINES: persistent state must stay
// consistent; losing an hour of it is survivable, an inconsistent hour is not).
// That is not the defect. The defect is that a checkpoint could be VETOED BY A
// NUMBER. An actor whose hunger arithmetic overshot to 25 on a [0,24] column
// does not just fail to persist himself — he stops the village from persisting
// at all, forever, because the next snapshot contains him too.
//
// # What this does
//
// Before the snapshot reaches the durable writer, every scalar that has a legal
// range is projected onto it: 25 → 24, a negative quantity → 0, a zero period →
// 1. Each correction is recorded, and a non-empty report raises an alarm
// (checkpoint_clamped) on every umbilical response until a checkpoint runs clean.
//
// # Why this is a projection and not a lie
//
// The clamp is only ever applied to a value that CANNOT EXIST. There is no world
// in which an actor holds -3 bread or a need sits at 25 — those columns are
// bounded because the domain is bounded, and a value outside the bound is an
// arithmetic bug that has already corrupted the actor in memory. Writing the
// nearest legal value does not invent a state the world never had; it records
// the state the world MEANT, and screams about the bug that produced the other
// one. Refusing to persist anything at all, by contrast, does not make the bad
// value go away — it just adds an outage on top of it.
//
// # What is deliberately NOT clamped
//
// Anything that is not a bounded number. An empty FSM state, an empty
// DisplayName, a self-referencing relationship, a duplicate lodging ledger row,
// a malformed identity, a zeroed timestamp, an unknown enum with no safe default
// — none of these have a "nearest legal value", and guessing one would fabricate
// state rather than repair it. Those still FAIL the checkpoint, loudly and
// atomically, exactly as before: every validator in engine/sim/repo/pg stays
// where it is, and this pass is a filter in front of them, never a replacement.
// LLM-394's alarm surfaces such a failure within ~3 minutes. That is the trade
// this file draws: an impossible NUMBER is repaired and reported; an incoherent
// WORLD is refused.
//
// # Why it is safe to mutate the snapshot
//
// The CheckpointSnapshot is a private deep clone built by BuildCheckpointSnapshot
// on the world goroutine and owned by the checkpointer. Nothing else reads it and
// it is discarded after the write, so correcting a value here cannot touch live
// world state — and MUST not, since this runs off the world goroutine. That is a
// real constraint, not a theoretical one: CloneActor used to copy the two
// schedule *int fields by value from `cp := *a` without re-allocating them, so a
// clamp here would have written straight through the shared pointer into the live
// actor while the world goroutine was reading it. Fixed alongside this (CloneActor
// now copyIntPtr's them); every other field clamped below was already deep.

// maxRetainedClamps caps the per-checkpoint clamp detail. The COUNT is always
// exact — only the row-by-row list is capped. A world bug that corrupts one value
// tends to corrupt thousands (a decay loop that overshoots does it to every actor
// at once), and this report is pasted into operator responses.
//
// The retained 64 are a SAMPLE, not the first 64 of anything: they are whichever
// ones map iteration reached first, so they identify the SHAPE of the bug (which
// table, which field, what kind of value) while the count carries the true scale.
// Clamps() sorts what it kept, so the report reads stably once collected.
const maxRetainedClamps = 64

// defaultFacing mirrors the actor.facing column DEFAULT and validateFacing's
// existing coalesce for the empty string.
const defaultFacing = "south"

// Clamp is one correction: a field that held a value outside its legal range,
// and the nearest legal value written in its place. From/To are rendered as
// strings so the report serializes uniformly across int, string and pointer
// fields.
type Clamp struct {
	Table string `json:"table"`
	Field string `json:"field"`
	Key   string `json:"key"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ClampReport is what a checkpoint had to correct in order to be persistable at
// all. Empty is the healthy case and the overwhelmingly common one.
//
// No mutex: the whole clamp pass runs on the checkpoint goroutine, sequentially,
// against a snapshot nobody else holds. A nil *ClampReport is a no-op on every
// method, so a caller that does not care can pass nil.
type ClampReport struct {
	clamps []Clamp
	total  int
}

// record notes one correction. The count is always incremented; the detail is
// retained only up to maxRetainedClamps.
func (r *ClampReport) record(table, field, key string, from, to any) {
	if r == nil {
		return
	}
	r.total++
	if len(r.clamps) >= maxRetainedClamps {
		return
	}
	r.clamps = append(r.clamps, Clamp{
		Table: table,
		Field: field,
		Key:   key,
		From:  fmt.Sprint(from),
		To:    fmt.Sprint(to),
	})
}

// Total is the exact number of corrections, including any beyond the retained
// detail cap. Nil-safe.
func (r *ClampReport) Total() int {
	if r == nil {
		return 0
	}
	return r.total
}

// Clean reports whether the checkpoint needed no corrections at all. Nil-safe.
func (r *ClampReport) Clean() bool {
	return r.Total() == 0
}

// Clamps returns the retained corrections in a stable order (table, key, field).
// Sorted rather than map-iteration order so the operator-visible report and the
// tests do not shuffle between identical checkpoints. Nil-safe.
func (r *ClampReport) Clamps() []Clamp {
	if r == nil || len(r.clamps) == 0 {
		return nil
	}
	out := append([]Clamp(nil), r.clamps...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// Summary renders the report as one line for the log and the umbilical's
// checkpoint-health. Empty string when clean, so a caller can test it directly.
func (r *ClampReport) Summary() string {
	if r.Clean() {
		return ""
	}
	shown := r.Clamps()
	parts := make([]string, 0, len(shown))
	for _, c := range shown {
		parts = append(parts, c.Table+"."+c.Field+"["+c.Key+"] "+c.From+"→"+c.To)
	}
	out := strconv.Itoa(r.Total()) + " value(s) clamped to persist: " + strings.Join(parts, "; ")
	if r.Total() > len(shown) {
		out += "; and " + strconv.Itoa(r.Total()-len(shown)) + " more"
	}
	return out
}

// ClampToPersistable projects every bounded scalar in the snapshot onto its legal
// range, in place, and returns what it had to correct. Pure apart from that
// mutation: no I/O, no clock, no world access — so it is directly unit-testable
// against a hand-built snapshot.
//
// Call it on the checkpoint goroutine, after the clone and BEFORE the durable
// write. CheckpointNow does exactly that, which covers both the periodic loop and
// the shutdown checkpoint.
//
// The ranges below are the COLUMN's domain, not a guess: each is either an
// explicit CHECK constraint or the width of the integer type the value lands in.
// Where Go's int is wider than the column (SMALLINT, INTEGER), the upper bound
// matters as much as the lower — pgx will refuse to encode 40000 into a SMALLINT,
// and that refusal aborts the transaction just as surely as a CHECK violation.
func (cp *CheckpointSnapshot) ClampToPersistable() *ClampReport {
	r := &ClampReport{}
	if cp == nil {
		return r
	}
	clampActors(cp.Actors, r)
	clampOrders(cp.Orders, r)
	clampScenes(cp.Scenes, r)
	return r
}

// clampInt projects v onto [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampInt64 is clampInt for the one BIGINT-backed field (production remaining
// seconds), which Go holds as an int64.
func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampActors corrects the actor aggregate.
//
// Visitor actors are skipped, and the reason is worth stating: ActorsRepo's
// SaveSnapshot filters them out (VisitorState != nil), so a visitor's needs,
// inventory, relationships and schedule never reach actor_need / actor_inventory /
// actor_relationship / the actor row AT ALL. Clamping them would record — and
// alarm on — corrections to values that are never persisted, which is a false
// alarm on a surface whose whole worth is that it never cries wolf. The `visitor`
// row they DO get carries no CHECK-constrained numeric column, so there is nothing
// there to project either.
func clampActors(actors map[ActorID]*Actor, r *ClampReport) {
	for id, a := range actors {
		if a == nil || a.VisitorState != nil {
			continue
		}
		key := string(id)

		// actor.facing — CHECK (facing IN ('north','south','east','west')), column
		// DEFAULT 'south'. The one enum clamped rather than refused, because it
		// already HAS a no-information fallback that the codebase applies in two
		// other places: validateFacing coalesces "" to south on this very write
		// path, and httpapi's normalizeFacing does it on the read path. Facing is
		// the direction a sprite looks; there is no world invariant riding on it,
		// so a garbage value is worth a default and an alarm, not an outage.
		if a.Facing != "" && !isValidFacing(a.Facing) {
			r.record("actor", "facing", key, a.Facing, defaultFacing)
			a.Facing = defaultFacing
		}

		// actor.schedule_{start,end}_minute — SMALLINT, CHECK 0..1439 (minute of
		// day). Half-set schedules are NOT repaired here: which bound is missing is
		// unknowable, so that stays a hard failure in the writer.
		clampIntPtr(a.ScheduleStartMin, 0, 1439, "actor", "schedule_start_minute", key, r)
		clampIntPtr(a.ScheduleEndMin, 0, 1439, "actor", "schedule_end_minute", key, r)

		// actor_need.value — SMALLINT, CHECK 0..24. This is the exact shape the
		// ticket was written about: a decay tick that overshoots the ceiling.
		for k, v := range a.Needs {
			if c := clampInt(v, 0, 24); c != v {
				r.record("actor_need", "value", key+"/"+string(k), v, c)
				a.Needs[k] = c
			}
		}

		// actor_inventory.quantity — SMALLINT, CHECK (quantity > 0). Zero is legal
		// in memory and already means "no row": the writer skips a zero-quantity
		// entry and the generation sweep removes it. So a negative quantity clamps
		// to 0 — the actor holds none of the item, which is the only thing "-3
		// bread" can honestly mean — and the existing zero path does the rest.
		for kind, qty := range a.Inventory {
			if c := clampInt(qty, 0, math.MaxInt16); c != qty {
				r.record("actor_inventory", "quantity", key+"/"+string(kind), qty, c)
				a.Inventory[kind] = c
			}
		}

		// actor_inventory.uses_left — INTEGER, CHECK (uses_left IS NULL OR > 0).
		// Tool wear rides on the inventory row (LLM-330); there is no separate
		// table. A spent tool should have been removed from the map by
		// applyToolWear, so a non-positive entry is a mechanics bug: clamp to 1
		// (one use left — the nearest legal value to spent) rather than resurrect
		// it to full durability, which is what dropping the entry would do.
		for kind, wear := range a.ToolWear {
			if c := clampInt(wear, 1, math.MaxInt32); c != wear {
				r.record("actor_inventory", "uses_left", key+"/"+string(kind), wear, c)
				a.ToolWear[kind] = c
			}
		}

		// actor_relationship.{interaction_count,dropped_fact_count} — INTEGER, both
		// DEFAULT 0. No CHECK on the column; the writer rejects negatives because a
		// negative count is meaningless. Clamp to the column default.
		for peer, rel := range a.Relationships {
			if rel == nil {
				continue
			}
			rk := key + "/" + string(peer)
			if c := clampInt(rel.InteractionCount, 0, math.MaxInt32); c != rel.InteractionCount {
				r.record("actor_relationship", "interaction_count", rk, rel.InteractionCount, c)
				rel.InteractionCount = c
			}
			if c := clampInt(rel.DroppedFactCount, 0, math.MaxInt32); c != rel.DroppedFactCount {
				r.record("actor_relationship", "dropped_fact_count", rk, rel.DroppedFactCount, c)
				rel.DroppedFactCount = c
			}
		}

		// actor_dwell_credit — dwell_delta SMALLINT CHECK (< 0), dwell_period_minutes
		// INTEGER CHECK (> 0), remaining_ticks INTEGER CHECK (NULL OR > 0). The
		// remaining↔source PAIRING (item ⇒ set, object ⇒ null) is a shape rule, not
		// a range, so it is left to the writer to refuse.
		for dk, dc := range a.DwellCredits {
			if dc == nil {
				continue
			}
			ck := key + "/" + string(dk.ObjectID) + "/" + string(dk.Attribute)
			if c := clampInt(dc.DwellDelta, math.MinInt16, -1); c != dc.DwellDelta {
				r.record("actor_dwell_credit", "dwell_delta", ck, dc.DwellDelta, c)
				dc.DwellDelta = c
			}
			if c := clampInt(dc.DwellPeriodMinutes, 1, math.MaxInt32); c != dc.DwellPeriodMinutes {
				r.record("actor_dwell_credit", "dwell_period_minutes", ck, dc.DwellPeriodMinutes, c)
				dc.DwellPeriodMinutes = c
			}
			clampIntPtr(dc.RemainingTicks, 1, math.MaxInt32, "actor_dwell_credit", "remaining_ticks", ck, r)
		}

		// actor.production_{batch_qty,remaining_seconds} — INTEGER / BIGINT, no CHECK.
		// The writer refuses a non-positive cycle because the LOAD side reads
		// remaining <= 0 as "no cycle at all", so persisting one would silently
		// delete the actor's production on the next boot. Clamping to 1 keeps the
		// cycle and lets it complete on the next tick.
		if pa := a.ProductionActivity; pa != nil {
			if c := clampInt(pa.BatchQty, 1, math.MaxInt32); c != pa.BatchQty {
				r.record("actor", "production_batch_qty", key, pa.BatchQty, c)
				pa.BatchQty = c
			}
			if c := clampInt64(pa.RemainingSeconds, 1, math.MaxInt64); c != pa.RemainingSeconds {
				r.record("actor", "production_remaining_seconds", key, pa.RemainingSeconds, c)
				pa.RemainingSeconds = c
			}
		}
	}
}

// clampOrders corrects the orders aggregate — pay_ledger, the table the LLM-392
// outage actually happened on.
//
// The writer has NO range validation of its own here, so these values reach
// Postgres unguarded and a bad one trips the CHECK mid-transaction, which aborts
// the whole checkpoint from inside the Tx. That is the worst version of the
// failure: no Go-side error names the offending row, just a constraint name.
//
// The lodging double-book that caused the outage was a partial UNIQUE INDEX
// violation, NOT a range violation — it is not clamped and could not be (two
// rows are individually legal; only their coexistence is not). LLM-391 removed
// that specific poison at the source, and a recurrence still fails the checkpoint
// loudly under LLM-394's alarm. This pass closes the SCALAR doors on the same
// table, which were open too.
func clampOrders(orders map[OrderID]*Order, r *ClampReport) {
	for id, o := range orders {
		if o == nil {
			continue
		}
		key := strconv.FormatUint(uint64(id), 10)

		// pay_ledger.qty — INTEGER, CHECK (qty IS NULL OR qty > 0). The writer always
		// binds a value (never NULL), so the floor is 1: an order for zero goods is
		// not an order.
		if c := clampInt(o.Qty, 1, math.MaxInt32); c != o.Qty {
			r.record("pay_ledger", "qty", key, o.Qty, c)
			o.Qty = c
		}
		// pay_ledger.offered_amount — INTEGER, CHECK (>= 0).
		if c := clampInt(o.Amount, 0, math.MaxInt32); c != o.Amount {
			r.record("pay_ledger", "offered_amount", key, o.Amount, c)
			o.Amount = c
		}
		// pay_ledger.deposit_amount — INTEGER, CHECK (>= 0 AND <= offered_amount)
		// (LLM-357). The ceiling is the OTHER field, so this clamp must run after
		// offered_amount has been corrected or it could project onto a bound that is
		// itself about to move. A deposit above the price means the buyer was charged
		// more than the agreed total; the nearest legal reading is that they prepaid
		// it in full.
		if c := clampInt(o.Deposit, 0, o.Amount); c != o.Deposit {
			r.record("pay_ledger", "deposit_amount", key, o.Deposit, c)
			o.Deposit = c
		}
	}
}

// clampScenes corrects the scene aggregate: bound_radius INTEGER, CHECK (>= 0)
// as part of scene_bound_shape_valid.
//
// Worth noting that NewAreaBound already clamps a negative radius to 0 at
// construction — so the world's own constructor agrees this is the right
// projection. A negative radius reaching a snapshot means something bypassed it.
// The bound SHAPE rules (structure ⇒ no anchor/radius, area ⇒ both) are
// pairings, not ranges, and stay hard failures in the writer.
func clampScenes(scenes map[SceneID]*Scene, r *ClampReport) {
	for id, s := range scenes {
		if s == nil {
			continue
		}
		clampIntPtr(s.Bound.Radius, 0, math.MaxInt32, "scene", "bound_radius", string(id), r)
	}
}

// clampIntPtr projects the pointee onto [lo, hi] in place, if the pointer is set.
// Only ever called on a pointer the snapshot clone owns outright — see the
// deep-clone note in this file's header.
func clampIntPtr(p *int, lo, hi int, table, field, key string, r *ClampReport) {
	if p == nil {
		return
	}
	c := clampInt(*p, lo, hi)
	if c == *p {
		return
	}
	r.record(table, field, key, *p, c)
	*p = c
}

// isValidFacing reports whether facing is a member of the actor_facing_check
// enum. The empty string is NOT valid here — the writer's validateFacing
// coalesces it to the column default on its own, and clamping it would report a
// correction for what is really just an unset field.
func isValidFacing(facing string) bool {
	switch facing {
	case "north", "south", "east", "west":
		return true
	default:
		return false
	}
}
