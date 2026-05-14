package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// tick_tool_command_test.go — RunTickToolCommand: the attempt-guarded,
// root-inheriting wrapper PR 3's off-world worker routes every world-
// mutating tool call through. The guard runs on the world goroutine
// against live actor state; a stale attempt must never run its tool.

// buildToolCmdWorld seeds a running world with one in-flight actor and
// emits a single event so EventID(1) is a valid inherited root. Returns
// the world, its cancel, and that valid root EventID.
func buildToolCmdWorld(t *testing.T, attemptID sim.TickAttemptID) (*sim.World, context.CancelFunc, sim.EventID) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Alice"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	var root sim.EventID
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.TickInFlight = true
			a.TickAttemptID = attemptID
			// Emit one event so eventSeq >= 1 and EventID(1) is a valid
			// inherited root for newRootedCommand's root <= eventSeq check.
			sim.EmitForTest(world, &sim.ReactorTickDue{ActorID: "alice"})
			root = sim.EventID(sim.WorldEventSeq(world))
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed in-flight actor: %v", err)
	}
	return w, cancel, root
}

func TestRunTickToolCommandRunsToolForCurrentAttempt(t *testing.T) {
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()

	ran := false
	tool := sim.Command{Fn: func(*sim.World) (any, error) {
		ran = true
		return "tool-ran", nil
	}}

	val, err := w.Send(sim.RunTickToolCommand("alice", "A1", root, tool))
	if err != nil {
		t.Fatalf("RunTickToolCommand: unexpected error %v", err)
	}
	if !ran {
		t.Fatal("tool.Fn was not run for the current attempt")
	}
	if val != "tool-ran" {
		t.Fatalf("expected tool's value to pass through, got %v", val)
	}
}

func TestRunTickToolCommandRejectsStaleAttempt(t *testing.T) {
	// Actor is in-flight under "A2"; the worker's command is for "A1".
	w, cancel, root := buildToolCmdWorld(t, "A2")
	defer cancel()

	ran := false
	tool := sim.Command{Fn: func(*sim.World) (any, error) {
		ran = true
		return nil, nil
	}}

	_, err := w.Send(sim.RunTickToolCommand("alice", "A1", root, tool))
	if !errors.Is(err, sim.ErrTickAttemptStale) {
		t.Fatalf("expected ErrTickAttemptStale, got %v", err)
	}
	if ran {
		t.Fatal("tool.Fn ran for a stale attempt — the guard must block it")
	}
}

func TestRunTickToolCommandRejectsIdleActor(t *testing.T) {
	// Actor in-flight under "A1", then completes — TickInFlight clears. A
	// late tool command for "A1" must be rejected: the zero TickAttemptID
	// is also "", so the TickInFlight half of the guard is load-bearing.
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.TickInFlight = false
		a.TickAttemptID = ""
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear in-flight: %v", err)
	}

	ran := false
	tool := sim.Command{Fn: func(*sim.World) (any, error) {
		ran = true
		return nil, nil
	}}

	_, err := w.Send(sim.RunTickToolCommand("alice", "A1", root, tool))
	if !errors.Is(err, sim.ErrTickAttemptStale) {
		t.Fatalf("expected ErrTickAttemptStale for an idle actor, got %v", err)
	}
	if ran {
		t.Fatal("tool.Fn ran against an idle actor")
	}
}

func TestRunTickToolCommandRejectsNilTool(t *testing.T) {
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()

	// sim.Command{} has a nil Fn — a harness bug. RunTickToolCommand must
	// fail the tick with a plain error, never panic the world goroutine on
	// the nil-Fn dereference. (If it panicked, w.Run's goroutine would die
	// and this Send would hang — so reaching the assertions at all is part
	// of the check.)
	_, err := w.Send(sim.RunTickToolCommand("alice", "A1", root, sim.Command{}))
	if err == nil {
		t.Fatal("expected an error for a nil tool command")
	}
	if errors.Is(err, sim.ErrTickAttemptStale) {
		t.Fatal("a nil tool command should be a plain error, not ErrTickAttemptStale")
	}
}

func TestRunTickToolCommandUnknownActor(t *testing.T) {
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()

	tool := sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}

	_, err := w.Send(sim.RunTickToolCommand("ghost", "A1", root, tool))
	if err == nil {
		t.Fatal("expected an error for an unknown actor")
	}
	if errors.Is(err, sim.ErrTickAttemptStale) {
		t.Fatal("an unknown actor should be a plain error, not ErrTickAttemptStale")
	}
}
