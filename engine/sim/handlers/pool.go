package handlers

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

const (
	// defaultTickWorkerCount is used when WorldSettings.TickWorkerCount is
	// unset (<= 0). One worker gives deterministic processing order; a pool
	// > 1 trades that for nondeterministic cross-actor commit order, so the
	// default deliberately does not imply an ordering guarantee the system
	// does not provide.
	defaultTickWorkerCount = 1

	// minTickJobBuffer / maxTickJobBuffer clamp the derived job-channel
	// size. The buffer is deliberately small: backpressure is a feature.
	// A deep queue just means warrants sit consumed and actors sit
	// in-flight while the batch goes stale relative to newer open warrants.
	minTickJobBuffer = 2
	maxTickJobBuffer = 16
)

// TickWorkerPool is the off-world tick execution pool. It plays three roles:
//
//   - sim.TickAdmissionController — CanAdmit gates the reactor evaluator
//     BEFORE it consumes an actor's warrants (Option A — admit before
//     consume), so a "no" is loss-free.
//   - ReactorTickDue subscriber host — handleEvent (subscriber.go) enqueues
//     one tickJob per event onto the bounded channel.
//   - worker pool — a fixed set of goroutines drain the channel, run each
//     tick via the runner seam, and report completion through
//     sim.CompleteReactorTick.
//
// Lifecycle: NewTickWorkerPool → RegisterTickHandlers → Start → … → Stop →
// Wait. The job channel is NEVER closed. Workers exit on context
// cancellation instead, which removes any send-on-closed race with the
// subscriber. Jobs still buffered at Stop are dropped — reactor state is
// ephemeral, and a shutdown discards or checkpoints the world anyway, so a
// dropped buffered job is a tick that simply does not happen.
type TickWorkerPool struct {
	world       *sim.World
	sink        sim.TickTelemetrySink
	runner      tickRunner
	workerCount int
	jobs        chan tickJob

	// stopping flips true on Stop. CanAdmit reads it (from the world
	// goroutine); Stop writes it — hence atomic.
	stopping atomic.Bool

	// mu guards cancel only. Start and Stop are not expected to race in
	// normal use, but the guard keeps the cancel handoff well-defined.
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewTickWorkerPool builds a pool for w with the PR 3b stub runner —
// retained for backward-compat with tests that don't need a real turn
// executor. Production builds + integration tests should use
// NewTickWorkerPoolWithHarness instead; this constructor exists to keep
// the PR 3b worker / subscriber / admission tests focused on pipeline
// plumbing without dragging in a full LLM stack.
//
// Worker count and channel buffer come from w.Settings.TickWorkerCount /
// the clamp; see newPoolWithRunner. The pool is not wired into the world
// until RegisterTickHandlers, and its workers do not run until Start.
func NewTickWorkerPool(w *sim.World, sink sim.TickTelemetrySink) *TickWorkerPool {
	if w == nil {
		panic("handlers: NewTickWorkerPool requires a non-nil world")
	}
	return newPoolWithRunner(w, sink, stubRunner{})
}

// NewTickWorkerPoolWithHarness builds a TickWorkerPool with the PR 3d
// Harness as its tickRunner — the production constructor for the
// agent-tick execution pipeline.
//
// h MUST be non-nil. The harness, the registry it dispatches to, the LLM
// client it calls, and the world it submits commands to must all be
// configured and ready before Start is called on the returned pool.
func NewTickWorkerPoolWithHarness(w *sim.World, sink sim.TickTelemetrySink, h *Harness) *TickWorkerPool {
	if w == nil {
		panic("handlers: NewTickWorkerPoolWithHarness requires a non-nil world")
	}
	if h == nil {
		panic("handlers: NewTickWorkerPoolWithHarness requires a non-nil harness")
	}
	return newPoolWithRunner(w, sink, h)
}

// newPoolWithRunner is the shared constructor. NewTickWorkerPool passes the
// PR 3b stub runner; PR 3c/3d (and tests) inject a real one. A nil world or
// runner is a wiring bug — the worker body dereferences both, so reject
// them here rather than panicking later in a less obvious place.
func newPoolWithRunner(w *sim.World, sink sim.TickTelemetrySink, runner tickRunner) *TickWorkerPool {
	if w == nil || runner == nil {
		panic("handlers: newPoolWithRunner requires a non-nil world and runner")
	}
	count := defaultTickWorkerCount
	if w.Settings.TickWorkerCount > 0 {
		count = w.Settings.TickWorkerCount
	}
	buf := 2 * count
	if buf < minTickJobBuffer {
		buf = minTickJobBuffer
	}
	if buf > maxTickJobBuffer {
		buf = maxTickJobBuffer
	}
	return &TickWorkerPool{
		world:       w,
		sink:        sink,
		runner:      runner,
		workerCount: count,
		jobs:        make(chan tickJob, buf),
	}
}

// CanAdmit reports whether the pool has buffer space for another tick job.
// The reactor evaluator calls this on the world goroutine BEFORE consuming
// an actor's warrants. It returns false once Stop has begun, so the
// evaluator stops feeding a draining pool — an admit-then-enqueue against a
// pool whose workers are exiting would just drop the job, whereas a
// deferral leaves the warrants open for a later, healthy pool.
//
// len and cap on a channel are safe to read from any goroutine.
func (p *TickWorkerPool) CanAdmit() bool {
	return !p.stopping.Load() && len(p.jobs) < cap(p.jobs)
}

// Start launches the worker goroutines. They run until the context derived
// from ctx is cancelled — by Stop, or by ctx itself. Start must be called
// exactly once, with a non-nil context, and never after Stop: a stopped
// pool is permanently dead (CanAdmit stays false), so starting workers on
// it would only produce a half-disabled state. There are no restart
// semantics — build a fresh pool instead.
func (p *TickWorkerPool) Start(ctx context.Context) {
	if ctx == nil {
		panic("handlers: TickWorkerPool.Start requires a non-nil context")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		panic("handlers: TickWorkerPool.Start called more than once")
	}
	if p.stopping.Load() {
		panic("handlers: TickWorkerPool.Start called after Stop")
	}
	workerCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	for i := 0; i < p.workerCount; i++ {
		p.wg.Add(1)
		go p.worker(workerCtx)
	}
}

// Stop signals shutdown: CanAdmit begins returning false (so the evaluator
// stops feeding the pool) and the worker context is cancelled (so the
// workers exit). Idempotent. Calling Stop before Start is allowed — it
// permanently disables the pool, and a subsequent Start panics rather than
// half-starting it. Call Wait afterward to join the workers.
func (p *TickWorkerPool) Stop() {
	p.stopping.Store(true)
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait blocks until every worker goroutine has exited. Call after Stop.
func (p *TickWorkerPool) Wait() {
	p.wg.Wait()
}
