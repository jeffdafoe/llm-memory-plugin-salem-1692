package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_door_note_test.go — LLM-19. A turn whose prompt showed a door note
// reports it on the TickResult (ShownDoorNotes), which CompleteReactorTick reads
// to mark it read; a note for someone else is not reported.
func TestHarness_ReportsShownDoorNotes(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()
	now := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].InsideStructureID = "alice-home"
		world.DoorNotes["note-0000aa01"] = &sim.DoorNote{ID: "note-0000aa01", StructureID: "alice-home", Revision: 2,
			RecipientID: "alice", AuthorID: "pc", AuthorName: "Jefferey", Body: "I came by.", LeftAt: now.Add(-time.Hour)}
		world.DoorNotes["note-0000aa02"] = &sim.DoorNote{ID: "note-0000aa02", StructureID: "alice-home",
			RecipientID: "someone-else", AuthorID: "pc", AuthorName: "Jefferey", Body: "Not yours.", LeftAt: now.Add(-time.Hour)}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed notes: %v", err)
	}
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c-term", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if len(result.ShownDoorNotes) != 1 || result.ShownDoorNotes[0].ID != "note-0000aa01" || result.ShownDoorNotes[0].Revision != 2 {
		t.Errorf("ShownDoorNotes = %v, want note-0000aa01 at revision 2", result.ShownDoorNotes)
	}
}
