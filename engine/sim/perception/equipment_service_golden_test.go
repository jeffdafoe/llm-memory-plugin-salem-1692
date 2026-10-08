package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// equipment_service_golden_test.go — golden scenarios + cross-scenario
// invariants for the LLM-648 wright's trade: the owner-side "## Your
// equipment" due cue (standing fact alone; act-now imperative with the wright
// co-present) and the wright-side "## Your trade" rounds steer (resupply /
// walk-to / co-present offer). Registered into perceptionScenarios so
// TestPerceptionGoldens covers them and the terminal-verb invariant sweeps
// their imperatives.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "owner_equipment_due",
			summary: "LLM-648: Joseph's mill has accrued past the due threshold with no wright around. " +
				"'## Your equipment' renders the standing fact (millstones want the wright's attention) and no " +
				"imperative — it keeps until the wright calls.",
			build: ownerEquipmentDueScenario,
		},
		perceptionScenario{
			name: "owner_equipment_wright_copresent",
			summary: "LLM-648: same due mill, but Lewis the wright shares the huddle. The cue escalates to the " +
				"act-now imperative naming pay_with_item (seller Lewis, item equipment_service) with his ~10-coin rate.",
			build: ownerEquipmentWrightCopresentScenario,
		},
		perceptionScenario{
			name: "wright_rounds_walk",
			summary: "LLM-648: Lewis, stone in hand, with the mill long past due across the village. '## Your " +
				"trade' steers the walk (move_to the mill's label, with a bearing) to offer Joseph his service.",
			build: wrightRoundsWalkScenario,
		},
		perceptionScenario{
			name: "wright_rounds_copresent",
			summary: "LLM-648: Lewis co-present with Joseph, mill due. '## Your trade' switches to the offer " +
				"imperative (speak — name the rate); the owner's own cue carries the pay half of the deal.",
			build: wrightRoundsCopresentScenario,
		},
		perceptionScenario{
			name: "wright_no_stone",
			summary: "LLM-648/717: Lewis holds no whetstone, so the rounds cue preempts the rounds with the " +
				"resupply line even though the mill is due. No one in the village holds a stone, so it names nobody " +
				"and gives no destination — 'Nobody in the village has a whetstone to sell just now'.",
			build: wrightNoStoneScenario,
		},
		perceptionScenario{
			name: "wright_no_stone_smith_stocked",
			summary: "LLM-717: Lewis at his own workshop, on shift, out of stone; Ezekiel holds two at the Blacksmith. " +
				"The rounds line names the Blacksmith walk-to bullet (destination: smithy) — the same supplier " +
				"'## Restocking' resolves — and the at-post steer takes its step-out form, not the bare 'stay'.",
			build: wrightNoStoneSmithStockedScenario,
		},
		perceptionScenario{
			name: "wright_no_stone_worn_workshop",
			summary: "LLM-717: as wright_no_stone_smith_stocked, but Lewis's workshop is worn past the degrade line, so " +
				"'## Restocking' is suppressed (LLM-304/608 — whetstone is not a production input). The rounds line " +
				"alone carries the Blacksmith walk-to bullet, and alone turns the at-post steer to its step-out form.",
			build: wrightNoStoneWornWorkshopScenario,
		},
		perceptionScenario{
			name: "wright_no_stone_smith_copresent",
			summary: "LLM-717: Lewis out of stone, standing in the Blacksmith in company with Ezekiel, who holds two. " +
				"The rounds line issues the co-present pay_with_item buy (seller Ezekiel Crane, item whetstone, qty up to 1).",
			build: wrightNoStoneSmithCopresentScenario,
		},
		perceptionScenario{
			name: "wright_no_stone_smith_blocked",
			summary: "LLM-717: Ezekiel holds stones, but Lewis has no coin and no spare goods — the supplier is blocked " +
				"(no means). The rounds line names nobody and gives no destination: no way to buy one just now.",
			build: wrightNoStoneSmithBlockedScenario,
		},
		perceptionScenario{
			name: "wright_copresent_no_odd_job_bid",
			summary: "LLM-651: Lewis — a worker who takes odd jobs, acquainted with Joseph — co-present with him " +
				"while the mill is due. The rounds cue's offer imperative stands ALONE: no solicit_work affordance " +
				"(live, the odd-job bid landed first and Moses paid for the blades and barrow twice). The control " +
				"twin wright_copresent_mill_not_due keeps the bid.",
			build: wrightCopresentNoOddJobBidScenario,
		},
		perceptionScenario{
			name: "wright_copresent_mill_not_due",
			summary: "LLM-651 control: the same Lewis and Joseph with the mill under the threshold. No rounds cue, " +
				"so the ordinary solicit_work affordance renders — the fixture can tell the two apart.",
			build: wrightCopresentMillNotDueScenario,
		},
		perceptionScenario{
			name: "owner_wright_copresent_not_hireable",
			summary: "LLM-651 owner side: Joseph, acquainted with Lewis the odd-jobbing wright, mill due, Lewis " +
				"co-present. '## Your equipment' carries the pay_with_item imperative and the offer_work cue does " +
				"NOT name Lewis. The control twin owner_wright_copresent_mill_not_due names him.",
			build: ownerWrightCopresentNotHireableScenario,
		},
		perceptionScenario{
			name: "owner_wright_copresent_mill_not_due",
			summary: "LLM-651 control: the same pair with the mill under the threshold. No equipment cue, so Lewis " +
				"is an ordinary hand and the offer_work cue names him.",
			build: ownerWrightCopresentMillNotDueScenario,
		},
		perceptionScenario{
			name: "wright_working_for_due_owner",
			summary: "LLM-651 (code_review): Lewis is mid-job for Joseph — an odd-job contract minted before the " +
				"mill came due — and co-present with him. The labor coda pins him; no rounds cue, no solicit_work. " +
				"The rounds resume when the contract settles.",
			build: wrightWorkingForDueOwnerScenario,
		},
		perceptionScenario{
			name: "owner_with_wright_on_the_job",
			summary: "LLM-651 (code_review): Joseph with Lewis mid-job for him and the mill due. '## Your equipment' " +
				"stays the standing fact — no pay_with_item imperative beside 'don't pay again by hand' — and the " +
				"offer_work cue does not name him. The service keeps until the contract settles.",
			build: ownerWithWrightOnTheJobScenario,
		},
	)
}

// equipmentScenarioBase builds the shared snapshot: Joseph owning a due mill,
// Lewis working the wright-tagged workshop, catalog + recipe for the service.
// millUse and lewisStones vary per scenario; huddle "" leaves everyone apart.
func equipmentScenarioBase(millUse, lewisStones int, huddle sim.HuddleID) (*sim.Snapshot, map[sim.ActorID]*sim.ActorSnapshot) {
	now := 600 // 10:00 daytime
	joseph := &sim.ActorSnapshot{
		Kind:               sim.KindNPCShared,
		DisplayName:        "Joseph Scott",
		Role:               "miller",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 40, Y: 40},
		WorkStructureID:    "mill",
		InsideStructureID:  "mill",
		CurrentHuddleID:    huddle,
		Coins:              600,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "miller"},
	}
	lewis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCShared,
		DisplayName:     "Lewis Walker",
		Role:            "wright",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 41, Y: 40},
		WorkStructureID: "workshop",
		CurrentHuddleID: huddle,
		Coins:           24,
		Needs:           map[sim.NeedKey]int{},
		Inventory:       map[sim.ItemKind]int{},
	}
	if lewisStones > 0 {
		lewis.Inventory[sim.WhetstoneKind] = lewisStones
	}
	actors := map[sim.ActorID]*sim.ActorSnapshot{"joseph": joseph, "lewis": lewis}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:             &now,
		NeedThresholds:               sim.NeedThresholds{},
		EquipmentServiceDueThreshold: 100,
		Actors:                       actors,
		Structures: map[sim.StructureID]*sim.Structure{
			"mill":     plainStructure("mill", "The Mill"),
			"workshop": plainStructure("workshop", "Lewis's Workshop"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"mill": {ID: "mill", Pos: sim.WorldPos{X: 320, Y: 320}, OwnerActorID: "joseph",
				Tags: []string{sim.TagBusiness, sim.TagWholesaler}, EquipmentUse: millUse},
			"workshop": {ID: "workshop", Pos: sim.WorldPos{X: 328, Y: 320}, OwnerActorID: "lewis",
				Tags: []string{sim.TagBusiness, sim.TagWright}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"equipment_service": {Name: "equipment_service", DisplayLabel: "Equipment service",
				Capabilities: []string{"service", sim.CapabilityEquipmentService}},
			sim.WhetstoneKind: {Name: sim.WhetstoneKind, DisplayLabel: "Whetstone"},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"equipment_service": {WholesalePrice: 6, RetailPrice: 10},
		},
	}
	if huddle != "" {
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			huddle: {Members: map[sim.ActorID]struct{}{"joseph": {}, "lewis": {}}},
		}
	}
	return snap, actors
}

func ownerEquipmentDueScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actors := equipmentScenarioBase(120, 2, "")
	// No wright around: Lewis stays across the village, out of any huddle.
	actors["lewis"].Pos = sim.TilePos{X: 90, Y: 90}
	return snap, "joseph", nil
}

func ownerEquipmentWrightCopresentScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, _ := equipmentScenarioBase(120, 2, "h1")
	return snap, "joseph", nil
}

func wrightRoundsWalkScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actors := equipmentScenarioBase(200, 2, "")
	// Lewis far from the mill so the steer carries a bearing.
	actors["lewis"].Pos = sim.TilePos{X: 90, Y: 90}
	return snap, "lewis", nil
}

func wrightRoundsCopresentScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, _ := equipmentScenarioBase(150, 2, "h1")
	return snap, "lewis", nil
}

func wrightNoStoneScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actors := equipmentScenarioBase(150, 0, "")
	actors["lewis"].Pos = sim.TilePos{X: 90, Y: 90}
	return snap, "lewis", nil
}

// addStoneSmith gives the equipment fixture a whetstone supply (LLM-717):
// Ezekiel making stones at the Blacksmith with `stones` on hand, and Lewis's
// live `{whetstone, buy, cap 4}` restock entry, so "## Restocking" runs the
// same finder the rounds line does.
func addStoneSmith(snap *sim.Snapshot, stones int) {
	snap.Actors["ezekiel"] = &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 60, Y: 60},
		WorkStructureID:   "smithy",
		InsideStructureID: "smithy",
		Coins:             40,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{sim.WhetstoneKind: stones},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: sim.WhetstoneKind, Source: sim.RestockSourceProduce, Max: 4},
		}},
	}
	snap.Structures["smithy"] = plainStructure("smithy", "Blacksmith")
	snap.VillageObjects["smithy"] = &sim.VillageObject{ID: "smithy", Pos: sim.WorldPos{X: 480, Y: 480},
		OwnerActorID: "ezekiel", Tags: []string{sim.TagBusiness}}
	snap.Recipes[sim.WhetstoneKind] = &sim.ItemRecipe{OutputItem: sim.WhetstoneKind, OutputQty: 1, RateQty: 1,
		RatePerHours: 1, WholesalePrice: 2, RetailPrice: 4}
	snap.Actors["lewis"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: sim.WhetstoneKind, Source: sim.RestockSourceBuy, Max: 4},
	}}
	snap.RestockReorderPct = 25
}

func wrightNoStoneSmithStockedScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actors := equipmentScenarioBase(150, 0, "")
	addStoneSmith(snap, 2)
	// At his own workshop on shift, so the at-post steer must reconcile with
	// the walk-to bullet (LLM-491).
	start, end := 360, 1080
	actors["lewis"].InsideStructureID = "workshop"
	actors["lewis"].ScheduleStartMin = &start
	actors["lewis"].ScheduleEndMin = &end
	return snap, "lewis", nil
}

func wrightNoStoneWornWorkshopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := wrightNoStoneSmithStockedScenario()
	snap.StallWearRepairThreshold = 180
	snap.StallWearDegradeThreshold = 270
	snap.StallDegradedProducePct = 50
	snap.StallNailsPerRepair = 5
	snap.VillageObjects["workshop"].Wear = 300
	snap.Actors[actorID].Inventory["nail"] = 6
	return snap, actorID, warrants
}

func wrightNoStoneSmithCopresentScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actors := equipmentScenarioBase(150, 0, "")
	addStoneSmith(snap, 2)
	lewis := actors["lewis"]
	lewis.Pos = sim.TilePos{X: 61, Y: 60}
	lewis.InsideStructureID = "smithy"
	lewis.CurrentHuddleID = "h3"
	snap.Actors["ezekiel"].CurrentHuddleID = "h3"
	snap.Huddles = map[sim.HuddleID]*sim.Huddle{
		"h3": {ID: "h3", Members: map[sim.ActorID]struct{}{"lewis": {}, "ezekiel": {}}},
	}
	return snap, "lewis", nil
}

func wrightNoStoneSmithBlockedScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actors := equipmentScenarioBase(150, 0, "")
	addStoneSmith(snap, 2)
	actors["lewis"].Pos = sim.TilePos{X: 90, Y: 90}
	actors["lewis"].Coins = 0
	return snap, "lewis", nil
}

// TestWrightNoStoneLineFollowsSupply pins the LLM-717 arms by their load-bearing
// tokens: each supply state renders its own line, and only the walk-to arm
// carries a destination.
func TestWrightNoStoneLineFollowsSupply(t *testing.T) {
	cases := []struct {
		name  string
		build func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta)
		want  string
		dest  bool
	}{
		{"nobody", wrightNoStoneScenario, "Nobody in the village has a whetstone to sell just now — your rounds wait until one comes in.", false},
		{"walk-to", wrightNoStoneSmithStockedScenario, "  - buy from Blacksmith (destination: smithy)", true},
		{"co-present", wrightNoStoneSmithCopresentScenario, `call pay_with_item with seller "Ezekiel Crane", item "whetstone", a qty up to 1`, false},
		{"blocked", wrightNoStoneSmithBlockedScenario, "You have no way to buy one just now — your rounds wait until you can.", false},
		{"peddler", wrightViewsPeddlerScenario, "The trader here has whetstones — buy one from him before you make your rounds.", false},
	}
	for _, c := range cases {
		snap, actorID, warrants := c.build()
		p := Build(snap, actorID, warrants)
		var b strings.Builder
		renderWrightRounds(&b, p.WrightRounds)
		got := b.String()
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: rounds line lacks %q:\n%s", c.name, c.want, got)
		}
		if has := strings.Contains(got, "(destination: "); has != c.dest {
			t.Errorf("%s: destination present=%v, want %v:\n%s", c.name, has, c.dest, got)
		}
		if c.name != "walk-to" && c.name != "co-present" && strings.Contains(got, "Blacksmith") {
			t.Errorf("%s: a line with no buy path names the smith (LLM-301):\n%s", c.name, got)
		}
	}
}

// TestGoldensWrightStoneSellersMatchRestocking — LLM-717 cross-scenario
// invariant: wherever the rounds line and "## Restocking" both resolve
// whetstone suppliers, they name the same destinations. The walk-to scenario
// keeps it non-vacuous.
func TestGoldensWrightStoneSellersMatchRestocking(t *testing.T) {
	compared := 0
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		if !p.WrightRounds.HasWalkToSupplier() || p.Restocking == nil {
			continue
		}
		label := itemDisplayLabel(snap, sim.WhetstoneKind)
		for _, it := range p.Restocking.Items {
			if it.ItemLabel != label {
				continue
			}
			compared++
			if got, want := vendorIDs(p.WrightRounds.StoneVendors), vendorIDs(it.Vendors); got != want {
				t.Errorf("scenario %q: rounds names %s, Restocking names %s", sc.name, got, want)
			}
		}
	}
	if compared == 0 {
		t.Fatal("no scenario renders both whetstone lists — the invariant is vacuous (LLM-717)")
	}
}

func vendorIDs(vs []RestockVendor) string {
	ids := make([]string, 0, len(vs))
	for _, v := range vs {
		ids = append(ids, string(v.StructureID))
	}
	return strings.Join(ids, ",")
}

// oddJobWrightPair gives the base fixture the two facts the labor cues key on
// (LLM-651): Lewis takes odd jobs (AttrWorker), and the pair know each other by
// name, so the solicit cue could name Joseph and the offer_work cue could name
// Lewis. Both share huddle h1. millUse decides whether the service is due.
func oddJobWrightPair(millUse int) (*sim.Snapshot, map[sim.ActorID]*sim.ActorSnapshot) {
	snap, actors := equipmentScenarioBase(millUse, 2, "h1")
	actors["lewis"].AttributeSlugs = []string{sim.AttrWorker}
	actors["lewis"].Acquaintances = map[string]sim.Acquaintance{"Joseph Scott": {}}
	actors["joseph"].Acquaintances = map[string]sim.Acquaintance{"Lewis Walker": {}}
	return snap, actors
}

func wrightCopresentNoOddJobBidScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, _ := oddJobWrightPair(150)
	return snap, "lewis", nil
}

func wrightCopresentMillNotDueScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, _ := oddJobWrightPair(50)
	return snap, "lewis", nil
}

func ownerWrightCopresentNotHireableScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, _ := oddJobWrightPair(120)
	return snap, "joseph", nil
}

func ownerWrightCopresentMillNotDueScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, _ := oddJobWrightPair(50)
	return snap, "joseph", nil
}

// wrightOnTheJobForOwner is the LLM-651 live-contract fixture: the odd-job
// pair, mill due, with Lewis an hour into a four-hour job for Joseph that
// was minted before the mill crossed the threshold.
func wrightOnTheJobForOwner() *sim.Snapshot {
	snap, actors := oddJobWrightPair(150)
	// At the employer's post, as a working hand is — outdoors beside the mill
	// would read as having wandered off the job.
	actors["lewis"].InsideStructureID = "mill"
	published := time.Date(2026, time.September, 3, 15, 0, 0, 0, time.UTC)
	workingUntil := published.Add(3 * time.Hour)
	snap.PublishedAt = published
	snap.LaborLedger = map[sim.LaborID]*sim.LaborOffer{
		1: {ID: 1, WorkerID: "lewis", EmployerID: "joseph", InitiatedBy: "lewis",
			State: sim.LaborStateWorking, Reward: 10, DurationMin: 240,
			WorkingUntil: &workingUntil, HuddleID: "h1"},
	}
	return snap
}

func wrightWorkingForDueOwnerScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wrightOnTheJobForOwner(), "lewis", nil
}

func ownerWithWrightOnTheJobScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wrightOnTheJobForOwner(), "joseph", nil
}

// TestServiceCuesYieldToLiveContract — LLM-651 (code_review): with a live
// labor contract between the pair, neither service imperative renders. Both
// pages carry the contract instead, and the standing fact on the owner's page
// keeps the due-ness visible without an act-now arm.
func TestServiceCuesYieldToLiveContract(t *testing.T) {
	snap, actorID, warrants := wrightWorkingForDueOwnerScenario()
	p := Build(snap, actorID, warrants)
	if p.WrightRounds != nil {
		t.Errorf("wright mid-job: want no rounds cue, got %+v", p.WrightRounds)
	}
	if p.CanSolicitWork {
		t.Error("wright mid-job: want CanSolicitWork false")
	}
	if out := combinedPrompt(Render(p, DefaultRenderConfig())); strings.Contains(out, "The wright's rounds") {
		t.Error("wright mid-job: the rounds cue still rendered")
	}

	snap, actorID, warrants = ownerWithWrightOnTheJobScenario()
	p = Build(snap, actorID, warrants)
	if p.EquipmentService == nil {
		t.Fatal("owner: the mill is due, want the standing fact to render")
	}
	if p.EquipmentService.WrightID != "" || p.EquipmentService.WrightName != "" {
		t.Errorf("owner: the wright on the job must not be named for the pay arm, got %q", p.EquipmentService.WrightName)
	}
	for _, id := range p.HireableWorkers {
		if id == "lewis" {
			t.Error("owner: a worker already on the job must not be hireable")
		}
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if strings.Contains(out, "pay_with_item") {
		t.Error("owner: the pay_with_item imperative rendered beside a live contract")
	}
	if !strings.Contains(out, "It will keep until the wright calls.") {
		t.Error("owner: want the standing-fact wording while the contract runs")
	}
}

// TestLiveLaborBetween — the shared predicate both service builders read: a
// live pending offer, en-route, or working row between the two ids counts;
// an expired pending row, a settled row, or a row with either party swapped
// does not.
func TestLiveLaborBetween(t *testing.T) {
	at := time.Date(2026, time.September, 3, 15, 0, 0, 0, time.UTC)
	past, future := at.Add(-time.Minute), at.Add(time.Minute)
	cases := []struct {
		name string
		row  *sim.LaborOffer
		want bool
	}{
		{"working", &sim.LaborOffer{WorkerID: "w", EmployerID: "e", State: sim.LaborStateWorking}, true},
		{"en route", &sim.LaborOffer{WorkerID: "w", EmployerID: "e", State: sim.LaborStateEnRoute}, true},
		{"pending, unexpired", &sim.LaborOffer{WorkerID: "w", EmployerID: "e", State: sim.LaborStatePending, ExpiresAt: future}, true},
		{"pending, expired", &sim.LaborOffer{WorkerID: "w", EmployerID: "e", State: sim.LaborStatePending, ExpiresAt: past}, false},
		{"completed", &sim.LaborOffer{WorkerID: "w", EmployerID: "e", State: sim.LaborStateCompleted}, false},
		{"parties swapped", &sim.LaborOffer{WorkerID: "e", EmployerID: "w", State: sim.LaborStateWorking}, false},
		{"other employer", &sim.LaborOffer{WorkerID: "w", EmployerID: "x", State: sim.LaborStateWorking}, false},
	}
	for _, c := range cases {
		snap := &sim.Snapshot{PublishedAt: at, LaborLedger: map[sim.LaborID]*sim.LaborOffer{1: c.row}}
		if got := liveLaborBetween(snap, "w", "e"); got != c.want {
			t.Errorf("%s: liveLaborBetween = %v, want %v", c.name, got, c.want)
		}
	}
	if liveLaborBetween(&sim.Snapshot{}, "w", "e") {
		t.Error("empty ledger: want false")
	}
}

// TestGoldensLaborCuesStepAsideForTheService — LLM-651 cross-scenario
// invariant. In every scenario whose subject is a wright on the co-present arm
// of his rounds, the prompt carries no solicit_work affordance; in every
// scenario whose owner-side cue names a co-present wright, that wright is
// absent from HireableWorkers (the one signal behind the offer_work cue and
// tool). Each half must apply to at least one scenario or it is vacuous.
func TestGoldensLaborCuesStepAsideForTheService(t *testing.T) {
	wrightArms, ownerArms := 0, 0
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		if wrightOfferingServiceHere(p.WrightRounds) {
			wrightArms++
			if p.CanSolicitWork {
				t.Errorf("scenario %q: wright offering his service co-present, but CanSolicitWork is still true (LLM-651)", sc.name)
			}
			if out := combinedPrompt(Render(p, DefaultRenderConfig())); strings.Contains(out, "solicit_work") {
				t.Errorf("scenario %q: wright offering his service co-present, but the prompt still advertises solicit_work (LLM-651)", sc.name)
			}
		}
		if p.EquipmentService != nil && p.EquipmentService.WrightID != "" {
			ownerArms++
			for _, id := range p.HireableWorkers {
				if id == p.EquipmentService.WrightID {
					t.Errorf("scenario %q: the equipment cue names the co-present wright, but HireableWorkers still lists him (LLM-651)", sc.name)
				}
			}
		}
	}
	if wrightArms == 0 {
		t.Fatal("no scenario put a wright co-present with a due owner — the wright half is vacuous (LLM-651)")
	}
	if ownerArms == 0 {
		t.Fatal("no scenario put a due owner with a co-present wright — the owner half is vacuous (LLM-651)")
	}
}

// TestLaborCuesReturnWhenNothingIsDue — the LLM-651 controls must discriminate:
// with the mill under the threshold the same pair get the ordinary labor cues,
// so the suppression above is keyed on the service being due, not on the pair.
func TestLaborCuesReturnWhenNothingIsDue(t *testing.T) {
	snap, actorID, warrants := wrightCopresentMillNotDueScenario()
	p := Build(snap, actorID, warrants)
	if p.WrightRounds != nil {
		t.Fatalf("control: mill under threshold, want no rounds cue, got %+v", p.WrightRounds)
	}
	if !p.CanSolicitWork {
		t.Error("control: nothing due, want CanSolicitWork true for the odd-jobbing wright")
	}
	snap, actorID, warrants = ownerWrightCopresentMillNotDueScenario()
	p = Build(snap, actorID, warrants)
	if p.EquipmentService != nil {
		t.Fatalf("control: mill under threshold, want no equipment cue, got %+v", p.EquipmentService)
	}
	found := false
	for _, id := range p.HireableWorkers {
		if id == "lewis" {
			found = true
		}
	}
	if !found {
		t.Errorf("control: nothing due, want Lewis hireable, got %v", p.HireableWorkers)
	}
}

// TestWrightRoundsSkipsNonBusinessObjects — the rounds pick runs the same
// wearable-business predicate as accrual and delivery (code_review): a
// non-business owned object with a stray persisted EquipmentUse must never
// outrank a due wearable business, however high its counter reads.
func TestWrightRoundsSkipsNonBusinessObjects(t *testing.T) {
	snap, actors := equipmentScenarioBase(150, 2, "")
	actors["lewis"].Pos = sim.TilePos{X: 90, Y: 90}
	// A stray owned decoration carrying a higher counter than the due mill —
	// no TagBusiness, so ServiceEquipment could never reset it.
	snap.VillageObjects["shed"] = &sim.VillageObject{
		ID: "shed", DisplayName: "Old Shed", Pos: sim.WorldPos{X: 100, Y: 100},
		OwnerActorID: "joseph", EquipmentUse: 999,
	}
	v := buildWrightRounds(snap, "lewis", snap.Actors["lewis"], nil)
	if v == nil {
		t.Fatal("the due mill must still produce a rounds steer")
	}
	if v.Business != "The Mill" {
		t.Errorf("rounds picked %q, want the wearable business The Mill", v.Business)
	}
}

// TestWrightCuesRequireKeeperOwnership — workplace assignment alone is not the
// trade (code_review): a hired hand assigned to the wright's workshop neither
// gets the rounds steer nor counts as the co-present wright who escalates the
// owner's cue to the pay imperative.
func TestWrightCuesRequireKeeperOwnership(t *testing.T) {
	snap, _ := equipmentScenarioBase(150, 2, "h1")
	// Reassign the workshop to someone else: Lewis still WORKS there but no
	// longer owns it, so he is no longer its keeper.
	snap.VillageObjects["workshop"].OwnerActorID = "someone-else"
	if v := buildWrightRounds(snap, "lewis", snap.Actors["lewis"], nil); v != nil {
		t.Errorf("a non-keeper at the workshop got the rounds steer: %+v", v)
	}
	members := []HuddleMember{{ID: "lewis", DisplayName: "Lewis Walker"}}
	if v := buildEquipmentService(snap, "joseph", snap.Actors["joseph"], members); v == nil || v.WrightName != "" {
		t.Errorf("owner cue: want the standing fact with no wright imperative, got %+v", v)
	}
}

// TestGoldensEquipmentCueOnlyForDueOwner — "## Your equipment" may render only
// for a resident subject whose OWN business has accrued to the due threshold.
// The gate and the cue share DueOwnedBusiness, so a render without a due owned
// business means the build gate broke.
func TestGoldensEquipmentCueOnlyForDueOwner(t *testing.T) {
	const marker = "## Your equipment"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, marker) {
				return
			}
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.VisitorState != nil {
				t.Fatalf("scenario %q: %q rendered for a visitor/absent subject", sc.name, marker)
			}
			if sim.DueOwnedBusiness(snap.VillageObjects, actorID, snap.EquipmentServiceDueThreshold) == nil {
				t.Errorf("scenario %q: %q rendered with no due owned business", sc.name, marker)
			}
		})
	}
}

// TestGoldensWrightTradeCueOnlyForWright — "## Your trade" may render only for
// a resident subject working a wright-tagged structure.
func TestGoldensWrightTradeCueOnlyForWright(t *testing.T) {
	const marker = "## The wright's rounds"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, marker) {
				return
			}
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.VisitorState != nil || !sim.ActorIsWright(snap.VillageObjects, a.WorkStructureID, actorID) {
				t.Errorf("scenario %q: %q rendered for a non-wright subject", sc.name, marker)
			}
		})
	}
}

// TestGoldensEquipmentPayImperativeNeedsWright — the owner-side pay_with_item
// imperative ("have him see to it") may render only when a wright shares the
// subject's huddle; without one the cue must stay a standing fact.
func TestGoldensEquipmentPayImperativeNeedsWright(t *testing.T) {
	const imperative = "have him see to it"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, imperative) {
				return
			}
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || a.CurrentHuddleID == "" {
				t.Fatalf("scenario %q: the pay imperative rendered outside any huddle", sc.name)
			}
			h := snap.Huddles[a.CurrentHuddleID]
			if h == nil {
				t.Fatalf("scenario %q: subject's huddle missing from the snapshot", sc.name)
			}
			hasWright := false
			for id := range h.Members {
				if id == actorID {
					continue
				}
				if m := snap.Actors[id]; m != nil && sim.ActorIsWright(snap.VillageObjects, m.WorkStructureID, id) {
					hasWright = true
				}
			}
			if !hasWright {
				t.Errorf("scenario %q: the pay imperative rendered with no wright co-present", sc.name)
			}
		})
	}
}
