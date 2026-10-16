package perception

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// door_notes.go — LLM-19 perception: a note a player left at a door nobody
// answered. The recipient finds it on their first turn at or inside that house
// (inside it, or outdoors at its door) — found on the door, not carried to them
// elsewhere. ShownDoorNotes hands the harness what the prompt showed, and
// CompleteReactorTick marks those read once the turn answered. No tool: the
// villager acts on it with what they already have.

// DoorNoteView is one note as its recipient finds it.
type DoorNoteView struct {
	ID         sim.DoorNoteID
	AuthorName string
	Body       string
	LeftAt     time.Time
}

// doorNoteBodyMaxBytes bounds a note in the prompt. A note is at most
// sim.MaxDoorNoteChars runes; four bytes a rune covers any of them.
const doorNoteBodyMaxBytes = sim.MaxDoorNoteChars * 4

// buildDoorNotes returns the unread notes addressed to the subject at the
// house they are in or standing at the door of, oldest first; nil when none.
func buildDoorNotes(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) []DoorNoteView {
	if snap == nil || actorSnap == nil || len(snap.DoorNotes) == 0 {
		return nil
	}
	var out []DoorNoteView
	for _, n := range snap.DoorNotes {
		if !n.Unread() || n.RecipientID != actorID {
			continue
		}
		atIt := actorSnap.InsideStructureID == n.StructureID ||
			sim.AtDoorOf(snap.VillageObjects, snap.Assets, actorSnap.InsideStructureID, actorSnap.Pos, n.StructureID)
		if !atIt {
			continue
		}
		out = append(out, DoorNoteView{ID: n.ID, AuthorName: n.AuthorName, Body: n.Body, LeftAt: n.LeftAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LeftAt.Equal(out[j].LeftAt) {
			return out[i].LeftAt.Before(out[j].LeftAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ShownDoorNotes lists the notes the payload shows, each with the version it
// shows — what the turn read.
func ShownDoorNotes(p Payload) []sim.DoorNoteSeen {
	if len(p.DoorNotes) == 0 {
		return nil
	}
	seen := make([]sim.DoorNoteSeen, 0, len(p.DoorNotes))
	for _, n := range p.DoorNotes {
		seen = append(seen, sim.DoorNoteSeen{ID: n.ID, LeftAt: n.LeftAt})
	}
	return seen
}

// doorNoteAge says how long ago a note was left, as the reader would put it.
func doorNoteAge(leftAt, now time.Time) string {
	d := now.Sub(leftAt)
	switch {
	case leftAt.IsZero() || now.IsZero() || d < 20*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return "a little while ago"
	case d < 18*time.Hour:
		return "some hours ago"
	case d < 36*time.Hour:
		return "a day ago"
	}
	days := int(d / (24 * time.Hour))
	words := []string{"", "", "two", "three", "four", "five", "six"}
	if days < len(words) {
		return words[days] + " days ago"
	}
	return "many days ago"
}

// renderDoorNotes writes "## A note at your door". Content-gated. The author's
// name stays exact so the reader can find and address them.
func renderDoorNotes(b *strings.Builder, notes []DoorNoteView, now time.Time) {
	if len(notes) == 0 {
		return
	}
	if len(notes) == 1 {
		b.WriteString("## A note at your door\n")
	} else {
		b.WriteString("## Notes at your door\n")
	}
	for _, n := range notes {
		body, _ := sanitizeText(n.Body, doorNoteBodyMaxBytes)
		fmt.Fprintf(b, "A note addressed to you is tucked in the door, from %s, left %s: \"%s\"\n",
			sanitizeInline(n.AuthorName), doorNoteAge(n.LeftAt, now), body)
	}
	b.WriteString("\n")
}
