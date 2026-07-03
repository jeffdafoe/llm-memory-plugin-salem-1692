package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reactor_laboring_test.go — LLM-190. A worker on a live labor job is shelved
// from the tick the same way a mid-source-activity or on-break actor is: the
// huddle it struck the deal in keeps churning, but the worker should be getting
// the job done, not drawing a full-context LLM tick per line. The gate keys on
// the LaboringUntil window (the authoritative busy signal), NOT the StateLaboring
// enum — a stranded enum with no live window must stay tickable so it can recover
// (code_review). Same high-value interrupt carve-outs as the break / source-
// activity gates (PC speech, operator nudge, red-tier hunger/thirst); a red
// TIREDNESS warrant deliberately does NOT cut a job short. Mirrors the resting-
// gate tests in reactor_test.go.

func TestActorCanReactNow_LaboringShelvedForJobWindow(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if eligible {
				t.Errorf("laboring (live window): eligible=true, want false (shelved for the job window)")
			}
			if stale {
				t.Errorf("laboring (live window): stale=true, want false (warrant stays open)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestActorCanReactNow_LaboringInterruptedByHunger(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}}}
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if !eligible || stale {
				t.Errorf("laboring + red hunger: eligible=%v stale=%v; want true,false (a starving worker may break off to eat)", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestActorCanReactNow_LaboringNotInterruptedByTiredness(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: "tiredness"}}}
			eligible, _ := sim.ActorCanReactNowAt(world, a, now)
			if eligible {
				t.Errorf("laboring + red tiredness: eligible=true, want false (a job runs to its end; tiredness doesn't cut it, mirroring a break)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// LLM-230: NPC speech directed at a laboring worker ticks her too, but rate-
// limited to one conversational reply per LaborReplyCadence (default 3m). She can
// answer a peer without regressing to the pre-190 per-line babble; within the
// window the utterance waits in the huddle transcript for her next tick.
func TestActorCanReactNow_LaboringRepliesToNPCSpeechOnCadence(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NPCSpeechWarrantReason{SpeechID: 1, Speaker: "bob"}}}

			// Never ticked: nothing to pace against, so the first reply is due.
			if eligible, stale := sim.ActorCanReactNowAt(world, a, now); !eligible || stale {
				t.Errorf("laboring + NPC speech, never ticked: eligible=%v stale=%v; want true,false (first reply due)", eligible, stale)
			}

			// Ticked 1m ago (inside the 3m default cadence): shelved — the utterance
			// waits in the transcript; the warrant stays open.
			a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
			a.RecentReactorTicks.Push(now.Add(-1 * time.Minute))
			if eligible, stale := sim.ActorCanReactNowAt(world, a, now); eligible || stale {
				t.Errorf("laboring + NPC speech, ticked 1m ago: eligible=%v stale=%v; want false,false (within reply cadence)", eligible, stale)
			}

			// Last tick 4m ago (past the 3m cadence): a fresh reply is due.
			a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
			a.RecentReactorTicks.Push(now.Add(-4 * time.Minute))
			if eligible, stale := sim.ActorCanReactNowAt(world, a, now); !eligible || stale {
				t.Errorf("laboring + NPC speech, ticked 4m ago: eligible=%v stale=%v; want true,false (cadence elapsed)", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// A PC speaking to a laboring worker is NOT subject to the reply cadence — a human
// is waiting, so she answers even if she ticked a moment ago (the always-through
// PC-speech carve-out, unchanged from LLM-190). Guards the cadence from swallowing
// a player's address.
func TestActorCanReactNow_LaboringPCSpeechBypassesCadence(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			// Just ticked (1s ago) — inside any cadence — but the speaker is a PC.
			a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
			a.RecentReactorTicks.Push(now.Add(-1 * time.Second))
			a.Warrants = []sim.WarrantMeta{{Reason: sim.PCSpeechWarrantReason{SpeechID: 1, Speaker: "player"}}}
			if eligible, stale := sim.ActorCanReactNowAt(world, a, now); !eligible || stale {
				t.Errorf("laboring + PC speech, just ticked: eligible=%v stale=%v; want true,false (a human is waiting; cadence doesn't gate PC speech)", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// A non-speech ambient warrant (an arrival / idle) never ticks a laboring worker,
// even past the reply cadence — the cadence carve-out is for directed NPC speech
// only; the job still shelves everything else.
func TestActorCanReactNow_LaboringAmbientWarrantStillShelved(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			// No prior tick (cadence trivially elapsed), but the warrant is a bare
			// idle-backstop, not NPC speech.
			a.Warrants = []sim.WarrantMeta{{Reason: sim.BasicWarrantReason{K: sim.WarrantKindIdleBackstop}}}
			if eligible, stale := sim.ActorCanReactNowAt(world, a, now); eligible || stale {
				t.Errorf("laboring + idle-backstop warrant: eligible=%v stale=%v; want false,false (only directed speech / high-value interrupts tick a laboring worker)", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// The inverse regression: a stranded StateLaboring enum with NO live window (nil
// or elapsed LaboringUntil, e.g. a missed settle or a reload before the on-load
// reconcile) must NOT be shelved — otherwise the worker is untickable forever and
// can never recover. The window, not the enum, gates the shelve (code_review).
func TestActorCanReactNow_StrandedLaboringEnumStaysTickable(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring

			// nil window
			a.LaboringUntil = nil
			if eligible, _ := sim.ActorCanReactNowAt(world, a, now); !eligible {
				t.Errorf("stranded laboring (nil window): eligible=false, want true (must stay tickable to recover)")
			}
			// elapsed window
			past := now.Add(-time.Minute)
			a.LaboringUntil = &past
			if eligible, _ := sim.ActorCanReactNowAt(world, a, now); !eligible {
				t.Errorf("stranded laboring (elapsed window): eligible=false, want true (must stay tickable to recover)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}
