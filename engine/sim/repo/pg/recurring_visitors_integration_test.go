package pg

// Real-pg integration tests for the returning-traveler tier (LLM-372). Run
// against embedded Postgres with the full prod-baseline schema + post-baseline
// migrations applied; skipped under `go test -short`.
//
// These prove what spies can't: the recurring_visitor + acquaintance rows and the
// in-flight visitor's recurring_visitor_id link round-trip through the real
// columns of a genuine SaveWorld → LoadWorld, and — the property that
// distinguishes this tier from the visitor mirror — a returner's row SURVIVES the
// traveler's departure (no generation-marker sweep; it outlives the visit).

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// recurringWorld sets up a checkpointable world with one in-flight returner (an
// actor linked via recurring_visitor_id) and its durable recurring_visitor row +
// one PC acquaintance. Times are truncated to microseconds to match Postgres
// timestamptz precision on round-trip.
func recurringWorld(repo sim.Repository, now time.Time) *sim.World {
	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		"vstr-0000abcd": {
			ID: "vstr-0000abcd", DisplayName: "Elias Drum the peddler",
			Kind: sim.KindNPCShared, LLMAgent: sim.VisitorAgentName,
			Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY + 6}, State: sim.StateIdle,
			VisitorState: &sim.VisitorState{
				Archetype: "peddler", Origin: "Boston", Disposition: "weary",
				ExpiresAt: now.Add(2 * time.Hour), Phase: sim.VisitorPhasePresent,
				RecurringID: "rvis-0000abcd",
			},
		},
	}
	w.RecurringVisitors = map[sim.RecurringVisitorID]*sim.RecurringVisitor{
		"rvis-0000abcd": {
			ID: "rvis-0000abcd", Name: "Elias Drum", Archetype: "peddler",
			Origin: "Boston", Disposition: "weary", VisitCount: 3,
			FirstSeenAt: now.Add(-60 * 24 * time.Hour), LastSeenAt: now.Add(-20 * 24 * time.Hour),
			NextReturnAt: now.Add(20 * 24 * time.Hour),
			Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{
				"pc-jeff": {
					PCActorID: "pc-jeff", PCDisplayName: "Jeff",
					FirstMetAt: now.Add(-60 * 24 * time.Hour), LastMetAt: now.Add(-20 * 24 * time.Hour),
				},
			},
		},
	}
	return w
}

// TestIntegration_RecurringVisitor_RoundTrip — a returner's persona + schedule +
// PC familiarity, and the in-flight visitor's recurring_visitor_id link, all
// round-trip through the real columns end to end through SaveWorld → LoadWorld.
func TestIntegration_RecurringVisitor_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	nextReturn := now.Add(20 * 24 * time.Hour)
	w := recurringWorld(repo, now)

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	rvs, err := repo.RecurringVisitors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("RecurringVisitors.LoadAll: %v", err)
	}
	rv := rvs["rvis-0000abcd"]
	if rv == nil {
		t.Fatal("recurring_visitor did not round-trip")
	}
	if rv.Name != "Elias Drum" || rv.Archetype != "peddler" || rv.Origin != "Boston" ||
		rv.Disposition != "weary" || rv.VisitCount != 3 {
		t.Errorf("recurring persona round-trip = %+v", rv)
	}
	if !rv.NextReturnAt.Equal(nextReturn) {
		t.Errorf("NextReturnAt = %v, want %v", rv.NextReturnAt, nextReturn)
	}
	acq := rv.Acquaintances["pc-jeff"]
	if acq == nil || acq.PCDisplayName != "Jeff" {
		t.Fatalf("acquaintance did not round-trip: %+v", rv.Acquaintances)
	}

	vrows, err := repo.Visitors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Visitors.LoadAll: %v", err)
	}
	if lv := vrows["vstr-0000abcd"]; lv == nil || lv.VisitorState.RecurringID != "rvis-0000abcd" {
		t.Fatalf("in-flight visitor's recurring link = %+v", vrows["vstr-0000abcd"])
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if loaded.RecurringVisitors["rvis-0000abcd"] == nil {
		t.Error("recurring visitor not rehydrated into World.RecurringVisitors")
	}
	if v := loaded.Actors["vstr-0000abcd"]; v == nil || v.VisitorState == nil ||
		v.VisitorState.RecurringID != "rvis-0000abcd" {
		t.Errorf("rehydrated visitor lost its recurring link: %+v", loaded.Actors["vstr-0000abcd"])
	}
}

// TestIntegration_RecurringVisitor_SurvivesDeparture — the property that sets this
// tier apart from the visitor mirror: after the in-flight traveler departs (gone
// from w.Actors), the durable recurring_visitor row is STILL present at the next
// checkpoint. No generation-marker sweep — a returner outlives the visit.
func TestIntegration_RecurringVisitor_SurvivesDeparture(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	w := recurringWorld(repo, now)

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (present): %v", err)
	}
	// The traveler departs and is cleaned up; the in-flight visitor row is swept by
	// its own tier. The recurring identity must remain (and gets its next_return_at
	// stamped on departure — modeled here as the row simply staying put).
	delete(w.Actors, "vstr-0000abcd")
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (after depart): %v", err)
	}

	vrows, err := repo.Visitors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Visitors.LoadAll: %v", err)
	}
	if len(vrows) != 0 {
		t.Errorf("visitor rows = %d, want 0 (departed visitor swept by its tier)", len(vrows))
	}
	rvs, err := repo.RecurringVisitors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("RecurringVisitors.LoadAll: %v", err)
	}
	if rvs["rvis-0000abcd"] == nil {
		t.Error("recurring_visitor was swept on the traveler's departure — it must outlive the visit (no delete-stale)")
	}
}
