package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestConsumeNarration covers the immediate consume self-line vocab (HOME-302
// §A): verb by primary need, shared ease-fragment phrasing, primary-need
// selection by magnitude with a stable tiebreak, and the empty cases.
func TestConsumeNarration(t *testing.T) {
	cases := []struct {
		name    string
		kind    sim.ItemKind
		applied map[sim.NeedKey]int
		want    string
	}{
		{"hunger eats", "bread", map[sim.NeedKey]int{"hunger": 5}, "You eat the bread; the gnawing ebbs."},
		{"thirst drinks", "ale", map[sim.NeedKey]int{"thirst": 4}, "You drink the ale; the dryness fades."},
		{"tiredness takes", "coca_tea", map[sim.NeedKey]int{"tiredness": 3}, "You take the coca_tea; the weariness eases."},
		{"multi need picks largest magnitude", "stew", map[sim.NeedKey]int{"hunger": 6, "thirst": 2}, "You eat the stew; the gnawing ebbs."},
		{"tie breaks by canonical need order (hunger first)", "morsel", map[sim.NeedKey]int{"thirst": 5, "hunger": 5}, "You eat the morsel; the gnawing ebbs."},
		{"no kind omits the item", "", map[sim.NeedKey]int{"hunger": 5}, "You eat; the gnawing ebbs."},
		{"empty applied yields nothing", "bread", map[sim.NeedKey]int{}, ""},
		{"unhandled need only yields nothing", "tonic", map[sim.NeedKey]int{"mood": 5}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sim.ConsumeNarration(c.kind, c.applied); got != c.want {
				t.Errorf("ConsumeNarration(%q, %v) = %q, want %q", c.kind, c.applied, got, c.want)
			}
		})
	}
}
