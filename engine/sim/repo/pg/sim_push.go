package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sim_push.go — pg-backed durable surface for the daily sim-conversation push
// (ZBBS-WORK-376 piece 3). The simpush.Dispatcher orchestrates the day-rollover
// catch-up; this is the DB half it reads/writes: the push cursor (a setting
// row), the agentized-actor roster (the actor table), and one actor's day of
// agent_action_log rows (delegated to the shared queryDayEvents pull). All
// read-only / single-row upsert against the pool, no checkpoint Tx.

// simPushCursorKey is the setting row holding the most recent UTC day
// (YYYY-MM-DD) successfully pushed for ALL agentized actors. Empty / missing =
// "never pushed" → the dispatcher anchors at yesterday on first run rather than
// backfilling unbounded history. Engine-authored state, never read into
// WorldSettings, so writing it does not collide with the load-once setting
// catalog. Matches v1's sim_conversation_last_pushed_day key.
const simPushCursorKey = "sim_conversation_last_pushed_day"

// selectSettingSQL reads one setting value by key. Companion to
// environment.go's upsertSettingSQL (reused here for the cursor write).
const selectSettingSQL = `SELECT value FROM setting WHERE key = $1`

// listAgentizedActorsSQL enumerates actors backed by an llm-memory agent. The
// daily push builds a day-note per agentized actor; un-agentized actors
// (decorative NPCs) have no namespace to write into and are excluded. Matches
// v1's query (sim_conversation_push.go pushSimDay).
const listAgentizedActorsSQL = `
SELECT id, llm_memory_agent
  FROM actor
 WHERE llm_memory_agent IS NOT NULL
   AND llm_memory_agent <> ''`

// SimPushStore is the simpush.Store implementation. Construct with
// NewSimPushStore and hand to simpush.NewDispatcher.
type SimPushStore struct {
	pool Pool
}

// NewSimPushStore builds the store against the given pool.
func NewSimPushStore(pool Pool) *SimPushStore {
	return &SimPushStore{pool: pool}
}

// LastPushedDay returns the push cursor (YYYY-MM-DD), or "" when the row is
// absent (never pushed). A missing row is the expected first-run state, not an
// error.
func (s *SimPushStore) LastPushedDay(ctx context.Context) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, selectSettingSQL, simPushCursorKey).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pg sim_push: read cursor: %w", err)
	}
	return value, nil
}

// SetLastPushedDay stamps the push cursor. Upserts the single setting row.
func (s *SimPushStore) SetLastPushedDay(ctx context.Context, day string) error {
	if _, err := s.pool.Exec(ctx, upsertSettingSQL, simPushCursorKey, day); err != nil {
		return fmt.Errorf("pg sim_push: stamp cursor %q: %w", day, err)
	}
	return nil
}

// AgentizedActors lists every actor with a non-empty llm_memory_agent.
func (s *SimPushStore) AgentizedActors(ctx context.Context) ([]sim.AgentActor, error) {
	rows, err := s.pool.Query(ctx, listAgentizedActorsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg sim_push: list agentized actors: %w", err)
	}
	defer rows.Close()

	actors := []sim.AgentActor{}
	for rows.Next() {
		var id, agent string
		if err := rows.Scan(&id, &agent); err != nil {
			return nil, fmt.Errorf("pg sim_push: scan agentized actor: %w", err)
		}
		actors = append(actors, sim.AgentActor{ID: sim.ActorID(id), Agent: agent})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg sim_push: iterate agentized actors: %w", err)
	}
	return actors, nil
}

// DayEvents returns one actor's [dayStart, dayEnd) events — own committed
// actions plus overheard co-present speech. Delegates to the shared
// queryDayEvents pull (action_log.go), the same query ActionLogRepo.LoadDayEvents
// uses.
func (s *SimPushStore) DayEvents(ctx context.Context, actorID sim.ActorID, dayStart, dayEnd time.Time) ([]sim.SimDayEvent, error) {
	return queryDayEvents(ctx, s.pool, actorID, dayStart, dayEnd)
}
