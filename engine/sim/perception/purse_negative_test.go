package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// purse_negative_test.go — LLM-995. A wallet never goes below zero, so the
// purse line has no debt to describe. A negative figure that slipped in by some
// other path reads as an empty purse, for a resident and for a visitor who
// still carries takings: never a bare negative number, never "in debt".
func TestRenderActor_NegativePurseReadsAsEmpty(t *testing.T) {
	cases := []struct {
		name string
		av   ActorView
		want string
	}{
		{"resident, no goods", ActorView{State: sim.StateIdle, Coins: -12},
			"Coins in your purse: 0 — you have no coins to spend, so you cannot pay for anything until you earn some."},
		{"visitor with takings", ActorView{State: sim.StateIdle, Coins: -5, VisitorTakings: 30},
			"Coins in your purse: 30 — but all of it is the trip's takings, bound for home. Your buying purse for this journey is spent"},
	}
	for _, c := range cases {
		var b strings.Builder
		renderActor(&b, c.av)
		out := b.String()
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: purse line missing or wrong:\n%s\nwant it to contain %q", c.name, out, c.want)
		}
		if strings.Contains(out, "Coins in your purse: -") || strings.Contains(out, "of which -") {
			t.Errorf("%s: renders a bare negative purse:\n%s", c.name, out)
		}
		if strings.Contains(out, "debt") {
			t.Errorf("%s: says debt, but a wallet never goes below zero:\n%s", c.name, out)
		}
	}
}
