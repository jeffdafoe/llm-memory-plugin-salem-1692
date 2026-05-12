package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildDwellTestWorld seeds an actor at a Shade Tree (object credit,
// tiredness recovery 2 every 10 min, no countdown) plus the Tree's
// village_object. Used by the object-source tests.
func buildDwellTestWorld(t *testing.T, lastCreditedAt time.Time, actorX, actorY int) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"shade_tree": {
			ID: "shade_tree", AssetID: "tree-oak", CurrentState: "default",
			X: 200, Y: 200,
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			CurrentX: actorX,
			CurrentY: actorY,
			Needs:    map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 12},
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "shade_tree", Attribute: "tiredness", Source: sim.DwellSourceObject}: {
					ObjectID:           "shade_tree",
					Attribute:          "tiredness",
					Source:             sim.DwellSourceObject,
					LastCreditedAt:     lastCreditedAt,
					DwellDelta:         -2,
					DwellPeriodMinutes: 10,
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestApplyDwellTickRipeObjectCredit covers the happy path — credit is
// ripe, actor at object, need decrements, anchor advances by exactly
// the period.
func TestApplyDwellTickRipeObjectCredit(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute) // ripe (period 10 min)
	w, cancel := buildDwellTestWorld(t, anchor, 210, 210)
	defer cancel()

	res, err := w.Send(sim.ApplyDwellTick(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	r := res.(sim.DwellTickResult)
	if r.Applied != 1 {
		t.Errorf("Applied = %d, want 1", r.Applied)
	}

	// tiredness dropped from 12 to 10.
	snap := w.Published().Actors["hannah"]
	if snap.Needs["tiredness"] != 10 {
		t.Errorf("tiredness = %d, want 10", snap.Needs["tiredness"])
	}

	// Anchor advanced by exactly the period (10 min).
	dc, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			k := sim.DwellCreditKey{ObjectID: "shade_tree", Attribute: "tiredness", Source: sim.DwellSourceObject}
			return *world.Actors["hannah"].DwellCredits[k], nil
		},
	})
	wantAnchor := anchor.Add(10 * time.Minute)
	if !dc.(sim.DwellCredit).LastCreditedAt.Equal(wantAnchor) {
		t.Errorf("anchor = %v, want %v", dc.(sim.DwellCredit).LastCreditedAt, wantAnchor)
	}
}

// TestApplyDwellTickUnripeSkipped covers the period-not-elapsed branch.
func TestApplyDwellTickUnripeSkipped(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-5 * time.Minute) // not yet ripe
	w, cancel := buildDwellTestWorld(t, anchor, 210, 210)
	defer cancel()

	res, _ := w.Send(sim.ApplyDwellTick(now))
	if res.(sim.DwellTickResult).Applied != 0 {
		t.Errorf("Applied = %d, want 0", res.(sim.DwellTickResult).Applied)
	}
	if w.Published().Actors["hannah"].Needs["tiredness"] != 12 {
		t.Errorf("tiredness changed despite unripe credit")
	}
}

// TestApplyDwellTickWalkedOffDeletesCredit covers actor moved away.
func TestApplyDwellTickWalkedOffDeletesCredit(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute)
	w, cancel := buildDwellTestWorld(t, anchor, 1000, 1000) // far from shade_tree (200,200)
	defer cancel()

	_, _ = w.Send(sim.ApplyDwellTick(now))

	count, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return len(world.Actors["hannah"].DwellCredits), nil
		},
	})
	if count.(int) != 0 {
		t.Errorf("credit count after walk-off = %d, want 0", count.(int))
	}
	// tiredness unchanged (no delta applied when actor walked off).
	if w.Published().Actors["hannah"].Needs["tiredness"] != 12 {
		t.Errorf("tiredness applied despite walk-off")
	}
}

// TestApplyDwellTickItemCountdown covers source=item: remaining_ticks
// decrements per applied tick.
func TestApplyDwellTickItemCountdown(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute)
	repo, handles := mem.NewRepository()
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {ID: "inn", AssetID: "inn-thatched", X: 100, Y: 100},
	})
	remaining := 3
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			CurrentX: 110, CurrentY: 110,
			Needs: map[sim.NeedKey]int{"hunger": 15, "thirst": 5, "tiredness": 5},
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}: {
					ObjectID:           "inn",
					Attribute:          "hunger",
					Source:             sim.DwellSourceItem,
					LastCreditedAt:     anchor,
					RemainingTicks:     &remaining,
					DwellDelta:         -3,
					DwellPeriodMinutes: 10,
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	_, _ = w.Send(sim.ApplyDwellTick(now))

	rt, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			k := sim.DwellCreditKey{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}
			c := world.Actors["hannah"].DwellCredits[k]
			if c == nil {
				return -1, nil
			}
			return *c.RemainingTicks, nil
		},
	})
	if rt.(int) != 2 {
		t.Errorf("RemainingTicks after tick = %d, want 2", rt.(int))
	}
	if w.Published().Actors["hannah"].Needs["hunger"] != 12 {
		t.Errorf("hunger = %d, want 12 (15 - 3)", w.Published().Actors["hannah"].Needs["hunger"])
	}
}

// TestApplyDwellTickItemExhaustedDeletes covers RemainingTicks=1 →
// credit applied and deleted, completion narration stamped for PC.
func TestApplyDwellTickItemExhaustedDeletes(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute)
	repo, handles := mem.NewRepository()
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {ID: "inn", AssetID: "inn-thatched", X: 100, Y: 100},
	})
	remaining := 1
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"player": {
			ID:            "player",
			LoginUsername: "alice",
			CurrentX:      105, CurrentY: 100,
			Needs: map[sim.NeedKey]int{"hunger": 8, "thirst": 5, "tiredness": 5},
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}: {
					ObjectID:           "inn",
					Attribute:          "hunger",
					Source:             sim.DwellSourceItem,
					LastCreditedAt:     anchor,
					RemainingTicks:     &remaining,
					DwellDelta:         -3,
					DwellPeriodMinutes: 10,
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, _ := w.Send(sim.ApplyDwellTick(now))
	r := res.(sim.DwellTickResult)
	if r.Applied != 1 {
		t.Errorf("Applied = %d, want 1", r.Applied)
	}
	if len(r.Completions) != 1 {
		t.Fatalf("Completions = %d, want 1", len(r.Completions))
	}
	c := r.Completions[0]
	if !c.ItemExhausted {
		t.Error("Completion ItemExhausted = false, want true")
	}
	if c.Text != "You finish the last bite, satisfied." {
		t.Errorf("Completion Text = %q", c.Text)
	}

	// Credit deleted.
	count, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return len(world.Actors["player"].DwellCredits), nil
		},
	})
	if count.(int) != 0 {
		t.Errorf("credits remaining = %d, want 0", count.(int))
	}
}

// TestApplyDwellTickFloorHitNarration covers source=object reaching
// hunger=0 → "You feel full." for PC.
func TestApplyDwellTickFloorHitNarration(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute)
	repo, handles := mem.NewRepository()
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {ID: "bush", AssetID: "bush-berries", X: 0, Y: 0},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"player": {
			ID:            "player",
			LoginUsername: "alice",
			CurrentX:      0, CurrentY: 0,
			Needs: map[sim.NeedKey]int{"hunger": 1},
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "bush", Attribute: "hunger", Source: sim.DwellSourceObject}: {
					ObjectID:           "bush",
					Attribute:          "hunger",
					Source:             sim.DwellSourceObject,
					LastCreditedAt:     anchor,
					DwellDelta:         -2,
					DwellPeriodMinutes: 10,
				},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, _ := w.Send(sim.ApplyDwellTick(now))
	r := res.(sim.DwellTickResult)
	if len(r.Completions) != 1 || !r.Completions[0].FloorHit {
		t.Fatalf("expected floor-hit narration, got %+v", r.Completions)
	}
	if r.Completions[0].Text != "You feel full." {
		t.Errorf("narration = %q, want 'You feel full.'", r.Completions[0].Text)
	}
}

// TestApplyDwellTickNPCSkipsNarration covers the NPC branch — no
// LoginUsername means no completion stamped (NPCs perceive via the
// next tick build, not a private room_event).
func TestApplyDwellTickNPCSkipsNarration(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute)
	w, cancel := buildDwellTestWorld(t, anchor, 200, 200)
	defer cancel()

	// hannah has LLMAgent="hannah-innkeeper", no LoginUsername. Drive
	// tiredness to 1 so the next tick triggers floor-hit.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["hannah"].Needs["tiredness"] = 1
			return nil, nil
		},
	})

	res, _ := w.Send(sim.ApplyDwellTick(now))
	r := res.(sim.DwellTickResult)
	if r.Applied != 1 {
		t.Errorf("Applied = %d, want 1", r.Applied)
	}
	if len(r.Completions) != 0 {
		t.Errorf("NPC produced completions: %+v, want none", r.Completions)
	}
}

// TestUpsertItemDwellCreditsHappy covers the basic upsert.
func TestUpsertItemDwellCreditsHappy(t *testing.T) {
	actor := &sim.Actor{ID: "p", DwellCredits: nil}
	now := time.Now().UTC()
	sim.UpsertItemDwellCredits(actor, []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 3, DwellPeriodMinutes: 15, DwellTotalTicks: 4},
		{Attribute: "thirst", DwellAmount: 2, DwellPeriodMinutes: 10, DwellTotalTicks: 3},
	}, "inn", now)

	if len(actor.DwellCredits) != 2 {
		t.Fatalf("DwellCredits count = %d, want 2", len(actor.DwellCredits))
	}
	hunger := actor.DwellCredits[sim.DwellCreditKey{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}]
	if hunger == nil || hunger.DwellDelta != -3 || hunger.DwellPeriodMinutes != 15 {
		t.Errorf("hunger credit = %+v", hunger)
	}
	if hunger.RemainingTicks == nil || *hunger.RemainingTicks != 4 {
		t.Errorf("hunger RemainingTicks = %v, want 4", hunger.RemainingTicks)
	}
}

// TestUpsertItemDwellCreditsSkipsIncomplete covers missing dwell triple.
func TestUpsertItemDwellCreditsSkipsIncomplete(t *testing.T) {
	actor := &sim.Actor{ID: "p"}
	now := time.Now().UTC()
	sim.UpsertItemDwellCredits(actor, []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 0, DwellPeriodMinutes: 15, DwellTotalTicks: 4},   // amount=0 → skip
		{Attribute: "thirst", DwellAmount: 2, DwellPeriodMinutes: 0, DwellTotalTicks: 3},    // period=0 → skip
		{Attribute: "tiredness", DwellAmount: 1, DwellPeriodMinutes: 5, DwellTotalTicks: 0}, // ticks=0 → skip
		{Attribute: "tiredness", DwellAmount: 1, DwellPeriodMinutes: 5, DwellTotalTicks: 2}, // valid
	}, "inn", now)

	if len(actor.DwellCredits) != 1 {
		t.Errorf("DwellCredits count = %d, want 1 (only the complete triple)", len(actor.DwellCredits))
	}
}

// TestUpsertItemDwellCreditsSkipsEmptyStructure covers the
// eating-while-walking case (structure unknown).
func TestUpsertItemDwellCreditsSkipsEmptyStructure(t *testing.T) {
	actor := &sim.Actor{ID: "p"}
	sim.UpsertItemDwellCredits(actor, []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 3, DwellPeriodMinutes: 10, DwellTotalTicks: 2},
	}, "", time.Now().UTC())
	if len(actor.DwellCredits) != 0 {
		t.Errorf("empty structureID produced credits: %d", len(actor.DwellCredits))
	}
}

// TestUpsertItemDwellCreditsResetsExisting covers re-consume reset.
func TestUpsertItemDwellCreditsResetsExisting(t *testing.T) {
	initial := time.Now().UTC().Add(-30 * time.Minute)
	earlier := 1
	actor := &sim.Actor{
		ID: "p",
		DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "inn",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     initial,
				RemainingTicks:     &earlier,
				DwellDelta:         -3,
				DwellPeriodMinutes: 10,
			},
		},
	}
	fresh := time.Now().UTC()
	sim.UpsertItemDwellCredits(actor, []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 3, DwellPeriodMinutes: 10, DwellTotalTicks: 4},
	}, "inn", fresh)

	c := actor.DwellCredits[sim.DwellCreditKey{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}]
	if !c.LastCreditedAt.Equal(fresh) {
		t.Errorf("LastCreditedAt = %v, want %v", c.LastCreditedAt, fresh)
	}
	if *c.RemainingTicks != 4 {
		t.Errorf("RemainingTicks = %d, want 4 (reset)", *c.RemainingTicks)
	}
}

// TestDwellCompletionNarrationVocab covers vocabulary by attribute,
// including the precedence rule (item-exhausted over floor-hit).
func TestDwellCompletionNarrationVocab(t *testing.T) {
	cases := []struct {
		attr      sim.NeedKey
		source    sim.DwellCreditSource
		exhausted bool
		floor     bool
		want      string
	}{
		{"hunger", sim.DwellSourceItem, true, false, "You finish the last bite, satisfied."},
		{"thirst", sim.DwellSourceItem, true, false, "You drain the last drop."},
		{"tiredness", sim.DwellSourceItem, true, false, "You feel a little less tired than before."},
		{"hunger", sim.DwellSourceObject, false, true, "You feel full."},
		{"thirst", sim.DwellSourceObject, false, true, "Your thirst is quenched."},
		{"tiredness", sim.DwellSourceObject, false, true, "You feel rested."},
		{"hunger", sim.DwellSourceItem, true, true, "You finish the last bite, satisfied."}, // exhausted wins
		{"hunger", sim.DwellSourceObject, false, false, ""},                                 // no event
		{"mood", sim.DwellSourceItem, true, false, "You finish what you had."},              // unknown attr fallback
		{"mood", sim.DwellSourceObject, false, true, ""},                                    // unknown attr + floor → ""
	}
	for _, c := range cases {
		got := sim.DwellCompletionNarration(c.attr, c.source, c.exhausted, c.floor)
		if got != c.want {
			t.Errorf("Narration(%q, %q, exh=%t, floor=%t) = %q, want %q",
				c.attr, c.source, c.exhausted, c.floor, got, c.want)
		}
	}
}
