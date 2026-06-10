package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// setHuddleActivity sets a huddle's StartedAt/LastActivityAt directly on the
// world goroutine so a test can simulate dormancy without waiting wall-clock
// time. Passing a zero LastActivityAt exercises the StartedAt fallback.
func setHuddleActivity(t *testing.T, w *sim.World, id sim.HuddleID, started, lastActivity time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok {
			t.Fatalf("huddle %q not found when setting activity", id)
		}
		h.StartedAt = started
		h.LastActivityAt = lastActivity
		return nil, nil
	}})
}

// huddleConcludedAt reads a huddle's ConcludedAt off the world goroutine.
func huddleConcludedAt(t *testing.T, w *sim.World, id sim.HuddleID) *time.Time {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok {
			return (*time.Time)(nil), nil
		}
		return h.ConcludedAt, nil
	}})
	ct, _ := v.(*time.Time)
	return ct
}

// actorHasWarrantKind reports whether an actor currently carries a warrant of
// the given kind (read on the world goroutine).
func actorHasWarrantKind(t *testing.T, w *sim.World, id sim.ActorID, kind sim.WarrantKind) bool {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok || a == nil {
			return false, nil
		}
		for _, meta := range a.Warrants {
			if meta.Reason != nil && meta.Reason.Kind() == kind {
				return true, nil
			}
		}
		return false, nil
	}})
	return v.(bool)
}

// TestEffectiveHuddleSilence_Defaults pins the zero-settings fallbacks and the
// honor-configured path for both knobs.
func TestEffectiveHuddleSilence_Defaults(t *testing.T) {
	if got := sim.EffectiveHuddleSilenceTimeout(sim.WorldSettings{}); got != sim.HuddleSilenceTimeoutDefault {
		t.Errorf("timeout(zero) = %v, want %v", got, sim.HuddleSilenceTimeoutDefault)
	}
	if got := sim.EffectiveHuddleSilenceSweepCadence(sim.WorldSettings{}); got != sim.HuddleSilenceSweepCadenceDefault {
		t.Errorf("cadence(zero) = %v, want %v", got, sim.HuddleSilenceSweepCadenceDefault)
	}
	wantT := 45 * time.Minute
	if got := sim.EffectiveHuddleSilenceTimeout(sim.WorldSettings{HuddleSilenceTimeout: wantT}); got != wantT {
		t.Errorf("timeout(configured) = %v, want %v", got, wantT)
	}
	wantC := 30 * time.Second
	if got := sim.EffectiveHuddleSilenceSweepCadence(sim.WorldSettings{HuddleSilenceSweepCadence: wantC}); got != wantC {
		t.Errorf("cadence(configured) = %v, want %v", got, wantC)
	}
}

// TestHuddleSilenceSweep_ConcludesDormantSparesActive is the core lever: a
// huddle idle past the timeout is concluded (members evicted), while a huddle
// with recent activity is left intact.
func TestHuddleSilenceSweep_ConcludesDormantSparesActive(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	dormant := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	active := sendT(t, w, sim.JoinHuddle("bob", "smithy", "", now)).(sim.JoinHuddleResult).HuddleID

	// Default timeout is 2h. Age the dormant huddle past it; keep the active
	// one fresh.
	setHuddleActivity(t, w, dormant, now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	setHuddleActivity(t, w, active, now.Add(-3*time.Hour), now.Add(-1*time.Minute))

	sendT(t, w, sim.EvaluateHuddleSilenceSweep(now))

	if huddleConcludedAt(t, w, dormant) == nil {
		t.Error("dormant huddle should be concluded by the sweep")
	}
	if huddleConcludedAt(t, w, active) != nil {
		t.Error("active huddle (recent activity) must NOT be concluded")
	}

	// The dormant huddle's member is released (CurrentHuddleID cleared).
	snap := w.Published()
	if got := snap.Actors["alice"].CurrentHuddleID; got != "" {
		t.Errorf("alice CurrentHuddleID = %q, want cleared after conclude", got)
	}
	if got := snap.Actors["bob"].CurrentHuddleID; got != active {
		t.Errorf("bob CurrentHuddleID = %q, want %q (active huddle intact)", got, active)
	}
}

// TestHuddleSilenceSweep_StartedAtFallback covers the dormancy baseline when a
// huddle has no LastActivityAt stamp (zero) — the sweep falls back to StartedAt.
func TestHuddleSilenceSweep_StartedAtFallback(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	// Zero LastActivityAt, old StartedAt → measured against StartedAt → dormant.
	setHuddleActivity(t, w, h, now.Add(-3*time.Hour), time.Time{})

	sendT(t, w, sim.EvaluateHuddleSilenceSweep(now))

	if huddleConcludedAt(t, w, h) == nil {
		t.Error("huddle with zero LastActivityAt but old StartedAt should conclude (StartedAt fallback)")
	}
}

// TestHuddleSilenceSweep_SilentConclusion verifies the sweep concludes WITHOUT
// stamping a HuddleConcluded warrant (no member is woken into a tick), unlike
// the explicit ConcludeHuddle command which does stamp it.
func TestHuddleSilenceSweep_SilentConclusion(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	swept := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	explicit := sendT(t, w, sim.JoinHuddle("bob", "smithy", "", now)).(sim.JoinHuddleResult).HuddleID

	setHuddleActivity(t, w, swept, now.Add(-3*time.Hour), now.Add(-3*time.Hour))

	// Silence sweep concludes alice's huddle silently.
	sendT(t, w, sim.EvaluateHuddleSilenceSweep(now))
	// Explicit conclude of bob's huddle DOES stamp the warrant.
	sendT(t, w, sim.ConcludeHuddle(explicit, now))

	if actorHasWarrantKind(t, w, "alice", sim.WarrantKindHuddleConcluded) {
		t.Error("silence-swept member should NOT carry a HuddleConcluded warrant (conclusion must be silent)")
	}
	if !actorHasWarrantKind(t, w, "bob", sim.WarrantKindHuddleConcluded) {
		t.Error("explicitly-concluded member SHOULD carry a HuddleConcluded warrant (control)")
	}
}

// TestHuddleSilenceSweep_ActivityResetsClock confirms touchHuddleActivity (the
// helper the speak / pay-accept sites call) keeps a huddle alive: an otherwise-
// dormant huddle touched to `now` survives the sweep.
func TestHuddleSilenceSweep_ActivityResetsClock(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	setHuddleActivity(t, w, h, now.Add(-3*time.Hour), now.Add(-3*time.Hour))

	// A fresh activity stamp (speech / transaction) resets the dormancy clock.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.TouchHuddleActivity(world, h, now)
		return nil, nil
	}})

	sendT(t, w, sim.EvaluateHuddleSilenceSweep(now))

	if huddleConcludedAt(t, w, h) != nil {
		t.Error("huddle touched to now must survive the sweep")
	}
}

// TestJoinHuddle_StampsActivity locks in that a join sets LastActivityAt (so a
// freshly-joined huddle isn't immediately swept as dormant).
func TestJoinHuddle_StampsActivity(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	snap := w.Published()
	if got := snap.Huddles[h].LastActivityAt; !got.Equal(now) {
		t.Errorf("LastActivityAt after join = %v, want %v", got, now)
	}
}

// TestClearConversationalHuddlesOnBoot covers the boot-clear: every huddle is
// dropped, actor back-refs cleared, and scene observed-huddle refs cleared —
// while the durable scenes themselves remain.
func TestClearConversationalHuddlesOnBoot(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// A real huddle anchored to a structure scene (JoinHuddle with a resolved
	// scene attaches the huddle to scene.Huddles), so the clear has scene refs
	// to wipe.
	res := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		sceneAny, err := sim.CreateScene("colocated_talk", sim.NewStructureBound("tavern"), now).Fn(world)
		if err != nil {
			return nil, err
		}
		sceneID := sceneAny.(sim.SceneID)
		return sim.JoinHuddle("alice", "tavern", sceneID, now).Fn(world)
	}}).(sim.JoinHuddleResult)
	huddleID := res.HuddleID

	// Sanity: huddle + back-ref + scene ref all present pre-clear.
	pre := w.Published()
	if _, ok := pre.Huddles[huddleID]; !ok {
		t.Fatal("huddle should exist before boot-clear")
	}
	if pre.Actors["alice"].CurrentHuddleID != huddleID {
		t.Fatal("alice should reference the huddle before boot-clear")
	}

	// Boot-clear (direct, pre-Run semantics — but safe here under Send since we
	// run it on the world goroutine via a command for the test harness).
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.ClearConversationalHuddlesOnBoot(world)
		return nil, nil
	}})

	post := w.Published()
	if len(post.Huddles) != 0 {
		t.Errorf("Huddles after boot-clear = %d, want 0", len(post.Huddles))
	}
	if got := post.Actors["alice"].CurrentHuddleID; got != "" {
		t.Errorf("alice CurrentHuddleID after boot-clear = %q, want cleared", got)
	}
	// Durable scenes survive; their observed-huddle refs are cleared.
	for sid, s := range post.Scenes {
		if len(s.Huddles) != 0 {
			t.Errorf("scene %q still has %d huddle refs after boot-clear, want 0", sid, len(s.Huddles))
		}
	}
	if len(post.Scenes) == 0 {
		t.Error("durable scenes should NOT be removed by boot-clear")
	}
}
