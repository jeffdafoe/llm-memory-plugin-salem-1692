package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// Per-tick default budgets. Settle exact values empirically during PR 3d
// integration; the defaults are conservative.
const (
	// DefaultIterationBudget is the per-tick LLM-call cap, matching v1's
	// agentTickBudget. A tick that does not terminate within budget ends
	// as TickStatusBudgetForced.
	DefaultIterationBudget = 6

	// DefaultMaxToolCallsPerResponse caps the number of tool calls the
	// harness will process per LLM response. Independent of the iteration
	// budget — without it a single 500-call response is a runaway commit
	// path (multi-call invariant 2 in the design note §5).
	DefaultMaxToolCallsPerResponse = 8
)

// HarnessConfig is the wiring + budgets the Harness needs. Client and
// Registry are required; the rest have sensible defaults.
type HarnessConfig struct {
	// Client is the provider-neutral LLM client. Required.
	Client llm.Client

	// Registry is the tool registry. Required. The harness reads
	// AdvertisedSpecs() once per tick to build Request.Tools, and
	// dispatches validated calls through the entries returned by the
	// validator.
	Registry *Registry

	// Validator is the call validator. Optional; defaults to
	// NewValidator(Registry).
	Validator *Validator

	// IterationBudget caps per-tick iterations. Zero → DefaultIterationBudget.
	IterationBudget int

	// MaxToolCallsPerResponse caps the number of tool calls processed
	// per LLM response. Zero → DefaultMaxToolCallsPerResponse.
	MaxToolCallsPerResponse int

	// PerceptionRenderConfig controls prompt-render limits. Zero-valued
	// fields fall back to perception.DefaultRenderConfig() defaults.
	PerceptionRenderConfig perception.RenderConfig

	// ToolDispatchTimeout caps how long a single commit-tool dispatch
	// (sim.RunTickToolCommand → World.SendContext) is allowed to take.
	// Zero → no harness-imposed timeout beyond the parent ctx.
	ToolDispatchTimeout time.Duration

	// Clock is injectable for tests. Zero → time.Now.
	Clock func() time.Time
}

// Harness is the real tickRunner — replaces PR 3b's stubRunner. It owns
// the per-tick iteration loop: preflight stale-check, perception build +
// render (once per tick), within-tick transcript continuation, multi-call
// dispatch by tool class, attempt-guarded commits via
// sim.RunTickToolCommand, and the LLM error classification table.
//
// All RunTick exit paths return a populated sim.TickResult; the worker
// (handlers/worker.go) ferries it to sim.CompleteReactorTick exactly
// once. The harness does NOT call CompleteReactorTick directly.
type Harness struct {
	client    llm.Client
	registry  *Registry
	validator *Validator

	iterationBudget         int
	maxToolCallsPerResponse int
	renderConfig            perception.RenderConfig
	toolDispatchTimeout     time.Duration

	clock func() time.Time
}

// NewHarness constructs a Harness from cfg. Returns an error on missing
// required fields (Client, Registry) — a wiring bug that should fail
// loudly at startup rather than mid-tick.
func NewHarness(cfg HarnessConfig) (*Harness, error) {
	if cfg.Client == nil {
		return nil, errors.New("handlers: HarnessConfig.Client is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("handlers: HarnessConfig.Registry is required")
	}
	v := cfg.Validator
	if v == nil {
		v = NewValidator(cfg.Registry)
	}
	if cfg.IterationBudget <= 0 {
		cfg.IterationBudget = DefaultIterationBudget
	}
	if cfg.MaxToolCallsPerResponse <= 0 {
		cfg.MaxToolCallsPerResponse = DefaultMaxToolCallsPerResponse
	}
	clk := cfg.Clock
	if clk == nil {
		clk = time.Now
	}
	return &Harness{
		client:                  cfg.Client,
		registry:                cfg.Registry,
		validator:               v,
		iterationBudget:         cfg.IterationBudget,
		maxToolCallsPerResponse: cfg.MaxToolCallsPerResponse,
		renderConfig:            cfg.PerceptionRenderConfig,
		toolDispatchTimeout:     cfg.ToolDispatchTimeout,
		clock:                   clk,
	}, nil
}

// RunTick implements the tickRunner interface. Always returns a populated
// sim.TickResult — every code path sets TerminalStatus, Duration, and the
// diagnostic fields the worker passes to telemetry + CompleteReactorTick.
//
// Multi-call invariant 6 in the design note ("CompleteReactorTick exactly
// once on every exit path") is enforced by the worker, not by RunTick —
// the worker calls CompleteReactorTick once after RunTick returns
// regardless of which path RunTick took. RunTick's contract is: always
// return, never panic.
func (h *Harness) RunTick(ctx context.Context, w *sim.World, job tickJob) (result sim.TickResult) {
	start := h.clock()
	// Named return so the deferred Duration stamp mutates the slot the
	// caller observes. A non-named return would copy result into the
	// return slot BEFORE the defer ran, leaving callers with Duration=0.
	defer func() { result.Duration = h.clock().Sub(start) }()

	result = sim.TickResult{
		AttemptID:      job.attemptID,
		ActorID:        job.actorID,
		TerminalStatus: sim.TickStatusUnknown,
	}

	// --- preflight: snapshot read + stale check ---
	// Cheap: no world goroutine round-trip, no LLM tokens spent.
	snap := w.Published()
	if snap == nil {
		// Defensive: a missing published snapshot means the world has not
		// been initialized for snapshots, which is a wiring bug. Carry the
		// batch forward as before-render.
		return failBeforeRender(result, job, "")
	}
	actor, ok := snap.Actors[job.actorID]
	if !ok {
		return failBeforeRender(result, job, "")
	}
	if !actor.TickInFlight || actor.TickAttemptID != job.attemptID {
		// The world has already moved past this attempt (typed out,
		// superseded). All consumed warrants carry forward — none of
		// them have been addressed.
		result.TerminalStatus = sim.TickStatusStale
		result.StaleStage = sim.StaleStageBeforeRender
		result.UnaddressedWarrants = copyWarrants(job.warrants)
		return result
	}

	// --- perception build (cheap; no rendering yet) ---
	payload := perception.Build(snap, job.actorID, job.warrants)

	// --- noop-skip preflight (pre-LLM gate) ---
	// If perception has nothing actionable (no co-present peer, no need
	// at red) AND the consumed batch carries only low-information warrant
	// kinds (idle-backstop / huddle-concluded / huddle-left), the LLM
	// call would produce a noop tick. Skip it — the consumed warrants
	// land in recently-consumed via terminalStatusAddresses so they
	// don't re-fire on the next scan.
	//
	// Replaces v1's salem-vendor-only skip at engine/agent_tick.go:
	// 211-221 (ZBBS-WORK-235). Order is deliberate: this runs BEFORE
	// perception.Render, so the prompt-build allocations are skipped
	// too on the gate-hit path.
	if shouldSkipNoop(payload, snap.NeedThresholds, job.warrants) {
		result.TerminalStatus = sim.TickStatusSkipped
		return result
	}

	// --- render (allocates the prompt) ---
	rendered := perception.Render(payload, h.renderConfig)
	// Render-time drops are the "consumed but not addressed" set the
	// harness must carry forward. Subsequent paths append to this set on
	// failures (e.g. mid-tick LLM error).
	if len(rendered.DroppedWarrants) > 0 {
		result.UnaddressedWarrants = copyWarrants(rendered.DroppedWarrants)
	}

	// --- transcript init ---
	// PR 3d ships single-user-message perception. A separate system
	// prompt is the cutover layer's responsibility (the VA system loads
	// <Self> from context/soul there) — see PR 3d design note §3.1.
	transcript := []llm.Message{
		{Role: llm.RoleUser, Content: rendered.Text},
	}
	tools := h.registry.AdvertisedSpecs()

	// --- iteration loop ---
	for iter := 0; iter < h.iterationBudget; iter++ {
		result.IterationCount = iter + 1

		if err := ctx.Err(); err != nil {
			result.TerminalStatus = sim.TickStatusShutdown
			result.LLMErrorClass = llm.ErrorContextCancelled.String()
			return result
		}

		resp, err := h.client.Complete(ctx, llm.Request{
			Messages: transcript,
			Tools:    tools,
		})
		if err != nil {
			cls := llm.Classify(err)
			result.LLMErrorClass = cls.String()
			result.TerminalStatus = llmErrorToStatus(cls, iter)
			return result
		}

		// No tool calls = content-only response = the model is done
		// thinking, no actions to dispatch. Treat as successful tick end.
		if len(resp.ToolCalls) == 0 {
			result.TerminalStatus = sim.TickStatusSuccess
			return result
		}

		// Append the assistant message with ALL tool calls (the provider
		// requires the assistant turn to carry every call ID it emitted;
		// truncated calls still need a matching `tool` reply below).
		transcript = append(transcript, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: append([]llm.RawToolCall(nil), resp.ToolCalls...),
		})

		// Multi-call cap (invariant 2). Calls beyond the cap are dropped
		// and surfaced as typed errors.
		calls := resp.ToolCalls
		var truncated []llm.RawToolCall
		if len(calls) > h.maxToolCallsPerResponse {
			truncated = calls[h.maxToolCallsPerResponse:]
			calls = calls[:h.maxToolCallsPerResponse]
		}

		// Walk in-budget calls in order. A terminal call ends the batch.
		batchEnded := false
		var endedAt int
		var endedStatus sim.TickTerminalStatus

		for i, call := range calls {
			result.ToolsRequested = append(result.ToolsRequested, call.Name)

			// Validate.
			vc, verr := h.validator.Validate(call)
			if verr != nil {
				result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
				transcript = append(transcript, toolResultMsg(call.ID, formatValidationError(verr)))
				continue // invariant 4: validation failure is non-terminal
			}

			// Dispatch by class.
			content, outcome := h.dispatch(ctx, w, job, vc)
			transcript = append(transcript, toolResultMsg(call.ID, content))

			if outcome.stale {
				// Invariant 7: stale mid-batch ends the batch as stale.
				// Record the stale call itself as failed, and surface the
				// remaining in-budget + truncated calls as both requested
				// AND failed — ToolsFailedRejected MUST stay a subset of
				// ToolsRequested per the TickResult contract.
				result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
				for j := i + 1; j < len(calls); j++ {
					result.ToolsRequested = append(result.ToolsRequested, calls[j].Name)
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, calls[j].Name)
					transcript = append(transcript, toolResultMsg(calls[j].ID, "[error: stale_skip] earlier call in this batch went stale"))
				}
				for _, c := range truncated {
					result.ToolsRequested = append(result.ToolsRequested, c.Name)
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, c.Name)
					transcript = append(transcript, toolResultMsg(c.ID, "[error: stale_skip] earlier call in this batch went stale"))
				}
				result.TerminalStatus = sim.TickStatusStale
				result.StaleStage = sim.StaleStageAtTool
				return result
			}

			if outcome.success {
				result.ToolsSucceeded = append(result.ToolsSucceeded, call.Name)
			} else {
				result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
			}

			if outcome.ended {
				// Invariant 3: post-terminal calls skipped + logged.
				for j := i + 1; j < len(calls); j++ {
					result.ToolsRequested = append(result.ToolsRequested, calls[j].Name)
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, calls[j].Name)
					transcript = append(transcript, toolResultMsg(calls[j].ID, "[skipped: post_terminal] earlier call in this batch ended the tick"))
				}
				batchEnded = true
				endedAt = i
				endedStatus = outcome.terminalStatus
				break
			}
		}

		// Surface truncated calls as typed validation failures (invariant 2).
		// Only if we didn't already account for them via the stale path.
		if !batchEnded || endedStatus != sim.TickStatusStale {
			for _, c := range truncated {
				result.ToolsRequested = append(result.ToolsRequested, c.Name)
				result.ToolsFailedRejected = append(result.ToolsFailedRejected, c.Name)
				transcript = append(transcript, toolResultMsg(c.ID, "[error: excess_calls_truncated] dropped per MaxToolCallsPerResponse"))
			}
		}

		_ = endedAt
		if batchEnded {
			result.TerminalStatus = endedStatus
			return result
		}
	}

	// Iteration budget exhausted.
	result.TerminalStatus = sim.TickStatusBudgetForced
	result.BudgetHit = true
	return result
}

// dispatchOutcome bundles a single tool dispatch result.
type dispatchOutcome struct {
	success        bool
	ended          bool                   // tick ends after this call
	stale          bool                   // commit dispatch returned ErrTickAttemptStale
	terminalStatus sim.TickTerminalStatus // populated when ended
}

// dispatch executes one validated call. Returns the content string for
// the resulting "tool" message and an outcome describing what happened.
//
// Routing:
//   - ClassObservation → run the ObservationFn inline; observations never
//     end the tick (TerminalPolicy == TerminalNever guaranteed by the
//     constructor).
//   - ClassCommit → build the sim.Command via CommitFn, wrap with
//     sim.RunTickToolCommand for the attempt guard, submit via
//     World.SendContext. A successful submission with
//     TerminalPolicy=TerminalOnSuccess ends the tick.
//   - ClassTerminal → no handler; the tick ends.
func (h *Harness) dispatch(ctx context.Context, w *sim.World, job tickJob, vc *ValidatedCall) (string, dispatchOutcome) {
	in := HandlerInput{
		ActorID:     job.actorID,
		AttemptID:   job.attemptID,
		RootEventID: job.rootEventID,
		Args:        vc.DecodedArgs,
	}

	switch vc.Entry.Class {
	case ClassObservation:
		fn := vc.Entry.Observation()
		if fn == nil {
			log.Printf("handlers: dispatch %q: observation handler is nil (registration bug)", vc.Name)
			return "[error: handler_missing] observation handler is nil", dispatchOutcome{}
		}
		content, err := fn(ctx, in)
		if err != nil {
			// Log the detailed error; surface a stable label to the model
			// so handler-internal details (file paths, stack traces, API
			// responses) don't leak into the LLM transcript.
			log.Printf("handlers: dispatch %q: observation handler failed: %v", vc.Name, err)
			return "[error: handler_failed] tool handler returned an error", dispatchOutcome{}
		}
		return content, dispatchOutcome{success: true}

	case ClassCommit:
		fn := vc.Entry.Commit()
		if fn == nil {
			log.Printf("handlers: dispatch %q: commit handler is nil (registration bug)", vc.Name)
			return "[error: handler_missing] commit handler is nil", dispatchOutcome{}
		}
		cmd, err := fn(in)
		if err != nil {
			log.Printf("handlers: dispatch %q: commit handler failed: %v", vc.Name, err)
			return "[error: handler_failed] tool handler returned an error", dispatchOutcome{}
		}

		dispatchCtx := ctx
		if h.toolDispatchTimeout > 0 {
			var cancel context.CancelFunc
			dispatchCtx, cancel = context.WithTimeout(ctx, h.toolDispatchTimeout)
			defer cancel()
		}

		_, err = w.SendContext(dispatchCtx, sim.RunTickToolCommand(job.actorID, job.attemptID, job.rootEventID, cmd))
		if err != nil {
			if errors.Is(err, sim.ErrTickAttemptStale) {
				return "[error: stale] tick attempt superseded", dispatchOutcome{stale: true}
			}
			log.Printf("handlers: dispatch %q: command send failed: %v", vc.Name, err)
			return "[error: command_failed] world command rejected the tool", dispatchOutcome{}
		}

		ended := vc.Entry.TerminalPolicy == TerminalOnSuccess
		out := dispatchOutcome{success: true, ended: ended}
		if ended {
			out.terminalStatus = sim.TickStatusSuccess
		}
		return "[ok]", out

	case ClassTerminal:
		return "[done]", dispatchOutcome{
			success:        true,
			ended:          true,
			terminalStatus: sim.TickStatusDone,
		}

	default:
		log.Printf("handlers: dispatch %q: unknown class %v (typed-constructor invariant violated)", vc.Name, vc.Entry.Class)
		return "[error: unknown_class] tool dispatch encountered an unknown class", dispatchOutcome{}
	}
}

// --- helpers --------------------------------------------------------------

// failBeforeRender stages a "couldn't even render perception" exit:
// nothing addressed, the whole consumed batch carries forward.
func failBeforeRender(result sim.TickResult, job tickJob, llmErrClass string) sim.TickResult {
	result.TerminalStatus = sim.TickStatusFailedBeforeRender
	result.UnaddressedWarrants = copyWarrants(job.warrants)
	if llmErrClass != "" {
		result.LLMErrorClass = llmErrClass
	}
	return result
}

// llmErrorToStatus maps an llm.ErrorClass into the matching
// TickTerminalStatus for the harness's CompleteReactorTick handoff. The
// first-iteration distinction matters: a failure on iteration 0 means
// nothing was addressed (FailedBeforeRender semantically — the model
// never produced any response the actor could act on), while later
// iterations imply rendered content the actor *did* address (so prior
// successful tool calls count and FailedAfterRender is correct).
func llmErrorToStatus(cls llm.ErrorClass, iter int) sim.TickTerminalStatus {
	if cls == llm.ErrorContextCancelled {
		return sim.TickStatusShutdown
	}
	if iter == 0 {
		return sim.TickStatusFailedBeforeRender
	}
	return sim.TickStatusFailedAfterRender
}

// toolResultMsg builds a "tool" role message with the given content and
// matching tool_call_id. Centralized so the harness can't accidentally
// emit a tool message without the call_id (the provider would reject the
// transcript on the next Complete).
func toolResultMsg(callID, content string) llm.Message {
	return llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: callID,
		Content:    content,
	}
}

// formatValidationError produces the "tool" message content the model
// sees for a rejected call. Format: `[error: <kind>] <message>` — the
// kind is the stable label the design contract pins (e.g.
// "tool_unavailable_in_this_build"), the message is the per-call detail.
func formatValidationError(v *ValidationError) string {
	if v == nil {
		return "[error: unknown] validation error"
	}
	return fmt.Sprintf("[error: %s] %s", v.Kind, v.Message)
}

// copyWarrants returns a fresh slice with the same warrants. Defensive
// against later mutation by either the caller's job.warrants or the
// recipient (CompleteReactorTick).
func copyWarrants(src []sim.WarrantMeta) []sim.WarrantMeta {
	if len(src) == 0 {
		return nil
	}
	out := make([]sim.WarrantMeta, len(src))
	copy(out, src)
	return out
}
