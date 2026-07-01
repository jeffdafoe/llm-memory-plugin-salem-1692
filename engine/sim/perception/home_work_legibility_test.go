package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// home_work_legibility_test.go — LLM-212: the "## Around you" location line names
// when the actor is inside its OWN home / workplace ("inside the James Residence,
// your home"), so a weak model can tell at a glance it is already at its anchor —
// the legibility half of the move_to(home) confusion LLM-209 hardened. The
// Build+render integration is proved by the perception goldens; these cover the
// relation logic (incl. the home==workplace case the goldens don't have) and the
// render append in isolation.

func TestInsideRelationLabel(t *testing.T) {
	cases := []struct {
		name           string
		inside, hm, wk sim.StructureID
		want           string
	}{
		{"inside own home", "h", "h", "w", "your home"},
		{"inside own workplace", "w", "h", "w", "your workplace"},
		{"home is also workplace", "h", "h", "h", "your home and workplace"},
		{"inside neither", "o", "h", "w", ""},
		{"outdoors (no inside structure)", "", "h", "w", ""},
		{"no anchors set", "o", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := insideRelationLabel(tc.inside, tc.hm, tc.wk); got != tc.want {
				t.Errorf("insideRelationLabel(%q,%q,%q) = %q, want %q", tc.inside, tc.hm, tc.wk, got, tc.want)
			}
		})
	}
}

func TestRenderSurroundings_InsideRelationAnnotation(t *testing.T) {
	t.Run("own home annotated", func(t *testing.T) {
		var b strings.Builder
		renderSurroundings(&b, SurroundingsView{
			InsideStructureID: "home", StructureName: "James Residence", InsideRelation: "your home",
		})
		want := "You are inside the James Residence, your home, with no one else here to hear you speak."
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing home annotation.\nwant substring: %q\ngot:\n%s", want, b.String())
		}
	})

	t.Run("home and workplace annotated", func(t *testing.T) {
		var b strings.Builder
		renderSurroundings(&b, SurroundingsView{
			InsideStructureID: "tavern", StructureName: "Tavern", InsideRelation: "your home and workplace",
		})
		if !strings.Contains(b.String(), "inside the Tavern, your home and workplace,") {
			t.Errorf("missing home-and-workplace annotation:\n%s", b.String())
		}
	})

	t.Run("no annotation when relation is empty", func(t *testing.T) {
		var b strings.Builder
		renderSurroundings(&b, SurroundingsView{
			InsideStructureID: "shop", StructureName: "General Store", InsideRelation: "",
		})
		out := b.String()
		if !strings.Contains(out, "You are inside the General Store, with no one else here to hear you speak.") {
			t.Errorf("unexpected location line:\n%s", out)
		}
		if strings.Contains(out, ", your ") {
			t.Errorf("annotation leaked when InsideRelation was empty:\n%s", out)
		}
	})
}
