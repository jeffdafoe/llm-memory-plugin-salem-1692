package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// door_note.go — LLM-19. A player who knocks and gets no answer may leave a
// note for one of the people of that house. The note waits on the door; the
// recipient reads it on their first turn at or inside that house after it was
// left (perception buildDoorNotes), and it is marked read only once that turn
// answered (CompleteReactorTick → markDoorNotesRead). One unread note per author
// per door: a second note from the same author replaces the first.
//
// Durable: World.DoorNotes is loaded from door_note at boot (unread rows only)
// and upserted every checkpoint, like court_case — a note left in the morning
// must survive the day's deploys to be found in the evening. A read note stays
// in the map (ReadAt set) until the next boot so the checkpoint records it.

// DoorNoteID names one note ("note-" + 8 hex).
type DoorNoteID string

// MaxDoorNoteChars caps a note's text, in runes.
const MaxDoorNoteChars = 200

// DoorNote is one note left at a door.
type DoorNote struct {
	ID          DoorNoteID
	StructureID StructureID
	RecipientID ActorID
	AuthorID    ActorID
	AuthorName  string
	Body        string
	LeftAt      time.Time
	ReadAt      time.Time // zero while unread
	// Revision counts replacements, so a turn that rendered one text never
	// marks a later one read (DoorNoteSeen). In memory only: no turn outlives
	// a restart, so a reloaded note starting again at 0 cannot be confused.
	Revision int
}

// Unread reports whether the note still waits on the door.
func (n *DoorNote) Unread() bool { return n != nil && n.ReadAt.IsZero() }

// Clone returns a copy (the struct is flat).
func (n *DoorNote) Clone() *DoorNote {
	if n == nil {
		return nil
	}
	c := *n
	return &c
}

// DoorNoteSeen is a note as a turn's prompt showed it: the id and the
// revision. A note replaced while that turn was in flight is not marked read
// by it.
type DoorNoteSeen struct {
	ID       DoorNoteID
	Revision int
}

// DoorNoteRecipient is one person a note at a door may be left for.
type DoorNoteRecipient struct {
	ID   ActorID
	Name string
}

// ErrDoorNoteRefused: a note that cannot be left — not at that door, a door
// that would open, someone answered, or the recipient is not of that house.
var ErrDoorNoteRefused = errors.New("you cannot leave a note here")

// ErrDoorNoteText: the note's text is empty or too long.
var ErrDoorNoteText = errors.New("the note must have between 1 and 200 characters")

func newDoorNoteID() DoorNoteID {
	return DoorNoteID("note-" + randomHex(4))
}

// doorNoteRecipients lists the people of structureID a note may be left for,
// by name: its residents (HomeStructureID), plus the owner of the place, plus —
// when neither exists — its keeper. Villagers only: no player, decorative or
// visitor.
func doorNoteRecipients(w *World, structureID StructureID) []DoorNoteRecipient {
	seen := map[ActorID]bool{}
	var out []DoorNoteRecipient
	add := func(a *Actor) {
		if a == nil || seen[a.ID] || (a.Kind != KindNPCStateful && a.Kind != KindNPCShared) || IsVisitorActorID(a.ID) {
			return
		}
		seen[a.ID] = true
		out = append(out, DoorNoteRecipient{ID: a.ID, Name: a.DisplayName})
	}
	for _, a := range w.Actors {
		if a != nil && a.HomeStructureID == structureID {
			add(a)
		}
	}
	if vobj, _, ok := villageObjectForStructure(w, structureID); ok && vobj.OwnerActorID != "" {
		add(w.Actors[vobj.OwnerActorID])
	}
	if len(out) == 0 {
		if _, keeper := keeperForStructure(w, structureID); keeper != nil {
			add(keeper)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// mayKnockHere is KnockAtDoor's test that actor stands at the door of
// structureID and would have to knock: the structure exists with a door, it is
// one they must knock at (mustKnockAt), and they are at it, not walking.
func mayKnockHere(w *World, actor *Actor, structureID StructureID, now time.Time) error {
	vobj, _, ok := villageObjectForStructure(w, structureID)
	if _, isStructure := w.Structures[structureID]; !ok || !isStructure {
		return fmt.Errorf("%w: there is no such house", ErrKnockRefused)
	}
	_, hasDoor := structureEntryTile(w, structureID)
	if !mustKnockAt(vobj, effectiveEntryPolicy(w, structureID, vobj, now), structureMembershipAllows(w, actor, structureID, now), hasDoor) {
		return fmt.Errorf("%w: this is not a door to knock on", ErrKnockRefused)
	}
	if actor.MoveIntent != nil || !AtDoorOf(w.VillageObjects, w.Assets, actor.InsideStructureID, actor.Pos, structureID) {
		return fmt.Errorf("%w: you are not at that door", ErrKnockRefused)
	}
	return nil
}

// LeaveDoorNote pins a player's note to the door of structureID for one of its
// people (POST /pc/leave-note). Refused (ErrDoorNoteRefused) unless the player
// could knock there now (mayKnockHere), nobody of the house is at the door with
// them, nobody inside would answer a knock (receptiveKnockReceivers), and
// recipientID is one of doorNoteRecipients. text must already be
// validated as player text; it is trimmed and capped at MaxDoorNoteChars
// (ErrDoorNoteText). A second unread note from the same author at the same door
// replaces the first in place.
func LeaveDoorNote(actorID ActorID, structureID StructureID, recipientID ActorID, text string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor := w.Actors[actorID]
			if actor == nil || actor.Kind != KindPC {
				return nil, fmt.Errorf("LeaveDoorNote: %q is not a player's character", actorID)
			}
			body := strings.TrimSpace(text)
			if body == "" || utf8.RuneCountInString(body) > MaxDoorNoteChars {
				return nil, ErrDoorNoteText
			}
			if err := mayKnockHere(w, actor, structureID, now); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrDoorNoteRefused, err)
			}
			if actorInActiveHuddle(w, actor) && doorConversationAt(w, actor, structureID) {
				return nil, fmt.Errorf("%w: someone has come to the door", ErrDoorNoteRefused)
			}
			// Someone inside would come to a knock: the note is for a door nobody
			// answers, so the same receiver test as the knock (code_review).
			if len(receptiveKnockReceivers(w, structureID)) > 0 {
				return nil, fmt.Errorf("%w: someone is in — knock at the door", ErrDoorNoteRefused)
			}
			var recipient *DoorNoteRecipient
			for _, r := range doorNoteRecipients(w, structureID) {
				if r.ID == recipientID {
					r := r
					recipient = &r
					break
				}
			}
			if recipient == nil {
				return nil, fmt.Errorf("%w: they do not live or keep here", ErrDoorNoteRefused)
			}
			if w.DoorNotes == nil {
				w.DoorNotes = make(map[DoorNoteID]*DoorNote)
			}
			var note *DoorNote
			for _, n := range w.DoorNotes {
				if n.Unread() && n.AuthorID == actorID && n.StructureID == structureID {
					note = n
					break
				}
			}
			if note == nil {
				note = &DoorNote{ID: newDoorNoteID(), StructureID: structureID, AuthorID: actorID}
				w.DoorNotes[note.ID] = note
			} else {
				note.Revision++
			}
			note.RecipientID = recipient.ID
			note.AuthorName = actor.DisplayName
			note.Body = body
			note.LeftAt = now
			w.emit(&DoorNoteLeft{ActorID: actorID, StructureID: structureID, Text: doorNoteLeftText, At: now})
			w.emit(&DoorNotePinned{StructureID: structureID, HasNote: true})
			w.AppendActionLogDurable(DurableActionLogRow{
				ActorID:    actorID,
				OccurredAt: now,
				ActionType: ActionTypeLeftNote,
				Payload: map[string]any{
					"note_id":      string(note.ID),
					"structure_id": string(structureID),
					"recipient":    recipient.Name,
					"text":         body,
				},
				SpeakerName: actor.DisplayName,
				Source:      "player",
			})
			return nil, nil
		},
	}
}

// doorNoteLeftText is the author's private confirmation.
const doorNoteLeftText = "You tuck the note into the door."

// DoorNoteLeft confirms to the author, privately, that their note is on the
// door. TranslateEvent sends it as a private room_event like
// KnockUnansweredNarrated.
type DoorNoteLeft struct {
	EventBase
	ActorID     ActorID
	StructureID StructureID
	Text        string
	At          time.Time
}

func (DoorNoteLeft) isSimEvent() {}

// DoorNotePinned reports whether structureID now has an unread note on its
// door — the client's pinned-paper flag. It carries no text: every client
// receives it.
type DoorNotePinned struct {
	EventBase
	StructureID StructureID
	HasNote     bool
}

func (DoorNotePinned) isSimEvent() {}

// StructureHasUnreadNote reports whether any unread note waits at structureID.
// Pure over the map, so it serves the live world and the snapshot alike.
func StructureHasUnreadNote(notes map[DoorNoteID]*DoorNote, structureID StructureID) bool {
	for _, n := range notes {
		if n.Unread() && n.StructureID == structureID {
			return true
		}
	}
	return false
}

// markDoorNotesRead stamps each shown note read for actorID — the turn that
// showed them answered. A note addressed to someone else, already read, or
// replaced since the turn saw it (Revision moved) is left alone. Each read leaves
// a trail line and a durable row (so the night's dream records it), and the
// door's pinned paper comes down once no unread note is left there.
func markDoorNotesRead(w *World, actorID ActorID, seen []DoorNoteSeen, now time.Time) {
	actor := w.Actors[actorID]
	if actor == nil {
		return
	}
	touched := map[StructureID]bool{}
	for _, s := range seen {
		n := w.DoorNotes[s.ID]
		if !n.Unread() || n.RecipientID != actorID || n.Revision != s.Revision {
			continue
		}
		n.ReadAt = now
		touched[n.StructureID] = true
		if _, err := AppendActionLogEntry(ActionLogEntry{
			ActorID:          actorID,
			OccurredAt:       now,
			ActionType:       ActionTypeReadNote,
			Text:             n.Body,
			HuddleID:         actor.CurrentHuddleID,
			CounterpartyName: n.AuthorName,
		}).Fn(w); err != nil {
			log.Printf("sim/door_note: append read_note for %s: %v", actor.DisplayName, err)
		}
		w.AppendActionLogDurable(DurableActionLogRow{
			ActorID:    actorID,
			OccurredAt: now,
			ActionType: ActionTypeReadNote,
			Payload: map[string]any{
				"note_id": string(n.ID),
				"from":    n.AuthorName,
				"text":    n.Body,
				"left_at": n.LeftAt.UTC().Format(time.RFC3339),
			},
			SpeakerName: actor.DisplayName,
			HuddleID:    actor.CurrentHuddleID,
			Source:      "engine",
		})
	}
	for sid := range touched {
		if !StructureHasUnreadNote(w.DoorNotes, sid) {
			w.emit(&DoorNotePinned{StructureID: sid, HasNote: false})
		}
	}
}

// rehydrateDoorNotesOnLoad loads the unread notes at boot. A missing repo (a
// test Repository without the tier) leaves none.
func (w *World) rehydrateDoorNotesOnLoad(ctx context.Context) error {
	if w.repo.DoorNotes == nil {
		if w.DoorNotes == nil {
			w.DoorNotes = make(map[DoorNoteID]*DoorNote)
		}
		return nil
	}
	notes, err := w.repo.DoorNotes.LoadUnread(ctx)
	if err != nil {
		return err
	}
	if notes == nil {
		notes = make(map[DoorNoteID]*DoorNote)
	}
	w.DoorNotes = notes
	return nil
}
