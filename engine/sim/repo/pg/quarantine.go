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

// quarantineOf extracts the quarantine from a checkpoint Tx. Returns nil for
// a plain Tx — the non-checkpoint write paths (WriteTerminal and friends run
// against the pool, outside any checkpoint) — and every sim.Quarantine method
// is nil-safe, so callers never branch on it.
func quarantineOf(tx sim.Tx) *sim.Quarantine {
	if ctx, ok := tx.(*checkpointTx); ok {
		return ctx.q
	}
	return nil
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
// pre-pass dropped that row (q.Dropped), so the two must agree exactly; that
// is the only reason this is a shared helper rather than an inline Sprintf.
func childID(owner sim.ActorID, key string) string {
	return string(owner) + "/" + key
}

// roomAccessID builds the quarantine key for a room_access row. Keyed on the
// table's real PK, (room_id, actor_id) — NOT on the in-memory RoomAccessKey,
// whose Source is not part of the PK.
func roomAccessID(room sim.RoomID, actor sim.ActorID) string {
	return fmt.Sprintf("%d/%s", room, actor)
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
