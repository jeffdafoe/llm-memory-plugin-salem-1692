package pg

import (
	"context"
	"encoding/json"
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
