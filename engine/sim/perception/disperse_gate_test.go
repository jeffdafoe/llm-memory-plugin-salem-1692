package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestOffersDisperse is the LLM-453 gate-predicate unit test: disperse is offered in
// a wound-down huddle (looping / run-long / lingering) with a peer and no live
// commerce or higher-priority directive, and gated off otherwise. The
// cross-scenario invariant TestGoldensDisperseCueOnlyWhenOffered checks the same
// predicate against the render over the whole matrix; this exercises each exclusion
// branch directly, including the ones no golden scenario happens to hit.
func TestOffersDisperse(t *testing.T) {
	member := []HuddleMember{{ID: "peer"}}
	base := func() Payload {
		return Payload{
			TurnState:    TurnStateView{ConversationRunLong: true},
			Surroundings: SurroundingsView{HuddleMembers: member},
		}
	}
	if !base().OffersDisperse() {
		t.Fatal("a wound-down huddle with a peer and nothing pending should offer disperse")
	}
	// Looping and lingering are wound-down signals too.
	for _, ts := range []TurnStateView{{ConversationLooping: true}, {ConversationLingering: true}} {
		p := base()
		p.TurnState = ts
		if !p.OffersDisperse() {
			t.Errorf("wind-down signal %+v should offer disperse", ts)
		}
	}
	// Each exclusion gates it off — mirrors renderTriage's switch precedence (a
	// higher-priority coda preempts the wind-down cue) plus the live-commerce guard.
	cases := []struct {
		name string
		mut  func(*Payload)
	}{
		{"no wind-down signal", func(p *Payload) { p.TurnState = TurnStateView{} }},
		{"no huddle peer", func(p *Payload) { p.Surroundings.HuddleMembers = nil }},
		{"already walking", func(p *Payload) { p.Actor.InFlightMove = &InFlightMoveView{} }},
		{"mid source activity", func(p *Payload) { p.Actor.InFlightSourceActivity = &InFlightSourceActivityView{} }},
		{"seek-work directive", func(p *Payload) { p.SeekWorkPlaces = []SeekWorkPlace{{}} }},
		{"need redirect", func(p *Payload) { p.NeedRedirect = &NeedRedirectView{} }},
		{"pinned mid-meal", func(p *Payload) { p.Actor.ActiveDwellCredits = []DwellCreditView{{Source: sim.DwellSourceItem}} }},
		{"own pending offer", func(p *Payload) { p.PendingOffersFromMe = []PendingOfferView{{}} }},
		{"pay offer to answer", func(p *Payload) { p.PayOffersForMe = []sim.PayOfferWarrantReason{{}} }},
		{"pending labor offer", func(p *Payload) { p.LaborOffersForMe = []LaborOfferView{{}} }},
		{"pending gift to answer", func(p *Payload) { p.GiftsForMe = []GiftOfferView{{}} }},
	}
	for _, c := range cases {
		p := base()
		c.mut(&p)
		if p.OffersDisperse() {
			t.Errorf("%s: OffersDisperse() = true, want false", c.name)
		}
	}
}
