package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log.go — durable agent_action_log sink (ZBBS-WORK-376).
//
// Persists committed action rows (the structured sim.DurableActionLogRow the
// cascade action-log subscribers build) to the agent_action_log audit table.
// llm-memory-api's daily sim-conversation push reads that table to feed the
// four stateful NPCs' nightly dream memory. This is the engine half v1 had
// (engine/sim_conversation_push.go, package main) that the v2 rewrite never
// ported, so that durable memory has been frozen since cutover.
//
// Async by design. Append is called inline on the world goroutine (inside the
// action-log event subscribers); it only enqueues onto a buffered channel and
// returns immediately, so the hot action-emit path never blocks on PG. A single
// writer goroutine (Run) drains the channel and INSERTs off the world goroutine
// — the same "keep the slow Tx off the world goroutine" posture the checkpoint
// flow uses (engine/sim/checkpoint.go: clone on-goroutine, write off-goroutine).
//
// On a full buffer Append drops + counts rather than blocking: a backlog means
// the writer is stalled (PG slow/down), and stalling the single world goroutine
// is worse than losing audit rows the operator already has bigger alarms for
// (checkpoints fail against the same DB). A crash loses only the in-flight
// buffer — an accepted window for an append-only audit trail with no cross-row
// consistency at stake.

// actionLogBufferSize bounds the in-memory queue between the world goroutine
// and the writer. At Hannah scale (<10 NPCs, low TPS) the writer keeps it
// near-empty; the buffer only absorbs cascade fan-out bursts. Sized generously
// so a drop means a genuinely stalled writer, not a normal burst.
const actionLogBufferSize = 4096

// actionLogWriteTimeout bounds a single INSERT so a wedged DB can't hang the
// writer goroutine forever.
const actionLogWriteTimeout = 5 * time.Second

// insertActionLogSQL appends one audit row. id is BIGSERIAL (DB-assigned);
// result is always 'ok' (v2 logs committed actions only); error is left NULL;
// huddle_id is NULL for outdoor / pre-huddle actions. payload is cast from a
// JSON text param to jsonb in SQL so the bind value is a plain string.
const insertActionLogSQL = `
INSERT INTO agent_action_log
    (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id)
VALUES ($1, $2, $3, $4, $5::jsonb, 'ok', $6, $7)`

// ActionLogRepo is the durable sim.ActionLogSink. Construct with
// NewActionLogRepo, install on the World via SetActionLogSink, and run its
// writer goroutine via Run for the life of the process.
type ActionLogRepo struct {
	pool    Pool
	ch      chan sim.DurableActionLogRow
	dropped atomic.Uint64
}

// NewActionLogRepo builds the sink. The caller installs it on the World
// (SetActionLogSink) and starts Run in a goroutine.
func NewActionLogRepo(pool Pool) *ActionLogRepo {
	return &ActionLogRepo{
		pool: pool,
		ch:   make(chan sim.DurableActionLogRow, actionLogBufferSize),
	}
}

// Append enqueues a row for the writer goroutine. Non-blocking: on a full
// buffer it drops + counts rather than stall the world goroutine. ctx is
// unused (the enqueue can't block long enough to need cancellation) — it exists
// to satisfy sim.ActionLogSink. Always returns nil; INSERT errors surface on
// the writer goroutine.
func (r *ActionLogRepo) Append(_ context.Context, row sim.DurableActionLogRow) error {
	select {
	case r.ch <- row:
	default:
		n := r.dropped.Add(1)
		// Loud on the first drop, then sparse, so a stalled writer is visible
		// without flooding the log on a sustained backlog.
		if n == 1 || n%256 == 0 {
			log.Printf("pg action_log: buffer full, dropped %d audit row(s) (writer stalled?)", n)
		}
	}
	return nil
}

// Run is the writer goroutine: drain the queue and INSERT each row until ctx is
// cancelled, then make a best-effort final drain of whatever is still buffered
// (so a graceful shutdown doesn't truncate the day) and return. Start it in a
// goroutine after construction; on shutdown cancel ctx AFTER the world goroutine
// has stopped (no more Appends incoming) and BEFORE closing the pool, then wait
// for Run to return.
func (r *ActionLogRepo) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.drainRemaining()
			return
		case row := <-r.ch:
			r.writeOne(row)
		}
	}
}

// drainRemaining writes every row still buffered at shutdown, then returns. The
// non-blocking default case stops the loop once the buffer is empty. Each
// writeOne uses its own fresh context, so the cancelled Run ctx doesn't abort
// these final writes.
func (r *ActionLogRepo) drainRemaining() {
	for {
		select {
		case row := <-r.ch:
			r.writeOne(row)
		default:
			return
		}
	}
}

// writeOne INSERTs a single row against the pool. Errors are logged, not
// returned — best-effort audit sink, and the recorded action has already
// committed in-memory. A per-write timeout (fresh, Background-derived) keeps a
// wedged DB from hanging the writer and survives Run-ctx cancellation during
// the shutdown drain.
func (r *ActionLogRepo) writeOne(row sim.DurableActionLogRow) {
	payload, err := json.Marshal(row.Payload)
	if err != nil {
		log.Printf("pg action_log: marshal payload for actor %q action %q: %v", row.ActorID, row.ActionType, err)
		return
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	// huddle_id is a nullable uuid: empty HuddleID → SQL NULL, not "".
	var huddle any
	if row.HuddleID != "" {
		huddle = string(row.HuddleID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), actionLogWriteTimeout)
	defer cancel()
	if _, err := r.pool.Exec(ctx, insertActionLogSQL,
		string(row.ActorID),    // $1 actor_id (uuid)
		row.OccurredAt,         // $2 occurred_at
		row.Source,             // $3 source
		string(row.ActionType), // $4 action_type
		string(payload),        // $5 payload (::jsonb in SQL)
		row.SpeakerName,        // $6 speaker_name
		huddle,                 // $7 huddle_id (uuid or NULL)
	); err != nil {
		log.Printf("pg action_log: insert actor %q action %q: %v", row.ActorID, row.ActionType, err)
	}
}

// loadDayEventsSQL pulls one actor's day of agent_action_log rows for the daily
// sim-conversation push — the actor's own committed actions PLUS the speech it
// overheard from huddle-mates while co-present. See LoadDayEvents for the CTE
// walkthrough and the v2-vs-v1 differences.
const loadDayEventsSQL = `
WITH actor_seed AS (
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
           al.action_type = 'spoke'
           AND al.huddle_id IS NOT NULL
           AND EXISTS (
               SELECT 1 FROM my_intervals mi
                WHERE mi.huddle_id = al.huddle_id
                  AND al.occurred_at >= mi.start_at
                  AND al.occurred_at <  mi.end_at
           )
       )
   )
 ORDER BY al.occurred_at ASC, al.id ASC`

// LoadDayEvents returns one actor's agent_action_log rows for the [dayStart,
// dayEnd) window: the actor's own committed actions, PLUS speech it overheard
// from huddle-mates while it was co-present. The cross-actor speech is what
// makes the distilled note usable as dream input — a tavernkeeper's day
// reduces to a monologue without the customers' lines, and the model needs the
// full back-and-forth to reflect on the scene (ZBBS-WORK-376, ported from v1's
// engine/sim_conversation_push.go loadDayEvents).
//
// Cross-actor inclusion is bounded by per-huddle presence intervals, not "any
// huddle they touched today" — otherwise an actor who entered a tavern at 23:00
// would have their note flooded with that huddle's speech back to 00:00, from
// before they arrived. Three CTEs build the actor's presence intervals:
//
//   - actor_seed: the actor's most recent row from BEFORE the window (LIMIT 1,
//     DESC). Carries the huddle state at midnight when the actor sits silently
//     in a huddle that spans the day boundary — without it, a keeper who
//     entered at 22:00 yesterday and doesn't act until 09:00 today has no
//     in-window row to anchor an interval, and cross-actor speech from
//     00:00–09:00 is wrongly excluded.
//
//   - actor_raw / actor_rows: a virtual seed row stamped at $2 (day-start,
//     id 0 so it sorts first on a midnight tie — real ids are positive
//     BIGSERIAL) UNIONed with the in-window rows, with LEAD(occurred_at) over
//     the whole set. LEAD spans EVERY row, including NULL-huddle ones, so a
//     transition to elsewhere correctly bounds the preceding huddle's interval.
//
//   - my_intervals: the non-NULL-huddle rows, each [occurred_at, next_at)
//     (next_at falling back to dayEnd for the actor's last row of the day).
//
// The cross-actor predicate then includes another actor's `spoke` row when its
// occurred_at falls inside any such interval for the same huddle.
//
// NULL-huddle invariant: the durable sink stamps huddle_id from the originating
// event's huddle (cascade/action_log.go) — spoke from spoke.HuddleID; paid /
// consumed / delivered / took_break from the actor's CurrentHuddleID; walked
// always "" (arrival precedes any huddle join). DurableActionLogRow.HuddleID ""
// becomes SQL NULL. So huddle_id IS NULL means and only means "actor was not in
// a huddle at insert time," and such rows correctly act as interval boundaries
// rather than presence.
//
// v2 differences from v1:
//   - The cross-actor predicate keys on `spoke` only. v1 also matched `act`
//     (model-narrated physical actions); v2 dropped `act` entirely — the social
//     beat now rides on a real `spoke` row alongside the committed action — so
//     `spoke` is the sole overhearable cross-actor kind.
//   - huddle_id is TEXT here (`hud-<hex>` ids), not v1's uuid — ZBBS-WORK-239
//     dropped its scene_huddle FK and retyped it. The interval logic is
//     type-agnostic; the equality + IS NOT NULL checks port unchanged.
//
// Known limitation (carried from v1): a walk's prior huddle interval extends to
// the actor's arrival row rather than to the moment they actually left (no
// leave-huddle row is logged), so cross-actor speech at the FROM huddle during
// a walk can be attributed to the actor's day. Minor — the walk is short and
// the actor was adjacent to that scene moments earlier.
//
// Empty (non-nil) slice on no rows: a caller marshaling to JSON emits "[]" not
// "null", which the conversation-day endpoint requires.
func (r *ActionLogRepo) LoadDayEvents(ctx context.Context, actorID sim.ActorID, dayStart, dayEnd time.Time) ([]sim.SimDayEvent, error) {
	rows, err := r.pool.Query(ctx, loadDayEventsSQL, string(actorID), dayStart, dayEnd)
	if err != nil {
		return nil, fmt.Errorf("query day events for actor %q: %w", actorID, err)
	}
	defer rows.Close()

	events := []sim.SimDayEvent{}
	for rows.Next() {
		var (
			occurredAt time.Time
			actionType string
			payloadRaw []byte
			speaker    string
		)
		if err := rows.Scan(&occurredAt, &actionType, &payloadRaw, &speaker); err != nil {
			return nil, fmt.Errorf("scan day event for actor %q: %w", actorID, err)
		}
		payload := map[string]any{}
		if len(payloadRaw) > 0 {
			if err := json.Unmarshal(payloadRaw, &payload); err != nil {
				// One malformed payload shouldn't sink the whole day — emit the
				// event with an empty payload so the kind + timestamp + speaker
				// still reach the distiller.
				log.Printf("pg action_log: malformed payload on action_type=%s actor=%q: %v", actionType, actorID, err)
				payload = map[string]any{}
			}
		}
		events = append(events, sim.SimDayEvent{
			At:      occurredAt,
			Kind:    sim.ActionType(actionType),
			Payload: payload,
			Speaker: speaker,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate day events for actor %q: %w", actorID, err)
	}
	return events, nil
}
