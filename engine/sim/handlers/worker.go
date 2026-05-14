package handlers

import (
	"context"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// worker is one pool goroutine. It drains the job channel until the
// context is cancelled. The channel is never closed (see TickWorkerPool),
// so ctx cancellation is the sole exit — which keeps the subscriber's
// enqueue free of any send-on-closed hazard.
func (p *TickWorkerPool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-p.jobs:
			// select picks a ready case at random — after Stop, with both
			// ctx.Done() and a buffered job ready, it may land here. Stop
			// drops buffered jobs, so re-check cancellation before starting
			// one rather than handing an already-cancelled ctx to the runner.
			select {
			case <-ctx.Done():
				return
			default:
			}
			p.runJob(ctx, job)
		}
	}
}

// runJob executes one tick job: emit `started` telemetry, delegate the
// turn to the runner (off the world goroutine), report completion via
// sim.CompleteReactorTick, then emit the terminal telemetry record.
//
// In PR 3b the runner is a stub returning TickStatusUnknown, so this
// exercises the lifecycle without a real perception/LLM turn. PR 3c/3d
// swap in the real runner; runJob itself does not change.
func (p *TickWorkerPool) runJob(ctx context.Context, job tickJob) {
	p.writeTelemetry(job, telemetryStarted, nil)

	result := p.runner.RunTick(ctx, p.world, job)

	val, err := p.world.SendContext(ctx, sim.CompleteReactorTick(
		job.actorID, job.attemptID, result, time.Now()))
	if err != nil {
		// The completion never landed — the world goroutine is gone
		// (shutdown) or ctx was cancelled. There is nothing to carry
		// forward to; the world is being discarded or checkpointed.
		p.writeTelemetry(job, telemetryFailed, map[string]string{
			"stage": "complete_send",
			"error": err.Error(),
		})
		return
	}

	// A stale completion means the attempt was superseded before it
	// landed — CompleteReactorTick's policy did not run. Informational:
	// the superseding attempt now owns the actor.
	if res, ok := val.(sim.CompleteReactorTickResult); ok && res.Stale {
		p.writeTelemetry(job, telemetryStale, nil)
		return
	}

	kind := telemetryCompleted
	if isFailureStatus(result.TerminalStatus) {
		kind = telemetryFailed
	}
	p.writeTelemetry(job, kind, map[string]string{
		"terminal_status": terminalStatusName(result.TerminalStatus),
	})
}

// isFailureStatus reports whether a terminal status represents a tick that
// did not complete cleanly. TickStatusUnknown — the PR 3b stub runner's
// result — is NOT a failure: it is a valid minimal completion.
func isFailureStatus(s sim.TickTerminalStatus) bool {
	switch s {
	case sim.TickStatusFailedBeforeRender,
		sim.TickStatusFailedAfterRender,
		sim.TickStatusShutdown:
		return true
	default:
		return false
	}
}
