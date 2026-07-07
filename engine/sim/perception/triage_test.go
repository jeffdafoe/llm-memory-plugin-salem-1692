package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// The non-awaiting universal decision section (ZBBS-WORK-374) opens with this
// phrase; it replaced the bare HOME-355 "Weigh everything above … Choose one
// thing and do it." coda.
const triageMarker = "Weigh what's in front of you"

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
		if got := combinedPrompt(Render(p, DefaultRenderConfig())); !strings.Contains(got, triageMarker) {
			t.Errorf("triage line missing from rich payload:\n%s", got)
		}
	})

	t.Run("empty payload (routine check-in)", func(t *testing.T) {
		got := combinedPrompt(Render(Payload{ActorID: "moses"}, DefaultRenderConfig()))
		if !strings.Contains(got, triageMarker) {
			t.Errorf("triage line missing from empty payload:\n%s", got)
		}
	})
}

// TestRender_TriageInstructionIsLast: the triage line is the closing instruction
// — it must come after every context section (here, the "since your last turn"
// warrant block) so the model reads the directive last, right before it acts.
// ZBBS-HOME-355.
func TestRender_TriageInstructionIsLast(t *testing.T) {
	p := Payload{
		ActorID: "moses",
		Warrants: []sim.WarrantMeta{
			speechWarrant(1, "s1", "bob", "good morrow"),
		},
	}
	got := combinedPrompt(Render(p, DefaultRenderConfig()))
	triageIdx := strings.Index(got, triageMarker)
	warrantIdx := strings.Index(got, "Since your last turn")
	if triageIdx < 0 || warrantIdx < 0 {
		t.Fatalf("expected both the warrant block and the triage line, got:\n%s", got)
	}
	if triageIdx < warrantIdx {
		t.Errorf("triage line should come AFTER the context sections, got:\n%s", got)
	}
}

const restFirstMarker = "rest before tending to other needs"

// TestRender_RestFirstSteer_PeakFatigue: when tiredness is at Peak (exhausted/
// maxed) AND another need is also pressing, the triage closes with a steer to
// resolve rest first — the dual-distress flip-flop fix (ZBBS-WORK-354). Gated on
// Peak, not Red: while only mildly/moderately tired the model chooses freely.
func TestRender_RestFirstSteer_PeakFatigue(t *testing.T) {
	t.Run("exhausted + starving → steer present", func(t *testing.T) {
		p := Payload{
			ActorID: "moses",
			Actor: ActorView{
				State: sim.StateIdle,
				Needs: map[sim.NeedKey]int{"tiredness": sim.NeedMax, "hunger": sim.NeedMax},
			},
		}
		if got := combinedPrompt(Render(p, DefaultRenderConfig())); !strings.Contains(got, restFirstMarker) {
			t.Errorf("expected rest-first steer when exhausted + starving:\n%s", got)
		}
	})

	t.Run("exhausted but nothing else pressing → no steer", func(t *testing.T) {
		p := Payload{
			ActorID: "moses",
			Actor: ActorView{
				State: sim.StateIdle,
				Needs: map[sim.NeedKey]int{"tiredness": sim.NeedMax, "hunger": 0, "thirst": 0},
			},
		}
		if got := combinedPrompt(Render(p, DefaultRenderConfig())); strings.Contains(got, restFirstMarker) {
			t.Errorf("rest-first steer must not fire without a competing pressing need:\n%s", got)
		}
	})

	t.Run("only moderately tired → no steer (free choice)", func(t *testing.T) {
		p := Payload{
			ActorID: "moses",
			Actor: ActorView{
				State: sim.StateIdle,
				Needs: map[sim.NeedKey]int{"tiredness": 12, "hunger": sim.NeedMax},
			},
		}
		if got := combinedPrompt(Render(p, DefaultRenderConfig())); strings.Contains(got, restFirstMarker) {
			t.Errorf("rest-first steer must not fire below peak fatigue:\n%s", got)
		}
	})
}
