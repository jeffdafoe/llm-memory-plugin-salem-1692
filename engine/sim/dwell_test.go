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
	// The dwell "still here?" check resolves via resolveLoiteringObject, which
	// only considers NAMED objects with a resolvable asset and measures
	// Chebyshev tiles to the loiter pin. Name the tree, seed its asset, and
	// give it a zero loiter offset anchored at tile (200,200) so an actor on
	// tile (200,200) is exactly at the pin.
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"tree-oak": {ID: "tree-oak"}})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"shade_tree": {
			ID: "shade_tree", DisplayName: "Shade Tree", AssetID: "tree-oak", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.TileToWorld(sim.GridPoint{X: 200, Y: 200}),
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			Pos:      sim.TilePos{X: actorX, Y: actorY},
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
	anchor := now.Add(-11 * time.Minute)                  // ripe (period 10 min)
	w, cancel := buildDwellTestWorld(t, anchor, 200, 200) // on the tree's pin
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
	w, cancel := buildDwellTestWorld(t, anchor, 200, 200)
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
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"inn-thatched": {ID: "inn-thatched"}})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {
			ID: "inn", DisplayName: "Inn", AssetID: "inn-thatched",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.TileToWorld(sim.GridPoint{X: 110, Y: 110}), // pin on the actor's tile
		},
	})
	remaining := 3
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			Pos:      sim.TilePos{X: 110, Y: 110},
			Needs:    map[sim.NeedKey]int{"hunger": 15, "thirst": 5, "tiredness": 5},
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
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"inn-thatched": {ID: "inn-thatched"}})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {
			ID: "inn", DisplayName: "Inn", AssetID: "inn-thatched",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.TileToWorld(sim.GridPoint{X: 105, Y: 100}), // pin on the player's tile
		},
	})
	remaining := 1
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"player": {
			ID:            "player",
			LoginUsername: "alice",
			Pos:           sim.TilePos{X: 105, Y: 100},
			Needs:         map[sim.NeedKey]int{"hunger": 8, "thirst": 5, "tiredness": 5},
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

// TestApplyDwellTickFloorHitTerminatesCredit covers source=object
// reaching hunger=0 → "You feel full." narration AND credit deletion
// (floor-hit now terminates the credit, parity with v1's "you feel
// full → meal done" intent — design call 4 of the dwell-perception PR).
func TestApplyDwellTickFloorHitTerminatesCredit(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-11 * time.Minute)
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"bush-berries": {ID: "bush-berries"}})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {
			ID: "bush", DisplayName: "Berry Bush", AssetID: "bush-berries",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.TileToWorld(sim.GridPoint{X: 0, Y: 0}), // pin on the player's tile
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"player": {
			ID:            "player",
			LoginUsername: "alice",
			Pos:           sim.TilePos{X: 0, Y: 0},
			Needs:         map[sim.NeedKey]int{"hunger": 1},
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
	// Floor-hit now terminates the credit (call 4 design change). Credit
	// map should be empty post-tick.
	count, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return len(world.Actors["player"].DwellCredits), nil
		},
	})
	if count.(int) != 0 {
		t.Errorf("floor-hit did not terminate credit: %d remaining", count.(int))
	}
}

// TestApplyDwellTickNPCAlsoNarrates covers the v2 behavior change
// (call 4: PC-only gating dropped): NPCs produce Completions on the
// same terminal conditions as PCs, so subscribers and the Hub-broadcast
// layer (when ported) can pick who gets render-time treatment instead
// of emit-time hiding the signal entirely. This is load-bearing for
// the LLM perception cue — without it, the NPC would never see a
// "you feel rested" terminal beat in their warrant batch.
func TestApplyDwellTickNPCAlsoNarrates(t *testing.T) {
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
	if len(r.Completions) != 1 {
		t.Fatalf("NPC Completions = %d, want 1 (PC-only gating dropped)", len(r.Completions))
	}
	if !r.Completions[0].FloorHit {
		t.Errorf("Completion FloorHit = false, want true")
	}
	if r.Completions[0].Text != "You feel rested." {
		t.Errorf("Completion Text = %q, want 'You feel rested.'", r.Completions[0].Text)
	}
}

// TestUpsertItemDwellCreditsHappy covers the basic upsert. The returned
// stamped snapshots should reflect every credit that landed.
func TestUpsertItemDwellCreditsHappy(t *testing.T) {
	actor := &sim.Actor{ID: "p", DwellCredits: nil}
	now := time.Now().UTC()
	stamped := sim.UpsertItemDwellCredits(actor, "stew", []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 3, DwellPeriodMinutes: 15, DwellTotalTicks: 4},
		{Attribute: "thirst", DwellAmount: 2, DwellPeriodMinutes: 10, DwellTotalTicks: 3},
	}, "inn", now)

	if len(actor.DwellCredits) != 2 {
		t.Fatalf("DwellCredits count = %d, want 2", len(actor.DwellCredits))
	}
	if len(stamped) != 2 {
		t.Fatalf("stamped count = %d, want 2", len(stamped))
	}
	hunger := actor.DwellCredits[sim.DwellCreditKey{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceItem}]
	if hunger == nil || hunger.DwellDelta != -3 || hunger.DwellPeriodMinutes != 15 {
		t.Errorf("hunger credit = %+v", hunger)
	}
	if hunger.RemainingTicks == nil || *hunger.RemainingTicks != 4 {
		t.Errorf("hunger RemainingTicks = %v, want 4", hunger.RemainingTicks)
	}
	if hunger.Kind != "stew" {
		t.Errorf("hunger Kind = %q, want %q", hunger.Kind, "stew")
	}
}

// TestUpsertItemDwellCreditsSkipsIncomplete covers missing dwell triple.
func TestUpsertItemDwellCreditsSkipsIncomplete(t *testing.T) {
	actor := &sim.Actor{ID: "p"}
	now := time.Now().UTC()
	stamped := sim.UpsertItemDwellCredits(actor, "soup", []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 0, DwellPeriodMinutes: 15, DwellTotalTicks: 4},   // amount=0 → skip
		{Attribute: "thirst", DwellAmount: 2, DwellPeriodMinutes: 0, DwellTotalTicks: 3},    // period=0 → skip
		{Attribute: "tiredness", DwellAmount: 1, DwellPeriodMinutes: 5, DwellTotalTicks: 0}, // ticks=0 → skip
		{Attribute: "tiredness", DwellAmount: 1, DwellPeriodMinutes: 5, DwellTotalTicks: 2}, // valid
	}, "inn", now)

	if len(actor.DwellCredits) != 1 {
		t.Errorf("DwellCredits count = %d, want 1 (only the complete triple)", len(actor.DwellCredits))
	}
	if len(stamped) != 1 {
		t.Errorf("stamped count = %d, want 1", len(stamped))
	}
}

// TestUpsertItemDwellCreditsSkipsEmptyStructure covers the
// eating-while-walking case (structure unknown). Returns nil stamped
// since no credits landed.
func TestUpsertItemDwellCreditsSkipsEmptyStructure(t *testing.T) {
	actor := &sim.Actor{ID: "p"}
	stamped := sim.UpsertItemDwellCredits(actor, "stew", []sim.ItemSatisfaction{
		{Attribute: "hunger", DwellAmount: 3, DwellPeriodMinutes: 10, DwellTotalTicks: 2},
	}, "", time.Now().UTC())
	if len(actor.DwellCredits) != 0 {
		t.Errorf("empty structureID produced credits: %d", len(actor.DwellCredits))
	}
	if stamped != nil {
		t.Errorf("empty structureID returned stamped = %v, want nil", stamped)
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
				Kind:               "stew",
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
	sim.UpsertItemDwellCredits(actor, "stew", []sim.ItemSatisfaction{
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

// TestDwellStayClause pins the ZBBS-WORK-409 shared stay message: prose that
// names how long the dwell takes to FINISH and the cost of leaving early. The
// food/drink wording and the "remain ___" tail are driven by the need; the
// wasteExtra arg carries " and the coins you paid" on the settle path only (the
// generic dwell line omits it, since an item dwell can be self-consumed food).
func TestDwellStayClause(t *testing.T) {
	cases := []struct {
		name       string
		minutes    int
		attribute  sim.NeedKey
		wasteExtra string
		want       string
	}{
		{
			name:      "hunger without coins (dwell line / self-consumed food)",
			minutes:   12,
			attribute: "hunger",
			want:      "it will take you 12 more minute(s) to finish eating it all. If you leave now you will waste the rest, and you will remain hungry",
		},
		{
			name:       "hunger with coins (settle path)",
			minutes:    12,
			attribute:  "hunger",
			wasteExtra: " and the coins you paid",
			want:       "it will take you 12 more minute(s) to finish eating it all. If you leave now you will waste the rest and the coins you paid, and you will remain hungry",
		},
		{
			name:      "thirst uses drink wording",
			minutes:   8,
			attribute: "thirst",
			want:      "it will take you 8 more minute(s) to finish drinking it all. If you leave now you will waste the rest, and you will remain thirsty",
		},
		{
			name:      "tiredness: generic finish phrase, tired adjective",
			minutes:   5,
			attribute: "tiredness",
			want:      "it will take you 5 more minute(s) to finish it. If you leave now you will waste the rest, and you will remain tired",
		},
		{
			name:      "unknown need: generic finish, no remain clause",
			minutes:   3,
			attribute: "mana",
			want:      "it will take you 3 more minute(s) to finish it. If you leave now you will waste the rest",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sim.DwellStayClause(tc.minutes, tc.attribute, tc.wasteExtra); got != tc.want {
				t.Errorf("DwellStayClause\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}

// TestMaxDwellMinutes covers the settle-feedback helper that picks the longest
// remaining dwell (minutes) across the stamped snapshots, skipping object-style
// credits with no countdown (RemainingTicks == nil). ZBBS-WORK-409.
func TestMaxDwellMinutes(t *testing.T) {
	mk := func(remaining, period int) sim.DwellCreditSnapshot {
		r := remaining
		return sim.DwellCreditSnapshot{RemainingTicks: &r, PeriodMinutes: period}
	}
	noCount := sim.DwellCreditSnapshot{PeriodMinutes: 10} // RemainingTicks nil
	cases := []struct {
		name    string
		stamped []sim.DwellCreditSnapshot
		want    int
	}{
		{"none", nil, 0},
		{"single", []sim.DwellCreditSnapshot{mk(6, 2)}, 12},
		{"longest wins", []sim.DwellCreditSnapshot{mk(3, 2), mk(6, 2), mk(1, 2)}, 12},
		{"nil countdown skipped", []sim.DwellCreditSnapshot{noCount, mk(4, 2)}, 8},
		{"all nil countdown", []sim.DwellCreditSnapshot{noCount}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sim.MaxDwellMinutes(tc.stamped); got != tc.want {
				t.Errorf("MaxDwellMinutes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDwellCompletionNarrationVocab covers vocabulary by attribute,
// including the precedence rule (item-exhausted over floor-hit) and
// the v2-new walked-away branch.
func TestDwellCompletionNarrationVocab(t *testing.T) {
	cases := []struct {
		attr      sim.NeedKey
		source    sim.DwellCreditSource
		exhausted bool
		floor     bool
		walked    bool
		want      string
	}{
		{"hunger", sim.DwellSourceItem, true, false, false, "You finish the last bite, satisfied."},
		{"thirst", sim.DwellSourceItem, true, false, false, "You drain the last drop."},
		{"tiredness", sim.DwellSourceItem, true, false, false, "You feel a little less tired than before."},
		{"hunger", sim.DwellSourceObject, false, true, false, "You feel full."},
		{"thirst", sim.DwellSourceObject, false, true, false, "Your thirst is quenched."},
		{"tiredness", sim.DwellSourceObject, false, true, false, "You feel rested."},
		{"hunger", sim.DwellSourceItem, true, true, false, "You finish the last bite, satisfied."}, // exhausted wins
		{"hunger", sim.DwellSourceItem, false, false, true, "You walk away from your meal, leaving it half-eaten."},
		{"thirst", sim.DwellSourceItem, false, false, true, "You walk away from your drink."},
		{"tiredness", sim.DwellSourceObject, false, false, true, "You stop resting and move on."},
		{"hunger", sim.DwellSourceObject, false, false, true, ""},                     // object+walked+hunger has no line
		{"hunger", sim.DwellSourceObject, false, false, false, ""},                    // no event
		{"mood", sim.DwellSourceItem, true, false, false, "You finish what you had."}, // unknown attr fallback
		{"mood", sim.DwellSourceObject, false, true, false, ""},                       // unknown attr + floor → ""
	}
	for _, c := range cases {
		got := sim.DwellCompletionNarration(c.attr, c.source, c.exhausted, c.floor, c.walked)
		if got != c.want {
			t.Errorf("Narration(%q, %q, exh=%t, floor=%t, walked=%t) = %q, want %q",
				c.attr, c.source, c.exhausted, c.floor, c.walked, got, c.want)
		}
	}
}

// TestDwellTickNarrationVocab covers the per-tick payoff vocab — one
// felt-language line per (attribute, source). Unknown combinations
// return "".
func TestDwellTickNarrationVocab(t *testing.T) {
	cases := []struct {
		attr   sim.NeedKey
		source sim.DwellCreditSource
		want   string
	}{
		{"hunger", sim.DwellSourceItem, "You take another bite, the gnawing ebbs."},
		{"thirst", sim.DwellSourceItem, "You drink; the dryness fades."},
		{"tiredness", sim.DwellSourceItem, "You rest a moment; the weariness eases."},
		{"hunger", sim.DwellSourceObject, "You pick at what's here; the gnawing eases."},
		{"thirst", sim.DwellSourceObject, "You sip from the source; the dryness fades."},
		{"tiredness", sim.DwellSourceObject, "You linger here; the weariness eases."},
		{"mood", sim.DwellSourceItem, ""},   // unknown attr → ""
		{"mood", sim.DwellSourceObject, ""}, // unknown attr → ""
	}
	for _, c := range cases {
		got := sim.DwellTickNarration(c.attr, c.source)
		if got != c.want {
			t.Errorf("DwellTickNarration(%q, %q) = %q, want %q",
				c.attr, c.source, got, c.want)
		}
	}
}
