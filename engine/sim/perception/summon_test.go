package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// summon_test.go — ZBBS-HOME-311, reworked LLM-414. Covers the two
// content-gated summon perception sections: the target-side "## You have
// been summoned" (now with the read-time TTL and the inline meet-place
// structure_id) and the summoner-side "## Your messenger returned", over
// build (cue present vs absent vs aged-out) and render.

func TestBuildSummonsForYou_PresentAndAbsent(t *testing.T) {
	now := time.Now().UTC()
	snap := &sim.Snapshot{PublishedAt: now}
	// Absent: no PendingSummon → nil view.
	if v := buildSummonsForYou(snap, &sim.ActorSnapshot{}); v != nil {
		t.Errorf("want nil with no PendingSummon, got %+v", v)
	}
	if v := buildSummonsForYou(snap, nil); v != nil {
		t.Errorf("want nil for nil actor snapshot, got %+v", v)
	}
	if v := buildSummonsForYou(nil, &sim.ActorSnapshot{}); v != nil {
		t.Errorf("want nil for nil snapshot, got %+v", v)
	}
	// Present.
	subj := &sim.ActorSnapshot{PendingSummon: &sim.PendingSummon{
		SummonerName: "Goodwife Bishop", Place: "the town square", PlaceStructureID: "square",
		Reason: "News of the trial.", At: now,
	}}
	v := buildSummonsForYou(snap, subj)
	if v == nil {
		t.Fatal("want a view with a pending summons, got nil")
	}
	if v.SummonerName != "Goodwife Bishop" || v.Place != "the town square" || v.Reason != "News of the trial." {
		t.Errorf("view fields not carried through: %+v", v)
	}
	if v.PlaceStructureID != "square" {
		t.Errorf("PlaceStructureID not carried through: %+v", v)
	}
	// Aged out (LLM-414 read-time TTL): a cue older than summonCueRenderTTL
	// builds nothing — defense in depth behind the errand machine's clears.
	stale := &sim.ActorSnapshot{PendingSummon: &sim.PendingSummon{
		SummonerName: "S", Place: "p", At: now.Add(-summonCueRenderTTL - time.Minute),
	}}
	if v := buildSummonsForYou(snap, stale); v != nil {
		t.Errorf("want nil for an aged-out cue, got %+v", v)
	}
	// Clock skew (cue stamped 'after' the snapshot) still renders.
	skew := &sim.ActorSnapshot{PendingSummon: &sim.PendingSummon{
		SummonerName: "S", Place: "p", At: now.Add(time.Minute),
	}}
	if v := buildSummonsForYou(snap, skew); v == nil {
		t.Error("clock-skewed fresh cue must still build")
	}
}

func TestBuildSummonRefusal_PresentAndAbsent(t *testing.T) {
	if v := buildSummonRefusal(&sim.ActorSnapshot{}); v != nil {
		t.Errorf("want nil with no SummonRefusal, got %+v", v)
	}
	subj := &sim.ActorSnapshot{SummonRefusal: &sim.SummonRefusal{TargetName: "John Proctor"}}
	v := buildSummonRefusal(subj)
	if v == nil || v.TargetName != "John Proctor" {
		t.Fatalf("want refusal view for John Proctor, got %+v", v)
	}
}

func TestRenderSummonsForYou(t *testing.T) {
	var b strings.Builder
	renderSummonsForYou(&b, &SummonsForYouView{SummonerName: "Goodwife Bishop", Place: "the town square", PlaceStructureID: "sq-1", Reason: "News of the trial."})
	out := b.String()
	for _, want := range []string{"## You have been summoned", "Goodwife Bishop", "the town square", "(structure_id: sq-1)", "News of the trial.", "move_to"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered summons missing %q:\n%s", want, out)
		}
	}
	// No structure id → no empty token rendered.
	var noID strings.Builder
	renderSummonsForYou(&noID, &SummonsForYouView{SummonerName: "S", Place: "the well"})
	if strings.Contains(noID.String(), "structure_id") {
		t.Errorf("id-less summons rendered a structure_id token:\n%s", noID.String())
	}

	// Content-gated: nil view writes nothing.
	var empty strings.Builder
	renderSummonsForYou(&empty, nil)
	if empty.Len() != 0 {
		t.Errorf("nil view rendered content: %q", empty.String())
	}

	// No reason → no trailing reason text, but section still renders.
	var nr strings.Builder
	renderSummonsForYou(&nr, &SummonsForYouView{SummonerName: "S", Place: "the well"})
	if !strings.Contains(nr.String(), "## You have been summoned") {
		t.Errorf("reasonless summons did not render the section:\n%s", nr.String())
	}
}

func TestRenderSummonRefusal(t *testing.T) {
	var b strings.Builder
	renderSummonRefusal(&b, &SummonRefusalView{TargetName: "John Proctor"})
	out := b.String()
	for _, want := range []string{"## Your messenger returned", "John Proctor", "could not be found"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered refusal missing %q:\n%s", want, out)
		}
	}
	var empty strings.Builder
	renderSummonRefusal(&empty, nil)
	if empty.Len() != 0 {
		t.Errorf("nil view rendered content: %q", empty.String())
	}
}

// TestBuildWiresSummonSections: the top-level Build dispatch populates both
// summon views from the snapshot's per-actor cues.
func TestBuildWiresSummonSections(t *testing.T) {
	subj := &sim.ActorSnapshot{
		PendingSummon: &sim.PendingSummon{SummonerName: "S", Place: "the square"},
		SummonRefusal: &sim.SummonRefusal{TargetName: "T"},
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"a": subj}}
	p := Build(snap, "a", nil)
	if p.SummonsForYou == nil {
		t.Error("Build did not wire SummonsForYou")
	}
	if p.SummonRefusal == nil {
		t.Error("Build did not wire SummonRefusal")
	}
}
