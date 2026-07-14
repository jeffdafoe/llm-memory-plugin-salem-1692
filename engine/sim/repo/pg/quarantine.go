package pg

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// quarantine.go — how a pg writer reaches the checkpoint's quarantine
// (LLM-392). The policy and the reasoning live on sim.Quarantine; this file
// is only the plumbing.

// checkpointTx is the sim.Tx SaveWorld hands to every SaveSnapshot: the real
// transaction, plus the quarantine that collects the rows this checkpoint
// could not persist.
//
// Riding on the Tx (rather than adding a parameter to SaveSnapshot) is
// deliberate. The quarantine's lifetime IS the checkpoint transaction's
// lifetime, so the Tx is the honest place to hang it — and it keeps the
// sim.Repository interface, every mem-repo impl, and every test fake
// untouched. The embedded sim.Tx satisfies the interface, so a writer that
// never asks about quarantine (and every non-checkpoint caller, which gets a
// plain Tx) behaves exactly as before.
type checkpointTx struct {
	sim.Tx
	q *sim.Quarantine
}

// quarantineOf extracts the quarantine from a checkpoint Tx.
//
// It NEVER returns nil. A nil quarantine would make this mechanism FAIL OPEN,
// which is strictly worse than the bug it replaces: `Drop` would be a silent
// no-op, so `Dropped` would report false and `SweepBlocked` would report false
// — yet the writers would still leave the bad row out of the write set. The row
// would then keep its old snapshot_gen, the sweep would run unblocked, and the
// row we meant to PRESERVE would be DELETED, silently, with no alarm. Before
// LLM-392 that same row returned a loud error. So a writer handed a plain Tx
// gets a throwaway quarantine and stays internally consistent (drops recorded,
// sweeps blocked, rows preserved); only the report is discarded.
//
// Both the pointer and value forms are matched. checkpointTx embeds the sim.Tx
// INTERFACE, so its methods are promoted into the VALUE's method set too — a
// by-value checkpointTx satisfies sim.Tx and would silently miss a
// pointer-only type assertion, with no compiler warning.
func quarantineOf(tx sim.Tx) *sim.Quarantine {
	switch t := tx.(type) {
	case *checkpointTx:
		if t.q != nil {
			return t.q
		}
	case checkpointTx:
		if t.q != nil {
			return t.q
		}
	}
	return &sim.Quarantine{}
}

// execSweep runs a gen-marker stale-row sweep (`DELETE FROM t WHERE
// snapshot_gen < $1`) UNLESS this checkpoint dropped a row from that table,
// in which case the sweep is skipped for the cycle.
//
// The skip is not an optimization, it is a correctness requirement. A dropped
// row was not re-upserted, so its existing durable row still carries the OLD
// gen — precisely what the sweep deletes. Sweeping would therefore DELETE the
// row we merely declined to update, converting "this row is one checkpoint
// stale" into "this row is gone", which for an actor or a structure is far
// worse than the staleness we were trying to contain.
//
// The cost is that the table also retains rows for entities that legitimately
// departed during the cycle, and keeps retaining them for as long as the
// offending world state persists (see the sim.Quarantine header — this is why
// quarantine alarms). Skipping is scoped to the one physical table with the
// drop; a drop in actor_need does not stop actor or actor_inventory from
// sweeping.
//
// A dropped PARENT blocks its child tables explicitly, via q.BlockSweep — see
// dropActor / dropStructure / dropHuddle / dropScene / dropVillageObject. Do
// NOT remove those calls on the assumption that a child-row drop will cover it:
// a dropped parent's children are simply never visited by the write loops, so
// they record no drops of their own, and nothing would make this guard fire on
// their tables. Without the explicit block, dropping an actor keeps her `actor`
// row while her needs and inventory are swept out from under her.
func execSweep(ctx context.Context, tx sim.Tx, table, sql string, args ...any) error {
	q := quarantineOf(tx)
	if q.SweepBlocked(table) {
		q.SweepSkipped(table)
		log.Printf("sim/checkpoint: quarantine — skipping stale-row sweep on %s (a row was dropped this checkpoint; departed rows may be retained)", table)
		return nil
	}
	_, err := tx.Exec(ctx, sql, args...)
	return err
}

// childID builds the quarantine key for a child row of an owning entity —
// "<owner>/<key>". The write loops rebuild the same string to ask whether the
// pre-pass dropped that row (q.Dropped), so the two must agree exactly; that is
// the only reason this is a shared helper rather than an inline Sprintf. Every
// aggregate with child rows must route BOTH the drop and the check through it —
// a hand-rolled Sprintf on one side and a helper on the other is precisely how
// the two drift.
func childID[T ~string](owner T, key string) string {
	return string(owner) + "/" + key
}

// roomAccessID builds the quarantine key for a room_access row.
//
// Source is part of the key even though it is NOT part of the table's PK
// (room_id, actor_id). That is deliberate: Actor.RoomAccess is keyed by
// {RoomID, Source}, so one actor can hold two in-memory grants for the same
// room under different sources. The pre-pass keeps the first and drops the
// second — and if both minted the same quarantine key, the write loop's
// q.Dropped check would match the SURVIVOR too and skip it, writing neither.
// (structure_room hit the identical trap and solved it with position keys.)
func roomAccessID(room sim.RoomID, actor sim.ActorID, source sim.RoomAccessSource) string {
	return fmt.Sprintf("%d/%s/%s", room, actor, source)
}

// roomRowID is the quarantine key for a structure_room row AS SEEN FROM ANOTHER
// AGGREGATE — the raw room id.
//
// Structures keys its own room drops by slice POSITION (structureRoomID), which
// is right for its write loop but useless to the actors writer, which only
// knows a RoomID. So a room that will have NO durable row this checkpoint is
// ALSO recorded under this id-shaped key, letting the room_access writer skip a
// grant whose room was quarantined. Distinct key strings in the same table, so
// the two never collide.
func roomRowID(room sim.RoomID) string {
	return fmt.Sprintf("%d", room)
}

// ledgerRowID is the quarantine key for a pay_ledger row.
//
// This is the ONE key that crosses an aggregate boundary — Orders mints the
// drop, and the Actors room_access writer reads it back to cascade the drop to
// the grant that ledger row paid for — which makes it the key least able to
// afford a drift, and the one whose drift is SILENT (Postgres accepts a grant
// bound to a stale ledger row; only Go can see it is wrong).
//
// It bridges three Go types for the SAME number: Orders holds a sim.OrderID
// (uint64), pay_ledger.id is a sim.LedgerID (uint64), and RoomAccess.LedgerID is
// a plain int64. Routing them all through one helper is the point — a
// hand-rolled Sprintf on each side is how a uint64 and an int64 silently stop
// producing the same key.
func ledgerRowID[T ~uint64 | ~int64](id T) string {
	return fmt.Sprintf("%d", id)
}

// clampInt forces v into [lo, hi]. The checkpoint clamps an out-of-range value
// rather than dropping the row that carries it (see sim.Quarantine): losing an
// actor because one need overshot its column range would be a wildly
// disproportionate response to an arithmetic bug elsewhere.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sortedRoomKeys returns an actor's room-access keys in a stable order. Map
// order is random; when two grants collide on the same room, the one written
// first is the one that survives, so an unsorted iteration would let the
// durable winner flap between checkpoints.
func sortedRoomKeys(m map[sim.RoomAccessKey]*sim.RoomAccess) []sim.RoomAccessKey {
	out := make([]sim.RoomAccessKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RoomID != out[j].RoomID {
			return out[i].RoomID < out[j].RoomID
		}
		return out[i].Source < out[j].Source
	})
	return out
}
