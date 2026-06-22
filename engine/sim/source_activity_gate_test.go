package sim

import (
	"testing"
	"time"
)

// source_activity_gate_test.go — LLM-69. The reactor shelve for an in-flight
// SourceActivity (actorCanReactNow): it holds the tick like a short sleep so a
// passer-by / huddle / idle warrant can't yank the actor off mid-bite into a
// move that abandons the pick — but it YIELDS to the same high-value interrupts a
// break does (a red hunger/thirst need, an operator nudge, a PC speaking), so the
// actor can respond while the standing busy-state perception line keeps it from
// walking off. Mirrors the sleep/break gate matrix in npc_sleep_test.go.
func TestActorCanReactNow_SourceActivityShelvesExceptInterrupts(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(5 * time.Second)
	cases := []struct {
		name         string
		mutate       func(a *Actor)
		wantEligible bool
	}{
		{"busy, no warrant — shelved", func(a *Actor) {
			a.SourceActivity = &SourceActivity{Kind: SourceActivityHarvest, ObjectID: "bush", Until: future}
		}, false},
		{"busy + NPC chatter — shelved", func(a *Actor) {
			a.SourceActivity = &SourceActivity{Kind: SourceActivityHarvest, ObjectID: "bush", Until: future}
			a.Warrants = []WarrantMeta{{Reason: NPCSpeechWarrantReason{}}}
		}, false},
		{"busy + red hunger need — interrupts", func(a *Actor) {
			a.SourceActivity = &SourceActivity{Kind: SourceActivityRefresh, ObjectID: "well", Until: future}
			a.Warrants = []WarrantMeta{{Reason: NeedThresholdWarrantReason{Need: "hunger"}}}
		}, true},
		{"busy + PC speech — interrupts", func(a *Actor) {
			a.SourceActivity = &SourceActivity{Kind: SourceActivityHarvest, ObjectID: "bush", Until: future}
			a.Warrants = []WarrantMeta{{Reason: PCSpeechWarrantReason{}}}
		}, true},
		{"busy + operator nudge — interrupts", func(a *Actor) {
			a.SourceActivity = &SourceActivity{Kind: SourceActivityHarvest, ObjectID: "bush", Until: future}
			a.Warrants = []WarrantMeta{{Force: true, Reason: BasicWarrantReason{K: WarrantKindAdmin}}}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := npc("a", KindNPCStateful)
			a.State = StateIdle
			tc.mutate(a)
			w := sleepTestWorld(a)
			eligible, stale := actorCanReactNow(w, a, now)
			if eligible != tc.wantEligible || stale {
				t.Errorf("got (eligible=%v, stale=%v), want (eligible=%v, stale=false)", eligible, stale, tc.wantEligible)
			}
		})
	}
}
