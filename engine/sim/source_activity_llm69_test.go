package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
)

// source_activity_llm69_test.go — LLM-69. The NPC side of the consume-at-source
// feature: the completion beat (handlers subscriber + enriched event), the
// standing busy-state projection, and the move-cancel belt. Reuses
// buildGatherTestWorld / placeAt / inventoryOf / forceComplete / liveActivity
// from gather_commands_test.go + source_activity_test.go (same package).

// setActorKindNPC stamps an NPC kind on the actor — buildGatherTestWorld seeds
// hannah with no kind, and tryStampWarrant only warrants agent-backed NPCs, so
// the completion warrant won't land until she's NPCStateful.
func setActorKindNPC(t *testing.T, w *sim.World, id sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].Kind = sim.KindNPCStateful
		return nil, nil
	}}); err != nil {
		t.Fatalf("setActorKindNPC: %v", err)
	}
}

// registerSourceActivityHandlers wires the LLM-69 completion subscriber on the
// world goroutine (safe post-Run, per the handler's doc contract).
func registerSourceActivityHandlers(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handlers.RegisterSourceActivityHandlers(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("registerSourceActivityHandlers: %v", err)
	}
}

// completionWarrant returns the actor's SourceActivityCompletedWarrantReason (and
// whether one is present), read off the world goroutine.
func completionWarrant(t *testing.T, w *sim.World, id sim.ActorID) (sim.SourceActivityCompletedWarrantReason, bool) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, m := range world.Actors[id].Warrants {
			if r, ok := m.Reason.(sim.SourceActivityCompletedWarrantReason); ok {
				return r, nil
			}
		}
		return sim.SourceActivityCompletedWarrantReason{}, nil
	}})
	if err != nil {
		t.Fatalf("completionWarrant: %v", err)
	}
	r := res.(sim.SourceActivityCompletedWarrantReason)
	return r, r.NarrationText != ""
}

func TestSourceActivityCompletionNarration(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"harvest with yield", sim.SourceActivityCompletionNarration(sim.SourceActivityHarvest, "berries", 3, "", "Berry Bush"),
			"You finish gathering at Berry Bush; you now have 3 berries in your pack."},
		{"harvest no source name", sim.SourceActivityCompletionNarration(sim.SourceActivityHarvest, "water", 1, "", ""),
			"You finish gathering; you now have 1 water in your pack."},
		{"harvest zero qty is silent", sim.SourceActivityCompletionNarration(sim.SourceActivityHarvest, "berries", 0, "", "Berry Bush"), ""},
		{"refresh hunger", sim.SourceActivityCompletionNarration(sim.SourceActivityRefresh, "", 0, "hunger", "Berry Bush"),
			"You finish eating at Berry Bush; the gnawing eases."},
		{"refresh thirst", sim.SourceActivityCompletionNarration(sim.SourceActivityRefresh, "", 0, "thirst", "Old Well"),
			"You finish drinking at Old Well; the dryness fades."},
		{"refresh unknown attr is silent", sim.SourceActivityCompletionNarration(sim.SourceActivityRefresh, "", 0, "mana", "X"), ""},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s:\n got  %q\n want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestHarvestCompletion_StampsCompletionWarrant — a full harvest (StartHarvest →
// completion sweep) emits the enriched SourceActivityCompleted, and the subscriber
// stamps a completion warrant carrying the yield + a non-empty narration. This is
// the forage-to-sell closing signal LLM-53/56 wrongly assumed NPCs didn't need.
func TestHarvestCompletion_StampsCompletionWarrant(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKindNPC(t, w, "hannah")
	registerSourceActivityHandlers(t, w)
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartHarvest("hannah", 2)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	forceComplete(t, w)

	r, ok := completionWarrant(t, w, "hannah")
	if !ok {
		t.Fatal("no SourceActivityCompletedWarrantReason stamped after harvest completion")
	}
	if r.ActivityKind != sim.SourceActivityHarvest {
		t.Errorf("ActivityKind = %q, want harvest", r.ActivityKind)
	}
	if r.Item != "berries" || r.Qty < 1 {
		t.Errorf("Item/Qty = %q/%d, want berries / >=1", r.Item, r.Qty)
	}
	if !strings.Contains(r.NarrationText, "in your pack") {
		t.Errorf("NarrationText = %q, want the yield beat", r.NarrationText)
	}
}

// TestSourceActivityCompletion_ContinuesSkipsWarrant — a non-terminal auto-repeat
// bite (Continues=true) stamps nothing; only the terminal completion does. Drives
// the subscriber directly via EmitForTest so the Continues gate is isolated from
// the refresh auto-repeat machinery.
func TestSourceActivityCompletion_ContinuesSkipsWarrant(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKindNPC(t, w, "hannah")
	registerSourceActivityHandlers(t, w)

	emit := func(continues bool) {
		t.Helper()
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			sim.EmitForTest(world, &sim.SourceActivityCompleted{
				ActorID:    "hannah",
				ObjectID:   "bush",
				Kind:       sim.SourceActivityRefresh,
				Attribute:  "hunger",
				SourceName: "Berry Bush",
				Continues:  continues,
				At:         time.Now().UTC(),
			})
			return nil, nil
		}}); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}

	emit(true)
	if _, ok := completionWarrant(t, w, "hannah"); ok {
		t.Error("a continuing (auto-repeat) bite must not stamp a completion warrant")
	}
	emit(false)
	if _, ok := completionWarrant(t, w, "hannah"); !ok {
		t.Error("the terminal completion must stamp a completion warrant")
	}
}

func TestRepublish_ProjectsHarvestSourceActivity(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush")
	if _, err := w.Send(sim.StartHarvest("hannah", 2)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	sa := w.Published().Actors["hannah"]
	if sa.SourceActivityKind != sim.SourceActivityHarvest || sa.SourceActivityObjectID != "bush" {
		t.Errorf("projection = %q @ %q, want harvest @ bush", sa.SourceActivityKind, sa.SourceActivityObjectID)
	}
}

func TestRepublish_ProjectsRefreshSourceActivityWithNeed(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "well")
	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	sa := w.Published().Actors["hannah"]
	if sa.SourceActivityKind != sim.SourceActivityRefresh || sa.SourceActivityObjectID != "well" {
		t.Fatalf("projection = %q @ %q, want refresh @ well", sa.SourceActivityKind, sa.SourceActivityObjectID)
	}
	if sa.SourceActivityAttribute != "thirst" {
		t.Errorf("SourceActivityAttribute = %q, want thirst (the well's eased need)", sa.SourceActivityAttribute)
	}
}

// TestMove_LandsExpiredWindowInsteadOfCancelling — the move-cancel belt: a move
// that arrives in the gap between a window expiring and the completion sweep must
// COMPLETE the finished pick (mint + completion beat), not discard it as an
// abandon. Without the belt the harvested berries would be lost on the move.
func TestMove_LandsExpiredWindowInsteadOfCancelling(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()

	rec := &eventRec{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(rec.handle))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	placeAt(t, w, "hannah", "bush")
	if _, err := w.Send(sim.StartHarvest("hannah", 2)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	// Expire the window WITHOUT sweeping, simulating the sub-second gap before the
	// completion ticker runs.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		past := time.Now().UTC().Add(-time.Hour)
		sa := world.Actors["hannah"].SourceActivity
		sa.StartedAt, sa.Until = past, past
		return nil, nil
	}}); err != nil {
		t.Fatalf("expire: %v", err)
	}

	// Move one tile — the belt should complete the expired window first.
	var pos sim.TilePos
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].Pos, nil
	}})
	if err != nil {
		t.Fatalf("read pos: %v", err)
	}
	pos = res.(sim.TilePos)
	dest := sim.NewPositionDestination(sim.Position{X: pos.X + 1, Y: pos.Y})
	if _, err := w.Send(sim.MoveActor("hannah", dest, false, time.Now().UTC())); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}

	if got := inventoryOf(t, w, "hannah", "berries"); got < 1 {
		t.Errorf("berries = %d after move on an expired harvest, want the pick to have landed (>=1)", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("SourceActivity = %+v after move, want cleared", sa)
	}
	cancelled := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.SourceActivityCancelled)
		return ok
	})
	if cancelled != 0 {
		t.Errorf("got %d SourceActivityCancelled events, want 0 (the window had finished — it should complete, not cancel)", cancelled)
	}
	completed := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.SourceActivityCompleted)
		return ok
	})
	if completed < 1 {
		t.Errorf("got %d SourceActivityCompleted events, want >=1", completed)
	}
}
