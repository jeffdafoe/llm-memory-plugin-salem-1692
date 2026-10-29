package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ClientEventsRepo reads and writes client_event (LLM-880): the client-only
// events the Godot client reports for statistics. Written straight from the
// httpapi route, one row per report — not world state, so no checkpoint path.
type ClientEventsRepo struct {
	pool Pool
}

// NewClientEventsRepo constructs a ClientEventsRepo.
func NewClientEventsRepo(pool Pool) *ClientEventsRepo {
	return &ClientEventsRepo{pool: pool}
}

const insertClientEventSQL = `
INSERT INTO client_event (at, username, actor_id, kind, detail)
VALUES ($1, $2, $3, $4, $5::jsonb)`

const loadClientEventsBaseSQL = `
SELECT at, username, COALESCE(actor_id, ''), kind, detail
  FROM client_event
 WHERE TRUE`

const countClientEventsBaseSQL = `
SELECT date_trunc('day', at AT TIME ZONE 'UTC') AS day, kind, count(*)
  FROM client_event
 WHERE TRUE`

// InsertClientEvent stores one event. An empty ActorID is stored as NULL.
func (r *ClientEventsRepo) InsertClientEvent(ctx context.Context, ev sim.ClientEvent) error {
	var actorID any
	if ev.ActorID != "" {
		actorID = string(ev.ActorID)
	}
	detail := string(ev.Detail)
	if detail == "" {
		detail = "{}"
	}
	if _, err := r.pool.Exec(ctx, insertClientEventSQL, ev.At, ev.Username, actorID, ev.Kind, detail); err != nil {
		return fmt.Errorf("pg client_events insert kind=%s: %w", ev.Kind, err)
	}
	return nil
}

// clientEventWhere appends the filter's bound clauses to q, returning the query
// and its args. Values are always bound, never interpolated.
func clientEventWhere(q string, filter sim.ClientEventFilter) (string, []any) {
	args := []any{}
	add := func(clause string, val any) {
		args = append(args, val)
		q += fmt.Sprintf(clause, len(args))
	}
	if filter.Kind != "" {
		add(" AND kind = $%d", filter.Kind)
	}
	if !filter.Since.IsZero() {
		add(" AND at >= $%d", filter.Since)
	}
	if !filter.Until.IsZero() {
		add(" AND at < $%d", filter.Until)
	}
	return q, args
}

// LoadClientEvents returns up to limit events, newest first.
func (r *ClientEventsRepo) LoadClientEvents(ctx context.Context, filter sim.ClientEventFilter, limit int) ([]sim.ClientEvent, error) {
	if limit <= 0 {
		return []sim.ClientEvent{}, nil
	}
	q, args := clientEventWhere(loadClientEventsBaseSQL, filter)
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY at DESC, id DESC LIMIT $%d", len(args))
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pg client_events load: %w", err)
	}
	defer rows.Close()
	out := []sim.ClientEvent{}
	for rows.Next() {
		var (
			ev      sim.ClientEvent
			actorID string
		)
		if err := rows.Scan(&ev.At, &ev.Username, &actorID, &ev.Kind, &ev.Detail); err != nil {
			return nil, fmt.Errorf("pg client_events load scan: %w", err)
		}
		ev.ActorID = sim.ActorID(actorID)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg client_events load iter: %w", err)
	}
	return out, nil
}

// CountClientEvents returns per-kind counts for each UTC day in the filter's
// window, newest day first.
func (r *ClientEventsRepo) CountClientEvents(ctx context.Context, filter sim.ClientEventFilter) ([]sim.ClientEventDayCount, error) {
	q, args := clientEventWhere(countClientEventsBaseSQL, filter)
	q += " GROUP BY 1, 2 ORDER BY 1 DESC, 2"
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pg client_events count: %w", err)
	}
	defer rows.Close()
	out := []sim.ClientEventDayCount{}
	for rows.Next() {
		var (
			day   time.Time
			c     sim.ClientEventDayCount
			count int64
		)
		if err := rows.Scan(&day, &c.Kind, &count); err != nil {
			return nil, fmt.Errorf("pg client_events count scan: %w", err)
		}
		// date_trunc over "at AT TIME ZONE 'UTC'" yields a timestamp without time
		// zone, which pgx scans as a UTC-located time already.
		c.Day = day.UTC()
		c.Count = int(count)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg client_events count iter: %w", err)
	}
	return out, nil
}
