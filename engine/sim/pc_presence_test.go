package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

func TestPCPresenceStale(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	staleAfter := 40 * time.Second
	fresh := now.Add(-30 * time.Second)
	old := now.Add(-50 * time.Second)
	exact := now.Add(-40 * time.Second)

	cases := []struct {
		name string
		seen *time.Time
		want bool
	}{
		{"nil seen is stale (no client this session)", nil, true},
		{"within threshold is fresh", &fresh, false},
		{"past threshold is stale", &old, true},
		{"exactly at threshold is fresh (not strictly greater)", &exact, false},
	}
	for _, c := range cases {
		if got := sim.PCPresenceStale(c.seen, now, staleAfter); got != c.want {
			t.Errorf("%s: PCPresenceStale = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPCPresenceStaleAfter(t *testing.T) {
	if got := sim.PCPresenceStaleAfter(nil); got != sim.DefaultPCPresenceStaleAfter {
		t.Errorf("nil world: got %v, want default %v", got, sim.DefaultPCPresenceStaleAfter)
	}
	zero := &sim.World{}
	if got := sim.PCPresenceStaleAfter(zero); got != sim.DefaultPCPresenceStaleAfter {
		t.Errorf("unset setting: got %v, want default %v", got, sim.DefaultPCPresenceStaleAfter)
	}
	configured := &sim.World{Settings: sim.WorldSettings{PCPresenceStaleAfter: 90 * time.Second}}
	if got := sim.PCPresenceStaleAfter(configured); got != 90*time.Second {
		t.Errorf("configured setting: got %v, want 90s", got)
	}
}

// buildPresenceTestWorld seeds a PC and an NPC at the tavern, ready to be
// huddled together, and returns the running world.
func buildPresenceTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"player": {ID: "player", DisplayName: "Player", Kind: sim.KindPC, LoginUsername: "player"},
		"nora":   {ID: "nora", DisplayName: "Nora", Kind: sim.KindNPCShared},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func sendPresence(t *testing.T, w *sim.World, cmd sim.Command) any {
	t.Helper()
	v, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return v
}

// huddlePlayerAndNora joins both into one tavern huddle and returns the player's
// huddle id (asserted non-empty + shared).
func huddlePlayerAndNora(t *testing.T, w *sim.World, now time.Time) sim.HuddleID {
	t.Helper()
	sendPresence(t, w, sim.JoinHuddle("nora", "tavern", "", now))
	sendPresence(t, w, sim.JoinHuddle("player", "tavern", "", now))
	snap := w.Published()
	pc := snap.Actors["player"]
	if pc == nil || pc.CurrentHuddleID == "" {
		t.Fatalf("player not in a huddle after join: %+v", pc)
	}
	if snap.Actors["nora"].CurrentHuddleID != pc.CurrentHuddleID {
		t.Fatalf("player and nora not in the same huddle")
	}
	return pc.CurrentHuddleID
}

// A never-polled PC (nil LastPCSeenAt) is stale, so the sweep ejects it from its
// huddle while leaving the NPC behind.
func TestSweepStalePCPresence_EjectsGhostFromHuddle(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	huddleID := huddlePlayerAndNora(t, w, now)

	ejected := sendPresence(t, w, sim.SweepStalePCPresence(now)).(int)
	if ejected != 1 {
		t.Fatalf("ejected = %d, want 1", ejected)
	}
	snap := w.Published()
	if got := snap.Actors["player"].CurrentHuddleID; got != "" {
		t.Errorf("ghost PC still in huddle %q, want ejected", got)
	}
	if got := snap.Actors["nora"].CurrentHuddleID; got != huddleID {
		t.Errorf("nora left the huddle (%q), should remain", got)
	}
}

// LLM-342: presence now derives from the live WebSocket, re-stamped by the
// heartbeat command StampConnectedPCsSeen. A PC whose /pc/me poll has gone stale
// (nil LastPCSeenAt — the hidden/occluded-tab case, where the render loop and its
// poll are suspended) but whose login still holds a live socket is refreshed by
// the heartbeat, so the sweep must NOT eject it.
func TestStampConnectedPCsSeen_KeepsWSConnectedPC(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	huddleID := huddlePlayerAndNora(t, w, now)

	stamped := sendPresence(t, w, sim.StampConnectedPCsSeen(map[string]struct{}{"player": {}})).(int)
	if stamped != 1 {
		t.Fatalf("stamped = %d, want 1 (player login is connected)", stamped)
	}
	ejected := sendPresence(t, w, sim.SweepStalePCPresence(now)).(int)
	if ejected != 0 {
		t.Fatalf("ejected = %d, want 0 (WS-connected PC must stay)", ejected)
	}
	if got := w.Published().Actors["player"].CurrentHuddleID; got != huddleID {
		t.Errorf("WS-connected PC was ejected (%q), should remain in %q", got, huddleID)
	}
}

// The inverse: a PC whose login holds no live socket is not refreshed by the
// heartbeat, so it stays stale and the sweep reclaims it — the closed-tab /
// dropped-socket cleanup ZBBS-WORK-326 introduced, now keyed on the WS instead of
// the poll. A different login being connected must not spare it.
func TestStampConnectedPCsSeen_DisconnectedPCSwept(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	huddlePlayerAndNora(t, w, now)

	stamped := sendPresence(t, w, sim.StampConnectedPCsSeen(map[string]struct{}{"someone-else": {}})).(int)
	if stamped != 0 {
		t.Fatalf("stamped = %d, want 0 (player login not connected)", stamped)
	}
	ejected := sendPresence(t, w, sim.SweepStalePCPresence(now)).(int)
	if ejected != 1 {
		t.Fatalf("ejected = %d, want 1 (disconnected ghost reclaimed)", ejected)
	}
}

// StampConnectedPCsSeen only touches PCs whose login is in the connected set: an
// empty set is a no-op, and an NPC id in the set matches nothing (the command
// keys on LoginUsername over KindPC actors only, never an NPC).
func TestStampConnectedPCsSeen_OnlyConnectedPCs(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()

	if got := sendPresence(t, w, sim.StampConnectedPCsSeen(nil)).(int); got != 0 {
		t.Errorf("empty set stamped %d, want 0", got)
	}
	if got := sendPresence(t, w, sim.StampConnectedPCsSeen(map[string]struct{}{"nora": {}})).(int); got != 0 {
		t.Errorf("NPC-name set stamped %d, want 0 (PC-login only)", got)
	}
}

// A PC that just polled /pc/me (StampPCSeen) is fresh, so the sweep keeps it.
func TestSweepStalePCPresence_KeepsFreshPC(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	huddleID := huddlePlayerAndNora(t, w, now)

	sendPresence(t, w, sim.StampPCSeen("player"))
	ejected := sendPresence(t, w, sim.SweepStalePCPresence(now)).(int)
	if ejected != 0 {
		t.Fatalf("ejected = %d, want 0 (PC was just seen)", ejected)
	}
	if got := w.Published().Actors["player"].CurrentHuddleID; got != huddleID {
		t.Errorf("fresh PC was ejected (%q), should remain in %q", got, huddleID)
	}
}

// A stale PC standing alone (no huddle) is left untouched — nothing to clear.
func TestSweepStalePCPresence_IgnoresPCNotInHuddle(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	ejected := sendPresence(t, w, sim.SweepStalePCPresence(now)).(int)
	if ejected != 0 {
		t.Fatalf("ejected = %d, want 0 (no PC in a huddle)", ejected)
	}
}

// StampPCSeen no-ops on an NPC id: an NPC in a huddle is never treated as a PC
// and never ejected by the presence sweep regardless of timing.
func TestStampPCSeen_NPCUnaffectedBySweep(t *testing.T) {
	w, cancel := buildPresenceTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	huddleID := huddlePlayerAndNora(t, w, now)

	// Stamp the player fresh so only nora could be a candidate; the NPC must
	// still never be swept (sweep is PC-only).
	sendPresence(t, w, sim.StampPCSeen("player"))
	sendPresence(t, w, sim.SweepStalePCPresence(now.Add(time.Hour)))
	if got := w.Published().Actors["nora"].CurrentHuddleID; got != huddleID {
		t.Errorf("NPC nora was ejected (%q), sweep must be PC-only", got)
	}
}

// An NPC arriving next to a STALE (ghost) PC must NOT form a greeting huddle
// with it — the encounter guard skips stale PCs. With the ghost the only nearby
// actor, no huddle forms at all.
func TestArrivalEncounter_SkipsStaleGhostPC(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "nora", x: 100, y: 100},                    // arriving NPC
		{id: "ghost", x: 101, y: 100, kind: sim.KindPC}, // ghost PC, nil seen → stale
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "nora", now)

	snap := w.Published()
	if got := snap.Actors["nora"].CurrentHuddleID; got != "" {
		t.Errorf("nora formed a huddle (%q) with a stale ghost PC — should have skipped it", got)
	}
}

// A STALE PC must not INITIATE an encounter either: when a ghost PC is itself
// the arriver, no huddle forms with nearby NPCs (code_review R1 — the guard has
// to cover the initiator, not just nearby participants).
func TestArrivalEncounter_StalePCInitiatorFormsNoHuddle(t *testing.T) {
	now := time.Now().UTC()
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ghost", x: 100, y: 100, kind: sim.KindPC}, // arriving ghost PC, nil seen → stale
		{id: "nora", x: 101, y: 100},                    // nearby NPC
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "ghost", now)

	snap := w.Published()
	if got := snap.Actors["ghost"].CurrentHuddleID; got != "" {
		t.Errorf("stale PC initiator formed a huddle (%q) — should have been skipped", got)
	}
	if got := snap.Actors["nora"].CurrentHuddleID; got != "" {
		t.Errorf("nora was pulled into a huddle (%q) by a stale PC initiator", got)
	}
}

// An NPC arriving next to a FRESH (recently-polled) PC forms the huddle as
// normal — the guard only skips stale PCs.
func TestArrivalEncounter_HuddlesFreshPC(t *testing.T) {
	now := time.Now().UTC()
	seen := now
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "nora", x: 100, y: 100},
		{id: "player", x: 101, y: 100, kind: sim.KindPC, lastPCSeenAt: &seen},
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "nora", now)

	snap := w.Published()
	hid := snap.Actors["nora"].CurrentHuddleID
	if hid == "" {
		t.Fatal("nora did not form a huddle with a fresh PC")
	}
	if got := snap.Actors["player"].CurrentHuddleID; got != hid {
		t.Errorf("fresh PC not in nora's huddle: player huddle %q, nora huddle %q", got, hid)
	}
}
