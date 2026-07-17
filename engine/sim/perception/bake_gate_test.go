package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake_gate_test.go — LLM-454. The evening bake affordance gate (buildBakeChoice)
// and its cue (renderBakeChoice). Reuses the evening_leisure_test fixtures: a homed
// day-worker at 20:00 (inside the [19:00, 22:00) evening window).

// homeBaker is a homed evening worker standing at `inside`, carrying `flour`.
func homeBaker(inside sim.StructureID, flour int) *sim.ActorSnapshot {
	a := eveningWorker(inside)
	if flour > 0 {
		a.Inventory = map[sim.ItemKind]int{sim.BakeFlourItem: flour}
	}
	return a
}

func TestBuildBakeChoice(t *testing.T) {
	const evening = 20 * 60 // 20:00 — inside [dusk 19:00, bedtime 22:00), 2h to spare

	if v := buildBakeChoice(eveningSnap(evening), homeBaker("cottage", 2)); v == nil || v.Joining {
		t.Errorf("at-home evening with flour: got %+v, want START (non-nil, Joining=false)", v)
	}
	if v := buildBakeChoice(eveningSnap(evening), homeBaker("tavern", 2)); v != nil {
		t.Errorf("away from home: got %+v, want nil", v)
	}
	if v := buildBakeChoice(eveningSnap(16*60), homeBaker("cottage", 2)); v != nil {
		t.Errorf("afternoon (before dusk): got %+v, want nil", v)
	}
	if v := buildBakeChoice(eveningSnap(21*60+45), homeBaker("cottage", 2)); v != nil {
		t.Errorf("too close to bedtime (< 30m left): got %+v, want nil", v)
	}
	if v := buildBakeChoice(eveningSnap(evening), homeBaker("cottage", 0)); v != nil {
		t.Errorf("no flour and no bake going: got %+v, want nil", v)
	}
	// A household bake already going here → a flourless resident JOINS it.
	snap := eveningSnap(evening)
	snap.HomeBakesActive = map[sim.StructureID]bool{"cottage": true}
	if v := buildBakeChoice(snap, homeBaker("cottage", 0)); v == nil || !v.Joining {
		t.Errorf("flourless with a bake going: got %+v, want JOIN (non-nil, Joining=true)", v)
	}
}

func TestRenderBakeChoice(t *testing.T) {
	var b strings.Builder
	renderBakeChoice(&b, &BakeChoiceView{Joining: false})
	if !strings.Contains(b.String(), "Call bake to start") {
		t.Errorf("start cue missing the bake tool: %q", b.String())
	}
	b.Reset()
	renderBakeChoice(&b, &BakeChoiceView{Joining: true})
	if !strings.Contains(b.String(), "Call bake to join") {
		t.Errorf("join cue missing the bake tool: %q", b.String())
	}
	b.Reset()
	renderBakeChoice(&b, nil)
	if b.String() != "" {
		t.Errorf("nil view should render nothing, got %q", b.String())
	}
}
