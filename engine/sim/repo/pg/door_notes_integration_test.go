package pg

// Real-pg integration tests for door notes (LLM-19): notes round-trip through
// SaveWorld → LoadWorld against the real door_note columns and CHECKs, a read
// note keeps its row but does not come back at boot, and a replaced note is
// updated in place.

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestIntegration_DoorNotes_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	w := checkpointableWorld(repo)
	w.DoorNotes = map[sim.DoorNoteID]*sim.DoorNote{
		"note-0000aa01": {
			ID: "note-0000aa01", StructureID: "walker-home", RecipientID: "silence", AuthorID: "pc-jeff",
			AuthorName: "Jefferey", Body: "I came about the bread.", LeftAt: now.Add(-3 * time.Hour),
		},
		"note-0000aa02": {
			ID: "note-0000aa02", StructureID: "ward-home", RecipientID: "prudence", AuthorID: "pc-jeff",
			AuthorName: "Jefferey", Body: "The tincture, if you please.", LeftAt: now.Add(-5 * time.Hour),
		},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}
	// Prudence reads hers; the bread note is replaced in place.
	w.DoorNotes["note-0000aa02"].ReadAt = now
	w.DoorNotes["note-0000aa01"].Body = "I came about the bread. I will be at the Inn tonight."
	w.DoorNotes["note-0000aa01"].LeftAt = now
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("second SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if len(loaded.DoorNotes) != 1 {
		t.Fatalf("loaded %d notes, want only the unread one", len(loaded.DoorNotes))
	}
	n := loaded.DoorNotes["note-0000aa01"]
	if n == nil || !n.Unread() || n.StructureID != "walker-home" || n.RecipientID != "silence" || n.AuthorID != "pc-jeff" ||
		n.AuthorName != "Jefferey" || n.Body != "I came about the bread. I will be at the Inn tonight." || !n.LeftAt.Equal(now) {
		t.Errorf("unread note = %+v", n)
	}
	var readAt *time.Time
	if err := f.Pool.QueryRow(ctx, `SELECT read_at FROM door_note WHERE id = 'note-0000aa02'`).Scan(&readAt); err != nil {
		t.Fatalf("read the read note's row: %v", err)
	}
	if readAt == nil || !readAt.Equal(now) {
		t.Errorf("read note's read_at = %v, want %v", readAt, now)
	}
}

func TestIntegration_DoorNotes_ChecksRefuseABadNote(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for label, q := range map[string]string{
		"bad id":     `INSERT INTO door_note (id, structure_id, recipient_actor_id, author_actor_id, author_name, body, left_at) VALUES ('n1', 's', 'r', 'a', 'A', 'hello', now())`,
		"empty body": `INSERT INTO door_note (id, structure_id, recipient_actor_id, author_actor_id, author_name, body, left_at) VALUES ('note-0000bb01', 's', 'r', 'a', 'A', '', now())`,
		"long body":  `INSERT INTO door_note (id, structure_id, recipient_actor_id, author_actor_id, author_name, body, left_at) VALUES ('note-0000bb02', 's', 'r', 'a', 'A', '` + strings.Repeat("x", 201) + `', now())`,
	} {
		if _, err := f.Pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: insert succeeded, want a CHECK violation", label)
		}
	}
}
