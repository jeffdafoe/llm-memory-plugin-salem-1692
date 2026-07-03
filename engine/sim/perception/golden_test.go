package perception

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

// TestGoldensNeverAdvertiseHomeAsMoveTargetWhenInside is the LLM-214 cross-scenario
// invariant: whenever the subject actor is standing INSIDE its own home, the
// rendered prompt must never advertise that home's structure_id as a move target.
// "(structure_id: <id>)" is the load-bearing token the model echoes into move_to
// (HOME-349), and you can't move to where you already are — the no-op the model
// looped on (Lewis Walker calling move_to{residence} every tick). Runs over the
// whole matrix so a future cue can't reintroduce the current-home move target for
// any situation, not just the one weary_resident_in_own_home scenario pins.
func TestGoldensNeverAdvertiseHomeAsMoveTargetWhenInside(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.HomeStructureID == "" || a.InsideStructureID != a.HomeStructureID {
				return // subject isn't inside its own home — invariant N/A here
			}
			token := "(structure_id: " + string(a.HomeStructureID) + ")"
			if out := renderScenario(sc); strings.Contains(out, token) {
				t.Errorf("scenario %q: subject stands inside its own home but the prompt advertises that home as a move target %q — you can't move_to where you stand (LLM-214)", sc.name, token)
			}
		})
	}
}

// TestGoldensEnRouteWorkerNotOfferedNewWork is the LLM-229 cross-scenario
// invariant: whenever the subject is a WORKER relocating to an accepted job (an
// EnRoute LaborOffer with the subject as worker), the rendered prompt must offer
// neither the solicit affordance nor the businesses directory — the worker is
// already committed, and a second job would strand the first. Runs over the
// whole matrix so a future cue can't reintroduce work-seeking for a committed
// relocating worker in any situation, not just the one worker_en_route_to_workplace
// scenario pins.
func TestGoldensEnRouteWorkerNotOfferedNewWork(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			enRoute := false
			for _, o := range snap.LaborLedger {
				if o != nil && o.State == sim.LaborStateEnRoute && o.WorkerID == actorID {
					enRoute = true
					break
				}
			}
			if !enRoute {
				return // subject isn't relocating to a job — invariant N/A here
			}
			out := renderScenario(sc)
			if strings.Contains(out, "offer your labor with solicit_work") {
				t.Errorf("scenario %q: subject is relocating to an accepted job but the prompt still offers the solicit affordance (LLM-229)", sc.name)
			}
			if strings.Contains(out, "head to one of the town's businesses") {
				t.Errorf("scenario %q: subject is relocating to an accepted job but the prompt still shows the seek-work businesses directory (LLM-229)", sc.name)
			}
		})
	}
}

// TestGoldensConversationLinesCarryIntervalStamps is the LLM-217 cross-scenario
// invariant: in any scenario whose snapshot carries a clock (PublishedAt set —
// every clocked fixture stamps its utterances relative to it), every line of
// "## Recent conversation here" must carry an interval stamp ("(just now)" /
// "(40s ago)"). The stamp is what lets the model tell rapid-fire churn from a
// normally paced exchange (the Patience Walker go-home ↔ seek-work loop read as
// one continuous moment without it); a future cue path that builds UtteranceView
// without At — or a render change that drops the stamp — fails here for every
// affected scenario, not just the one the LLM-217 golden pins.
func TestGoldensConversationLinesCarryIntervalStamps(t *testing.T) {
	stamped := regexp.MustCompile(`\((just now|\d+[smh] ago)\): `)
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, _, _ := sc.build()
			if snap.PublishedAt.IsZero() {
				return // clockless fixture — stamps are correctly omitted
			}
			out := renderScenario(sc)
			_, section, found := strings.Cut(out, "## Recent conversation here\n")
			if !found {
				return // no conversation section in this situation
			}
			section, _, _ = strings.Cut(section, "\n\n")
			for _, line := range strings.Split(section, "\n") {
				if !stamped.MatchString(line) {
					t.Errorf("scenario %q: conversation line %q carries no interval stamp — the model can't gauge tempo without it (LLM-217)", sc.name, line)
				}
			}
		})
	}
}

// TestGoldensRestockNeverTargetsRememberedShutSupplier is the LLM-216 cross-scenario
// invariant: within the "## Restocking" section of any scenario, a structure the
// subject remembers finding shut (a live ObservedClosed memory) must never appear as
// a "(structure_id: <id>)" walk-to target. A shut supplier is a dead end the weak
// model toured on (Josiah's every-tick move_to loop among shut farms), so the restock
// builder DROPS it rather than annotating it. Runs over the whole matrix so a future
// restock cue change can't reintroduce a shut supplier as a target for any situation,
// not just the one keeper_restock_drops_shut_keeps_open_supplier scenario pins.
// Non-vacuous: that scenario renders a restock section while remembering James Farm
// shut, so the check actually exercises a shut structure.
func TestGoldensRestockNeverTargetsRememberedShutSupplier(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			_, section, found := strings.Cut(renderScenario(sc), "## Restocking\n")
			if !found {
				return // no restock section in this situation — invariant N/A here
			}
			// Bound the scan to the restock section by cutting at the next markdown
			// header, NOT the first blank line — a future intra-section blank line would
			// otherwise hide a bad remembered-shut target lower in the same section
			// (code_review). The section runs to the next "## " or end of prompt.
			if idx := strings.Index(section, "\n## "); idx >= 0 {
				section = section[:idx]
			}
			for structureID := range snap.Structures {
				if !businessRememberedShut(snap, a, structureID) {
					continue
				}
				token := "(structure_id: " + string(structureID) + ")"
				if strings.Contains(section, token) {
					t.Errorf("scenario %q: the restock section advertises remembered-shut supplier %q as a move target — a shut supplier is a dead end and must be dropped (LLM-216)", sc.name, token)
				}
			}
		})
	}
}

// TestGoldensNeverCoachSpeakingAtCompany is the LLM-220 cross-scenario
// invariant: no rendered situation coaches the actor to speak at whoever is
// present. The old co-presence clause ("— speak to start conversing with them")
// fired on every arrival and pushed NPCs into unprompted monologues at any
// co-present actor, PCs included (the live Josiah-at-the-Tavern cold-open).
// Naming the company is legibility; telling the actor to speak is compulsion.
func TestGoldensNeverCoachSpeakingAtCompany(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if strings.Contains(out, "speak to start conversing") {
				t.Errorf("scenario %q: co-presence line coaches speaking — presence must be stated neutrally (LLM-220):\n%s", sc.name, out)
			}
		})
	}
}

// TestGoldensNonDistributorRestockNeverTargetsFarm is the LLM-223 cross-scenario
// invariant: within the "## Restocking" section of any scenario whose subject is
// NOT the village distributor, a farm-tagged structure must never appear as a
// "(structure_id: <id>)" walk-to target. Farm-origin goods flow farms → distributor
// → everyone else, so perception routes a non-distributor's restock through the
// distributor, never straight to a farm the PayWithItem backstop would refuse. Runs
// over the whole matrix so a future restock/vendor cue change can't reintroduce a
// farm as a target for any non-distributor situation. Non-vacuous: the
// reseller_restock_routed_to_distributor_not_farm scenario renders a restock section
// with a farm-tagged milk supplier present in the fixture.
func TestGoldensNonDistributorRestockNeverTargetsFarm(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || sim.ActorIsDistributor(snap.VillageObjects, a.WorkStructureID) {
				return // subject is the distributor (or missing) — invariant N/A here
			}
			_, section, found := strings.Cut(renderScenario(sc), "## Restocking\n")
			if !found {
				return // no restock section in this situation — invariant N/A here
			}
			// Bound the scan to the restock section — cut at the next markdown header
			// so a farm id lower in the prompt (a home/work anchor) can't false-positive.
			if idx := strings.Index(section, "\n## "); idx >= 0 {
				section = section[:idx]
			}
			for id, obj := range snap.VillageObjects {
				if !sim.IsFarmStructure(obj) {
					continue
				}
				token := "(structure_id: " + string(id) + ")"
				if strings.Contains(section, token) {
					t.Errorf("scenario %q: the restock section advertises farm %q as a move target for a non-distributor — farm goods must route through the distributor (LLM-223)", sc.name, token)
				}
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
			"the structural pull that made the model greet an empty room. The speak-audience gate (LLM-106 slice 2) " +
			"fixed it at the tool-advertising layer, so this PAYLOAD is unchanged — the golden is a regression pin; " +
			"the fix's guard is the handlers gating test.",
		build: keeperAloneAtPostOnShift,
	},
	{
		name: "tired_keeper_at_post_onshift",
		summary: "Tired keeper standing at its own post, on shift (LLM-100 positive case). The '## How you can rest' " +
			"cue offers take_break (rest in place) only because the actor is on shift. The golden pins the bullet's " +
			"presence; a regression to the on-shift gate would flip it in the diff.",
		build: tiredKeeperAtPostOnShift,
	},
	{
		name: "weary_resident_in_own_home",
		summary: "LLM-214: a weary salem-vendor (Anne Walker) stands INSIDE its own home, off-shift for the evening, with " +
			"a separate workplace. Before the fix the '## How you can rest' list handed it the home structure_id as a move_to " +
			"target ('sleep in your own bed (structure_id …)') for the structure it was standing in — the no-op move Lewis / " +
			"Anne looped on — and the anchor pointer told it to 'head back there'. The golden pins the in-place cues: the rest " +
			"section leads with the RestAtHome take_break bullet and carries NO home structure_id, and the anchor states " +
			"'You're home' while keeping only the workplace as a reachable move target. The matrix-wide guard is " +
			"TestGoldensNeverAdvertiseHomeAsMoveTargetWhenInside.",
		build: wearyResidentInOwnHome,
	},
	{
		name: "shared_npc_soul_who_you_are",
		summary: "A shared-VA keeper (Hannah, salem-vendor) at her own post during working hours, carrying a " +
			"synthesized about_me soul (LLM-199). The golden pins that '## Who you are' renders the soul prose — the " +
			"fix for the empty-block bug (shared VAs had no rendered identity because render emitted only the never-" +
			"populated seed_text). A regression that muted about_me, reverted the render field, or dropped the build " +
			"gate would show the block going empty in the diff.",
		build: sharedNpcWithSoul,
	},
	{
		name: "homed_worker_evening_tavern_open",
		summary: "A homed day-shift agent (Ezekiel, 07:00–19:00), off-shift and awake at 20:30 — inside the " +
			"evening window [shift-end, 22:00) — standing at his forge after closing up (LLM-149, Lever 2). The golden " +
			"pins the evening 'tavern's open' invitation in ## Around you (carrying the tavern + home structure_ids, no " +
			"forced walk) AND that the off-shift go-home wind-down steer ('Your working hours are over …') is ABSENT: the " +
			"cue REPLACES that turn-in pressure for the window (bedtime is Lever 1's 22:00 gate). A regression that let the " +
			"go-home steer leak back in, or dropped the invitation, shows in the diff.",
		build: homedWorkerEveningTavernOpen,
	},
	{
		name: "homed_worker_evening_too_broke_for_tavern",
		summary: "A homed day-shift agent (Ezekiel, 07:00–19:00) off-shift at 20:30 — inside the evening window — but " +
			"holding only 2 coins, below the tavern's cheapest drink (ale, retail 3, sold by the co-located keeper). LLM-205: " +
			"a night out costs coin, so canAffordLeisure fails and the agent is NOT in evening leisure. The golden pins that " +
			"the 'tavern's open of an evening' invitation is ABSENT and the off-shift go-home wind-down steer ('Your working " +
			"hours are over …') is PRESENT — the broke have no evening; they wind down home and bed at shift-end. The mirror " +
			"of homed_worker_evening_tavern_open (same situation, affordable there).",
		build: homedWorkerEveningTooBrokeForTavern,
	},
	{
		name: "homed_workers_evening_commons_no_solicit",
		summary: "Two homed day-shift workers (Ezekiel + Lewis, different homes and trades) off-shift at 20:30, together at " +
			"the Village Commons — neither at home nor the tavern — with a tavern placed and the subject flush enough to afford " +
			"a drink (10 coins, ale retail 3). LLM-205 rule 2: the subject is in affordable evening leisure, so the solicit-work " +
			"affordance ('offer your labor with solicit_work') is SUPPRESSED even though a solicitable peer is present (without " +
			"the gate an employed worker with a solicitable audience would be offered it). The golden pins the evening cue PRESENT " +
			"and the solicit affordance ABSENT — the lingering don't hustle. Makes TestEveningLeisureSuppressesSolicit non-vacuous.",
		build: homedWorkersEveningCommonsNoSolicit,
	},
	{
		name: "workless_tired_rejoiner_self_action_trail",
		summary: "LLM-217: a workless, tired shared-worker NPC (Patience Walker, the live case) stands back in the Tavern " +
			"huddle with John Ellis the tavernkeeper after twice announcing 'I'll head home now', walking out, and bouncing " +
			"back — the go-home ↔ seek-work oscillation. The golden pins the two perception surfaces that make the churn " +
			"visible: '## Recent conversation here' lines carry interval stamps (John's byte-identical re-greetings read " +
			"'2m ago' vs 'just now', not as one moment), and '## What you've recently done' lists her own departed/arrived " +
			"trail most-recent-first with stamps. Her in-current-huddle 'I'll head home now.' spoke entry appears ONLY in the " +
			"conversation ring — the trail's current-huddle spoke de-dup keeps it out — and John's own walked entry is absent " +
			"(subject filter). The matrix-wide guard is TestGoldensConversationLinesCarryIntervalStamps.",
		build: worklessTiredRejoinerSelfActionTrail,
	},
	{
		name: "keeper_with_ready_order",
		summary: "An innkeeper holds a Ready order (a nights_stay check-in) for a co-present guest. Exercises the " +
			"order book with a deterministic expiry clause — the LLM-106 render-clock fix anchors 'expires in N " +
			"minutes' to the snapshot instant (RenderedAt), so this golden is byte-stable. Without it the expiry text " +
			"drifts with wall-clock and the determinism guard trips. Demonstrates an order-bearing scenario joining " +
			"the matrix.",
		build: keeperWithReadyOrder,
	},
	{
		name: "grower_at_stripped_bush",
		summary: "A forager stands at her own raspberry bush after harvesting it clean (the live Prudence case, " +
			"LLM-98). Her bushes sit wider apart than LoiterAttributionTiles, so the only in-reach gather candidate " +
			"is the now-empty bush — ResolveGatherSource hands it back. The golden pins that the prompt carries NO " +
			"'you can gather' cue (and so no gather tool): the LLM-98 stock gate suppresses the depleted source. A " +
			"regression would make the 'You're at Raspberry Bush — you can gather raspberries here.' line reappear in the diff.",
		build: growerAtStrippedBush,
	},
	{
		name: "hungry_forager_at_stocked_bush",
		summary: "A hungry forager stands at an unowned raspberry bush that still has stock, with a cheese seller at " +
			"the General Store nearby — the LLM-113 situation (Ezekiel at the Raspberry Bush with buy options). The " +
			"golden pins the count-aware catalog phrasing the singular/plural labels drive: the gather cue 'you can " +
			"gather raspberries here', and the buy cue 'buy a wedge of cheese' (the period measure phrase with an " +
			"indefinite article) rather than the bare 'buy Cheese'. A regression in the label model flips those lines.",
		build: hungryForagerAtStockedBush,
	},
	{
		name: "smith_choosing_at_forge",
		summary: "A multi-output crafter (Ezekiel the blacksmith: skillet + nail) stands UNFOCUSED at his own forge on " +
			"shift — the post-restart state the production-choice warrant fires on (LLM-116/LLM-128). The golden pins the " +
			"'## Time to produce' CHOOSE menu — each makeable good with its per-unit time, stock vs cap, and weekly made/sold " +
			"counts — under the 'Choose what to produce next' header, plus the 'decide what to make next' wake warrant. With no " +
			"focus set, the steer cue and the standing 'You are making nail.' line do NOT render here (see " +
			"smith_forging_focused). A single-output producer never gets this section (see " +
			"TestForgeCueOnlyForMultiOutputCrafterAtForge).",
		build: smithChoosingAtForge,
	},
	{
		name: "smith_forging_focused",
		summary: "The same multi-output crafter (Ezekiel) at his forge WITH a productive focus already set (nail, below " +
			"cap) and no production-choice warrant — the steady state after he has chosen (LLM-128). The golden pins the " +
			"focus-aware cue: the '## Time to produce' section leads with 'You are producing nails now — tend your post or call " +
			"done()' INSTEAD of the choose menu, so the weak model isn't re-invited to pick what it is already forging. The " +
			"standing 'You are making nail.' self-state line renders too. Pairs with smith_choosing_at_forge (unfocused -> " +
			"menu) to pin both halves of the cue.",
		build: smithForgingFocused,
	},
	{
		name: "smith_off_work_focus_hidden",
		summary: "The same multi-output crafter (Ezekiel, focus still nail) is NOT at his forge — he is at the Tavern " +
			"after his shift (the live Tavern bug, LLM-121). produce_tick makes nothing away from the workplace, so the " +
			"standing 'You are making nail.' self-state line must NOT render here, and the '## Time to produce' cue is " +
			"likewise absent. The golden pins that neither leaks into an off-work turn; a regression to the work-structure " +
			"gate would make the line reappear in the diff (see TestProductionFocusLineOnlyAtWork).",
		build: smithOffWorkFocusHidden,
	},
	{
		name: "smith_bartering_at_tavern",
		summary: "A smith (Ezekiel) carrying his own wares stands in the Tavern in company with John Ellis the " +
			"tavernkeeper — the live LLM-125 barter scene. Off shift and away from the forge, so neither '## Time to " +
			"produce' nor the 'You are making nail.' line render; the new '## What your wares fetch' cue DOES, valuing his " +
			"own-trade goods (nail 1-2, skillet 5-10 from the recipe wholesale-retail spread) so a barter has a coin " +
			"yardstick instead of an invented number. No coin sales history yet (empty PriceBook), so no recent-price clause.",
		build: smithBarteringAtTavern,
	},
	{
		name: "keeper_reselling_in_company",
		summary: "A pure RESELLER — Josiah Thorne, general-store keeper, all-`buy` restock (cheese, milk), produces " +
			"nothing — stands in his store in company with a customer holding bought-in stock. LLM-191: his empty " +
			"ProduceEntries() left the '## What your wares fetch' cue blank before, so he named prices with no anchor and " +
			"never reliably moved stock (live: 0 coins, empty sell-through). The golden pins the cue now valuing his resold " +
			"goods from the recipe wholesale-retail spread AND surfacing his own recent purchase cost ('you have lately " +
			"paid about N each for it') from the buyer-side PriceBook — the cost basis to mark up from. No sale history " +
			"yet, so no 'sold for' clause. Pairs with smith_bartering_at_tavern (the producer leg) to pin both halves.",
		build: keeperResellingInCompany,
	},
	{
		name: "innkeeper_pricing_with_makings_cost",
		summary: "A PRODUCER whose recipe has real inputs — Hannah Boggs keeping her inn in company with a guest, " +
			"porridge made 10 bowls at a time from 3 milk + 5 water (the live catalog shape). LLM-226: the wares-worth " +
			"cue previously gave a producer no cost anchor, so she could price below cost unknowing (live: 1-coin " +
			"porridge against an 0.8-coin makings cost). The golden pins the makings clause: inputs priced from catalog " +
			"wholesale with no purchase history (8 coins a batch), spoken per-unit as 'nearly 1 coin each' — the engine " +
			"does the division and rounds the prose UP, never down to a break-even-erasing 'about 1'. Stated as a fact " +
			"with no pricing directive (LLM-227) — the NPC decides what to do with its cost. Pairs with " +
			"keeper_reselling_in_company (the resale cost basis) and smith_bartering_at_tavern (the no-inputs producer, " +
			"no makings clause).",
		build: innkeeperPricingWithMakingsCost,
	},
	{
		name: "keeper_not_pitching_makers_own_ware",
		summary: "LLM-171 seller side: John Ellis keeps his tavern in company with Ezekiel Crane the smith, and John's " +
			"stock holds skillet + nail he bought FROM Ezekiel. The '## Custom at hand' cue lists those wares to pitch, so " +
			"the golden pins the producer-note line that steers the keeper off selling a smith his own ware back (the live " +
			"buy-back: John read Ezekiel's sell-offer as a buy and quoted skillets at him). A customer who makes none of " +
			"the goods draws no note (see TestProducerPitchNoteOnlyForCoPresentMaker).",
		build: keeperNotPitchingMakersOwnWare,
	},
	{
		name: "maker_offered_own_ware_buy_quote",
		summary: "LLM-171 buyer side: Ezekiel Crane (skillet at his cap of 5, which he makes) has a targeted skillet " +
			"quote posted at him by John Ellis for 2 coins — the mis-pitched buy-back quote from the live trace. The " +
			"golden pins that the quote warrant line WITHHOLDS the 'pay_with_item with quote_id' take and steers 'these " +
			"are wares you make yourself … decline' instead, so a mis-pitched quote can't close the buy-back loop. A " +
			"quote for a good the buyer doesn't make keeps its take (see TestBuyBackQuoteSteerOnlyForOwnProducedOrAtCap).",
		build: makerOfferedOwnWareBuyQuote,
	},
	{
		name: "buyer_offered_quote_take_names_terms",
		summary: "LLM-172 buyer side: John Ellis posts a targeted STEW quote (qty 1, 4 coins) at Ezekiel Crane — a good he " +
			"buys, not makes — so the actionable take RENDERS (unlike the maker buy-back above). Ezekiel carries 20 nails, " +
			"the live trap: the prior take said 'pay_with_item with quote_id 1 and the same item, qty, and amount', and a " +
			"buyer holding other goods bound 'the same item' to a nail, dead-ended on the term-mismatch reject, and fell " +
			"back to a bare pay that leaked coins for an undelivered stew (the quote still open). The golden pins that the " +
			"take now names the concrete 'item \"stew\", qty 1, and amount 4' so there is nothing to misbind. Only golden " +
			"exercising the single-line coin-quote actionable take (see TestCoinQuoteTakeNamesConcreteTerms).",
		build: buyerOfferedQuoteTakeNamesTerms,
	},
	{
		name: "dairy_choosing_at_farm",
		summary: "LLM-144: a NON-smith multi-output producer (Elizabeth Ellis at Ellis Farm: milk + meat + cheese) stands " +
			"UNFOCUSED at her own workplace on shift — the same production-choice state smith_choosing_at_forge pins for the " +
			"blacksmith, but for a dairy/farm trade. The golden proves the cue and wake warrant render trade-neutrally: the " +
			"'## Time to produce' header, the 'Choose what to produce next' menu, and the 'It's time to produce — decide what to " +
			"make next' warrant — NOT the blacksmith-only 'forge' wording a dairywoman was wrongly shown (the live Elizabeth " +
			"cheese scene 019f0969). Mirrors smithChoosingAtForge; byte-stable.",
		build: dairyChoosingAtFarm,
	},
	{
		name: "keeper_offers_room_to_coinless_guest",
		summary: "John Ellis the tavernkeeper shares his tavern (one free private room at a live nightly rate) with " +
			"Ezekiel Crane, a homeless smith with no home, no lodging grant, and 0 coins, carrying his own wares. The " +
			"'## A room to let' cue fires and now names the goods-for-room path (LLM-136): a coinless guest is offered " +
			"the room for goods (offer_trade → accept_pay) rather than dead-ended on coins. Keeper side of the live livelock.",
		build: keeperOffersRoomToCoinlessGuest,
	},
	{
		name: "homed_guest_lodging_quote_suppressed",
		summary: "LLM-208 buyer side: John Ellis posts a targeted nights_stay (room) quote at Prudence Ward, but Prudence " +
			"HAS a home (Ward Residence) — she structurally can't take a room (the buyer-side pay_with_item guard rejects " +
			"it, LLM-182). The golden pins that the room-offer take is SUPPRESSED for her: filterHomedLodgingQuoteWarrants " +
			"drops the lodging quote warrant at build, so the prompt carries no 'offers you … nights_stay' take line and she " +
			"isn't pulled into a doomed nightly negotiation (the live John↔Prudence tavern loop). Contrast " +
			"keeper_offers_room_to_coinless_guest (a HOMELESS seeker, who correctly DOES get offered the room).",
		build: homedGuestLodgingQuoteSuppressed,
	},
	{
		name: "peers_holding_same_food_no_degenerate_buy",
		summary: "Two hungry NPCs stand together, each already carrying the same food (stew) — the LLM-138 " +
			"degenerate-buy shape from live hud-6a887a…, where each was told ONLY to BUY the other's blueberries " +
			"(the cue that drove the hollow 'I can offer thee blueberries' beats backed by no transaction). The golden " +
			"pins that the '## What you can eat or drink' section shows the subject its OWN stew to consume but carries " +
			"NO 'offer to buy it from them' peer line — buying a copy of food already in hand is pointless " +
			"(gatherCoPresentPeerOffers gate). A regression would make the buy line reappear in the diff.",
		build: peersHoldingSameFood,
	},
	{
		name: "coinless_worker_among_peers",
		summary: "Two laborers stand together in the commons and the one we render (Goodwife Bishop, a newcomer) has " +
			"an empty purse — the LLM-153 situation, where 0-coin workers tried to BUY services from each other. The pay " +
			"path rejects a coinless buy, but the model kept attempting it because '## You' showed 'Coins in your purse: 0' " +
			"with no consequence. The golden pins the empty-purse line now spelling out that the actor cannot pay until it " +
			"earns coin — coin-specific wording so barter (offer_trade) is left untouched.",
		build: coinlessWorkerAmongPeers,
	},
	{
		name: "broke_employer_cannot_pay_labor_offer",
		summary: "A worker (Lewis Walker) has solicited the subject (Ezekiel Crane) for a 5-coin job, but the subject " +
			"has an empty purse — the LLM-158 situation. accept_work's funds gate (buyerCanAfford) would only flip the " +
			"offer to failed_unavailable, so the model 'accepts' verbally and the deal dies in silence (the live ~10-min " +
			"Lewis<->Ezekiel blacksmith dead-air). The golden pins the affordability steer: the unaffordable offer is " +
			"directed to decline_work WITH an explicit speak, and the generic accept_work/decline_work footer is suppressed " +
			"because no offer is affordable.",
		build: brokeEmployerCannotPayLaborOffer,
	},
	{
		name: "worker_en_route_to_workplace",
		summary: "LLM-229: a worker (Patience Walker) has accepted a job for Josiah Thorne struck away from his store " +
			"and is now relocating to his workplace — she is NOT yet laboring, so no coins/boost accrue and she must not " +
			"statue where the deal was struck. The golden pins the relocation self-state ('You've taken on a job for Josiah " +
			"Thorne — make your way to their workplace and get to work…'), and — because she already holds a committed job — " +
			"the absence of both the solicit affordance and the businesses directory. The matrix-wide guard is " +
			"TestGoldensEnRouteWorkerNotOfferedNewWork.",
		build: workerEnRouteToWorkplace,
	},
	{
		name: "labor_offer_in_kind_reward",
		summary: "A worker (Anne Walker) has solicited the subject (Hannah Boggs) for a job paid in kind — 1 porridge " +
			"plus 2 coins — and the subject holds both legs. The LLM-225 situation: spoken in-kind hire terms ('a bowl " +
			"of porridge for some help') are now real contract terms, not talk that evaporates at commit (the live " +
			"Hannah Boggs Inn hires, where the workers ended up buying the promised porridge with their own coins). The " +
			"golden pins the decision line naming BOTH reward legs via the payment phrase, and the normal " +
			"accept_work/decline_work footer (the offer is affordable — the employer holds the porridge and the coins).",
		build: laborOfferInKindReward,
	},
	{
		name: "employer_missing_reward_items_steer",
		summary: "The same in-kind solicitation, but the subject does NOT hold the asked porridge (coins are ample) — " +
			"the goods-leg half of the LLM-158 affordability steer (LLM-225). accept_work's gate 8 now spans both legs " +
			"(employerCanCoverLaborReward), so an accept would only flip the offer to failed_unavailable and the deal " +
			"would die in silence. The golden pins the missing-goods decline steer ('You do not hold the 1 porridge " +
			"they ask to be paid in') and the suppressed footer (no affordable offer remains).",
		build: employerMissingRewardItemsSteer,
	},
	{
		name: "worker_among_household_no_solicit",
		summary: "Two worker-tagged Walker siblings (Lewis + Anne) stand together in their own home, both jobless — the " +
			"LLM-157 situation, where housemates solicited each other for work ('I'm looking for work, does anyone need a " +
			"hand?'). LLM-145 already hides the solicit_work tool among kin, but the seek-work backstop warrant still made " +
			"the model ask the housemate as freeform speech. The golden pins the '## Around you' annotation that now names " +
			"Anne as the subject's housemate, so the worker reads her as kin rather than a work prospect and steers to a " +
			"real employer instead. A non-kin co-present worker would carry no such annotation.",
		build: workerAmongHousehold,
	},
	{
		name: "owner_at_worn_stall",
		summary: "A stall owner (Ezekiel) stands at his own worn market stall (wear past the repair threshold, " +
			"below degrade) carrying too few nails to mend it. The golden pins the '## Your stall' cue: the worn-boards " +
			"problem AND the buy-nails-from-the-smith steer in one line (symmetrical awareness, LLM-118). The repair tool " +
			"rides the same StallRepair signal (handlers gating test).",
		build: ownerAtWornStall,
	},
	{
		name: "owner_at_degraded_stall",
		summary: "A stall owner stands at his own DEGRADED stall (wear past the degrade threshold — closed for trade), " +
			"carrying enough nails. The golden pins the escalated '## Your stall' steer ('too worn to trade … repair it " +
			"now') — the seller-facing half of the degrade sales-block (LLM-118).",
		build: ownerAtDegradedStall,
	},
	{
		name: "passerby_at_worn_stall",
		summary: "A non-owner (John) stands at someone else's worn market stall. The golden pins the co-present " +
			"atmosphere line ('The market stall here looks worn…') and the ABSENCE of the owner '## Your stall' cue — a " +
			"passerby can remark on the wear but isn't handed the repair (LLM-118).",
		build: passerbyAtWornStall,
	},
	{
		name: "farm_owner_owes_upkeep",
		summary: "A farm owner (Elizabeth Ellis) with 95 coins (floor 30, band 20 → owes 3 upkeep shovels) and none in " +
			"hand. The golden pins the '## Farm upkeep' cue: the worn-tools problem AND the buy-N-shovels-from-the-blacksmith " +
			"steer in one line (the farm wealth tax, LLM-215). Stock-based — derived from coins, not a per-object meter — and " +
			"not co-location-gated, so it rides any tick.",
		build: farmOwnerOwesUpkeep,
	},
	{
		name: "keeper_at_post_onshift",
		summary: "A keeper (shopkeeper) stands at his own store during business hours. The golden pins the " +
			"'How you trade:' trade-conduct block — the positive case for the operating-hours gate (LLM-123). On shift " +
			"and at-post, the keeper is open for trade, so the cue renders.",
		build: keeperAtPostOnShift,
	},
	{
		name: "keeper_at_closed_post_offshift_night",
		summary: "The same keeper stands at his own CLOSED store late at night, off shift (the LLM-123 bug shape: " +
			"Ezekiel told to 'tend to your trade' at midnight). The golden pins that the 'How you trade:' block is ABSENT " +
			"after hours — the off-shift work-pressure that fought his needs-pull and drove the post<->Tavern oscillation " +
			"is gone — while the off-shift wind-down steer (head home) renders instead. A regression to the operating-hours " +
			"gate would make the trade block reappear in the diff.",
		build: keeperAtClosedPostOffshiftNight,
	},
	{
		name: "keeper_staying_open_offshift",
		summary: "The same keeper, off shift at night, but holding a live stay_open commitment (committed to keep the " +
			"store open past close). The golden pins that the 'How you trade:' block renders despite being off-shift — the " +
			"operating-hours gate (LLM-123) opens on a stay_open commitment too, so a keeper working late by choice still " +
			"gets the trade-conduct framing, and the routine wind-down is suppressed.",
		build: keeperStayingOpenOffshift,
	},
	{
		name: "lodger_renewal_due_in_conversation",
		summary: "Renewal-due lodger (Ezekiel Crane, 0 coins, room at the Tavern nearly up) mid-conversation with an " +
			"awake huddle peer — the live incident where the renewal walk-pull dragged him out of a PC exchange. Gate 1 " +
			"(LLM-127): the golden pins that NO '## Your lodging' section renders, so rent math never interrupts a live " +
			"social beat.",
		build: lodgerRenewalDueInConversation,
	},
	{
		name: "lodger_renewal_due_onshift_away",
		summary: "The same renewal-due lodger, on-shift and away from his inn, not in conversation. Gate 3 (LLM-127): the " +
			"golden pins the deferred headline ('see the keeper to renew when you are next back at the inn') — no walk-pull " +
			"off his post — plus the rate hint and the earn cue (he's broke). The abed-keeper note is absent (deferral makes " +
			"it redundant).",
		build: lodgerRenewalDueOnShiftAway,
	},
	{
		name: "lodger_renewal_due_offshift",
		summary: "The same renewal-due lodger, off-shift and away from the inn, not in conversation — the case where the " +
			"renewal IS actionable now. The golden pins the active walk-pull ('if you wish to stay on, see the keeper to " +
			"renew') plus the rate hint and earn cue: the positive baseline the two suppression gates are measured against.",
		build: lodgerRenewalDueOffShift,
	},
	{
		name: "lodger_renewal_due_desk_remembered_shut",
		summary: "The same renewal-due lodger, off-shift and away from the inn (so the walk-pull is actionable), but he went " +
			"to the Tavern within the decay window and found the keeper's desk shut (LLM-126). The golden pins the experiential " +
			"wait-steer ('you stopped by not long ago and found the keeper's desk shut — best wait until they are tending it') " +
			"in place of the retired omniscient 'the keeper is abed just now' read: the lodger only knows the desk was shut " +
			"because it was actually there, and the memory decays on the 4h closed-business TTL.",
		build: lodgerRenewalDueDeskRememberedShut,
	},
	{
		name: "buyer_remembers_vendor_shut",
		summary: "A hungry forager (Ezekiel) stands near a cheese seller at the General Store, but he went there within the " +
			"decay window and found it shut — no keeper tending it (now including an abed keeper, since the capture gates on " +
			"availability, LLM-126). The golden pins that the '## What you can eat or drink' buy cue carries the experiential " +
			"'found it shut up' annotation — the only path to a closed cue now that the omniscient live-asleep '(currently " +
			"closed)' marker is retired. The seller is present and awake; the cue is driven by his memory, not her state.",
		build: buyerRemembersVendorShut,
	},
	{
		name: "producer_hungry_mild_at_post",
		summary: "A farmer (Moses James) stands at his own farm on shift, only MILDLY hungry (felt, below the red " +
			"threshold), carrying nothing edible but the carrots he grows to sell (the live grazing case, LLM-134). The " +
			"golden pins that the '## What you can eat or drink' own-stock 'consume to eat' cue is ABSENT — his own trade " +
			"stock is demoted out of the personal eat menu below desperation, so he isn't nudged to graze the merchandise. " +
			"Pairs with producer_starving_at_post (same farmer, red hunger -> the cue returns).",
		build: producerHungryMildAtPost,
	},
	{
		name: "producer_starving_at_post",
		summary: "The same farmer (Moses) at the same farm, now at the red/distress hunger tier with the same carrots and " +
			"no other food (LLM-134). The golden pins that the own-stock 'consume to eat' cue DOES surface his carrots — at " +
			"desperation the trade stock returns as the last resort the own-stock line was built to be (the ZBBS-123 don't-" +
			"starve-next-to-your-food safety net). Pairs with producer_hungry_mild_at_post.",
		build: producerStarvingAtPost,
	},
	{
		name: "broke_worker_no_employer_seeks_work",
		summary: "A broke worker (Lewis Walker, a salem-vendor) idle at home with no employer present — the live LLM-160 " +
			"case. The golden pins the make-it-move fix: the businesses directory renders as a STANDING cue (the town's " +
			"businesses by their resolvable structure names) even with no seek-work warrant, so move_to has a real target " +
			"instead of an invented place ('the market', 'the Well') that bounces; and the triage coda is the decisive " +
			"'call move_to now' go-line, not the default act-now/await-reply coda the agree-loop fed on. A regression to the " +
			"warrant gate would drop the directory line, and a regression to the coda swap would bring back 'Choose one action'.",
		build: brokeWorkerNoEmployerSeeksWork,
	},
	{
		name: "broke_worker_seeks_work_skips_shut_business",
		summary: "The LLM-155 companion to broke_worker_no_employer_seeks_work: the same broke idle worker (Lewis Walker), but he " +
			"remembers finding the Inn shut an hour ago (an earned ObservedClosed memory within the 4h TTL). The golden pins that the " +
			"seek-work directory DROPS the remembered-shut Inn entirely — not annotates it — and lists only the open General Store, " +
			"carrying its qualitative distance + direction ('a short walk east'). A regression that stopped consulting the shut " +
			"memory would re-list the Inn; one that dropped distance would lose the walk descriptor.",
		build: brokeWorkerSeeksWorkSkipsShutBusiness,
	},
	{
		name: "worker_with_coin_no_employer_seeks_work",
		summary: "The LLM-168 live case: a WORKLESS worker (Silence Walker — worker attribute, no work_structure_id) idle at " +
			"home holding a few coins, no employer present. Under the old broke (Coins==0) gate she got no directory and no " +
			"seek-work warrant, so the brand-new Walker family idled all shift inventing move_to destinations. LLM-168 re-" +
			"anchored eligibility on workless, so the same standing businesses directory + decisive 'call move_to now' coda " +
			"fire whether or not she holds coin. The golden pins that a coin-holding workless worker gets the identical leave-" +
			"for-a-business directive as the broke one; a regression to the Coins==0 gate would drop the directory + go-coda here.",
		build: workerWithCoinNoEmployerSeeksWork,
	},
	{
		name: "comfortable_worker_no_seek_work",
		summary: "The LLM-194 case: the same workless Silence Walker as worker_with_coin_no_employer_seeks_work, but holding " +
			"coin AT/ABOVE the seek-work ceiling (40 >= the default 25). A coin-rich worker is 'comfortable' — it doesn't need " +
			"odd jobs — so the golden pins that it gets NEITHER the businesses directory NOR the 'call move_to now' go-coda: " +
			"it's left to idle and drain its purse via ordinary consumption instead of pestering keepers for work. The negative " +
			"counterpart of the 15-coin scenario (which still seeks); a regression that dropped the coin ceiling would re-add " +
			"the seek-work cue here and flip TestSeekWorkDirectiveOnlyForWorklessWorker.",
		build: comfortableWorkerNoSeekWork,
	},
	{
		name: "worker_seeks_work_after_employer_declines",
		summary: "The LLM-181 live case (Lewis Walker at the General Store, hud-8db08741…), reduced: a workless worker shares a " +
			"huddle with a co-present stranger employer (Josiah Thorne) who has ALREADY declined his labor offer. Pre-fix, the " +
			"co-present employer kept hasSolicitableAudience true, which suppressed SeekWorkPlaces and the seek-work off-ramp — so " +
			"the worker re-soliciting the same refusal was never told to leave. LLM-181 drops a declined employer from the " +
			"solicitable audience, so the standing businesses directory + decisive 'call move_to now' go-coda arm DESPITE the " +
			"employer being present, and no solicit_work affordance is offered for the refuser. A regression that forgot the " +
			"decline would re-suppress the directory and bring back the solicit cue against Josiah.",
		build: workerSeeksWorkAfterEmployerDeclines,
	},
	{
		name: "worker_seeks_work_skips_no_hiring_business",
		summary: "The LLM-210 companion to broke_worker_seeks_work_skips_shut_business: the same workless idle worker (Lewis " +
			"Walker), but he last found the General Store's keeper on a break — an earned ObservedNoHiring memory within its 2h " +
			"TTL — where the keeper was PRESENT (so the store is NOT remembered shut) yet could not take him on. The golden pins " +
			"that the seek-work directory DROPS the no-hiring General Store entirely and lists only the open Blacksmith, carrying " +
			"its distance + direction, so he is steered to a business with an available keeper. A regression that stopped " +
			"consulting the no-hiring memory would re-list the General Store and re-strand him on the resting-keeper loop that " +
			"ObservedClosed (sleeping only) and ObservedDeclinedWork (a refusal) both miss.",
		build: workerSeeksWorkSkipsNoHiringBusiness,
	},
	{
		name: "red_tired_worker_no_seek_work",
		summary: "The LLM-210 case: a WORKLESS worker (Lewis Walker) idle at home holding a few coins (15, below the seek-work " +
			"ceiling → not comfortable) but at RED tiredness (20 >= the default red-line 16). A pressing need outranks job-" +
			"hunting, so the golden pins that he gets NEITHER the businesses directory NOR the 'call move_to now' go-coda — both " +
			"seek-work gates suppress and the already-present weariness cue is left to win, so he rests on his own rather than " +
			"pacing to a shop while exhausted (the live home<->store loop). The rested counterpart is " +
			"worker_with_coin_no_employer_seeks_work (same workless coin-holder, not red → still seeks). A regression that dropped " +
			"the hasRedNeed gate would re-add the directory + go-coda here and flip TestSeekWorkSuppressedByRedNeed.",
		build: redTiredWorkerNoSeekWork,
	},
	{
		name: "customer_at_shut_business_loitering",
		summary: "A laborer (Goodman Silence) stands OUTDOORS at the Tavern's loiter slot, but its only keeper (John Ellis) " +
			"is asleep inside — the live LLM-154 case (Silence stuck at the closed Tavern while seeking work). The golden pins " +
			"the at-location dead-end clause 'The Tavern is shut — no one is tending it.' next to the 'outdoors by the Tavern' " +
			"location line: a live, situated read (the keeper is abed, so the place reads shut) distinct from the ObservedClosed " +
			"memory, so a weak model isn't left to infer 'closed' from 'the keeper is asleep'.",
		build: customerAtShutBusinessLoitering,
	},
	{
		name: "customer_at_shut_business_inside",
		summary: "The same laborer, now standing INSIDE the Tavern's interior with the keeper asleep there (LLM-154). The " +
			"golden pins that the shut clause fires on the interior placement too — keyed on the current location whether the " +
			"actor entered or is loitering at the slot — and that the abed keeper is named as a co-present sleeper (visible but " +
			"not addressable).",
		build: customerAtShutBusinessInside,
	},
	{
		name: "customer_at_open_business",
		summary: "The positive control for LLM-154: the same laborer outdoors at the Tavern's loiter slot, but the keeper is " +
			"awake and present inside. The golden pins that NO shut clause renders — an awake, present keeper means the business " +
			"is tended, so the live read stays silent (render the situation, not omniscient).",
		build: customerAtOpenBusiness,
	},
	{
		name: "huddle_conversation_looping",
		summary: "Two idle workers (the Walker sisters) stand together going in circles — the live LLM-169 case: " +
			"Patience and Anne re-echo 'Let's go to the well!' / 'Let's go!' without it converting to a move. The huddle " +
			"is in an armed conversational loop (ActorSnapshot.ConversationLooping — the same huddleLoopArmed signal the " +
			"loop sweep arms on, surfaced per-tick), and Anne holds a live await edge to Patience. The golden pins the " +
			"LLM-169 swap: the 'Anne Walker is waiting for your reply.' nag is SUPPRESSED (that nag is what manufactures " +
			"the echo) and the coda is the 'you've agreed — act now or done()' loop steer, NOT the default/awaiting coda " +
			"that fed the agree-loop. A regression that dropped the flag would bring back the nag and the 'Choose one action' coda.",
		build: huddleConversationLoopingScenario,
	},
	{
		name: "hungry_looper_at_foodless_home",
		summary: "The live LLM-176 case: hungry Walker sisters loop in a huddle at their foodless residence, " +
			"confabulating 'food in the kitchen' instead of walking to a real source. Patience (the subject) is in an " +
			"armed conversational loop, feels hunger, holds nothing edible, has 1 coin, and a free Raspberry Bush sits a " +
			"walk away. The golden pins BOTH LLM-176 cues: the at-location dead-end ('There's no food to be had here — " +
			"you'll need to forage or buy a meal elsewhere.') that kills the confabulation, and the need-redirect coda " +
			"(swapping the generic 'do what you've agreed' line for 'go to Raspberry Bush (structure_id: …) now and eat') " +
			"that names the engine's known affordance. A regression that dropped either would bring back the silent dead " +
			"end or the plan-endorsing generic coda.",
		build: hungryLooperAtFoodlessHome,
	},
	{
		name: "undirected_reask_sole_peer",
		summary: "The live LLM-232 case: John Ellis floated a plain, unaddressed trade proposal to the only other " +
			"person in his huddle (Anne Walker) and she has said nothing back. Because the ask named no addressee it opened " +
			"no WORK-370 edge, and John's own last line is ~75s old — past the 60s directed-edge window (so even a directed " +
			"edge would have lapsed) but well inside ReaskSuppressWindow. The golden pins the LLM-232 anchor: the " +
			"sole-awake-peer condition folds the peer into " +
			"AwaitingReplyFrom, so the 'You already spoke to the villager and are waiting for their reply. Do not repeat " +
			"yourself…' line renders (name acquaintance-gated to 'the villager' here) and the coda swaps to the " +
			"awaiting-reply wait-framing — the cross-tick memory an " +
			"undirected re-ask storm otherwise lacks. A regression that dropped the anchor would leave no wait line and " +
			"re-open the re-pitch loop.",
		build: undirectedReaskSolePeerScenario,
	},
	{
		name: "hungry_actor_holding_raw_meat",
		summary: "A hungry shopkeeper (Josiah Thorne) at his post carries raw Meat — a stew INGREDIENT (food-category but " +
			"eases no need raw) — alongside edible Cheese (the live LLM-166 case: he fired consume{Meat} 22 times). The golden " +
			"pins the use annotation folded into the carry readout, 'Meat (x7, used to produce stew)', while Cheese stays bare " +
			"(the satiation cue owns edibles). A regression that dropped the annotation would let the most food-like name in a " +
			"flat inventory read as a meal again.",
		build: hungryActorHoldingRawMeat,
	},
	{
		name: "seller_with_taken_quote_at_post",
		summary: "A vendor (Prudence Ward) at her post has just SOLD one lot — its quote is now " +
			"SceneQuoteStateTaken — while a second lot stays on offer (the live LLM-189 case). The golden pins that " +
			"'## Offers you've put out' lists ONLY the still-active lot (raspberries); the taken lot (blueberries) is " +
			"gone, not shown as 'they have yet to answer'. Reverting the close-on-take fix would make the sold lot " +
			"reappear in the diff — the phantom standing offer that lured the live seller into firing pay_with_item at " +
			"her own buyer. The reverse-pay dispatch gate itself is pinned by the sim-package handler tests.",
		build: sellerWithTakenQuoteAtPost,
	},
	{
		name: "buyer_kept_consume_remainder_reconciled",
		summary: "A buyer (Anne Walker) just took a consume_now quote for 5 blueberries, but her low hunger meant the " +
			"needs-clamp ate only 1 and pocketed 4 (the live LLM-188 case). The golden pins that '## Recently settled " +
			"offers' reconciles the split — 'you ate 1 on the spot and kept the other 4' — so it agrees with the carried " +
			"Blueberries (x4) readout instead of claiming all 5 were had right away. The bare 'had it right away' line " +
			"contradicted the inventory and drove both NPCs to confabulate a missing-blueberry short-count; a regression " +
			"that dropped the reconciliation would resurface that contradiction in the diff.",
		build: buyerKeptConsumeRemainderReconciled,
	},
	{
		name: "employer_with_worker_on_job",
		summary: "An employer (John Ellis the tavernkeeper) stands with a worker (Silence Walker) who is mid-contract " +
			"for him — a Working labor offer, ~90 minutes left (the live LLM-202 case). The golden pins the new " +
			"'## Workers currently working for you' employer-side cue ('Silence Walker is working a job for you — about " +
			"1 hour 30 minutes left; 2 coins owed when it's done') plus the shared 'already covered … don't hire someone " +
			"else for it or pay again by hand' steer. Without it the employer saw only the pending-offer decision view and " +
			"re-hired a second worker for the same job. The worker's own '## Work offers awaiting your decision' is ABSENT " +
			"here (the offer is Working, not Pending). A regression that dropped the cue resurfaces the blind re-hire in the diff.",
		build: employerWithWorkerOnJob,
	},
	{
		name: "broke_keeper_shut_and_unaffordable_suppliers_no_restock",
		summary: "LLM-216, the live Josiah Thorne case: a broke (0 coins) general-store keeper whose bought-in carrots " +
			"and milk are both empty stands alone at his store on shift. His carrot supplier (James Farm) he remembers " +
			"finding SHUT; his milk supplier (Ellis Farm) is open but its remembered price (4 coins) is beyond his empty " +
			"purse. Before the fix the '## Restocking' cue handed him BOTH farms as move_to targets — annotating James " +
			"'found it shut up' yet still steering there, and listing an Ellis he couldn't pay — and he toured them every " +
			"tick instead of tending his shop and earning. The golden pins that NO '## Restocking' section renders: the " +
			"shut supplier is dropped and the unaffordable one is dropped, so with no actionable buy path both items are " +
			"omitted. The matrix-wide guard is TestGoldensRestockNeverTargetsRememberedShutSupplier.",
		build: brokeKeeperShutAndUnaffordableSuppliersNoRestock,
	},
	{
		name: "keeper_restock_drops_shut_keeps_open_supplier",
		summary: "LLM-216 shut-drop, section-present half: a general-store keeper with coin (30) is low on carrots and has " +
			"TWO carrot suppliers — Bell Farm (open, ~3 coins, affordable) and James Farm (remembered SHUT). The golden pins " +
			"that the '## Restocking' cue renders and lists ONLY Bell Farm as the move_to target: the shut James Farm is " +
			"dropped (not annotated 'found it shut up' as before), so the keeper is never routed to the dead end while a live " +
			"supplier is available. Makes TestGoldensRestockNeverTargetsRememberedShutSupplier non-vacuous (a rendered restock " +
			"section with a remembered-shut structure in the fixture). Pairs with " +
			"broke_keeper_shut_and_unaffordable_suppliers_no_restock (the whole-section suppression half).",
		build: keeperRestockDropsShutKeepsOpenSupplier,
	},
	{
		name: "reseller_restock_routed_to_distributor_not_farm",
		summary: "LLM-223 farm wholesale tier: a non-distributor reseller (Hannah Boggs, the innkeeper) is low on milk and " +
			"has two milk suppliers — Ellis Farm (farm-tagged) and Josiah's General Store (the distributor). The golden pins " +
			"that the '## Restocking' cue lists ONLY the General Store as the walk-to target: the farm is dropped for every " +
			"non-distributor buyer (farm-origin goods route through the distributor), so Hannah is never sent straight to the " +
			"farm the PayWithItem backstop would refuse. Keeps TestGoldensNonDistributorRestockNeverTargetsFarm non-vacuous " +
			"(a rendered restock section with a farm-tagged supplier in the fixture).",
		build: resellerRestockRoutedToDistributorNotFarm,
	},
}

// brokeKeeperShutAndUnaffordableSuppliersNoRestock is the LLM-216 live fixture:
// Josiah Thorne, a broke (0 coins) general-store keeper with empty carrot and milk
// stock, stands alone at his store on shift. His only carrot supplier (James Farm)
// he remembers finding shut; his only milk supplier (Ellis Farm) is open but its
// remembered price (4 coins) is beyond his empty purse. Both suppliers are present
// as resolvable vendor structures — so WITHOUT the LLM-216 drops the restock cue
// would list both as move_to targets (the every-tick tour). With them, the shut
// James Farm and the unaffordable Ellis Farm are both dropped, and an item with no
// actionable buy path (no surviving walk-to supplier, no co-present seller) is
// omitted — so the golden carries no "## Restocking" section at all. Clock-free: the
// shut memory and the price history are stamped relative to PublishedAt, and the
// render path reads no wall clock.
func brokeKeeperShutAndUnaffordableSuppliersNoRestock() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID  = sim.ActorID("josiah")
		jamesID   = sim.ActorID("james")
		ellisID   = sim.ActorID("ellis")
		store     = sim.StructureID("general_store")
		jamesFarm = sim.StructureID("james_farm")
		ellisFarm = sim.StructureID("ellis_farm")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the store
	published := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   store,
		InsideStructureID: store,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"carrots": 0, "milk": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceBuy, Max: 12},
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
		// He went to James Farm and found it shut; Ellis Farm he has no shut memory of.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: jamesFarm, Condition: sim.ObservedClosed}: published.Add(-time.Hour),
		}),
	}
	james := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "James Fuller",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: jamesFarm,
		Inventory:       map[sim.ItemKind]int{"carrots": 40},
	}
	ellis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Ellis Ward",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 420, Y: 420},
		WorkStructureID: ellisFarm,
		Inventory:       map[sim.ItemKind]int{"milk": 40},
	}
	// Josiah's buyer-side price history: 6 coins/carrot from James, 4 coins/milk from
	// Ellis — both beyond his empty purse (the affordability drop), and James is shut
	// on top of that.
	carrotBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	carrotBuys.Push(sim.PriceObservation{BuyerID: josiahID, Amount: 6, Qty: 1, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	milkBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	milkBuys.Push(sim.PriceObservation{BuyerID: josiahID, Amount: 4, Qty: 1, Consumers: 1, At: published.Add(-1 * 24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josiahID: josiah, jamesID: james, ellisID: ellis,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			store:     plainStructure(store, "General Store"),
			jamesFarm: plainStructure(jamesFarm, "James Farm"),
			ellisFarm: plainStructure(ellisFarm, "Ellis Farm"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"carrots": {Name: "carrots", DisplayLabel: "carrots", Category: sim.ItemCategoryFood},
			"milk":    {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: jamesID, Item: "carrots"}: carrotBuys,
			{SellerID: ellisID, Item: "milk"}:    milkBuys,
		},
	}
	return snap, josiahID, nil
}

// keeperRestockDropsShutKeepsOpenSupplier is the LLM-216 section-present fixture: a
// coin-holding keeper (Thomas Bishop, 30 coins) is low on carrots and has two carrot
// suppliers — Bell Farm (open, remembered price ~3 coins, affordable) and James Farm
// (remembered shut). With the shut James Farm dropped and the affordable Bell Farm
// kept, the "## Restocking" cue renders and lists ONLY Bell Farm as the walk-to
// target — the visible half of the shut-drop, and the fixture that keeps
// TestGoldensRestockNeverTargetsRememberedShutSupplier non-vacuous (a rendered restock
// section carrying a remembered-shut structure). Clock-free: the shut memory and price
// history are stamped relative to PublishedAt.
func keeperRestockDropsShutKeepsOpenSupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		thomasID  = sim.ActorID("thomas")
		bellID    = sim.ActorID("bell")
		jamesID   = sim.ActorID("james")
		store     = sim.StructureID("general_store")
		bellFarm  = sim.StructureID("bell_farm")
		jamesFarm = sim.StructureID("james_farm")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the store
	published := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	thomas := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Thomas Bishop",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   store,
		InsideStructureID: store,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"carrots": 2},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceBuy, Max: 12},
		}},
		// He remembers James Farm shut; Bell Farm he does not.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: jamesFarm, Condition: sim.ObservedClosed}: published.Add(-time.Hour),
		}),
	}
	bell := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Bell Farmer",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: bellFarm,
		Inventory:       map[sim.ItemKind]int{"carrots": 40},
	}
	james := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "James Fuller",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 420, Y: 420},
		WorkStructureID: jamesFarm,
		Inventory:       map[sim.ItemKind]int{"carrots": 40},
	}
	// Buyer-side price history: ~3 coins/carrot at Bell (affordable on 30 coins), ~6 at
	// James (which is shut anyway).
	bellBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	bellBuys.Push(sim.PriceObservation{BuyerID: thomasID, Amount: 3, Qty: 1, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	jamesBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	jamesBuys.Push(sim.PriceObservation{BuyerID: thomasID, Amount: 6, Qty: 1, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			thomasID: thomas, bellID: bell, jamesID: james,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			store:     plainStructure(store, "General Store"),
			bellFarm:  plainStructure(bellFarm, "Bell Farm"),
			jamesFarm: plainStructure(jamesFarm, "James Farm"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"carrots": {Name: "carrots", DisplayLabel: "carrots", Category: sim.ItemCategoryFood},
		},
		RestockReorderPct: 25,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: bellID, Item: "carrots"}:  bellBuys,
			{SellerID: jamesID, Item: "carrots"}: jamesBuys,
		},
	}
	return snap, thomasID, nil
}

// resellerRestockRoutedToDistributorNotFarm is the LLM-223 farm-wholesale fixture:
// a non-distributor reseller (Hannah Boggs, the innkeeper) is low on milk and has
// two milk suppliers — Ellis Farm (farm-tagged) and Josiah's General Store (the
// distributor-tagged wholesaler). The farm is dropped from every non-distributor's
// buy cues, so the "## Restocking" section lists ONLY the General Store as the
// walk-to target: Hannah restocks farm-origin milk through the distributor, never
// straight from the farm the PayWithItem backstop would refuse. Keeps
// TestGoldensNonDistributorRestockNeverTargetsFarm non-vacuous (a rendered restock
// section with a farm-tagged supplier present in the fixture). Clock-free: no
// price/shut memory and no wall-clock read in the render path.
func resellerRestockRoutedToDistributorNotFarm() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		josiahID = sim.ActorID("josiah")
		ellisID  = sim.ActorID("ellis")
		inn      = sim.StructureID("the_inn")
		store    = sim.StructureID("general_store")
		farm     = sim.StructureID("ellis_farm")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the inn
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"milk": 2},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Josiah Thorne",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 200, Y: 200},
		WorkStructureID: store,
		Inventory:       map[sim.ItemKind]int{"milk": 40},
	}
	ellis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Ellis Ward",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: farm,
		Inventory:       map[sim.ItemKind]int{"milk": 40},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			hannahID: hannah, josiahID: josiah, ellisID: ellis,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			inn:   plainStructure(inn, "The Inn"),
			store: plainStructure(store, "General Store"),
			farm:  plainStructure(farm, "Ellis Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
			sim.VillageObjectID(farm):  {ID: sim.VillageObjectID(farm), OwnerActorID: ellisID, Tags: []string{sim.TagFarm}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
	}
	return snap, hannahID, nil
}

// buyerKeptConsumeRemainderReconciled is the LLM-188 buyer-POV fixture: Anne
// Walker took a consume_now quote for 5 blueberries from Prudence Ward, but her
// hunger was low so the needs-clamp (consumableUnits, ZBBS-WORK-391) ate 1 and
// pocketed 4 to her pack. The settled ledger entry carries KeptUnits=4, and the
// golden pins that the "## Recently settled offers" line reads "you ate 1 on the
// spot and kept the other 4" — internally consistent with the Blueberries (x4)
// she carries — rather than the bare "you had it right away" that contradicted
// her inventory and triggered the confabulated short-count. Clock-free: the
// settled-offers recency window is measured against the fixture's PublishedAt /
// ResolvedAt, and the render path reads no wall clock.
func buyerKeptConsumeRemainderReconciled() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		anneID     = sim.ActorID("anne")
		prudenceID = sim.ActorID("prudence")
		apothecary = sim.StructureID("apothecary")
	)
	now := 915 // 15:15, the repro window
	published := time.Date(2026, 6, 30, 15, 15, 0, 0, time.UTC)
	anne := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Anne Walker",
		Role:              "traveler",
		State:             sim.StateIdle,
		InsideStructureID: apothecary,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"blueberries": 4},
		Acquaintances:     map[string]sim.Acquaintance{"Prudence Ward": {}},
	}
	prudence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Prudence Ward",
		Role:              "apothecary",
		State:             sim.StateIdle,
		InsideStructureID: apothecary,
		WorkStructureID:   apothecary,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
	}
	settled := &sim.PayLedgerEntry{
		ID: 449, BuyerID: anneID, SellerID: prudenceID,
		ItemKind: "blueberries", Qty: 5, Amount: 10, ConsumeNow: true,
		KeptUnits:  4,
		State:      sim.PayLedgerStateAccepted,
		ResolvedAt: published.Add(-30 * time.Second),
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{anneID: anne, prudenceID: prudence},
		Quotes:           map[sim.QuoteID]*sim.SceneQuote{},
		PayLedger:        map[sim.LedgerID]*sim.PayLedgerEntry{449: settled},
		Scenes:           map[sim.SceneID]*sim.Scene{},
		Huddles:          map[sim.HuddleID]*sim.Huddle{},
		Structures:       map[sim.StructureID]*sim.Structure{apothecary: plainStructure(apothecary, "PW Apothecary")},
	}
	return snap, anneID, nil
}

// sellerWithTakenQuoteAtPost is the LLM-189 perception regression fixture: a
// stateful vendor who just sold a lot (quote flipped to SceneQuoteStateTaken)
// while another lot stays active. The golden proves the taken lot drops out of
// "## Offers you've put out" and only the active lot renders.
func sellerWithTakenQuoteAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		prudenceID = sim.ActorID("prudence")
		anneID     = sim.ActorID("anne")
		apothecary = sim.StructureID("apothecary")
	)
	now := 600 // 10:00
	active := &sim.SceneQuote{
		ID: 1, SellerID: prudenceID, TargetBuyer: anneID,
		Lines: []sim.QuoteLine{{ItemKind: "raspberries", Qty: 5}}, Amount: 10,
		State: sim.SceneQuoteStateActive,
	}
	taken := &sim.SceneQuote{
		ID: 2, SellerID: prudenceID, TargetBuyer: anneID,
		Lines: []sim.QuoteLine{{ItemKind: "blueberries", Qty: 5}}, Amount: 10,
		State: sim.SceneQuoteStateTaken,
	}
	prudence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Prudence Ward",
		Role:              "apothecary",
		State:             sim.StateIdle,
		InsideStructureID: apothecary,
		WorkStructureID:   apothecary,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Anne": {}},
	}
	anne := &sim.ActorSnapshot{
		Kind: sim.KindNPCShared, DisplayName: "Anne", Role: "traveler", Needs: map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{prudenceID: prudence, anneID: anne},
		Quotes:           map[sim.QuoteID]*sim.SceneQuote{1: active, 2: taken},
		PayLedger:        map[sim.LedgerID]*sim.PayLedgerEntry{},
		Scenes:           map[sim.SceneID]*sim.Scene{},
		Huddles:          map[sim.HuddleID]*sim.Huddle{},
		Structures:       map[sim.StructureID]*sim.Structure{apothecary: plainStructure(apothecary, "PW Apothecary")},
	}
	return snap, prudenceID, nil
}

// lodgerGoldenBase builds the shared LLM-127 lodging-gate fixture: Ezekiel Crane,
// a renewal-due lodger of the Tavern (room 2, expiring 8h out — inside the 13h
// renewal window), 0 coins, scheduled 06:00–18:00. The caller positions him
// (inside) and sets the local clock (nowMin) to drive the on-shift gate, and may
// add an awake huddle companion. Renewal-due is computed off PublishedAt, so the
// rendered cue is deterministic; nowMin only moves the shift gate.
func lodgerGoldenBase(inside sim.StructureID, nowMin int, withCompanion bool) (*sim.Snapshot, sim.ActorID) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		patronID  = sim.ActorID("patron")
		tavern    = sim.StructureID("tavern")
		market    = sim.StructureID("market")
		huddleID  = sim.HuddleID("h1")
	)
	start, end := 360, 1080 // 06:00–18:00
	published := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC).Add(time.Duration(nowMin) * time.Minute)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		InsideStructureID: inside,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: {
				RoomID:    2,
				Source:    sim.AccessSourceLedger,
				LedgerID:  1,
				ExpiresAt: ptrTime(published.Add(8 * time.Hour)),
				Active:    true,
			},
		},
	}
	actors := map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel}
	huddles := map[sim.HuddleID]*sim.Huddle{}
	if withCompanion {
		ezekiel.CurrentHuddleID = huddleID
		actors[patronID] = &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			DisplayName:       "Goodwife Hale",
			Role:              "patron",
			State:             sim.StateIdle,
			InsideStructureID: inside,
			CurrentHuddleID:   huddleID,
			Needs:             map[sim.NeedKey]int{},
		}
		huddles[huddleID] = &sim.Huddle{ID: huddleID, Members: map[sim.ActorID]struct{}{ezekielID: {}, patronID: {}}}
	}
	nm := nowMin
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &nm,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           actors,
		Huddles:          huddles,
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: innStructure(tavern, "Tavern"),
			market: plainStructure(market, "Market"),
		},
		LodgingDefaultWeeklyRate: 14, // nightly 2
		LodgingBedtimeMinute:     22 * 60,
		LodgingCheckOutMinute:    11 * 60,
	}
	return snap, ezekielID
}

func lodgerRenewalDueInConversation() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := lodgerGoldenBase("market", 12*60, true) // on-shift, awake huddle companion
	return snap, id, nil
}

func lodgerRenewalDueOnShiftAway() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := lodgerGoldenBase("market", 12*60, false) // on-shift, away from inn, alone
	return snap, id, nil
}

func lodgerRenewalDueOffShift() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := lodgerGoldenBase("market", 20*60, false) // off-shift, away from inn, alone
	return snap, id, nil
}

// lodgerRenewalDueDeskRememberedShut is the LLM-126 Step-B surface: the same off-shift,
// away-from-inn, alone lodger as the positive baseline (so the walk-pull is actionable),
// but he went to the Tavern within the decay window and found the keeper's desk shut.
// The experiential wait-steer replaces the retired omniscient "keeper is abed" read; the
// memory is stamped relative to PublishedAt so it decays on the 4h closed-business TTL.
func lodgerRenewalDueDeskRememberedShut() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := lodgerGoldenBase("market", 20*60, false)
	snap.Actors[id].Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "tavern", Condition: sim.ObservedClosed}: snap.PublishedAt.Add(-time.Hour),
	})
	return snap, id, nil
}

// buyerRemembersVendorShut is the LLM-126 Step-A surface: a hungry forager (Ezekiel)
// stands near a cheese seller at the General Store, but he went there within the decay
// window and found it shut — no keeper tending it (now including an abed keeper, since
// the capture gates on availability). The golden pins the "## What you can eat or drink"
// buy cue carrying the experiential "found it shut up" annotation — the only path to a
// closed cue now that the omniscient "(currently closed)" marker is retired. The seller
// is present and awake; the cue is driven by his memory, not her state. No orders, fixed
// PublishedAt (the observation is stamped relative to it) → byte-stable.
func buyerRemembersVendorShut() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		mabelID   = sim.ActorID("mabel")
		store     = sim.StructureID("general_store")
	)
	now := 600 // 10:00 — daytime
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:        sim.KindNPCStateful,
		DisplayName: "Ezekiel Crane",
		Role:        "forager",
		State:       sim.StateIdle,
		Pos:         sim.WorldPos{X: 0, Y: 0}.Tile(),
		Coins:       6,
		Needs:       map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		// He went to the store within the decay window and found it shut.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: store, Condition: sim.ObservedClosed}: published.Add(-time.Hour),
		}),
	}
	mabel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Mabel Stone",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   store,
		InsideStructureID: store,
		Coins:             20,
		Inventory:         map[sim.ItemKind]int{"cheese": 5},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, mabelID: mabel},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"cheese": {
				Name: "cheese", DisplayLabel: "Cheese",
				DisplayLabelSingular: "wedge of cheese", DisplayLabelPlural: "wedges of cheese",
				Category:  sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
			},
		},
	}
	return snap, ezekielID, nil
}

// TestForgeCueOnlyForMultiOutputCrafterAtForge is the LLM-116 cross-scenario
// invariant: the "## Time to produce" cue appears in EXACTLY the multi-output-producer-
// at-workplace scenarios and no other — whether unfocused (choose menu,
// smith_choosing_at_forge / the non-smith dairy_choosing_at_farm) or focused (the
// LLM-128 continue-and-stop steer, smith_forging_focused). A single-output producer
// or a non-crafter must never see it — the structural property the per-builder gate
// (>1 produce entry AND at workplace) is meant to hold across the whole matrix.
func TestForgeCueOnlyForMultiOutputCrafterAtForge(t *testing.T) {
	const marker = "## Time to produce"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "smith_choosing_at_forge" || sc.name == "smith_forging_focused" || sc.name == "dairy_choosing_at_farm"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: forge cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestProductionFocusLineOnlyAtWork is the LLM-121 cross-scenario invariant: the
// standing "You are making X." self-state line appears in EXACTLY the scenario where
// the crafter has a focus set AND is at its own work structure (smith_forging_focused)
// and never away from it. The off-work smith (same focus, at the Tavern) must not
// carry it — produce_tick makes nothing there, so the present-tense line would
// misstate the situation; the unfocused smith (smith_choosing_at_forge) has no focus
// to state. Mirrors the forge-cue invariant; both express the "only at the forge"
// gate as a property over the whole matrix.
func TestProductionFocusLineOnlyAtWork(t *testing.T) {
	const marker = "You are making"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		// innkeeper_pricing_with_makings_cost (LLM-226) is Hannah focused on porridge
		// AT her inn — the same focus-at-work state as the forging smith, so the line
		// is correct there too.
		want := sc.name == "smith_forging_focused" || sc.name == "innkeeper_pricing_with_makings_cost"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: production-focus line present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestActiveWorkerCueOnlyForEmployerWithWorkingOffer is the LLM-202 cross-scenario
// invariant: the employer-side "X is working a job for you" cue (renderWorkersForMe)
// renders in EXACTLY the scenario where the subject is an employer with a worker
// mid-contract (a Working offer where EmployerID == subject). It must NOT appear for
// an employer whose only labor offer is Pending (broke_employer_cannot_pay_labor_offer
// — that renders in "## Work offers awaiting your decision", not as an active worker),
// nor anywhere else in the matrix. The marker is distinct from the worker's own
// "You are working a job for X" self-state line (renderLaborSelfState), which is
// second-person and never carries "is working a job for you".
func TestActiveWorkerCueOnlyForEmployerWithWorkingOffer(t *testing.T) {
	const marker = "is working a job for you"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "employer_with_worker_on_job"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: active-worker cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestWaresWorthCueOnlyInCompanyWithOwnTrade is the LLM-125 / LLM-191 cross-scenario
// invariant: the "## What your wares fetch" cue appears in EXACTLY the scenarios where
// the actor is in company (a huddle) AND has priced own wares — produced
// (smith_bartering_at_tavern) or resold (keeper_reselling_in_company, LLM-191). An
// actor alone — even at its forge with recipes — or one in company but without its
// own priced wares must never see it: the own-wares base price stays out of solo and
// no-own-trade turns, and is gated on company rather than location (unlike the forge
// cue).
func TestWaresWorthCueOnlyInCompanyWithOwnTrade(t *testing.T) {
	const marker = "## What your wares fetch"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "smith_bartering_at_tavern" || sc.name == "keeper_reselling_in_company" ||
			sc.name == "innkeeper_pricing_with_makings_cost" // LLM-226: producer in company, priced own ware
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: wares-worth cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestProducerPitchNoteOnlyForCoPresentMaker is the LLM-171 seller-side
// cross-scenario invariant: the producer-awareness note that steers a keeper off
// pitching a maker their own ware back appears in EXACTLY the scenario where a
// co-present customer makes one of the seller's listed goods
// (keeper_not_pitching_makers_own_ware). No other "## Custom at hand" scenario —
// nor any unrelated turn — carries it.
func TestProducerPitchNoteOnlyForCoPresentMaker(t *testing.T) {
	const marker = "don't pitch those back to their own maker"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "keeper_not_pitching_makers_own_ware"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: producer-pitch note present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestBuyBackQuoteSteerOnlyForOwnProducedOrAtCap is the LLM-171 buyer-side
// cross-scenario invariant: the steer that withholds a buy-quote's take for a
// good the buyer makes itself or already holds at cap appears in EXACTLY the
// scenario where that holds (maker_offered_own_ware_buy_quote). In that scenario
// the actionable "pay_with_item with quote_id" take is absent — the steer
// REPLACES it, so the buy-back loop can't close — while no other turn shows it.
func TestBuyBackQuoteSteerOnlyForOwnProducedOrAtCap(t *testing.T) {
	const (
		steer = "there's no reason to buy"
		take  = "pay_with_item with quote_id"
	)
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "maker_offered_own_ware_buy_quote"
		if has := strings.Contains(got, steer); has != want {
			t.Errorf("scenario %q: buy-back steer present=%v, want %v", sc.name, has, want)
		}
		if want && strings.Contains(got, take) {
			t.Errorf("scenario %q: redundant buy-quote still shows the actionable take %q — it must be withheld", sc.name, take)
		}
	}
}

// TestCoinQuoteTakeNamesConcreteTerms is the LLM-172 cross-scenario invariant:
// the single-line coin-quote take never falls back to the unanchored "the same
// item, qty, and amount" phrasing that a buyer carrying other goods misbound to
// one of those (paying for nothing via a bare pay). Wherever the actionable take
// renders it must name the concrete item/qty/amount; buyer_offered_quote_take_names_terms
// pins the exact string for the live stew case.
func TestCoinQuoteTakeNamesConcreteTerms(t *testing.T) {
	const vague = "the same item, qty, and amount"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		if strings.Contains(got, vague) {
			t.Errorf("scenario %q: coin-quote take still uses the unanchored %q phrasing — name the concrete item/qty/amount", sc.name, vague)
		}
		if sc.name == "buyer_offered_quote_take_names_terms" {
			if want := `item "stew", qty 1, and amount 4`; !strings.Contains(got, want) {
				t.Errorf("scenario %q: take missing the concrete terms %q\n%s", sc.name, want, got)
			}
		}
	}
}

// TestStallRepairCueOnlyAtOwnWornStall is the LLM-118 cross-scenario invariant:
// the "## Your stall" owner repair cue appears in EXACTLY the scenarios where the
// actor stands at their OWN worn stall — never for a passerby (who gets the
// co-present line instead) or any unrelated scenario. The same StallRepair signal
// gates the repair tool, so this also pins where the tool is offered.
func TestStallRepairCueOnlyAtOwnWornStall(t *testing.T) {
	const marker = "## Your stall"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "owner_at_worn_stall" || sc.name == "owner_at_degraded_stall"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: '## Your stall' cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestFarmUpkeepCueOnlyForOwingFarmOwner is the LLM-215 cross-scenario invariant: the
// "## Farm upkeep" cue appears in EXACTLY the scenarios where the actor owns a farm
// and owes upkeep shovels — never for a non-farm-owner or any unrelated scenario. It
// backstops the leak an owner-scoped, stock-derived cue is most prone to: showing up
// for someone who doesn't own a farm.
func TestFarmUpkeepCueOnlyForOwingFarmOwner(t *testing.T) {
	const marker = "## Farm upkeep"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "farm_owner_owes_upkeep"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: '## Farm upkeep' cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestVendorOperatingCueOnlyDuringOperatingHours is the LLM-123 cross-scenario
// invariant: the "How you trade:" trade-conduct block appears in EXACTLY the
// scenarios where a keeper is at its own post AND operating — on shift
// (keeper_at_post_onshift) or staying open past close (keeper_staying_open_offshift)
// — and never at a closed post off-shift (keeper_at_closed_post_offshift_night) nor
// in any non-keeper / off-post scenario. The structural property the
// AtOwnBusinessOperating gate is meant to hold across the whole matrix: off-shift at
// a closed post, the keeper is no longer told to "tend to your trade" at midnight.
func TestVendorOperatingCueOnlyDuringOperatingHours(t *testing.T) {
	const marker = "How you trade:"
	operating := map[string]bool{
		"keeper_at_post_onshift":       true,
		"keeper_staying_open_offshift": true,
		// LLM-171: John keeps his tavern on shift, at post — legitimately operating.
		"keeper_not_pitching_makers_own_ware": true,
	}
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := operating[sc.name]
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: trade-conduct cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestLodgingDeskShutCueOnlyWhenRemembered is the LLM-126 cross-scenario invariant:
// the experiential "found the keeper's desk shut" wait-steer appears in EXACTLY the
// scenario where a renewal-due lodger remembers its inn shut and the pull is not
// deferred (lodger_renewal_due_desk_remembered_shut). It must never leak into a lodger
// turn without that memory — the omniscient keeper-asleep read it replaced is gone, so
// the cue is gated purely on the decaying experiential memory.
func TestLodgingDeskShutCueOnlyWhenRemembered(t *testing.T) {
	const marker = "found the keeper's desk shut"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "lodger_renewal_due_desk_remembered_shut"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: desk-shut cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestExperientialShutCueOnlyWhenRemembered is the LLM-126 cross-scenario invariant:
// the experiential closed-business annotation (a buy/rest cue's "found it shut up"
// recollection) appears in EXACTLY the scenario where the buyer remembers the vendor
// shut (buyer_remembers_vendor_shut). With the omniscient live-asleep marker retired, a
// closed buy cue is reachable only through the decaying experiential memory — never from
// a keeper's live state across the map.
func TestExperientialShutCueOnlyWhenRemembered(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "buyer_remembers_vendor_shut"
		if has := strings.Contains(got, closedBusinessAnnotation); has != want {
			t.Errorf("scenario %q: experiential shut annotation present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestEmptyPurseCannotPayCueTracksActorCoins is the LLM-153 cross-scenario invariant:
// the "you cannot pay" consequence appears in EXACTLY the scenarios whose rendered
// actor holds zero coins, and never with a positive balance. The expected branch is
// derived from the BUILT actor state (snap.Actors[actorID].Coins), NOT from the
// rendered purse text — so this independently asserts the cue tracks the actor's coins
// rather than merely pinning that the rendered line is internally self-consistent (it
// would catch a positive actor wrongly rendering the empty-purse form). The matrix must
// exercise both branches for the check to mean anything, so we also require one of each.
func TestEmptyPurseCannotPayCueTracksActorCoins(t *testing.T) {
	const cannotPayMarker = "you cannot pay for anything until you earn some"
	var sawEmpty, sawPositive bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, _ := sc.build()
		actor := snap.Actors[actorID]
		if actor == nil {
			t.Fatalf("scenario %q: rendered actor %q missing from snapshot", sc.name, actorID)
		}
		wantCannotPay := actor.Coins == 0
		if wantCannotPay {
			sawEmpty = true
		} else {
			sawPositive = true
		}
		if has := strings.Contains(renderScenario(sc), cannotPayMarker); has != wantCannotPay {
			t.Errorf("scenario %q: coins=%d cannot-pay cue=%v, want %v", sc.name, actor.Coins, has, wantCannotPay)
		}
	}
	if !sawEmpty || !sawPositive {
		t.Errorf("matrix must exercise both branches: sawEmpty=%v sawPositive=%v", sawEmpty, sawPositive)
	}
}

// TestLaborTieAnnotationTracksWorkerKin is the LLM-157 cross-scenario invariant: the
// "(your housemate)" / "(your workmate)" relationship annotation appears in EXACTLY the
// scenarios where the subject is a worker AND at least one of its addressable co-present members (huddle
// peers ∪ co-present, the same lists Render names) shares its household or workplace.
// The expectation is recomputed from raw ActorSnapshot fields (subjectIsWorker +
// sharesHousehold/sharesWorkplace) — NOT from the member's SolicitTie — so it independently
// asserts the annotation tracks co-residence/co-employment rather than pinning the render
// against its own marker. The matrix must exercise both branches to mean anything.
func TestLaborTieAnnotationTracksWorkerKin(t *testing.T) {
	const (
		housemateMarker = "(your housemate)"
		workmateMarker  = "(your workmate)"
	)
	var sawTied, sawUntied bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, warrants := sc.build()
		subj := snap.Actors[actorID]
		p := Build(snap, actorID, warrants)
		want := false
		if subjectIsWorker(subj) {
			audience := append(append([]HuddleMember{}, p.Surroundings.HuddleMembers...), p.Surroundings.CoPresent...)
			for _, m := range audience {
				if peer := snap.Actors[m.ID]; peer != nil && (sharesHousehold(subj, peer) || sharesWorkplace(subj, peer)) {
					want = true
					break
				}
			}
		}
		if want {
			sawTied = true
		} else {
			sawUntied = true
		}
		// Scope the search to the "## Around you" block where the annotation renders,
		// not the whole prompt — so the invariant can't pass/fail on the marker phrase
		// appearing in some unrelated cue or section later (code_review note).
		around := aroundYouSection(renderScenario(sc))
		has := strings.Contains(around, housemateMarker) || strings.Contains(around, workmateMarker)
		if has != want {
			t.Errorf("scenario %q: labor-tie annotation=%v, want %v", sc.name, has, want)
		}
	}
	if !sawTied || !sawUntied {
		t.Errorf("matrix must exercise both branches: sawTied=%v sawUntied=%v", sawTied, sawUntied)
	}
}

// aroundYouSection returns the rendered "## Around you" block (its header line
// excluded), up to the next "## " section header or the end of the prompt — so a
// surroundings-specific assertion can scope to where a cue actually renders instead
// of scanning the whole prompt and risking a false match elsewhere.
func aroundYouSection(rendered string) string {
	const head = "## Around you\n"
	i := strings.Index(rendered, head)
	if i < 0 {
		return ""
	}
	rest := rendered[i+len(head):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// growerAtStrippedBush reproduces the LLM-98 live shape: Prudence, a forager,
// stands on her own raspberry bush during her shift, having just stripped it to
// zero stock. It is the only gatherable within loiter reach, so
// ResolveGatherSource resolves it — the LLM-98 stock gate is what keeps the cue
// (and the gather tool) off an empty bush. No orders, no clock read → byte-stable.
func growerAtStrippedBush() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const prudenceID = sim.ActorID("prudence")
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift, mid-harvest
	bushPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	prudence := &sim.ActorSnapshot{
		Kind:             sim.KindNPCShared,
		DisplayName:      "Prudence Hart",
		Role:             "forager",
		State:            sim.StateIdle,
		Pos:              bushPin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            12,
		Needs:            map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{prudenceID: prudence},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"prudence_bush": {
				ID:            "prudence_bush",
				DisplayName:   "Raspberry Bush",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  prudenceID,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Attribute: "hunger", Amount: 0, GatherItem: "raspberries", AvailableQuantity: intp(0)},
				},
			},
		},
	}
	return snap, prudenceID, nil
}

// stallWearSnapshot builds a one-stall, one-actor snapshot for the LLM-118 cues.
// The actor stands on the stall's loiter pin; the stall is a tagged, owned market
// stall worn to `wear`. ownerID is the stall's owner (the perceiving actor for the
// owner cues; a different actor for the passerby cue). nails seeds the actor's
// pack. No orders, no clock read → byte-stable.
func stallWearSnapshot(actorID, ownerID sim.ActorID, displayName, role string, wear, nails int) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	stallPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	actor := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      displayName,
		Role:             role,
		State:            sim.StateIdle,
		Pos:              stallPin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            8,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"nail": nails},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{actorID: actor},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"market_stall": {
				ID:            "market_stall",
				DisplayName:   "Blacksmith",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  ownerID,
				Tags:          []string{sim.TagMarketStall},
				Wear:          wear,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, actorID, nil
}

// ownerAtWornStall: the owner at his own worn stall, short on nails — the buy-then-
// mend steer. wear 450 (>= repair 400, < degrade 600), 2 nails (< 5 needed).
func ownerAtWornStall() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return stallWearSnapshot("ezekiel", "ezekiel", "Ezekiel Crane", "blacksmith", 450, 2)
}

// ownerAtDegradedStall: the owner at his own degraded stall with nails in hand —
// the "too worn to trade … repair it now" steer. wear 650 (>= degrade 600).
func ownerAtDegradedStall() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return stallWearSnapshot("ezekiel", "ezekiel", "Ezekiel Crane", "blacksmith", 650, 5)
}

// passerbyAtWornStall: a non-owner standing at someone else's worn stall — the
// co-present atmosphere line, no owner cue. The actor (John) differs from the
// stall's owner (Ezekiel).
func passerbyAtWornStall() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return stallWearSnapshot("john", "ezekiel", "John Ellis", "tavernkeeper", 450, 0)
}

// farmUpkeepSnapshot: the actor owns a farm-tagged object and, with `coins` held
// against `floor`/`coinsPerShovel`, owes more upkeep shovels than the `shovels` they
// carry — so the "## Farm upkeep" cue renders. Not co-location-gated (the buy happens
// at the blacksmith), so the actor's position is irrelevant to the cue. No orders, no
// clock read → byte-stable.
func farmUpkeepSnapshot(coins, shovels, floor, coinsPerShovel int) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const actorID = sim.ActorID("elizabeth")
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	farmPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	inv := map[sim.ItemKind]int{}
	if shovels > 0 {
		inv[sim.ShovelItemKind] = shovels
	}
	actor := &sim.ActorSnapshot{
		Kind:             sim.KindNPCShared,
		DisplayName:      "Elizabeth Ellis",
		Role:             "farmer",
		State:            sim.StateIdle,
		Pos:              farmPin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            coins,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        inv,
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.NeedThresholds{},
		Assets:                   emptyAssetSet,
		FarmUpkeepFloor:          floor,
		FarmUpkeepCoinsPerShovel: coinsPerShovel,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{actorID: actor},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"ellis_farm": {
				ID:            "ellis_farm",
				DisplayName:   "Ellis Farm",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  actorID,
				Tags:          []string{sim.TagFarm},
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, actorID, nil
}

// farmOwnerOwesUpkeep: Elizabeth owns Ellis Farm with 95 coins (floor 30, band 20 →
// owes 3 shovels) and none in hand — the "## Farm upkeep" buy-3-from-the-blacksmith cue.
func farmOwnerOwesUpkeep() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return farmUpkeepSnapshot(95, 0, 30, 20)
}

// hungryForagerAtStockedBush is the LLM-113 situation: a hungry forager stands at
// an unowned (commons) raspberry bush that still has stock, with a cheese seller
// at the General Store. It exercises the singular/plural catalog phrasing in two
// cues at once — the gather affordance ("you can gather raspberries here") and the
// buy menu ("buy a wedge of cheese", the measure phrase + indefinite article). No
// orders, no clock read → byte-stable.
func hungryForagerAtStockedBush() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		mabelID   = sim.ActorID("mabel")
		store     = sim.StructureID("general_store")
	)
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — daytime
	bushPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	ezekiel := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Ezekiel Crane",
		Role:             "forager",
		State:            sim.StateIdle,
		Pos:              bushPin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            6,
		Needs:            map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
	}
	mabel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Mabel Stone",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   store,
		InsideStructureID: store,
		Coins:             20,
		Inventory:         map[sim.ItemKind]int{"cheese": 5},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, mabelID: mabel},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"raspberries": {
				Name: "raspberries", DisplayLabel: "Raspberries",
				DisplayLabelSingular: "raspberry", DisplayLabelPlural: "raspberries",
				Category:  sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 2}},
			},
			"cheese": {
				Name: "cheese", DisplayLabel: "Cheese",
				DisplayLabelSingular: "wedge of cheese", DisplayLabelPlural: "wedges of cheese",
				Category:  sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
			},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"wild_bush": {
				ID:            "wild_bush",
				DisplayName:   "Raspberry Bush",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Attribute: "hunger", Amount: 0, GatherItem: "raspberries", AvailableQuantity: intp(3)},
				},
			},
		},
	}
	return snap, ezekielID, nil
}

// grazingProducerScenario builds the LLM-134 fixture: Moses James, a carrot
// farmer, standing at his own farm on shift carrying only the carrots he grows
// to sell, at the given hunger level. No other food, vendor, or free source is
// present, so the carrots are the only possible own-stock cue — the scenario
// isolates the trade-stock demotion. No PriceBook/orders, so the render takes no
// wall-clock read and stays byte-stable.
func grazingProducerScenario(hunger int) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		mosesID = sim.ActorID("moses")
		farm    = sim.StructureID("james_farm")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	moses := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Moses James",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		WorkStructureID:   farm,
		InsideStructureID: farm,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             4,
		Inventory:         map[sim.ItemKind]int{"carrots": 20},
		Needs:             map[sim.NeedKey]int{"hunger": hunger},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceProduce, Max: 30},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{mosesID: moses},
		Structures: map[sim.StructureID]*sim.Structure{
			farm: plainStructure(farm, "James Farm"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"carrots": {
				Name: "carrots", DisplayLabel: "Carrots",
				DisplayLabelSingular: "carrot", DisplayLabelPlural: "carrots",
				Category:  sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 3}},
			},
		},
	}
	return snap, mosesID, nil
}

func producerHungryMildAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return grazingProducerScenario(14) // mild: felt (>= silent floor 10), below red (18)
}

// hungryActorHoldingRawMeat is the LLM-166 fixture: a hungry stateful NPC stands
// at its post carrying raw Meat (a stew INGREDIENT — food-category but eases no
// need raw) alongside edible Cheese. The golden pins the use annotation folded
// into the carry readout — "Meat (x7, used to produce stew)" — while Cheese stays
// bare. This is the Josiah-eats-raw-meat case: the most food-like name in a flat
// inventory was the rejected eat that burned the turn.
func hungryActorHoldingRawMeat() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID = sim.ActorID("josiah")
		store    = sim.StructureID("general_store")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		WorkStructureID:   store,
		InsideStructureID: store,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             6,
		Inventory:         map[sim.ItemKind]int{"meat": 7, "cheese": 15},
		Needs:             map[sim.NeedKey]int{"hunger": 14}, // felt, below red
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"meat": {
				Name: "meat", DisplayLabel: "Meat",
				DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat",
				Category: sim.ItemCategoryFood, // food, but no Satisfies -> inedible raw
			},
			"cheese": {
				Name: "cheese", DisplayLabel: "Cheese",
				DisplayLabelSingular: "wedge of cheese", DisplayLabelPlural: "wedges of cheese",
				Category:  sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 4}},
			},
			"stew": {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "bowl of stew"},
		},
		// stew consumes meat — the engine derives the reverse use-index from this
		// (World.recipeUses), aliased onto the snapshot as RecipeUses. Set both so
		// the fixture reads coherently.
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"stew": {OutputItem: "stew", Inputs: []sim.RecipeInput{{Item: "meat", Qty: 10}}},
		},
		RecipeUses: map[sim.ItemKind][]sim.ItemKind{"meat": {"stew"}},
	}
	return snap, josiahID, nil
}

func producerStarvingAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return grazingProducerScenario(sim.DefaultHungerRedThreshold) // 18 — red/desperation tier
}

// huddleConversationLoopingScenario is the LLM-169 fixture: two idle workers (the
// Walker sisters) stand together in a huddle going in circles. Patience (the
// subject) is in an armed conversational loop — ConversationLooping is set, the
// publish-time huddleLoopArmed signal the loop sweep arms on — and Anne holds a
// live await edge to her (Anne addressed Patience and waits on a reply). The golden
// pins the LLM-169 swap: the "Anne Walker is waiting for your reply." nag is
// suppressed (that nag is what manufactures the echo) and the coda is the "you've
// agreed — act now or done()" loop steer rather than the default/awaiting coda the
// agree-loop fed on. Byte-stable: fixed PublishedAt, the await edge + utterances
// stamped relative to it, no orders.
func huddleConversationLoopingScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		patienceID = sim.ActorID("patience")
		anneID     = sim.ActorID("anne")
		huddleID   = sim.HuddleID("walker_huddle")
	)
	now := 13 * 60 // 13:00 — afternoon
	published := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)
	patience := &sim.ActorSnapshot{
		Kind:                sim.KindNPCStateful,
		DisplayName:         "Patience Walker",
		Role:                "villager",
		State:               sim.StateIdle,
		CurrentHuddleID:     huddleID,
		Coins:               5,
		Needs:               map[sim.NeedKey]int{},
		ConversationLooping: true,
	}
	anne := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Anne Walker",
		Role:            "villager",
		State:           sim.StateIdle,
		CurrentHuddleID: huddleID,
		Coins:           5,
		Needs:           map[sim.NeedKey]int{},
		// Anne addressed Patience and awaits her reply — the edge that would render
		// "Anne Walker is waiting for your reply." but for the LLM-169 suppression.
		AwaitingReplyFrom: map[sim.ActorID]time.Time{patienceID: published.Add(-10 * time.Second)},
	}
	utter := func(spk sim.ActorID, name, text string, agoSec int) sim.Utterance {
		return sim.Utterance{SpeakerID: spk, SpeakerName: name, Text: text, At: published.Add(-time.Duration(agoSec) * time.Second)}
	}
	snap := &sim.Snapshot{
		PublishedAt:         published,
		LocalMinuteOfDay:    &now,
		NeedThresholds:      sim.NeedThresholds{},
		Assets:              emptyAssetSet,
		NPCAwaitReplyWindow: 60 * time.Second,
		Actors:              map[sim.ActorID]*sim.ActorSnapshot{patienceID: patience, anneID: anne},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {
				ID:      huddleID,
				Members: map[sim.ActorID]struct{}{patienceID: {}, anneID: {}},
				RecentUtterances: []sim.Utterance{
					utter(patienceID, "Patience Walker", "Let's go to the well!", 40),
					utter(anneID, "Anne Walker", "Let's go to the well!", 32),
					utter(patienceID, "Patience Walker", "Let's go!", 24),
					utter(anneID, "Anne Walker", "Let's go to the well!", 16),
					utter(patienceID, "Patience Walker", "Lead the way, Anne.", 8),
				},
			},
		},
	}
	return snap, patienceID, nil
}

// undirectedReaskSolePeerScenario is the LLM-232 fixture: John Ellis stands in a
// two-body huddle with Anne Walker and has floated a plain, unaddressed trade
// proposal that opened no WORK-370 edge; Anne has said nothing back. John spoke
// most recently (~75s ago — past the 60s directed-edge window, so even a directed
// edge would have lapsed, but well inside ReaskSuppressWindow), and the huddle is
// NOT looping, so the sole-awake-peer
// anchor folds Anne into AwaitingReplyFrom: the golden pins the "you already
// spoke, wait, don't repeat" line + the awaiting-reply coda on an otherwise
// undirected re-ask. Fixed PublishedAt, utterances stamped relative to it, no
// orders → byte-stable.
func undirectedReaskSolePeerScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID   = sim.ActorID("john")
		anneID   = sim.ActorID("anne")
		huddleID = sim.HuddleID("store_huddle")
	)
	now := 13 * 60 // 13:00 — afternoon, no sleep/return-to-post cue competes
	published := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	john := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "John Ellis",
		Role:            "villager",
		State:           sim.StateIdle,
		CurrentHuddleID: huddleID,
		Coins:           5,
		Needs:           map[sim.NeedKey]int{},
	}
	anne := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Anne Walker",
		Role:            "villager",
		State:           sim.StateIdle,
		CurrentHuddleID: huddleID,
		Coins:           5,
		Needs:           map[sim.NeedKey]int{},
	}
	utter := func(spk sim.ActorID, name, text string, agoSec int) sim.Utterance {
		return sim.Utterance{SpeakerID: spk, SpeakerName: name, Text: text, At: published.Add(-time.Duration(agoSec) * time.Second)}
	}
	snap := &sim.Snapshot{
		PublishedAt:         published,
		LocalMinuteOfDay:    &now,
		NeedThresholds:      sim.NeedThresholds{},
		Assets:              emptyAssetSet,
		NPCAwaitReplyWindow: 60 * time.Second,
		Actors:              map[sim.ActorID]*sim.ActorSnapshot{johnID: john, anneID: anne},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {
				ID:      huddleID,
				Members: map[sim.ActorID]struct{}{johnID: {}, anneID: {}},
				RecentUtterances: []sim.Utterance{
					utter(anneID, "Anne Walker", "Morning, John.", 110),
					utter(johnID, "John Ellis", "Morning. Say — I've cheese to spare; could you fetch me carrots?", 85),
					utter(johnID, "John Ellis", "A fair trade, cheese for carrots?", 75),
				},
			},
		},
	}
	return snap, johnID, nil
}

// hungryLooperAtFoodlessHome is the LLM-176 fixture: the Walker sisters loop in a
// huddle inside their foodless residence while hungry. Patience (the subject) is
// in an armed conversational loop, feels red-tier hunger, carries nothing edible,
// holds 1 coin, and a free Raspberry Bush sits a walk away (in VillageObjects but
// far from the home, so it lists in the eat cue yet is NOT co-located). It drives
// both LLM-176 cues at once: the no-food-here dead end (inside a structure, felt
// hunger, nothing held, no source on the tile) and the need-redirect coda (the
// looping coda names the nearest free source + move_to instead of the generic
// "do what you've agreed" line). Fixed PublishedAt, no orders/PriceBook →
// byte-stable.
func hungryLooperAtFoodlessHome() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		patienceID = sim.ActorID("patience")
		anneID     = sim.ActorID("anne")
		homeID     = sim.StructureID("walker_residence")
		huddleID   = sim.HuddleID("walker_huddle")
	)
	zero := 0
	now := 13 * 60 // 13:00 — afternoon, so no sleep/return-to-post cue competes
	published := time.Date(2026, 6, 29, 13, 0, 0, 0, time.UTC)
	homeTile := sim.WorldPos{X: 10, Y: 10}.Tile() // at home, far from the bush
	mkSister := func(name string, looping bool) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:                sim.KindNPCStateful,
			DisplayName:         name,
			Role:                "villager",
			State:               sim.StateIdle,
			Pos:                 homeTile,
			HomeStructureID:     homeID,
			InsideStructureID:   homeID,
			CurrentHuddleID:     huddleID,
			Coins:               1,
			Needs:               map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
			ConversationLooping: looping,
		}
	}
	patience := mkSister("Patience Walker", true)
	anne := mkSister("Anne Walker", false)
	utter := func(spk sim.ActorID, name, text string, agoSec int) sim.Utterance {
		return sim.Utterance{SpeakerID: spk, SpeakerName: name, Text: text, At: published.Add(-time.Duration(agoSec) * time.Second)}
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{patienceID: patience, anneID: anne},
		Structures: map[sim.StructureID]*sim.Structure{
			homeID: plainStructure(homeID, "Walker Residence"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"raspberries": {
				Name: "raspberries", DisplayLabel: "Raspberries",
				DisplayLabelSingular: "raspberry", DisplayLabelPlural: "raspberries",
				Category:  sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 2}},
			},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"wild_bush": {
				ID:            "wild_bush",
				DisplayName:   "Raspberry Bush",
				Pos:           sim.WorldPos{X: 400, Y: 400}, // a walk away — listed in the eat cue, not co-located
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Attribute: "hunger", Amount: -2}, // eases hunger on arrival — a free public source
				},
			},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {
				ID:      huddleID,
				Members: map[sim.ActorID]struct{}{patienceID: {}, anneID: {}},
				RecentUtterances: []sim.Utterance{
					utter(patienceID, "Patience Walker", "I'm sure there's bread in the kitchen.", 40),
					utter(anneID, "Anne Walker", "Let's check the kitchen for food.", 32),
					utter(patienceID, "Patience Walker", "There must be something to eat at home.", 24),
					utter(anneID, "Anne Walker", "Let's look in the kitchen.", 16),
					utter(patienceID, "Patience Walker", "I'll find us a bite here.", 8),
				},
			},
		},
	}
	return snap, patienceID, nil
}

// TestConversationLoopingCodaOnlyWhenLooping is the LLM-169 cross-scenario
// invariant: the "you've agreed, act now or done()" loop coda appears in EXACTLY
// the scenario whose rendered actor is in an armed conversational loop
// (ActorSnapshot.ConversationLooping), and never elsewhere. The expectation is
// recomputed from the BUILT actor state, not the rendered text, so it independently
// asserts the coda tracks the flag rather than pinning the render against its own
// marker.
func TestConversationLoopingCodaOnlyWhenLooping(t *testing.T) {
	const marker = "keep saying the same thing"
	var sawLooping bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, _ := sc.build()
		actor := snap.Actors[actorID]
		if actor == nil {
			t.Fatalf("scenario %q: rendered actor %q missing from snapshot", sc.name, actorID)
		}
		want := actor.ConversationLooping
		if want {
			sawLooping = true
		}
		if has := strings.Contains(renderScenario(sc), marker); has != want {
			t.Errorf("scenario %q: looping coda present=%v, want %v (ConversationLooping=%v)", sc.name, has, want, actor.ConversationLooping)
		}
	}
	if !sawLooping {
		t.Error("matrix must exercise the looping branch (ConversationLooping=true) at least once")
	}
}

// TestIngredientUseAnnotationOnlyForInedibleRecipeInputs is the LLM-166
// cross-scenario invariant: the "used to produce X" annotation appears in EXACTLY
// the scenarios whose rendered actor carries an INEDIBLE item that some recipe
// consumes as an input (snap.RecipeUses), and never otherwise. The gate is
// recomputed from BUILT state — non-consumable AND a recipe input — so it mirrors
// inventoryItemUse exactly (an edible item, a non-ingredient, or an item with no
// catalog def draws no annotation, even if it appears in RecipeUses).
func TestIngredientUseAnnotationOnlyForInedibleRecipeInputs(t *testing.T) {
	const marker = "used to produce"
	var sawAnnotated bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, _ := sc.build()
		actor := snap.Actors[actorID]
		want := false
		if actor != nil {
			for kind, qty := range actor.Inventory {
				if qty <= 0 {
					continue
				}
				def := snap.ItemKinds[kind]
				if def == nil || def.Consumable() {
					continue // edible or uncatalogued -> not annotated
				}
				if len(snap.RecipeUses[kind]) > 0 {
					want = true
					break
				}
			}
		}
		if want {
			sawAnnotated = true
		}
		if has := strings.Contains(renderScenario(sc), marker); has != want {
			t.Errorf("scenario %q: ingredient-use annotation present=%v, want %v", sc.name, has, want)
		}
	}
	if !sawAnnotated {
		t.Error("matrix must exercise the annotated branch (an inedible carried recipe-input) at least once")
	}
}

// TestOwnTradeStockEatCueOnlyAtDesperation is the LLM-134 cross-scenario
// invariant: a producer's own trade stock surfaces in the own-stock "consume to
// eat" cue ONLY at the red/desperation tier. The same farmer holding the same
// carrots is offered them when starving and NOT when only mildly hungry — the
// demotion that stops merchandise grazing while preserving the don't-starve-next-
// to-your-food safety net.
func TestOwnTradeStockEatCueOnlyAtDesperation(t *testing.T) {
	const cue = "consume to eat"
	mild := renderScenario(perceptionScenario{name: "producer_hungry_mild_at_post", build: producerHungryMildAtPost})
	if strings.Contains(mild, cue) {
		t.Errorf("mild-hunger producer was offered its own trade stock to eat (cue %q should be absent):\n%s", cue, mild)
	}
	red := renderScenario(perceptionScenario{name: "producer_starving_at_post", build: producerStarvingAtPost})
	if !strings.Contains(red, cue) {
		t.Errorf("starving producer was NOT offered its own trade stock as last resort (cue %q should be present):\n%s", cue, red)
	}
}

// smithChoosingAtForge is the LLM-116/LLM-128 situation: Ezekiel, a multi-output
// crafter, stands inside his own forge on shift with two produce goods (skillet at
// cap, nail empty) and NO focus set yet — the realistic post-restart state the
// production-choice warrant fires on. The "## Time to produce" cue lists both makeable
// goods (time cost, stock vs cap, empty weekly made/sold counts) under the "Choose
// what to produce next" header, and the production-choice wake warrant renders. With
// no focus, neither the "— making this now" marker nor the standing "You are making
// nail." line appears — those move to smithForgingFocused. No orders, no clock read
// (PriceBook/RecentProduce empty so the windowed counts are 0 regardless of
// PublishedAt) → byte-stable.
func smithChoosingAtForge() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 5},
		ProductionFocus:   "", // unfocused — the post-restart state the warrant fires on (LLM-128)
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge: plainStructure(forge, "Blacksmith"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
			"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
	}
	warrants := []sim.WarrantMeta{
		{TriggerActorID: ezekielID, Reason: sim.ProductionChoiceWarrantReason{}, SourceEventID: 1},
	}
	return snap, ezekielID, warrants
}

// dairyChoosingAtFarm is the LLM-144 trade-neutral-wording pin: a NON-smith
// multi-output producer (Elizabeth Ellis at Ellis Farm: milk + meat + cheese)
// stands UNFOCUSED at her own workplace on shift — the same production-choice
// state smithChoosingAtForge pins for the blacksmith, but for a dairy/farm trade.
// The golden proves the cue and the wake warrant render trade-neutrally: the
// "## Time to produce" header, the "Choose what to produce next" menu, and the
// "It's time to produce — decide what to make next" warrant — NOT the blacksmith-only
// "forge" wording a dairywoman was wrongly shown (the live Elizabeth cheese scene
// 019f0969). Mirrors smithChoosingAtForge; byte-stable.
func dairyChoosingAtFarm() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		elizabethID = sim.ActorID("elizabeth")
		farm        = sim.StructureID("ellis_farm")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		WorkStructureID:   farm,
		InsideStructureID: farm,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"milk": 4, "cheese": 2},
		ProductionFocus:   "", // unfocused — the post-restart production-choice state
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 10},
			{Item: "cheese", Source: sim.RestockSourceProduce, Max: 8},
			{Item: "meat", Source: sim.RestockSourceProduce, Max: 6},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{elizabethID: elizabeth},
		Structures: map[sim.StructureID]*sim.Structure{
			farm: plainStructure(farm, "Ellis Farm"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"milk":   {OutputItem: "milk", OutputQty: 1, RateQty: 1, RatePerHours: 2, WholesalePrice: 1, RetailPrice: 2},
			"cheese": {OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 4, WholesalePrice: 2, RetailPrice: 4},
			"meat":   {OutputItem: "meat", OutputQty: 1, RateQty: 1, RatePerHours: 6, WholesalePrice: 3, RetailPrice: 6},
		},
	}
	warrants := []sim.WarrantMeta{
		{TriggerActorID: elizabethID, Reason: sim.ProductionChoiceWarrantReason{}, SourceEventID: 1},
	}
	return snap, elizabethID, warrants
}

// smithForgingFocused is the LLM-128 steady state: Ezekiel at his own forge on
// shift WITH a productive focus already set (nail, below cap) and NO production-
// choice warrant — the consistent state once he has chosen (shouldChooseProduction
// gates the warrant off for a productive focus, so no "decide what to make next").
// The "## Time to produce" cue leads with the "You are producing nails now — tend your
// post or call done()" steer instead of the choose menu, and the standing "You are
// making nail." self-state line renders. ItemKinds carry the singular/plural
// counting phrases (LLM-113) so the steer reads "nails", as the live catalog does.
// Byte-stable.
func smithForgingFocused() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 5},
		ProductionFocus:   "nail",
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge: plainStructure(forge, "Blacksmith"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
			"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nail":    {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategoryCraft},
			"skillet": {Name: "skillet", DisplayLabel: "Skillet", DisplayLabelSingular: "skillet", DisplayLabelPlural: "skillets", Category: sim.ItemCategoryCraft},
		},
	}
	return snap, ezekielID, nil
}

// smithOffWorkFocusHidden is the LLM-121 regression: the same multi-output crafter
// (Ezekiel, focus still nail) is NOT at his forge — he is at the Tavern after his
// shift. produce_tick makes nothing away from the workplace, so the standing
// "You are making nail." self-state line must NOT render (the live Tavern bug), and
// the "## Time to produce" choice cue is likewise gated off. Mirrors smithChoosingAtForge
// but with InsideStructureID = the tavern and off-shift, no production-choice warrant.
// No orders, no clock read → byte-stable.
func smithOffWorkFocusHidden() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
		tavern    = sim.StructureID("tavern")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 1140             // 19:00 — off shift, resting at the tavern
	published := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 5},
		ProductionFocus:   "nail",
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			tavern: plainStructure(tavern, "Tavern"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
			"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
	}
	return snap, ezekielID, nil
}

// smithBarteringAtTavern is the LLM-125 situation: Ezekiel, a smith carrying his
// own wares, stands in the Tavern in company with John Ellis the tavernkeeper —
// the live barter scene. Off his shift and away from the forge, so neither the
// "## Time to produce" cue nor the "You are making nail." line render; what DOES
// render is the new "## What your wares fetch" cue, valuing his own-trade goods
// (nail 1-2, skillet 5-10 from the recipe wholesale-retail spread) so a barter has
// a coin yardstick. Empty PriceBook → no recent-price clause; no orders, no clock
// read → byte-stable.
func smithBarteringAtTavern() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		johnID    = sim.ActorID("john")
		forge     = sim.StructureID("blacksmith")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 1140             // 19:00 — off shift, at the tavern
	published := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 5},
		ProductionFocus:   "nail",
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             267,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{ezekielID: {}, johnID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
			"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
	}
	return snap, ezekielID, nil
}

// keeperResellingInCompany is the LLM-191 reseller leg: Josiah Thorne keeps his
// general store on shift in company with a customer (Martha). His restock policy is
// all `buy` (cheese, milk) and he produces nothing, so the pre-LLM-191 wares-worth
// cue — gated to ProduceEntries() — rendered him NOTHING, leaving a reseller to name
// prices with no anchor (the live 0-coin, empty-sell-through Josiah). He holds
// bought-in stock and his buyer-side PriceBook carries this week's restock purchases
// (cheese 8 coins / 4 units = 2 each, milk 6 coins / 6 units = 1 each), so the
// extended cue values both goods off the recipe spread AND adds the cost-basis
// clause. No seller ring for him → no realized-sale clause.
func keeperResellingInCompany() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID = sim.ActorID("josiah")
		marthaID = sim.ActorID("martha")
		store    = sim.StructureID("general_store")
		supplier = sim.ActorID("ellis_farm")
		huddle   = sim.HuddleID("h1")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the store
	published := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   store,
		InsideStructureID: store,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"cheese": 4, "milk": 6},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "cheese", Source: sim.RestockSourceBuy, Max: 10},
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	martha := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Martha Bishop",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: store,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
	}
	// Josiah's buyer-side history: he restocked cheese and milk from the farm this
	// week. Keyed by the SELLER (supplier) ring; buyerRecentPurchases reads it by
	// obs.BuyerID == josiah, so the per-unit cost is 2 (cheese) and 1 (milk).
	cheeseBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	cheeseBuys.Push(sim.PriceObservation{BuyerID: josiahID, Amount: 8, Qty: 4, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	milkBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	milkBuys.Push(sim.PriceObservation{BuyerID: josiahID, Amount: 6, Qty: 6, Consumers: 1, At: published.Add(-1 * 24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah, marthaID: martha},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{josiahID: {}, marthaID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"cheese": {OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 3, RetailPrice: 6},
			"milk":   {OutputItem: "milk", OutputQty: 1, RateQty: 1, RatePerHours: 2, WholesalePrice: 1, RetailPrice: 3},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: supplier, Item: "cheese"}: cheeseBuys,
			{SellerID: supplier, Item: "milk"}:   milkBuys,
		},
	}
	return snap, josiahID, nil
}

// innkeeperPricingWithMakingsCost is the LLM-226 producer cost-of-goods leg: Hannah
// Boggs keeps her inn on shift in company with a guest, producing porridge from a
// recipe with REAL inputs (10 bowls from 3 milk + 5 water — the live catalog shape).
// Before LLM-226 the wares-worth cue gave a producer no cost anchor at all, so she
// could price below cost without knowing it (live: porridge quoted at 1 coin against
// an 0.8-coin makings cost). The golden pins the makings clause: with no purchase
// history the inputs price from catalog wholesale (3×1 + 5×1 = 8 a batch), and 8/10
// is spoken as "nearly 1 coin each" — rounded UP in prose, never down to a
// break-even-erasing "about 1". A fact with no pricing directive (LLM-227).
func innkeeperPricingWithMakingsCost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		guestID  = sim.ActorID("ezekiel")
		inn      = sim.StructureID("inn")
		huddle   = sim.HuddleID("h1")
	)
	start, end := 360, 1200 // 06:00-20:00 — the innkeeper day shift
	now := 480              // 08:00 — breakfast custom
	published := time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             10,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"porridge": 30},
		ProductionFocus:   "porridge",
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
		}},
	}
	guest := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             15,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah, guestID: guest},
		Structures: map[sim.StructureID]*sim.Structure{
			inn: plainStructure(inn, "Inn"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{hannahID: {}, guestID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"porridge": {OutputItem: "porridge", OutputQty: 10, RateQty: 8, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2,
				Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}}},
			"milk":  {OutputItem: "milk", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"water": {OutputItem: "water", OutputQty: 1, RateQty: 12, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
		},
	}
	return snap, hannahID, nil
}

// keeperNotPitchingMakersOwnWare is the LLM-171 seller side: John Ellis keeps
// his tavern (on shift, at post) co-present with Ezekiel Crane the smith, and
// John's stock includes skillet + nail he BOUGHT from Ezekiel. The "## Custom
// at hand" cue lists those goods to pitch, so the golden pins the producer-note
// line — "Ezekiel Crane makes nail and skillet themselves — don't pitch those
// back to their own maker" — that steers the keeper off selling a smith his own
// ware back (the live buy-back, where John read Ezekiel's sell-offer as a buy
// and quoted skillets at him). A co-present customer who makes none of the goods
// would draw no such note.
func keeperNotPitchingMakersOwnWare() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		johnID    = sim.ActorID("john")
		forge     = sim.StructureID("blacksmith")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1320 // 06:00–22:00, on shift in the evening
	now := 1140             // 19:00 — keeping the tavern, a customer present
	published := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	john := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "John Ellis",
		Role:               "tavernkeeper",
		State:              sim.StateIdle,
		WorkStructureID:    tavern,
		InsideStructureID:  tavern,
		ScheduleStartMin:   &start,
		ScheduleEndMin:     &end,
		CurrentHuddleID:    huddle,
		Coins:              267,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{},
		// Skillet + nail here came FROM Ezekiel — the reseller stock the cue would
		// otherwise pitch straight back at its maker.
		Inventory:     map[sim.ItemKind]int{"skillet": 4, "nail": 38},
		Acquaintances: map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 5},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{johnID: john, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{johnID: {}, ezekielID: {}}},
		},
	}
	return snap, johnID, nil
}

// makerOfferedOwnWareBuyQuote is the LLM-171 buyer side: Ezekiel Crane the smith
// (skillet at his cap of 5, which he MAKES) is co-present with John Ellis, who
// has posted a targeted skillet quote at him for 2 coins — the mis-pitched
// buy-back quote from the live trace. The golden pins that the quote warrant
// line withholds the actionable "pay_with_item with quote_id" take and instead
// steers "these are wares you make yourself … decline", so a mis-pitched quote
// can't close the buy-back loop. A quote for a good the buyer does NOT make
// keeps its normal take.
func makerOfferedOwnWareBuyQuote() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		johnID    = sim.ActorID("john")
		forge     = sim.StructureID("blacksmith")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 1140             // 19:00 — off shift, visiting the tavern
	published := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 5},
		Acquaintances:     map[string]sim.Acquaintance{"John Ellis": {}},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             267,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{ezekielID: {}, johnID: {}}},
		},
	}
	// John's targeted skillet quote at Ezekiel — the mis-pitched buy-back offer.
	warrants := []sim.WarrantMeta{
		{
			TriggerActorID: johnID,
			Reason: sim.SceneQuoteTargetedWarrantReason{
				QuoteID: 1, SellerID: johnID,
				Lines:  []sim.QuoteLine{{ItemKind: "skillet", Qty: 1}},
				Amount: 2,
			},
			SourceEventID: 1,
		},
	}
	return snap, ezekielID, warrants
}

// buyerOfferedQuoteTakeNamesTerms is the LLM-172 buyer side: John Ellis posts a
// targeted STEW quote at Ezekiel Crane — a good Ezekiel does NOT make and isn't
// at cap on — so the actionable take renders (unlike the maker buy-back above,
// which withholds it). Ezekiel is carrying 20 nails, the live trap: the prior
// take read "call pay_with_item with quote_id 1 and the same item, qty, and
// amount", and a buyer holding other goods bound "the same item" to a nail,
// dead-ended on the term-mismatch reject, and fell back to a bare pay that
// leaked coins for an undelivered stew with the quote still open. The golden
// pins that the take now names the concrete item/qty/amount ("item \"stew\",
// qty 1, and amount 4") so there is nothing to misbind. This is the ONLY golden
// exercising the single-line coin-quote actionable take.
func buyerOfferedQuoteTakeNamesTerms() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		johnID    = sim.ActorID("john")
		forge     = sim.StructureID("blacksmith")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 1140             // 19:00 — off shift, visiting the tavern
	published := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             25,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 20},
		Acquaintances:     map[string]sim.Acquaintance{"John Ellis": {}},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             267,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{ezekielID: {}, johnID: {}}},
		},
	}
	// John's targeted stew quote at Ezekiel — a good he buys, not makes, so the
	// actionable take renders.
	warrants := []sim.WarrantMeta{
		{
			TriggerActorID: johnID,
			Reason: sim.SceneQuoteTargetedWarrantReason{
				QuoteID: 1, SellerID: johnID,
				Lines:  []sim.QuoteLine{{ItemKind: "stew", Qty: 1}},
				Amount: 4,
			},
			SourceEventID: 1,
		},
	}
	return snap, ezekielID, warrants
}

// peersHoldingSameFood is the LLM-138 degenerate-buy scene: two hungry NPCs
// stand together, each already carrying the same food (stew). The live
// hud-6a887a… case had each told ONLY to BUY the other's blueberries — the
// degenerate cue that drove the hollow "I can offer thee blueberries" beats
// backed by no transaction. The golden pins that the satiation section shows
// the subject its OWN stock to eat but carries NO "offer to buy it from them"
// peer line, because buying a copy of food already in hand is pointless (the
// gatherCoPresentPeerOffers gate suppresses it).
func peersHoldingSameFood() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		lewisID   = sim.ActorID("lewis")
		commons   = sim.StructureID("commons")
		huddle    = sim.HuddleID("h1")
	)
	published := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Inventory:         map[sim.ItemKind]int{"stew": 3},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Inventory:         map[sim.ItemKind]int{"stew": 1},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			commons: plainStructure(commons, "Village Commons"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{ezekielID: {}, lewisID: {}}},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, ezekielID, nil
}

// coinlessWorkerAmongPeers is the LLM-153 situation: two laborers stand together in
// the commons and the one we render (Goodwife Bishop, a newcomer) has an empty purse.
// Live, 0-coin workers tried to BUY services from each other — the pay path rejected
// every attempt (engine/sim/pay_commands.go), but the model kept trying because the
// prompt showed "Coins in your purse: 0" without saying that meant it could not pay.
// The golden pins the consequence line the empty-purse case now renders in "## You".
// No needs, no clock-bound content → byte-stable.
func coinlessWorkerAmongPeers() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		bishopID = sim.ActorID("bishop")
		walkerID = sim.ActorID("walker")
		commons  = sim.StructureID("commons")
		huddle   = sim.HuddleID("h1")
	)
	published := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	bishop := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Goodwife Bishop",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	walker := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             6,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Goodwife Bishop": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{bishopID: bishop, walkerID: walker},
		Structures: map[sim.StructureID]*sim.Structure{
			commons: plainStructure(commons, "Village Commons"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{bishopID: {}, walkerID: {}}},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, bishopID, nil
}

// brokeEmployerCannotPayLaborOffer is the LLM-158 situation, reduced to its
// load-bearing parts: Lewis Walker (a worker) has solicited the subject (Ezekiel
// Crane) for a 5-coin job, but Ezekiel's purse is empty. accept_work's funds
// gate (buyerCanAfford, labor_commands.go) would flip the offer to
// failed_unavailable, so the cue must steer Ezekiel to decline_work WITH a spoken
// reason rather than present accept_work — otherwise he "accepts" verbally and
// the deal dies in silence (the live Lewis<->Ezekiel blacksmith dead-air). No
// needs, no clock-bound content → byte-stable.
func brokeEmployerCannotPayLaborOffer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		lewisID   = sim.ActorID("lewis")
		commons   = sim.StructureID("commons")
		huddle    = sim.HuddleID("h1")
	)
	published := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			commons: plainStructure(commons, "Village Commons"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{ezekielID: {}, lewisID: {}}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:          1,
				WorkerID:    lewisID,
				EmployerID:  ezekielID,
				Reward:      5,
				DurationMin: 60,
				State:       sim.LaborStatePending,
				HuddleID:    huddle,
			},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, ezekielID, nil
}

// workerEnRouteToWorkplace is the LLM-229 relocation self-state: Patience Walker
// (a worker) accepted a job for Josiah Thorne struck away from his General Store
// and is now on her way to his workplace — an EnRoute LaborOffer with her as the
// worker. She is not yet laboring (no Working offer, no laboring mirror), so the
// self-state must send her to the post and get her to work; and because she is
// already committed, the solicit affordance and the businesses directory must
// stay suppressed even though she is a worker. Solo, no clock-bound content →
// byte-stable.
func workerEnRouteToWorkplace() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		patienceID = sim.ActorID("patience")
		josiahID   = sim.ActorID("josiah")
		store      = sim.StructureID("store")
	)
	published := time.Date(2026, 7, 3, 11, 0, 0, 0, time.UTC)
	patience := &sim.ActorSnapshot{
		Kind:           sim.KindNPCShared,
		DisplayName:    "Patience Walker",
		Role:           "laborer",
		State:          sim.StateIdle,
		Coins:          0,
		Needs:          map[sim.NeedKey]int{},
		AttributeSlugs: []string{sim.AttrWorker},
		Acquaintances:  map[string]sim.Acquaintance{"Josiah Thorne": {}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		InsideStructureID: store,
		WorkStructureID:   store,
		Coins:             50,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{patienceID: patience, josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:          1,
				WorkerID:    patienceID,
				EmployerID:  josiahID,
				Reward:      1,
				RewardItems: []sim.ItemKindQty{{Kind: "cheese", Qty: 1}},
				DurationMin: 120,
				State:       sim.LaborStateEnRoute,
			},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, patienceID, nil
}

// inKindLaborOfferSnapshot builds the shared LLM-225 shape: Anne Walker has
// solicited Hannah Boggs for a job paid 1 porridge + 2 coins. employerHoldsGoods
// controls whether Hannah's inventory carries the porridge — true renders the
// both-legs decision line + normal footer (labor_offer_in_kind_reward), false
// the missing-goods decline steer (employer_missing_reward_items_steer).
func inKindLaborOfferSnapshot(employerHoldsGoods bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		anneID   = sim.ActorID("anne")
		inn      = sim.StructureID("inn")
		huddle   = sim.HuddleID("h1")
	)
	published := time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		InsideStructureID: inn,
		CurrentHuddleID:   huddle,
		Coins:             50,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Anne Walker": {}},
	}
	if employerHoldsGoods {
		hannah.Inventory = map[sim.ItemKind]int{"porridge": 3}
	}
	anne := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Anne Walker",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: inn,
		CurrentHuddleID:   huddle,
		Coins:             1,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Hannah Boggs": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah, anneID: anne},
		Structures: map[sim.StructureID]*sim.Structure{
			inn: plainStructure(inn, "The Inn"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{hannahID: {}, anneID: {}}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:          1,
				WorkerID:    anneID,
				EmployerID:  hannahID,
				Reward:      2,
				RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
				DurationMin: 120,
				State:       sim.LaborStatePending,
				HuddleID:    huddle,
			},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, hannahID, nil
}

func laborOfferInKindReward() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return inKindLaborOfferSnapshot(true)
}

func employerMissingRewardItemsSteer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return inKindLaborOfferSnapshot(false)
}

// employerWithWorkerOnJob is the LLM-202 employer-side cue fixture: John Ellis the
// tavernkeeper stands with Silence Walker, who is mid-contract for him (a Working
// labor offer with ~90 minutes left). The subject is the EMPLOYER, so the new
// "## Workers currently working for you" cue (renderWorkersForMe) renders — the
// mirror of the worker's Laboring self-state. WorkingUntil is anchored to the
// snapshot instant + 90m so the "about N left" line is byte-stable (RenderedAt =
// PublishedAt). The reward (2) renders in the owed clause.
func employerWithWorkerOnJob() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID    = sim.ActorID("john")
		silenceID = sim.ActorID("silence")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	published := time.Date(2026, 6, 30, 20, 30, 0, 0, time.UTC)
	workingUntil := published.Add(90 * time.Minute)
	acceptedAt := published.Add(-30 * time.Minute)
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Coins:             50,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		Role:              "laborer",
		State:             sim.StateLaboring,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"John Ellis": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{johnID: john, silenceID: silence},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{johnID: {}, silenceID: {}}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:           1,
				WorkerID:     silenceID,
				EmployerID:   johnID,
				Reward:       2,
				DurationMin:  120,
				State:        sim.LaborStateWorking,
				HuddleID:     huddle,
				AcceptedAt:   &acceptedAt,
				WorkingUntil: &workingUntil,
			},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, johnID, nil
}

// workerAmongHousehold is the LLM-157 situation: two worker-tagged Walker siblings
// (Lewis, the rendered subject, + Anne) share a home and stand together in it, both
// jobless. LLM-145 already hides the solicit_work tool when only kin are present,
// but the seek-work backstop warrant still nudged the model to ask the housemate for
// work as freeform speech. The golden pins the "## Around you" annotation that now
// names Anne as the subject's housemate. Small non-zero purses keep the empty-purse
// line out so the golden centers on the household annotation. No clock-bound content
// → byte-stable.
func workerAmongHousehold() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		lewisID = sim.ActorID("lewis")
		anneID  = sim.ActorID("anne")
		home    = sim.StructureID("walker-residence")
		huddle  = sim.HuddleID("h1")
	)
	published := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: home,
		HomeStructureID:   home,
		CurrentHuddleID:   huddle,
		AttributeSlugs:    []string{sim.AttrWorker},
		Coins:             2,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Anne Walker": {}},
	}
	anne := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Anne Walker",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: home,
		HomeStructureID:   home,
		CurrentHuddleID:   huddle,
		AttributeSlugs:    []string{sim.AttrWorker},
		Coins:             2,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{lewisID: lewis, anneID: anne},
		Structures: map[sim.StructureID]*sim.Structure{
			home: plainStructure(home, "Walker Residence"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{lewisID: {}, anneID: {}}},
		},
	}
	return snap, lewisID, nil
}

// keeperOffersRoomToCoinlessGuest is the LLM-136 host-side scene. John Ellis, the
// tavernkeeper, shares his tavern (one free private room at a live nightly rate)
// with Ezekiel Crane — a homeless smith with no home, no lodging grant, and 0
// coins, carrying only his own wares. The "## A room to let" cue fires; the golden
// pins the new goods-for-room clause, so a coinless guest is offered the room for
// goods (offer_trade → accept_pay) instead of being dead-ended on coins he doesn't
// have. This is the keeper side of the live livelock from LLM-136.
func keeperOffersRoomToCoinlessGuest() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		johnID    = sim.ActorID("john")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1320 // 06:00–22:00, on shift in the evening
	now := 1140             // 19:00 — a guest seeking a bed for the night
	published := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             267,
		Needs:             map[sim.NeedKey]int{},
	}
	// No HomeStructureID and no RoomAccess → a structural lodging-seeker. 0 coins
	// with wares on hand is the whole point: the goods path is his only way in.
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"skillet": 4, "nail": 27},
	}
	snap := &sim.Snapshot{
		PublishedAt:              published,
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.NeedThresholds{},
		LodgingDefaultWeeklyRate: 28, // → 4 coins/night
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{johnID: john, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: {ID: tavern, DisplayName: "Tavern", Rooms: []*sim.Room{
				{ID: 1, StructureID: tavern, Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			}},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{johnID: {}, ezekielID: {}}},
		},
	}
	return snap, johnID, nil
}

// homedGuestLodgingQuoteSuppressed is the LLM-208 buyer side: John Ellis posts a
// targeted nights_stay (room) quote at Prudence Ward, but Prudence HAS a home
// (Ward Residence). A homed guest can't take a room — the buyer-side
// pay_with_item guard rejects it (LLM-182) — so surfacing the offer only pulls
// her into a doomed nightly negotiation (the live John↔Prudence tavern loop).
// The golden pins that the room-offer take is SUPPRESSED for her:
// filterHomedLodgingQuoteWarrants drops the lodging quote warrant at build, so
// the assembled prompt carries no "offers you … nights_stay" / pay_with_item
// take line. Contrast keeper_offers_room_to_coinless_guest (a HOMELESS seeker,
// who correctly DOES get the offer). TestHomedGuestLodgingQuoteSuppressed pins
// that clearing her home restores the take — proving the home is the sole cause.
func homedGuestLodgingQuoteSuppressed() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		prudenceID = sim.ActorID("prudence")
		johnID     = sim.ActorID("john")
		tavern     = sim.StructureID("tavern")
		wardHome   = sim.StructureID("ward_residence")
		huddle     = sim.HuddleID("h1")
	)
	now := 1140 // 19:00 — visiting the tavern in the evening
	published := time.Date(2026, 6, 30, 19, 0, 0, 0, time.UTC)
	// Prudence has a home (Ward Residence) and no lodging grant — the homed guest.
	prudence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Prudence Ward",
		Role:              "villager",
		State:             sim.StateIdle,
		InsideStructureID: tavern,
		HomeStructureID:   wardHome,
		CurrentHuddleID:   huddle,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"John Ellis": {}},
	}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Coins:             267,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:              published,
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.NeedThresholds{},
		LodgingDefaultWeeklyRate: 28, // → 4 coins/night
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
		},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{prudenceID: prudence, johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern:   {ID: tavern, DisplayName: "Tavern", Rooms: []*sim.Room{{ID: 1, StructureID: tavern, Kind: sim.RoomKindPrivate, Name: "bedroom_1"}}},
			wardHome: plainStructure(wardHome, "Ward Residence"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{prudenceID: {}, johnID: {}}},
		},
	}
	// John's targeted nights_stay quote at Prudence — the room offer she can't take.
	warrants := []sim.WarrantMeta{
		{
			TriggerActorID: johnID,
			Reason: sim.SceneQuoteTargetedWarrantReason{
				QuoteID: 1, SellerID: johnID,
				Lines:  []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}},
				Amount: 4,
			},
			SourceEventID: 1,
		},
	}
	return snap, prudenceID, warrants
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

// worklessTiredRejoinerSelfActionTrail is the LLM-217 fixture: the live Patience
// Walker oscillation, mid-loop. She is workless (no work structure), tired, and
// back in the Tavern huddle with John Ellis after two announce-leave-return
// cycles. The huddle ring holds John's two byte-identical re-greetings plus her
// own "I'll head home now." — with At stamps spanning the cycles — and the
// snapshot's ActionLog carries her consumed/departed/arrived trail (plus one of
// John's arrivals, which the subject filter must drop, and one of her own spoke
// entries in the CURRENT huddle, which the ring de-dup must keep out of the
// trail). Fixed PublishedAt, utterances and log entries stamped relative to it
// → byte-stable.
func worklessTiredRejoinerSelfActionTrail() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		patienceID = sim.ActorID("patience")
		johnID     = sim.ActorID("john")
		tavern     = sim.StructureID("tavern")
		home       = sim.StructureID("walker_residence")
		huddleID   = sim.HuddleID("tavern_huddle")
	)
	start, end := 360, 1260 // John's working hours 06:00–21:00
	now := 15*60 + 50       // 15:50 — afternoon, John on shift
	published := time.Date(2026, 7, 1, 19, 50, 0, 0, time.UTC)
	ago := func(sec int) time.Time { return published.Add(-time.Duration(sec) * time.Second) }
	patience := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Patience Walker",
		Role:              "villager",
		State:             sim.StateIdle,
		HomeStructureID:   home,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddleID,
		Coins:             3,
		Needs:             map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold},
	}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddleID,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{patienceID: patience, johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
			home:   plainStructure(home, "Walker Residence"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {
				ID:      huddleID,
				Members: map[sim.ActorID]struct{}{patienceID: {}, johnID: {}},
				RecentUtterances: []sim.Utterance{
					{SpeakerID: johnID, SpeakerName: "John Ellis", Text: "Welcome back to the tavern, Patience!", At: ago(170)},
					{SpeakerID: patienceID, SpeakerName: "Patience Walker", Text: "I'll head home now.", At: ago(150)},
					{SpeakerID: johnID, SpeakerName: "John Ellis", Text: "Welcome back to the tavern, Patience!", At: ago(8)},
				},
			},
		},
		ActionLog: []sim.ActionLogEntry{
			{Seq: 1, ActorID: patienceID, OccurredAt: ago(480), ActionType: sim.ActionTypeConsumed, Text: "carrot", HuddleID: huddleID},
			{Seq: 2, ActorID: patienceID, OccurredAt: ago(420), ActionType: sim.ActionTypeDeparted, Text: "Tavern"},
			// John's own arrival — the subject filter drops it from HER trail.
			{Seq: 3, ActorID: johnID, OccurredAt: ago(300), ActionType: sim.ActionTypeWalked, Text: "Tavern"},
			{Seq: 4, ActorID: patienceID, OccurredAt: ago(240), ActionType: sim.ActionTypeWalked, Text: "Tavern"},
			// Her announce line, spoken IN the current huddle — the ring above
			// renders it, so the self-action trail must NOT repeat it.
			{Seq: 5, ActorID: patienceID, OccurredAt: ago(150), ActionType: sim.ActionTypeSpoke, Text: "I'll head home now.", HuddleID: huddleID},
			{Seq: 6, ActorID: patienceID, OccurredAt: ago(130), ActionType: sim.ActionTypeDeparted, Text: "Tavern"},
			{Seq: 7, ActorID: patienceID, OccurredAt: ago(45), ActionType: sim.ActionTypeWalked, Text: "Tavern"},
		},
	}
	return snap, patienceID, nil
}

// sharedNpcWithSoul is the LLM-199 case: a shared-VA keeper standing at her own
// post during working hours, carrying a synthesized about_me soul. The golden
// pins that "## Who you are" renders that prose (the empty-block fix) rather
// than a bare header — the render now emits AboutMe, gated by a non-empty value.
func sharedNpcWithSoul() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		inn      = sim.StructureID("wayfarer_inn")
	)
	start, end := 360, 1260 // working hours 06:00–21:00
	now := 540              // 09:00 — on shift
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Hannah",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Narrative: &sim.NarrativeState{
			AboutMe: "I am Hannah, keeper of the Wayfarer Inn. My days run to the rhythm of the hearth and the door — I take a quiet pride in a warm room and a fair reckoning, and I have come to know the regulars by their thirst.",
		},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah},
		Structures: map[sim.StructureID]*sim.Structure{
			inn: plainStructure(inn, "Wayfarer Inn"),
		},
	}
	warrants := []sim.WarrantMeta{
		{
			TriggerActorID: hannahID,
			Reason:         sim.ArrivalWarrantReason{AtStructureID: inn},
			SourceEventID:  1,
		},
	}
	return snap, hannahID, warrants
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

// wearyResidentInOwnHome is the LLM-214 fixture: a weary salem-vendor standing
// INSIDE its own home (home != work), off-shift in the evening. Before the fix the
// "## How you can rest" list handed it the home structure_id as a move_to target
// ("sleep in your own bed (structure_id …)") for the structure it was already in —
// the no-op move Lewis / Anne Walker looped on — and the anchor pointer told it to
// "head back there whenever you wish". The golden pins the in-place cues: the rest
// section leads with the RestAtHome take_break bullet (no home id), and the anchor
// states "You're home" while keeping only the workplace as a reachable move target.
// Off-shift and already home, so no to-work / wind-down steer clutters the pin.
func wearyResidentInOwnHome() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		anneID = sim.ActorID("anne")
		home   = sim.StructureID("walker_residence")
		garden = sim.StructureID("walker_garden")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 1200             // 20:00 — off shift, home for the evening
	anne := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Anne Walker",
		Role:              "gardener",
		State:             sim.StateIdle,
		WorkStructureID:   garden,
		HomeStructureID:   home,
		InsideStructureID: home,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{"tiredness": 23},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{anneID: anne},
		Structures: map[sim.StructureID]*sim.Structure{
			home:   plainStructure(home, "Walker Residence"),
			garden: plainStructure(garden, "Walker Garden"),
		},
	}
	return snap, anneID, nil
}

// homedWorkerEveningTavernOpen is the LLM-149 (Lever 2) positive case: a homed
// day-shift agent, off-shift and awake in the evening window [shift-end, 22:00),
// standing at its workplace after closing up. The evening "tavern's open" cue
// fires in ## Around you, and the off-shift go-home wind-down steer is suppressed
// in-window so the cue is the single voice. No co-present actor, no orders — the
// evening invitation is the point.
func homedWorkerEveningTavernOpen() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
		home      = sim.StructureID("crane_cottage")
		tavern    = sim.StructureID("tavern")
	)
	start, end := 420, 1140 // 07:00–19:00
	now := 1230             // 20:30 — off shift, inside the evening window
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		HomeStructureID:   home,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:     &now,
		LodgingBedtimeMinute: 1320, // 22:00 — the evening window's close
		NeedThresholds:       sim.NeedThresholds{},
		Actors:               map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			home:   plainStructure(home, "Crane Cottage"),
			tavern: plainStructure(tavern, "the Tavern"),
		},
		// The tavern venue: a VillageObject tagged "tavern" bridged to the
		// same-id Structure (the shared-identity bridge nearestTaggedVenue reads).
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(tavern): {Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{X: 0, Y: 0}},
		},
	}
	return snap, ezekielID, nil
}

// homedWorkerEveningTooBrokeForTavern is the LLM-205 rule-1 case: the same homed
// day-shift agent as homedWorkerEveningTavernOpen, in the evening window, but too
// broke to afford the tavern's cheapest drink (2 coins; the co-located keeper sells
// ale at retail 3). canAffordLeisure fails, so the agent is NOT in evening leisure:
// no tavern invitation, and the off-shift go-home wind-down steer resumes (the broke
// have no evening). No needs / no PriceBook / no orders → byte-stable.
func homedWorkerEveningTooBrokeForTavern() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		keeperID  = sim.ActorID("innkeep")
		forge     = sim.StructureID("blacksmith")
		home      = sim.StructureID("crane_cottage")
		tavern    = sim.StructureID("tavern")
	)
	start, end := 420, 1140 // 07:00–19:00
	now := 1230             // 20:30 — off shift, inside the evening window
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		HomeStructureID:   home,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             2, // below the tavern's cheapest drink (ale, retail 3)
		Needs:             map[sim.NeedKey]int{},
	}
	keeper := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		Inventory:         map[sim.ItemKind]int{"ale": 5},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:     &now,
		LodgingBedtimeMinute: 1320, // 22:00 — the evening window's close
		NeedThresholds:       sim.NeedThresholds{},
		Actors:               map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, keeperID: keeper},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			home:   plainStructure(home, "Crane Cottage"),
			tavern: plainStructure(tavern, "the Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(tavern): {Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{X: 0, Y: 0}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"ale": {OutputItem: "ale", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 3},
		},
	}
	return snap, ezekielID, nil
}

// homedWorkersEveningCommonsNoSolicit is the LLM-205 rule-2 case: two homed
// day-shift workers (different homes + trades, so solicitable to each other) off
// shift in the evening window, together at the Commons — neither at home nor the
// tavern, so the evening cue still fires. The subject carries AttrWorker and is flush
// enough for a drink (10 coins, ale retail 3), so it is in evening leisure: the
// solicit-work affordance is suppressed even though a solicitable peer is present.
// Without the gate, an employed worker with a solicitable audience would be offered
// solicit_work — so this pins that evening leisure replaces the hustle. Fixed
// PublishedAt, no orders/PriceBook → byte-stable.
func homedWorkersEveningCommonsNoSolicit() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		lewisID   = sim.ActorID("lewis")
		keeperID  = sim.ActorID("innkeep")
		forge     = sim.StructureID("blacksmith")
		farm      = sim.StructureID("walker_farm")
		ezHome    = sim.StructureID("crane_cottage")
		lwHome    = sim.StructureID("walker_house")
		commons   = sim.StructureID("commons")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 420, 1140 // 07:00–19:00
	now := 1230             // 20:30 — off shift, inside the evening window
	published := time.Date(2026, 6, 30, 20, 30, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		HomeStructureID:   ezHome,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             10, // affords ale (retail 3); below the comfort ceiling (25)
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "farmer",
		State:             sim.StateIdle,
		WorkStructureID:   farm,
		HomeStructureID:   lwHome,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             8,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	keeper := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		Inventory:         map[sim.ItemKind]int{"ale": 5},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:          published,
		LocalMinuteOfDay:     &now,
		LodgingBedtimeMinute: 1320,
		NeedThresholds:       sim.NeedThresholds{},
		Actors:               map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, lewisID: lewis, keeperID: keeper},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:   plainStructure(forge, "Blacksmith"),
			farm:    plainStructure(farm, "Walker Farm"),
			ezHome:  plainStructure(ezHome, "Crane Cottage"),
			lwHome:  plainStructure(lwHome, "Walker House"),
			commons: plainStructure(commons, "Village Commons"),
			tavern:  plainStructure(tavern, "the Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(tavern): {Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{X: 0, Y: 0}},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{ezekielID: {}, lewisID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"ale": {OutputItem: "ale", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 3},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, ezekielID, nil
}

// keeperWithReadyOrder is an order-bearing scenario unblocked by the LLM-106
// render-clock fix: Hannah, an innkeeper, holds a Ready order (a nights_stay
// check-in) for a co-present guest. The order's ExpiresAt is anchored to the
// snapshot instant (PublishedAt → RenderedAt), so the "expires in N minutes"
// clause renders deterministically — byte-stable with no wall-clock read.
func keeperWithReadyOrder() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		guestID  = sim.ActorID("jeff")
		inn      = sim.StructureID("hannahs_inn")
		huddle   = sim.HuddleID("h1")
	)
	start, end := 360, 1320 // 06:00–22:00
	nowMin := 600           // 10:00, on shift
	// The render instant: ExpiresAt is set relative to this, so the expiry clause
	// is fixed regardless of when the test runs.
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddle,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
	}
	guest := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Jeff",
		Role:              "traveler",
		State:             sim.StateIdle,
		InsideStructureID: inn,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalDateUTC:     time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC),
		LocalMinuteOfDay: &nowMin,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah, guestID: guest},
		Structures: map[sim.StructureID]*sim.Structure{
			inn: plainStructure(inn, "Hannah's Inn"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{hannahID: {}, guestID: {}}},
		},
		Orders: map[sim.OrderID]*sim.Order{
			1: {
				ID:          1,
				State:       sim.OrderStateReady,
				SellerID:    hannahID,
				BuyerID:     guestID,
				Item:        "nights_stay",
				Qty:         1,
				ConsumerIDs: []sim.ActorID{guestID},
				CreatedAt:   published.Add(-2 * time.Minute),
				ExpiresAt:   published.Add(8 * time.Minute),
			},
		},
	}
	return snap, hannahID, nil
}

// operatingKeeperSnapshot builds a one-actor snapshot for the LLM-123 operating-
// hours cue: a homed shopkeeper standing inside his own store, with the given local
// minute (on/off shift) and an optional live stay_open commitment. No co-present
// actors, no recipes, no orders → no forge/wares/stall cue and byte-stable. The
// trade-conduct block ("How you trade:") renders iff the keeper is operating —
// on shift, or off-shift with stayOpen — which is exactly what the three scenarios
// below and the cross-scenario invariant exercise.
func operatingKeeperSnapshot(nowMin int, stayOpen bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		keeperID = sim.ActorID("moses")
		store    = sim.StructureID("general_store")
		home     = sim.StructureID("james_farm")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := nowMin
	// PublishedAt's wall-clock tracks the local minute so the stay_open OpenUntil
	// (set relative to it) is internally consistent; fixed date → byte-stable.
	published := time.Date(2026, 6, 25, nowMin/60, nowMin%60, 0, 0, time.UTC)
	moses := &sim.ActorSnapshot{
		Kind:               sim.KindNPCShared,
		DisplayName:        "Moses James",
		Role:               "shopkeeper",
		State:              sim.StateIdle,
		WorkStructureID:    store,
		InsideStructureID:  store,
		HomeStructureID:    home,
		ScheduleStartMin:   &start,
		ScheduleEndMin:     &end,
		BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
		Coins:              20,
		Needs:              map[sim.NeedKey]int{},
	}
	if stayOpen {
		ou := published.Add(2 * time.Hour) // committed to keep the store open until ~1am
		moses.OpenUntil = &ou
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{keeperID: moses},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
			home:  plainStructure(home, "James Farm"),
		},
	}
	return snap, keeperID, nil
}

// keeperAtPostOnShift: keeper at his own store during business hours → the
// "How you trade:" block renders (LLM-123 positive case).
func keeperAtPostOnShift() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return operatingKeeperSnapshot(600, false) // 10:00 — on shift, open for trade
}

// keeperAtClosedPostOffshiftNight: keeper at his own CLOSED store late at night,
// off shift → the trade block is gone (the LLM-123 fix); the off-shift wind-down
// "head home" steer renders instead.
func keeperAtClosedPostOffshiftNight() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return operatingKeeperSnapshot(1380, false) // 23:00 — off shift, stall closed
}

// keeperStayingOpenOffshift: keeper off shift at night but holding a live stay_open
// commitment → the trade block renders despite being off-shift (the operating-hours
// gate opens on a stay_open commitment too), and the routine wind-down is suppressed.
func keeperStayingOpenOffshift() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return operatingKeeperSnapshot(1380, true) // 23:00 — off shift but committed to stay open
}

// brokeWorkerNoEmployerSeeksWork builds the live LLM-160 situation: a broke
// salem-vendor worker (Lewis Walker) idle at home with no employer present. Drives
// the standing seek-work directory + the "go now" coda — see the scenario summary.
func brokeWorkerNoEmployerSeeksWork() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		lewisID   = sim.ActorID("lewis")
		residence = sim.StructureID("walker_residence")
		inn       = sim.StructureID("inn")
		store     = sim.StructureID("general_store")
	)
	now := 540 // 09:00 — daytime
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		InsideStructureID: residence,
		HomeStructureID:   residence,
		Coins:             0,
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			residence: plainStructure(residence, "Walker Residence"),
			inn:       plainStructure(inn, "Inn"),
			store:     plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(inn):   {ID: sim.VillageObjectID(inn), Tags: []string{"business", "lodging"}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), Tags: []string{"business", "shop"}},
		},
	}
	return snap, lewisID, nil
}

// brokeWorkerSeeksWorkSkipsShutBusiness is the LLM-155 companion to
// brokeWorkerNoEmployerSeeksWork: the same broke idle worker, but he carries an
// earned ObservedClosed memory of the Inn (found shut an hour ago, within the 4h
// TTL). The golden pins that the directory DROPS the remembered-shut Inn entirely
// — not annotates it — and lists only the open General Store with its qualitative
// distance + direction. Positions are set so the kept entry renders "a short walk
// east"; the Inn's position is irrelevant since it is dropped.
func brokeWorkerSeeksWorkSkipsShutBusiness() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		lewisID   = sim.ActorID("lewis")
		residence = sim.StructureID("walker_residence")
		inn       = sim.StructureID("inn")
		store     = sim.StructureID("general_store")
	)
	now := 540 // 09:00 — daytime
	published := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		InsideStructureID: residence,
		HomeStructureID:   residence,
		Pos:               sim.WorldToTile(0, 0),
		Coins:             0,
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: inn, Condition: sim.ObservedClosed}: published.Add(-time.Hour),
		}),
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			residence: plainStructure(residence, "Walker Residence"),
			inn:       plainStructure(inn, "Inn"),
			store:     plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(inn):   {ID: sim.VillageObjectID(inn), Pos: sim.WorldPos{X: 0, Y: 160}, Tags: []string{"business", "lodging"}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), Pos: sim.WorldPos{X: 160, Y: 0}, Tags: []string{"business", "shop"}},
		},
	}
	return snap, lewisID, nil
}

// workerWithCoinNoEmployerSeeksWork is the LLM-168 live case: a WORKLESS worker
// (Silence Walker — worker attribute, no work_structure_id) idle at home holding a
// few coins, no employer present. The same fixture as brokeWorkerNoEmployerSeeksWork
// but with coins: under the old broke (Coins==0) gate she got no directory; LLM-168
// re-anchored eligibility on workless, so the standing seek-work directory + "go now"
// coda fire whether or not she holds coin.
func workerWithCoinNoEmployerSeeksWork() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		silenceID = sim.ActorID("silence")
		residence = sim.StructureID("walker_residence")
		inn       = sim.StructureID("inn")
		store     = sim.StructureID("general_store")
	)
	now := 540 // 09:00 — daytime
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		State:             sim.StateIdle,
		InsideStructureID: residence,
		HomeStructureID:   residence,
		Coins:             15, // holds coin, but workless → still directed to seek work
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{silenceID: silence},
		Structures: map[sim.StructureID]*sim.Structure{
			residence: plainStructure(residence, "Walker Residence"),
			inn:       plainStructure(inn, "Inn"),
			store:     plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(inn):   {ID: sim.VillageObjectID(inn), Tags: []string{"business", "lodging"}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), Tags: []string{"business", "shop"}},
		},
	}
	return snap, silenceID, nil
}

// comfortableWorkerNoSeekWork is the LLM-194 case: the SAME workless worker as
// workerWithCoinNoEmployerSeeksWork, but holding coin AT/ABOVE the seek-work ceiling
// (40 >= the default 25). The snapshot is built directly, so SeekWorkCoinCeiling is 0
// and subjectIsComfortable resolves it to the default — the worker reads as comfortable,
// so the golden pins that it gets NEITHER the businesses directory NOR the "call move_to
// now" go-coda: a coin-rich worker stops hustling and is left to idle/consume. The
// negative counterpart of worker_with_coin_no_employer_seeks_work (same actor, 15 coins,
// still seeks).
func comfortableWorkerNoSeekWork() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		silenceID = sim.ActorID("silence")
		residence = sim.StructureID("walker_residence")
		inn       = sim.StructureID("inn")
		store     = sim.StructureID("general_store")
	)
	now := 540 // 09:00 — daytime
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		State:             sim.StateIdle,
		InsideStructureID: residence,
		HomeStructureID:   residence,
		Coins:             40, // at/above the default seek-work ceiling (25) → comfortable
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{silenceID: silence},
		Structures: map[sim.StructureID]*sim.Structure{
			residence: plainStructure(residence, "Walker Residence"),
			inn:       plainStructure(inn, "Inn"),
			store:     plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(inn):   {ID: sim.VillageObjectID(inn), Tags: []string{"business", "lodging"}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), Tags: []string{"business", "shop"}},
		},
	}
	return snap, silenceID, nil
}

// workerSeeksWorkAfterEmployerDeclines is the LLM-181 live case (Lewis Walker at the
// General Store, hud-8db08741…), reduced to its load-bearing parts: a workless worker
// shares a huddle with a co-present stranger employer (Josiah Thorne) who has already
// declined his labor offer. The declined ledger entry is what flips
// hasSolicitableAudience to false, so SeekWorkPlaces populates and the seek-work
// off-ramp ("call move_to now") arms even though an employer is physically present —
// the fix that frees the worker from re-soliciting the same refusal. No needs, the
// offer is terminal (no clock-bound content) → byte-stable.
func workerSeeksWorkAfterEmployerDeclines() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		lewisID   = sim.ActorID("lewis")
		josiahID  = sim.ActorID("josiah")
		residence = sim.StructureID("walker_residence")
		thorne    = sim.StructureID("thorne_house")
		commons   = sim.StructureID("commons")
		inn       = sim.StructureID("inn")
		store     = sim.StructureID("general_store")
		huddle    = sim.HuddleID("h1")
	)
	now := 540 // 09:00 — daytime, on shift
	published := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "laborer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		HomeStructureID:   residence,
		CurrentHuddleID:   huddle,
		Coins:             0,
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Josiah Thorne": {}},
	}
	// Josiah is a structural stranger to Lewis (different home; Lewis is workless so
	// they never share a workplace) — solicitable by anchor, excluded only by the decline.
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		HomeStructureID:   thorne,
		WorkStructureID:   store,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{lewisID: lewis, josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			commons:   plainStructure(commons, "Village Commons"),
			residence: plainStructure(residence, "Walker Residence"),
			thorne:    plainStructure(thorne, "Thorne House"),
			inn:       plainStructure(inn, "Inn"),
			store:     plainStructure(store, "General Store"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{lewisID: {}, josiahID: {}}},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(inn):   {ID: sim.VillageObjectID(inn), Tags: []string{"business", "lodging"}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), Tags: []string{"business", "shop"}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:          1,
				WorkerID:    lewisID,
				EmployerID:  josiahID,
				Reward:      10,
				DurationMin: 60,
				State:       sim.LaborStateDeclined,
				HuddleID:    huddle,
			},
		},
	}
	return snap, lewisID, nil
}

// workerSeeksWorkSkipsNoHiringBusiness is the LLM-210 companion to
// brokeWorkerSeeksWorkSkipsShutBusiness: the same workless idle worker (Lewis Walker),
// but he last found the General Store's keeper on a break — an earned ObservedNoHiring
// memory within its 2h TTL — where the keeper was PRESENT (so the store is NOT
// remembered shut) yet could not take him on. The seek-work directory drops the
// no-hiring store and lists only the open Blacksmith, steering him to a business with
// an available keeper instead of looping back to the resting one.
func workerSeeksWorkSkipsNoHiringBusiness() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		lewisID    = sim.ActorID("lewis")
		residence  = sim.StructureID("walker_residence")
		store      = sim.StructureID("general_store")
		blacksmith = sim.StructureID("blacksmith")
	)
	now := 540 // 09:00 — daytime
	published := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		InsideStructureID: residence,
		HomeStructureID:   residence,
		Pos:               sim.WorldToTile(0, 0),
		Coins:             0,
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: store, Condition: sim.ObservedNoHiring}: published.Add(-30 * time.Minute),
		}),
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			residence:  plainStructure(residence, "Walker Residence"),
			store:      plainStructure(store, "General Store"),
			blacksmith: plainStructure(blacksmith, "Blacksmith"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store):      {ID: sim.VillageObjectID(store), Pos: sim.WorldPos{X: 160, Y: 0}, Tags: []string{"business", "shop"}},
			sim.VillageObjectID(blacksmith): {ID: sim.VillageObjectID(blacksmith), Pos: sim.WorldPos{X: 0, Y: 160}, Tags: []string{"business", "shop"}},
		},
	}
	return snap, lewisID, nil
}

// redTiredWorkerNoSeekWork is the LLM-210 case: a WORKLESS worker (Lewis Walker) idle at
// home holding a few coins (15, below the seek-work ceiling → not comfortable) but at RED
// tiredness (20 >= the default red-line 16). A red need outranks job-hunting, so both
// seek-work gates suppress — the businesses directory and the "call move_to now" go-coda
// are gone and the weariness cue is left to win. The rested counterpart is
// worker_with_coin_no_employer_seeks_work (same workless coin-holder, not red → still seeks).
func redTiredWorkerNoSeekWork() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		lewisID   = sim.ActorID("lewis")
		residence = sim.StructureID("walker_residence")
		store     = sim.StructureID("general_store")
	)
	now := 540 // 09:00 — daytime
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		InsideStructureID: residence,
		HomeStructureID:   residence,
		Coins:             15, // below the seek-work ceiling (25) → not comfortable
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{"tiredness": 20}, // red: >= the default red-line (16)
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			residence: plainStructure(residence, "Walker Residence"),
			store:     plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), Tags: []string{"business", "shop"}},
		},
	}
	return snap, lewisID, nil
}

// TestSeekWorkDirectiveOnlyForWorklessWorker is the LLM-160/155/168 cross-scenario
// invariant: the decisive "call move_to now" go-coda appears in EXACTLY the
// workless-worker-no-employer scenarios and nowhere else in the matrix. A regression
// that re-gated the directory on a warrant, that restored the Coins==0 gate (dropping
// the coin-holding worker_with_coin scenario), or that let another scenario trip the
// workless-worker-with-no-employer condition, would flip a cell here.
func TestSeekWorkDirectiveOnlyForWorklessWorker(t *testing.T) {
	const marker = "call move_to now"
	seekWorkScenarios := map[string]bool{
		"broke_worker_no_employer_seeks_work":         true,
		"broke_worker_seeks_work_skips_shut_business": true,
		"worker_with_coin_no_employer_seeks_work":     true,
		"worker_seeks_work_after_employer_declines":   true,
		"worker_seeks_work_skips_no_hiring_business":  true,
	}
	for _, sc := range perceptionScenarios {
		want := seekWorkScenarios[sc.name]
		got := strings.Contains(renderScenario(sc), marker)
		if got != want {
			t.Errorf("scenario %q: seek-work go-coda present = %v, want %v", sc.name, got, want)
		}
	}
}

// TestSeekWorkSuppressedByRedNeed is the LLM-210 cross-scenario invariant: a red need
// outranks job-hunting, so the SAME workless worker gets the businesses directory when
// rested but NOT when red-tired. Flipping only tiredness across the red-line toggles the
// directory, proving the gate is the need itself and not some other fixture difference. A
// regression that dropped the hasRedNeed gate would leave the directory present in both.
func TestSeekWorkSuppressedByRedNeed(t *testing.T) {
	const directoryMarker = "offer your labor"
	render := func(tiredness int) string {
		return renderScenario(perceptionScenario{
			name: "redneed_flip",
			build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
				snap, id, warrants := redTiredWorkerNoSeekWork()
				snap.Actors[id].Needs["tiredness"] = tiredness
				return snap, id, warrants
			},
		})
	}
	if strings.Contains(render(20), directoryMarker) {
		t.Errorf("red-tired workless worker: seek-work directory present, want absent")
	}
	if !strings.Contains(render(0), directoryMarker) {
		t.Errorf("rested workless worker: seek-work directory absent, want present")
	}
}
