package sim_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildReactorTestWorld seeds a small world with two actors and starts
// the world goroutine. Settings get a tight evaluator cadence and a
// short jitter window so tests don't wait seconds.
func buildReactorTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Alice"},
		"bob":   {ID: "bob", DisplayName: "Bob"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.ReactorJitterMin = 10 * time.Millisecond
			world.Settings.ReactorJitterMax = 11 * time.Millisecond
			world.Settings.ReactorEvaluatorCadence = 5 * time.Millisecond
			world.Settings.MaxWarrantsPerActor = 4
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return w, cancel
}

// inspectActor pulls actor state through a Command (single-goroutine
// observation, no race against the world).
func inspectActor(t *testing.T, w *sim.World, id sim.ActorID, check func(*sim.Actor)) {
	t.Helper()
	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a, ok := world.Actors[id]
			if !ok {
				t.Errorf("actor %q not found", id)
				return nil, nil
			}
			check(a)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("inspect %q: %v", id, err)
	}
}

func TestWarrantReason_KindAccessor(t *testing.T) {
	tests := []struct {
		name   string
		reason sim.WarrantReason
		want   sim.WarrantKind
	}{
		{"basic", sim.BasicWarrantReason{K: sim.WarrantKindHuddleJoined}, sim.WarrantKindHuddleJoined},
		{"pc_speech", sim.PCSpeechWarrantReason{SpeechID: 1, Speaker: "alice"}, sim.WarrantKindPCSpoke},
		{"npc_speech", sim.NPCSpeechWarrantReason{SpeechID: 2, Speaker: "bob"}, sim.WarrantKindNPCSpoke},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.reason.Kind(); got != tc.want {
				t.Errorf("Kind() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWarrantMeta_KindNilReasonReturnsUnknown(t *testing.T) {
	m := sim.WarrantMeta{TriggerActorID: "alice"}
	if got := m.Kind(); got != sim.WarrantKindUnknown {
		t.Errorf("nil-reason Kind() = %q, want unknown", got)
	}
}

// TestTryStampWarrant_FreshStampsAndChoosesJitter verifies the fresh-
// stamp branch: WarrantedSince = now, WarrantDueAt in jitter window,
// Warrants = [meta].
func TestTryStampWarrant_FreshStampsAndChoosesJitter(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	_, err := w.Send(sim.StampWarrant("alice", sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke},
	}, now))
	if err != nil {
		t.Fatalf("StampWarrant: %v", err)
	}

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil || !a.WarrantedSince.Equal(now) {
			t.Errorf("WarrantedSince = %v, want %v", a.WarrantedSince, now)
		}
		if a.WarrantDueAt == nil {
			t.Fatal("WarrantDueAt nil after fresh stamp")
		}
		// Settings were seeded to 10..11ms — DueAt should land in that window.
		delta := a.WarrantDueAt.Sub(now)
		if delta < 10*time.Millisecond || delta >= 11*time.Millisecond {
			t.Errorf("WarrantDueAt delta = %v, want in [10ms, 11ms)", delta)
		}
		if len(a.Warrants) != 1 {
			t.Errorf("Warrants len = %d, want 1", len(a.Warrants))
		}
		if a.Warrants[0].Kind() != sim.WarrantKindPCSpoke {
			t.Errorf("Warrants[0].Kind = %q, want pc_spoke", a.Warrants[0].Kind())
		}
		if a.Warrants[0].TriggerActorID != "bob" {
			t.Errorf("Warrants[0].TriggerActorID = %q, want bob", a.Warrants[0].TriggerActorID)
		}
	})
}

// TestTryStampWarrant_MergePreservesEarliest verifies the append branch:
// a second stamp while already warranted preserves the first
// WarrantedSince + WarrantDueAt and appends to Warrants.
func TestTryStampWarrant_MergePreservesEarliest(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	first := time.Now().UTC()
	second := first.Add(50 * time.Millisecond)

	_, _ = w.Send(sim.StampWarrant("alice", sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke},
	}, first))

	var firstDueAt time.Time
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		firstDueAt = *a.WarrantDueAt
	})

	_, _ = w.Send(sim.StampWarrant("alice", sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.PCSpeechWarrantReason{SpeechID: 2, Speaker: "bob", Excerpt: "hello"},
	}, second))

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if !a.WarrantedSince.Equal(first) {
			t.Errorf("WarrantedSince = %v, want earliest = %v", a.WarrantedSince, first)
		}
		if !a.WarrantDueAt.Equal(firstDueAt) {
			t.Errorf("WarrantDueAt = %v, want preserved %v", a.WarrantDueAt, firstDueAt)
		}
		if len(a.Warrants) != 2 {
			t.Fatalf("Warrants len = %d, want 2", len(a.Warrants))
		}
		// Second entry is the speech reason.
		if _, ok := a.Warrants[1].Reason.(sim.PCSpeechWarrantReason); !ok {
			t.Errorf("Warrants[1].Reason concrete type = %T, want PCSpeechWarrantReason", a.Warrants[1].Reason)
		}
	})
}

// TestTryStampWarrant_CapDropsOldest verifies MaxWarrantsPerActor drops
// oldest entries when exceeded.
func TestTryStampWarrant_CapDropsOldest(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	// Settings seeded to MaxWarrantsPerActor=4.
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		_, _ = w.Send(sim.StampWarrant("alice", sim.WarrantMeta{
			Reason: sim.PCSpeechWarrantReason{
				SpeechID: sim.SpeechID(i + 1),
				Speaker:  "bob",
				Excerpt:  "msg",
			},
		}, now.Add(time.Duration(i)*time.Millisecond)))
	}

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if len(a.Warrants) != 4 {
			t.Fatalf("Warrants len = %d, want 4 (capped)", len(a.Warrants))
		}
		// Oldest dropped → freshest 4 remain (indices 3..6 → SpeechID 4..7).
		got := a.Warrants[0].Reason.(sim.PCSpeechWarrantReason).SpeechID
		if got != 4 {
			t.Errorf("oldest retained = %d, want 4 (oldest 3 dropped)", got)
		}
		got = a.Warrants[3].Reason.(sim.PCSpeechWarrantReason).SpeechID
		if got != 7 {
			t.Errorf("newest = %d, want 7", got)
		}
	})
}

// TestActorReactorDue_AllBranches exercises the cheap pre-check.
func TestActorReactorDue_AllBranches(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Second)
	future := now.Add(time.Second)
	earlier := now.Add(-2 * time.Second)

	cases := []struct {
		name string
		a    *sim.Actor
		want bool
	}{
		{"nil actor", nil, false},
		{"no warrant", &sim.Actor{}, false},
		// Inconsistent state — DueAt without WarrantedSince must NOT be
		// reported due, because EvaluateReactors dereferences both.
		{"due-without-since (inconsistent)", &sim.Actor{WarrantDueAt: &past}, false},
		{"since-without-due (inconsistent)", &sim.Actor{WarrantedSince: &earlier}, false},
		{"due in future", &sim.Actor{WarrantedSince: &earlier, WarrantDueAt: &future}, false},
		{"due in past, not in flight", &sim.Actor{WarrantedSince: &earlier, WarrantDueAt: &past}, true},
		{"due in past, in flight", &sim.Actor{WarrantedSince: &earlier, WarrantDueAt: &past, TickInFlight: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sim.ActorReactorDue(tc.a, now); got != tc.want {
				t.Errorf("ActorReactorDue = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTryStampWarrant_NilReasonIsNoop guards the internal funnel: callers
// that build a meta with no Reason (accidentally or otherwise) must not
// land a warrant of WarrantKindUnknown. External callers go through the
// StampWarrant Command which errors on nil; the funnel itself silently
// drops so internal misuse doesn't crash the world goroutine.
func TestTryStampWarrant_NilReasonIsNoop(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			sim.TryStampWarrant(world, a, sim.WarrantMeta{TriggerActorID: "bob"}, time.Now())
			if a.WarrantedSince != nil || a.WarrantDueAt != nil || a.Warrants != nil {
				t.Errorf("nil-reason stamp leaked state: since=%v due=%v warrants=%v",
					a.WarrantedSince, a.WarrantDueAt, a.Warrants)
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_ConcludedHuddleIsStale: an actor whose
// CurrentHuddleID points at a concluded huddle is stale — caller clears.
func TestActorCanReactNow_ConcludedHuddleIsStale(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	// Alice joins, then huddle concludes; CurrentHuddleID still set since
	// ConcludeHuddle clears it on the actor. So seed manually with a
	// stale back-ref.
	concludedAt := time.Now().UTC()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Huddles["dead"] = &sim.Huddle{
				ID:          "dead",
				Members:     map[sim.ActorID]struct{}{},
				StructureID: "tavern",
				ConcludedAt: &concludedAt,
			}
			world.Actors["alice"].CurrentHuddleID = "dead"
			return nil, nil
		},
	})

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			eligible, stale := sim.ActorCanReactNow(world, world.Actors["alice"])
			if !stale {
				t.Errorf("expected stale=true for actor with concluded-huddle back-ref")
			}
			if eligible {
				t.Errorf("expected eligible=false")
			}
			return nil, nil
		},
	})
}

func TestActorCanReactNow_HealthyActor(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			eligible, stale := sim.ActorCanReactNow(world, world.Actors["alice"])
			if !eligible || stale {
				t.Errorf("healthy actor: eligible=%v stale=%v; want true,false", eligible, stale)
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_SleepingNotEligible: a sleeping actor returns
// eligible=false, stale=false — the warrant stays open and the
// evaluator backs off by unavailableBackoff.
func TestActorCanReactNow_SleepingNotEligible(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].State = sim.StateSleeping
			eligible, stale := sim.ActorCanReactNow(world, world.Actors["alice"])
			if eligible {
				t.Errorf("sleeping actor: eligible=true, want false")
			}
			if stale {
				t.Errorf("sleeping actor: stale=true, want false (warrant stays open)")
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_RestingNotEligible: same posture as sleeping.
// Resting per actor.go's State enum is the take_break / dwell-credit-
// accumulating posture — recovering, do-not-disturb.
func TestActorCanReactNow_RestingNotEligible(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].State = sim.StateResting
			eligible, stale := sim.ActorCanReactNow(world, world.Actors["alice"])
			if eligible {
				t.Errorf("resting actor: eligible=true, want false")
			}
			if stale {
				t.Errorf("resting actor: stale=true, want false (warrant stays open)")
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_RestingInterruptedByNeed: ZBBS-HOME-329 #3 — a red-tier
// need warrant makes an otherwise do-not-disturb resting actor eligible, so a
// starving NPC isn't locked in rest. The break itself is ended on the emit path
// (EvaluateReactors), not here.
func TestActorCanReactNow_RestingInterruptedByNeed(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateResting
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
			if !eligible || stale {
				t.Errorf("resting + need warrant: eligible=%v stale=%v; want true,false", eligible, stale)
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_RestingInterruptedByForce: ZBBS-HOME-329 #4 — an operator
// force / admin warrant also breaks a rester out, so /nudge can rescue a stuck
// on-break actor (the eligibility gate used to run before the pacing Force-
// bypass, leaving the operator powerless against the most common wedge).
func TestActorCanReactNow_RestingInterruptedByForce(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateResting
			a.Warrants = []sim.WarrantMeta{{Force: true, Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
			if !eligible || stale {
				t.Errorf("resting + force warrant: eligible=%v stale=%v; want true,false", eligible, stale)
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_SleepingNotInterruptedByNeed: sleep stays sacrosanct —
// even a red-tier need does NOT wake a sleeper (contrast a break, above).
func TestActorCanReactNow_SleepingNotInterruptedByNeed(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateSleeping
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NeedThresholdWarrantReason{Need: "hunger"}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
			if eligible {
				t.Errorf("sleeping + need warrant: eligible=true, want false (sleep sacrosanct)")
			}
			if stale {
				t.Errorf("sleeping + need warrant: stale=true, want false (warrant stays open)")
			}
			return nil, nil
		},
	})
}

// TestActorCanReactNow_RestingInterruptedByPCSpeech: ZBBS-HOME-377 — a player
// speaking into a resting NPC's huddle breaks it out of the break the same way a
// red need does, so the keeper a customer is talking to actually answers instead
// of resting through the conversation. The break is ended on the emit path
// (EvaluateReactors), not here.
func TestActorCanReactNow_RestingInterruptedByPCSpeech(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	// Error-check the Send: the assertions live inside Fn, so a command-level
	// error would otherwise skip them and the test would pass hollow (ZBBS-HOME-377
	// code_review #1).
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateResting
			a.Warrants = []sim.WarrantMeta{{Reason: sim.PCSpeechWarrantReason{SpeechID: 1, Speaker: "player"}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
			if !eligible || stale {
				t.Errorf("resting + PC-speech warrant: eligible=%v stale=%v; want true,false", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send command: %v", err)
	}
}

// TestActorCanReactNow_RestingNotInterruptedByNPCSpeech: the discrimination that
// makes ZBBS-HOME-377 safe — an NPC's heard-speech does NOT wake a rester, so
// the village's own chatter can't yank an on-break NPC out (only a PC address, a
// red need, or an operator nudge can). Contrast RestingInterruptedByPCSpeech.
func TestActorCanReactNow_RestingNotInterruptedByNPCSpeech(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateResting
			a.Warrants = []sim.WarrantMeta{{Reason: sim.NPCSpeechWarrantReason{SpeechID: 1, Speaker: "bob"}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
			if eligible {
				t.Errorf("resting + NPC-speech warrant: eligible=true, want false (NPC chatter must not wake a rester)")
			}
			if stale {
				t.Errorf("resting + NPC-speech warrant: stale=true, want false (warrant stays open)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send command: %v", err)
	}
}

// TestActorCanReactNow_SleepingNotInterruptedByPCSpeech: sleep stays sacrosanct —
// even a player's direct address does NOT wake a sleeper (contrast a break,
// above). ZBBS-HOME-377 only makes a break interruptible, never sleep.
func TestActorCanReactNow_SleepingNotInterruptedByPCSpeech(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateSleeping
			a.Warrants = []sim.WarrantMeta{{Reason: sim.PCSpeechWarrantReason{SpeechID: 1, Speaker: "player"}}}
			eligible, stale := sim.ActorCanReactNow(world, a)
			if eligible {
				t.Errorf("sleeping + PC-speech warrant: eligible=true, want false (sleep sacrosanct)")
			}
			if stale {
				t.Errorf("sleeping + PC-speech warrant: stale=true, want false (warrant stays open)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send command: %v", err)
	}
}

// TestActorCanReactNow_OtherStatesEligible — table-drives every other
// macro-state to confirm the gate only fires on sleeping/resting.
// Pinning the negative cases prevents an over-eager future addition
// from silently muting warrants on, say, in-transaction or conversing
// actors.
func TestActorCanReactNow_OtherStatesEligible(t *testing.T) {
	others := []sim.ActorState{
		sim.StateIdle,
		sim.StateWalking,
		sim.StateConversing,
		sim.StateWorking,
		sim.StateShopping,
		sim.StateInTransaction,
		sim.StateEating,
	}
	for _, s := range others {
		state := s
		t.Run(string(state), func(t *testing.T) {
			w, cancel := buildReactorTestWorld(t)
			defer cancel()
			_, _ = w.Send(sim.Command{
				Fn: func(world *sim.World) (any, error) {
					world.Actors["alice"].State = state
					eligible, stale := sim.ActorCanReactNow(world, world.Actors["alice"])
					if !eligible || stale {
						t.Errorf("state %q: eligible=%v stale=%v; want true,false",
							state, eligible, stale)
					}
					return nil, nil
				},
			})
		})
	}
}

// TestEvaluateReactors_SleepingActorWarrantStaysOpen — end-to-end. A
// sleeping actor with a due warrant must NOT tick, and the warrant
// must survive (WarrantedSince stays set, Warrants slice intact);
// WarrantDueAt gets pushed by unavailableBackoff so the next scan
// doesn't immediately reconsider this actor.
func TestEvaluateReactors_SleepingActorWarrantStaysOpen(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)

	var emitted int
	var mu sync.Mutex
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if _, ok := evt.(*sim.ReactorTickDue); ok {
					mu.Lock()
					emitted++
					mu.Unlock()
				}
			}))
			a := world.Actors["alice"]
			a.State = sim.StateSleeping
			t1 := now.Add(-50 * time.Millisecond)
			a.WarrantedSince = &t1
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	mu.Lock()
	if emitted != 0 {
		mu.Unlock()
		t.Fatalf("ReactorTickDue events = %d, want 0 (sleeping actor must not tick)", emitted)
	}
	mu.Unlock()

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("WarrantedSince cleared; want warrant still open after eligible=false")
		}
		if len(a.Warrants) != 1 {
			t.Errorf("Warrants len = %d, want 1 (unconsumed)", len(a.Warrants))
		}
		if a.TickInFlight {
			t.Error("TickInFlight set; sleeping actor must not be marked mid-tick")
		}
		if a.WarrantDueAt == nil {
			t.Fatal("WarrantDueAt cleared; want push by unavailableBackoff")
		}
		// WarrantDueAt should be pushed forward — strictly after `due`.
		// (Not asserting exact backoff value here — that's an internal
		// constant; this test pins the push-forward behavior, not the
		// magnitude.)
		if !a.WarrantDueAt.After(due) {
			t.Errorf("WarrantDueAt = %v, want pushed past original due=%v",
				a.WarrantDueAt, due)
		}
	})
}

// TestEvaluateReactors_ShelvedStaleCycleEvicted — ZBBS-WORK-361. A shelved
// (sleeping) actor whose warrant cycle began longer ago than MaxWarrantAge
// has the cycle dropped instead of pushed forward, so it wakes to current
// state rather than a transcript of everything it slept through. Sleep stays
// sacrosanct — no tick is emitted; the cycle is simply expired.
func TestEvaluateReactors_ShelvedStaleCycleEvicted(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)

	var emitted int
	var mu sync.Mutex
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.MaxWarrantAge = 10 * time.Millisecond
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if _, ok := evt.(*sim.ReactorTickDue); ok {
					mu.Lock()
					emitted++
					mu.Unlock()
				}
			}))
			a := world.Actors["alice"]
			a.State = sim.StateSleeping
			stale := now.Add(-time.Second) // far older than MaxWarrantAge
			a.WarrantedSince = &stale
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	mu.Lock()
	if emitted != 0 {
		mu.Unlock()
		t.Fatalf("ReactorTickDue events = %d, want 0 (sleeping actor must not tick)", emitted)
	}
	mu.Unlock()

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil {
			t.Errorf("WarrantedSince = %v, want nil (stale cycle evicted)", a.WarrantedSince)
		}
		if len(a.Warrants) != 0 {
			t.Errorf("Warrants len = %d, want 0 (stale cycle evicted)", len(a.Warrants))
		}
		if a.TickInFlight {
			t.Error("TickInFlight set; sleeping actor must not be marked mid-tick")
		}
	})
}

// TestEvaluateReactors_ShelvedStaleCyclePrunesToForced — ZBBS-WORK-361
// (code_review R1). A stale cycle that holds a Force warrant (operator nudge)
// alongside ordinary ones is pruned to ONLY the Force warrant — the operator
// signal survives shelving, but the stale ordinary pile is dropped rather than
// re-protected by the presence of the Force warrant. The cycle clock is
// re-anchored so the kept warrant gets a fresh TTL window.
func TestEvaluateReactors_ShelvedStaleCyclePrunesToForced(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.MaxWarrantAge = 10 * time.Millisecond
			a := world.Actors["alice"]
			a.State = sim.StateSleeping
			stale := now.Add(-time.Second)
			a.WarrantedSince = &stale
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{TriggerActorID: "bob", Reason: sim.NPCSpeechWarrantReason{SpeechID: 1, Speaker: "bob"}},
				{TriggerActorID: "carol", Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
				{TriggerActorID: "op", Force: true, Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Fatal("WarrantedSince cleared; a cycle with a Force warrant must be re-anchored, not cleared")
		}
		if len(a.Warrants) != 1 {
			t.Fatalf("Warrants len = %d, want 1 (stale ordinary warrants pruned, Force kept)", len(a.Warrants))
		}
		if !a.Warrants[0].Force {
			t.Errorf("surviving warrant is not the Force warrant: %+v", a.Warrants[0])
		}
		// Cycle clock re-anchored to ~now (>= the original stale stamp) so the
		// kept Force warrant gets a fresh TTL window, not an immediate re-evict.
		if a.WarrantedSince.Before(now) {
			t.Errorf("WarrantedSince = %v, want re-anchored to >= now (%v)", a.WarrantedSince, now)
		}
	})
}

// TestEvaluateReactors_WakingActorFiresWarrant — state-transition
// liveness. A sleeping actor's warrant survives the gate; flipping the
// actor's State back out of sleeping makes the next EvaluateReactors
// pass tick them. Confirms the "lazy clear, state change revives" path
// — no explicit state-transition pass walks the warrant list.
func TestEvaluateReactors_WakingActorFiresWarrant(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)

	var emitted int
	var mu sync.Mutex
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if _, ok := evt.(*sim.ReactorTickDue); ok {
					mu.Lock()
					emitted++
					mu.Unlock()
				}
			}))
			a := world.Actors["alice"]
			a.State = sim.StateSleeping
			t1 := now.Add(-50 * time.Millisecond)
			a.WarrantedSince = &t1
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddleJoined}},
			}
			return nil, nil
		},
	})

	// First pass — sleeping, no tick.
	_, _ = w.Send(sim.EvaluateReactors(now))
	mu.Lock()
	if emitted != 0 {
		mu.Unlock()
		t.Fatalf("first pass while sleeping: emitted=%d, want 0", emitted)
	}
	mu.Unlock()

	// Wake up + ensure the pushed WarrantDueAt is back in the past so
	// the actor is "due" again for the second pass.
	wakeAt := now.Add(time.Hour)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.State = sim.StateIdle
			pastDue := wakeAt.Add(-time.Millisecond)
			a.WarrantDueAt = &pastDue
			return nil, nil
		},
	})

	// Second pass — awake, ticks.
	_, _ = w.Send(sim.EvaluateReactors(wakeAt))
	mu.Lock()
	defer mu.Unlock()
	if emitted != 1 {
		t.Errorf("second pass after waking: emitted=%d, want 1", emitted)
	}
}

// TestEvaluateReactors_EmitsAndConsumesWarrant — the core consume-at-emit
// contract. A due actor's warrant is consumed (cleared); TickInFlight
// flips on; AttemptID is set; ReactorTickDue event fires with the
// Warrants snapshot.
func TestEvaluateReactors_EmitsAndConsumesWarrant(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond) // due immediately

	var received []*sim.ReactorTickDue
	var mu sync.Mutex
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					mu.Lock()
					defer mu.Unlock()
					received = append(received, e)
				}
			}))
			// Hand-stamp so we control WarrantDueAt precisely.
			a := world.Actors["alice"]
			t1 := now.Add(-50 * time.Millisecond)
			a.WarrantedSince = &t1
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	mu.Lock()
	if len(received) != 1 {
		mu.Unlock()
		t.Fatalf("ReactorTickDue events = %d, want 1", len(received))
	}
	evt := received[0]
	mu.Unlock()

	if evt.ActorID != "alice" {
		t.Errorf("evt.ActorID = %q, want alice", evt.ActorID)
	}
	if evt.AttemptID == "" {
		t.Error("evt.AttemptID empty")
	}
	if len(evt.Warrants) != 1 || evt.Warrants[0].Kind() != sim.WarrantKindHuddlePeerJoined {
		t.Errorf("evt.Warrants = %+v", evt.Warrants)
	}

	// Actor state: warrant cleared, in-flight set, attempt-ID matches.
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil || a.WarrantDueAt != nil || a.Warrants != nil {
			t.Errorf("warrant not cleared at emit: since=%v due=%v warrants=%v",
				a.WarrantedSince, a.WarrantDueAt, a.Warrants)
		}
		if !a.TickInFlight {
			t.Error("TickInFlight not set")
		}
		if a.TickAttemptID != evt.AttemptID {
			t.Errorf("actor TickAttemptID = %q, evt = %q", a.TickAttemptID, evt.AttemptID)
		}
	})
}

// TestEvaluateReactors_NewWarrantDuringInFlightSurvives is the load-
// bearing test for the consume-at-emit design — events arriving DURING
// an in-flight LLM call must accumulate, not be silently dropped.
func TestEvaluateReactors_NewWarrantDuringInFlightSurvives(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)

	// Stamp warrant 1, evaluate (consumes, sets in-flight).
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			t1 := now.Add(-50 * time.Millisecond)
			a.WarrantedSince = &t1
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{Reason: sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke}},
			}
			return nil, nil
		},
	})
	_, _ = w.Send(sim.EvaluateReactors(now))

	// Capture the in-flight attempt ID.
	var attemptID sim.TickAttemptID
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if !a.TickInFlight {
			t.Fatal("expected TickInFlight after evaluate")
		}
		attemptID = a.TickAttemptID
	})

	// New warrant stamped DURING in-flight — this is the case that
	// would silently drop signals if WarrantedSince stayed set during
	// the LLM call. Should start a fresh cycle.
	mid := now.Add(time.Millisecond)
	_, _ = w.Send(sim.StampWarrant("alice", sim.WarrantMeta{
		Reason: sim.PCSpeechWarrantReason{SpeechID: 999, Speaker: "bob"},
	}, mid))

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Fatal("fresh warrant not stamped during in-flight")
		}
		if !a.WarrantedSince.Equal(mid) {
			t.Errorf("WarrantedSince = %v, want fresh stamp at %v", a.WarrantedSince, mid)
		}
		if len(a.Warrants) != 1 {
			t.Errorf("Warrants len = %d, want 1 (fresh cycle)", len(a.Warrants))
		}
		if !a.TickInFlight {
			t.Error("TickInFlight should still be true (attempt unfinished)")
		}
		if a.TickAttemptID != attemptID {
			t.Errorf("AttemptID changed during in-flight: %q -> %q", attemptID, a.TickAttemptID)
		}
	})
}

// TestEvaluateReactors_StaleWarrantCleared: an actor with a concluded-
// huddle back-ref has the warrant cleared by the evaluator, no event.
func TestEvaluateReactors_StaleWarrantCleared(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)
	concluded := now.Add(-time.Second)

	var emitted []*sim.ReactorTickDue
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					emitted = append(emitted, e)
				}
			}))
			world.Huddles["dead"] = &sim.Huddle{
				ID:          "dead",
				Members:     map[sim.ActorID]struct{}{},
				StructureID: "tavern",
				ConcludedAt: &concluded,
			}
			a := world.Actors["alice"]
			a.CurrentHuddleID = "dead"
			t1 := now.Add(-50 * time.Millisecond)
			a.WarrantedSince = &t1
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	if len(emitted) != 0 {
		t.Errorf("expected no emission for stale warrant, got %d", len(emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil || a.WarrantDueAt != nil || a.Warrants != nil {
			t.Errorf("stale warrant not cleared")
		}
	})
}

// TestEvaluateReactors_ForceBypassesRateGate: a warrant with Force=true
// emits even when the actor is rate-capped. Used by admin overrides and
// emergency reasons that must fire regardless of pacing.
func TestEvaluateReactors_ForceBypassesRateGate(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.MaxReactorTicksPerActorPerMinute = 2
			a := world.Actors["alice"]
			a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
			recent := now.Add(-30 * time.Second)
			for i := 0; i < 2; i++ {
				a.RecentReactorTicks.Push(recent.Add(time.Duration(i) * time.Second))
			}
			t1 := now.Add(-time.Millisecond)
			a.WarrantedSince = &t1
			due := now.Add(-time.Millisecond)
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{
					Force:  true,
					Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin},
				},
			}
			return nil, nil
		},
	})

	var emitted []*sim.ReactorTickDue
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					emitted = append(emitted, e)
				}
			}))
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	if len(emitted) != 1 {
		t.Fatalf("expected 1 emit with Force=true bypass; got %d", len(emitted))
	}
}

// TestEvaluateReactors_RateGateDelaysInsteadOfDropping: when a rate-
// capped actor would exceed the per-minute cap, WarrantDueAt is pushed
// out — the warrant survives, just delayed.
func TestEvaluateReactors_RateGateDelaysInsteadOfDropping(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()

	// Pre-populate RecentReactorTicks with 4 entries inside the 1-minute
	// window and set the cap to 4 → next fire should NOT emit but should
	// push WarrantDueAt out.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.MaxReactorTicksPerActorPerMinute = 4
			a := world.Actors["alice"]
			a.RecentReactorTicks = sim.NewRingBuffer[time.Time](8)
			recent := now.Add(-30 * time.Second)
			for i := 0; i < 4; i++ {
				a.RecentReactorTicks.Push(recent.Add(time.Duration(i) * time.Second))
			}
			t1 := now.Add(-time.Millisecond)
			a.WarrantedSince = &t1
			due := now.Add(-time.Millisecond)
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{Reason: sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke}},
			}
			return nil, nil
		},
	})

	var emitted []*sim.ReactorTickDue
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					emitted = append(emitted, e)
				}
			}))
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	if len(emitted) != 0 {
		t.Errorf("expected no emit on rate-cap; got %d", len(emitted))
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("warrant cleared by rate-cap (should survive)")
		}
		if a.WarrantDueAt == nil || !a.WarrantDueAt.After(now) {
			t.Errorf("WarrantDueAt not pushed forward: %v (now=%v)", a.WarrantDueAt, now)
		}
	})
}

// TestCompleteReactorTick_MatchingAttemptIDClears: completion command
// with the right AttemptID clears in-flight state.
func TestCompleteReactorTick_MatchingAttemptIDClears(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.TickInFlight = true
			a.TickAttemptID = "tk-abc"
			return nil, nil
		},
	})

	res, err := w.Send(sim.CompleteReactorTick("alice", "tk-abc", sim.TickResult{}, time.Now()))
	if err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
	r := res.(sim.CompleteReactorTickResult)
	if r.Stale {
		t.Error("expected Stale=false on matching attempt")
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.TickInFlight {
			t.Error("TickInFlight not cleared")
		}
		if a.TickAttemptID != "" {
			t.Errorf("TickAttemptID not cleared: %q", a.TickAttemptID)
		}
	})
}

// TestCompleteReactorTick_StaleCompletionIgnored: completion command
// with a mismatched AttemptID is a no-op (Stale=true). This is the
// guard against a late attempt-1 completion clearing attempt-2's flag.
func TestCompleteReactorTick_StaleCompletionIgnored(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.TickInFlight = true
			a.TickAttemptID = "tk-2" // newer attempt
			return nil, nil
		},
	})

	res, err := w.Send(sim.CompleteReactorTick("alice", "tk-1", sim.TickResult{}, time.Now())) // stale
	if err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
	r := res.(sim.CompleteReactorTickResult)
	if !r.Stale {
		t.Error("expected Stale=true on mismatched attempt")
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if !a.TickInFlight {
			t.Error("TickInFlight cleared by stale completion (should not)")
		}
		if a.TickAttemptID != "tk-2" {
			t.Errorf("TickAttemptID = %q, want tk-2 (unchanged)", a.TickAttemptID)
		}
	})
}

// TestCompleteReactorTick_DoesNotClearWarrant: completion must NOT clear
// a fresh warrant cycle stamped during in-flight — that signal would be
// lost. Verifies the "consume at emit, not completion" contract from
// the other direction.
func TestCompleteReactorTick_DoesNotClearWarrant(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.TickInFlight = true
			a.TickAttemptID = "tk-1"
			// Fresh warrant stamped during in-flight.
			t1 := now
			a.WarrantedSince = &t1
			due := now.Add(time.Second)
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{Reason: sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.CompleteReactorTick("alice", "tk-1", sim.TickResult{}, time.Now()))

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Error("warrant cleared by completion (should survive)")
		}
		if a.TickInFlight {
			t.Error("TickInFlight not cleared")
		}
	})
}

func TestStampWarrant_ErrorsOnUnknownActor(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, err := w.Send(sim.StampWarrant("nobody", sim.WarrantMeta{
		Reason: sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke},
	}, time.Now()))
	if err == nil {
		t.Error("expected error for unknown actor")
	}
}

func TestStampWarrant_ErrorsOnNilReason(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	_, err := w.Send(sim.StampWarrant("alice", sim.WarrantMeta{}, time.Now()))
	if err == nil {
		t.Error("expected error for nil Reason")
	}
}

// TestHuddleCommands_StampWarrantsWithExpectedKinds: existing PR 1
// callsites in huddle_commands.go now route through tryStampWarrant
// with kind-specific WarrantMeta. Verifies the wiring.
func TestHuddleCommands_StampWarrantsWithExpectedKinds(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Alice joins tavern alone — gets a HuddleJoined warrant.
	_, _ = w.Send(sim.JoinHuddle("alice", "tavern", "", now))
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if len(a.Warrants) != 1 || a.Warrants[0].Kind() != sim.WarrantKindHuddleJoined {
			t.Errorf("alice (first joiner): warrants = %+v, want one HuddleJoined", a.Warrants)
		}
	})

	// Bob joins — bob gets HuddleJoined, alice gets a HuddlePeerJoined
	// appended (carrying bob as TriggerActorID).
	_, _ = w.Send(sim.JoinHuddle("bob", "tavern", "", now.Add(time.Millisecond)))
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		var sawPeer bool
		for _, war := range a.Warrants {
			if war.Kind() == sim.WarrantKindHuddlePeerJoined && war.TriggerActorID == "bob" {
				sawPeer = true
			}
		}
		if !sawPeer {
			t.Errorf("alice didn't get HuddlePeerJoined for bob's arrival; warrants = %+v", a.Warrants)
		}
	})
	inspectActor(t, w, "bob", func(a *sim.Actor) {
		if len(a.Warrants) != 1 || a.Warrants[0].Kind() != sim.WarrantKindHuddleJoined {
			t.Errorf("bob warrants = %+v, want one HuddleJoined", a.Warrants)
		}
	})

	// Alice leaves — alice gets HuddleLeft, bob gets HuddlePeerLeft.
	_, _ = w.Send(sim.LeaveHuddle("alice", now.Add(2*time.Millisecond)))
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		var sawLeft bool
		for _, war := range a.Warrants {
			if war.Kind() == sim.WarrantKindHuddleLeft {
				sawLeft = true
			}
		}
		if !sawLeft {
			t.Errorf("alice didn't get HuddleLeft; warrants = %+v", a.Warrants)
		}
	})
	inspectActor(t, w, "bob", func(a *sim.Actor) {
		var sawPeerLeft bool
		for _, war := range a.Warrants {
			if war.Kind() == sim.WarrantKindHuddlePeerLeft && war.TriggerActorID == "alice" {
				sawPeerLeft = true
			}
		}
		if !sawPeerLeft {
			t.Errorf("bob didn't get HuddlePeerLeft for alice; warrants = %+v", a.Warrants)
		}
	})
}

// TestLoadWorld_WipesReactorState: ephemeral reactor state is cleared on
// LoadWorld so checkpoint reload doesn't wedge actors or carry stale
// rate-gate history that would delay fresh post-restart warrants.
//
// World.LoadedAt is set as the cold-start anchor for the idle-backstop
// cascade slice — read by that slice's effective-last-activity floor
// so fresh-loaded actors with no RecentReactorTicks aren't treated as
// "idle forever" on the first post-restart sweep.
func TestLoadWorld_WipesReactorState(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	due := now.Add(time.Second)

	preTicks := sim.NewRingBuffer[time.Time](8)
	preTicks.Push(now.Add(-30 * time.Second))
	preTicks.Push(now.Add(-15 * time.Second))

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID:             "alice",
			WarrantedSince: &now,
			WarrantDueAt:   &due,
			Warrants: []sim.WarrantMeta{
				{Reason: sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke}},
			},
			TickInFlight:       true,
			TickAttemptID:      "tk-old",
			RecentReactorTicks: preTicks,
		},
	})

	beforeLoad := time.Now().UTC()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	afterLoad := time.Now().UTC()

	a := w.Actors["alice"]
	if a.WarrantedSince != nil || a.WarrantDueAt != nil || a.Warrants != nil {
		t.Errorf("warrant state survived LoadWorld: since=%v due=%v warrants=%v",
			a.WarrantedSince, a.WarrantDueAt, a.Warrants)
	}
	if a.TickInFlight {
		t.Error("TickInFlight survived LoadWorld")
	}
	if a.TickAttemptID != "" {
		t.Errorf("TickAttemptID survived LoadWorld: %q", a.TickAttemptID)
	}
	if a.RecentReactorTicks != nil {
		t.Errorf("RecentReactorTicks survived LoadWorld (len=%d)", a.RecentReactorTicks.Len())
	}
	// World.LoadedAt is the cold-start anchor; must be set inside the
	// load window so the idle-backstop sweep treats fresh-loaded actors
	// as "active at load time" rather than "idle forever."
	if w.LoadedAt.Before(beforeLoad) || w.LoadedAt.After(afterLoad) {
		t.Errorf("World.LoadedAt = %v, expected within [%v, %v]",
			w.LoadedAt, beforeLoad, afterLoad)
	}
}

// TestEvaluator_RunReactorEvaluatorFiresEventually integrates the
// AfterFunc chain — kicks off the evaluator goroutine, stamps a
// warrant, waits up to ~100ms for the event. Confirms the periodic
// path actually wakes and emits without a hand-driven Send.
func TestEvaluator_RunReactorEvaluatorFiresEventually(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	gotCh := make(chan *sim.ReactorTickDue, 1)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					select {
					case gotCh <- e:
					default:
					}
				}
			}))
			return nil, nil
		},
	})

	evalCtx, evalCancel := context.WithCancel(context.Background())
	defer evalCancel()
	go sim.RunReactorEvaluator(evalCtx, w)

	now := time.Now().UTC()
	_, _ = w.Send(sim.StampWarrant("alice", sim.WarrantMeta{
		Reason: sim.BasicWarrantReason{K: sim.WarrantKindPCSpoke},
	}, now))

	select {
	case <-gotCh:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ReactorTickDue not emitted within 200ms (jitter=10-11ms, cadence=5ms)")
	}
}

// TestRecordReactorTick_ResizesRingOnRaisedCap: if the per-minute cap is
// raised at runtime above the existing ring's capacity, recordReactorTick
// rebuilds the ring at the larger size and preserves the existing
// entries. Without resize, the rate-gate couldn't enforce a higher cap
// because old ticks would drop out before count reached the new threshold.
func TestRecordReactorTick_ResizesRingOnRaisedCap(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()

	// First pass: cap=2 → ring sized to defaultRecentReactorTicksCap (32).
	// Drive 4 emits to populate the ring.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.MaxReactorTicksPerActorPerMinute = 2
			return nil, nil
		},
	})
	for i := 0; i < 4; i++ {
		t1 := now.Add(time.Duration(i) * 100 * time.Millisecond)
		stamped := t1.Add(-50 * time.Millisecond)
		due := t1.Add(-time.Millisecond)
		_, _ = w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				a := world.Actors["alice"]
				// Simulate completion of the previous attempt before
				// re-stamping the next warrant cycle. Without clearing,
				// TickInFlight from the prior emit blocks this iteration.
				a.TickInFlight = false
				a.TickAttemptID = ""
				a.WarrantedSince = &stamped
				a.WarrantDueAt = &due
				a.Warrants = []sim.WarrantMeta{
					{Force: true, Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}},
				}
				return nil, nil
			},
		})
		_, _ = w.Send(sim.EvaluateReactors(t1))
	}

	var preCap int
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.RecentReactorTicks == nil {
			t.Fatal("RecentReactorTicks not allocated")
		}
		preCap = a.RecentReactorTicks.Cap()
		if a.RecentReactorTicks.Len() != 4 {
			t.Errorf("ring len after 4 emits = %d, want 4", a.RecentReactorTicks.Len())
		}
	})

	// Raise the cap dramatically. Next emit should rebuild at 2*cap.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Settings.MaxReactorTicksPerActorPerMinute = 100
			return nil, nil
		},
	})
	t5 := now.Add(500 * time.Millisecond)
	stamped5 := t5.Add(-50 * time.Millisecond)
	due5 := t5.Add(-time.Millisecond)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.TickInFlight = false
			a.TickAttemptID = ""
			a.WarrantedSince = &stamped5
			a.WarrantDueAt = &due5
			a.Warrants = []sim.WarrantMeta{
				{Force: true, Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}},
			}
			return nil, nil
		},
	})
	_, _ = w.Send(sim.EvaluateReactors(t5))

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.RecentReactorTicks.Cap() <= preCap {
			t.Errorf("ring cap %d not raised above prev %d after cap increase",
				a.RecentReactorTicks.Cap(), preCap)
		}
		if a.RecentReactorTicks.Cap() < 200 {
			t.Errorf("ring cap %d < 2*new cap (200)", a.RecentReactorTicks.Cap())
		}
		if a.RecentReactorTicks.Len() != 5 {
			t.Errorf("ring len after resize+1 emit = %d, want 5 (4 preserved + 1 new)",
				a.RecentReactorTicks.Len())
		}
	})
}

// TestNewTickAttemptID_UniqueAndPrefixed: attempt IDs are unique enough
// for stale-completion guards and prefixed for log readability.
func TestNewTickAttemptID_UniqueAndPrefixed(t *testing.T) {
	seen := map[sim.TickAttemptID]bool{}
	for i := 0; i < 100; i++ {
		id := sim.NewTickAttemptID()
		if !strings.HasPrefix(string(id), "tk-") {
			t.Errorf("id %q missing tk- prefix", id)
		}
		if seen[id] {
			t.Errorf("collision: %q", id)
		}
		seen[id] = true
	}
}

// TestEvaluateReactors_AgentlessWarrantCycleCleared pins the evaluator-side
// half of ZBBS-HOME-428: a warrant cycle sitting on an agent-less actor
// (direct field mutation, or pre-fix state carried in memory — the stamping
// funnel refuses such stamps) must emit NO tick and be CLEARED, not skipped,
// so the dead cycle can't re-enter the scan forever. This is the structural
// backstop for the reopenWarrants carry-forward path, which bypasses the
// funnel by design — exactly the path that made the live failure permanent.
func TestEvaluateReactors_AgentlessWarrantCycleCleared(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	due := now.Add(-time.Millisecond)

	var received []*sim.ReactorTickDue
	var mu sync.Mutex
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				if e, ok := evt.(*sim.ReactorTickDue); ok {
					mu.Lock()
					defer mu.Unlock()
					received = append(received, e)
				}
			}))
			a := world.Actors["alice"]
			a.Kind = sim.KindPC
			t1 := now.Add(-50 * time.Millisecond)
			a.WarrantedSince = &t1
			a.WarrantDueAt = &due
			a.Warrants = []sim.WarrantMeta{
				{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined}},
			}
			return nil, nil
		},
	})

	_, _ = w.Send(sim.EvaluateReactors(now))

	mu.Lock()
	if len(received) != 0 {
		mu.Unlock()
		t.Fatalf("ReactorTickDue events = %d, want 0 (agent-less actor must not tick)", len(received))
	}
	mu.Unlock()

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil || a.WarrantDueAt != nil || a.Warrants != nil {
			t.Errorf("agent-less warrant cycle not cleared: since=%v due=%v warrants=%v",
				a.WarrantedSince, a.WarrantDueAt, a.Warrants)
		}
		if a.TickInFlight {
			t.Error("TickInFlight set for an agent-less actor")
		}
	})
}
