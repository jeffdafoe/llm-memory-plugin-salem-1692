package handlers_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// speech_reactor_test.go — coverage of handleSpokeWarrants (registered
// via handlers.RegisterSpeechHandlers). Drives the subscriber by sending
// real sim.Speak commands so the test exercises the production wire:
// Speak emits Spoke, subscriber stamps NPCSpeechWarrantReason warrants
// on each peer.
//
// Source-dedup behavior of the warrant infrastructure itself is tested
// in sim/reactor_pr3a_test.go — this file only verifies that the speech
// subscriber stamps with the right SHAPE (kind, SourceEventID, Force).

type speakActor struct {
	id          sim.ActorID
	displayName string
	kind        sim.ActorKind
	huddleID    sim.HuddleID
}

func buildSpeechReactorWorld(t *testing.T, specs ...speakActor) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(specs))
	for _, s := range specs {
		seed[s.id] = &sim.Actor{
			ID:               s.id,
			DisplayName:      s.displayName,
			Kind:             s.kind,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			CurrentHuddleID:  s.huddleID,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		}
	}
	handles.Actors.Seed(seed)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	handlers.RegisterSpeechHandlers(w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// peekWarrants reads the actor's Warrants slice off the world goroutine
// for assertion.
func peekWarrants(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantMeta {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return []sim.WarrantMeta(nil), nil
		}
		// Copy so the caller doesn't race with later writes.
		return append([]sim.WarrantMeta(nil), a.Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peekWarrants(%s): %v", id, err)
	}
	return v.([]sim.WarrantMeta)
}

// --- TestSpeechReactor_OneWarrantPerRecipient -------------------------
// Two-peer huddle: speak from one, both peers receive an NPCSpeechWarrantReason.
func TestSpeechReactor_OneWarrantPerRecipient(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Speak("hannah", "Hello.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	bobWarrants := peekWarrants(t, w, "bob")
	aliceWarrants := peekWarrants(t, w, "alice")
	hannahWarrants := peekWarrants(t, w, "hannah")

	if len(bobWarrants) != 1 {
		t.Errorf("bob warrants = %d, want 1", len(bobWarrants))
	}
	if len(aliceWarrants) != 1 {
		t.Errorf("alice warrants = %d, want 1", len(aliceWarrants))
	}
	if len(hannahWarrants) != 0 {
		t.Errorf("hannah (speaker) self-warrants = %d, want 0", len(hannahWarrants))
	}
}

// --- TestSpeechReactor_WarrantShape ----------------------------------
// Asserts the WarrantMeta + NPCSpeechWarrantReason fields the subscriber
// produces: kind, SourceEventID, Force, payload Speaker, payload Excerpt.
func TestSpeechReactor_WarrantShape(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Speak("hannah", "Have a good day.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	bobWarrants := peekWarrants(t, w, "bob")
	if len(bobWarrants) != 1 {
		t.Fatalf("bob warrants = %d, want 1", len(bobWarrants))
	}
	m := bobWarrants[0]

	if m.Kind() != sim.WarrantKindNPCSpoke {
		t.Errorf("Kind = %q, want npc_spoke", m.Kind())
	}
	if m.Force {
		t.Error("Force = true, want false (PR A doesn't force speech)")
	}
	if m.SourceEventID == 0 {
		t.Error("SourceEventID is zero (not event-sourced) — dedup would be broken")
	}
	if m.TriggerActorID != "hannah" {
		t.Errorf("TriggerActorID = %q, want hannah", m.TriggerActorID)
	}
	if m.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1", m.HuddleID)
	}
	reason, ok := m.Reason.(sim.NPCSpeechWarrantReason)
	if !ok {
		t.Fatalf("Reason concrete type = %T, want NPCSpeechWarrantReason", m.Reason)
	}
	if reason.Speaker != "hannah" {
		t.Errorf("Reason.Speaker = %q, want hannah", reason.Speaker)
	}
	if reason.Excerpt != "Have a good day." {
		t.Errorf("Reason.Excerpt = %q, want %q", reason.Excerpt, "Have a good day.")
	}
	// SpeechID is the decimal of the SourceEventID.
	if reason.SpeechID != sim.SpeechID(strconv.FormatUint(uint64(m.SourceEventID), 10)) {
		t.Errorf("SpeechID = %q, want %d", reason.SpeechID, m.SourceEventID)
	}
}

// --- TestSpeechReactor_NoHuddleNoWarrants ----------------------------
// Speaker with no huddle: Spoke event emits with empty RecipientIDs,
// subscriber sees empty list, no warrants minted.
func TestSpeechReactor_NoHuddleNoWarrants(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCShared},
	)
	defer stop()

	if _, err := w.Send(sim.Speak("hannah", "Echo?", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if w := peekWarrants(t, w, "bob"); len(w) != 0 {
		t.Errorf("bob warrants on no-huddle speak = %d, want 0", len(w))
	}
}

// --- TestSpeechReactor_ExcerptTruncatedToMaxSalientFactTextLen --------
// Long speech (above MaxSalientFactTextLen) produces a truncated Excerpt.
// The Text on the Spoke event itself is bounded by the handler at 1000
// chars; the warrant Excerpt is bounded by sim.MaxSalientFactTextLen (220).
func TestSpeechReactor_ExcerptTruncated(t *testing.T) {
	const longLen = 600 // > MaxSalientFactTextLen (220), < MaxSpeakTextBytes (1000)
	long := make([]byte, longLen)
	for i := range long {
		long[i] = 'a'
	}
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()
	if _, err := w.Send(sim.Speak("hannah", string(long), time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	bobWarrants := peekWarrants(t, w, "bob")
	if len(bobWarrants) != 1 {
		t.Fatalf("bob warrants = %d, want 1", len(bobWarrants))
	}
	reason := bobWarrants[0].Reason.(sim.NPCSpeechWarrantReason)
	if got := len([]rune(reason.Excerpt)); got != sim.MaxSalientFactTextLen {
		t.Errorf("Excerpt rune len = %d, want %d", got, sim.MaxSalientFactTextLen)
	}
}

// --- TestSpeechReactor_StatefulVAPeerStillGetsWarrant ----------------
// The KindNPCShared gate is on the RELATIONSHIP write side, not on the
// warrant side — stateful-VA peers must still receive warrants so their
// VA can react to the speech. Confirms the subscriber doesn't accidentally
// gate on Kind.
func TestSpeechReactor_StatefulVAPeerGetsWarrant(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCStateful, huddleID: "h1"},
	)
	defer stop()
	if _, err := w.Send(sim.Speak("hannah", "How are you, friend?", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if w := peekWarrants(t, w, "ezekiel"); len(w) != 1 {
		t.Errorf("stateful-VA ezekiel warrants = %d, want 1", len(w))
	}
}

// --- TestRegisterSpeechHandlers_NilWorldPanics ------------------------
func TestRegisterSpeechHandlers_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterSpeechHandlers(nil): expected panic, got none")
		}
	}()
	handlers.RegisterSpeechHandlers(nil)
}
