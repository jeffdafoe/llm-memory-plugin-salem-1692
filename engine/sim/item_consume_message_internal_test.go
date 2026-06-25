package sim

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// LLM-113: the Consume failure copy is plain, groundable NPC English built from
// the catalog's singular phrase — "you cannot eat a skillet" when the actor
// holds an inedible item (the live Ezekiel-tries-to-eat-a-skillet case), and
// "you don't have any raspberry to consume" when it doesn't hold the food. The
// raspberry case is named in the SINGULAR to prove resolution + the groundable
// inventory message land together (instead of the old "has no satisfactions").
func TestConsume_FailureMessages(t *testing.T) {
	w := &World{
		ItemKinds: map[ItemKind]*ItemKindDef{
			"skillet": {
				Name: "skillet", DisplayLabel: "Skillet",
				DisplayLabelSingular: "skillet", DisplayLabelPlural: "skillets",
				Category: ItemCategoryMaterial, // no Satisfies -> not consumable
			},
			"raspberries": {
				Name: "raspberries", DisplayLabel: "Raspberries",
				DisplayLabelSingular: "raspberry", DisplayLabelPlural: "raspberries",
				Category:  ItemCategoryFood,
				Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 2}},
			},
		},
		Actors: map[ActorID]*Actor{
			"ezekiel": {ID: "ezekiel", Inventory: map[ItemKind]int{"skillet": 1}},
		},
	}
	at := time.Now().UTC()

	// Held but inedible: article + singular phrase + the "eat" verb.
	_, err := Consume("ezekiel", "skillet", 1, at).Fn(w)
	if err == nil || !errors.Is(err, ErrNotConsumable) {
		t.Fatalf("skillet consume: want ErrNotConsumable, got %v", err)
	}
	if !strings.Contains(err.Error(), "you cannot eat a skillet") {
		t.Errorf("skillet message = %q, want it to contain %q", err.Error(), "you cannot eat a skillet")
	}

	// Not held, named in the singular: resolves to the food kind, then reports the
	// honest "you don't have any" rather than a catalog-shape rejection.
	_, err = Consume("ezekiel", "Raspberry", 1, at).Fn(w)
	if err == nil || !errors.Is(err, ErrInsufficientInventory) {
		t.Fatalf("raspberry consume: want ErrInsufficientInventory, got %v", err)
	}
	if !strings.Contains(err.Error(), "you don't have any raspberry to consume") {
		t.Errorf("raspberry message = %q, want it to contain %q", err.Error(), "you don't have any raspberry to consume")
	}
}
