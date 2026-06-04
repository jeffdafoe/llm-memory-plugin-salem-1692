package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sim_push_integration_test.go — real-pg coverage for SimPushStore (ZBBS-WORK-376
// piece 3): the agentized-actor roster filter, the push-cursor setting
// round-trip, and the DayEvents delegation. The heavy cross-actor query logic is
// covered by action_log_loadday_integration_test.go; DayEvents here is a smoke
// test that the delegation is wired.

// insertActorWithAgent seeds an actor row with an explicit llm_memory_agent.
// Pass a string for a value or nil for SQL NULL. login_username stays NULL so
// the actor_driver_not_both CHECK is satisfied.
func insertActorWithAgent(t *testing.T, ctx context.Context, pool Pool, id, name string, agent any) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y, llm_memory_agent)
		 VALUES ($1, $2, 0, 0, $3)`,
		id, name, agent,
	); err != nil {
		t.Fatalf("seed actor %s: %v", name, err)
	}
}

// TestSimPushStore_AgentizedActors: only actors with a non-empty
// llm_memory_agent are returned (NULL and empty-string agents excluded).
func TestSimPushStore_AgentizedActors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := NewSimPushStore(f.Pool)

	insertActorWithAgent(t, ctx, f.Pool, "11111111-1111-1111-1111-111111111111", "Ezekiel", "salem-ezekiel")
	insertActorWithAgent(t, ctx, f.Pool, "22222222-2222-2222-2222-222222222222", "EmptyAgent", "")  // excluded by <> ''
	insertActorWithAgent(t, ctx, f.Pool, "33333333-3333-3333-3333-333333333333", "Decoration", nil) // excluded by IS NOT NULL

	got, err := s.AgentizedActors(ctx)
	if err != nil {
		t.Fatalf("AgentizedActors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d agentized actors, want 1 (NULL + empty agent excluded): %+v", len(got), got)
	}
	if got[0].Agent != "salem-ezekiel" || got[0].ID != sim.ActorID("11111111-1111-1111-1111-111111111111") {
		t.Errorf("got[0] = %+v, want id=1111… agent=salem-ezekiel", got[0])
	}
}

// TestSimPushStore_CursorRoundTrip: a fresh DB has no cursor (""), a set is
// read back, and a second set updates in place.
func TestSimPushStore_CursorRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := NewSimPushStore(f.Pool)

	got, err := s.LastPushedDay(ctx)
	if err != nil {
		t.Fatalf("LastPushedDay (fresh): %v", err)
	}
	if got != "" {
		t.Errorf("fresh cursor = %q, want empty", got)
	}

	if err := s.SetLastPushedDay(ctx, "2026-06-03"); err != nil {
		t.Fatalf("SetLastPushedDay: %v", err)
	}
	if got, _ := s.LastPushedDay(ctx); got != "2026-06-03" {
		t.Errorf("cursor after set = %q, want 2026-06-03", got)
	}

	if err := s.SetLastPushedDay(ctx, "2026-06-04"); err != nil {
		t.Fatalf("SetLastPushedDay (update): %v", err)
	}
	if got, _ := s.LastPushedDay(ctx); got != "2026-06-04" {
		t.Errorf("cursor after update = %q, want 2026-06-04", got)
	}
}

// TestSimPushStore_DayEvents: the delegation to queryDayEvents is wired —
// an actor's own in-window row comes back. (Interval logic is exhaustively
// covered in action_log_loadday_integration_test.go.)
func TestSimPushStore_DayEvents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := NewSimPushStore(f.Pool)

	const a = "11111111-1111-1111-1111-111111111111"
	seedActorForLog(t, ctx, f.Pool, a, "Ezekiel")
	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"hi"}`, loadDayStart.Add(9*time.Hour), "ok")

	got, err := s.DayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("DayEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].Kind != sim.ActionTypeSpoke || got[0].Payload["text"] != "hi" {
		t.Errorf("got[0] = %+v, want spoke / text=hi", got[0])
	}
}
