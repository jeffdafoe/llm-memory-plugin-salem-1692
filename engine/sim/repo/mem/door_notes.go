package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// DoorNotesRepo is an in-memory sim.DoorNotesRepo (LLM-19). SaveSnapshot
// mirrors whatever the checkpoint hands it; LoadUnread returns the notes not
// yet read, matching the pg tier.
type DoorNotesRepo struct {
	notes map[sim.DoorNoteID]*sim.DoorNote
}

func NewDoorNotesRepo() *DoorNotesRepo {
	return &DoorNotesRepo{notes: make(map[sim.DoorNoteID]*sim.DoorNote)}
}

func (r *DoorNotesRepo) Seed(notes map[sim.DoorNoteID]*sim.DoorNote) {
	for id, n := range notes {
		r.notes[id] = n.Clone()
	}
}

func (r *DoorNotesRepo) LoadUnread(_ context.Context) (map[sim.DoorNoteID]*sim.DoorNote, error) {
	out := make(map[sim.DoorNoteID]*sim.DoorNote)
	for id, n := range r.notes {
		if n.Unread() {
			out[id] = n.Clone()
		}
	}
	return out, nil
}

func (r *DoorNotesRepo) SaveSnapshot(_ context.Context, _ sim.Tx, notes map[sim.DoorNoteID]*sim.DoorNote) error {
	for id, n := range notes {
		if n != nil {
			r.notes[id] = n.Clone()
		}
	}
	return nil
}
