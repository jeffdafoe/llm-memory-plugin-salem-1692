package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// dwell_reactor_test.go — coverage of the three dwell-lifecycle event
// subscribers (handleDwellStartedWarrants, handleDwellTickAppliedWarrants,
// handleDwellEndedWarrants). Drives them by sending real Consume + then
// ApplyDwellTick commands so the test exercises the full wire:
// emit → subscriber → warrant on actor.
//
// Source-dedup behavior is tested at the substrate level
// (reactor_pr3a_test.go); these tests only verify the dwell subscribers
// stamp with the right SHAPE.

func buildDwellReactorWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	// The dwell-pin resolver (resolveLoiteringObject) needs a resolvable asset
	// and measures Chebyshev tiles to the loiter pin; a zero loiter offset
	// anchored at the actor's tile (105,100) puts hannah exactly on the pin.
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"inn-thatched": {ID: "inn-thatched"}})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {
			ID: "tavern", AssetID: "inn-thatched",
			DisplayName:   "The Drunken Hare",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.TileToWorld(sim.GridPoint{X: 105, Y: 100}),
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:            "hannah",
			DisplayName:   "Hannah",
			LLMAgent:      "hannah-innkeeper",
			Kind:          sim.KindNPCStateful,
			State:         sim.StateIdle,
			Pos:           sim.TilePos{X: 105, Y: 100},
			Needs:         map[sim.NeedKey]int{"hunger": 20},
			Inventory:     map[sim.ItemKind]int{"stew": 2},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	handlers.RegisterDwellHandlers(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// peekDwellActorWarrants snapshots an actor's warrant list from the world
// goroutine.
func peekDwellActorWarrants(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantMeta {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return []sim.WarrantMeta(nil), nil
		}
		return append([]sim.WarrantMeta(nil), a.Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peekDwellActorWarrants(%s): %v", id, err)
	}
	return v.([]sim.WarrantMeta)
}

// TestDwellStartedSubscriberStampsWarrant — Consume of a dwell-bearing
// item triggers DwellStarted, the subscriber stamps a
// DwellStartedWarrantReason on the eater with the catalog narration
// pre-rendered.
func TestDwellStartedSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	warrants := peekDwellActorWarrants(t, w, "hannah")
	var got *sim.DwellStartedWarrantReason
	for _, m := range warrants {
		if r, ok := m.Reason.(sim.DwellStartedWarrantReason); ok {
			got = &r
			break
		}
	}
	if got == nil {
		t.Fatalf("no DwellStartedWarrantReason on hannah; got %d warrant(s)", len(warrants))
	}
	if got.ItemKind != "stew" {
		t.Errorf("ItemKind = %q, want stew", got.ItemKind)
	}
	if got.StructureID != "tavern" {
		t.Errorf("StructureID = %q, want tavern", got.StructureID)
	}
	if got.NarrationText == "" {
		t.Errorf("NarrationText empty; want catalog hint")
	}
	if len(got.Credits) != 1 || got.Credits[0].Attribute != "hunger" {
		t.Errorf("Credits = %+v, want one hunger credit", got.Credits)
	}
}

// TestDwellTickAppliedSubscriberStampsWarrant — a DwellTickApplied that crosses
// the need out of its red tier fires the boundary wake (ZBBS-WORK-407): the
// subscriber stamps DwellTickAppliedWarrantReason with per-tick narration
// pre-rendered. A tick that crosses no boundary stamps nothing (the NoWake tests
// below).
func TestDwellTickAppliedSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	// Seed a ripe credit directly to avoid the consume path's
	// DwellStarted warrant cluttering the assertion target.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 3
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		// ZBBS-WORK-407: park hunger AT its red threshold so this -1 tick crosses
		// out of the red tier — the boundary the wake is now cadenced to.
		world.Actors["hannah"].Needs["hunger"] = world.Settings.NeedThresholds.Get("hunger")
		// Clear any existing warrants so the assertion target stays
		// uncluttered.
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	warrants := peekDwellActorWarrants(t, w, "hannah")
	var got *sim.DwellTickAppliedWarrantReason
	for _, m := range warrants {
		if r, ok := m.Reason.(sim.DwellTickAppliedWarrantReason); ok {
			got = &r
			break
		}
	}
	if got == nil {
		t.Fatalf("no DwellTickAppliedWarrantReason on hannah; got %d warrant(s)", len(warrants))
	}
	if got.ItemKind != "stew" || got.Attribute != "hunger" {
		t.Errorf("ItemKind/Attribute = %q/%q, want stew/hunger", got.ItemKind, got.Attribute)
	}
	if got.NarrationText != "You take another bite, the gnawing ebbs." {
		t.Errorf("NarrationText = %q", got.NarrationText)
	}
	if got.RemainingTicks == nil || *got.RemainingTicks != 2 {
		t.Errorf("RemainingTicks = %v, want 2", got.RemainingTicks)
	}
	if got.PeriodMinutes != 2 {
		t.Errorf("PeriodMinutes = %d, want 2", got.PeriodMinutes)
	}
}

// TestDwellTickApplied_NoWakeMidDwell — ZBBS-WORK-407. A per-minute tick whose
// recovery leaves the need still inside its red tier crosses no boundary, so the
// subscriber stamps no warrant — the actor isn't woken for a mid-meal minute
// that changes nothing. (Recovery + HUD still happen via the event.)
func TestDwellTickApplied_NoWakeMidDwell(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 3
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		// Well inside red: a -1 tick leaves it still >= threshold, so no boundary
		// is crossed and no wake warrant should be stamped.
		world.Actors["hannah"].Needs["hunger"] = world.Settings.NeedThresholds.Get("hunger") + 10
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	for _, m := range peekDwellActorWarrants(t, w, "hannah") {
		if _, ok := m.Reason.(sim.DwellTickAppliedWarrantReason); ok {
			t.Fatalf("a mid-dwell tick that crossed no boundary stamped a wake warrant: %+v", m.Reason)
		}
	}
}

// TestDwellTickApplied_NoWakeOnTerminalTick — ZBBS-WORK-407. The terminal tick
// would cross the boundary, but completion is DwellEnded's beat, so the tick
// subscriber defers: no DwellTickAppliedWarrantReason, and DwellEnded still wakes
// the actor.
func TestDwellTickApplied_NoWakeOnTerminalTick(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 1 // post-decrement -> 0 this tick: terminal, DwellEnded fires
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		// At the threshold: the tick WOULD cross out of red, but the terminal-tick
		// guard makes it defer to DwellEnded.
		world.Actors["hannah"].Needs["hunger"] = world.Settings.NeedThresholds.Get("hunger")
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var sawTick, sawEnded bool
	for _, m := range peekDwellActorWarrants(t, w, "hannah") {
		switch m.Reason.(type) {
		case sim.DwellTickAppliedWarrantReason:
			sawTick = true
		case sim.DwellEndedWarrantReason:
			sawEnded = true
		}
	}
	if sawTick {
		t.Errorf("terminal tick stamped a dwell-tick wake warrant; DwellEnded should own the completion beat")
	}
	if !sawEnded {
		t.Errorf("terminal tick should still wake via DwellEnded (completion beat)")
	}
}

// TestDwellEndedSubscriberStampsWarrant — ApplyDwellTick on the last
// tick fires DwellEnded{ItemExhausted}, subscriber stamps
// DwellEndedWarrantReason with terminal narration.
func TestDwellEndedSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 1 // last tick
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	warrants := peekDwellActorWarrants(t, w, "hannah")
	var got *sim.DwellEndedWarrantReason
	for _, m := range warrants {
		if r, ok := m.Reason.(sim.DwellEndedWarrantReason); ok {
			got = &r
			break
		}
	}
	if got == nil {
		t.Fatalf("no DwellEndedWarrantReason on hannah; got %d warrant(s)", len(warrants))
	}
	if got.Reason != sim.DwellEndItemExhausted {
		t.Errorf("Reason = %s, want item_exhausted", got.Reason)
	}
	if got.ItemKind != "stew" {
		t.Errorf("ItemKind = %q, want stew", got.ItemKind)
	}
	if got.NarrationText != "You finish the last bite, satisfied." {
		t.Errorf("NarrationText = %q", got.NarrationText)
	}
}

// TestDwellEndedSubscriberSkipsEmptyNarration — LLM-65. An object-source
// walk-away (free, resumable, no resource lost) produces an empty terminal
// narration, so the subscriber must NOT stamp a DwellEndedWarrantReason — an
// empty-text warrant would render the vague "Something happened nearby"
// fallback. The DwellEnded event still fires (the credit is torn down); only
// the perception warrant is suppressed.
func TestDwellEndedSubscriberSkipsEmptyNarration(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	// Seed a ripe object-source thirst credit pinned to "well". hannah stands
	// on the tavern pin (105,100), so she is NOT co-located with the well —
	// ApplyDwellTick reads this as a walk-away (DwellEndWalkedAway), and
	// object-source thirst walk-aways have no narration by design.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject}: {
				ObjectID:           "well",
				Attribute:          "thirst",
				Source:             sim.DwellSourceObject,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// The walk-away path tears the credit down — proves the tick actually took
	// the walk-away branch, so the absence of a warrant below is meaningful.
	gone, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.Actors["hannah"].DwellCredits) == 0, nil
	}})
	if err != nil {
		t.Fatalf("credit check: %v", err)
	}
	if !gone.(bool) {
		t.Fatalf("walked-away credit not torn down — tick didn't take the walk-away path; test is moot")
	}

	for _, m := range peekDwellActorWarrants(t, w, "hannah") {
		if _, ok := m.Reason.(sim.DwellEndedWarrantReason); ok {
			t.Errorf("object-source walk-away stamped a DwellEndedWarrantReason; an empty narration must be silent (LLM-65)")
		}
	}
}

// TestDwellSubscribersBypassDedup — each dwell Reason returns
// DedupDiscriminator=0, so the warrant infrastructure routes them
// through the bypass path (same as BasicWarrantReason). This locks the
// posture so a future per-Reason rewrite doesn't accidentally restore
// dedup keying and collapse unrelated dwell stamps.
func TestDwellSubscribersBypassDedup(t *testing.T) {
	cases := []sim.WarrantReason{
		sim.DwellStartedWarrantReason{ItemKind: "stew"},
		sim.DwellTickAppliedWarrantReason{ItemKind: "stew", Attribute: "hunger"},
		sim.DwellEndedWarrantReason{Reason: sim.DwellEndItemExhausted},
	}
	for _, r := range cases {
		if got := r.DedupDiscriminator(); got != 0 {
			t.Errorf("%T.DedupDiscriminator() = %d, want 0", r, got)
		}
	}
}
