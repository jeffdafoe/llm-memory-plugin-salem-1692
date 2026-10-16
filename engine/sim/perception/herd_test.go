package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// herd_test.go — LLM-797. The at-animal cue (which is what offers the gather
// tool: gateTools reads Surroundings.GatherableItem) and the herd cue.

func TestAtAnimalCue(t *testing.T) {
	t.Run("beside a full cow", func(t *testing.T) {
		snap, id, w := dairyScenario(true)
		p := Build(snap, id, w)
		if p.Surroundings.GatherableItem != "milk" || p.Surroundings.GatherableSource != "Cow 2" || !p.Surroundings.GatherableFromAnimal {
			t.Fatalf("surroundings = %+v, want milk from Cow 2", p.Surroundings)
		}
	})
	t.Run("far from the pen", func(t *testing.T) {
		snap, id, w := dairyScenario(false)
		if p := Build(snap, id, w); p.Surroundings.GatherableItem != "" {
			t.Fatalf("gather cue %q at the farm, want none", p.Surroundings.GatherableItem)
		}
	})
	t.Run("no milk trade", func(t *testing.T) {
		snap, id, w := dairyScenario(true)
		snap.Actors[id].RestockPolicy = nil
		p := Build(snap, id, w)
		if p.Surroundings.GatherableItem != "" {
			t.Fatalf("gather cue %q for an actor with no milk entry", p.Surroundings.GatherableItem)
		}
		if p.Forage != nil {
			t.Fatalf("forage view %+v for an actor with no milk entry", p.Forage)
		}
	})
	t.Run("beside only a milked-dry cow", func(t *testing.T) {
		snap, id, w := dairyScenario(true)
		zero := 0
		snap.Actors["cow2"].Yield.Row.AvailableQuantity = &zero
		if p := Build(snap, id, w); p.Surroundings.GatherableItem != "" {
			t.Fatalf("gather cue %q beside an empty cow", p.Surroundings.GatherableItem)
		}
	})
}

func TestHerdCue(t *testing.T) {
	t.Run("every cow full is the pressing tier", func(t *testing.T) {
		snap, id, w := dairyScenario(false)
		four := 4
		snap.Actors["cow3"].Yield.Row.AvailableQuantity = &four
		out := combinedPrompt(Render(Build(snap, id, w), DefaultRenderConfig()))
		if !strings.Contains(out, "the cows are lowing in the pen, heavy with it: Cow, Cow 2, Cow 3, and Cow 4.") {
			t.Fatalf("pressing tier missing:\n%s", sectionOf(out, "## Your cows"))
		}
	})
	t.Run("plenty of milk on hand is silence", func(t *testing.T) {
		snap, id, w := dairyScenario(false)
		snap.Actors[id].Inventory["milk"] = 20
		out := combinedPrompt(Render(Build(snap, id, w), DefaultRenderConfig()))
		if strings.Contains(out, "## Your cows") {
			t.Fatal("herd cue rendered with milk well stocked")
		}
	})
	t.Run("no cow ready", func(t *testing.T) {
		snap, id, w := dairyScenario(false)
		for _, cid := range []sim.ActorID{"cow1", "cow2", "cow4"} {
			zero := 0
			snap.Actors[cid].Yield.Row.AvailableQuantity = &zero
		}
		p := Build(snap, id, w)
		if p.Forage.ActionableErrand() != ForageErrandNone {
			t.Errorf("errand = %v with no cow ready, want none", p.Forage.ActionableErrand())
		}
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		if !strings.Contains(out, "none of the cows has any to give just now") || strings.Contains(out, "move_to") {
			t.Fatalf("no-cow-ready line wrong:\n%s", sectionOf(out, "## Your cows"))
		}
	})
	t.Run("a ready cow is an errand", func(t *testing.T) {
		snap, id, w := dairyScenario(false)
		if got := Build(snap, id, w).Forage.ActionableErrand(); got != ForageErrandHerd {
			t.Errorf("errand = %v, want ForageErrandHerd", got)
		}
	})
}

// sectionOf returns the text from head to the next "## " heading (for messages).
func sectionOf(rendered, head string) string {
	i := strings.Index(rendered, head)
	if i < 0 {
		return "(no " + head + " section)"
	}
	rest := rendered[i:]
	if j := strings.Index(rest[len(head):], "\n## "); j >= 0 {
		return rest[:len(head)+j]
	}
	return rest
}
