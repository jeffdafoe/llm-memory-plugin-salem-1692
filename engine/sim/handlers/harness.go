package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
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

	// DefaultMaxSpeaksPerTick is the per-tick committed-speak count past which the
	// harness STOPS RE-PROMPTING the model and ends the tick (ZBBS-HOME-402).
	// The post-speak nudge (commitResultContent) ASKS the model to call done()
	// once it has spoken; a weak stateful model ignores it and re-pitches a
	// REWORDED line the exact same-tick dedup can't catch (live: Josiah's 13
	// reworded greetings in 35s). This is checked at the ROUND boundary, so a
	// model that says its piece and calls done() still ends cleanly as Done
	// first (the cap never fires); it bites only a model that keeps speaking
	// WITHOUT done(). 2 preserves the legitimate greet-THEN-distinct-answer
	// two-beat (the case HOME-381's hard one-speak cap wrongly cut). Note: a
	// single response may still commit more than this many speaks in one batch
	// (bounded by MaxToolCallsPerResponse + the same-tick dedup) — the cap caps
	// ROUNDS of speaking, not speaks within one response.
	DefaultMaxSpeaksPerTick = 2
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

	// MaxSpeaksPerTick caps how many speaks an actor may COMMIT per tick
	// before the harness ends the tick (ZBBS-HOME-402 — teeth for the
	// post-speak done() nudge the weak model ignores). Zero →
	// DefaultMaxSpeaksPerTick.
	MaxSpeaksPerTick int

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
	maxSpeaksPerTick        int
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
	if cfg.MaxSpeaksPerTick <= 0 {
		cfg.MaxSpeaksPerTick = DefaultMaxSpeaksPerTick
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
		maxSpeaksPerTick:        cfg.MaxSpeaksPerTick,
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
	// ZBBS-HOME-397: the conversation_id grouping key, threaded alongside the
	// per-tick sceneID so memory-api can group the whole exchange under one
	// conversation in the admin chat viewer (see conversationIDFromPayload).
	conversationID := conversationIDForChat(actor, payload)

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
		h.persistTickToolResults(ctx, model, sceneID, conversationID, transcript, result.TerminalStatus)
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
	// that the buyer awaits the seller's accept/decline/counter — at any terms.
	// See payOfferKey for the keying rationale (price AND qty deliberately
	// excluded).
	offeredThisTick := map[string]struct{}{}
	// quotedThisTick holds the dedup key of every scene_quote this actor has
	// successfully POSTED this tick (ZBBS-HOME-433 — the seller-side analogue
	// of offeredThisTick). Pre-433 a posted quote came back as a bare [ok], so
	// the model had no within-tick reason to stop and re-posted the identical
	// quote every round to the iteration budget (the live John×Ezekiel bread
	// storm: five scene_quote calls in one tick). One quote per (item, qty,
	// disposition, target) per tick; see sceneQuoteKey for the keying
	// rationale (price deliberately excluded, qty kept).
	quotedThisTick := map[string]struct{}{}
	// triedThisTick holds the identical-call key (name + canonical decoded args)
	// of every action this actor has ATTEMPTED this tick under the ZBBS-HOME-414
	// general guard — the action tools without their own same-tick guard. Recorded
	// on FIRST attempt regardless of outcome (unlike spokenThisTick/offeredThisTick,
	// which record on success), so a byte-identical retry of a call that itself
	// FAILED is rejected, not just a repeat of a successful one — the degenerate
	// case is precisely accept_pay(234) re-fired after "no longer pending". See the
	// guard in the dispatch loop and genericCallKey for what is in/out of scope.
	triedThisTick := map[string]struct{}{}
	// speaksThisTick counts SUCCESSFUL speaks this tick (ZBBS-HOME-402). When it
	// reaches maxSpeaksPerTick the loop ends the tick — teeth for the post-speak
	// done() nudge the weak model ignores. Counts committed speaks only (a
	// bounced or deduped speak reached no one), mirroring spokenThisTick.
	speaksThisTick := 0
	// ephemeralText is the recency-dominant decision-support body sent with each
	// round's Complete call. It starts as the full per-tick perception furniture
	// (affordances + act-now coda) and swaps to the lean continuation body after
	// the first committed speak (ZBBS-HOME-411), so a model that has already
	// spoken reads a stop-biased decision instead of the affordances that prime a
	// re-pitch. See perception.RenderedPrompt.ContinuationText.
	ephemeralText := rendered.EphemeralText
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
			ConversationID:   conversationID,
			Messages:         transcript,
			Tools:            tools,
			EphemeralContext: ephemeralText,
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
			// the buyer awaits the seller's answer or calls done(). One offer per
			// (seller, item, disposition) per tick: the key excludes BOTH price
			// and qty (see payOfferKey), so a re-offer at drifting terms (5 coins,
			// then 10; or a changed quantity) still matches — the buyer reconsiders
			// next tick, after the seller responds. Quote-take and counter-response
			// paths are exempt (see payOfferKey).
			if key, isOffer := payOfferKey(vc); isOffer {
				if _, dup := offeredThisTick[key]; dup {
					observationOnly = false
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
					transcript = append(transcript, toolResultMsg(call.ID, "[error: already_offered] you already made an offer for that item to that seller this turn — wait for their answer, or call done()."))
					continue
				}
			}

			// ZBBS-HOME-433: same-tick repeat-QUOTE guard, the seller-side
			// analogue of the offer guard above. A posted quote already stands
			// before the whole scene — re-posting it (at any price) buys
			// nothing within this tick; the buyer answers on their own tick.
			if key, isQuote := sceneQuoteKey(vc); isQuote {
				if _, dup := quotedThisTick[key]; dup {
					observationOnly = false
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
					transcript = append(transcript, toolResultMsg(call.ID, "[error: already_quoted] your offer for that item already stands from this turn — the room has heard it; await an answer or call done()."))
					continue
				}
			}

			// ZBBS-HOME-414: tool-agnostic same-tick identical-call guard for the
			// action tools that lack their own (accept_pay / decline_pay /
			// counter_pay / deliver_order / withdraw_pay / consume / move_to). The
			// weak model re-fires a byte-identical call until the iteration budget —
			// accept_pay(234) after it is already accepted, consume(Milk x1) six
			// times, move_to(here) again — burning rounds and bloating the durable
			// transcript later ticks replay. Unlike the speak/offer guards above
			// (record on SUCCESS, so a bounced line may be retried after the
			// situation changes), this records on the FIRST attempt regardless of
			// outcome: the degenerate case IS the identical retry of a call that
			// FAILED, which a record-on-success guard would never catch. An identical
			// repeat is provably useless for these tools, so rejecting it model-facing
			// costs nothing and steers the model to a different action or done().
			if key, ok := genericCallKey(vc); ok {
				if _, dup := triedThisTick[key]; dup {
					observationOnly = false
					result.ToolsFailedRejected = append(result.ToolsFailedRejected, call.Name)
					transcript = append(transcript, toolResultMsg(call.ID, "[error: already_did_that] you already tried that exact action this turn — it won't change anything; do something different or call done()."))
					continue
				}
				triedThisTick[key] = struct{}{}
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
					speaksThisTick++
					// ZBBS-HOME-411: after the first committed speak, swap the
					// recency-dominant ephemeral to the lean continuation body —
					// dropping the affordances (inn/food/rest cues, act-now coda)
					// that prime a re-pitch. Idempotent on later speaks.
					ephemeralText = rendered.ContinuationText
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
				// ZBBS-HOME-433: record a posted quote so a later round (or a
				// later call in this same batch) re-posting the same (item,
				// disposition, target) is rejected by the guard above. Success-
				// only, like the offer record — a bounced quote created nothing
				// and may be retried.
				if key, isQuote := sceneQuoteKey(vc); isQuote {
					quotedThisTick[key] = struct{}{}
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

		// ZBBS-HOME-402: speak cap — teeth for the post-speak done() nudge the
		// weak stateful model ignores. The same-tick dedup (above) blocks verbatim
		// repeats and commitResultContent ASKS the model to call done() once it has
		// spoken, but the model re-pitches a REWORDED line the exact-dedup can't
		// catch (live: Josiah's 13 reworded greetings in 35s). Once it has
		// committed maxSpeaksPerTick speaks this tick, end the tick: it has had its
		// say. Deliberately checked HERE, after the batchEnded return above — a
		// model that says its piece and calls done() in the same batch returns Done
		// before this fires (the legitimate greet-then-answer-then-done two-beat);
		// this bites only a model that keeps speaking WITHOUT done(). Reuses
		// BudgetForced — a per-tick cap was hit and the rendered inputs were
		// addressed (the actor did act), so warrants don't re-fire.
		if speaksThisTick >= h.maxSpeaksPerTick {
			result.TerminalStatus = sim.TickStatusBudgetForced
			result.BudgetHit = true
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
			// A modelSafeError is a hand-authored, model-safe rejection (a
			// post-decode static check the handler ran — empty-after-trim, a
			// control char): surface its reason so the model self-corrects,
			// matching the command layer's sim.ModelFacingError echo below
			// (ZBBS-WORK-413). Every other handler error stays generic —
			// handler-internal detail (file paths, stack traces, API
			// responses) must not leak into the LLM transcript.
			var safe modelSafeError
			if errors.As(err, &safe) {
				log.Printf("handlers: dispatch %q: observation handler rejected: %v", vc.Name, err)
				return fmt.Sprintf("[error] %s", safe.Error()), dispatchOutcome{}
			}
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
			// Model-safe post-decode static-validation rejection → surface the
			// reason; any other build error stays generic (see the observation
			// branch above and ZBBS-WORK-413).
			var safe modelSafeError
			if errors.As(err, &safe) {
				log.Printf("handlers: dispatch %q: commit handler rejected: %v", vc.Name, err)
				return fmt.Sprintf("[error] %s", safe.Error()), dispatchOutcome{}
			}
			log.Printf("handlers: dispatch %q: commit handler failed: %v", vc.Name, err)
			return "[error: handler_failed] tool handler returned an error", dispatchOutcome{}
		}

		dispatchCtx := ctx
		if h.toolDispatchTimeout > 0 {
			var cancel context.CancelFunc
			dispatchCtx, cancel = context.WithTimeout(ctx, h.toolDispatchTimeout)
			defer cancel()
		}

		cmdResult, err := w.SendContext(dispatchCtx, sim.RunTickToolCommand(job.actorID, job.attemptID, job.rootEventID, cmd))
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
		return commitResultContent(vc, cmdResult), out

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
// cmdResult is the value the committed world command returned through
// RunTickToolCommand (nil for commands that return nothing). Most content
// below is composed from the call's decoded args alone; the consume branch
// needs the result because the ZBBS-WORK-391 needs-clamp decides the
// eaten/kept split on the world goroutine, after the args are fixed.
func commitResultContent(vc *ValidatedCall, cmdResult any) string {
	// A clamped consume must tell the model what actually happened: a bare
	// [ok] after "consume 10" reads as ten eaten, and the model either
	// re-consumes the surplus it doesn't know it holds or distrusts its
	// inventory. Unclamped consumes (Kept == 0) keep the generic [ok].
	if vc.Name == "consume" {
		if r, ok := cmdResult.(sim.ConsumeResult); ok && r.Kept > 0 {
			return fmt.Sprintf(
				"[ok] You consume %d %s — that satisfies you; the remaining %d stay in your pack. Do not consume more now.",
				r.Consumed, r.Kind, r.Kept,
			)
		}
	}
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
	// scene_quote: a clamped disposition must be reported for the same
	// reason as the pay clamp below (ZBBS-WORK-405) — the seller model
	// would otherwise believe it posted a take-home quote it didn't. Both
	// variants carry the post-quote steer (ZBBS-HOME-433): pre-433 a posted
	// quote returned a bare [ok], so the model had no within-tick reason to
	// stop and re-posted the identical quote to the iteration budget. The
	// steer is the soft half; quotedThisTick's already_quoted reject is the
	// teeth.
	if vc.Name == "scene_quote" {
		const quoteSteer = "The room has heard your offer — await an answer or call done(). Do not post the same offer again."
		// "Your offer now stands" only when the result proves a quote was
		// actually created (code_review #415) — an unexpected result shape
		// still steers, but doesn't assert state without evidence.
		if r, ok := cmdResult.(sim.SceneQuoteCreateResult); ok {
			if r.EatHereClamped {
				item := "those goods"
				if args, ok := vc.DecodedArgs.(SceneQuoteArgs); ok {
					if it := strings.ToLower(strings.Join(strings.Fields(args.ItemKind), " ")); it != "" {
						item = it
					}
				}
				return fmt.Sprintf(
					"[ok] Mind: %s can't be carried away — your offer stands as eat-here, taken on the spot. %s",
					item, quoteSteer,
				)
			}
			return "[ok] Your offer now stands. " + quoteSteer
		}
		return "[ok] " + quoteSteer
	}
	// offer_trade lowers onto a PayWithItemArgs (ZBBS-HOME-407), so it carries
	// the same decoded shape and earns the same post-offer steer.
	if vc.Name == "pay_with_item" || vc.Name == "offer_trade" {
		// ZBBS-WORK-405: when the engine clamped a take-home request to
		// eat-here (non-portable consumable), the feedback must say so —
		// same reasoning as the consume clamp above: a silently adjusted
		// action leaves the model believing it carried off goods it never
		// held. Rides the pending-offer steer below AND the generic [ok]
		// flows (quote take, counter-response).
		clampNote := ""
		if r, ok := cmdResult.(sim.PayWithItemResult); ok && r.EatHereClamped {
			clampItem := "those goods"
			if args, ok := vc.DecodedArgs.(PayWithItemArgs); ok {
				if it := strings.ToLower(strings.Join(strings.Fields(args.Item), " ")); it != "" {
					clampItem = it
				}
			}
			clampNote = fmt.Sprintf(
				" Mind: %s can't be carried away — this settles eat-here, taken on the spot.",
				clampItem,
			)
		}
		// A quote-take settles INSTANTLY (ZBBS-HOME-424): payment, goods,
		// and any consume_now meal all land inside this one tool call. The
		// old generic "[ok]" told the model nothing happened — and the
		// within-tick continuation body re-renders from the tick-start
		// snapshot, so the buyer's felt needs never legibly move either.
		// The model re-bought the same item to the iteration budget (six
		// meats in six seconds, live 2026-06-12). Voice what the settle
		// actually did — money, goods, the meal, and the post-meal felt
		// state computed from LIVE world state at commit — and steer to
		// done(). ZBBS-HOME-436; supersedes the pre-424 "quote-take
		// doesn't storm" assumption recorded below. Gated on Accepted so a
		// future fast-path result in any other state can't voice a settle
		// that didn't happen (code_review).
		if r, ok := cmdResult.(sim.PayWithItemResult); ok && r.FastPath && r.State == sim.PayLedgerStateAccepted {
			if args, ok := vc.DecodedArgs.(PayWithItemArgs); ok {
				return settledPayContent(args, r, clampNote)
			}
			if clampNote != "" {
				return "[ok]" + clampNote
			}
			return "[ok]"
		}
		if args, ok := vc.DecodedArgs.(PayWithItemArgs); ok {
			// A plain new offer (no quote_id / in_response_to) is now a pending
			// ledger entry the seller must accept, decline, or counter — the
			// buyer's move this tick is finished. Pre-395 this returned a bare
			// "[ok]", which read as "nothing happened, try again" and drove the
			// re-offer storm. Echo what was offered for salience (Llama-3.3 emits
			// empty assistant content on a tool call — the same weak-salience gap
			// the speak echo above closes) and steer to done(), forbidding the
			// re-offer. Counter-responses are a distinct flow that doesn't
			// storm, so they keep the generic "[ok]".
			if args.QuoteID == 0 && args.InResponseTo == 0 {
				item := strings.ToLower(strings.Join(strings.Fields(args.Item), " "))
				if item == "" {
					item = "those goods"
				}
				other := strings.TrimSpace(args.Seller)
				// A workplace-name reroute (ZBBS-HOME-460) resolved the offer to
				// the worker, not the building the model named — echo the real
				// recipient so "bide for their answer" points at a person who
				// can actually answer.
				if r, ok := cmdResult.(sim.PayWithItemResult); ok && r.ReroutedSellerName != "" {
					other = r.ReroutedSellerName
				}
				if other == "" {
					other = "them"
				}
				// offer_trade is a proposer-framed barter ("trade for X");
				// a plain pay_with_item buys ("buy X"). Same pending-offer
				// mechanics, different lead verb for legibility. Copy is light
				// period voice (ZBBS-HOME-421): NPCs mirror the register of what
				// they read, and the old contract language came back out of their
				// mouths verbatim. Keep the functional tokens (qty, item, seller,
				// done(), accept/decline/counter) intact in any rewording.
				lead := fmt.Sprintf("Your offer to buy %d %s", args.Qty, item)
				if vc.Name == "offer_trade" {
					lead = fmt.Sprintf("Your offer to trade for %d %s", args.Qty, item)
				}
				return fmt.Sprintf(
					"[ok] %s is before %s — bide for their answer. Make no second "+
						"offer; call done() and let them accept, decline, or counter.%s",
					lead, other, clampNote,
				)
			}
		}
		if clampNote != "" {
			return "[ok]" + clampNote
		}
	}
	return "[ok]"
}

// settledPayContent composes the buyer's tool feedback for an instantly-
// settled quote-take (ZBBS-HOME-436). The copy is light period voice
// (ZBBS-HOME-421) with the functional tokens (done()) intact. Each sentence
// states something the model cannot see anywhere else this tick: the settle
// happened, what it ate vs pocketed, and how it feels NOW — the perception
// body it re-reads after this message still shows tick-start needs.
func settledPayContent(args PayWithItemArgs, r sim.PayWithItemResult, clampNote string) string {
	// Defensive: decoded args drive the display. The command layer rejects
	// qty < 1 / amount < 0 before any settle, so these can't co-occur with
	// an Accepted result — but if they ever do, degrade to the generic ok
	// rather than voice a nonsensical settle ("0 meat", a negative price
	// normalized into a free purchase). Same no-claims-without-evidence
	// rule as the nil-result path (code_review).
	if args.Qty <= 0 || args.Amount < 0 {
		if clampNote != "" {
			return "[ok]" + clampNote
		}
		return "[ok]"
	}
	item := strings.ToLower(strings.Join(strings.Fields(args.Item), " "))
	if item == "" {
		item = "those goods"
	}
	seller := strings.TrimSpace(args.Seller)
	if seller == "" {
		seller = "them"
	}
	var b strings.Builder
	if args.Amount > 0 {
		coinWord := "coins"
		if args.Amount == 1 {
			coinWord = "coin"
		}
		fmt.Fprintf(&b, "[ok] Settled on the spot — you pay %s %d %s for %d %s.", seller, args.Amount, coinWord, args.Qty, item)
	} else {
		fmt.Fprintf(&b, "[ok] Settled on the spot — %s hands over %d %s for nothing.", seller, args.Qty, item)
	}
	b.WriteString(clampNote)
	// ZBBS-WORK-409: an eat-here meal/drink keeps easing the need for MealMinutes
	// after the first bite, but only while the buyer stays put — walking off
	// deletes the dwell credit, wasting the food and the coins and leaving the
	// need to return. The generic "call done() now unless something else needs
	// you" closer read as "free to leave" and let NPCs bolt mid-meal (Prudence,
	// Inn, 2026-06-15), since nothing told them a sit-down meal means staying.
	// Voice the same stay message as the perception dwell line (sim.DwellStayClause)
	// so the buyer hears it consistently, fold in the WORK-391/405 anti-rebuy
	// guard, and make clear done() keeps them seated. Gated to hunger/thirst —
	// the only needs purchasable eat-here items satisfy; any other dwell falls
	// through to the generic closer below.
	if r.MealMinutes > 0 && (r.SatisfiesNeed == "hunger" || r.SatisfiesNeed == "thirst") {
		gerund := "eating"
		noMore := " Buy no more food now."
		if r.SatisfiesNeed == "thirst" {
			gerund = "drinking"
			noMore = " Buy no more drink now."
		}
		stay := sim.DwellStayClause(r.MealMinutes, r.SatisfiesNeed, " and the coins you paid")
		stay = strings.ToUpper(stay[:1]) + stay[1:]
		fmt.Fprintf(&b, " You start %s it now. %s.", gerund, stay)
		if r.KeptToInventory > 0 {
			fmt.Fprintf(&b, " The other %d goes into your pack.", r.KeptToInventory)
		}
		b.WriteString(noMore)
		fmt.Fprintf(&b, " Call done() to keep %s where you sit.", gerund)
		return b.String()
	}
	switch {
	case r.BuyerAte > 0 && r.KeptToInventory > 0:
		fmt.Fprintf(&b, " You eat %d now; %d goes into your pack — you can absorb no more.", r.BuyerAte, r.KeptToInventory)
	case r.BuyerAte == 1:
		b.WriteString(" You eat it now.")
	case r.BuyerAte > 1:
		fmt.Fprintf(&b, " You eat %d now.", r.BuyerAte)
	case r.KeptToInventory > 0:
		// Group order: others ate, the surplus pockets to the buyer.
		fmt.Fprintf(&b, " %d uneaten goes into your pack.", r.KeptToInventory)
	}
	if r.BuyerAte > 0 {
		if r.FeltAfter != "" {
			fmt.Fprintf(&b, " You still feel %s.", r.FeltAfter)
		} else {
			switch r.SatisfiesNeed {
			case "hunger":
				b.WriteString(" Your hunger is met — buy no more food now.")
			case "thirst":
				b.WriteString(" Your thirst is met — buy no more drink now.")
			default:
				b.WriteString(" You are satisfied — buy no more now.")
			}
		}
	}
	if r.TookHome {
		b.WriteString(" The goods are in your pack.")
	}
	if r.Booked {
		b.WriteString(" Your lodging is booked — the keeper will see you checked in.")
	}
	b.WriteString(" Call done() now unless something else needs you.")
	return b.String()
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
// plain new offer. The key is (seller, item, disposition) — the offer's identity
// MINUS its terms. Both price (amount) AND quantity (qty) are excluded by
// design: the rule is one pending offer per (seller, item, disposition) per
// tick. Once an offer is before the seller, the buyer awaits their
// accept/decline/counter rather than placing a second offer the seller has not
// yet seen — at ANY terms; a genuine change of price or quantity belongs in the
// NEXT tick, after the response. Excluding the terms is also what makes the
// guard robust to the observed storm, which re-offered the same item at a
// drifting price (5 coins, then 10, then 10…) every round — a key that included
// the terms would let each drifted re-offer straight through.
//
// Scoped to the default pending-offer path: a quote take (quote_id) closes the
// deal instantly and a counter-response (in_response_to) is a deliberate,
// distinct move, so neither storms — both pass through untouched, matching the
// commitResultContent steer's scope. Mirrors speakUtteranceKey; seller and item
// are lowercased + whitespace-collapsed the same way so trivial spacing/case
// drift in a repeat still matches. The disposition byte (keep vs consume-now)
// keeps a genuine "buy one to keep AND one to eat now" pair distinct.
func payOfferKey(vc *ValidatedCall) (string, bool) {
	// offer_trade lowers onto a PayWithItemArgs (ZBBS-HOME-407), so it mints
	// the same kind of pending offer and earns the same same-tick dedup.
	if vc == nil || (vc.Name != "pay_with_item" && vc.Name != "offer_trade") {
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

// sceneQuoteKey returns the same-tick repeat-QUOTE dedup key for a scene_quote
// call (ZBBS-HOME-433 — the seller-side analogue of payOfferKey). One quote per
// (item, qty, disposition, target) per tick: price (amount) is deliberately
// EXCLUDED, so a re-post at a drifting price (4 coins, then 5) still matches —
// within one tick a re-quote of the same lot is churn at any price (the live
// John×Ezekiel bread storm: five identical scene_quote calls in one tick, all
// succeeding, terminal budget_forced). Qty IS part of the key — unlike a
// buyer's pending offer, a different lot size (1 bread vs 3 bread) is a
// genuinely distinct standing offer, and the substrate's own supersede/coexist
// rules should decide its fate (code_review #415). Cross-tick re-pricing stays
// legal and rides the supersede path. Returns ("", false) for non-quote calls
// or undecodable args.
func sceneQuoteKey(vc *ValidatedCall) (string, bool) {
	if vc == nil || vc.Name != "scene_quote" {
		return "", false
	}
	args, ok := vc.DecodedArgs.(SceneQuoteArgs)
	if !ok {
		return "", false
	}
	item := strings.ToLower(strings.Join(strings.Fields(args.ItemKind), " "))
	if item == "" {
		return "", false
	}
	target := strings.ToLower(strings.Join(strings.Fields(args.TargetBuyer), " "))
	disposition := "keep"
	if args.ConsumeNow {
		disposition = "consume"
	}
	return item + "\x00" + disposition + "\x00" + target + "\x00" + strconv.Itoa(args.Qty), true
}

// genericCallKey returns the same-tick identical-call dedup key for the
// ZBBS-HOME-414 guard: the tool name plus a canonical JSON rendering of the
// DECODED args. Returns ("", false) — the guard does NOT apply — unless the tool
// is on the explicit action allowlist below.
//
// The allowlist (rather than a broad "any non-observation commit" test) is
// deliberate: the guard is tool-agnostic in MECHANISM but explicit in SCOPE, so
// (a) a newly-added commit tool does not silently inherit same-args dedup that
// may be wrong for it, and (b) the boundary the code enforces matches the
// boundary this comment documents (code_review HOME-414). speak and the offer
// family are also excluded — they own their own broader, success-only same-tick
// guards (speakUtteranceKey / payOfferKey) — as are observation-class calls
// (pure thinking is not penalized, ZBBS-WORK-321).
//
// The key is canonical JSON (json.Marshal), not %#v: for structs encoding/json
// preserves field order and for maps it sorts keys, so the "canonical decoded
// args" claim holds for any future allowlisted tool's arg shape rather than
// relying on the implicit invariant that every arg type is a flat struct.
func genericCallKey(vc *ValidatedCall) (string, bool) {
	if vc == nil || vc.Entry == nil {
		return "", false
	}
	if _, isSpeak := speakUtteranceKey(vc); isSpeak {
		return "", false
	}
	if _, isOffer := payOfferKey(vc); isOffer {
		return "", false
	}
	if vc.Entry.Class == ClassObservation {
		return "", false
	}
	switch vc.Name {
	case "accept_pay", "decline_pay", "counter_pay", "deliver_order", "withdraw_pay", "consume", "move_to":
		// The action tools where a byte-identical repeat in one tick is provably
		// useless: a resolve-by-id call cannot resolve the same id twice, consume
		// should pass a larger qty, move_to to your current place is a no-op.
	default:
		return "", false
	}
	b, err := json.Marshal(vc.DecodedArgs)
	if err != nil {
		// A non-marshalable args value can't be keyed — fail open (no dedup)
		// rather than risk a wrong collision. Not expected for any allowlisted
		// tool's args.
		return "", false
	}
	return vc.Name + "\x00" + string(b), true
}

// conversationIDFromPayload returns the narrative-beat scene id the perception
// was built within (sim.Scene.ID, via the primary SceneView) — the cross-tick,
// cross-participant conversation_id (ZBBS-HOME-397). All ticks of one
// conversation beat, by every participant, resolve to the same primary scene, so
// stamping it on the chat rows lets memory-api collapse the whole exchange into
// one conversation in the admin viewer. Empty when no primary scene resolved (a
// solo tick with no active huddle) so the row stays ungrouped, like
// companion-mode chat.
func conversationIDFromPayload(p perception.Payload) string {
	if p.Primary == nil {
		return ""
	}
	return string(p.Primary.SceneID)
}

// conversationIDForChat derives the conversation_id grouping key for the chat
// rows this tick emits (ZBBS-HOME-417). It prefers the actor's huddle id — the
// actual conversation unit, which begins when an exchange starts and ends when
// the silence sweep concludes it — so the admin viewer groups one exchange per
// huddle. It falls back to the scene id (conversationIDFromPayload) for
// huddle-less speech (a solo tick), preserving that case's prior grouping.
//
// Why the huddle, not the scene: an indoor structure scene is intentionally
// durable and reused across every conversation at the structure (it anchors the
// pay ledger), so keying conversation_id on it collapsed a busy structure's
// entire multi-day history into one "conversation" in the viewer. The huddle
// rotates per exchange — which is the grouping HOME-397 intended. The admin
// chat viewer is the only consumer of conversation_id, so this is a pure
// grouping-granularity change with no behavioral effect.
func conversationIDForChat(actor *sim.ActorSnapshot, p perception.Payload) string {
	if actor != nil && actor.CurrentHuddleID != "" {
		return string(actor.CurrentHuddleID)
	}
	return conversationIDFromPayload(p)
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
	model, sceneID, conversationID string,
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
		Model:          model,
		SceneID:        sceneID,
		ConversationID: conversationID,
		Results:        results,
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
