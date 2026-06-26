package perception

import (
	"flag"
	"os"
	"path/filepath"
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
			"'## At your forge' CHOOSE menu — each makeable good with its per-unit time, stock vs cap, and weekly made/sold " +
			"counts — under the 'Choose what to forge next' header, plus the 'decide what to make next' wake warrant. With no " +
			"focus set, the steer cue and the standing 'You are making nail.' line do NOT render here (see " +
			"smith_forging_focused). A single-output producer never gets this section (see " +
			"TestForgeCueOnlyForMultiOutputCrafterAtForge).",
		build: smithChoosingAtForge,
	},
	{
		name: "smith_forging_focused",
		summary: "The same multi-output crafter (Ezekiel) at his forge WITH a productive focus already set (nail, below " +
			"cap) and no production-choice warrant — the steady state after he has chosen (LLM-128). The golden pins the " +
			"focus-aware cue: the '## At your forge' section leads with 'You are crafting nails now — tend your post or call " +
			"done()' INSTEAD of the choose menu, so the weak model isn't re-invited to pick what it is already forging. The " +
			"standing 'You are making nail.' self-state line renders too. Pairs with smith_choosing_at_forge (unfocused -> " +
			"menu) to pin both halves of the cue.",
		build: smithForgingFocused,
	},
	{
		name: "smith_off_work_focus_hidden",
		summary: "The same multi-output crafter (Ezekiel, focus still nail) is NOT at his forge — he is at the Tavern " +
			"after his shift (the live Tavern bug, LLM-121). produce_tick makes nothing away from the workplace, so the " +
			"standing 'You are making nail.' self-state line must NOT render here, and the '## At your forge' cue is " +
			"likewise absent. The golden pins that neither leaks into an off-work turn; a regression to the work-structure " +
			"gate would make the line reappear in the diff (see TestProductionFocusLineOnlyAtWork).",
		build: smithOffWorkFocusHidden,
	},
	{
		name: "smith_bartering_at_tavern",
		summary: "A smith (Ezekiel) carrying his own wares stands in the Tavern in company with John Ellis the " +
			"tavernkeeper — the live LLM-125 barter scene. Off shift and away from the forge, so neither '## At your " +
			"forge' nor the 'You are making nail.' line render; the new '## What your wares fetch' cue DOES, valuing his " +
			"own-trade goods (nail 1-2, skillet 5-10 from the recipe wholesale-retail spread) so a barter has a coin " +
			"yardstick instead of an invented number. No coin sales history yet (empty PriceBook), so no recent-price clause.",
		build: smithBarteringAtTavern,
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
// invariant: the "## At your forge" cue appears in EXACTLY the multi-output-crafter-
// at-forge scenarios and no other — whether unfocused (choose menu,
// smith_choosing_at_forge) or focused (the LLM-128 continue-and-stop steer,
// smith_forging_focused). A single-output producer or a non-crafter must never see
// it — the structural property the per-builder gate (>1 produce entry AND at
// workplace) is meant to hold across the whole matrix.
func TestForgeCueOnlyForMultiOutputCrafterAtForge(t *testing.T) {
	const marker = "## At your forge"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "smith_choosing_at_forge" || sc.name == "smith_forging_focused"
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
		want := sc.name == "smith_forging_focused"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: production-focus line present=%v, want %v", sc.name, has, want)
		}
	}
}

// TestWaresWorthCueOnlyInCompanyWithOwnTrade is the LLM-125 cross-scenario
// invariant: the "## What your wares fetch" cue appears in EXACTLY the scenario
// where the actor is in company (a huddle) AND has priced own-trade goods
// (smith_bartering_at_tavern). An actor alone — even at its forge with recipes —
// or one in company but without its own priced trade goods must never see it: the
// own-trade base price stays out of solo and non-producer turns, and is gated on
// company rather than location (unlike the forge cue).
func TestWaresWorthCueOnlyInCompanyWithOwnTrade(t *testing.T) {
	const marker = "## What your wares fetch"
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := sc.name == "smith_bartering_at_tavern"
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: wares-worth cue present=%v, want %v", sc.name, has, want)
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

func producerStarvingAtPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return grazingProducerScenario(sim.DefaultHungerRedThreshold) // 18 — red/desperation tier
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
// production-choice warrant fires on. The "## At your forge" cue lists both makeable
// goods (time cost, stock vs cap, empty weekly made/sold counts) under the "Choose
// what to forge next" header, and the production-choice wake warrant renders. With
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

// smithForgingFocused is the LLM-128 steady state: Ezekiel at his own forge on
// shift WITH a productive focus already set (nail, below cap) and NO production-
// choice warrant — the consistent state once he has chosen (shouldChooseProduction
// gates the warrant off for a productive focus, so no "decide what to make next").
// The "## At your forge" cue leads with the "You are crafting nails now — tend your
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
// the "## At your forge" choice cue is likewise gated off. Mirrors smithChoosingAtForge
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
// "## At your forge" cue nor the "You are making nail." line render; what DOES
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
