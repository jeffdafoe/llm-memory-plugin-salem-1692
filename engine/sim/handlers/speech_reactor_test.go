package handlers_test

import (
	"context"
	"strings"
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
	seed := make(map[sim.ActorID]*sim.Actor, len(specs))
	for _, s := range specs {
		seed[s.id] = &sim.Actor{
			ID:              s.id,
			DisplayName:     s.displayName,
			Kind:            s.kind,
			State:           sim.StateIdle,
			CurrentHuddleID: s.huddleID,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
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
	// SpeechID is the Spoke event's EventID, uint64-typed.
	if reason.SpeechID != sim.SpeechID(m.SourceEventID) {
		t.Errorf("SpeechID = %d, want %d", reason.SpeechID, m.SourceEventID)
	}
}

// --- TestSpeechReactor_NoHuddleNoWarrants ----------------------------
// Speaker with no huddle: Spoke event emits with empty RecipientIDs,
// subscriber sees empty list, no warrants minted. Uses a PC speaker — an NPC
// with no audience is now rejected before emit (ZBBS-HOME-402), so the
// empty-recipient EMIT path this test exercises is PC-only.
func TestSpeechReactor_NoHuddleNoWarrants(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindPC},
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

// --- TestSpeechReactor_ExcerptCarriesFullUtterance -------------------
// A long utterance (well past the old MaxSalientFactTextLen cut) reaches the
// listener's warrant WHOLE — LLM-396. This test previously asserted the exact
// opposite (Excerpt rune len == MaxSalientFactTextLen); that contract was the
// bug, so it is inverted here rather than deleted.
//
// The old 220-rune cut was unmarked and landed mid-word. A listener perceiving
// "...they're about finding home in small" reads an unfinished sentence and asks
// the speaker to complete it; the reply is clipped identically, so every turn
// mints a fresh dangling question and the huddle cannot terminate. That is the
// live 13-minute Inn loop (Lewis / Silence / Hannah, 2026-07-14), and 40% of
// real utterances were long enough to trigger it.
//
// Length is still bounded, just where it belongs: upstream the speak tool caps
// text at MaxSpeakTextChars (1000 runes), and downstream the renderer caps the
// warrant payload at RenderConfig.MaxBytesPerWarrant — and the renderer MARKS
// what it elides, so a clipped line can never again read as an unfinished one.
func TestSpeechReactor_ExcerptCarriesFullUtterance(t *testing.T) {
	// 600 runes: far past the old 220 cut, inside MaxSpeakTextChars (1000).
	long := strings.Repeat("a", 600)
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()
	if _, err := w.Send(sim.Speak("hannah", long, time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	bobWarrants := peekWarrants(t, w, "bob")
	if len(bobWarrants) != 1 {
		t.Fatalf("bob warrants = %d, want 1", len(bobWarrants))
	}
	reason := bobWarrants[0].Reason.(sim.NPCSpeechWarrantReason)
	if reason.Excerpt != long {
		t.Errorf(
			"Excerpt did not carry the full utterance: got %d runes, want %d (the whole line)",
			len([]rune(reason.Excerpt)), len([]rune(long)),
		)
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

// --- TestSpeechReactor_MovingListenerSkipped -------------------------
// ZBBS-HOME-330: a recipient who is mid-walk (MoveIntent != nil) is NOT
// warranted by heard speech — a walking actor can't act on it, so the warrant
// would only yield a command-failing tick (the Josiah<->Elizabeth ping-pong).
// A stationary peer in the same huddle is still warranted, so standing
// discussion (at a stall, in the tavern) is unaffected.
func TestSpeechReactor_MovingListenerSkipped(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "walker", displayName: "Walker", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "stander", displayName: "Stander", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	// Put "walker" mid-walk; leave "stander" idle.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].MoveIntent = &sim.MoveIntent{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed MoveIntent: %v", err)
	}

	if _, err := w.Send(sim.Speak("hannah", "Good morrow to you both.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := peekWarrants(t, w, "walker"); len(got) != 0 {
		t.Errorf("moving listener warrants = %d, want 0 (motion gate)", len(got))
	}
	if got := peekWarrants(t, w, "stander"); len(got) != 1 {
		t.Errorf("stationary listener warrants = %d, want 1 (discussion unaffected)", len(got))
	}
}

// --- TestSpeechReactor_ClearedMoveIntentReWarrants -------------------
// ZBBS-HOME-330 "drop, don't defer": a listener skipped while walking is
// warranted normally by the NEXT utterance once it has stopped (MoveIntent
// cleared). Locks in that the motion gate suppresses only the in-motion tick,
// not the actor's future eligibility (code_review optional-coverage ask).
func TestSpeechReactor_ClearedMoveIntentReWarrants(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "walker", displayName: "Walker", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	// Walking → first utterance is dropped for walker.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].MoveIntent = &sim.MoveIntent{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed MoveIntent: %v", err)
	}
	if _, err := w.Send(sim.Speak("hannah", "First call, while you walk.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak (walking): %v", err)
	}
	if got := peekWarrants(t, w, "walker"); len(got) != 0 {
		t.Fatalf("walker warrants while moving = %d, want 0", len(got))
	}

	// Stop walking → the next utterance warrants normally.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear MoveIntent: %v", err)
	}
	if _, err := w.Send(sim.Speak("hannah", "Second call, now you have stopped.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak (stopped): %v", err)
	}
	if got := peekWarrants(t, w, "walker"); len(got) != 1 {
		t.Errorf("walker warrants after stopping = %d, want 1 (drop, not defer)", len(got))
	}
}

// --- TestSpeechReactor_PCSpeakerStampsPCSpeechReason ------------------
// ZBBS-HOME-377: when the speaker is a PC (the player), the subscriber stamps
// PCSpeechWarrantReason (Kind WarrantKindPCSpoke) instead of NPCSpeechWarrantReason.
// The kind split is what lets actorCanReactNow treat a player's address as a
// break-interrupter while NPC<->NPC chatter stays gated behind the break.
func TestSpeechReactor_PCSpeakerStampsPCSpeechReason(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "player", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1"},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCStateful, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Speak("player", "John, what do you have available?", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	bobWarrants := peekWarrants(t, w, "bob")
	if len(bobWarrants) != 1 {
		t.Fatalf("bob warrants = %d, want 1", len(bobWarrants))
	}
	m := bobWarrants[0]
	if m.Kind() != sim.WarrantKindPCSpoke {
		t.Errorf("Kind = %q, want pc_spoke", m.Kind())
	}
	reason, ok := m.Reason.(sim.PCSpeechWarrantReason)
	if !ok {
		t.Fatalf("Reason concrete type = %T, want PCSpeechWarrantReason", m.Reason)
	}
	if reason.Speaker != "player" {
		t.Errorf("Reason.Speaker = %q, want player", reason.Speaker)
	}
	if reason.Excerpt != "John, what do you have available?" {
		t.Errorf("Reason.Excerpt = %q", reason.Excerpt)
	}
}

// --- TestSpeechReactor_PCSpeakerStillSkipsMovingListener --------------
// ZBBS-HOME-330: the mid-walk gate applies to a PC speaker too — a walking NPC
// can't act on the warrant either way (the speak handler would reject it), so a
// moving listener is skipped while a stationary peer is warranted as normal.
func TestSpeechReactor_PCSpeakerStillSkipsMovingListener(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "player", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1"},
		speakActor{id: "walker", displayName: "Walker", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "stander", displayName: "Stander", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].MoveIntent = &sim.MoveIntent{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed MoveIntent: %v", err)
	}

	if _, err := w.Send(sim.Speak("player", "Hello to you both.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := peekWarrants(t, w, "walker"); len(got) != 0 {
		t.Errorf("moving listener warrants (PC speaker) = %d, want 0 (motion gate still applies)", len(got))
	}
	if got := peekWarrants(t, w, "stander"); len(got) != 1 {
		t.Errorf("stationary listener warrants (PC speaker) = %d, want 1", len(got))
	}
}

// --- TestSpeechReactor_PCSpeechNeverSuppressedUnderRepetition ---------
// Repeated speech is never rate-damped: five distinct PC utterances each warrant
// the listener (distinct timestamps → distinct Spoke events → no source-dedup
// collapse). Was a ZBBS-HOME-377 guard that the PC path bypassed the heard-speech
// breaker; the breaker was retired in ZBBS-WORK-371, so this now pins only that
// the reactor does not collapse repeated utterances.
func TestSpeechReactor_PCSpeechNeverSuppressedUnderRepetition(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "player", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1"},
		speakActor{id: "bob", displayName: "Bob", kind: sim.KindNPCStateful, huddleID: "h1"},
	)
	defer stop()

	base := time.Now().UTC()
	const utterances = 5
	for i := 0; i < utterances; i++ {
		// Distinct timestamps → distinct Spoke events → distinct warrants (no
		// source-dedup collapse).
		at := base.Add(time.Duration(i) * time.Second)
		if _, err := w.Send(sim.Speak("player", "Still here, keeper?", at)); err != nil {
			t.Fatalf("Speak #%d: %v", i, err)
		}
	}

	bobWarrants := peekWarrants(t, w, "bob")
	if len(bobWarrants) != utterances {
		t.Fatalf("bob warrants after %d PC utterances = %d, want %d (repeated speech is not collapsed)", utterances, len(bobWarrants), utterances)
	}
	for i, m := range bobWarrants {
		if m.Kind() != sim.WarrantKindPCSpoke {
			t.Errorf("warrant[%d] Kind = %q, want pc_spoke", i, m.Kind())
		}
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
