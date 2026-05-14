package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// worker_test.go — the worker body: it runs a job through the runner,
// reports completion via CompleteReactorTick, and emits the lifecycle
// telemetry (started + completed / failed / stale).

func TestWorkerRunsJobAndCompletesTick(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	setInFlight(t, w, "A1")

	runner := &fakeRunner{
		result: sim.TickResult{TerminalStatus: sim.TickStatusSuccess},
		called: make(chan tickJob, 1),
	}
	p := newPoolWithRunner(w, tel, runner)
	p.Start(context.Background())
	defer func() { p.Stop(); p.Wait() }()

	p.jobs <- tickJob{actorID: "alice", attemptID: "A1"}

	select {
	case <-runner.called:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was never invoked")
	}
	// `completed` is the worker's last step — wait on it, not on the
	// actor's in-flight flag, which clears one step earlier (inside
	// CompleteReactorTick, before the telemetry write).
	eventually(t, "completed telemetry written", func() bool {
		return contains(tel.kinds(), telemetryCompleted)
	})
	if !contains(tel.kinds(), telemetryStarted) {
		t.Fatalf("expected started telemetry too, got %v", tel.kinds())
	}
	if actorTickInFlight(t, w) {
		t.Fatal("CompleteReactorTick did not clear the actor's in-flight state")
	}
}

func TestWorkerFailedStatusTelemetry(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	setInFlight(t, w, "A1")

	runner := &fakeRunner{
		result: sim.TickResult{TerminalStatus: sim.TickStatusFailedAfterRender},
		called: make(chan tickJob, 1),
	}
	p := newPoolWithRunner(w, tel, runner)
	p.Start(context.Background())
	defer func() { p.Stop(); p.Wait() }()

	p.jobs <- tickJob{actorID: "alice", attemptID: "A1"}
	<-runner.called

	eventually(t, "failed telemetry written", func() bool {
		return contains(tel.kinds(), telemetryFailed)
	})
}

// TestWorkerStaleCompletionTelemetry: the runner supersedes the attempt
// while it is "running" — by the time the worker calls CompleteReactorTick,
// the actor's current attempt has moved on, so the completion is stale and
// the worker records `stale` rather than `completed`.
func TestWorkerStaleCompletionTelemetry(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	setInFlight(t, w, "A1")

	runner := &fakeRunner{
		result: sim.TickResult{TerminalStatus: sim.TickStatusSuccess},
		called: make(chan tickJob, 1),
		onRun: func(world *sim.World, _ tickJob) {
			_, _ = world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
				wd.Actors["alice"].TickAttemptID = "A2"
				return nil, nil
			}})
		},
	}
	p := newPoolWithRunner(w, tel, runner)
	p.Start(context.Background())
	defer func() { p.Stop(); p.Wait() }()

	p.jobs <- tickJob{actorID: "alice", attemptID: "A1"}
	<-runner.called

	eventually(t, "stale telemetry written", func() bool {
		return contains(tel.kinds(), telemetryStale)
	})
}

// TestStubRunnerCompletesTickEndToEnd drives the whole PR 3b pipeline with
// the real NewTickWorkerPool (stub runner): the evaluator admits → the
// subscriber enqueues → a worker runs the stub → CompleteReactorTick clears
// the actor's in-flight state.
func TestStubRunnerCompletesTickEndToEnd(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		RegisterTickHandlers(world, p)
		return nil, nil
	}}); err != nil {
		t.Fatalf("RegisterTickHandlers: %v", err)
	}
	p.Start(context.Background())
	defer func() { p.Stop(); p.Wait() }()

	now := time.Now().UTC()
	seedDueWarrant(t, w, now)
	if _, err := w.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	eventually(t, "tick completed end-to-end", func() bool {
		return contains(tel.kinds(), telemetryCompleted)
	})
	if actorTickInFlight(t, w) {
		t.Fatal("end-to-end tick did not clear the actor's in-flight state")
	}
}
