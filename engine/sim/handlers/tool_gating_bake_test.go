package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// tool_gating_bake_test.go — LLM-454. A baker mid the evening bake is held speak-only
// the same way a laboring worker is (labor_gating_test.go): the commerce tools that
// would walk her off the bread are stripped, and move_to unless a red hunger/thirst
// need justifies breaking off to eat — in lockstep with the reactor carve-out
// (bakeReplyDue) that ticks her to answer a housemate with one word. Reuses the
// laborSpeakOnlyRegistry (it already advertises the commerce + consume set).

// bakingPayload builds a payload for a resident mid an evening bake with no pressing
// need (all needs green), for the base speak-only strip. The per-need move_to contract
// is exercised by TestGateTools_Baking_MoveToByNeed.
func bakingPayload() perception.Payload {
	return perception.Payload{
		ActorID:      "silence",
		Surroundings: speakAudience(),
		Actor: perception.ActorView{
			InFlightSourceActivity: &perception.InFlightSourceActivityView{Kind: sim.SourceActivityBake},
			Needs:                  map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
			NeedThresholds:         sim.NeedThresholds{"hunger": 20, "thirst": 20, "tiredness": 20},
		},
	}
}

func TestGateTools_Baking_SpeakOnlySurface(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	names := specNameSet(gateTools(r, bakingPayload(), nil))

	for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell", "move_to"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised to a baker; want it stripped (speak-only surface, LLM-454)", gated)
		}
	}
	for _, keep := range []string{"speak", "consume", "done"} {
		if names[keep] != 1 {
			t.Errorf("%q should stay advertised to a baker; count %d", keep, names[keep])
		}
	}
}

// TestGateTools_Baking_MoveToByNeed pins the move_to contract for a baker across need
// tiers, mirroring laboringMayBreakOffToEat: a red hunger OR thirst keeps move_to (break
// off to eat/drink), tiredness does NOT (bedtime ends the bake; a nap never justifies
// abandoning the bread), and no red need keeps her committed. Commerce stays stripped in
// every case. NeedRed is value >= threshold (needs.go NeedLabelTier), so 22 with a 20
// threshold is red and 5 is not.
func TestGateTools_Baking_MoveToByNeed(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	bakePayloadWithNeeds := func(needs map[sim.NeedKey]int) perception.Payload {
		return perception.Payload{
			ActorID:      "silence",
			Surroundings: speakAudience(),
			Actor: perception.ActorView{
				InFlightSourceActivity: &perception.InFlightSourceActivityView{Kind: sim.SourceActivityBake},
				Needs:                  needs,
				NeedThresholds:         sim.NeedThresholds{"hunger": 20, "thirst": 20, "tiredness": 20},
			},
		}
	}
	for _, tc := range []struct {
		name     string
		needs    map[sim.NeedKey]int
		wantMove bool
	}{
		{"committed (no red need)", map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5}, false},
		{"red hunger — walk to eat", map[sim.NeedKey]int{"hunger": 22, "thirst": 5, "tiredness": 5}, true},
		{"red thirst — walk to drink", map[sim.NeedKey]int{"hunger": 5, "thirst": 22, "tiredness": 5}, true},
		{"red tiredness — stays put", map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 22}, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			names := specNameSet(gateTools(r, bakePayloadWithNeeds(tc.needs), nil))
			if got := names["move_to"] == 1; got != tc.wantMove {
				t.Errorf("move_to advertised=%v, want %v", got, tc.wantMove)
			}
			// Commerce stays stripped regardless of the need tier — a baker eats/drinks
			// if she must, she never trades mid-bake.
			for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell"} {
				if names[gated] != 0 {
					t.Errorf("%q advertised to a baker; commerce stays stripped in all need cases", gated)
				}
			}
		})
	}
}

func TestGateTools_NotBaking_KeepsCommerceTools(t *testing.T) {
	// Control: the strip is scoped to a bake source activity, not a blanket source-
	// activity strip. A plain actor and a non-bake source activity (harvest) both keep
	// their commerce tools.
	r := laborSpeakOnlyRegistry(t)

	idle := perception.Payload{ActorID: "silence", Surroundings: speakAudience()}
	names := specNameSet(gateTools(r, idle, nil))
	for _, keep := range []string{"pay", "pay_with_item", "offer_trade", "sell", "move_to", "speak"} {
		if names[keep] != 1 {
			t.Errorf("%q should be advertised to a non-baking actor; count %d", keep, names[keep])
		}
	}

	// A non-bake source activity (harvest) is NOT held speak-only by this gate — the
	// laboring/baking speak-only surface is the only source of the commerce strip.
	harvesting := perception.Payload{
		ActorID:      "silence",
		Surroundings: speakAudience(),
		Actor: perception.ActorView{
			InFlightSourceActivity: &perception.InFlightSourceActivityView{Kind: sim.SourceActivityHarvest},
		},
	}
	hnames := specNameSet(gateTools(r, harvesting, nil))
	for _, keep := range []string{"sell", "move_to"} {
		if hnames[keep] != 1 {
			t.Errorf("%q should stay for a harvesting actor; the speak-only strip is bake-scoped (count %d)", keep, hnames[keep])
		}
	}
}
