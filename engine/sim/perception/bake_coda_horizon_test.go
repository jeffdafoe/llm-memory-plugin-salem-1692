package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake_coda_horizon_test.go — LLM-464. The mid-activity triage coda (LLM-69) tells an
// actor its in-flight source activity "will finish on its own <horizon>". That horizon
// was a shared constant, "shortly", which was honest for every kind the coda was written
// for — refresh 3s, harvest 5s, stoke 30s, repair 15m — and became a lie when LLM-454
// put bake on the same substrate: a bake runs until DUSK. Live 2026-07-18 the engine
// told Anne Walker at midday that her five-hour batch would finish shortly, and she
// announced to the household "the loaves are nearly ready — just a few more minutes by
// the hearth", which kept the whole Walker house asking after bread that was hours out.
// The model was being accurate to what it was told. Same failure class as LLM-446's
// frozen WorkLeft ("three more minutes, Josiah"), one layer up: a constant that stopped
// being true when a slower kind joined the substrate.

// TestSourceActivityCompletionHorizon pins the horizon per kind at the helper directly,
// so the split is proven even if the matrix later loses a scenario that renders it.
//
// This table covers the kinds that exist today; it canNOT catch a future kind silently
// inheriting "shortly" (code_review), and neither can the switch in the helper — Go has
// no exhaustiveness check. The switch lists every kind explicitly so the question is at
// least visible at the point of change, and this table pins the answers; a new to-dusk
// activity still needs its author to think, and the live cost of not doing so is the
// LLM-464 bug itself.
func TestSourceActivityCompletionHorizon(t *testing.T) {
	for _, tc := range []struct {
		kind sim.SourceActivityKind
		want string
		why  string
	}{
		{sim.SourceActivityRefresh, "shortly", "a refresh is 3 seconds"},
		{sim.SourceActivityHarvest, "shortly", "a harvest is 5 seconds"},
		{sim.SourceActivityStoke, "shortly", "a stoke is 30 seconds"},
		{sim.SourceActivityRepair, "shortly", "a repair is 15 minutes — long, but still 'shortly' to a villager"},
		{sim.SourceActivityBake, "by dusk", "a bake runs to dusk — hours, not minutes (LLM-464)"},
	} {
		if got := sourceActivityCompletionHorizon(tc.kind); got != tc.want {
			t.Errorf("sourceActivityCompletionHorizon(%q) = %q, want %q — %s", tc.kind, got, tc.want, tc.why)
		}
	}
}

// TestBakeCodaNamesDuskNotShortly is the corpus invariant over the real Build → Render
// path: any prompt that renders the mid-bake coda must name dusk, never "shortly". This
// catches a regression that reached neither the helper nor the coda call site directly —
// e.g. someone collapsing the horizon back to a constant, or a new bake-adjacent kind
// inheriting the wrong one.
func TestBakeCodaNamesDuskNotShortly(t *testing.T) {
	// Counted in the parent goroutine off a pre-rendered corpus rather than incremented
	// inside the subtests: as a t.Run closure the counter would race the moment someone
	// adds t.Parallel() to the scenario runner, and a vacuity guard that breaks silently
	// under a test-mode change is worse than no guard (code_review).
	rendered := make(map[string]string, len(perceptionScenarios))
	sawBakeCoda := 0
	for _, sc := range perceptionScenarios {
		out := renderScenario(sc)
		rendered[sc.name] = out
		if strings.Contains(out, "You are baking bread") {
			sawBakeCoda++
		}
	}
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := rendered[sc.name]
			// sourceActivityPhrase renders the bake coda as "You are baking bread at
			// <home>"; the standing self-line uses different words ("at the hearth with
			// the household's bread"), so this matches the coda alone.
			if !strings.Contains(out, "You are baking bread") {
				return
			}
			if strings.Contains(out, "finish on its own shortly") {
				t.Errorf("scenario %q tells a baker the bread will finish 'shortly'. A bake runs until "+
					"dusk — the NPC relays this to the household as 'nearly ready, just a few more "+
					"minutes' and the house spends the afternoon asking after it (LLM-464). The horizon "+
					"is per-kind: sourceActivityCompletionHorizon (render.go).", sc.name)
			}
			if !strings.Contains(out, "finish on its own by dusk") {
				t.Errorf("scenario %q renders the mid-bake coda without naming dusk. The bake cue already "+
					"promises 'fresh loaves by dusk', and the coda must agree with it or the NPC is told "+
					"two different completion times for the same bread (LLM-464).", sc.name)
			}
		})
	}
	// Vacuity floor (the LLM-457 lesson, same posture as
	// TestNoPromptOffersBakeAndSeekWorkTogether): this is string matching over the
	// assembled prompt, so a wording drift in sourceActivityPhrase would silently make
	// every scenario skip and the invariant would pass having checked nothing.
	// homebody_mid_bake is the scenario that meets this floor.
	if sawBakeCoda == 0 {
		t.Error("invariant matched no mid-bake coda anywhere in the matrix — sourceActivityPhrase's bake " +
			"wording probably drifted, or homebody_mid_bake was removed. The dusk assertions above are now " +
			"vacuous; restore a scenario that renders a baker mid-activity.")
	}
}
