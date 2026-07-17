package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sourceactivity_view_test.go — LLM-69. Coverage of ActorView.InFlightSourceActivity
// projection in buildActorView, the standing "you are gathering at X — stay put,
// walking off abandons it" perception line, and the mid-activity triage coda. The
// source-activity sibling of inflight_move_test.go: the cue that holds a mid-pick
// NPC in place when a tick fires mid-window (a PC speaking, a red need). Snapshot
// is hand-built so the test stays independent of LoadWorld / the world goroutine.

func sourceActivitySnap(kind sim.SourceActivityKind, objID sim.VillageObjectID, attr sim.NeedKey, objects map[sim.VillageObjectID]*sim.VillageObject) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"john": {
				State:                   sim.StateIdle,
				Needs:                   map[sim.NeedKey]int{"hunger": 24},
				SourceActivityKind:      kind,
				SourceActivityObjectID:  objID,
				SourceActivityAttribute: attr,
			},
		},
		Structures:     map[sim.StructureID]*sim.Structure{},
		VillageObjects: objects,
		Scenes:         map[sim.SceneID]*sim.Scene{},
		Huddles:        map[sim.HuddleID]*sim.Huddle{},
	}
}

func TestBuildActorView_NotEngaged_NilInFlightSourceActivity(t *testing.T) {
	snap := sourceActivitySnap("", "", "", nil)
	av := buildActorView(snap, "john", snap.Actors["john"])
	if av.InFlightSourceActivity != nil {
		t.Errorf("InFlightSourceActivity = %+v, want nil when not engaged", av.InFlightSourceActivity)
	}
}

func TestBuildActorView_Harvest_ResolvesLabelAndRenders(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {ID: "bush", DisplayName: "Berry Bush"},
	}
	snap := sourceActivitySnap(sim.SourceActivityHarvest, "bush", "", objects)
	v := buildActorView(snap, "john", snap.Actors["john"]).InFlightSourceActivity
	if v == nil {
		t.Fatal("InFlightSourceActivity = nil, want a view")
	}
	if v.Kind != sim.SourceActivityHarvest || v.SourceLabel != "Berry Bush" {
		t.Fatalf("view = %+v, want harvest @ 'Berry Bush'", v)
	}
	got := renderInFlightSourceActivity(*v)
	if !strings.Contains(got, "gathering at Berry Bush") || !strings.Contains(got, "abandon the pick") {
		t.Errorf("render = %q, want 'gathering at Berry Bush ... abandon the pick'", got)
	}
}

func TestBuildActorView_Refresh_VerbFromAttribute(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"well": {ID: "well", DisplayName: "Old Well"},
	}
	cases := []struct {
		attr sim.NeedKey
		want string
	}{
		{"thirst", "drinking at Old Well"},
		{"hunger", "eating at Old Well"},
	}
	for _, tc := range cases {
		snap := sourceActivitySnap(sim.SourceActivityRefresh, "well", tc.attr, objects)
		v := buildActorView(snap, "john", snap.Actors["john"]).InFlightSourceActivity
		if v == nil {
			t.Fatalf("attr %q: nil view", tc.attr)
		}
		if got := renderInFlightSourceActivity(*v); !strings.Contains(got, tc.want) {
			t.Errorf("attr %q: render = %q, want contains %q", tc.attr, got, tc.want)
		}
	}
}

func TestBuildActorView_UnresolvedLabel_DropsAtClause(t *testing.T) {
	// Source object not present in the snapshot maps → empty label → no "at X".
	snap := sourceActivitySnap(sim.SourceActivityHarvest, "missing", "", nil)
	v := buildActorView(snap, "john", snap.Actors["john"]).InFlightSourceActivity
	if v == nil {
		t.Fatal("nil view")
	}
	if got := renderInFlightSourceActivity(*v); strings.Contains(got, " at ") {
		t.Errorf("render = %q, want no 'at <label>' clause when label unresolved", got)
	}
}

func TestRenderActor_IncludesSourceActivityLine(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {ID: "bush", DisplayName: "Berry Bush"},
	}
	snap := sourceActivitySnap(sim.SourceActivityHarvest, "bush", "", objects)
	av := buildActorView(snap, "john", snap.Actors["john"])
	var b strings.Builder
	renderActor(&b, av)
	if !strings.Contains(b.String(), "You are gathering at Berry Bush") {
		t.Errorf("renderActor missing source-activity self-state line:\n%s", b.String())
	}
}

func TestRenderTriage_MidActivityCodaHoldsTheActor(t *testing.T) {
	v := &InFlightSourceActivityView{Kind: sim.SourceActivityHarvest, SourceLabel: "Berry Bush"}
	var b strings.Builder
	renderTriage(&b, map[sim.NeedKey]int{"hunger": 24}, nil, false, false, false, false /*conversationLingering*/, nil, false, false, nil, v)
	out := b.String()
	if !strings.Contains(out, "gathering at Berry Bush") || !strings.Contains(out, "abandons it") {
		t.Errorf("triage coda = %q, want a mid-activity hold steer", out)
	}
	// The mid-activity coda must pre-empt the generic act-now coda.
	if strings.Contains(out, "Choose one action") {
		t.Errorf("triage coda fell through to the generic act-now line:\n%s", out)
	}
}

// TestRenderInFlightSourceActivity_BakeIsSpeechPermissive — LLM-454. The standing
// self-line for a baker invites the one housemate reply the reactor ticks her for
// (bakeReplyDue): unlike the solitary repair/harvest tails ("stay where you are"), it
// says a word to those about her is fine but to stay with the bread. A committed move
// still abandons it.
func TestRenderInFlightSourceActivity_BakeIsSpeechPermissive(t *testing.T) {
	got := renderInFlightSourceActivity(InFlightSourceActivityView{Kind: sim.SourceActivityBake, SourceLabel: "Walker Residence"})
	for _, want := range []string{"a word to those about you is fine", "stay with it", "the bread won't be finished"} {
		if !strings.Contains(got, want) {
			t.Errorf("bake self-line = %q, want it to contain %q", got, want)
		}
	}
	// It replaces the solitary "stay where you are" template with the permissive framing.
	if strings.Contains(got, "stay where you are") {
		t.Errorf("bake self-line should not use the solitary 'stay where you are' template: %q", got)
	}
}

// TestSourceActivityVerb_Bake — LLM-454. The mid-activity triage coda reads the verb
// through sourceActivityPhrase; a bake must render "baking bread", not the "busy"
// fallback. Guards the coda from calling a baker "busy".
func TestSourceActivityVerb_Bake(t *testing.T) {
	if got := sourceActivityVerb(InFlightSourceActivityView{Kind: sim.SourceActivityBake}); got != "baking bread" {
		t.Errorf("sourceActivityVerb(bake) = %q, want %q", got, "baking bread")
	}
}

func TestRenderTriage_BakeMidActivityCoda(t *testing.T) {
	v := &InFlightSourceActivityView{Kind: sim.SourceActivityBake, SourceLabel: "Walker Residence"}
	var b strings.Builder
	renderTriage(&b, map[sim.NeedKey]int{}, nil, false, false, false, false, nil, false, false, nil, v)
	out := b.String()
	if !strings.Contains(out, "baking bread") {
		t.Errorf("bake triage coda = %q, want it to name 'baking bread' (not the 'busy' fallback)", out)
	}
	if strings.Contains(out, "Choose one action") {
		t.Errorf("bake triage coda fell through to the generic act-now line:\n%s", out)
	}
}
