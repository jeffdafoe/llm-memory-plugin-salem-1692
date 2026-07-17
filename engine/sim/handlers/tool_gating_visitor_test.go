package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// tool_gating_visitor_test.go — LLM-455. A merchant visitor's commerce tools (pay /
// pay_with_item / offer_trade / sell) are stripped on his rounds UNLESS he stands at a
// sanctioned place — his errand counterparty or a tavern/inn. The gate keys off
// perception.VisitorCommerceStripped (computed from the SAME co-presence the rounds cue frames
// as talk-only). speak stays, so he can still greet and pass the news.

// TestGateTools_VisitorTalkOnlyRounds_StripsCommerce is the tool half of the talk-only-rounds
// invariant: a rounds stop away from a sanctioned keeper advertises no pay_with_item, while
// speak survives; at a sanctioned place the commerce tool returns.
func TestGateTools_VisitorTalkOnlyRounds_StripsCommerce(t *testing.T) {
	r := gatingTestRegistry(t)

	// Sanctioned (at his counterparty or a tavern/inn) OR not a visitor: commerce stays.
	allowed := perception.Payload{ActorID: "vstr-abcd1234", Surroundings: speakAudience(), VisitorCommerceStripped: false}
	names := specNameSet(gateTools(r, allowed, nil))
	if names["pay_with_item"] != 1 {
		t.Errorf("pay_with_item stripped when commerce is allowed (at a sanctioned place): %v", names)
	}

	// Talk-only rounds stop: the commerce tools are stripped, speak survives.
	stripped := perception.Payload{ActorID: "vstr-abcd1234", Surroundings: speakAudience(), VisitorCommerceStripped: true}
	names = specNameSet(gateTools(r, stripped, nil))
	if names["pay_with_item"] != 0 {
		t.Errorf("pay_with_item advertised on a talk-only rounds stop — commerce must be confined to the counterparty: %v", names)
	}
	if names["speak"] != 1 {
		t.Errorf("speak stripped on a talk-only rounds stop (it must stay so he can greet + pass news): %v", names)
	}
}
