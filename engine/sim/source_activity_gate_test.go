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

// TestActorCanReactNow_BakeRepliesToNPCSpeechOnCadence — LLM-454. A baker mid the
// evening bake is BusyAtSource-shelved like any source activity, but the bake is a
// SHARED, sociable occupation, so NPC speech directed at her ticks her too — rate-
// limited to one reply per LaborReplyCadence (default 3m), exactly the laboring
// npcReplyDue carve-out (reactor_laboring_test.go). She answers a housemate without
// abandoning the bread; within the window the utterance waits in her transcript for
// the next tick. The speak-only tool surface (handlers.gateTools) keeps the reply
// from walking her off — tested there.
func TestActorCanReactNow_BakeRepliesToNPCSpeechOnCadence(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	until := now.Add(2 * time.Hour) // the bake runs until the bed cue
	newBaker := func() (*Actor, *World) {
		a := npc("a", KindNPCStateful)
		a.State = StateIdle
		a.SourceActivity = &SourceActivity{Kind: SourceActivityBake, ObjectID: "home", Until: until}
		a.Warrants = []WarrantMeta{{Reason: NPCSpeechWarrantReason{SpeechID: 1, Speaker: "b"}}}
		return a, sleepTestWorld(a)
	}

	// Never ticked: nothing to pace against, so the first reply is due.
	a, w := newBaker()
	if eligible, stale := actorCanReactNow(w, a, now); !eligible || stale {
		t.Errorf("baking + NPC speech, never ticked: eligible=%v stale=%v; want true,false (first reply due)", eligible, stale)
	}

	// Ticked 1m ago (inside the 3m default cadence): shelved — the utterance waits.
	a, w = newBaker()
	a.RecentReactorTicks = NewRingBuffer[time.Time](8)
	a.RecentReactorTicks.Push(now.Add(-1 * time.Minute))
	if eligible, stale := actorCanReactNow(w, a, now); eligible || stale {
		t.Errorf("baking + NPC speech, ticked 1m ago: eligible=%v stale=%v; want false,false (within reply cadence)", eligible, stale)
	}

	// Last tick 4m ago (past the 3m cadence): a fresh reply is due.
	a, w = newBaker()
	a.RecentReactorTicks = NewRingBuffer[time.Time](8)
	a.RecentReactorTicks.Push(now.Add(-4 * time.Minute))
	if eligible, stale := actorCanReactNow(w, a, now); !eligible || stale {
		t.Errorf("baking + NPC speech, ticked 4m ago: eligible=%v stale=%v; want true,false (cadence elapsed)", eligible, stale)
	}
}

// TestActorCanReactNow_NonBakeSourceActivityStillShelvesNPCSpeech — LLM-454. The
// bake NPC-reply carve-out is SCOPED to SourceActivityBake. A harvest (or any non-
// bake source activity) with the same NPC-speech warrant, cadence trivially elapsed,
// stays shelved — only the sociable bake answers chatter. Guards the scope so a
// future kind can't silently inherit the carve-out.
func TestActorCanReactNow_NonBakeSourceActivityStillShelvesNPCSpeech(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	a := npc("a", KindNPCStateful)
	a.State = StateIdle
	a.SourceActivity = &SourceActivity{Kind: SourceActivityHarvest, ObjectID: "bush", Until: now.Add(5 * time.Second)}
	a.Warrants = []WarrantMeta{{Reason: NPCSpeechWarrantReason{SpeechID: 1, Speaker: "b"}}}
	a.RecentReactorTicks = NewRingBuffer[time.Time](8)
	a.RecentReactorTicks.Push(now.Add(-10 * time.Minute)) // cadence trivially elapsed
	w := sleepTestWorld(a)
	if eligible, stale := actorCanReactNow(w, a, now); eligible || stale {
		t.Errorf("harvesting + NPC speech, cadence elapsed: eligible=%v stale=%v; want false,false (only bake answers NPC chatter)", eligible, stale)
	}
}
