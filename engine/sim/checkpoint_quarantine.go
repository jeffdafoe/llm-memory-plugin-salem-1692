package sim

import (
	"fmt"
	"sort"
	"strings"
)

// checkpoint_quarantine.go — the checkpoint's row-level quarantine (LLM-392).
//
// # Why this exists
//
// On 2026-07-12 the village ran 17.5 hours with ZERO durability. One
// pay_ledger row violated a partial unique index; Orders.SaveSnapshot
// returned that error; SaveWorld rolled the whole Tx back. Every checkpoint
// for the next 17.5 hours re-failed on the same row — the snapshot state
// never changed, so the failure never cleared. The restart then loaded a
// 17.5-hour-old checkpoint and silently rolled back every actor's position,
// coins, inventory and orders.
//
// The bug was not "SaveWorld is one transaction". The bug was that the
// checkpoint could be VETOED BY THE CONTENT OF WORLD STATE at all. The pg
// writers carried ~85 validation checks that each returned an error — an
// actor with a need value of 25, an empty DisplayName, a half-set schedule —
// and EVERY one of them aborted the entire checkpoint for every aggregate.
// One bad field on one actor was a total, permanent, silent durability
// outage for the whole village.
//
// # The rule
//
// A checkpoint must never be vetoable by world-state content. Persisting is
// not the place to enforce correctness: validation's job is to REPORT, not to
// REFUSE. So a row the writers judge unpersistable is quarantined — dropped
// from this checkpoint, recorded here, and screamed about (the
// checkpoint_quarantine alarm) — while every other row commits normally.
//
// The transaction itself is UNCHANGED: still one Tx, still one commit, still
// all-or-nothing. Nothing inside it can fail on row content anymore, because
// the bad rows never reach SQL. That is the whole point of quarantining in Go
// rather than reaching for per-row SAVEPOINTs: the pre-pass knows what a row
// MEANS — its identity, its parent, its aggregate — where a SQL-level
// savepoint only knows that Postgres said no.
//
// # What a quarantined checkpoint actually commits
//
// Be precise about this, because it is NOT "the snapshot minus a row":
//
//   - every non-quarantined row, at the current snapshot — normal;
//   - the PREVIOUS durable version of each quarantined row — because the
//     gen-marker sweep is skipped for any table that has a drop (otherwise
//     `DELETE ... WHERE snapshot_gen < $1` would delete the very row we
//     declined to rewrite, turning "one stale row" into "one DELETED row");
//   - and, as a consequence of that skipped sweep, rows for entities that
//     departed the world during the same cycle, which would normally have
//     been swept.
//
// So the committed state is a DEGRADED HYBRID with bounded stale retention,
// not a clean point-in-time image. It is a legitimate durable state — every
// DB constraint holds, and a reload produces a working world — but it is not
// a healthy one, and it stays degraded until a clean checkpoint succeeds.
// Nor is the staleness one-cycle-bounded: if the offending world state
// persists (as it did for 17.5 hours), every checkpoint quarantines it again
// and the unswept rows accumulate for as long as it lasts. That is the cost
// of not losing everything, and it is why quarantine ALARMS rather than
// merely logging.
//
// # Clamp vs drop
//
// A drop is the last resort, not the first. Where a value is merely out of
// the column's legal range (a need of 25 on a [0,24] column, a schedule
// minute past 1439), the writers CLAMP it and record the clamp: losing an
// actor's whole row because one need overshot would be a wildly
// disproportionate response to an arithmetic bug. Rows are dropped only when
// they cannot be meaningfully written at all — an empty identity, an empty
// FSM state, a row that would violate a cross-row invariant. Both outcomes
// are recorded and both raise the alarm; the world is wrong either way and
// somebody needs to know.
//
// # Parent drops take their children
//
// Dropping a parent row does NOT let its children through. Postgres would
// often accept them — the parent's PREVIOUS row is still there, so the FK
// holds — and the result would be fresh children pinned to a stale parent: a
// cross-generation hybrid inside a single aggregate, which is exactly the
// persistent inconsistency the atomic Tx exists to prevent. So a dropped
// parent is recorded here and every child writer consults Dropped() before
// writing. This is also load-bearing for village_object, whose writer runs a
// hard orphan check (fresh child + stale parent => error) that would
// otherwise abort the whole checkpoint.

// QuarantinedRow is one row a checkpoint refused to persist, or persisted
// only after clamping a value into range. Table and ID identify it for an
// operator; Reason is a plain-English sentence because the reader is the
// person holding the alarm at 3am, not a parser.
type QuarantinedRow struct {
	Table   string `json:"table"`
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Clamped bool   `json:"clamped,omitempty"`
}

// Quarantine collects the rows one checkpoint could not persist cleanly.
//
// One Quarantine belongs to one SaveWorld call and is used only by that
// call's goroutine (the checkpointer runs writes sequentially, off the world
// goroutine), so it carries no lock. Every method is nil-safe: a writer
// reached outside a checkpoint (WriteTerminal and friends run against the
// pool with no quarantine attached) calls Drop/Dropped on a nil *Quarantine
// and gets a no-op, so the non-checkpoint write paths need no special-casing.
type Quarantine struct {
	rows []QuarantinedRow
	// ids indexes drops by table -> id, for the Dropped() lookups child
	// writers use to skip a dropped parent's rows. Clamps are NOT indexed
	// here: a clamped row is still written, so nothing downstream should
	// skip it.
	ids map[string]map[string]bool
	// blocked holds tables whose sweep must be skipped even though no row of
	// THEIR own was dropped — the child tables of a dropped parent. See
	// BlockSweep.
	blocked map[string]bool
	// sweeps records the tables whose gen-marker sweep was skipped this
	// checkpoint. Derived from the drops (any table with a drop skips its
	// sweep), but recorded explicitly because the operator needs to know
	// which tables may now be retaining departed rows.
	sweeps map[string]bool
}

// Drop records that a row was left out of this checkpoint entirely. The row
// keeps whatever version it already had in Postgres (its table's sweep is
// skipped, so the old row survives), or has no durable row at all if it was
// new. Nil-safe.
func (q *Quarantine) Drop(table, id, reason string) {
	if q == nil {
		return
	}
	q.rows = append(q.rows, QuarantinedRow{Table: table, ID: id, Reason: reason})
	if q.ids == nil {
		q.ids = make(map[string]map[string]bool)
	}
	if q.ids[table] == nil {
		q.ids[table] = make(map[string]bool)
	}
	q.ids[table][id] = true
}

// Clamp records that a row WAS persisted, but only after forcing a value back
// into its column's legal range. Unlike Drop this does not mark the row (or
// its table's sweep) as skipped — the row is present and current, just
// corrected. Nil-safe.
func (q *Quarantine) Clamp(table, id, reason string) {
	if q == nil {
		return
	}
	q.rows = append(q.rows, QuarantinedRow{Table: table, ID: id, Reason: reason, Clamped: true})
}

// Dropped reports whether this exact row was dropped. Child writers call it
// with their PARENT's table and id to skip children of a dropped parent; the
// room_access writer calls it with ("pay_ledger", ledgerID) to skip a grant
// whose ledger row was dropped. Nil-safe (false).
func (q *Quarantine) Dropped(table, id string) bool {
	if q == nil || q.ids == nil {
		return false
	}
	return q.ids[table][id]
}

// BlockSweep marks a table's sweep unsafe for this checkpoint WITHOUT
// recording a row drop against that table.
//
// This is what a dropped PARENT does to its child tables. Dropping an actor
// removes it from the write set, so its child rows (needs, inventory, room
// access …) are never re-upserted and keep their old gen — exactly what their
// sweeps delete. Without this, dropping one actor would leave its parent row
// intact (that table's sweep is skipped) while its needs and inventory were
// swept out from under it: a half-erased actor, which is worse than either
// keeping or dropping it whole. The child tables have no drop of their own to
// trigger the guard, so the parent blocks them explicitly. Nil-safe.
func (q *Quarantine) BlockSweep(table string) {
	if q == nil {
		return
	}
	if q.blocked == nil {
		q.blocked = make(map[string]bool)
	}
	q.blocked[table] = true
}

// SweepBlocked reports whether this table's gen-marker sweep must be skipped —
// because a row of its own was dropped, or because a parent drop blocked it.
// Nil-safe (false).
func (q *Quarantine) SweepBlocked(table string) bool {
	if q == nil {
		return false
	}
	if len(q.ids[table]) > 0 {
		return true
	}
	return q.blocked[table]
}

// SweepSkipped records that a table's stale-row sweep was skipped this
// checkpoint, so the health report can name the tables that may now be
// retaining departed rows. Nil-safe.
func (q *Quarantine) SweepSkipped(table string) {
	if q == nil {
		return
	}
	if q.sweeps == nil {
		q.sweeps = make(map[string]bool)
	}
	q.sweeps[table] = true
}

// Clean reports whether this checkpoint was fully healthy: nothing dropped,
// nothing clamped, no sweep skipped. Only a clean checkpoint clears the
// quarantine alarm. Nil-safe (true — no quarantine attached means nothing
// went wrong).
func (q *Quarantine) Clean() bool {
	if q == nil {
		return true
	}
	return len(q.rows) == 0 && len(q.sweeps) == 0
}

// Rows returns the quarantined rows in the order they were recorded. Nil-safe.
func (q *Quarantine) Rows() []QuarantinedRow {
	if q == nil {
		return nil
	}
	return q.rows
}

// SkippedSweeps returns the tables whose sweep was skipped, sorted for a
// stable report. Nil-safe.
func (q *Quarantine) SkippedSweeps() []string {
	if q == nil || len(q.sweeps) == 0 {
		return nil
	}
	out := make([]string, 0, len(q.sweeps))
	for t := range q.sweeps {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Summary renders the quarantine as one log line — the line an operator greps
// for in journalctl. Empty string when clean. Caps the per-row detail so a
// pathological checkpoint (a schema bug dropping hundreds of rows) can't flood
// the journal with one line per row.
func (q *Quarantine) Summary() string {
	if q.Clean() {
		return ""
	}
	const maxDetail = 5
	var b strings.Builder
	dropped, clamped := 0, 0
	for _, r := range q.Rows() {
		if r.Clamped {
			clamped++
			continue
		}
		dropped++
	}
	fmt.Fprintf(&b, "%d row(s) dropped, %d clamped", dropped, clamped)
	if sweeps := q.SkippedSweeps(); len(sweeps) > 0 {
		fmt.Fprintf(&b, "; sweep skipped on %s (departed rows may be retained)", strings.Join(sweeps, ", "))
	}
	shown := 0
	for _, r := range q.Rows() {
		if shown == maxDetail {
			fmt.Fprintf(&b, "; +%d more", len(q.Rows())-shown)
			break
		}
		verb := "DROPPED"
		if r.Clamped {
			verb = "CLAMPED"
		}
		fmt.Fprintf(&b, "; %s %s id=%s: %s", verb, r.Table, r.ID, r.Reason)
		shown++
	}
	return b.String()
}
