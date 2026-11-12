package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake_red_need_test.go — LLM-983. The bake scene must agree with the tool gate
// and the reactor: a baker with a red hunger, thirst or cold keeps move_to and
// is ticked through the shelve, so her page has to send her, not hold her. And
// a helper is never told that her leaving costs the bread.
//
// Live 2026-11-12: Anne Walker, a helper at the Walker bake, sat at hunger 22
// and thirst 20 for the morning. Her page listed the Inn, the Tavern and the
// Well, and the same page said "stay with it; if you walk off now the bread
// won't be finished" and "leaving now abandons it". She obeyed the hold.

// The phrases that HOLD a baker at the hearth. A page that sends her must carry
// none of them.
var bakeHoldPhrases = []string{
	"stay with it",
	"the bread won't be finished",
	"leaving now abandons it",
	"do not walk away",
}

// The phrases that state a cost to the batch. A helper's page must carry none
// of them, pressing need or not.
var bakeFalseStakeForHelper = []string{
	"the bread won't be finished",
	"leaving now abandons it",
	"this batch ends",
}

// bakeSceneFlags reads the two view facts for a scenario's subject, or ok=false
// when the subject is not mid-bake.
func bakeSceneFlags(sc perceptionScenario) (helper, mayStepOut, ok bool) {
	snap, actorID, warrants := sc.build()
	v := Build(snap, actorID, warrants).Actor.InFlightSourceActivity
	if v == nil || v.Kind != sim.SourceActivityBake {
		return false, false, false
	}
	return v.BakeHelper, v.MayStepOut, true
}

// TestGoldensBakeSceneYieldsToRedNeed — cross-scenario invariant. Wherever a
// baker has a red need the shelve leaves actionable, no hold phrase renders and
// the page names move_to. Vacuity-guarded.
func TestGoldensBakeSceneYieldsToRedNeed(t *testing.T) {
	seen := 0
	for _, sc := range perceptionScenarios {
		_, mayStepOut, ok := bakeSceneFlags(sc)
		if !ok || !mayStepOut {
			continue
		}
		seen++
		out := renderScenario(sc)
		for _, phrase := range bakeHoldPhrases {
			if strings.Contains(out, phrase) {
				t.Errorf("scenario %q: a baker with a red need is still held at the hearth by %q (LLM-983)", sc.name, phrase)
			}
		}
		if !strings.Contains(out, "your need is real cause to leave it") || !strings.Contains(out, "move_to") {
			t.Errorf("scenario %q: a baker with a red need is not sent to tend it (LLM-983):\n%s", sc.name, out)
		}
	}
	if seen == 0 {
		t.Fatal("no scenario puts a red need on a baker — the invariant is vacuous (LLM-983)")
	}
}

// TestGoldensBakeHelperNeverToldTheBreadIsLost — cross-scenario invariant. A
// helper's leaving is a no-op for the batch (sim.concludeAbandonedBake), so her
// page never states a cost to it. Vacuity-guarded.
func TestGoldensBakeHelperNeverToldTheBreadIsLost(t *testing.T) {
	seen := 0
	for _, sc := range perceptionScenarios {
		helper, _, ok := bakeSceneFlags(sc)
		if !ok || !helper {
			continue
		}
		seen++
		out := renderScenario(sc)
		for _, phrase := range bakeFalseStakeForHelper {
			if strings.Contains(out, phrase) {
				t.Errorf("scenario %q: a helper is told %q, which is false for her (LLM-983)", sc.name, phrase)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no scenario has a bake helper mid-activity — the invariant is vacuous (LLM-983)")
	}
}

// TestBakeSceneFourWays pins each of the four standing lines and its coda by
// its load-bearing tokens.
func TestBakeSceneFourWays(t *testing.T) {
	cases := []struct {
		name               string
		build              func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta)
		helper, mayStepOut bool
		want               []string
	}{
		{"lead, no need", homebodyMidBake, false, false, []string{
			"stay with it; if you walk off now the bread won't be finished",
			"do not walk away without real cause: leaving now abandons it",
		}},
		{"helper, no need", helperMidBake, true, false, []string{
			"lending a hand with the household's bread — a word to those about you is fine, but stay with it till the loaves are done",
			"do not walk away without real cause. Otherwise call done()",
		}},
		{"helper, red need", helperMidBakeRedHungerThirst, true, true, []string{
			"your own need will not keep till dusk. The bread is in other hands and loses nothing if you step out",
			"your need is real cause to leave it. Go and see to yourself now: use move_to for a place named above. The bread will be here when you are back.",
		}},
		{"lead, red need", bakerMidBakeRedHunger, false, true, []string{
			"If you walk off, this batch ends, though the flour keeps",
			"your need is real cause to leave it. Go and see to yourself now: use move_to for a place named above.\n",
		}},
	}
	for _, c := range cases {
		snap, actorID, warrants := c.build()
		p := Build(snap, actorID, warrants)
		v := p.Actor.InFlightSourceActivity
		if v == nil {
			t.Fatalf("%s: subject is not mid-bake", c.name)
		}
		if v.BakeHelper != c.helper || v.MayStepOut != c.mayStepOut {
			t.Errorf("%s: got helper=%v mayStepOut=%v, want helper=%v mayStepOut=%v", c.name, v.BakeHelper, v.MayStepOut, c.helper, c.mayStepOut)
		}
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: prompt lacks %q", c.name, w)
			}
		}
		if c.name == "lead, red need" && strings.Contains(out, "The bread will be here when you are back") {
			t.Errorf("%s: the baker who started the batch is promised the bread on her return — her leaving ends it", c.name)
		}
	}
}

// TestHasRedNeedBesidesTiredness tables the predicate against the tool gate's
// rule: any need but tiredness at or over its red threshold.
func TestHasRedNeedBesidesTiredness(t *testing.T) {
	snap := &sim.Snapshot{NeedThresholds: sim.NeedThresholds{}}
	red := func(key sim.NeedKey) int { return snap.NeedThresholds.Get(key) }
	cases := []struct {
		name  string
		needs map[sim.NeedKey]int
		want  bool
	}{
		{"no needs", nil, false},
		{"hunger just under red", map[sim.NeedKey]int{"hunger": red("hunger") - 1}, false},
		{"hunger at red", map[sim.NeedKey]int{"hunger": red("hunger")}, true},
		{"thirst at red", map[sim.NeedKey]int{"thirst": red("thirst")}, true},
		{"cold at red", map[sim.NeedKey]int{"cold": red("cold")}, true},
		{"hunger at the peak", map[sim.NeedKey]int{"hunger": sim.NeedMax}, true},
		{"tiredness alone, however high, does not count", map[sim.NeedKey]int{"tiredness": sim.NeedMax}, false},
		{"tiredness red with hunger mild", map[sim.NeedKey]int{"tiredness": sim.NeedMax, "hunger": red("hunger") - 1}, false},
	}
	for _, c := range cases {
		if got := hasRedNeedBesidesTiredness(&sim.ActorSnapshot{Needs: c.needs}, snap); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if hasRedNeedBesidesTiredness(nil, snap) || hasRedNeedBesidesTiredness(&sim.ActorSnapshot{}, nil) {
		t.Error("nil actor or snapshot: want false")
	}
}

// TestBakeHelperNeedsAKnownOtherInitiator — the helper reading is taken only on
// positive evidence: a live session at her hearth that someone else started.
// With no session on the snapshot she reads as the baker with something to lose.
func TestBakeHelperNeedsAKnownOtherInitiator(t *testing.T) {
	snap, actorID, _ := homebodyMidBake()
	a := snap.Actors[actorID]
	home := sim.StructureID(a.SourceActivityObjectID)
	cases := []struct {
		name       string
		initiators map[sim.StructureID]sim.ActorID
		want       bool
	}{
		{"no session known", nil, false},
		{"she started it", map[sim.StructureID]sim.ActorID{home: actorID}, false},
		{"a housemate started it", map[sim.StructureID]sim.ActorID{home: "someone-else"}, true},
		{"a bake at another house", map[sim.StructureID]sim.ActorID{"other_house": "someone-else"}, false},
		{"session with no initiator recorded", map[sim.StructureID]sim.ActorID{home: ""}, false},
	}
	for _, c := range cases {
		snap.HomeBakeInitiators = c.initiators
		v := buildInFlightSourceActivity(snap, actorID, a)
		if v == nil || v.BakeHelper != c.want {
			t.Errorf("%s: got %+v, want BakeHelper=%v", c.name, v, c.want)
		}
	}
}
