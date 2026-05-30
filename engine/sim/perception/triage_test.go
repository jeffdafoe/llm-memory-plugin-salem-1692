package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

const triageMarker = "Weigh everything above and act on what matters most right now"

// TestRender_TriageInstructionAlwaysPresent: the closing prioritization line
// (ZBBS-HOME-355) renders on every NPC tick — both a content-rich payload and
// the near-empty routine-check-in case. Render is NPC-only (harness path), so it
// is unconditional. ZBBS-HOME-355.
func TestRender_TriageInstructionAlwaysPresent(t *testing.T) {
	t.Run("rich payload", func(t *testing.T) {
		p := Payload{
			ActorID: "moses",
			Actor:   ActorView{State: sim.StateIdle, Needs: map[sim.NeedKey]int{"hunger": 24}},
			Warrants: []sim.WarrantMeta{
				{Reason: sim.ArrivalWarrantReason{}, TriggerActorID: "x"},
			},
		}
		if got := Render(p, DefaultRenderConfig()).Text; !strings.Contains(got, triageMarker) {
			t.Errorf("triage line missing from rich payload:\n%s", got)
		}
	})

	t.Run("empty payload (routine check-in)", func(t *testing.T) {
		got := Render(Payload{ActorID: "moses"}, DefaultRenderConfig()).Text
		if !strings.Contains(got, triageMarker) {
			t.Errorf("triage line missing from empty payload:\n%s", got)
		}
	})
}

// TestRender_TriageInstructionIsLast: the triage line is the closing instruction
// — it must come after every context section (here, the "what just happened"
// warrant block) so the model reads the directive last, right before it acts.
// ZBBS-HOME-355.
func TestRender_TriageInstructionIsLast(t *testing.T) {
	p := Payload{
		ActorID: "moses",
		Warrants: []sim.WarrantMeta{
			speechWarrant(1, "s1", "bob", "good morrow"),
		},
	}
	got := Render(p, DefaultRenderConfig()).Text
	triageIdx := strings.Index(got, triageMarker)
	warrantIdx := strings.Index(got, "What just happened")
	if triageIdx < 0 || warrantIdx < 0 {
		t.Fatalf("expected both the warrant block and the triage line, got:\n%s", got)
	}
	if triageIdx < warrantIdx {
		t.Errorf("triage line should come AFTER the context sections, got:\n%s", got)
	}
}
