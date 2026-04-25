package main

// LLM-driven NPC agent tick dispatcher (Salem M6.3).
//
// Runs every server tick (60s wall-clock). For each NPC with a linked
// llm_memory_agent that hasn't been ticked this in-world hour and is in
// the active 6am-9pm window, runs the harness loop:
//
//   1. Build a minimal perception (location, time, scheduler default,
//      list of named destinations).
//   2. POST /agent/tick with perception + tool spec.
//   3. If response carries observation tool_calls (look_around, recall),
//      resolve them against engine state, append to messages, re-POST.
//   4. If response carries a commit tool_call (move_to, speak, done),
//      execute it against engine state, write an agent_action_log row,
//      stamp last_agent_tick_at, and (for move_to) set agent_override_until
//      + forward-stamp last_shift_tick_at so the scheduler steps aside.
//   5. Hard-cap iterations at agentTickBudget. Beyond that, force a "done"
//      so the LLM can't burn budget thinking forever.
//
// Idempotency: last_agent_tick_at is stamped once per game-hour boundary
// the dispatcher acts on. A server crash mid-tick leaves the stamp empty
// for that hour and the next eligible tick re-fires. Same model the worker
// scheduler uses.
//
// Failure mode: if /agent/tick returns an error (rate-limit, cost-budget,
// HTTP failure, malformed response), the dispatcher logs and returns
// without stamping. The next server tick re-attempts. The NPC's existing
// scheduler keeps running underneath unless agent_override_until is set,
// so a hard outage on llm-memory-api degrades gracefully to scheduler-only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	agentTickBudget          = 6
	agentTickActiveStartHour = 6
	agentTickActiveEndHour   = 21 // exclusive — last active hour is 20:00-21:00
)

// agentNPCRow bundles everything the harness loop needs for one NPC.
type agentNPCRow struct {
	ID                 string
	DisplayName        string
	LLMMemoryAgent     string
	APIKey             string
	InsideStructureID  sql.NullString
	CurrentX           float64
	CurrentY           float64
	HomeStructureID    sql.NullString
	WorkStructureID    sql.NullString
	LastAgentTickAt    sql.NullTime
}

// dispatchAgentTicks is the per-server-tick entry. Loaded NPCs are processed
// sequentially within this server tick — the harness loop blocks on HTTP, so
// running all NPCs in parallel within one tick risks bursting the upstream
// LLM provider. Sequential keeps things bounded and easier to reason about.
//
// Future co-location grouping (M6.5) will run NPCs at the same location in
// sequential rounds so they can react to each other's speech.
func (app *App) dispatchAgentTicks(ctx context.Context) {
	now := time.Now()
	if now.Hour() < agentTickActiveStartHour || now.Hour() >= agentTickActiveEndHour {
		// Outside active hours — skip entirely. Avoids burning budget while
		// the village sleeps. Game-hour boundaries during the night will be
		// resumed at the first eligible morning tick.
		return
	}

	rows, err := app.loadAgentNPCRows(ctx)
	if err != nil {
		log.Printf("agent-tick: load rows: %v", err)
		return
	}

	hourStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	for i := range rows {
		r := &rows[i]
		// Skip NPCs already ticked this hour. Stamp uses hourStart so we don't
		// double-fire when the dispatcher runs multiple times per hour.
		if r.LastAgentTickAt.Valid && !r.LastAgentTickAt.Time.Before(hourStart) {
			continue
		}
		app.runAgentTick(ctx, r, hourStart)
	}
}

// loadAgentNPCRows pulls every NPC with a linked virtual agent + a configured
// API key. NPCs without a VA aren't agentized and run on the existing
// scheduler only.
func (app *App) loadAgentNPCRows(ctx context.Context) ([]agentNPCRow, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT n.id, n.display_name, n.llm_memory_agent,
		        va.llm_memory_api_key,
		        n.inside_structure_id, n.current_x, n.current_y,
		        n.home_structure_id, n.work_structure_id,
		        n.last_agent_tick_at
		 FROM npc n
		 JOIN village_agent va ON va.llm_memory_agent = n.llm_memory_agent
		 WHERE n.llm_memory_agent IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []agentNPCRow
	for rows.Next() {
		var r agentNPCRow
		if err := rows.Scan(&r.ID, &r.DisplayName, &r.LLMMemoryAgent,
			&r.APIKey,
			&r.InsideStructureID, &r.CurrentX, &r.CurrentY,
			&r.HomeStructureID, &r.WorkStructureID,
			&r.LastAgentTickAt); err != nil {
			log.Printf("agent-tick: scan: %v", err)
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// runAgentTick is the harness loop for one NPC. Stamps last_agent_tick_at
// at the end so a partial failure doesn't burn the hour — the next tick
// retries.
func (app *App) runAgentTick(ctx context.Context, r *agentNPCRow, hourStart time.Time) {
	perception, locationName := app.buildAgentPerception(ctx, r, hourStart)
	tools := agentToolSpec()

	messages := []agentMessage{}
	currentRequest := agentTickRequest{
		Perception: perception,
		Tools:      tools,
	}

	var commitCall *agentToolCall
	for iter := 0; iter < agentTickBudget; iter++ {
		resp, err := app.agentTickClient.callTick(ctx, r.APIKey, currentRequest)
		if err != nil {
			log.Printf("agent-tick %s iter=%d: %v", r.DisplayName, iter, err)
			return
		}

		// First iteration: seed the running messages array with the original
		// perception so subsequent iterations can keep adding to it.
		if iter == 0 {
			messages = append(messages, agentMessage{Role: "user", Content: perception})
		}

		// Append assistant response to the running conversation. Even when
		// only tool_calls are emitted (text=""), we keep the entry so the
		// LLM sees its own prior tool requests in subsequent rounds.
		assistantMsg := agentMessage{
			Role:    "assistant",
			Content: resp.Text,
		}
		for _, tc := range resp.ToolCalls {
			argsBytes, _ := json.Marshal(tc.Input)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, agentMessageCall{
				ID:   tc.ID,
				Type: "function",
				Function: agentMessageCallDetails{
					Name:      tc.Name,
					Arguments: string(argsBytes),
				},
			})
		}
		messages = append(messages, assistantMsg)

		if len(resp.ToolCalls) == 0 {
			// No tool call — model committed via plain text. Treat it as a
			// "done" with the text as the speech content (best-effort
			// recovery; ideally the model uses the tools).
			commitCall = &agentToolCall{
				ID:    fmt.Sprintf("synthetic-text-%d", iter),
				Name:  "speak",
				Input: map[string]interface{}{"text": resp.Text},
			}
			break
		}

		// Resolve every tool call returned this iteration. If ANY of them is
		// a commit tool, execute it and exit the loop. Observation tools get
		// resolved and their results appended for the next iteration.
		hadCommit := false
		for i := range resp.ToolCalls {
			tc := &resp.ToolCalls[i]
			if isCommitTool(tc.Name) {
				commitCall = tc
				hadCommit = true
				break
			}
			result := app.resolveObservationTool(ctx, r, tc, locationName)
			messages = append(messages, agentMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
		if hadCommit {
			break
		}

		// Continue the loop with the accumulated history. Drop perception
		// from this point on — it's now in messages[0].
		currentRequest = agentTickRequest{
			Messages: messages,
			Tools:    tools,
		}
	}

	// Budget exhausted without a commit — synthesize a "done" so we always
	// terminate the tick cleanly. Costs the equivalent of one wasted hour
	// for the NPC; rare, and the agent will get a fresh chance next hour.
	if commitCall == nil {
		commitCall = &agentToolCall{
			ID:    "synthetic-budget-exhausted",
			Name:  "done",
			Input: map[string]interface{}{},
		}
	}

	app.executeAgentCommit(ctx, r, commitCall)
	app.stampAgentTick(ctx, r, hourStart)
}

// stampAgentTick records that we've ticked this NPC for this in-world hour.
// Subsequent server ticks within the hour will skip this NPC.
func (app *App) stampAgentTick(ctx context.Context, r *agentNPCRow, hourStart time.Time) {
	if _, err := app.DB.Exec(ctx,
		`UPDATE npc SET last_agent_tick_at = $2 WHERE id = $1`,
		r.ID, hourStart,
	); err != nil {
		log.Printf("agent-tick: stamp %s: %v", r.DisplayName, err)
	}
}

// agentToolSpec returns the tool definitions the engine offers each tick.
// Same neutral shape the providers/index.js opts.tools contract expects.
// Subset of the design's full vocabulary — recall / accuse / gossip /
// assess_person / pray / think defer to later milestones (M6.4+).
func agentToolSpec() []agentToolDef {
	return []agentToolDef{
		{
			Name:        "look_around",
			Description: "Observe who and what is at your current location. Returns a description.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "move_to",
			Description: "Walk to a named structure in Salem. The engine handles pathfinding.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"destination": map[string]interface{}{
						"type":        "string",
						"description": "Structure name (e.g. 'Smithy', 'Tavern', 'Home').",
					},
				},
				"required": []string{"destination"},
			},
		},
		{
			Name:        "speak",
			Description: "Say something out loud to people at your current location.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{"type": "string"},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "done",
			Description: "Take no action this hour — rest or continue what you're already doing.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

// isCommitTool reports whether the named tool, when emitted, ends the
// harness loop. Observation tools (look_around, recall) don't.
func isCommitTool(name string) bool {
	switch name {
	case "move_to", "speak", "done":
		return true
	}
	return false
}

// buildAgentPerception constructs the user-message text the LLM sees on the
// first iteration. Minimal for M6.3 — location, time, list of valid named
// destinations. Acquaintance gating, recent block, scheduler-default
// injection are M6.4 work.
//
// Returns (perception, currentLocationName). The location name is also
// passed to the look_around resolver so it doesn't have to re-query.
func (app *App) buildAgentPerception(ctx context.Context, r *agentNPCRow, hourStart time.Time) (string, string) {
	locationName := "the open village"
	if r.InsideStructureID.Valid {
		var name sql.NullString
		_ = app.DB.QueryRow(ctx,
			`SELECT a.name FROM village_object o JOIN asset a ON a.id = o.asset_id WHERE o.id = $1`,
			r.InsideStructureID.String).Scan(&name)
		if name.Valid && name.String != "" {
			locationName = name.String
		}
	}

	// Destinations: distinct asset names with door_offset set (walkable-into).
	// Filtered to assets with at least one placed instance so the LLM doesn't
	// pick a destination that has no actual structure.
	var destinations []string
	rows, _ := app.DB.Query(ctx,
		`SELECT DISTINCT a.name
		 FROM asset a
		 JOIN village_object o ON o.asset_id = a.id
		 WHERE a.door_offset_x IS NOT NULL AND a.door_offset_y IS NOT NULL
		 ORDER BY a.name`)
	if rows != nil {
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err == nil && n != "" {
				destinations = append(destinations, n)
			}
		}
		rows.Close()
	}

	timeStr := hourStart.Format("Monday 15:04")
	dest := "(none catalogued)"
	if len(destinations) > 0 {
		dest = strings.Join(destinations, ", ")
	}

	perception := fmt.Sprintf(
		"You are %s. You are at %s. The time is %s.\n\n"+
			"Available destinations: %s.\n\n"+
			"Decide what to do this hour. You may use look_around first to see who is here, "+
			"then commit with move_to (walk to a destination), speak (say something), or done (rest).",
		r.DisplayName, locationName, timeStr, dest,
	)
	return perception, locationName
}

// resolveObservationTool runs an observation tool against engine state and
// returns the text the LLM sees as the tool result. M6.3 implements only
// look_around; recall and assess_person are M6.4+ work.
func (app *App) resolveObservationTool(ctx context.Context, r *agentNPCRow, tc *agentToolCall, locationName string) string {
	switch tc.Name {
	case "look_around":
		return app.resolveLookAround(ctx, r, locationName)
	}
	return fmt.Sprintf("[Unknown observation tool: %s]", tc.Name)
}

// resolveLookAround reports who else shares the NPC's current location.
// Inside-structure NPCs see other NPCs in the same structure; outside NPCs
// see no one specific (we'd need a position-radius query for the open map,
// deferred to a future tick).
func (app *App) resolveLookAround(ctx context.Context, r *agentNPCRow, locationName string) string {
	if !r.InsideStructureID.Valid {
		return fmt.Sprintf("You are out in %s. No one specific is at hand.", locationName)
	}
	rows, err := app.DB.Query(ctx,
		`SELECT display_name FROM npc
		 WHERE inside_structure_id = $1 AND id != $2`,
		r.InsideStructureID.String, r.ID)
	if err != nil {
		return fmt.Sprintf("You are at %s. (Engine error querying who is here.)", locationName)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("You are at %s. No one else is here.", locationName)
	}
	return fmt.Sprintf("You are at %s. Also here: %s.", locationName, strings.Join(names, ", "))
}

// executeAgentCommit translates the LLM's chosen commit tool into engine
// state changes and writes an agent_action_log row. Failures during
// execution still write a row with result='failed' so the audit trail
// captures every decision attempt.
func (app *App) executeAgentCommit(ctx context.Context, r *agentNPCRow, tc *agentToolCall) {
	payload, _ := json.Marshal(tc.Input)
	result := "ok"
	errStr := ""

	switch tc.Name {
	case "move_to":
		dest, _ := tc.Input["destination"].(string)
		if dest == "" {
			result, errStr = "rejected", "missing destination"
			break
		}
		if err := app.executeAgentMoveTo(ctx, r, dest); err != nil {
			result, errStr = "failed", err.Error()
		}

	case "speak":
		text, _ := tc.Input["text"].(string)
		if text == "" {
			result, errStr = "rejected", "empty text"
			break
		}
		// Speech is instant — no override needed. The Hub broadcast lets
		// any listening clients render the speech bubble in real time.
		// Engine log is the visible-to-humans record until the Godot client
		// gets an npc_spoke handler (see tasks/pending/salem-speech-bubble-ui).
		log.Printf("npc_spoke: %s says %q", r.DisplayName, text)
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_spoke",
			Data: map[string]interface{}{
				"npc_id": r.ID,
				"name":   r.DisplayName,
				"text":   text,
				"at":     time.Now().UTC().Format(time.RFC3339),
			},
		})

	case "done":
		// No state change. Audit row preserves the decision.

	default:
		result, errStr = "rejected", fmt.Sprintf("unknown commit tool: %s", tc.Name)
	}

	// Write audit row. Errors here are logged but don't propagate — the
	// commit already happened (or already failed); the audit row is a
	// best-effort record.
	_, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log (npc_id, source, action_type, payload, result, error)
		 VALUES ($1, 'agent', $2, $3, $4, NULLIF($5, ''))`,
		r.ID, tc.Name, payload, result, errStr,
	)
	if err != nil {
		log.Printf("agent-tick: audit insert %s/%s: %v", r.DisplayName, tc.Name, err)
	}
}

// executeAgentMoveTo finds the destination structure by asset name and
// dispatches a walk. Sets agent_override_until to a fixed 30-minute window
// covering the walk + arrival, and forward-stamps last_shift_tick_at to
// the same timestamp so the existing scheduler doesn't snap the NPC back
// to a missed worker boundary when override expires.
//
// Match strategy: case-insensitive prefix on asset.name. If multiple match,
// pick the one nearest the NPC's current position. Returns an error if no
// match — surfaces as "failed" in the audit log.
func (app *App) executeAgentMoveTo(ctx context.Context, r *agentNPCRow, dest string) error {
	row := app.DB.QueryRow(ctx,
		`SELECT o.id,
		        COALESCE(o.x + a.door_offset_x * 32.0, o.x),
		        COALESCE(o.y + a.door_offset_y * 32.0, o.y)
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 WHERE a.name ILIKE $1 || '%'
		   AND a.door_offset_x IS NOT NULL
		 ORDER BY (o.x - $2) * (o.x - $2) + (o.y - $3) * (o.y - $3)
		 LIMIT 1`,
		dest, r.CurrentX, r.CurrentY)
	var structureID string
	var doorX, doorY float64
	if err := row.Scan(&structureID, &doorX, &doorY); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("no structure matches %q", dest)
		}
		return err
	}

	npc := &behaviorNPC{ID: r.ID, CurX: r.CurrentX, CurY: r.CurrentY}
	app.interpolateCurrentPos(npc)
	if err := app.startReturnWalk(ctx, npc, doorX, doorY, structureID, "agent-move"); err != nil {
		return fmt.Errorf("startReturnWalk: %w", err)
	}

	// Conservative 30-minute override — covers any walk within the village
	// at typical walking speed. A future refinement can compute from the
	// pathfinder's expected duration.
	overrideUntil := time.Now().Add(30 * time.Minute)
	if _, err := app.DB.Exec(ctx,
		`UPDATE npc SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
		r.ID, overrideUntil,
	); err != nil {
		// Walk is already underway — log but don't unwind.
		log.Printf("agent-tick: stamp override %s: %v", r.DisplayName, err)
	}
	return nil
}
