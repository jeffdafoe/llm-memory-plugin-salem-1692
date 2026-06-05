package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// speak_addressee_test.go — ZBBS-WORK-369 addressee resolution: the chain
// explicit `to` → sentence-position vocative in text → whole-huddle, carried
// on Spoke.AddressedID. Reuses buildSpeakTestWorld / captureSpoke from
// speak_commands_test.go (same sim_test package).

// TestSpeakTo_AddresseeResolution drives the full resolution chain against a
// three-actor huddle (speaker hannah + present peers ezekiel + bob).
func TestSpeakTo_AddresseeResolution(t *testing.T) {
	cases := []struct {
		name string
		to   string
		text string
		want sim.ActorID
	}{
		{
			name: "explicit to by first name",
			to:   "Ezekiel",
			text: "Good morrow.",
			want: "ezekiel",
		},
		{
			name: "explicit to by full display name",
			to:   "Ezekiel Crane",
			text: "Good morrow.",
			want: "ezekiel",
		},
		{
			name: "explicit to is case-insensitive",
			to:   "ezekiel",
			text: "Good morrow.",
			want: "ezekiel",
		},
		{
			// `to` precedes the vocative step, so it wins even when the text
			// vocatively names a different present peer.
			name: "to wins over a different vocative in text",
			to:   "Ezekiel",
			text: "Bob, good morrow.",
			want: "ezekiel",
		},
		{
			name: "no to, vocative in text resolves to that peer",
			to:   "",
			text: "Ezekiel, good morrow.",
			want: "ezekiel",
		},
		{
			name: "no to, no vocative is whole-huddle (empty)",
			to:   "",
			text: "Good morrow all.",
			want: "",
		},
		{
			// A `to` naming someone not in the huddle is ignored, dropping
			// through to the vocative step.
			name: "to naming an absent actor falls through to vocative",
			to:   "Walker",
			text: "Bob, good morrow.",
			want: "bob",
		},
		{
			name: "to naming an absent actor with no vocative is whole-huddle",
			to:   "Walker",
			text: "Good morrow.",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildSpeakTestWorld(t,
				actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
				actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
				actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
			)
			defer stop()

			captured := captureSpoke(t, w)
			if _, err := w.Send(sim.SpeakTo("hannah", tc.text, tc.to, true, time.Now().UTC())); err != nil {
				t.Fatalf("SpeakTo: %v", err)
			}
			if len(*captured) != 1 {
				t.Fatalf("Spoke events = %d, want 1", len(*captured))
			}
			got := (*captured)[0]
			if got.AddressedID != tc.want {
				t.Errorf("AddressedID = %q, want %q", got.AddressedID, tc.want)
			}
			// Invariant: a resolved addressee is always one of the recipients.
			if got.AddressedID != "" {
				found := false
				for _, r := range got.RecipientIDs {
					if r == got.AddressedID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("AddressedID %q not in RecipientIDs %v", got.AddressedID, got.RecipientIDs)
				}
			}
		})
	}
}

// TestSpeakTo_NoHuddle_AddresseeEmpty — a speaker with no huddle has no peers,
// so even an explicit `to` resolves to empty (whole-huddle / no one). Uses a PC
// speaker: an NPC with no audience is now rejected (ZBBS-HOME-402), so the
// no-huddle COMMIT path this test exercises is PC-only.
func TestSpeakTo_NoHuddle_AddresseeEmpty(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindPC},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared},
	)
	defer stop()

	captured := captureSpoke(t, w)
	if _, err := w.Send(sim.SpeakTo("hannah", "Good morrow.", "Ezekiel", true, time.Now().UTC())); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("Spoke events = %d, want 1", len(*captured))
	}
	if got := (*captured)[0]; got.AddressedID != "" {
		t.Errorf("AddressedID = %q, want empty (no huddle)", got.AddressedID)
	}
}

// TestSpeak_WrapperLeavesAddresseeToResolution — the to-less Speak wrapper
// passes no explicit addressee, so resolution comes from the text vocative.
func TestSpeak_WrapperLeavesAddresseeToResolution(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	if _, err := w.Send(sim.Speak("hannah", "Ezekiel, good morrow.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("Spoke events = %d, want 1", len(*captured))
	}
	if got := (*captured)[0]; got.AddressedID != "ezekiel" {
		t.Errorf("AddressedID = %q, want ezekiel", got.AddressedID)
	}
}
