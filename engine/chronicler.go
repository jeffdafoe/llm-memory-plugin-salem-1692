package main

// Salem Chronicler integration.
//
// The Chronicler is a virtual agent (salem-chronicler) that fires only
// at scheduled phase boundaries (dawn / midday / dusk per game day) as
// of ZBBS-WORK-202. It writes atmosphere via set_environment into
// world_environment; the marquee ticker on the Godot client and the
// "Atmosphere:" line in NPC perception read from there.
//
// The chronicler does NOT direct or move NPCs. NPC ticks fire from
// cascade origins, the engine self-tick scheduler, and the engine
// idle-sweep dispatcher (ZBBS-HOME-201). The pre-202 attend_to tool was
// removed in ZBBS-HOME-202; the pre-202 record_event / record_announcement
// tools and the buffered cascade-firing infrastructure were removed in
// ZBBS-WORK-202.
//
// Canonical design: shared/notes/codebase/salem/overseer-design (history)
// and shared/notes/codebase/salem/chronicler-buffered-dispatch (post-rip
// shipped mechanism, kept under that slug for continuity).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// chroniclerAgent is the slug of the narrative-author virtual agent
	// the engine fires at phase boundaries. Realms=['salem'] matches the
	// four NPCs and the engine itself, so realm-overlap permits cross-
	// namespace recall against any of them.
	chroniclerAgent = "salem-chronicler"

	// Harness budget — max iterations per chronicler fire. Each
	// iteration is one model API call processing one tool call
	// (set_environment, recall, done). Loaded per fire via
	// chronicler_tick_budget setting, clamped to
	// [chroniclerTickBudgetMin, chroniclerTickBudgetMax]. With the post-
	// 202 surface (one authoring tool plus recall plus done) typical
	// fires consume 2-3 iterations and the ceiling is harmless headroom.
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

)

// chroniclerFireReason captures why the chronicler is being fired this
// invocation. Post-ZBBS-WORK-202 the chronicler only fires at scheduled
// phase boundaries (dawn / midday / dusk); cascade-origin and shift-
// boundary firing paths were removed.
type chroniclerFireReason struct {
	// Type is "phase" — the only firing path post-rip. Retained as a
	// field for prompt rendering and phase-column stamping.
	Type string

	// Phase is "dawn" | "midday" | "dusk". Stamped onto world_environment
	// rows the chronicler writes during this fire.
	Phase string
}

// chroniclerToolSpec returns the tool definitions offered to the
// chronicler at every fire.
//
// Post-ZBBS-WORK-202 the chronicler authors atmosphere (set_environment)
// and can search collective memory (recall). It does not direct or
// dispatch NPCs. The pre-202 record_event / record_announcement /
// attend_to tools were removed: attend_to in ZBBS-HOME-202 (chronicler
// dispatch in practice selected nearly every candidate every fire and
// billed LLM cost without producing a durable schedule); record_event
// and record_announcement in ZBBS-WORK-202 (mostly-redundant restatements
// of agent_action_log content with zero observable NPC citation, and
// a silent town crier was acceptable).
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
	if err := app.fireChronicler(ctx, chroniclerFireReason{Type: "phase", Phase: currentPhase}); err != nil {
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

	// nextMessage carries the iter-0 perception; from iter 1 onward the
	// model is responding to tool results, so message="" and nextResults
	// is the bridge instead. Mutually exclusive — sendChat picks the
	// non-empty one.
	nextMessage := perception
	var nextResults []toolResult
	chatSucceeded := false
	// Group this fire's harness iterations under one scene_id (MEM-121)
	// so the admin UI can collapse them into a single expandable row.
	// Phase fires are village-wide, so the scene records NULL structure_id
	// and the admin UI shows no location chip.
	sceneID := app.newScene(ctx, "")

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
		reply, err := app.npcChatClient.sendChat(ctx, chroniclerAgent, nextMessage, nextResults, sceneID, sceneStructure, tools)
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

		// Reset the iteration carries — they'll be repopulated below if
		// we're going to make another sendChat call. Iter-0's perception
		// has been delivered; from here on the bridge is tool results.
		nextMessage = ""
		nextResults = nil

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

		// Process ALL tool calls the model emitted in this reply.
		// Pre-fix this used to pick reply.ToolCalls[0] only, forcing the
		// model to re-emit dropped calls across multiple round-trips and
		// leaving orphan tool_call_ids in the persisted history. Now we
		// run them in order, accumulate one toolResult per call, and
		// hand the whole batch back on the next sendChat.
		terminal := false
		pendingResults := make([]toolResult, 0, len(reply.ToolCalls))

		for i := range reply.ToolCalls {
			tc := &reply.ToolCalls[i]
			var resultContent string

			switch tc.Name {
			case "set_environment":
				text, _ := tc.Input["text"].(string)
				text = strings.TrimSpace(text)
				if text == "" {
					resultContent = "[The atmosphere you tried to write was empty. Try again or say done.]"
				} else if app.recentEnvironmentMatches(ctx, text, 60*time.Second) {
					// Dedupe (2026-05-02): chronicler occasionally emits
					// identical atmosphere text twice in the same cascade —
					// saw a verbatim "Dawn lies pale over the spring village"
					// repeat 3 seconds apart at 11:01:05/11:01:08. Skip the
					// write but acknowledge so the harness moves on instead of
					// burning another iteration trying to re-emit.
					resultContent = "[Atmosphere unchanged — the prose you wrote matches what is already set. Move on or say done.]"
				} else if err := app.recordEnvironment(ctx, text, reason.Phase); err != nil {
					log.Printf("chronicler set_environment: %v", err)
					resultContent = "[Atmosphere could not be recorded. Try again or say done.]"
				} else {
					resultContent = "[Atmosphere noted.]"
				}

			case "recall":
				query, _ := tc.Input["query"].(string)
				resultContent = app.resolveChroniclerRecall(ctx, query)

			case "done":
				terminal = true
				// Acknowledge the call so it isn't orphan in history.
				// persistToolResults below writes this row without
				// firing another LLM turn.
				resultContent = "[OK]"

			default:
				log.Printf("chronicler: unknown tool %q, terminating", tc.Name)
				terminal = true
				resultContent = fmt.Sprintf("[Unknown tool %q.]", tc.Name)
			}

			pendingResults = append(pendingResults, toolResult{ID: tc.ID, Content: resultContent})

			// Stop processing subsequent tool calls in this reply once
			// a terminal tool fires. Otherwise a reply like
			// [done, record_event] would still execute record_event
			// after done, which doesn't match the semantic of "done".
			// The terminal tool's own result is preserved (just
			// appended above); later un-processed tool calls remain
			// orphan but get dropped by openai.js orphan-filtering on
			// the next history reconstruction.
			if terminal {
				break
			}
		}

		if terminal {
			// Persist the tool results without firing another model
			// turn. Closes out the assistant's tool_calls in conversation
			// history (no orphan tool_call_ids) at the cost of one
			// quick API write rather than a full LLM round-trip.
			if err := app.npcChatClient.persistToolResults(ctx, chroniclerAgent, pendingResults, sceneID, sceneStructure); err != nil {
				log.Printf("chronicler: persistToolResults: %v", err)
			}
			break
		}

		// Non-terminal: hand the accumulated results forward to the
		// next iteration's sendChat call.
		nextResults = pendingResults
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

// buildChroniclerPerception constructs the user-message text the
// chronicler sees on iteration 0. Sections in order:
//
//  1. Why you wake (phase boundary)
//  2. Mood + season (config-driven knobs the admin flips)
//  3. NPC roster grouped by current location
//  4. Recent atmospheric statements (your own last N — evolve, don't whiplash)
//  5. Activity digest since last fire (deterministic Go-rendered)
//  6. Decision prompt
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

	// 5. Activity digest since last fire — what's happened in the
	// village in the interval the chronicler hasn't been awake for.
	if digest := app.buildActivityDigest(ctx, app.loadChroniclerLastAttentionAt(ctx)); digest != "" {
		sections = append(sections, digest)
	}

	// 6. Decision prompt. Atmosphere-author only post-ZBBS-WORK-202.
	sections = append(sections, "Attend to the village. Use set_environment to write the current atmosphere if it has shifted. You may use recall to remember anything the village has experienced. Use done when your office is finished.")

	return strings.Join(sections, "\n\n")
}

// chroniclerOpeningLine renders the perception's opening "why are you
// waking" line. Post-ZBBS-WORK-202 the chronicler only fires at phase
// boundaries.
func (app *App) chroniclerOpeningLine(ctx context.Context, reason chroniclerFireReason) string {
	if reason.Type == "phase" && reason.Phase != "" {
		return fmt.Sprintf("It is %s. The hour has come for you to attend the village.", reason.Phase)
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
// when no prior fire timestamp is recorded (fresh deploy / first fire
// after restart). Falls back to "the village has been quiet" when there
// is a prior fire timestamp but no activity in the window.
//
// Format: "Since the last fire: John walked 2 times, spoke 4 times.
// Prudence completed 1 chore. ..."
func (app *App) buildActivityDigest(ctx context.Context, since time.Time) string {
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


