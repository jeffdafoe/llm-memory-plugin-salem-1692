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
	// LLM-88: the result is wrapped in a TickToolResult — the tool's own value
	// plus the acting actor's post-commit snapshot (used for mid-tick own-state
	// re-perception).
	res, ok := val.(sim.TickToolResult)
	if !ok {
		t.Fatalf("expected a sim.TickToolResult envelope, got %T", val)
	}
	if res.Result != "tool-ran" {
		t.Fatalf("expected tool's value to pass through, got %v", res.Result)
	}
	if res.PostActorSnapshot == nil {
		t.Fatal("expected PostActorSnapshot to be captured for the acting actor")
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

func TestRunTickToolCommandTagsToolErrorAsModelFacing(t *testing.T) {
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()

	// A command validator's rejection is the model-facing reason the LLM needs
	// to correct its next call. It must reach the harness tagged ModelFacingError
	// so the harness echoes it (see harness.go's errors.As gate). Returning a
	// generic label instead is the bug that trapped NPCs retrying forever.
	wantMsg := `no one named "Ellis Farm" in this conversation`
	tool := sim.Command{Fn: func(*sim.World) (any, error) {
		return nil, errors.New(wantMsg)
	}}

	_, err := w.Send(sim.RunTickToolCommand("alice", "A1", root, tool))
	var modelErr sim.ModelFacingError
	if !errors.As(err, &modelErr) {
		t.Fatalf("tool.Fn error should be tagged ModelFacingError, got %T: %v", err, err)
	}
	if modelErr.Error() != wantMsg {
		t.Fatalf("ModelFacingError message = %q, want %q", modelErr.Error(), wantMsg)
	}
}

func TestRunTickToolCommandPreservesTerminalNoOp(t *testing.T) {
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()

	// LLM-209: a no-op rejection (already there / already on break) is a
	// TerminalNoOpError. RunTickToolCommand must preserve its concrete type — NOT
	// flatten it to ModelFacingError — so the harness can distinguish it via
	// errors.As and end the tick on it. If it were flattened, the harness would
	// echo it non-terminally and the weak model would re-fire the identical no-op
	// to the iteration budget.
	wantMsg := `you are already at "inn" — you're right where you meant to be; nothing more to do here.`
	tool := sim.Command{Fn: func(*sim.World) (any, error) {
		return nil, sim.TerminalNoOpError{Msg: wantMsg}
	}}

	_, err := w.Send(sim.RunTickToolCommand("alice", "A1", root, tool))
	var noop sim.TerminalNoOpError
	if !errors.As(err, &noop) {
		t.Fatalf("TerminalNoOpError should survive RunTickToolCommand, got %T: %v", err, err)
	}
	if noop.Error() != wantMsg {
		t.Fatalf("TerminalNoOpError message = %q, want %q", noop.Error(), wantMsg)
	}
	// It must NOT masquerade as a (non-terminal) ModelFacingError.
	var modelErr sim.ModelFacingError
	if errors.As(err, &modelErr) {
		t.Fatalf("TerminalNoOpError must not be tagged ModelFacingError (that path is non-terminal): %v", err)
	}
}

func TestRunTickToolCommandWrapperErrorsNotModelFacing(t *testing.T) {
	w, cancel, root := buildToolCmdWorld(t, "A1")
	defer cancel()

	// Wrapper/internal dispatch errors (here: unknown actor) are NOT model-facing
	// reasons — they must not be echoed into the prompt, so they must not carry
	// ModelFacingError (the harness falls back to a generic label for them).
	tool := sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}
	_, err := w.Send(sim.RunTickToolCommand("ghost", "A1", root, tool))
	if err == nil {
		t.Fatal("expected an error for an unknown actor")
	}
	var modelErr sim.ModelFacingError
	if errors.As(err, &modelErr) {
		t.Fatalf("unknown-actor dispatch error must NOT be ModelFacingError, got %v", err)
	}
}
