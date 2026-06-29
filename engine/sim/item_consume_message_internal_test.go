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

// LLM-166: an inedible item that IS a recipe input names what it's for, so a
// hungry model redirects instead of re-trying to eat it (the Josiah raw-meat
// loop). An inedible item that is NOT a recipe input states the honest floor.
// Both still match ErrNotConsumable (Unwrap), and the prose REPLACES the bare
// sentinel text rather than appending a redundant "item is not consumable".
func TestConsume_InedibleNamesRecipeUse(t *testing.T) {
	w := &World{
		ItemKinds: map[ItemKind]*ItemKindDef{
			"meat": {
				Name: "meat", DisplayLabel: "Meat",
				DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat",
				Category: ItemCategoryFood, // food, no Satisfies -> inedible raw
			},
			"stew": {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "bowl of stew"},
			"horseshoe": {
				Name: "horseshoe", DisplayLabel: "Horseshoe",
				DisplayLabelSingular: "horseshoe", Category: ItemCategoryMaterial,
			},
		},
		Recipes: map[ItemKind]*ItemRecipe{
			"stew": {OutputItem: "stew", Inputs: []RecipeInput{{Item: "meat", Qty: 10}}},
		},
		Actors: map[ActorID]*Actor{
			"josiah": {ID: "josiah", Inventory: map[ItemKind]int{"meat": 7, "horseshoe": 2}},
		},
	}
	at := time.Now().UTC()

	// Recipe input -> names the output, no trailing sentinel text.
	_, err := Consume("josiah", "meat", 1, at).Fn(w)
	if err == nil || !errors.Is(err, ErrNotConsumable) {
		t.Fatalf("meat consume: want ErrNotConsumable, got %v", err)
	}
	if got := err.Error(); got != "you cannot eat a cut of meat — it's used to produce stew" {
		t.Errorf("meat message = %q, want the recipe-use steer", got)
	}

	// Inedible non-ingredient -> honest floor, no fabricated reason.
	_, err = Consume("josiah", "horseshoe", 1, at).Fn(w)
	if err == nil || !errors.Is(err, ErrNotConsumable) {
		t.Fatalf("horseshoe consume: want ErrNotConsumable, got %v", err)
	}
	if got := err.Error(); got != "you cannot eat a horseshoe — it isn't something you can eat as it is" {
		t.Errorf("horseshoe message = %q, want the reason-free floor", got)
	}
}
