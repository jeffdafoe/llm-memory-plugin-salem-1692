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
