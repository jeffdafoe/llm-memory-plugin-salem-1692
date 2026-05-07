package main

// LLM-driven NPC agent tick harness (Salem M6.3).
//
// NPC ticking is reactive-only — there is no autonomous baseline pass.
// Cascade origins (PC speech, NPC arrival, heard-speech reactions,
// chronicler dispatch) call triggerImmediateTick, which loads the one
// affected NPC and runs the harness loop:
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
// Cost guard: triggerImmediateTick rejects re-entry within agentMinTickGap
// (5 min) UNLESS force=true. PC-speak, heard-speech cascades, and
// chronicler attend_to dispatches force=true (direct-address paths).
// Arrival cascades respect the gap.
//
// Failure mode: if /agent/tick returns an error (rate-limit, cost-budget,
// HTTP failure, malformed response), the harness logs and returns without
// stamping. The next event tick re-attempts. The NPC's existing scheduler
// keeps running underneath unless agent_override_until is set, so a hard
// outage on llm-memory-api degrades gracefully to scheduler-only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// errMoveAlreadyAtDest is returned by executeAgentMoveTo when the resolved
// destination structure equals the NPC's current inside_structure_id —
// "go to where you already are" is a no-op walk that would otherwise
// produce a phantom "X left for Y" narration. The dispatcher in
// executeAgentCommit maps this sentinel to a rejected result so the LLM
// gets feedback ("already at destination") in its next continuation
// instead of seeing a successful move.
var errMoveAlreadyAtDest = errors.New("already at destination")

const (
	agentTickBudget = 6
	// Top-K notes returned per recall. The API groups by (namespace,
	// source_file) so each row is one note.
	recallResultLimit = 5
	// Defensive cap on the query length the model can submit to recall.
	// The tool schema is just `string`, so a runaway model could otherwise
	// dump kilobytes of text into the embedding pipeline.
	recallQueryMaxChars = 500
	// Minimum gap between any two ticks for the same NPC. Cost guard against
	// tick storms when several NPCs co-locate and react to each other's
	// speech. Bypassed by force=true (PC-speak and heard-speech cascades).
	agentMinTickGap = 5 * time.Minute
	// Settling period an NPC must observe between their last engagement
	// (speak / serve / pay) and a take_break commit. Without it, reactive
	// ticks fired by a PC's polite refusal tend to land on take_break —
	// the model reads "no thanks, I'm good" as a cue to close shop. This
	// forces a quiet stretch before retreat is allowed.
	takeBreakEngagementCooldown = 5 * time.Minute
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
	// Needs and currency (ZBBS-082). Loaded each dispatch so the perception
	// reflects post-tick state. Stale within one tick if pay fires mid-tick,
	// but the tick is one shot per hour so that's acceptable.
	Coins     int
	Hunger    int
	Thirst    int
	Tiredness int
	// BreakUntil (ZBBS-148) — gates the take_break perception cue so a
	// vendor already mid-break stops being attended into a re-fire loop
	// when their tiredness still reads above the threshold. Loaded with
	// the row in runAgentTick. Valid + future means the cue suppresses.
	BreakUntil sql.NullTime

	// LastServeResult captures the most recent successful serve
	// dispatched via executeAgentCommit during this tick. The harness
	// loop reads it to surface satiation notes ("Ezekiel is stuffed.")
	// in the server's tool result. Cleared at the top of each serve
	// dispatch and consumed once read — never carried across ticks.
	LastServeResult *serveResult
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
// the cascade origin (PC speak / NPC arrival / chronicler dispatch) and
// inherited unchanged by every reactive tick the cascade fires off. It
// rides on every sendChat the harness emits and is passed forward into
// executeAgentCommit, which forwards into triggerCoLocatedTicks for the
// speak case so nested speech reactions stay in the same scene.
func (app *App) runAgentTick(ctx context.Context, r *agentNPCRow, hourStart time.Time, dawnMin, duskMin int, sceneID string) {
	app.stampAgentTick(ctx, r)
	// Defensive — every sim tick should have a sceneID. Cascade origins
	// (PC-speak handler, arrival hook, heard-speech, chronicler dispatch)
	// all pass newUUIDv7() or an inherited cascade ID. An empty value
	// here means a future call site forgot to mint or newUUIDv7 returned
	// empty; the API would silently accept the row with NULL scene_id
	// and we'd lose
	// grouping with no obvious signal. Log so the bug is visible
	// without auto-minting (which would split a cascade into a fresh
	// scene mid-flight and hide the upstream issue).
	if sceneID == "" {
		log.Printf("agent-tick %s: missing sceneID — every sim tick should carry one", r.DisplayName)
	}
	perception, locationName := app.buildAgentPerception(ctx, r, hourStart, dawnMin, duskMin)
	tools := app.buildAgentTools(ctx, r.ID)

	currentMessage := perception
	currentToolCallID := ""

	// Hoist the structure-name lookup outside the iteration loop —
	// avoids N redundant queries per cascade and keeps every chat row
	// in the cascade stamped with the same scene_structure even if a
	// rename lands mid-tick (the comms page assumes one structure per
	// scene_id and would break if rows in the same scene reported
	// different names).
	sceneStructure := app.lookupSceneStructureName(ctx, sceneID)

	var commitCall *agentToolCall
	for iter := 0; iter < agentTickBudget; iter++ {
		reply, err := app.npcChatClient.sendChat(ctx, r.LLMMemoryAgent, currentMessage, currentToolCallID, sceneID, sceneStructure, tools)
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
		// chore, done) win over pay/consume/serve/speak/act. Pay/consume
		// /serve run before speak so a "serve-and-here-you-are" or
		// "pay-and-thank-you" sequence unfolds in the natural order:
		// transaction first, speech next iteration. All inline tools
		// execute and let the loop continue — none ends the turn.
		var terminalCall, payCall, deliverCall, consumeCall, serveCall, gatherCall, summonCall, speakCall, actCall, observation *agentToolCall
		for i := range reply.ToolCalls {
			tc := &reply.ToolCalls[i]
			switch tc.Name {
			case "move_to", "chore", "done", "take_break":
				if terminalCall == nil {
					terminalCall = tc
				}
			case "pay":
				if payCall == nil {
					payCall = tc
				}
			case "deliver_order":
				if deliverCall == nil {
					deliverCall = tc
				}
			case "consume":
				if consumeCall == nil {
					consumeCall = tc
				}
			case "serve":
				if serveCall == nil {
					serveCall = tc
				}
			case "gather":
				if gatherCall == nil {
					gatherCall = tc
				}
			case "summon":
				if summonCall == nil {
					summonCall = tc
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
			//
			// Snapshot needs before so the post-action readback can describe
			// what changed (pay can include immediate consumption per
			// ZBBS-091, in which case the buyer's needs drop alongside).
			// Read from DB rather than r.Hunger/etc. — those are tick-start
			// values and would be stale after a prior consume/pay this turn.
			beforeH, beforeT, beforeTi := app.snapshotNeeds(ctx, r.ID)
			result, errStr, extra := app.executeAgentCommit(ctx, r, payCall, sceneID)
			if result == "ok" {
				readback := app.buildPostConsumeReadback(ctx, r.ID, beforeH, beforeT, beforeTi)
				currentMessage = "[OK] You paid. " + readback + "If a customer or merchant addressed you mid-transaction, speak to them now (a thanks, a follow-up, an answer). Then you may move or call done."
			} else if result == "countered" && extra != "" {
				// Recipient haggled back. Echo the ledger_id so a retry
				// can extend the chain via in_response_to.
				currentMessage = fmt.Sprintf("[Pay countered, ledger_id=%s] %s. To pay the new amount and continue this haggling thread, call pay() again with in_response_to=%s. You may also speak, move, or call done instead.", extra, errStr, extra)
			} else {
				currentMessage = fmt.Sprintf("[Pay %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = payCall.ID
			continue
		}

		if deliverCall != nil {
			// deliver_order: seller-side fulfillment of an accepted ledger
			// row. State change (fulfillment_status ready→delivered, plus
			// inventory transfer or applyConsumption depending on the
			// row's consume_now). Inline like pay so a
			// "deliver-then-here-you-are" chain reads naturally — the
			// continuation message nudges a brief speak so the keeper
			// names the handover.
			result, errStr, extra := app.executeAgentCommit(ctx, r, deliverCall, sceneID)
			if result == "ok" {
				if extra != "" {
					currentMessage = "[OK] You delivered " + extra + ". Speak to the buyer now if you haven't already (a brief 'here you are' or similar). Then you may move or call done."
				} else {
					currentMessage = "[OK] You delivered the order. Speak to the buyer now if you haven't already. Then you may move or call done."
				}
			} else {
				currentMessage = fmt.Sprintf("[Deliver %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = deliverCall.ID
			continue
		}

		if serveCall != nil {
			// Serve: vendor (tavernkeeper, herbalist, blacksmith,
			// merchant) hands stock to co-located people. Decrements
			// own stock, drops recipients' needs (consume_now) or
			// credits their inventories (take-home). No coin transfer.
			// Inline like pay so a "serve-then-mention-the-price" speak
			// chain reads naturally.
			//
			// Continuation explicitly nudges speak — without this the
			// model often picks done after serving even when a customer
			// just asked a question. Silent service to a hanging
			// question reads as cold and unwelcoming.
			result, errStr, _ := app.executeAgentCommit(ctx, r, serveCall, sceneID)
			if result == "ok" {
				msg := "[OK] You served."
				// Satiation notes for any recipient whose relevant
				// need landed at 0. Real bartender-style awareness:
				// "Ezekiel is stuffed" tells John he's done his job
				// for that patron and another round would be wasted.
				if notes := satiationNotes(r.LastServeResult); len(notes) > 0 {
					msg += " " + strings.Join(notes, " ")
				}
				r.LastServeResult = nil
				msg += " If a customer asked you something or is mid-conversation with you, speak to them now — answer the question, name the price, share a word. Then you may move or call done."
				currentMessage = msg
			} else {
				currentMessage = fmt.Sprintf("[Serve %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = serveCall.ID
			continue
		}

		if consumeCall != nil {
			// Consume: eats from your own inventory. Drops the linked need
			// per the item's configured satisfaction. Inline so a "drink
			// then thank the host" sequence reads naturally.
			//
			// Snapshot needs before so the post-action readback can tell
			// the model what changed and what's still pressing — without
			// it the model tends to call done after one consume even if
			// other needs are still at red tier (saw John Ellis eat bread
			// then done while still parched and exhausted on 2026-05-02).
			// Read from DB so a second consume in the same tick gets fresh
			// pre-action values instead of tick-start (stale) ones.
			beforeH, beforeT, beforeTi := app.snapshotNeeds(ctx, r.ID)
			result, errStr, _ := app.executeAgentCommit(ctx, r, consumeCall, sceneID)
			if result == "ok" {
				readback := app.buildPostConsumeReadback(ctx, r.ID, beforeH, beforeT, beforeTi)
				currentMessage = "[OK] You consumed it. " + readback + "If anyone is mid-conversation with you, speak to them now. Then you may move or call done."
			} else {
				currentMessage = fmt.Sprintf("[Consume %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = consumeCall.ID
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
			_, _, _ = app.executeAgentCommit(ctx, r, speakCall, sceneID)
			currentMessage = "[OK] You spoke. Continue your turn — you may move or run a chore now, or call done if you're staying put."
			currentToolCallID = speakCall.ID
			continue
		}

		if actCall != nil {
			// act is non-terminal like speak — the model often pairs a
			// physical action with a follow-up speech ("served stew" then
			// "here you are, mind the heat"). Same [OK] nudge so the
			// model knows the turn isn't over.
			_, _, _ = app.executeAgentCommit(ctx, r, actCall, sceneID)
			currentMessage = "[OK] You did that. If anyone is mid-conversation with you, speak to them now. Then you may move or call done."
			currentToolCallID = actCall.ID
			continue
		}

		if gatherCall != nil {
			// gather is non-terminal — the typical chain is gather then
			// move_to back home, or gather then act/speak about it.
			// Surfaces the rejection text verbatim so a "not at a source"
			// or "depleted" outcome feeds the model's next decision
			// instead of silently disappearing.
			result, errStr, extra := app.executeAgentCommit(ctx, r, gatherCall, sceneID)
			if result == "ok" {
				if extra != "" {
					currentMessage = "[OK] You gathered " + extra + ". If anyone is mid-conversation with you, speak to them now. Then you may move or call done."
				} else {
					currentMessage = "[OK] You filled your inventory. If anyone is mid-conversation with you, speak to them now. Then you may move or call done."
				}
			} else {
				currentMessage = fmt.Sprintf("[Gather %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = gatherCall.ID
			continue
		}

		if summonCall != nil {
			// summon is non-terminal — sender typically follows the call
			// with a speak ("I've sent for them") or a move. Like pay,
			// the rejection text matters: the model should know if the
			// summons was rejected (cooldown / co-located / unknown
			// target) so it doesn't loop "send messenger, send messenger".
			result, errStr, _ := app.executeAgentCommit(ctx, r, summonCall, sceneID)
			if result == "ok" {
				currentMessage = "[OK] The messenger is on their way. If anyone is mid-conversation with you, speak to them now (a 'I've sent for them' would be natural). Then you may move or call done."
			} else {
				currentMessage = fmt.Sprintf("[Summon %s] %s. Continue your turn — you may correct it, speak, move, or call done.", result, errStr)
			}
			currentToolCallID = summonCall.ID
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

	_, _, _ = app.executeAgentCommit(ctx, r, commitCall, sceneID)
	app.stampAgentTick(ctx, r)

	// Return-to-work follow-up (ZBBS-110). After the commit settled, if
	// the nudge predicate still applies, schedule a self-tick 30-60s out
	// so the LLM gets another turn after the conversation has had a beat
	// to land. Re-queries inside_structure_id and current coords because
	// a move_to commit will have updated them; the perception's snapshot
	// is now stale. If conditions are no longer true (NPC moved to work,
	// fell into a pressing need), no schedule is written.
	app.maybeScheduleReturnToWork(ctx, r.ID)
}

// stampAgentTick records that we've ticked this NPC. Stamps to time.Now()
// so the agentMinTickGap cost guard reads an accurate elapsed time when
// triggerImmediateTick checks last_agent_tick_at on subsequent events.
func (app *App) stampAgentTick(ctx context.Context, r *agentNPCRow) {
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET last_agent_tick_at = $2 WHERE id = $1`,
		r.ID, time.Now(),
	); err != nil {
		log.Printf("agent-tick: stamp %s: %v", r.DisplayName, err)
	}
}

// triggerImmediateTick fires an agent tick for one NPC in response to a
// cascade origin (PC speech at their location, NPC arrival, heard-speech
// reaction, chronicler dispatch). This is the only path NPCs tick on —
// there is no autonomous baseline pass.
//
// Cost guard: when force=false, respects agentMinTickGap so an NPC
// can't be tick-stormed by a chain of NPC-to-NPC reactions. When
// force=true (PC-initiated triggers and heard-speech cascades), the
// guard is bypassed — PCs type at human pace so the storm risk is
// bounded by a real person's speed, and we WANT every NPC in the room
// to potentially react to the player's words.
//
// Loads world config on the spot for dawn/dusk inheritance — event ticks
// are rare enough that the extra query is fine.
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
	// In-flight gate (ZBBS-100). Drops cleanly when this actor is
	// already running an LLM tick from a prior cascade — the previous
	// tick will commit whatever it commits and we don't want a parallel
	// goroutine producing duplicate output. Caught the live case of a
	// PC-speak cascade and an overseer-attend-to firing on the same
	// actor seconds apart, both bypassing cost guard via force=true,
	// both producing identical "served stew" act rows because the
	// model saw the same room twice.
	//
	// In-flight gate AND cost guard both run BEFORE scene-dedup so a
	// dropped tick never consumes a reaction-cap slot in the
	// SceneTickedActors map. Without that ordering an arrival cascade
	// (force=false) could be cost-skipped after claiming John's
	// (sceneID, actor) slot with lastTriggerActor=Josiah, then the
	// very next force=true speak cascade (same scene, same speaker)
	// would be turned away by claimSceneTick's "same trigger actor"
	// rule and John would never react to a speech aimed at the room
	// he was sitting in.
	if !app.tryClaimAgentTick(npcID) {
		log.Printf("event-tick %s (%s): skipped — prior tick still in flight", npcID, reason)
		return
	}
	defer app.releaseAgentTick(npcID)

	// Load the single NPC row. Pulled forward (was after scene-dedup) so
	// the cost guard below can run before claimSceneTick — see comment
	// above on why the order matters.
	// Need values come from actor_need rows now that ZBBS-121 dropped the
	// legacy actor.{hunger,thirst,tiredness} columns. Scalar subqueries
	// with COALESCE so an actor missing one of the three rows reads as 0
	// rather than aborting the whole tick.
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
		        n.coins,
		        COALESCE((SELECT value FROM actor_need WHERE actor_id = n.id AND key = 'hunger'), 0)::smallint,
		        COALESCE((SELECT value FROM actor_need WHERE actor_id = n.id AND key = 'thirst'), 0)::smallint,
		        COALESCE((SELECT value FROM actor_need WHERE actor_id = n.id AND key = 'tiredness'), 0)::smallint,
		        n.break_until
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
		&r.Coins, &r.Hunger, &r.Thirst, &r.Tiredness,
		&r.BreakUntil); err != nil {
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
	now := time.Now().In(cfg.Location)

	// Cancel any pending self-tick (ZBBS-110). A cascade origin (PC speak,
	// other-NPC arrival, chronicler attend, summon delivery, scheduled
	// shift boundary) is fresher signal than a self-tick we queued earlier
	// — the harness end below will re-evaluate and reschedule if the
	// nudge predicate still applies. Skipped for self-tick fires
	// themselves: dispatchSelfTicks already cleared the slot in the
	// SQL UPDATE that selected this NPC.
	if !strings.HasPrefix(reason, "self:") {
		app.cancelSelfTick(ctx, npcID)
	}

	// Dawn/dusk for the schedule-note inheritance the perception surfaces.
	dawnMin, duskMin := 6*60, 18*60
	if dh, dm, err := parseHM(cfg.DawnTime); err == nil {
		dawnMin = dh*60 + dm
	}
	if dh, dm, err := parseHM(cfg.DuskTime); err == nil {
		duskMin = dh*60 + dm
	}

	// hourStart is the current-hour wall-clock boundary the perception
	// uses to format "you would currently be on shift" / time-of-day
	// signals for the model. Computed once per tick from cfg.Location.
	hourStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	log.Printf("event-tick %s: %s", r.DisplayName, reason)
	app.runAgentTick(ctx, &r, hourStart, dawnMin, duskMin, sceneID)
}

// findVocativeStaleAddressees returns the display names of addressable
// actors who are NOT in the speaker's huddle but are addressed in the
// speech text in vocative position — first name immediately followed
// by a comma ("Ezekiel, you look hungry"). The speak tool rejects the
// whole speak when the addressee has already left the conversation.
//
// Vocative-only on purpose: third-person mentions ("I've sent for
// Prudence Ward") name distant actors without addressing them, and a
// previous post-broadcast "had already left" narration on those
// produced misleading false positives — implying the named actor
// had been there. Only direct address gates here.
//
// Co-location is defined by shared current_huddle_id, not shared
// inside_structure_id. The huddle is the engine's conversational
// unit: a PC who knocks at a door joins the inside NPC's huddle but
// stays outside the structure (their inside_structure_id stays
// null). Using inside_structure_id as the predicate turned every
// vocative welcome ("Wendy, please come in") into a false rejection
// because the door-knocker isn't technically inside. Huddle
// membership is the right signal.
//
// If the speaker has no current_huddle_id, returns nil (no
// rejection) — there's no conversational unit to validate against,
// so don't second-guess the model.
//
// Scope: only addressable actors (PC login or active
// llm_memory_agent). Background placeholder NPCs can't respond and
// shouldn't gate other NPCs' speech if their first names land in
// vocative position.
func (app *App) findVocativeStaleAddressees(ctx context.Context, speakerID, text string) ([]string, error) {
	var speakerHuddle sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_huddle_id::text FROM actor WHERE id::text = $1`,
		speakerID).Scan(&speakerHuddle); err != nil {
		return nil, err
	}
	if !speakerHuddle.Valid || speakerHuddle.String == "" {
		return nil, nil
	}
	rows, err := app.DB.Query(ctx,
		`SELECT display_name, current_huddle_id::text
		   FROM actor
		  WHERE id::text != $1
		    AND (login_username IS NOT NULL OR llm_memory_agent IS NOT NULL)`,
		speakerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var absent []string
	for rows.Next() {
		var name string
		var huddle sql.NullString
		if err := rows.Scan(&name, &huddle); err != nil {
			continue
		}
		first := strings.SplitN(strings.TrimSpace(name), " ", 2)[0]
		if first == "" {
			continue
		}
		// Vocative pattern: first name immediately followed by a comma.
		// Case-sensitive — same rationale as checkStaleAddressees:
		// case-insensitive matching turned common verbs into name hits.
		re, err := regexp.Compile(`\b` + regexp.QuoteMeta(first) + `,`)
		if err != nil {
			continue
		}
		if !re.MatchString(text) {
			continue
		}
		if huddle.Valid && huddle.String == speakerHuddle.String {
			continue
		}
		absent = append(absent, name)
	}
	return absent, rows.Err()
}

// actorStructureScope returns the effective conversational structure
// scope for an actor at a given position. Used by the speak broadcast
// and the event-tick cascade to decide which structure_id this actor's
// utterance belongs to.
//
// Indoor actors return their inside_structure_id directly. Outdoor
// actors fall back to the nearest village_object whose loiter pin is
// within Chebyshev 64 px of their position — the well's pin for an
// actor standing at the well, the lamp post's pin for someone
// loitering there. Returns "" when the actor is outdoors and not at
// any structure's loiter ring (open road, courtyard).
//
// 64 px tolerance covers slot snap and minor drift; same value as
// the loiter-huddle formation predicate in handlePCMove. Single
// indexed-ish lookup against village_object — small table.
func (app *App) actorStructureScope(ctx context.Context, insideStructureID sql.NullString, x, y float64) string {
	if insideStructureID.Valid && insideStructureID.String != "" {
		return insideStructureID.String
	}
	var scope sql.NullString
	err := app.DB.QueryRow(ctx,
		`SELECT o.id::text FROM village_object o
		  WHERE o.loiter_offset_x IS NOT NULL
		    AND o.loiter_offset_y IS NOT NULL
		    AND GREATEST(
		          ABS(o.x + o.loiter_offset_x * 32 - $1),
		          ABS(o.y + o.loiter_offset_y * 32 - $2)
		        ) <= 64
		  ORDER BY GREATEST(
		          ABS(o.x + o.loiter_offset_x * 32 - $1),
		          ABS(o.y + o.loiter_offset_y * 32 - $2)
		        ) ASC
		  LIMIT 1`,
		x, y).Scan(&scope)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("actorStructureScope at (%.1f, %.1f): %v", x, y, err)
		}
		return ""
	}
	if scope.Valid {
		return scope.String
	}
	return ""
}

// triggerCoLocatedTicks fires immediate ticks for every other agentized
// NPC at the given structureID (excluding excludeNpcID, the source of
// the event). Used by the speak commit, arrival hook, and PC speech.
// Each affected NPC's tick is gated by the cost guard in
// triggerImmediateTick UNLESS force=true (PC-initiated — see that
// function's comment for rationale).
//
// "Co-located" includes both NPCs inside the structure (the indoor
// case, the original behavior) and outdoor NPCs standing at the
// structure's loiter ring (within Chebyshev 64 px of the loiter pin).
// The latter covers loiter-huddle scenarios — e.g., Prudence drinking
// at the well reacts to a PC speaking in the well's huddle even
// though her inside_structure_id is NULL. Same 64 px tolerance as
// actorStructureScope and the loiter-huddle formation predicate.
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
	// ZBBS-149: when triggering co-located ticks for an event inside a
	// structure, filter to actors in the SAME subspace as the trigger
	// source. Loiter-ring co-location at a structure with no
	// inside_structure_id is unaffected — outdoors has no subspace.
	//
	// Subspace resolution by case:
	//   - Anonymous trigger ($3 = ''): chronicler dispatch or other
	//     non-actor-rooted event. Default to the structure's 'common'
	//     subspace so cascade triggers don't reach lodgers in their
	//     bedrooms.
	//   - Actor trigger ($3 != ''): use that actor's inside_subspace_id
	//     directly. Equality (=) rather than IS NOT DISTINCT FROM, so a
	//     trigger actor with NULL subspace (broken state, shouldn't
	//     happen post-migration) reaches nobody — fail closed instead
	//     of silently bucketing into common.
	rows, err := app.DB.Query(ctx,
		`SELECT n.id FROM actor n
		 LEFT JOIN village_object o ON o.id::text = $1
		 WHERE n.llm_memory_agent IS NOT NULL
		   AND ($2 = '' OR n.id::text != $2)
		   AND (
		     (
		       n.inside_structure_id::text = $1
		       AND n.inside_subspace_id = CASE
		         WHEN $3 = '' THEN (
		           SELECT id FROM structure_subspace
		            WHERE structure_id::text = $1 AND kind = 'common'
		            LIMIT 1
		         )
		         ELSE (
		           SELECT inside_subspace_id FROM actor
		            WHERE id::text = $3
		              AND inside_structure_id::text = $1
		         )
		       END
		     )
		     OR (
		       n.inside_structure_id IS NULL
		       AND o.loiter_offset_x IS NOT NULL
		       AND o.loiter_offset_y IS NOT NULL
		       AND GREATEST(
		             ABS(n.current_x - (o.x + o.loiter_offset_x * 32)),
		             ABS(n.current_y - (o.y + o.loiter_offset_y * 32))
		           ) <= 64
		     )
		   )`,
		structureID, excludeNpcID, triggerActorID)
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
	log.Printf("knock-trace co-located reason=%s structure=%s exclude=%q found=%d ids=%v force=%v scene=%s",
		reason, structureID, excludeNpcID, len(ids), ids, force, sceneID)
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
			Description: "Run a quick errand by category. Engine picks the nearest matching place and walks you to it. The chore itself is just travel — what you do once you arrive is up to your next decision. Examples: chore(well) walks you to the nearest well; once there you can drink (your thirst drops on arrival automatically) and/or call gather to fill a pail of water to take home. chore(tavern) walks you to a tavern but doesn't order anything — speak to a tavernkeeper or pay them to actually consume.",
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
			Description: "Say something out loud to people at your current location. Optional mentions field tags item_kinds your speech references (so customers' pay dropdowns can populate); see the parameter description. Optional price field locks in a per-unit asking price for ALL items in mentions — customers who pay less than that will be REJECTED at the engine, so use it whenever you state a price out loud.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{"type": "string"},
					"mentions": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
						"description": "OPTIONAL. Item kinds your speech is referencing — e.g. ['cheese'] when you tell a customer 'I have cheese for 5 coins', or ['ale','bread'] when listing what's available. PC clients use this to populate the customer's pay dropdown so they can buy from you. Each entry MUST exist in your inventory ('Items you can sell' line) — the speak will be REJECTED if you mention items you don't have. Use lowercase item kind names (e.g. 'cheese', not 'Cheese' or 'a wedge of cheese'). Omit when your speech doesn't reference goods.",
					},
					"price": map[string]interface{}{
						"type":        "integer",
						"description": "OPTIONAL. The per-unit asking price (in whole coins) you are quoting for the item(s) you mention. ONLY set this when you say a price out loud and want it enforced — e.g. text 'Stew for 3 coins.' with mentions=['stew'] and price=3. The engine records this quote in the current scene; subsequent pay() calls from anyone in the room must offer at least price * qty for this item or they will be REJECTED. Applies the SAME price to every item in mentions, so only set it when one price covers everything you mention. Omit when your speech is conversational or doesn't fix a price (greetings, listings without prices, follow-ups). Whole numbers only; must be non-negative.",
					},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "act",
			Description: "Commit a brief physical action with your hands or body — serving food, pouring a drink, leaning on the bar, wiping a counter, gesturing. Use this for what you DO, not what you SAY (use speak for speech). The action becomes part of the scene's recent history that other people in the room perceive on their next turn, so use it when an action is worth others noticing. Slow tasks (cooking, roasting, baking, brewing, smithing, building) take in-world minutes to hours: commit a single act announcing the start (e.g. 'started roasting meat for Jefferey') and STOP THERE for this turn — do NOT also commit a 'served/presented/here's your meal' follow-up act or speech in the same response. The completion happens naturally later, as its own act in a future tick.",
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
			Name:        "take_break",
			Description: "Close your post — use when you can't or won't serve customers right now (feeling unwell, family matter, taking lunch, needing rest). Don't also call speak in the same turn — the engine speaks a brief excuse for you using the reason you provide. You stay where you are; your shop becomes closed for the duration. Any customers inside will be asked (politely, then firmly) to leave. Customers who try to enter see that you've stepped out and won't expect service. You recover tiredness while closed.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"until_hour": map[string]interface{}{
						"type":        "integer",
						"description": "Hour of day (0-23) you intend to be back today. Defaults to four hours from now if omitted. Past hours are rejected — pick a later hour or omit the field.",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Short period-appropriate phrase the engine uses to compose your spoken excuse. Examples: 'feeling unwell', 'gone to fetch supplies', 'family matter at home'. Omit if you don't want to give a reason.",
					},
				},
			},
		},
		{
			Name:        "pay",
			Description: "Hand coins to another villager. The single verb for every commercial transaction — including SALES from vendors. Sales are buyer-initiated and atomic: stock decrements, goods or consumption land, coins move, all in one transaction. Vendors do NOT push goods at you via serve; you call pay to buy from them. Use after agreeing on a price in conversation. For purchases at an establishment (tavern, shop, smithy), pay the proprietor or staff working there — not another patron who happens to be present.\n\nFour shapes:\n  - pay(recipient, amount) — generic coin transfer for a tip, service, news, or anything not item-shaped. No goods change hands.\n  - pay(recipient, amount, item, qty?, consume_now=true) — at-source consumption (the tavern verb). The seller's stock decrements and your linked need (hunger/thirst) drops. Default when you specify an item.\n  - pay(recipient, amount, item, qty?, consume_now=false) — take-home. The seller's stock moves into your inventory for later use. Only works for portable items; non-portable like stew (hot bowl, not packable) must be consumed at-source.\n  - pay(recipient, amount, item, qty?, consume_now=true, consumers=[names]) — at-source GROUP order: buy a round for everyone in your huddle. Stock decrements by qty * len(consumers); each named consumer's need drops; you pay the full amount. Use when ordering for a group at a tavern.\n\nNo fixed price list — agree on the amount in speak() first, then commit the agreed total here.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"recipient": map[string]interface{}{
						"type":        "string",
						"description": "The villager you're paying, by display name. For a purchase at a tavern, shop, or other establishment, this is the proprietor/staff who works there (e.g. the tavernkeeper if you're buying at the tavern), not any other villager who is present.",
					},
					"amount": map[string]interface{}{
						"type":        "integer",
						"description": "Number of coins to hand over. WHOLE NUMBER ONLY — fractional coins are not supported and will reject. Must be non-negative and no more than you currently hold. The negotiated total, not per-unit when qty > 1.",
					},
					"for": map[string]interface{}{
						"type":        "string",
						"description": "Optional flavor text describing what the payment is for ('a pint of ale', 'the news from Boston', 'your help with the cart'). Audit-only, no mechanical effect.",
					},
					"item": map[string]interface{}{
						"type":        "string",
						"description": "Optional item kind being purchased. Match the names in the vendor's recent speech (their speak.mentions field) or in their 'Items you can sell' inventory readout. Omit for non-item payments (tips, services).",
					},
					"qty": map[string]interface{}{
						"type":        "integer",
						"description": "How many of the item, PER CONSUMER (not total when consumers > 1). Defaults to 1.",
					},
					"consume_now": map[string]interface{}{
						"type":        "boolean",
						"description": "True (default) consumes the item at the seller's place — drink the ale at the bar, eat the stew at the tavern, the consumer's need drops immediately. False takes the item home into your own inventory; only works for portable items.",
					},
					"consumers": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
						"description": "OPTIONAL. At-source group orders only (consume_now=true with an item). Display names of the people who will eat/drink — typically you and your tablemates. You can include yourself or omit yourself. Each named consumer must be in your huddle. Stock decrements by qty * len(consumers); each consumer's need drops. Omit for solo orders (you become the implicit consumer).",
					},
					"in_response_to": map[string]interface{}{
						"type":        "integer",
						"description": "OPTIONAL. When the seller previously countered an offer from you in this exchange (you saw a 'countered' tool result with a ledger id), pass that id here to declare this pay as your response to that counter. The new pay links to the prior one in the haggling chain. Omit for the first pay in any negotiation.",
					},
				},
				"required": []string{"recipient", "amount"},
			},
		},
		{
			Name:        "consume",
			Description: "Eat or drink an item from your own inventory. Reduces the linked need (food → hunger, drink → thirst). Use this when you actually want to satisfy a need from goods you already own — the bread you bought from the merchant, the ale in your flask. Materials (wheat, flour, iron) can't be consumed; you'd need to make something with them first.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"item": map[string]interface{}{
						"type":        "string",
						"description": "Item kind, lowercase. Must be a consumable food or drink in your inventory.",
					},
					"qty": map[string]interface{}{
						"type":        "integer",
						"description": "How many to consume. Defaults to 1 if omitted.",
					},
				},
				"required": []string{"item"},
			},
		},
		{
			Name:        "gather",
			Description: "Take a portable item from a source you're loitering at — fill a pail of water at the well, pluck berries at an orchard. You must already be at the source (use chore first to walk there). Sources today: " + gatherToolSourceLine() + ". The product goes into your inventory; you can carry it home, serve it to customers, or consume it later. Bounded sources (orchards) deplete and refresh over time; unbounded sources (wells) never run dry.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"qty": map[string]interface{}{
						"type":        "integer",
						"description": "How many units to take. Defaults to 1.",
					},
				},
			},
		},
		{
			Name:        "summon",
			Description: "Send a messenger to fetch another villager — a child running with a message, an apprentice sent across the lane, hollering over the fence. The named villager will perceive the summons on their next moment and decide whether to come. Use when you want company, need help, or have business with someone who isn't here. They may or may not actually come; refusal or delay is part of village life. Do NOT summon someone who is already in the room with you.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target": map[string]interface{}{
						"type":        "string",
						"description": "Display name of the villager to fetch.",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Optional short message the messenger carries (e.g. 'come share an ale', 'we need your counsel'). Audit-only flavor; the target sees this in their perception.",
					},
				},
				"required": []string{"target"},
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
		{
			Name:        "check_order_book",
			Description: "Look at orders that customers have paid for and that you still owe — what's been bought from you that hasn't yet been handed over. Returns a list with ledger ids the deliver_order tool uses. Read-only; calling this doesn't deliver anything. Use when you want to see what's pending before deciding who to serve next.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "deliver_order",
			Description: "Hand over goods (or serve consumption) for a paid-but-not-yet-delivered order from your order book. The customer paid you earlier; this is the moment you slide the bowl across, hand them their bread, or pour their ale. For at-source consumption (consume_now=true items like stew, ale at the bar) this is when the customer's hunger or thirst actually drops — coins moved at pay time, but they aren't fed until you deliver. For take-home items, this is when the goods enter their inventory. Use the ledger_id from your check_order_book result. You can call this on its own or follow it with a brief speak (\"here you are\", \"that'll keep you warm\").",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ledger_id": map[string]interface{}{
						"type":        "integer",
						"description": "The pay_ledger row id for the order you're delivering. Get this from check_order_book — each entry is prefixed with its ledger_id. Whole numbers only.",
					},
				},
				"required": []string{"ledger_id"},
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
	// loiteringAtID is the structure the NPC is parked at via a visitor
	// move (move_to a non-owned target, chore destination, loiter
	// relocate). Visitor moves keep inside_structure_id NULL by design;
	// the perception used to call the NPC's location "the open village"
	// even when standing on a shop's loiter slot. resolveLoiteringStructure
	// reverses pickVisitorSlot so we can name the building the player
	// sees on screen, and below we use it to surface unattended-shop
	// signals when the proprietor isn't present.
	loiteringAtID := ""
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
	} else if id, name := app.resolveLoiteringStructure(ctx, r.CurrentX, r.CurrentY); id != "" {
		locationName = name
		loiteringAtID = id
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

	// 1b. Role instructions (ZBBS-098). For each attribute the actor
	// holds whose attribute_definition.instructions is non-empty, append
	// the configured prompt copy. Empty when the actor has no
	// attributes or none carry instructions; in that case nothing is
	// added so legacy NPCs see no extra section.
	if roleText, _ := app.loadInstructionsForActor(ctx, r.ID); roleText != "" {
		sections = append(sections, roleText)
	}

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
		shiftLine := fmt.Sprintf(
			"Your usual hours at your work are %s–%s. The time is now %s — you would currently be %s.",
			formatHHMM(startMin), formatHHMM(endMin), hourStart.Format("15:04"), shiftWord,
		)
		// For home==work vendors (e.g. tavernkeeper, blacksmith), the off-
		// shift cue used to prompt move_to(home) — which the engine rejects
		// as already-there. Surface the right tool (take_break) and remove
		// the implicit relocation cue.
		if !onShift && homeIsWork {
			shiftLine += " Since your home and work are the same place, you don't need to relocate — call take_break if you want to close your shop until you're ready to work again."
		}
		sections = append(sections, shiftLine)

		// ZBBS-129 step 4: working-late cue. In the final hour of shift,
		// if there are still ready orders the vendor hasn't delivered,
		// nudge them to clear the queue before going off duty (or
		// explicitly stay past shift end). Per home's design review the
		// cue is gated to the final hour to avoid pre-emptive overtime
		// — earlier in the shift the LLM should rely on the standing
		// "Customers awaiting delivery" perception block (ZBBS-136)
		// without a temporal urgency overlay.
		if onShift {
			minsUntilShiftEnd := (endMin - nowMin + 1440) % 1440
			if minsUntilShiftEnd > 0 && minsUntilShiftEnd <= 60 {
				if entries, err := app.readyOrdersForSeller(ctx, r.ID); err != nil {
					log.Printf("perception working-late check %s: %v", r.DisplayName, err)
				} else if len(entries) > 0 {
					plural := "s"
					if len(entries) == 1 {
						plural = ""
					}
					sections = append(sections, fmt.Sprintf(
						"Your shift ends in %d min. You still have %d order%s ready to deliver — finalize before going off duty, or stay past shift end if you want to clear the queue.",
						minsUntilShiftEnd, len(entries), plural,
					))
				}
			}
		}
	}

	// Need thresholds loaded early — used by both the visible-needs
	// section below (for co-located others) and the body section further
	// down (for the perceiver themselves). Three setting reads, cheap.
	hungerT := app.loadNeedThreshold(ctx, "hunger_red_threshold", defaultHungerRedThreshold)
	thirstT := app.loadNeedThreshold(ctx, "thirst_red_threshold", defaultThirstRedThreshold)
	tiredT := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)

	// 3. Right-now.
	sections = append(sections, fmt.Sprintf(
		"You are at %s. The time is %s.",
		locationName, hourStart.Format("Monday 15:04"),
	))

	// 3.0a Unattended-structure / shop-stock signal. When the NPC is
	// loitering at a structure (visitor move, no huddle), one of two
	// signals fires depending on whether the proprietor is present:
	//
	//   Unattended: name the missing workers so the LLM can plausibly
	//   ask others or pick a substitute destination on purpose. Without
	//   this an NPC arrives at a closed-feeling shop with no signal
	//   that "Josiah isn't here" and re-decides blindly.
	//
	//   Stocked: list the present workers' inventory with quantities so
	//   the LLM can decide whether to top up before leaving — pairs
	//   with "Your inventory: ..." below for a quick "have vs. offered"
	//   comparison.
	//
	// Mutually exclusive — a tended shop never shows the unattended
	// line, and an empty shop never shows stock the LLM can't buy.
	if loiteringAtID != "" {
		if line := app.unattendedWorkersLine(ctx, loiteringAtID, r.ID); line != "" {
			sections = append(sections, line)
		} else if line := app.shopStockLine(ctx, loiteringAtID, r.ID); line != "" {
			sections = append(sections, line)
		}
		// Posted notice text on a tagged noticeboard. Independent of the
		// staffed/unstaffed signal above — a board can stand at a tended
		// shop AND carry its own posted content; both lines render. Empty
		// string when the loiter target isn't a noticeboard or has no
		// content posted on it.
		if line := app.noticeBoardLineForLoiterer(ctx, loiteringAtID); line != "" {
			sections = append(sections, line)
		}
	}

	// 3.0b Retained concerns (ZBBS-117). Chronicler-authored notice prose
	// can attach structured facts to named actors and structures; this
	// section surfaces those facts to the affected NPC even when they
	// haven't been near the noticeboard. Workers-only on the structure
	// cascade — the tavernkeeper retains a "lost shawl" concern on his
	// tavern; a patron sitting at a table does not. Capped at 3 newest
	// per category inside loadConcernsForActor.
	{
		var homeID, workID string
		if r.HomeStructureID.Valid {
			homeID = r.HomeStructureID.String
		}
		if r.WorkStructureID.Valid {
			workID = r.WorkStructureID.String
		}
		concerns, cerr := app.loadConcernsForActor(ctx, r.ID, homeID, workID)
		if cerr != nil {
			log.Printf("perception: load concerns for %s: %v", r.ID, cerr)
		} else if rendered := renderConcerns(concerns); rendered != "" {
			sections = append(sections, strings.TrimRight(rendered, "\n"))
		}
	}

	// 3.0b.1 Buyer-side overdue orders (ZBBS-129 step 4). Surfaces any
	// pay_ledger row the perceiver paid for whose ready_by has passed
	// without the seller calling deliver_order. Location-independent
	// per the locked design — fires anywhere in the village so a
	// wandering buyer can't forget about stuck orders. Empty list
	// suppresses the section. Companion to the seller-side
	// "Customers awaiting delivery" block (ZBBS-136) so both sides of
	// a stuck transaction have visibility.
	if entries, err := app.overdueOrdersForBuyer(ctx, r.ID); err != nil {
		log.Printf("perception overdueOrdersForBuyer %s: %v", r.DisplayName, err)
	} else if line := formatOverdueOrdersForPerception(entries, time.Now()); line != "" {
		sections = append(sections, line)
	}

	// 3.0c. Visible needs of co-located others. When the perceiver is
	// inside a structure, surface red-tier-or-higher needs of other
	// NPCs in the same room — "Ezekiel Crane looks hungry." — so a
	// tavernkeeper, healer, or any character reading the room can act
	// on what's plainly visible. Silent for actors at mild tier or
	// below (their needs aren't outwardly obvious yet) and silent for
	// the perceiver themselves (their own state is already in 3a).
	// Threshold and same-structure scope are intentionally hardcoded
	// for now; they'll move into per-attribute config when the
	// attribute_definition table absorbs hunger/thirst/tiredness.
	if r.InsideStructureID.Valid {
		for _, line := range app.visibleNeedsLines(ctx, r.ID, r.InsideStructureID.String) {
			sections = append(sections, line)
		}
	}

	// 3.1 Gatherable affordance — when the NPC is loitering at a source
	// that produces an item (well → water; future orchards, fishing
	// spots), surface it as an explicit prompt line. Without this hint
	// the model can stand at the well and not connect "I'm here" to
	// "call gather()" — chore=well becomes a no-op loop instead of
	// part of a fetch-and-return chain.
	if affordance := app.gatherableHereForActor(ctx, r.CurrentX, r.CurrentY); affordance != "" {
		sections = append(sections, affordance)
	}

	// 3a. Body and purse (ZBBS-082 / ZBBS-083). Each need maps to a
	// period-appropriate descriptor (peckish/hungry/starving etc.); silent
	// when the value is below the awareness floor. Coins remain numeric —
	// money is a thing you count, not a feeling. The whole sentence is
	// omitted when no need is currently surfaced, keeping the perception
	// quiet for a freshly-rested NPC. Thresholds loaded earlier (above
	// section 3) so the visible-needs section can reuse them.
	bodyParts := []string{}
	pressing := []string{}
	pressingTiers := map[string]NeedTier{}
	if l := needLabel("hunger", r.Hunger, hungerT); l != "" {
		bodyParts = append(bodyParts, l)
		if t := needLabelTier(r.Hunger, hungerT); t >= 2 {
			pressing = append(pressing, "hunger")
			pressingTiers["hunger"] = NeedTier(t)
		}
	}
	if l := needLabel("thirst", r.Thirst, thirstT); l != "" {
		bodyParts = append(bodyParts, l)
		if t := needLabelTier(r.Thirst, thirstT); t >= 2 {
			pressing = append(pressing, "thirst")
			pressingTiers["thirst"] = NeedTier(t)
		}
	}
	if l := needLabel("tiredness", r.Tiredness, tiredT); l != "" {
		bodyParts = append(bodyParts, l)
		if t := needLabelTier(r.Tiredness, tiredT); t >= 2 {
			pressing = append(pressing, "tiredness")
			pressingTiers["tiredness"] = NeedTier(t)
		}
	}
	// 2026-05-02: red+ tier needs get an explicit imperative prefix.
	// The "You feel: hungry, parched, tired." line alone wasn't reliably
	// driving NPCs to consume; saw Ezekiel at 18/18/18 (all past
	// distress) call done without consuming despite "You feel" being
	// surfaced. Pulling pressing needs into a dedicated lead-in is a
	// stronger signal that the model should prioritize them on this turn.
	var bodyLine string
	if len(pressing) > 0 {
		bodyLine = fmt.Sprintf("Address now: %s. You feel: %s. ", strings.Join(pressing, ", "), strings.Join(bodyParts, ", "))
	} else if len(bodyParts) > 0 {
		bodyLine = fmt.Sprintf("You feel: %s. ", strings.Join(bodyParts, ", "))
	}
	sections = append(sections, fmt.Sprintf("%sCoins in your purse: %d.", bodyLine, r.Coins))

	// Tired-vendor cue (ZBBS-131 follow-up). When tiredness has crossed
	// the red threshold AND the NPC has a work assignment (so take_break
	// is a meaningful action), nudge them toward closing shop. Without
	// this hint, a tired vendor sees "Address now: tiredness" in the
	// body line above but reaches for consume() (no current item
	// satisfies tiredness) or move_to home (no-op for home==work
	// vendors). Gated solely on tiredness + workLabel; the take_break
	// engagement cooldown is enforced by the dispatcher, so the cue
	// stays simple and lets the engine reject + correct if the vendor
	// is mid-engagement.
	// ZBBS-148: suppress the cue when the vendor is already on break.
	// Otherwise the chronicler keeps attending tired-on-break vendors,
	// the cue says "call take_break", and the LLM picks take_break again
	// — extending the break and restarting the loop. Observed
	// 2026-05-07 18:56-19:02 UTC: John Ellis re-take_broke 3× in 6 min
	// while already on break, each time extending break_until forward.
	onBreak := r.BreakUntil.Valid && r.BreakUntil.Time.After(time.Now())
	if r.Tiredness >= tiredT && workLabel != "" && !onBreak {
		sections = append(sections, "A short break would help — call take_break to close your shop and recover tiredness.")
	}

	// Inventory (ZBBS-091) — items the NPC is carrying. Empty inventory
	// produces no line at all, so a freshly-deployed villager doesn't
	// see "Inventory: nothing" noise. Other actors' inventories are not
	// shown — privacy/realism.
	//
	// ZBBS-114: vendors (actors with the 'serve' role tool) get the
	// inventory line reframed as "Items you can sell" — same data,
	// stricter framing. The vendor role prompts (blacksmith, herbalist,
	// merchant, tavernkeeper) reference this exact label in their
	// grounding rule against off-list offers, so the LLM sees data and
	// constraint together. Non-vendors retain "Your inventory" — for
	// them the line is personal carry, not a sales catalog.
	if inv := app.inventoryLine(ctx, r.ID); inv != "" {
		label := "Your inventory"
		if app.actorIsVendor(ctx, r.ID) {
			label = "Items you can sell"
		}
		sections = append(sections, label+": "+inv+".")
	}

	// ZBBS-136: customers awaiting delivery. Surfaces pay_ledger rows
	// at state=accepted, fulfillment_status=ready so the LLM sees
	// pending handovers directly in the perception, before the
	// satiation block competes for attention. ZBBS-129 step 2 split
	// pay-accept from delivery; without this nudge the seller would
	// only learn about the order via the check_order_book tool, which
	// they rarely call when their own needs are pressing. Limited to
	// the seller side — buyers don't deliver. Empty list suppresses
	// the section entirely.
	if entries, err := app.readyOrdersForSeller(ctx, r.ID); err == nil {
		if line := formatReadyOrdersForPerception(entries, time.Now()); line != "" {
			sections = append(sections, line)
		}
	} else {
		log.Printf("perception readyOrdersForSeller %s: %v", r.DisplayName, err)
	}

	// 3a. Satiation block (ZBBS-123). When a consumable need is
	// pressing (hunger, thirst), surface own-stock + nearby-vendor
	// satisfiers so the LLM has the bridge between "Address now"
	// and the resolution it should pick. See engine/satiation.go.
	if satBlocks := app.buildSatiationLines(ctx, r.ID, r.CurrentX, r.CurrentY, pressingTiers); len(satBlocks) > 0 {
		sections = append(sections, strings.Join(satBlocks, "\n\n"))
	}

	// 3b. Return-to-work nudge (ZBBS-110). Fires when the NPC is on shift
	// but away from their work building, and no need is pressing enough
	// to justify the detour. Suppressed by any tier ≥ 2 need (Address now
	// already commands attention) and by being inside or loitering at
	// work. Placed after the inventory line so "what you have" reads
	// adjacent to "what you should do" — the LLM connects "I'm carrying
	// bread" with "your shift continues" naturally without the nudge text
	// having to reference inventory itself. The matching
	// scheduleReturnToWorkFollowup at end-of-harness uses the same
	// predicate so the perception line and the follow-up self-tick are
	// always in lock-step.
	nowMinuteOfDay := hourStart.Hour()*60 + hourStart.Minute()
	if shouldNudgeReturnToWork(r, r.InsideStructureID, loiteringAtID,
		nowMinuteOfDay, dawnMin, duskMin, hungerT, thirstT, tiredT) {
		sections = append(sections, returnToWorkPerceptionLine(workLabel))
	}

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

	// ZBBS-157: village gossip. Recent shared observations the
	// chronicler (or future auto-authoring path) has recorded.
	// Filtered to exclude gossip where the perceiver IS the subject —
	// nobody overhears gossip about themselves. Cap at 3 lines so the
	// section stays terse; newest-first ordering matches "around-the-
	// village" spoken-cue feel.
	if gossipLines := app.visibleGossipLines(ctx, r.ID, 3); len(gossipLines) > 0 {
		sections = append(sections, "Around the village:\n"+strings.Join(gossipLines, "\n"))
	}

	// ZBBS-158: sealed-note deliveries. Notes a courier has just
	// handed over land here so the recipient may react in their next
	// speak. 1-hour window keeps the section transient — a delivered
	// note becomes background context after; if the recipient hasn't
	// addressed it by then, they probably don't intend to.
	if noteLines := app.visibleDeliveredNotes(ctx, r.ID, time.Hour, 5); len(noteLines) > 0 {
		sections = append(sections, "Notes delivered to you:\n"+strings.Join(noteLines, "\n"))
	}

	// Pending summons targeting this NPC. Visible regardless of where
	// the perceiver is — a messenger reaches you whether you're at home,
	// at work, or in the open village. Falls off as soon as the NPC
	// commits a move/take_break/speak (see summonsTargetingPerceiver).
	if summonsLines := app.summonsTargetingPerceiver(ctx, r.ID, r.DisplayName); len(summonsLines) > 0 {
		sections = append(sections, "Summons for you:\n"+strings.Join(summonsLines, "\n"))
	}

	// Refusal feedback for the summoner. When the messenger reports
	// back that the target couldn't be found, a summon_failed audit
	// row is written for this actor; surface it so the model can
	// react (apologize to a waiting customer, try a different
	// villager, give up). Same fade-after-response rule.
	if failedSummons := app.summonFailedForPerceiver(ctx, r.ID); len(failedSummons) > 0 {
		sections = append(sections, "Your messenger returned with news:\n"+strings.Join(failedSummons, "\n"))
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
		"You can also use recall first if you want to remember anything specific. "+
		"If a customer arrives and you can't or won't serve them right now, use take_break "+
		"instead of refusing them in conversation — that closes your post and walks you home, "+
		"so they understand to come back later instead of standing there to be refused again.")

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
		// person. action_type IN ('speak', 'act', 'pay') unifies verbal,
		// physical, and transactional contributions to the scene's
		// recent history. pay rows surface the just-completed transaction
		// to the recipient as "X paid you N coins for Y" so a reactor-
		// triggered tick has the payment context the trigger reason
		// itself doesn't carry into the prompt.
		`SELECT al.speaker_name, al.action_type, al.payload
		 FROM agent_action_log al
		 WHERE al.action_type IN ('speak', 'act', 'pay')
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
		var name, actionType string
		var payloadJSON []byte
		if err := rows.Scan(&name, &actionType, &payloadJSON); err != nil {
			continue
		}
		var payload map[string]interface{}
		_ = json.Unmarshal(payloadJSON, &payload)
		isSelf := name == perceiverName
		var line string
		switch actionType {
		case "speak":
			detail, _ := payload["text"].(string)
			if detail == "" {
				continue
			}
			if isSelf {
				line = fmt.Sprintf("  You said: \"%s\"", detail)
			} else {
				line = fmt.Sprintf("  %s said: \"%s\"", name, detail)
			}
		case "act":
			detail, _ := payload["verb_phrase"].(string)
			if detail == "" {
				continue
			}
			if isSelf {
				line = fmt.Sprintf("  You %s", detail)
			} else {
				line = fmt.Sprintf("  %s %s", name, detail)
			}
		case "pay":
			text := narratePayForPerceiver(name, payload, perceiverName)
			if text == "" {
				continue
			}
			line = "  " + text
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
	case "check_order_book":
		entries, err := app.checkOrderBookForSeller(ctx, r.ID)
		if err != nil {
			log.Printf("agent-tick %s check_order_book: %v", r.DisplayName, err)
			return "You tried to check the order book but couldn't bring it to mind."
		}
		return formatOrderBookForLLM(entries)
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
// Returns (result, errStr, extra) so the dispatcher can surface the
// outcome of pay attempts back into the model's continuation message.
// `extra` carries tool-specific human-readable detail the caller can
// splice into the [OK] message: gather populates it with "<qty>
// <item> from the <source>" so the admin chat log shows what was
// gathered (the gather tool's input is `{}` by design — the engine
// resolves the product from the loiter slot, so without this the
// chip and the OK both read as content-free). Most tools leave it
// empty.
func (app *App) executeAgentCommit(ctx context.Context, r *agentNPCRow, tc *agentToolCall, sceneID string) (result string, errStr string, extra string) {
	// Augment several payload kinds with structure_id so recent-block
	// queries (perception lookback, talk-panel backload) can answer
	// "what happened here lately" with a single payload->>'structure_id'
	// filter. speak/act stamp because they're conversational and surface
	// in other actors' perceptions; move_to stamps the FROM structure
	// (where the actor was when they decided to leave) so departures
	// appear in the room they left, not the room they're walking to.
	if (tc.Name == "speak" || tc.Name == "act" || tc.Name == "move_to" ||
		tc.Name == "serve" || tc.Name == "pay" || tc.Name == "consume" ||
		tc.Name == "summon") && r.InsideStructureID.Valid {
		if tc.Input == nil {
			tc.Input = map[string]interface{}{}
		}
		tc.Input["structure_id"] = r.InsideStructureID.String
	}
	payload, _ := json.Marshal(tc.Input)
	result = "ok"
	errStr = ""

	switch tc.Name {
	case "move_to":
		dest, _ := tc.Input["destination"].(string)
		if dest == "" {
			result, errStr = "rejected", "missing destination"
			break
		}
		if err := app.executeAgentMoveTo(ctx, r, dest); err != nil {
			if errors.Is(err, errMoveAlreadyAtDest) {
				result, errStr = "rejected", err.Error()
			} else {
				result, errStr = "failed", err.Error()
			}
		}
		// Departure narration for the room. Stamps from_structure_id (the
		// room being LEFT) so subscribers filtering by structure can show
		// "Ezekiel left for home" in the place he just walked away from.
		// kind="departure" lets clients render this as italic narration
		// alongside speech and acts. Also lands a village_event so the
		// Village tab gets the same line — same text, broader scope.
		// narrateMoveDeparture normalizes self-references ("my home" →
		// "home") and renders "retired for the X" when home == work.
		if result == "ok" && r.InsideStructureID.Valid {
			text := app.narrateMoveDeparture(ctx, r.DisplayName, r.HomeStructureID, r.WorkStructureID, dest)
			app.Hub.Broadcast(WorldEvent{
				Type: "room_event",
				Data: map[string]interface{}{
					"actor_id":     r.ID,
					"actor_name":   r.DisplayName,
					"kind":         "departure",
					"text":         text,
					"structure_id": r.InsideStructureID.String,
					"at":           time.Now().UTC().Format(time.RFC3339),
				},
			})
			x, y := r.CurrentX, r.CurrentY
			app.recordVillageEvent(ctx, villageEventDeparture, text, r.ID, r.InsideStructureID.String, &x, &y)
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
		// Vocative stale-addressee guard: parallel ticks mean the
		// speaker's perception was snapshotted seconds before the LLM
		// produced its speech, so an addressee may have left the
		// conversation in the meantime. If the speech directly
		// addresses someone outside the speaker's huddle in vocative
		// position ("Ezekiel, you look hungry"), reject so the LLM
		// retries from a fresh perception. Non-vocative references
		// ("I told Ezekiel to be careful", "I've sent for Prudence")
		// are not rejected here. Co-location is defined by shared
		// current_huddle_id (the conversational unit), not shared
		// inside_structure_id — see findVocativeStaleAddressees.
		absent, err := app.findVocativeStaleAddressees(ctx, r.ID, text)
		if err != nil {
			log.Printf("vocative stale-addressee check failed for %s: %v", r.DisplayName, err)
		} else if len(absent) > 0 {
			result = "rejected"
			errStr = fmt.Sprintf("%s is no longer in your conversation — don't address them by name. Re-check who is present before speaking.", strings.Join(absent, " and "))
			break
		}
		// Mentions (Phase C of sales-and-gifts): structured tag for which
		// item_kinds this speech references. The PC talk panel uses these
		// to populate the buy/pay dropdown so a customer can select an
		// item the vendor just mentioned. Validated strictly against the
		// speaker's actor_inventory — bogus mentions reject the whole
		// speak and the LLM retries cleanly. Normalized to lowercase
		// trimmed strings; empty / non-strings are dropped silently
		// before validation.
		mentions := normalizeMentions(tc.Input["mentions"])
		if len(mentions) > 0 {
			bogus, err := app.validateMentionsAgainstInventory(ctx, r.ID, mentions)
			if err != nil {
				result, errStr = "failed", fmt.Sprintf("validate mentions: %v", err)
				break
			}
			if len(bogus) > 0 {
				result = "rejected"
				errStr = fmt.Sprintf("You don't have these items in your inventory: %s. Only mention items in your 'Items you can sell' list — speak references the same catalog the customer's pay dropdown is built from, so naming goods you don't have would offer the customer something you can't sell.", strings.Join(bogus, ", "))
				break
			}
			// Re-stash the normalized mentions so audit + WS broadcast
			// carry the cleaned form, not the model's raw input.
			tc.Input["mentions"] = mentions
			payload, _ = json.Marshal(tc.Input)
		}
		// Price-quoting (ZBBS-124). When the model emits price alongside
		// mentions, upsert one scene_quote row per mentioned item keyed
		// to the speaker's current huddle. The pay handler reads these
		// rows to enforce that buyer offers honor the seller's stated
		// price — protecting the supply-pressure-becomes-price-pressure
		// design from silent underpayment loopholes.
		//
		// Validation: price must be a non-negative integer; require at
		// least one mention. Any other shape (price without mentions,
		// non-integer, negative) is silently ignored — the speak still
		// goes through, just without recording a quote. Don't reject the
		// speak: the prose value of the speech is real even if the
		// structured price tag is malformed.
		if price, ok := normalizeQuotePrice(tc.Input["price"]); ok && price > 0 && len(mentions) > 0 {
			if err := app.upsertSceneQuotes(ctx, r.ID, mentions, price); err != nil {
				log.Printf("scene_quote upsert for %s (mentions=%v price=%d): %v", r.DisplayName, mentions, price, err)
				// Non-fatal — speak still commits.
			}
		}
		// Speech is instant — no override needed. The Hub broadcast lets
		// any listening clients render the speech bubble in real time.
		// Engine log is the visible-to-humans record until the Godot client
		// gets an npc_spoke handler (see tasks/pending/salem-speech-bubble-ui).
		log.Printf("npc_spoke: %s says %q", r.DisplayName, text)
		spokeData := map[string]interface{}{
			"npc_id": r.ID,
			"name":   r.DisplayName,
			"text":   text,
			"at":     time.Now().UTC().Format(time.RFC3339),
		}
		if len(mentions) > 0 {
			spokeData["mentions"] = mentions
		}
		// Carry structure_id so the talk panel can scope its room log
		// to the current room. World-view speech bubbles ignore this
		// field and render every npc_spoke (PCs see speech bubbles over
		// NPCs across structures); the panel uses it as a filter so a
		// player at the apothecary doesn't see tavern dialogue mixed in.
		//
		// Effective scope: inside_structure_id when indoors; otherwise
		// the nearest structure whose loiter pin is within 64 px (well,
		// lamp post, market square). Outdoor speakers at a loiter ring
		// take that structure's scope so their speech lands in the
		// same talk-panel scope as PCs in the loiter-huddle (#113).
		// See actorStructureScope.
		structureScope := app.actorStructureScope(ctx, r.InsideStructureID, r.CurrentX, r.CurrentY)
		if structureScope != "" {
			spokeData["structure_id"] = structureScope
		}
		app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: spokeData})
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
		if structureScope != "" {
			app.triggerCoLocatedTicks(ctx, structureScope, r.ID, "heard-speech", true, sceneID, r.ID)
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

	case "take_break":
		// take_break is the structured "I'm closing my post — come back later"
		// commit. The engine speaks a brief excuse on the NPC's behalf
		// (composed from the reason field), stamps agent_override_until +
		// break_until, and triggers the close-shop-with-vendor-inside flow
		// detailed below (ZBBS-133). The model should NOT also call speak in
		// the same turn — the tool description says so, but treat a redundant
		// speak as harmless if it happens (the speak path runs first per the
		// categorize order, then take_break adds its own).
		//
		// Already-on-break reject (ZBBS-154): if break_until is in the
		// future the NPC is mid-break. Accepting another take_break would
		// silently extend break_until and kick off a redundant eviction
		// goroutine — the John Ellis 18:56-19:02 re-fire chain from
		// ZBBS-148's commit message. ZBBS-148 closed the cue path (the
		// perception nudge no longer prompts take_break while on break);
		// this reject closes the engine-side gate for any residual prompt
		// path or attend_to nudge that still produces take_break.
		// r.BreakUntil is already loaded by ZBBS-148's row-select change —
		// no new query.
		if r.BreakUntil.Valid && r.BreakUntil.Time.After(time.Now()) {
			result = "rejected"
			errStr = fmt.Sprintf("You're already on break until %s. No need to call take_break again — pick a different action this turn.", r.BreakUntil.Time.Format("15:04"))
			break
		}

		// Engagement cooldown (takeBreakEngagementCooldown): if this NPC's
		// most recent ok-result speak/serve/pay landed inside the cooldown,
		// reject the take_break and feed a corrective error back to the
		// model. The trigger is the heard-speech reactive cascade: a PC's
		// "no thanks, I'm good" fires a forced tick and the model reads
		// the polite refusal as a cue to close shop, even though it just
		// served the customer a minute ago. The settling period forces
		// the model to pick a different verb (or stay silent) until the
		// scene quiets down. agent_action_log already has the right index
		// (idx_agent_action_log_npc on actor_id, occurred_at DESC) so this
		// is a sub-millisecond lookup.
		var lastEngagement sql.NullTime
		if err := app.DB.QueryRow(ctx, `
			SELECT MAX(occurred_at)
			  FROM agent_action_log
			 WHERE actor_id = $1
			   AND result = 'ok'
			   AND action_type IN ('speak', 'serve', 'pay')
		`, r.ID).Scan(&lastEngagement); err != nil {
			log.Printf("take_break engagement-cooldown query for %s: %v (allowing)", r.DisplayName, err)
		} else if lastEngagement.Valid {
			elapsed := time.Since(lastEngagement.Time)
			if elapsed < takeBreakEngagementCooldown {
				result = "rejected"
				errStr = fmt.Sprintf("You engaged with the room (speak / serve / pay) %s ago. Wait at least %s of quiet since your last engagement before closing your post — pick a different action this turn.", elapsed.Round(time.Second), takeBreakEngagementCooldown)
				break
			}
		}

		reason, _ := tc.Input["reason"].(string)
		untilHour := coerceIntInput(tc.Input["until_hour"])

		// Compose break_until. Default: NOW + 4h. If until_hour given,
		// resolve to that hour today and reject if it's already past —
		// take_break is a "back later today" verb, not an overnight
		// closure. Overnight is the sleep mechanic. Rejecting forces the
		// model to either pick a later hour or omit the field; the engine
		// returns the current hour in the error so the model has the
		// time anchor it needs to retry. Cap remains as a defensive
		// invariant; with the reject path it's strictly redundant.
		now := time.Now()
		var breakUntil time.Time
		if untilHour > 0 && untilHour < 24 {
			y, mo, d := now.Date()
			candidate := time.Date(y, mo, d, untilHour, 0, 0, 0, now.Location())
			if !candidate.After(now) {
				result = "rejected"
				errStr = fmt.Sprintf("until_hour=%d is already past today (current hour is %d). Pick a later hour or omit until_hour to get a 4-hour break.", untilHour, now.Hour())
				break
			}
			breakUntil = candidate
		} else {
			breakUntil = now.Add(4 * time.Hour)
		}
		if breakUntil.After(now.Add(24 * time.Hour)) {
			breakUntil = now.Add(24 * time.Hour)
		}

		// Compose the spoken excuse. Reason is a short fragment ("feeling
		// unwell"); the template wraps it. Empty or sub-threshold reason
		// → generic line.
		//
		// ZBBS-139: enforce a minimum reason length. Observed 2026-05-07
		// John Ellis emitting `reason="I"` which composed to `"I must
		// close for now — I. Please come back later."` — clearly a
		// truncated / garbled LLM output the engine accepted verbatim.
		// 8 chars is short enough to allow "too busy" / "tired now" but
		// long enough to reject single-letter / one-syllable noise.
		// Trim is applied after the length check so a reason like
		// "  I  " (5 chars before trim, 1 after) also falls back.
		const takeBreakReasonMinChars = 8
		trimmedReason := strings.TrimSpace(reason)
		var excuse string
		if len(trimmedReason) >= takeBreakReasonMinChars {
			excuse = fmt.Sprintf("I must close for now — %s. Please come back later.", trimmedReason)
		} else {
			excuse = "I must close for now — please come back later."
		}

		// Surface the spoken excuse the same way the "speak" branch does:
		// a single npc_spoke broadcast that the talk panel renders with
		// the speaker's name. Don't reuse the speak case directly so the
		// audit row carries action_type='take_break' (not 'speak') —
		// searches for break events should find them under their own type.
		log.Printf("npc_spoke: %s says %q (take_break)", r.DisplayName, excuse)
		spokeData := map[string]interface{}{
			"npc_id": r.ID,
			"name":   r.DisplayName,
			"text":   excuse,
			"at":     time.Now().UTC().Format(time.RFC3339),
		}
		if r.InsideStructureID.Valid {
			spokeData["structure_id"] = r.InsideStructureID.String
		}
		app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: spokeData})

		// Stash excuse + breakUntil into the payload so the audit row carries
		// the full context. structure_id was already injected above for the
		// FROM-room (where the NPC was when they decided to close).
		tc.Input["excuse"] = excuse
		tc.Input["break_until"] = breakUntil.Format(time.RFC3339)
		payload, _ = json.Marshal(tc.Input)

		// ZBBS-133: redesigned take_break is "close-shop-with-vendor-
		// inside" — the vendor stays put. The previous executeAgentMoveTo
		// to "home" is gone; the structure becomes closed (derived from
		// break_until in canEnter / isStructureClosed checks). For
		// interior-occupied structures, an eviction sequence kicks off
		// asynchronously to ask any non-exempt customers to leave.
		// Stamp agent_override_until + break_until + last_shift_tick_at +
		// last_tiredness_recovery_at. agent_override_until keeps the
		// scheduler stepping aside during the break. break_until (ZBBS-102)
		// is the explicit "I'm closed for business" stamp the knock
		// narration reads — distinct from override so a routine move_to
		// doesn't misrepresent the vendor as on break. last_shift_tick_at
		// is forward-stamped so the worker scheduler doesn't re-fire
		// shift_start during the break window. last_tiredness_recovery_at
		// (ZBBS-141) is the cursor read by runTirednessRecoverySweep —
		// stamping it to NOW() means the sweep starts crediting recovery
		// from the exact moment the break begins.
		// last_tiredness_recovery_at is preserved when re-committing
		// take_break during an active break — the existing cursor reflects
		// recovery already accrued but not yet swept; resetting it would
		// drop that minutes-of-recovery window. Otherwise stamped to NOW()
		// to start a fresh recovery window for this break.
		if _, err := app.DB.Exec(ctx,
			`UPDATE actor
			    SET agent_override_until = $2,
			        break_until = $2,
			        last_shift_tick_at = $2,
			        last_tiredness_recovery_at = CASE
			            WHEN break_until IS NOT NULL
			             AND break_until > NOW()
			             AND last_tiredness_recovery_at IS NOT NULL
			            THEN last_tiredness_recovery_at
			            ELSE NOW()
			        END
			  WHERE id = $1`,
			r.ID, breakUntil,
		); err != nil {
			log.Printf("take_break: stamp override %s: %v", r.DisplayName, err)
			result, errStr = "failed", err.Error()
		}

		// Eviction sequence (ZBBS-133): for interior structures with an
		// 'allowed' entry policy, run the three-phase ask/assert/eject
		// sequence in a goroutine. Skip for 'none' (market stalls — the
		// closed state is the entire signal; nobody's inside) and for
		// vendors without a work_structure_id assigned. setNPCInside's
		// closed-door gate already prevents new entries during the break,
		// so any actors NOT inside at take_break time can't sneak in.
		if result == "ok" && r.WorkStructureID.Valid {
			var entryPolicy string
			if err := app.DB.QueryRow(ctx,
				`SELECT entry_policy FROM village_object WHERE id = $1`,
				r.WorkStructureID.String,
			).Scan(&entryPolicy); err != nil {
				log.Printf("take_break: lookup entry_policy %s: %v", r.WorkStructureID.String, err)
			} else if entryPolicy != "none" {
				agent := r.LLMMemoryAgent
				app.startEvictionSequence(ctx, r.ID, r.DisplayName, agent, r.WorkStructureID.String, reason)
			}
		}

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
		item, _ := tc.Input["item"].(string)
		qty := coerceIntInput(tc.Input["qty"])
		if qty == 0 {
			qty = coerceIntInput(tc.Input["quantity"])
		}
		// Default consume_now=true — the tavern flow is the common case
		// and the LLM saying "pay 2 for ale" historically means drink it.
		// Take-home requires the buyer to explicitly set consume_now:false.
		consumeNow := true
		if v, ok := tc.Input["consume_now"].(bool); ok {
			consumeNow = v
		}
		// Phase C: optional consumers list for at-source group orders.
		// Same defensive parsing as serve recipients (handles []interface{},
		// bare string, and JSON-array-as-string variants from providers).
		var consumerNames []string
		if raw, ok := tc.Input["consumers"].([]interface{}); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok {
					consumerNames = append(consumerNames, s)
				}
			}
		} else if s, ok := tc.Input["consumers"].(string); ok {
			trimmed := strings.TrimSpace(s)
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				var parsed []string
				if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
					consumerNames = parsed
				} else {
					consumerNames = []string{s}
				}
			} else if trimmed != "" {
				consumerNames = []string{trimmed}
			}
		}
		inResponseTo := coerceInt64Input(tc.Input["in_response_to"])
		pr := app.executePay(ctx, r, payRequest{
			RecipientName: recipient,
			Amount:        amount,
			ForText:       forText,
			Item:          item,
			Qty:           qty,
			ConsumeNow:    consumeNow,
			ConsumerNames: consumerNames,
			SceneID:       sceneID,
			InResponseTo:  inResponseTo,
		})
		result = pr.Result
		errStr = pr.Err
		// Surface the ledger row id when the recipient countered, so the
		// buyer NPC's next tool-result message can echo it back into a
		// pay() retry via in_response_to. Empty for every other outcome
		// (root pays don't need it; declined/accepted are terminal).
		if pr.Result == "countered" && pr.LedgerID > 0 {
			extra = strconv.FormatInt(pr.LedgerID, 10)
		}
		// Parallel narration broadcast for the talk panel. Mirrors the
		// act/departure pattern — pay events are observable by anyone in
		// the room, and silently moving coins/items would leave the
		// player wondering what just happened. Skipped for non-ok or
		// when there's no room scope (open village).
		if result == "ok" && r.InsideStructureID.Valid {
			text := narratePay(r.DisplayName, tc.Input)
			if text != "" {
				app.Hub.Broadcast(WorldEvent{
					Type: "room_event",
					Data: map[string]interface{}{
						"actor_id":     r.ID,
						"actor_name":   r.DisplayName,
						"kind":         "pay",
						"text":         text,
						"structure_id": r.InsideStructureID.String,
						"at":           time.Now().UTC().Format(time.RFC3339),
					},
				})
			}
			// Post-pay reactor tick (ZBBS-126). Give the recipient a
			// chance to speak a thanks/farewell after the transaction
			// lands. Inherits this cascade's sceneID (the same UUID
			// every reactor in this scene shares) so the recipient's
			// acknowledgment groups with the rest of the conversation.
			// Goroutine + force=true match the PC-pay path's reasoning.
			if pr.RecipientIsAgent && pr.RecipientID != "" {
				recipientID := pr.RecipientID
				buyerID := r.ID
				localScene := sceneID
				go func() {
					app.triggerImmediateTick(context.Background(), recipientID, "npc-paid-you", true, localScene, buyerID)
				}()
			}
		}

	case "consume":
		item, _ := tc.Input["item"].(string)
		qty := coerceIntInput(tc.Input["qty"])
		if qty == 0 {
			qty = coerceIntInput(tc.Input["quantity"])
		}
		if qty == 0 {
			qty = 1
		}
		cr := app.executeConsume(ctx, r, item, qty)
		result = cr.Result
		errStr = cr.Err
		// Room narration: an NPC eating/drinking alone in the tavern is
		// part of the scene. Verb selection (eats/drinks/rests) comes
		// from the item's satisfies_attribute so "Jefferey drinks ale"
		// reads naturally rather than the generic "consumes".
		if result == "ok" && r.InsideStructureID.Valid {
			attr := app.itemAttributeFor(ctx, item)
			text := narrateConsume(r.DisplayName, tc.Input, attr)
			if text != "" {
				app.Hub.Broadcast(WorldEvent{
					Type: "room_event",
					Data: map[string]interface{}{
						"actor_id":     r.ID,
						"actor_name":   r.DisplayName,
						"kind":         "consume",
						"text":         text,
						"structure_id": r.InsideStructureID.String,
						"at":           time.Now().UTC().Format(time.RFC3339),
					},
				})
			}
		}

	case "serve":
		item, _ := tc.Input["item"].(string)
		qty := coerceIntInput(tc.Input["qty"])
		if qty == 0 {
			qty = coerceIntInput(tc.Input["quantity"])
		}
		if qty == 0 {
			qty = 1
		}
		// Default consume_now=true — the tavern flow (immediate eat/drink)
		// is the common case. Take-home requires the model to pass
		// consume_now=false explicitly.
		consumeNow := true
		if v, ok := tc.Input["consume_now"].(bool); ok {
			consumeNow = v
		}
		// Gift defaults to false (Phase C of sales-and-gifts). Without
		// gift=true the serve rejects and the model is nudged toward
		// the buyer-initiated pay() path. Only true gifts (free goods,
		// samples, comps, charity) opt in.
		gift := false
		if v, ok := tc.Input["gift"].(bool); ok {
			gift = v
		}
		// recipients arrives as []interface{} from JSON; coerce to []string.
		// Tolerate two provider quirks: (a) single-element lists arriving
		// as a bare string, (b) the entire array re-serialized as a JSON
		// string (e.g. `"[\"Wendy\",\"Jefferey\"]"`). Without (b) the
		// model's "two recipients" call lands as one impossibly-named
		// recipient and gets rejected; saw this on real serves.
		var recipientNames []string
		if raw, ok := tc.Input["recipients"].([]interface{}); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok {
					recipientNames = append(recipientNames, s)
				}
			}
		} else if s, ok := tc.Input["recipients"].(string); ok {
			trimmed := strings.TrimSpace(s)
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				var parsed []string
				if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
					recipientNames = parsed
				} else {
					recipientNames = []string{s}
				}
			} else {
				recipientNames = []string{s}
			}
		}
		// Clear up front so a prior successful serve in this tick can't
		// leak into a later serve's tool-result message via the stash —
		// matches the comment on agentNPCRow.LastServeResult.
		r.LastServeResult = nil
		sr := app.executeServe(ctx, r, serveRequest{
			RecipientNames: recipientNames,
			Item:           item,
			Qty:            qty,
			ConsumeNow:     consumeNow,
			Gift:           gift,
		})
		result = sr.Result
		errStr = sr.Err
		// Stash for the harness so the [OK] You served message can
		// suffix satiation notes ("Ezekiel is stuffed.") for any
		// recipient whose relevant need landed at 0. Consumed once
		// read.
		if result == "ok" {
			srCopy := sr
			r.LastServeResult = &srCopy
		}
		// Room narration: serve is the canonical "tavernkeeper hands
		// food/drink to a customer" verb. Without this broadcast a PC
		// who's served sees nothing in the talk panel — the model's
		// speak is optional, the serve mechanics are not. The kind is
		// "serve" so a future client could style it differently if
		// desired; the existing client treats any non-speech kind as
		// narration.
		if result == "ok" && r.InsideStructureID.Valid {
			text := narrateServe(r.DisplayName, tc.Input)
			if text != "" {
				app.Hub.Broadcast(WorldEvent{
					Type: "room_event",
					Data: map[string]interface{}{
						"actor_id":     r.ID,
						"actor_name":   r.DisplayName,
						"kind":         "serve",
						"text":         text,
						"structure_id": r.InsideStructureID.String,
						"at":           time.Now().UTC().Format(time.RFC3339),
					},
				})
			}
		}

	case "gather":
		qty := coerceIntInput(tc.Input["qty"])
		if qty == 0 {
			qty = coerceIntInput(tc.Input["quantity"])
		}
		gr := app.executeGather(ctx, r, qty)
		result = gr.Result
		errStr = gr.Err
		// Surface the resolved item / qty / source on success so the
		// caller's [OK] message can read "You gathered 1 herb from
		// the Apothecary Garden" instead of the content-free "You
		// filled your inventory" — the gather tool's input is `{}`
		// by design (engine resolves the product from the loiter
		// slot), so without this the admin chat log can't tell what
		// was gathered without joining against agent_action_log.
		if result == "ok" {
			itemPhrase := gr.Item
			if gr.Qty > 1 {
				itemPhrase = fmt.Sprintf("%d %s", gr.Qty, pluralize(gr.Item, gr.Qty))
			}
			source := gr.SourceName
			if source == "" {
				source = "the source"
			}
			extra = fmt.Sprintf("%s from the %s", itemPhrase, source)
		}
		// Room narration: wells, orchards etc. are typically outdoors
		// (entry_policy='none' loiter targets), so structure_id won't
		// be set — the line still goes out as a public observable for
		// any client that subscribes to broader event channels. If the
		// gatherer is inside a structure (rare for current sources),
		// the broadcast scopes to that room.
		if result == "ok" {
			text := narrateGather(r.DisplayName, gr.Item, gr.Qty, gr.SourceName)
			if text != "" {
				data := map[string]interface{}{
					"actor_id":   r.ID,
					"actor_name": r.DisplayName,
					"kind":       "gather",
					"text":       text,
					"at":         time.Now().UTC().Format(time.RFC3339),
				}
				if r.InsideStructureID.Valid {
					data["structure_id"] = r.InsideStructureID.String
				}
				app.Hub.Broadcast(WorldEvent{Type: "room_event", Data: data})
			}
		}

	case "summon":
		// Post-ZBBS-107 the summon flow is a multi-leg messenger errand
		// (see summon_errand.go). dispatchSummonErrand inserts the row,
		// stamps the summoner's override, and starts their walk to the
		// summon_point. The ring narration (village_event) and target
		// tick fire later — at summoner-arrival and at messenger
		// delivery respectively, NOT here. The legacy v1 post-commit
		// block that broadcast a room_event and immediately ticked the
		// target was the source of the "John didn't wait for the
		// messenger" behavior — it teleported the summons.
		target, _ := tc.Input["target"].(string)
		reason, _ := tc.Input["reason"].(string)
		sm := app.executeSummon(ctx, r, summonRequest{
			TargetName: target,
			Reason:     reason,
		})
		result = sm.Result
		errStr = sm.Err

	case "deliver_order":
		// ZBBS-129 step 2: finalize an accepted, ready ledger row by
		// flipping fulfillment_status to 'delivered' and applying the
		// physical handover (inventory credit for take-home, applyConsumption
		// for at-source). Validation rejects (missing/wrong row, wrong
		// seller, wrong state) surface as result="rejected" so the LLM
		// can correct and continue. Engine errors surface as "failed".
		ledgerID := coerceInt64Input(tc.Input["ledger_id"])
		dr := app.executeDeliverOrder(ctx, r.ID, ledgerID)
		result = dr.Result
		errStr = dr.Err
		if result == "ok" {
			extra = fmt.Sprintf("%d %s to %s", dr.Qty, dr.ItemKind, dr.BuyerName)
		}

	default:
		result, errStr = "rejected", fmt.Sprintf("unknown commit tool: %s", tc.Name)
	}

	// Write audit row. Errors here are logged but don't propagate — the
	// commit already happened (or already failed); the audit row is a
	// best-effort record.
	//
	// huddle_id (ZBBS-094) is sourced via subquery on the actor row so
	// the latest scene_huddle membership rides along on the audit. For a
	// move_to commit, current_huddle_id is still the FROM huddle at
	// insert time — the walk completion's setNPCInside is async, and the
	// leave/join happens after this insert returns. That matches the
	// move_to's semantic location ("decided to leave from this huddle").
	//
	// EXCEPTION: 'summon' commits (ZBBS-107) don't write an audit row
	// here. The summon flow is now a multi-leg messenger errand and the
	// audit row needs to reflect DELIVERY, not dispatch — otherwise
	// summonsTargetingPerceiver picks it up on the target's next tick
	// while the messenger is still walking, recreating the v1 teleport
	// behavior. summon_errand.go writes the row when the messenger
	// arrives at the target (state messenger_at_target → returning,
	// VA branch). Rejected/failed summons skip the audit entirely; the
	// per-summoner active-errand check in dispatchSummonErrand replaces
	// the audit-log lookback the v1 cooldown used.
	if tc.Name == "summon" {
		return
	}
	_, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, error, huddle_id)
		 VALUES ($1, $2, 'agent', $3, $4, $5, NULLIF($6, ''),
		         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
		r.ID, r.DisplayName, tc.Name, payload, result, errStr,
	)
	if err != nil {
		log.Printf("agent-tick: audit insert %s/%s: %v", r.DisplayName, tc.Name, err)
	}
	return
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

	// Refuse no-op walks (dest resolves to where the NPC already is). Without
	// this the LLM can ask a tavernkeeper whose home == work to "go to the
	// Tavern" and the engine dispatches a 0-step or door-bounce walk plus a
	// phantom "X left for Tavern" narration. See errMoveAlreadyAtDest doc.
	if r.InsideStructureID.Valid && structureID == r.InsideStructureID.String {
		return errMoveAlreadyAtDest
	}

	// Owners (this NPC's home or work) walk to door_offset and flip
	// inside on arrival — same flow scheduled worker arrivals use.
	// Visitors to public-entry structures (tavern, smithy, meeting house)
	// also enter on arrival (ZBBS-099, ZBBS-101). Owner-only structures
	// the visitor isn't associated with, and 'none' policies (wells,
	// market stalls, outhouses), keep the loiter-and-stand-outside
	// behavior.
	enterOnArrival := app.agentMoveShouldEnter(ctx, r, structureID)

	npc := &behaviorNPC{ID: r.ID, CurX: r.CurrentX, CurY: r.CurrentY}
	app.interpolateCurrentPos(npc)
	if err := app.startReturnWalk(ctx, npc, walkX, walkY, structureID, "agent-move", enterOnArrival); err != nil {
		return fmt.Errorf("startReturnWalk: %w", err)
	}

	// Movement fatigue (ZBBS-123). Accrues tiredness on depart so the new
	// value is visible in the next perception build. Uses the interpolated
	// start so a mid-walk redirect costs the remaining-leg distance, not
	// the whole journey from where the previous walk began.
	app.applyMovementFatigue(ctx, r.ID, npc.CurX, npc.CurY, walkX, walkY)

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
			wx, wy := app.pickWalkTarget(ctx, r, hsID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)
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
	wx, wy := app.pickWalkTarget(ctx, r, oID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)
	return oID, wx, wy, nil
}

// pickWalkTarget chooses the walk-to coordinates for an agent-initiated
// move. NPCs entering on arrival (owner moves, post-ZBBS-099 visitor
// moves to anyone-policy structures, and ZBBS-101 owner moves to
// owner-policy structures) walk to door_offset so the inside flip
// happens at the actual doorway. Visitor moves to none-policy /
// owner-policy-they-don't-belong-to destinations are distributed across
// the 8 king's-move slots around the loiter pin via pickVisitorSlot —
// the pin tile itself is the gathering CENTER, never a stand spot.
//
// All offsets are tile-unit ints; multiplied by tileSize=32.0 to get the
// pixel coordinate the walk dispatcher expects.
func (app *App) pickWalkTarget(ctx context.Context, r *agentNPCRow, structureID string, ox, oy float64,
	loiterX, loiterY, doorX, doorY sql.NullInt32, footprintBottom int) (float64, float64) {
	const tileSize = 32.0
	if !app.agentMoveShouldEnter(ctx, r, structureID) {
		lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
		return app.pickVisitorSlot(ctx, r.ID, ox, oy, lx, ly)
	}
	if doorX.Valid && doorY.Valid {
		return ox + float64(doorX.Int32)*tileSize, oy + float64(doorY.Int32)*tileSize
	}
	return ox, oy
}

// isAgentMoveOwner reports whether the destination structure is this
// NPC's home or work. Owner moves walk to door_offset and flip inside
// on arrival; visitor moves walk to the loiter point and stay outside.
func isAgentMoveOwner(r *agentNPCRow, structureID string) bool {
	return (r.HomeStructureID.Valid && r.HomeStructureID.String == structureID) ||
		(r.WorkStructureID.Valid && r.WorkStructureID.String == structureID)
}

// agentMoveShouldEnter returns true when an agent's move/chore arrival
// at structureID should flip them inside the building. Resolution is by
// village_object.entry_policy (ZBBS-101):
//
//   - 'none'   → never enter (decorative, fences, statues).
//   - 'owner'  → enter only if this NPC is the owner (home or work
//                points at this structure). A visiting NPC stands at
//                the loiter slot, same as a PC who clicks an
//                owner-only structure they don't belong to.
//   - 'anyone' → enter on arrival (taverns, public buildings).
//
// Errors fall back to false (don't enter) so a transient DB blip
// produces the more conservative behavior — the NPC stands outside
// rather than mistakenly entering a structure it shouldn't.
func (app *App) agentMoveShouldEnter(ctx context.Context, r *agentNPCRow, structureID string) bool {
	var policy string
	err := app.DB.QueryRow(ctx,
		`SELECT entry_policy FROM village_object WHERE id = $1`,
		structureID,
	).Scan(&policy)
	if err != nil {
		return false
	}
	switch policy {
	case "anyone":
		return true
	case "owner":
		return isAgentMoveOwner(r, structureID)
	default:
		return false
	}
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
	wx, wy := app.pickWalkTarget(ctx, r, oID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)
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
	wx, wy := app.pickWalkTarget(ctx, r, oID, ox, oy, loiterX, loiterY, doorX, doorY, footprintBottom)

	// Chore destinations resolve to a tagged placement (well, outhouse,
	// shop, tavern). agentMoveShouldEnter resolves entry from the
	// per-instance entry_policy + ownership (ZBBS-101): public-policy
	// destinations enter on arrival, owner-policy destinations only
	// enter when this NPC is the owner, and 'none'-policy destinations
	// (wells, outhouses, market stalls) keep the loiter-outside
	// behavior.
	enterOnArrival := app.agentMoveShouldEnter(ctx, r, oID)

	npc := &behaviorNPC{ID: r.ID, CurX: r.CurrentX, CurY: r.CurrentY}
	app.interpolateCurrentPos(npc)
	if err := app.startReturnWalk(ctx, npc, wx, wy, oID, "agent-chore", enterOnArrival); err != nil {
		return fmt.Errorf("startReturnWalk: %w", err)
	}

	// Movement fatigue (ZBBS-123). See executeAgentMoveTo for shape.
	app.applyMovementFatigue(ctx, r.ID, npc.CurX, npc.CurY, wx, wy)

	overrideUntil := time.Now().Add(30 * time.Minute)
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
		r.ID, overrideUntil,
	); err != nil {
		log.Printf("agent-tick: stamp override %s: %v", r.DisplayName, err)
	}
	return nil
}
