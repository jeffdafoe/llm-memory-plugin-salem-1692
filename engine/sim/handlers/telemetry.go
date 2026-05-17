package handlers

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Worker-side tick-telemetry Kinds. PR 3a's evaluator writes "deferred";
// these four are the worker pool's lifecycle records. Kind is an open
// string set by contract — consumers must tolerate unknown values.
const (
	telemetryStarted   = "started"
	telemetryCompleted = "completed"
	telemetryFailed    = "failed"
	telemetryStale     = "stale"
)

// writeTelemetry emits one TickTelemetryRecord for job. The sink is
// non-blocking by contract — it drops on a full buffer rather than
// waiting — so this never blocks the worker. detail may be nil.
//
// Detail values must stay REDACTED: no raw prompts, LLM responses, tool
// arguments carrying private text, or memory payloads. The records here
// carry only status labels and error classes.
func (p *TickWorkerPool) writeTelemetry(job tickJob, kind string, detail map[string]string) {
	if p.sink == nil {
		return
	}
	p.sink.WriteTickTelemetry(sim.TickTelemetryRecord{
		At:        time.Now(),
		ActorID:   job.actorID,
		AttemptID: job.attemptID,
		Kind:      kind,
		Detail:    detail,
	})
}

// terminalStatusName maps a sim.TickTerminalStatus to a stable lowercase
// label for telemetry Detail. Unknown values render as "unknown".
func terminalStatusName(s sim.TickTerminalStatus) string {
	switch s {
	case sim.TickStatusSuccess:
		return "success"
	case sim.TickStatusDone:
		return "done"
	case sim.TickStatusBudgetForced:
		return "budget_forced"
	case sim.TickStatusFailedBeforeRender:
		return "failed_before_render"
	case sim.TickStatusFailedAfterRender:
		return "failed_after_render"
	case sim.TickStatusStale:
		return "stale"
	case sim.TickStatusShutdown:
		return "shutdown"
	case sim.TickStatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}
