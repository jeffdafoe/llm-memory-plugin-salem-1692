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
	"errors"
	"fmt"
	"log"
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

	// Harness budget — the chronicler typically does set_environment +
	// (optional) record_event + done. 4 iterations is plenty; rarely
	// needs more than 3. Lower than agentTickBudget=6 for NPCs because
	// chroniclers don't have to react to follow-up nudges the way NPCs
	// do (no "you spoke, continue your turn" pattern).
	chroniclerTickBudget = 4

	// Recent atmospheric statements surfaced in the chronicler's own
	// perception. Lets it evolve atmosphere coherently rather than
	// pivoting wildly each fire.
	chroniclerEnvHistoryCount = 3

	// Recent events surfaced in NPC and chronicler perceptions. Cap to
	// avoid prompt bloat; events older than the lookback window can
	// still be surfaced via recall but don't appear automatically.
	recentEventsCount  = 20
	recentEventsWindow = 7 * 24 * time.Hour
)

// chroniclerFireReason captures why the chronicler is being fired this
// invocation. Used to render the perception's opening line and to
// stamp the phase column on world_environment writes.
type chroniclerFireReason struct {
	// Type is "phase" (scheduled boundary) or "cascade" (event-driven).
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
			Description: "Direct your attention to a named villager so they may rouse and tend to themselves. Use when you see a soul whose body is in distress — hungry, parched, or weary — and a small voice within them might move them to act. The villager you attend to will think and may take an action; you do not move their hands. Use sparingly: there is a finite measure to how many you may attend in one waking.",
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
// runServerTickOnce alongside dispatchAgentTicks.
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
	if err := app.fireChronicler(ctx, chroniclerFireReason{Type: "phase", Phase: currentPhase}); err != nil {
		log.Printf("chronicler-phase: fire failed, not advancing phase state: %v", err)
		return
	}
	app.saveChroniclerPhaseState(ctx, currentPhase, boundaryAt)
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
// Concurrency-capped via app.ChroniclerSem so a slow / hung chat API
// can't pile up unbounded goroutines. Cascade fires that arrive while
// the slot is full are skipped (logged) rather than queued — better to
// drop a fire than to back up an arbitrary queue with stale work.
func (app *App) cascadeOriginFireChronicler(reason, structureID string) {
	if app.ChroniclerSem == nil {
		// Defensive — should always be initialized at startup. If not,
		// fall through to the unbounded path so we don't silently drop
		// fires on a misconfigured engine.
		go app.runCascadeFire(reason, structureID)
		return
	}
	select {
	case app.ChroniclerSem <- struct{}{}:
		go func() {
			defer func() { <-app.ChroniclerSem }()
			app.runCascadeFire(reason, structureID)
		}()
	default:
		log.Printf("chronicler-cascade: slot full, skipping fire (reason=%q)", reason)
	}
}

// runCascadeFire is the body of a cascade-origin chronicler fire.
// Extracted so cascadeOriginFireChronicler can wrap it with the
// concurrency-cap semaphore.
func (app *App) runCascadeFire(reason, structureID string) {
	ctx := context.Background()
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("chronicler-cascade: load config: %v", err)
		return
	}
	if cfg.AgentTicksPaused {
		return
	}
	if err := app.fireChronicler(ctx, chroniclerFireReason{
		Type:          "cascade",
		CascadeReason: reason,
		StructureID:   structureID,
	}); err != nil {
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
	perception := app.buildChroniclerPerception(ctx, reason)
	tools := chroniclerToolSpec()

	currentMessage := perception
	currentToolCallID := ""
	chatSucceeded := false
	// Group this fire's harness iterations under one scene_id (MEM-121)
	// so the admin UI can collapse them into a single expandable row.
	sceneID := newUUIDv7()

	// Per-fire attend_to counter (ZBBS-083). Reset each fire — the
	// configured ceiling is per-fire, not per-day. Read once at the top
	// so an admin tweaking the setting mid-fire doesn't change the cap
	// underneath us. loadNonNegativeIntSetting clamps a negative value to
	// the default rather than rejecting every call.
	attendCeiling := app.loadNonNegativeIntSetting(ctx, "chronicler_dispatch_ceiling", 12)
	attendCount := 0

	for iter := 0; iter < chroniclerTickBudget; iter++ {
		reply, err := app.npcChatClient.sendChat(ctx, chroniclerAgent, currentMessage, currentToolCallID, sceneID, tools)
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
			// Trigger the NPC's tick. Force=false so the existing cost-
			// guard in triggerImmediateTick (agentMinTickGap) still
			// applies — the overseer can rouse the same NPC repeatedly
			// across phases, but not within a single 5-minute window.
			// Background goroutine so a slow agent tick doesn't block the
			// overseer's harness loop. App-level semaphore caps aggregate
			// concurrency across overlapping fires; if the slot is full
			// we still let the call through (vs dropping) — the goroutine
			// just blocks waiting for a slot, then runs. Acceptable for
			// directorial dispatches; if backpressure becomes an issue we
			// can switch to skip-if-full like ChroniclerSem does.
			// Thread the chronicler's sceneID so the dispatched NPC tick
			// lands in the same scene as the overseer's fire — keeps the
			// admin UI's scene grouping coherent across the cascade.
			go func(id, name, scene string) {
				app.OverseerAttendSem <- struct{}{}
				defer func() { <-app.OverseerAttendSem }()
				app.triggerImmediateTick(context.Background(), id, "overseer-attend-to", false, scene)
			}(npcID, displayName, sceneID)
			attendCount++
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
func (app *App) buildChroniclerPerception(ctx context.Context, reason chroniclerFireReason) string {
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
	sections = append(sections, "Attend to the village. Use set_environment to write the current atmosphere if it has shifted. Use record_event to record any happening that should persist in village memory (default scope is village; pass scope_type='local' with scope_target=<structure_id> to restrict to one place, or scope_type='private' with scope_target=<npc_id> to restrict to one person). Use attend_to to rouse a villager whose body is in distress so they may tend to themselves. You may use recall to remember anything the village has experienced. Use done when your office is finished.")

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

// recordEnvironment appends a row to world_environment. Phase is "" for
// cascade-origin fires (NULL in DB), or one of dawn/midday/dusk for
// phase fires.
func (app *App) recordEnvironment(ctx context.Context, text, phase string) error {
	var phaseArg interface{}
	if phase == "" {
		phaseArg = nil
	} else {
		phaseArg = phase
	}
	_, err := app.DB.Exec(ctx,
		`INSERT INTO world_environment (text, set_by, phase, set_at) VALUES ($1, $2, $3, NOW())`,
		text, chroniclerAgent, phaseArg)
	return err
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
// Lives outside dispatchAgentTicks because that function short-circuits
// during paused/asleep/baseline-disabled paths, but reactive ticks
// (cascade origins) fire at any hour and need the map.
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
	hungerT := app.loadNeedThreshold(ctx, "hunger_red_threshold", defaultHungerRedThreshold)
	thirstT := app.loadNeedThreshold(ctx, "thirst_red_threshold", defaultThirstRedThreshold)
	tiredT := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)

	rows, err := app.DB.Query(ctx, `
		SELECT n.display_name,
		       COALESCE(o.display_name, a.name) AS place,
		       n.hunger, n.thirst, n.tiredness
		FROM actor n
		LEFT JOIN village_object o ON o.id = n.inside_structure_id
		LEFT JOIN asset a ON a.id = o.asset_id
		WHERE n.llm_memory_agent IS NOT NULL
		  AND (n.hunger    >= $1
		   OR  n.thirst    >= $2
		   OR  n.tiredness >= $3)
		ORDER BY n.display_name
	`, hungerT, thirstT, tiredT)
	if err != nil {
		log.Printf("chronicler: distress query: %v", err)
		return ""
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var name string
		var place sql.NullString
		var hunger, thirst, tiredness int
		if err := rows.Scan(&name, &place, &hunger, &thirst, &tiredness); err != nil {
			log.Printf("chronicler: distress scan: %v", err)
			continue
		}

		// Filter to red-tier and above only; mild-tier doesn't surface here.
		var labels []string
		if needLabelTier(hunger, hungerT) >= 2 {
			labels = append(labels, needLabel("hunger", hunger, hungerT))
		}
		if needLabelTier(thirst, thirstT) >= 2 {
			labels = append(labels, needLabel("thirst", thirst, thirstT))
		}
		if needLabelTier(tiredness, tiredT) >= 2 {
			labels = append(labels, needLabel("tiredness", tiredness, tiredT))
		}
		if len(labels) == 0 {
			// Row qualified by the threshold OR but all needs landed mild
			// (shouldn't happen given the WHERE clause matches the same
			// thresholds, but defensive). Skip.
			continue
		}

		placeStr := "the open village"
		if place.Valid && place.String != "" {
			placeStr = place.String
		}
		lines = append(lines, fmt.Sprintf("- %s (at %s): %s", name, placeStr, strings.Join(labels, ", ")))
	}
	if err := rows.Err(); err != nil {
		log.Printf("chronicler: distress rows: %v", err)
		return ""
	}

	if len(lines) == 0 {
		return ""
	}
	return "Villagers in distress:\n" + strings.Join(lines, "\n")
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

