package sim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildHuddleTestWorld seeds three actors at no structure, ready to be
// joined into huddles via JoinHuddle. Returns the running world and a
// cancel func.
func buildHuddleTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
		"smithy": {ID: "smithy", DisplayName: "Smithy"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice":   {ID: "alice", DisplayName: "Alice"},
		"bob":     {ID: "bob", DisplayName: "Bob"},
		"charlie": {ID: "charlie", DisplayName: "Charlie"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// sendT is a test helper that sends a command and fails on error.
func sendT(t *testing.T, w *sim.World, cmd sim.Command) any {
	t.Helper()
	v, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return v
}

// TestJoinHuddle_FirstJoinCreatesHuddle covers the create-on-first-join
// path: when no active huddle exists at the structure, JoinHuddle mints
// one, places the actor as sole member, stamps a warrant, and emits
// HuddleJoined with HuddleNew=true.
func TestJoinHuddle_FirstJoinCreatesHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	if !res.HuddleNew {
		t.Error("expected HuddleNew=true on first join")
	}
	if res.HuddleID == "" {
		t.Fatal("expected non-empty HuddleID")
	}
	if len(res.OtherMembers) != 0 {
		t.Errorf("expected no other members on first join, got %v", res.OtherMembers)
	}

	// World state reflects the join.
	snap := w.Published()
	huddle, ok := snap.Huddles[res.HuddleID]
	if !ok {
		t.Fatal("huddle not in snapshot")
	}
	if _, in := huddle.Members["alice"]; !in {
		t.Error("alice not in huddle members")
	}
	if huddle.StructureID != "tavern" {
		t.Errorf("huddle structure = %q, want tavern", huddle.StructureID)
	}
	if huddle.ConcludedAt != nil {
		t.Error("freshly-created huddle should not be concluded")
	}

	// Back-reference on actor matches.
	if got := snap.Actors["alice"].CurrentHuddleID; got != res.HuddleID {
		t.Errorf("alice CurrentHuddleID = %q, want %q", got, res.HuddleID)
	}

	// Warrant stamped (alice has actionable change since last tick — she
	// just joined a huddle, by herself yes, but the seam is established).
	// First-join-alone is a slightly degenerate case; the intent here is
	// to lock in that warrant-stamping fires reliably from the join path.
	postJoinAlice, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["alice"].WarrantedSince, nil
		},
	})
	if postJoinAlice == nil || postJoinAlice.(*time.Time) == nil {
		t.Error("alice WarrantedSince not stamped after JoinHuddle")
	}
}

// TestJoinHuddle_SecondJoinReusesAndWarrantsBoth covers the join-into-
// existing path: the second actor at the same structure finds the
// existing huddle, sees alice in OtherMembers, gets a HuddleJoined with
// HuddleNew=false, and BOTH actors get warrant-stamped (peer change is
// actionable for the prior member too).
func TestJoinHuddle_SecondJoinReusesAndWarrantsBoth(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	first := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)

	// Clear alice's warrant so we can verify the SECOND join re-stamps it
	// — without this, the assertion would pass trivially from the first
	// join's stamp.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].WarrantedSince = nil
			return nil, nil
		},
	})

	later := now.Add(30 * time.Second)
	second := sendT(t, w, sim.JoinHuddle("bob", "tavern", "", later)).(sim.JoinHuddleResult)

	if second.HuddleNew {
		t.Error("expected HuddleNew=false on second join (reuse)")
	}
	if second.HuddleID != first.HuddleID {
		t.Errorf("second join huddle = %q, first = %q (should reuse)", second.HuddleID, first.HuddleID)
	}
	if len(second.OtherMembers) != 1 || second.OtherMembers[0] != "alice" {
		t.Errorf("expected OtherMembers=[alice], got %v", second.OtherMembers)
	}

	// World state reflects both members.
	snap := w.Published()
	huddle := snap.Huddles[second.HuddleID]
	if _, in := huddle.Members["alice"]; !in {
		t.Error("alice missing from huddle after bob join")
	}
	if _, in := huddle.Members["bob"]; !in {
		t.Error("bob missing from huddle after join")
	}

	// BOTH actors warranted by the join. Alice's warrant was cleared
	// pre-join, so its non-nil presence here proves the join re-stamped.
	post, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return [2]*time.Time{world.Actors["alice"].WarrantedSince, world.Actors["bob"].WarrantedSince}, nil
		},
	})
	stamps := post.([2]*time.Time)
	if stamps[0] == nil {
		t.Error("alice WarrantedSince not re-stamped by bob's join")
	}
	if stamps[1] == nil {
		t.Error("bob WarrantedSince not stamped by his own join")
	}
}

// TestJoinHuddle_AtomicCrossStructureMove covers the leave-then-join
// atomic transition: when alice is in a tavern huddle and joins the
// smithy huddle, the tavern huddle loses her membership, the smithy
// huddle gains her, and her CurrentHuddleID points at smithy — all
// inside one Command.Fn so no observer ever sees her in zero or two
// huddles.
func TestJoinHuddle_AtomicCrossStructureMove(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	first := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	second := sendT(t, w, sim.JoinHuddle("alice", "smithy", "", now.Add(time.Minute))).(sim.JoinHuddleResult)

	if second.HuddleID == first.HuddleID {
		t.Error("smithy join should produce a different huddle from tavern")
	}

	snap := w.Published()
	tavernHuddle := snap.Huddles[first.HuddleID]
	if _, in := tavernHuddle.Members["alice"]; in {
		t.Error("alice still in tavern huddle after smithy join")
	}
	if tavernHuddle.ConcludedAt == nil {
		t.Error("tavern huddle should have concluded when alice was its sole member and left")
	}

	smithyHuddle := snap.Huddles[second.HuddleID]
	if _, in := smithyHuddle.Members["alice"]; !in {
		t.Error("alice missing from smithy huddle")
	}

	if got := snap.Actors["alice"].CurrentHuddleID; got != second.HuddleID {
		t.Errorf("alice CurrentHuddleID = %q, want %q (smithy)", got, second.HuddleID)
	}
}

// TestLeaveHuddle_LastMemberConcludes covers the conclude-on-empty path:
// when the last member leaves, the huddle is concluded (ConcludedAt
// stamped, removed from actorsByHuddle index) and HuddleConcluded
// follows HuddleLeft.
func TestLeaveHuddle_LastMemberConcludes(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	first := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	res := sendT(t, w, sim.LeaveHuddle("alice", now.Add(time.Minute))).(sim.LeaveHuddleResult)

	if !res.Concluded {
		t.Error("expected Concluded=true on solo-member leave")
	}
	if len(res.RemainingMembers) != 0 {
		t.Errorf("expected empty RemainingMembers, got %v", res.RemainingMembers)
	}

	snap := w.Published()
	huddle := snap.Huddles[first.HuddleID]
	if huddle.ConcludedAt == nil {
		t.Error("huddle ConcludedAt not stamped after last leave")
	}
	if got := snap.Actors["alice"].CurrentHuddleID; got != "" {
		t.Errorf("alice CurrentHuddleID = %q, want empty after leave", got)
	}
}

// TestLeaveHuddle_RemainingMemberWarranted covers the peer-departure
// warrant: when bob leaves a huddle alice is still in, alice gets her
// WarrantedSince re-stamped — peer departure is actionable for the
// remainer.
func TestLeaveHuddle_RemainingMemberWarranted(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now.Add(10*time.Second)))

	// Clear alice's warrant before bob leaves to isolate the leave-side
	// stamping.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].WarrantedSince = nil
			return nil, nil
		},
	})

	res := sendT(t, w, sim.LeaveHuddle("bob", now.Add(time.Minute))).(sim.LeaveHuddleResult)
	if res.Concluded {
		t.Error("huddle should not conclude — alice still present")
	}
	if len(res.RemainingMembers) != 1 || res.RemainingMembers[0] != "alice" {
		t.Errorf("expected RemainingMembers=[alice], got %v", res.RemainingMembers)
	}

	post, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["alice"].WarrantedSince, nil
		},
	})
	if post == nil || post.(*time.Time) == nil {
		t.Error("alice WarrantedSince not re-stamped by bob's leave")
	}
}

// TestLeaveHuddle_NoOpWhenNotInHuddle covers the no-op path: actor with
// empty CurrentHuddleID returns a zero result with no error, no events,
// no state change.
func TestLeaveHuddle_NoOpWhenNotInHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	res := sendT(t, w, sim.LeaveHuddle("alice", time.Now().UTC())).(sim.LeaveHuddleResult)
	if res.HuddleID != "" || res.Concluded || len(res.RemainingMembers) != 0 {
		t.Errorf("expected zero LeaveHuddleResult, got %+v", res)
	}
}

// TestConcludeHuddle_ForceEvictsAndStamps covers the force-conclude path:
// every member's CurrentHuddleID clears, all members get warrant-stamped,
// the huddle is marked concluded, and HuddleConcluded fires.
func TestConcludeHuddle_ForceEvictsAndStamps(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))

	// Clear warrants so the conclude-time re-stamp is observable.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].WarrantedSince = nil
			world.Actors["bob"].WarrantedSince = nil
			return nil, nil
		},
	})

	if _, err := w.Send(sim.ConcludeHuddle(res.HuddleID, now.Add(time.Minute))); err != nil {
		t.Fatalf("ConcludeHuddle: %v", err)
	}

	snap := w.Published()
	huddle := snap.Huddles[res.HuddleID]
	if huddle.ConcludedAt == nil {
		t.Error("ConcludedAt not stamped after force-conclude")
	}
	if len(huddle.Members) != 0 {
		t.Errorf("members not cleared after force-conclude, got %v", huddle.Members)
	}
	for _, who := range []sim.ActorID{"alice", "bob"} {
		if got := snap.Actors[who].CurrentHuddleID; got != "" {
			t.Errorf("%s CurrentHuddleID = %q, want empty after force-conclude", who, got)
		}
	}

	post, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return [2]*time.Time{world.Actors["alice"].WarrantedSince, world.Actors["bob"].WarrantedSince}, nil
		},
	})
	stamps := post.([2]*time.Time)
	if stamps[0] == nil || stamps[1] == nil {
		t.Errorf("expected both alice and bob warranted by force-conclude, got %+v", stamps)
	}
}

// TestConcludeHuddle_IdempotentOnConcluded covers the idempotency path:
// re-concluding an already-concluded huddle is a no-op (no error).
func TestConcludeHuddle_IdempotentOnConcluded(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	if _, err := w.Send(sim.ConcludeHuddle(res.HuddleID, now.Add(time.Minute))); err != nil {
		t.Fatalf("first ConcludeHuddle: %v", err)
	}
	if _, err := w.Send(sim.ConcludeHuddle(res.HuddleID, now.Add(2*time.Minute))); err != nil {
		t.Errorf("second ConcludeHuddle (idempotent path): %v", err)
	}
}

// TestCreateScene_CapturesParticipantsAtOriginStructure covers the diff
// seam: when a scene is minted at a structure that has actors inside it,
// the scene captures snapshots of every actor present at mint, keyed by
// ActorID. Subsequent in-world mutations must not bleed into those
// captured snapshots.
//
// Setup seeds alice with InsideStructureID="tavern" pre-LoadWorld so
// rebuildIndices populates actorsByStructure at load time — that's how
// real arrivals will land alice in the index once the locomotion port
// (Phase 2 PR 4-ish) wires structure-entry commands.
func TestCreateScene_CapturesParticipantsAtOriginStructure(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID:                "alice",
			DisplayName:       "Alice",
			InsideStructureID: "tavern",
			Needs:             map[sim.NeedKey]int{"hunger": 4, "thirst": 1},
			Coins:             12,
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
	res := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now))
	sceneID := res.(sim.SceneID)

	// Verify capture.
	snap := w.Published()
	scene := snap.Scenes[sceneID]
	if scene == nil {
		t.Fatal("scene not in snapshot")
	}
	if scene.OriginStructureID != "tavern" {
		t.Errorf("scene OriginStructureID = %q, want tavern", scene.OriginStructureID)
	}
	captured, ok := scene.ParticipantStateAtOrigin["alice"]
	if !ok {
		t.Fatal("alice not captured in ParticipantStateAtOrigin")
	}
	if got := captured.Coins; got != 12 {
		t.Errorf("captured coins = %d, want 12", got)
	}
	if got := captured.Needs["hunger"]; got != 4 {
		t.Errorf("captured hunger = %d, want 4", got)
	}

	// Mutate alice's needs in-world, take a fresh snapshot, confirm the
	// captured snapshot doesn't shift.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].Needs["hunger"] = 999
			return nil, nil
		},
	})
	snap2 := w.Published()
	if got := snap2.Scenes[sceneID].ParticipantStateAtOrigin["alice"].Needs["hunger"]; got != 4 {
		t.Errorf("captured hunger leaked: post-mutation snapshot has %d, want 4", got)
	}
}

// TestCreateScene_StructurelessOriginEmptyParticipants covers the
// non-structure-tied cascade path (chronicler atmosphere refresh,
// admin-triggered fires): empty originStructureID yields empty
// ParticipantStateAtOrigin.
func TestCreateScene_StructurelessOriginEmptyParticipants(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res := sendT(t, w, sim.CreateScene("atmosphere_refresh", "", now))
	sceneID := res.(sim.SceneID)

	snap := w.Published()
	scene := snap.Scenes[sceneID]
	if scene == nil {
		t.Fatal("scene not in snapshot")
	}
	if scene.OriginStructureID != "" {
		t.Errorf("OriginStructureID = %q, want empty", scene.OriginStructureID)
	}
	if len(scene.ParticipantStateAtOrigin) != 0 {
		t.Errorf("ParticipantStateAtOrigin = %v, want empty", scene.ParticipantStateAtOrigin)
	}
}

// TestJoinHuddle_AssociatesWithSceneWhenProvided covers the scene-
// association path: when a sceneID is passed to JoinHuddle, the joined
// huddle gets added to Scene.Huddles. With no sceneID, the scene set
// stays untouched.
func TestJoinHuddle_AssociatesWithSceneWhenProvided(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sceneID := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now)).(sim.SceneID)

	res := sendT(t, w, sim.JoinHuddle("alice", "tavern", sceneID, now)).(sim.JoinHuddleResult)

	snap := w.Published()
	scene := snap.Scenes[sceneID]
	if _, in := scene.Huddles[res.HuddleID]; !in {
		t.Errorf("scene Huddles missing %q after associated join", res.HuddleID)
	}
}

// TestJoinHuddle_SameStructureIdempotent locks in the idempotent path:
// alice in the tavern huddle joins the tavern huddle again. No leave/
// rejoin churn, HuddleID stable, HuddleNew=false, no fake events, no
// warrant re-stamping. The only side effect of a same-structure rejoin
// is optional scene association (covered separately).
func TestJoinHuddle_SameStructureIdempotent(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Subscriber registered up front so we see ALL events from the
	// idempotent rejoin (or, expected, none beyond the initial join's).
	var (
		mu     sync.Mutex
		events []sim.Event
	)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				mu.Lock()
				events = append(events, evt)
				mu.Unlock()
			}))
			return nil, nil
		},
	})

	// Two members so leaving+rejoin would observably emit a HuddleLeft
	// for alice + an ActorMet for the rejoin pair if churn happened.
	first := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now.Add(time.Second)))

	mu.Lock()
	preCount := len(events)
	mu.Unlock()

	// Clear warrants so we can verify the idempotent path DOESN'T
	// re-stamp.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].WarrantedSince = nil
			world.Actors["bob"].WarrantedSince = nil
			return nil, nil
		},
	})

	res := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now.Add(time.Minute))).(sim.JoinHuddleResult)

	if res.HuddleID != first.HuddleID {
		t.Errorf("idempotent rejoin minted new HuddleID: got %q, want %q", res.HuddleID, first.HuddleID)
	}
	if res.HuddleNew {
		t.Error("HuddleNew=true on idempotent rejoin")
	}
	if len(res.OtherMembers) != 1 || res.OtherMembers[0] != "bob" {
		t.Errorf("idempotent rejoin OtherMembers = %v, want [bob]", res.OtherMembers)
	}

	mu.Lock()
	postCount := len(events)
	mu.Unlock()
	if postCount != preCount {
		t.Errorf("idempotent rejoin emitted %d events, want 0", postCount-preCount)
	}

	// No warrant re-stamp from the idempotent path.
	post, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return [2]*time.Time{world.Actors["alice"].WarrantedSince, world.Actors["bob"].WarrantedSince}, nil
		},
	})
	stamps := post.([2]*time.Time)
	if stamps[0] != nil || stamps[1] != nil {
		t.Errorf("idempotent rejoin stamped warrants: %+v", stamps)
	}
}

// TestJoinHuddle_SameStructureIdempotentAddsScene covers the one
// permitted side effect of an idempotent rejoin: when a sceneID is
// supplied, the existing huddle is added to that scene's Huddles set.
// This lets a fresh cascade scene that fires at an already-active
// structure observe the in-flight huddle.
func TestJoinHuddle_SameStructureIdempotentAddsScene(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	first := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	sceneID := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now.Add(time.Second))).(sim.SceneID)
	// CreateScene also adds the active huddle to scene at mint — wipe
	// the scene's Huddles set so we can verify the idempotent rejoin
	// re-adds it.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Scenes[sceneID].Huddles = map[sim.HuddleID]struct{}{}
			return nil, nil
		},
	})

	sendT(t, w, sim.JoinHuddle("alice", "tavern", sceneID, now.Add(2*time.Second)))

	snap := w.Published()
	if _, in := snap.Scenes[sceneID].Huddles[first.HuddleID]; !in {
		t.Error("idempotent rejoin didn't add huddle to scene")
	}
}

// TestJoinHuddle_RejectsEmptyStructure covers fix 4: empty structureID
// is invalid input.
func TestJoinHuddle_RejectsEmptyStructure(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.JoinHuddle("alice", "", "", time.Now().UTC()))
	if err == nil {
		t.Error("expected error for empty structureID")
	}
}

// TestJoinHuddle_RejectsUnknownStructure covers fix 4: a structureID
// that isn't in the world is rejected — silent acceptance would create
// a huddle attached to a missing structure, with downstream perception
// lookups failing in non-obvious ways.
func TestJoinHuddle_RejectsUnknownStructure(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.JoinHuddle("alice", "ghost-structure", "", time.Now().UTC()))
	if err == nil {
		t.Error("expected error for unknown structureID")
	}
}

// TestJoinHuddle_RejectsUnknownScene covers fix 2: a sceneID that
// doesn't match a known scene is rejected — silent drop of the scene
// association would contradict the emitted HuddleJoined event's
// SceneID field.
func TestJoinHuddle_RejectsUnknownScene(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.JoinHuddle("alice", "tavern", "ghost-scene", time.Now().UTC()))
	if err == nil {
		t.Error("expected error for unknown sceneID")
	}
}

// TestCreateScene_RejectsUnknownStructure covers fix 4 on the scene
// side: an originStructureID that doesn't exist is rejected (empty
// remains the legitimate "non-structure-tied cascade" sentinel).
func TestCreateScene_RejectsUnknownStructure(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.CreateScene("pc_speak", "ghost-structure", time.Now().UTC()))
	if err == nil {
		t.Error("expected error for unknown originStructureID")
	}
}

// TestCreateScene_AddsActiveHuddleAtOrigin covers fix 3: a scene minted
// at a structure that already has an active huddle gets that huddle in
// its Huddles set, so subsequent perception observes the in-flight
// conversation from mint without waiting for a join.
func TestCreateScene_AddsActiveHuddleAtOrigin(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	join := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	sceneID := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now.Add(time.Second))).(sim.SceneID)

	snap := w.Published()
	if _, in := snap.Scenes[sceneID].Huddles[join.HuddleID]; !in {
		t.Errorf("scene Huddles missing active origin huddle %q", join.HuddleID)
	}
}

// TestCreateScene_CapturesHuddleMembersNotInStructureIndex covers the
// R2 follow-up: when the active huddle's members aren't (yet) in
// actorsByStructure (locomotion port still pending — until then,
// JoinHuddle doesn't set InsideStructureID), the scene mint still
// captures their baselines via the huddle membership union. Without
// this, a scene at a structure with an active huddle would observe
// the huddle but have empty ParticipantStateAtOrigin.
func TestCreateScene_CapturesHuddleMembersNotInStructureIndex(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// alice joins a huddle at the tavern but is NOT in
	// actorsByStructure["tavern"] (no locomotion command exists in
	// PR 1 to set that up).
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	// Sanity check the gap exists (otherwise this test is vacuous).
	val, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["alice"].InsideStructureID, nil
		},
	})
	if got := val.(sim.StructureID); got != "" {
		t.Fatalf("test setup assumed alice has no InsideStructureID, got %q", got)
	}

	sceneID := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now.Add(time.Second))).(sim.SceneID)

	snap := w.Published()
	if _, in := snap.Scenes[sceneID].ParticipantStateAtOrigin["alice"]; !in {
		t.Error("scene ParticipantStateAtOrigin missing alice (huddle-membership union path)")
	}
}

// TestLeaveHuddle_StampsLeaver covers fix 5: the leaver's
// WarrantedSince is stamped (they're now in a different macro-state and
// the scheduler should pick them up to re-plan). Previous behavior only
// stamped remaining members.
func TestLeaveHuddle_StampsLeaver(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))

	// Clear alice's warrant from the join so the leave-side stamp is
	// observable in isolation.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].WarrantedSince = nil
			return nil, nil
		},
	})

	sendT(t, w, sim.LeaveHuddle("alice", now.Add(time.Minute)))

	post, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["alice"].WarrantedSince, nil
		},
	})
	if post == nil || post.(*time.Time) == nil {
		t.Error("alice WarrantedSince not stamped by her own leave")
	}
}

// TestLeaveHuddle_ConcludeDetachesFromScenes covers fix 6: when a leave
// concludes a huddle, that huddle is removed from every Scene.Huddles
// set referencing it. Scene.Huddles is "currently active observed
// huddles" — readers shouldn't have to filter ConcludedAt.
func TestLeaveHuddle_ConcludeDetachesFromScenes(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sceneID := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now)).(sim.SceneID)
	join := sendT(t, w, sim.JoinHuddle("alice", "tavern", sceneID, now.Add(time.Second))).(sim.JoinHuddleResult)

	// Pre: scene observes the huddle.
	{
		snap := w.Published()
		if _, in := snap.Scenes[sceneID].Huddles[join.HuddleID]; !in {
			t.Fatal("test setup: scene should observe the huddle pre-conclude")
		}
	}

	// Solo leave concludes the huddle.
	sendT(t, w, sim.LeaveHuddle("alice", now.Add(2*time.Second)))

	snap := w.Published()
	if _, in := snap.Scenes[sceneID].Huddles[join.HuddleID]; in {
		t.Error("scene still references concluded huddle in Huddles set")
	}
}

// TestConcludeHuddle_DetachesFromScenes covers fix 6 on the force-
// conclude path: ConcludeHuddle removes the huddle from every scene
// referencing it, same as the conclude-on-empty branch in
// leaveCurrentHuddle.
func TestConcludeHuddle_DetachesFromScenes(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	sceneID := sendT(t, w, sim.CreateScene("pc_speak", "tavern", now)).(sim.SceneID)
	join := sendT(t, w, sim.JoinHuddle("alice", "tavern", sceneID, now.Add(time.Second))).(sim.JoinHuddleResult)

	if _, err := w.Send(sim.ConcludeHuddle(join.HuddleID, now.Add(2*time.Second))); err != nil {
		t.Fatalf("ConcludeHuddle: %v", err)
	}

	snap := w.Published()
	if _, in := snap.Scenes[sceneID].Huddles[join.HuddleID]; in {
		t.Error("scene still references force-concluded huddle")
	}
}
