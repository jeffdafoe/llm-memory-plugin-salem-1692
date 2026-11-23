package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// command_panic_health_test.go — LLM-997: World.Run recovers a panic in a
// command Fn, fails that command with a *CommandPanicError, records it for the
// command_panic alarm, and keeps serving.

// divideForPanicTest divides at run time so the compiler cannot fold a
// constant divide by zero. It is the frame the recorded panic must name.
func divideForPanicTest(a, b int) int {
	return a / b
}

func runPanicTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func TestRun_RecoversCommandPanicAndKeepsServing(t *testing.T) {
	w, cancel := runPanicTestWorld(t)
	defer cancel()

	zero := 0
	_, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) {
		return divideForPanicTest(7, zero), nil
	}})
	var perr *sim.CommandPanicError
	if !errors.As(err, &perr) {
		t.Fatalf("panicking command returned %v (%T), want *sim.CommandPanicError", err, err)
	}
	if !strings.Contains(perr.Value, "divide by zero") {
		t.Errorf("panic value = %q, want it to name the divide by zero", perr.Value)
	}
	if len(perr.Frames) == 0 || !strings.Contains(perr.Frames[0], "divideForPanicTest") {
		t.Errorf("innermost frame = %v, want divideForPanicTest first (runtime frames skipped)", perr.Frames)
	}
	for _, f := range perr.Frames {
		if strings.HasPrefix(f, "runtime.") || strings.Contains(f, "runCommandFn") {
			t.Errorf("frames carry a runtime or recover frame: %v", perr.Frames)
		}
	}

	// The world goroutine is still serving.
	v, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return "alive", nil }})
	if err != nil || v != "alive" {
		t.Fatalf("command after the panic = (%v, %v), want (alive, nil)", v, err)
	}

	snap := w.CommandPanicHealthSnapshot()
	if snap.Count != 1 {
		t.Errorf("recorded count = %d, want 1", snap.Count)
	}
	if snap.FirstAt.IsZero() || !snap.FirstAt.Equal(snap.LastAt) {
		t.Errorf("first/last = %v/%v, want equal and set for one panic", snap.FirstAt, snap.LastAt)
	}
	if !strings.Contains(snap.LastPanic, "divide by zero") {
		t.Errorf("recorded panic = %q", snap.LastPanic)
	}
	if len(snap.LastFrames) == 0 || !strings.Contains(snap.LastFrames[0], "divideForPanicTest") {
		t.Errorf("recorded frames = %v", snap.LastFrames)
	}
}

// TestRun_PanicKeepsTheChangesMadeBeforeIt pins the accepted price of
// containment (Jeff, 2026-11-23): nothing rolls back what the command changed
// before it panicked.
func TestRun_PanicKeepsTheChangesMadeBeforeIt(t *testing.T) {
	w, cancel := runPanicTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ActionLog = append(world.ActionLog, sim.ActionLogEntry{})
		panic("half done")
	}})
	if err == nil || !strings.Contains(err.Error(), "half done") {
		t.Fatalf("err = %v, want the recovered panic", err)
	}
	n, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { return len(world.ActionLog), nil }})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != 1 {
		t.Errorf("action log length = %v, want 1 (the change before the panic stays)", n)
	}
}

// TestRun_RootedCommandPanicDoesNotBleedItsRoot: a rooted command that panics
// still restores the ambient root, so the next command's event is a fresh
// origin. The recorded frames skip withRoot and the recover closure.
func TestRun_RootedCommandPanicDoesNotBleedItsRoot(t *testing.T) {
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
	if _, err := w.Send(emitCmd(&sim.HuddleConcluded{})); err != nil {
		t.Fatalf("command 1: %v", err)
	}
	zero := 0
	rooted := sim.NewRootedCommand(sim.EventID(1), func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorMet{})
		return divideForPanicTest(1, zero), nil
	})
	_, err = w.Send(rooted)
	var perr *sim.CommandPanicError
	if !errors.As(err, &perr) {
		t.Fatalf("rooted panicking command returned %v, want *sim.CommandPanicError", err)
	}
	for _, f := range perr.Frames {
		if strings.Contains(f, "withRoot") || strings.Contains(f, "runCommandFn") {
			t.Errorf("frames carry dispatch plumbing: %v", perr.Frames)
		}
	}
	if _, err := w.Send(emitCmd(&sim.HuddleLeft{})); err != nil {
		t.Fatalf("command 3: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("recorded %d events, want 3", len(events))
	}
	if events[1].RootEventID() != events[0].EventID() {
		t.Errorf("rooted command's event: root %d, want inherited %d", events[1].RootEventID(), events[0].EventID())
	}
	if events[2].RootEventID() != events[2].EventID() {
		t.Errorf("event after the panic must be fresh-origin: root %d != id %d", events[2].RootEventID(), events[2].EventID())
	}
}

func TestCommandPanicHealth_RunWindow(t *testing.T) {
	h := &sim.CommandPanicHealth{}
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	perr := func(v string) *sim.CommandPanicError {
		return &sim.CommandPanicError{Value: v, Frames: []string{"sim.f (f.go:1)"}}
	}

	h.Record(t0, perr("a"))
	h.Record(t0.Add(sim.CommandPanicAlarmWindow), perr("b"))
	snap := h.Snapshot()
	if snap.Count != 2 || !snap.FirstAt.Equal(t0) || snap.LastPanic != "b" {
		t.Errorf("within the window: %+v, want count 2, first %v, last b", snap, t0)
	}

	// A panic more than the window after the previous one starts a new run.
	t2 := t0.Add(2*sim.CommandPanicAlarmWindow + time.Second)
	h.Record(t2, perr("c"))
	snap = h.Snapshot()
	if snap.Count != 1 || !snap.FirstAt.Equal(t2) || !snap.LastAt.Equal(t2) || snap.LastPanic != "c" {
		t.Errorf("new run: %+v, want count 1 starting at %v", snap, t2)
	}
}

func TestCommandPanicHealth_NilSafe(t *testing.T) {
	var h *sim.CommandPanicHealth
	h.Record(time.Now(), &sim.CommandPanicError{Value: "x"})
	if snap := h.Snapshot(); snap.Count != 0 {
		t.Errorf("nil recorder snapshot = %+v, want zero", snap)
	}
	(&sim.CommandPanicHealth{}).Record(time.Now(), nil)
}

// TestRun_SubscriberPanicInsideAnEmitIsContained: a subscriber that panics
// while an event dispatches inside the Fn unwinds through emit's withRoot; the
// command fails, and the next command's event is a fresh origin.
func TestRun_SubscriberPanicInsideAnEmitIsContained(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	var seen []sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if _, ok := evt.(*sim.HuddleConcluded); ok {
			panic("subscriber blew up")
		}
		seen = append(seen, evt)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.HuddleConcluded{})
		return "unreached", nil
	}})
	var perr *sim.CommandPanicError
	if !errors.As(err, &perr) || perr.Value != "subscriber blew up" {
		t.Fatalf("err = %v, want the subscriber's recovered panic", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.HuddleLeft{})
		return nil, nil
	}}); err != nil {
		t.Fatalf("next command: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("recorded %d events, want 1", len(seen))
	}
	if seen[0].RootEventID() != seen[0].EventID() {
		t.Errorf("event after the subscriber panic must be fresh-origin: root %d != id %d", seen[0].RootEventID(), seen[0].EventID())
	}
}

// TestRun_SubmittedCommandPanicIsRecorded: a fire-and-forget command has no
// reply channel; its panic is still recorded and the loop keeps serving.
func TestRun_SubmittedCommandPanicIsRecorded(t *testing.T) {
	w, cancel := runPanicTestWorld(t)
	defer cancel()

	w.Submit(func(_ *sim.World) (any, error) { panic("no one is listening") })
	// Commands run in order, so this Send returns after the submitted one ran.
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("command after the submitted panic: %v", err)
	}
	snap := w.CommandPanicHealthSnapshot()
	if snap.Count != 1 || snap.LastPanic != "no one is listening" {
		t.Errorf("snapshot = %+v, want the submitted panic recorded once", snap)
	}
}

// TestRun_InlineFnPanicIsRecordedOnce: an Fn that runs another command's Fn
// inline is still one command; its panic is recovered and recorded once.
func TestRun_InlineFnPanicIsRecordedOnce(t *testing.T) {
	w, cancel := runPanicTestWorld(t)
	defer cancel()

	zero := 0
	inner := sim.Command{Fn: func(_ *sim.World) (any, error) { return divideForPanicTest(3, zero), nil }}
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return inner.Fn(world)
	}})
	var perr *sim.CommandPanicError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want *sim.CommandPanicError", err)
	}
	if snap := w.CommandPanicHealthSnapshot(); snap.Count != 1 {
		t.Errorf("recorded count = %d, want 1", snap.Count)
	}
}

// TestRun_PanicNilIsRecovered: panic(nil) reaches recover as a
// *runtime.PanicNilError (Go 1.21+), so it is contained like any other.
func TestRun_PanicNilIsRecovered(t *testing.T) {
	w, cancel := runPanicTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) {
		var v any
		panic(v)
	}})
	var perr *sim.CommandPanicError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want *sim.CommandPanicError", err)
	}
	if snap := w.CommandPanicHealthSnapshot(); snap.Count != 1 {
		t.Errorf("recorded count = %d, want 1", snap.Count)
	}
}

// TestRun_PostCommandWorkRunsAfterAPanic: the tick counter still advances and
// the world still republishes after a panicked command.
func TestRun_PostCommandWorkRunsAfterAPanic(t *testing.T) {
	w, cancel := runPanicTestWorld(t)
	defer cancel()

	readTick := func() int64 {
		v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { return int64(world.TickCounter), nil }})
		if err != nil {
			t.Fatalf("read tick: %v", err)
		}
		return v.(int64)
	}
	before := readTick()
	published := w.Published()
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { panic("x") }}); err == nil {
		t.Fatal("no error from the panicking command")
	}
	if w.Published() == published {
		t.Error("the world did not republish after the panicked command")
	}
	if after := readTick(); after-before != 2 {
		t.Errorf("tick advanced by %d across the read and the panicked command, want 2", after-before)
	}
}

// TestRun_DeltaSubscriberPanicIsContained: a subscriber that panics on a
// post-command delta event (NPCCoinsChanged) is contained and recorded; the
// command's own result stands and the loop keeps serving.
func TestRun_DeltaSubscriberPanicIsContained(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {ID: "npc", Kind: sim.KindNPCStateful, LLMAgent: "salem-vendor", State: sim.StateIdle, Coins: 10},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if _, ok := evt.(*sim.NPCCoinsChanged); ok {
			panic("coins subscriber blew up")
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["npc"].Coins = 12
		return "paid", nil
	}})
	if err != nil || v != "paid" {
		t.Fatalf("command = (%v, %v), want its own result (paid, nil)", v, err)
	}
	snap := w.CommandPanicHealthSnapshot()
	if snap.Count != 1 || snap.LastPanic != "coins subscriber blew up" {
		t.Errorf("snapshot = %+v, want the delta subscriber's panic recorded", snap)
	}
	for _, f := range snap.LastFrames {
		if strings.Contains(f, "containPanic") || strings.Contains(f, "emitPostCommandDeltas") {
			t.Errorf("frames carry dispatch plumbing: %v", snap.LastFrames)
		}
	}
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("command after the delta panic: %v", err)
	}
}

// TestRun_DeltaPanicSkipsLaterEmittersWithoutReplay: a panic in a needs-delta
// subscriber skips the coins emitter for that command, a later no-op command
// does not replay the lost deltas, and dispatch state is restored (the next
// event is a fresh origin).
func TestRun_DeltaPanicSkipsLaterEmittersWithoutReplay(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {ID: "npc", Kind: sim.KindNPCStateful, LLMAgent: "salem-vendor", State: sim.StateIdle, Coins: 10,
			Needs: map[sim.NeedKey]int{"hunger": 2}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	coinsEvents := 0
	var later []sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		switch evt.(type) {
		case *sim.NPCNeedsChanged:
			panic("needs subscriber blew up")
		case *sim.NPCCoinsChanged:
			coinsEvents++
		case *sim.HuddleLeft:
			later = append(later, evt)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["npc"].Needs["hunger"] = 9
		world.Actors["npc"].Coins = 12
		return nil, nil
	}}); err != nil {
		t.Fatalf("mutating command: %v", err)
	}
	if snap := w.CommandPanicHealthSnapshot(); snap.Count != 1 || snap.LastPanic != "needs subscriber blew up" {
		t.Fatalf("snapshot = %+v, want the needs panic recorded once", snap)
	}
	if coinsEvents != 0 {
		t.Errorf("coins emitter ran %d time(s) after the needs emitter panicked, want 0", coinsEvents)
	}
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("no-op command: %v", err)
	}
	if coinsEvents != 0 {
		t.Errorf("the lost coins delta was replayed by a later command (%d event(s))", coinsEvents)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.HuddleLeft{})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit command: %v", err)
	}
	if len(later) != 1 || later[0].RootEventID() != later[0].EventID() {
		t.Errorf("event after the delta panic must be fresh-origin: %+v", later)
	}
}

// TestRun_FnPanicAndDeltaPanicBothRecorded: a command that changes state and
// then panics, whose delta emit panics as well, records two panics; the reply
// is the Fn's panic and the recorder's latest is the delta's.
func TestRun_FnPanicAndDeltaPanicBothRecorded(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {ID: "npc", Kind: sim.KindNPCStateful, LLMAgent: "salem-vendor", State: sim.StateIdle, Coins: 10},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if _, ok := evt.(*sim.NPCCoinsChanged); ok {
			panic("delta panic")
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["npc"].Coins = 12
		panic("fn panic")
	}})
	var perr *sim.CommandPanicError
	if !errors.As(err, &perr) || perr.Value != "fn panic" {
		t.Fatalf("reply = %v, want the Fn's panic", err)
	}
	snap := w.CommandPanicHealthSnapshot()
	if snap.Count != 2 || snap.LastPanic != "delta panic" {
		t.Errorf("snapshot = %+v, want 2 panics with the delta's latest", snap)
	}
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("command after both panics: %v", err)
	}
}
