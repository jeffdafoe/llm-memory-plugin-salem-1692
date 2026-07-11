package pg

// Real-pg integration tests for the visitor mirror (LLM-369). Run against
// embedded Postgres with the full prod-baseline schema + post-baseline
// migrations applied; skipped under `go test -short`.
//
// These prove the parts spies can't: the in-flight visitor subset survives a
// genuine SaveWorld → LoadWorld roundtrip (persisted by the visitor tier,
// skipped by the actor aggregate, rehydrated by FinalizeLoad), the persona +
// placement round-trip through the real columns, and a departed visitor's row is
// swept by the gen-marker delete-stale.

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestIntegration_Visitor_RoundTrip — a visitor and a normal NPC checkpoint
// together; the visitor is persisted by the visitor tier (not the actor
// aggregate, which skips VisitorState != nil), round-trips its persona +
// position through the real columns, and — because its stay is still in-window —
// is rehydrated into loaded.Actors by FinalizeLoad. The normal NPC round-trips
// via the actor aggregate, proving the two partition the actor set cleanly.
func TestIntegration_Visitor_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	// Wall-clock future so rehydrateVisitorsOnLoad resumes it (the reconcile
	// compares ExpiresAt to time.Now, not the world clock).
	expiresAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		"vstr-0000abcd": {
			ID: "vstr-0000abcd", DisplayName: "Elias Drum the peddler",
			Kind: sim.KindNPCShared, LLMAgent: sim.VisitorAgentName,
			Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY + 6}, State: sim.StateIdle,
			VisitorState: &sim.VisitorState{
				Archetype: "peddler", Origin: "Boston", Disposition: "weary",
				ExpiresAt: expiresAt, Phase: sim.VisitorPhasePresent,
				Payload: "Ezekiel Crane turned out a plow for the Hale farm",
			},
		},
		// A normal actor (real UUID id, persisted by the actor aggregate) proves
		// the two tiers partition the actor set cleanly: the visitor's vstr- id
		// rides the visitor table's text column, this one the actor table's uuid.
		"dddddddd-0000-0000-0000-000000000369": {
			ID: "dddddddd-0000-0000-0000-000000000369", DisplayName: "Goodwife Keeper",
			Kind: sim.KindNPCShared, State: sim.StateIdle,
		},
	}

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	// Tier LoadAll (before rehydrate) — proves the SaveSnapshot columns round-trip.
	rows, err := repo.Visitors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Visitors.LoadAll: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("visitor rows = %d, want 1 (only the visitor, not the npc)", len(rows))
	}
	lv := rows["vstr-0000abcd"]
	if lv == nil {
		t.Fatal("visitor row did not round-trip")
	}
	if lv.DisplayName != "Elias Drum the peddler" ||
		lv.VisitorState.Archetype != "peddler" || lv.VisitorState.Origin != "Boston" ||
		lv.VisitorState.Disposition != "weary" || lv.VisitorState.Phase != sim.VisitorPhasePresent ||
		lv.VisitorState.Payload != "Ezekiel Crane turned out a plow for the Hale farm" {
		t.Errorf("round-tripped visitor = %+v / state %+v", lv, lv.VisitorState)
	}
	if lv.Pos.X != sim.PadX+4 || lv.Pos.Y != sim.PadY+6 {
		t.Errorf("round-tripped Pos = %+v, want the persisted tile", lv.Pos)
	}
	if !lv.VisitorState.ExpiresAt.Equal(expiresAt) {
		t.Errorf("round-tripped ExpiresAt = %v, want %v", lv.VisitorState.ExpiresAt, expiresAt)
	}
	if lv.InsideStructureID != "" {
		t.Errorf("round-tripped InsideStructureID = %q, want empty (outdoors)", lv.InsideStructureID)
	}

	// Full path — LoadWorld runs FinalizeLoad → rehydrateVisitorsOnLoad.
	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	v := loaded.Actors["vstr-0000abcd"]
	if v == nil || v.VisitorState == nil {
		t.Fatal("in-window visitor not rehydrated into loaded actors")
	}
	if v.VisitorState.Origin != "Boston" || v.Kind != sim.KindNPCShared || v.State != sim.StateIdle {
		t.Errorf("rehydrated visitor = origin %q kind %q state %q", v.VisitorState.Origin, v.Kind, v.State)
	}
	if loaded.Actors["dddddddd-0000-0000-0000-000000000369"] == nil {
		t.Error("normal npc missing — the actor aggregate should have persisted it")
	}
}

// TestIntegration_Visitor_DeleteStaleOnDepart — a visitor present at one
// checkpoint but gone from w.Actors at the next (departed + cleaned up) must have
// its row pruned by the gen-marker delete-stale, end to end through SaveWorld.
func TestIntegration_Visitor_DeleteStaleOnDepart(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		"vstr-0000beef": {
			ID: "vstr-0000beef", DisplayName: "Wandering Scholar",
			Kind: sim.KindNPCShared, LLMAgent: sim.VisitorAgentName,
			Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY + 1}, State: sim.StateIdle,
			VisitorState: &sim.VisitorState{
				Archetype: "traveling scholar", Origin: "Cambridge", Disposition: "curious",
				ExpiresAt: expiresAt, Phase: sim.VisitorPhasePresent,
			},
		},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (present): %v", err)
	}

	delete(w.Actors, "vstr-0000beef") // departed + cleaned up between checkpoints
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (after depart): %v", err)
	}

	rows, err := repo.Visitors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Visitors.LoadAll: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("visitor rows = %d, want 0 (departed visitor swept by delete-stale)", len(rows))
	}
}
