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
//   3. If response carries observation tool_calls (recall),
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
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	agentTickBudget          = 6
	agentTickActiveStartHour = 6
	agentTickActiveEndHour   = 21 // exclusive — last active hour is 20:00-21:00
	// Top-K notes returned per recall. The API groups by (namespace,
	// source_file) so each row is one note.
	recallResultLimit = 5
	// Defensive cap on the query length the model can submit to recall.
	// The tool schema is just `string`, so a runaway model could otherwise
	// dump kilobytes of text into the embedding pipeline.
	recallQueryMaxChars = 500
	// Per-NPC tick offset window. The dispatcher staggers each NPC's
	// baseline tick within the game-hour using a deterministic hash of
	// their UUID — Ezekiel might fire at hour+7min, John at hour+23min,
	// etc. Spreads load on the LLM provider and avoids the artificial
	// "everyone decides at the same instant" feel.
	agentTickOffsetWindow = 3300 // seconds within an hour (55 min, leaves 5 min slack at end)
	// Minimum gap between any two ticks for the same NPC, regardless of
	// trigger source (baseline or event). Cost guard against tick storms
	// when several NPCs co-locate and react to each other.
	agentMinTickGap = 5 * time.Minute
)

// npcTickOffsetSeconds returns the per-NPC tick offset within the
// game-hour, deterministic from the NPC's UUID. SHA1 hash → first 4
// bytes as uint32 → mod agentTickOffsetWindow. Stable across restarts
// and migrations — no schema column needed.
func npcTickOffsetSeconds(npcID string) int {
	sum := sha1.Sum([]byte(npcID))
	val := binary.BigEndian.Uint32(sum[:4])
	return int(val % uint32(agentTickOffsetWindow))
}

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
	// Needs and currency (ZBBS-082). Loaded each dispatch so the perception
	// reflects post-tick state. Stale within one tick if pay fires mid-tick,
	// but the tick is one shot per hour so that's acceptable.
	Coins     int
	Hunger    int
	Thirst    int
	Tiredness int
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
	// Admin kill switch — when world_agent_ticks_paused is set, suppress
	// all agent-tick dispatch. Other schedulers (worker, social, lamplighter,
	// rotation) continue running so the village still moves and we can
	// observe deterministic behavior in isolation.
	if cfg.AgentTicksPaused {
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

	// Day-one chronicler design: NPCs go reactive-only (no autonomous
	// hourly baseline ticks). Reactive event-ticks (PC speech, NPC
	// arrival, NPC speech inside a cascade) continue to fire through
	// existing handlers. The npc_baseline_ticks_enabled setting is the
	// kill switch / safety valve — if reactive-only feels too sparse,
	// flip it on to restore the prior cadence.
	baselineEnabled := app.loadSetting(ctx, "npc_baseline_ticks_enabled", "false") == "true"
	if !baselineEnabled {
		return
	}

	hourStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	secondsIntoHour := int(now.Sub(hourStart).Seconds())
	for i := range rows {
		r := &rows[i]
		// Skip NPCs already ticked this hour. Stamp uses hourStart so we don't
		// double-fire when the dispatcher runs multiple times per hour.
		if r.LastAgentTickAt.Valid && !r.LastAgentTickAt.Time.Before(hourStart) {
			continue
		}
		// Staggered cadence: each NPC has a deterministic offset within the
		// hour. They tick when wall-clock time crosses (hourStart + offset),
		// not at the boundary itself. Spreads load and avoids
		// everyone-decides-at-noon synchronization. The first server tick
		// of any hour where wall-clock has already passed offset will catch
		// up — no cumulative drift.
		if secondsIntoHour < npcTickOffsetSeconds(r.ID) {
			continue
		}
		// Baseline hourly ticks are their own one-NPC scenes — they
		// aren't part of a speech/arrival cascade. If the NPC speaks
		// during the tick, the speak commit will fan out via
		// triggerCoLocatedTicks with the SAME sceneID and the scene
		// naturally grows; if not, the scene is just this NPC's
		// perception + tool calls + terminal commit.
		app.runAgentTick(ctx, r, hourStart, dawnMin, duskMin, newUUIDv7())
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
		        n.llm_memory_api_key,
		        n.role,
		        n.inside_structure_id, n.current_x, n.current_y,
		        n.home_structure_id, n.work_structure_id,
		        n.last_agent_tick_at,
		        n.schedule_start_minute, n.schedule_end_minute,
		        COALESCE(wo.display_name, wa.name) AS work_label,
		        COALESCE(ho.display_name, ha.name) AS home_label,
		        n.coins, n.hunger, n.thirst, n.tiredness
		 FROM actor n
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
			&r.WorkLabel, &r.HomeLabel,
		&r.Coins, &r.Hunger, &r.Thirst, &r.Tiredness); err != nil {
			log.Printf("agent-tick: scan: %v", err)
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// runAgentTick is the harness loop for one NPC. Stamps last_agent_tick_at
// twice: once at the start (to close the cost-guard race window — see
// below) and once at the end (refresh, so a partial failure doesn't burn
// the hour and the next tick retries cleanly).
//
// Cost-guard race: triggerImmediateTick reads last_agent_tick_at to gate
// NPC-triggered event ticks. If we only stamp at the end of runAgentTick,
// any speech/movement event that fires during this NPC's in-flight loop
// reads a stale stamp (from the previous hour) and the cost guard
// passes, allowing a concurrent runAgentTick goroutine for the same NPC.
// Stamping at the start makes the cost guard see this tick's start time
// while the loop is still running, blocking concurrent re-entry.
//
// Transport: each iteration is one wait=true chat_send to the NPC. The chat
// history on the API side IS the conversation accumulator — the engine no
// longer holds a local messages[]. Iter 0 sends the perception; subsequent
// iterations send the tool-result text keyed to the prior assistant
// tool_call.id. handleDirectChat rebuilds OpenAI-shape messages[] from the
// stored chat rows on every call.
//
// Multi-tool turns: the harness resolves ONE tool per iteration. Speak
// is non-terminal — after a speak fires, the loop continues so the model
// can follow through with a move/chore in the same tick (e.g. "I'll go
// close the stall" → move_to). Move/chore/done terminate the loop:
// move/chore put a walk in flight, done means no further action. Multi-
// tool replies in a single iteration are rare; if they happen, anything
// past [0] is dropped and the model gets another iteration.
//
// sceneID (MEM-121) is the cascade UUID this tick belongs to. Minted at
// the cascade origin (PC speak / NPC arrival / baseline tick) and
// inherited unchanged by every reactive tick the cascade fires off. It
// rides on every sendChat the harness emits and is passed forward into
// executeAgentCommit, which forwards into triggerCoLocatedTicks for the
// speak case so nested speech reactions stay in the same scene.
func (app *App) runAgentTick(ctx context.Context, r *agentNPCRow, hourStart time.Time, dawnMin, duskMin int, sceneID string) {
	app.stampAgentTick(ctx, r, hourStart)
	// Defensive — every sim tick should have a sceneID. The three
	// minting points (baseline dispatcher, PC-speak handler, arrival
	// hook) all pass newUUIDv7(). An empty value here means a future
	// call site forgot to mint or newUUIDv7 returned empty; the API
	// would silently accept the row with NULL scene_id and we'd lose
	// grouping with no obvious signal. Log so the bug is visible
	// without auto-minting (which would split a cascade into a fresh
	// scene mid-flight and hide the upstream issue).
	if sceneID == "" {
		log.Printf("agent-tick %s: missing sceneID — every sim tick should carry one", r.DisplayName)
	}
	perception, locationName := app.buildAgentPerception(ctx, r, hourStart, dawnMin, duskMin)
	tools := agentToolSpec()

	currentMessage := perception
	currentToolCallID := ""

	var commitCall *agentToolCall
	for iter := 0; iter < agentTickBudget; iter++ {
		reply, err := app.npcChatClient.sendChat(ctx, r.LLMMemoryAgent, currentMessage, currentToolCallID, sceneID, tools)
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

		// Pick the first tool_call to act on. Terminal commits (move_to,
		// chore, done) win over pay/speak/act. Pay runs before speak so a
		// "pay-and-thank-you" sequence unfolds as pay first, speak next
		// iteration. Pay/speak/act all execute inline and let the loop
		// continue — none ends the turn. The harness lets the model follow
		// up with movement or another speech turn within the per-tick budget.
		var terminalCall, payCall, speakCall, actCall, observation *agentToolCall
		for i := range reply.ToolCalls {
			tc := &reply.ToolCalls[i]
			switch tc.Name {
			case "move_to", "chore", "done":
				if terminalCall == nil {
					terminalCall = tc
				}
			case "pay":
				if payCall == nil {
					payCall = tc
				}
			case "speak":
				if speakCall == nil {
					speakCall = tc
				}
			case "act":
				if actCall == nil {
					actCall = tc
				}
			default:
				if observation == nil {
					observation = tc
				}
			}
		}

		if terminalCall != nil {
			commitCall = terminalCall
			break
		}

		if payCall != nil {
			// Pay executes inline like speak — state change happens (coins
			// move, hunger/thirst drop if matched), but the loop continues
			// so the model can follow through with thanks or a move. The
			// continuation message reflects the actual result so the model
			// knows whether the transaction landed: a "rejected" pay (bad
			// recipient, insufficient coins, self-payment) gets surfaced
			// verbatim so the model can correct itself rather than thanking
			// someone for ale they didn't actually receive.
			result, errStr := app.executeAgentCommit(ctx, r, payCall, sceneID)
			if result == "ok" {
				currentMessage = "[OK] You paid. Continue your turn — you may speak, move, or call done."
			} else {
				currentMessage = fmt.Sprintf("[Pay %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = payCall.ID
			continue
		}

		if speakCall != nil {
			// Execute the speak inline (audit + WS broadcast + co-located
			// event-ticks) but DON'T terminate the loop. The model gets to
			// follow through with a move/chore/done on the next iteration.
			// The tool_result reminds the model that action is still on
			// the table — without it, models tend to default to "done"
			// after speaking ("I responded, my turn's over"). Non-directive
			// nudge: doesn't name a specific action, just affirms agency.
			_, _ = app.executeAgentCommit(ctx, r, speakCall, sceneID)
			currentMessage = "[OK] You spoke. Continue your turn — you may move or run a chore now, or call done if you're staying put."
			currentToolCallID = speakCall.ID
			continue
		}

		if actCall != nil {
			// act is non-terminal like speak — the model often pairs a
			// physical action with a follow-up speech ("served stew" then
			// "here you are, mind the heat"). Same [OK] nudge so the
			// model knows the turn isn't over.
			_, _ = app.executeAgentCommit(ctx, r, actCall, sceneID)
			currentMessage = "[OK] You did that. Continue your turn — you may speak, move, or call done."
			currentToolCallID = actCall.ID
			continue
		}

		if observation == nil {
			// All tool_calls unrecognized. Defensive — agentToolSpec is
			// fixed — but treat as no-op done so the tick still terminates
			// with an audit row.
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

	// Budget exhausted without a terminal commit — synthesize "done" so we
	// always terminate cleanly. Note: any speak commits during the loop
	// have already been executed; this only writes the audit row for the
	// final terminal action.
	if commitCall == nil {
		commitCall = &agentToolCall{
			ID:    "synthetic-budget-exhausted",
			Name:  "done",
			Input: map[string]interface{}{},
		}
	}

	_, _ = app.executeAgentCommit(ctx, r, commitCall, sceneID)
	app.stampAgentTick(ctx, r, hourStart)
}

// stampAgentTick records that we've ticked this NPC. Stamps to
// time.Now() (NOT hourStart) so the agentMinTickGap cost guard
// reads an accurate elapsed time. The baseline-gate check
// (`!last.Before(hourStart)` in dispatchAgentTicks) still works:
// any time.Now() within the current hour is >= hourStart.
//
// Earlier versions stamped to hourStart, which made the cost guard
// useless — `time.Since(hourStart)` was always near the full hour,
// so event-tick triggers cascaded freely. Tick storms ensued when
// two co-located agents reacted to each other's speech.
func (app *App) stampAgentTick(ctx context.Context, r *agentNPCRow, hourStart time.Time) {
	_ = hourStart // kept for signature stability; baseline gate uses Before()
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET last_agent_tick_at = $2 WHERE id = $1`,
		r.ID, time.Now(),
	); err != nil {
		log.Printf("agent-tick: stamp %s: %v", r.DisplayName, err)
	}
}

// triggerImmediateTick fires an out-of-band agent tick for one NPC in
// response to an event (someone speaks at their location, they arrive
// somewhere with another agent present, etc.). Bypasses the per-NPC
// hour-offset gate.
//
// Cost guard: when force=false, respects agentMinTickGap so an NPC
// can't be tick-stormed by a chain of NPC-to-NPC reactions. When
// force=true (PC-initiated triggers), the guard is bypassed — PCs
// type at human pace so the storm risk is bounded by a real person's
// speed, and we WANT every NPC in the room to potentially react to
// the player's words.
//
// Reuses the same dawn/dusk inheritance as the dispatcher by re-loading
// world config on the spot — event ticks are rare relative to baseline
// ticks, so the extra query is fine. Active-hours guard still applies:
// no event ticks while the village sleeps.
//
// Safe to call from goroutines (the engine's async paths). Failures are
// logged and don't propagate — event ticks are best-effort.
//
// sceneID (MEM-121) is the cascade UUID this reactive tick belongs to.
// Inherited from the cascade origin and forwarded into runAgentTick so
// every chat row and provider call this NPC produces while reacting
// shares the same scene.
//
// triggerActorID is the actor_id of who caused this tick (the speaker
// for heard-speech, the actor for saw-action, the arriver for
// arrival, the PC's actor_id for pc-spoke, "" for chronicler dispatch
// or any trigger without a salient speaker). Used by claimSceneTick
// to decide whether this is "the same conversational partner I just
// reacted to" (drop) or "someone new" (allow).
func (app *App) triggerImmediateTick(ctx context.Context, npcID, reason string, force bool, sceneID, triggerActorID string) {
	// Scene-level dedup. See SceneTickedActors / claimSceneTick comments
	// for the policy: same triggering actor in the same scene drops, and
	// a hard cap on reactions per (scene, actor) backstops cost.
	//
	// Empty sceneID skips the gate — used only for paths that haven't
	// adopted scene_id yet (defensive; current callers all provide one).
	if sceneID != "" {
		if allowed, reason2 := app.claimSceneTick(sceneID, npcID, triggerActorID); !allowed {
			log.Printf("event-tick %s (%s): skipped — %s in scene %s", npcID, reason, reason2, sceneID)
			return
		}
	}
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("event-tick %s (%s): load config: %v", npcID, reason, err)
		return
	}
	// NOTE: event ticks are NOT gated by agentTickActive(Start|End)Hour.
	// Those constants gate the BASELINE dispatcher (autonomous hourly
	// ticks) so NPCs don't burn budget acting on their own at 4am while
	// the village sleeps. Event ticks are triggered by something
	// happening — a PC speaking, an NPC arriving, an NPC's speech being
	// heard — so by definition the world is active. Gating them by the
	// clock produced silent missed reactions: a PC speaking at 21:00 EDT
	// at the tavern got no NPC response because Hour()==21==EndHour
	// silently returned. The tavernkeeper at 9pm should not go silent
	// just because the autonomous-tick window closed.
	now := time.Now().In(cfg.Location)

	// Load the single NPC row. Mirrors loadAgentNPCRows but for one id.
	row := app.DB.QueryRow(ctx,
		`SELECT n.id, n.display_name, n.llm_memory_agent,
		        n.llm_memory_api_key,
		        n.role,
		        n.inside_structure_id, n.current_x, n.current_y,
		        n.home_structure_id, n.work_structure_id,
		        n.last_agent_tick_at,
		        n.schedule_start_minute, n.schedule_end_minute,
		        COALESCE(wo.display_name, wa.name) AS work_label,
		        COALESCE(ho.display_name, ha.name) AS home_label,
		        n.coins, n.hunger, n.thirst, n.tiredness
		 FROM actor n
		 LEFT JOIN village_object wo ON wo.id = n.work_structure_id
		 LEFT JOIN asset wa ON wa.id = wo.asset_id
		 LEFT JOIN village_object ho ON ho.id = n.home_structure_id
		 LEFT JOIN asset ha ON ha.id = ho.asset_id
		 WHERE n.id = $1 AND n.llm_memory_agent IS NOT NULL`,
		npcID)
	var r agentNPCRow
	if err := row.Scan(&r.ID, &r.DisplayName, &r.LLMMemoryAgent,
		&r.APIKey, &r.Role,
		&r.InsideStructureID, &r.CurrentX, &r.CurrentY,
		&r.HomeStructureID, &r.WorkStructureID,
		&r.LastAgentTickAt,
		&r.ScheduleStartMinute, &r.ScheduleEndMinute,
		&r.WorkLabel, &r.HomeLabel,
		&r.Coins, &r.Hunger, &r.Thirst, &r.Tiredness); err != nil {
		if err != sql.ErrNoRows {
			log.Printf("event-tick %s (%s): load row: %v", npcID, reason, err)
		}
		return
	}

	// Cost guard (NPC-triggered only). PC-triggered (force=true) skips
	// this so multiple co-located NPCs can respond to the same player
	// broadcast. Storm risk is bounded by human typing speed.
	if !force && r.LastAgentTickAt.Valid && time.Since(r.LastAgentTickAt.Time) < agentMinTickGap {
		// Surface the silent skip — without this log, "why didn't NPC X
		// react to Y" investigations have to reconstruct the cost-gap
		// from agent_action_log timestamps. Cheap insurance.
		log.Printf("event-tick %s (%s): skipped — cost guard, last tick %s ago",
			r.DisplayName, reason, time.Since(r.LastAgentTickAt.Time).Round(time.Second))
		return
	}

	// Dawn/dusk for the schedule-note inheritance — same parse path the
	// dispatcher uses.
	dawnMin, duskMin := 6*60, 18*60
	if dh, dm, err := parseHM(cfg.DawnTime); err == nil {
		dawnMin = dh*60 + dm
	}
	if dh, dm, err := parseHM(cfg.DuskTime); err == nil {
		duskMin = dh*60 + dm
	}

	// Event-tick stamp uses time.Now (not hourStart) so the cost guard
	// reads the actual moment of the event, and the baseline gate
	// (last_agent_tick_at < hourStart) STILL passes for the next hour
	// boundary — event ticks don't cancel the next hour's baseline.
	hourStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	log.Printf("event-tick %s: %s", r.DisplayName, reason)
	app.runAgentTick(ctx, &r, hourStart, dawnMin, duskMin, sceneID)
}

// checkStaleAddressees scans freshly-spoken text for first-name
// references to other actors and emits a narration room_event for any
// who are no longer co-located with the speaker. Catches the
// parallel-tick perception race where actor B leaves between when
// actor A's tick snapshotted perception (B present) and when the LLM
// finally produced speech that addresses B by name. Without this,
// the chat log just shows the speaker addressing a phantom.
//
// Heuristic: split each candidate's display_name on whitespace and
// match the first token as a whole word (case-insensitive). Names
// like "Josiah Thorne" or "John Ellis" match on "Josiah" / "John";
// single-token names ("Wendy", "Jefferey") match as themselves. The
// false-positive risk is low because Salem names are picked
// distinctly and not common English words; if that ever changes,
// this should switch to "must be followed/preceded by punctuation"
// or pre-built regex from the actor table.
//
// Runs in a goroutine off the speak commit (fire-and-forget). Errors
// are logged and dropped — the absence of the narration is a minor
// UX glitch, not a sim correctness issue.
func (app *App) checkStaleAddressees(speakerID, speakerName, text, structureID string) {
	ctx := context.Background()
	rows, err := app.DB.Query(ctx,
		`SELECT id, display_name, inside_structure_id FROM actor WHERE id::text != $1`,
		speakerID)
	if err != nil {
		log.Printf("stale-addressee query failed: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var inside sql.NullString
		if err := rows.Scan(&id, &name, &inside); err != nil {
			continue
		}
		first := strings.SplitN(strings.TrimSpace(name), " ", 2)[0]
		if first == "" {
			continue
		}
		// regexp.QuoteMeta covers names with regex-special chars (e.g.
		// O'Brien). (?i) is case-insensitive; \b is word boundary.
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(first) + `\b`)
		if err != nil {
			continue
		}
		if !re.MatchString(text) {
			continue
		}
		// Mentioned by first name. Still co-located with the speaker?
		if inside.Valid && inside.String == structureID {
			continue
		}
		// Absent addressee — emit narration so observers see it.
		log.Printf("stale-addressee: %s addressed %s, who has left structure %s", speakerName, name, structureID)
		app.Hub.Broadcast(WorldEvent{
			Type: "room_event",
			Data: map[string]interface{}{
				"actor_id":     id,
				"actor_name":   name,
				"kind":         "act",
				"text":         fmt.Sprintf("[%s had already left.]", name),
				"structure_id": structureID,
				"at":           time.Now().UTC().Format(time.RFC3339),
			},
		})
	}
}

// triggerCoLocatedTicks fires immediate ticks for every other agentized
// NPC at the given structureID (excluding excludeNpcID, the source of
// the event). Used by the speak commit, arrival hook, and PC speech.
// Each affected NPC's tick is gated by the cost guard in
// triggerImmediateTick UNLESS force=true (PC-initiated — see that
// function's comment for rationale).
//
// sceneID (MEM-121) is the cascade UUID this fan-out belongs to. The
// caller — a cascade origin (PC speak / NPC arrival) or a propagating
// speak commit inside another tick — passes the UUID so every reactive
// tick lands in the same scene. Each goroutine calls
// triggerImmediateTick with context.Background(), intentionally
// detached from the parent ctx; sceneID has to ride along as an
// argument because it can't propagate via context.WithValue.
func (app *App) triggerCoLocatedTicks(ctx context.Context, structureID, excludeNpcID, reason string, force bool, sceneID, triggerActorID string) {
	if structureID == "" {
		return
	}
	rows, err := app.DB.Query(ctx,
		// excludeNpcID is empty when the speaker isn't an NPC (PC speak).
		// Comparing n.id (UUID) to the empty text "" fails PG's implicit
		// cast — the query errors and no NPCs get event-ticked. Guard with
		// a length check so empty just skips the exclusion.
		`SELECT n.id FROM actor n
		 WHERE n.inside_structure_id = $1
		   AND n.llm_memory_agent IS NOT NULL
		   AND ($2 = '' OR n.id::text != $2)`,
		structureID, excludeNpcID)
	if err != nil {
		log.Printf("event-tick co-located query (%s): %v", reason, err)
		return
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		go app.triggerImmediateTick(context.Background(), id, reason, force, sceneID, triggerActorID)
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
			Name:        "move_to",
			Description: "Walk to a named structure, your own home or work, or a neighbor's home. The engine handles pathfinding.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"destination": map[string]interface{}{
						"type":        "string",
						"description": "A structure name ('Smithy', 'Tavern', 'Meeting House'), your own place ('home', 'work', 'my house'), or a neighbor's home ('Goody Smith's home').",
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
			Name:        "act",
			Description: "Commit a brief physical action with your hands or body — serving food, pouring a drink, leaning on the bar, wiping a counter, gesturing. Use this for what you DO, not what you SAY (use speak for speech). The action becomes part of the scene's recent history that other people in the room perceive on their next turn, so use it when an action is worth others noticing.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"verb_phrase": map[string]interface{}{
						"type":        "string",
						"description": "What you do, in past tense and third person, as a single phrase. Examples: 'served stew to Jefferey', 'poured ale for Ezekiel', 'wiped the counter', 'leaned on the bar'.",
					},
				},
				"required": []string{"verb_phrase"},
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
		{
			Name:        "pay",
			Description: "Hand coins to another villager. Use after agreeing on a price in conversation. For purchases at an establishment (tavern, shop, smithy, etc.), pay the proprietor or staff working there — not another patron who happens to be present. The 'for' field is a free-text note about what the payment is for ('a pint of ale', 'the bread', 'the news from Boston'); the engine uses it to decide whether the payment also reduces your hunger or thirst.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"recipient": map[string]interface{}{
						"type":        "string",
						"description": "The villager you're paying, by display name. For a purchase at a tavern, shop, or other establishment, this is the proprietor/staff who works there (e.g. the tavernkeeper if you're buying ale or a meal at the tavern), not any other villager who is present.",
					},
					"amount": map[string]interface{}{
						"type":        "integer",
						"description": "Number of coins to hand over. Must be positive and no more than you currently hold.",
					},
					"for": map[string]interface{}{
						"type":        "string",
						"description": "What the payment is for (free text). Mention 'ale' / 'beer' / 'cider' for a drink, or 'meal' / 'stew' / 'bread' for food, so the engine can reduce the right need.",
					},
				},
				"required": []string{"recipient", "amount"},
			},
		},
		{
			Name:        "recall",
			Description: "Try to remember something — search your past notes, dreams, and impressions for anything relevant. Use this when you want to recall what you know about a person, place, or event. Phrase the query in your own words.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "What you're trying to remember.",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// isCommitTool reports whether the named tool, when emitted, ends the
// harness loop. Observation tools (recall) don't.
//
// Note: this helper has a misleading doc — `speak` is in here for historical
// reasons but is actually inline (loop continues after it executes). The
// helper has no callers today; touching the inconsistency is out of scope.
// `pay` is intentionally NOT included here even though it's an executable
// commit-style tool, to avoid compounding the existing mismatch.
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
// Returns (perception, currentLocationName). The location name is plumbed
// through to resolveObservationTool for any future location-aware
// observation tool that wants it without a re-query.
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
		 FROM actor n
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

	// 3a. Body and purse (ZBBS-082 / ZBBS-083). Each need maps to a
	// period-appropriate descriptor (peckish/hungry/starving etc.); silent
	// when the value is below the awareness floor. Coins remain numeric —
	// money is a thing you count, not a feeling. The whole sentence is
	// omitted when no need is currently surfaced, keeping the perception
	// quiet for a freshly-rested NPC. Thresholds read once per perception
	// build to avoid three setting round-trips per agent tick.
	hungerT := app.loadNeedThreshold(ctx, "hunger_red_threshold", defaultHungerRedThreshold)
	thirstT := app.loadNeedThreshold(ctx, "thirst_red_threshold", defaultThirstRedThreshold)
	tiredT := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)
	bodyParts := []string{}
	if l := needLabel("hunger", r.Hunger, hungerT); l != "" {
		bodyParts = append(bodyParts, l)
	}
	if l := needLabel("thirst", r.Thirst, thirstT); l != "" {
		bodyParts = append(bodyParts, l)
	}
	if l := needLabel("tiredness", r.Tiredness, tiredT); l != "" {
		bodyParts = append(bodyParts, l)
	}
	var bodyLine string
	if len(bodyParts) > 0 {
		bodyLine = fmt.Sprintf("You feel: %s. ", strings.Join(bodyParts, ", "))
	}
	sections = append(sections, fmt.Sprintf("%sCoins in your purse: %d.", bodyLine, r.Coins))

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

	// 5. Here block (M6.4.5) — who else is in this NPC's current scene
	// huddle. Sourced from npc.current_huddle_id; matched against this
	// NPC's acquaintances so unknown others render as descriptors
	// ("the blacksmith") rather than full names ("Ezekiel Crane").
	// Skipped when the NPC isn't in a huddle (alone or outdoors).
	hereLines := app.coLocatedHuddleMembers(ctx, r.ID)
	if len(hereLines) > 0 {
		sections = append(sections, "Here:\n"+strings.Join(hereLines, "\n"))
	}

	// 6. Recent block (M6.4.6) — what people in the room have said and
	// done at this NPC's current location in the last 30 minutes.
	// Sourced from agent_action_log rows with action_type IN
	// ('speak', 'act') whose payload's structure_id matches this NPC's
	// inside_structure_id. Capped at 5 most-recent lines, oldest first
	// so the LLM reads them in chronological order. Skipped when the
	// NPC is outside (no structure context to filter by).
	if r.InsideStructureID.Valid {
		recentLines := app.recentActivityAtStructure(ctx, r.InsideStructureID.String, r.DisplayName, 30*time.Minute, 5)
		if len(recentLines) > 0 {
			sections = append(sections, "Recent:\n"+strings.Join(recentLines, "\n"))
		}
	}

	// Atmosphere — the chronicler's most recent set_environment text.
	// Empty when the chronicler hasn't fired yet (fresh deploy) or
	// when world_environment is otherwise empty. The chronicler writes
	// at phase boundaries and cascade origins; what's here is the
	// village's current ambient texture.
	if atm := app.latestEnvironmentText(ctx); atm != "" {
		sections = append(sections, "Atmosphere: "+atm)
	}

	// Recent events visible to this NPC — village-scope events plus
	// any local-scope events at the NPC's current structure plus any
	// private-scope events targeted at this NPC. Last 7 game-days,
	// capped per-scope at recentEventsCount entries (so up to 3x
	// recentEventsCount total in the worst case, but typically much
	// less since local and private events are sparse).
	since := time.Now().Add(-recentEventsWindow)
	var visibleEvents []string
	visibleEvents = append(visibleEvents,
		app.recentVisibleEvents(ctx, "village", "", since, recentEventsCount)...)
	if r.InsideStructureID.Valid {
		visibleEvents = append(visibleEvents,
			app.recentVisibleEvents(ctx, "local", r.InsideStructureID.String, since, recentEventsCount)...)
	}
	visibleEvents = append(visibleEvents,
		app.recentVisibleEvents(ctx, "private", r.ID, since, recentEventsCount)...)
	if len(visibleEvents) > 0 {
		var lines []string
		for _, e := range visibleEvents {
			lines = append(lines, "- "+e)
		}
		sections = append(sections, "What has happened recently:\n"+strings.Join(lines, "\n"))
	}

	// 7. Feedback from last action (M6.4.8) — when the NPC's most
	// recent commit failed (move_to to an unknown destination, chore
	// with an unknown category, etc.), surface the error so the LLM
	// has a chance to self-correct on the next tick. Without this,
	// failures are silent and the model keeps repeating the same
	// failing call. Only the immediately-prior action is surfaced;
	// older failures are presumably forgotten / superseded.
	if feedback := app.lastActionFeedback(ctx, r.ID); feedback != "" {
		sections = append(sections, "[Can't do that] "+feedback)
	}

	// 6. Decision prompt.
	sections = append(sections, "Decide what to do this hour, then commit with move_to (walk to a named place), "+
		"chore (run a quick errand by category), speak (say something), or done (rest). "+
		"You can also use recall first if you want to remember anything specific.")

	return strings.Join(sections, "\n\n"), locationName
}

// lastActionFeedback returns a one-line error description from the
// NPC's most-recent failed action_log row (within the past hour).
// Empty string when the last action was successful or there's no
// recent action. The hour cap prevents stale feedback from following
// an NPC across long quiet periods.
func (app *App) lastActionFeedback(ctx context.Context, npcID string) string {
	var actionType, result, errMsg string
	err := app.DB.QueryRow(ctx,
		`SELECT action_type, result, COALESCE(error, '')
		 FROM agent_action_log
		 WHERE actor_id::text = $1
		   AND occurred_at > NOW() - INTERVAL '1 hour'
		 ORDER BY occurred_at DESC LIMIT 1`,
		npcID,
	).Scan(&actionType, &result, &errMsg)
	if err != nil || result == "ok" || errMsg == "" {
		return ""
	}
	return fmt.Sprintf("Your last %s attempt failed: %s. Try a different approach.", actionType, errMsg)
}

// coLocatedHuddleMembers returns "Here:" lines for every other NPC and
// PC in the perceiving NPC's current scene huddle. Each line is either
// the other party's name (when acquainted) or a generic descriptor
// (when not). Empty result when the NPC isn't in a huddle, or is the
// only one in their huddle.
//
// NPCs and PCs are unioned in one query — both populations participate
// in scene huddles. Acquaintance is read from npc_acquaintance — the
// perception only shows full names for parties this NPC has previously
// met (huddle co-presence, prior conversation). The descriptor
// fallback for unknown NPCs uses their role label ("the blacksmith");
// for unknown PCs it's always "a traveler" (PCs don't have engine-
// assigned roles — they're identified by character, not occupation).
func (app *App) coLocatedHuddleMembers(ctx context.Context, npcID string) []string {
	rows, err := app.DB.Query(ctx,
		// Co-located actors in the same huddle (excluding self). Returns
		// both kinds: LLM-driven NPCs (llm_memory_agent IS NOT NULL) and
		// human-driven PCs (login_username IS NOT NULL). Decorative NPCs
		// (both NULL) are skipped — physically present but conversationally
		// invisible, otherwise agents would address them and get nothing
		// back, breaking immersion. The 'kind' value lets the caller
		// distinguish NPC vs PC for role-display and tone.
		`SELECT a.display_name AS name,
		        a.role,
		        CASE WHEN a.login_username IS NOT NULL THEN 'pc' ELSE 'npc' END AS kind,
		        EXISTS(
		            SELECT 1 FROM npc_acquaintance ac
		             WHERE ac.actor_id::text = $1
		               AND ac.other_name = a.display_name
		        ) AS acquainted
		   FROM actor a
		  WHERE a.current_huddle_id IS NOT NULL
		    AND a.current_huddle_id = (
		        SELECT current_huddle_id FROM actor WHERE id::text = $1
		    )
		    AND a.id::text != $1
		    AND (a.llm_memory_agent IS NOT NULL OR a.login_username IS NOT NULL)
		  ORDER BY a.display_name`,
		npcID)
	if err != nil {
		log.Printf("here-block: query: %v", err)
		return nil
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var name, kind string
		var role sql.NullString
		var acquainted bool
		if err := rows.Scan(&name, &role, &kind, &acquainted); err != nil {
			continue
		}
		if acquainted {
			lines = append(lines, "  "+name)
			continue
		}
		// Descriptor fallback for unknown others.
		switch kind {
		case "pc":
			lines = append(lines, "  a traveler")
		default:
			if role.Valid && role.String != "" {
				lines = append(lines, "  the "+role.String)
			} else {
				lines = append(lines, "  a stranger")
			}
		}
	}
	return lines
}

// recentActivityAtStructure pulls recent speak and act audit rows whose
// payload records the same structure_id as the perceiving NPC's current
// location. Returns lines in chronological order (oldest → newest):
//
//   - speak: `Speaker said: "text"` (or `You said: "text"` for the perceiver)
//   - act:   `Speaker verb_phrase` (or `You verb_phrase` for the perceiver)
//
// The perceiver's own utterances/acts appear in second person so they
// see their own commitments without the disorientation of being referred
// to in third person in their own perception.
//
// Window is wall-clock minutes; in Salem's no-time-acceleration model
// that maps directly to game-minutes. Capped at the requested count.
func (app *App) recentActivityAtStructure(ctx context.Context, structureID, perceiverName string, window time.Duration, limit int) []string {
	rows, err := app.DB.Query(ctx,
		// Reads speaker_name directly so PC speech (npc_id NULL) lands
		// in the same query — no JOIN to npc. Includes the perceiver's
		// own utterances/acts; the formatter rewrites them to second
		// person. action_type IN ('speak', 'act') unifies both physical
		// and verbal contributions to the scene's recent history.
		`SELECT al.speaker_name, al.action_type,
		        COALESCE(al.payload->>'text', al.payload->>'verb_phrase') AS detail
		 FROM agent_action_log al
		 WHERE al.action_type IN ('speak', 'act')
		   AND al.result = 'ok'
		   AND al.payload->>'structure_id' = $1
		   AND al.occurred_at > NOW() - $2::interval
		 ORDER BY al.occurred_at DESC
		 LIMIT $3`,
		structureID, fmt.Sprintf("%d seconds", int(window.Seconds())), limit)
	if err != nil {
		log.Printf("recent-activity: query: %v", err)
		return nil
	}
	defer rows.Close()
	// Pull rows in DESC order, then reverse to chronological for output.
	var collected []string
	for rows.Next() {
		var name, actionType, detail string
		if err := rows.Scan(&name, &actionType, &detail); err != nil {
			continue
		}
		if detail == "" {
			continue
		}
		isSelf := name == perceiverName
		var line string
		switch actionType {
		case "speak":
			if isSelf {
				line = fmt.Sprintf("  You said: \"%s\"", detail)
			} else {
				line = fmt.Sprintf("  %s said: \"%s\"", name, detail)
			}
		case "act":
			if isSelf {
				line = fmt.Sprintf("  You %s", detail)
			} else {
				line = fmt.Sprintf("  %s %s", name, detail)
			}
		default:
			continue
		}
		collected = append(collected, line)
	}
	// Reverse in place so oldest comes first.
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	return collected
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
// returns the text the LLM sees as the tool result.
func (app *App) resolveObservationTool(ctx context.Context, r *agentNPCRow, tc *agentToolCall, locationName string) string {
	switch tc.Name {
	case "recall":
		query, _ := tc.Input["query"].(string)
		return app.resolveRecall(ctx, r, query)
	}
	return fmt.Sprintf("[Unknown observation tool: %s]", tc.Name)
}

// resolveRecall queries the NPC's own namespace via /v1/memory/search and
// formats the top-K note hits as a tool-result text block. agentTickBudget
// is the natural ceiling on how many recalls a single tick can spend — no
// soft cap here. Empty results return "Nothing comes to mind." so the
// model has a clean signal that the search ran but found nothing.
func (app *App) resolveRecall(ctx context.Context, r *agentNPCRow, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "You tried to remember something but couldn't form the question."
	}
	if len(query) > recallQueryMaxChars {
		query = query[:recallQueryMaxChars]
	}
	if strings.TrimSpace(r.LLMMemoryAgent) == "" {
		log.Printf("agent-tick %s recall: missing llm memory agent", r.DisplayName)
		return "You tried to recall but the memory wouldn't come."
	}
	hits, err := app.npcChatClient.searchMemory(ctx, r.LLMMemoryAgent, query, recallResultLimit)
	if err != nil {
		log.Printf("agent-tick %s recall: %v", r.DisplayName, err)
		return "You tried to recall but the memory wouldn't come."
	}
	return formatRecallHits(hits, app.namespaceDisplayName)
}

// executeAgentCommit translates the LLM's chosen commit tool into engine
// state changes and writes an agent_action_log row. Failures during
// execution still write a row with result='failed' so the audit trail
// captures every decision attempt.
//
// Returns (result, errStr) so inline-handled tools (pay, speak) can
// surface the outcome to the model in the next-iteration continuation
// message — without that signal a "rejected" pay would silently look
// like a successful one to the LLM.
//
// Speak commits stash the speaker's current inside_structure_id into
// the audit payload as `structure_id` so the recent-block perception
// query (M6.4.6) can find which speeches happened at a given location
// without needing a schema migration. Reading
// `payload->>'structure_id'` from agent_action_log is enough.
//
// sceneID (MEM-121) is the cascade UUID of the tick this commit
// originated from. Forwarded to triggerCoLocatedTicks on speak/act
// so reactor ticks land in the same scene; ignored for non-cascading
// commits (move/chore/done/pay don't fan out — move triggers an arrival
// LATER, after the walk completes, which is its own new scene).
//
// Returns (result, errStr) so the dispatcher can surface the outcome
// of pay attempts back into the model's continuation message. Other
// commit types ("ok"/"") are ignored by callers but kept consistent
// for the audit row.
func (app *App) executeAgentCommit(ctx context.Context, r *agentNPCRow, tc *agentToolCall, sceneID string) (string, string) {
	// Augment several payload kinds with structure_id so recent-block
	// queries (perception lookback, talk-panel backload) can answer
	// "what happened here lately" with a single payload->>'structure_id'
	// filter. speak/act stamp because they're conversational and surface
	// in other actors' perceptions; move_to stamps the FROM structure
	// (where the actor was when they decided to leave) so departures
	// appear in the room they left, not the room they're walking to.
	if (tc.Name == "speak" || tc.Name == "act" || tc.Name == "move_to") && r.InsideStructureID.Valid {
		if tc.Input == nil {
			tc.Input = map[string]interface{}{}
		}
		tc.Input["structure_id"] = r.InsideStructureID.String
	}
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
		// Departure narration for the room. Stamps from_structure_id (the
		// room being LEFT) so subscribers filtering by structure can show
		// "Ezekiel left for home" in the place he just walked away from.
		// kind="departure" lets clients render this as italic narration
		// alongside speech and acts.
		if result == "ok" && r.InsideStructureID.Valid {
			app.Hub.Broadcast(WorldEvent{
				Type: "room_event",
				Data: map[string]interface{}{
					"actor_id":     r.ID,
					"actor_name":   r.DisplayName,
					"kind":         "departure",
					"text":         fmt.Sprintf("%s left for %s.", r.DisplayName, dest),
					"structure_id": r.InsideStructureID.String,
					"at":           time.Now().UTC().Format(time.RFC3339),
				},
			})
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
		// Stale-addressee narration: parallel ticks mean an NPC's
		// perception was snapshotted seconds before the LLM produced its
		// speech. If someone they address has already left in the
		// meantime, surface a small narration line so observers
		// understand. See checkStaleAddressees for the policy.
		if r.InsideStructureID.Valid {
			go app.checkStaleAddressees(r.ID, r.DisplayName, text, r.InsideStructureID.String)
		}
		// Event-tick co-located agents so they can react in-band. Force
		// the cost guard off here — when an NPC addresses another NPC
		// by name (or makes a speech the room is reacting to), the
		// addressee should be able to respond inside the same scene
		// even if they ticked seconds ago. The 5-minute agentMinTickGap
		// was too coarse: a tavernkeeper saying "Ezekiel, would you
		// like ale?" was getting 5 minutes of silence back because
		// Ezekiel's tick from the prior PC-speak cascade had stamped
		// LastAgentTickAt 3 seconds earlier.
		//
		// Residual risk: NPC A speaks → B's tick fires → B speaks →
		// A's tick fires → repeat. Each tick is bounded by
		// agentTickBudget (6 iterations) and the model's `done`
		// termination, so individual ticks can't run away — but the
		// inter-tick ping-pong is unbounded. If this becomes a cost
		// problem in practice, the next layer of protection is a
		// per-scene round counter (track depth via scene_huddle or
		// sceneID, force `done` past N rounds).
		if r.InsideStructureID.Valid {
			app.triggerCoLocatedTicks(ctx, r.InsideStructureID.String, r.ID, "heard-speech", true, sceneID, r.ID)
		}

	case "act":
		verb, _ := tc.Input["verb_phrase"].(string)
		if verb == "" {
			result, errStr = "rejected", "empty verb_phrase"
			break
		}
		// act creates a fact in the room — visible to other co-located
		// NPCs on their next perception via recentActivityAtStructure.
		// No engine-state change beyond the audit row; the world doesn't
		// model inventories or seating, but the action is recorded as
		// having happened so future perceptions don't have to reconstruct
		// it from speech alone.
		log.Printf("npc_act: %s — %s", r.DisplayName, verb)
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_acted",
			Data: map[string]interface{}{
				"npc_id":      r.ID,
				"name":        r.DisplayName,
				"verb_phrase": verb,
				"at":          time.Now().UTC().Format(time.RFC3339),
			},
		})
		// Parallel narration broadcast for the talk panel. kind="act" lets
		// the client render "John Ellis poured ale for everyone" as italic
		// narration between dialogue lines. structure_id scopes the event
		// to the room it happened in.
		if r.InsideStructureID.Valid {
			app.Hub.Broadcast(WorldEvent{
				Type: "room_event",
				Data: map[string]interface{}{
					"actor_id":     r.ID,
					"actor_name":   r.DisplayName,
					"kind":         "act",
					"text":         fmt.Sprintf("%s %s.", r.DisplayName, verb),
					"structure_id": r.InsideStructureID.String,
					"at":           time.Now().UTC().Format(time.RFC3339),
				},
			})
		}
		// Same cascade trigger as speak — co-located NPCs may want to
		// react to the action ("oh, you served the merchant first").
		// force=true for the same reason the speak path forces: the
		// addressee/witness shouldn't be cost-gated out of reacting.
		if r.InsideStructureID.Valid {
			app.triggerCoLocatedTicks(ctx, r.InsideStructureID.String, r.ID, "saw-action", true, sceneID, r.ID)
		}

	case "done":
		// No state change. Audit row preserves the decision.

	case "pay":
		recipient, _ := tc.Input["recipient"].(string)
		// Amount tolerates float, int, and string because providers vary on
		// numeric coercion of model output. Reject fractional floats — coins
		// are whole-number; silently truncating 1.9 to 1 would underpay.
		var amount int
		var amountErr string
		switch v := tc.Input["amount"].(type) {
		case float64:
			if v != math.Trunc(v) {
				amountErr = "amount must be a whole number of coins"
			} else {
				amount = int(v)
			}
		case int:
			amount = v
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				amountErr = fmt.Sprintf("amount %q is not a number", v)
			} else {
				amount = n
			}
		}
		if amountErr != "" {
			result, errStr = "rejected", amountErr
			break
		}
		forText, _ := tc.Input["for"].(string)
		pr := app.executePay(ctx, r, recipient, amount, forText)
		result = pr.Result
		errStr = pr.Err

	default:
		result, errStr = "rejected", fmt.Sprintf("unknown commit tool: %s", tc.Name)
	}

	// Write audit row. Errors here are logged but don't propagate — the
	// commit already happened (or already failed); the audit row is a
	// best-effort record.
	_, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, error)
		 VALUES ($1, $2, 'agent', $3, $4, $5, NULLIF($6, ''))`,
		r.ID, r.DisplayName, tc.Name, payload, result, errStr,
	)
	if err != nil {
		log.Printf("agent-tick: audit insert %s/%s: %v", r.DisplayName, tc.Name, err)
	}
	return result, errStr
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
		`UPDATE actor SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
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
	// 1. Self-reference keywords. The model often says move_to("home") or
	// move_to("my work") rather than the explicit structure name — and the
	// tool description even claims "Home" works. Resolve these against
	// this NPC's own home_structure_id / work_structure_id without
	// relying on a name match. Case-insensitive, accepts a few common
	// phrasings.
	if structureID, x, y, ok, err := app.resolveSelfReference(ctx, r, dest); ok || err != nil {
		if err != nil {
			return "", 0, 0, err
		}
		return structureID, x, y, nil
	}

	// 2. Occupant-named home? Strip trailing "'s home" / "'s house" and
	// look up the owner's home_structure_id. We accept either suffix
	// because the perception emits "X's home" but the LLM may rephrase.
	if owner, ok := stripOccupantHomeSuffix(dest); ok {
		row := app.DB.QueryRow(ctx,
			`SELECT n.home_structure_id::text, o.id::text, o.x, o.y,
			        o.loiter_offset_x, o.loiter_offset_y,
			        a.door_offset_x, a.door_offset_y, a.footprint_bottom
			 FROM actor n
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
		var footprintBottom int
		if err := row.Scan(&hsID, &oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err == nil {
			wx, wy := pickWalkTarget(r, hsID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)
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
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom
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
	var footprintBottom int
	if err := row.Scan(&oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err != nil {
		if err == sql.ErrNoRows {
			return "", 0, 0, fmt.Errorf("no structure matches %q", dest)
		}
		return "", 0, 0, err
	}
	wx, wy := pickWalkTarget(r, oID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)
	return oID, wx, wy, nil
}

// pickWalkTarget chooses the walk-to coordinates for an agent-initiated
// move. Owners (NPC's own home or work) walk to door_offset so the
// existing arrive/inside/stand_offset rendering chain stays intact.
// Visitors land at the placement's effective loiter spot (the same one
// the editor's green marker renders at) — see effectiveLoiterTile in
// village_objects.go for the resolution formula.
//
// All offsets are tile-unit ints; multiplied by tileSize=32.0 to get the
// pixel coordinate the walk dispatcher expects.
//
// Visitor jitter (ZBBS-075): when several NPCs walk to the same loiter
// point in close succession (e.g. four villagers heading to the well),
// landing all of them on the same pixel is visually confusing — they
// stack and look like one sprite. A small ±half-tile random offset
// spreads them out naturally. Owners (door path) get no jitter — their
// arrive/inside flow assumes the door tile exactly.
func pickWalkTarget(r *agentNPCRow, structureID string, ox, oy float64,
	loiterX, loiterY, doorX, doorY sql.NullInt32, footprintBottom int) (float64, float64) {
	const tileSize = 32.0
	isOwner := (r.HomeStructureID.Valid && r.HomeStructureID.String == structureID) ||
		(r.WorkStructureID.Valid && r.WorkStructureID.String == structureID)

	if !isOwner {
		lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
		jx, jy := loiterJitter()
		return ox + float64(lx)*tileSize + jx, oy + float64(ly)*tileSize + jy
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

// resolveSelfReference handles destinations that point at this NPC's own
// home or workplace. Returns (id, walkX, walkY, true, nil) when matched,
// (_, _, _, false, nil) when not a self-reference, or an error if a
// match was attempted but the lookup failed (e.g. NPC has no home set).
//
// Owners walk to the door tile (no jitter) — same flow scheduled worker
// arrivals use, so the existing inside/stand-offset rendering chain
// stays intact.
func (app *App) resolveSelfReference(ctx context.Context, r *agentNPCRow, dest string) (string, float64, float64, bool, error) {
	d := strings.ToLower(strings.TrimSpace(dest))
	homePhrases := map[string]bool{
		"home": true, "my home": true, "back home": true,
		"house": true, "my house": true, "go home": true,
	}
	workPhrases := map[string]bool{
		"work": true, "my work": true, "the shop": true,
		"my shop": true, "go to work": true, "the workplace": true,
		"my workplace": true,
	}

	var targetID string
	switch {
	case homePhrases[d]:
		if !r.HomeStructureID.Valid || r.HomeStructureID.String == "" {
			return "", 0, 0, true, fmt.Errorf("you have no home assigned")
		}
		targetID = r.HomeStructureID.String
	case workPhrases[d]:
		if !r.WorkStructureID.Valid || r.WorkStructureID.String == "" {
			return "", 0, 0, true, fmt.Errorf("you have no work assigned")
		}
		targetID = r.WorkStructureID.String
	default:
		return "", 0, 0, false, nil
	}

	row := app.DB.QueryRow(ctx,
		`SELECT o.id::text, o.x, o.y,
		        o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom
		 FROM village_object o JOIN asset a ON a.id = o.asset_id
		 WHERE o.id::text = $1`,
		targetID)
	var oID string
	var ox, oy float64
	var loiterX, loiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	var footprintBottom int
	if err := row.Scan(&oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err != nil {
		return "", 0, 0, true, err
	}
	wx, wy := pickWalkTarget(r, oID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)
	return oID, wx, wy, true, nil
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
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom
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
	var footprintBottom int
	if err := row.Scan(&oID, &ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("no placement tagged %q", category)
		}
		return err
	}
	wx, wy := pickWalkTarget(r, oID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)

	npc := &behaviorNPC{ID: r.ID, CurX: r.CurrentX, CurY: r.CurrentY}
	app.interpolateCurrentPos(npc)
	if err := app.startReturnWalk(ctx, npc, wx, wy, oID, "agent-chore"); err != nil {
		return fmt.Errorf("startReturnWalk: %w", err)
	}

	overrideUntil := time.Now().Add(30 * time.Minute)
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
		r.ID, overrideUntil,
	); err != nil {
		log.Printf("agent-tick: stamp override %s: %v", r.DisplayName, err)
	}
	return nil
}
