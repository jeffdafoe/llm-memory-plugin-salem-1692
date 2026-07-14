package sim

import (
	"context"
	"testing"
	"time"
)

// world_command_probe_test.go — the world-command liveness probe (LLM-402).
//
// These tests drive the RECORDER and one real probe against a real World, rather
// than waiting on the 15s production cadence: probeWorldCommandWithTimeout is the unit that
// matters (RunWorldCommandProbe is a time.Ticker loop around it), and a test that
// slept for a cadence would be a slow test that proved less.

func TestWorldCommandHealth_NilIsSafeAndSilent(t *testing.T) {
	var h *WorldCommandHealth
	// Every method must be a no-op on a nil recorder — an engine wired without one
	// must not panic the umbilical, which evaluates alarms on EVERY response.
	h.RecordAttempt(time.Now())
	h.RecordSuccess(time.Now(), time.Second)
	h.RecordTimeout(time.Now(), WorldCommandPhaseReply, context.DeadlineExceeded)

	snap := h.Snapshot()
	if snap.ConsecutiveTimeouts != 0 {
		t.Errorf("nil recorder reported a timeout streak of %d", snap.ConsecutiveTimeouts)
	}
	// The constants still come through, so the route is self-describing even here.
	if snap.ProbeTimeoutSeconds != WorldCommandProbeTimeout.Seconds() {
		t.Errorf("ProbeTimeoutSeconds=%v, want %v", snap.ProbeTimeoutSeconds, WorldCommandProbeTimeout.Seconds())
	}
}

// The streak-start stamp is what the alarm reports as "since", so it must be
// written ONCE per streak and left alone while the streak stands — otherwise the
// alarm's Since would creep forward with every probe and an operator watching the
// banner would never learn how long the world had been down.
func TestWorldCommandHealth_StreakStartIsStampedOnceAndClearedOnSuccess(t *testing.T) {
	h := &WorldCommandHealth{}
	t0 := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	h.RecordTimeout(t0, WorldCommandPhaseReply, context.DeadlineExceeded)
	h.RecordTimeout(t0.Add(15*time.Second), WorldCommandPhaseReply, context.DeadlineExceeded)
	h.RecordTimeout(t0.Add(30*time.Second), WorldCommandPhaseReply, context.DeadlineExceeded)

	snap := h.Snapshot()
	if snap.ConsecutiveTimeouts != 3 {
		t.Errorf("ConsecutiveTimeouts=%d, want 3", snap.ConsecutiveTimeouts)
	}
	if !snap.TimeoutStreakStartedAt.Equal(t0) {
		t.Errorf("TimeoutStreakStartedAt=%v, want the FIRST timeout %v — the stamp moved with the streak",
			snap.TimeoutStreakStartedAt, t0)
	}
	if !snap.LastTimeoutAt.Equal(t0.Add(30 * time.Second)) {
		t.Errorf("LastTimeoutAt=%v, want the most recent timeout", snap.LastTimeoutAt)
	}

	// A recovery clears the streak AND its start stamp, so the next streak dates
	// itself honestly instead of inheriting the old one.
	h.RecordSuccess(t0.Add(45*time.Second), 12*time.Millisecond)
	snap = h.Snapshot()
	if snap.ConsecutiveTimeouts != 0 {
		t.Errorf("ConsecutiveTimeouts=%d after a success, want 0", snap.ConsecutiveTimeouts)
	}
	if !snap.TimeoutStreakStartedAt.IsZero() {
		t.Errorf("TimeoutStreakStartedAt=%v after a success, want zero", snap.TimeoutStreakStartedAt)
	}
	if snap.LastError != "" {
		t.Errorf("LastError=%q after a success, want cleared", snap.LastError)
	}

	h.RecordTimeout(t0.Add(60*time.Second), WorldCommandPhaseEnqueue, context.DeadlineExceeded)
	if got := h.Snapshot().TimeoutStreakStartedAt; !got.Equal(t0.Add(60 * time.Second)) {
		t.Errorf("the new streak inherited the old start stamp: %v", got)
	}
}

// slowestRoundTrip is the early warning the alarm deliberately withholds: it must
// track the high-water mark across successes, not the latest one.
func TestWorldCommandHealth_SlowestRoundTripIsAHighWaterMark(t *testing.T) {
	h := &WorldCommandHealth{}
	now := time.Now()
	h.RecordSuccess(now, 5*time.Millisecond)
	h.RecordSuccess(now, 800*time.Millisecond)
	h.RecordSuccess(now, 20*time.Millisecond)

	snap := h.Snapshot()
	if snap.LastRoundTripMS != 20 {
		t.Errorf("LastRoundTripMS=%v, want the MOST RECENT round-trip (20)", snap.LastRoundTripMS)
	}
	if snap.SlowestRoundTripMS != 800 {
		t.Errorf("SlowestRoundTripMS=%v, want the high-water mark (800)", snap.SlowestRoundTripMS)
	}
}

// The happy path, against a real running world: the probe round-trips and records
// a success. This also pins the invariant that the probe's Fn is safe to run — it
// goes through the FULL loop body (TickCounter++, republish, delta emitters).
func TestProbeWorldCommand_RecordsSuccessAgainstARunningWorld(t *testing.T) {
	w := NewWorld(Repository{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	before := w.WorldCommandHealthSnapshot()
	probeWorldCommandWithTimeout(ctx, w, WorldCommandProbeTimeout)
	after := w.WorldCommandHealthSnapshot()

	if after.TotalProbes != before.TotalProbes+1 {
		t.Errorf("TotalProbes=%d, want one more than %d", after.TotalProbes, before.TotalProbes)
	}
	if after.ConsecutiveTimeouts != 0 {
		t.Errorf("a running world timed out the probe: %+v", after)
	}
	if after.LastSuccessAt.IsZero() {
		t.Error("no success recorded against a running world")
	}
	if after.TotalTimeouts != 0 {
		t.Errorf("TotalTimeouts=%d against a running world, want 0", after.TotalTimeouts)
	}
}

// THE INCIDENT THIS WHOLE TICKET EXISTS FOR: the world command goroutine accepts a
// command and never completes it. The probe must time out in the REPLY phase and
// say so — that phase is what separates a wedged loop from a merely saturated
// queue, and the operator's next move differs.
func TestProbeWorldCommand_RecordsAReplyTimeoutWhenTheWorldIsWedged(t *testing.T) {
	w := NewWorld(Repository{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Wedge the world goroutine inside a command, exactly as a deadlocked handler
	// would. It is released via the channel below so the goroutine does not leak.
	release := make(chan struct{})
	wedged := make(chan struct{})
	w.Submit(func(*World) (any, error) {
		close(wedged)
		<-release
		return nil, nil
	})
	<-wedged
	defer close(release)

	// A short deadline keeps the test fast; the production constant is 5s.
	probeCtx, probeCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer probeCancel()
	probeWorldCommandWithTimeout(probeCtx, w, 100*time.Millisecond)

	snap := w.WorldCommandHealthSnapshot()
	if snap.ConsecutiveTimeouts != 1 {
		t.Fatalf("ConsecutiveTimeouts=%d against a wedged world, want 1: %+v", snap.ConsecutiveTimeouts, snap)
	}
	if snap.LastTimeoutPhase != WorldCommandPhaseReply {
		t.Errorf("LastTimeoutPhase=%q, want %q — the command was accepted and never completed",
			snap.LastTimeoutPhase, WorldCommandPhaseReply)
	}
	if snap.TimeoutStreakStartedAt.IsZero() {
		t.Error("no streak start stamped — the alarm would have no Since to report")
	}
}

// The other failure the probe must not conflate: the queue is full, so the command
// never even gets in. The world loop may be perfectly healthy and simply
// out-produced, and the operator's first move (find what is flooding the queue) is
// different from a wedge's (get a goroutine dump).
func TestProbeWorldCommand_RecordsAnEnqueueTimeoutWhenTheQueueIsSaturated(t *testing.T) {
	w := NewWorld(Repository{})
	// No World.Run at all: nothing drains cmds, so the 256-deep buffer fills and
	// the probe cannot hand its command over — the saturation shape.
	ctx := context.Background()
	for i := 0; i < cap(w.cmds); i++ {
		w.cmds <- Command{Fn: func(*World) (any, error) { return nil, nil }}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	probeWorldCommandWithTimeout(probeCtx, w, 100*time.Millisecond)

	snap := w.WorldCommandHealthSnapshot()
	if snap.ConsecutiveTimeouts != 1 {
		t.Fatalf("ConsecutiveTimeouts=%d against a saturated queue, want 1: %+v", snap.ConsecutiveTimeouts, snap)
	}
	if snap.LastTimeoutPhase != WorldCommandPhaseEnqueue {
		t.Errorf("LastTimeoutPhase=%q, want %q — the probe never got into the queue",
			snap.LastTimeoutPhase, WorldCommandPhaseEnqueue)
	}
}

// A cancelled parent context is a SHUTDOWN, not a stall. Recording it would stamp a
// world_command_stalled alarm onto every clean restart and teach operators to
// ignore the alarm — the one thing a fire alarm must never do.
func TestProbeWorldCommand_ShutdownIsNotRecordedAsAStall(t *testing.T) {
	w := NewWorld(Repository{})
	// A world that was never started and a context already cancelled: the shape of
	// the moment after worldCtx is cancelled and World.Run has returned.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	probeWorldCommandWithTimeout(ctx, w, WorldCommandProbeTimeout)

	snap := w.WorldCommandHealthSnapshot()
	if snap.ConsecutiveTimeouts != 0 {
		t.Errorf("shutdown recorded as a stall (streak=%d) — this would alarm on every restart", snap.ConsecutiveTimeouts)
	}
	if snap.TotalTimeouts != 0 {
		t.Errorf("TotalTimeouts=%d after a shutdown-time probe, want 0", snap.TotalTimeouts)
	}
}

// THE LOAD-BEARING ORDERING: the prober beats BEFORE it sends, so a wedged world
// cannot silence the instrument that detects it. Invert it and ticker_stale and
// world_command_stalled collapse into the same silence — the two would go quiet
// together and the ticket's whole premise (a measurement independent of the
// silence) would be gone.
//
// This drives runWorldCommandProbeOnce — the REAL loop body RunWorldCommandProbe
// calls — rather than hand-beating and then probing, which would merely re-enact the
// ordering and keep passing if someone moved the beat below the probe.
func TestRunWorldCommandProbeOnce_BeatsEvenWhileTheWorldIsWedged(t *testing.T) {
	w := NewWorld(Repository{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Wedge the world goroutine inside a command and hold it there for longer than
	// the probe's deadline: the beat has to land DURING the wedge, not after it.
	release := make(chan struct{})
	wedged := make(chan struct{})
	w.Submit(func(*World) (any, error) {
		close(wedged)
		<-release
		return nil, nil
	})
	<-wedged
	defer close(release)

	runWorldCommandProbeOnce(ctx, w, 100*time.Millisecond)

	var probe TickerHealthEntry
	for _, e := range w.TickerHealthSnapshot() {
		if e.Name == WorldCommandProbeTickerName {
			probe = e
		}
	}
	if probe.Count == 0 {
		t.Fatal("the prober's beat did not land while the world was wedged — a wedged world would silence its own detector, " +
			"and ticker_stale would go quiet at the same moment world_command_stalled fired")
	}
	if w.WorldCommandHealthSnapshot().ConsecutiveTimeouts == 0 {
		t.Error("the probe did not record the wedge")
	}
}

// Shutdown records NOTHING — not an outcome, and not even an attempt. An attempt
// counted against a world that is meant to be gone would leave total_probes and
// last_attempt_at creeping through every clean restart, a small lie on a route whose
// entire job is to be trusted about what the engine is doing.
func TestRunWorldCommandProbeOnce_ShutdownRecordsNoAttempt(t *testing.T) {
	w := NewWorld(Repository{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runWorldCommandProbeOnce(ctx, w, 100*time.Millisecond)

	snap := w.WorldCommandHealthSnapshot()
	if snap.TotalProbes != 0 {
		t.Errorf("TotalProbes=%d after a shutdown-time probe, want 0", snap.TotalProbes)
	}
	if !snap.LastAttemptAt.IsZero() {
		t.Errorf("LastAttemptAt=%v after a shutdown-time probe, want zero", snap.LastAttemptAt)
	}
	if snap.ConsecutiveTimeouts != 0 {
		t.Errorf("shutdown recorded as a stall (streak=%d)", snap.ConsecutiveTimeouts)
	}
}
