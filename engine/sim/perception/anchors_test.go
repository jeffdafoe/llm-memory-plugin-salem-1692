package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func anchorTestSnap() *sim.Snapshot {
	return &sim.Snapshot{
		Structures: map[sim.StructureID]*sim.Structure{
			"019dbcd2": {DisplayName: "Tavern"},
			"gstore":   {DisplayName: "General Store"},
			"thorne":   {DisplayName: "Thorne Residence"},
		},
	}
}

func TestBuildAnchors_SamePlace(t *testing.T) {
	v := buildAnchors(anchorTestSnap(), &sim.ActorSnapshot{WorkStructureID: "019dbcd2", HomeStructureID: "019dbcd2"})
	if v == nil {
		t.Fatal("expected non-nil anchors")
	}
	if !v.SamePlace {
		t.Errorf("SamePlace = false, want true (home==work)")
	}
	if v.WorkID != "019dbcd2" || v.WorkLabel != "Tavern" {
		t.Errorf("work = %q/%q, want 019dbcd2/Tavern", v.WorkID, v.WorkLabel)
	}
}

func TestBuildAnchors_Different(t *testing.T) {
	v := buildAnchors(anchorTestSnap(), &sim.ActorSnapshot{WorkStructureID: "gstore", HomeStructureID: "thorne"})
	if v == nil || v.SamePlace {
		t.Fatalf("got %+v, want distinct anchors", v)
	}
	if v.WorkID != "gstore" || v.WorkLabel != "General Store" || v.HomeID != "thorne" || v.HomeLabel != "Thorne Residence" {
		t.Errorf("got %+v", v)
	}
}

func TestBuildAnchors_Neither_nil(t *testing.T) {
	if v := buildAnchors(anchorTestSnap(), &sim.ActorSnapshot{}); v != nil {
		t.Errorf("expected nil for an actor with no anchors, got %+v", v)
	}
}

func TestBuildAnchors_PresentButUnlabeled_keepsId(t *testing.T) {
	// A structure PRESENT in the snapshot but with no DisplayName still surfaces
	// its id — the model needs the id for move_to; render uses a generic phrase.
	snap := &sim.Snapshot{Structures: map[sim.StructureID]*sim.Structure{"nolabel": {}}}
	v := buildAnchors(snap, &sim.ActorSnapshot{WorkStructureID: "nolabel"})
	if v == nil || v.WorkID != "nolabel" || v.WorkLabel != "" {
		t.Fatalf("got %+v, want WorkID=nolabel with empty label", v)
	}
}

func TestBuildAnchors_MissingStructure_dropped(t *testing.T) {
	// An anchor id ABSENT from the snapshot must NOT be surfaced — move_to would
	// reject it, recreating the bouncing-target failure this change removes.
	v := buildAnchors(anchorTestSnap(), &sim.ActorSnapshot{WorkStructureID: "ghost"})
	if v != nil {
		t.Fatalf("expected nil (unresolvable anchor dropped), got %+v", v)
	}
}

func TestRenderAnchors_SamePlace_carriesProseAndId(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkLabel: "Tavern", WorkID: "019dbcd2", HomeLabel: "Tavern", HomeID: "019dbcd2", SamePlace: true}, false, "")
	out := b.String()
	if !strings.Contains(out, "structure_id: 019dbcd2") {
		t.Errorf("missing structure_id; got %q", out)
	}
	if !strings.Contains(out, "Tavern") {
		t.Errorf("missing label; got %q", out)
	}
	t.Logf("RENDERED (same place): %s", strings.TrimSpace(out))
}

func TestRenderAnchors_Different_bothIds(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkLabel: "General Store", WorkID: "gstore", HomeLabel: "Thorne Residence", HomeID: "thorne"}, false, "")
	out := b.String()
	if !strings.Contains(out, "structure_id: gstore") || !strings.Contains(out, "structure_id: thorne") {
		t.Errorf("missing one of the ids; got %q", out)
	}
	if !strings.Contains(out, "whenever you wish") {
		t.Errorf("off-post anchors should carry the open invite; got %q", out)
	}
	t.Logf("RENDERED (different): %s", strings.TrimSpace(out))
}

// ZBBS-WORK-431: on-shift AT its own post, the anchors line keeps BOTH
// structure_ids (still navigable) but drops the "head ... whenever you wish"
// invite that pulled an idle owner home — home is reframed as after-hours. The
// at-post duty steer (renderDutySteer) carries the "stay put" cue in tandem.
func TestRenderAnchors_AtPost_reframesDeparture(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkLabel: "General Store", WorkID: "gstore", HomeLabel: "Thorne Residence", HomeID: "thorne"}, true, "")
	out := b.String()
	if !strings.Contains(out, "structure_id: gstore") || !strings.Contains(out, "structure_id: thorne") {
		t.Errorf("at-post anchors must still carry both ids (move_to tokens); got %q", out)
	}
	if strings.Contains(out, "whenever you wish") {
		t.Errorf("at-post anchors must NOT invite departure; got %q", out)
	}
	if !strings.Contains(out, "once your work is done") {
		t.Errorf("at-post anchors should frame home as after-hours; got %q", out)
	}
	t.Logf("RENDERED (at post): %s", strings.TrimSpace(out))
}

func TestRenderAnchors_WorkOnly_emptyLabelFallback(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkID: "x"}, false, "")
	out := b.String()
	if !strings.Contains(out, "your workplace") || !strings.Contains(out, "structure_id: x") {
		t.Errorf("expected generic fallback + id; got %q", out)
	}
}

func TestRenderAnchors_Nil_noOutput(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, nil, false, "")
	if b.String() != "" {
		t.Errorf("expected no output for nil anchors, got %q", b.String())
	}
}

// LLM-214: standing INSIDE its own home (home-only anchor), the pointer must not
// invite a move to the current structure — state it in-place with no id.
func TestRenderAnchors_InsideHome_marksInPlace(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{HomeLabel: "Thorne Residence", HomeID: "thorne"}, false, "thorne")
	out := b.String()
	if !strings.Contains(out, "You're home") {
		t.Errorf("want an in-place 'You're home' line; got %q", out)
	}
	if strings.Contains(out, "structure_id") {
		t.Errorf("must NOT advertise the current structure as a move target; got %q", out)
	}
	if strings.Contains(out, "whenever you wish") {
		t.Errorf("must NOT invite heading back to where it's standing; got %q", out)
	}
}

// LLM-214: inside its home with a SEPARATE workplace, the home id drops (no-op
// move) but the workplace stays a reachable target.
func TestRenderAnchors_InsideHome_bothAnchors_keepsWorkTargetOnly(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkLabel: "General Store", WorkID: "gstore", HomeLabel: "Thorne Residence", HomeID: "thorne"}, false, "thorne")
	out := b.String()
	if !strings.Contains(out, "You're home") {
		t.Errorf("want an in-place 'You're home' line; got %q", out)
	}
	if !strings.Contains(out, "structure_id: gstore") {
		t.Errorf("workplace must stay a reachable move target; got %q", out)
	}
	if strings.Contains(out, "structure_id: thorne") {
		t.Errorf("home (current structure) must NOT be advertised as a move target; got %q", out)
	}
}

// LLM-214: inside its workplace OFF shift (atPost handles the on-shift case), the
// work id drops but home stays reachable.
func TestRenderAnchors_InsideWorkOffShift_keepsHomeTargetOnly(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkLabel: "General Store", WorkID: "gstore", HomeLabel: "Thorne Residence", HomeID: "thorne"}, false, "gstore")
	out := b.String()
	if !strings.Contains(out, "You're at your workplace") {
		t.Errorf("want an in-place workplace line; got %q", out)
	}
	if !strings.Contains(out, "structure_id: thorne") {
		t.Errorf("home must stay a reachable move target; got %q", out)
	}
	if strings.Contains(out, "structure_id: gstore") {
		t.Errorf("workplace (current structure) must NOT be advertised as a move target; got %q", out)
	}
}

// LLM-214: home==work keeper standing at that shared structure — one in-place line,
// no move id (there's nowhere else to point it).
func TestRenderAnchors_InsideSamePlace_marksInPlace(t *testing.T) {
	var b strings.Builder
	renderAnchors(&b, &AnchorsView{WorkLabel: "Tavern", WorkID: "019dbcd2", HomeLabel: "Tavern", HomeID: "019dbcd2", SamePlace: true}, false, "019dbcd2")
	out := b.String()
	if !strings.Contains(out, "home and workplace") {
		t.Errorf("want an in-place home-and-workplace line; got %q", out)
	}
	if strings.Contains(out, "structure_id") {
		t.Errorf("must NOT advertise the current structure as a move target; got %q", out)
	}
}
