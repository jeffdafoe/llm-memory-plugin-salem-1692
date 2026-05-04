package main

// Buffered chronicler dispatcher (ZBBS-119). Sits in front of the
// chronicler-fire path and coalesces routine village events
// (arrivals, shift boundaries, atmosphere shifts, needs_resolved)
// into a single consolidated chronicler call per window, instead of
// firing a separate cascade scene for each event.
//
// Why: today every cascade-origin event spawns its own chronicler
// fire. Concurrent cascades stomp each other's `attend_to` dispatches
// via the in-flight gate at agent_tick.go:480 — diagnosed
// 2026-05-04 from scene 019df3f0-…-6f60932414f5 where Prudence's
// PW Apothecary arrival cascade fired in parallel with Josiah's
// General Store arrival cascade and the chronicler's attend_to calls
// were silently dropped. Buffering coalesces routine events; full
// serialization of fires lands in a follow-up commit that tightens
// ChroniclerSem.
//
// Design: shared/tasks/pending/salem-chronicler-buffered-dispatch.
//
// State machine:
//
//   idle               — no timer armed, no events pending
//     ↓ notify()
//   armed              — one-shot timer running, events in queue
//     ↓ timer fires
//   firing             — fire() invoked, drains queue via existing
//                        cascadeOriginFireChronicler path
//     ↓ fire returns
//   idle (or armed if  — next notify after fire start re-arms the timer
//        notify raced)
//
// The dispatcher holds NO queue of its own — events live on
// app.ChroniclerDispatchQueue and are drained by the existing
// fireChronicler path. The dispatcher only schedules WHEN to fire.
// This keeps the queue's existing batching semantics (per-minute
// dedup keys, drain-once-per-fire) intact and means a cascade fire
// from PC speech mid-window picks up pending buffered events for
// free.
//
// Gating: the dispatcher exists from engine startup but only does
// useful work when callers enqueue arrivals to it (gated on the
// chronicler_buffered_dispatch flag at the call site). With the
// flag off, no enqueues land, no notifies arrive, the dispatcher
// idles.

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// Bounds on chronicler_buffer_window_seconds. Below 5s the
	// dispatcher would fire so often it'd be worse than the legacy
	// path; above 600s the village would feel narratively dead for
	// long stretches. Out-of-bounds values log and fall back to the
	// default rather than letting a misconfigured row break the loop.
	chroniclerBufferWindowMinSeconds = 5
	chroniclerBufferWindowMaxSeconds = 600
	chroniclerBufferWindowDefault    = 60

	// settingKeyChroniclerBufferWindow is the setting row read on each
	// enqueue. Re-read every time so an admin tweak takes effect on
	// the next batch without an engine restart. Already-armed timers
	// don't observe a config change mid-window — acceptable for a
	// 5-600s knob.
	settingKeyChroniclerBufferWindow = "chronicler_buffer_window_seconds"

	// settingKeyChroniclerBufferedDispatch is the feature flag the
	// enqueue sites read to decide whether to route through the
	// buffered path or fall through to the legacy immediate-cascade
	// path. Default false until an admin flips it after observation.
	settingKeyChroniclerBufferedDispatch = "chronicler_buffered_dispatch"
)

// chroniclerBufferedDispatcher schedules buffered chronicler fires.
// Held on App so all enqueue sites share the same timer.
type chroniclerBufferedDispatcher struct {
	app *App

	mu    sync.Mutex
	timer *time.Timer // nil when idle; non-nil when armed
}

// newChroniclerBufferedDispatcher constructs the dispatcher. Safe to
// call before main() finishes building the rest of App — fire()
// dereferences app.ChroniclerDispatchQueue lazily and is nil-safe
// through the queue's own nil checks.
func newChroniclerBufferedDispatcher(app *App) *chroniclerBufferedDispatcher {
	return &chroniclerBufferedDispatcher{app: app}
}

// notify is called by enqueue sites after an event lands in the queue.
// Arms the one-shot flush timer if not already armed. Subsequent
// notifies while the timer is running do NOT reset it — events
// accumulate in the queue and flush together when the original timer
// fires. This avoids the sliding-window starvation case where a
// continuous trickle of events keeps the timer perpetually deferred.
//
// Nil-safe: returns silently when d is nil so partially-constructed
// App instances (tests, alternate initializers) don't panic.
func (d *chroniclerBufferedDispatcher) notify() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		return
	}
	ctx := context.Background()
	windowSec := d.loadWindowSeconds(ctx)
	d.timer = time.AfterFunc(time.Duration(windowSec)*time.Second, d.fire)
}

// fire is the timer callback. Clears the timer state, then triggers a
// chronicler fire through the existing cascade path so the queue
// drains and the chronicler sees the consolidated batch in its
// perception. The "buffered_flush" reason flags it for the perception
// builder (lands in a later commit).
//
// Skips the fire if the queue is empty when the timer elapses (the
// notify could have raced with a separate cascade fire that already
// drained the queue — common case when PC speech arrives mid-window).
func (d *chroniclerBufferedDispatcher) fire() {
	d.mu.Lock()
	d.timer = nil
	d.mu.Unlock()

	if d.app == nil || d.app.ChroniclerDispatchQueue == nil {
		return
	}
	if d.app.ChroniclerDispatchQueue.pending() == 0 {
		return
	}
	// Reuse cascadeOriginFireChronicler — same sem, same drain
	// semantics. Empty structureID; the buffered flush is village-wide,
	// not place-anchored.
	d.app.cascadeOriginFireChronicler("buffered_flush", "")
}

// loadWindowSeconds reads the buffer-window setting and clamps to
// [chroniclerBufferWindowMinSeconds, chroniclerBufferWindowMaxSeconds].
// Out-of-range values log and fall back to chroniclerBufferWindowDefault.
func (d *chroniclerBufferedDispatcher) loadWindowSeconds(ctx context.Context) int {
	n := d.app.loadIntSetting(ctx, settingKeyChroniclerBufferWindow, chroniclerBufferWindowDefault)
	if n < chroniclerBufferWindowMinSeconds || n > chroniclerBufferWindowMaxSeconds {
		log.Printf("chronicler-buffered: window %d out of bounds [%d, %d], using default %d",
			n, chroniclerBufferWindowMinSeconds, chroniclerBufferWindowMaxSeconds, chroniclerBufferWindowDefault)
		return chroniclerBufferWindowDefault
	}
	return n
}

// chroniclerBufferedDispatchEnabled reads the feature flag. Called by
// enqueue sites at decision time so flipping the flag mid-session
// takes effect on the next event without an engine restart.
//
// Lives on App rather than on the dispatcher because the legacy
// immediate-cascade path also needs to consult it (to decide whether
// to fire-and-forget the cascade or skip in favor of buffering).
func (app *App) chroniclerBufferedDispatchEnabled(ctx context.Context) bool {
	return app.loadSetting(ctx, settingKeyChroniclerBufferedDispatch, "false") == "true"
}
