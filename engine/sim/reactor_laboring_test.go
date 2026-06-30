package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reactor_laboring_test.go — LLM-190. A worker on a live labor job is shelved
// from the tick the same way a mid-source-activity or on-break actor is: the
// huddle it struck the deal in keeps churning, but the worker should be getting
// the job done, not drawing a full-context LLM tick per line. Same high-value
// interrupt carve-outs as the break / source-activity gates (PC speech, operator
// nudge, red-tier hunger/thirst); a red TIREDNESS warrant deliberately does NOT
// cut a job short (the job runs to its end, and the shift-end clamp keeps it out
// of the worker's own bedtime). Mirrors the resting-gate tests in reactor_test.go.

func TestActorCanReactNow_LaboringNotEligible(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].State = sim.StateLaboring
			eligible, stale := sim.ActorCanReactNow(world, world.Actors["alice"])
			if eligible {
				t.Errorf("laboring actor: eligible=true, want false (shelved for the job window)")
			}
			if stale {
				t.Errorf("laboring actor: stale=true, want false (warrant stays open)")
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
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
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
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: "tiredness"}}}
			eligible, _ := sim.ActorCanReactNow(world, a)
			if eligible {
				t.Errorf("laboring + red tiredness: eligible=true, want false (a job runs to its end; tiredness doesn't cut it, mirroring a break)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// The State enum can lag the authoritative LaboringUntil window (as Sleeping
// does), so the gate fires on the window alone even when State has not been set
// to Laboring.
func TestActorCanReactNow_LaboringUntilTimestampOnly(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateIdle
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if eligible || stale {
				t.Errorf("laboring window (enum idle): eligible=%v stale=%v; want false,false", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}
