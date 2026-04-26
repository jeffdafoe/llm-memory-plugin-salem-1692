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
	"math/rand"
	"strings"
	"time"
)

const (
	agentTickBudget          = 6
	agentTickActiveStartHour = 6
	agentTickActiveEndHour   = 21 // exclusive — last active hour is 20:00-21:00
)

// agentNPCRow bundles everything the harness loop needs for one NPC.
//
// WorkLabel / HomeLabel are the thematic display labels for the NPC's work
// and home structures — village_object.display_name when set, otherwise the
// raw asset.name. They drive the identity-recap section of the perception so
// the model can ground "your work is the Tavern" rather than guessing from
// an undifferentiated destination list.
type agentNPCRow struct {
	ID                  string
	DisplayName         string
	LLMMemoryAgent      string
	APIKey              string
	Role                string
	InsideStructureID   sql.NullString
	CurrentX            float64
	CurrentY            float64
	HomeStructureID     sql.NullString
	WorkStructureID     sql.NullString
	LastAgentTickAt     sql.NullTime
	WorkLabel           sql.NullString
	HomeLabel           sql.NullString
	ScheduleStartMinute sql.NullInt32
	ScheduleEndMinute   sql.NullInt32
}

// dispatchAgentTicks is the per-server-tick entry. Loaded NPCs are processed
// sequentially within this server tick — the harness loop blocks on HTTP, so
// running all NPCs in parallel within one tick risks bursting the upstream
// LLM provider. Sequential keeps things bounded and easier to reason about.
//
// Future co-location grouping (M6.5) will run NPCs at the same location in
// sequential rounds so they can react to each other's speech.
func (app *App) dispatchAgentTicks(ctx context.Context) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("agent-tick: load config: %v", err)
		return
	}
	// Use world timezone — Salem's "game time" is the configured world
	// timezone (America/New_York by default), not the server's UTC clock.
	// Same pattern the worker scheduler uses.
	now := time.Now().In(cfg.Location)
	if now.Hour() < agentTickActiveStartHour || now.Hour() >= agentTickActiveEndHour {
		// Outside active hours — skip entirely. Avoids burning budget while
		// the village sleeps. Game-hour boundaries during the night will be
		// resumed at the first eligible morning tick.
		return
	}

	// Dawn/dusk in minutes-of-day for the schedule-note inheritance in
	// buildAgentPerception. Same parse the worker scheduler does — a worker
	// NPC with NULL schedule columns inherits this pair as their effective
	// shift, so the perception should surface those values for the LLM.
	dawnMin, duskMin := 6*60, 18*60
	if dh, dm, err := parseHM(cfg.DawnTime); err == nil {
		dawnMin = dh*60 + dm
	}
	if dh, dm, err := parseHM(cfg.DuskTime); err == nil {
		duskMin = dh*60 + dm
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
		app.runAgentTick(ctx, r, hourStart, dawnMin, duskMin)
	}
}

// loadAgentNPCRows pulls every NPC with a linked virtual agent + a configured
// API key. NPCs without a VA aren't agentized and run on the existing
// scheduler only.
//
// LEFT JOINs to village_object/asset for work and home resolve the NPC's
// thematic labels in one round trip. COALESCE(o.display_name, a.name)
// matches the destination-list and move_to resolver so the model sees one
// consistent name for each place across the perception.
func (app *App) loadAgentNPCRows(ctx context.Context) ([]agentNPCRow, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT n.id, n.display_name, n.llm_memory_agent,
		        va.llm_memory_api_key,
		        va.role,
		        n.inside_structure_id, n.current_x, n.current_y,
		        n.home_structure_id, n.work_structure_id,
		        n.last_agent_tick_at,
		        n.schedule_start_minute, n.schedule_end_minute,
		        COALESCE(wo.display_name, wa.name) AS work_label,
		        COALESCE(ho.display_name, ha.name) AS home_label
		 FROM npc n
		 JOIN village_agent va ON va.llm_memory_agent = n.llm_memory_agent
		 LEFT JOIN village_object wo ON wo.id = n.work_structure_id
		 LEFT JOIN asset wa ON wa.id = wo.asset_id
		 LEFT JOIN village_object ho ON ho.id = n.home_structure_id
		 LEFT JOIN asset ha ON ha.id = ho.asset_id
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
			&r.Role,
			&r.InsideStructureID, &r.CurrentX, &r.CurrentY,
			&r.HomeStructureID, &r.WorkStructureID,
			&r.LastAgentTickAt,
			&r.ScheduleStartMinute, &r.ScheduleEndMinute,
			&r.WorkLabel, &r.HomeLabel); err != nil {
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
//
// Transport: each iteration is one wait=true chat_send to the NPC. The chat
// history on the API side IS the conversation accumulator — the engine no
// longer holds a local messages[]. Iter 0 sends the perception; subsequent
// iterations send the tool-result text keyed to the prior assistant
// tool_call.id. handleDirectChat rebuilds OpenAI-shape messages[] from the
// stored chat rows on every call.
//
// Multi-tool turns: the harness only resolves ONE observation tool per
// iteration (or executes the first commit tool if any). Multi-tool replies
// are rare in practice; if they happen, the model gets another iteration to
// re-request anything we skipped. This keeps the chat-message shape clean
// (one tool_call_id per row) without bundling logic on the engine side.
func (app *App) runAgentTick(ctx context.Context, r *agentNPCRow, hourStart time.Time, dawnMin, duskMin int) {
	perception, locationName := app.buildAgentPerception(ctx, r, hourStart, dawnMin, duskMin)
	tools := agentToolSpec()

	currentMessage := perception
	currentToolCallID := ""

	var commitCall *agentToolCall
	for iter := 0; iter < agentTickBudget; iter++ {
		reply, err := app.npcChatClient.sendChat(ctx, r.LLMMemoryAgent, currentMessage, currentToolCallID, tools)
		if err != nil {
			log.Printf("agent-tick %s iter=%d: %v", r.DisplayName, iter, err)
			return
		}

		if len(reply.ToolCalls) == 0 {
			// No tool — synthesize a speak with the reply text. Same fallback
			// as the old /agent/tick path; the engine always commits so the
			// audit trail stays intact.
			commitCall = &agentToolCall{
				ID:    fmt.Sprintf("synthetic-text-%d", iter),
				Name:  "speak",
				Input: map[string]interface{}{"text": reply.Text},
			}
			break
		}

		// Prefer commit tool if any (matches prior precedence). Otherwise
		// take the first observation tool. Anything beyond [0] is dropped —
		// see comment on the function for why.
		var observation *agentToolCall
		for i := range reply.ToolCalls {
			tc := &reply.ToolCalls[i]
			if isCommitTool(tc.Name) {
				commitCall = tc
				break
			}
			if observation == nil {
				observation = tc
			}
		}
		if commitCall != nil {
			break
		}
		if observation == nil {
			// All tool_calls unrecognized as commit or observation. Defensive
			// — agentToolSpec is fixed — but treat as no-op done so the tick
			// still terminates with an audit row.
			commitCall = &agentToolCall{
				ID:    fmt.Sprintf("synthetic-unknown-%d", iter),
				Name:  "done",
				Input: map[string]interface{}{},
			}
			break
		}

		toolResult := app.resolveObservationTool(ctx, r, observation, locationName)
		currentMessage = toolResult
		currentToolCallID = observation.ID
	}

	// Budget exhausted without a commit — synthesize "done" so we always
	// terminate cleanly. Costs the NPC one wasted hour; rare.
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
//
// `chore` (ZBBS-075) lets the model express a brief errand by category
// (outhouse, well, shop, ...) without naming a specific placement. The
// engine resolves to the nearest tagged placement and walks the NPC to
// its loiter point. Same per-tick commit semantics as move_to: one chore
// per tick. Multi-step plans ("outhouse, then the well") play out across
// successive ticks.
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
			Description: "Walk to a named structure or person's home in Salem. The engine handles pathfinding.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"destination": map[string]interface{}{
						"type":        "string",
						"description": "Structure name ('Smithy', 'Tavern', 'Home') or a neighbor's home ('Goody Smith's home').",
					},
				},
				"required": []string{"destination"},
			},
		},
		{
			Name:        "chore",
			Description: "Run a quick errand by category. Engine picks the nearest matching place and walks you to it.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type": map[string]interface{}{
						"type":        "string",
						"description": "Category of place: outhouse, well, shop, tavern, smithy, meeting-house.",
					},
				},
				"required": []string{"type"},
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
	case "move_to", "chore", "speak", "done":
		return true
	}
	return false
}

// buildAgentPerception constructs the user-message text the LLM sees on the
// first iteration. Sections (in order):
//
//   1. Identity recap — name, role, home and work labels. When home and
//      work are the same placement (e.g. tavernkeeper living above the
//      tavern), the two lines collapse to "Your home and work: <label>."
//   2. Schedule note — usual work hours and whether the current time falls
//      inside or outside that window. Omitted when the NPC has no schedule.
//   3. Right-now — current location and timestamp.
//   4. Destinations — categorical destinations (tagged placements:
//      taverns, smithies, shops, meeting houses, wells, outhouses) plus
//      occupant-named residences (anyone else's home), excluding this
//      NPC's own home and work.
//   5. Decision prompt.
//
// Categorical filter (ZBBS-075): "Other places nearby" pulls from
// village_object_tag where the tag is in the engine's category set
// (categoryObjectTags map). Decorative placements without a category tag
// never appear. Residences are surfaced separately via the home_structure_id
// linkage from npc — that way a placement is identifiable as "Goody Smith's
// house" even when the placement itself has no display_name.
//
// Returns (perception, currentLocationName). The location name is also
// passed to the look_around resolver so it doesn't have to re-query.
func (app *App) buildAgentPerception(ctx context.Context, r *agentNPCRow, hourStart time.Time, dawnMin, duskMin int) (string, string) {
	locationName := "the open village"
	if r.InsideStructureID.Valid {
		var name sql.NullString
		_ = app.DB.QueryRow(ctx,
			`SELECT COALESCE(o.display_name, a.name)
			 FROM village_object o JOIN asset a ON a.id = o.asset_id
			 WHERE o.id = $1`,
			r.InsideStructureID.String).Scan(&name)
		if name.Valid && name.String != "" {
			locationName = name.String
		}
	}

	homeLabel := ""
	if r.HomeLabel.Valid {
		homeLabel = r.HomeLabel.String
	}
	workLabel := ""
	if r.WorkLabel.Valid {
		workLabel = r.WorkLabel.String
	}
	homeIsWork := r.HomeStructureID.Valid && r.WorkStructureID.Valid &&
		r.HomeStructureID.String == r.WorkStructureID.String

	// Categorical destinations — placements tagged with a role from
	// categoryObjectTags. The IN clause is parameterized by enumerating the
	// allowed list; PG handles the array literal cleanly. Excludes the NPC's
	// own home and work IDs (those are stated explicitly above).
	excludeHome, excludeWork := "", ""
	if r.HomeStructureID.Valid {
		excludeHome = r.HomeStructureID.String
	}
	if r.WorkStructureID.Valid {
		excludeWork = r.WorkStructureID.String
	}
	categoryTags := categoryTagList()

	var others []string
	categoryRows, _ := app.DB.Query(ctx,
		`SELECT label
		 FROM (
		     SELECT COALESCE(o.display_name, a.name) AS label,
		            MIN((o.x - $1) * (o.x - $1) + (o.y - $2) * (o.y - $2)) AS min_d
		     FROM village_object o
		     JOIN asset a ON a.id = o.asset_id
		     JOIN village_object_tag vot ON vot.object_id = o.id
		     WHERE vot.tag = ANY($3)
		       AND ($4 = '' OR o.id::text != $4)
		       AND ($5 = '' OR o.id::text != $5)
		     GROUP BY COALESCE(o.display_name, a.name)
		 ) labelled
		 WHERE label IS NOT NULL AND label != ''
		 ORDER BY min_d ASC
		 LIMIT 8`,
		r.CurrentX, r.CurrentY, categoryTags, excludeHome, excludeWork)
	if categoryRows != nil {
		for categoryRows.Next() {
			var n string
			if err := categoryRows.Scan(&n); err == nil && n != "" {
				others = append(others, n)
			}
		}
		categoryRows.Close()
	}

	// Residences — occupant-named homes belonging to other NPCs. Sourced
	// from npc.home_structure_id (the same linkage used for "Your home" so
	// the labels stay consistent). Capped at 8 nearest. Acquaintance gating
	// (M6.4.5) will later swap unfamiliar names for generic descriptors.
	var residences []string
	residenceRows, _ := app.DB.Query(ctx,
		`SELECT n.display_name
		 FROM npc n
		 JOIN village_object o ON o.id = n.home_structure_id
		 WHERE n.id != $1
		   AND n.home_structure_id IS NOT NULL
		   AND ($2 = '' OR n.home_structure_id::text != $2)
		   AND ($3 = '' OR n.home_structure_id::text != $3)
		 ORDER BY (o.x - $4) * (o.x - $4) + (o.y - $5) * (o.y - $5)
		 LIMIT 8`,
		r.ID, excludeHome, excludeWork, r.CurrentX, r.CurrentY)
	if residenceRows != nil {
		for residenceRows.Next() {
			var name string
			if err := residenceRows.Scan(&name); err == nil && name != "" {
				residences = append(residences, fmt.Sprintf("%s's home", name))
			}
		}
		residenceRows.Close()
	}

	var sections []string

	// 1. Identity recap. Collapse home/work when same placement.
	identityLines := []string{fmt.Sprintf("You are %s the %s.", r.DisplayName, r.Role)}
	if homeIsWork && homeLabel != "" {
		identityLines = append(identityLines, fmt.Sprintf("Your home and work: %s.", homeLabel))
	} else {
		if homeLabel != "" {
			identityLines = append(identityLines, fmt.Sprintf("Your home is %s.", homeLabel))
		}
		if workLabel != "" {
			identityLines = append(identityLines, fmt.Sprintf("Your work is %s.", workLabel))
		}
	}
	sections = append(sections, strings.Join(identityLines, "\n"))

	// 2. Schedule note. Mirror npc_scheduler.go's resolveWorkerWindow:
	// per-NPC start/end win when both are set; both-NULL falls back to the
	// world's dawn/dusk (the same shift the worker scheduler applies at
	// runtime). Skipped only when the NPC has no work assignment at all
	// (workLabel empty) — there's no "shift" to talk about.
	if workLabel != "" {
		var startMin, endMin int
		if r.ScheduleStartMinute.Valid && r.ScheduleEndMinute.Valid {
			startMin = int(r.ScheduleStartMinute.Int32)
			endMin = int(r.ScheduleEndMinute.Int32)
		} else {
			startMin = dawnMin
			endMin = duskMin
		}
		nowMin := hourStart.Hour()*60 + hourStart.Minute()
		var onShift bool
		if startMin <= endMin {
			onShift = nowMin >= startMin && nowMin < endMin
		} else {
			// Wraps midnight (e.g. 16:00 to 03:00 next day).
			onShift = nowMin >= startMin || nowMin < endMin
		}
		shiftWord := "off shift"
		if onShift {
			shiftWord = "on shift"
		}
		sections = append(sections, fmt.Sprintf(
			"Your usual hours at your work are %s–%s. The time is now %s — you would currently be %s.",
			formatHHMM(startMin), formatHHMM(endMin), hourStart.Format("15:04"), shiftWord,
		))
	}

	// 3. Right-now.
	sections = append(sections, fmt.Sprintf(
		"You are at %s. The time is %s.",
		locationName, hourStart.Format("Monday 15:04"),
	))

	// 4. Destinations. Categorical first, then occupant-named residences.
	var destLines []string
	if len(others) > 0 {
		destLines = append(destLines, fmt.Sprintf("Other places nearby: %s.", strings.Join(others, ", ")))
	}
	if len(residences) > 0 {
		destLines = append(destLines, fmt.Sprintf("Neighbors' homes: %s.", strings.Join(residences, ", ")))
	}
	if len(destLines) == 0 {
		destLines = append(destLines, "No other catalogued destinations nearby.")
	}
	sections = append(sections, strings.Join(destLines, "\n"))

	// 5. Decision prompt.
	sections = append(sections, "Decide what to do this hour. You may use look_around first to see who is here, "+
		"then commit with move_to (walk to a named place), chore (run a quick errand by category), "+
		"speak (say something), or done (rest).")

	return strings.Join(sections, "\n\n"), locationName
}

// categoryTagList returns the categoryObjectTags map as a sorted []string
// suitable for a PG `= ANY($1)` parameter. Materialized per call (small
// list, infrequent calls) rather than cached, so the migration story stays
// simple — adding a tag to the map is the only change required.
func categoryTagList() []string {
	out := make([]string, 0, len(categoryObjectTags))
	for tag := range categoryObjectTags {
		out = append(out, tag)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// formatHHMM renders a minutes-since-midnight value as "HH:MM".
func formatHHMM(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
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

	case "chore":
		category, _ := tc.Input["type"].(string)
		if category == "" {
			result, errStr = "rejected", "missing chore type"
			break
		}
		if err := app.executeAgentChore(ctx, r, category); err != nil {
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

// executeAgentMoveTo finds the destination structure and dispatches a walk.
// Sets agent_override_until to a fixed 30-minute window covering the walk +
// arrival, and forward-stamps last_shift_tick_at to the same timestamp so
// the existing scheduler doesn't snap the NPC back to a missed worker
// boundary when override expires.
//
// Resolution strategy (in order):
//
//  1. Occupant-named home — "Goody Smith's home" / "Goody Smith's house" →
//     resolve via npc.home_structure_id of the matching name. The owner's
//     home placement provides x/y; the visitor walks to its loiter point
//     (per the visitor rule below).
//  2. Structure label — case-insensitive prefix on
//     COALESCE(display_name, name) of the village_object/asset. Mirrors the
//     "Other places nearby" labels in the perception. If multiple match,
//     pick the placement closest to the NPC.
//
// Walk target (in order of preference):
//
//  1. If the destination IS this NPC's home or work placement, use the
//     asset's door_offset — owners enter their own buildings the same way
//     they always have (existing scheduler/behavior code uses this path).
//  2. Else, the placement has loiter_offset_x/y set: walk to that.
//  3. Else, fall back to the asset's door_offset (legacy behavior).
//  4. Else, walk to the placement's anchor (final fallback).
//
// The door-offset path is preserved intact for own-home/work moves so
// nothing about scheduled worker arrivals or social-leave home-returns
// changes. Visitor moves (the new agent-driven case) prefer loiter.
func (app *App) executeAgentMoveTo(ctx context.Context, r *agentNPCRow, dest string) error {
	structureID, walkX, walkY, err := app.resolveMoveDestination(ctx, r, dest)
	if err != nil {
		return err
	}

	npc := &behaviorNPC{ID: r.ID, CurX: r.CurrentX, CurY: r.CurrentY}
	app.interpolateCurrentPos(npc)
	if err := app.startReturnWalk(ctx, npc, walkX, walkY, structureID, "agent-move"); err != nil {
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

// resolveMoveDestination handles the dest-string → (structureID, walk_x,
// walk_y) lookup for both move_to and the look-around-arrival path it
// reuses. See executeAgentMoveTo's comment for the resolution order.
func (app *App) resolveMoveDestination(ctx context.Context, r *agentNPCRow, dest string) (string, float64, float64, error) {
	// 1. Occupant-named home? Strip trailing "'s home" / "'s house" and
	// look up the owner's home_structure_id. We accept either suffix
	// because the perception emits "X's home" but the LLM may rephrase.
	if owner, ok := stripOccupantHomeSuffix(dest); ok {
		row := app.DB.QueryRow(ctx,
			`SELECT n.home_structure_id::text, o.id::text, o.x, o.y,
			        o.loiter_offset_x, o.loiter_offset_y,
			        a.door_offset_x, a.door_offset_y
			 FROM npc n
			 JOIN village_object o ON o.id = n.home_structure_id
			 JOIN asset a ON a.id = o.asset_id
			 WHERE n.display_name ILIKE $1
			   AND n.home_structure_id IS NOT NULL
			 LIMIT 1`,
			owner)
		var hsID, oID string
		var ox, oy float64
		var loiterX, loiterY sql.NullInt32
		var doorX, doorY sql.NullInt32
		if err := row.Scan(&hsID, &oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY); err == nil {
			wx, wy := pickWalkTarget(r, hsID, ox, oy, loiterX, loiterY, doorX, doorY)
			return oID, wx, wy, nil
		} else if err != sql.ErrNoRows {
			return "", 0, 0, err
		}
		// fall through if no match — maybe it was a building name with
		// "'s home" coincidence, try the regular path
	}

	// 2. Structure label match. Closest first.
	row := app.DB.QueryRow(ctx,
		`SELECT o.id::text, o.x, o.y,
		        o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 WHERE COALESCE(o.display_name, a.name) ILIKE $1 || '%'
		 ORDER BY (o.x - $2) * (o.x - $2) + (o.y - $3) * (o.y - $3)
		 LIMIT 1`,
		dest, r.CurrentX, r.CurrentY)
	var oID string
	var ox, oy float64
	var loiterX, loiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	if err := row.Scan(&oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY); err != nil {
		if err == sql.ErrNoRows {
			return "", 0, 0, fmt.Errorf("no structure matches %q", dest)
		}
		return "", 0, 0, err
	}
	wx, wy := pickWalkTarget(r, oID, ox, oy, loiterX, loiterY, doorX, doorY)
	return oID, wx, wy, nil
}

// pickWalkTarget chooses the walk-to coordinates for an agent-initiated
// move. Owners (NPC's own home or work) walk to door_offset so the
// existing arrive/inside/stand_offset rendering chain stays intact.
// Visitors prefer loiter_offset, falling back to door_offset, then anchor.
//
// All offsets are tile-unit ints; multiplied by tileSize=32.0 to get the
// pixel coordinate the walk dispatcher expects.
//
// Visitor jitter (ZBBS-075): when several NPCs walk to the same loiter
// point in close succession (e.g. four villagers heading to the well),
// landing all of them on the same pixel is visually confusing — they
// stack and look like one sprite. We add a small ±half-tile random
// offset to the loiter target so they cluster naturally instead. Owners
// (door path) get no jitter — their arrive/inside flow assumes the door
// tile exactly. Same for the anchor fallback.
func pickWalkTarget(r *agentNPCRow, structureID string, ox, oy float64,
	loiterX, loiterY, doorX, doorY sql.NullInt32) (float64, float64) {
	const tileSize = 32.0
	isOwner := (r.HomeStructureID.Valid && r.HomeStructureID.String == structureID) ||
		(r.WorkStructureID.Valid && r.WorkStructureID.String == structureID)

	if !isOwner && loiterX.Valid && loiterY.Valid {
		baseX := ox + float64(loiterX.Int32)*tileSize
		baseY := oy + float64(loiterY.Int32)*tileSize
		jx, jy := loiterJitter()
		return baseX + jx, baseY + jy
	}
	if doorX.Valid && doorY.Valid {
		return ox + float64(doorX.Int32)*tileSize, oy + float64(doorY.Int32)*tileSize
	}
	return ox, oy
}

// loiterJitter returns a small random offset in pixels for visitor walk
// targets so multiple NPCs heading to the same loiter point spread out
// naturally instead of stacking on one pixel. Range is roughly half a
// tile in each direction.
func loiterJitter() (float64, float64) {
	const jitterRange = 14.0 // pixels; half-tile-ish
	return (rand.Float64()*2 - 1) * jitterRange, (rand.Float64()*2 - 1) * jitterRange
}

// stripOccupantHomeSuffix detects strings like "Goody Smith's home" /
// "John Ellis's house" and returns the bare name. Returns ("", false) when
// there's no match. ILIKE is the lookup, but we strip in Go because PG
// regex would be heavier for the small payload.
func stripOccupantHomeSuffix(s string) (string, bool) {
	const homeSuffix = "'s home"
	const houseSuffix = "'s house"
	switch {
	case strings.HasSuffix(strings.ToLower(s), homeSuffix):
		return strings.TrimSpace(s[:len(s)-len(homeSuffix)]), true
	case strings.HasSuffix(strings.ToLower(s), houseSuffix):
		return strings.TrimSpace(s[:len(s)-len(houseSuffix)]), true
	}
	return "", false
}

// executeAgentChore resolves a category to the nearest tagged placement
// and walks the NPC to its loiter point. Same single-commit-per-tick
// semantics as move_to. The audit log records the requested category and
// the resolved placement so multi-tick behavior is reconstructable.
//
// Validation: the category must be in categoryObjectTags. Unknown
// categories return an error so the audit trail records the rejection
// rather than silently picking a wrong target.
func (app *App) executeAgentChore(ctx context.Context, r *agentNPCRow, category string) error {
	if !categoryObjectTags[category] {
		return fmt.Errorf("unknown chore category %q", category)
	}

	row := app.DB.QueryRow(ctx,
		`SELECT o.id::text, o.x, o.y,
		        o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 JOIN village_object_tag vot ON vot.object_id = o.id
		 WHERE vot.tag = $1
		 ORDER BY (o.x - $2) * (o.x - $2) + (o.y - $3) * (o.y - $3)
		 LIMIT 1`,
		category, r.CurrentX, r.CurrentY)
	var oID string
	var ox, oy float64
	var loiterX, loiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	if err := row.Scan(&oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("no placement tagged %q", category)
		}
		return err
	}
	wx, wy := pickWalkTarget(r, oID, ox, oy, loiterX, loiterY, doorX, doorY)

	npc := &behaviorNPC{ID: r.ID, CurX: r.CurrentX, CurY: r.CurrentY}
	app.interpolateCurrentPos(npc)
	if err := app.startReturnWalk(ctx, npc, wx, wy, oID, "agent-chore"); err != nil {
		return fmt.Errorf("startReturnWalk: %w", err)
	}

	overrideUntil := time.Now().Add(30 * time.Minute)
	if _, err := app.DB.Exec(ctx,
		`UPDATE npc SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
		r.ID, overrideUntil,
	); err != nil {
		log.Printf("agent-tick: stamp override %s: %v", r.DisplayName, err)
	}
	return nil
}
