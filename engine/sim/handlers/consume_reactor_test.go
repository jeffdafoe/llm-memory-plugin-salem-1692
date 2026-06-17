package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// consume_reactor_test.go — coverage of the ItemConsumed self-narration
// subscriber (handleConsumedNarrationWarrants). Drives it by sending a real
// Consume so the test exercises emit → subscriber → warrant. No village object
// is seeded, so no dwell credit is stamped — keeping the ConsumedWarrantReason
// the sole warrant under test.

func buildConsumeReactorWorld(t *testing.T, hunger int) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   time.Now().UTC(),
			Pos:              sim.TilePos{X: 105, Y: 100},
			Needs:            map[sim.NeedKey]int{"hunger": hunger},
			Inventory:        map[sim.ItemKind]int{"stew": 2},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	handlers.RegisterConsumeHandlers(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

func consumeWarrantReason(t *testing.T, w *sim.World, id sim.ActorID) (sim.ConsumedWarrantReason, bool) {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return []sim.WarrantMeta(nil), nil
		}
		return append([]sim.WarrantMeta(nil), a.Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peek warrants(%s): %v", id, err)
	}
	for _, m := range v.([]sim.WarrantMeta) {
		if r, ok := m.Reason.(sim.ConsumedWarrantReason); ok {
			return r, true
		}
	}
	return sim.ConsumedWarrantReason{}, false
}

// putActorInHuddle places an actor in a test huddle on the world goroutine,
// keeping the Members ⇄ CurrentHuddleID invariant. memberCount >= 2 adds a
// second peer so the huddle reads as a live conversation; concluded stamps
// ConcludedAt. StartedAt/LastActivityAt are set to now so a silence sweep, if
// running, won't conclude it under the test.
func putActorInHuddle(t *testing.T, w *sim.World, primary sim.ActorID, memberCount int, concluded bool) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		now := time.Now().UTC()
		members := map[sim.ActorID]struct{}{primary: {}}
		if memberCount >= 2 {
			members["test-peer"] = struct{}{}
		}
		hud := &sim.Huddle{
			ID:             "h-test",
			Members:        members,
			StartedAt:      now,
			LastActivityAt: now,
		}
		if concluded {
			hud.ConcludedAt = &now
		}
		world.Huddles[hud.ID] = hud
		world.Actors[primary].CurrentHuddleID = hud.ID
		return nil, nil
	}}); err != nil {
		t.Fatalf("put %s in huddle: %v", primary, err)
	}
}

// TestConsumeSubscriberSuppressedMidConversation — ZBBS-HOME-471. A need-moving
// consume while the actor is in a live, two-party huddle stamps NO warrant: the
// atmosphere beat would otherwise hand the model a turn it spends re-pitching
// the standing sell-cue (the John Ellis "shall I prepare a serving?" double-offer).
func TestConsumeSubscriberSuppressedMidConversation(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 20)
	defer stop()

	putActorInHuddle(t, w, "hannah", 2, false)
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, ok := consumeWarrantReason(t, w, "hannah"); ok {
		t.Error("ConsumedWarrantReason stamped while mid-conversation; want suppressed")
	}
}

// TestConsumeSubscriberKeptBeatSurvivesMidConversation — the Kept > 0 buyer
// notification is EXEMPT from the mid-conversation suppression: a pocketed
// consume_now surplus reaches the buyer only through this beat, and a purchase
// is itself a conversation, so it must still fire inside a live huddle.
func TestConsumeSubscriberKeptBeatSurvivesMidConversation(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 0) // sated → qty 2 all kept (Kept > 0)
	defer stop()

	putActorInHuddle(t, w, "hannah", 2, false)
	if _, err := w.Send(sim.Consume("hannah", "stew", 2, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	got, ok := consumeWarrantReason(t, w, "hannah")
	if !ok {
		t.Fatal("Kept-surplus beat suppressed mid-conversation; want it to survive (buyer notification)")
	}
	if got.NarrationText != "You eat your fill; the rest you tuck away for later." {
		t.Errorf("NarrationText = %q, want the kept-fallback line", got.NarrationText)
	}
}

// TestConsumeSubscriberNotSuppressedInConcludedHuddle — a concluded huddle is
// not a live conversation, so the suppression does not apply and the felt beat
// stamps as usual. Guards the ConcludedAt branch of actorInLiveConversation.
func TestConsumeSubscriberNotSuppressedInConcludedHuddle(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 20)
	defer stop()

	putActorInHuddle(t, w, "hannah", 2, true)
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, ok := consumeWarrantReason(t, w, "hannah"); !ok {
		t.Error("ConsumedWarrantReason suppressed in a concluded huddle; want it stamped")
	}
}

// TestConsumeSubscriberNotSuppressedWhenHuddleExcludesActor — defensive: if a
// stale CurrentHuddleID points at a live two-member huddle that does NOT
// contain the actor, the actor is not actually in that conversation, so the
// felt beat must still stamp. Guards the membership check in
// actorInLiveConversation against a CurrentHuddleID ⇄ Members invariant slip.
func TestConsumeSubscriberNotSuppressedWhenHuddleExcludesActor(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 20)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		now := time.Now().UTC()
		world.Huddles["h-other"] = &sim.Huddle{
			ID:             "h-other",
			Members:        map[sim.ActorID]struct{}{"peer-a": {}, "peer-b": {}},
			StartedAt:      now,
			LastActivityAt: now,
		}
		world.Actors["hannah"].CurrentHuddleID = "h-other" // stale: hannah not a member
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed stale huddle: %v", err)
	}
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, ok := consumeWarrantReason(t, w, "hannah"); !ok {
		t.Error("ConsumedWarrantReason suppressed though the actor is not in the huddle; want it stamped")
	}
}

// TestConsumeSubscriberStampsWarrant — consuming stew while hungry moves
// hunger, so the subscriber stamps a ConsumedWarrantReason with the felt line.
func TestConsumeSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 20)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	got, ok := consumeWarrantReason(t, w, "hannah")
	if !ok {
		t.Fatal("no ConsumedWarrantReason stamped after a need-moving consume")
	}
	if got.ItemKind != "stew" {
		t.Errorf("ItemKind = %q, want stew", got.ItemKind)
	}
	if got.NarrationText != "You eat the stew; the gnawing ebbs." {
		t.Errorf("NarrationText = %q, want the felt consume line", got.NarrationText)
	}
}

// TestConsumeSubscriberDoesNotDedup — two consumes before the warrant list is
// drained stamp two distinct ConsumedWarrantReasons. Locks the dedup-bypass
// posture: ConsumedWarrantReason.DedupDiscriminator()==0 makes
// WarrantMeta.eventSourced() false, so tryStampWarrant skips its source-aware
// dedup entirely — without that, both would collapse under (WarrantKindConsumed, 0).
func TestConsumeSubscriberDoesNotDedup(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, sim.NeedMax) // hunger high so both consumes move it
	defer stop()

	for i := 0; i < 2; i++ {
		if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
			t.Fatalf("Consume %d: %v", i, err)
		}
	}
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return append([]sim.WarrantMeta(nil), world.Actors["hannah"].Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peek warrants: %v", err)
	}
	count := 0
	for _, m := range v.([]sim.WarrantMeta) {
		if _, ok := m.Reason.(sim.ConsumedWarrantReason); ok {
			count++
		}
	}
	if count != 2 {
		t.Errorf("ConsumedWarrantReason count = %d, want 2 (no dedup collapse)", count)
	}
}

// TestConsumeSubscriberSilentOnNoOp — consuming while already sated (hunger 0)
// moves no need (Applied empty), so the audit ItemConsumed still fires but NO
// consume narration warrant is stamped.
func TestConsumeSubscriberSilentOnNoOp(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 0)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, ok := consumeWarrantReason(t, w, "hannah"); ok {
		t.Error("ConsumedWarrantReason stamped on a no-op consume (no need moved); want none")
	}
}

// TestConsumeSubscriberKeptFallbackBeat — a fully-sated clamped consume
// (ZBBS-WORK-391: hunger 0, qty 2 → eat 1, kept 1) moves no need, but the
// kept surplus still produces a beat of its own. This is the one channel
// telling an actor whose consume_now surplus was pocketed on the seller's
// tick that the food is in their pack, so silence here would leave them
// blind to it.
func TestConsumeSubscriberKeptFallbackBeat(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 0)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 2, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	got, ok := consumeWarrantReason(t, w, "hannah")
	if !ok {
		t.Fatal("no ConsumedWarrantReason stamped for a sated clamped consume (Kept > 0); want the pocket beat")
	}
	if got.NarrationText != "You eat your fill; the rest you tuck away for later." {
		t.Errorf("NarrationText = %q, want the kept-fallback line", got.NarrationText)
	}
}

// TestConsumeSubscriberKeptAppendsToBeat — a clamped consume that DID move a
// need appends the pocket clause to the normal felt line.
func TestConsumeSubscriberKeptAppendsToBeat(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 3) // stew Immediate=4: one unit overshoots, second is surplus
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 2, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	got, ok := consumeWarrantReason(t, w, "hannah")
	if !ok {
		t.Fatal("no ConsumedWarrantReason stamped after a need-moving clamped consume")
	}
	want := "You eat the stew; the gnawing ebbs. The rest you tuck away for later."
	if got.NarrationText != want {
		t.Errorf("NarrationText = %q, want %q", got.NarrationText, want)
	}
}
