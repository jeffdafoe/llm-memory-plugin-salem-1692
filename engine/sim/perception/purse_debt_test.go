package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// purse_debt_test.go — LLM-993. A purse below zero (a lapsed order refunded out
// of coins already spent) renders as a debt in words, for a resident and for a
// visitor who still carries takings, and never as a bare negative number.
func TestRenderActor_PurseInDebt(t *testing.T) {
	cases := []struct {
		name string
		av   ActorView
		want string
	}{
		{"resident, no goods", ActorView{State: sim.StateIdle, Coins: -12},
			"Coins in your purse: none — you are 12 coins in debt, so you cannot pay for anything until you earn some back."},
		{"visitor with takings, debt smaller", ActorView{State: sim.StateIdle, Coins: -5, VisitorTakings: 30},
			"Coins in your purse: 30 — but all of it is the trip's takings, bound for home, and you are 5 coins in debt besides."},
		{"visitor with takings, debt equal", ActorView{State: sim.StateIdle, Coins: -30, VisitorTakings: 30},
			"Coins in your purse: 30 — but all of it is the trip's takings, bound for home, and you are 30 coins in debt besides."},
		{"visitor with takings, debt larger", ActorView{State: sim.StateIdle, Coins: -45, VisitorTakings: 30},
			"Coins in your purse: 30 — but all of it is the trip's takings, bound for home, and you are 45 coins in debt besides."},
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
	}
}
