package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ContactsRepo reads and writes actor_contact — the per-pair conversational
// recency trail (LLM-547): who each actor has already had its word with, and
// how lately.
//
// Same posture as RecurringVisitorsRepo, not the visitor mirror's: SaveSnapshot
// is a plain per-row UPSERT with NO generation-marker delete-stale sweep. A pair
// that stops being written is not stale data to reclaim — it simply ages past
// the recall horizon and is dropped by RehydrateContactLedger at the next boot.
// That is the whole reason the trail can be stored as an array in one row.
//
// Written inside the caller's checkpoint Tx so the trail can never split from
// the actors it references across a crash. There is no cross-aggregate FK
// (deliberately — see the migration), so ordering relative to the other
// aggregates is free.
type ContactsRepo struct {
	pool Pool
}

// NewContactsRepo constructs a ContactsRepo against the given pool. Normal
// wiring is pg.NewRepository.
func NewContactsRepo(pool Pool) *ContactsRepo {
	return &ContactsRepo{pool: pool}
}

const loadContactsSQL = `
SELECT actor_id, peer_id, contact_at
  FROM actor_contact`

// upsertContactSQL replaces a pair's whole trail rather than appending to it.
// The in-memory ledger is authoritative — it has already pruned to the recall
// horizon and enforced the per-pair cap — so the checkpoint's job is to make the
// row match memory, not to accumulate alongside it. An append would let the
// column grow past the cap the engine maintains and would re-introduce entries
// memory had already dropped.
const upsertContactSQL = `
INSERT INTO actor_contact (actor_id, peer_id, contact_at)
VALUES ($1, $2, $3)
ON CONFLICT (actor_id, peer_id) DO UPDATE SET
    contact_at = EXCLUDED.contact_at`

// advisoryLockContactsSQL serializes concurrent checkpoint writers on this
// table, matching the recurring_visitor posture. Transaction-scoped, so it is
// released with the Tx whether it commits or rolls back.
const advisoryLockContactsSQL = `SELECT pg_advisory_xact_lock(hashtext('actor_contact_snapshot'), 0)`

// LoadAll reads every persisted pair. Read-only restart path off the pool, same
// posture as the other LoadAll implementations.
//
// Deliberately does NOT prune or validate here: the horizon cutoff needs a
// "now", and the caller (FinalizeLoad, via sim.RehydrateContactLedger) owns both
// the clock and the settings. Keeping this a dumb read means the pruning rule
// lives in exactly one place and is unit-testable without a database.
func (r *ContactsRepo) LoadAll(ctx context.Context) ([]sim.ContactPair, error) {
	rows, err := r.pool.Query(ctx, loadContactsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg contacts LoadAll query: %w", err)
	}
	defer rows.Close()

	var out []sim.ContactPair
	for rows.Next() {
		var (
			actorID string
			peerID  string
			at      []time.Time
		)
		if err := rows.Scan(&actorID, &peerID, &at); err != nil {
			return nil, fmt.Errorf("pg contacts LoadAll scan: %w", err)
		}
		out = append(out, sim.ContactPair{
			SubjectID: sim.ActorID(actorID),
			PeerID:    sim.ActorID(peerID),
			At:        at,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg contacts LoadAll iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot upserts the flattened in-memory ledger inside the checkpoint Tx.
//
// No delete-stale sweep, per the posture above. One consequence worth stating:
// a pair whose trail has aged out of memory keeps its stale row until the next
// boot drops it. Harmless — nothing but the boot load reads this table, and the
// boot load applies the horizon.
//
// Substrate-boundary validation rejects an empty or self-referential pair so an
// upstream bug surfaces on the failing checkpoint rather than silently
// persisting a row the CHECK constraints would reject anyway.
func (r *ContactsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, pairs []sim.ContactPair) error {
	if tx == nil {
		return fmt.Errorf("pg contacts SaveSnapshot: nil tx")
	}
	if len(pairs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, advisoryLockContactsSQL); err != nil {
		return fmt.Errorf("pg contacts SaveSnapshot: advisory lock: %w", err)
	}

	for _, p := range pairs {
		if p.SubjectID == "" || p.PeerID == "" {
			return fmt.Errorf("pg contacts SaveSnapshot: empty pair id (subject=%q peer=%q)", p.SubjectID, p.PeerID)
		}
		if p.SubjectID == p.PeerID {
			return fmt.Errorf("pg contacts SaveSnapshot: self-pair for %q", p.SubjectID)
		}
		if len(p.At) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, upsertContactSQL,
			string(p.SubjectID), // $1 actor_id
			string(p.PeerID),    // $2 peer_id
			p.At,                // $3 contact_at
		); err != nil {
			return fmt.Errorf("pg contacts SaveSnapshot: upsert %s→%s: %w", p.SubjectID, p.PeerID, err)
		}
	}
	return nil
}
