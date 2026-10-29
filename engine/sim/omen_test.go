package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// omen_test.go — LLM-843: each omen happens at its subject's natural moment
// through the real completion path, takes effect, tells the witness, and leaves
// the witness holding it first-hand.

// armOmen sets an omen of kind against subject, live for a day (forceComplete
// lands its sweep an hour ahead).
func armOmen(t *testing.T, w *sim.World, kind sim.OmenKind, subject sim.ActorID) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Settings.OmenChancePermille = 1000 // omens on: chance 0 also stops an armed omen happening
		now := time.Now().UTC()
		world.Environment.Omens.Armed = append(world.Environment.Omens.Armed, sim.ArmedOmen{
			Kind: kind, SubjectID: subject, ArmedAt: now.Add(-time.Minute), LapsesAt: now.Add(24 * time.Hour),
		})
	})
}

// omenEvents captures completions and OmenNoticed from here on.
type omenEvents struct {
	done   []*sim.SourceActivityCompleted
	notice []*sim.OmenNoticed
}

func recordOmenEvents(t *testing.T, w *sim.World) *omenEvents {
	t.Helper()
	rec := &omenEvents{}
	mustSend(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
			switch ev := e.(type) {
			case *sim.SourceActivityCompleted:
				rec.done = append(rec.done, ev)
			case *sim.OmenNoticed:
				rec.notice = append(rec.notice, ev)
			}
		}))
	})
	return rec
}

// omenState reads the record and one actor's rumors on the world goroutine.
func omenState(t *testing.T, w *sim.World, id sim.ActorID) (sim.OmenRecord, []sim.KnownRumor) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var rumors []sim.KnownRumor
		if a := world.Actors[id]; a != nil {
			rumors = append(rumors, a.Rumors...)
		}
		return []any{world.Environment.Omens.Clone(), rumors}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	pair := res.([]any)
	return pair[0].(sim.OmenRecord), pair[1].([]sim.KnownRumor)
}

// assertOmenHappened checks the shared aftermath: one completion carrying the
// omen, one OmenNoticed, the armed omen gone, the subject's cooldown stamped,
// and the witness holding the omen first-hand.
func assertOmenHappened(t *testing.T, w *sim.World, rec *omenEvents, kind sim.OmenKind, witness, subject sim.ActorID) {
	t.Helper()
	if len(rec.done) != 1 || rec.done[0].Omen != kind {
		t.Fatalf("completions = %+v, want one carrying omen %q", rec.done, kind)
	}
	if len(rec.notice) != 1 || rec.notice[0].Kind != kind || rec.notice[0].WitnessID != witness || rec.notice[0].SubjectID != subject {
		t.Fatalf("OmenNoticed = %+v, want one %q seen by %s about %s", rec.notice, kind, witness, subject)
	}
	record, rumors := omenState(t, w, witness)
	if len(record.Armed) != 0 {
		t.Errorf("armed omens left = %+v, want none (claimed)", record.Armed)
	}
	if record.LastAt[subject].IsZero() {
		t.Errorf("cooldown for %s not stamped: %+v", subject, record.LastAt)
	}
	found := false
	for _, r := range rumors {
		if r.Topic == sim.RumorTopic("omen_"+string(kind)) && r.SubjectID == subject && r.FirstHand {
			found = true
		}
	}
	if !found {
		t.Errorf("witness %s rumors = %+v, want the omen first-hand", witness, rumors)
	}
}

func TestOmen_SourMilkSpoilsTheMilking(t *testing.T) {
	w := buildDairyWorld(t)
	rec := recordOmenEvents(t, w)
	armOmen(t, w, sim.OmenSourMilk, dairyID)

	if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	finishWindow(t, w, dairyID)

	if got := dairyActor(t, w, dairyID).Inventory["milk"]; got != 0 {
		t.Errorf("milk = %d, want 0 (the milk turned)", got)
	}
	if got := *dairyActor(t, w, milkCow).Yield.Row.AvailableQuantity; got != 0 {
		t.Errorf("cow milk left = %d, want 0 (drawn, then spoiled)", got)
	}
	assertOmenHappened(t, w, rec, sim.OmenSourMilk, dairyID, dairyID)
	if line := sim.OmenWitnessLine(rec.done[0].Omen, rec.done[0].SourceName); !strings.Contains(line, "Cow 2") || !strings.Contains(line, "turned") {
		t.Errorf("witness line = %q", line)
	}
}

func TestOmen_LapsedOmenDoesNotHappen(t *testing.T) {
	w := buildDairyWorld(t)
	rec := recordOmenEvents(t, w)
	mustSend(t, w, func(world *sim.World) {
		past := time.Now().UTC().Add(-time.Hour)
		world.Environment.Omens.Armed = []sim.ArmedOmen{{Kind: sim.OmenSourMilk, SubjectID: dairyID, ArmedAt: past.Add(-time.Hour), LapsesAt: past}}
	})
	if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	finishWindow(t, w, dairyID)
	if got := dairyActor(t, w, dairyID).Inventory["milk"]; got != 4 {
		t.Errorf("milk = %d, want 4 (a lapsed omen never happens)", got)
	}
	if len(rec.notice) != 0 {
		t.Errorf("OmenNoticed = %+v, want none", rec.notice)
	}
}

func TestOmen_ColdHearthGuttersTheStoke(t *testing.T) {
	w, cancel := buildHearthTestWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) { world.Actors["hannah"].Kind = sim.KindNPCShared })
	rec := recordOmenEvents(t, w)
	armOmen(t, w, sim.OmenColdHearth, "hannah")

	res, err := w.Send(sim.StartStoke("hannah"))
	if err != nil {
		t.Fatalf("StartStoke: %v", err)
	}
	after := res.(sim.SourceActivityStartResult).Until.Add(time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CompleteDueSourceActivities(world, after), nil
	}}); err != nil {
		t.Fatal(err)
	}
	if lit := hearthLitUntilOf(t, w, "tavern"); !lit.Equal(after.Add(15 * time.Minute)) {
		t.Errorf("HearthLitUntil = %v, want %v (the fire only catches briefly)", lit, after.Add(15*time.Minute))
	}
	if got := inventoryOf(t, w, "hannah", sim.FirewoodItemKind); got != 2 {
		t.Errorf("firewood = %d, want 2 (the wood is spent all the same)", got)
	}
	assertOmenHappened(t, w, rec, sim.OmenColdHearth, "hannah", "hannah")
}

func TestOmen_FlatBreadSpoilsTheBake(t *testing.T) {
	w, cancel, now := buildBakeTestWorld(t)
	defer cancel()
	rec := recordOmenEvents(t, w)
	armOmen(t, w, sim.OmenFlatBread, "alice")

	res, err := w.Send(sim.StartOrJoinBake("alice", "", false, now))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	after := res.(sim.SourceActivityStartResult).Until.Add(time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CompleteDueSourceActivities(world, after), nil
	}}); err != nil {
		t.Fatal(err)
	}
	if got := inventoryOf(t, w, "alice", "bread"); got != 0 {
		t.Errorf("bread = %d, want 0 (the bread would not rise)", got)
	}
	if got := inventoryOf(t, w, "alice", "flour"); got != 0 {
		t.Errorf("flour = %d, want 0 (spent all the same)", got)
	}
	assertOmenHappened(t, w, rec, sim.OmenFlatBread, "alice", "alice")
}

func TestOmen_WitheredBushYieldsNothing(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindNPCShared)
	mustSend(t, w, func(world *sim.World) { world.Actors["hannah"].DisplayName = "Hannah Boggs" })
	placeAt(t, w, "hannah", "bush")
	rec := recordOmenEvents(t, w)
	armOmen(t, w, sim.OmenWitheredBush, "hannah")

	if _, err := w.Send(sim.StartHarvest("hannah", 2)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	forceComplete(t, w)
	if got := inventoryOf(t, w, "hannah", "berries"); got != 0 {
		t.Errorf("berries = %d, want 0 (withered on the stem)", got)
	}
	if got := availOf(t, w, "bush"); got != 0 {
		t.Errorf("bush supply = %d, want 0 (stripped, to regrow)", got)
	}
	assertOmenHappened(t, w, rec, sim.OmenWitheredBush, "hannah", "hannah")
	if !rec.done[0].SourceDepleted {
		t.Errorf("completion %+v should mark the bush bare", rec.done[0])
	}
}

func TestOmen_FoulWellStillGivesWater(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindNPCShared)
	mustSend(t, w, func(world *sim.World) { world.Actors["hannah"].DisplayName = "Hannah Boggs" })
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["well"].Tags = append(world.VillageObjects["well"].Tags, sim.TagWell)
	})
	placeAt(t, w, "hannah", "well")
	rec := recordOmenEvents(t, w)
	armOmen(t, w, sim.OmenFoulWell, "hannah")

	if _, err := w.Send(sim.StartHarvest("hannah", 3)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	forceComplete(t, w)
	if got := inventoryOf(t, w, "hannah", "water"); got <= 0 {
		t.Errorf("water = %d, want some (the foul well takes nothing away)", got)
	}
	assertOmenHappened(t, w, rec, sim.OmenFoulWell, "hannah", "hannah")
}

// TestOmen_WellOmenNeverTakenAtABush — an armed well omen is not spent by a
// berry harvest (the hook is the well's alone), and a bush omen is not spent at
// the well.
func TestOmen_WellOmenNeverTakenAtABush(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindNPCShared)
	mustSend(t, w, func(world *sim.World) { world.Actors["hannah"].DisplayName = "Hannah Boggs" })
	placeAt(t, w, "hannah", "bush")
	armOmen(t, w, sim.OmenFoulWell, "hannah")
	if _, err := w.Send(sim.StartHarvest("hannah", 2)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	forceComplete(t, w)
	if got := inventoryOf(t, w, "hannah", "berries"); got != 2 {
		t.Errorf("berries = %d, want 2 (a well omen does nothing at a bush)", got)
	}
	if record, _ := omenState(t, w, "hannah"); len(record.Armed) != 1 {
		t.Errorf("armed = %+v, want the well omen still waiting", record.Armed)
	}
}

// TestOmen_WitnessLineInTheCompletionWarrant — the witness reads the omen in
// their completion beat: it replaces the usual harvest line, and the well's is
// added to a drink that still did its work.
func TestOmen_WitnessLineInTheCompletionWarrant(t *testing.T) {
	t.Run("withered bush replaces the yield line", func(t *testing.T) {
		w, cancel := buildGatherTestWorld(t)
		defer cancel()
		setActorKindNPC(t, w, "hannah")
		registerSourceActivityHandlers(t, w)
		placeAt(t, w, "hannah", "bush")
		armOmen(t, w, sim.OmenWitheredBush, "hannah")
		if _, err := w.Send(sim.StartHarvest("hannah", 2)); err != nil {
			t.Fatalf("StartHarvest: %v", err)
		}
		forceComplete(t, w)
		r, ok := completionWarrant(t, w, "hannah")
		if !ok {
			t.Fatal("no completion warrant")
		}
		if want := sim.OmenWitnessLine(sim.OmenWitheredBush, "Berry Bush"); r.NarrationText != want {
			t.Errorf("NarrationText = %q, want %q", r.NarrationText, want)
		}
	})
	t.Run("foul well adds to the drink", func(t *testing.T) {
		w, cancel := buildGatherTestWorld(t)
		defer cancel()
		setActorKindNPC(t, w, "hannah")
		registerSourceActivityHandlers(t, w)
		mustSend(t, w, func(world *sim.World) {
			world.VillageObjects["well"].Tags = append(world.VillageObjects["well"].Tags, sim.TagWell)
		})
		placeAt(t, w, "hannah", "well")
		armOmen(t, w, sim.OmenFoulWell, "hannah")
		if _, err := w.Send(sim.StartHarvest("hannah", 1)); err != nil {
			t.Fatalf("StartHarvest: %v", err)
		}
		forceComplete(t, w)
		r, ok := completionWarrant(t, w, "hannah")
		if !ok {
			t.Fatal("no completion warrant")
		}
		if !strings.Contains(r.NarrationText, "in your pack") || !strings.HasSuffix(r.NarrationText, sim.OmenWitnessLine(sim.OmenFoulWell, "")) {
			t.Errorf("NarrationText = %q, want the water line followed by the taste of iron", r.NarrationText)
		}
	})
}

// TestOmen_SwitchedOffAfterArmingDoesNotHappen — omen_chance_permille 0 is the
// off switch: an omen armed before the switch does not happen, and stays armed
// to lapse on its own.
func TestOmen_SwitchedOffAfterArmingDoesNotHappen(t *testing.T) {
	w := buildDairyWorld(t)
	rec := recordOmenEvents(t, w)
	armOmen(t, w, sim.OmenSourMilk, dairyID)
	mustSend(t, w, func(world *sim.World) { world.Settings.OmenChancePermille = 0 })

	if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	finishWindow(t, w, dairyID)
	if got := dairyActor(t, w, dairyID).Inventory["milk"]; got != 4 {
		t.Errorf("milk = %d, want 4 (omens are off)", got)
	}
	if len(rec.notice) != 0 {
		t.Errorf("OmenNoticed = %+v, want none", rec.notice)
	}
}
