package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// lane_test.go — the player tick lane (LLM-723): a turn a player caused is
// admitted, queued and run ahead of queued village turns.

func TestCanAdmitIsPerLane(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel) // buffer 2 per lane

	p.jobs <- tickJob{}
	p.jobs <- tickJob{}
	if p.CanAdmit(sim.TickLaneVillage) {
		t.Fatal("village lane should be full")
	}
	if !p.CanAdmit(sim.TickLanePlayer) {
		t.Fatal("a full village queue must not block the player lane")
	}
	p.playerJobs <- tickJob{}
	p.playerJobs <- tickJob{}
	if p.CanAdmit(sim.TickLanePlayer) {
		t.Fatal("player lane should be full")
	}
	<-p.jobs
	p.Stop()
	if p.CanAdmit(sim.TickLaneVillage) || p.CanAdmit(sim.TickLanePlayer) {
		t.Fatal("neither lane may admit once Stop has begun")
	}
}

func TestHandleEventEnqueuesOnTheEventsLane(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	p.handleEvent(w, &sim.ReactorTickDue{ActorID: "alice", AttemptID: "A1", Lane: sim.TickLanePlayer})
	p.handleEvent(w, &sim.ReactorTickDue{ActorID: "alice", AttemptID: "A2"})

	if len(p.playerJobs) != 1 || len(p.jobs) != 1 {
		t.Fatalf("player queue = %d, village queue = %d; want 1 and 1", len(p.playerJobs), len(p.jobs))
	}
	if job := <-p.playerJobs; job.attemptID != "A1" {
		t.Fatalf("player queue holds %q, want A1", job.attemptID)
	}
}

func TestHandleEventPanicsWhenPlayerLaneFull(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	p.playerJobs <- tickJob{}
	p.playerJobs <- tickJob{}

	assertPanics(t, "enqueue against a full player lane", func() {
		p.handleEvent(w, &sim.ReactorTickDue{ActorID: "alice", AttemptID: "A1", Lane: sim.TickLanePlayer})
	})
}

// TestWorkerRunsPlayerLaneFirst: with village jobs queued ahead of it, a
// player job is the next one a worker runs. Repeated because a plain select
// over both queues would still pass about half the time by chance.
func TestWorkerRunsPlayerLaneFirst(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	for round := 0; round < 10; round++ {
		runner := &fakeRunner{called: make(chan tickJob, 3)}
		p := newPoolWithRunner(w, tel, runner)

		p.jobs <- tickJob{actorID: "alice", attemptID: "village-1"}
		p.jobs <- tickJob{actorID: "alice", attemptID: "village-2"}
		p.playerJobs <- tickJob{actorID: "alice", attemptID: "player"}

		p.Start(context.Background())
		select {
		case job := <-runner.called:
			if job.attemptID != "player" {
				t.Fatalf("round %d: first job run = %q, want the player job", round, job.attemptID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: worker ran nothing", round)
		}
		p.Stop()
		p.Wait()
	}
}

// TestEvaluatorAdmitsPlayerCycleWhenVillageQueueFull is the live 2026-10-08
// shape under the real evaluator and pool: the village queue is full, an NPC
// holds a cycle a player's arrival caused, and another NPC holds an ordinary
// cycle. The player-caused cycle is admitted to the player lane; the other is
// deferred.
func TestEvaluatorAdmitsPlayerCycleWhenVillageQueueFull(t *testing.T) {
	w, tel, cancel := newTestWorldWithActors(t, []sim.ActorID{"john", "hannah", "jefferey"}, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	registerPool(t, w, p)

	p.jobs <- tickJob{}
	p.jobs <- tickJob{}

	now := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["jefferey"].Kind = sim.KindPC
		since := now.Add(-50 * time.Millisecond)
		due := now.Add(-time.Millisecond)
		john := world.Actors["john"]
		john.WarrantedSince, john.WarrantDueAt = &since, &due
		john.Warrants = []sim.WarrantMeta{{
			TriggerActorID: "jefferey",
			Reason:         sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerJoined},
		}}
		hannah := world.Actors["hannah"]
		hannahSince, hannahDue := since, due
		hannah.WarrantedSince, hannah.WarrantDueAt = &hannahSince, &hannahDue
		hannah.Warrants = []sim.WarrantMeta{{
			TriggerActorID: "john",
			Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke},
		}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := w.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	select {
	case job := <-p.playerJobs:
		if job.actorID != "john" {
			t.Fatalf("player lane holds %q, want john", job.actorID)
		}
	default:
		t.Fatal("john's player-caused cycle was not admitted to the player lane")
	}
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].WarrantedSince != nil, nil
	}})
	if err != nil {
		t.Fatalf("read hannah: %v", err)
	}
	if !v.(bool) {
		t.Fatal("hannah's village cycle should stay open (deferred) while the village queue is full")
	}
	sawVillageDefer := false
	for _, rec := range tel.snapshot() {
		if rec.Kind == "deferred" && rec.ActorID == "hannah" && rec.Detail["lane"] == "village" {
			sawVillageDefer = true
		}
	}
	if !sawVillageDefer {
		t.Fatal("expected a deferred record for hannah tagged lane=village")
	}
}
