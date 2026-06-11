package sim

import (
	"testing"
	"time"
)

// warrant_kind_gate_test.go — ZBBS-HOME-428. The stamping funnel refuses
// agent-less actor kinds: warrants drive LLM reactor ticks, and PCs /
// decoratives have no agent to drive. Before the gate, a PC swept into a
// huddle got a HuddleJoined warrant, the reactor ticked the agent-less human,
// and the failed_before_render carry-forward re-opened the warrant in a
// permanent retry loop (the 2026-06-10 "52 malformed" telemetry).

func TestTryStampWarrant_AgentlessKindsRefused(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		kind ActorKind
		want bool
	}{
		{"stateful NPC stamps", KindNPCStateful, true},
		{"shared-VA NPC stamps", KindNPCShared, true},
		{"PC refused", KindPC, false},
		{"decorative refused", KindDecorative, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &World{}
			a := &Actor{ID: "subject", Kind: c.kind}
			got := tryStampWarrant(w, a, WarrantMeta{
				TriggerActorID: a.ID,
				Reason:         BasicWarrantReason{K: WarrantKindHuddleJoined},
			}, now)
			if got != c.want {
				t.Errorf("tryStampWarrant(kind=%v) = %v, want %v", c.kind, got, c.want)
			}
			if c.want {
				if a.WarrantedSince == nil || len(a.Warrants) != 1 {
					t.Errorf("agent kind should open a cycle, got WarrantedSince=%v Warrants=%d", a.WarrantedSince, len(a.Warrants))
				}
			} else {
				if a.WarrantedSince != nil || len(a.Warrants) != 0 {
					t.Errorf("agent-less kind must hold no warrant state, got WarrantedSince=%v Warrants=%d", a.WarrantedSince, len(a.Warrants))
				}
			}
		})
	}
}
