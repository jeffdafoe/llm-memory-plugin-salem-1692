package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// Compile-time checks: the closed event set is pointer-only after the PR
// 3a EventBase embed. Only *ConcreteEvent satisfies Event — the value
// type does not (setEventBase has a pointer receiver). A line like
// `var _ sim.Event = sim.HuddleConcluded{}` would fail to build.
var (
	_ sim.Event = &sim.SceneMinted{}
	_ sim.Event = &sim.HuddleJoined{}
	_ sim.Event = &sim.HuddleLeft{}
	_ sim.Event = &sim.HuddleConcluded{}
	_ sim.Event = &sim.ActorMet{}
	_ sim.Event = &sim.ReactorTickDue{}
)

// newBareWorld returns a NewWorld with no Run goroutine — for single-
// threaded event-identity tests that drive World.emit directly via
// EmitForTest. Safe because emit only touches world-goroutine-owned
// fields and there is no concurrent Run.
func newBareWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	return sim.NewWorld(repo)
}

// TestEventID_MonotonicFromOne covers the per-run event counter: it starts
// at 0 so the first emitted event is EventID(1), IDs increase by one per
// emit, and EventID(0) — the unset sentinel — is never assigned.
func TestEventID_MonotonicFromOne(t *testing.T) {
	w := newBareWorld(t)

	var got []sim.EventID
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		got = append(got, evt.EventID())
	}))

	if seq := sim.WorldEventSeq(w); seq != 0 {
		t.Fatalf("eventSeq before any emit = %d, want 0", seq)
	}
	for i := 0; i < 5; i++ {
		sim.EmitForTest(w, &sim.HuddleConcluded{})
	}

	want := []sim.EventID{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("recorded %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d].EventID() = %d, want %d", i, got[i], want[i])
		}
	}
	if got[0] == 0 {
		t.Error("first EventID is 0 — the unset sentinel must never be assigned")
	}
}

// TestRootEventID_FreshOriginIsItsOwnRoot covers a fresh-origin emit (no
// ambient cascade root): the event is its own causal root, and withRoot
// restores the ambient root to 0 once the dispatch unwinds.
func TestRootEventID_FreshOriginIsItsOwnRoot(t *testing.T) {
	w := newBareWorld(t)

	var e sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) { e = evt }))

	sim.EmitForTest(w, &sim.HuddleConcluded{})

	if e == nil || e.EventID() == 0 {
		t.Fatal("event was not delivered / not stamped")
	}
	if e.RootEventID() != e.EventID() {
		t.Errorf("fresh-origin RootEventID = %d, want == EventID %d", e.RootEventID(), e.EventID())
	}
	if r := sim.WorldCurrentRootEventID(w); r != 0 {
		t.Errorf("ambient root after fresh emit = %d, want 0 (withRoot must restore)", r)
	}
}

// TestRootEventID_NestedEmitInheritsRoot covers a nested cascade emit: an
// event emitted by a subscriber inherits the originating event's root
// rather than starting a fresh one, while still getting its own unique
// EventID.
func TestRootEventID_NestedEmitInheritsRoot(t *testing.T) {
	w := newBareWorld(t)

	var outer, inner sim.Event
	emittedInner := false
	w.Subscribe(sim.SubscriberFunc(func(world *sim.World, evt sim.Event) {
		if emittedInner {
			inner = evt
			return
		}
		outer = evt
		emittedInner = true
		// Nested emit from inside the outer event's dispatch.
		sim.EmitForTest(world, &sim.ActorMet{})
	}))

	sim.EmitForTest(w, &sim.HuddleConcluded{})

	if outer == nil || inner == nil {
		t.Fatal("expected both the outer and the nested event to be delivered")
	}
	if outer.RootEventID() != outer.EventID() {
		t.Errorf("outer is fresh-origin: RootEventID %d != EventID %d", outer.RootEventID(), outer.EventID())
	}
	if inner.RootEventID() != outer.EventID() {
		t.Errorf("nested emit RootEventID = %d, want outer EventID %d", inner.RootEventID(), outer.EventID())
	}
	if inner.EventID() == outer.EventID() {
		t.Error("nested event must still get its own unique EventID")
	}
	if r := sim.WorldCurrentRootEventID(w); r != 0 {
		t.Errorf("ambient root after nested cascade = %d, want 0", r)
	}
}

// TestRootEventID_SubscriberPanicRestoresAmbientRoot covers withRoot's
// defer-scoped restore on the panic path: a subscriber panicking mid-
// dispatch must not leave the ambient cascade root stuck.
func TestRootEventID_SubscriberPanicRestoresAmbientRoot(t *testing.T) {
	w := newBareWorld(t)
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, _ sim.Event) {
		panic("subscriber boom")
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the subscriber panic to propagate out of emit")
			}
		}()
		sim.EmitForTest(w, &sim.HuddleConcluded{})
	}()

	if r := sim.WorldCurrentRootEventID(w); r != 0 {
		t.Errorf("ambient root after panicking subscriber = %d, want 0 (withRoot defer must restore)", r)
	}
}

// TestNewRootedCommand_RejectsInvalidRoot covers the internal cross-
// boundary root hook's validation: root == 0 and root > eventSeq (a root
// referring to no emitted event) are rejected and the handler does not
// run; a root equal to an emitted EventID is accepted.
func TestNewRootedCommand_RejectsInvalidRoot(t *testing.T) {
	w := newBareWorld(t)
	// One emit so eventSeq == 1 and EventID(1) is a valid prior root.
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, _ sim.Event) {}))
	sim.EmitForTest(w, &sim.HuddleConcluded{})

	ran := false
	run := func(_ *sim.World) (any, error) { ran = true; return nil, nil }

	// root == 0 — rejected.
	if _, err := sim.NewRootedCommand(0, run).Fn(w); err == nil {
		t.Error("newRootedCommand(0): expected an error for root == 0")
	}
	if ran {
		t.Error("handler ran despite the root == 0 rejection")
	}

	// root > eventSeq — refers to no emitted event, rejected.
	ran = false
	if _, err := sim.NewRootedCommand(sim.EventID(999), run).Fn(w); err == nil {
		t.Error("newRootedCommand(999): expected an error for root > eventSeq")
	}
	if ran {
		t.Error("handler ran despite the root > eventSeq rejection")
	}

	// root == an emitted EventID — accepted, handler runs.
	ran = false
	if _, err := sim.NewRootedCommand(sim.EventID(1), run).Fn(w); err != nil {
		t.Errorf("newRootedCommand(1) with a valid root: unexpected error %v", err)
	}
	if !ran {
		t.Error("handler did not run for a valid root")
	}
}

// TestRootedCommand_DoesNotBleedToNextCommand covers the Run-dispatch
// withRoot wrapping: a rooted command's emits inherit its root, but the
// ambient root is restored on the command's unwind, so the NEXT command's
// emit is a fresh origin — the inherited root does not bleed across.
func TestRootedCommand_DoesNotBleedToNextCommand(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	var events []sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		events = append(events, evt)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	emitCmd := func(evt sim.Event) sim.Command {
		return sim.Command{Fn: func(world *sim.World) (any, error) {
			sim.EmitForTest(world, evt)
			return nil, nil
		}}
	}

	// Command 1 — a normal command: its event is a fresh origin (EventID 1,
	// root 1). EventID(1) is then a valid root for the rooted command.
	if _, err := w.Send(emitCmd(&sim.HuddleConcluded{})); err != nil {
		t.Fatalf("command 1: %v", err)
	}
	// Command 2 — a rooted command continuing root 1: its event inherits
	// root 1 (EventID 2, root 1).
	rooted := sim.NewRootedCommand(sim.EventID(1), func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorMet{})
		return nil, nil
	})
	if _, err := w.Send(rooted); err != nil {
		t.Fatalf("rooted command: %v", err)
	}
	// Command 3 — a normal command after the rooted one: its event must be
	// a fresh origin again (EventID 3, root 3) — the rooted command's
	// inherited root must not have bled across.
	if _, err := w.Send(emitCmd(&sim.HuddleLeft{})); err != nil {
		t.Fatalf("command 3: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("recorded %d events, want 3", len(events))
	}
	if events[0].RootEventID() != events[0].EventID() {
		t.Errorf("event 1 should be fresh-origin: root %d != id %d", events[0].RootEventID(), events[0].EventID())
	}
	if events[1].RootEventID() != events[0].EventID() {
		t.Errorf("rooted command's event: root %d, want inherited %d", events[1].RootEventID(), events[0].EventID())
	}
	if events[2].RootEventID() != events[2].EventID() {
		t.Errorf("event 3 must be fresh-origin (no bleed): root %d != id %d", events[2].RootEventID(), events[2].EventID())
	}
}
