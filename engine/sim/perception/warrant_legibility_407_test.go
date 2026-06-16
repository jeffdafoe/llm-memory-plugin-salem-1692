package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Render layer (ZBBS-WORK-407 / -418): warrants that wake the actor but carry no
// standalone event content -- pay offers ("## Offers awaiting your decision"),
// shift duty (the duty steer), and the bare operator nudge (WarrantKindAdmin,
// which has no in-world content at all) -- must NOT render as the vague "something
// happened nearby" catch-all in "## What just happened". They are dropped from
// that list; they still wake the actor.

func TestRenderWarrants_SuppressesSectionSurfacedKinds(t *testing.T) {
	nameOf := func(sim.ActorID) string { return "someone" }
	placeNameOf := func(string) string { return "" }
	for _, w := range []sim.WarrantMeta{
		{Reason: sim.PayOfferWarrantReason{}},
		{Reason: sim.ShiftDutyWarrantReason{}},
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}}, // bare operator nudge (ZBBS-WORK-418)
	} {
		var b strings.Builder
		out := &RenderedPrompt{}
		renderWarrants(&b, []sim.WarrantMeta{w}, nameOf, placeNameOf, nil, DefaultRenderConfig(), out)
		got := b.String()
		if strings.Contains(got, "Something happened") {
			t.Errorf("%s rendered the vague catch-all line:\n%s", w.Kind(), got)
		}
		if !strings.Contains(got, "(nothing specific") {
			t.Errorf("%s: with only a section-surfaced warrant, expected the routine-check-in fallback:\n%s", w.Kind(), got)
		}
		if out.RenderedWarrantCount != 0 {
			t.Errorf("%s: RenderedWarrantCount = %d, want 0 (suppressed)", w.Kind(), out.RenderedWarrantCount)
		}
	}
}

// A suppressed kind alongside a real one must not leave a numbering gap: the
// surviving warrant renders as "1.", not "2." (ZBBS-WORK-407).
func TestRenderWarrants_SuppressedKindKeepsContiguousNumbering(t *testing.T) {
	nameOf := func(sim.ActorID) string { return "Ezekiel Crane" }
	placeNameOf := func(string) string { return "" }
	var b strings.Builder
	out := &RenderedPrompt{}
	warrants := []sim.WarrantMeta{
		{Reason: sim.PayOfferWarrantReason{}},                                             // suppressed
		{Reason: sim.NPCSpeechWarrantReason{Speaker: "ezekiel", Excerpt: "Good morrow."}}, // rendered
	}
	renderWarrants(&b, warrants, nameOf, placeNameOf, nil, DefaultRenderConfig(), out)
	got := b.String()
	if strings.Contains(got, "Something happened") {
		t.Errorf("pay-offer leaked the vague catch-all:\n%s", got)
	}
	if !strings.Contains(got, "1. ") || strings.Contains(got, "2. ") {
		t.Errorf("surviving warrant should be numbered 1. with no gap:\n%s", got)
	}
	if out.RenderedWarrantCount != 1 {
		t.Errorf("RenderedWarrantCount = %d, want 1", out.RenderedWarrantCount)
	}
}
