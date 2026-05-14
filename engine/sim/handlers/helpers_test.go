package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// helpers_test.go — shared test infrastructure for the handlers package:
// a recording telemetry sink, a fake tickRunner, and world/actor setup.

// recordingTelemetry captures TickTelemetryRecords. Safe for concurrent
// writes from worker goroutines.
type recordingTelemetry struct {
	mu      sync.Mutex
	records []sim.TickTelemetryRecord
}

func (r *recordingTelemetry) WriteTickTelemetry(rec sim.TickTelemetryRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
}

func (r *recordingTelemetry) snapshot() []sim.TickTelemetryRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.TickTelemetryRecord(nil), r.records...)
}

// kinds returns the Kind of every recorded record, in write order.
func (r *recordingTelemetry) kinds() []string {
	recs := r.snapshot()
	out := make([]string, len(recs))
	for i, rec := range recs {
		out[i] = rec.Kind
	}
	return out
}

// fakeRunner is a tickRunner test double. onRun, if set, runs (on the
// worker goroutine) before the result is returned — tests use it to mutate
// the world mid-tick, e.g. to supersede the attempt. called, if set,
// receives the job after onRun so a test can synchronize on "runner ran".
type fakeRunner struct {
	result sim.TickResult
	onRun  func(w *sim.World, job tickJob)
	called chan tickJob
}

func (f *fakeRunner) RunTick(_ context.Context, w *sim.World, job tickJob) sim.TickResult {
	if f.onRun != nil {
		f.onRun(w, job)
	}
	if f.called != nil {
		f.called <- job
	}
	return f.result
}

// newTestWorld seeds a running world with a single idle actor "alice" and
// the given TickWorkerCount (pass 0 to leave it unset and exercise the
// default).
func newTestWorld(t *testing.T, workerCount int) (*sim.World, *recordingTelemetry, context.CancelFunc) {
	t.Helper()
	return newTestWorldWithActors(t, []sim.ActorID{"alice"}, workerCount)
}

// newTestWorldWithActors seeds a running world with one idle actor per id
// and the given TickWorkerCount (pass 0 to leave it unset and exercise the
// default). The returned telemetry sink is wired as repo.TickTelemetry.
func newTestWorldWithActors(t *testing.T, ids []sim.ActorID, workerCount int) (*sim.World, *recordingTelemetry, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	actors := make(map[sim.ActorID]*sim.Actor, len(ids))
	for _, id := range ids {
		actors[id] = &sim.Actor{ID: id, DisplayName: string(id)}
	}
	handles.Actors.Seed(actors)
	tel := &recordingTelemetry{}
	repo.TickTelemetry = tel

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	if workerCount > 0 {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Settings.TickWorkerCount = workerCount
			return nil, nil
		}}); err != nil {
			cancel()
			t.Fatalf("set TickWorkerCount: %v", err)
		}
	}
	return w, tel, cancel
}

// setInFlight marks alice in-flight under attemptID — the precondition for
// CompleteReactorTick to treat a completion as non-stale.
func setInFlight(t *testing.T, w *sim.World, attemptID sim.TickAttemptID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.TickInFlight = true
		a.TickAttemptID = attemptID
		return nil, nil
	}}); err != nil {
		t.Fatalf("setInFlight: %v", err)
	}
}

// actorTickInFlight reads alice's TickInFlight flag from the world goroutine.
func actorTickInFlight(t *testing.T, w *sim.World) bool {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["alice"].TickInFlight, nil
	}})
	if err != nil {
		t.Fatalf("actorTickInFlight: %v", err)
	}
	return v.(bool)
}

// seedDueWarrant hand-stamps a single due (now-1ms) warrant cycle on alice
// so an immediate EvaluateReactors(now) considers her.
func seedDueWarrant(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	seedDueWarrantFor(t, w, "alice", now)
}

// seedDueWarrantFor is seedDueWarrant for an arbitrary actor id.
func seedDueWarrantFor(t *testing.T, w *sim.World, id sim.ActorID, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		since := now.Add(-50 * time.Millisecond)
		due := now.Add(-time.Millisecond)
		a.WarrantedSince = &since
		a.WarrantDueAt = &due
		a.Warrants = []sim.WarrantMeta{{
			TriggerActorID: "bob",
			Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke},
		}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedDueWarrantFor(%s): %v", id, err)
	}
}

// eventually polls cond up to a 2s deadline, failing the test if it never
// holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// contains reports whether s appears in ss.
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// assertPanics fails the test unless fn panics.
func assertPanics(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected a panic for %s", what)
		}
	}()
	fn()
}
