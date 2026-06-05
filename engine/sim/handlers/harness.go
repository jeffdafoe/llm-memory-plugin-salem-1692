package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// maxPreflightSnapshotSpins bounds the busy-wait in RunTick's preflight that
// waits for the published snapshot to catch up to this job's dispatch (the
// enqueue→republish lag — see RunTick). Each spin is a runtime.Gosched +
// re-read; the wait normally resolves in a handful of spins because the
// dispatching command's republish is microseconds away on the world
// goroutine (which Gosched yields to). The cap is a safety ceiling for a
// wedged/lagging world goroutine, after which the preflight falls through to
// the check against whatever snapshot it has (degrading to the prior, racy
// behavior rather than spinning forever).
const maxPreflightSnapshotSpins = 1000

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

	// DefaultMaxObservationRounds is how many observation-only LLM rounds
	// (e.g. the model spends a whole round just calling recall to gather
	// context) are allowed PER TICK on top of the action budget. Such rounds
	// do NOT consume IterationBudget — thinking isn't penalized as acting
	// (ZBBS-WORK-321) — but they're still bounded so a model that only ever
	// recalls can't loop forever: the hard per-tick ceiling is
	// IterationBudget + MaxObservationRounds total LLM rounds.
	DefaultMaxObservationRounds = 3
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

	// IterationBudget caps per-tick ACTION iterations (rounds that dispatch
	// a commit or end the tick). Zero → DefaultIterationBudget.
	IterationBudget int

	// MaxObservationRounds caps per-tick observation-only rounds (e.g. a
	// round the model spends only calling recall). These do NOT count
	// against IterationBudget — thinking isn't penalized as acting — but
	// the per-tick hard ceiling is IterationBudget + MaxObservationRounds
	// total LLM rounds. Zero → DefaultMaxObservationRounds.
	MaxObservationRounds int

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

	// PromptSink, when set, receives each tick's rendered deliberation prompt
	// for the operator-gated umbilical debug surface (ZBBS-HOME-360). Nil (the
	// umbilical-disabled default) means prompts are not captured — zero cost.
	PromptSink sim.PromptSink

	// ChatSink, when set, receives the engine<->model chat exchange (the
	// rendered perception tx + each round's response rx) keyed by scene for the
	// operator-gated umbilical (ZBBS-HOME-382). Nil = not captured, zero cost.
	ChatSink sim.ChatSink

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
	maxObservationRounds    int
	maxToolCallsPerResponse int
	renderConfig            perception.RenderConfig
	toolDispatchTimeout     time.Duration

	// promptSink captures rendered deliberation prompts for the umbilical
	// (ZBBS-HOME-360); nil when the umbilical is disabled.
	promptSink sim.PromptSink

	// chatSink captures the engine<->model chat exchange per scene for the
	// umbilical (ZBBS-HOME-382); nil when the umbilical is disabled.
	chatSink sim.ChatSink

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
	if cfg.MaxObservationRounds <= 0 {
		cfg.MaxObservationRounds = DefaultMaxObservationRounds
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
		maxObservationRounds:    cfg.MaxObservationRounds,
		maxToolCallsPerResponse: cfg.MaxToolCallsPerResponse,
		renderConfig:            cfg.PerceptionRenderConfig,
		toolDispatchTimeout:     cfg.ToolDispatchTimeout,
		promptSink:              cfg.PromptSink,
		chatSink:                cfg.ChatSink,
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
	// Wait out the dispatch→republish lag. This job was enqueued from inside
	// the dispatching command's synchronous emit (subscriber.go's handleEvent
	// runs inline on the world goroutine), but the snapshot reflecting our
	// TickInFlight dispatch is not republished until that command returns
	// (world.go command loop: Fn → TickCounter++ → republish). A fast worker
	// can read the published snapshot in that enqueue→republish window and see
	// a pre-dispatch view (TickInFlight=false) — not because the tick was
	// superseded, but because the snapshot hasn't caught up. The stale check
	// below would then false-classify a perfectly live tick as Stale.
	//
	// job.dispatchTick is World.TickCounter at enqueue; the dispatching
	// command's republish stamps Snapshot.AtTick = dispatchTick+1. So while
	// AtTick <= dispatchTick the snapshot predates our dispatch — re-read
	// until it catches up (bounded; the republish is unconditional and
	// imminent). dispatchTick == 0 means a hand-built test job (no real
	// dispatch) — skip the wait so unit tests don't spin.
	for spins := 0; job.dispatchTick > 0 && snap != nil && snap.AtTick <= job.dispatchTick && spins < maxPreflightSnapshotSpins; spins++ {
		runtime.Gosched()
		snap = w.Published()
	}
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
	// Capture the rendered prompt for the umbilical debug surface (ZBBS-HOME-360),
	// before the transcript continuation appends tool results — this is the
	// perception the model deliberated from. Nil sink (umbilical off) → skipped.
	// Placed after the noop-skip gate so only actually-rendered prompts are
	// captured, never a tick that never built one.
	if h.promptSink != nil {
		h.promptSink.WritePrompt(sim.PromptRecord{
			At:        h.clock().UTC(),
			ActorID:   job.actorID,
			AttemptID: job.attemptID,
			Prompt:    fullPerceptionPrompt(rendered),
		})
	}
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
	tools := gateTools(h.registry, payload, snap)

	// Move targets this tick's perception surfaced — collected once and threaded
	// into every tool dispatch so move_to can resolve a place NAME to any id the
	// actor was shown (a distant vendor / rest cue), not just anchors + scene
	// radius. ZBBS-HOME-389.
	perceivedPlaces := perception.CollectPerceivedPlaces(payload)

	// Scene + VA-routing context for every Complete + persist call this
	// tick. SceneID is minted once and reused so the API's per-scene
	// history loader (chat_messages.scene_id filter) sees a coherent
	// conversation across iterations. Model is the actor's VA slug.
	sceneID := llm.NewSceneID()
	model := actor.LLMAgent

	// ZBBS-HOME-382: capture the rendered perception (tx) into the per-scene
	// chat ring so the umbilical /chat route shows what the model was sent
	// alongside its responses (rx, captured per round below). Keyed by the same
	// sceneID stamped on the llm-memory chat rows. Nil sink -> skipped (zero cost).
	if h.chatSink != nil {
		h.chatSink.WriteChat(sim.ChatRecord{
			At:        h.clock().UTC(),
			SceneID:   sceneID,
			ActorID:   job.actorID,
			AttemptID: job.attemptID,
			Model:     model,
			Direction: "perception",
			Content:   fullPerceptionPrompt(rendered),
		})
	}

	// Defer persist-on-exit for the orphan-tool_use case. v1's bug: a
	// terminal-class tool (done() / TerminalOnSuccess commit) ends the
	// tick without firing another Complete to deliver tool_result rows
	// to the API. The assistant message that contained the terminal
	// tool_call sits in chat_messages history with no matching tool_use
	// row, breaking the NEXT tool-use call against the same VA
	// ("tool_use without tool_result"). The defer runs on every exit
	// path; persistTickToolResults gates on TerminalStatus + the
	// presence of trailing tool messages, so spurious exits skip the
	// call. See engine/sim/llm/memapi package doc.
	defer func() {
		h.persistTickToolResults(ctx, model, sceneID, transcript, result.TerminalStatus)
	}()

	// --- iteration loop ---
	// IterationBudget bounds ACTION rounds (a round that dispatches a commit
	// or ends the tick). Observation-only rounds — the model spends a whole
	// LLM round just gathering context (recall) — do NOT consume it
	// (ZBBS-WORK-321: thinking isn't penalized as acting). maxTotalRounds is
	// the hard ceiling so a model that only ever recalls can't loop forever.
	maxTotalRounds := h.iterationBudget + h.maxObservationRounds
	actionRounds := 0
	// spokenThisTick holds the normalized text of every utterance this actor has
	// successfully spoken this tick (ZBBS-WORK-375 same-tick repetition guard).
	// A normalized-exact repeat — within the same response batch or on a later
	// round — is rejected model-facing so the model self-corrects or calls
	// done(), instead of re-pitching the identical line every round to the
	// iteration budget (the observed speak×6 budget_forced storm). This replaces
	// HOME-381's hard one-utterance cap: a DISTINCT follow-up (greet THEN a
	// separate answer) is allowed through; only verbatim repeats are blocked.
	// After a speak the loop continues — the model ends the tick by calling
	// done() (steered there by the post-speak nudge in commitResultContent).
	spokenThisTick := map[string]struct{}{}
	// offeredThisTick holds the dedup key of every pay_with_item OFFER this actor
	// has successfully placed this tick (ZBBS-HOME-395 same-tick repeat-offer
	// guard — the pay analogue of spokenThisTick). Pre-395 a placed offer came
	// back as a bare [ok] with no "now pending, await their answer" signal, so the
	// model had no within-tick reason to stop and re-offered the same item to the
	// same seller every round to the iteration budget (the Josiah×Moses carrot
	// storm). Each offer also spawned its own ledger row that later rendered a
	// separate "fell through" line, so the storm multiplied the NEXT tick's
	// perception too. One offer per (seller, item, disposition) per tick: after
	// that the buyer awaits the seller's accept/decline/counter. See payOfferKey
	// for the keying rationale (price deliberately excluded).
	offeredThisTick := map[string]struct{}{}
	for round := 0; round < maxTotalRounds; round++ {
		result.IterationCount = round + 1

		if err := ctx.Err(); err != nil {
			result.TerminalStatus = sim.TickStatusShutdown
			result.LLMErrorClass = llm.ErrorContextCancelled.String()
			return result
		}

		resp, err := h.client.Complete(ctx, llm.Request{
			Model:            model,
			SceneID:          sceneID,
			Messages:         transcript,
			Tools:            tools,
			EphemeralContext: rendered.EphemeralText,
		})
		if err != nil {
			cls := llm.Classify(err)
			result.LLMErrorClass = cls.String()
			result.TerminalStatus = llmErrorToStatus(cls, round)
			return result
		}

		// ZBBS-HOME-382: capture the model's response (rx) into the per-scene
		// chat ring — content + a compact tool-call summary — so the umbilical
		// /chat route shows the full exchange for this scene. Nil sink -> skipped.
		if h.chatSink != nil {
			h.chatSink.WriteChat(sim.ChatRecord{
				At:        h.clock().UTC(),
				SceneID:   sceneID,
				ActorID:   job.actorID,
				AttemptID: job.attemptID,
				Model:     model,
				Direction: "response",
				Content:   resp.Content,
				ToolCalls: summarizeToolCalls(resp.ToolCalls),
			})
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

		// observationOnly tracks whether this round was pure thinking (every
		// call a successfully-dispatched observation). Dropped (truncated)
		// calls mean the model tried to act beyond the cap — not a clean
		// think — so the round counts against the action budget.
		observationOnly := true
		if len(truncated) > 0 {
			observationOnly = false
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
				observationOnly = false // a malformed action attempt isn't a clean think
				result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
				transcript = append(transcript, toolResultMsg(call.ID, formatValidationError(verr)))
				continue // invariant 4: validation failure is non-terminal
			}
			if vc.Entry.Class != ClassObservation {
				observationOnly = false
			}

			// ZBBS-WORK-375: same-tick repetition guard. Reject a normalized-
			// exact repeat of something this actor already said THIS tick,
			// before dispatch, as a model-facing typed error so the model can
			// say something new or call done(). Catches both the within-batch
			// [speak X, speak X] case and the cross-round re-pitch that drove
			// the budget_forced storm. Recipient-agnostic on purpose: an exact
			// same-tick repeat reads as a defect regardless of who it is aimed
			// at, and keying on the model's DECLARED `to` would MISS repeats
			// when the model sets `to` inconsistently across rounds (the worse
			// error). Refine toward resolved-addressee / semantic similarity
			// only if a false-positive shows up live (design fork 3:
			// normalized-exact first).
			if norm, isSpeak := speakUtteranceKey(vc); isSpeak {
				if _, dup := spokenThisTick[norm]; dup {
					observationOnly = false
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
					transcript = append(transcript, toolResultMsg(call.ID, "[error: already_said_that] you already said that this turn — say something new, or call done()."))
					continue
				}
			}

			// ZBBS-HOME-395: same-tick repeat-offer guard — the pay analogue of
			// the speak guard above. A buyer that placed an offer and got a bare
			// [ok] back (no "now pending" signal pre-395) re-offered the same item
			// to the same seller every round to the iteration budget, each offer
			// spawning a ledger row that later rendered its own "fell through"
			// line (the observed Josiah×Moses carrot storm). Reject a second offer
			// for the same (seller, item, disposition) this tick model-facing so
			// the buyer awaits the seller's answer or calls done(). Keyed WITHOUT
			// price so a re-offer at a drifting amount (5 coins, then 10, then 10…)
			// still matches — that drift WAS the storm. Quote-take and
			// counter-response paths are exempt (see payOfferKey).
			if key, isOffer := payOfferKey(vc); isOffer {
				if _, dup := offeredThisTick[key]; dup {
					observationOnly = false
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
					transcript = append(transcript, toolResultMsg(call.ID, "[error: already_offered] you already made that offer this turn — wait for their answer, or call done()."))
					continue
				}
			}

			// Dispatch by class.
			content, outcome := h.dispatch(ctx, w, job, vc, actor.LLMAgent, perceivedPlaces)
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
				// ZBBS-WORK-375: record the committed utterance so a later
				// round (or a later call in this same batch) that repeats it
				// verbatim is rejected by the dedup guard above. Only a
				// SUCCESSFUL speak is recorded — a bounced/rejected speak never
				// reached the transcript, so it is not a repeat to guard
				// against, and the model still gets to retry the bounced line.
				if norm, isSpeak := speakUtteranceKey(vc); isSpeak {
					spokenThisTick[norm] = struct{}{}
				}
				// ZBBS-HOME-395: record a placed offer so a later round (or a
				// later call in this same batch) re-offering the same (seller,
				// item, disposition) is rejected by the guard above. Only a
				// SUCCESSFUL offer is recorded — a bounced offer never created a
				// ledger row, so it is not a repeat to guard against and the model
				// may retry the bounced offer.
				if key, isOffer := payOfferKey(vc); isOffer {
					offeredThisTick[key] = struct{}{}
				}
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

		// ZBBS-WORK-375: a speak no longer ends the tick (HOME-381's cap is
		// gone). The loop continues so the model can follow a greeting with a
		// distinct answer in a later round; the post-speak nudge in
		// commitResultContent steers it to call done() once it has nothing new,
		// the same-tick dedup guard blocks verbatim repeats, and the iteration
		// budget below remains the hard ceiling on a runaway loop.

		// Round complete, tick continues. An observation-only round (recall
		// to gather context) is "thinking" — it does NOT consume the action
		// budget (ZBBS-WORK-321); it's bounded only by maxTotalRounds. Any
		// other round (a commit, a validation failure, dropped calls) is an
		// action round and counts.
		if !observationOnly {
			actionRounds++
			if actionRounds >= h.iterationBudget {
				result.TerminalStatus = sim.TickStatusBudgetForced
				result.BudgetHit = true
				return result
			}
		}
	}

	// Per-tick hard ceiling hit (IterationBudget + MaxObservationRounds) —
	// e.g. a model that only ever recalls and never commits.
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
func (h *Harness) dispatch(ctx context.Context, w *sim.World, job tickJob, vc *ValidatedCall, llmMemoryAgent string, perceivedPlaces perception.PerceivedPlaces) (string, dispatchOutcome) {
	in := HandlerInput{
		ActorID:        job.actorID,
		AttemptID:      job.attemptID,
		RootEventID:    job.rootEventID,
		LLMMemoryAgent: llmMemoryAgent,
		Args:           vc.DecodedArgs,
		// Move targets this tick's perception surfaced (ZBBS-HOME-389) — the move_to
		// commit resolves a structure_name against these. Empty for non-move ticks.
		PerceivedStructureIDs: perceivedPlaces.StructureIDs,
		PerceivedObjectIDs:    perceivedPlaces.ObjectIDs,
		// New-news signal for the turn-state gate (ZBBS-WORK-370). Computed from
		// the tick's consumed warrant batch; only the speak commit consumes it.
		HasNewNews: batchHasNewNews(job.warrants),
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
			// Echo the command validator's rejection reason to the model so it can
			// correct its next call ("no one named X in this conversation", "use a
			// structure_id you can see in your perception"). These reasons are
			// authored to be model-facing and reach us tagged as ModelFacingError
			// (see RunTickToolCommand). Returning a generic "command_failed" here
			// previously severed self-correction — the model never learned WHY a
			// call failed and retried the same bad call indefinitely (e.g. paying a
			// structure name as if it were a person, or move_to'ing a name instead
			// of a structure_id). Only ModelFacingError is echoed; every other
			// dispatch error (actor-not-found race, nil command, invalid root,
			// context deadline) stays generic so internal detail never leaks.
			var modelErr sim.ModelFacingError
			if errors.As(err, &modelErr) {
				log.Printf("handlers: dispatch %q: command rejected: %v", vc.Name, err)
				return fmt.Sprintf("[error] %s", modelErr.Error()), dispatchOutcome{}
			}
			log.Printf("handlers: dispatch %q: command send failed: %v", vc.Name, err)
			return "[error: command_failed] world command rejected the tool", dispatchOutcome{}
		}

		ended := vc.Entry.TerminalPolicy == TerminalOnSuccess
		out := dispatchOutcome{success: true, ended: ended}
		if ended {
			out.terminalStatus = sim.TickStatusSuccess
		}
		return commitResultContent(vc), out

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

// commitResultContent builds the "tool" message content a successful commit
// returns to the model. Most commits return the generic "[ok]"; speak and a
// newly-placed pay_with_item offer are the exceptions. speak echoes the line it
// just said back plus a post-speak continuation steer; a placed offer
// (ZBBS-HOME-395) echoes the pending offer plus an await-the-seller / done()
// steer. Both replace a bare "[ok]" that read as "nothing happened, try again"
// and drove a same-tick repeat storm (speak×6 / pay_with_item×6 to the budget).
//
// Why echo the line (ZBBS-WORK-368, Track B within-tick salience): Llama-3.3
// emits an EMPTY assistant content string when it makes a tool call, so a spoken
// line lives ONLY inside the speak call's arguments JSON — weak salience. Within
// a single tick the model then can't saliently see that it just spoke, and
// re-emits the same line. Echoing the utterance back as the tool result puts it
// in plain language on the next within-tick Complete. The cross-tick replay path
// already does the equivalent (memapi paraphrases speak into `(I said aloud:
// "...")`); this closes the engine's within-tick gap.
//
// Why the continuation steer (ZBBS-WORK-375, Variant-B continuation prompt):
// with HOME-381's hard one-speak cap removed, the model itself must call done()
// to end a turn after speaking. This tool result is the recency-dominant message
// it reads before its next decision, so the stop-rule lives HERE, at the
// decision point — biased to done() and explicitly forbidding the re-greet /
// re-pitch / rephrase the storm was made of. The same-tick dedup guard
// (harness.go RunTick) is the hard floor behind this soft steer; a genuine
// distinct follow-up ("here is your bread") still goes through.
//
// The text is re-trimmed to match what was actually spoken (sim.Speak trims);
// the success branch is only reached after the speak command committed, so the
// utterance is non-empty and control-char-clean by then. %q quotes + escapes
// it, so an utterance containing a quote can't break the echo's framing.
func commitResultContent(vc *ValidatedCall) string {
	if vc.Name == "speak" {
		if args, ok := vc.DecodedArgs.(SpeakArgs); ok {
			text := strings.TrimSpace(args.Text)
			if text != "" {
				return fmt.Sprintf(
					"[ok] You said: %q. You have spoken — call done() now unless a new "+
						"event has arrived or someone asked you something distinct you "+
						"have not yet answered. Do not greet again, re-pitch, or rephrase "+
						"what you just said.",
					text,
				)
			}
		}
	}
	if vc.Name == "pay_with_item" {
		if args, ok := vc.DecodedArgs.(PayWithItemArgs); ok {
			// A plain new offer (no quote_id / in_response_to) is now a pending
			// ledger entry the seller must accept, decline, or counter — the
			// buyer's move this tick is finished. Pre-395 this returned a bare
			// "[ok]", which read as "nothing happened, try again" and drove the
			// re-offer storm. Echo what was offered for salience (Llama-3.3 emits
			// empty assistant content on a tool call — the same weak-salience gap
			// the speak echo above closes) and steer to done(), forbidding the
			// re-offer. Quote-take (instant close) and counter-response are
			// distinct flows that don't storm, so they keep the generic "[ok]".
			if args.QuoteID == 0 && args.InResponseTo == 0 {
				item := strings.ToLower(strings.Join(strings.Fields(args.Item), " "))
				if item == "" {
					item = "those goods"
				}
				seller := strings.TrimSpace(args.Seller)
				if seller == "" {
					seller = "the seller"
				}
				return fmt.Sprintf(
					"[ok] Your offer to buy %d %s from %s is now before them, awaiting "+
						"their answer. Do not offer again — call done() and let them accept, "+
						"decline, or counter. Offer again only after they have responded.",
					args.Qty, item, seller,
				)
			}
		}
	}
	return "[ok]"
}

// speakUtteranceKey returns the normalized dedup key for a speak call and true,
// or ("", false) for any non-speak tool or a speak whose text is empty after
// normalization (which the decode/handler layer rejects anyway). The key is the
// utterance text lowercased, trimmed, and inner-whitespace-collapsed, so trivial
// spacing/case differences in an otherwise identical repeat still match. Mirrors
// commitResultContent's speak-by-name special-casing (the harness knows the
// speak tool's arg shape; the registry stays free of tool-cadence markers since
// HOME-381's was removed). Normalization is intentionally simple — ZBBS-WORK-375
// fork 3: normalized-exact first, semantic similarity only if paraphrased
// repeats show up live.
func speakUtteranceKey(vc *ValidatedCall) (string, bool) {
	if vc == nil || vc.Name != "speak" {
		return "", false
	}
	args, ok := vc.DecodedArgs.(SpeakArgs)
	if !ok {
		return "", false
	}
	norm := strings.ToLower(strings.Join(strings.Fields(args.Text), " "))
	if norm == "" {
		return "", false
	}
	return norm, true
}

// payOfferKey returns the normalized same-tick dedup key for a pay_with_item
// call and true, or ("", false) for any other tool or a pay call that is NOT a
// plain new offer. The key is (seller, item, disposition) — deliberately WITHOUT
// amount or qty, because the observed storm re-offered the SAME item to the SAME
// seller at a drifting price (5 coins, then 10, then 10…) every round; keying on
// price would let that exact storm straight through. One offer per (seller,
// item, disposition) per tick is the intent: once an offer is before the seller,
// the buyer awaits their accept/decline/counter rather than piling on more
// offers the seller has not yet seen.
//
// Scoped to the default pending-offer path: a quote take (quote_id) closes the
// deal instantly and a counter-response (in_response_to) is a deliberate,
// distinct move, so neither storms — both pass through untouched, matching the
// commitResultContent steer's scope. Mirrors speakUtteranceKey; seller and item
// are lowercased + whitespace-collapsed the same way so trivial spacing/case
// drift in a repeat still matches. The disposition byte (keep vs consume-now)
// keeps a genuine "buy one to keep AND one to eat now" pair distinct.
func payOfferKey(vc *ValidatedCall) (string, bool) {
	if vc == nil || vc.Name != "pay_with_item" {
		return "", false
	}
	args, ok := vc.DecodedArgs.(PayWithItemArgs)
	if !ok {
		return "", false
	}
	if args.QuoteID != 0 || args.InResponseTo != 0 {
		return "", false
	}
	seller := strings.ToLower(strings.Join(strings.Fields(args.Seller), " "))
	item := strings.ToLower(strings.Join(strings.Fields(args.Item), " "))
	if seller == "" || item == "" {
		return "", false
	}
	disposition := "keep"
	if args.ConsumeNow {
		disposition = "consume"
	}
	return seller + "\x00" + item + "\x00" + disposition, true
}

// fullPerceptionPrompt joins the durable turn and the ephemeral current-tick
// context into the single prompt the model effectively saw, for the umbilical
// debug surface (ZBBS-WORK-364). The two travel separately on the wire (durable
// = persisted message; ephemeral = /chat/send ephemeral_context attached to the
// current turn), but the operator wants to read the whole perception.
func fullPerceptionPrompt(r perception.RenderedPrompt) string {
	if r.EphemeralText == "" {
		return r.Text
	}
	return r.Text + "\n\n" + r.EphemeralText
}

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

// persistTickToolResults runs at tick-exit (deferred from RunTick) and
// writes the last batch's tool-result rows to the provider's history
// via the optional llm.ToolResultPersister. Closes the v1 orphan-
// tool_use defect: when a terminal-class tool ends the tick without
// firing another Complete, the assistant's tool_call sits in history
// with no matching tool_result row, breaking the next tool-use call.
//
// Gates:
//
//   - Adapter must implement llm.ToolResultPersister. FakeClient does;
//     a hypothetical non-history adapter would skip this branch.
//   - Model must be non-empty (no VA → no persist target).
//   - Status must be one of the "clean exit with possibly-orphan
//     tool_results" set: TickStatusDone (terminal tool fired),
//     TickStatusSuccess (TerminalOnSuccess commit ended the tick), or
//     TickStatusBudgetForced (budget exhausted before next Complete).
//     Other statuses are either no-op (Skipped, Shutdown, Stale,
//     FailedBeforeRender) or have already had their results delivered
//     via a prior Complete (FailedAfterRender — the failed Complete
//     posted iter N-1's results).
//   - Transcript must end in one or more tool messages after the last
//     assistant — that's the unpersisted last batch.
//
// Errors are logged, not returned: the TickResult is already populated
// and the worker's CompleteReactorTick contract is the harness's
// authoritative exit. A persist failure leaves orphans in history —
// surfaced via logs for monitoring.
// maxToolCallArgSummaryLen bounds each tool call's argument blob in the chat-ring
// summary (ZBBS-HOME-382) so a large argument can't bloat a debug record.
const maxToolCallArgSummaryLen = 200

// summarizeToolCalls renders a response's tool calls into a compact one-line
// summary for the umbilical chat ring (ZBBS-HOME-382): "name(args); name(args)",
// each argument blob truncated on a rune boundary. Debug-surface display only —
// never parsed. strings.Builder keeps this cheap on the tick-worker path.
func summarizeToolCalls(calls []llm.RawToolCall) string {
	var b strings.Builder
	for i, c := range calls {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.Name)
		b.WriteByte('(')
		b.WriteString(truncateUTF8(string(c.Arguments), maxToolCallArgSummaryLen))
		b.WriteByte(')')
	}
	return b.String()
}

// truncateUTF8 caps s at max bytes without splitting a UTF-8 rune (a tool-arg
// blob can carry multibyte runes), appending "..." when it truncates.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}

func (h *Harness) persistTickToolResults(
	ctx context.Context,
	model, sceneID string,
	transcript []llm.Message,
	status sim.TickTerminalStatus,
) {
	if model == "" {
		return
	}
	switch status {
	case sim.TickStatusDone, sim.TickStatusSuccess, sim.TickStatusBudgetForced:
		// proceed
	default:
		return
	}
	persister, ok := h.client.(llm.ToolResultPersister)
	if !ok {
		return
	}
	results := extractTrailingToolResults(transcript)
	if len(results) == 0 {
		return
	}
	// On engine shutdown (parent ctx cancelled): skip persist — the
	// orphan is acceptable, engine restart is the bigger concern. On
	// any other live path: run persist on a fresh detached context
	// with a short budget so a future per-tick deadline (not yet
	// implemented but plausible) doesn't abort cleanup that's
	// specifically there to prevent provider-side corruption (R1
	// finding #7).
	if ctx.Err() != nil {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	err := persister.PersistToolResults(persistCtx, llm.PersistRequest{
		Model:   model,
		SceneID: sceneID,
		Results: results,
	})
	if err != nil {
		log.Printf("handlers: persist tick tool results (model=%q scene=%q n=%d): %v",
			model, sceneID, len(results), err)
	}
}

// persistTimeout caps the deferred persist call. v1's per-attempt
// budget is 90s but the retry schedule sums to ~800ms; 5s here is
// generous for the typical case and bounded enough that engine
// shutdown won't block on it past the operator's patience.
const persistTimeout = 5 * time.Second

// extractTrailingToolResults walks the transcript from the end,
// collecting tool messages until the first non-tool boundary (the
// preceding assistant message). Defensive: tool messages without
// ToolCallID are skipped silently (toolResultMsg ensures the ID is
// set, but a future caller could violate that).
func extractTrailingToolResults(transcript []llm.Message) []llm.ToolResult {
	// Find first non-tool from the end.
	end := len(transcript)
	start := end
	for i := end - 1; i >= 0; i-- {
		if transcript[i].Role != llm.RoleTool {
			start = i + 1
			break
		}
		if i == 0 {
			start = 0
		}
	}
	if start >= end {
		return nil
	}
	out := make([]llm.ToolResult, 0, end-start)
	for i := start; i < end; i++ {
		m := transcript[i]
		if m.ToolCallID == "" {
			continue
		}
		out = append(out, llm.ToolResult{ID: m.ToolCallID, Content: m.Content})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
