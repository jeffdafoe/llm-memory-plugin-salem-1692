package sim_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	mem "github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestCreateScene_AreaBound_CapturesOutdoorActors covers the outdoor
// scene mint path: ParticipantStateAtOrigin captures every actor whose
// Bound.Contains check passes (outdoor AND within radius). An indoor
// actor at a tile within radius is rejected. An outdoor actor outside
// the radius is rejected.
func TestCreateScene_AreaBound_CapturesOutdoorActors(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"outdoor-near": {
			ID:  "outdoor-near",
			Pos: sim.TilePos{X: 11, Y: 10},
		},
		"outdoor-far": {
			ID:  "outdoor-far",
			Pos: sim.TilePos{X: 50, Y: 50},
		},
		"indoor-at-anchor": {
			ID:                "indoor-at-anchor",
			InsideStructureID: "tavern",
			Pos:               sim.TilePos{X: 10, Y: 10},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()
	bound := sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3)
	res, err := w.Send(sim.CreateScene("encounter", bound, now))
	if err != nil {
		t.Fatalf("CreateScene area-bound: %v", err)
	}
	sceneID := res.(sim.SceneID)

	snap := w.Published()
	scene := snap.Scenes[sceneID]
	if scene == nil {
		t.Fatal("scene not in snapshot")
	}
	if scene.Bound.Kind != sim.SceneBoundArea {
		t.Errorf("scene.Bound.Kind = %q, want %q", scene.Bound.Kind, sim.SceneBoundArea)
	}
	if scene.OriginPosition != (sim.Position{X: 10, Y: 10}) {
		t.Errorf("scene.OriginPosition = %+v, want (10, 10)", scene.OriginPosition)
	}

	if _, in := scene.ParticipantStateAtOrigin["outdoor-near"]; !in {
		t.Error("outdoor-near should be captured (within radius)")
	}
	if _, in := scene.ParticipantStateAtOrigin["outdoor-far"]; in {
		t.Error("outdoor-far should NOT be captured (outside radius)")
	}
	if _, in := scene.ParticipantStateAtOrigin["indoor-at-anchor"]; in {
		t.Error("indoor-at-anchor should NOT be captured (indoors)")
	}
}

// TestCreateScene_UnboundedBound_EmptyParticipants covers the unbounded
// scene mint path: no actor capture, no huddle association.
// Replaces the legacy empty-string-as-sentinel call pattern with the
// explicit NewUnboundedBound() constructor.
func TestCreateScene_UnboundedBound_EmptyParticipants(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()
	res, err := w.Send(sim.CreateScene("atmosphere_refresh", sim.NewUnboundedBound(), now))
	if err != nil {
		t.Fatalf("CreateScene unbounded: %v", err)
	}
	sceneID := res.(sim.SceneID)

	snap := w.Published()
	scene := snap.Scenes[sceneID]
	if scene == nil {
		t.Fatal("scene not in snapshot")
	}
	if scene.Bound.Kind != sim.SceneBoundUnbounded {
		t.Errorf("scene.Bound.Kind = %q, want %q", scene.Bound.Kind, sim.SceneBoundUnbounded)
	}
	if scene.OriginStructureID() != "" {
		t.Errorf("scene.OriginStructureID() = %q, want empty", scene.OriginStructureID())
	}
	if len(scene.ParticipantStateAtOrigin) != 0 {
		t.Errorf("unbounded scene captured participants: %v", scene.ParticipantStateAtOrigin)
	}
}

// TestCreateScene_RejectsMissingBoundFields covers the validation paths
// for malformed bounds: a structure bound with no StructureID, an area
// bound with no Anchor or Radius, and an unknown bound kind.
func TestCreateScene_RejectsMissingBoundFields(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()

	// Structure bound with no StructureID.
	bad := sim.SceneBound{Kind: sim.SceneBoundStructure}
	if _, err := w.Send(sim.CreateScene("pc_speak", bad, now)); err == nil {
		t.Error("expected error for structure bound with no StructureID")
	}

	// Area bound with no Anchor.
	bad = sim.SceneBound{Kind: sim.SceneBoundArea}
	if _, err := w.Send(sim.CreateScene("encounter", bad, now)); err == nil {
		t.Error("expected error for area bound with no Anchor/Radius")
	}

	// Unknown bound kind.
	bad = sim.SceneBound{Kind: "garbage"}
	if _, err := w.Send(sim.CreateScene("pc_speak", bad, now)); err == nil {
		t.Error("expected error for unknown bound kind")
	}
}

// TestJoinHuddle_RejectsSceneStructureMismatch covers the new
// consistency check: joining at structure B while passing a sceneID
// whose Bound is StructureBound{A} is rejected. Without the check,
// subscribers would receive a HuddleJoined event whose StructureID
// disagrees with the scene's bound — silent perception state drift.
func TestJoinHuddle_RejectsSceneStructureMismatch(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":     {ID: "tavern", DisplayName: "Tavern"},
		"blacksmith": {ID: "blacksmith", DisplayName: "Blacksmith"},
	})
	// Shared-Identity Bridge for SceneBoundStructure (ZBBS-WORK-342).
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern":     {ID: "tavern", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
		"blacksmith": {ID: "blacksmith", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", InsideStructureID: "blacksmith"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()
	res, err := w.Send(sim.CreateScene("pc_speak", sim.NewStructureBound("tavern"), now))
	if err != nil {
		t.Fatalf("CreateScene: %v", err)
	}
	sceneID := res.(sim.SceneID)

	// alice is at blacksmith, but the scene is bound to tavern. Joining
	// at blacksmith while passing the tavern-scene sceneID must error.
	_, err = w.Send(sim.JoinHuddle("alice", "blacksmith", sceneID, now.Add(time.Second)))
	if err == nil {
		t.Error("expected error joining at structure B with scene bound to structure A")
	}
}

// TestConcludeHuddle_AutoConcludesAreaScene covers the PR 4a invariant:
// when the sole huddle in a SceneBoundArea scene concludes, the scene
// concludes too (gets removed from world state). Indoor and unbounded
// scenes do NOT auto-conclude — they follow the PR 1 model where
// scenes accrue huddles and never officially end.
//
// Coverage uses ConcludeHuddle (force-conclude path); the last-leave
// path in leaveCurrentHuddle uses the same helper.
func TestConcludeHuddle_AutoConcludesAreaScene(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"plaza": {ID: "plaza", DisplayName: "Plaza"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", Pos: sim.TilePos{X: 10, Y: 10}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()

	// Mint an outdoor scene at the plaza.
	res, err := w.Send(sim.CreateScene("encounter", sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3), now))
	if err != nil {
		t.Fatalf("CreateScene area: %v", err)
	}
	sceneID := res.(sim.SceneID)

	// Manually associate a huddle with the scene. (PR 4 will land
	// StartOutdoorHuddle as the proper command for this; for PR 4a
	// we simulate the association directly to exercise the conclude
	// path.)
	huddleID := sim.HuddleID("h-test-1")
	_, err = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Huddles[huddleID] = &sim.Huddle{
				ID:        huddleID,
				Members:   map[sim.ActorID]struct{}{},
				StartedAt: now,
			}
			world.Scenes[sceneID].Huddles[huddleID] = struct{}{}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("seed huddle: %v", err)
	}

	// Conclude the huddle. The area scene should auto-conclude.
	if _, err := w.Send(sim.ConcludeHuddle(huddleID, now.Add(time.Minute))); err != nil {
		t.Fatalf("ConcludeHuddle: %v", err)
	}

	snap := w.Published()
	if _, stillThere := snap.Scenes[sceneID]; stillThere {
		t.Errorf("area scene %q should have auto-concluded after sole huddle ended", sceneID)
	}
}

// TestConcludeHuddle_DoesNotAutoConcludeStructureScene covers the
// inverse: indoor scenes are NOT auto-concluded when one of their
// huddles concludes. Indoor scenes may host parallel huddles; the
// scene lifecycle does not depend on any single huddle.
func TestConcludeHuddle_DoesNotAutoConcludeStructureScene(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	// Shared-Identity Bridge for SceneBoundStructure (ZBBS-WORK-342).
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {ID: "tavern", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", InsideStructureID: "tavern"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()

	// Indoor scene; join alice to trigger a huddle; conclude the
	// huddle; scene must still exist.
	sceneID := sendTestScene(t, w, sim.NewStructureBound("tavern"), now)
	res, err := w.Send(sim.JoinHuddle("alice", "tavern", sceneID, now.Add(time.Second)))
	if err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}
	huddleID := res.(sim.JoinHuddleResult).HuddleID

	if _, err := w.Send(sim.ConcludeHuddle(huddleID, now.Add(time.Minute))); err != nil {
		t.Fatalf("ConcludeHuddle: %v", err)
	}

	snap := w.Published()
	if _, stillThere := snap.Scenes[sceneID]; !stillThere {
		t.Error("structure-bound scene should not auto-conclude on huddle conclude")
	}
}

// TestJoinHuddle_RejectsAreaSceneID covers the R1 fix: JoinHuddle is
// the structure-huddle path; passing a SceneBoundArea sceneID must
// reject. Outdoor huddles will land in PR 4's StartOutdoorHuddle; the
// rejection here prevents accidental "structure huddle attached to
// area scene" malformed state.
func TestJoinHuddle_RejectsAreaSceneID(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", InsideStructureID: "tavern"},
	})
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	now := time.Now().UTC()

	areaSceneID := sendTestScene(t, w, sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3), now)
	if _, err := w.Send(sim.JoinHuddle("alice", "tavern", areaSceneID, now.Add(time.Second))); err == nil {
		t.Error("JoinHuddle should reject area-bound sceneID")
	}
}

// TestJoinHuddle_RejectsUnboundedSceneID covers the R1 fix: JoinHuddle
// must reject unbounded-scene sceneIDs too. Unbounded scenes don't
// associate with structure huddles via the JoinHuddle path.
func TestJoinHuddle_RejectsUnboundedSceneID(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", InsideStructureID: "tavern"},
	})
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	now := time.Now().UTC()

	unboundedSceneID := sendTestScene(t, w, sim.NewUnboundedBound(), now)
	if _, err := w.Send(sim.JoinHuddle("alice", "tavern", unboundedSceneID, now.Add(time.Second))); err == nil {
		t.Error("JoinHuddle should reject unbounded sceneID")
	}
}

// TestAttachHuddleToScene_RejectsAreaSceneSecondHuddle covers the R1
// invariant: area scenes are 1:1 with huddles. Attempting to attach a
// second different huddle to an area scene must reject. Re-attaching
// the same huddle is a no-op (idempotent).
//
// Exercises the helper indirectly through CreateScene's auto-attach
// path: an area scene is minted; we manually attach a huddle to it
// via the test seam; a second attach attempt must fail.
//
// (PR 4 will land StartOutdoorHuddle as the production path. PR 4a's
// rejection guarantees malformed state can't accumulate even if a
// future caller tries to bypass.)
func TestAttachHuddleToScene_RejectsAreaSceneSecondHuddle(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", Pos: sim.TilePos{X: 10, Y: 10}},
	})
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	now := time.Now().UTC()

	sceneID := sendTestScene(t, w, sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3), now)

	// Manually seed a huddle and attach via the test helper.
	huddleID1 := sim.HuddleID("h-1")
	huddleID2 := sim.HuddleID("h-2")
	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			scene := world.Scenes[sceneID]
			world.Huddles[huddleID1] = &sim.Huddle{ID: huddleID1, Members: map[sim.ActorID]struct{}{}, StartedAt: now}
			world.Huddles[huddleID2] = &sim.Huddle{ID: huddleID2, Members: map[sim.ActorID]struct{}{}, StartedAt: now}

			if err := sim.AttachHuddleToScene(scene, huddleID1); err != nil {
				return nil, fmt.Errorf("first attach should succeed: %w", err)
			}
			// Re-attach same → idempotent no-op.
			if err := sim.AttachHuddleToScene(scene, huddleID1); err != nil {
				return nil, fmt.Errorf("idempotent re-attach should succeed: %w", err)
			}
			// Different huddle → must reject. We return the rejection as
			// the command's error so w.Send surfaces it; if the helper
			// incorrectly allowed it, the command would return nil here
			// and the test would catch the missing rejection.
			if err := sim.AttachHuddleToScene(scene, huddleID2); err == nil {
				return nil, fmt.Errorf("second different-huddle attach should reject")
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("attach invariant test failed: %v", err)
	}
}

// TestAttachHuddleToScene_RejectsUnboundedScene covers the R1
// invariant: unbounded scenes don't accept huddle attach via the
// helper. Any future path that tries gets a clear error.
func TestAttachHuddleToScene_RejectsUnboundedScene(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, _ := sim.LoadWorld(context.Background(), repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	now := time.Now().UTC()

	sceneID := sendTestScene(t, w, sim.NewUnboundedBound(), now)
	huddleID := sim.HuddleID("h-1")
	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			scene := world.Scenes[sceneID]
			world.Huddles[huddleID] = &sim.Huddle{ID: huddleID, Members: map[sim.ActorID]struct{}{}, StartedAt: now}
			if err := sim.AttachHuddleToScene(scene, huddleID); err == nil {
				return nil, fmt.Errorf("attach to unbounded scene should reject")
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("unbounded attach rejection test failed: %v", err)
	}
}

// TestNormalizeOutdoorSceneRadius covers the R1 fix: LoadWorld now
// applies default-and-clamp to WorldSettings.DefaultOutdoorSceneRadius.
// 0 → 3 (default), negative → 0 wait, table-driven cases below.
//
// Cases: 0 or negative → default (3), in-range stays, above max → 10.
func TestNormalizeOutdoorSceneRadius(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  int
	}{
		{"zero stays default", 0, sim.DefaultOutdoorSceneRadiusValue},
		{"negative -> default", -5, sim.DefaultOutdoorSceneRadiusValue},
		{"in-range 5", 5, 5},
		{"max stays max", sim.DefaultOutdoorSceneRadiusMax, sim.DefaultOutdoorSceneRadiusMax},
		{"above max clamps", sim.DefaultOutdoorSceneRadiusMax + 5, sim.DefaultOutdoorSceneRadiusMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sim.WorldSettings{DefaultOutdoorSceneRadius: tc.input}
			sim.NormalizeOutdoorSceneRadius(&s)
			if s.DefaultOutdoorSceneRadius != tc.want {
				t.Errorf("DefaultOutdoorSceneRadius = %d, want %d", s.DefaultOutdoorSceneRadius, tc.want)
			}
		})
	}
}

func sendTestScene(t *testing.T, w *sim.World, bound sim.SceneBound, now time.Time) sim.SceneID {
	t.Helper()
	res, err := w.Send(sim.CreateScene("pc_speak", bound, now))
	if err != nil {
		t.Fatalf("CreateScene: %v", err)
	}
	return res.(sim.SceneID)
}
