package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	mem "github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestHuddleDrift_StructureBoundLeaveOnStructureExit covers the
// drift-out path for indoor scenes: when an actor in a SceneBoundStructure
// huddle has their InsideStructureID mutated to a different value (e.g.
// admin teleport, future scripted move), the drift helper observes the
// rejection from the scene's Bound.Contains check and auto-runs
// LeaveHuddle on them.
//
// PR 4a defines the helper; PR 4 wires it into locomotion commands.
// This test exercises the helper directly inside a Command so we cover
// the drift detection without needing a locomotion command to land.
func TestHuddleDrift_StructureBoundLeaveOnStructureExit(t *testing.T) {
	w, cancel := newDriftTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sceneID := mustSendDrift(t, w, sim.CreateScene("pc_speak", sim.NewStructureBound("tavern"), now)).(sim.SceneID)
	join := mustSendDrift(t, w, sim.JoinHuddle("alice", "tavern", sceneID, now.Add(time.Second))).(sim.JoinHuddleResult)
	mustSendDrift(t, w, sim.JoinHuddle("bob", "tavern", sceneID, now.Add(2*time.Second)))

	huddleID := join.HuddleID

	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			sim.SetActorInsideStructure(world, world.Actors["alice"], "")
			return sim.CheckHuddleDriftAfterPositionMutation(world, "alice", now.Add(3*time.Second)), nil
		},
	})
	if err != nil {
		t.Fatalf("drift command: %v", err)
	}

	snap := w.Published()
	if got := snap.Actors["alice"].CurrentHuddleID; got != "" {
		t.Errorf("alice should be out of huddle, still in %q", got)
	}
	if got := snap.Actors["bob"].CurrentHuddleID; got != huddleID {
		t.Errorf("bob drifted incorrectly: CurrentHuddleID = %q, want %q", got, huddleID)
	}
}

// TestHuddleDrift_AreaBoundLeaveOnDriftPastRadius covers the drift-out
// path for outdoor scenes: an actor in a SceneBoundArea huddle whose
// CurrentX/CurrentY moves past the bound's radius is auto-removed.
// Triggers the scene-auto-conclude path when the actor is the sole
// huddle member.
func TestHuddleDrift_AreaBoundLeaveOnDriftPastRadius(t *testing.T) {
	w, cancel := newDriftTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sceneID := mustSendDrift(t, w, sim.CreateScene("encounter", sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3), now)).(sim.SceneID)
	huddleID := sim.HuddleID("h-area-1")

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].Pos.X = 10
			world.Actors["alice"].Pos.Y = 10
			sim.SetActorInsideStructure(world, world.Actors["alice"], "")

			world.Huddles[huddleID] = &sim.Huddle{
				ID:        huddleID,
				Members:   map[sim.ActorID]struct{}{"alice": {}},
				StartedAt: now,
			}
			world.Actors["alice"].CurrentHuddleID = huddleID
			world.Scenes[sceneID].Huddles[huddleID] = struct{}{}
			return nil, nil
		},
	})

	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].Pos.X = 50
			world.Actors["alice"].Pos.Y = 50
			return sim.CheckHuddleDriftAfterPositionMutation(world, "alice", now.Add(time.Second)), nil
		},
	})
	if err != nil {
		t.Fatalf("drift command: %v", err)
	}

	snap := w.Published()
	if got := snap.Actors["alice"].CurrentHuddleID; got != "" {
		t.Errorf("alice should be out of huddle, still in %q", got)
	}
	if _, stillThere := snap.Scenes[sceneID]; stillThere {
		t.Error("area scene should have auto-concluded when sole huddle ended")
	}
}

// TestHuddleDrift_NoDriftWhenStillContained covers the no-op case: an
// actor who moves but stays within the scene's bound should NOT be
// auto-removed.
func TestHuddleDrift_NoDriftWhenStillContained(t *testing.T) {
	w, cancel := newDriftTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sceneID := mustSendDrift(t, w, sim.CreateScene("encounter", sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 5), now)).(sim.SceneID)
	huddleID := sim.HuddleID("h-stay-1")

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].Pos.X = 10
			world.Actors["alice"].Pos.Y = 10
			sim.SetActorInsideStructure(world, world.Actors["alice"], "")

			world.Huddles[huddleID] = &sim.Huddle{
				ID:        huddleID,
				Members:   map[sim.ActorID]struct{}{"alice": {}},
				StartedAt: now,
			}
			world.Actors["alice"].CurrentHuddleID = huddleID
			world.Scenes[sceneID].Huddles[huddleID] = struct{}{}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].Pos.X = 12
			world.Actors["alice"].Pos.Y = 13
			return sim.CheckHuddleDriftAfterPositionMutation(world, "alice", now.Add(time.Second)), nil
		},
	})

	snap := w.Published()
	if got := snap.Actors["alice"].CurrentHuddleID; got != huddleID {
		t.Errorf("alice incorrectly drifted out: CurrentHuddleID = %q, want %q", got, huddleID)
	}
}

// TestHuddleDrift_StaleBackRefIsRepaired covers the R1 fix: when an
// actor's CurrentHuddleID points at a missing or concluded huddle,
// the drift helper opportunistically clears the stale back-ref so
// subsequent commands see consistent state.
//
// Setup: join alice to a real huddle (this populates her
// CurrentHuddleID through the canonical command path); then delete
// the huddle directly from w.Huddles to simulate stale-after-cleanup
// state. The drift helper should observe the dangling back-ref and
// repair it.
func TestHuddleDrift_StaleBackRefIsRepaired(t *testing.T) {
	w, cancel := newDriftTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Join alice to a huddle through the canonical command path so
	// CurrentHuddleID is set normally.
	join := mustSendDrift(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	huddleID := join.HuddleID

	// Simulate stale-after-cleanup: delete the huddle from
	// w.Huddles without going through LeaveHuddle/ConcludeHuddle,
	// leaving alice's CurrentHuddleID dangling.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			delete(world.Huddles, huddleID)
			return nil, nil
		},
	})

	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			drifted := sim.CheckHuddleDriftAfterPositionMutation(world, "alice", now.Add(time.Second))
			return struct {
				Drifted         []sim.HuddleID
				CurrentHuddleID sim.HuddleID
			}{drifted, world.Actors["alice"].CurrentHuddleID}, nil
		},
	})
	got := res.(struct {
		Drifted         []sim.HuddleID
		CurrentHuddleID sim.HuddleID
	})
	if len(got.Drifted) != 0 {
		t.Errorf("expected no drift report for stale back-ref, got %v", got.Drifted)
	}
	if got.CurrentHuddleID != "" {
		t.Errorf("stale CurrentHuddleID not cleared: got %q", got.CurrentHuddleID)
	}
}

// TestHuddleDrift_NoOpWhenNotInHuddle covers the early-return path:
// the helper returns nil for actors who aren't in any huddle.
func TestHuddleDrift_NoOpWhenNotInHuddle(t *testing.T) {
	w, cancel := newDriftTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CheckHuddleDriftAfterPositionMutation(world, "alice", now), nil
		},
	})
	if got := res.([]sim.HuddleID); len(got) != 0 {
		t.Errorf("expected empty drifted list for actor not in huddle, got %v", got)
	}
}

func newDriftTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Alice", InsideStructureID: "tavern"},
		"bob":   {ID: "bob", DisplayName: "Bob", InsideStructureID: "tavern"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func mustSendDrift(t *testing.T, w *sim.World, cmd sim.Command) any {
	t.Helper()
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return res
}
