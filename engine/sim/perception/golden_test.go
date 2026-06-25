package perception

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// golden_test.go — LLM-106 perception golden-payload scenario harness (proof of
// concept). Each scenario builds a deterministic Snapshot fixture for one
// situation the perception layer branches on, renders the WHOLE assembled prompt
// (durable + ephemeral — exactly what the model receives, via combinedPrompt),
// and pins it to a checked-in golden file under testdata/goldens/.
//
// The value is the DIFF: a cue change shows, per scenario, exactly how the prompt
// an NPC sees changed — surfacing a cue that leaks into (or vanishes from) a
// situation it shouldn't, which per-builder unit tests structurally can't see
// (they assert one builder's output, never the assembled prompt across the
// branching axes). After an INTENDED change, regenerate and review:
//
//	go test ./sim/perception -run TestPerceptionGoldens -update-goldens
//	git diff -- engine/sim/perception/testdata/goldens   # read every changed scenario
//
// Scope (POC): scenarios MUST be clock-free — no pending deliveries / owed orders.
// renderPendingDeliveries{From,To}Me read time.Now() for the per-order expiry
// clause (render.go), so an order-bearing scenario is not byte-stable yet.
// Injecting that render clock from the Payload is the prerequisite for bringing
// order scenarios into the matrix — tracked on LLM-106. The per-scenario
// determinism guard below trips loudly if a wall-clock read ever sneaks in.

var updateGoldens = flag.Bool("update-goldens", false, "rewrite perception scenario golden files instead of comparing")

// perceptionScenario is one situation under test: a deterministic fixture builder
// plus a stable, filesystem-safe name that maps to testdata/goldens/<name>.golden.
// summary documents intent for a human reading the scenario list — it is NOT
// written into the golden, which stays the exact prompt text the model sees.
type perceptionScenario struct {
	name    string
	summary string
	build   func() (snap *sim.Snapshot, actorID sim.ActorID, warrants []sim.WarrantMeta)
}

func renderScenario(sc perceptionScenario) string {
	snap, actorID, warrants := sc.build()
	return combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
}

func TestPerceptionGoldens(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			got := renderScenario(sc)

			// Determinism guard: re-render from a freshly built fixture and require
			// byte equality. Map-iteration order or a wall-clock read sneaking into
			// the render path would trip this here rather than silently churning the
			// golden on the next -update.
			if second := renderScenario(sc); second != got {
				t.Fatalf("non-deterministic render for %q: two renders of the same fixture differ", sc.name)
			}

			goldenPath := filepath.Join("testdata", "goldens", sc.name+".golden")
			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("mkdir goldens dir: %v", err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s (run with -update-goldens to create it): %v", goldenPath, err)
			}
			if got != string(want) {
				t.Errorf("perception prompt for %q changed.\nIf this change is intended, re-run with -update-goldens and review the golden diff before committing.\n--- got ---\n%s\n--- want (golden) ---\n%s", sc.name, got, string(want))
			}
		})
	}
}

// perceptionScenarios is the (growing) matrix. Seeded from LLM-106 with two
// situations: a keeper alone at its post, and a tired keeper on shift at its post.
// Each new live (a)-class failure should add a scenario here (and, where it states
// a property over the whole matrix, a cross-scenario invariant test).
var perceptionScenarios = []perceptionScenario{
	{
		name: "keeper_alone_at_post_onshift",
		summary: "Stateful keeper arrives at its own store during working hours with no one else present " +
			"(the live Josiah Thorne case, LLM-106). The golden pins exactly what the engine shows him: " +
			"co-presence reads 'no one else here', yet the turn is speak-eligible and framed for trade — " +
			"the structural pull that made the model greet an empty room. When the speak-audience gate lands, " +
			"this golden's diff is where the framing change shows up.",
		build: keeperAloneAtPostOnShift,
	},
	{
		name: "tired_keeper_at_post_onshift",
		summary: "Tired keeper standing at its own post, on shift (LLM-100 positive case). The '## How you can rest' " +
			"cue offers take_break (rest in place) only because the actor is on shift. The golden pins the bullet's " +
			"presence; a regression to the on-shift gate would flip it in the diff.",
		build: tiredKeeperAtPostOnShift,
	},
}

// keeperAloneAtPostOnShift reproduces the LLM-106 live shape: Josiah Thorne, a
// stateful keeper, has just arrived at his own General Store during working hours
// with no one else present. He is not tired or hungry — the only stimulus is the
// arrival itself.
func keeperAloneAtPostOnShift() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID = sim.ActorID("josiah")
		store    = sim.StructureID("general_store")
		home     = sim.StructureID("thorne_residence")
	)
	start, end := 360, 1260 // working hours 06:00–21:00 (closes at 9 in the evening)
	now := 540              // 09:00 — morning, on shift
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   store,
		InsideStructureID: store,
		HomeStructureID:   home,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             44,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
			home:  plainStructure(home, "Thorne Residence"),
		},
	}
	// Self-arrival at the store → "## What just happened: You arrived at General Store."
	warrants := []sim.WarrantMeta{
		{
			TriggerActorID: josiahID,
			Reason:         sim.ArrivalWarrantReason{AtStructureID: store},
			SourceEventID:  1,
		},
	}
	return snap, josiahID, warrants
}

// tiredKeeperAtPostOnShift is the LLM-100 positive case: a tired keeper standing
// inside its own post during its shift, so the rest-in-place (take_break) cue
// fires. No co-present actor, no orders — the rest section is the point.
func tiredKeeperAtPostOnShift() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             3,
		Needs:             map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge: plainStructure(forge, "Blacksmith"),
		},
	}
	return snap, ezekielID, nil
}
