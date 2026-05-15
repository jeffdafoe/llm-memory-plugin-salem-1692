package sim_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// speak_commands_test.go — sim-level coverage of the Speak Command's
// world-state validation, event emission, and bidirectional
// RecordInteraction matrix.
//
// Handler-level static validation (empty/length/control-char) lives in
// handlers/speak_test.go since it's tested through DecodeSpeakArgs +
// HandleSpeak. Subscriber tests (warrant minting) live in
// handlers/speech_reactor_test.go.

// buildSpeakTestWorld stands up a world with actors keyed by ID. Each
// actorSpec specifies kind, optional huddle membership, and optional
// MoveIntent presence (the walk-in-flight test toggles this on one
// actor). LoadWorld rebuilds actorsByHuddle from CurrentHuddleID so the
// peer set the Speak Command reads matches what tests assert against.
type actorSpec struct {
	id           sim.ActorID
	displayName  string
	kind         sim.ActorKind
	huddleID     sim.HuddleID
	moveInFlight bool
}

func buildSpeakTestWorld(t *testing.T, specs ...actorSpec) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(specs))
	for _, s := range specs {
		a := &sim.Actor{
			ID:               s.id,
			DisplayName:      s.displayName,
			Kind:             s.kind,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			CurrentHuddleID:  s.huddleID,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		}
		if s.moveInFlight {
			// MoveIntent's contents are not the point — only its presence
			// (nil vs non-nil) gates the walk-in-flight reject. An empty
			// pointer is enough for the test, but build something
			// well-formed in case future Speak passes look at the fields.
			a.MoveIntent = &sim.MoveIntent{
				AttemptID: sim.MovementAttemptID(1),
			}
		}
		seed[s.id] = a
	}
	handles.Actors.Seed(seed)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// captureSpoke registers a subscriber that records every emitted Spoke
// event into the returned slice. The Subscribe call routes through
// w.Send so it runs on the world goroutine — production wiring rule
// per the World.Subscribe doc: "call before World.Run or from inside a
// Command.Fn." Subscribers dispatch synchronously, so by the time the
// caller's w.Send(Speak(...)) returns the capture slice already
// reflects any emit from that command.
func captureSpoke(t *testing.T, w *sim.World) *[]sim.Spoke {
	t.Helper()
	var out []sim.Spoke
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if s, ok := evt.(*sim.Spoke); ok {
				out = append(out, *s) // value-copy: survives any caller mutation
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("captureSpoke subscribe: %v", err)
	}
	return &out
}

// --- TestSpeak_NoHuddle commits with empty RecipientIDs, no writes, no
// warrants. The "speaker has no current huddle" branch from the design
// walkthrough — commit + no writes.
func TestSpeak_NoHuddle(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared},
		// ezekiel exists but is in NO huddle (separate from hannah)
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared},
	)
	defer stop()

	captured := captureSpoke(t, w)
	at := time.Now().UTC()
	if _, err := w.Send(sim.Speak("hannah", "Anyone there?", at)); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("Spoke events emitted = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.SpeakerID != "hannah" {
		t.Errorf("SpeakerID = %q, want hannah", got.SpeakerID)
	}
	if got.HuddleID != "" {
		t.Errorf("HuddleID = %q, want empty", got.HuddleID)
	}
	if len(got.RecipientIDs) != 0 {
		t.Errorf("RecipientIDs = %v, want empty", got.RecipientIDs)
	}
	if got.Text != "Anyone there?" {
		t.Errorf("Text = %q, want %q", got.Text, "Anyone there?")
	}

	// No-huddle case writes no relationships.
	snap := w.Published()
	if rel := snap.Actors["hannah"].Relationships; len(rel) != 0 {
		t.Errorf("hannah Relationships = %v, want empty", rel)
	}
	if rel := snap.Actors["ezekiel"].Relationships; len(rel) != 0 {
		t.Errorf("ezekiel Relationships = %v, want empty", rel)
	}
}

// --- TestSpeak_SinglePeerHuddle: one peer in huddle, both shared, both
// directions write. One Spoke event, RecipientIDs = [ezekiel].
func TestSpeak_SinglePeerHuddle(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	at := time.Now().UTC()
	if _, err := w.Send(sim.Speak("hannah", "Hello friend.", at)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("Spoke events = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1", got.HuddleID)
	}
	if len(got.RecipientIDs) != 1 || got.RecipientIDs[0] != "ezekiel" {
		t.Errorf("RecipientIDs = %v, want [ezekiel]", got.RecipientIDs)
	}

	snap := w.Published()
	// Hannah speaks → "spoke" salient fact on her side toward ezekiel.
	hannah := snap.Actors["hannah"]
	if rel := hannah.Relationships["ezekiel"]; rel == nil {
		t.Fatal("hannah.Relationships[ezekiel] missing")
	} else if len(rel.SalientFacts) != 1 {
		t.Errorf("hannah→ezekiel SalientFacts = %d, want 1", len(rel.SalientFacts))
	} else if rel.SalientFacts[0].Kind != sim.InteractionSpoke {
		t.Errorf("hannah→ezekiel fact.Kind = %q, want Spoke", rel.SalientFacts[0].Kind)
	}
	// Ezekiel hears → "heard" salient fact on his side toward hannah.
	ezekiel := snap.Actors["ezekiel"]
	if rel := ezekiel.Relationships["hannah"]; rel == nil {
		t.Fatal("ezekiel.Relationships[hannah] missing")
	} else if len(rel.SalientFacts) != 1 {
		t.Errorf("ezekiel→hannah SalientFacts = %d, want 1", len(rel.SalientFacts))
	} else if rel.SalientFacts[0].Kind != sim.InteractionHeard {
		t.Errorf("ezekiel→hannah fact.Kind = %q, want Heard", rel.SalientFacts[0].Kind)
	}
}

// --- TestSpeak_ThreePeerHuddle: speaker + 3 peers in the same huddle.
// Three Spoke→Heard pairs (6 facts total when all 4 are shared).
// RecipientIDs is sorted by ActorID for deterministic ordering.
func TestSpeak_ThreePeerHuddle(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	at := time.Now().UTC()
	if _, err := w.Send(sim.Speak("hannah", "Greetings all.", at)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("Spoke events = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	// RecipientIDs must be sorted by ActorID, speaker excluded.
	wantIDs := []sim.ActorID{"alice", "bob", "carol"}
	if len(got.RecipientIDs) != len(wantIDs) {
		t.Fatalf("RecipientIDs len = %d, want %d", len(got.RecipientIDs), len(wantIDs))
	}
	for i, w := range wantIDs {
		if got.RecipientIDs[i] != w {
			t.Errorf("RecipientIDs[%d] = %q, want %q", i, got.RecipientIDs[i], w)
		}
	}
	// No self-warrant: speaker not in own RecipientIDs.
	for _, r := range got.RecipientIDs {
		if r == "hannah" {
			t.Errorf("speaker hannah in own RecipientIDs")
		}
	}

	// Hannah's Relationships should hold 3 Spoke facts (one per peer).
	snap := w.Published()
	hannah := snap.Actors["hannah"]
	if len(hannah.Relationships) != 3 {
		t.Errorf("hannah Relationships len = %d, want 3", len(hannah.Relationships))
	}
	for _, peerID := range wantIDs {
		rel := hannah.Relationships[peerID]
		if rel == nil {
			t.Errorf("hannah.Relationships[%s] missing", peerID)
			continue
		}
		if len(rel.SalientFacts) != 1 {
			t.Errorf("hannah→%s SalientFacts = %d, want 1", peerID, len(rel.SalientFacts))
		}
		// And each peer should have a single Heard fact toward hannah.
		peer := snap.Actors[peerID]
		prel := peer.Relationships["hannah"]
		if prel == nil {
			t.Errorf("%s.Relationships[hannah] missing", peerID)
			continue
		}
		if len(prel.SalientFacts) != 1 || prel.SalientFacts[0].Kind != sim.InteractionHeard {
			t.Errorf("%s→hannah facts = %+v, want one Heard", peerID, prel.SalientFacts)
		}
	}
}

// --- TestSpeak_KindNPCSharedGate_Matrix: persistence matrix from the
// design — 4 combinations of (speaker kind, peer kind). The
// KindNPCShared gate inside RecordInteraction filters writes on the
// rememberer side. Each case puts two actors in a huddle and verifies
// which side(s) actually wrote a SalientFact.
func TestSpeak_KindNPCSharedGate_Matrix(t *testing.T) {
	cases := []struct {
		name          string
		speakerKind   sim.ActorKind
		peerKind      sim.ActorKind
		speakerWrites bool // (speaker, peer, Spoke) persists?
		peerWrites    bool // (peer, speaker, Heard) persists?
	}{
		{"shared_shared", sim.KindNPCShared, sim.KindNPCShared, true, true},
		{"shared_stateful", sim.KindNPCShared, sim.KindNPCStateful, true, false},
		{"stateful_shared", sim.KindNPCStateful, sim.KindNPCShared, false, true},
		{"stateful_stateful", sim.KindNPCStateful, sim.KindNPCStateful, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildSpeakTestWorld(t,
				actorSpec{id: "s", displayName: "Speaker", kind: tc.speakerKind, huddleID: "h1"},
				actorSpec{id: "p", displayName: "Peer", kind: tc.peerKind, huddleID: "h1"},
			)
			defer stop()

			if _, err := w.Send(sim.Speak("s", "test", time.Now().UTC())); err != nil {
				t.Fatalf("Speak: %v", err)
			}
			snap := w.Published()
			speakerSideWrote := snap.Actors["s"].Relationships["p"] != nil
			peerSideWrote := snap.Actors["p"].Relationships["s"] != nil
			if speakerSideWrote != tc.speakerWrites {
				t.Errorf("speaker side wrote = %v, want %v", speakerSideWrote, tc.speakerWrites)
			}
			if peerSideWrote != tc.peerWrites {
				t.Errorf("peer side wrote = %v, want %v", peerSideWrote, tc.peerWrites)
			}
		})
	}
}

// --- TestSpeak_WalkInFlight_Rejected: actor.MoveIntent != nil rejects.
// The error message should mention "walking" so the model can read it.
func TestSpeak_WalkInFlight_Rejected(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", moveInFlight: true},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	at := time.Now().UTC()
	_, err := w.Send(sim.Speak("hannah", "On my way.", at))
	if err == nil {
		t.Fatal("Speak: want error for walk-in-flight, got nil")
	}
	if !strings.Contains(err.Error(), "walking") {
		t.Errorf("error message lacks 'walking' guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Spoke event emitted on rejected speak: %v", *captured)
	}
	// No relationship writes either.
	snap := w.Published()
	if len(snap.Actors["hannah"].Relationships) != 0 {
		t.Errorf("hannah Relationships after reject: %v", snap.Actors["hannah"].Relationships)
	}
}

// --- TestSpeak_VocativeStale_Rejected: speech addresses someone who
// exists in the world but is NOT in the speaker's current huddle.
// Reject with a "no longer in your conversation" message.
func TestSpeak_VocativeStale_Rejected(t *testing.T) {
	// Hannah & Bob are in the huddle. Ezekiel exists but is not in the
	// huddle. The model addresses Ezekiel in vocative position.
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared}, // NOT in huddle
	)
	defer stop()

	captured := captureSpoke(t, w)
	_, err := w.Send(sim.Speak("hannah", "Ezekiel, you look hungry.", time.Now().UTC()))
	if err == nil {
		t.Fatal("Speak: want error for vocative-stale, got nil")
	}
	if !strings.Contains(err.Error(), "Ezekiel") {
		t.Errorf("error message should name the absent actor; got: %v", err)
	}
	if !strings.Contains(err.Error(), "no longer in your conversation") {
		t.Errorf("error message lacks expected guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Spoke emitted on rejected speak: %v", *captured)
	}
}

// --- TestSpeak_VocativeNonStale_PeerInHuddle: addressing a peer who IS
// in the huddle is fine.
func TestSpeak_VocativeNonStale_PeerInHuddle(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	if _, err := w.Send(sim.Speak("hannah", "Ezekiel, you look hungry.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v (expected success — peer is in huddle)", err)
	}
	if len(*captured) != 1 {
		t.Errorf("Spoke events = %d, want 1", len(*captured))
	}
}

// --- TestSpeak_VocativeNonStale_NonVocativeReference: mid-sentence
// reference to an absent person is NOT vocative and should NOT reject.
// "I told Ezekiel to be careful" passes; "Ezekiel, ..." would not.
func TestSpeak_VocativeNonStale_NonVocativeReference(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared}, // absent
	)
	defer stop()

	captured := captureSpoke(t, w)
	if _, err := w.Send(sim.Speak("hannah", "I told Ezekiel to be careful.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v (expected success — non-vocative reference)", err)
	}
	if len(*captured) != 1 {
		t.Errorf("Spoke events = %d, want 1", len(*captured))
	}
}

// --- TestSpeak_VocativeStale_GreetingDoesNotFalsePositive: a stray
// capitalized salutation ("Hello, friend") at sentence start triggers
// the vocative regex but the candidate doesn't match any actor → no
// reject. Documents the false-positive avoidance.
func TestSpeak_VocativeStale_GreetingDoesNotFalsePositive(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	if _, err := w.Send(sim.Speak("hannah", "Hello, friend.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(*captured) != 1 {
		t.Errorf("Spoke events = %d, want 1", len(*captured))
	}
}

// --- TestSpeak_NoSelfWarrant_RecipientsExcludeSpeaker: ensure that
// even if the index ever contained the speaker, RecipientIDs excludes
// them. Belt-and-braces.
func TestSpeak_NoSelfRecipient(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	if _, err := w.Send(sim.Speak("hannah", "Greetings.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("Spoke events = %d, want 1", len(*captured))
	}
	for _, r := range (*captured)[0].RecipientIDs {
		if r == "hannah" {
			t.Errorf("self in RecipientIDs: %v", (*captured)[0].RecipientIDs)
		}
	}
}

// --- TestSpeak_UnknownSpeaker: speakerID not in w.Actors errors.
func TestSpeak_UnknownSpeaker(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared},
	)
	defer stop()
	_, err := w.Send(sim.Speak("ghost", "boo", time.Now().UTC()))
	if err == nil {
		t.Fatal("Speak: want error for unknown speaker, got nil")
	}
	if !strings.Contains(err.Error(), "not in world") {
		t.Errorf("error message = %v, want 'not in world'", err)
	}
}

// --- TestSpeak_AtTimeOnRelationshipFact: the `at` passed to Speak is
// the timestamp on every SalientFact written. Verifies temporal
// alignment between the Spoke event and the relationship writes.
func TestSpeak_AtTimeOnRelationshipFact(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	at := time.Date(2026, 5, 15, 12, 30, 45, 0, time.UTC)
	if _, err := w.Send(sim.Speak("hannah", "Test", at)); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["bob"]
	if rel == nil {
		t.Fatal("hannah.Relationships[bob] missing")
	}
	if len(rel.SalientFacts) != 1 {
		t.Fatalf("SalientFacts = %d, want 1", len(rel.SalientFacts))
	}
	if !rel.SalientFacts[0].At.Equal(at) {
		t.Errorf("SalientFact.At = %v, want %v", rel.SalientFacts[0].At, at)
	}
}

// --- TestSpeak_ErrorIsNotEvent: a rejected speak must not emit Spoke
// (defensive — covers walk-in-flight + vocative-stale paths).
func TestSpeak_RejectionEmitsNoSpoke(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T) (*sim.World, func(), sim.ActorID, string)
	}{
		{
			name: "walk_in_flight",
			set: func(t *testing.T) (*sim.World, func(), sim.ActorID, string) {
				w, stop := buildSpeakTestWorld(t,
					actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", moveInFlight: true},
					actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
				)
				return w, stop, "hannah", "On the way."
			},
		},
		{
			name: "vocative_stale",
			set: func(t *testing.T) (*sim.World, func(), sim.ActorID, string) {
				w, stop := buildSpeakTestWorld(t,
					actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
					actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
					actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared},
				)
				return w, stop, "hannah", "Ezekiel, where are you?"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop, speakerID, text := tc.set(t)
			defer stop()
			captured := captureSpoke(t, w)
			_, err := w.Send(sim.Speak(speakerID, text, time.Now().UTC()))
			if err == nil {
				t.Fatal("Speak: want error, got nil")
			}
			if len(*captured) != 0 {
				t.Errorf("Spoke events emitted on reject: %d", len(*captured))
			}
		})
	}
}

// --- TestSpeak_SortedAbsenteeList: when two absent actors are
// addressed, the error message lists them deterministically (sorted).
func TestSpeak_SortedAbsenteeList(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared},
		actorSpec{id: "alice", displayName: "Alice Stone", kind: sim.KindNPCShared},
	)
	defer stop()

	_, err := w.Send(sim.Speak("hannah", "Ezekiel, please leave. Alice, you too.", time.Now().UTC()))
	if err == nil {
		t.Fatal("Speak: want error, got nil")
	}
	// "Alice Stone" sorts before "Ezekiel Crane". Both names must appear.
	msg := err.Error()
	if !strings.Contains(msg, "Alice Stone") {
		t.Errorf("error msg missing Alice Stone: %v", err)
	}
	if !strings.Contains(msg, "Ezekiel Crane") {
		t.Errorf("error msg missing Ezekiel Crane: %v", err)
	}
	aliceIdx := strings.Index(msg, "Alice Stone")
	ezekielIdx := strings.Index(msg, "Ezekiel Crane")
	if aliceIdx > ezekielIdx {
		t.Errorf("absentees not sorted: %q", msg)
	}
}

// --- TestSpeak_LastInteractionAtBumps: after Speak, the Relationship
// row's InteractionCount and LastInteractionAt advance.
func TestSpeak_LastInteractionAtBumps(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	first := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	if _, err := w.Send(sim.Speak("hannah", "Hi.", first)); err != nil {
		t.Fatalf("Speak first: %v", err)
	}
	second := first.Add(time.Minute)
	if _, err := w.Send(sim.Speak("hannah", "Still here.", second)); err != nil {
		t.Fatalf("Speak second: %v", err)
	}
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["bob"]
	if rel == nil {
		t.Fatal("hannah.Relationships[bob] missing")
	}
	if rel.InteractionCount != 2 {
		t.Errorf("InteractionCount = %d, want 2", rel.InteractionCount)
	}
	if rel.LastInteractionAt == nil || !rel.LastInteractionAt.Equal(second) {
		t.Errorf("LastInteractionAt = %v, want %v", rel.LastInteractionAt, second)
	}
	if len(rel.SalientFacts) != 2 {
		t.Errorf("SalientFacts len = %d, want 2", len(rel.SalientFacts))
	}
}

// --- assertion helper: assert sorted ActorID slice equals expectation
// without depending on sort import in every test.
func assertSortedActorIDs(t *testing.T, got, want []sim.ActorID) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("len = %d, want %d", len(got), len(want))
		return
	}
	sorted := append([]sim.ActorID(nil), got...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i := range got {
		if got[i] != sorted[i] {
			t.Errorf("[%d] = %q, but sorted = %q (input not sorted)", i, got[i], sorted[i])
			return
		}
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- TestSpeak_RecipientIDsOrderedDeterministically: multiple speak calls
// to the same huddle produce the same sorted RecipientIDs every time.
func TestSpeak_RecipientIDsOrderedDeterministically(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "zelda", displayName: "Zelda", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := captureSpoke(t, w)
	for i := 0; i < 3; i++ {
		if _, err := w.Send(sim.Speak("hannah", "msg", time.Now().UTC())); err != nil {
			t.Fatalf("Speak[%d]: %v", i, err)
		}
	}
	if len(*captured) != 3 {
		t.Fatalf("Spoke events = %d, want 3", len(*captured))
	}
	want := []sim.ActorID{"alice", "carol", "zelda"}
	for i, evt := range *captured {
		assertSortedActorIDs(t, evt.RecipientIDs, want)
		if t.Failed() {
			t.Fatalf("Spoke[%d] RecipientIDs not as expected", i)
		}
	}
}
