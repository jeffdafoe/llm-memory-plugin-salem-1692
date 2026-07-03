package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// gift_degenerate_buy_test.go — LLM-138 part 2. When the subject ALREADY
// carries the same item a co-present peer holds, the "buy it from them" peer
// line is suppressed: buying a copy of what you already hold is pointless. The
// live hud-6a887a… case — two NPCs at a free blueberry bush, each holding
// blueberries, each told only to BUY the other's — is what this gate removes.

func TestBuildSatiation_DegenerateBuy_SuppressedWhenSubjectHoldsItem(t *testing.T) {
	hid, h := huddleWith("ezekiel", "lewis")
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		CurrentHuddleID: hid,
		Acquaintances:   map[string]sim.Acquaintance{"Lewis Walker": {}},
		Inventory:       map[sim.ItemKind]int{"stew": 1}, // subject already holds stew
	}
	peer := &sim.ActorSnapshot{
		DisplayName: "Lewis Walker", Role: "farmer",
		Inventory: map[sim.ItemKind]int{"stew": 2},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "lewis": peer},
		Huddles:   map[sim.HuddleID]*sim.Huddle{hid: h},
		ItemKinds: foodDrinkCatalog(),
	}

	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need, got %+v", v)
	}
	if got := len(v.Needs[0].CoPresentPeers); got != 0 {
		t.Errorf("subject already holds stew → peer buy line must be suppressed, got %+v", v.Needs[0].CoPresentPeers)
	}
	var b strings.Builder
	renderSatiation(&b, v)
	if strings.Contains(b.String(), "offer to buy it from them") {
		t.Errorf("degenerate buy line rendered though subject holds the item:\n%s", b.String())
	}

	// Control: a subject NOT holding the item still sees the peer offer (the gate
	// is item-specific, not a blanket peer suppression). Give it coins so the
	// LLM-242 means-to-pay gate passes — a 0-coin, goods-less buyer is correctly
	// suppressed for lack of any way to pay, but that is a DIFFERENT gate than the
	// item-specific degenerate one under test here.
	subj.Inventory = nil
	subj.Coins = 5
	v = buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("subject without stew but with coins → want 1 pressing need, got %+v", v)
	}
	if got := len(v.Needs[0].CoPresentPeers); got != 1 {
		t.Errorf("subject without stew → peer offer should appear, got %d", got)
	}
}
