package sim

import (
	"context"
	"log"
	"sync"
	"time"
)

// world_command_probe.go — a DIRECT liveness measurement of the single world
// command goroutine (LLM-402).
//
// Every mutation in the engine goes through one goroutine (World.Run), and every
// one of the ~36 cadence tickers drives its work by sending to it. So when that
// goroutine wedges, all of them starve at once — and until now the only evidence
// was their silence.
//
// SILENCE IS AN INFERENCE, NOT A MEASUREMENT. Stale tickers cannot tell a wedged
// world loop from process-wide scheduler starvation, a long stop-the-world GC, a
// lock convoy, or several unlucky per-ticker panics: all of them look identical
// from the ticker registry. An operator holding an all-stale ticker_stale alarm
// at 3am knows something upstream is wrong but not what, and the alarm's prose
// can only hedge. Staleness is also a SLOW instrument for this case by
// construction — the deadline is max(3 x interval, 2m), so the fastest a wedge
// can possibly surface is ~2 minutes.
//
// This file replaces the inference with a measurement: enqueue a no-op command on
// the world and require the round-trip to land inside a deadline. If it does not,
// the world command path is not serving commands. No inference, and ~20-35s
// rather than 2+ minutes.
//
// WHAT A MISSED DEADLINE ACTUALLY PROVES — and what it does not. It proves the
// command path missed its SERVICE deadline. That covers a genuinely wedged
// handler, a wedge inside republish or an event subscriber, AND a command queue
// so saturated the probe cannot even get in. Those are different diseases with
// different fixes, so the probe records WHICH HALF of the round-trip expired (see
// WorldCommandPhase) instead of collapsing them into "the world is stuck". The
// alarm text is careful about this too: stalled OR saturated, never a claim the
// evidence cannot carry.

// WorldCommandProbeInterval is how often the prober enqueues its no-op command.
//
// 15s, against a ticker_stale floor of 2 minutes: the point of this instrument is
// to be FAST for the one incident staleness is slowest at. It is also cheap — one
// no-op command every 15s, against a reactor evaluator that sends commands at
// 250ms.
const WorldCommandProbeInterval = 15 * time.Second

// WorldCommandProbeTimeout is how long one probe round-trip may take before it is
// judged a timeout.
//
// 5s is roughly ten times any legitimate round-trip: commands are contractually
// non-blocking (no I/O inside a Command.Fn — see the World docs), the queue is 256
// deep, and the heaviest thing that ever runs in-band on the world goroutine is
// the checkpoint's deep clone, which is sub-second. The headroom is deliberate.
// The failure this must never produce is a false alarm: the umbilical alarm
// surface is worth exactly as much as its silence, and a probe that cries wolf on
// a GC pause would spend that down (see httpapi/alarms.go).
const WorldCommandProbeTimeout = 5 * time.Second

// WorldCommandProbeTickerName is the ticker-registry name the prober beats under.
// Exported because the ticker_stale alarm must EXCLUDE it from its "every ticker
// is stale" judgement — see httpapi/alarms.go.
//
// The prober is a REGISTERED ticker, so ticker_stale watches the watchman: if the
// prober goroutine itself dies, its silence raises the staleness alarm, and the
// direct instrument cannot fail silently.
//
// That mutual check only works because the prober BEATS BEFORE IT SENDS (see
// RunWorldCommandProbe). If it beat on success, a wedged world would silence the
// beat too, both instruments would collapse into the same silence, and the whole
// exercise would buy nothing. Beating first, the two signals stay independent and
// say different things:
//
//   - probe ticker fresh + probe timing out  ⇒ the prober is alive; the WORLD is not.
//   - probe ticker stale                     ⇒ the PROBER is dead; judge nothing from its silence.
const WorldCommandProbeTickerName = "world_command_probe"

// WorldCommandPhase is which half of the probe's round-trip expired. The two
// halves are different failures and the operator's next move differs, so the
// recorder keeps them apart rather than reporting an undifferentiated timeout.
type WorldCommandPhase string

const (
	// WorldCommandPhaseEnqueue — the probe could not even hand its command to the
	// world: the 256-deep cmds channel stayed full for the entire deadline. The
	// world loop may well be running; it is being out-produced (or is wedged and
	// the queue has backed up behind it). Look at what is flooding the queue.
	WorldCommandPhaseEnqueue WorldCommandPhase = "enqueue"

	// WorldCommandPhaseReply — the command was accepted but never completed. The
	// world goroutine took it and did not come back inside the deadline: a wedged
	// Command.Fn, a wedge in republish or an event subscriber, or a process starved
	// hard enough to not schedule the goroutine at all. This is the true wedge.
	WorldCommandPhaseReply WorldCommandPhase = "reply"
)

// WorldCommandHealth is the operator-visible liveness of the world command path,
// recorded by RunWorldCommandProbe and read by the umbilical (the
// world_command_stalled alarm and /umbilical/world-command-health).
//
// Same shape and same posture as CheckpointHealth: mutex-guarded (the prober
// writes, HTTP request goroutines read), nil-safe on every method, and in-memory
// and lossy-on-restart by design — this is transient diagnostics with no
// durability need (shared GUIDELINES: "Postgres is for durable storage, not an
// infrastructure substitute"). A restart cannot hide an ONGOING stall: the next
// probe re-detects it within a cadence.
//
// It lives on the World rather than being constructed in cmd/engine and injected
// (as CheckpointHealth is), because it describes the world's own command loop.
// That means every wiring — production, headless, tests — has it without plumbing,
// exactly like the TickerHealth registry next to it.
type WorldCommandHealth struct {
	mu sync.Mutex

	lastAttemptAt time.Time
	lastSuccessAt time.Time
	lastTimeoutAt time.Time

	consecutiveTimeouts int

	// timeoutStreakStartedAt is when the CURRENT run of consecutive timeouts
	// began — the moment the world command path stopped serving, as best the
	// prober can date it. It is what the alarm reports as Since.
	//
	// Not lastSuccessAt, which is "last known GOOD" — a different question, and a
	// stale one: it is up to a full probe interval older than the failure, and on
	// an engine whose world loop never came up at all there is no success to point
	// at. Not "now" either: the alarm evaluator is stateless and re-derives the
	// alarm on every umbilical request, so a Since computed at request time would
	// walk forward on every poll. Cleared on success, stamped once per streak.
	timeoutStreakStartedAt time.Time

	totalProbes   uint64
	totalTimeouts uint64

	lastRoundTrip time.Duration

	// slowestRoundTrip is the high-water mark across SUCCESSFUL round-trips. The
	// early warning the alarm deliberately does not give: a world loop whose
	// probes are landing at 4.5s of a 5s budget is about to fall over, and this is
	// where an operator sees that coming. Not an alarm — a successful probe is a
	// successful probe, and a fire alarm that fires on "getting slow" is a fire
	// alarm nobody trusts.
	slowestRoundTrip time.Duration

	lastError        string
	lastTimeoutPhase WorldCommandPhase
}

// WorldCommandHealthSnapshot is an immutable point-in-time copy of a
// WorldCommandHealth, suitable for serialization to the umbilical.
type WorldCommandHealthSnapshot struct {
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	LastTimeoutAt time.Time `json:"last_timeout_at"`

	ConsecutiveTimeouts    int       `json:"consecutive_timeouts"`
	TimeoutStreakStartedAt time.Time `json:"timeout_streak_started_at"`

	TotalProbes   uint64 `json:"total_probes"`
	TotalTimeouts uint64 `json:"total_timeouts"`

	LastRoundTripMS    float64 `json:"last_round_trip_ms"`
	SlowestRoundTripMS float64 `json:"slowest_round_trip_ms"`

	LastError        string            `json:"last_error,omitempty"`
	LastTimeoutPhase WorldCommandPhase `json:"last_timeout_phase,omitempty"`

	// ProbeIntervalSeconds and ProbeTimeoutSeconds are echoed so the reader can
	// judge the numbers above without knowing the engine's constants by heart —
	// the same reason TickerHealthEntryDTO carries its declared interval.
	ProbeIntervalSeconds float64 `json:"probe_interval_seconds"`
	ProbeTimeoutSeconds  float64 `json:"probe_timeout_seconds"`
}

// newWorldCommandHealth returns an initialized recorder. Called from NewWorld, so
// every World (production and test) has one.
func newWorldCommandHealth() *WorldCommandHealth {
	return &WorldCommandHealth{}
}

// RecordAttempt stamps the start of one probe. Called BEFORE the send, so
// lastAttemptAt advances even while the world is wedged and the round-trip never
// returns — otherwise an operator looking at the route mid-stall would see a
// last-attempt time frozen at the last SUCCESS and reasonably conclude the prober
// had died too. Nil-safe.
func (h *WorldCommandHealth) RecordAttempt(now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAttemptAt = now
	h.totalProbes++
}

// RecordSuccess marks a completed round-trip: clears the timeout streak (and the
// streak-start stamp with it, so the next streak dates itself honestly) and the
// last error. Nil-safe.
func (h *WorldCommandHealth) RecordSuccess(now time.Time, roundTrip time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSuccessAt = now
	h.consecutiveTimeouts = 0
	h.timeoutStreakStartedAt = time.Time{}
	h.lastRoundTrip = roundTrip
	if roundTrip > h.slowestRoundTrip {
		h.slowestRoundTrip = roundTrip
	}
	h.lastError = ""
	h.lastTimeoutPhase = ""
}

// RecordTimeout marks a probe that missed its deadline in the given phase,
// advancing the consecutive-timeout streak and stamping the streak's start on the
// FIRST timeout of the run — that stamp is what the alarm reports as "since", so
// it must be written once per streak and never overwritten while the streak
// stands. Nil-safe.
func (h *WorldCommandHealth) RecordTimeout(now time.Time, phase WorldCommandPhase, err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.consecutiveTimeouts == 0 {
		h.timeoutStreakStartedAt = now
	}
	h.consecutiveTimeouts++
	h.totalTimeouts++
	h.lastTimeoutAt = now
	h.lastTimeoutPhase = phase
	if err != nil {
		h.lastError = err.Error()
	}
}

// Snapshot returns an immutable copy of the current health. Nil-safe (returns the
// zero value), so the umbilical route and the alarm evaluator work on a world that
// never launched a prober — a zero snapshot has a zero streak, so it never fires.
func (h *WorldCommandHealth) Snapshot() WorldCommandHealthSnapshot {
	out := WorldCommandHealthSnapshot{
		ProbeIntervalSeconds: WorldCommandProbeInterval.Seconds(),
		ProbeTimeoutSeconds:  WorldCommandProbeTimeout.Seconds(),
	}
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out.LastAttemptAt = h.lastAttemptAt
	out.LastSuccessAt = h.lastSuccessAt
	out.LastTimeoutAt = h.lastTimeoutAt
	out.ConsecutiveTimeouts = h.consecutiveTimeouts
	out.TimeoutStreakStartedAt = h.timeoutStreakStartedAt
	out.TotalProbes = h.totalProbes
	out.TotalTimeouts = h.totalTimeouts
	out.LastRoundTripMS = float64(h.lastRoundTrip.Microseconds()) / 1000
	out.SlowestRoundTripMS = float64(h.slowestRoundTrip.Microseconds()) / 1000
	out.LastError = h.lastError
	out.LastTimeoutPhase = h.lastTimeoutPhase
	return out
}

// WorldCommandHealthSnapshot returns the current liveness view of the world
// command path. Read by the world_command_stalled alarm evaluator and the
// /umbilical/world-command-health route; safe to call from any goroutine.
func (w *World) WorldCommandHealthSnapshot() WorldCommandHealthSnapshot {
	return w.worldCmdHealth.Snapshot()
}

// RunWorldCommandProbe drives the liveness probe. The caller starts it in a
// goroutine alongside the other tickers (cmd/engine's startTickers); it returns
// when ctx is cancelled.
//
// One probe = one no-op command, round-tripped under a deadline.
//
// THE NO-OP COMMAND IS NOT A NO-OP TO THE LOOP, AND THAT IS THE POINT. World.Run
// runs the whole loop body after every Fn — TickCounter++, republish(), and the
// three delta emitters — so an empty Fn still measures the ENTIRE iteration. A
// probe that measured only Fn would sail through a world loop wedged inside
// republish or an event subscriber, which is precisely a stall an operator needs
// to know about. The price is one extra republish per interval and a TickCounter
// that advances ~4/min on nothing; the loop already runs at 250ms cadence from the
// reactor alone, so both are noise.
//
// DO NOT PUT SIDE EFFECTS IN THE PROBE'S Fn. A timed-out probe's command is still
// sitting in the queue and WILL execute once the world recovers, long after the
// prober stopped waiting for it (its reply channel is buffered, so the late reply
// neither leaks a goroutine nor blocks the world). That is harmless for a command
// that does nothing, and only for a command that does nothing.
func RunWorldCommandProbe(ctx context.Context, w *World) {
	// Beat and register are deliberately separate: RegisterCoreTickers declares
	// this ticker's cadence BEFORE this goroutine is launched, so a prober that
	// never starts is stale from its registration stamp onward rather than
	// invisible (see ticker_health.go).
	ticker := time.NewTicker(WorldCommandProbeInterval)
	defer ticker.Stop()
	log.Printf("sim/world-probe: world command liveness probe started (interval %s, deadline %s)",
		WorldCommandProbeInterval, WorldCommandProbeTimeout)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Beat FIRST, before the send that may never return. See
			// WorldCommandProbeTickerName: a wedged world must not be able to
			// silence the instrument that detects it.
			w.beatTicker(WorldCommandProbeTickerName)
			probeWorldCommand(ctx, w)
		}
	}
}

// probeWorldCommand runs exactly one probe at the production deadline.
func probeWorldCommand(ctx context.Context, w *World) {
	probeWorldCommandWithTimeout(ctx, w, WorldCommandProbeTimeout)
}

// probeWorldCommandWithTimeout runs exactly one probe under the given deadline and
// records its outcome. The deadline is a parameter purely so the tests can drive
// the real code path in milliseconds instead of sleeping through the production
// constant; nothing in the engine calls it with anything but WorldCommandProbeTimeout.
//
// IT HAND-ROLLS THE SEND rather than calling SendContext, and that is the point.
// SendContext collapses both halves of the round-trip into a bare ctx.Err(), so a
// caller cannot tell a command it could not ENQUEUE (the queue is saturated — the
// world loop may be running perfectly and simply being out-produced) from one that
// was accepted and never COMPLETED (the true wedge). Those are different failures
// with different first moves for the operator, so the probe keeps them apart. The
// send semantics are otherwise identical to SendContext's, including the buffered
// reply channel that keeps a late reply from ever blocking the world goroutine.
func probeWorldCommandWithTimeout(ctx context.Context, w *World, timeout time.Duration) {
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	w.worldCmdHealth.RecordAttempt(time.Now().UTC())

	reply := make(chan CommandResult, 1)
	cmd := Command{
		Fn:    func(*World) (any, error) { return nil, nil },
		Reply: reply,
	}

	start := time.Now()
	select {
	case w.cmds <- cmd:
	case <-sendCtx.Done():
		// A cancelled PARENT is a shutdown, not a stall: the world goroutine is
		// MEANT to be gone. Recording it would stamp a world_command_stalled alarm
		// onto the engine's last moments and — far worse — teach operators that the
		// alarm fires on clean restarts, which is how a fire alarm becomes furniture.
		// Same posture as RunCheckpointer's shutdown skip.
		if ctx.Err() != nil {
			return
		}
		w.worldCmdHealth.RecordTimeout(time.Now().UTC(), WorldCommandPhaseEnqueue, sendCtx.Err())
		log.Printf("sim/world-probe: TIMEOUT enqueueing probe command after %s — the world command queue is saturated", timeout)
		return
	}

	select {
	case <-reply:
		w.worldCmdHealth.RecordSuccess(time.Now().UTC(), time.Since(start))
	case <-sendCtx.Done():
		if ctx.Err() != nil {
			return
		}
		w.worldCmdHealth.RecordTimeout(time.Now().UTC(), WorldCommandPhaseReply, sendCtx.Err())
		log.Printf("sim/world-probe: TIMEOUT awaiting probe command completion after %s — the world command loop accepted the command and did not complete it", timeout)
	}
}
