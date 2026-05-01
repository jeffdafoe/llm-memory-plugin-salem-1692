package main

// Daily activity push to llm-memory-api's /v1/sim/conversation-day.
//
// The api-side dream pipeline reads conversations/* notes per agent.
// For sim NPCs, the per-call /chat/send transcript writer used to dump
// the raw chat-completion payload (system prompt + JSON-stringified
// user messages + response) — accumulated per-tick, hit 77K+ chars,
// and the dream prefilter couldn't extract signal because its patterns
// are tuned for natural conversation, not API JSON.
//
// Replacement: this push collects each sim NPC's just-completed-day
// agent_action_log rows and POSTs them to the api as a typed event
// payload. The api joins with its own chat_message_texts (multi-party
// scene speech, 1-on-1 chat) and writes a single narrative
// conversations/YYYY-MM-DD-sim-day note. The dream cron then reads
// that one note instead of dozens of bloated raw payloads.
//
// Cadence: registered in runServerTickOnce. On the first tick after a
// real UTC day rolls over, the push fires for every day between the
// last successful push and yesterday. Idempotent — the api endpoint
// upserts on (agent, day), so a double-push on restart is harmless.
//
// Engine doesn't know which agents are dream_mode='sim' (that lives in
// api's agent_configuration). It pushes for every agentized actor and
// the api rejects non-sim ones with 400; we log and skip.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Setting key for the most recent UTC day successfully pushed (any
// agent — push runs across all agentized actors atomically per day).
// Stored as ISO date string (YYYY-MM-DD). Empty / missing means "never
// pushed", in which case we initialize to yesterday on first run rather
// than backfilling unbounded history — the dream cron only reads
// recent days anyway.
const simPushLastPushedDayKey = "sim_conversation_last_pushed_day"

// Package-level HTTP client. One client across all per-agent / per-day
// pushes lets the underlying transport reuse connections during a
// catch-up window. Per-call clients prevent that and add allocation
// churn for no gain.
var simPushHTTPClient = &http.Client{Timeout: 30 * time.Second}

// pushEvent is one row in the api payload — mirrors agent_action_log
// shape minus internal IDs. The api narrates each by kind.
//
// Speaker is the agent_action_log.speaker_name (NPC display name or PC
// character name). Always populated. The api uses it for the line label
// so cross-actor speak/act rows (Ezekiel speaking in John's tavern day)
// render under the correct speaker rather than the day's target agent.
type pushEvent struct {
	At      time.Time              `json:"at"`
	Kind    string                 `json:"kind"`
	Payload map[string]interface{} `json:"payload"`
	Speaker string                 `json:"speaker,omitempty"`
}

type simPushBody struct {
	Agent  string      `json:"agent"`
	Day    string      `json:"day"`
	Events []pushEvent `json:"events"`
}

// dispatchSimConversationPush is registered in runServerTickOnce. Cheap
// when there's nothing to push (single setting read + date compare).
//
// Walking the catch-up window day-by-day (rather than batching) keeps
// the api endpoint simple — one push, one day, one note. A long
// downtime would mean several pushes back-to-back, but that's the
// recovery path, not the steady state.
func (app *App) dispatchSimConversationPush(ctx context.Context) {
	lastPushed := app.loadSetting(ctx, simPushLastPushedDayKey, "")
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")

	// First-run init: skip backfill, anchor at yesterday so the next
	// tick after midnight starts pushing forward. Avoids dumping weeks
	// of pre-existing agent_action_log rows on a fresh deploy.
	if lastPushed == "" {
		if err := app.upsertSetting(ctx, simPushLastPushedDayKey, yesterday); err != nil {
			log.Printf("sim-push: init last-pushed-day: %v", err)
		}
		return
	}

	// Validate the stored cursor before using it for comparison or
	// arithmetic — a malformed value would (a) lex-compare incorrectly
	// against yesterday and (b) cause the catch-up loop to spin if
	// nextDay can't advance it.
	if _, err := time.Parse("2006-01-02", lastPushed); err != nil {
		log.Printf("sim-push: invalid %s=%q: %v (re-anchoring to yesterday)", simPushLastPushedDayKey, lastPushed, err)
		if uerr := app.upsertSetting(ctx, simPushLastPushedDayKey, yesterday); uerr != nil {
			log.Printf("sim-push: re-anchor: %v", uerr)
		}
		return
	}

	// Already caught up.
	if lastPushed >= yesterday {
		return
	}

	// Catch-up loop: push each day from lastPushed+1 through yesterday.
	// Bounded by the gap; in steady state runs at most once per real
	// UTC day.
	day, err := nextDay(lastPushed)
	if err != nil {
		log.Printf("sim-push: advance from %q: %v", lastPushed, err)
		return
	}
	for day <= yesterday {
		if err := app.pushSimDay(ctx, day); err != nil {
			log.Printf("sim-push: %s: %v", day, err)
			return
		}
		if err := app.upsertSetting(ctx, simPushLastPushedDayKey, day); err != nil {
			log.Printf("sim-push: stamp last-pushed-day %s: %v", day, err)
			return
		}
		day, err = nextDay(day)
		if err != nil {
			log.Printf("sim-push: advance from %q: %v", day, err)
			return
		}
	}
}

// nextDay returns the YYYY-MM-DD day after the given one. Returns
// an error rather than silently echoing the input on parse failure
// so the dispatch loop can abort cleanly instead of spinning on a
// malformed cursor.
func nextDay(dayStr string) (string, error) {
	t, err := time.Parse("2006-01-02", dayStr)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02"), nil
}

// pushSimDay walks every agentized actor and pushes one (agent, day)
// payload to the api. Errors on individual agents are logged and
// skipped — one bad actor doesn't block the rest. Returns an error
// only on a fatal precondition (DB unavailable, etc.).
func (app *App) pushSimDay(ctx context.Context, day string) error {
	dayStart, err := time.Parse("2006-01-02", day)
	if err != nil {
		return fmt.Errorf("parse day %q: %w", day, err)
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	rows, err := app.DB.Query(ctx,
		`SELECT id, llm_memory_agent FROM actor
		 WHERE llm_memory_agent IS NOT NULL
		   AND llm_memory_agent <> ''`)
	if err != nil {
		return fmt.Errorf("list agentized actors: %w", err)
	}
	type actorRow struct {
		id    string
		agent string
	}
	var actors []actorRow
	for rows.Next() {
		var ar actorRow
		if err := rows.Scan(&ar.id, &ar.agent); err != nil {
			rows.Close()
			return fmt.Errorf("scan actor row: %w", err)
		}
		actors = append(actors, ar)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate actors: %w", err)
	}

	// Track whether any agent's push had a retryable failure. If so
	// we want the dispatcher to NOT stamp the day as pushed, so the
	// next tick re-runs the same window. Per-agent retries are cheap
	// because the api endpoint upserts on (agent, day).
	//
	// 400 / 404 from the api are non-fatal (agent isn't sim-mode, or
	// isn't known) — those are caught inside postSimDay and returned
	// as nil so the day still completes.
	hadFailure := false
	for _, a := range actors {
		events, err := app.loadDayEvents(ctx, a.id, dayStart, dayEnd)
		if err != nil {
			log.Printf("sim-push: %s %s: load events: %v", a.agent, day, err)
			hadFailure = true
			continue
		}
		if err := app.postSimDay(ctx, a.agent, day, events); err != nil {
			log.Printf("sim-push: %s %s: post: %v", a.agent, day, err)
			hadFailure = true
			continue
		}
	}
	if hadFailure {
		return fmt.Errorf("one or more agent pushes failed for %s", day)
	}
	return nil
}

// loadDayEvents pulls agent_action_log rows for one actor in the day
// window — both the actor's own rows AND co-located other-actor speak/act
// rows in the same scene_huddle (ZBBS-094). The cross-actor pull is what
// makes the distilled note useful as dream input: a tavernkeeper's day
// reduces to a monologue without the customers' contributions, since the
// model needs the full back-and-forth to reflect on the scene.
//
// Cross-actor inclusion is bounded by per-huddle presence intervals
// rather than "any huddle they touched today". Without bounds, an actor
// who entered a tavern at 23:00 would have their note flooded with
// every speak/act in that huddle since 00:00 — pre-arrival speech they
// were nowhere near.
//
// Three CTEs build the actor's presence intervals:
//
//   - actor_seed: the actor's most recent audit row from BEFORE the
//     day window (LIMIT 1, DESC). Provides the carryover huddle state
//     at midnight when the actor sits silently in a huddle that spans
//     the day boundary. Without this seed, a tavernkeeper who entered
//     the Tavern at 22:00 yesterday and doesn't act until 09:00 today
//     has no in-window stamped row to anchor an interval, and
//     cross-actor speech in the Tavern from 00:00–09:00 would be
//     wrongly excluded.
//
//   - actor_rows: union of (a virtual seed row at $2/day-start carrying
//     the seed huddle) plus the actor's in-window rows, with LEAD over
//     the combined set. The seed row's id is 0; real ids are positive
//     BIGSERIAL, so the seed sorts first deterministically when
//     occurred_at ties at exactly midnight. LEAD looks at EVERY row
//     including outdoor (NULL huddle) rows so a transition-to-
//     elsewhere correctly bounds the previous huddle's interval.
//
//   - my_intervals: filter to rows with huddle_id IS NOT NULL, use
//     occurred_at as start_at and LEAD'd next_at as end_at (or
//     end-of-day if this was the actor's last row).
//
// The cross-actor predicate uses EXISTS over my_intervals to check
// whether the other-actor row's occurred_at falls inside any such
// interval for the same huddle.
//
// NULL-huddle invariant: every INSERT into agent_action_log stamps
// huddle_id from the actor's current_huddle_id, and current_huddle_id
// is maintained synchronously by joinOrCreateHuddle / leaveHuddle
// (engine/scene_huddles.go) which fire from setNPCInside whenever the
// actor's inside_structure_id flips. Every structure entry creates/
// joins a huddle (engine/scene_huddles.go:12 — solo occupants get one
// too). So huddle_id IS NULL on an audit row means and only means
// "actor was outdoors at insert time." The interval logic relies on
// this: outdoor rows correctly act as boundaries that end any
// preceding huddle interval.
//
// Known limitation: a move_to row stamps the FROM huddle (because
// current_huddle_id isn't cleared until the async leave fires after
// walk completion) and contributes [move_at, next_row_at) as a
// presence interval. The actor was actually walking away during that
// span, so cross-actor speech at the FROM huddle during the walk gets
// included in the actor's note even though they'd left. Minor — the
// walk is short, and contextually the actor JUST left the scene so
// the tail-end speech is still adjacent to their day. A second
// limitation: silent walk-OUT-to-outdoors followed by no in-window
// rows preserves the seed/last-known huddle past the silent leave.
// Both would be tightened by a leaveHuddle audit row, deferred.
//
// Empty result is fine — the api treats no-events as "skip writing the
// note".
func (app *App) loadDayEvents(ctx context.Context, actorID string, dayStart, dayEnd time.Time) ([]pushEvent, error) {
	rows, err := app.DB.Query(ctx,
		`WITH actor_seed AS (
		     SELECT huddle_id
		       FROM agent_action_log
		      WHERE actor_id = $1
		        AND occurred_at < $2
		        AND result = 'ok'
		      ORDER BY occurred_at DESC, id DESC
		      LIMIT 1
		 ),
		 actor_raw AS (
		     SELECT huddle_id, $2::timestamptz AS occurred_at, 0::bigint AS id
		       FROM actor_seed
		     UNION ALL
		     SELECT huddle_id, occurred_at, id
		       FROM agent_action_log
		      WHERE actor_id = $1
		        AND occurred_at >= $2
		        AND occurred_at < $3
		        AND result = 'ok'
		 ),
		 actor_rows AS (
		     SELECT huddle_id,
		            occurred_at,
		            LEAD(occurred_at) OVER (ORDER BY occurred_at, id) AS next_at
		       FROM actor_raw
		 ),
		 my_intervals AS (
		     SELECT huddle_id,
		            occurred_at AS start_at,
		            COALESCE(next_at, $3::timestamptz) AS end_at
		       FROM actor_rows
		      WHERE huddle_id IS NOT NULL
		 )
		 SELECT al.occurred_at, al.action_type, al.payload, al.speaker_name
		   FROM agent_action_log al
		  WHERE al.occurred_at >= $2
		    AND al.occurred_at < $3
		    AND al.result = 'ok'
		    AND (
		        al.actor_id = $1
		        OR (
		            al.action_type IN ('speak', 'act')
		            AND al.huddle_id IS NOT NULL
		            AND EXISTS (
		                SELECT 1 FROM my_intervals mi
		                 WHERE mi.huddle_id = al.huddle_id
		                   AND al.occurred_at >= mi.start_at
		                   AND al.occurred_at <  mi.end_at
		            )
		        )
		    )
		  ORDER BY al.occurred_at ASC, al.id ASC`,
		actorID, dayStart, dayEnd)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var events []pushEvent
	for rows.Next() {
		var occurredAt time.Time
		var actionType string
		var payloadRaw []byte
		var speakerName string
		if err := rows.Scan(&occurredAt, &actionType, &payloadRaw, &speakerName); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var payload map[string]interface{}
		if len(payloadRaw) > 0 {
			if err := json.Unmarshal(payloadRaw, &payload); err != nil {
				// Don't drop the whole batch on one malformed payload;
				// emit the event with an empty payload so the api still
				// gets the kind + timestamp.
				log.Printf("sim-push: malformed payload on action_type=%s: %v", actionType, err)
				payload = map[string]interface{}{}
			}
		} else {
			payload = map[string]interface{}{}
		}
		events = append(events, pushEvent{
			At:      occurredAt,
			Kind:    actionType,
			Payload: payload,
			Speaker: speakerName,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return events, nil
}

// postSimDay POSTs one (agent, day, events) batch to the api endpoint.
// 400 on non-sim agents is non-fatal — the api filters by dream_mode
// and rejects non-sim, but engine doesn't know dream_mode and pushes
// for everyone agentized. We log+skip those.
func (app *App) postSimDay(ctx context.Context, agent, day string, events []pushEvent) error {
	body := simPushBody{
		Agent:  agent,
		Day:    day,
		Events: events,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimRight(app.LLMMemoryURL, "/") + "/v1/sim/conversation-day"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+app.npcChatClient.engineKey)

	resp, err := simPushHTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	// Cap the body read so a misbehaving response can't balloon engine
	// memory. 64KB is plenty for the small JSON the api returns.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode == 400 {
		// Most common cause: agent isn't dream_mode='sim'. Non-fatal.
		log.Printf("sim-push: %s %s: api 400 (likely non-sim): %s", agent, day, string(respBody))
		return nil
	}
	if resp.StatusCode == 404 {
		// Agent unknown to api — also non-fatal (engine has actors api
		// hasn't been told about, e.g. decorative NPCs without an
		// agent_configuration row).
		log.Printf("sim-push: %s %s: api 404 (unknown agent): %s", agent, day, string(respBody))
		return nil
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("api %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
