package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// move_to_lodging_test.go — the lodging keyword arm of move_to name resolution
// (LLM-569): "my room"/"my bed"/… resolve grant-first to the structure holding
// the actor's rented room, else home; already-under-the-roof answers with the
// room-aware terminal no-op that points at turn_in exactly when the substrate
// would accept it. The live trigger: a visitor lodged at the Tavern called
// move_to("my room") 14 times in one evening, got the "no place called" error
// (whose steer lists OTHER structures) every time, and spent ~90 minutes
// wandering out of and back into the building his paid bed was in.

// seedLodger gives walker an active ledger grant on a private room of the inn,
// makes it an agent NPC (the turn_in gate ignores non-agents), and pins the
// village clock to dawn 07:00 / dusk 19:00 UTC so tests can pick a pre- or
// post-dusk now deterministically (Settings.Location nil → UTC).
func seedLodger(t *testing.T, w *sim.World, expiresAt time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.DawnTime = "07:00"
		world.Settings.DuskTime = "19:00"
		world.Settings.Location = time.UTC
		inn := world.Structures["inn"]
		inn.Rooms = append(inn.Rooms, &sim.Room{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"})
		a := world.Actors["walker"]
		a.Kind = sim.KindNPCShared
		exp := expiresAt
		a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 1, Source: sim.AccessSourceLedger}: {
				RoomID: 1, Source: sim.AccessSourceLedger, LedgerID: 7, ExpiresAt: &exp, Active: true,
			},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed lodger: %v", err)
	}
}

func TestMoveTo_LodgingKeywords(t *testing.T) {
	// 12:00 UTC — midday, well before the 19:00 dusk.
	noon := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// 20:00 UTC — inside the voluntary night window [dusk, dawn).
	evening := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)

	t.Run("lodger elsewhere walks to the grant's structure", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, noon.Add(24*time.Hour))
		if _, err := w.Send(sim.MoveToStructureByName("walker", "my room", nil, sim.RememberedPlaces{}, noon)); err != nil {
			t.Fatalf("move_to(my room): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("'my room' resolved to %q, want inn (the lodging grant's structure)", sid)
		}
	})

	t.Run("'at <place>' suffix is tolerated; the grant decides", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, noon.Add(24*time.Hour))
		// "the gazebo" names a real structure, but the grant is at the inn — the
		// grant is the truth about where the actor's bed is.
		if _, err := w.Send(sim.MoveToStructureByName("walker", "my room at the gazebo", nil, sim.RememberedPlaces{}, noon)); err != nil {
			t.Fatalf("move_to(my room at the gazebo): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("'my room at the gazebo' resolved to %q, want inn", sid)
		}
	})

	t.Run("inside pre-dusk: terminal no-op, too-early steer, no turn_in mention", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, noon.Add(24*time.Hour))
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["walker"].InsideStructureID = "inn"
			return nil, nil
		}}); err != nil {
			t.Fatalf("seed inside: %v", err)
		}
		_, err := w.Send(sim.MoveToStructureByName("walker", "my room", nil, sim.RememberedPlaces{}, noon))
		var noop sim.TerminalNoOpError
		if !errors.As(err, &noop) {
			t.Fatalf("move_to(my room) while inside the lodging must be TerminalNoOpError, got %T: %v", err, err)
		}
		if !strings.Contains(noop.Msg, "too early") {
			t.Errorf("pre-dusk no-op should carry the too-early steer: %q", noop.Msg)
		}
		if strings.Contains(noop.Msg, "turn_in") {
			t.Errorf("pre-dusk no-op must not name turn_in — the tool is not gated in yet: %q", noop.Msg)
		}
	})

	t.Run("inside post-dusk: terminal no-op that points at turn_in", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, evening.Add(24*time.Hour))
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["walker"].InsideStructureID = "inn"
			return nil, nil
		}}); err != nil {
			t.Fatalf("seed inside: %v", err)
		}
		_, err := w.Send(sim.MoveToStructureByName("walker", "my room", nil, sim.RememberedPlaces{}, evening))
		var noop sim.TerminalNoOpError
		if !errors.As(err, &noop) {
			t.Fatalf("post-dusk move_to(my room) inside must be TerminalNoOpError, got %T: %v", err, err)
		}
		if !strings.Contains(noop.Msg, "turn_in") {
			t.Errorf("post-dusk no-op should point at turn_in: %q", noop.Msg)
		}
	})

	t.Run("no grant falls back to home", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "gazebo", "")
		if _, err := w.Send(sim.MoveToStructureByName("walker", "my bed", nil, sim.RememberedPlaces{}, noon)); err != nil {
			t.Fatalf("move_to(my bed): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "gazebo" {
			t.Errorf("'my bed' resolved to %q, want gazebo (HomeStructureID)", sid)
		}
	})

	t.Run("expired grant is skipped — falls back to home", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, noon.Add(-time.Hour)) // already expired
		setWalkerAnchors(t, w, "gazebo", "")
		if _, err := w.Send(sim.MoveToStructureByName("walker", "my room", nil, sim.RememberedPlaces{}, noon)); err != nil {
			t.Fatalf("move_to(my room): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "gazebo" {
			t.Errorf("expired grant: 'my room' resolved to %q, want gazebo (home fallback)", sid)
		}
	})

	t.Run("neither grant nor home: retryable steer, not a no-op", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		_, err := w.Send(sim.MoveToStructureByName("walker", "my room", nil, sim.RememberedPlaces{}, noon))
		if err == nil {
			t.Fatal("want error for a bedless actor's move_to(my room), got nil")
		}
		if !strings.Contains(err.Error(), "no room of your own") {
			t.Errorf("error should explain there is no room: %v", err)
		}
		var noop sim.TerminalNoOpError
		if errors.As(err, &noop) {
			t.Fatalf("bedless move_to(my room) must stay retryable, not TerminalNoOpError: %v", err)
		}
	})

	t.Run("keyword rides the structure_id path too", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, noon.Add(24*time.Hour))
		if _, err := w.Send(sim.MoveToStructure("walker", "my room", noon)); err != nil {
			t.Fatalf("MoveToStructure(my room): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("structure_id 'my room' resolved to %q, want inn", sid)
		}
	})

	t.Run("a real place named 'My Room' wins the name", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedLodger(t, w, noon.Add(24*time.Hour))
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			// Rename an existing placed structure so the name resolver can match it.
			world.Structures["gazebo"].DisplayName = "My Room"
			return nil, nil
		}}); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if _, err := w.Send(sim.MoveToStructureByName("walker", "my room", nil, sim.RememberedPlaces{}, noon)); err != nil {
			t.Fatalf("move_to(my room): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "gazebo" {
			t.Errorf("'my room' resolved to %q, want gazebo (a real place name wins the keyword)", sid)
		}
	})
}
