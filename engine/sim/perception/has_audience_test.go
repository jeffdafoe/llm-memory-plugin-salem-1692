package perception

import "testing"

// TestSurroundingsView_HasAudience locks down the shared audience predicate that
// BOTH the speak tool-gate (handlers.gateTools) and Harness.RunTick's HadAudience
// read: huddle peers OR co-present AWAKE actors are an addressable audience;
// asleep or resting actors are not (this NPC's speech can't rouse them). LLM-106.
func TestSurroundingsView_HasAudience(t *testing.T) {
	one := []HuddleMember{{ID: "x"}}
	cases := []struct {
		name string
		s    SurroundingsView
		want bool
	}{
		{"empty", SurroundingsView{}, false},
		{"huddle peer", SurroundingsView{HuddleMembers: one}, true},
		{"co-present awake", SurroundingsView{CoPresent: one}, true},
		{"asleep only", SurroundingsView{CoPresentAsleep: one}, false},
		{"resting only", SurroundingsView{CoPresentResting: one}, false},
		{"huddle peer despite a sleeper", SurroundingsView{HuddleMembers: one, CoPresentAsleep: one}, true},
		{"co-present awake despite a rester", SurroundingsView{CoPresent: one, CoPresentResting: one}, true},
	}
	for _, c := range cases {
		if got := c.s.HasAudience(); got != c.want {
			t.Errorf("%s: HasAudience() = %v, want %v", c.name, got, c.want)
		}
	}
}
