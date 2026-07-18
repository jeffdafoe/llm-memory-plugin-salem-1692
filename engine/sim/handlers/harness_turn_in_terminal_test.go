package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_turn_in_terminal_test.go — LLM-447. A successful turn_in ENDS the tick,
// and the household state it leaves behind is what makes the evening end.
//
// The terminal half is not a style choice: the actor is asleep when the command
// returns, so any further verb in the same batch would be acted by a sleeper.
// This is also what lets the goodnight ride turn_in's own `say` — speak is
// terminal too (LLM-321), so a "say goodnight, then go to bed" pair of calls
// could never both land, and the cue would be unobeyable.
//
// Like harness_craft_terminal_test.go this registers a REAL commit (routed to the
// production sim.TurnIn command) and drives it through the integration fixture,
// because a real committing tick needs the causal root only the ReactorTickDue
// path supplies. client.Requests() is the metric that matters: terminal means
// the done() round never runs.

const turnInTestHuddle = sim.HuddleID("hud-longgoodnight")

// turnInEndState is the post-tick world state the bed-down assertions read,
// collected inside a single world-goroutine command so the reads are consistent.
type turnInEndState struct {
	asleep      bool
	huddle      sim.HuddleID
	peerRetired bool
	peerLeft    bool
}

// newTurnInRegistry registers turn_in exactly as production does (terminal), plus
// speak and the terminal done — speak so the "verb storm after bed" case has a
// second verb available to attempt.
func newTurnInRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := RegisterTurnIn(r); err != nil {
		t.Fatalf("register turn_in: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	return r
}

// seedEveningHousehold puts alice and bess in one huddle inside alice's home,
// with the village clock arranged so that RIGHT NOW is the Walker state: past
// dusk, and still an hour short of the auto-bed hour — the stretch where nothing
// in the engine would bed them and talk was the only affordance.
//
// The clock is derived from time.Now() rather than pinned to a literal 20:30
// because sim.TurnIn is built with time.Now().UTC() inside HandleTurnIn (like
// every other commit handler), so the harness path can't be handed a fabricated
// instant. Dusk goes an hour behind now and dawn eight hours ahead, which puts
// now inside [dusk, dawn) — the turn_in window — while the lodger bedtime lands
// two hours ahead, leaving now OUTSIDE [bedtime, dawn). That gap is the test's
// whole point: if turn_in didn't exist, nothing would bed this actor yet.
func seedEveningHousehold(t *testing.T, w *sim.World) {
	t.Helper()
	nowMin := time.Now().UTC().Hour()*60 + time.Now().UTC().Minute()
	hhmm := func(minuteOfDay int) string {
		m := ((minuteOfDay % 1440) + 1440) % 1440
		return time.Date(2026, 1, 1, m/60, m%60, 0, 0, time.UTC).Format("15:04")
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.Location = time.UTC
		world.Settings.DuskTime = hhmm(nowMin - 60)  // an hour ago — the evening is under way
		world.Settings.DawnTime = hhmm(nowMin + 480) // eight hours out
		// Two hours out, so the deterministic auto-bed window has NOT opened.
		world.Settings.LodgingBedtimeHour = ((nowMin + 120) / 60) % 24
		world.Settings.NPCSleepMaxDurationHours = 12
		world.Structures = map[sim.StructureID]*sim.Structure{
			"walker_residence": {ID: "walker_residence", DisplayName: "Walker Residence"},
		}
		for _, id := range []sim.ActorID{"alice", "bess"} {
			a := world.Actors[id]
			if a == nil {
				// The shared integration fixture seeds only "alice"; the housemate is
				// this test's own. A bed-down needs someone left in the room to read
				// the "has turned in for the night" line — that peer IS the mechanism.
				a = &sim.Actor{ID: id, DisplayName: string(id)}
				world.Actors[id] = a
			}
			a.Kind = sim.KindNPCShared
			a.HomeStructureID = "walker_residence"
			a.InsideStructureID = "walker_residence"
			a.CurrentHuddleID = turnInTestHuddle
			a.Attributes = map[string][]byte{sim.AttrWorker: nil}
			a.Needs = map[sim.NeedKey]int{"tiredness": 12} // below the awareness floor
		}
		world.Huddles = map[sim.HuddleID]*sim.Huddle{
			turnInTestHuddle: {
				ID:          turnInTestHuddle,
				StructureID: "walker_residence",
				Members:     map[sim.ActorID]struct{}{"alice": {}, "bess": {}},
			},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedEveningHousehold: %v", err)
	}
}

// turnInTestNow is the instant the reactor warrant is dated. It must be the REAL
// current time, not a fabricated evening hour: the warrant only comes due when
// its timestamps are in the past, and HandleTurnIn stamps the command with
// time.Now() regardless. seedEveningHousehold is what makes this instant read as
// evening, by moving the village's dusk/dawn around it rather than moving the
// clock.
func turnInTestNow() time.Time { return time.Now().UTC() }

// TestHarness_TurnInSuccess_EndsTheTick is the core terminal assertion: one LLM
// round, no done() round, and the actor is bedded when it is over.
func TestHarness_TurnInSuccess_EndsTheTick(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "turn_in", `{"say":"Goodnight to you both."}`),
		doneTurn("d1"), // must NOT be consumed — turn_in already ended the tick
	)
	f := newIntegrationFixture(t, newTurnInRegistry(t), client)
	defer f.stop()

	seedEveningHousehold(t, f.world)
	now := turnInTestNow()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	rec := f.waitForTerminalTelemetry(t)
	if rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q detail=%v", rec.Kind, rec.Detail)
	}
	if got := rec.Detail["terminal_status"]; got != "success" {
		t.Errorf("terminal_status: got %q, want \"success\" — turn_in ends the tick on success (the actor is asleep)", got)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — turn_in is terminal, so the done() round must never run "+
			"(a second round would be a sleeper still choosing actions)", n)
	}
}

// TestHarness_TurnInBedsTheActorAndPartsTheHuddle asserts the world state the
// tick leaves behind — the part that actually ends the Long Goodnight.
//
// Three things must hold together, and each maps to a way the live loop kept
// going:
//
//   - alice is SleepingUntil-bedded. Without this she is simply a woman who
//     said goodnight, which is what she did 26 times.
//   - alice has LEFT the huddle. A goodnight from someone still standing in the
//     conversation is another turn of the loop, not an exit from it.
//   - bess's warrant reads huddle_peer_RETIRED, not huddle_peer_left. That
//     distinction is the cue that legitimizes the rest of the household
//     following her to bed; "stepped away" invites waiting for her to come back.
func TestHarness_TurnInBedsTheActorAndPartsTheHuddle(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "turn_in", `{"say":"Goodnight to you both."}`),
		doneTurn("d1"),
	)
	f := newIntegrationFixture(t, newTurnInRegistry(t), client)
	defer f.stop()

	seedEveningHousehold(t, f.world)
	now := turnInTestNow()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q detail=%v", rec.Kind, rec.Detail)
	}

	got, err := f.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		alice, bess := world.Actors["alice"], world.Actors["bess"]
		s := turnInEndState{
			asleep: alice.SleepingUntil != nil,
			huddle: alice.CurrentHuddleID,
		}
		for _, wm := range bess.Warrants {
			r, ok := wm.Reason.(sim.BasicWarrantReason)
			if !ok {
				continue
			}
			switch r.K {
			case sim.WarrantKindHuddlePeerRetired:
				s.peerRetired = true
			case sim.WarrantKindHuddlePeerLeft:
				s.peerLeft = true
			}
		}
		return s, nil
	}})
	if err != nil {
		t.Fatalf("inspect world: %v", err)
	}
	s := got.(turnInEndState)

	if !s.asleep {
		t.Error("alice is not bedded after a successful turn_in — she said goodnight and stayed up, " +
			"which is the live failure this ticket exists to fix")
	}
	if s.huddle != "" {
		t.Errorf("alice is still in huddle %q after turning in — a goodnight from inside the conversation "+
			"is another turn of the loop, not an exit", s.huddle)
	}
	if !s.peerRetired {
		t.Error("bess did not receive a huddle_peer_retired warrant — she will read \"stepped away\" and " +
			"wait for alice to come back, instead of \"has turned in for the night\" and following her")
	}
	if s.peerLeft {
		t.Error("bess received the generic huddle_peer_left warrant for a bed-down — the sleep " +
			"classification did not replace it")
	}
}

// TestHarness_TurnInOffGate_IsRefused pins the substrate authority: the perception
// gate is an optimization, sim.TurnIn is the gate. A stale or forged call from a
// place the actor doesn't sleep must bounce as a model-facing error rather than
// bedding it somewhere it has no bed — and the tick must NOT end, so the model
// still gets its turn.
func TestHarness_TurnInOffGate_IsRefused(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "turn_in", `{"say":"Goodnight."}`), // refused: standing in the tavern
		doneTurn("d1"), // round 2 runs — a refused call is not terminal
	)
	f := newIntegrationFixture(t, newTurnInRegistry(t), client)
	defer f.stop()

	seedEveningHousehold(t, f.world)
	// Move alice out of her home — the residency arm now fails.
	if _, err := f.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].InsideStructureID = "tavern"
		return nil, nil
	}}); err != nil {
		t.Fatalf("relocate alice: %v", err)
	}
	now := turnInTestNow()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q detail=%v", rec.Kind, rec.Detail)
	}

	asleep, err := f.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["alice"].SleepingUntil != nil, nil
	}})
	if err != nil {
		t.Fatalf("inspect world: %v", err)
	}
	if asleep.(bool) {
		t.Error("alice was bedded by a turn_in called from the tavern — sim.TurnIn must re-check the gate, " +
			"not trust the advertised tool surface")
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM rounds: got %d, want 2 — a REFUSED turn_in is not terminal, so the model still gets its turn", n)
	}
}
