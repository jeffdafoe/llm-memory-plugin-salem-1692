package pg

// Real-pg integration tests for actor_display_name_excl (LLM-586) — the
// partial, deferrable exclusion constraint that lets DECORATIVE actors share a
// display name while every driven (agent-backed or player) actor stays unique.
//
// These run against the migrated template, so they exercise the constraint the
// migration actually installs rather than a hand-written copy of it. That
// matters here more than usual: the shape is unusual (EXCLUDE USING btree with
// both a WHERE clause and DEFERRABLE INITIALLY DEFERRED), because the obvious
// alternatives are rejected outright by PostgreSQL — CREATE UNIQUE INDEX has
// no deferrability option, and a UNIQUE constraint cannot be partial.
//
// Deferrability is the load-bearing half. The checkpoint upserts the live
// actor set BEFORE deleting stale rows inside ONE transaction (SaveSnapshot),
// so an immediate constraint wedges checkpointing permanently the first time
// an actor is deleted and its name recreated between two checkpoints — the
// LLM-580 durability incident. TestIntegration_ActorDisplayName_CheckpointOrdering
// below replays exactly that ordering.

import (
	"strings"
	"testing"
)

const (
	dnActorA = "aaaaaaaa-0000-4000-8000-000000000001"
	dnActorB = "aaaaaaaa-0000-4000-8000-000000000002"
	dnActorC = "aaaaaaaa-0000-4000-8000-000000000003"
	dnActorD = "aaaaaaaa-0000-4000-8000-000000000004"
)

// insertActor writes one actor row directly. driver selects the class the
// constraint keys on: "" = decorative (neither column set), "agent" = an
// llm_memory_agent link, "login" = a player login.
func insertActor(t *testing.T, f *integrationFixture, id, name, driver string) error {
	t.Helper()
	// Derived from the WHOLE id: these ids share a prefix, and a truncated
	// suffix collided on actor_login_username_key instead — a different
	// constraint failing looks like a pass for the wrong reason.
	var agent, login any
	switch driver {
	case "agent":
		agent = "zbbs-" + id
	case "login":
		login = "user-" + id
	}
	_, err := f.Pool.Exec(t.Context(),
		`INSERT INTO actor (id, display_name, current_x, current_y, llm_memory_agent, login_username)
		 VALUES ($1, $2, 0, 0, $3, $4)`,
		id, name, agent, login)
	return err
}

// TestIntegration_ActorDisplayName_DecorativesMayShareAName — the headline
// case: several ducks all called "Duck" persist side by side.
func TestIntegration_ActorDisplayName_DecorativesMayShareAName(t *testing.T) {
	f := newFixture(t)

	for _, id := range []string{dnActorA, dnActorB, dnActorC} {
		if err := insertActor(t, f, id, "Duck", ""); err != nil {
			t.Fatalf("decorative %q named Duck: %v", id, err)
		}
	}

	var n int
	if err := f.Pool.QueryRow(t.Context(),
		`SELECT count(*) FROM actor WHERE display_name = 'Duck'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("ducks named Duck = %d, want 3", n)
	}
}

// TestIntegration_ActorDisplayName_DrivenActorsStayUnique — the control. The
// exemption must be decorative-only; a driven duplicate is the original
// checkpoint-killing bug. Covers both driver columns, and the mixed pair,
// since the predicate is an OR over the two.
func TestIntegration_ActorDisplayName_DrivenActorsStayUnique(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{"agent vs agent", "agent", "agent"},
		{"login vs login", "login", "login"},
		{"agent vs login", "agent", "login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if err := insertActor(t, f, dnActorA, "Josiah Thorne", tc.first); err != nil {
				t.Fatalf("first insert: %v", err)
			}
			err := insertActor(t, f, dnActorB, "Josiah Thorne", tc.second)
			if err == nil {
				t.Fatal("second driven actor with the same name was accepted; want an exclusion violation")
			}
			if !strings.Contains(err.Error(), "actor_display_name_excl") {
				t.Errorf("error = %v, want a violation naming actor_display_name_excl", err)
			}
		})
	}
}

// TestIntegration_ActorDisplayName_DecorativeMayTakeADrivenName — a decorative
// row sits outside the predicate entirely, so it may duplicate a driven
// actor's name. Pinned because the in-memory gates deliberately mirror this
// (SetActorDisplayName skips the check for a decorative subject); if this ever
// needs forbidding, BOTH have to change.
func TestIntegration_ActorDisplayName_DecorativeMayTakeADrivenName(t *testing.T) {
	f := newFixture(t)

	if err := insertActor(t, f, dnActorA, "Josiah Thorne", "agent"); err != nil {
		t.Fatalf("driven insert: %v", err)
	}
	if err := insertActor(t, f, dnActorB, "Josiah Thorne", ""); err != nil {
		t.Errorf("decorative taking a driven name should be allowed: %v", err)
	}
}

// TestIntegration_ActorDisplayName_WhitespaceDriverIsNotNull — the database
// half of the ActorIsDriven whitespace contract. nilOnEmpty maps ONLY "" to
// NULL, so a whitespace-only agent persists NOT NULL and the row is inside the
// constraint's predicate. If ActorIsDriven trimmed, the in-memory gate would
// call this actor decorative and allow a duplicate that the deferred
// constraint then rejects at COMMIT — a wedged checkpoint, not an error the
// operator sees. Pinned at the boundary so the Go-side unit test cannot be
// right about the wrong thing.
func TestIntegration_ActorDisplayName_WhitespaceDriverIsNotNull(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// A whitespace-only agent, exactly as nilOnEmpty would pass it through.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y, llm_memory_agent)
		 VALUES ($1, 'Josiah Thorne', 0, 0, ' ')`, dnActorA); err != nil {
		t.Fatalf("seed whitespace-agent actor: %v", err)
	}

	var isNull bool
	if err := f.Pool.QueryRow(ctx,
		`SELECT llm_memory_agent IS NULL FROM actor WHERE id = $1`, dnActorA).Scan(&isNull); err != nil {
		t.Fatalf("nullness probe: %v", err)
	}
	if isNull {
		t.Fatal("setup: a whitespace agent stored as NULL — the premise of this test is gone")
	}

	// Being NOT NULL, the row is in the predicate: a second driven actor with
	// the same name must be rejected.
	err := insertActor(t, f, dnActorB, "Josiah Thorne", "agent")
	if err == nil {
		t.Fatal("a whitespace-agent row must occupy the name; the duplicate was accepted")
	}
	if !strings.Contains(err.Error(), "actor_display_name_excl") {
		t.Errorf("error = %v, want a violation naming actor_display_name_excl", err)
	}
}

// TestIntegration_ActorDisplayName_CheckpointOrdering — the reason the
// constraint must be DEFERRABLE, replayed as the checkpoint performs it:
// upsert the live actor set FIRST, delete stale rows SECOND, one transaction.
// An actor removed from the world whose name is taken by a NEW actor collides
// mid-transaction with the not-yet-deleted row. Deferred, the constraint judges
// only the committed end state, where the stale row is gone.
//
// With an immediate constraint this transaction fails, and because it is the
// checkpoint, it fails FOREVER — durability dies villagewide until an operator
// intervenes. That is the LLM-580 incident.
func TestIntegration_ActorDisplayName_CheckpointOrdering(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// The outgoing driven actor.
	if err := insertActor(t, f, dnActorA, "Josiah Thorne", "agent"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Upsert the live set — a DIFFERENT actor now carries the name, while
	//    the outgoing row still exists. This is the colliding moment.
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y, llm_memory_agent)
		 VALUES ($1, 'Josiah Thorne', 0, 0, 'zbbs-josiah-thorne-2')`, dnActorD); err != nil {
		t.Fatalf("upsert live set (deferral should have allowed this): %v", err)
	}

	// 2. Delete the stale row, as SaveSnapshot's gen sweep does.
	if _, err := tx.Exec(ctx, `DELETE FROM actor WHERE id = $1`, dnActorA); err != nil {
		t.Fatalf("delete stale: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: the constraint is not behaving as DEFERRABLE INITIALLY DEFERRED, "+
			"which wedges checkpointing permanently: %v", err)
	}

	var name string
	if err := f.Pool.QueryRow(ctx, `SELECT display_name FROM actor WHERE id = $1`, dnActorD).Scan(&name); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "Josiah Thorne" {
		t.Errorf("surviving actor name = %q, want %q", name, "Josiah Thorne")
	}
}

// TestIntegration_ActorDisplayName_DeferredViolationFailsAtCommit — the other
// half of deferral: a genuine duplicate is not waved through, it is caught at
// COMMIT rather than at the statement. Without this, "deferred" could hide a
// constraint that never fires at all.
func TestIntegration_ActorDisplayName_DeferredViolationFailsAtCommit(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y, llm_memory_agent)
		 VALUES ($1, 'Ezekiel Crane', 0, 0, 'zbbs-a'), ($2, 'Ezekiel Crane', 0, 0, 'zbbs-b')`,
		dnActorA, dnActorB); err != nil {
		t.Fatalf("insert should be accepted at statement time when deferred: %v", err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("two driven actors sharing a name committed; want a violation at COMMIT")
	} else if !strings.Contains(err.Error(), "actor_display_name_excl") {
		t.Errorf("commit error = %v, want a violation naming actor_display_name_excl", err)
	}
}
