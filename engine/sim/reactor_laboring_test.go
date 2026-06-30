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
