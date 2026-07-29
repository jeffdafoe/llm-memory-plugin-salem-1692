package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_affordance_render_test.go — LLM-564. renderLaborAffordance's named
// branch must only ever carry REAL display names: solicit_work resolves its
// employer argument by exact display name, so a cue that names a resolver
// fallback ("someone") or an empty string hands the model a target the tool
// must refuse. The corpus invariant (TestGoldensSolicitCueNeverNamesSomeone)
// guards the scenarios the matrix happens to carry; this pins the renderer's
// own contract directly, including resolver shapes no scenario builds
// (code_review).
func TestRenderLaborAffordance_NameGuard(t *testing.T) {
	resolve := func(names map[sim.ActorID]string) func(sim.ActorID) string {
		return func(id sim.ActorID) string { return names[id] }
	}
	cases := []struct {
		name       string
		employers  []sim.ActorID
		nameOf     func(sim.ActorID) string
		wantNamed  string // "" = expect the unnamed fallback branch
		wantAbsent []string
	}{
		{
			name:      "real name renders the named branch",
			employers: []sim.ActorID{"hannah"},
			nameOf:    resolve(map[sim.ActorID]string{"hannah": "Hannah Boggs"}),
			wantNamed: "Hannah Boggs might have work that wants doing",
		},
		{
			name:       "empty resolution falls back to unnamed",
			employers:  []sim.ActorID{"hannah"},
			nameOf:     resolve(map[sim.ActorID]string{}),
			wantNamed:  "",
			wantAbsent: []string{"might have work that wants doing"},
		},
		{
			name:       "someone fallback falls back to unnamed",
			employers:  []sim.ActorID{"hannah"},
			nameOf:     resolve(map[sim.ActorID]string{"hannah": "someone"}),
			wantNamed:  "",
			wantAbsent: []string{"someone might have work"},
		},
		{
			name:       "mixed slice keeps only the real name",
			employers:  []sim.ActorID{"ghost", "hannah"},
			nameOf:     resolve(map[sim.ActorID]string{"hannah": "Hannah Boggs"}),
			wantNamed:  "Hannah Boggs might have work that wants doing",
			wantAbsent: []string{"someone", " and Hannah"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderLaborAffordance(&b, true, tc.employers, tc.nameOf)
			out := b.String()
			if out == "" {
				t.Fatal("cue absent under canSolicit=true — the affordance must render on one branch or the other")
			}
			if tc.wantNamed != "" && !strings.Contains(out, tc.wantNamed) {
				t.Errorf("named branch missing %q in %q", tc.wantNamed, out)
			}
			if tc.wantNamed == "" && !strings.Contains(out, "You take work for pay.") {
				t.Errorf("expected the unnamed fallback branch, got %q", out)
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(out, absent) {
					t.Errorf("cue must not contain %q, got %q", absent, out)
				}
			}
			// Both branches carry the say/speak-first warning — the fold is the
			// fix, whichever wording renders.
			if !strings.Contains(out, "do NOT ask with speak first") {
				t.Errorf("speak-first warning missing from %q", out)
			}
		})
	}

	// canSolicit=false renders nothing regardless of the slice.
	var b strings.Builder
	renderLaborAffordance(&b, false, []sim.ActorID{"hannah"}, resolve(map[sim.ActorID]string{"hannah": "Hannah Boggs"}))
	if b.String() != "" {
		t.Errorf("canSolicit=false must render nothing, got %q", b.String())
	}
}
