package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// eco_mode_test.go — LLM-313. The eco pacing gate in EvaluateReactors: while
// eco mode is enabled and no PC has a fresh presence stamp, a warrant cycle
// made entirely of social-bucket (or economy-bucket) kinds waits out the
// configured gap since the actor's last tick; any survival/duty/commerce
// warrant in the cycle — or a fresh player presence, or Force — lifts the
// throttle. Also covers SetEcoMode validation and the idle-backstop /
// visitor-spawn eco pauses (the latter two in their own files' style but
// kept here so the LLM-313 surface reads in one place).
//
// Reuses seedDueWarrant / subscribeReactorTicks (reactor_pr3a_test.go) and
// inspectActor (reactor_test.go), same package.

// buildEcoWorld stands up a running world with one shared-VA NPC ("hannah"),
// one PC ("player"), eco mode armed (social 60s / economy 30s), and the tight
// reactor jitter the other reactor tests use. The PC's presence stamp starts
// ABSENT (nil LastPCSeenAt = stale by design), so the world reads unwatched
// until a test stamps it.
func buildEcoWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared, DisplayName: "Hannah"},
		"player": {ID: "player", Kind: sim.KindPC, DisplayName: "Player"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.ReactorJitterMin = 10 * time.Millisecond
		world.Settings.ReactorJitterMax = 11 * time.Millisecond
		world.Settings.MaxWarrantsPerActor = 16
		world.Settings.EcoEnabled = true
		world.Settings.EcoSocialGap = 60 * time.Second
		world.Settings.EcoEconomyGap = 30 * time.Second
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return w, cancel
}

// seedLastTick gives the actor a single reactor-tick history entry at the
// given instant, so the eco gate (and MinReactorTickGap) have a pacing anchor.
func seedLastTick(t *testing.T, w *sim.World, id sim.ActorID, at time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
		a.RecentReactorTicks.Push(at)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedLastTick: %v", err)
	}
}

// stampPlayerPresent gives the PC a fresh presence stamp so AudienceActive
// reads true at `now`.
func stampPlayerPresent(t *testing.T, w *sim.World, id sim.ActorID, at time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		stamp := at
		world.Actors[id].LastPCSeenAt = &stamp
		return nil, nil
	}}); err != nil {
		t.Fatalf("stampPlayerPresent: %v", err)
	}
}

// npcSpokeMeta builds a social-bucket warrant (NPC speech).
func npcSpokeMeta(speech uint64) sim.WarrantMeta {
	return sim.WarrantMeta{Reason: sim.NPCSpeechWarrantReason{SpeechID: sim.SpeechID(speech), Speaker: "other"}}
}

func TestEcoMode_DefersSocialCycleWhenUnwatched(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	lastTick := now.Add(-30 * time.Second) // past the 5s min-gap, inside the 60s eco gap
	seedLastTick(t, w, "hannah", lastTick)
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(1)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))

	if len(*emitted) != 0 {
		t.Errorf("unwatched social cycle inside the eco gap: want 0 emits, got %d", len(*emitted))
	}
	inspectActor(t, w, "hannah", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("eco gate cleared the warrant — it must stay OPEN (delay, not drop)")
		}
		if a.TickInFlight {
			t.Error("TickInFlight set despite eco deferral (nothing consumed)")
		}
		want := lastTick.Add(60 * time.Second)
		if a.WarrantDueAt == nil || !a.WarrantDueAt.Equal(want) {
			t.Errorf("WarrantDueAt not pushed to last-tick+gap: got %v want %v", a.WarrantDueAt, want)
		}
	})
}

func TestEcoMode_GapElapsed_Emits(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedLastTick(t, w, "hannah", now.Add(-120*time.Second)) // past the 60s eco gap
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(2)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("social cycle past the eco gap must emit: got %d, want 1", len(*emitted))
	}
}

func TestEcoMode_AudiencePresent_NoDeferral(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	stampPlayerPresent(t, w, "player", now.Add(-5*time.Second)) // fresh (40s staleness)
	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(3)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("fresh player presence must lift the eco gate: got %d emits, want 1", len(*emitted))
	}
}

func TestEcoMode_StalePresence_StillDefers(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Stamp far past the 40s default staleness: the player closed the tab.
	stampPlayerPresent(t, w, "player", now.Add(-5*time.Minute))
	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(4)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 0 {
		t.Errorf("a stale presence stamp is not an audience: got %d emits, want 0", len(*emitted))
	}
}

func TestEcoMode_SurvivalWarrantFullSpeed(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{
		{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("a survival warrant must never be eco-paced: got %d emits, want 1", len(*emitted))
	}
}

func TestEcoMode_MixedCycleFullSpeed(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Social chatter riding alongside a red need: the survival warrant makes
	// the whole cycle full speed (the tick consumes the batch either way).
	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{
		npcSpokeMeta(5),
		{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("a mixed cycle with a survival warrant must emit: got %d, want 1", len(*emitted))
	}
}

func TestEcoMode_EconomyBucketUsesEconomyGap(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	lastTick := now.Add(-10 * time.Second) // past min-gap, inside the 30s economy gap
	seedLastTick(t, w, "hannah", lastTick)
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{
		{Reason: sim.RestockWarrantReason{Item: "bread"}},
	}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 0 {
		t.Errorf("unwatched economy cycle inside the economy gap: want 0 emits, got %d", len(*emitted))
	}
	inspectActor(t, w, "hannah", func(a *sim.Actor) {
		want := lastTick.Add(30 * time.Second)
		if a.WarrantDueAt == nil || !a.WarrantDueAt.Equal(want) {
			t.Errorf("WarrantDueAt not pushed by the ECONOMY gap: got %v want %v", a.WarrantDueAt, want)
		}
	})
}

func TestEcoMode_Disabled_NoDeferral(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.EcoEnabled = false
		return nil, nil
	}}); err != nil {
		t.Fatalf("disable eco: %v", err)
	}
	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(6)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("eco disabled must not defer: got %d emits, want 1", len(*emitted))
	}
}

func TestEcoMode_ZeroGapDisablesBucket(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.EcoSocialGap = 0
		return nil, nil
	}}); err != nil {
		t.Fatalf("zero social gap: %v", err)
	}
	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(7)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("a zero social gap disables that bucket's throttle: got %d emits, want 1", len(*emitted))
	}
}

func TestEcoMode_ForceBypasses(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedLastTick(t, w, "hannah", now.Add(-30*time.Second))
	meta := npcSpokeMeta(8)
	meta.Force = true
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{meta}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("Force must bypass the eco gate: got %d emits, want 1", len(*emitted))
	}
}

func TestEcoMode_NoTickHistory_EmitsImmediately(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// No RecentReactorTicks seeded: eco is a rate bound anchored on the last
	// tick, not added latency for an actor that hasn't spoken all boot.
	seedDueWarrant(t, w, "hannah", []sim.WarrantMeta{npcSpokeMeta(9)}, now)
	emitted := subscribeReactorTicks(t, w)

	_, _ = w.Send(sim.EvaluateReactors(now))
	if len(*emitted) != 1 {
		t.Errorf("no tick history must emit immediately: got %d, want 1", len(*emitted))
	}
}

// ---- SetEcoMode ----------------------------------------------------------

func TestSetEcoMode_UpdatesAndEchoes(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()

	enabled := false
	social := 45
	res, err := w.Send(sim.SetEcoMode(&enabled, &social, nil, nil))
	if err != nil {
		t.Fatalf("SetEcoMode: %v", err)
	}
	out, ok := res.(sim.EcoModeSettingsResult)
	if !ok {
		t.Fatalf("SetEcoMode returned %T, want EcoModeSettingsResult", res)
	}
	if out.Enabled {
		t.Error("Enabled = true, want false")
	}
	if out.SocialGapSeconds != 45 {
		t.Errorf("SocialGapSeconds = %d, want 45", out.SocialGapSeconds)
	}
	if out.EconomyGapSeconds != 30 {
		t.Errorf("EconomyGapSeconds = %d, want 30 (unchanged)", out.EconomyGapSeconds)
	}
	if out.Engaged {
		t.Error("Engaged = true with eco disabled, want false")
	}
	// The world's settings actually moved (not just the echo).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if world.Settings.EcoEnabled {
			t.Error("world EcoEnabled still true")
		}
		if world.Settings.EcoSocialGap != 45*time.Second {
			t.Errorf("world EcoSocialGap = %v, want 45s", world.Settings.EcoSocialGap)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("verify settings: %v", err)
	}
}

func TestSetEcoMode_Rejects(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()

	neg := -1
	atHorizon := 90 // == default MaxWarrantAge: a parked cycle could age out
	pastHorizon := 3600
	cases := []struct {
		name    string
		enabled *bool
		social  *int
		economy *int
	}{
		{"all absent", nil, nil, nil},
		{"negative social", nil, &neg, nil},
		{"negative economy", nil, nil, &neg},
		{"social at stale horizon", nil, &atHorizon, nil},
		{"economy past stale horizon", nil, nil, &pastHorizon},
	}
	for _, tc := range cases {
		if _, err := w.Send(sim.SetEcoMode(tc.enabled, tc.social, tc.economy, nil)); err == nil {
			t.Errorf("%s: want ErrInvalidEcoModeSetting, got nil", tc.name)
		}
	}
}

// TestSetEcoMode_ZeroValidUnderTightHorizon: 0 (the explicit off-switch) must
// stay accepted even when MaxWarrantAge is configured so tight that no
// positive gap fits under the eco ceiling (code_review R2 — the horizon
// comparison used to reject 0 when the horizon rounded to 0).
func TestSetEcoMode_ZeroValidUnderTightHorizon(t *testing.T) {
	w, cancel := buildEcoWorld(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.MaxWarrantAge = 500 * time.Millisecond
		return nil, nil
	}}); err != nil {
		t.Fatalf("tighten MaxWarrantAge: %v", err)
	}
	zero := 0
	if _, err := w.Send(sim.SetEcoMode(nil, &zero, &zero, nil)); err != nil {
		t.Errorf("zero gaps under a tight horizon must stay valid: %v", err)
	}
	one := 1
	if _, err := w.Send(sim.SetEcoMode(nil, &one, nil, nil)); err == nil {
		t.Error("a positive gap that cannot fit under the ceiling must be rejected")
	}
}

// ---- Idle backstop eco pause ---------------------------------------------

func TestEvaluateIdleBackstop_EcoSkipsPlainIdle(t *testing.T) {
	// Indoors pins the actor out of the stranded classification (same trick
	// as TestEvaluateIdleBackstop_StampsPastThreshold), so this asserts the
	// PLAIN idle path specifically.
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared, InsideStructureID: "cottage"},
		"player": {ID: "player", Kind: sim.KindPC},
	})
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.EcoEnabled = true
		return nil, nil
	}}); err != nil {
		t.Fatalf("arm eco: %v", err)
	}

	loadAt := loadTimeOf(t, w, "hannah")
	now := loadAt.Add(31 * time.Minute)

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0 (eco withholds the plain idle poke); telemetry=%+v", tm.Stamped, tm)
	}
	if tm.SkippedEco != 1 {
		t.Errorf("SkippedEco = %d, want 1; telemetry=%+v", tm.SkippedEco, tm)
	}

	// Audience back → the very next sweep stamps as usual.
	stampPlayerPresent(t, w, "player", now)
	tm = runEvaluate(t, w, now)
	if tm.Stamped != 1 {
		t.Errorf("Stamped = %d after presence returned, want 1; telemetry=%+v", tm.Stamped, tm)
	}
}
