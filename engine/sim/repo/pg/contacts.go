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

// deleteStaleContactsSQL removes pairs whose whole trail has aged past the
// recall horizon — the table's only reclamation path.
//
// It exists because "bounded by actor pairs" is FALSE over time: a transient
// visitor gets a fresh vstr-<8hex> ActorID every visit and this ledger covers
// visitors on purpose, so the set of pairs that have EVER existed grows without
// limit even though the set of live actors does not. Load-time pruning drops
// those from memory but never from Postgres, which would leave the table (and
// the full scan every boot does over it) growing forever.
//
// max(unnest) rather than indexing the last element: the trail is written
// ordered, but a stored array is data from outside this process and a wrong
// order here would delete live rows rather than merely render oddly. An empty
// or NULL array has no max, so `< $1` is NULL and the row is left alone — those
// are never written (SaveSnapshot skips empty trails) and are not this
// statement's business to clean up.
const deleteStaleContactsSQL = `
DELETE FROM actor_contact
 WHERE (SELECT max(t) FROM unnest(contact_at) AS t) < $1`

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

// SaveSnapshot upserts the flattened in-memory ledger inside the checkpoint Tx,
// then deletes pairs whose whole trail has aged past `staleBefore`.
//
// The delete runs AFTER the upserts and in the same Tx, so a pair refreshed by
// this very checkpoint is judged on its new trail rather than its old one.
//
// It is not a generation-marker sweep — nothing is marked, and it removes only
// rows already dead by the horizon rule the loader applies. Skipped on a zero
// cutoff (no clock established), which is the honest reading of "I don't know
// what time it is": deleting nothing is always safe, deleting against a zero
// cutoff would be too, but skipping keeps the intent legible.
//
// Substrate-boundary validation rejects an empty or self-referential pair so an
// upstream bug surfaces on the failing checkpoint rather than silently
// persisting a row the CHECK constraints would reject anyway.
func (r *ContactsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, pairs []sim.ContactPair, staleBefore time.Time) error {
	if tx == nil {
		return fmt.Errorf("pg contacts SaveSnapshot: nil tx")
	}
	if len(pairs) == 0 && staleBefore.IsZero() {
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
	if !staleBefore.IsZero() {
		if _, err := tx.Exec(ctx, deleteStaleContactsSQL, staleBefore); err != nil {
			return fmt.Errorf("pg contacts SaveSnapshot: delete stale before %s: %w", staleBefore, err)
		}
	}
	return nil
}
