package main

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stop.go — the two ways the engine can be asked to stop (LLM-404).
//
// The failure this exists for: a checkpoint that has been failing for hours is
// operationally harmless — the world is in memory and the engine is perfectly
// happy. ALL of the damage lands at the moment the process exits, because the
// restart boots onto the last GOOD checkpoint and silently rolls the village
// back to it (the 2026-07-12 outage discarded ~17.5 hours that way). So the
// gate belongs on the exit, not on the checkpoint.
//
// A graceful stop therefore checkpoints FIRST, with the whole engine still
// running, and refuses to proceed if that write fails. Because nothing has been
// torn down at that point, the abort needs no recovery path at all: the world
// goroutine, the tick pool, the tickers, the periodic checkpointer and the HTTP
// surface are all still up and simply keep running. (This is why the gate runs
// before the teardown rather than at the final checkpoint inside it: the
// teardown is one-way. handlers.TickWorkerPool is explicitly non-restartable —
// Start panics after Stop — and an http.Server cannot be reused after Shutdown,
// so an abort discovered mid-teardown would have nothing left to resume.)

// stopMode is what the requester is willing to lose.
type stopMode int

const (
	// stopGraceful will not exit onto a stale checkpoint. If the world cannot be
	// durably saved, the stop is ABORTED and the engine keeps running. This is
	// what a deploy asks for — it wants the process down, but never at the cost
	// of the village. Triggered by the graceful-stop signal (SIGWINCH; see
	// stop_signals_unix.go).
	stopGraceful stopMode = iota

	// stopForce exits regardless of whether the world could be saved — "I accept
	// the loss", rolling back to whatever the last good checkpoint holds. This is
	// SIGINT/SIGTERM, i.e. plain `systemctl stop`, and is the pre-LLM-404
	// behaviour unchanged. It stays the escape hatch for when the process must
	// come down and durability cannot be repaired.
	stopForce
)

func (m stopMode) String() string {
	if m == stopForce {
		return "force"
	}
	return "graceful"
}

// stopRequest is one request to stop the engine. Sent by the signal handler in
// main; consumed by run's stop gate. A graceful request that is aborted leaves
// the engine running and waiting for the next one, so the channel carries a
// STREAM of requests rather than a single close.
type stopRequest struct {
	mode stopMode
}

// discardedSince renders how much world state exiting right now would throw
// away: the age of the last GOOD checkpoint, which is exactly what a restart
// would boot onto. A zero LastSuccessAt means this process has never written a
// checkpoint successfully, so there is no bound to state at all — say so rather
// than printing an age measured from 1970.
func discardedSince(health *sim.CheckpointHealth, now time.Time) string {
	last := health.Snapshot().LastSuccessAt
	if last.IsZero() {
		return "an unknown amount (no checkpoint has succeeded since boot)"
	}
	return now.Sub(last).Round(time.Second).String() + " of world state"
}

// logShutdownSummary emits the single line the deploy reads back out of the
// journal to report what a stop actually cost (LLM-404). One line, stable
// prefix, key=value fields — the playbook greps for it and echoes it after
// "deploy complete", so a force stop that discarded hours of village cannot
// pass as a clean deploy.
//
// Deliberately machine-readable AND readable: the operator holding this at 3am
// is as likely to be a human reading journalctl as the playbook.
func logShutdownSummary(mode stopMode, health *sim.CheckpointHealth, clamps *sim.ClampReport, took time.Duration, err error) {
	if err != nil {
		log.Printf("engine: shutdown summary: mode=%s checkpoint=FAILED discarded=%q error=%q",
			mode, discardedSince(health, time.Now()), err.Error())
		return
	}
	log.Printf("engine: shutdown summary: mode=%s checkpoint=ok duration=%s clamps=%d discarded=none",
		mode, took.Round(time.Millisecond), clamps.Total())
}
