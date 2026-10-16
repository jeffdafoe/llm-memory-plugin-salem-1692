package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// door_note_test.go — LLM-19. A knock nobody answers names who a note may be
// left for; the player leaves one at the door; a second note from them replaces
// the first; the note is marked read only when the recipient's turn answered.

// nameCottageFolk gives the membership fixture's villagers names, so the note
// recipients sort and read as they would live.
func nameCottageFolk(t *testing.T, w *sim.World) {
	t.Helper()
	mutate(t, w, func(world *sim.World) {
		world.Actors["homeowner"].DisplayName = "Homer Owen"
		world.Actors["spouse"].DisplayName = "Sarah Owen"
		world.Actors["servant"].DisplayName = "Sam Server"
		world.Actors["stranger"].DisplayName = "Jefferey"
	})
}

func leaveNote(w *sim.World, recipient sim.ActorID, text string, now time.Time) error {
	_, err := w.Send(sim.LeaveDoorNote("stranger", "cottage", recipient, text, now))
	return err
}

// doorNotes copies the world's notes out.
func doorNotes(t *testing.T, w *sim.World) []*sim.DoorNote {
	t.Helper()
	var out []*sim.DoorNote
	mutate(t, w, func(world *sim.World) {
		for _, n := range world.DoorNotes {
			out = append(out, n.Clone())
		}
	})
	return out
}

// pinnedEvents records every DoorNotePinned flag and every private
// DoorNoteLeft confirmation to the stranger.
func pinnedEvents(t *testing.T, w *sim.World) (flags *[]bool, told *int) {
	t.Helper()
	var f []bool
	var n int
	mutate(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
			switch ev := e.(type) {
			case *sim.DoorNotePinned:
				if ev.StructureID == "cottage" {
					f = append(f, ev.HasNote)
				}
			case *sim.DoorNoteLeft:
				if ev.ActorID == "stranger" && ev.StructureID == "cottage" {
					n++
				}
			}
		}))
	})
	return &f, &n
}

func TestKnockUnansweredNamesWhoANoteMayBeFor(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	playerAtCottageDoor(t, w, now)
	giveWritingKit(t, w, 5)
	out, err := knock(t, w, now)
	if err != nil || out.Answered {
		t.Fatalf("knock with nobody in: %+v %v, want unanswered", out, err)
	}
	// The resident and the owner, by name; the servant (who only works there)
	// is not offered while the house has people of its own.
	want := []sim.DoorNoteRecipient{{ID: "homeowner", Name: "Homer Owen"}, {ID: "spouse", Name: "Sarah Owen"}}
	if len(out.NoteFor) != len(want) || out.NoteFor[0] != want[0] || out.NoteFor[1] != want[1] {
		t.Errorf("NoteFor = %+v, want %+v", out.NoteFor, want)
	}
	// Answered: nobody to leave a note for.
	setInside(t, w, "servant", "cottage")
	if out, err := knock(t, w, now); err != nil || !out.Answered || len(out.NoteFor) != 0 {
		t.Errorf("answered knock: %+v %v, want answered with no note recipients", out, err)
	}
}

func TestLeaveDoorNoteAndReplace(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	flags, told := pinnedEvents(t, w)
	playerAtCottageDoor(t, w, now)
	giveWritingKit(t, w, 5)
	if err := leaveNote(w, "spouse", "  I came about the bread.  ", now); err != nil {
		t.Fatalf("leave a note: %v", err)
	}
	notes := doorNotes(t, w)
	if len(notes) != 1 {
		t.Fatalf("%d notes, want 1", len(notes))
	}
	first := notes[0]
	if !first.Unread() || first.RecipientID != "spouse" || first.AuthorID != "stranger" || first.AuthorName != "Jefferey" ||
		first.StructureID != "cottage" || first.Body != "I came about the bread." || !strings.HasPrefix(string(first.ID), "note-") {
		t.Errorf("note = %+v", first)
	}
	if *told != 1 || len(*flags) != 1 || !(*flags)[0] {
		t.Errorf("after leaving: told %d, flags %v; want told once and the door flagged", *told, *flags)
	}
	snap := w.Published()
	if !sim.StructureHasUnreadNote(snap.DoorNotes, "cottage") {
		t.Error("the snapshot does not flag the cottage's door")
	}
	// A second note from the same author replaces the first, in place.
	later := now.Add(time.Minute)
	if err := leaveNote(w, "homeowner", "Never mind the bread — the ale.", later); err != nil {
		t.Fatalf("second note: %v", err)
	}
	notes = doorNotes(t, w)
	if len(notes) != 1 || notes[0].ID != first.ID || notes[0].RecipientID != "homeowner" ||
		notes[0].Body != "Never mind the bread — the ale." || !notes[0].LeftAt.Equal(later) {
		t.Errorf("after the second note: %+v, want the first replaced in place", notes)
	}
}

func TestLeaveDoorNoteRefusals(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	mutate(t, w, func(world *sim.World) {
		world.Actors["stranger"].Kind = sim.KindPC
		world.Actors["stranger"].LastPCSeenAt = &now
	})
	if err := leaveNote(w, "spouse", "hello", now); !errors.Is(err, sim.ErrDoorNoteRefused) {
		t.Errorf("away from the door: err %v, want ErrDoorNoteRefused", err)
	}
	playerAtCottageDoor(t, w, now)
	giveWritingKit(t, w, 5)
	if err := leaveNote(w, "servant", "hello", now); !errors.Is(err, sim.ErrDoorNoteRefused) {
		t.Errorf("for the servant: err %v, want ErrDoorNoteRefused", err)
	}
	if err := leaveNote(w, "spouse", "   ", now); !errors.Is(err, sim.ErrDoorNoteText) {
		t.Errorf("blank note: err %v, want ErrDoorNoteText", err)
	}
	if err := leaveNote(w, "spouse", strings.Repeat("é", sim.MaxDoorNoteChars+1), now); !errors.Is(err, sim.ErrDoorNoteText) {
		t.Errorf("too long: err %v, want ErrDoorNoteText", err)
	}
	if err := leaveNote(w, "spouse", strings.Repeat("é", sim.MaxDoorNoteChars), now); err != nil {
		t.Errorf("exactly the cap: err %v, want accepted", err)
	}
	// Someone came to the door: talk to them instead.
	setInside(t, w, "servant", "cottage")
	if out, err := knock(t, w, now); err != nil || !out.Answered {
		t.Fatalf("harness: knock answered: %+v %v", out, err)
	}
	if err := leaveNote(w, "spouse", "hello", now); !errors.Is(err, sim.ErrDoorNoteRefused) {
		t.Errorf("answered door: err %v, want ErrDoorNoteRefused", err)
	}
	// A door they may walk through.
	mutate(t, w, func(world *sim.World) { world.Actors["stranger"].HomeStructureID = "cottage" })
	if err := leaveNote(w, "spouse", "hello", now); !errors.Is(err, sim.ErrDoorNoteRefused) {
		t.Errorf("their own home: err %v, want ErrDoorNoteRefused", err)
	}
}

// completeTurn runs a reactor-tick completion for actorID with the given
// status and shown notes.
func completeTurn(t *testing.T, w *sim.World, actorID sim.ActorID, status sim.TickTerminalStatus, shown []sim.DoorNoteSeen, now time.Time) {
	t.Helper()
	mutate(t, w, func(world *sim.World) {
		a := world.Actors[actorID]
		a.TickInFlight = true
		a.TickAttemptID = "attempt-1"
	})
	if _, err := w.Send(sim.CompleteReactorTick(actorID, "attempt-1",
		sim.TickResult{TerminalStatus: status, ShownDoorNotes: shown}, now)); err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
}

func TestDoorNoteReadOnlyWhenTheTurnAnswered(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	flags, _ := pinnedEvents(t, w)
	playerAtCottageDoor(t, w, now)
	giveWritingKit(t, w, 5)
	if err := leaveNote(w, "spouse", "I came about the bread.", now); err != nil {
		t.Fatalf("leave a note: %v", err)
	}
	n0 := doorNotes(t, w)[0]
	seen := []sim.DoorNoteSeen{{ID: n0.ID, Revision: n0.Revision}}
	// A failed turn leaves it on the door.
	completeTurn(t, w, "spouse", sim.TickStatusFailedAfterRender, seen, now)
	if n := doorNotes(t, w)[0]; !n.Unread() {
		t.Fatal("a failed turn marked the note read")
	}
	// Someone else's answered turn that somehow listed it reads nothing.
	completeTurn(t, w, "homeowner", sim.TickStatusSuccess, seen, now)
	if n := doorNotes(t, w)[0]; !n.Unread() {
		t.Fatal("another villager's turn marked the note read")
	}
	// The recipient's answered turn reads it; the paper comes down.
	readAt := now.Add(time.Hour)
	completeTurn(t, w, "spouse", sim.TickStatusSuccess, seen, readAt)
	n := doorNotes(t, w)[0]
	if n.Unread() || !n.ReadAt.Equal(readAt) {
		t.Errorf("after the recipient's turn: %+v, want read at %v", n, readAt)
	}
	if got := *flags; len(got) != 2 || !got[0] || got[1] {
		t.Errorf("door flags %v, want [true false]", got)
	}
	if sim.StructureHasUnreadNote(w.Published().DoorNotes, "cottage") {
		t.Error("the snapshot still flags the cottage's door")
	}
	// The read leaves a trail line for the reader.
	found := false
	for _, e := range w.Published().ActionLog {
		if e.ActorID == "spouse" && e.ActionType == sim.ActionTypeReadNote && e.CounterpartyName == "Jefferey" && e.Text == "I came about the bread." {
			found = true
		}
	}
	if !found {
		t.Error("no read_note trail entry for the reader")
	}
}

// A note replaced while the recipient's turn was in flight is not marked read
// by that turn: it showed the old text (code_review). The next turn, which
// shows the new one, reads it.
func TestDoorNoteReplacedMidTurnStaysUnread(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	playerAtCottageDoor(t, w, now)
	giveWritingKit(t, w, 5)
	if err := leaveNote(w, "spouse", "I came about the bread.", now); err != nil {
		t.Fatalf("leave a note: %v", err)
	}
	old := doorNotes(t, w)[0]
	shownOld := []sim.DoorNoteSeen{{ID: old.ID, Revision: old.Revision}}
	// The player replaces it after the turn rendered the old one — at the same
	// instant, so only the revision tells the two apart.
	if err := leaveNote(w, "spouse", "Never mind the bread — the ale.", now); err != nil {
		t.Fatalf("replace the note: %v", err)
	}
	completeTurn(t, w, "spouse", sim.TickStatusSuccess, shownOld, now.Add(2*time.Minute))
	cur := doorNotes(t, w)[0]
	if cur.ID != old.ID || !cur.Unread() || cur.Body != "Never mind the bread — the ale." {
		t.Fatalf("after a turn that saw only the old text: %+v, want the new note still unread", cur)
	}
	completeTurn(t, w, "spouse", sim.TickStatusSuccess, []sim.DoorNoteSeen{{ID: cur.ID, Revision: cur.Revision}}, now.Add(3*time.Minute))
	if doorNotes(t, w)[0].Unread() {
		t.Error("a turn that showed the new text did not read it")
	}
}

// A note is for a door nobody answers: with someone awake inside, it is
// refused even without a knock first (code_review); once they are asleep it is
// taken.
func TestLeaveDoorNoteRefusedWhileSomeoneWouldAnswer(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	setInside(t, w, "spouse", "cottage")
	playerAtCottageDoor(t, w, now)
	giveWritingKit(t, w, 5)
	if err := leaveNote(w, "spouse", "hello", now); !errors.Is(err, sim.ErrDoorNoteRefused) {
		t.Fatalf("someone awake inside, no knock: err %v, want ErrDoorNoteRefused", err)
	}
	mutate(t, w, func(world *sim.World) { world.Actors["spouse"].State = sim.StateSleeping })
	if err := leaveNote(w, "spouse", "hello", now); err != nil {
		t.Errorf("everyone inside asleep: err %v, want accepted", err)
	}
}

// giveWritingKit puts a quill and ink and `sheets` sheets of parchment in the
// stranger's pack (LLM-799); sheets 0 gives the quill alone.
func giveWritingKit(t *testing.T, w *sim.World, sheets int) {
	t.Helper()
	mutate(t, w, func(world *sim.World) {
		a := world.Actors["stranger"]
		if a.Inventory == nil {
			a.Inventory = map[sim.ItemKind]int{}
		}
		a.Inventory[sim.NoteQuillKind] = 1
		if sheets > 0 {
			a.Inventory[sim.NoteParchmentKind] = sheets
		} else {
			delete(a.Inventory, sim.NoteParchmentKind)
		}
	})
}

// strangerHolds reads the stranger's count of kind.
func strangerHolds(t *testing.T, w *sim.World, kind sim.ItemKind) int {
	t.Helper()
	var n int
	mutate(t, w, func(world *sim.World) { n = world.Actors["stranger"].Inventory[kind] })
	return n
}

// LLM-799: an unanswered knock says what the player lacks to write a note,
// naming only what is missing; with both in hand it says nothing.
func TestKnockUnansweredSaysWhatANoteNeeds(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	playerAtCottageDoor(t, w, now)

	out, err := knock(t, w, now)
	if err != nil || out.Answered {
		t.Fatalf("knock: %+v %v, want unanswered", out, err)
	}
	if !strings.HasPrefix(out.NoteLack, "You need a quill and ink and a sheet of parchment to leave a note. ") ||
		!strings.HasSuffix(out.NoteLack, " has them for sale.") {
		t.Errorf("empty-handed NoteLack = %q", out.NoteLack)
	}

	giveWritingKit(t, w, 0)
	out, _ = knock(t, w, now)
	if !strings.HasPrefix(out.NoteLack, "You need a sheet of parchment to leave a note. ") ||
		!strings.HasSuffix(out.NoteLack, " has it for sale.") {
		t.Errorf("quill-only NoteLack = %q", out.NoteLack)
	}

	giveWritingKit(t, w, 1)
	if out, _ = knock(t, w, now); out.NoteLack != "" {
		t.Errorf("with both, NoteLack = %q, want empty", out.NoteLack)
	}
}

// LLM-799: a note needs a quill and ink and a sheet of parchment; each note
// pinned (a replacement too) uses a sheet, and the quill is kept.
func TestLeaveDoorNoteNeedsTheKitAndUsesASheet(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	nameCottageFolk(t, w)
	playerAtCottageDoor(t, w, now)

	err := leaveNote(w, "spouse", "hello", now)
	if !errors.Is(err, sim.ErrDoorNoteRefused) || !strings.Contains(err.Error(), "You need a quill and ink and a sheet of parchment") {
		t.Fatalf("empty-handed note: %v, want refused naming both", err)
	}
	if n := len(doorNotes(t, w)); n != 0 {
		t.Fatalf("%d notes after a refusal, want 0", n)
	}

	giveWritingKit(t, w, 2)
	if err := leaveNote(w, "spouse", "I came about the bread.", now); err != nil {
		t.Fatalf("note with the kit: %v", err)
	}
	if got := strangerHolds(t, w, sim.NoteParchmentKind); got != 1 {
		t.Errorf("parchment after one note = %d, want 1", got)
	}
	if err := leaveNote(w, "spouse", "Never mind the bread.", now); err != nil {
		t.Fatalf("replacing note: %v", err)
	}
	var held bool
	mutate(t, w, func(world *sim.World) { _, held = world.Actors["stranger"].Inventory[sim.NoteParchmentKind] })
	if held {
		t.Error("the last sheet left a parchment entry in the pack, want it removed at zero")
	}
	if got := strangerHolds(t, w, sim.NoteQuillKind); got != 1 {
		t.Errorf("quill and ink after two notes = %d, want 1 (kept)", got)
	}

	err = leaveNote(w, "spouse", "One more thing.", now)
	if !errors.Is(err, sim.ErrDoorNoteRefused) || !strings.Contains(err.Error(), "You need a sheet of parchment") {
		t.Fatalf("note with no sheet left: %v, want refused naming the parchment", err)
	}
	if notes := doorNotes(t, w); len(notes) != 1 || notes[0].Body != "Never mind the bread." {
		t.Errorf("after the refusal: %+v, want the second note unchanged", notes)
	}
}
