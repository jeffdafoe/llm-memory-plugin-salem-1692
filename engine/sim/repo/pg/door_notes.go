package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// DoorNotesRepo reads and writes door_note (LLM-19): the notes players left at
// doors nobody answered. Upsert-only inside the checkpoint Tx, like
// CourtCasesRepo — a read note keeps its row with read_at set; only unread rows
// come back at boot.
type DoorNotesRepo struct {
	pool Pool
}

// NewDoorNotesRepo constructs a DoorNotesRepo. Normal wiring is pg.NewRepository.
func NewDoorNotesRepo(pool Pool) *DoorNotesRepo {
	return &DoorNotesRepo{pool: pool}
}

const loadUnreadDoorNotesSQL = `
SELECT id, structure_id, recipient_actor_id, author_actor_id, author_name, body, left_at
  FROM door_note
 WHERE read_at IS NULL`

const upsertDoorNoteSQL = `
INSERT INTO door_note (id, structure_id, recipient_actor_id, author_actor_id, author_name, body, left_at, read_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    recipient_actor_id = EXCLUDED.recipient_actor_id,
    author_name        = EXCLUDED.author_name,
    body               = EXCLUDED.body,
    left_at            = EXCLUDED.left_at,
    read_at            = EXCLUDED.read_at`

const advisoryLockDoorNotesSQL = `SELECT pg_advisory_xact_lock(hashtext('door_note_snapshot'), 0)`

// LoadUnread loads every note still waiting on a door.
func (r *DoorNotesRepo) LoadUnread(ctx context.Context) (map[sim.DoorNoteID]*sim.DoorNote, error) {
	out := make(map[sim.DoorNoteID]*sim.DoorNote)
	rows, err := r.pool.Query(ctx, loadUnreadDoorNotesSQL)
	if err != nil {
		return nil, fmt.Errorf("pg door_notes LoadUnread query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, structureID, recipient, author, authorName, body string
			leftAt                                               time.Time
		)
		if err := rows.Scan(&id, &structureID, &recipient, &author, &authorName, &body, &leftAt); err != nil {
			return nil, fmt.Errorf("pg door_notes LoadUnread scan: %w", err)
		}
		out[sim.DoorNoteID(id)] = &sim.DoorNote{
			ID:          sim.DoorNoteID(id),
			StructureID: sim.StructureID(structureID),
			RecipientID: sim.ActorID(recipient),
			AuthorID:    sim.ActorID(author),
			AuthorName:  authorName,
			Body:        body,
			LeftAt:      leftAt,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg door_notes LoadUnread iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot upserts every note inside the checkpoint Tx. No sweep.
func (r *DoorNotesRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, notes map[sim.DoorNoteID]*sim.DoorNote) error {
	if tx == nil {
		return fmt.Errorf("pg door_notes SaveSnapshot: nil tx")
	}
	if len(notes) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, advisoryLockDoorNotesSQL); err != nil {
		return fmt.Errorf("pg door_notes SaveSnapshot: advisory lock: %w", err)
	}
	for id, n := range notes {
		if n == nil {
			continue
		}
		if n.ID != id || strings.TrimSpace(string(n.ID)) == "" {
			return fmt.Errorf("pg door_notes SaveSnapshot: bad note id %q (key %q)", n.ID, id)
		}
		var readAt any
		if !n.ReadAt.IsZero() {
			readAt = n.ReadAt
		}
		if _, err := tx.Exec(ctx, upsertDoorNoteSQL,
			string(n.ID),
			string(n.StructureID),
			string(n.RecipientID),
			string(n.AuthorID),
			n.AuthorName,
			n.Body,
			n.LeftAt,
			readAt,
		); err != nil {
			return fmt.Errorf("pg door_notes SaveSnapshot: upsert id=%s: %w", n.ID, err)
		}
	}
	return nil
}
