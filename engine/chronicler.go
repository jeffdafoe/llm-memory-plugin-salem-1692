package main

// Salem Chronicler dispatcher and integration.
//
// The Chronicler is a virtual agent (salem-chronicler) that fires at
// scheduled phase boundaries (dawn / midday / dusk per game day) and at
// cascade origins (PC speech in a structure, NPC arrival after walk).
// It writes atmosphere via set_environment and records sticky narrative
// facts via record_event into shared world state. NPC perception
// builders read those tables on each tick so what the chronicler curates
// becomes the world the NPCs decide inside.
//
// The chronicler does NOT direct or move NPCs. There is no dispatch_tick
// tool. NPCs fire only on reactive events (existing handlers); the
// chronicler's leverage is entirely indirect, through the perception.
//
// Canonical design: shared/notes/codebase/salem/overseer-design.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// chroniclerAgent is the slug of the directorial virtual agent the
	// engine fires at phase boundaries and cascade origins. Realms=['salem']
	// matches the four NPCs and the engine itself, so realm-overlap permits
	// cross-namespace recall against any of them.
	chroniclerAgent = "salem-chronicler"

	// Harness budget — max iterations per chronicler fire. Each
	// iteration is one model API call processing one tool call
	// (set_environment, record_event, recall, attend_to, done).
	// Default 8; pre-buffering this was 4 in-code, plenty when each
	// fire saw one event. Post-buffering (ZBBS-119) the dispatcher
	// consolidates 5+ events into one fire so the chronicler needs
	// more iterations to process them all — at 4 iterations the
	// effective attend ceiling was ~3 NPCs per fire (one slot for
	// done). Loaded per fire via chronicler_tick_budget setting,
	// clamped to [chroniclerTickBudgetMin, chroniclerTickBudgetMax].
	chroniclerTickBudgetDefault = 8
	chroniclerTickBudgetMin     = 1
	chroniclerTickBudgetMax     = 32

	// settingKeyChroniclerTickBudget is the setting row read on each
	// fire so an admin tweak takes effect on the next fire without an
	// engine restart.
	settingKeyChroniclerTickBudget = "chronicler_tick_budget"

	// Recent atmospheric statements surfaced in the chronicler's own
	// perception. Lets it evolve atmosphere coherently rather than
	// pivoting wildly each fire.
	chroniclerEnvHistoryCount = 3

	// Recent events surfaced in NPC and chronicler perceptions. Cap to
	// avoid prompt bloat; events older than the lookback window can
	// still be surfaced via recall but don't appear automatically.
	recentEventsCount  = 20
	recentEventsWindow = 7 * 24 * time.Hour

	// Cross-fire attend cooldown (ZBBS-119). Routine chronicler
	// attend_to calls — those whose fire reason is buffered_flush,
	// phase, or shift_boundary — refuse a re-attend within this
	// window of the prior dispatch. PC-speech and admin-attend-now
	// cascade fires are exempt: a player's presence or an operator's
	// override is a fresh significant event, not redundant signal.
	// The chronicler sees the rejection as a tool result so it can
	// reason about it instead of getting a silent in-flight-gate drop.
	chroniclerAttendCooldown = 45 * time.Second
)

// chroniclerFireReason captures why the chronicler is being fired this
// invocation. Used to render the perception's opening line and to
// stamp the phase column on world_environment writes.
type chroniclerFireReason struct {
	// Type is "phase" (scheduled boundary), "cascade" (event-driven), or
	// "shift_boundary" (agent-NPC shift start/end queued by the worker
	// scheduler — see dispatch_queue.go).
	Type string

	// Phase is set when Type == "phase". One of "dawn" | "midday" | "dusk".
	// Also stamped onto world_environment rows the chronicler writes
	// during this fire.
	Phase string

	// CascadeReason is set when Type == "cascade". Free-text describing
	// what triggered the fire — "pc-spoke (Jefferey)" / "arrival" / etc.
	CascadeReason string

	// StructureID is set when Type == "cascade". The location where the
	// cascade originated, used to ground the chronicler's perception.
	StructureID string

	// Priority is the routing tier consulted by fireChroniclerSerialized
	// when the in-flight slot is full and by chroniclerAttendExempt for
	// cooldown bypass. High-priority fires (PC speech, PC arrival, admin
	// attend-now) get queued as pending instead of dropped on full and
	// bypass the cross-fire attend cooldown. Routine fires (phase, shift
	// boundary, NPC arrival cascade-origin, buffered_flush) drop on full
	// and apply cooldown — their underlying events live on
	// ChroniclerDispatchQueue and the next fire will pick them up.
	//
	// Replaces the prior reason-string prefix check (chroniclerAttendExempt
	// previously parsed CascadeReason for "pc-" / "admin-attend-now");
	// classification is now explicit at the call site.
	Priority chroniclerFirePriority
}

// chroniclerFirePriority is the routing tier on chroniclerFireReason.
// Reserved third tier (e.g. PriorityImmediate that preempts an active
// fire) intentionally not added until a concrete use case appears —
// salem is laid back; no emergency events exist today. The dispatcher's
// serialization invariant (one fire per world at a time) holds
// regardless of how many priority tiers are added later.
type chroniclerFirePriority int

const (
	chroniclerFirePriorityRoutine chroniclerFirePriority = iota
	chroniclerFirePriorityHigh
)

// pendingChroniclerFire holds a high-priority cascade fire that
// arrived while ChroniclerFireSem was occupied. Stored on
// app.ChroniclerPendingFire and drained by releaseChroniclerFireSem
// right after the active fire releases the sem.
type pendingChroniclerFire struct {
	Reason     chroniclerFireReason
	EnqueuedAt time.Time
}

// chroniclerToolSpec returns the tool definitions offered to the
// chronicler at every fire.
//
// attend_to (ZBBS-083) is the directorial action — the overseer rouses a
// villager whose body is in distress so they have a chance to act on it.
// The roused NPC ticks through the normal agent path and decides for
// themselves what to do (eat, drink, rest, leave). attend_to does NOT
// puppet — it only opens the door for action.
//
// Per-fire calls to attend_to are capped by chronicler_dispatch_ceiling
// in the dispatcher. The model is told the limit is finite but not the
// exact number, to discourage greedy use-until-rejected patterns.
func chroniclerToolSpec() []agentToolDef {
	return []agentToolDef{
		{
			Name:        "set_environment",
			Description: "Author the village's current atmosphere — weather, mood, ambient texture. Plain prose, biblical in cadence. NPCs perceive this when they next tick. Last write wins; you may evolve atmosphere across phases.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{
						"type":        "string",
						"description": "Atmospheric description. One or two sentences. No preaching, no editorializing.",
					},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "record_event",
			Description: "Record a narrative fact that should persist in village memory — births, deaths, accusations, fires, illnesses, harvests. Append-only. Default scope is village (everyone perceives).",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{
						"type":        "string",
						"description": "Plain statement of fact. Period-correct prose.",
					},
					"scope_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"village", "local", "private"},
						"description": "Visibility. Defaults to 'village' (everyone). 'local' restricts to one structure (set scope_target to that structure id). 'private' restricts to one NPC (set scope_target to that NPC's id).",
					},
					"scope_target": map[string]interface{}{
						"type":        "string",
						"description": "Required for local (structure id) or private (npc id). Omit for village.",
					},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "recall",
			Description: "Search Salem's collective memory — your own past observations, what each NPC has been thinking, dreams, impressions. Use this when you want to remember anything the village has experienced.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "What you want to remember.",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "attend_to",
			Description: "Direct your attention to a named villager so they may rouse and tend to themselves. Use when you see a soul whose body is in distress — hungry, parched, or weary — and a small voice within them might move them to act. Also use when a villager's shift has begun and they are not yet at their workplace, or when their shift has ended and they are still at their workplace. The villager you attend to will think and may take an action; you do not move their hands. Use sparingly: there is a finite measure to how many you may attend in one waking.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"villager": map[string]interface{}{
						"type":        "string",
						"description": "The villager you attend, by name (the same name that appears in your perception of who is at each place).",
					},
				},
				"required": []string{"villager"},
			},
		},
		{
			Name:        "done",
			Description: "Finish your fire. Use when you have nothing more to write or record.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

// dispatchChroniclerPhase is the per-server-tick entry that handles
// scheduled chronicler fires at dawn / midday / dusk. Called from
// runServerTickOnce.
//
// Phase boundaries are computed from the existing world_dawn_time and
// world_dusk_time settings. Midday is the midpoint between dawn and
// dusk. State is persisted in setting rows so we don't double-fire and
// so we catch up correctly after server restart (firing only the most
// recently missed boundary, not every missed one).
//
// Phase state is only advanced when the fire succeeds — if the chat API
// is down or returns an error on iteration 0, the boundary stays
// unfired and we retry on the next server tick.
//
// Honors AgentTicksPaused — if the admin has halted agent activity for
// debugging, the chronicler stops too.
func (app *App) dispatchChroniclerPhase(ctx context.Context) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("chronicler-phase: load config: %v", err)
		return
	}
	if cfg.AgentTicksPaused {
		return
	}

	dawnH, dawnM, err := parseHM(cfg.DawnTime)
	if err != nil {
		log.Printf("chronicler-phase: bad dawn time %q: %v", cfg.DawnTime, err)
		return
	}
	duskH, duskM, err := parseHM(cfg.DuskTime)
	if err != nil {
		log.Printf("chronicler-phase: bad dusk time %q: %v", cfg.DuskTime, err)
		return
	}

	// Reject invalid configs early — dusk must come after dawn within
	// the same calendar day. Equal/inverted values would cause boundary
	// collisions (midday == dawn == dusk) and ambiguous phase ordering.
	// This is admin error, not a runtime case to handle gracefully.
	if duskH*60+duskM <= dawnH*60+dawnM {
		log.Printf("chronicler-phase: dusk must be after dawn within the same day (dawn=%s dusk=%s); skipping", cfg.DawnTime, cfg.DuskTime)
		return
	}

	now := time.Now().In(cfg.Location)
	currentPhase, boundaryAt := mostRecentChroniclerPhase(now, dawnH, dawnM, duskH, duskM)

	lastAt := app.loadChroniclerLastPhaseFiredAt(ctx)

	// Already fired this boundary (or anything later)? Skip.
	if !lastAt.IsZero() && !lastAt.Before(boundaryAt) {
		return
	}

	log.Printf("chronicler-phase: firing %s phase (boundary at %s)", currentPhase, boundaryAt.Format(time.RFC3339))
	if err := app.fireChronicler(ctx, chroniclerFireReason{Type: "phase", Phase: currentPhase, Priority: chroniclerFirePriorityRoutine}); err != nil {
		log.Printf("chronicler-phase: fire failed, not advancing phase state: %v", err)
		return
	}
	app.saveChroniclerPhaseState(ctx, currentPhase, boundaryAt)

	// Village event for the phase boundary so the village-log tab can
	// render "Dawn breaks over the village." etc. World-wide event —
	// no actor, no structure, no coordinate. Only writes after a
	// successful chronicler fire so a failed phase doesn't pollute the
	// log with a phantom transition. Guarded against unknown phase
	// strings (would empty-string both fields and trip the CHECK
	// constraint silently).
	eventType := villageEventTypeForPhase(currentPhase)
	eventText := villageEventTextForPhase(currentPhase)
	if eventType != "" && eventText != "" {
		app.recordVillageEvent(ctx, eventType, eventText, "", "", nil, nil)
	} else {
		log.Printf("chronicler-phase: unknown phase %q, skipping village_event", currentPhase)
	}
}

// villageEventTypeForPhase maps the chronicler phase string ("dawn" /
// "midday" / "dusk") to the corresponding village_event event_type.
func villageEventTypeForPhase(phase string) string {
	switch phase {
	case "dawn":
		return villageEventPhaseDawn
	case "midday":
		return villageEventPhaseMidday
	case "dusk":
		return villageEventPhaseDusk
	}
	return ""
}

// villageEventTextForPhase produces the player-readable narration line
// that lands in the Village tab when a phase fires. Kept simple — the
// chronicler's atmospheric prose for the phase lives in
// world_environment and surfaces via the marquee ticker, not here.
func villageEventTextForPhase(phase string) string {
	switch phase {
	case "dawn":
		return "Dawn breaks over the village."
	case "midday":
		return "Midday settles over the village."
	case "dusk":
		return "Dusk falls over the village."
	}
	return ""
}

// cascadeOriginFireChronicler is called from cascade-origin handlers
// (PC speech in pc_handlers.go, NPC arrival in npc_movement.go) when a
// new scene starts. The chronicler may set atmosphere, record an event,
// recall, or just say done. Fire-and-forget — runs in a background
// goroutine so it doesn't block the caller's cascade dispatch.
//
// NOT called for ticks WITHIN an existing cascade (NPCs reacting to
// each other's speech inside an in-flight scene). Only at scene-starts.
// Bounds chronicler cost — once per scene, not per utterance.
//
// Two concurrency policies, picked at call time by the
// chronicler_buffered_dispatch feature flag (ZBBS-119):
//
//   - flag ON  → ChroniclerFireSem (size 1). Single in-flight slot
//     across cascade fires AND the buffered dispatcher's flush. No
//     two chronicler fires run in parallel. High-priority fires that
//     hit a full sem are queued as pending and run after the active
//     fire releases (so PC speech / PC arrival / admin attend-now
//     reactions are never dropped); routine fires drop on full and
//     rely on ChroniclerDispatchQueue + the buffered timer to retry.
//   - flag OFF → ChroniclerSem (size 2, legacy). Two concurrent
//     cascades allowed. This is the diagnosed parallel-cascade race;
//     buffering is the fix, this branch stays only as the rollback.
//
// Caller declares the fire's priority so the queue-or-drop choice is
// explicit at the source — no reason-string parsing.
func (app *App) cascadeOriginFireChronicler(reasonStr, structureID string, priority chroniclerFirePriority) {
	reason := chroniclerFireReason{
		Type:          "cascade",
		CascadeReason: reasonStr,
		StructureID:   structureID,
		Priority:      priority,
	}
	if app.chroniclerBufferedDispatchEnabled(context.Background()) {
		app.fireChroniclerSerialized(reason)
		return
	}
	if app.ChroniclerSem == nil {
		// Defensive — should always be initialized at startup. If not,
		// fall through to the unbounded path so we don't silently drop
		// fires on a misconfigured engine.
		go app.runCascadeFire(reason)
		return
	}
	select {
	case app.ChroniclerSem <- struct{}{}:
		go func() {
			defer func() { <-app.ChroniclerSem }()
			app.runCascadeFire(reason)
		}()
	default:
		log.Printf("chronicler-cascade: slot full, skipping fire (reason=%q)", reasonStr)
	}
}

// fireChroniclerSerialized is the size-1 sem path used when
// chronicler_buffered_dispatch is on. Same shape as the legacy sem
// branch in cascadeOriginFireChronicler but routes through
// ChroniclerFireSem so cascade fires and buffered-dispatcher timer
// flushes share one in-flight slot.
//
// Drop-on-full behavior depends on Priority:
//   - High: queued in ChroniclerPendingFire so the fire runs right after
//     the active one releases. Last-one-wins coalesces bursts; the
//     underlying NPC-event queue is drained by every fire so events
//     from coalesced reasons aren't lost, only their cascade-origin
//     metadata.
//   - Routine: dropped (logged). Events stay on ChroniclerDispatchQueue
//     for the buffered timer's next fire to pick up.
func (app *App) fireChroniclerSerialized(reason chroniclerFireReason) {
	if app.ChroniclerFireSem == nil {
		// Defensive — same as the legacy fall-through.
		go app.runCascadeFire(reason)
		return
	}
	select {
	case app.ChroniclerFireSem <- struct{}{}:
		go func() {
			defer app.releaseChroniclerFireSem()
			app.runCascadeFire(reason)
		}()
	default:
		if reason.Priority == chroniclerFirePriorityHigh {
			app.queuePendingChroniclerFire(reason)
			log.Printf("chronicler-buffered: fire slot full, queued high-pri (reason=%q) for after current fire", reason.CascadeReason)
		} else {
			log.Printf("chronicler-buffered: fire slot full, skipping (reason=%q) — events stay queued for next fire", reason.CascadeReason)
		}
	}
}

// queuePendingChroniclerFire stores a high-priority cascade fire to
// run after the currently in-flight chronicler fire releases the sem.
// Last-one-wins on the single slot: a burst of high-priority fires
// arriving in the same busy window collapses to one follow-up. The
// ChroniclerDispatchQueue is drained on every fire so the underlying
// NPC events from coalesced reasons are still captured in the
// follow-up fire's perception — only the cascade-origin metadata
// (which structure to anchor at) gets coalesced.
func (app *App) queuePendingChroniclerFire(reason chroniclerFireReason) {
	app.ChroniclerPendingFireMu.Lock()
	defer app.ChroniclerPendingFireMu.Unlock()
	app.ChroniclerPendingFire = &pendingChroniclerFire{
		Reason:     reason,
		EnqueuedAt: time.Now(),
	}
}

// releaseChroniclerFireSem completes a chronicler fire and either
// chains into a pending high-priority fire or releases the in-flight
// slot. Sem-inheritance: when a pending fire exists, the sem token is
// NOT released; instead the pending fire's runCascadeFire runs in a
// new goroutine with the held slot, and chains back through this
// function on completion via deferred release.
//
// This eliminates the race the reviewer flagged where releasing the
// sem before claiming pending opens a gap for an external acquire to
// slip in. A new fire arriving in that gap would acquire the sem and
// later clobber a pending entry that the original release was about
// to launch. With sem-inheritance, external callers see the slot as
// held until the chain exhausts; they queue as pending and run in
// order via the chain.
//
// The defer release inside the spawned goroutine ensures the slot is
// freed (or chained again) even if runCascadeFire panics.
//
// Recursion is bounded by the rate of high-priority events arriving
// during the chain: each follow-up fire takes a chronicler API call
// (seconds), so this is a chain of distinct fires, not a tight loop.
// Goroutine spawn per chain link (no stack growth).
func (app *App) releaseChroniclerFireSem() {
	app.ChroniclerPendingFireMu.Lock()
	pending := app.ChroniclerPendingFire
	app.ChroniclerPendingFire = nil
	app.ChroniclerPendingFireMu.Unlock()

	if pending != nil {
		go func(reason chroniclerFireReason) {
			defer app.releaseChroniclerFireSem()
			app.runCascadeFire(reason)
		}(pending.Reason)
		return
	}

	<-app.ChroniclerFireSem
}

// runCascadeFire is the body of a cascade-origin chronicler fire.
// Extracted so cascadeOriginFireChronicler can wrap it with the
// concurrency-cap semaphore.
func (app *App) runCascadeFire(reason chroniclerFireReason) {
	ctx := context.Background()
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("chronicler-cascade: load config: %v", err)
		return
	}
	if cfg.AgentTicksPaused {
		return
	}
	if err := app.fireChronicler(ctx, reason); err != nil {
		log.Printf("chronicler-cascade: fire failed: %v", err)
	}
}

// fireChronicler runs the chronicler's harness loop for one fire. Same
// shape as runAgentTick for NPCs — build perception, send chat, resolve
// tool calls, repeat until terminal or budget exhausted.
//
// Tool resolution: set_environment and record_event are commits-as-
// observations; they execute and the chronicler gets a confirmation
// message back so it can decide whether to continue (write another
// event, or done). recall is a pure observation. done terminates.
//
// If the chronicler returns plain text instead of a tool call, treat
// it as an implicit set_environment so the writing isn't lost.
//
// Returns nil if the chronicler reached the model at least once
// (regardless of whether it produced output). Returns an error when
// the very first sendChat fails — in that case the caller should NOT
// advance phase state (we never got the chronicler's turn at all).
// Subsequent-iteration sendChat errors are logged but not surfaced;
// at that point the chronicler did get its turn and any partial
// output (a previous successful set_environment / record_event) has
// already been persisted.
//
// On every successful fire (cleanly completed or budget-exhausted),
// also stamps the attention state — a separate timestamp from the
// phase state, used by the activity digest as the "since when" cutoff
// so cascade fires don't repeatedly digest the same activity window.
func (app *App) fireChronicler(ctx context.Context, reason chroniclerFireReason) error {
	// Drain the dispatch queue here, before perception build, so the
	// destructive action is tied to an actual chronicler invocation
	// rather than perception formatting. Any fire (phase, cascade,
	// shift_boundary) picks up pending events; the perception render is
	// a pure formatting step. Drained events are lost if the LLM call
	// later fails — acceptable trade-off; alternative is a claim/commit
	// model and the queue's content is best-effort context, not durable
	// state.
	shiftBatches := app.ChroniclerDispatchQueue.drain()
	perception := app.buildChroniclerPerception(ctx, reason, shiftBatches)
	tools := chroniclerToolSpec()

	currentMessage := perception
	currentToolCallID := ""
	chatSucceeded := false
	// Group this fire's harness iterations under one scene_id (MEM-121)
	// so the admin UI can collapse them into a single expandable row.
	// reason.StructureID carries the cascade origin for "cascade" fires
	// and is empty for "phase" / "shift_boundary" fires — both are
	// village-wide, so newScene records NULL structure_id and the admin
	// UI shows no location chip.
	sceneID := app.newScene(ctx, reason.StructureID)

	// Per-fire attend_to counter (ZBBS-083). Reset each fire — the
	// configured ceiling is per-fire, not per-day. Read once at the top
	// so an admin tweaking the setting mid-fire doesn't change the cap
	// underneath us. loadNonNegativeIntSetting clamps a negative value to
	// the default rather than rejecting every call.
	attendCeiling := app.loadNonNegativeIntSetting(ctx, "chronicler_dispatch_ceiling", 12)
	attendCount := 0

	// Per-fire seen-villager set (2026-05-02). Models occasionally fire
	// attend_to twice for the same villager in a single cascade — saw a
	// "Prudence Ward" repeat 6 seconds apart at 11:01:08, burning a tool
	// slot toward the rate limit for no narrative effect. The dispatch
	// would also re-tick the same NPC. Refusing the duplicate at the
	// chronicler boundary saves the call and tells the model to move on.
	attendedThisFire := map[string]bool{}

	// Per-fire tick budget — read from settings each fire so an admin
	// tweak takes effect immediately. Out-of-bounds values fall back
	// to the default rather than letting a misconfigured row break
	// every fire.
	tickBudget := app.loadNonNegativeIntSetting(ctx, settingKeyChroniclerTickBudget, chroniclerTickBudgetDefault)
	if tickBudget < chroniclerTickBudgetMin || tickBudget > chroniclerTickBudgetMax {
		log.Printf("chronicler: tick budget %d out of bounds [%d, %d], using default %d",
			tickBudget, chroniclerTickBudgetMin, chroniclerTickBudgetMax, chroniclerTickBudgetDefault)
		tickBudget = chroniclerTickBudgetDefault
	}

	// Hoist the structure-name lookup outside the iteration loop —
	// see the matching comment in agent_tick.go for the rationale.
	sceneStructure := app.lookupSceneStructureName(ctx, sceneID)

	for iter := 0; iter < tickBudget; iter++ {
		reply, err := app.npcChatClient.sendChat(ctx, chroniclerAgent, currentMessage, currentToolCallID, sceneID, sceneStructure, tools)
		if err != nil {
			log.Printf("chronicler iter=%d: %v", iter, err)
			if !chatSucceeded {
				// Total failure — never reached the model. Caller
				// should not advance phase state.
				return fmt.Errorf("chronicler chat failed at iteration %d: %w", iter, err)
			}
			// Partial success — already produced some output in earlier
			// iterations. Bail but treat as "done enough."
			break
		}
		chatSucceeded = true

		if len(reply.ToolCalls) == 0 {
			// Chronicler returned plain text — treat as implicit
			// set_environment so the writing isn't lost. Models
			// occasionally narrate without explicit tool calls.
			text := strings.TrimSpace(reply.Text)
			if text != "" {
				if err := app.recordEnvironment(ctx, text, reason.Phase); err != nil {
					log.Printf("chronicler implicit set_environment: %v", err)
				} else {
					log.Printf("chronicler: implicit set_environment used (model returned plain text instead of tool call)")
				}
			}
			break
		}

		// Pick the first tool call. The harness loop iterates so
		// multiple tool calls across iterations are supported (write
		// atmosphere, then record an event, then done — three turns).
		tc := &reply.ToolCalls[0]
		terminal := false

		switch tc.Name {
		case "set_environment":
			text, _ := tc.Input["text"].(string)
			text = strings.TrimSpace(text)
			if text == "" {
				currentMessage = "[The atmosphere you tried to write was empty. Try again or say done.]"
			} else if app.recentEnvironmentMatches(ctx, text, 60*time.Second) {
				// Dedupe (2026-05-02): chronicler occasionally emits
				// identical atmosphere text twice in the same cascade —
				// saw a verbatim "Dawn lies pale over the spring village"
				// repeat 3 seconds apart at 11:01:05/11:01:08. Skip the
				// write but acknowledge so the harness moves on instead of
				// burning another iteration trying to re-emit.
				currentMessage = "[Atmosphere unchanged — the prose you wrote matches what is already set. Move on or say done.]"
			} else if err := app.recordEnvironment(ctx, text, reason.Phase); err != nil {
				log.Printf("chronicler set_environment: %v", err)
				currentMessage = "[Atmosphere could not be recorded. Try again or say done.]"
			} else {
				currentMessage = "[Atmosphere noted.]"
			}
			currentToolCallID = tc.ID

		case "record_event":
			text, _ := tc.Input["text"].(string)
			text = strings.TrimSpace(text)
			scopeType, _ := tc.Input["scope_type"].(string)
			scopeType = strings.TrimSpace(scopeType)
			scopeTarget, _ := tc.Input["scope_target"].(string)
			scopeTarget = strings.TrimSpace(scopeTarget)
			if scopeType == "" {
				scopeType = "village"
			}
			if !validEventScope(scopeType) {
				currentMessage = fmt.Sprintf("[Unknown scope %q. Use 'village', 'local', or 'private'.]", scopeType)
				currentToolCallID = tc.ID
				break
			}
			if text == "" {
				currentMessage = "[The event you tried to record was empty. Try again or say done.]"
				currentToolCallID = tc.ID
				break
			}
			// Default scope_target for cascade-origin local events: use
			// the structure where the cascade started. The model has
			// trouble inventing structure UUIDs, so this is the safe
			// out for "an event happened HERE."
			if scopeType == "local" && scopeTarget == "" && reason.Type == "cascade" && reason.StructureID != "" {
				scopeTarget = reason.StructureID
			}
			// Reject local/private without a target — the row would be
			// invisible to every NPC if we wrote it (visibility queries
			// require a target match). Better to nudge the model than
			// silently waste a write.
			if (scopeType == "local" || scopeType == "private") && scopeTarget == "" {
				currentMessage = fmt.Sprintf("[Scope %q requires a scope_target (structure id for local, npc id for private). Try again or say done.]", scopeType)
				currentToolCallID = tc.ID
				break
			}
			if err := app.recordEvent(ctx, text, scopeType, scopeTarget); err != nil {
				log.Printf("chronicler record_event: %v", err)
				currentMessage = "[Event could not be recorded. Try again or say done.]"
			} else {
				currentMessage = "[Event recorded.]"
			}
			currentToolCallID = tc.ID

		case "recall":
			query, _ := tc.Input["query"].(string)
			currentMessage = app.resolveChroniclerRecall(ctx, query)
			currentToolCallID = tc.ID

		case "attend_to":
			villager, _ := tc.Input["villager"].(string)
			villager = strings.TrimSpace(villager)
			if villager == "" {
				currentMessage = "[The villager you tried to attend was unnamed. Try again or say done.]"
				currentToolCallID = tc.ID
				break
			}
			if attendCount >= attendCeiling {
				currentMessage = "[You have attended to as many villagers as one waking allows. Move on, or say done.]"
				currentToolCallID = tc.ID
				break
			}
			npcID, displayName, ok := app.resolveVillagerForAttention(ctx, villager)
			if !ok {
				currentMessage = fmt.Sprintf("[There is no villager by the name %q. Try again with the name as it appears in your roster, or say done.]", villager)
				currentToolCallID = tc.ID
				break
			}
			if attendedThisFire[npcID] {
				// Already attended this villager this fire (2026-05-02).
				// Re-attending re-ticks the NPC and burns a chronicler
				// call slot for no new effect — the prior dispatch is
				// already in flight or has run.
				currentMessage = fmt.Sprintf("[You have already attended %s in this waking. Move on, or say done.]", displayName)
				currentToolCallID = tc.ID
				break
			}
			// Cross-fire attend cooldown (ZBBS-119). The
			// attendedThisFire check above only catches duplicates
			// within one chronicler turn; serialized back-to-back
			// fires can still re-attend the same NPC. Skip routine
			// re-attends within chroniclerAttendCooldown of the prior
			// dispatch, with a visible tool result so the model reads
			// the rejection. PC-speech and admin-attend-now fires are
			// exempt — those represent fresh significant events and
			// should always go through.
			if !chroniclerAttendExempt(reason) {
				if since, recent := app.recentChroniclerAttend(npcID); recent {
					currentMessage = fmt.Sprintf("[%s was already dispatched %s ago in another waking; attend_to skipped. Move on, or say done.]",
						displayName, since.Round(time.Second))
					currentToolCallID = tc.ID
					break
				}
			}
			// Trigger the NPC's tick. Force=true so the agentMinTickGap
			// cost guard in triggerImmediateTick is bypassed — chronicler
			// attend_to is a directorial action, not a sim-layer cascade.
			// The cost guard exists to dampen NPC-to-NPC tick storms
			// (co-located NPCs reacting to each other's speech); a
			// chronicler dispatch is a deliberate top-down pick that
			// should always go through. Without the bypass, an NPC who
			// happened to tick within the last 5 minutes silently
			// disappears from the chronicler's view of the world: it
			// gets back "[You attend to X. They will rouse...]" but
			// no actual prompt fires for X.
			//
			// Cost is bounded chronicler-side, not at the cost guard:
			// attendCeiling caps attend_to calls per fire,
			// OverseerAttendSem bounds concurrent attends across
			// overlapping fires, and chronicler fires themselves are
			// gated to specific events (phase / cascade / shift_boundary
			// / needs_resolved) — not arbitrary. Same-NPC re-ticks
			// across two close fires are still possible but they're
			// substantively different perceptions (different events),
			// not the storm the guard targets.
			//
			// Background goroutine so a slow agent tick doesn't block
			// the overseer's harness loop. App-level semaphore caps
			// aggregate concurrency across overlapping fires; if the
			// slot is full we still let the call through (vs dropping)
			// — the goroutine just blocks waiting for a slot, then
			// runs. Acceptable for directorial dispatches; if
			// backpressure becomes an issue we can switch to skip-if-
			// full like ChroniclerSem does.
			//
			// Thread the chronicler's sceneID so the dispatched NPC
			// tick lands in the same scene as the overseer's fire —
			// keeps the admin UI's scene grouping coherent across the
			// cascade.
			go func(id, name, scene string) {
				app.OverseerAttendSem <- struct{}{}
				defer func() { <-app.OverseerAttendSem }()
				// triggerActorID = "" — chronicler dispatch has no salient
				// speaker, so this won't lock the attended NPC against
				// subsequent heard-speech reactions in the same scene.
				app.triggerImmediateTick(context.Background(), id, "overseer-attend-to", true, scene, "")
			}(npcID, displayName, sceneID)
			attendCount++
			attendedThisFire[npcID] = true
			// Stamp the cross-fire cooldown clock (ZBBS-119). Includes
			// exempt fires (PC speech, admin) — the stamp is what the
			// next fire's routine attends measure their cooldown
			// against, regardless of which fire stamped it.
			app.recordChroniclerAttend(npcID)
			currentMessage = fmt.Sprintf("[You attend to %s. They will rouse and decide what to do.]", displayName)
			currentToolCallID = tc.ID

		case "done":
			terminal = true

		default:
			log.Printf("chronicler: unknown tool %q, terminating", tc.Name)
			terminal = true
		}

		if terminal {
			break
		}
	}

	if !chatSucceeded {
		// Defensive — shouldn't reach here without chatSucceeded since
		// the iter=0 error path returns early. Belt and suspenders.
		return fmt.Errorf("chronicler: no chat call succeeded")
	}

	// Attention state — used by the activity digest cutoff. Updated
	// whether this was a phase fire or cascade fire, so cascade fires
	// don't repeatedly re-digest the same activity window between
	// phases. Phase state is updated separately by the caller and only
	// when the type is "phase" (in dispatchChroniclerPhase).
	app.saveChroniclerAttentionState(ctx, time.Now())
	return nil
}

// validEventScope reports whether the given scope_type is one of the
// recognized event_scope enum values. Defensive — the LLM could emit
// arbitrary strings even with the enum constraint in the schema.
func validEventScope(s string) bool {
	switch s {
	case "village", "local", "private":
		return true
	}
	return false
}

// buildChroniclerPerception constructs the user-message text the
// chronicler sees on iteration 0. Sections in order:
//
//  1. Why you wake (phase boundary or cascade origin description)
//  2. Mood + season (config-driven knobs the admin flips)
//  3. NPC roster grouped by current location
//  4. Recent atmospheric statements (your own last N — evolve, don't whiplash)
//  5. Recent visible events (last N village-scope within window)
//  6. Activity digest since last fire (deterministic Go-rendered)
//  7. Decision prompt
func (app *App) buildChroniclerPerception(ctx context.Context, reason chroniclerFireReason, shiftBatches []*chroniclerDispatchBatch) string {
	var sections []string

	// 1. Why you wake.
	sections = append(sections, app.chroniclerOpeningLine(ctx, reason))

	// 2. Mood + season.
	mood := app.loadSetting(ctx, "overseer_mood", "watchful")
	season := app.loadSetting(ctx, "salem_season", "spring")
	sections = append(sections, fmt.Sprintf("The season is %s. Your watchful mood: %s.", season, mood))

	// 3. NPC roster grouped by location.
	if rosterText := app.buildChroniclerNPCRoster(ctx); rosterText != "" {
		sections = append(sections, rosterText)
	}

	// 3a. Villagers in distress (ZBBS-083). Lists NPCs whose body has
	// crossed the red threshold for one or more needs. Mild-tier discomfort
	// stays private to the NPC — only red-and-above surfaces here so the
	// overseer's attention list is signal, not noise. Empty section omitted
	// when no one is suffering.
	if distress := app.buildChroniclerDistressList(ctx); distress != "" {
		sections = append(sections, distress)
	}

	// 3b. Shift boundary events for agent NPCs (chronicler-dispatch
	// redesign). Renders one section per event_type from the batches
	// the caller drained. Drain happens in fireChronicler, not here —
	// keeps perception build read-only and makes the destructive action
	// tied to an actual chronicler invocation. A phase or cascade fire
	// that happens to overlap a shift boundary picks up the events for
	// free because every fireChronicler call drains the queue.
	for _, section := range app.renderDispatchSections(ctx, shiftBatches) {
		sections = append(sections, section)
	}

	// 4. Your recent atmospheric statements.
	if recent := app.recentEnvironmentTexts(ctx, chroniclerEnvHistoryCount); len(recent) > 0 {
		var b strings.Builder
		b.WriteString("Your recent atmospheric writings (most recent first):\n")
		for _, e := range recent {
			b.WriteString("- ")
			b.WriteString(e)
			b.WriteString("\n")
		}
		sections = append(sections, strings.TrimRight(b.String(), "\n"))
	}

	// 5. Recent village-visible events.
	if events := app.recentVisibleEvents(ctx, "village", "", time.Now().Add(-recentEventsWindow), recentEventsCount); len(events) > 0 {
		var b strings.Builder
		b.WriteString("Recent entries in the chronicle:\n")
		for _, e := range events {
			b.WriteString("- ")
			b.WriteString(e)
			b.WriteString("\n")
		}
		sections = append(sections, strings.TrimRight(b.String(), "\n"))
	}

	// 6. Activity digest since last fire (phase OR cascade — uses the
	// attention timestamp, not the phase one, so cascade fires between
	// phases see only the activity since the previous fire instead of
	// repeatedly re-digesting back to the last phase boundary).
	//
	// Pass reason so the digest can suppress its "the village has been
	// quiet" fallback on cascade fires — the opening line already named
	// the stirring (e.g. "Something stirs at Blacksmith: arrival") and
	// asserting "quiet" six lines later contradicted it, biasing the
	// model toward a set_environment atmosphere response on every cascade.
	if digest := app.buildActivityDigest(ctx, app.loadChroniclerLastAttentionAt(ctx), reason); digest != "" {
		sections = append(sections, digest)
	}

	// 7. Decision prompt — names tools explicitly so the model knows
	// to bridge "I write atmosphere" → set_environment() and
	// "I record an event" → record_event(). Without naming the tools
	// the model occasionally narrates as plain text (the implicit
	// set_environment fallback catches this, but explicit naming
	// reduces the failure rate).
	sections = append(sections, "Attend to the village. Use set_environment to write the current atmosphere if it has shifted. Use record_event to record any happening that should persist in village memory (default scope is village; pass scope_type='local' with scope_target=<structure_id> to restrict to one place, or scope_type='private' with scope_target=<npc_id> to restrict to one person). Use attend_to to rouse a villager whose body is in distress, whose shift has begun and who is not at their workplace, or whose shift has ended and who is still at their workplace. You may use recall to remember anything the village has experienced. Use done when your office is finished.")

	return strings.Join(sections, "\n\n")
}

// chroniclerOpeningLine renders the perception's opening "why are you
// waking" line. Phase fires get a clean phase mention; cascade fires
// describe the originating event in-character.
func (app *App) chroniclerOpeningLine(ctx context.Context, reason chroniclerFireReason) string {
	switch reason.Type {
	case "phase":
		return fmt.Sprintf("It is %s. The hour has come for you to attend the village.", reason.Phase)
	case "cascade":
		structName := ""
		if reason.StructureID != "" {
			structName = app.lookupStructureName(ctx, reason.StructureID)
		}
		if structName != "" {
			return fmt.Sprintf("Something stirs at %s: %s.", structName, reason.CascadeReason)
		}
		return fmt.Sprintf("Something stirs in the village: %s.", reason.CascadeReason)
	case "shift_boundary":
		return "A villager's working hours have shifted. The hour has come for you to attend the village."
	case "buffered_flush":
		return "The village has stirred in the past minutes. The hour has come for you to attend."
	}
	return "You wake to attend the village."
}

// buildChroniclerNPCRoster returns a grouped-by-location render of
// every agentized NPC, plus their last action and recency. Empty
// string when no NPCs have been loaded yet (fresh deploy).
//
// Format:
//
//	The village right now:
//	- At the Tavern: John Ellis (last spoke 12 minutes ago).
//	- At the Smithy: Josiah Thorne (last walked here 3 minutes ago).
//	- Out in the open: Ezekiel Crane.
func (app *App) buildChroniclerNPCRoster(ctx context.Context) string {
	rows, err := app.DB.Query(ctx, `
		SELECT n.id, n.display_name, n.inside_structure_id,
		       COALESCE(o.display_name, a.name, '') AS struct_label
		FROM actor n
		LEFT JOIN village_object o ON o.id = n.inside_structure_id
		LEFT JOIN asset a ON a.id = o.asset_id
		WHERE n.llm_memory_agent IS NOT NULL
		ORDER BY n.display_name`)
	if err != nil {
		log.Printf("chronicler roster: %v", err)
		return ""
	}
	defer rows.Close()

	type npcEntry struct {
		ID, DisplayName, StructLabel string
		Inside                       bool
	}
	byLoc := map[string][]npcEntry{}
	openAir := []npcEntry{}
	for rows.Next() {
		var id, displayName, structLabel string
		var insideStructID sql.NullString
		if err := rows.Scan(&id, &displayName, &insideStructID, &structLabel); err != nil {
			continue
		}
		entry := npcEntry{ID: id, DisplayName: displayName, StructLabel: structLabel, Inside: insideStructID.Valid}
		if !insideStructID.Valid || structLabel == "" {
			openAir = append(openAir, entry)
			continue
		}
		byLoc[structLabel] = append(byLoc[structLabel], entry)
	}
	if err := rows.Err(); err != nil {
		log.Printf("chronicler npc roster rows: %v", err)
	}

	if len(byLoc) == 0 && len(openAir) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("The village right now:\n")
	// Sort locations alphabetically for stable output.
	locs := make([]string, 0, len(byLoc))
	for loc := range byLoc {
		locs = append(locs, loc)
	}
	// Simple insertion sort — n is small (~5 structures).
	for i := 1; i < len(locs); i++ {
		for j := i; j > 0 && locs[j-1] > locs[j]; j-- {
			locs[j-1], locs[j] = locs[j], locs[j-1]
		}
	}
	for _, loc := range locs {
		names := make([]string, 0, len(byLoc[loc]))
		for _, e := range byLoc[loc] {
			names = append(names, e.DisplayName)
		}
		fmt.Fprintf(&b, "- At the %s: %s.\n", loc, strings.Join(names, ", "))
	}
	if len(openAir) > 0 {
		names := make([]string, 0, len(openAir))
		for _, e := range openAir {
			names = append(names, e.DisplayName)
		}
		fmt.Fprintf(&b, "- Out in the open: %s.\n", strings.Join(names, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildActivityDigest renders a deterministic summary of agent_action_log
// rows since `since`. Aggregates per-NPC action counts. Empty string
// when no activity (fresh deploy or quiet window).
//
// reason is the fire reason for this perception. On cascade fires the
// "the village has been quiet" fallback is suppressed and we return ""
// instead — the opening line already named the cascade trigger
// (arrival, etc.) and asserting "quiet" here contradicted that.
//
// Format: "Since the last fire: John walked 2 times, spoke 4 times.
// Prudence completed 1 chore. ..."
func (app *App) buildActivityDigest(ctx context.Context, since time.Time, reason chroniclerFireReason) string {
	if since.IsZero() {
		// No prior fire — skip digest. Fresh deploy or first fire after restart.
		return ""
	}
	rows, err := app.DB.Query(ctx, `
		SELECT n.display_name, l.action_type, COUNT(*) AS cnt
		FROM agent_action_log l
		JOIN actor n ON n.id = l.actor_id
		WHERE l.occurred_at > $1 AND l.result = 'ok'
		GROUP BY n.display_name, l.action_type
		ORDER BY n.display_name, l.action_type`, since)
	if err != nil {
		log.Printf("chronicler digest: %v", err)
		return ""
	}
	defer rows.Close()

	type bucket struct {
		Walks, Speaks, Chores, Other int
	}
	per := map[string]*bucket{}
	for rows.Next() {
		var name, action string
		var cnt int
		if err := rows.Scan(&name, &action, &cnt); err != nil {
			continue
		}
		if per[name] == nil {
			per[name] = &bucket{}
		}
		switch action {
		case "move_to":
			per[name].Walks += cnt
		case "speak":
			per[name].Speaks += cnt
		case "chore":
			per[name].Chores += cnt
		default:
			per[name].Other += cnt
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("chronicler digest rows: %v", err)
		// Don't return a partial digest — better to render no section
		// than a misleading "village has been quiet" when iteration
		// failed mid-stream.
		return ""
	}
	if len(per) == 0 {
		if reason.Type == "cascade" {
			return ""
		}
		return "Since your last attention, the village has been quiet."
	}

	var b strings.Builder
	b.WriteString("Since your last attention:\n")
	// Stable iteration order — sort names.
	names := make([]string, 0, len(per))
	for name := range per {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	wrote := false
	for _, name := range names {
		bk := per[name]
		var parts []string
		if bk.Walks > 0 {
			parts = append(parts, fmt.Sprintf("walked %d time%s", bk.Walks, plural(bk.Walks)))
		}
		if bk.Speaks > 0 {
			parts = append(parts, fmt.Sprintf("spoke %d time%s", bk.Speaks, plural(bk.Speaks)))
		}
		if bk.Chores > 0 {
			parts = append(parts, fmt.Sprintf("completed %d chore%s", bk.Chores, plural(bk.Chores)))
		}
		// Bucket "Other" so unrendered action types don't silently drop
		// the line — without this, an NPC with only Other actions would
		// produce a "Since your last attention:" header with no body.
		if bk.Other > 0 {
			parts = append(parts, fmt.Sprintf("acted %d time%s", bk.Other, plural(bk.Other)))
		}
		if len(parts) == 0 {
			continue
		}
		wrote = true
		fmt.Fprintf(&b, "- %s %s.\n", name, strings.Join(parts, ", "))
	}
	if !wrote {
		if reason.Type == "cascade" {
			return ""
		}
		return "Since your last attention, the village has been quiet."
	}
	return strings.TrimRight(b.String(), "\n")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// recentEnvironmentMatches returns true if the most recent
// world_environment row's text matches `text` exactly and was set within
// `maxAge`. Used by the chronicler set_environment handler to skip a
// no-op rewrite when the model emits the same prose twice in a cascade.
// Errors (no rows, query failure) return false so the write proceeds —
// dedupe is best-effort, not a correctness requirement.
func (app *App) recentEnvironmentMatches(ctx context.Context, text string, maxAge time.Duration) bool {
	var existingText string
	var setAt time.Time
	err := app.DB.QueryRow(ctx,
		`SELECT text, set_at FROM world_environment ORDER BY set_at DESC LIMIT 1`,
	).Scan(&existingText, &setAt)
	if err != nil {
		return false
	}
	if existingText != text {
		return false
	}
	return time.Since(setAt) <= maxAge
}

// recordEnvironment appends a row to world_environment. Phase is "" for
// cascade-origin fires (NULL in DB), or one of dawn/midday/dusk for
// phase fires. Broadcasts world_environment_added so the top-bar
// marquee ticker on connected clients picks up the new prose. set_at
// is generated by the DB (NOW()) and returned, so the broadcast carries
// the same timestamp the row was stamped with — no app/DB clock skew.
func (app *App) recordEnvironment(ctx context.Context, text, phase string) error {
	var phaseArg interface{}
	if phase == "" {
		phaseArg = nil
	} else {
		phaseArg = phase
	}
	var id int64
	var setAt time.Time
	err := app.DB.QueryRow(ctx,
		`INSERT INTO world_environment (text, set_by, phase, set_at) VALUES ($1, $2, $3, NOW())
		 RETURNING id, set_at`,
		text, chroniclerAgent, phaseArg).Scan(&id, &setAt)
	if err != nil {
		return err
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "world_environment_added",
		Data: map[string]any{
			"id":     id,
			"text":   text,
			"phase":  phase,
			"set_at": setAt.Format(time.RFC3339Nano),
		},
	})
	return nil
}

// recordEvent appends a row to world_events. Caller must have validated
// scope_type via validEventScope. Empty scope_target is fine for
// village scope; required by convention for local/private but the DB
// allows NULL (engine code filters anyway).
func (app *App) recordEvent(ctx context.Context, text, scopeType, scopeTarget string) error {
	var targetArg interface{}
	if scopeTarget == "" {
		targetArg = nil
	} else {
		targetArg = scopeTarget
	}
	_, err := app.DB.Exec(ctx,
		`INSERT INTO world_events (text, scope_type, scope_target, set_by, occurred_at)
		 VALUES ($1, $2, $3, $4, NOW())`,
		text, scopeType, targetArg, chroniclerAgent)
	return err
}

// recentEnvironmentTexts returns the last n atmospheric statements,
// most recent first. Used in the chronicler's own perception so it can
// see what it just wrote. Tiebreaks on id DESC so identical set_at
// timestamps (rare but possible at sub-second precision) deterministic-
// ally surface the row inserted last.
func (app *App) recentEnvironmentTexts(ctx context.Context, n int) []string {
	rows, err := app.DB.Query(ctx,
		`SELECT text FROM world_environment ORDER BY set_at DESC, id DESC LIMIT $1`, n)
	if err != nil {
		log.Printf("recent environment: %v", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			out = append(out, t)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("recent environment rows: %v", err)
	}
	return out
}

// latestEnvironmentText returns the most recent atmospheric statement,
// or empty string if none exists. Used by the NPC perception builder.
func (app *App) latestEnvironmentText(ctx context.Context) string {
	var t sql.NullString
	err := app.DB.QueryRow(ctx,
		`SELECT text FROM world_environment ORDER BY set_at DESC, id DESC LIMIT 1`).Scan(&t)
	// Distinguish "no rows yet" (fresh deploy / chronicler hasn't fired)
	// from real DB errors. Only the latter deserves a log line; both
	// return empty so the perception just omits the atmosphere line.
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("latest environment: %v", err)
		}
		return ""
	}
	if !t.Valid {
		return ""
	}
	return t.String
}

// recentVisibleEvents returns recent event texts for ONE scope. Caller
// composes village + local + private separately. Per-scope semantics
// keep the function small and let callers choose what they want without
// redundant village queries (an NPC perception that wants all three
// scopes would otherwise hit the same village rows three times).
//
// scope must be 'village', 'local', or 'private' (matches the
// event_scope enum). target is the structure id for 'local' or NPC id
// for 'private'; ignored for 'village'.
//
// Returns up to limit rows occurring after `since`, most recent first.
func (app *App) recentVisibleEvents(ctx context.Context, scope, target string, since time.Time, limit int) []string {
	// Build query + args per scope, then issue a single Query call so we
	// don't need to declare the concrete row-type variable (app.DB is
	// pgx; *sql.Rows would mismatch).
	var query string
	var args []any
	switch scope {
	case "village":
		query = `SELECT text FROM world_events
		         WHERE scope_type = 'village' AND occurred_at > $1
		         ORDER BY occurred_at DESC, id DESC LIMIT $2`
		args = []any{since, limit}
	case "local":
		if target == "" {
			return nil
		}
		query = `SELECT text FROM world_events
		         WHERE scope_type = 'local' AND scope_target = $1 AND occurred_at > $2
		         ORDER BY occurred_at DESC, id DESC LIMIT $3`
		args = []any{target, since, limit}
	case "private":
		if target == "" {
			return nil
		}
		query = `SELECT text FROM world_events
		         WHERE scope_type = 'private' AND scope_target = $1 AND occurred_at > $2
		         ORDER BY occurred_at DESC, id DESC LIMIT $3`
		args = []any{target, since, limit}
	default:
		return nil
	}
	rows, err := app.DB.Query(ctx, query, args...)
	if err != nil {
		log.Printf("recent events (%s): %v", scope, err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			out = append(out, t)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("recent events (%s) rows: %v", scope, err)
	}
	return out
}

// resolveChroniclerRecall is the recall-tool resolver for the chronicler.
// Wildcard namespace search — realm-overlap (chronicler has realms=['salem'])
// returns hits from any salem-realm namespace. Format hits with
// display-name lookup so the chronicler sees who each note belongs to.
func (app *App) resolveChroniclerRecall(ctx context.Context, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "You tried to remember something but couldn't form the question."
	}
	if len(query) > recallQueryMaxChars {
		query = query[:recallQueryMaxChars]
	}
	hits, err := app.npcChatClient.searchMemory(ctx, "*", query, recallResultLimit)
	if err != nil {
		log.Printf("chronicler recall: %v", err)
		return "You reached for memory, but it would not come."
	}
	if len(hits) == 0 {
		return "Nothing comes to mind."
	}
	return formatRecallHits(hits, app.namespaceDisplayName)
}

// formatRecallHits renders search hits as a tool-result text block.
// Each hit gets a [DisplayName] prefix from the lookup function, which
// falls back to the raw namespace string when no display name is known
// (overseer's own namespace, future shared namespaces).
//
// Shared between NPC recall (resolveRecall) and chronicler recall —
// the formatting is the same; only the namespace scope differs.
func formatRecallHits(hits []searchMemoryHit, displayName func(ns string) string) string {
	if len(hits) == 0 {
		return "Nothing comes to mind."
	}
	var b strings.Builder
	b.WriteString("You remember:\n\n")
	for _, h := range hits {
		label := displayName(h.Namespace)
		if label == "" {
			label = h.Namespace
		}
		fmt.Fprintf(&b, "— [%s] %s —\n%s\n\n", label, h.SourceFile, h.ChunkText)
	}
	return strings.TrimRight(b.String(), "\n")
}

// refreshNPCDisplayNames repopulates the agent-slug → display-name map
// from the npc table. Called from runServerTickOnce every tick (60s)
// so newly added NPCs (or renamed slugs) propagate to recall result
// formatting without an engine restart. Cheap — bounded by NPC count.
//
// Refreshed unconditionally (not gated by AgentTicksPaused) so the
// recall-result display-name cache stays current while LLM activity is
// paused, and is ready when reactive ticks resume.
func (app *App) refreshNPCDisplayNames(ctx context.Context) {
	rows, err := app.DB.Query(ctx,
		`SELECT llm_memory_agent, display_name FROM actor
		 WHERE llm_memory_agent IS NOT NULL`)
	if err != nil {
		log.Printf("refresh display names: %v", err)
		return
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var slug, name string
		if err := rows.Scan(&slug, &name); err != nil {
			continue
		}
		if slug != "" {
			m[slug] = name
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("refresh display names rows: %v", err)
		// Don't replace the existing map with a partial one — keep
		// whatever was there from the previous successful refresh.
		return
	}
	app.NPCDisplayNamesMu.Lock()
	app.NPCDisplayNames = m
	app.NPCDisplayNamesMu.Unlock()
}

// namespaceDisplayName returns the display_name of an agent given its
// namespace (= agent slug), or empty string if no matching NPC is
// loaded. Callers (formatRecallHits) handle the empty-string case by
// rendering the raw namespace instead.
//
// Reads from the NPC roster cache populated by refreshNPCDisplayNames;
// misses fall through to a one-shot DB lookup so newly added agents
// work without waiting for the next server-tick refresh.
func (app *App) namespaceDisplayName(namespace string) string {
	if namespace == "" {
		return ""
	}
	app.NPCDisplayNamesMu.RLock()
	name, ok := app.NPCDisplayNames[namespace]
	app.NPCDisplayNamesMu.RUnlock()
	if ok {
		return name
	}
	// Fallback — try a direct query. The chronicler's own namespace and
	// any future non-NPC namespaces (salem-village-lore, etc.) won't
	// have a display_name in the npc table; return the raw namespace
	// so the recall result is at least labeled with something.
	var dbName sql.NullString
	err := app.DB.QueryRow(context.Background(),
		`SELECT display_name FROM actor WHERE llm_memory_agent = $1 LIMIT 1`,
		namespace).Scan(&dbName)
	if err != nil || !dbName.Valid {
		return ""
	}
	return dbName.String
}

// loadSetting reads a single setting key as text, falling back to the
// supplied default when the row is missing or NULL. Generic helper for
// the chronicler-specific settings (mood, season, last-fired).
func (app *App) loadSetting(ctx context.Context, key, fallback string) string {
	var v sql.NullString
	err := app.DB.QueryRow(ctx, `SELECT value FROM setting WHERE key = $1`, key).Scan(&v)
	if err != nil || !v.Valid {
		return fallback
	}
	return v.String
}

// loadIntRange reads a setting whose value is a JSON 2-element int
// array (e.g. "[30,60]") and returns (min, max). Falls back to the
// provided defaults on missing row, parse error, wrong length, or any
// other shape mismatch — never panics, never returns partials.
//
// Storage is JSON so the (eventual) settings UI can extend the shape
// later without a migration churn (range with a step, range with
// labels, etc.). The same UI is expected to surface the friendlier
// "30,60" form for input and translate to JSON on save; the engine
// only ever reads the JSON form.
func (app *App) loadIntRange(ctx context.Context, key string, defaultMin, defaultMax int) (int, int) {
	v := app.loadSetting(ctx, key, "")
	if v == "" {
		return defaultMin, defaultMax
	}
	var arr []int
	if err := json.Unmarshal([]byte(v), &arr); err != nil {
		log.Printf("loadIntRange %s: parse %q: %v", key, v, err)
		return defaultMin, defaultMax
	}
	if len(arr) != 2 {
		log.Printf("loadIntRange %s: expected 2 elements, got %d", key, len(arr))
		return defaultMin, defaultMax
	}
	return arr[0], arr[1]
}

// loadChroniclerLastPhaseFiredAt returns the timestamp of the most
// recent successful PHASE fire (cascade fires don't update this).
// Used by dispatchChroniclerPhase to detect whether we've already
// processed the current boundary. Zero time when never fired.
func (app *App) loadChroniclerLastPhaseFiredAt(ctx context.Context) time.Time {
	return app.parseSettingTime(ctx, "last_chronicler_phase_fired_at")
}

// loadChroniclerLastAttentionAt returns the timestamp of the most
// recent successful chronicler fire (phase OR cascade). Used as the
// activity digest cutoff so each fire's perception only includes
// activity since the chronicler last attended. Zero time when never
// fired (fresh deploy).
func (app *App) loadChroniclerLastAttentionAt(ctx context.Context) time.Time {
	return app.parseSettingTime(ctx, "last_chronicler_attention_at")
}

// parseSettingTime is shared parser used by the two state-loaders
// above. Tries RFC3339Nano first, falls back to RFC3339 for hand-
// edited values. Returns zero on any failure.
func (app *App) parseSettingTime(ctx context.Context, key string) time.Time {
	v := app.loadSetting(ctx, key, "")
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		t, err = time.Parse(time.RFC3339, v)
		if err != nil {
			log.Printf("chronicler: parse %s %q: %v", key, v, err)
			return time.Time{}
		}
	}
	return t
}

// saveChroniclerPhaseState records that the chronicler successfully
// fired for the given phase at the given boundary timestamp. Only
// called by phase fires, not cascade fires.
//
// Does NOT write last_chronicler_attention_at — that's owned by
// fireChronicler so there's a single writer using actual completion
// time. Otherwise this would clobber fireChronicler's Now() with the
// earlier boundary timestamp and reopen the digest window between
// boundaryAt and completion.
func (app *App) saveChroniclerPhaseState(ctx context.Context, phase string, boundaryAt time.Time) {
	if err := app.upsertSetting(ctx, "last_chronicler_phase_fired_at", boundaryAt.UTC().Format(time.RFC3339Nano)); err != nil {
		log.Printf("chronicler save phase fired_at: %v", err)
	}
	if err := app.upsertSetting(ctx, "last_chronicler_fired_phase", phase); err != nil {
		log.Printf("chronicler save fired_phase: %v", err)
	}
}

// saveChroniclerAttentionState records the timestamp of any successful
// chronicler fire (phase or cascade). Drives the activity digest cutoff.
func (app *App) saveChroniclerAttentionState(ctx context.Context, at time.Time) {
	if err := app.upsertSetting(ctx, "last_chronicler_attention_at", at.UTC().Format(time.RFC3339Nano)); err != nil {
		log.Printf("chronicler save attention_at: %v", err)
	}
}

// mostRecentChroniclerPhase returns the phase and timestamp of the most
// recent dawn / midday / dusk boundary at or before now. Search window
// is the last 24 hours (always contains all three boundaries).
//
// Midday is the midpoint between dawn and dusk. With dawn=06:00 and
// dusk=19:00 (defaults), midday is 12:30. The midpoint is computed in
// minutes-of-day to handle non-symmetric configurations.
func mostRecentChroniclerPhase(now time.Time, dawnH, dawnM, duskH, duskM int) (phase string, at time.Time) {
	loc := now.Location()
	y, mo, d := now.Date()

	dawnMinutes := dawnH*60 + dawnM
	duskMinutes := duskH*60 + duskM
	if duskMinutes < dawnMinutes {
		// Defensive — admins shouldn't set dusk before dawn, but if
		// they do, treat midday as halfway across midnight (rare and
		// nonsensical, but don't blow up).
		duskMinutes += 24 * 60
	}
	middayMinutes := (dawnMinutes + duskMinutes) / 2
	middayMinutes %= 24 * 60
	middayH := middayMinutes / 60
	middayM := middayMinutes % 60

	todayDawn := time.Date(y, mo, d, dawnH, dawnM, 0, 0, loc)
	todayMidday := time.Date(y, mo, d, middayH, middayM, 0, 0, loc)
	todayDusk := time.Date(y, mo, d, duskH, duskM, 0, 0, loc)

	candidates := []struct {
		t     time.Time
		phase string
	}{
		{todayDawn.Add(-24 * time.Hour), "dawn"},
		{todayMidday.Add(-24 * time.Hour), "midday"},
		{todayDusk.Add(-24 * time.Hour), "dusk"},
		{todayDawn, "dawn"},
		{todayMidday, "midday"},
		{todayDusk, "dusk"},
	}

	var latestT time.Time
	var latestPhase string
	for _, c := range candidates {
		if !c.t.After(now) && c.t.After(latestT) {
			latestT = c.t
			latestPhase = c.phase
		}
	}
	return latestPhase, latestT
}

// lookupStructureName returns the human-readable label for a structure
// (display_name when set, else asset.name). Empty string when no row
// matches — caller should fall back to a generic phrasing.
func (app *App) lookupStructureName(ctx context.Context, structureID string) string {
	if structureID == "" {
		return ""
	}
	var name sql.NullString
	err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.display_name, a.name)
		 FROM village_object o JOIN asset a ON a.id = o.asset_id
		 WHERE o.id = $1`,
		structureID).Scan(&name)
	if err != nil || !name.Valid {
		return ""
	}
	return name.String
}

// actorCurrent is the at-flush-time location reading for one actor:
// where they are NOW, distinct from where the buffered arrival event
// said they had been. StructureID is "" when the actor is currently
// outdoors (inside_structure_id NULL).
type actorCurrent struct {
	StructureID   string
	StructureName string
}

// lookupCurrentStructures resolves where each actor is RIGHT NOW for the
// arrival current-state validation in renderArrivalSection. One batched
// query keyed on actor.id so a buffered flush with N arrivals costs one
// roundtrip, not N. Outdoor actors (inside_structure_id NULL) get an
// entry with empty StructureID/StructureName.
//
// On query failure, returns nil — renderArrivalSection treats the whole
// arrival batch as "current unknown" and falls back to bare per-arrival
// lines (Phase 1 behavior). Don't lose the section over one bad lookup.
func (app *App) lookupCurrentStructures(ctx context.Context, actorIDs []string) map[string]actorCurrent {
	if len(actorIDs) == 0 {
		return nil
	}
	rows, err := app.DB.Query(ctx,
		`SELECT n.id::text,
		        COALESCE(n.inside_structure_id::text, ''),
		        COALESCE(o.display_name, a.name, '')
		 FROM actor n
		 LEFT JOIN village_object o ON o.id = n.inside_structure_id
		 LEFT JOIN asset a ON a.id = o.asset_id
		 WHERE n.id::text = ANY($1)`,
		actorIDs)
	if err != nil {
		log.Printf("lookupCurrentStructures: query: %v", err)
		return nil
	}
	defer rows.Close()
	out := map[string]actorCurrent{}
	for rows.Next() {
		var id, structID, structName string
		if err := rows.Scan(&id, &structID, &structName); err != nil {
			log.Printf("lookupCurrentStructures: scan: %v", err)
			return nil
		}
		out[id] = actorCurrent{StructureID: structID, StructureName: structName}
	}
	if err := rows.Err(); err != nil {
		log.Printf("lookupCurrentStructures: rows: %v", err)
		return nil
	}
	return out
}

// buildChroniclerDistressList renders the "Villagers needing attention"
// block for the overseer's perception. Lists any NPC whose hunger, thirst,
// or tiredness is at or above the configured red threshold for that need
// (mild-tier discomfort isn't surfaced — too noisy at the director level).
// Format: "- <Name> (at <Place>): <need labels>". Empty string when no
// one is in distress, in which case the section is omitted entirely from
// the perception.
//
// Locations come from the same coalesced display_name → asset.name path
// the rest of the chronicler perception uses, so the place names match
// the roster block.
func (app *App) buildChroniclerDistressList(ctx context.Context) string {
	thresholds := app.loadNeedThresholds(ctx)

	// Reads from actor_need (ZBBS-121 commit 3). The previous version
	// filtered red-tier+ actors at the DB level via a hardcoded
	// (hunger >= $1 OR thirst >= $2 OR tiredness >= $3) WHERE clause;
	// this version pulls all agent NPCs with their needs via a JOIN
	// to actor_need and filters in code through the Need registry. At
	// salem-village scale (tens of NPCs, three needs each) the extra
	// rows transferred are negligible and the registry-driven filter
	// stays correct as future needs are added.
	// LEFT JOIN to actor_need so an actor with missing rows still
	// surfaces; per-need GetOK in the band-classification loop logs
	// and skips missing rows so partial backfills are observable
	// instead of silently treated as silent.
	rows, err := app.DB.Query(ctx, `
		SELECT actr.id::text, actr.display_name,
		       COALESCE(o.display_name, ass.name) AS place,
		       n.key, n.value
		FROM actor actr
		LEFT JOIN village_object o ON o.id = actr.inside_structure_id
		LEFT JOIN asset ass ON ass.id = o.asset_id
		LEFT JOIN actor_need n ON n.actor_id = actr.id
		WHERE actr.llm_memory_agent IS NOT NULL
		ORDER BY actr.display_name, n.key
	`)
	if err != nil {
		log.Printf("chronicler: distress query: %v", err)
		return ""
	}
	defer rows.Close()

	type actorAcc struct {
		name  string
		place string
		needs NeedSet
	}
	byID := map[string]*actorAcc{}
	var order []string // first-appearance order matches ORDER BY display_name
	for rows.Next() {
		var id, name string
		var place sql.NullString
		var key sql.NullString
		var value sql.NullInt64
		if err := rows.Scan(&id, &name, &place, &key, &value); err != nil {
			log.Printf("chronicler: distress scan: %v", err)
			continue
		}
		a, ok := byID[id]
		if !ok {
			placeStr := "the open village"
			if place.Valid && place.String != "" {
				placeStr = place.String
			}
			a = &actorAcc{name: name, place: placeStr, needs: NeedSet{}}
			byID[id] = a
			order = append(order, id)
		}
		if key.Valid && value.Valid {
			a.needs[key.String] = int(value.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("chronicler: distress rows: %v", err)
		return ""
	}

	var lines []string
	for _, id := range order {
		a := byID[id]
		// Filter to red-tier and above; mild-tier doesn't surface here.
		var labels []string
		for _, nd := range Needs {
			value, ok := a.needs.GetOK(nd.Key)
			if !ok {
				log.Printf("chronicler: distress missing actor_need row actor=%s key=%s (treating as silent)", id, nd.Key)
				continue
			}
			tier := nd.Tier(value, thresholds.Get(nd.Key))
			if tier >= NeedRed {
				labels = append(labels, nd.Label(tier))
			}
		}
		if len(labels) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s (at %s): %s", a.name, a.place, strings.Join(labels, ", ")))
	}

	if len(lines) == 0 {
		return ""
	}
	return "Villagers in distress:\n" + strings.Join(lines, "\n")
}

// renderDispatchSections renders perception sections from the caller-
// supplied batches — one section per event_type. Pure function; the
// caller (fireChronicler) is responsible for draining the queue. Empty
// when batches is empty.
//
// Sections in stable order: arrival, shift_start, shift_end,
// needs_onset, needs_resolved. Each section uses its own line format
// (shift events lead with the work assignment + window; needs events
// lead with the relevant needs and the actor's current place).
//
// Examples:
//
//	Beginning their shift now:
//	- Ezekiel Crane (Blacksmith, 07:00-19:00) -- currently at the Inn
//
//	Needs newly arising:
//	- Josiah Brand -- now weary -- currently at the Mill (work: Mill, 07:00-19:00)
//
//	Needs newly satisfied:
//	- Prudence Ward -- no longer parched [from the well] -- currently at the Well (work: Apothecary, 07:30-18:30)
//
// The chronicler reads these sections and decides whether to attend
// each listed villager. The attend_to tool's broader authorization
// covers shift-begin, shift-end, needs-onset, and needs-resolved as
// legitimate reasons (NPCs newly distressed at their workplace, or
// who abandoned posts due to high needs and now don't have those
// needs, are both the case the chronicler should nudge).
func (app *App) renderDispatchSections(ctx context.Context, batches []*chroniclerDispatchBatch) []string {
	if len(batches) == 0 {
		return nil
	}
	// Group by event type so two batches of the same type (different
	// boundary minutes within the same fire window — rare but possible
	// near phase boundaries) collapse into one section. Preserves the
	// chronicler's mental model of "what's happening now" rather than
	// surfacing the queue's internal sharding.
	byType := map[chroniclerDispatchEventType][]chroniclerDispatchAgent{}
	for _, b := range batches {
		byType[b.EventType] = append(byType[b.EventType], b.Agents...)
	}

	// Layer-2 merge: collect the (actor, workplace) set from shift_start
	// agents. Arrivals at a matching workplace pick up an "as their shift
	// began" suffix; the matching shift_start agent is omitted from the
	// shift section. Without this, Prudence's workplace arrival surfaces
	// in BOTH "Recent arrivals" and "Beginning their shift now" with
	// overlapping content — two lines about one event.
	shiftStarted := map[shiftMergeKey]struct{}{}
	for _, a := range byType[dispatchShiftStart] {
		if a.WorkStructureID == "" {
			continue
		}
		shiftStarted[shiftMergeKey{ActorID: a.ID, StructureID: a.WorkStructureID}] = struct{}{}
	}

	// Arrival current-state validation: at flush time, look up where each
	// arriving actor is RIGHT NOW. The buffered queue holds the structure
	// they were ENTERING when the event fired — they may have moved on by
	// the time the chronicler reads this. groupArrivalsByActor consolidates
	// per-actor arrival sequences and decides phrasing (single arrival,
	// multi-stop trail, current-matches-final, moved-on) so the chronicler
	// reads the actor's whole journey as one line instead of disconnected
	// "X arrived at Y" entries that imply they're still there.
	//
	// consumedShiftStarts is populated only when the shift suffix would
	// actually fire (current matches final + shift_started covers it),
	// so an actor who arrived at workplace then wandered off keeps their
	// shift_start line visible in the shift section instead of silently
	// vanishing.
	var arrivalGroupOrder []string
	var arrivalGroups map[string]*actorArrivalGroup
	consumedShiftStarts := map[shiftMergeKey]struct{}{}
	if arrivals := byType[dispatchArrival]; len(arrivals) > 0 {
		actorIDs := uniqueArrivalActorIDs(arrivals)
		currentByActor := app.lookupCurrentStructures(ctx, actorIDs)
		arrivalGroupOrder, arrivalGroups, consumedShiftStarts = groupArrivalsByActor(arrivals, currentByActor, shiftStarted)
	}

	// Stable section order across fires regardless of map iteration order.
	// Onset before resolved so the chronicler reads new distress before
	// recoveries — fresh problems usually warrant attention sooner than
	// resolutions (which are nudge-back-to-work signals, not crises).
	var sections []string
	for _, et := range []chroniclerDispatchEventType{
		dispatchArrival,
		dispatchShiftStart,
		dispatchShiftEnd,
		dispatchNeedsOnset,
		dispatchNeedsResolved,
	} {
		agents := byType[et]
		if len(agents) == 0 {
			continue
		}
		switch et {
		case dispatchArrival:
			sections = append(sections, renderArrivalSection(arrivalGroupOrder, arrivalGroups, shiftStarted))
		case dispatchShiftStart:
			if s := renderShiftSection(et, agents, consumedShiftStarts); s != "" {
				sections = append(sections, s)
			}
		case dispatchShiftEnd:
			sections = append(sections, renderShiftSection(et, agents, nil))
		case dispatchNeedsOnset:
			sections = append(sections, renderNeedsOnsetSection(agents))
		case dispatchNeedsResolved:
			sections = append(sections, renderNeedsResolvedSection(agents))
		}
	}
	return sections
}

// uniqueArrivalActorIDs collects each actor ID once in first-appearance
// order from a slice of arrival events. Used to scope the
// lookupCurrentStructures query to only the actors that have arrivals
// in this batch.
func uniqueArrivalActorIDs(arrivals []chroniclerDispatchAgent) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(arrivals))
	for _, a := range arrivals {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a.ID)
	}
	return out
}

// actorArrivalGroup consolidates one actor's arrival events for the
// per-actor render in renderArrivalSection. Arrivals are sorted by
// OccurredAt asc — Final is the latest, Intermediates is everything
// before. CurrentKnown distinguishes "DB lookup succeeded for this
// actor" from "lookup failed / actor row gone" so the renderer can
// fall back to bare per-arrival lines instead of inventing a location.
// ShiftBegan is true iff the actor's current location matches the
// final arrival AND a shift_start covers the same (actor, workplace),
// which is exactly when "as their shift began" should fire.
type actorArrivalGroup struct {
	ActorID              string
	DisplayName          string
	Arrivals             []chroniclerDispatchAgent // sorted by OccurredAt asc
	CurrentKnown         bool
	CurrentStructureID   string
	CurrentStructureName string
	ShiftBegan           bool
}

// groupArrivalsByActor folds a slice of arrival events into per-actor
// groups, preserving first-appearance order in actorOrder for stable
// rendering. For each actor with a current_inside_structure_id reading,
// computes ShiftBegan (current matches final AND shift_started covers
// it). Adds the corresponding (actor, workplace) key to consumedShiftStarts
// so renderShiftSection knows to skip that shift_start row.
//
// Actors absent from currentByActor (lookup query failed, or DB row
// gone between enqueue and flush) get CurrentKnown=false; the renderer
// treats them as "current unknown" and emits bare per-arrival lines
// like Phase 1.
func groupArrivalsByActor(
	arrivals []chroniclerDispatchAgent,
	currentByActor map[string]actorCurrent,
	shiftStarted map[shiftMergeKey]struct{},
) (actorOrder []string, groupsByActor map[string]*actorArrivalGroup, consumedShiftStarts map[shiftMergeKey]struct{}) {
	groupsByActor = map[string]*actorArrivalGroup{}
	consumedShiftStarts = map[shiftMergeKey]struct{}{}
	for _, a := range arrivals {
		g, ok := groupsByActor[a.ID]
		if !ok {
			g = &actorArrivalGroup{ActorID: a.ID, DisplayName: a.DisplayName}
			groupsByActor[a.ID] = g
			actorOrder = append(actorOrder, a.ID)
		}
		g.Arrivals = append(g.Arrivals, a)
	}
	for _, g := range groupsByActor {
		sort.SliceStable(g.Arrivals, func(i, j int) bool {
			return g.Arrivals[i].OccurredAt.Before(g.Arrivals[j].OccurredAt)
		})
		cur, ok := currentByActor[g.ActorID]
		if !ok {
			// Current unknown for this actor — DB query failed or row
			// missing. Fall back to Part A merge: consume shift_start
			// for any arrival that matches a shifted workplace, so the
			// shift section doesn't duplicate the arrival section's
			// "as their shift began" line. Slightly stale (the suffix
			// fires even if the actor has since wandered off the
			// workplace), but DB failures are rare and avoiding the
			// duplicate is the higher-value behavior to preserve.
			for _, a := range g.Arrivals {
				k := shiftMergeKey{ActorID: g.ActorID, StructureID: a.ArrivalStructureID}
				if _, ok := shiftStarted[k]; ok {
					consumedShiftStarts[k] = struct{}{}
				}
			}
			continue
		}
		g.CurrentKnown = true
		g.CurrentStructureID = cur.StructureID
		g.CurrentStructureName = cur.StructureName
		final := g.Arrivals[len(g.Arrivals)-1]
		if cur.StructureID != "" && cur.StructureID == final.ArrivalStructureID {
			k := shiftMergeKey{ActorID: g.ActorID, StructureID: final.ArrivalStructureID}
			if _, ok := shiftStarted[k]; ok {
				g.ShiftBegan = true
				consumedShiftStarts[k] = struct{}{}
			}
		}
	}
	return
}

// shiftMergeKey identifies an (actor, workplace) pair for layer-2
// perception grouping: a shift_start at workplace X coincident with an
// arrival at workplace X collapses into one merged line in the arrival
// section. Joined on village_object.id (not display name) so a
// display_name flip between the scheduler's per-tick load and the
// arrival's lookupStructureName call can't break the merge.
type shiftMergeKey struct {
	ActorID     string
	StructureID string
}

// renderArrivalSection renders the buffered-arrival section (ZBBS-119).
// One line per actor (not per arrival event) so a multi-stop journey
// reads as one sentence instead of disconnected lines.
//
// Per-actor phrasing depends on the actor's current location vs the
// arrival sequence:
//   - 1 arrival, current matches: "X arrived at Y" (+ shift suffix if applicable)
//   - 1 arrival, current is some other place: "X briefly appeared at Y, then went to Z"
//   - 1 arrival, current is outdoors: "X briefly appeared at Y"
//   - multi, final matches current: "X passed through A and B, then arrived at Y"
//   - multi, final doesn't match: "X passed through A, B, and Y, then went to Z" (or just the trail if outdoors)
//
// Fallback: when the actor's CurrentKnown is false (DB lookup failed
// or actor row gone between enqueue and flush), each arrival renders
// as a bare "X arrived at Y" line (Phase 1 behavior). The shift suffix
// still fires per-arrival when shiftStarted matches so we don't
// regress Part A's merge in the fallback path.
func renderArrivalSection(actorOrder []string, groups map[string]*actorArrivalGroup, shiftStarted map[shiftMergeKey]struct{}) string {
	if len(actorOrder) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Recent arrivals:")
	joinNames := func(arr []chroniclerDispatchAgent) {
		for i, p := range arr {
			if i > 0 {
				if i == len(arr)-1 {
					b.WriteString(" and ")
				} else {
					b.WriteString(", ")
				}
			}
			b.WriteString(p.ArrivalStructureName)
		}
	}
	for _, actorID := range actorOrder {
		g := groups[actorID]
		if !g.CurrentKnown {
			// DB lookup didn't return this actor; fall back to bare per-
			// arrival lines so we don't invent a current location.
			for _, a := range g.Arrivals {
				b.WriteString("\n- ")
				b.WriteString(a.DisplayName)
				b.WriteString(" arrived at ")
				b.WriteString(a.ArrivalStructureName)
				if _, ok := shiftStarted[shiftMergeKey{ActorID: actorID, StructureID: a.ArrivalStructureID}]; ok {
					b.WriteString(" as their shift began")
				}
			}
			continue
		}
		final := g.Arrivals[len(g.Arrivals)-1]
		intermediates := g.Arrivals[:len(g.Arrivals)-1]
		currentMatchesFinal := g.CurrentStructureID != "" && g.CurrentStructureID == final.ArrivalStructureID
		b.WriteString("\n- ")
		b.WriteString(g.DisplayName)
		switch {
		case len(g.Arrivals) == 1 && currentMatchesFinal:
			b.WriteString(" arrived at ")
			b.WriteString(final.ArrivalStructureName)
		case len(g.Arrivals) == 1 && !currentMatchesFinal:
			b.WriteString(" briefly appeared at ")
			b.WriteString(final.ArrivalStructureName)
			if g.CurrentStructureName != "" {
				b.WriteString(", then went to ")
				b.WriteString(g.CurrentStructureName)
			}
		case len(g.Arrivals) > 1 && currentMatchesFinal:
			b.WriteString(" passed through ")
			joinNames(intermediates)
			b.WriteString(", then arrived at ")
			b.WriteString(final.ArrivalStructureName)
		case len(g.Arrivals) > 1 && !currentMatchesFinal:
			b.WriteString(" passed through ")
			joinNames(g.Arrivals)
			if g.CurrentStructureName != "" {
				b.WriteString(", then went to ")
				b.WriteString(g.CurrentStructureName)
			}
		}
		if g.ShiftBegan {
			b.WriteString(" as their shift began")
		}
	}
	return b.String()
}

// renderShiftSection renders a single shift_start or shift_end section.
// Format: "<heading>\n- <name> (<work>, HH:MM-HH:MM) -- currently at <place>".
//
// Agents whose (actor, workplace) appears in mergedIntoArrival are skipped
// — their entry has been folded into the arrival section's "as their
// shift began" suffix. Returns "" if every agent was filtered, so the
// caller can omit the section entirely.
func renderShiftSection(et chroniclerDispatchEventType, agents []chroniclerDispatchAgent, mergedIntoArrival map[shiftMergeKey]struct{}) string {
	heading := "Beginning their shift now:"
	if et == dispatchShiftEnd {
		heading = "Ending their shift now:"
	}
	var b strings.Builder
	wrote := false
	for _, a := range agents {
		if _, ok := mergedIntoArrival[shiftMergeKey{ActorID: a.ID, StructureID: a.WorkStructureID}]; ok {
			continue
		}
		if !wrote {
			b.WriteString(heading)
			wrote = true
		}
		b.WriteString("\n- ")
		b.WriteString(a.DisplayName)
		b.WriteString(" (")
		b.WriteString(a.WorkPlace)
		b.WriteString(", ")
		b.WriteString(a.ShiftStart)
		b.WriteString("-")
		b.WriteString(a.ShiftEnd)
		b.WriteString(") -- currently at ")
		b.WriteString(a.CurrentPlace)
	}
	if !wrote {
		return ""
	}
	return b.String()
}

// renderNeedsOnsetSection renders the "Needs newly arising" section for
// villagers whose hunger/thirst/tiredness crossed UP into the red
// threshold on the most recent needs tick. Inverse of
// renderNeedsResolvedSection — the chronicler reads these as fresh
// distress events that may warrant attend_to before the need climbs
// to peak. No source field; the onset is the natural drift of the
// hourly tick, not a discrete in-world action.
//
// Lines mirror the resolved section's structure (place + work
// annotation) so the chronicler can spot "newly weary at the Mill,
// works there" — distress at the workplace is an attend candidate,
// distress off-shift may not need intervention.
func renderNeedsOnsetSection(agents []chroniclerDispatchAgent) string {
	var b strings.Builder
	b.WriteString("Needs newly arising:")
	for _, a := range agents {
		b.WriteString("\n- ")
		b.WriteString(a.DisplayName)
		b.WriteString(" -- now ")
		// joinResolvedNeedLabels joins need keys into the red-tier
		// vocabulary (hungry / parched / weary). The "Resolved" prefix
		// in its name is historical — the join itself is direction-
		// agnostic, and the same vocabulary applies to fresh distress.
		b.WriteString(joinResolvedNeedLabels(a.OnsetNeeds))
		b.WriteString(" -- currently at ")
		b.WriteString(a.CurrentPlace)
		if a.WorkPlace != "" {
			b.WriteString(" (work: ")
			b.WriteString(a.WorkPlace)
			if a.ShiftStart != "" && a.ShiftEnd != "" {
				b.WriteString(", ")
				b.WriteString(a.ShiftStart)
				b.WriteString("-")
				b.WriteString(a.ShiftEnd)
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

// renderNeedsResolvedSection renders the "Needs newly satisfied" section
// for villagers whose needs crossed below the red threshold this tick.
// Tone: factual + recovery-leading, so the chronicler reads them as
// candidates for attend_to (especially when their current place is not
// their work place).
//
// Lines fold each agent's resolved need(s) and source into one phrase.
// One need: "no longer parched". Two needs: "no longer hungry or
// parched". Source becomes a parenthetical hint for non-admin sources;
// admin resets are unattributed (the chronicler doesn't need to know
// the operator intervened, just that the need is gone).
//
// Work annotation: when the agent has a work assignment, append a
// "(work: <place>, HH:MM-HH:MM)" suffix so the chronicler can spot
// "currently at the Well, but works at the Apothecary 07:30-18:30"
// at a glance — the exact case that should trigger attend_to.
func renderNeedsResolvedSection(agents []chroniclerDispatchAgent) string {
	var b strings.Builder
	b.WriteString("Needs newly satisfied:")
	for _, a := range agents {
		b.WriteString("\n- ")
		b.WriteString(a.DisplayName)
		b.WriteString(" -- no longer ")
		b.WriteString(joinResolvedNeedLabels(a.ResolvedNeeds))
		if hint := sourceHint(a.Source); hint != "" {
			b.WriteString(" ")
			b.WriteString(hint)
		}
		b.WriteString(" -- currently at ")
		b.WriteString(a.CurrentPlace)
		if a.WorkPlace != "" {
			b.WriteString(" (work: ")
			b.WriteString(a.WorkPlace)
			if a.ShiftStart != "" && a.ShiftEnd != "" {
				b.WriteString(", ")
				b.WriteString(a.ShiftStart)
				b.WriteString("-")
				b.WriteString(a.ShiftEnd)
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

// joinResolvedNeedLabels turns a list of resolved-need keys ("hunger",
// "thirst", "tiredness") into a recovery-tense phrase using the same
// vocabulary as needLabel's red-tier so the chronicler reads
// "no longer parched" rather than the bland "no longer thirsty" (which
// is the mild-tier word). Recovery is from the red tier — that's what
// the threshold crossing represents.
func joinResolvedNeedLabels(needs []string) string {
	if len(needs) == 0 {
		return "in distress"
	}
	labels := make([]string, 0, len(needs))
	for _, n := range needs {
		switch n {
		case "hunger":
			labels = append(labels, "hungry")
		case "thirst":
			labels = append(labels, "parched")
		case "tiredness":
			labels = append(labels, "weary")
		default:
			labels = append(labels, n)
		}
	}
	switch len(labels) {
	case 1:
		return labels[0]
	case 2:
		return labels[0] + " or " + labels[1]
	default:
		// Three needs all crossing in one call is rare (only the admin
		// reset path produces it today). Render as Oxford-comma list.
		return strings.Join(labels[:len(labels)-1], ", ") + ", or " + labels[len(labels)-1]
	}
}

// sourceHint renders the consumption source as a chronicler-friendly
// parenthetical. Admin resets are unattributed — the operator's hand
// isn't part of the in-world narrative. Other sources surface so the
// chronicler can shade its attention call ("she has slaked her thirst
// at the well" reads differently from "she has finished her meal").
//
// Whitelisted: a future caller passing free-form (or worse, model-
// influenced) source text shouldn't end up rendering arbitrary content
// into the chronicler's perception. Unknown sources collapse to the
// silent case rather than echoing the input.
func sourceHint(source string) string {
	switch source {
	case "well":
		return "[from the well]"
	case "meal_or_drink":
		return "[from a meal or drink]"
	default:
		return ""
	}
}

// dispatchChroniclerShiftBoundaries fires the chronicler when the
// dispatch queue has pending shift events AND no other fire (phase or
// cascade) has already drained them this tick. Called from the server
// tick loop after dispatchScheduledBehaviors so the worker scheduler
// has had a chance to enqueue.
//
// Cheap when nothing is pending (single mutex-guarded len check). When
// pending, fires the chronicler with reason.Type == "shift_boundary";
// the perception build drains the queue inside the fire, so this caller
// doesn't need to pass the batches through.
//
// Honors AgentTicksPaused — if the admin halted agent activity, the
// chronicler stays quiet on shift boundaries too. The queued events
// remain in memory until the next fire (or process restart) drains
// them; that is intentional, matching how phase fires behave under
// pause.
func (app *App) dispatchChroniclerShiftBoundaries(ctx context.Context) {
	if app.ChroniclerDispatchQueue.pending() == 0 {
		return
	}
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("chronicler-shift: load config: %v", err)
		return
	}
	if cfg.AgentTicksPaused {
		return
	}
	if err := app.fireChronicler(ctx, chroniclerFireReason{Type: "shift_boundary", Priority: chroniclerFirePriorityRoutine}); err != nil {
		log.Printf("chronicler-shift: fire failed: %v", err)
	}
}

// resolveVillagerForAttention maps a villager name (display_name or slug)
// to the npc.id needed by triggerImmediateTick. Display name is primary
// (matches what appears in the roster the overseer sees) with a slug
// fallback so the overseer can address NPCs either way. Case-insensitive,
// trimmed. Returns the display_name as well so the dispatcher can render
// the "[You attend to X.]" tool result with the canonical form.
func (app *App) resolveVillagerForAttention(ctx context.Context, villager string) (string, string, bool) {
	var npcID, displayName string
	// After ZBBS-084 the actor table holds display_name directly, no
	// JOIN needed. We only attend to LLM-driven NPCs (the overseer's
	// directorial dispatch is meant to rouse agents, not PCs or
	// decorative villagers).
	err := app.DB.QueryRow(ctx, `
		SELECT id, display_name
		FROM actor
		WHERE llm_memory_agent IS NOT NULL
		  AND LOWER(display_name) = LOWER($1)
		LIMIT 1
	`, villager).Scan(&npcID, &displayName)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("chronicler: resolve villager %q: %v", villager, err)
		}
		return "", "", false
	}
	return npcID, displayName, true
}

// chroniclerAttendExempt reports whether a fire's reason exempts its
// attend_to calls from the cross-fire cooldown (ZBBS-119). Exempt:
// cascade fires whose reason names a PC speech, PC arrival, or admin
// override — those represent fresh significant events that should
// always go through. Routine fires (buffered_flush, phase,
// shift_boundary) apply the cooldown.
//
// Classification reads chroniclerFireReason.Priority — set explicitly
// at the cascade-origin call site. Replaces the prior wire-format
// prefix check ("pc-" / "admin-attend-now") which was flagged fragile
// in the original implementation.
func chroniclerAttendExempt(reason chroniclerFireReason) bool {
	return reason.Type == "cascade" && reason.Priority == chroniclerFirePriorityHigh
}

// recentChroniclerAttend reports whether the named NPC was attended
// within chroniclerAttendCooldown of now. Returns the elapsed time
// since the prior attend (for the rejection message) and true when
// recent. The mutex is held only for the lookup.
func (app *App) recentChroniclerAttend(npcID string) (time.Duration, bool) {
	app.LastChroniclerAttendAtMu.Lock()
	defer app.LastChroniclerAttendAtMu.Unlock()
	last, ok := app.LastChroniclerAttendAt[npcID]
	if !ok {
		return 0, false
	}
	since := time.Since(last)
	return since, since < chroniclerAttendCooldown
}

// recordChroniclerAttend stamps the cross-fire cooldown clock for
// npcID. Called after every successful attend_to dispatch (including
// exempt ones, so the next routine fire measures cooldown against the
// most recent dispatch regardless of who made it).
func (app *App) recordChroniclerAttend(npcID string) {
	app.LastChroniclerAttendAtMu.Lock()
	defer app.LastChroniclerAttendAtMu.Unlock()
	app.LastChroniclerAttendAt[npcID] = time.Now()
}

