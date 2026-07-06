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

// TestGoldensTendNeedYieldsToEating is the LLM-276 cross-scenario invariant: whenever
// a tend-need warrant is present (the seek-work backstop redirected a workless idle
// worker with a resolvable hunger/thirst to eat), the rendered prompt must carry the
// tend-need felt pull and must NOT carry the go-earn seek-work directive — eating
// outranks job-hunting exactly as it does for a red need. Runs over the whole matrix so
// no future cue can reintroduce the labor directive for a redirected-to-eat worker.
func TestGoldensTendNeedYieldsToEating(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			_, _, warrants := sc.build()
			tendNeed := false
			for _, wm := range warrants {
				if _, ok := wm.Reason.(sim.TendNeedWarrantReason); ok {
					tendNeed = true
					break
				}
			}
			if !tendNeed {
				return // invariant N/A — no tend-need redirect in this scenario
			}
			out := renderScenario(sc)
			if strings.Contains(out, "offer your labor") {
				t.Errorf("scenario %q: tend-need active but the prompt still shows the seek-work go-earn line — eating must outrank job-hunting (LLM-276)", sc.name)
			}
			if !strings.Contains(out, "the means to see to it") {
				t.Errorf("scenario %q: tend-need active but the prompt lacks the tend-need felt pull (LLM-276)", sc.name)
			}
		})
	}
}

// TestGoldensSettledCloseNamesTheOffer is the LLM-296 cross-scenario invariant:
// every "## Recently settled offers" CLOSE line ("didn't go through") must name
// what the buyer OFFERED ("Your offer of ..."), never the thin want-item-only
// line that let two declines render byte-identically — so the standing "never
// repeat what you said" instruction had nothing to bind to and the model
// re-posted the same bundle. Runs over the whole matrix so a future edit to the
// close copy can't drop the offered bundle for any situation.
func TestGoldensSettledCloseNamesTheOffer(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			for _, line := range strings.Split(renderScenario(sc), "\n") {
				if strings.Contains(line, "didn't go through") && !strings.Contains(line, "Your offer of ") {
					t.Errorf("scenario %q: a settled-offer close names no offered bundle (LLM-296):\n%s", sc.name, line)
				}
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

// TestGoldensLaboringPeerNotAnOfferTarget is the LLM-231 cross-scenario invariant: a
// co-present huddle peer fulfilling a hired job (a Working LaborOffer with the peer as
// worker) is never surfaced as an offerable customer — a worker mid-job is not a valid
// sale target. The laboring set is recomputed from the raw LaborLedger (laboringOfferFor),
// NOT from HuddleMember.Laboring, so it independently asserts buildOfferableCustomers drops
// them rather than pinning the cue against its own flag. Requires the matrix to exercise
// the exclusion at least once (a seller scenario with a laboring co-present peer).
func TestGoldensLaboringPeerNotAnOfferTarget(t *testing.T) {
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		if p.OfferableCustomers == nil {
			continue // subject isn't a seller with co-present customers — invariant N/A here
		}
		for _, m := range p.Surroundings.HuddleMembers {
			if laboringOfferFor(snap, m.ID) == nil {
				continue
			}
			exercised = true
			label := descriptorLabel(m.DisplayName, m.Role, m.Acquainted)
			for _, name := range p.OfferableCustomers.CustomerNames {
				if name == label {
					t.Errorf("scenario %q: laboring peer %q surfaced as an offerable customer — a worker mid-job is not a pitch target (LLM-231)", sc.name, label)
				}
			}
		}
	}
	if !exercised {
		t.Error("matrix must exercise a seller scenario with a laboring co-present peer (LLM-231)")
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

// TestGoldensRestockSupplierProducesOrForagesOrIsDistributor is the LLM-252
// cross-scenario invariant: every supplier the restock directory advertises for a
// low `buy` item must supply that item at first hand — some vendor stationed there
// produces or forages it — or be the distributor. A structure whose only vendors of
// the item hold it via a past `buy` (a fellow reseller) must never surface: that is
// the Josiah↔John carrot buy-back. Re-derived from snap.Actors so it independently
// confirms findItemVendors' output rather than trusting the gate that produced it.
func TestGoldensRestockSupplierProducesOrForagesOrIsDistributor(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subj := snap.Actors[actorID]
			if subj == nil || subj.RestockPolicy == nil {
				return // no reseller restock manifest — invariant N/A here
			}
			// Effective demand (LLM-260) so derived buy inputs are held to the
			// same supplier invariant as hand-authored entries.
			for _, e := range sim.EffectiveBuyEntries(snap.Recipes, subj.RestockPolicy) {
				for _, vd := range findItemVendors(snap, actorID, subj, e.Item) {
					if sim.ActorIsDistributor(snap.VillageObjects, vd.StructureID) {
						continue // the distributor is a standing supplier of everything
					}
					firstHand := false
					for _, a := range snap.Actors {
						if a != nil && a.WorkStructureID == vd.StructureID && a.RestockPolicy.ProducesOrForages(e.Item) {
							firstHand = true
							break
						}
					}
					if !firstHand {
						t.Errorf("scenario %q: restock directory advertises %q as a supplier of %q, but no vendor there produces/forages it and it is not the distributor — a reseller holding bought stock must not be a supplier (LLM-252)", sc.name, vd.StructureID, e.Item)
					}
				}
			}
		})
	}
}

// TestGoldensConserveKeeperNeverGetsBuyImperative is the LLM-294 cross-scenario
// invariant: whenever the subject is in conserve mode (coin-poor + overstocked, per
// merchantConserve), its "## Restocking" section must lead with the hold-off-buying
// steer and must NEVER carry a "Buy it now" imperative — the cue cannot tell a keeper
// to conserve and to buy in the same breath, even with a seller co-present. Runs over
// the whole matrix so no future cue can reintroduce the buy imperative for a conserving
// keeper.
func TestGoldensConserveKeeperNeverGetsBuyImperative(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subj := snap.Actors[actorID]
			if subj == nil || !merchantConserve(snap, actorID, subj).Active {
				return // invariant N/A — subject isn't conserving in this scenario
			}
			out := renderScenario(sc)
			// Isolate the "## Restocking" section: from its header to the next section
			// header (or end). The conserve steer is scoped to this section.
			const header = "## Restocking\n"
			idx := strings.Index(out, header)
			if idx < 0 {
				return // no restock section rendered (nothing low to buy) — nothing to assert
			}
			section := out[idx+len(header):]
			if next := strings.Index(section, "\n## "); next >= 0 {
				section = section[:next]
			}
			if !strings.Contains(section, "Hold off buying") {
				t.Errorf("scenario %q: subject is conserving but the Restocking section lacks the hold-off-buying steer:\n%s", sc.name, section)
			}
			if strings.Contains(section, "Buy it now") {
				t.Errorf("scenario %q: subject is conserving but the Restocking section still carries a 'Buy it now' imperative (LLM-294):\n%s", sc.name, section)
			}
		})
	}
}

// TestGoldensConserveLowItemAlwaysSelfResolves is the LLM-298 cross-scenario
// invariant: whenever the subject is conserving, every "- You are low on …" bullet in
// its "## Restocking" section must state what to do INSTEAD (the no-errand-now steer),
// never a bare lack. Conserve strips the co-present imperative and the walk-to list, so
// a bare "You are low on X" names a want with no outlet — the vacuum llama-3.3-70b
// filled by inventing a nonexistent "Market" to move_to (live scene 019f38de). The
// per-item steer closes the want even on a restock-wakeup turn that points at this
// section. The complementary non-conserve guarantee (a low item always carries a named
// destination/seller) is structural: buildRestocking omits any item with neither a
// co-present seller nor a walk-to vendor, so no bare non-conserve line can exist. Runs
// over the whole matrix so no future cue can reintroduce the dangling want.
func TestGoldensConserveLowItemAlwaysSelfResolves(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subj := snap.Actors[actorID]
			if subj == nil || !merchantConserve(snap, actorID, subj).Active {
				return // invariant N/A — subject isn't conserving in this scenario
			}
			out := renderScenario(sc)
			const header = "## Restocking\n"
			idx := strings.Index(out, header)
			if idx < 0 {
				return // no restock section rendered (nothing low to buy) — nothing to assert
			}
			section := out[idx+len(header):]
			if next := strings.Index(section, "\n## "); next >= 0 {
				section = section[:next]
			}
			for _, line := range strings.Split(section, "\n") {
				if !strings.HasPrefix(line, "- You are low on ") {
					continue // co-present standing-offer line etc. carry their own steer
				}
				if !strings.Contains(line, "no errand for it now") {
					t.Errorf("scenario %q: conserve Restocking names a low item with no no-errand-now steer (LLM-298 dangling want):\n%s", sc.name, line)
				}
			}
		})
	}
}

// TestGoldensConserveNoProductionInputsNag is the LLM-298 Phase 3 cross-scenario
// invariant: a conserving subject (coin-poor + overstocked) never renders the
// "## Keeping up production" section. That section is pure buy-motivation for a low
// input, but conserve tells the keeper to hold off buying and sell down — so a
// "running low on X" line there dangles a second want with no legal outlet (the live
// sage→stew produce-retry nag). Runs over the whole matrix so no future path
// reintroduces it for a conserving keeper.
func TestGoldensConserveNoProductionInputsNag(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subj := snap.Actors[actorID]
			if subj == nil || !merchantConserve(snap, actorID, subj).Active {
				return // invariant N/A — subject isn't conserving in this scenario
			}
			if out := renderScenario(sc); strings.Contains(out, "## Keeping up production") {
				t.Errorf("scenario %q: conserving subject still gets the '## Keeping up production' nag (LLM-298):\n%s", sc.name, out)
			}
		})
	}
}

// TestGoldensUnobtainableInputSurfacesNoDemand is the LLM-260 cross-scenario
// invariant: an effective buy item (explicit or derived from a produce recipe)
// that NO other actor in the world holds at a workplace must surface in NEITHER
// demand section — no "## Restocking" line, no "## Keeping up production"
// runway/booster line. "Nobody holds it anywhere" is the loosest possible
// vendor superset (every gate variant — wholesale, LLM-252 first-hand, LLM-216
// drops — can only shrink it), so the check is independent of the gates that
// produced the render: if the item genuinely has no source, ANY demand line for
// it is a dead-end cue the model would improvise on (the live Hannah Boggs
// phantom fetch-water hires).
func TestGoldensUnobtainableInputSurfacesNoDemand(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			subj := snap.Actors[actorID]
			if subj == nil || subj.RestockPolicy == nil {
				return // no restock manifest — invariant N/A here
			}
			prompt := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			for _, e := range sim.EffectiveBuyEntries(snap.Recipes, subj.RestockPolicy) {
				held := false
				for id, a := range snap.Actors {
					if id == actorID || a == nil || a.WorkStructureID == "" {
						continue
					}
					if a.Inventory[e.Item] > 0 {
						held = true
						break
					}
				}
				if held {
					continue // someone holds it — obtainability is the gated cues' call
				}
				label := itemDisplayLabel(snap, e.Item)
				for _, header := range []string{"## Restocking", "## Keeping up production"} {
					if strings.Contains(promptSection(prompt, header), label) {
						t.Errorf("scenario %q: %s names %q, but no actor in the world holds it at a workplace — an unobtainable item must surface no demand (LLM-260)", sc.name, header, label)
					}
				}
			}
		})
	}
}

// promptSection returns the body of the markdown section starting at header,
// cut at the next "## " heading; "" when the section is absent.
func promptSection(prompt, header string) string {
	idx := strings.Index(prompt, header)
	if idx < 0 {
		return ""
	}
	section := prompt[idx+len(header):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	return section
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
		name: "visitor_arrives_at_keepers_workplace",
		summary: "LLM-284: a tavern keeper (John Ellis) arrives at another keeper's workplace — the Blacksmith, " +
			"kept by the co-present Ezekiel Crane — on an errand. The golden pins the keeper possessive in the " +
			"'## What just happened' arrival line ('You arrived at Ezekiel Crane's Blacksmith.') so the model reads " +
			"whose shop it entered instead of greeting the smith as if hosting its own forge (the live host-role " +
			"inversion). A regression back to the ownerless 'the Blacksmith' shows in the diff.",
		build: visitorArrivesAtKeepersWorkplace,
	},
	{
		name: "hungry_worker_with_means_redirected_to_eat",
		summary: "LLM-276: a workless, on-shift, idle worker whose hunger sits in the upper felt band (15, " +
			"below the red-line 18) and who can resolve it now (holds coin, a free bush + a porridge vendor in reach). " +
			"The seek-work backstop stamped a tend-need warrant, so the prompt steers the worker to EAT — the tend-need " +
			"felt line, the eat/drink options, and the one-target need-redirect — and suppresses the businesses directory " +
			"and the solicit-work hustle, exactly as a red need would. The perception half of the Silence Walker beg-loop fix.",
		build: hungryWorkerWithMeansRedirectedToEat,
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
		name: "lodger_evening_tavern_open",
		summary: "LLM-311: the SAME evening as homed_worker_evening_tavern_open, but the agent (Ezekiel) is homeless-by-design " +
			"(home NULL) and lodges via an active nightly room grant at the Inn — the canonical rent-a-room NPC. Before LLM-311 the " +
			"living-evening scope was homed-only, so this agent got no tavern invitation and the off-shift wind-down steered it to " +
			"its rented room all evening (the live Inn↔Blacksmith oscillation). The golden pins that the evening 'tavern's open' " +
			"invitation now fires — its co-equal 'stay in' destination is the rented Inn (structure_id: inn), not an empty home token " +
			"— AND that the go-home/wind-down steer ('Your working hours are over …') is ABSENT: a lodger with a paid room has an " +
			"evening exactly as a homed peer does. A regression that re-narrowed the scope to homed-only shows in the diff.",
		build: lodgerEveningTavernOpen,
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
		name: "herbalist_ranged_wild_forage",
		summary: "The LLM-253 ranged forage cue. Prudence (herbalist, tagged sim.AttrForageRange) is low on sage " +
			"(0 of cap 5) and owns no sage bush, while an UNOWNED Sage Bush with 10 ripe sits ~80 tiles to the " +
			"northeast — the gap the owner-only owned-bush cue and the proximity-only at-bush cue both leave open. The " +
			"golden pins the distinct '## Free sources you can gather from' section (never 'your bushes'), the qualitative " +
			"distance+direction ('a long walk to the northeast'), the ripe count, and a move_to-by-structure_id handle — " +
			"move_to ONLY, no gather mention (LLM-59/79). Paired with untagged_forager_no_ranged_wild_forage.",
		build: herbalistRangedWildForage,
	},
	{
		name: "untagged_forager_no_ranged_wild_forage",
		summary: "The tag gate for the LLM-253 ranged forage cue: the SAME fixture as herbalist_ranged_wild_forage " +
			"(low on sage, unowned distant Sage Bush) but the forager does NOT carry sim.AttrForageRange. The golden " +
			"pins that NO '## Free sources you can gather from' section renders — ranged awareness of an unowned distant source " +
			"is the tagged 'herbalist gift' only. Enforced across the matrix by TestGoldensRangedWildForageRequiresTag.",
		build: untaggedForagerNoRangedWildForage,
	},
	{
		name: "general_store_water_forage_at_well",
		summary: "LLM-254 two-row Well. The town Well is an UNOWNED commons carrying BOTH a public thirst " +
			"drink row (Amount -8, slake-in-place) AND a yield-only water gather row (Amount 0, unset attribute — " +
			"the LLM-264 clean yield row). Josiah Thorne (merchant, tagged sim.AttrForageRange, low on water with " +
			"a `forage water` restock entry) is thirsty ~10 tiles away, so ONE unowned object surfaces in TWO " +
			"independent cues at once with no owner-gate conflict: the free-drink satiation cue ('## What you can " +
			"eat or drink' — the -8 thirst row) AND the ranged forage cue ('## Free sources you can gather from' — " +
			"the water yield row, 20 ready to gather). The forage count reads the yield row alone; the -8 drink row " +
			"never pollutes it (forageStockForItem gates on Amount==0). Byte-stable: on shift, no orders, no clock read.",
		build: generalStoreWaterForageAtWell,
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
		name: "hungry_holding_nibble_sees_meal_vendor",
		summary: "LLM-307: a mildly-hungry NPC (Ezekiel Crane, hunger 14 — felt, below the red threshold 18) carries " +
			"ONLY raspberries (a nibble, magnitude 2), with a cheese seller (a good meal, magnitude 8) at the General " +
			"Store. This is the live starvation-by-snacking loop (2026-07-06): the consume-first suppression used to " +
			"collapse the section to 'You have Raspberries … consume to eat' the moment he carried any food, hiding the " +
			"meal vendor for as long as a single berry was held. The golden pins the fix: the own-stock consume line, " +
			"the bridging line ('A nibble won't quiet this hunger, though — for a real meal, see the options below.'), " +
			"AND the re-opened cheese buy entry with its structure_id. Pairs with hungry_holding_meal_keeps_suppression.",
		build: hungryHoldingNibbleSeesMealVendor,
	},
	{
		name: "hungry_holding_meal_keeps_suppression",
		summary: "LLM-307 foil: the same mildly-hungry NPC and cheese seller, but Ezekiel carries a MEAL-class satisfier " +
			"(cheese, magnitude 8) instead of a nibble. A meal on hand is the answer, so the LLM-139 consume-first " +
			"suppression must STAND: the golden pins that the section shows only 'You have Cheese (a good meal) on hand — " +
			"consume to eat.' with NO bridging line and NO re-opened vendor directory. Guards the fix against over-firing " +
			"(re-opening the directory whenever any food is carried). Pairs with hungry_holding_nibble_sees_meal_vendor.",
		build: hungryHoldingMealKeepsSuppression,
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
		name: "tavernkeeper_starved_focus_at_forge",
		summary: "A multi-output crafter whose chosen focus is INPUT-STARVED — John Ellis at his tavern on shift, focus=stew " +
			"but holding no sage — the live LLM-257 deadlock that froze his production for a week. The golden pins the fix: " +
			"the '## Time to produce' cue does NOT lead with the 'You are producing stew now' steer (a starved focus isn't " +
			"productive) but falls through to the CHOOSE menu, with stew annotated 'can't make now: short of Sage (need 2, " +
			"have 0)' (meat, held in full, omitted) and no-input Water offered plainly; the standing 'You are making Stew.' " +
			"phantom self-state line is suppressed (forgeFocusLabel now inputs-gated); and the production-choice wake warrant " +
			"fires so he re-picks a good he can actually make. Contrast smith_forging_focused (a craftable focus DOES lead " +
			"with the steer).",
		build: tavernkeeperStarvedFocusAtForge,
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
		name: "wholesaler_producer_bartering_with_customer",
		summary: "A WHOLESALE producer (Moses James, James Farm tagged wholesaler; grows carrots + wheat) stands in " +
			"company with a would-be customer (Silence Walker) — the LLM-291 seller leg of live hud-9b23…, where Moses, " +
			"pressed to answer a buyer, hawked a retail carrot sale the wholesale gate then refused (and mis-fired the buy " +
			"verb to 'buy' his own carrots back). His produce sells only to the village distributor (Josiah Thorne), so the " +
			"'## What your wares fetch' cue draws the WHOLESALE-CHANNEL line for each own crop — who buys it, what the " +
			"distributor pays (carrots ~2 coins, wheat ~1 coin), and to send other buyers to Josiah — NOT a retail spread " +
			"that would invite the street sale. Pairs with smith_bartering_at_tavern (the ordinary, retail-priced producer).",
		build: wholesalerProducerBarteringWithCustomer,
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
		name: "producer_input_below_batch_floor_reorders",
		summary: "LLM-279 produce-input batch floor: Hannah Boggs makes porridge (3 milk + 5 water per 10-bowl batch) " +
			"and is low on WATER at 4 — stranded in the deadlock band, above the cap fraction (derived cap 15 → fires " +
			"only below 3.75) yet below a single 5-unit batch, so she can't cover the next batch but the old cap-fraction " +
			"rule would never reorder her. A well-keeper sells water. The golden pins that BOTH the '## Restocking' cue " +
			"(walk-to the well for water) AND the '## Keeping up production' runway line now render for water — the batch " +
			"floor (2×5=10) catching what the fraction skipped — while MILK, stocked at 9 (above its 2×3=6 floor), stays " +
			"silent. Guards the reorder-on-batch-coverage fix end to end at the perception layer.",
		build: producerInputBelowBatchFloorReorders,
	},
	{
		name: "reseller_arrives_at_supplier_buy_here_no_huddle",
		summary: "LLM-286 arrival tick: John Ellis, a tavernkeeper reselling ale, walked to the Brewery to restock and " +
			"stands inside it with the brewer Anders (an ale PRODUCER), but NO huddle exists yet — one forms only when " +
			"someone speaks. pay_with_item bootstraps the co-located huddle on the call itself (withHuddleBootstrap, " +
			"ZBBS-HOME-400), so the keeper IS reachable this tick. The golden pins that the '## Restocking' section renders " +
			"the concrete 'Anders Brewer is here with you and sells ale. Buy it now …' imperative, NOT the 'No seller is " +
			"here now — use move_to …' walk-to list that would wrongly point him to the very Brewery he stands in. Before " +
			"LLM-286 the huddle-only co-presence gate could not fire on an arrival tick, so the buyer was told to walk to " +
			"where he already was (live: zbbs-john-ellis, virtual_agent_calls id 63123).",
		build: resellerArrivesAtSupplierBuyHereNoHuddle,
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
		name: "seller_huddled_with_laboring_peer",
		summary: "LLM-231: John Ellis keeps his tavern in company with two peers — Patience Walker, mid-job for Josiah " +
			"Thorne (a Working LaborOffer, StateLaboring), and Grace Bishop, free. The live shape: John burned ~20 ticks " +
			"re-pitching a laboring Patience because nothing told him she was busy. The golden pins that the '## Around you' " +
			"line annotates Patience busy ('working a job for Josiah Thorne just now — not free to trade') while Grace reads " +
			"plainly, AND that the seller offer cue lists Grace but NOT Patience (a worker mid-job is not a pitch target). The " +
			"busy annotation deliberately does not say 'won't respond' — a laborer can still answer speech (LLM-230). Pairs " +
			"with TestGoldensLaboringPeerNotAnOfferTarget (the matrix-wide exclusion).",
		build: sellerHuddledWithLaboringPeer,
	},
	{
		name: "seller_employing_own_laboring_worker",
		summary: "LLM-231 employer-seller case: John Ellis keeps his tavern (stew to sell) while a worker he himself " +
			"hired, Silence Walker, labors for him (a Working LaborOffer with John as employer), alongside Grace Bishop, a " +
			"free customer. The golden pins that Silence is STILL dropped from the '## Custom at hand' offer cue (a worker " +
			"mid-job isn't a sale target even for their own employer) while Grace is listed — AND that Silence carries NO " +
			"busy annotation in '## Around you' (the employer gets the richer '## Workers currently working for you' cue " +
			"instead; the annotation is bystander-only). Complements seller_huddled_with_laboring_peer (the bystander case).",
		build: sellerEmployingOwnLaboringWorker,
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
		name: "hungry_buyer_with_wholesaler_peer_no_buy_cue",
		summary: "A hungry buyer (Silence Walker, coins in hand) huddles with a wholesaler-farmer (Moses James, work " +
			"anchor tagged wholesaler) carrying stew — the LLM-289 shape from live hud-843da92a…, where the co-present " +
			"peer cue said 'Moses James is here with you, carrying Carrots — you could offer to buy it from them now' and " +
			"every cued pay_with_item died on the LLM-223/252 wholesale gate (40 of the huddle's 57 turns). The golden " +
			"pins that the '## What you can eat or drink' section carries NO peer-buy line for the wholesaler's goods: " +
			"the peer cue now applies the same SellerAtWholesaler/ActorIsDistributor pair as the dispatch gate. A " +
			"regression would make the doomed buy line reappear in the diff.",
		build: hungryBuyerWithWholesalerPeer,
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
		name: "employer_recalls_returning_helper",
		summary: "A producing keeper (Hannah Boggs, who makes porridge) is solicited again by Anne Walker, who completed " +
			"a paid job for her a few hours ago (an Active ObservedHelpedByWorker memory). The LLM-228 situation: rather than " +
			"an engine hire-value pitch at the decision point (that pitch shipped in #690 and was pulled in #691), the " +
			"decision section recalls the past help experientially. The golden pins the returning-helper recall WITH the " +
			"added-output clause ('You remember Anne Walker lending you a hand recently, and you got more done for it.') — a " +
			"producing employer really does get more done from help — above the normal accept/decline footer. Pairs with " +
			"employer_recalls_returning_helper_nonproducer.",
		build: employerRecallsReturningHelper,
	},
	{
		name: "employer_recalls_returning_helper_nonproducer",
		summary: "The same returning-helper solicitation, but the employer makes no goods itself (no makeable produce " +
			"entry). The golden pins the bare social recall ('You remember Anne Walker lending you a hand recently.') with NO " +
			"'got more done' clause — a non-producer never claims output it did not make (LLM-228). Pairs with " +
			"employer_recalls_returning_helper.",
		build: employerRecallsReturningHelperNonProducer,
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
		summary: "A business owner (Ezekiel, a smith) stands at his own worn premises (wear past the repair threshold, " +
			"below degrade) carrying too few nails to mend it. The golden pins the '## Your business' cue: the wear " +
			"problem AND the buy-nails-from-the-smith steer in one line (symmetrical awareness, LLM-118). The repair tool " +
			"rides the same StallRepair signal (handlers gating test).",
		build: ownerAtWornStall,
	},
	{
		name: "owner_at_worn_stall_with_nail_supplier",
		summary: "LLM-274: a business owner (Elizabeth Ellis) stands at her own worn Ellis Farm with 0 nails while a " +
			"SEPARATE open nail supplier — Ezekiel, the blacksmith, holding 21 nails at the Blacksmith — exists in the " +
			"world. The golden pins the destination-bearing '## Your business' steer: findItemVendors resolves the smith, " +
			"so the cue names 'buy from Blacksmith (structure_id: blacksmith)' with move_to + pay_with_item, replacing the " +
			"dead-end 'the smith' that llama-3.3-70b narrated but never walked (the live 2026-07-04 case). Its foil is " +
			"owner_at_worn_stall, where no other supplier exists and the generic sentence is correctly kept.",
		build: ownerAtWornStallWithNailSupplier,
	},
	{
		name: "owner_off_post_at_smith_short_nails",
		summary: "LLM-277 (the live 2026-07-04 failure): Elizabeth Ellis, owner of the worn Ellis Farm with 0 nails, has " +
			"walked OFF her farm and shares the smith's huddle with Ezekiel (21 nails, the nail producer). The golden pins " +
			"the off-post '## Nails to mend your business' cue with the co-present pay_with_item buy-here imperative naming " +
			"Ezekiel — the second half of LLM-274 — AND the absence of any return-to-post steer (she is on-shift off her " +
			"post, so the to-work nag WOULD fire, but the active nail-buy errand suppresses it). Before LLM-277 she got no " +
			"buy cue and a go-home nag here, so she left without buying.",
		build: ownerOffPostAtSmithShortNails,
	},
	{
		name: "owner_off_post_short_nails_walking",
		summary: "LLM-277 walk-to arm: Elizabeth, 0 nails, is off her worn farm and NOT co-present with the smith (no shared " +
			"huddle). The golden pins the same '## Nails to mend your business' cue naming the walk-to destination ('buy from " +
			"Blacksmith (structure_id: blacksmith)') and no return-to-post steer — the 'while away' half of the errand that " +
			"persists across the whole walk, not just at the farm. Foil of owner_off_post_at_smith_short_nails.",
		build: ownerOffPostShortNailsWalking,
	},
	{
		name: "owner_off_post_enough_nails",
		summary: "LLM-277 negative arm: Elizabeth is off her worn farm but already carries enough nails (5 == NAILS_PER_REPAIR) " +
			"to mend it, so there is no buy errand — the '## Nails to mend your business' cue is ABSENT. With no errand to " +
			"defer, the to-work nag correctly fires (head back to the post to mend), pinning that the suppression is " +
			"conditional on an actual nail shortfall.",
		build: ownerOffPostEnoughNails,
	},
	{
		name: "keeper_conserving_owes_nail_repair",
		summary: "LLM-297 invariant anchor (the live 2026-07-06 Josiah case): a shopkeeper (Josiah Thorne) whose " +
			"working capital has collapsed — 1 coin, below the 10-coin floor, a full shelf of unsold cloth — owns his " +
			"worn General Store and has stepped away from it to the Blacksmith, sharing Ezekiel Crane's huddle. Two " +
			"standing sections fire: '## Restocking' flips to the coin-poor 'Hold off buying more for now' steer, and the " +
			"off-post '## Nails to mend your business' errand sits him in front of the nail seller. Before LLM-297 the " +
			"errand goaded 'Buy it now' into that thin purse, contradicting the restock advice; merchantConserve now softens " +
			"it to a hold-off, so the two cues agree. The non-vacuous anchor for TestGoldensBuyNowGoadNeverBesideHoldOff.",
		build: keeperConservingOwesNailRepair,
	},
	{
		name: "owner_standoff_declined_nails",
		summary: "LLM-297 standoff arm: Elizabeth Ellis, off her worn farm and sharing the smith's huddle with Ezekiel, " +
			"has already had two nail offers to him declined in this huddle. She is not a keeper (no restock policy, so " +
			"merchantConserve never fires), so the softening is driven purely by the dead-ended negotiation — the cue drops " +
			"'Buy it now' for a hold-off rather than goading a third offer into the same no. Foil of " +
			"owner_off_post_at_smith_short_nails (same co-present setup, no prior offers → the buy imperative still stands).",
		build: ownerStandoffDeclinedNails,
	},
	{
		name: "owner_short_nails_seller_low_stock",
		summary: "LLM-297 stock-cap arm: Elizabeth shares the smith's huddle with Ezekiel, but he holds only 2 nails " +
			"against the 5 a repair needs. Affordable and no prior offers, so the buy still stands — but the qty is capped " +
			"at his stock ('can spare only 2 … a qty up to 2') instead of goading 'up to 5' for stock he can't deliver (the " +
			"live smith-held-only-1-nail case). Foil of owner_off_post_at_smith_short_nails (smith well-stocked → the full " +
			"shortfall is asked).",
		build: ownerShortNailsSellerLowStock,
	},
	{
		name: "owner_at_degraded_stall",
		summary: "A business owner stands at his own DEGRADED premises (wear past the degrade threshold — closed for " +
			"trade), carrying enough nails. The golden pins the escalated '## Your business' steer ('too worn to trade … " +
			"use the repair tool now to fix it') — the seller-facing half of the degrade sales-block (LLM-118).",
		build: ownerAtDegradedStall,
	},
	{
		name: "owner_at_degraded_store_conserving",
		summary: "LLM-301: Josiah Thorne stands at his own DEGRADED General Store (shut for trade) with 0 of the 5 " +
			"nails a mend takes, 1 coin (below the 10-coin MerchantCoinFloor) and 17 unsold flour — conserve active — and " +
			"NO nail supplier survives the LLM-216 drops. The golden pins the vendor-less fallback's sell-first soften " +
			"('your purse can't take on nails just now') and the ABSENCE of the old destination-less 'buy more from the " +
			"smith' goad, which llama-3.3-70b answered by inventing 'the Smithy' and burning its turn retrying the move " +
			"(the live 2026-07-06 scene).",
		build: ownerAtDegradedStoreConserving,
	},
	{
		name: "owner_conserving_with_nail_supplier",
		summary: "LLM-301 (code_review arm): a conserving owner at her worn farm while a nail supplier DOES survive " +
			"findItemVendors (unknown price, so the affordability drop keeps him). Conserve must WIN over the vendor " +
			"list — the golden pins the sell-first soften and the ABSENCE of the 'Use move_to to reach a supplier' walk-to " +
			"goad, so this cue can never push a buy while '## Restocking' says hold off (the LLM-297 posture; " +
			"findItemVendors' affordability drop and the working-capital floor are different filters and can disagree).",
		build: ownerConservingWithNailSupplier,
	},
	{
		name: "passerby_at_worn_stall",
		summary: "A non-owner (John) stands at someone else's worn business. The golden pins the co-present " +
			"atmosphere line ('The Blacksmith here looks worn…') and the ABSENCE of the owner '## Your business' cue — a " +
			"passerby can remark on the wear but isn't handed the repair (LLM-118).",
		build: passerbyAtWornStall,
	},
	{
		name: "hired_worker_at_employer_worn_business",
		summary: "LLM-271: Lewis Walker, hired to labor for Prudence Ward (a Working LaborOffer, WorkerID == subject), " +
			"stands INSIDE her worn PW Apothecary with enough nails to mend it. The golden pins the hired-framed " +
			"'## The business you're working at … needs mending' cue (NOT the owner '## Your business') plus the hired repair " +
			"warrant line — the wake that pierces the laboring shelve-gate so a hired hand actually mends it. The live " +
			"2026-07-04 case that motivated the feature; the repair tool rides the same StallRepair signal.",
		build: hiredWorkerAtEmployerWornBusiness,
	},
	{
		name: "owner_at_worn_tavern",
		summary: "John Ellis stands at his own worn Tavern — an object tagged {business, lodging, tavern} with NO " +
			"market_stall tag, pinning that LLM-247 widened the wear gate to any owned business, not just stalls. The " +
			"object has no DisplayName, so the '## Your business' cue names it from the co-located structure ('Your Tavern " +
			"is showing hard use…') and steers to buy nails from the smith (2 held < 5). The repair tool rides the same " +
			"StallRepair signal.",
		build: ownerAtWornTavern,
	},
	{
		name: "owner_inside_worn_business",
		summary: "LLM-266 regression fixture: John Ellis stands INSIDE his own worn Tavern (InsideStructureID == the " +
			"business id) and AWAY from the outdoor loiter pin — the live keeper-at-post posture the old pin-only " +
			"co-location gate silently excluded, so the '## Your business' cue had never once rendered for a real NPC. With " +
			"sim.AtBusiness treating 'inside your business structure' as co-located, the cue (and the repair tool that rides " +
			"the same StallRepair signal) renders. The non-vacuous anchor for TestGoldensRepairCueWheneverColocatedOwnerRepairable.",
		build: ownerInsideWornBusiness,
	},
	{
		name: "passerby_inside_worn_business",
		summary: "LLM-266 non-owner arm: a non-owner (Ezekiel) stands INSIDE someone else's worn business (John's Tavern) " +
			"and away from the outdoor loiter pin. The golden pins the co-present atmosphere line ('The Tavern here looks " +
			"worn…') now firing via the inside-structure branch of sim.AtBusiness, and the ABSENCE of the owner-only " +
			"'## Your business' cue (Ezekiel isn't the owner).",
		build: passerbyInsideWornBusiness,
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
		name: "farm_owner_owes_upkeep_with_shovel_supplier",
		summary: "LLM-274: a farm owner (Elizabeth Ellis) owes 3 upkeep shovels and holds none, while a SEPARATE " +
			"shovel-producing smith (Ezekiel at the Blacksmith) exists. The golden pins the destination-bearing " +
			"'## Farm upkeep' steer: findItemVendors resolves the smith, so the cue names 'buy from Blacksmith " +
			"(structure_id: blacksmith)' with move_to + pay_with_item, replacing the dead-end 'from the blacksmith'. " +
			"Its foil is farm_owner_owes_upkeep, where no supplier exists and the generic sentence is correctly kept.",
		build: farmOwnerOwesUpkeepWithShovelSupplier,
	},
	{
		name: "farm_owner_owes_upkeep_seller_present",
		summary: "LLM-277 farm-upkeep co-present arm: the same owing farm owner (Elizabeth, 3 shovels short) now shares the " +
			"smith's huddle with Ezekiel (12 shovels). The golden pins the '## Farm upkeep' cue flipping from the walk-to " +
			"list to the concrete co-present pay_with_item buy-here imperative naming Ezekiel — the same fast-path the nail " +
			"repair-buy uses, closing the same at-the-seller dead-spot where the weak model narrated and walked off. Foil of " +
			"farm_owner_owes_upkeep_with_shovel_supplier (smith far off → the walk-to destination is named instead).",
		build: farmOwnerOwesUpkeepSellerPresent,
	},
	{
		name: "farm_owner_owes_upkeep_seller_low_stock",
		summary: "LLM-299 stock-cap arm (shovel twin of owner_short_nails_seller_low_stock): Elizabeth owes 3 upkeep shovels " +
			"and shares the smith's huddle with Ezekiel, but he holds only 1 shovel against the 3 she needs. Affordable and " +
			"no prior offers, so the buy still stands — but the '## Farm upkeep' cue caps the ask at his stock ('can spare " +
			"only 1 … a qty up to 1') instead of goading 'up to 3' for stock he can't deliver. Foil of " +
			"farm_owner_owes_upkeep_seller_present (smith well-stocked with 12 → the full shortfall is asked).",
		build: farmOwnerOwesUpkeepSellerLowStock,
	},
	{
		name: "farm_owner_standoff_declined_shovels",
		summary: "LLM-299 standoff arm (shovel twin of owner_standoff_declined_nails): Elizabeth owes 3 upkeep shovels and " +
			"shares the smith's huddle with Ezekiel (well-stocked, 12 shovels), but has already had two shovel offers to him " +
			"declined in this huddle. She is a plain farmer (no restock policy → merchantConserve never fires), so the " +
			"softening is driven purely by the dead-ended negotiation — the '## Farm upkeep' cue drops 'Buy it now' for a " +
			"hold-off rather than goading a third offer into the same no. Foil of farm_owner_owes_upkeep_seller_present " +
			"(same co-present setup, no prior offers → the buy imperative still stands).",
		build: farmOwnerStandoffDeclinedShovels,
	},
	{
		name: "farm_owner_conserving_owes_upkeep",
		summary: "LLM-299 conserve-coupling arm + the non-vacuous anchor for TestGoldensFarmUpkeepGoadNeverBesideHoldOff " +
			"(the shovel twin of keeper_conserving_owes_nail_repair): Marta Vale is a shopkeeper whose working capital has " +
			"collapsed (51 coins, below the 60-coin floor, a full shelf of unsold cloth) who ALSO owns a farm and owes 1 " +
			"upkeep shovel. She has stepped off her farm to the Blacksmith, sharing Ezekiel Crane's huddle — he produces both " +
			"shovels (12 held, the upkeep supply) and coal (15 held, her low restock input). Two standing sections fire: " +
			"'## Restocking' flips to the coin-poor 'Hold off buying more for now' steer, and '## Farm upkeep' sits her in " +
			"front of the shovel seller. merchantConserve now softens the shovel goad to a hold-off, so the two cues agree " +
			"instead of the '## Farm upkeep' 'Buy it now' contradicting the restock hold-off.",
		build: farmOwnerConservingOwesUpkeep,
	},
	{
		name: "farm_owner_off_post_owes_upkeep_no_supplier",
		summary: "LLM-277 review-caught edge (code_review c11007e7): a farm owner off her post owes 3 upkeep shovels but NO " +
			"shovel supplier is reachable (findItemVendors empty, none co-present). The '## Farm upkeep' cue keeps its generic " +
			"'buy … from the blacksmith' fallback (LLM-216), but the to-work steer STILL fires ('away from your post — make " +
			"your way to the Ellis Farm now') — a dead-end upkeep cue must not suppress the return-to-post nag and strand her. " +
			"Pins that hasFarmUpkeepErrand is gated on an actionable buy path, not the cue's mere presence.",
		build: farmOwnerOffPostOwesUpkeepNoSupplier,
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
		summary: "A hungry forager (Ezekiel, holding coins he could spend) stands near a cheese seller at the General Store, " +
			"but he went there within the decay window and found it shut — no keeper tending it (now including an abed keeper, " +
			"LLM-126). The golden pins the LLM-222 seller-availability drop: the '## What you can eat or drink' buy cue DROPS the " +
			"remembered-shut vendor entirely rather than annotating it 'found it shut up', so with no other satisfier nearby the " +
			"whole section is omitted. Mirrors LLM-216's restock drop — the retired annotate-only posture left the weak model " +
			"touring the dead end (his live asleep-Inn walk). He can afford the cheese, so the drop is driven by the shut memory, " +
			"not affordability.",
		build: buyerRemembersVendorShut,
	},
	{
		name: "buyer_drops_shut_keeps_open_vendor",
		summary: "A hungry forager (Ezekiel, 6 coins) can buy cheese at two shops: the General Store, which he remembers finding " +
			"shut within the decay window, and the open Tavern he has no shut memory of. The golden pins that the LLM-222 " +
			"seller-availability drop is surgical — the shut General Store is dropped from the '## What you can eat or drink' buy " +
			"cue while the open Tavern is kept — the eat/drink analogue of keeper_restock_drops_shut_keeps_open_supplier. Also the " +
			"non-vacuous fixture for TestGoldensSatiationBuyCueNeverTargetsRememberedShutVendor.",
		build: buyerDropsShutKeepsOpenVendor,
	},
	{
		name: "broke_buyer_with_goods_barters_for_food",
		summary: "A hungry forager (Ezekiel) with 0 coins but a pelt in his pack stands near an open cheese seller (Mabel, awake, " +
			"not shut). The golden pins the LLM-222 means-to-pay 'barter' state: because barter works (pay_with_item / offer_trade " +
			"accept goods), a 0-coin buyer holding tradeable goods is NOT a dead end — the buy cue is kept but steered to a goods " +
			"offer ('which your coins won't cover — offer goods you carry in trade instead (pay_items)') rather than a coin price " +
			"he can't meet. The pelt is non-food, so it drives the barter path without adding an own-stock eat cue.",
		build: brokeBuyerWithGoodsBartersForFood,
	},
	{
		name: "broke_buyer_no_goods_no_buy_cue",
		summary: "The same hungry forager (Ezekiel), 0 coins, but now with nothing at all in his pack — no coins and nothing to " +
			"trade, the one genuine payment dead-end. The golden pins the LLM-222 means-to-pay suppression: the buy cue is dropped " +
			"entirely (the co-present open cheese seller is unpayable for him), and with no free source or own stock nearby the " +
			"whole '## What you can eat or drink' section is omitted — the free-food cues, not a futile buy imperative, are what " +
			"cover this actor. Non-vacuous fixture for TestGoldensNoBuyCueWithoutMeansToPay.",
		build: brokeBuyerNoGoodsNoBuyCue,
	},
	{
		name: "broke_buyer_no_goods_no_peer_buy",
		summary: "The LLM-242 co-present peer arm of the same means-to-pay dead-end: the broke forager (Ezekiel, 0 coins, empty " +
			"pack) stands in a huddle with a co-present peer (Lewis) carrying stew he'd otherwise be cued to buy with pay_with_item. " +
			"With no coins and nothing to trade there is no means of payment, so the peer buy offer is suppressed (the sibling of the " +
			"LLM-222 vendor-cue drop); with no free source or own stock nearby the whole '## What you can eat or drink' section is " +
			"omitted. Contrast peers_holding_same_food, where the subject DOES hold goods and so keeps a means to pay. Non-vacuous " +
			"fixture for the peer half of TestGoldensNoBuyCueWithoutMeansToPay.",
		build: brokeBuyerNoGoodsNoPeerBuy,
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
		name: "wholesaler_starving_own_produce_at_post",
		summary: "A WHOLESALER farmer (Moses James, James Farm tagged wholesaler) at the red/distress hunger tier, carrying " +
			"nothing but the carrots he grows to sell and no other food (LLM-267). Unlike producer_starving_at_post, the own-" +
			"stock 'consume to eat' cue is ABSENT even at desperation — a wholesaler's produce is stock to sell, never its " +
			"larder, with NO red-tier escape hatch (the Consume guard rejects it too). Food must come from buying, foraging, " +
			"or barter. Pairs with wholesaler_bought_food_at_post (the same wholesaler IS offered a bought loaf).",
		build: wholesalerStarvingOwnProduceAtPost,
	},
	{
		name: "wholesaler_bought_food_at_post",
		summary: "The same wholesaler (Moses), mildly hungry, carrying a bought loaf of bread (NOT one of his produce rows) " +
			"alongside his own carrots (LLM-267 positive control). The own-stock 'consume to eat' cue surfaces the BREAD — real " +
			"provisions he may eat — while his carrots stay out. Proves the block is own-produce-scoped, not a blanket ban on a " +
			"wholesaler eating.",
		build: wholesalerCarryingBoughtFoodAtPost,
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
		name: "worker_solicits_goods_rich_coin_poor_employer",
		summary: "The LLM-243 live case (Silence Walker / Prudence Ward at the PW Apothecary, hud-36317f65…), reduced: a workless " +
			"worker shares a huddle with a co-present stranger employer who holds 0 coins but goods on hand (berries, tea). Such " +
			"an employer can still hire IN KIND (LLM-225), so SolicitWork no longer auto-declines a bad coin ask against it — the " +
			"barter branch mints no offer and records no decline. With no Declined ledger entry the employer stays a solicitable " +
			"prospect: the golden pins the solicit_work affordance PRESENT for Prudence and the ABSENCE of the SeekWorkPlaces " +
			"businesses directory + the 'No one here can hire you' seek-work dead-end. A regression that foreclosed a coin-poor " +
			"employer (dropping it from the solicitable audience for its empty purse) would re-suppress the affordance and bring " +
			"back the dead-end. Matrix-wide guard: TestGoldensCoinPoorEmployerStaysSolicitable.",
		build: workerSolicitsGoodsRichCoinPoorEmployer,
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
		name: "buyer_offer_declined_seller_short_stock",
		summary: "LLM-296: a buyer (Josiah Thorne) whose pay offer of 6 carrots + 1 coin for 5 nails to Ezekiel Crane " +
			"was just declined — Ezekiel holds only 1 nail (the live hud-e7fec94 case). The golden pins that '## Recently " +
			"settled offers' names the OFFERED bundle (not just the want-item, so two declines aren't byte-identical — the " +
			"repeat the thin line drove) and appends the engine-known stock shortfall ('they hold only 1 nail') as the " +
			"informed reason it closed.",
		build: buyerOfferDeclinedSellerShortStock,
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
		name: "laboring_worker_addressed_while_working",
		summary: "LLM-230, the worker side of the same job: Silence Walker is mid-contract for John Ellis and he speaks " +
			"to her. The golden pins her standing self-state anchor — 'You are working a job for John Ellis … Stay with it " +
			"until it's done' — the cue that grounds a 'can't stop just now, I'm minding the work' reply. The reactor reply-" +
			"cadence and the speak-only tool surface that make that reply happen are covered by unit tests (the render is " +
			"unchanged); a regression that muted the laboring self-state for an addressed worker would surface here.",
		build: laboringWorkerAddressedByEmployer,
	},
	{
		name: "laboring_worker_off_post",
		summary: "LLM-268 symptom 1 (the marooning): Silence Walker walked off John Ellis's Tavern mid-contract (a " +
			"need-break that left her, needs now green) while John still holds the post. LLM-230 stripped her move_to when " +
			"the need cleared and took her tick eligibility with it, so she stood marooned until the completion sweep. The " +
			"golden pins the two things that recover her: the return-to-post felt-impulse warrant line that wakes her (the " +
			"backstop's engine-authored nudge) and her self-state cue 'you have wandered off … Head back there with " +
			"move_to'. The move_to re-grant itself is asserted in handlers/labor_gating_test.go.",
		build: laboringWorkerOffPost,
	},
	{
		name: "laboring_worker_at_post_employer_present",
		summary: "LLM-268 regression guard for LLM-230: Silence is inside the Tavern with John present, green needs — the " +
			"normal committed case. Neither off-post flag holds, so the golden pins the unchanged 'Stay with it until it's " +
			"done' self-state line with NO directional cue. Widening the move_to gate for the off-post cases must not leak " +
			"the return/accompany cue (or move_to) into the at-post case; the tool-side half is the move_to-stripped " +
			"assertion in handlers/labor_gating_test.go.",
		build: laboringWorkerAtPostEmployerPresent,
	},
	{
		name: "laboring_worker_employer_away",
		summary: "LLM-268 symptom 2 (accompany): Silence is at the Tavern but John has walked off to the General Store " +
			"mid-contract (the live Hannah/Lewis case — an employer trying to take her hire along). The golden pins the " +
			"accompany cue 'they have left the Tavern and gone to the General Store … follow after them with move_to', so a " +
			"'come with me' errand can be acted on instead of being silently impossible. The tick that lets her act rides " +
			"the employer's speech reply-cadence (unchanged); the move_to re-grant is asserted in handlers/labor_gating_test.go.",
		build: laboringWorkerEmployerAway,
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
	{
		name: "distributor_restock_skips_coPresent_reseller",
		summary: "LLM-252 reseller buy-back guard: the distributor (Josiah Thorne) has dipped below his carrot reorder " +
			"threshold while a fellow reseller (John Ellis, the tavernkeeper) is co-present holding carrots he bought — the " +
			"exact buy-back trigger. The golden pins that the '## Restocking' carrot cue routes Josiah to the PRODUCING James " +
			"Farm (a walk-to) and does NOT surface John: John holds carrots only via a `buy` entry, so he is neither a walk-to " +
			"supplier nor the co-present buy-here seller (no 'John Ellis is here — buy it now' imperative). A restock supplier " +
			"must produce/forage the item or be the distributor. Cross-scenario guard: " +
			"TestGoldensRestockSupplierProducesOrForagesOrIsDistributor.",
		build: distributorRestockSkipsCoPresentReseller,
	},
	{
		name: "distributor_restocking_milk_bulk_rate_anchor",
		summary: "LLM-292 buy-leg anchor (the live Josiah milk leak): the distributor (Josiah Thorne) is low on milk, his " +
			"supplier is the wholesaler-tagged Ellis Farm, and the catalog prices milk wholesale 1 / retail 2. The golden pins " +
			"the catalog anchor clause on the '## Restocking' milk line — 'The fair bulk rate buying it in is about 1 coin " +
			"each — pay above that and you are overpaying.' Before LLM-292 the cue's only rate was the buyer's own last-paid, " +
			"a self-poisoning anchor (one overpay re-anchors every later offer; live he paid ~2.2/unit against the 1-coin " +
			"book). No price book in the fixture, so the last-paid CostText and weekly P&L stay silent — isolating the " +
			"catalog anchor as the line's one rate.",
		build: distributorRestockingMilkBulkRateAnchor,
	},
	{
		name: "distributor_restock_observed_supplier_rate",
		summary: "LLM-295 observed-first buy anchor: the same distributor-buys-milk-from-the-farm shape as " +
			"distributor_restocking_milk_bulk_rate_anchor, but the PriceBook now carries a real Ellis Farm milk sale at " +
			"~2 coins/unit — above the catalog wholesale SEED of 1. The golden pins that the '## Restocking' anchor reports the " +
			"OBSERVED supplier rate in lived phrasing ('Of late it has been going for about 2 coins each'), not the seed 1, once " +
			"transaction data exists. The sale's buyer is off-snapshot, so the distributor has no last-paid at Ellis and the " +
			"walk-to line carries no price — isolating the observed anchor as the line's one rate.",
		build: distributorRestockObservedSupplierRate,
	},
	{
		name: "wholesaler_producer_observed_rates",
		summary: "LLM-295 observed-first sell figures: the wholesale producer Moses stands with a customer, and the PriceBook " +
			"carries real transactions for both figures on his wholesale-channel wares line — his own carrot sales to the shop " +
			"at ~2 coins (bulk) and the shop's carrot resales to folk at ~5 coins (shelf), both above the catalog seed " +
			"(wholesale 1 / retail 3). The golden pins the OBSERVED rates in lived phrasing ('Folk have lately paid about 5 " +
			"coins each in the shops, but the shop has lately paid you about 2 coins each'), not the seed. Josiah does not " +
			"produce carrots, so his sales count as shop (shelf) rate, not the wholesale side.",
		build: wholesalerProducerObservedRates,
	},
	{
		name: "miller_wheat_restock_flat_band_anchor",
		summary: "LLM-292 flat-band anchor (the live Joseph Scott case): the miller produces flour from bought-in wheat — a " +
			"DERIVED buy entry (LLM-260) below its two-batch floor (LLM-279) — and wheat's catalog band is FLAT 1/1. Live he " +
			"paid 2/unit for 20 wheat against the flat 1-coin book; even a degenerate single-point band never reached his " +
			"buy-side perception. The golden pins the anchor clause on the derived wheat '## Restocking' line (a " +
			"single-priced band collapses to its one price), the General Store (the distributor holding wheat) as the " +
			"walk-to supplier, and the '## Keeping up production' runway line the same low state motivates.",
		build: millerWheatRestockFlatBandAnchor,
	},
	{
		name: "owner_holding_repair_nails_in_company",
		summary: "LLM-292 repair-reserve earmark (the live Josiah nail resale): Josiah stands at his worn General Store " +
			"(wear past the repair threshold) holding 3 of the 5 nails a mend takes, in a huddle with John Ellis — the " +
			"situation where a buyer's offer for the nails lands. Live, nothing marked the nails earmarked and the " +
			"role-agnostic coin band made any in-band offer look fair, so he resold 5 repair nails before mending (10 coins " +
			"lost, shop still broken). The golden pins the wares-cue earmark line ('you need 5 of these to mend your General " +
			"Store — the 3 you carry are for that mend, not for sale') rendering alongside his ordinary ware line (milk) and " +
			"the '## Your business' mend nag it shares predicates with.",
		build: ownerHoldingRepairNailsInCompany,
	},
	{
		name: "coin_poor_overstocked_keeper_conserves",
		summary: "LLM-294 working-capital tone gate: a coin-poor keeper (Hannah Boggs, 1 coin — below the 10 floor) sitting " +
			"on unsold stock (20 porridge, dead-stock over the 8 floor) is low on the milk she buys, with the milk seller " +
			"(Josiah) co-present in her huddle. The golden pins BOTH tiers: '## Restocking' flips to the hold-off-buying steer " +
			"(milk named, NO 'Buy it now' imperative despite the co-present seller), and '## What your wares fetch' carries the " +
			"sell-first nudge pointing at the sell tool for her most-overstocked ware (porridge). Cross-scenario guard: " +
			"TestGoldensConserveKeeperNeverGetsBuyImperative.",
		build: coinPoorOverstockedKeeperConserves,
	},
	{
		name: "coin_poor_keeper_alone_conserve_low_stock",
		summary: "LLM-298 dangling-want repro: John Ellis (tavernkeeper) ALONE at his post — the live config (scene 019f38de, " +
			"2026-07-06) that made llama-3.3-70b invent a nonexistent \"Market\" to move_to. 8 coins (below the 10 floor), " +
			"shelves overstocked (20 ale / 14 bread / 13 stew, all over the 8 dead-stock floor), low on the carrots he buys " +
			"in (1 of 6) with his supplier (Josiah, the general-store distributor) NOT co-present. The golden pins the " +
			"'## Restocking' conserve line self-resolving ('- You are low on carrots — no errand for it now; sell first, then " +
			"restock once your purse recovers.'): no co-present seller, no walk-to target, no bare lack for the model to " +
			"improvise a destination for. Cross-scenario guards: TestGoldensConserveKeeperNeverGetsBuyImperative + " +
			"TestGoldensConserveLowItemAlwaysSelfResolves.",
		build: coinPoorKeeperAloneConserveLowStock,
	},
	{
		name: "dairy_keeper_out_of_booster_at_post",
		summary: "LLM-248 optional booster inputs (the LLM-83 dairy sage edge): an Elizabeth-shaped dairy keeper at her farm " +
			"on shift, milk recipe carrying a sage booster (1 sage per execution → +2 milk), sage a buy entry at 0 on hand. " +
			"The golden pins the '## Keeping up production' booster line — the forgone-bonus motivation ('a measure of sage " +
			"in each batch of milk adds 2 extra to the yield') with NO supplier/structure_id/tool mechanics on the line " +
			"(the LLM-64 split; the adjacent '## Restocking' section carries the where). A booster is elective, so the line " +
			"must not read as a stall: no runway / 'enough for about N more' phrasing for it.",
		build: dairyKeeperOutOfBoosterAtPost,
	},
	{
		name: "producer_derived_input_demand",
		summary: "LLM-260 derived procurement, the live Hannah Boggs case: an innkeeper with `porridge: produce` and NO " +
			"hand-authored buy entries stands at her inn with zero milk and zero water (porridge needs both). A dairy farm " +
			"sells milk; nobody anywhere sells water. The golden pins that demand is DERIVED from the recipe — milk gets " +
			"the '## Keeping up production' runway line AND the '## Restocking' walk-to line with no explicit buy entry — " +
			"and that the unobtainable water surfaces NOWHERE (no runway line, no restock line): the buy-path gate keeps " +
			"the engine from motivating a purchase that cannot happen (the phantom fetch-water hires). Matrix-wide guard: " +
			"TestGoldensUnobtainableInputSurfacesNoDemand.",
		build: producerDerivedInputDemand,
	},
	{
		name: "producer_self_sourced_input_no_demand",
		summary: "LLM-260 self-source override, the John Ellis water case: a tavernkeeper produces stew (needs water + meat) " +
			"and ALSO produces his own water — both at zero on hand, with live vendors selling both. The golden pins that " +
			"meat derives buy demand (runway + restock lines) while water derives NONE despite the vendor and the empty " +
			"stock: a produce/forage entry for an input means 'I self-source this', so the derived-demand walk skips it. " +
			"The '## Time to produce' forge cue rendering alongside is deliberate: it shows the self-sourced water routed " +
			"to the PRODUCE path (water listed as craftable, stew annotated short of it — LLM-257) while the bought input " +
			"routes to the BUY path — the two procurement lanes of the same recipe, side by side.",
		build: producerSelfSourcedInputNoDemand,
	},
}

// producerDerivedInputDemand is the LLM-260 derived-demand fixture: Hannah-shaped
// innkeeper producing porridge (milk 3 + water 5 per 4-unit batch), no explicit
// buy entries, zero of both inputs. Milk has a first-hand supplier (Ellis Farm);
// water has no vendor anywhere — the derived milk demand surfaces in both restock
// sections, the derived water demand in neither.
func producerDerivedInputDemand() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		ellisID  = sim.ActorID("ellis")
		inn      = sim.StructureID("boggs_inn")
		farm     = sim.StructureID("ellis_farm")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             20,
		Inventory:         map[sim.ItemKind]int{"porridge": 11},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "porridge", Source: sim.RestockSourceProduce, Max: 12},
		}},
	}
	ellis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Elizabeth Ellis",
		State:           sim.StateIdle,
		WorkStructureID: farm,
		Inventory:       map[sim.ItemKind]int{"milk": 30},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah, ellisID: ellis},
		Structures: map[sim.StructureID]*sim.Structure{
			inn:  plainStructure(inn, "Boggs Inn"),
			farm: plainStructure(farm, "Ellis Farm"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"porridge": {Name: "porridge", DisplayLabel: "porridge", Category: sim.ItemCategoryFood},
			"milk":     {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
			"water":    {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"porridge": {
				OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
				Inputs:         []sim.RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}},
				WholesalePrice: 2, RetailPrice: 3,
			},
		},
		RestockReorderPct: 25,
	}
	return snap, hannahID, nil
}

// producerSelfSourcedInputNoDemand is the LLM-260 self-source-override fixture:
// John-shaped tavernkeeper producing stew (water 10 + meat 10 per 5-unit batch)
// AND his own water, zero of both inputs on hand, with first-hand vendors
// selling both. Meat derives buy demand; water derives none — the produce entry
// is the override, not vendor absence.
func producerSelfSourcedInputNoDemand() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID   = sim.ActorID("john")
		amosID   = sim.ActorID("amos")
		wellID   = sim.ActorID("welkeeper")
		tavern   = sim.StructureID("ellis_tavern")
		butchery = sim.StructureID("amos_butchery")
		wellHut  = sim.StructureID("well_hut")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             25,
		Inventory:         map[sim.ItemKind]int{"stew": 4},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 10},
			{Item: "water", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	amos := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Amos Reed",
		State:           sim.StateIdle,
		WorkStructureID: butchery,
		Inventory:       map[sim.ItemKind]int{"meat": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "meat", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	// A live water vendor proves the water silence below is the self-source
	// override, not a missing buy path.
	welkeeper := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Josiah Thorne",
		State:           sim.StateIdle,
		WorkStructureID: wellHut,
		Inventory:       map[sim.ItemKind]int{"water": 30},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "water", Source: sim.RestockSourceProduce, Max: 30},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			johnID: john, amosID: amos, wellID: welkeeper,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern:   plainStructure(tavern, "Ellis Tavern"),
			butchery: plainStructure(butchery, "Reed Butchery"),
			wellHut:  plainStructure(wellHut, "Well Hut"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"stew":  {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood},
			"meat":  {Name: "meat", DisplayLabel: "meat"},
			"water": {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"stew": {
				OutputItem: "stew", OutputQty: 5, RateQty: 5, RatePerHours: 2,
				Inputs:         []sim.RecipeInput{{Item: "water", Qty: 10}, {Item: "meat", Qty: 10}},
				WholesalePrice: 2, RetailPrice: 4,
			},
			"water": {OutputItem: "water", OutputQty: 10, RateQty: 10, RatePerHours: 1},
		},
		RestockReorderPct: 25,
	}
	return snap, johnID, nil
}

// dairyKeeperOutOfBoosterAtPost is the LLM-248 booster-cue fixture: a dairy
// keeper on shift inside her farm, producing milk whose recipe carries an
// optional sage booster (+2 per boosted execution), with sage as a buy-restock
// entry and none on hand. A sage-growing herbalist exists as a walk-to supplier
// — the LLM-260 buy-path gate silences the booster motivation when the item is
// unobtainable, so a vendor keeps the line rendering, and the golden now also
// pins the LLM-64 pairing: the booster motivate-line and the "## Restocking"
// sage buy line appear together.
func dairyKeeperOutOfBoosterAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		elizabethID = sim.ActorID("elizabeth")
		prudenceID  = sim.ActorID("prudence")
		farm        = sim.StructureID("ellis_farm")
		herbGarden  = sim.StructureID("ward_garden")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Elizabeth Ellis",
		Role:              "dairywoman",
		State:             sim.StateIdle,
		WorkStructureID:   farm,
		InsideStructureID: farm,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             26,
		Inventory:         map[sim.ItemKind]int{"milk": 10},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "sage", Source: sim.RestockSourceBuy, Max: 3},
		}},
	}
	// The sage supplier: a forage-sourced herbalist at her own garden, stocked —
	// the actionable buy path the booster line (and the sage Restocking line) is
	// gated on.
	prudence := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Prudence Ward",
		State:           sim.StateIdle,
		WorkStructureID: herbGarden,
		Inventory:       map[sim.ItemKind]int{"sage": 5},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "sage", Source: sim.RestockSourceForage, Max: 5},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{elizabethID: elizabeth, prudenceID: prudence},
		Structures: map[sim.StructureID]*sim.Structure{
			farm:       plainStructure(farm, "Ellis Farm"),
			herbGarden: plainStructure(herbGarden, "Ward Garden"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
			"sage": {Name: "sage", DisplayLabel: "sage"},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"milk": {
				OutputItem: "milk", OutputQty: 4, RateQty: 4, RatePerHours: 1,
				BoostInputs:    []sim.BoostInput{{Item: "sage", Qty: 1, BonusQty: 2}},
				WholesalePrice: 1, RetailPrice: 2,
			},
		},
		RestockReorderPct: 25,
	}
	return snap, elizabethID, nil
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
		// Produces carrots, so it's a first-hand supplier (LLM-252). Untagged (not
		// wholesaler), so this fixture isolates the shut-drop, not the wholesale gate.
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	james := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "James Fuller",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 420, Y: 420},
		WorkStructureID: jamesFarm,
		Inventory:       map[sim.ItemKind]int{"carrots": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceProduce, Max: 40},
		}},
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

// resellerRestockRoutedToDistributorNotFarm is the LLM-223 wholesale-tier fixture
// (generalized to the wholesaler tag in LLM-252): a non-distributor reseller
// (Hannah Boggs, the innkeeper) is low on milk and has two milk suppliers — Ellis
// Farm (tagged farm+wholesaler, and a milk PRODUCER so only the wholesale gate can
// drop it) and Josiah's General Store (the distributor-tagged wholesaler). The
// wholesale source is dropped from every non-distributor's buy cues, so the
// "## Restocking" section lists ONLY the General Store as the walk-to target:
// Hannah restocks wholesale-origin milk through the distributor, never straight
// from the farm the PayWithItem backstop would refuse. Keeps
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
		// Produces milk, so the LLM-252 supplier gate would KEEP him — isolating the
		// wholesale gate as the sole reason he's dropped from Hannah's cues below.
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 40},
		}},
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
			sim.VillageObjectID(farm):  {ID: sim.VillageObjectID(farm), OwnerActorID: ellisID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
	}
	return snap, hannahID, nil
}

// coinPoorOverstockedKeeperConserves is the LLM-294 working-capital tone-gate fixture.
// Hannah Boggs keeps her inn on shift with just 1 coin (below the 10-coin floor) while
// holding 20 porridge she made and can't move (dead stock — over the absolute 8 floor,
// no recent sales) and is low on the milk she buys in (2 of 12). Josiah, who produces
// milk, shares her huddle — the co-present buy path that would normally fire "buy it
// now". The golden pins both tiers of the gate: "## Restocking" flips to the
// conserve steer (milk named, no buy imperative) and "## What your wares fetch" gains
// the sell-first nudge naming porridge (her most-overstocked ware) and the sell tool.
// Fixed PublishedAt, no price book, no orders → byte-stable, no wall-clock in render.
func coinPoorOverstockedKeeperConserves() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		josiahID = sim.ActorID("josiah")
		inn      = sim.StructureID("the_inn")
		store    = sim.StructureID("general_store")
		huddleID = sim.HuddleID("inn_huddle")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the inn
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		CurrentHuddleID:   huddleID,
		Coins:             1,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"porridge": 20, "milk": 2},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 11, Y: 10},
		WorkStructureID:   store,
		InsideStructureID: inn, // co-present in the inn huddle so the milk buy path resolves this tick
		CurrentHuddleID:   huddleID,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"milk": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:       published,
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		MerchantCoinFloor: 10,
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah, josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			inn:   plainStructure(inn, "The Inn"),
			store: plainStructure(store, "General Store"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {ID: huddleID, Members: map[sim.ActorID]struct{}{hannahID: {}, josiahID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"porridge": {OutputItem: "porridge", OutputQty: 10, WholesalePrice: 1, RetailPrice: 2,
				Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}}},
			"milk":  {OutputItem: "milk", WholesalePrice: 1, RetailPrice: 2},
			"water": {OutputItem: "water", WholesalePrice: 1, RetailPrice: 1},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"porridge": {Name: "porridge", DisplayLabel: "porridge", Category: sim.ItemCategoryFood},
			"milk":     {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
			"water":    {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
	}
	return snap, hannahID, nil
}

// coinPoorKeeperAloneConserveLowStock is the LLM-298 dangling-want repro. It mirrors
// the live scene (019f38de, 2026-07-06): John Ellis, tavernkeeper, ALONE at his post
// with a thin purse (8 coins, below the 10 floor) and overstocked shelves (20 ale,
// 14 bread, 13 stew — all clearing the 8 dead-stock floor, no recent sales) is low on
// the carrots he buys in (1 of cap 6). His carrot supplier is Josiah, the village
// DISTRIBUTOR at the general store (a reseller buys from the distributor, not wholesale
// from a farm), NOT co-present — so the conserve branch strips the walk-to list and the
// co-present buy imperative alike. The golden pins the self-resolving conserve line:
// named lack + what to do INSTEAD (hold, sell first, restock later), never a bare want
// with no outlet — the vacuum that made the live NPC invent a "Market" to move_to.
// Clock-free render (no price book, no orders), byte-stable.
func coinPoorKeeperAloneConserveLowStock() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID   = sim.ActorID("john")
		josiahID = sim.ActorID("josiah")
		tavern   = sim.StructureID("the_tavern")
		store    = sim.StructureID("general_store")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the tavern, alone
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             8,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"ale": 20, "bread": 14, "stew": 13, "carrots": 1},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "ale", Source: sim.RestockSourceProduce, Max: 24},
			{Item: "bread", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "carrots", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
	// Josiah runs the general store — the village DISTRIBUTOR, John's carrot supplier
	// (a reseller buys from the distributor, not wholesale from a farm). NOT co-present,
	// so the conserve branch strips his walk-to entry too (no destination dangled). John
	// has never bought from him (no price on record), so the unknown price keeps the
	// supplier past the affordability drop even on 8 coins. A distributor is a restock
	// supplier of anything he stocks (isRestockSupplierOf via the TagDistributor store),
	// so no RestockPolicy is needed on him.
	josiah := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Josiah Thorne",
		Role:            "shopkeeper",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: store,
		Inventory:       map[sim.ItemKind]int{"carrots": 40},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		MerchantCoinFloor: 10,
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{johnID: john, josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "The Tavern"),
			store:  plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			// The general store is the village distributor — the one carrot source a
			// plain reseller (John) can see. A wholesaler-tagged farm would be visible
			// only to the distributor himself (eachVendorOffer), so the reseller's
			// supply chain runs through the distributor. This surfaces the walk-to path
			// (which conserve then strips) — the live config where the low-carrots line renders.
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"ale":     {OutputItem: "ale", OutputQty: 10, WholesalePrice: 1, RetailPrice: 2},
			"bread":   {OutputItem: "bread", OutputQty: 10, WholesalePrice: 1, RetailPrice: 2},
			"stew":    {OutputItem: "stew", OutputQty: 10, WholesalePrice: 1, RetailPrice: 2},
			"carrots": {OutputItem: "carrots", WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"ale":     {Name: "ale", DisplayLabel: "ale", Category: sim.ItemCategoryDrink},
			"bread":   {Name: "bread", DisplayLabel: "bread", Category: sim.ItemCategoryFood},
			"stew":    {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood},
			"carrots": {Name: "carrots", DisplayLabel: "carrots", Category: sim.ItemCategoryFood},
		},
		RestockReorderPct: 25,
	}
	// No restock warrant: LLM-298 Phase 2 suppresses the buy-restock wakeup for a
	// conserving keeper (sim.actorConserving), so the real steady-state render for John
	// is a routine at-post turn — the "## Restocking" section still renders (it is
	// content-gated on low stock, not on the warrant) and carries the self-resolving
	// line. The producer-side suppression is pinned separately in restock_tick_test.go.
	return snap, johnID, nil
}

// distributorRestockSkipsCoPresentReseller is the LLM-252 buy-back-guard fixture:
// Josiah Thorne (the distributor) has dipped below his carrot reorder threshold
// while John Ellis (the tavernkeeper, a carrot RESELLER via a `buy` entry) is
// co-present holding 12 carrots — the exact buy-back trigger. His only genuine
// carrot supplier, Moses at James Farm, PRODUCES carrots and is not co-present. The
// golden pins that the "## Restocking" carrot cue routes Josiah to the producing
// James Farm as a walk-to and NEVER surfaces John: a reseller who merely holds the
// item is not a first-hand supplier, so he is neither listed as a walk-to target
// nor named in the co-present buy-here imperative. Josiah is the distributor, so the
// wholesale gate keeps the farm visible to him; the reseller drop is the LLM-252
// producer/forager/distributor gate. Clock-free: no price/shut memory, no wall-clock
// read in the render path.
func distributorRestockSkipsCoPresentReseller() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID  = sim.ActorID("josiah")
		johnID    = sim.ActorID("john")
		mosesID   = sim.ActorID("moses")
		store     = sim.StructureID("general_store")
		tavern    = sim.StructureID("the_tavern")
		jamesFarm = sim.StructureID("james_farm")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the store
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   store,
		InsideStructureID: store,
		CurrentHuddleID:   "h1",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"carrots": 1}, // 1 of cap 6 → below the 25% reorder line
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
	// John is a carrot RESELLER (buy entry) co-present with Josiah, holding stock —
	// the buy-back specimen. He must NOT surface as a supplier.
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 11, Y: 10},
		WorkStructureID:   tavern,
		InsideStructureID: store, // visiting the distributor's store, carrots in hand — the buy-back specimen
		CurrentHuddleID:   "h1",
		Inventory:         map[sim.ItemKind]int{"carrots": 12},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	// Moses PRODUCES carrots at the farm — the legitimate supplier, not co-present.
	moses := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Moses James",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: jamesFarm,
		Inventory:       map[sim.ItemKind]int{"carrots": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josiahID: josiah, johnID: john, mosesID: moses,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			store:     plainStructure(store, "General Store"),
			tavern:    plainStructure(tavern, "The Tavern"),
			jamesFarm: plainStructure(jamesFarm, "James Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store):     {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
			sim.VillageObjectID(jamesFarm): {ID: sim.VillageObjectID(jamesFarm), OwnerActorID: mosesID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"carrots": {Name: "carrots", DisplayLabel: "carrots", Category: sim.ItemCategoryFood},
		},
		RestockReorderPct: 25,
	}
	return snap, josiahID, nil
}

// distributorRestockingMilkBulkRateAnchor is the LLM-292 buy-leg fixture: the
// distributor, low on milk (2 of cap 12), with the wholesaler-tagged Ellis Farm
// as his walk-to supplier and milk priced wholesale 1 / retail 2 in the catalog.
// Pins the catalog buying-in anchor on the Restocking line. No price book — the
// per-vendor last-paid CostText, the affordability fact, and the weekly P&L all
// stay silent, so the anchor is the line's only rate. Clock-free render path.
func distributorRestockingMilkBulkRateAnchor() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID    = sim.ActorID("josiah")
		elizabethID = sim.ActorID("elizabeth")
		store       = sim.StructureID("general_store")
		farm        = sim.StructureID("ellis_farm")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the store
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
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"milk": 2},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	elizabeth := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Elizabeth Ellis",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: farm,
		Inventory:       map[sim.ItemKind]int{"milk": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josiahID: josiah, elizabethID: elizabeth,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
			farm:  plainStructure(farm, "Ellis Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
			sim.VillageObjectID(farm):  {ID: sim.VillageObjectID(farm), OwnerActorID: elizabethID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"milk": {OutputItem: "milk", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
	}
	return snap, josiahID, nil
}

// distributorRestockObservedSupplierRate is the LLM-295 observed-buy-anchor fixture:
// the distributor-buys-milk-from-the-farm shape, but the PriceBook carries a real
// Ellis Farm milk sale at ~2 coins/unit (to an off-snapshot villager), above the
// catalog wholesale seed of 1. Pins that the "## Restocking" buy anchor reports the
// OBSERVED reachable-supplier rate once transaction data exists. Clock-anchored via
// PublishedAt so the observation lands inside the sales window.
func distributorRestockObservedSupplierRate() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID    = sim.ActorID("josiah")
		elizabethID = sim.ActorID("elizabeth")
		store       = sim.StructureID("general_store")
		farm        = sim.StructureID("ellis_farm")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the store
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
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
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"milk": 2},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	elizabeth := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Elizabeth Ellis",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: farm,
		Inventory:       map[sim.ItemKind]int{"milk": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	// Ellis has actually been selling milk at ~2 coins/unit — above the catalog
	// wholesale seed of 1. The observed rate is what the anchor must report. Buyer
	// is off-snapshot, so the distributor keeps no last-paid at Ellis (empty CostText).
	ellisMilkSales := sim.NewRingBuffer[sim.PriceObservation](8)
	ellisMilkSales.Push(sim.PriceObservation{BuyerID: "mary", Amount: 2, Qty: 1, Consumers: 1, At: published.Add(-6 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josiahID: josiah, elizabethID: elizabeth,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
			farm:  plainStructure(farm, "Ellis Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
			sim.VillageObjectID(farm):  {ID: sim.VillageObjectID(farm), OwnerActorID: elizabethID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"milk": {OutputItem: "milk", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: elizabethID, Item: "milk"}: ellisMilkSales,
		},
	}
	return snap, josiahID, nil
}

// wholesalerProducerObservedRates is the LLM-295 observed-sell-figures fixture: the
// wholesale producer Moses stands with a customer, and the PriceBook carries real
// transactions for both figures on his wholesale-channel wares line — his own carrot
// sales to the shop at ~2 coins (the bulk rate) and the shop's carrot resales to folk
// at ~5 coins (the shelf rate), both above the catalog seed (wholesale 1 / retail 3).
// Josiah does not produce carrots, so his sales count as the shop/shelf side, not the
// wholesale side. Pins the line reporting the OBSERVED rates in lived phrasing.
func wholesalerProducerObservedRates() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		mosesID   = sim.ActorID("moses")
		silenceID = sim.ActorID("silence")
		josiahID  = sim.ActorID("josiah")
		commons   = sim.StructureID("commons")
		farm      = sim.StructureID("james_farm")
		store     = sim.StructureID("general_store")
		huddle    = sim.HuddleID("h1")
	)
	published := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	moses := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Moses James",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		WorkStructureID:   farm,
		Coins:             38,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"carrots": 30},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceProduce, Max: 30},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		Role:              "villager",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             22,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Moses James": {}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		InsideStructureID: store,
		WorkStructureID:   store,
	}
	// Bulk rate observed: Moses's own carrot sales to the shop at ~2/unit.
	mosesCarrotSales := sim.NewRingBuffer[sim.PriceObservation](8)
	mosesCarrotSales.Push(sim.PriceObservation{BuyerID: josiahID, Amount: 2, Qty: 1, Consumers: 1, At: published.Add(-12 * time.Hour)})
	// Shelf rate observed: the shop's carrot resales to folk at ~5/unit. Josiah does
	// not produce carrots, so observedShopSales counts him as a shop, not wholesale.
	shopCarrotSales := sim.NewRingBuffer[sim.PriceObservation](8)
	shopCarrotSales.Push(sim.PriceObservation{BuyerID: silenceID, Amount: 5, Qty: 1, Consumers: 1, At: published.Add(-6 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{mosesID: moses, silenceID: silence, josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			commons: plainStructure(commons, "Village Commons"),
			farm:    plainStructure(farm, "James Farm"),
			store:   plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm):  {ID: sim.VillageObjectID(farm), OwnerActorID: mosesID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{mosesID: {}, silenceID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"carrots": {OutputItem: "carrots", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 3},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"carrots": {
				Name: "carrots", DisplayLabel: "Carrots",
				DisplayLabelSingular: "carrot", DisplayLabelPlural: "carrots",
				Category: sim.ItemCategoryFood,
			},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: mosesID, Item: "carrots"}:  mosesCarrotSales,
			{SellerID: josiahID, Item: "carrots"}: shopCarrotSales,
		},
	}
	return snap, mosesID, nil
}

// millerWheatRestockFlatBandAnchor is the LLM-292 flat-band fixture: the miller
// produces flour from bought-in wheat (derived buy entry — no hand-authored wheat
// row), his wheat shelf (2) is below the two-batch floor (2×4=8), and wheat's
// catalog band is flat 1/1. His supplier is the distributor's store. Pins the
// anchor clause collapsing a single-priced band to its one price, riding a
// DERIVED entry, alongside the production runway line. Also carries a second low
// buy entry with NO catalog price (sacks, 1 of cap 8, same store supplier) whose
// line must render anchor-FREE — the mixed priced/unpriced section that keeps the
// per-line attachment check in TestRestockCatalogAnchorRendersWithCatalogPrice
// non-vacuous (code_review). Clock-free render path.
func millerWheatRestockFlatBandAnchor() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josephID = sim.ActorID("joseph")
		josiahID = sim.ActorID("josiah")
		mill     = sim.StructureID("the_mill")
		store    = sim.StructureID("general_store")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the mill
	joseph := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Joseph Scott",
		Role:              "miller",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   mill,
		InsideStructureID: mill,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"wheat": 2, "sack": 1},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "flour", Source: sim.RestockSourceProduce, Max: 10},
			{Item: "sack", Source: sim.RestockSourceBuy, Max: 8},
		}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Josiah Thorne",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 200, Y: 200},
		WorkStructureID: store,
		Inventory:       map[sim.ItemKind]int{"wheat": 40, "sack": 20},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josephID: joseph, josiahID: josiah,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			mill:  plainStructure(mill, "The Mill"),
			store: plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(mill):  {ID: sim.VillageObjectID(mill), OwnerActorID: josephID, Tags: []string{sim.TagWholesaler}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"flour": {OutputItem: "flour", OutputQty: 2, RateQty: 2, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 3,
				Inputs: []sim.RecipeInput{{Item: "wheat", Qty: 4}}},
			"wheat": {OutputItem: "wheat", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"wheat": {Name: "wheat", DisplayLabel: "wheat", Category: sim.ItemCategoryMaterial},
			"flour": {Name: "flour", DisplayLabel: "flour", Category: sim.ItemCategoryMaterial},
			"sack":  {Name: "sack", DisplayLabel: "sacks", Category: sim.ItemCategoryMaterial},
		},
		RestockReorderPct: 25,
	}
	return snap, josephID, nil
}

// ownerHoldingRepairNailsInCompany is the LLM-292 earmark fixture: Josiah on the
// loiter pin of his worn General Store (wear 450 ≥ repair threshold 400, below
// degrade 600), holding 3 of the 5 nails a mend takes plus his ordinary milk
// stock (6 of cap 12 — above the reorder line, so no Restocking section), in a
// huddle with John Ellis. Pins the wares-cue repair-reserve line alongside the
// ordinary ware line and the "## Your business" mend nag. Clock-free render path.
func ownerHoldingRepairNailsInCompany() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID = sim.ActorID("josiah")
		johnID   = sim.ActorID("john")
		store    = sim.VillageObjectID("general_store")
		huddle   = sim.HuddleID("h1")
	)
	zero := 0
	now := 600 // 10:00
	storePin := sim.WorldPos{X: 100, Y: 100}.Tile()
	josiah := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Josiah Thorne",
		Role:            "shopkeeper",
		State:           sim.StateIdle,
		Pos:             storePin,
		CurrentHuddleID: huddle,
		Coins:           20,
		Needs:           map[sim.NeedKey]int{},
		Inventory:       map[sim.ItemKind]int{"nail": 3, "milk": 6},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceBuy, Max: 12},
		}},
		Acquaintances: map[string]sim.Acquaintance{"John Ellis": {}},
	}
	john := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "John Ellis",
		Role:            "tavernkeeper",
		State:           sim.StateIdle,
		Pos:             storePin,
		CurrentHuddleID: huddle,
		Coins:           15,
		Needs:           map[sim.NeedKey]int{},
		Acquaintances:   map[string]sim.Acquaintance{"Josiah Thorne": {}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josiahID: josiah, johnID: john,
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			store: {
				ID:            store,
				DisplayName:   "General Store",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  josiahID,
				Tags:          []string{sim.TagBusiness},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{josiahID: {}, johnID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"milk": {OutputItem: "milk", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
			"nail": {Name: "nail", DisplayLabel: "nails", Category: sim.ItemCategoryMaterial},
		},
		RestockReorderPct: 25,
	}
	return snap, josiahID, nil
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

// buyerOfferDeclinedSellerShortStock is the LLM-296 buyer-POV fixture: Josiah
// Thorne offered 6 carrots + 1 coin for 5 nails to Ezekiel Crane, who declined —
// Ezekiel holds only 1 nail. The settled ledger entry is Declined, and the golden
// pins that "## Recently settled offers" names the OFFERED bundle (so a re-post
// reads as visibly the same, not a byte-identical thin line the model can't tell
// apart) and appends the engine-known shortfall "they hold only 1 nail" as the
// informed reason it closed. Clock-free: the recency window is measured against
// the fixture's PublishedAt / ResolvedAt and the render path reads no wall clock.
func buyerOfferDeclinedSellerShortStock() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID  = sim.ActorID("josiah")
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
	)
	now := 1113 // 18:33, the live window
	published := time.Date(2026, 7, 6, 18, 33, 0, 0, time.UTC)
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Josiah Thorne",
		Role:              "traveler",
		State:             sim.StateIdle,
		InsideStructureID: forge,
		Coins:             1,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"carrots": 6},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		InsideStructureID: forge,
		WorkStructureID:   forge,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 1},
	}
	declined := &sim.PayLedgerEntry{
		ID: 866, BuyerID: josiahID, SellerID: ezekielID,
		ItemKind: "nail", Qty: 5, Amount: 1,
		PayItems:   []sim.ItemKindQty{{Kind: "carrots", Qty: 6}},
		State:      sim.PayLedgerStateDeclined,
		ResolvedAt: published.Add(-30 * time.Second),
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah, ezekielID: ezekiel},
		Quotes:           map[sim.QuoteID]*sim.SceneQuote{},
		PayLedger:        map[sim.LedgerID]*sim.PayLedgerEntry{866: declined},
		Scenes:           map[sim.SceneID]*sim.Scene{},
		Huddles:          map[sim.HuddleID]*sim.Huddle{},
		Structures:       map[sim.StructureID]*sim.Structure{forge: plainStructure(forge, "Blacksmith")},
	}
	return snap, josiahID, nil
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

// cheeseKind is the shared eat-here cheese ItemKindDef the LLM-222 buy-cue
// scenarios sell — a good meal for hunger, matching buyerRemembersVendorShut.
func cheeseKind() *sim.ItemKindDef {
	return &sim.ItemKindDef{
		Name: "cheese", DisplayLabel: "Cheese",
		DisplayLabelSingular: "wedge of cheese", DisplayLabelPlural: "wedges of cheese",
		Category:  sim.ItemCategoryFood,
		Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
	}
}

// buyerDropsShutKeepsOpenVendor — LLM-222. A hungry forager (6 coins) can buy
// cheese at two shops: the General Store he remembers finding shut, and the open
// Tavern he has no shut memory of. The buy cue drops the shut store and keeps the
// open Tavern — the surgical eat/drink analogue of the restock
// keeper_restock_drops_shut_keeps_open_supplier golden.
func buyerDropsShutKeepsOpenVendor() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		mabelID   = sim.ActorID("mabel")
		johnID    = sim.ActorID("john")
		store     = sim.StructureID("general_store")
		tavern    = sim.StructureID("tavern")
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
		// He found the General Store shut within the decay window; the Tavern he has
		// no shut memory of.
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
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		Coins:             20,
		Inventory:         map[sim.ItemKind]int{"cheese": 5},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, mabelID: mabel, johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			store:  plainStructure(store, "General Store"),
			tavern: plainStructure(tavern, "The Tavern"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{"cheese": cheeseKind()},
	}
	return snap, ezekielID, nil
}

// brokeBuyerWithGoodsBartersForFood — LLM-222 means-to-pay "barter" state. A
// hungry forager with 0 coins but a pelt to trade stands near an open cheese
// seller: the buy cue is kept but steered to a goods offer, because barter is a
// viable path a coins==0 suppression would have hidden.
func brokeBuyerWithGoodsBartersForFood() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
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
		Coins:       0,
		// A non-food trade good he can put up in barter, but no coins. Non-food so it
		// doesn't add an own-stock "consume to eat" cue.
		Inventory: map[sim.ItemKind]int{"pelt": 1},
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
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
			"cheese": cheeseKind(),
			"pelt":   {Name: "pelt", DisplayLabel: "Pelt", DisplayLabelSingular: "pelt"},
		},
	}
	return snap, ezekielID, nil
}

// brokeBuyerNoGoodsNoBuyCue — LLM-222 means-to-pay suppression. The same broke
// forager, now with nothing to trade: no coins and no goods is the one genuine
// payment dead-end, so the buy cue is dropped entirely and (no free source or own
// stock nearby) the whole eat/drink section is omitted.
func brokeBuyerNoGoodsNoBuyCue() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
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
		Coins:       0,
		Needs:       map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
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
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{"cheese": cheeseKind()},
	}
	return snap, ezekielID, nil
}

// brokeBuyerNoGoodsNoPeerBuy — LLM-242 means-to-pay suppression, the co-present
// peer arm (sibling of brokeBuyerNoGoodsNoBuyCue's vendor arm). The same broke
// forager (0 coins, nothing to trade) stands in a huddle with a co-present peer
// (Lewis) carrying stew he'd otherwise be told to buy with pay_with_item. No
// coins and no goods is no means to pay, so the peer offer is dropped; with no
// free source or own stock nearby the whole "## What you can eat or drink"
// section is omitted. Contrast peers_holding_same_food, where the subject holds
// stew (goods) and so keeps a means to pay.
func brokeBuyerNoGoodsNoPeerBuy() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
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
		Role:              "forager",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
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

// TestForgeCueOnlyForMultiOutputCrafterAtForge is the LLM-116 cross-scenario
// invariant: the "## Time to produce" cue appears in EXACTLY the multi-output-producer-
// at-workplace scenarios and no other — whether unfocused (choose menu,
// smith_choosing_at_forge / the non-smith dairy_choosing_at_farm), focused-and-
// productive (the LLM-128 continue-and-stop steer, smith_forging_focused), or
// focused-but-input-starved (falls back to the choose menu, LLM-257
// tavernkeeper_starved_focus_at_forge). A single-output producer or a non-crafter
// must never see it — the structural property the per-builder gate (>1 produce entry
// AND at workplace) is meant to hold across the whole matrix.
func TestForgeCueOnlyForMultiOutputCrafterAtForge(t *testing.T) {
	const marker = "## Time to produce"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "smith_choosing_at_forge" || sc.name == "smith_forging_focused" || sc.name == "dairy_choosing_at_farm" || sc.name == "tavernkeeper_starved_focus_at_forge" ||
			sc.name == "producer_self_sourced_input_no_demand" // multi-output (stew + water) keeper at his forge, LLM-260
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: forge cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestGoldensNoInputGoodAlwaysCraftable is the LLM-257 cross-scenario invariant:
// in any scenario's forge-choice view, a good whose recipe has NO inputs must never
// be flagged not-craftable. An origin producer (nail, water) makes its good from
// nothing, so HasProduceInputs is always satisfied for it — the "always makeable"
// property the inputs-aware gate must preserve. Guards against a future change that
// mistakes a no-input recipe for a starved one, which would drop the no-input good
// from the choose menu and could re-freeze a keeper behind an unmakeable focus with
// no fallback. White-box over buildForgeChoice so it reads the structured
// InputsReady flag rather than parsing rendered text.
func TestGoldensNoInputGoodAlwaysCraftable(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			as := snap.Actors[actorID]
			if as == nil {
				return
			}
			view := buildForgeChoice(snap, actorID, as)
			if view == nil {
				return // not a multi-output crafter at its forge — no forge choice here
			}
			for _, it := range view.Items {
				recipe := snap.Recipes[it.itemKind]
				if recipe == nil {
					continue
				}
				hasInput := false
				for _, in := range recipe.Inputs {
					if in.Qty > 0 {
						hasInput = true
						break
					}
				}
				if !hasInput && !it.InputsReady {
					t.Errorf("scenario %q: no-input good %q flagged not-craftable; an origin recipe is always makeable (LLM-257)", sc.name, it.itemKind)
				}
			}
		})
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
		// innkeeper_pricing_with_makings_cost (LLM-226) and
		// producer_input_below_batch_floor_reorders (LLM-279) are both Hannah focused
		// on porridge AT her inn — the same focus-at-work state as the forging smith,
		// so the line is correct there too.
		want := sc.name == "smith_forging_focused" ||
			sc.name == "innkeeper_pricing_with_makings_cost" ||
			sc.name == "producer_input_below_batch_floor_reorders"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: production-focus line present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestGoldensBoosterLineOnlyForBoostedRecipes is the LLM-248 cross-scenario
// invariant: the elective-booster line ("adds N extra to the yield") renders in
// EXACTLY the scenarios whose subject produces a good whose recipe defines a
// booster that is a low bought entry (dairy_keeper_out_of_booster_at_post) and
// nowhere else in the matrix. A booster line leaking into a scenario with no
// boosted recipe would mean the gate regressed to reading required inputs;
// required-input scenarios keep their runway phrasing and never the bonus one.
func TestGoldensBoosterLineOnlyForBoostedRecipes(t *testing.T) {
	const marker = "extra to the yield"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "dairy_keeper_out_of_booster_at_post"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: booster line present=%v, want %v", sc.name, has, want)
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
		// LLM-231: seller_employing_own_laboring_worker also puts the subject in the
		// employer seat of a Working offer (John employs Silence), so the cue is correct there.
		want := sc.name == "employer_with_worker_on_job" || sc.name == "seller_employing_own_laboring_worker"
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
			sc.name == "innkeeper_pricing_with_makings_cost" || // LLM-226: producer in company, priced own ware
			sc.name == "employer_recalls_returning_helper" || // LLM-228: producing keeper in company (incidental to the recall it tests)
			sc.name == "wholesaler_producer_bartering_with_customer" || // LLM-291: wholesale producer in company — cue draws the wholesale-channel line
			sc.name == "wholesaler_producer_observed_rates" || // LLM-295: same, with observed rates on both wholesale-line figures
			sc.name == "owner_holding_repair_nails_in_company" || // LLM-292: keeper in company with priced ware + the repair-reserve earmark
			sc.name == "coin_poor_overstocked_keeper_conserves" // LLM-294: producer in company (priced own wares) + the conserve sell-first nudge
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: wares-worth cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestWholesaleChannelLineOnlyForWholesalerProduce is the LLM-291 cross-scenario
// invariant: the wholesale-channel wares line ("your own produce — it sells in bulk
// to …") appears in EXACTLY the scenarios where a wholesale producer prices its own
// produce in company (the LLM-291 seed-copy scenario and the LLM-295 observed-rate
// scenario). It guards both directions — a wholesaler's own produce must never
// regress to a retail spread (the framing that invited Moses's refused street sale,
// live hud-9b23…), and no ordinary retail producer (the smith, the innkeeper) may
// ever pick up the wholesale line.
func TestWholesaleChannelLineOnlyForWholesalerProduce(t *testing.T) {
	const marker = "your own produce — it sells in bulk to"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "wholesaler_producer_bartering_with_customer" ||
			sc.name == "wholesaler_producer_observed_rates"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: wholesale-channel wares line present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestRenderedPromptsNeverSayDistributor is the LLM-292 copy-constraint invariant
// (Jeff, 2026-07-06): rendered prose must never hand the NPC's LLM a mechanic-role
// term — "distributor" is an engine/tag concept, and the NPC is told who stocks its
// goods in in-world relational terms ("whose shop stocks it for the village", "the
// village storekeeper"), never what engine role that actor holds. Runs the whole
// matrix so no future cue — or a fixture Role string leaking through a peer label —
// can reintroduce it.
func TestRenderedPromptsNeverSayDistributor(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			if got := renderScenario(sc); strings.Contains(strings.ToLower(got), "distributor") {
				t.Errorf("scenario %q: rendered prompt contains the mechanic-role term %q (LLM-292 copy constraint)", sc.name, "distributor")
			}
		})
	}
}

// TestRepairReserveLineOnlyForOwnerWithMendAndNails is the LLM-292 cross-scenario
// invariant: the earmarked repair-nails line ("… of these to mend your …") appears
// in EXACTLY the scenario where a business OWNER with a live mend obligation holds
// nails in company (owner_holding_repair_nails_in_company). A hired mender, an
// owner carrying no nails, an owner out of company, or an unworn business must
// never pick it up — and the mend-nag scenarios that share its predicates
// (owner_at_worn_stall & co) stay earmark-free because they render out of company.
func TestRepairReserveLineOnlyForOwnerWithMendAndNails(t *testing.T) {
	const marker = "of these to mend your"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "owner_holding_repair_nails_in_company"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: repair-reserve earmark line present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestRestockBuyAnchorRendersWhenRateKnown is the buy-leg anchor invariant
// (LLM-292, reworked observed-first in LLM-295), re-derived from each scenario's
// fixture rather than trusting the build that produced the section: the buying-in
// anchor ("… above that and you're overpaying") appears on a rendered "##
// Restocking" item line iff that ITEM has a resolvable rate — an observed
// supplier rate if one exists, else the catalog seed — checked per line, not
// section-wide, so a mixed rate/no-rate section can't hide an anchor attached to
// the wrong item (code_review). Guards both directions — a low item with a known
// rate must carry the corrective anchor (the self-poisoning last-paid anchor must
// never again be the cue's only number: the live Josiah 2.2/unit milk leg), and an
// item with neither an observed rate nor a catalog price must not conjure one. A
// fixture that owes an anchor but renders no Restocking section at all fails too
// (the anchor can't render if the section disappears). The marker is the phrase
// both the observed and the seed phrasings share.
func TestRestockBuyAnchorRendersWhenRateKnown(t *testing.T) {
	const marker = "above that and you're overpaying"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subj := snap.Actors[actorID]
			// LLM-294: a keeper in conserve mode (coin-poor + overstocked) has every
			// restock item line replaced by the hold-off-buying steer — no full
			// "- You have N <label>" line, so no catalog anchor is owed regardless of
			// catalog pricing. The per-line loop below is naturally a no-op then (the
			// conserve lines don't match the "- You have " prefix).
			conserve := subj != nil && merchantConserve(snap, actorID, subj).Active
			// Expected-anchor derivation, computed BEFORE looking at the render: an
			// anchor is owed iff some below-threshold effective buy entry (the
			// section's own gate) has a catalog rate AND actually renders a full item
			// line (an unactionable item is omitted per LLM-216; a pending-offer bide
			// steer replaces the whole line per LLM-64; conserve mode replaces every
			// line per LLM-294). Also maps each entry's display label to its rate for
			// the per-line check below.
			want := false
			rateByLabel := map[string]int{}
			labelAmbiguous := map[string]bool{}
			if subj != nil && subj.RestockPolicy != nil {
				floors := sim.ReorderFloors(snap.Recipes, subj.RestockPolicy)
				for _, e := range sim.EffectiveBuyEntries(snap.Recipes, subj.RestockPolicy) {
					// The RESOLVED anchor rate the same way buildRestocking derives it:
					// observed reachable-supplier rate first (LLM-295), catalog seed as
					// fallback. Same reachable-supplier inputs the build uses.
					_, coID := coPresentSellerForItem(snap, actorID, subj, e.Item)
					vendors := findItemVendors(snap, actorID, subj, e.Item)
					rate, observed := observedSupplierBuyRate(vendors, coID, snap, e.Item, restockSalesWindow)
					if !observed {
						rate = catalogBulkRate(snap, e.Item)
					}
					label := itemDisplayLabel(snap, e.Item)
					if prev, ok := rateByLabel[label]; ok && (prev > 0) != (rate > 0) {
						labelAmbiguous[label] = true // two kinds share a label with differing pricedness — per-line check skips it
					}
					rateByLabel[label] = rate
					if !sim.RestockReorderThresholdMet(subj.Inventory[e.Item], e.Cap(), snap.RestockReorderPct, floors[e.Item]) {
						continue
					}
					if rate <= 0 {
						continue
					}
					if !itemHasActionableBuyPath(snap, actorID, subj, e.Item) {
						continue // omitted line (LLM-216) carries no anchor
					}
					if _, coID := coPresentSellerForItem(snap, actorID, subj, e.Item); coID != "" && hasPendingOfferTo(snap, actorID, coID, e.Item) {
						continue // bide steer replaces the item line (LLM-64)
					}
					if conserve {
						continue // conserve mode replaces the item line — no anchor owed (LLM-294)
					}
					want = true
				}
			}
			_, section, found := strings.Cut(renderScenario(sc), "## Restocking\n")
			if !found {
				if want {
					t.Errorf("scenario %q: an anchor-owing buy entry exists but no '## Restocking' section rendered (LLM-292)", sc.name)
				}
				return
			}
			if idx := strings.Index(section, "\n## "); idx >= 0 {
				section = section[:idx]
			}
			if has := strings.Contains(section, marker); has != want {
				t.Errorf("scenario %q: restock catalog anchor present=%v, want %v (LLM-292)", sc.name, has, want)
			}
			// Per-line attachment: every full item line ("- You have N <label> on
			// hand…") carries the anchor iff ITS kind is priced. Bide steers and the
			// walk-to sub-bullets don't match the prefix and are skipped by design.
			for _, line := range strings.Split(section, "\n") {
				rest, ok := strings.CutPrefix(line, "- You have ")
				if !ok {
					continue
				}
				head, _, ok := strings.Cut(rest, " on hand")
				if !ok {
					continue
				}
				label := strings.TrimLeft(head, "0123456789 ")
				rate, known := rateByLabel[label]
				if !known || labelAmbiguous[label] {
					continue // not one of the effective entries (or ambiguous label) — nothing to assert
				}
				if has := strings.Contains(line, marker); has != (rate > 0) {
					t.Errorf("scenario %q: item line %q anchor present=%v, want %v (per-line attachment, LLM-292)", sc.name, line, has, rate > 0)
				}
			}
		})
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

// TestStallRepairCueOnlyAtOwnWornStall is the LLM-118 cross-scenario invariant
// (generalized to all businesses in LLM-247): the "## Your business" owner repair
// cue appears in EXACTLY the scenarios where the actor stands at their OWN worn
// business — never for a passerby (who gets the co-present line instead) or any
// unrelated scenario. Covers a market stall (owner_at_worn/degraded_stall) AND a
// tavern (owner_at_worn_tavern) to pin that the gate is the business tag, not
// market_stall. The same StallRepair signal gates the repair tool, so this also
// pins where the tool is offered.
func TestStallRepairCueOnlyAtOwnWornStall(t *testing.T) {
	const marker = "## Your business"
	ownWornBusiness := map[string]bool{
		"owner_at_worn_stall":                    true,
		"owner_at_worn_stall_with_nail_supplier": true, // LLM-274: owner short of nails WITH a resolvable supplier
		"owner_at_degraded_stall":                true,
		"owner_at_degraded_store_conserving":     true, // LLM-301: vendor-less fallback, conserve arm
		"owner_conserving_with_nail_supplier":    true, // LLM-301: conserve wins over a surviving vendor
		"owner_at_worn_tavern":                   true,
		"owner_inside_worn_business":             true, // LLM-266: owner INSIDE their worn business (not at the outdoor pin)
		"owner_holding_repair_nails_in_company":  true, // LLM-292: owner at own worn store's pin (the earmark fixture)
	}
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := ownWornBusiness[sc.name]
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: '## Your business' cue present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestGoldensRepairCueWheneverColocatedOwnerRepairable is the LLM-266 cross-scenario
// invariant: whenever the subject OWNS a repairable business and is co-located with
// it — standing INSIDE the business structure OR at its outdoor loiter pin — the
// rendered prompt must carry the "## Your business" repair cue. The repair tool is
// gated on the very same StallRepair payload signal (handlers/tool_gating.go), so
// "cue renders" ⇔ "tool advertised". The old pin-only gate silently failed this for
// every indoor keeper — the cue had never once rendered live. Runs over the whole
// matrix so a future change can't re-narrow co-location for any owned-business
// situation, not just the owner_inside_worn_business scenario that anchors it.
// Keyed off the same sim.AtBusiness predicate the production gate uses, so the two
// agree by construction; owner_inside_worn_business is the non-vacuous golden that
// would break if the cue stopped rendering end-to-end while the predicate held.
func TestGoldensRepairCueWheneverColocatedOwnerRepairable(t *testing.T) {
	const header = "## Your business"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			stall := sim.OwnedWearableStall(snap.VillageObjects, actorID)
			if stall == nil {
				return // subject owns no business — invariant N/A here
			}
			if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
				return // business isn't worn enough to mend — no cue expected
			}
			if !sim.AtBusiness(a.Pos, a.InsideStructureID, stall.ID, objectLoiterPin(stall), true) {
				return // subject isn't co-located with their business — cue correctly absent
			}
			if out := renderScenario(sc); !strings.Contains(out, header) {
				t.Errorf("scenario %q: subject owns a repairable business and is co-located with it (inside or at pin) but the prompt omits the %q repair cue (LLM-266)", sc.name, header)
			}
		})
	}
}

// TestGoldensHiredWorkerSeesRepairCueWhenColocated is the LLM-271 cross-scenario
// invariant, the hired-worker twin of TestGoldensRepairCueWheneverColocatedOwnerRepairable:
// whenever the subject is NOT the owner but is Working a hired job whose employer owns
// a repairable business, AND the subject is co-located with that business, the rendered
// prompt must carry the hired "## The business you're working at" cue — never the owner's
// "## Your business" (that would tell a hired hand it owns the shop). The repair tool is
// gated on the same StallRepair payload signal, so "cue renders" ⇔ "tool advertised".
// Keyed off the same sim.WearableStallToMend resolver + sim.AtBusiness predicate the
// production gate uses, so the two agree by construction; hired_worker_at_employer_worn_business
// is the non-vacuous golden that would break if the cue stopped rendering end-to-end.
func TestGoldensHiredWorkerSeesRepairCueWhenColocated(t *testing.T) {
	const (
		hiredHeader = "## The business you're working at"
		ownerHeader = "## Your business"
	)
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			stall, hired := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
			if !hired {
				return // subject reaches no business through a hire — invariant N/A here
			}
			if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
				return // business isn't worn enough to mend — no cue expected
			}
			if !sim.AtBusiness(a.Pos, a.InsideStructureID, stall.ID, objectLoiterPin(stall), true) {
				return // subject isn't co-located with the employer's business — cue correctly absent
			}
			exercised = true
			out := renderScenario(sc)
			if !strings.Contains(out, hiredHeader) {
				t.Errorf("scenario %q: subject is a hired worker co-located with the employer's repairable business but the prompt omits the %q repair cue (LLM-271)", sc.name, hiredHeader)
			}
			if strings.Contains(out, ownerHeader) {
				t.Errorf("scenario %q: subject is a hired worker (not the owner) but the prompt shows the owner %q cue — a hired hand doesn't own the shop (LLM-271)", sc.name, ownerHeader)
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise a hired worker co-located with the employer's repairable business (LLM-271)")
	}
}

// TestOwnerShortNailsWithSupplierNamesDestination is the LLM-274 cross-scenario
// invariant: whenever the OWNER "## Your business" repair cue renders, the owner is
// short of nails, findItemVendors resolves at least one open nail supplier, AND the
// working-capital gate isn't holding buys off (Conserve wins over the vendor list —
// LLM-301), the cue must name that supplier's move_to destination
// ("(structure_id: <id>)") rather than the destination-less dead end that llama-3.3-70b
// narrated but never walked (the live Elizabeth Ellis case). Keyed off the same
// buildStallRepair the production render uses, so the property holds by construction
// across the matrix; owner_at_worn_stall_with_nail_supplier is the non-vacuous golden
// that would break if the destination stopped rendering. Its foil — an owner short of
// nails with NO resolvable supplier — is correctly excluded (NailVendors empty), where
// the generic sentence with no dangling target is the intended output (LLM-216 posture).
func TestOwnerShortNailsWithSupplierNamesDestination(t *testing.T) {
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			v := buildStallRepair(snap, actorID, a)
			if v == nil || v.Hired || v.HasEnoughNails {
				return // not the owner repair cue, or the owner already carries enough nails
			}
			if len(v.NailVendors) == 0 {
				return // no resolvable supplier — the generic no-destination sentence is correct here
			}
			if v.Conserve {
				return // LLM-301: conserve wins over the vendor list — the sell-first soften renders instead
			}
			exercised = true
			token := "(structure_id: " + string(v.NailVendors[0].StructureID) + ")"
			// Scope the assertion to the "## Your business" section so a matching token
			// in some other section (e.g. a co-rendered Restocking cue) can't mask a
			// regression of the repair cue itself (code_review).
			section := promptSection(renderScenario(sc), "## Your business")
			if !strings.Contains(section, token) {
				t.Errorf("scenario %q: owner is short of nails with a resolvable nail supplier but the '## Your business' cue omits its move_to destination %q — the model narrates the errand instead of walking it (LLM-274)", sc.name, token)
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise an owner short on nails with a resolvable nail supplier (LLM-274)")
	}
}

// TestOwnerShortNailsRepairCueNeverGoadsUnactionableBuy is the LLM-301
// cross-scenario invariant, two arms:
//
//  1. CONSERVE WINS: whenever the owner "## Your business" cue renders short of
//     nails and the working-capital gate says hold off (Conserve), the sell-first
//     soften must render and the "Use move_to to reach a supplier" walk-to goad must
//     NOT — even when a supplier survives findItemVendors (the affordability drop
//     and the coin floor are different filters), so this cue can never push a buy
//     while "## Restocking" says hold off (the LLM-297 posture).
//     owner_conserving_with_nail_supplier / owner_at_degraded_store_conserving are
//     the non-vacuous anchors.
//  2. VENDOR-LESS FALLBACK: with no resolvable supplier and no conserve, the
//     section must state the plain shortfall and must NOT name "the smith" — a
//     person-shaped target with no move_to destination (the live model invented
//     "the Smithy" for it and burned its turn retrying the refused move).
//     owner_at_worn_stall & co are the anchors.
func TestOwnerShortNailsRepairCueNeverGoadsUnactionableBuy(t *testing.T) {
	var fallbackExercised, conserveExercised, conserveVendorExercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			v := buildStallRepair(snap, actorID, a)
			if v == nil || v.Hired || v.HasEnoughNails {
				return // not the owner short-of-nails cue — invariant N/A
			}
			section := promptSection(renderScenario(sc), "## Your business")
			if v.Conserve {
				conserveExercised = true
				if len(v.NailVendors) > 0 {
					conserveVendorExercised = true
				}
				if !strings.Contains(section, "your purse can't take on nails just now") {
					t.Errorf("scenario %q: conserve is active but the sell-first soften is missing from the '## Your business' cue (LLM-301)", sc.name)
				}
				if strings.Contains(section, "Use move_to to reach a supplier") || strings.Contains(section, "(structure_id:") {
					t.Errorf("scenario %q: conserve is active but the cue still goads the walk-to nail buy — it must not contradict the '## Restocking' hold-off (LLM-301)", sc.name)
				}
				return
			}
			if len(v.NailVendors) > 0 {
				return // vendor-list branch — TestOwnerShortNailsWithSupplierNamesDestination covers it
			}
			fallbackExercised = true
			if strings.Contains(section, "from the smith") {
				t.Errorf("scenario %q: vendor-less repair fallback names a person-shaped smith target with no move_to destination (LLM-301)", sc.name)
			}
			if !strings.Contains(section, "you'll need to buy more before you can mend it") {
				t.Errorf("scenario %q: vendor-less repair fallback is missing the plain shortfall statement (LLM-301)", sc.name)
			}
		})
	}
	if !fallbackExercised {
		t.Error("matrix must exercise an owner short on nails with no resolvable supplier and no conserve (LLM-301)")
	}
	if !conserveExercised {
		t.Error("matrix must exercise a conserving owner short on nails (LLM-301)")
	}
	if !conserveVendorExercised {
		t.Error("matrix must exercise conserve winning over a surviving nail supplier (LLM-301)")
	}
}

// TestGoldensOwnerOffPostShortNailsSeesBuyErrand is the LLM-277 cross-scenario
// invariant: whenever the subject OWNS a repairable business, is AWAY from it (not
// co-located), is short of the nails a repair takes, and has at least one actionable
// buy path — exactly the state buildStallRepairBuy renders — the prompt must carry the
// off-post "## Nails to mend your business" errand cue AND must NOT fire the
// return-to-post to-work steer (the errand suppresses it while she fetches nails). The
// two properties are the whole point of the ticket: the cue that vanished off-post now
// persists, and the nag that yanked her home now defers. Keyed off the same
// buildStallRepairBuy the production render uses, so it holds by construction across the
// matrix; owner_off_post_at_smith_short_nails / owner_off_post_short_nails_walking are
// the non-vacuous goldens. The at-business owner is excluded by buildStallRepairBuy's
// AtBusiness gate — "## Your business" (LLM-274) covers the buy there.
func TestGoldensOwnerOffPostShortNailsSeesBuyErrand(t *testing.T) {
	const header = "## Nails to mend your business"
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			if buildStallRepairBuy(snap, actorID, a) == nil {
				return // no off-post nail-buy errand for this actor — invariant N/A
			}
			exercised = true
			p := Build(snap, actorID, warrants)
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			if !strings.Contains(out, header) {
				t.Errorf("scenario %q: owner is off her worn business, short of nails, with a buy path, but the prompt omits the %q errand cue (LLM-277)", sc.name, header)
			}
			if p.DutySteer != nil && p.DutySteer.ToWork {
				t.Errorf("scenario %q: owner is on a nail-buy errand but the return-to-post to-work steer still fires — it must defer while she fetches nails (LLM-277)", sc.name)
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise an owner off-post, short of nails, with an actionable buy path (LLM-277)")
	}
}

// TestGoldensBuyNowGoadNeverBesideHoldOff is the LLM-297 cross-scenario invariant: the
// "## Nails to mend your business" co-present "Buy it now —" goad (renderCoPresentBuy) and
// the working-capital "Hold off buying more" restock steer must never render in the same
// prompt — they are contradictory buy/hold instructions. A keeper who owns a worn business,
// is off it beside a nail seller, AND is coin-poor+overstocked would emit both before the
// fix; merchantConserve now softens the nail goad for exactly that keeper.
// keeper_conserving_owes_nail_repair is the non-vacuous anchor (it carries the hold-off
// line). The goad check is scoped to the nail section because renderCoPresentBuy is shared
// with other cues (the farm-upkeep and restock co-present buys) that may legitimately say
// "Buy it now —" elsewhere in the prompt — this invariant is specifically the nail-repair /
// restock contradiction.
func TestGoldensBuyNowGoadNeverBesideHoldOff(t *testing.T) {
	const goad = "Buy it now —"
	const holdOff = "Hold off buying more"
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, holdOff) {
				return // no restock hold-off advice in this prompt — invariant N/A here
			}
			exercised = true
			if strings.Contains(promptSection(out, "## Nails to mend your business"), goad) {
				t.Errorf("scenario %q: the nail-repair goad (%q) renders in the same prompt as the restock hold-off advice (%q) — two sections issue contradictory buy/hold instructions (LLM-297)", sc.name, goad, holdOff)
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise a keeper in working-capital conserve mode (the 'Hold off buying more' advice) so the buy-now/hold-off invariant isn't vacuous (LLM-297)")
	}
}

// TestGoldensFarmUpkeepGoadNeverBesideHoldOff is the LLM-299 cross-scenario invariant — the
// shovel twin of TestGoldensBuyNowGoadNeverBesideHoldOff: the "## Farm upkeep" co-present
// "Buy it now —" goad (renderCoPresentBuy) and the working-capital "Hold off buying more"
// restock steer must never render in the same prompt. A farm-owning keeper who is off her
// farm beside a shovel seller AND coin-poor+overstocked would emit both before the fix;
// merchantConserve now softens the shovel goad for exactly that keeper. The goad check is
// scoped to the "## Farm upkeep" section because renderCoPresentBuy is shared with other
// cues (the nail repair-buy and restock co-present buys) that may legitimately say
// "Buy it now —" elsewhere in the prompt. farm_owner_conserving_owes_upkeep is the
// non-vacuous anchor (it carries the hold-off line beside the farm-upkeep cue).
func TestGoldensFarmUpkeepGoadNeverBesideHoldOff(t *testing.T) {
	const goad = "Buy it now —"
	const holdOff = "Hold off buying more"
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, holdOff) {
				return // no restock hold-off advice in this prompt — invariant N/A here
			}
			if !strings.Contains(out, "## Farm upkeep") {
				return // no farm-upkeep cue here — the shovel goad can't collide with the hold-off
			}
			exercised = true
			if strings.Contains(promptSection(out, "## Farm upkeep"), goad) {
				t.Errorf("scenario %q: the farm-upkeep goad (%q) renders in the same prompt as the restock hold-off advice (%q) — two sections issue contradictory buy/hold instructions (LLM-299)", sc.name, goad, holdOff)
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise a farm-owning keeper in working-capital conserve mode who owes upkeep (the 'Hold off buying more' advice beside a '## Farm upkeep' cue) so the buy-now/hold-off invariant isn't vacuous (LLM-299)")
	}
}

// TestFarmOwnerOwesUpkeepWithSupplierNamesDestination is the LLM-274 cross-scenario
// invariant for the farm-upkeep cue (the shovel twin of the nail invariant above):
// whenever the "## Farm upkeep" cue renders AND findItemVendors resolves at least one
// shovel supplier, the cue must name that supplier's move_to destination
// ("(structure_id: <id>)") rather than the destination-less "from the blacksmith" dead
// end. Keyed off the same buildFarmUpkeep the production render uses;
// farm_owner_owes_upkeep_with_shovel_supplier is the non-vacuous golden. Its foil — an
// owing farm owner with NO resolvable supplier — is correctly excluded (ShovelVendors
// empty), where the generic sentence with no dangling target is the intended output.
func TestFarmOwnerOwesUpkeepWithSupplierNamesDestination(t *testing.T) {
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			v := buildFarmUpkeep(snap, actorID, a)
			if v == nil {
				return // no upkeep cue for this actor
			}
			if len(v.ShovelVendors) == 0 {
				return // no resolvable supplier — the generic no-destination sentence is correct here
			}
			if v.CoPresentSeller != "" {
				return // LLM-277: a co-present seller renders the buy-here imperative, which supersedes the walk-to destination
			}
			exercised = true
			token := "(structure_id: " + string(v.ShovelVendors[0].StructureID) + ")"
			section := promptSection(renderScenario(sc), "## Farm upkeep")
			if !strings.Contains(section, token) {
				t.Errorf("scenario %q: farm owner owes upkeep with a resolvable shovel supplier but the '## Farm upkeep' cue omits its move_to destination %q — the model narrates the errand instead of walking it (LLM-274)", sc.name, token)
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise a farm owner owing upkeep with a resolvable shovel supplier (LLM-274)")
	}
}

// TestFarmUpkeepCueOnlyForOwingFarmOwner is the LLM-215 cross-scenario invariant: the
// "## Farm upkeep" cue appears in EXACTLY the scenarios where the actor owns a farm
// and owes upkeep shovels — never for a non-farm-owner or any unrelated scenario. It
// backstops the leak an owner-scoped, stock-derived cue is most prone to: showing up
// for someone who doesn't own a farm.
func TestFarmUpkeepCueOnlyForOwingFarmOwner(t *testing.T) {
	const marker = "## Farm upkeep"
	owesUpkeep := map[string]bool{
		"farm_owner_owes_upkeep":                      true,
		"farm_owner_owes_upkeep_with_shovel_supplier": true, // LLM-274: same owing owner, now with a resolvable supplier
		"farm_owner_owes_upkeep_seller_present":       true, // LLM-277: same owing owner, now co-present with the shovel seller
		"farm_owner_owes_upkeep_seller_low_stock":     true, // LLM-299: co-present, seller low on shovels — capped buy still renders the cue
		"farm_owner_standoff_declined_shovels":        true, // LLM-299: co-present, negotiation dead-ended — softened cue still renders
		"farm_owner_conserving_owes_upkeep":           true, // LLM-299: farm-owning keeper in conserve mode — softened cue still renders
		"farm_owner_off_post_owes_upkeep_no_supplier": true, // LLM-277: owes shovels, no reachable supplier — generic cue still renders
	}
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := owesUpkeep[sc.name]
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
		// LLM-231: John keeps his tavern on shift, at post, with a laboring peer present.
		"seller_huddled_with_laboring_peer": true,
		// LLM-231: John keeps his tavern on shift, at post, employing a laboring worker.
		"seller_employing_own_laboring_worker": true,
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

// TestGoldensSatiationBuyCueNeverTargetsRememberedShutVendor is the LLM-222 cross-
// scenario invariant: within the "## What you can eat or drink" section of any
// scenario, a vendor the buyer remembers finding shut must never appear as a
// "(structure_id: <id>)" buy target. A remembered-shut vendor is a seller-
// availability dead end the weak model toured on (Ezekiel's asleep-Inn walk), so
// the buy cue DROPS it rather than annotating it "found it shut up" — mirroring
// LLM-216's restock drop (TestGoldensRestockNeverTargetsRememberedShutSupplier).
// Runs over the whole matrix so a future satiation-cue change can't reintroduce a
// shut vendor as a target. Non-vacuous: buyer_drops_shut_keeps_open_vendor renders
// an eat/drink section while remembering the General Store shut, so the scan
// actually exercises a shut structure.
func TestGoldensSatiationBuyCueNeverTargetsRememberedShutVendor(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			_, section, found := strings.Cut(renderScenario(sc), "## What you can eat or drink\n")
			if !found {
				return // no eat/drink section in this situation — invariant N/A here
			}
			// Bound the scan to the eat/drink section by cutting at the next markdown
			// header, so a shut structure's id appearing lower in the prompt (a
			// restock/seek-work target) can't false-positive.
			if idx := strings.Index(section, "\n## "); idx >= 0 {
				section = section[:idx]
			}
			for structureID := range snap.Structures {
				if !businessRememberedShut(snap, a, structureID) {
					continue
				}
				token := "(structure_id: " + string(structureID) + ")"
				if strings.Contains(section, token) {
					t.Errorf("scenario %q: the eat/drink buy cue advertises remembered-shut vendor %q as a move target — a shut vendor is a dead end and must be dropped (LLM-222)", sc.name, token)
				}
			}
		})
	}
}

// TestGoldensNoBuyCueWithoutMeansToPay is the cross-scenario invariant over BOTH
// buy-food affordances in "## What you can eat or drink": the walk-to vendor cue
// ("Nearby to buy", LLM-222) and the co-present peer offer ("offer to buy it from
// them now with pay_with_item", LLM-242). A buyer with 0 coins AND no barterable
// goods holds no means of payment at all, so NEITHER affordance may be shown — a
// buy imperative they can neither pay nor barter is the genuine dead end (the
// 55-hit pay_with_item no-offer rejection). Reads the SAME means-to-pay signal the
// build gates do (holdsBarterableGoods + coins), per the discussion-109 no-drift
// rule. Non-vacuous on both arms: broke_buyer_no_goods_no_buy_cue builds this actor
// beside an open cheese seller (vendor arm), broke_buyer_no_goods_no_peer_buy stands
// it in a huddle with a peer carrying stew (peer arm) — each an affordance the cue
// would otherwise advertise.
func TestGoldensNoBuyCueWithoutMeansToPay(t *testing.T) {
	const vendorMarker = "Nearby to buy ("
	const peerMarker = "offer to buy it from them now with pay_with_item"
	var sawBroke, sawPeerArm bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.Coins != 0 || holdsBarterableGoods(a) {
				return // has some means to pay — invariant N/A here
			}
			sawBroke = true
			out := renderScenario(sc)
			if strings.Contains(out, vendorMarker) {
				t.Errorf("scenario %q: buyer holds 0 coins and no barterable goods but the eat/drink cue advertises a walk-to buy — no means to pay is a hard dead-end that must be suppressed (LLM-222)", sc.name)
			}
			if strings.Contains(out, peerMarker) {
				t.Errorf("scenario %q: buyer holds 0 coins and no barterable goods but the eat/drink cue offers to buy from a co-present peer — pay_with_item needs coins or goods, so the peer offer must be suppressed too (LLM-242)", sc.name)
			}
			// Track that the peer arm is genuinely exercised: this no-means subject
			// shares a huddle with a co-present non-PC peer carrying goods — the case
			// where a peer buy line WOULD render absent the LLM-242 gate. Guards the peer
			// half from silently going vacuous if its fixture is ever dropped.
			if h := snap.Huddles[a.CurrentHuddleID]; h != nil {
				for peerID := range h.Members {
					if p := snap.Actors[peerID]; peerID != actorID && p != nil && p.Kind != sim.KindPC && holdsBarterableGoods(p) {
						sawPeerArm = true
					}
				}
			}
		})
	}
	if !sawBroke {
		t.Error("no scenario exercised the no-means-to-pay branch — add one (broke_buyer_no_goods_no_buy_cue)")
	}
	if !sawPeerArm {
		t.Error("no no-means-to-pay scenario stands the buyer in a huddle with a goods-carrying peer — the LLM-242 peer-offer suppression is untested; add one (broke_buyer_no_goods_no_peer_buy)")
	}
}

// TestGoldensCoinPoorEmployerStaysSolicitable is the LLM-243 cross-scenario
// invariant — the hiring-side mirror of TestGoldensNoBuyCueWithoutMeansToPay. A
// co-present stranger employer that holds 0 coins BUT tradeable goods must NOT be
// foreclosed: it can still hire in kind (LLM-225), so a workless worker huddled
// with one must never be shown the "No one here can hire you" seek-work dead-end.
// A genuinely destitute employer (0 coins AND no goods) is NOT covered — it can
// hire no one and is allowed to dead-end — so the guard reuses the SAME
// holdsBarterableGoods predicate the sim gate (employerCanHireInKind) keys on, no
// drift. The other "could hire you" conditions are re-derived here independently
// of the employer's purse (present, not same household, no prior decline), so a
// regression that added a coin gate to the solicitable-audience check — re-breaking
// the barter path — would trip this rather than silently excusing itself. The
// subject is workless by the guard, so it shares a workplace with no one.
// Non-vacuous: worker_solicits_goods_rich_coin_poor_employer builds exactly this pair.
func TestGoldensCoinPoorEmployerStaysSolicitable(t *testing.T) {
	const deadEnd = "No one here can hire you"
	var sawCoinPoorGoodsHoldingEmployer bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subject := snap.Actors[actorID]
			if !subjectIsWorker(subject) || subject.WorkStructureID != "" {
				return // subject isn't a workless worker — invariant N/A here
			}
			hud := snap.Huddles[subject.CurrentHuddleID]
			if hud == nil {
				return // no huddle audience — invariant N/A here
			}
			for peerID := range hud.Members {
				if peerID == actorID {
					continue
				}
				peer := snap.Actors[peerID]
				// The LLM-243 rule covers only a coin-poor employer that can still hire
				// IN KIND — 0 coins AND holds goods. A destitute peer (no goods) is not
				// covered; it may legitimately dead-end. holdsBarterableGoods is the same
				// predicate the sim gate (employerCanHireInKind) uses.
				if peer == nil || peer.Coins != 0 || !holdsBarterableGoods(peer) {
					continue
				}
				// Coin-independent "could hire you" test, mirroring isSolicitableEmployer
				// but computed inline so a coin gate added to that predicate can't silence
				// the invariant: a co-present stranger (different household) with no prior
				// decline against this worker.
				if subject.HomeStructureID != "" && subject.HomeStructureID == peer.HomeStructureID {
					continue
				}
				if employerDeclinedSubject(snap, actorID, peerID) {
					continue
				}
				sawCoinPoorGoodsHoldingEmployer = true
				if strings.Contains(renderScenario(sc), deadEnd) {
					t.Errorf("scenario %q: co-present coin-poor goods-holding employer %s could hire the worker in kind, but the prompt renders the %q dead-end — an empty purse must not foreclose a goods-holding employer (LLM-243)", sc.name, peer.DisplayName, deadEnd)
				}
			}
		})
	}
	if !sawCoinPoorGoodsHoldingEmployer {
		t.Error("no scenario exercised a co-present coin-poor goods-holding solicitable employer — add one (worker_solicits_goods_rich_coin_poor_employer)")
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
	const barterMarker = "you may be able to offer goods you carry in trade"
	var sawEmptyNoGoods, sawEmptyWithGoods, sawPositive bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, _ := sc.build()
		actor := snap.Actors[actorID]
		if actor == nil {
			t.Fatalf("scenario %q: rendered actor %q missing from snapshot", sc.name, actorID)
		}
		out := renderScenario(sc)
		// The coin-only "cannot pay for anything" form appears iff the actor has 0
		// coins AND nothing to barter — a genuine payment dead-end. A 0-coin actor
		// holding goods gets the barter-aware form instead (LLM-222), so neither purse
		// line contradicts the satiation barter cue. Both flags are recomputed from raw
		// snapshot state (Coins + holdsBarterableGoods, the SAME predicate the buy-cue
		// gate reads), not the rendered text — so this asserts the cue tracks the actor.
		empty := actor.Coins == 0
		hasGoods := holdsBarterableGoods(actor)
		wantCannotPay := empty && !hasGoods
		wantBarter := empty && hasGoods
		if has := strings.Contains(out, cannotPayMarker); has != wantCannotPay {
			t.Errorf("scenario %q: coins=%d hasGoods=%v cannot-pay cue=%v, want %v", sc.name, actor.Coins, hasGoods, has, wantCannotPay)
		}
		if has := strings.Contains(out, barterMarker); has != wantBarter {
			t.Errorf("scenario %q: coins=%d hasGoods=%v barter purse cue=%v, want %v", sc.name, actor.Coins, hasGoods, has, wantBarter)
		}
		switch {
		case wantCannotPay:
			sawEmptyNoGoods = true
		case wantBarter:
			sawEmptyWithGoods = true
		default:
			sawPositive = true
		}
	}
	if !sawEmptyNoGoods || !sawEmptyWithGoods || !sawPositive {
		t.Errorf("matrix must exercise all three purse branches: emptyNoGoods=%v emptyWithGoods=%v positive=%v", sawEmptyNoGoods, sawEmptyWithGoods, sawPositive)
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

// herbalistRangedWildForage / untaggedForagerNoRangedWildForage are the LLM-253
// pair: a forager low on sage (0 of cap 5) who owns no sage bush, with an UNOWNED
// Sage Bush (10 ripe) ~80 tiles to the northeast — far outside loiter reach, so it
// falls in the gap between the owner-only owned-bush cue and the proximity-only
// at-bush cue. The tagged herbalist gets the ranged "## Free sources you can gather from"
// cue; the untagged forager gets nothing (the tag gate). Farm ~tile (23,73), bush
// ~tile (70,9): dx=+47, dy=-64 → ~79 tiles, rendered "a long walk to the
// northeast". On shift, no orders, no clock-driven text → byte-stable.
func herbalistRangedWildForage() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wildSageScenario(true)
}

func untaggedForagerNoRangedWildForage() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wildSageScenario(false)
}

func wildSageScenario(tagged bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const prudenceID = sim.ActorID("prudence")
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	var slugs []string
	if tagged {
		slugs = []string{sim.AttrForageRange}
	}
	prudence := &sim.ActorSnapshot{
		Kind:             sim.KindNPCShared,
		DisplayName:      "Prudence Ward",
		Role:             "herbalist",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 736, Y: 2336}.Tile(), // ~tile (23,73) — her farm
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            12,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"sage": 0},
		AttributeSlugs:   slugs,
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "sage", Source: sim.RestockSourceForage, Max: 5},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		RestockReorderPct: 25,
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{prudenceID: prudence},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"sage_bush": {
				ID:            "sage_bush",
				DisplayName:   "Sage Bush",
				Pos:           sim.WorldPos{X: 2240, Y: 288}, // ~tile (70,9) — far NW commons
				OwnerActorID:  "",                            // UNOWNED — a wild commons source
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Amount: 0, GatherItem: "sage", AvailableQuantity: intp(10)},
				},
			},
		},
	}
	return snap, prudenceID, nil
}

// generalStoreWaterForageAtWell is the LLM-254 two-row Well. The town Well is an
// UNOWNED commons carrying BOTH a public thirst drink row (Amount -8, slake-in-
// place) AND a yield-only water gather row (Amount 0, unset attribute — the clean
// LLM-264 yield row). Josiah Thorne (merchant, tagged sim.AttrForageRange, low on
// water with a `forage water` restock entry) stands ~10 tiles away and is thirsty,
// so the ONE unowned object surfaces in TWO independent cues at once with no owner-
// gate conflict: the free-drink satiation cue ("## What you can eat or drink", from
// the -8 thirst row) and the ranged forage cue ("## Free sources you can gather
// from", from the water yield row — 20 ready to gather). The forage stock count
// reads the yield row alone (forageStockForItem gates on Amount==0), so the -8
// drink row never pollutes it. Well ~tile (100,135), Josiah ~tile (108,141):
// dx=-8, dy=-6 → ~10 tiles, "a short walk to the northwest". On shift, no orders,
// no clock read → byte-stable.
func generalStoreWaterForageAtWell() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const josiahID = sim.ActorID("josiah")
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	josiah := &sim.ActorSnapshot{
		Kind:             sim.KindNPCShared,
		DisplayName:      "Josiah Thorne",
		Role:             "merchant",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 108 * 32, Y: 141 * 32}.Tile(), // ~10 tiles SE of the Well
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            20,
		Needs:            map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold},
		Inventory:        map[sim.ItemKind]int{"water": 0},
		AttributeSlugs:   []string{sim.AttrForageRange},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "water", Source: sim.RestockSourceForage, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		RestockReorderPct: 25,
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"water": {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"town_well": {
				ID:            "town_well",
				DisplayName:   "Well",
				Pos:           sim.WorldPos{X: 100 * 32, Y: 135 * 32}, // tile (100,135) — the real Well
				OwnerActorID:  "",                                     // UNOWNED commons — both cues require this
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					// Public drink row: slakes thirst in place, yields no inventory —
					// infinite, like the real commons Well. LLM-254 drops its gather_item
					// so it is drink-only, leaving the yield row below the sole water source.
					{Attribute: "thirst", Amount: -8},
					// Yield-only water row: forage-to-sell, unset attribute (LLM-264). A
					// forage_range holder draws a pail; drinking-in-place never touches this
					// counter (separate row, gated by Amount==0 in forageStockForItem).
					{Amount: 0, GatherItem: "water", AvailableQuantity: intp(20), MaxQuantity: intp(20)},
				},
			},
		},
	}
	return snap, josiahID, nil
}

// TestRangedWildForageRequiresTag is the LLM-253 tag-gate unit: the ranged
// "## Free sources you can gather from" cue (with its move_to handle and qualitative
// distance+direction) renders for a forager carrying sim.AttrForageRange and does
// NOT render for the same fixture without the tag. Asserts the render directly, so
// the gate is pinned independently of whether a golden was regenerated.
func TestRangedWildForageRequiresTag(t *testing.T) {
	const header = "## Free sources you can gather from"
	tagged := renderScenario(perceptionScenario{name: "tagged", build: herbalistRangedWildForage})
	if !strings.Contains(tagged, header) {
		t.Fatalf("tagged herbalist: expected the ranged wild-forage section %q, got:\n%s", header, tagged)
	}
	if !strings.Contains(tagged, `move_to with structure_id "sage_bush"`) {
		t.Errorf("tagged herbalist: expected a move_to handle to the Sage Bush, got:\n%s", tagged)
	}
	if !strings.Contains(tagged, "a long walk to the northeast") {
		t.Errorf("tagged herbalist: expected the qualitative distance+direction phrase, got:\n%s", tagged)
	}
	untagged := renderScenario(perceptionScenario{name: "untagged", build: untaggedForagerNoRangedWildForage})
	if strings.Contains(untagged, header) {
		t.Errorf("untagged forager: ranged wild-forage section must NOT render without sim.AttrForageRange, got:\n%s", untagged)
	}
}

// TestGoldensRangedWildForageRequiresTag is the LLM-253 cross-scenario invariant:
// the ranged "## Free sources you can gather from" section may render only for a subject
// carrying sim.AttrForageRange. Runs over the whole matrix so a future cue can't
// leak omniscient wild-source awareness to an untagged actor in any situation, not
// just the one pair pinned above.
func TestGoldensRangedWildForageRequiresTag(t *testing.T) {
	const header = "## Free sources you can gather from"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			tagged := false
			if a != nil {
				for _, slug := range a.AttributeSlugs {
					if slug == sim.AttrForageRange {
						tagged = true
						break
					}
				}
			}
			if tagged {
				return // subject carries the tag — the cue is permitted
			}
			if out := renderScenario(sc); strings.Contains(out, header) {
				t.Errorf("scenario %q: subject lacks sim.AttrForageRange but the prompt renders the ranged wild-forage section %q (LLM-253)", sc.name, header)
			}
		})
	}
}

// stallWearSnapshot builds a one-business, one-actor snapshot for the LLM-118
// cues. The actor stands on the business's loiter pin; the object is a tagged,
// owned business (TagBusiness — the LLM-247 gate) worn to `wear`. ownerID is the
// owner (the perceiving actor for the owner cues; a different actor for the
// passerby cue). nails seeds the actor's pack. No orders, no clock read →
// byte-stable.
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
				Tags:          []string{sim.TagBusiness},
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
// the "too worn to trade … use the repair tool now to fix it" steer. wear 650 (>= degrade 600).
func ownerAtDegradedStall() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return stallWearSnapshot("ezekiel", "ezekiel", "Ezekiel Crane", "blacksmith", 650, 5)
}

// ownerAtDegradedStoreConserving is the LLM-301 fixture — the live 2026-07-06 Josiah
// Thorne "the Smithy" case. The owner stands at his own DEGRADED General Store (wear
// 650 >= degrade 600, shut for trade) carrying 0 of the 5 nails a mend takes, with
// 1 coin (below the 10-coin MerchantCoinFloor) and 17 unsold flour on the shelf (past
// the 8-unit dead-stock overstock floor) — so merchantConserve is Active — while NO
// nail supplier resolves (none exists here; the live case dropped the smith on
// affordability — either way NailVendors is empty). The golden pins the sell-first
// soften in the vendor-less fallback instead of the old destination-less "buy more
// from the smith" goad, which the live model answered by hallucinating a "the Smithy"
// move_to target and burning its whole turn retrying it.
func ownerAtDegradedStoreConserving() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 720              // 12:00 — on shift, at his post
	storePin := sim.WorldPos{X: 100, Y: 100}.Tile()
	josiah := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Josiah Thorne",
		Role:             "shopkeeper",
		State:            sim.StateIdle,
		Pos:              storePin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            1,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"nail": 0, "flour": 17},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "flour", Source: sim.RestockSourceBuy, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		MerchantCoinFloor:         10,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"josiah": josiah},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"general_store": {
				ID:            "general_store",
				DisplayName:   "General Store",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  "josiah",
				Tags:          []string{sim.TagBusiness},
				Wear:          650,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, "josiah", nil
}

// ownerConservingWithNailSupplier is the LLM-301 code_review arm: conserve active
// WHILE a nail supplier survives findItemVendors. Elizabeth is coin-poor (6 < the
// 10-coin MerchantCoinFloor) and overstocked (17 flour, past the 8-unit dead-stock
// floor) — conserve Active — but she has never bought nails from Ezekiel, so his
// price is unknown and the LLM-216 affordability drop KEEPS him (patronage earns the
// number). The working-capital floor and the affordability drop are different
// filters, so this state is reachable live; the golden pins that conserve WINS —
// the sell-first soften renders, not the "Use move_to to reach a supplier" walk-to
// goad — keeping this cue from pushing a buy while "## Restocking" says hold off.
func ownerConservingWithNailSupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := ownerAtWornStallWithNailSupplier()
	owner := snap.Actors[actorID]
	owner.Coins = 6
	owner.Inventory["flour"] = 17
	owner.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "flour", Source: sim.RestockSourceBuy, Max: 20},
	}}
	snap.MerchantCoinFloor = 10
	return snap, actorID, warrants
}

// ownerAtWornStallWithNailSupplier is the LLM-274 fixture, modeled on the live
// 2026-07-04 Elizabeth Ellis case: the owner stands at her own worn Ellis Farm
// (wear 450 >= repair 400, < degrade 600) carrying 0 of the 5 nails a mend needs,
// while a SEPARATE open nail supplier exists — Ezekiel, the blacksmith, stationed
// at the Blacksmith structure holding 21 nails. findItemVendors resolves the smith
// as a walk-to destination (he isn't the buyer, works at a resolvable non-farm
// structure, holds nails, and Elizabeth has no remembered-shut/unaffordable strike
// against him), so the "## Your business" no-nails steer names it with a
// structure_id instead of the dead-end "buy more from the smith". Ezekiel is placed
// far from Elizabeth so he is a supplier-of-record, not a co-present seller — the
// cue names his WORKPLACE, which is exactly the move_to affordance the live model
// lacked. The foil is ownerAtWornStall (no other smith → generic sentence kept).
func ownerAtWornStallWithNailSupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	farmPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	elizabeth := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Elizabeth Ellis",
		Role:             "farmer",
		State:            sim.StateIdle,
		Pos:              farmPin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            39,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"nail": 0},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Ezekiel Crane",
		Role:             "blacksmith",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 2000, Y: 2000}.Tile(), // far from Elizabeth — a supplier of record, not co-present
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		WorkStructureID:  "blacksmith",
		Coins:            0,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"nail": 21},
		// The smith PRODUCES nails — the LLM-252 supplier-of-record gate
		// (isRestockSupplierOf) only names producers/foragers (or the distributor), so a
		// vendor merely holding nails from a past buy would NOT resolve here.
		RestockPolicy: producePolicy("nail", 40),
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": elizabeth, "ezekiel": ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			"blacksmith": plainStructure("blacksmith", "Blacksmith"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"ellis_farm": {
				ID:            "ellis_farm",
				DisplayName:   "Ellis Farm",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  "elizabeth",
				Tags:          []string{sim.TagBusiness},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, "elizabeth", nil
}

// ellisFarmWorn is the shared LLM-277 fixture spine: Elizabeth Ellis owns the worn
// (wear 450, past the 400 repair threshold) Ellis Farm and works it as her post
// (WorkStructureID → the ellis_farm structure, which shares the business object id),
// on-shift at 10:00. Ezekiel Crane is the nail-producing smith. The caller positions
// Elizabeth (at the smith in a huddle, walking, etc.), sets her nail count, and whether
// Ezekiel shares her huddle, to exercise the three off-post arms. Elizabeth having a
// resolvable work anchor while standing off it is what makes the to-work nag fire in
// the baseline, so a golden that shows NO nag proves the errand suppressed it.
func ellisFarmWorn(elizabethPos sim.WorldPos, insideStructure sim.StructureID, huddle sim.HuddleID, nails int) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	smithPos := sim.WorldPos{X: 2000, Y: 2000}
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               elizabethPos.Tile(),
		InsideStructureID: insideStructure,
		WorkStructureID:   "ellis_farm", // her post — she is standing off it below
		CurrentHuddleID:   huddle,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             39,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": nails},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               smithPos.Tile(),
		InsideStructureID: "blacksmith",
		WorkStructureID:   "blacksmith",
		CurrentHuddleID:   huddle, // "" when the caller wants him a supplier-of-record, not co-present
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 21},
		// The smith PRODUCES nails — the LLM-252 supplier-of-record gate names only
		// producers/foragers (or the distributor), not a holder of past-bought stock.
		RestockPolicy: producePolicy("nail", 40),
		Acquaintances: map[string]sim.Acquaintance{"Elizabeth Ellis": {}},
	}
	// Ezekiel shares Elizabeth's huddle only when the caller passes a non-empty huddle
	// AND he is at it; for the walking/enough-nails arms the caller passes "" so he is a
	// distant supplier of record. A blank huddle on Ezekiel keeps him out of her huddle.
	if huddle == "" {
		ezekiel.CurrentHuddleID = ""
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": elizabeth, "ezekiel": ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			"blacksmith": plainStructure("blacksmith", "Blacksmith"),
			"ellis_farm": plainStructure("ellis_farm", "Ellis Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"ellis_farm": {
				ID:            "ellis_farm",
				DisplayName:   "Ellis Farm",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  "elizabeth",
				Tags:          []string{sim.TagBusiness},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, "elizabeth", nil
}

// ownerOffPostAtSmithShortNails is the LLM-277 co-present arm — the live 2026-07-04
// failure the ticket fixes. Elizabeth, 0 nails, has walked off her worn farm and shares
// the smith's huddle with Ezekiel (21 nails). On-shift and off her post, so the to-work
// nag WOULD fire — but the active nail-buy errand suppresses it. The golden pins the
// off-post "## Nails to mend your business" cue with the co-present pay_with_item
// buy-here imperative naming Ezekiel, and the ABSENCE of any return-to-post steer.
func ownerOffPostAtSmithShortNails() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return ellisFarmWorn(sim.WorldPos{X: 2000, Y: 2000}, "blacksmith", "smith_huddle", 0)
}

// ownerOffPostShortNailsWalking is the LLM-277 walk-to arm: Elizabeth, 0 nails, is off
// her worn farm and NOT co-present with the smith (no shared huddle, Ezekiel far off).
// The golden pins the same "## Nails to mend your business" cue naming the walk-to
// destination ("buy from Blacksmith (structure_id: blacksmith)") and no return-to-post
// steer — the "while away" half of the errand that persists across the walk (LLM-274
// named this destination for the at-farm cue; here it rides the whole trip).
func ownerOffPostShortNailsWalking() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return ellisFarmWorn(sim.WorldPos{X: 1000, Y: 1000}, "", "", 0)
}

// ownerOffPostEnoughNails is the LLM-277 negative arm: Elizabeth is off her worn farm
// but already carries enough nails (5 == NAILS_PER_REPAIR) to mend it, so there is no
// buy errand — the "## Nails to mend your business" cue is absent. With no errand to
// defer, the to-work nag correctly fires (she should head back to her post to mend),
// the foil that shows the suppression is conditional on an actual shortfall.
func ownerOffPostEnoughNails() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return ellisFarmWorn(sim.WorldPos{X: 1000, Y: 1000}, "", "", 5)
}

// keeperConservingOwesNailRepair is the LLM-297 invariant anchor — the live 2026-07-06
// Josiah Thorne standoff. Josiah is a shopkeeper whose working capital has collapsed
// (1 coin, below the 10-coin MerchantCoinFloor, with 20 unsold cloth on the shelf), so
// merchantConserve is Active. He owns the worn General Store (his post) but has walked
// off it to the Blacksmith, where he shares Ezekiel Crane's huddle — Ezekiel produces
// both nails (10 held, the repair supply) and coal (15 held, Josiah's low restock
// input). So two standing sections would fire at once: "## Restocking" flips to the
// coin-poor "Hold off buying more for now" steer (coal is low and Ezekiel is a
// co-present coal supplier, so the item survives the LLM-216 drop), while the off-post
// "## Nails to mend your business" errand puts a nail seller in front of him. The fix
// softens the nail goad under conserve, so the prompt no longer carries "Buy it now"
// beside "Hold off buying more".
func keeperConservingOwesNailRepair() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josiahID  = sim.ActorID("josiah")
		ezekielID = sim.ActorID("ezekiel")
		store     = sim.StructureID("general_store")
		smithy    = sim.StructureID("blacksmith")
		huddleID  = sim.HuddleID("forge_huddle")
	)
	zero := 0
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, off his post at the smith
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 200, Y: 200},
		InsideStructureID: smithy,
		WorkStructureID:   store, // his post — he is standing off it, at the smith
		CurrentHuddleID:   huddleID,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             1,
		Needs:             map[sim.NeedKey]int{},
		// Overstocked cloth is the conserve trigger; low coal is the surviving Restocking
		// item (Ezekiel supplies it co-present); 0 nails is the repair shortfall.
		Inventory: map[sim.ItemKind]int{"cloth": 20, "coal": 1, "nail": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "cloth", Source: sim.RestockSourceBuy, Max: 24},
			{Item: "coal", Source: sim.RestockSourceBuy, Max: 12},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 201, Y: 200},
		InsideStructureID: smithy,
		WorkStructureID:   smithy,
		CurrentHuddleID:   huddleID,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 10, "coal": 15},
		// Produces nails AND coal, so the LLM-252 supplier-of-record gate names him for
		// each — the nail repair-buy and Josiah's coal restock both resolve to him.
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 40},
			{Item: "coal", Source: sim.RestockSourceProduce, Max: 40},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Josiah Thorne": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:               published,
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		MerchantCoinFloor:         10,
		RestockReorderPct:         25,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{josiahID: josiah, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			store:  plainStructure(store, "General Store"),
			smithy: plainStructure(smithy, "Blacksmith"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {ID: huddleID, Members: map[sim.ActorID]struct{}{josiahID: {}, ezekielID: {}}},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store): {
				ID:            sim.VillageObjectID(store),
				DisplayName:   "General Store",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  josiahID,
				Tags:          []string{sim.TagBusiness},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"cloth": {Name: "cloth", DisplayLabel: "cloth"},
			"coal":  {Name: "coal", DisplayLabel: "coal"},
			"nail":  {Name: "nail", DisplayLabel: "nails"},
		},
	}
	return snap, josiahID, nil
}

// ownerStandoffDeclinedNails is the LLM-297 standoff arm: the ownerOffPostAtSmithShortNails
// co-present setup (Elizabeth off her worn farm, sharing the smith's huddle with Ezekiel,
// 0 nails), but with two prior nail offers to Ezekiel already declined IN THIS HUDDLE on
// the pay ledger. Elizabeth is a plain farmer (no restock policy → merchantConserve never
// fires), so the softening here is driven purely by the dead-ended negotiation
// (nailStandoffToSeller), proving the standoff path independent of conserve mode.
func ownerStandoffDeclinedNails() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := ellisFarmWorn(sim.WorldPos{X: 2000, Y: 2000}, "blacksmith", "smith_huddle", 0)
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snap.PublishedAt = published
	// Thin purse — the live standoff is purse-driven (the smith declines a short purse),
	// which is why both offers were turned down.
	snap.Actors[actorID].Coins = 3
	// Two declined nail offers to Ezekiel in the current huddle, resolved a minute ago —
	// the threshold the cue reads as a standoff, and inside recentlyResolvedOfferWindow so
	// the recency guard counts them. Declined is terminal, so no ExpiresAt is needed.
	resolved := published.Add(-1 * time.Minute)
	// Amount 3 on each: the offers carried her whole thin purse (a live offer always
	// carries coins and/or goods), so the LLM-296 settled-offer line reads "Your offer
	// of 3 coins …" rather than the fixture artifact "of nothing".
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.NailItemKind, Qty: 5, Amount: 3, State: sim.PayLedgerStateDeclined, HuddleID: "smith_huddle", ResolvedAt: resolved},
		2: {ID: 2, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.NailItemKind, Qty: 5, Amount: 3, State: sim.PayLedgerStateDeclined, HuddleID: "smith_huddle", ResolvedAt: resolved},
	}
	return snap, actorID, warrants
}

// ownerShortNailsSellerLowStock is the LLM-297 stock-cap arm: the same co-present setup,
// but Ezekiel holds only 2 nails against the 5 a repair needs. Affordable and no prior
// offers, so the buy still stands — the render caps the ask at his stock instead of
// goading the full shortfall (the live case: the smith held only 1 nail).
func ownerShortNailsSellerLowStock() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := ellisFarmWorn(sim.WorldPos{X: 2000, Y: 2000}, "blacksmith", "smith_huddle", 0)
	snap.Actors["ezekiel"].Inventory[sim.NailItemKind] = 2
	return snap, actorID, warrants
}

// farmOwnerOwesUpkeepSellerPresent is the LLM-277 farm-upkeep co-present arm: Elizabeth
// owes 3 upkeep shovels (95 coins, floor 30, band 20) and holds none, and shares a
// huddle with Ezekiel — the shovel-producing smith holding 12 — at the Blacksmith. The
// golden pins the "## Farm upkeep" cue flipping from the walk-to list to the concrete
// co-present pay_with_item buy-here imperative naming Ezekiel, the same fast-path the
// nail repair-buy uses. Its foil is farmOwnerOwesUpkeepWithShovelSupplier (smith far
// off → the walk-to destination is named instead).
func farmOwnerOwesUpkeepSellerPresent() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const actorID = sim.ActorID("elizabeth")
	zero := 0
	start, end := 360, 1080
	now := 600
	huddle := sim.HuddleID("smith_huddle")
	smithPos := sim.WorldPos{X: 2000, Y: 2000}.Tile()
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               smithPos,
		InsideStructureID: "blacksmith",
		CurrentHuddleID:   huddle,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             95,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               smithPos,
		InsideStructureID: "blacksmith",
		WorkStructureID:   "blacksmith",
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"shovel": 12},
		RestockPolicy:     producePolicy("shovel", 40),
		Acquaintances:     map[string]sim.Acquaintance{"Elizabeth Ellis": {}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.NeedThresholds{},
		Assets:                   emptyAssetSet,
		FarmUpkeepFloor:          30,
		FarmUpkeepCoinsPerShovel: 20,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{actorID: elizabeth, "ezekiel": ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			"blacksmith": plainStructure("blacksmith", "Blacksmith"),
		},
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

// farmOwnerOwesUpkeepSellerLowStock is the LLM-299 stock-cap arm: the
// farmOwnerOwesUpkeepSellerPresent co-present setup (Elizabeth owes 3 upkeep shovels,
// sharing the smith's huddle with Ezekiel), but Ezekiel holds only 1 shovel against the 3
// she needs. Affordable and no prior offers, so the buy still stands — the render caps the
// ask at his stock instead of goading the full shortfall (the shovel twin of the live
// smith-held-only-1-nail case).
func farmOwnerOwesUpkeepSellerLowStock() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := farmOwnerOwesUpkeepSellerPresent()
	snap.Actors["ezekiel"].Inventory[sim.ShovelItemKind] = 1
	return snap, actorID, warrants
}

// farmOwnerStandoffDeclinedShovels is the LLM-299 standoff arm: the
// farmOwnerOwesUpkeepSellerPresent co-present setup (Elizabeth owes 3 upkeep shovels,
// sharing the smith's huddle with Ezekiel, who is well-stocked at 12), but with two prior
// shovel offers to Ezekiel already declined IN THIS HUDDLE on the pay ledger. Elizabeth is a
// plain farmer (no restock policy → merchantConserve never fires), so the softening here is
// driven purely by the dead-ended negotiation (coPresentBuyStandoff), proving the standoff
// path independent of conserve mode — the shovel twin of ownerStandoffDeclinedNails.
func farmOwnerStandoffDeclinedShovels() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := farmOwnerOwesUpkeepSellerPresent()
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snap.PublishedAt = published
	// Two declined shovel offers to Ezekiel in the current huddle, resolved a minute ago —
	// the threshold the cue reads as a standoff, and inside recentlyResolvedOfferWindow so the
	// recency guard counts them. Declined is terminal, so no ExpiresAt is needed.
	resolved := published.Add(-1 * time.Minute)
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.ShovelItemKind, Qty: 3, State: sim.PayLedgerStateDeclined, HuddleID: "smith_huddle", ResolvedAt: resolved},
		2: {ID: 2, BuyerID: actorID, SellerID: "ezekiel", ItemKind: sim.ShovelItemKind, Qty: 3, State: sim.PayLedgerStateDeclined, HuddleID: "smith_huddle", ResolvedAt: resolved},
	}
	return snap, actorID, warrants
}

// farmOwnerConservingOwesUpkeep is the LLM-299 conserve-coupling arm and the non-vacuous
// anchor for TestGoldensFarmUpkeepGoadNeverBesideHoldOff — the shovel twin of
// keeperConservingOwesNailRepair. Marta Vale is a shopkeeper whose working capital has
// collapsed (51 coins, below the 60-coin MerchantCoinFloor, with 20 unsold cloth on the
// shelf), so merchantConserve is Active. She ALSO owns Vale Farm and owes 1 upkeep shovel
// (51 coins over the 30 floor, banded by 20). She has stepped off her farm to the Blacksmith,
// where she shares Ezekiel Crane's huddle — Ezekiel produces both shovels (12 held, the
// upkeep supply) and coal (15 held, Marta's low restock input). Two standing sections fire
// at once: "## Restocking" flips to the coin-poor "Hold off buying more for now" steer (coal
// is low and Ezekiel is a co-present coal supplier, so the item survives the LLM-216 drop),
// while "## Farm upkeep" puts a shovel seller in front of her. The fix softens the shovel
// goad under conserve, so the prompt no longer carries "Buy it now" beside "Hold off buying
// more". She owns no worn business, so no nail repair cue competes here.
func farmOwnerConservingOwesUpkeep() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		martaID   = sim.ActorID("marta")
		ezekielID = sim.ActorID("ezekiel")
		farm      = sim.StructureID("marta_farm")
		smithy    = sim.StructureID("blacksmith")
		huddleID  = sim.HuddleID("forge_huddle")
	)
	zero := 0
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, off her farm at the smith
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	marta := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Marta Vale",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 200, Y: 200},
		InsideStructureID: smithy,
		WorkStructureID:   farm, // her post — she is standing off it, at the smith
		CurrentHuddleID:   huddleID,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             51, // below the 60 MerchantCoinFloor (conserve) yet above the 30 FarmUpkeepFloor (owes 1 shovel)
		Needs:             map[sim.NeedKey]int{},
		// Overstocked cloth is the conserve trigger; low coal is the surviving Restocking
		// item (Ezekiel supplies it co-present); 0 shovels is the upkeep shortfall.
		Inventory: map[sim.ItemKind]int{"cloth": 20, "coal": 1, "shovel": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "cloth", Source: sim.RestockSourceBuy, Max: 24},
			{Item: "coal", Source: sim.RestockSourceBuy, Max: 12},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 201, Y: 200},
		InsideStructureID: smithy,
		WorkStructureID:   smithy,
		CurrentHuddleID:   huddleID,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"shovel": 12, "coal": 15},
		// Produces shovels AND coal, so the LLM-252 supplier-of-record gate names him for
		// each — the farm-upkeep shovel buy and Marta's coal restock both resolve to him.
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "shovel", Source: sim.RestockSourceProduce, Max: 40},
			{Item: "coal", Source: sim.RestockSourceProduce, Max: 40},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Marta Vale": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:              published,
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.NeedThresholds{},
		Assets:                   emptyAssetSet,
		MerchantCoinFloor:        60,
		RestockReorderPct:        25,
		FarmUpkeepFloor:          30,
		FarmUpkeepCoinsPerShovel: 20,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{martaID: marta, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			farm:   plainStructure(farm, "Vale Farm"),
			smithy: plainStructure(smithy, "Blacksmith"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddleID: {ID: huddleID, Members: map[sim.ActorID]struct{}{martaID: {}, ezekielID: {}}},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm): {
				ID:            sim.VillageObjectID(farm),
				DisplayName:   "Vale Farm",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  martaID,
				Tags:          []string{sim.TagFarm},
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"cloth":  {Name: "cloth", DisplayLabel: "cloth"},
			"coal":   {Name: "coal", DisplayLabel: "coal"},
			"shovel": {Name: "shovel", DisplayLabel: "shovels"},
		},
	}
	return snap, martaID, nil
}

// farmOwnerOffPostOwesUpkeepNoSupplier is the LLM-277 review-caught edge (code_review
// c11007e7): a farm owner off her post, owing shovels, but with NO reachable shovel
// supplier anywhere (findItemVendors empty, no co-present seller). buildFarmUpkeep still
// renders the generic "from the blacksmith" fallback (the LLM-216 keep-the-sentence
// posture), but that dead-end must NOT suppress the return-to-post nag — suppressing it
// would strand her off-post with no way to complete the errand. The golden pins BOTH the
// generic "## Farm upkeep" line AND the to-work steer firing, because hasFarmUpkeepErrand
// is gated on an actionable buy path, not on the cue's mere presence.
func farmOwnerOffPostOwesUpkeepNoSupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const actorID = sim.ActorID("elizabeth")
	zero := 0
	start, end := 360, 1080
	now := 600
	elizabeth := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Elizabeth Ellis",
		Role:             "farmer",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 1000, Y: 1000}.Tile(), // off her farm
		WorkStructureID:  "ellis_farm",
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            95,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.NeedThresholds{},
		Assets:                   emptyAssetSet,
		FarmUpkeepFloor:          30,
		FarmUpkeepCoinsPerShovel: 20,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{actorID: elizabeth},
		Structures: map[sim.StructureID]*sim.Structure{
			"ellis_farm": plainStructure("ellis_farm", "Ellis Farm"),
		},
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

// passerbyAtWornStall: a non-owner standing at someone else's worn business — the
// co-present atmosphere line, no owner cue. The actor (John) differs from the
// business's owner (Ezekiel).
func passerbyAtWornStall() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return stallWearSnapshot("john", "ezekiel", "John Ellis", "tavernkeeper", 450, 0)
}

// hiredWorkerAtEmployerWornBusiness is the LLM-271 fixture, modeled on the live
// 2026-07-04 case: Lewis Walker, a hired hand mid-job for Prudence Ward (a Working
// LaborOffer, WorkerID == subject), stands INSIDE her worn PW Apothecary carrying
// enough nails to mend it. Prudence hired Lewis, who had nails and offered to do
// the repairs, but the owner-only repair cue never surfaced for him. The golden
// pins the hired-framed "## The business you're working at … needs mending" cue
// (NOT the owner's "## Your business") plus the hired repair warrant line — the
// wake that pierces the laboring shelve-gate. The repair tool rides the same
// StallRepair payload signal (tool_gating_stall_test.go covers the advertise side).
// Pos is far from the outdoor pin so the cue can only fire via the inside-structure
// branch of sim.AtBusiness, proving the hired path inherits LLM-266.
func hiredWorkerAtEmployerWornBusiness() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		prudenceID = sim.ActorID("prudence")
		lewisID    = sim.ActorID("lewis")
		shop       = sim.StructureID("apothecary")
		huddle     = sim.HuddleID("h1")
	)
	zero := 0
	published := time.Date(2026, 6, 30, 20, 30, 0, 0, time.UTC)
	workingUntil := published.Add(90 * time.Minute)
	acceptedAt := published.Add(-30 * time.Minute)
	insidePos := sim.WorldPos{X: 500, Y: 500}.Tile() // far from the pin at WorldPos{100,100}
	prudence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Prudence Ward",
		Role:              "apothecary",
		State:             sim.StateIdle,
		Pos:               insidePos,
		InsideStructureID: shop,
		WorkStructureID:   shop,
		CurrentHuddleID:   huddle,
		Coins:             50,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Lewis Walker",
		Role:              "laborer",
		State:             sim.StateLaboring,
		Pos:               insidePos,
		InsideStructureID: shop,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 5},
		Acquaintances:     map[string]sim.Acquaintance{"Prudence Ward": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:               published,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{prudenceID: prudence, lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			shop: plainStructure(shop, "PW Apothecary"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{prudenceID: {}, lewisID: {}}},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"apothecary": {
				ID:            "apothecary",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  prudenceID,
				Tags:          []string{sim.TagBusiness, "shop", "market_stall"},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:           1,
				WorkerID:     lewisID,
				EmployerID:   prudenceID,
				Reward:       10,
				DurationMin:  120,
				State:        sim.LaborStateWorking,
				HuddleID:     huddle,
				AcceptedAt:   &acceptedAt,
				WorkingUntil: &workingUntil,
			},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	warrants := []sim.WarrantMeta{{TriggerActorID: lewisID, Reason: sim.StallRepairHiredWarrantReason{StallID: "apothecary"}}}
	return snap, lewisID, warrants
}

// ownerAtWornTavern: John Ellis stands at his own worn Tavern — a business tagged
// {business, lodging, tavern} with NO market_stall tag, exercising the LLM-247
// widened gate (accrual keys off TagBusiness, not market_stall). The object
// carries no DisplayName, so the "## Your business" cue resolves the name from the
// co-located structure ("Your Tavern is showing hard use…"). Worn (450 >= repair
// 400, < degrade 600), short on nails (2 < 5) — the buy-then-mend steer.
func ownerAtWornTavern() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	pin := sim.WorldPos{X: 100, Y: 100}.Tile()
	john := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "John Ellis",
		Role:             "tavernkeeper",
		State:            sim.StateIdle,
		Pos:              pin,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            8,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"nail": 2},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"john": john},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:            "tavern",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  "john",
				Tags:          []string{sim.TagBusiness, "lodging", "tavern"},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, "john", nil
}

// ownerInsideWornBusiness: the LLM-266 regression fixture — the owner (John Ellis)
// stands INSIDE his own worn Tavern (InsideStructureID == the business id, since
// structures share the village_object's id) and AWAY from the outdoor loiter pin.
// This is the live keeper-at-post posture the old pin-only co-location gate
// silently excluded, so the "## Your business" cue had never rendered for any real
// NPC. Pos is deliberately many Chebyshev tiles from the pin (WorldPos{100,100}),
// so the cue can fire ONLY via the inside-structure branch of sim.AtBusiness — the
// pin proximity check never passes here. Worn (450 >= repair 400, < degrade 600),
// short on nails (2 < 5) — the buy-then-mend steer, named from the co-located
// structure ("Your Tavern is showing hard use…").
func ownerInsideWornBusiness() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080                          // 06:00–18:00
	now := 600                                       // 10:00 — on shift
	insidePos := sim.WorldPos{X: 500, Y: 500}.Tile() // far from the pin at WorldPos{100,100}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		Pos:               insidePos,
		InsideStructureID: "tavern",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             8,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 2},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"john": john},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:            "tavern",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  "john",
				Tags:          []string{sim.TagBusiness, "lodging", "tavern"},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, "john", nil
}

// passerbyInsideWornBusiness: the LLM-266 non-owner arm — a non-owner (Ezekiel)
// stands INSIDE someone else's worn business (John's Tavern) and away from the
// outdoor loiter pin. The co-present condition line ("The Tavern here looks
// worn…") now renders via the inside-structure branch of sim.AtBusiness, while the
// owner-only "## Your business" cue stays absent (Ezekiel owns nothing). The owner
// (John) need not be present as an actor — the condition line reads off the object.
func passerbyInsideWornBusiness() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	zero := 0
	start, end := 360, 1080                          // 06:00–18:00
	now := 600                                       // 10:00 — on shift
	insidePos := sim.WorldPos{X: 500, Y: 500}.Tile() // far from the pin at WorldPos{100,100}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               insidePos,
		InsideStructureID: "tavern",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             8,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:            "tavern",
				DisplayName:   "Tavern",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  "john",
				Tags:          []string{sim.TagBusiness, "lodging", "tavern"},
				Wear:          450,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, "ezekiel", nil
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
// owes 3 shovels) and none in hand, and NO shovel supplier exists in the world. The
// golden pins the "## Farm upkeep" cue with the GENERIC no-destination steer ("buy 3
// fresh shovels from the blacksmith") — the LLM-274 no-actionable-supplier arm.
func farmOwnerOwesUpkeep() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return farmUpkeepSnapshot(95, 0, 30, 20)
}

// farmOwnerOwesUpkeepWithShovelSupplier is the LLM-274 farm-upkeep arm: Elizabeth
// owes 3 upkeep shovels (95 coins, floor 30, band 20) and holds none, while a
// SEPARATE shovel-producing smith — Ezekiel at the Blacksmith holding 12 shovels —
// exists. findItemVendors resolves him (he PRODUCES shovels, LLM-200/LLM-252), so the
// "## Farm upkeep" cue names "buy from Blacksmith (structure_id: blacksmith)" with
// move_to + pay_with_item instead of the dead-end "from the blacksmith". Ezekiel is
// placed far from Elizabeth so he is a supplier of record, not co-present. The foil is
// farmOwnerOwesUpkeep (no smith → the generic sentence is correctly kept).
func farmOwnerOwesUpkeepWithShovelSupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := farmUpkeepSnapshot(95, 0, 30, 20)
	start, end := 360, 1080
	snap.Actors["ezekiel"] = &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Ezekiel Crane",
		Role:             "blacksmith",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 2000, Y: 2000}.Tile(), // far from Elizabeth — a supplier of record, not co-present
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		WorkStructureID:  "blacksmith",
		Coins:            0,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"shovel": 12},
		RestockPolicy:    producePolicy("shovel", 40), // the smith PRODUCES shovels — the LLM-252 supplier-of-record gate
	}
	// Add the smith's workplace without clobbering any structures farmUpkeepSnapshot
	// may seed (it doesn't today — farm ownership keys off village_object.OwnerActorID
	// + TagFarm, not the structure map — but the nil-guard keeps this robust if that
	// changes; code_review).
	if snap.Structures == nil {
		snap.Structures = map[sim.StructureID]*sim.Structure{}
	}
	snap.Structures["blacksmith"] = plainStructure("blacksmith", "Blacksmith")
	return snap, actorID, warrants
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

// snackLoopScenario is the LLM-307 fixture: a mildly-hungry stateful NPC (Ezekiel
// Crane, hunger 14 — felt at/above the silent floor 10, below the red threshold
// 18) carrying ONLY the food named by `carry`, with a cheese seller (a full meal,
// magnitude 8) at the General Store nearby. Parameterized by the carried food so
// the paired goldens differ in exactly one variable — own-stock class:
//
//   - a NIBBLE (raspberries, magnitude 2): the consume-first suppression must
//     re-open the meal directory and print the bridging line — a snack can't quiet
//     a persisting hunger, so the walk-to meal must stay visible (the live Ezekiel
//     raspberry loop, 2026-07-06).
//   - a MEAL (cheese, magnitude 8): the suppression must stand — a meal on hand is
//     the answer, the directory stays noise (the LLM-139 foil).
//
// He holds 59 coins (the live purse he never spent) so the vendor clears the
// means-to-pay gate. No PriceBook/orders, so the render takes no wall-clock read
// and stays byte-stable.
func snackLoopScenario(carry sim.ItemKind) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		mabelID   = sim.ActorID("mabel")
		store     = sim.StructureID("general_store")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — daytime
	ezekiel := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Ezekiel Crane",
		Role:             "blacksmith",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 100, Y: 100}.Tile(),
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            59,
		Inventory:        map[sim.ItemKind]int{carry: 3},
		Needs:            map[sim.NeedKey]int{"hunger": 14},
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
	}
	return snap, ezekielID, nil
}

func hungryHoldingNibbleSeesMealVendor() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return snackLoopScenario("raspberries")
}

func hungryHoldingMealKeepsSuppression() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return snackLoopScenario("cheese")
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

// wholesalerGrazingScenario builds the LLM-267 fixture: Moses James at his own
// farm on shift, carrying only the carrots he grows — but the farm is tagged
// wholesaler (the farms + mill are the wholesale tier), so those carrots are stock
// to sell, never his larder. Unlike grazingProducerScenario (LLM-134, which lets
// the trade stock return at the red tier), his own produce must NOT surface in the
// eat cue at ANY tier. Same isolation: no other food, vendor, or free source, so the
// carrots are the only own-stock candidate. Byte-stable (no PriceBook/orders).
func wholesalerGrazingScenario(hunger int) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
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
		// The farm carries both tags as the live data does; only wholesaler gates
		// the own-produce block (IsOwnProduce keys on it, like SellerAtWholesaler).
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm): {ID: sim.VillageObjectID(farm), OwnerActorID: mosesID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
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

func wholesalerStarvingOwnProduceAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wholesalerGrazingScenario(sim.DefaultHungerRedThreshold) // 18 — red: STILL not offered its own produce
}

// wholesalerCarryingBoughtFoodAtPost is the LLM-267 positive control: the same
// wholesaler, mildly hungry, carrying a bought loaf of bread (NOT one of its produce
// rows) alongside its own carrots. The bread — real provisions — IS offered in the
// eat cue; the carrots are not. Proves the block is item-scoped (own produce only),
// not a blanket "wholesalers never eat".
func wholesalerCarryingBoughtFoodAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id, warr := wholesalerGrazingScenario(14) // mild: felt (>= silent floor 10), below red (18)
	snap.Actors[id].Inventory["bread"] = 2
	snap.ItemKinds["bread"] = &sim.ItemKindDef{
		Name: "bread", DisplayLabel: "Bread",
		DisplayLabelSingular: "loaf of bread", DisplayLabelPlural: "loaves of bread",
		Category:  sim.ItemCategoryFood,
		Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
	}
	return snap, id, warr
}

// wholesalerProducerBarteringWithCustomer is the LLM-291 fixture: Moses James, a
// WHOLESALE producer (James Farm tagged wholesaler; grows carrots + wheat), stands
// in company with a would-be customer (Silence Walker). His produce sells only to
// the village distributor (Josiah Thorne, keeper of the distributor-tagged General
// Store), so the '## What your wares fetch' cue must draw the wholesale-channel line
// — who buys it, what the distributor pays, where to send other buyers — NOT a
// retail spread that nudges him to hawk carrots to whoever he is standing with (the
// street sale the PayWithItem wholesale gate then refuses; live hud-9b23…). Off the
// farm (InsideStructureID = commons) so '## Time to produce' doesn't render; no
// pressing needs so no eat/drink cues clutter the wares section. Josiah is present
// only so distributorLabel can resolve a person to route buyers to — not co-present.
func wholesalerProducerBarteringWithCustomer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		mosesID   = sim.ActorID("moses")
		silenceID = sim.ActorID("silence")
		josiahID  = sim.ActorID("josiah")
		commons   = sim.StructureID("commons")
		farm      = sim.StructureID("james_farm")
		store     = sim.StructureID("general_store")
		huddle    = sim.HuddleID("h1")
	)
	published := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	moses := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Moses James",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		WorkStructureID:   farm,
		Coins:             38,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"carrots": 30, "wheat": 30},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "carrots", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "wheat", Source: sim.RestockSourceProduce, Max: 30},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		Role:              "villager",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             22,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Moses James": {}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		InsideStructureID: store,
		WorkStructureID:   store,
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{mosesID: moses, silenceID: silence, josiahID: josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			commons: plainStructure(commons, "Village Commons"),
			farm:    plainStructure(farm, "James Farm"),
			store:   plainStructure(store, "General Store"),
		},
		// The farm carries both tags as the live data does; only wholesaler gates the
		// wholesale channel (IsOwnProduce / SellerAtWholesaler key on it).
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm):  {ID: sim.VillageObjectID(farm), OwnerActorID: mosesID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{mosesID: {}, silenceID: {}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"carrots": {OutputItem: "carrots", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 4},
			"wheat":   {OutputItem: "wheat", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"carrots": {
				Name: "carrots", DisplayLabel: "Carrots",
				DisplayLabelSingular: "carrot", DisplayLabelPlural: "carrots",
				Category: sim.ItemCategoryFood,
			},
			"wheat": {
				Name: "wheat", DisplayLabel: "Wheat",
				DisplayLabelSingular: "wheat", DisplayLabelPlural: "wheat",
				Category: sim.ItemCategoryMaterial,
			},
		},
	}
	return snap, mosesID, nil
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

// TestWholesalerNeverCuedToEatOwnProduce is the LLM-267 invariant: a wholesaler
// owner is NEVER offered its own produce in the eat cue — not even at the red/
// desperation tier where LLM-134 lets an ORDINARY producer's trade stock return as a
// last resort. The wholesale gate has no red-tier escape hatch (it pairs with the
// hard Consume guard). A bought food the wholesaler also carries IS still offered, so
// the block is own-produce-scoped, not a blanket ban on the wholesaler eating.
func TestWholesalerNeverCuedToEatOwnProduce(t *testing.T) {
	const cue = "consume to eat"
	for _, hunger := range []int{14, sim.DefaultHungerRedThreshold} { // mild AND red
		hunger := hunger
		got := renderScenario(perceptionScenario{name: "wholesaler_own_produce", build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
			return wholesalerGrazingScenario(hunger)
		}})
		if strings.Contains(got, cue) {
			t.Errorf("wholesaler at hunger %d was offered its own produce to eat (cue %q must be absent — no red-tier escape):\n%s", hunger, cue, got)
		}
	}
	// Positive control: a bought food the wholesaler carries IS offered — and the eat
	// cue names the BREAD, not the carrots (which stay only in the inventory readout,
	// so a bare Contains on the whole prompt wouldn't prove the produce is excluded).
	bought := renderScenario(perceptionScenario{name: "wholesaler_bought_food_at_post", build: wholesalerCarryingBoughtFoodAtPost})
	eatCue := promptSection(bought, "## What you can eat or drink")
	if !strings.Contains(eatCue, cue) {
		t.Fatalf("wholesaler carrying bought bread was NOT offered it to eat (cue %q should be present):\n%s", cue, bought)
	}
	if !strings.Contains(eatCue, "Bread") {
		t.Errorf("eat cue should offer the bought Bread, got:\n%s", eatCue)
	}
	if strings.Contains(eatCue, "Carrots") {
		t.Errorf("eat cue must NOT name own-produce Carrots, got:\n%s", eatCue)
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

// tavernkeeperStarvedFocusAtForge is the LLM-257 deadlock: John Ellis, a
// multi-output tavernkeeper (stew + water), stands at his tavern on shift with
// focus=stew but NO sage, so stew is input-starved. produce_tick fills only the
// focus and skips an input-short entry, so the tavern makes nothing — not even the
// no-input water — which is exactly how his live production froze for a week. The
// golden pins the FIX across the cue surfaces: the "## Time to produce" section
// falls through to the CHOOSE menu (a starved focus is not "productive", so it does
// NOT lead with "You are producing stew now"), stew is annotated "can't make now:
// short of Sage (need 2, have 0)" (meat, held in full, is omitted), water is
// offered plainly, the standing "You are making Stew." self-state line is suppressed
// (forgeFocusLabel is now inputs-gated), and the production-choice wake warrant fires
// so he re-picks a makeable good. Byte-stable: on shift, idle, empty
// PriceBook/RecentProduce, single-item inventory.
func tavernkeeperStarvedFocusAtForge() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID = sim.ActorID("john")
		tavern = sim.StructureID("tavern")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             0,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"meat": 10}, // has meat, NO sage → stew input-starved
		ProductionFocus:   "stew",
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "water", Source: sim.RestockSourceProduce, Max: 30},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{johnID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"stew":  {OutputItem: "stew", OutputQty: 1, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "sage", Qty: 2}, {Item: "meat", Qty: 10}}, WholesalePrice: 3, RetailPrice: 5},
			"water": {OutputItem: "water", OutputQty: 1, RateQty: 12, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"stew":  {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "stew", DisplayLabelPlural: "stews", Category: sim.ItemCategoryFood},
			"water": {Name: "water", DisplayLabel: "Water", DisplayLabelSingular: "water", DisplayLabelPlural: "water", Category: sim.ItemCategoryDrink},
			"sage":  {Name: "sage", DisplayLabel: "Sage", DisplayLabelSingular: "sage", DisplayLabelPlural: "sage", Category: sim.ItemCategoryMaterial},
			"meat":  {Name: "meat", DisplayLabel: "Meat", DisplayLabelSingular: "meat", DisplayLabelPlural: "meat", Category: sim.ItemCategoryFood},
		},
	}
	warrants := []sim.WarrantMeta{
		{TriggerActorID: johnID, Reason: sim.ProductionChoiceWarrantReason{}, SourceEventID: 1},
	}
	return snap, johnID, warrants
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

// producerInputBelowBatchFloorReorders is the LLM-279 fixture: Hannah Boggs makes
// porridge (3 milk + 5 water per 10-bowl batch) and holds water at 4 — below one
// 5-unit batch (she can't cover the next batch) but above the cap fraction (derived
// water cap 15 → the old rule fired only below 3.75, so she was never reordered).
// A well-keeper sells water, so the buy path is actionable. Milk at 9 is above its
// 2×3=6 floor, so it stays silent. Pins that the batch floor (2×5=10) surfaces both
// the "## Restocking" walk-to and the "## Keeping up production" runway for water.
// Clock-free: no pending orders/deliveries.
func producerInputBelowBatchFloorReorders() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID   = sim.ActorID("hannah")
		wellKeeper = sim.ActorID("osborne")
		inn        = sim.StructureID("inn")
		well       = sim.StructureID("well")
	)
	start, end := 360, 1200 // 06:00-20:00 innkeeper day shift
	now := 480              // 08:00
	published := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"porridge": 30, "milk": 9, "water": 4},
		ProductionFocus:   "porridge",
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
		}},
	}
	osborne := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Goodwife Osborne",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: well,
		Inventory:       map[sim.ItemKind]int{"water": 30},
		// Produces water, so she's a first-hand supplier (LLM-252), not a reseller.
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "water", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{hannahID: hannah, wellKeeper: osborne},
		Structures: map[sim.StructureID]*sim.Structure{
			inn:  plainStructure(inn, "Inn"),
			well: plainStructure(well, "Village Well"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"porridge": {Name: "porridge", DisplayLabel: "porridge", Category: sim.ItemCategoryFood},
			"milk":     {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
			"water":    {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"porridge": {OutputItem: "porridge", OutputQty: 10, RateQty: 8, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2,
				Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}}},
			"milk":  {OutputItem: "milk", OutputQty: 1, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			"water": {OutputItem: "water", OutputQty: 1, RateQty: 12, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
		},
	}
	return snap, hannahID, nil
}

// resellerArrivesAtSupplierBuyHereNoHuddle is the LLM-286 arrival tick: John Ellis,
// a tavernkeeper reselling ale, stepped out mid-shift and walked to the Brewery to
// restock. He stands INSIDE it with the brewer Anders (a first-hand ale producer),
// but no huddle exists yet — one forms only when someone speaks. pay_with_item
// bootstraps the co-located huddle on the call itself (withHuddleBootstrap,
// ZBBS-HOME-400), so the keeper is reachable this very tick. The golden pins that the
// "## Restocking" section renders the concrete "Anders Brewer is here with you and
// sells ale. Buy it now …" imperative rather than the "No seller is here now — use
// move_to …" walk-to list, which before LLM-286 wrongly told him to walk to the
// Brewery he already stood in (live: zbbs-john-ellis, virtual_agent_calls id 63123).
func resellerArrivesAtSupplierBuyHereNoHuddle() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID   = sim.ActorID("john")
		brewerID = sim.ActorID("anders")
		tavern   = sim.StructureID("tavern")
		brewery  = sim.StructureID("brewery")
	)
	start, end := 360, 1080 // 06:00-18:00 tavern day shift
	now := 720              // 12:00 — on shift, stepped out to restock
	published := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: brewery, // arrived at the supplier's shop, away from his own post
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"ale": 1}, // below the reorder threshold (cap 20)
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "ale", Source: sim.RestockSourceBuy, Max: 20},
		}},
		// World-computed awareness: unhuddled, John's co-located audience in the
		// Brewery is Anders — the same structure scope pay_with_item's bootstrap
		// resolves. Stamped here so the "## Around you" line names Anders as it does
		// in production (hand-built snapshots don't get the world-side republish),
		// keeping the pinned prompt internally consistent with the buy-here cue.
		ColocatedAudienceIDs: []sim.ActorID{brewerID},
	}
	anders := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Anders Brewer",
		Role:              "brewer",
		State:             sim.StateIdle,
		WorkStructureID:   brewery,
		InsideStructureID: brewery, // working inside, co-present with John — no huddle yet
		Coins:             10,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"ale": 40},
		// Produces ale, so he is a first-hand supplier (LLM-252), not a reseller.
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "ale", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{johnID: john, brewerID: anders},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern:  plainStructure(tavern, "Tavern"),
			brewery: plainStructure(brewery, "The Brewery"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"ale": {Name: "ale", DisplayLabel: "ale", Category: sim.ItemCategoryDrink},
		},
		RestockReorderPct: 25,
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"ale": {OutputItem: "ale", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
	}
	return snap, johnID, nil
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

// sellerHuddledWithLaboringPeer is the LLM-231 fixture: John Ellis (seller, at his own
// tavern with stew to sell) is huddled with Patience Walker — mid-job for Josiah Thorne
// (a Working LaborOffer, so StateLaboring) — and Grace Bishop, a free customer. It exercises
// both halves of the fix: the busy annotation on Patience in "## Around you", and her absence
// from the seller offer cue (which still lists the free Grace). Josiah is present only as the
// employer named by the annotation (not co-present). No clock-relative churn beyond the fixed
// PublishedAt → byte-stable.
func sellerHuddledWithLaboringPeer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID     = sim.ActorID("john")
		patienceID = sim.ActorID("patience")
		graceID    = sim.ActorID("grace")
		josiahID   = sim.ActorID("josiah")
		tavern     = sim.StructureID("tavern")
		huddle     = sim.HuddleID("h1")
	)
	start, end := 360, 1320 // 06:00–22:00, on shift in the evening
	now := 1140             // 19:00 — keeping the tavern, company present
	published := time.Date(2026, 7, 3, 19, 0, 0, 0, time.UTC)
	workingUntil := published.Add(90 * time.Minute)
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
		Inventory:          map[sim.ItemKind]int{"stew": 4},
		Acquaintances: map[string]sim.Acquaintance{
			"Patience Walker": {}, "Grace Bishop": {}, "Josiah Thorne": {},
		},
	}
	patience := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Patience Walker",
		Role:              "laborer",
		State:             sim.StateLaboring,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
	}
	grace := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Grace Bishop",
		Role:              "villager",
		State:             sim.StateIdle,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
	}
	josiah := &sim.ActorSnapshot{
		Kind:        sim.KindNPCStateful,
		DisplayName: "Josiah Thorne",
		Role:        "shopkeeper",
		State:       sim.StateIdle,
		Needs:       map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			johnID: john, patienceID: patience, graceID: grace, josiahID: josiah,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{johnID: {}, patienceID: {}, graceID: {}}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			5: {ID: 5, WorkerID: patienceID, EmployerID: josiahID, State: sim.LaborStateWorking, WorkingUntil: &workingUntil, HuddleID: huddle},
		},
	}
	return snap, johnID, nil
}

// sellerEmployingOwnLaboringWorker is the LLM-231 employer-seller fixture: John Ellis
// (seller, at his own tavern with stew to sell) is the EMPLOYER of a co-present laboring
// worker, Silence Walker (a Working LaborOffer with John as employer), and is also huddled
// with Grace Bishop, a free customer. It pins the two-way split of the fix: Silence is still
// dropped from the offer cue (exclusion is truthful for every observer, employer included),
// while she carries NO busy annotation in "## Around you" (that is bystander-only — the
// employer sees the "## Workers currently working for you" cue instead). Byte-stable.
func sellerEmployingOwnLaboringWorker() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID    = sim.ActorID("john")
		silenceID = sim.ActorID("silence")
		graceID   = sim.ActorID("grace")
		tavern    = sim.StructureID("tavern")
		huddle    = sim.HuddleID("h1")
	)
	start, end := 360, 1320 // 06:00–22:00, on shift in the evening
	now := 1140             // 19:00 — keeping the tavern, company present
	published := time.Date(2026, 7, 3, 19, 0, 0, 0, time.UTC)
	workingUntil := published.Add(90 * time.Minute)
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
		Inventory:          map[sim.ItemKind]int{"stew": 4},
		Acquaintances: map[string]sim.Acquaintance{
			"Silence Walker": {}, "Grace Bishop": {},
		},
	}
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Silence Walker",
		Role:              "laborer",
		State:             sim.StateLaboring,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
	}
	grace := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Grace Bishop",
		Role:              "villager",
		State:             sim.StateIdle,
		InsideStructureID: tavern,
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			johnID: john, silenceID: silence, graceID: grace,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{johnID: {}, silenceID: {}, graceID: {}}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			5: {ID: 5, WorkerID: silenceID, EmployerID: johnID, Reward: 2, State: sim.LaborStateWorking, WorkingUntil: &workingUntil, HuddleID: huddle},
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

// hungryBuyerWithWholesalerPeer is the LLM-289 situation from live hud-843da92a…:
// a hungry buyer with coins huddles with a wholesaler-farmer carrying food. The
// dispatch-side wholesale gate (LLM-223/252) keys on the SELLER's work anchor
// wherever the seller stands, so the peer's goods are a guaranteed pay_with_item
// rejection for a non-distributor — the co-present peer cue must not advertise
// them. The buyer holds coin (means-to-pay) and none of the peer's food
// (degenerate-buy), so the wholesale gate is the only thing suppressing the line.
// No clock-bound content → byte-stable.
func hungryBuyerWithWholesalerPeer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		silenceID = sim.ActorID("silence")
		mosesID   = sim.ActorID("moses")
		commons   = sim.StructureID("commons")
		farm      = sim.StructureID("james_farm")
		huddle    = sim.HuddleID("h1")
	)
	published := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		Role:              "villager",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		Coins:             22,
		Needs:             map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Acquaintances:     map[string]sim.Acquaintance{"Moses James": {}},
	}
	moses := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Moses James",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		CurrentHuddleID:   huddle,
		WorkStructureID:   farm,
		Inventory:         map[sim.ItemKind]int{"stew": 3},
		Acquaintances:     map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{silenceID: silence, mosesID: moses},
		Structures: map[sim.StructureID]*sim.Structure{
			commons: plainStructure(commons, "Village Commons"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			// The live James Farm carries market_stall+business+farm+wholesaler;
			// only the wholesaler tag gates selling.
			sim.VillageObjectID(farm): {ID: sim.VillageObjectID(farm), OwnerActorID: mosesID, Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{silenceID: {}, mosesID: {}}},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, silenceID, nil
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

// laboringWorkerAddressedByEmployer is the LLM-230 worker-side fixture: Silence
// Walker is mid-job for John Ellis in his tavern (a Working LaborOffer, WorkerID
// == subject) and John speaks to her (an NPC-speech warrant). The golden pins the
// standing "You are working a job for John Ellis … Stay with it until it's done"
// self-state line that anchors her reply — the cue that lets her answer "can't
// stop just now" instead of going silent or abandoning the job. The reply-cadence
// (reactor) and the speak-only tool surface (handlers.gateTools) are covered by
// their own unit tests; the render is unchanged, so this is a regression pin on
// the anchor's presence for the addressed-while-working situation.
func laboringWorkerAddressedByEmployer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
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
			huddle: {
				ID:      huddle,
				Members: map[sim.ActorID]struct{}{johnID: {}, silenceID: {}},
				RecentUtterances: []sim.Utterance{
					{SpeakerID: johnID, SpeakerName: "John Ellis", Text: "Care to tend the fire while you're at it?", At: published.Add(-20 * time.Second)},
				},
			},
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
	// John speaks to her mid-job — the addressed-while-working moment (LLM-230).
	warrants := []sim.WarrantMeta{{
		TriggerActorID: johnID,
		Reason:         sim.NPCSpeechWarrantReason{SpeechID: 1, Speaker: johnID, Excerpt: "Care to tend the fire while you're at it?"},
		SourceEventID:  1,
		HuddleID:       huddle,
		OccurredAt:     published,
	}}
	return snap, silenceID, warrants
}

// returningHelperLaborOfferSnapshot builds the LLM-228 shape: Anne Walker, who
// completed a paid job for Hannah Boggs a few hours ago (an Active
// ObservedHelpedByWorker memory on Hannah), has solicited Hannah again. The
// decision section recalls the past help. employerProduces controls whether
// Hannah makes goods herself (a makeable produce entry + recipe) — true renders
// the "…and you got more done for it" clause (employer_recalls_returning_helper),
// false the bare social beat (employer_recalls_returning_helper_nonproducer). The
// offer is coins-only and affordable so the normal accept/decline footer renders
// alongside the recall.
func returningHelperLaborOfferSnapshot(employerProduces bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		anneID   = sim.ActorID("anne")
		inn      = sim.StructureID("inn")
		huddle   = sim.HuddleID("h1")
	)
	published := time.Date(2026, 7, 3, 11, 0, 0, 0, time.UTC)
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
		// Hannah remembers Anne finishing a paid job for her five hours ago — well
		// inside the 36h HelpedByWorkerMemoryTTL, so the recall reads Active.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{PeerID: anneID, Condition: sim.ObservedHelpedByWorker}: published.Add(-5 * time.Hour),
		}),
	}
	var recipes map[sim.ItemKind]*sim.ItemRecipe
	if employerProduces {
		// A single makeable produce entry makes Hannah a producer for the copy
		// split (subjectProducesGoods) without tripping the multi-output forge cue.
		hannah.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "porridge", Source: sim.RestockSourceProduce, Max: 5},
		}}
		recipes = map[sim.ItemKind]*sim.ItemRecipe{
			"porridge": {OutputItem: "porridge", OutputQty: 1, RateQty: 1, RatePerHours: 2, WholesalePrice: 1, RetailPrice: 2},
		}
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
				Reward:      3,
				DurationMin: 60,
				State:       sim.LaborStatePending,
				HuddleID:    huddle,
			},
		},
		Recipes:   recipes,
		ItemKinds: foodDrinkCatalog(),
	}
	return snap, hannahID, nil
}

func employerRecallsReturningHelper() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return returningHelperLaborOfferSnapshot(true)
}

func employerRecallsReturningHelperNonProducer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return returningHelperLaborOfferSnapshot(false)
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

// visitorArrivesAtKeepersWorkplace reproduces the LLM-284 host-role inversion:
// John Ellis (a tavern keeper) walked to the Blacksmith on an errand and arrives
// with the smith, Ezekiel Crane, already there. Ezekiel keeps the Blacksmith
// (his WorkStructureID is it); John does not. The golden pins that the "## What
// just happened" arrival line names the keeper in the possessive — "You arrived
// at Ezekiel Crane's Blacksmith." — so the model sees whose shop it walked into
// and hosts as a guest instead of greeting the keeper as if hosting his own
// forge. A regression that dropped the possessive (back to "the Blacksmith")
// shows in the diff.
func visitorArrivesAtKeepersWorkplace() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		johnID     = sim.ActorID("john")
		ezekielID  = sim.ActorID("ezekiel")
		blacksmith = sim.StructureID("blacksmith")
		tavern     = sim.StructureID("tavern")
		johnHome   = sim.StructureID("ellis_residence")
	)
	start, end := 360, 1260 // working hours 06:00–21:00
	now := 540              // 09:00 — morning, both on shift
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: blacksmith, // walked over; standing in the smithy
		HomeStructureID:   johnHome,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
		// John knows Ezekiel (a fellow village keeper — in the incident he greeted
		// him by name), so the co-present line names him rather than "the blacksmith".
		Acquaintances: map[string]sim.Acquaintance{"Ezekiel Crane": {}},
		// Ezekiel is within earshot — the co-present keeper of the incident,
		// whom John greeted as if hosting his own forge.
		ColocatedAudienceIDs: []sim.ActorID{ezekielID},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   blacksmith,
		InsideStructureID: blacksmith,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{johnID: john, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			blacksmith: plainStructure(blacksmith, "Blacksmith"),
			tavern:     plainStructure(tavern, "Tavern"),
			johnHome:   plainStructure(johnHome, "Ellis Residence"),
		},
	}
	// John just arrived at the Blacksmith → "## What just happened: You arrived at
	// Ezekiel Crane's Blacksmith." (the keeper possessive, LLM-284).
	warrants := []sim.WarrantMeta{
		{
			TriggerActorID: johnID,
			Reason:         sim.ArrivalWarrantReason{AtStructureID: blacksmith},
			SourceEventID:  1,
		},
	}
	return snap, johnID, warrants
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

// lodgerEveningTavernOpen is the LLM-311 case: the SAME evening as
// homedWorkerEveningTavernOpen, but the agent is homeless-by-design (HomeStructureID
// "") and lodges via an active nightly ledger room grant at the Inn — the canonical
// rent-a-room NPC (Ezekiel). Before LLM-311 the living-evening scope was homed-only, so
// this agent got NO tavern invitation and the off-shift wind-down steered it to its
// rented room the whole evening (the live Inn↔Blacksmith oscillation). With the
// night-place scope the cue fires exactly as for a homed peer — its co-equal "stay in"
// destination is the rented Inn (structure_id: inn), not an empty home token — and the
// go-home/wind-down steer ("Your working hours are over …") is suppressed. Fixed
// PublishedAt (the grant clock) → byte-stable. Makes TestEveningCueReplacesGoHomeSteer
// non-vacuous for the lodger arm.
func lodgerEveningTavernOpen() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		forge     = sim.StructureID("blacksmith")
		inn       = sim.StructureID("inn")
		tavern    = sim.StructureID("tavern")
		innRoom   = sim.RoomID(42)
	)
	start, end := 420, 1140 // 07:00–19:00
	now := 1230             // 20:30 — off shift, inside the evening window
	published := time.Date(2026, 7, 6, 20, 30, 0, 0, time.UTC)
	grantExpires := published.Add(12 * time.Hour) // active, unexpired
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		HomeStructureID:   "", // homeless by design — lodges at the Inn
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: innRoom, Source: sim.AccessSourceLedger}: {
				RoomID: innRoom, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &grantExpires,
			},
		},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:     &now,
		LodgingBedtimeMinute: 1320, // 22:00 — the evening window's close
		PublishedAt:          published,
		NeedThresholds:       sim.NeedThresholds{},
		Actors:               map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:  plainStructure(forge, "Blacksmith"),
			tavern: plainStructure(tavern, "the Tavern"),
			// The Inn holds the lodger's rented room (room 42, distinct from the room id
			// plainStructure assigns) so structureForRoom resolves the night-place to it.
			inn: {
				ID: inn, DisplayName: "the Inn",
				Rooms: []*sim.Room{{ID: innRoom, StructureID: inn, Name: "private_1"}},
			},
		},
		// The tavern venue: a VillageObject tagged "tavern" bridged to the same-id
		// Structure (the shared-identity bridge nearestTaggedVenue reads).
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

// hungryWorkerWithMeansRedirectedToEat is the LLM-276 scenario: a workless, on-shift,
// idle worker whose hunger has climbed into the upper felt band (15, below the red-line
// 18) and who can resolve it now — it holds coin, a free bush is nearby, and a keeper
// sells porridge. The seek-work backstop has stamped a TendNeed warrant, so the prompt
// must steer the worker to EAT (the tend-need felt line + the "## What you can eat or
// drink" options + the one-target need-redirect) and must NOT show the businesses
// directory or the solicit-work hustle — those yield to the resolvable need exactly as
// they do for a red need. The perception half of the live Silence Walker beg-for-food
// fix.
func hungryWorkerWithMeansRedirectedToEat() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		silenceID = sim.ActorID("silence")
		keeperID  = sim.ActorID("keeper")
		home      = sim.StructureID("walker_house")
		store     = sim.StructureID("general_store")
		bush      = sim.VillageObjectID("blueberry_bush")
	)
	now := 720 // 12:00 — on shift (day window [420, 1140))
	silence := &sim.ActorSnapshot{
		Kind:            sim.KindNPCShared,
		DisplayName:     "Silence Walker",
		Role:            "villager",
		State:           sim.StateIdle,
		HomeStructureID: home,
		Coins:           5,
		Needs:           map[sim.NeedKey]int{"hunger": 15},
		AttributeSlugs:  []string{sim.AttrWorker},
	}
	keeper := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Anne Putnam",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   store,
		InsideStructureID: store,
		Inventory:         map[sim.ItemKind]int{"porridge": 5},
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		DawnMinute:       420,
		DuskMinute:       1140,
		DawnDuskMinuteOK: true,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{silenceID: silence, keeperID: keeper},
		Structures: map[sim.StructureID]*sim.Structure{
			home:  plainStructure(home, "Walker House"),
			store: plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			bush:                       {DisplayName: "Blueberry Bush", Pos: sim.WorldPos{X: 40, Y: 40}, Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -2}}},
			sim.VillageObjectID(store): {Pos: sim.WorldPos{X: 80, Y: 0}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"porridge": {DisplayLabel: "a bowl of porridge", Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"porridge": {OutputItem: "porridge", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 5},
		},
	}
	return snap, silenceID, []sim.WarrantMeta{{TriggerActorID: silenceID, Reason: sim.TendNeedWarrantReason{Need: "hunger"}}}
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

// workerSolicitsGoodsRichCoinPoorEmployer is the LLM-243 reduction: a workless
// worker (Silence Walker) shares a huddle with a co-present stranger employer
// (Prudence Ward) who holds 0 coins but goods on hand. Post-fix, a bad coin ask
// against her mints no offer and records no decline (the barter branch), so the
// ledger is EMPTY — she stays a solicitable prospect. The subject is the worker;
// the golden must show the solicit_work affordance for Prudence and NOT the
// SeekWorkPlaces businesses directory or the "No one here can hire you" dead-end.
// Coins=15 for Silence is below the seek-work ceiling (25), so the directory WOULD
// arm if Prudence were foreclosed — the suppression is earned by her solicitability,
// not by the worker being comfortable (non-vacuous).
func workerSolicitsGoodsRichCoinPoorEmployer() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		silenceID  = sim.ActorID("silence")
		prudenceID = sim.ActorID("prudence")
		residence  = sim.StructureID("walker_residence")
		wardHome   = sim.StructureID("ward_house")
		apothecary = sim.StructureID("pw_apothecary")
		commons    = sim.StructureID("commons")
		inn        = sim.StructureID("inn")
		huddle     = sim.HuddleID("h1")
	)
	now := 540 // 09:00 — daytime, on shift
	published := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		Role:              "peddler",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		HomeStructureID:   residence,
		CurrentHuddleID:   huddle,
		Coins:             15,
		AttributeSlugs:    []string{sim.AttrWorker},
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Prudence Ward": {}},
	}
	// Prudence is a structural stranger to Silence (different home; Silence is
	// workless so they never share a workplace) and has NO declined offer against
	// her — solicitable by anchor. Her empty purse must not change that: she holds
	// berries and tea and can hire in kind (LLM-225).
	prudence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Prudence Ward",
		Role:              "herbalist",
		State:             sim.StateIdle,
		InsideStructureID: commons,
		HomeStructureID:   wardHome,
		WorkStructureID:   apothecary,
		CurrentHuddleID:   huddle,
		Coins:             0,
		Inventory:         map[sim.ItemKind]int{"blueberry": 4, "coca_tea": 9, "raspberry": 14},
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{silenceID: silence, prudenceID: prudence},
		Structures: map[sim.StructureID]*sim.Structure{
			commons:    plainStructure(commons, "Village Commons"),
			residence:  plainStructure(residence, "Walker Residence"),
			wardHome:   plainStructure(wardHome, "Ward House"),
			apothecary: plainStructure(apothecary, "PW Apothecary"),
			inn:        plainStructure(inn, "Inn"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{silenceID: {}, prudenceID: {}}},
		},
		// Businesses exist and would populate SeekWorkPlaces if the directory armed —
		// so the absence of the dead-end is because Prudence stays solicitable.
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(apothecary): {ID: sim.VillageObjectID(apothecary), Tags: []string{"business", "shop"}},
			sim.VillageObjectID(inn):        {ID: sim.VillageObjectID(inn), Tags: []string{"business", "lodging"}},
		},
	}
	return snap, silenceID, nil
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
