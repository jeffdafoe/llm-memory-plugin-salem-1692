package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pc_idle_audience_test.go — LLM-466. The audience predicate's two halves and
// the candle prompt's raise/clear edges.
//
// The regression that motivates the whole file is
// TestAudienceActive_ConnectedButIdleIsNotAudience: before this ticket a live
// socket alone held audience true forever, so an abandoned browser tab kept the
// village deliberating at full cadence indefinitely.

// buildIdleWorld stands up a stopped world (no Run goroutine — every case here
// drives commands synchronously through Send on a running world) with one PC.
func buildIdleWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"player": {ID: "player", Kind: sim.KindPC, DisplayName: "Player", LoginUsername: "jeff"},
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared, DisplayName: "Hannah"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// stampPC sets the two PC stamps directly. A nil argument leaves that stamp nil
// (the "never happened" case both predicates read as stale).
func stampPC(t *testing.T, w *sim.World, id sim.ActorID, seenAt, activityAt *time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].LastPCSeenAt = seenAt
		world.Actors[id].LastPCActivityAt = activityAt
		return nil, nil
	}}); err != nil {
		t.Fatalf("stampPC: %v", err)
	}
}

func audienceAt(t *testing.T, w *sim.World, now time.Time) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.AudienceActive(world, now), nil
	}})
	if err != nil {
		t.Fatalf("AudienceActive: %v", err)
	}
	return res.(bool)
}

func promptPending(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].IdlePromptPending, nil
	}})
	if err != nil {
		t.Fatalf("read IdlePromptPending: %v", err)
	}
	return res.(bool)
}

// ---- AudienceActive ------------------------------------------------------

// TestAudienceActive_ConnectedButIdleIsNotAudience is the LLM-466 regression:
// the exact live shape from the ticket — a socket the heartbeat keeps fresh
// forever, behind which nobody has touched anything for hours.
func TestAudienceActive_ConnectedButIdleIsNotAudience(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second) // heartbeat stamped 5s ago: socket alive
	ancient := now.Add(-6 * time.Hour) // last human input: overnight
	stampPC(t, w, "player", &fresh, &ancient)

	if audienceAt(t, w, now) {
		t.Error("a live socket with no player input for 6h must NOT count as an audience")
	}
}

// A player who is watching without playing still counts: activity inside the
// horizon is enough, no matter that they've issued no in-world action.
func TestAudienceActive_ConnectedAndRecentlyActiveIsAudience(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	watching := now.Add(-30 * time.Minute) // inside the 1h horizon
	stampPC(t, w, "player", &fresh, &watching)

	if !audienceAt(t, w, now) {
		t.Error("a connected client active within the idle horizon must count as an audience")
	}
}

// The presence half still governs on its own: a player who was active and then
// closed the tab is gone the moment the socket goes stale, without waiting out
// the idle horizon.
func TestAudienceActive_DisconnectedIsNotAudience(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	stale := now.Add(-5 * time.Minute) // past the 40s presence horizon
	active := now.Add(-5 * time.Minute)
	stampPC(t, w, "player", &stale, &active)

	if audienceAt(t, w, now) {
		t.Error("a dropped socket is not an audience even with recent activity")
	}
}

// A nil activity stamp (a PC nothing has touched this session) is idle by
// design — the same posture PCPresenceStale takes on a nil presence stamp.
func TestAudienceActive_NilActivityIsNotAudience(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	stampPC(t, w, "player", &fresh, nil)

	if audienceAt(t, w, now) {
		t.Error("a nil activity stamp must read as idle")
	}
}

// The horizon is the configured one, not a hardcoded hour — this is the knob a
// live verification turns down so it doesn't have to wait an hour.
func TestAudienceActive_HonorsConfiguredHorizon(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	activity := now.Add(-2 * time.Minute)
	stampPC(t, w, "player", &fresh, &activity)

	if !audienceAt(t, w, now) {
		t.Fatal("2 minutes idle is inside the 1h default horizon")
	}
	idle := 60
	if _, err := w.Send(sim.SetEcoMode(nil, nil, nil, &idle)); err != nil {
		t.Fatalf("SetEcoMode: %v", err)
	}
	if audienceAt(t, w, now) {
		t.Error("2 minutes idle must be past a 60s horizon")
	}
}

// ---- TouchPCActivity / TouchPCInput --------------------------------------

// An in-world action restores audience: a player who comes back and simply
// walks somewhere has answered the question as well as one who clicks.
func TestTouchPCInput_RefreshesAudienceAndClearsPrompt(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !promptPending(t, w, "player") {
		t.Fatal("sweep must have raised the prompt")
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.TouchPCInput(world, "player", now)
		return nil, nil
	}}); err != nil {
		t.Fatalf("TouchPCInput: %v", err)
	}

	if promptPending(t, w, "player") {
		t.Error("a deliberate action must clear the pending prompt")
	}
	if !audienceAt(t, w, now) {
		t.Error("a deliberate action must restore audience")
	}
}

// The candle ack must NOT count as an in-world action: it proves a watcher
// without being an act of the character, so the idle-auto-bed timer
// (LastPCInputAt) has to stay where it was.
func TestAckPCIdlePrompt_DoesNotTouchInputCursor(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	acted := now.Add(-40 * time.Minute)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["player"].LastPCInputAt = &acted
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed input cursor: %v", err)
	}

	if _, err := w.Send(sim.AckPCIdlePrompt("player", now)); err != nil {
		t.Fatalf("AckPCIdlePrompt: %v", err)
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["player"]
		if a.LastPCInputAt == nil || !a.LastPCInputAt.Equal(acted) {
			t.Errorf("LastPCInputAt = %v, want unchanged %v — the ack is not an in-world action", a.LastPCInputAt, acted)
		}
		if a.LastPCActivityAt == nil || !a.LastPCActivityAt.Equal(now) {
			t.Errorf("LastPCActivityAt = %v, want %v", a.LastPCActivityAt, now)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("verify cursors: %v", err)
	}
}

// Answering with no prompt up is a no-op ack, not an error — a click that races
// an in-world action which already cleared it must still stamp.
func TestAckPCIdlePrompt_IdempotentWithoutPendingPrompt(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, err := w.Send(sim.AckPCIdlePrompt("player", now))
	if err != nil {
		t.Fatalf("AckPCIdlePrompt: %v", err)
	}
	if cleared, _ := res.(bool); cleared {
		t.Error("cleared = true with no prompt pending, want false")
	}
}

// ---- SweepPCIdleAudience -------------------------------------------------

// The sweep is edge-triggered: a PC that stays idle is asked once, not every
// 15s pass forever.
func TestSweepPCIdleAudience_RaisesOncePerIdleStretch(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)

	first, err := w.Send(sim.SweepPCIdleAudience(now))
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if raised, _ := first.(int); raised != 1 {
		t.Errorf("first sweep raised %d prompts, want 1", raised)
	}

	second, err := w.Send(sim.SweepPCIdleAudience(now.Add(15 * time.Second)))
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if raised, _ := second.(int); raised != 0 {
		t.Errorf("second sweep raised %d prompts, want 0 (already pending)", raised)
	}
}

// LLM-470: the sweep lowers a prompt whose PC is no longer idle. The reachable
// case is an operator RAISING eco_audience_idle_seconds over a pending prompt —
// the PC becomes un-idle with no activity event, so clear-on-activity alone left
// a candle stranded on that client until it was clicked.
func TestSweepPCIdleAudience_LowersPromptWhenNoLongerIdle(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	activity := now.Add(-10 * time.Minute)
	stampPC(t, w, "player", &fresh, &activity)

	short := 60
	if _, err := w.Send(sim.SetEcoMode(nil, nil, nil, &short)); err != nil {
		t.Fatalf("SetEcoMode short: %v", err)
	}
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep (idle): %v", err)
	}
	if !promptPending(t, w, "player") {
		t.Fatal("precondition: a 10-minute-idle PC must be prompted under a 60s horizon")
	}

	// Raise the horizon back over the PC's idleness. No activity event occurs.
	long := 3600
	if _, err := w.Send(sim.SetEcoMode(nil, nil, nil, &long)); err != nil {
		t.Fatalf("SetEcoMode long: %v", err)
	}
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep (no longer idle): %v", err)
	}

	if promptPending(t, w, "player") {
		t.Error("the sweep must lower a prompt whose PC is no longer idle")
	}
	if !audienceAt(t, w, now) {
		t.Error("precondition check: the PC should read as an audience again")
	}
}

// idle -> active -> idle: the raise and the clear each fire exactly once per
// transition, and the two arms don't fight across consecutive passes (a flicker
// would spam the client with overlay raise/lower frames every 15s).
func TestSweepPCIdleAudience_RaiseClearRaiseEachFireOnce(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)

	// Idle -> prompted.
	first, err := w.Send(sim.SweepPCIdleAudience(now))
	if err != nil {
		t.Fatalf("sweep 1: %v", err)
	}
	if raised, _ := first.(int); raised != 1 {
		t.Errorf("sweep 1 raised %d, want 1", raised)
	}
	// A second pass while still idle must be inert.
	second, err := w.Send(sim.SweepPCIdleAudience(now.Add(15 * time.Second)))
	if err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	if raised, _ := second.(int); raised != 0 {
		t.Errorf("sweep 2 raised %d, want 0 (still pending)", raised)
	}

	// Active -> cleared, and a further pass while active stays inert.
	if _, err := w.Send(sim.AckPCIdlePrompt("player", now)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if promptPending(t, w, "player") {
		t.Fatal("the ack should have cleared the prompt")
	}
	if _, err := w.Send(sim.SweepPCIdleAudience(now.Add(30 * time.Second))); err != nil {
		t.Fatalf("sweep 3: %v", err)
	}
	if promptPending(t, w, "player") {
		t.Error("sweep re-raised a prompt for an active player")
	}

	// Idle again -> raised once more.
	later := now.Add(2 * time.Hour)
	stillFresh := later.Add(-5 * time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["player"].LastPCSeenAt = &stillFresh
		return nil, nil
	}}); err != nil {
		t.Fatalf("refresh presence: %v", err)
	}
	fourth, err := w.Send(sim.SweepPCIdleAudience(later))
	if err != nil {
		t.Fatalf("sweep 4: %v", err)
	}
	if raised, _ := fourth.(int); raised != 1 {
		t.Errorf("sweep 4 raised %d, want 1 (idle again)", raised)
	}
}

// The reconnect path clears a pending prompt in the SAME world command that
// stamps activity — no window where the client is back but still prompted, and
// no dependence on the next 15s sweep pass.
func TestStampPCConnected_ClearsPendingPromptAtomically(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !promptPending(t, w, "player") {
		t.Fatal("precondition: prompt should be pending")
	}

	if _, err := w.Send(sim.StampPCConnected(map[string]struct{}{"jeff": {}}, now)); err != nil {
		t.Fatalf("StampPCConnected: %v", err)
	}
	if promptPending(t, w, "player") {
		t.Error("reconnect must clear the pending prompt in the same command")
	}
}

// A disconnected PC keeps its pending flag: there is no client to tell, and its
// reconnect stamps activity and clears it. Lowering it here would emit a frame
// into the void and lose the flag's meaning for the next connect.
func TestSweepPCIdleAudience_KeepsPromptPendingWhileDisconnected(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep (raise): %v", err)
	}
	if !promptPending(t, w, "player") {
		t.Fatal("precondition: the sweep should have raised the prompt")
	}

	// The client drops. Activity is still ancient, so the PC is still idle.
	stale := now.Add(-5 * time.Minute)
	stampPC(t, w, "player", &stale, &ancient)
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep (disconnected): %v", err)
	}
	if !promptPending(t, w, "player") {
		t.Error("a disconnected PC must keep its pending prompt")
	}
}

// Nobody to ask: a PC whose client has dropped gets no prompt. Its reconnect
// stamps activity anyway, so a prompt raised here would only ever be seen by a
// player who no longer needs to answer it.
func TestSweepPCIdleAudience_SkipsDisconnectedPC(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	stale := now.Add(-5 * time.Minute)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &stale, &ancient)

	res, err := w.Send(sim.SweepPCIdleAudience(now))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if raised, _ := res.(int); raised != 0 {
		t.Errorf("raised %d prompts for a disconnected PC, want 0", raised)
	}
}

// An active player must never see the prompt — the ticket's third acceptance
// criterion.
func TestSweepPCIdleAudience_SkipsActivePC(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	recent := now.Add(-90 * time.Second)
	stampPC(t, w, "player", &fresh, &recent)

	res, err := w.Send(sim.SweepPCIdleAudience(now))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if raised, _ := res.(int); raised != 0 {
		t.Errorf("raised %d prompts for an active player, want 0", raised)
	}
}

// The sweep only ever touches PCs — an NPC has no client and no player.
func TestSweepPCIdleAudience_IgnoresNPCs(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if promptPending(t, w, "hannah") {
		t.Error("the sweep prompted an NPC")
	}
}

// ---- StampConnectedPCsActive ---------------------------------------------

// Opening a client is an input: a player who connects and then only watches
// holds audience for a full horizon before anything is asked of them.
func TestStampConnectedPCsActive_ConnectStartsHorizon(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)
	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := w.Send(sim.StampConnectedPCsActive(map[string]struct{}{"jeff": {}}, now)); err != nil {
		t.Fatalf("StampConnectedPCsActive: %v", err)
	}

	if promptPending(t, w, "player") {
		t.Error("a reconnect must not leave the PC holding a stale prompt")
	}
	if !audienceAt(t, w, now) {
		t.Error("a fresh connection must count as an audience")
	}
}

// The WS connect path stamps both halves in ONE command, so no scan can observe
// a socket-fresh but activity-nil PC mid-registration. Both stamps land and the
// PC is an audience straight away.
func TestStampPCConnected_StampsBothHalves(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	stampPC(t, w, "player", nil, nil) // never attached this session
	if audienceAt(t, w, now) {
		t.Fatal("precondition: an unattached PC is not an audience")
	}

	if _, err := w.Send(sim.StampPCConnected(map[string]struct{}{"jeff": {}}, now)); err != nil {
		t.Fatalf("StampPCConnected: %v", err)
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["player"]
		if a.LastPCSeenAt == nil {
			t.Error("presence stamp missing after connect")
		}
		if a.LastPCActivityAt == nil {
			t.Error("activity stamp missing after connect")
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("verify stamps: %v", err)
	}
	if !audienceAt(t, w, now) {
		t.Error("a freshly connected client must count as an audience")
	}
}

// The heartbeat's own stamp path must never refresh activity — that is exactly
// the bug. StampConnectedPCsSeen touches presence only.
func TestStampConnectedPCsSeen_DoesNotRefreshActivity(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)

	if _, err := w.Send(sim.StampConnectedPCsSeen(map[string]struct{}{"jeff": {}})); err != nil {
		t.Fatalf("StampConnectedPCsSeen: %v", err)
	}

	if audienceAt(t, w, now) {
		t.Error("the presence heartbeat must not manufacture an audience — that is the LLM-466 bug")
	}
}

// ---- LLM-473: the candle never stacks on the sleep overlay ----------------

// bedPC puts a PC to sleep directly, the way the auto-bed sweeps leave it: a
// SleepingUntil in the future. Bypasses the grant machinery — these cases are
// about the prompt, not about where a PC is allowed to bed down.
func bedPC(t *testing.T, w *sim.World, id sim.ActorID, until time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].SleepingUntil = &until
		return nil, nil
	}}); err != nil {
		t.Fatalf("bedPC: %v", err)
	}
}

// A bedded PC is not asked whether anyone is there: the sleep overlay already
// owns the screen and carries its own Wake button, so raising the candle would
// stack two modals and ask a question the player answers by waking anyway.
func TestSweepPCIdleAudience_SkipsSleepingPC(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Connected but long idle — the exact shape that WOULD raise a candle.
	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)
	bedPC(t, w, "player", now.Add(4*time.Hour))

	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("SweepPCIdleAudience: %v", err)
	}

	if promptPending(t, w, "player") {
		t.Error("a sleeping PC must not be prompted — the sleep overlay owns the screen")
	}
	// Only the PROMPT is withheld. Audience still drops on the same stamps, so
	// eco mode paces the village down for a bedded idle PC exactly as before.
	if audienceAt(t, w, now) {
		t.Error("suppressing the candle must not resurrect the audience — eco still engages")
	}
}

// A PC bedded while its candle was already up drops the prompt: otherwise the
// candle sits underneath the sleep overlay where it cannot be clicked, which is
// the same unclearable-flag shape LLM-470 fixed for the horizon-raise case.
func TestSweepPCIdleAudience_LowersPromptWhenPCBedsDown(t *testing.T) {
	w, cancel := buildIdleWorld(t)
	defer cancel()
	now := time.Now().UTC()

	fresh := now.Add(-5 * time.Second)
	ancient := now.Add(-6 * time.Hour)
	stampPC(t, w, "player", &fresh, &ancient)

	if _, err := w.Send(sim.SweepPCIdleAudience(now)); err != nil {
		t.Fatalf("SweepPCIdleAudience (raise): %v", err)
	}
	if !promptPending(t, w, "player") {
		t.Fatal("precondition: an idle connected PC should have been prompted")
	}

	bedPC(t, w, "player", now.Add(4*time.Hour))

	// Re-stamp presence against the later sweep instant: the client is still
	// attached (its 15s heartbeat outpaces the stale horizon), and the lower arm
	// deliberately leaves a DISCONNECTED PC's prompt pending, so letting the
	// stamp age past the horizon here would test the wrong branch entirely.
	later := now.Add(time.Minute)
	stillConnected := later.Add(-5 * time.Second)
	stampPC(t, w, "player", &stillConnected, &ancient)

	if _, err := w.Send(sim.SweepPCIdleAudience(later)); err != nil {
		t.Fatalf("SweepPCIdleAudience (lower): %v", err)
	}

	if promptPending(t, w, "player") {
		t.Error("bedding a prompted PC must lower the candle, not strand it under the sleep overlay")
	}
}
