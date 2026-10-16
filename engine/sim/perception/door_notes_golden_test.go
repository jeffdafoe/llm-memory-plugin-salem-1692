package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// door_notes_golden_test.go — LLM-19 golden scenarios + a cross-scenario
// invariant: a note a player left at a door shows its recipient on a turn at or
// inside that house, and nowhere else, and nobody else.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "resident_finds_note_on_coming_home",
			summary: "LLM-19: Jefferey knocked at the Walker Residence while nobody was in and left Silence a note three " +
				"hours ago. Silence is home now: '## A note at your door' reads it, from Jefferey (exact name), left " +
				"some hours ago, the text in quotes. No tool is named.",
			build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) { return doorNoteScenario("inside") },
		},
		perceptionScenario{
			name:    "resident_at_own_door_finds_note",
			summary: "LLM-19: the same note; Silence stands outdoors at her own door, just come back. She finds it there.",
			build:   func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) { return doorNoteScenario("at_door") },
		},
		perceptionScenario{
			name: "resident_away_does_not_see_note",
			summary: "LLM-19: the same note; Silence is out across the village. The note waits on the door — her " +
				"prompt says nothing of it.",
			build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) { return doorNoteScenario("away") },
		},
		perceptionScenario{
			name: "note_for_housemate_not_shown",
			summary: "LLM-19: Silence is home, but the note on the door is addressed to Patience, who lives there too. " +
				"Silence's prompt says nothing of it.",
			build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) { return doorNoteScenario("for_housemate") },
		},
		perceptionScenario{
			name: "keeper_finds_note_at_shop_on_return",
			summary: "LLM-19: Jefferey knocked at the General Store while Josiah was away and left him a note a day " +
				"ago. Josiah is back in the store: the note reads, left a day ago.",
			build: keeperFindsNoteAtShopScenario,
		},
	)
}

const (
	doorNoteID         = sim.DoorNoteID("note-0000aa01")
	doorNoteHousemate  = sim.ActorID("patience")
	doorNoteBodySilenc = "I came about the bread. I will be at the Inn tonight, if you have any to spare."
)

// doorNoteScenario: the let-in fixture's Walker Residence with nobody at the
// door — Jefferey left a note for Silence (or, "for_housemate", for Patience)
// three hours before. where places Silence: "inside", "at_door" (outdoors at
// its pin) or "away" (outdoors far off).
func doorNoteScenario(where string) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id, _ := knockAtHomeScenario(false)
	delete(snap.Actors, letInKnockID)
	snap.Huddles = nil
	host := snap.Actors[id]
	host.CurrentHuddleID = ""
	switch where {
	case "at_door":
		pin, _ := sim.ObjectLoiterPin(snap.VillageObjects, snap.Assets, sim.VillageObjectID(letInHomeID))
		host.InsideStructureID = ""
		host.Pos = pin
	case "away":
		host.InsideStructureID = ""
		host.Pos = sim.TilePos{X: 2, Y: 2}
	}
	recipient := id
	if where == "for_housemate" {
		recipient = doorNoteHousemate
		snap.Actors[doorNoteHousemate] = &sim.ActorSnapshot{
			Kind: sim.KindNPCShared, DisplayName: "Patience Walker", Role: "villager", State: sim.StateSleeping,
			Needs: map[sim.NeedKey]int{}, HomeStructureID: letInHomeID, InsideStructureID: letInHomeID,
		}
	}
	snap.DoorNotes = map[sim.DoorNoteID]*sim.DoorNote{
		doorNoteID: {
			ID: doorNoteID, StructureID: letInHomeID, RecipientID: recipient, AuthorID: letInKnockID,
			AuthorName: "Jefferey", Body: doorNoteBodySilenc, LeftAt: snap.PublishedAt.Add(-3 * time.Hour),
		},
	}
	return snap, id, nil
}

func keeperFindsNoteAtShopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := damagedShopSnapshot(100, false, false)
	snap.PublishedAt = time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	snap.DoorNotes = map[sim.DoorNoteID]*sim.DoorNote{
		"note-0000aa02": {
			ID: "note-0000aa02", StructureID: "store", RecipientID: pwJosiah, AuthorID: "jefferey",
			AuthorName: "Jefferey", Body: "Have you a spare shovel? I will pay well.", LeftAt: snap.PublishedAt.Add(-26 * time.Hour),
		},
	}
	return snap, pwJosiah, nil
}

// TestGoldensDoorNoteOnlyAtItsDoor — across the whole matrix, an unread note's
// text renders exactly when the subject is its recipient AND is inside its
// house or outdoors at its door (found by the test's own reading of the
// snapshot, not buildDoorNotes), and ShownDoorNotes lists exactly those.
// Vacuity-guarded both ways.
func TestGoldensDoorNoteOnlyAtItsDoor(t *testing.T) {
	sawShown, sawHidden := false, false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			subject := snap.Actors[actorID]
			if subject == nil || len(snap.DoorNotes) == 0 {
				return
			}
			p := Build(snap, actorID, warrants)
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			shown := map[sim.DoorNoteID]bool{}
			for _, s := range ShownDoorNotes(p) {
				shown[s.ID] = true
			}
			for id, n := range snap.DoorNotes {
				pin, ok := sim.ObjectLoiterPin(snap.VillageObjects, snap.Assets, sim.VillageObjectID(n.StructureID))
				atDoor := subject.InsideStructureID == "" && ok && subject.Pos.Chebyshev(pin) <= sim.LoiterAttributionTiles
				want := n.Unread() && n.RecipientID == actorID && (subject.InsideStructureID == n.StructureID || atDoor)
				if want {
					sawShown = true
				} else {
					sawHidden = true
				}
				if got := strings.Contains(out, n.Body); got != want {
					t.Errorf("note %s for %s: rendered %v, want %v:\n%s", id, n.RecipientID, got, want, out)
				}
				if shown[id] != want {
					t.Errorf("note %s: ShownDoorNotes lists it %v, want %v", id, shown[id], want)
				}
			}
		})
	}
	if !sawShown || !sawHidden {
		t.Fatalf("invariant is vacuous: shown seen=%v, hidden seen=%v", sawShown, sawHidden)
	}
}

func TestDoorNoteAge(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{5 * time.Minute, "just now"},
		{time.Hour, "a little while ago"},
		{5 * time.Hour, "some hours ago"},
		{26 * time.Hour, "a day ago"},
		{50 * time.Hour, "two days ago"},
		{6*24*time.Hour + time.Hour, "six days ago"},
		{9 * 24 * time.Hour, "many days ago"},
	} {
		if got := doorNoteAge(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("%v ago = %q, want %q", tc.ago, got, tc.want)
		}
	}
}
