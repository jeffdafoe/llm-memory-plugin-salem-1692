package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// public_works_golden_test.go — golden scenarios + a cross-scenario invariant for
// LLM-654 public works: a broken well, the hand's "## The town's works" bounty
// (walk-to, then call repair at the well), the constable's standing fact, the
// empty-chest silence, and a thirsty villager who is never offered the broken
// well. Registered into perceptionScenarios so TestPerceptionGoldens covers them
// and the terminal-verb invariant sweeps their imperatives.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "hand_hears_of_broken_well",
			summary: "LLM-654: the well by the Mill is broken and the chest can pay. Anne, a worker at home across " +
				"the village, gets '## The town's works': the windlass is down, the town pays 12 coins, about an hour " +
				"of work, no nails — and a walk-to by bearing and id. It names no second tool (repair is offered only " +
				"at the well).",
			build: handHearsOfBrokenWellScenario,
		},
		perceptionScenario{
			name: "hand_at_broken_well_offered_repair",
			summary: "LLM-654: Anne stands at the broken well. The cue switches to the act-now line — call repair " +
				"and stay put — the same signal that hands her the repair tool.",
			build: handAtBrokenWellScenario,
		},
		perceptionScenario{
			name: "constable_knows_the_well_is_broken",
			summary: "LLM-654: Gideon the constable hears the standing fact — the well is broken and the town has " +
				"posted 12 coins for the mending, open to any hand. No imperative, no tool: he hires no one.",
			build: constableKnowsWellIsBrokenScenario,
		},
		perceptionScenario{
			name: "hand_broken_well_chest_cannot_pay",
			summary: "LLM-654: the same broken well, but the chest holds less than bounty + reserve. The hand hears " +
				"nothing of it — the town cannot pay, so there is no work on offer.",
			build: handBrokenWellChestLowScenario,
		},
		perceptionScenario{
			name: "thirsty_villager_passes_a_broken_well",
			summary: "LLM-654: thirsty Joseph (not a worker) stands beside the broken well. His drink options list " +
				"only the sound well across the village — the broken one is never offered — and he hears no bounty.",
			build: thirstyVillagerPassesBrokenWellScenario,
		},
		perceptionScenario{
			name: "hand_hears_of_storm_damaged_shop",
			summary: "LLM-675: a storm has torn at the General Store (both wells sound). Anne, a worker at home, " +
				"hears it under '## The town's works': it takes in no new stock until mended, the town pays 25 coins " +
				"for about two hours of work, no nails — and a walk-to by the store's id. No second tool.",
			build: handHearsOfDamagedShopScenario,
		},
		perceptionScenario{
			name: "hand_in_damaged_shop_offered_repair",
			summary: "LLM-675: Anne stands inside the damaged General Store. The cue names only that site and " +
				"switches to 'Call repair' — the signal that hands her the repair tool.",
			build: handInDamagedShopScenario,
		},
		perceptionScenario{
			name: "keeper_of_damaged_shop_hears_town_pays",
			summary: "LLM-675: Josiah keeps the storm-damaged General Store and stands in it. He is told what the " +
				"storm did, that he can take in no new shelf stock and works slowly until it is mended, and that the " +
				"town pays a hand 25 coins — the work is not his. No repair tool, no '## Your business' mend.",
			build: keeperOfDamagedShopScenario,
		},
		perceptionScenario{
			name: "constable_knows_shop_and_well_are_damaged",
			summary: "LLM-675: the mill well is broken and the General Store damaged at once. Gideon hears both " +
				"standing facts, each with the bounty the town has posted — 12 for the well, 25 for the store.",
			build: constableKnowsShopAndWellScenario,
		},
		perceptionScenario{
			name: "hand_after_shop_mended_hears_nothing",
			summary: "LLM-675: the General Store has been mended and both wells are sound. Anne hears nothing of " +
				"the town's works.",
			build: handAfterShopMendedScenario,
		},
		perceptionScenario{
			name: "hand_hears_of_fallen_tree_on_road",
			summary: "LLM-677: a fallen maple lies across the road by the Mill (both wells sound). Anne, a worker at home, " +
				"hears it under '## The town's works': walkers must go around it until it is cleared, the town pays 10 " +
				"coins to whoever clears it, about an hour of work — and a walk-to by the tree's id. No second tool.",
			build: handHearsOfFallenTreeScenario,
		},
		perceptionScenario{
			name: "hand_at_fallen_tree_offered_repair",
			summary: "LLM-677: Anne stands at the fallen maple's loiter pin, just south of it on the road. The cue names " +
				"only that site and switches to 'Call repair' — the signal that hands her the repair tool.",
			build: handAtFallenTreeScenario,
		},
		perceptionScenario{
			name: "constable_knows_the_road_is_blocked",
			summary: "LLM-677: Gideon hears the standing fact — a fallen maple lies across the road by the Mill, and the " +
				"town has posted 10 coins for the clearing, open to any hand. No imperative, no tool.",
			build: constableKnowsRoadIsBlockedScenario,
		},
	)
}

const pwJosiah = sim.ActorID("josiah")

// damagedShopSnapshot is publicWorksSnapshot with the mill well set by
// wellBroken, plus Josiah's General Store — storm-damaged when shopDamaged,
// carrying its storm debris — and the business terms (25 coins, two hours).
func damagedShopSnapshot(chest int, wellBroken, shopDamaged bool) *sim.Snapshot {
	snap := publicWorksSnapshot(chest)
	if !wellBroken {
		snap.VillageObjects["mill_well"].DamagedAt = time.Time{}
	}
	zero := 0
	start, end := 480, 1080
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Josiah Thorne",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 1600, Y: 1200}.Tile(),
		InsideStructureID: "store",
		WorkStructureID:   "store",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
	}
	snap.Actors[pwJosiah] = josiah
	snap.Structures["store"] = plainStructure("store", "General Store")
	store := &sim.VillageObject{ID: "store", DisplayName: "General Store", Pos: sim.WorldPos{X: 1600, Y: 1200},
		OwnerActorID: pwJosiah, Tags: []string{sim.TagBusiness}, Wear: 60,
		LoiterOffsetX: &zero, LoiterOffsetY: &zero}
	snap.VillageObjects["store"] = store
	if shopDamaged {
		store.DamagedAt = time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
		snap.VillageObjects["store_debris"] = &sim.VillageObject{ID: "store_debris", AssetID: sim.DebrisAssetID,
			CurrentState: sim.DebrisStateStorm, AttachedTo: "store", Tags: []string{sim.TagDebris},
			Pos: sim.WorldPos{X: 1600, Y: 1200}}
	}
	snap.PublicWorksBusinessBounty = 25
	snap.PublicWorksBusinessRepairSeconds = 7200
	snap.StallWearRepairThreshold = 180
	snap.StallWearDegradeThreshold = 270
	return snap
}

func handHearsOfDamagedShopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return damagedShopSnapshot(100, false, true), pwAnne, nil
}

func handInDamagedShopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := damagedShopSnapshot(100, false, true)
	a := snap.Actors[pwAnne]
	a.Pos = sim.WorldPos{X: 1600, Y: 1200}.Tile()
	a.InsideStructureID = "store"
	return snap, pwAnne, nil
}

func keeperOfDamagedShopScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return damagedShopSnapshot(100, false, true), pwJosiah, nil
}

func constableKnowsShopAndWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return damagedShopSnapshot(100, true, true), pwGideon, nil
}

func handAfterShopMendedScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return damagedShopSnapshot(100, false, false), pwAnne, nil
}

const (
	pwAnne   = sim.ActorID("anne")
	pwGideon = sim.ActorID("gideon")
	pwJoseph = sim.ActorID("joseph")
)

// publicWorksSnapshot builds the shared fixture: the mill well broken (by the
// Mill), the centre well sound far away, Anne (a worker) at her home, Gideon
// the constable at the Meeting House, Joseph (not a worker) at the Mill. The
// chest holds `chest` against a bounty of 12 and a reserve of 50.
func publicWorksSnapshot(chest int) *sim.Snapshot {
	zero := 0
	now := 600
	start, end := 480, 1080
	broken := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ip := func(v int) *int { return &v }
	wellRows := func() []*sim.ObjectRefresh {
		return []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -8},
			{Amount: 0, GatherItem: "water", AvailableQuantity: ip(20), MaxQuantity: ip(20)},
		}
	}
	actor := func(name string, pos sim.WorldPos, inside sim.StructureID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCShared,
			DisplayName:       name,
			State:             sim.StateIdle,
			Pos:               pos.Tile(),
			InsideStructureID: inside,
			ScheduleStartMin:  &start,
			ScheduleEndMin:    &end,
			Coins:             20,
			Needs:             map[sim.NeedKey]int{},
			Inventory:         map[sim.ItemKind]int{},
		}
	}
	anne := actor("Anne Walker", sim.WorldPos{X: 3000, Y: 600}, "walker_home")
	anne.AttributeSlugs = []string{sim.AttrWorker}
	anne.HomeStructureID = "walker_home"
	gideon := actor("Constable Gideon Marsh", sim.WorldPos{X: 2000, Y: 2000}, "meeting_house")
	gideon.Kind = sim.KindNPCStateful
	gideon.AttributeSlugs = []string{sim.AttrConstable}
	gideon.WorkStructureID = "meeting_house"
	joseph := actor("Joseph Scott", sim.WorldPos{X: 700, Y: 400}, "")
	joseph.WorkStructureID = "mill"

	return &sim.Snapshot{
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.DefaultNeedThresholds(),
		Assets:                   emptyAssetSet,
		Environment:              sim.WorldEnvironment{TownChest: chest},
		PublicWorksBounty:        12,
		PublicWorksChestReserve:  50,
		PublicWorksRepairSeconds: 3600,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{pwAnne: anne, pwGideon: gideon, pwJoseph: joseph},
		Structures: map[sim.StructureID]*sim.Structure{
			"mill":          plainStructure("mill", "Mill"),
			"meeting_house": plainStructure("meeting_house", "Meeting House"),
			"walker_home":   plainStructure("walker_home", "Walker Residence"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"mill": {ID: "mill", DisplayName: "Mill", Pos: sim.WorldPos{X: 500, Y: 400},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero},
			"meeting_house": {ID: "meeting_house", DisplayName: "Meeting House", Pos: sim.WorldPos{X: 2000, Y: 2000},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero},
			"walker_home": {ID: "walker_home", DisplayName: "Walker Residence", Pos: sim.WorldPos{X: 3000, Y: 600},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero},
			"mill_well": {ID: "mill_well", DisplayName: "Well", Pos: sim.WorldPos{X: 700, Y: 400},
				Tags: []string{sim.TagWell}, LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				DamagedAt: broken, Refreshes: wellRows()},
			"centre_well": {ID: "centre_well", DisplayName: "Well", Pos: sim.WorldPos{X: 2400, Y: 2400},
				Tags: []string{sim.TagWell}, LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				Refreshes: wellRows()},
		},
	}
}

func handHearsOfBrokenWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return publicWorksSnapshot(100), pwAnne, nil
}

func handAtBrokenWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := publicWorksSnapshot(100)
	a := snap.Actors[pwAnne]
	a.Pos = sim.WorldPos{X: 700, Y: 400}.Tile()
	a.InsideStructureID = ""
	return snap, pwAnne, nil
}

func constableKnowsWellIsBrokenScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return publicWorksSnapshot(100), pwGideon, nil
}

func handBrokenWellChestLowScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return publicWorksSnapshot(61), pwAnne, nil
}

func thirstyVillagerPassesBrokenWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := publicWorksSnapshot(100)
	snap.Actors[pwJoseph].Needs = map[sim.NeedKey]int{"thirst": sim.NeedMax * 7 / 10}
	return snap, pwJoseph, nil
}

// TestGoldensTownWorksReachOnlyHandsAndTheConstable — across the whole matrix,
// "## The town's works" renders only for a worker, the constable, or the keeper
// of a damaged business (LLM-675), and "Call repair" only for a worker standing
// at a damaged site (the one signal that also hands out the repair tool).
// Vacuity-guarded on both lines.
func TestGoldensTownWorksReachOnlyHandsAndTheConstable(t *testing.T) {
	sawSection, sawRepair := false, false
	keepsDamagedShop := func(snap *sim.Snapshot, id sim.ActorID) bool {
		for _, obj := range snap.VillageObjects {
			if obj.OwnerActorID == id && sim.PublicWorksKind(obj) == sim.PublicWorksBusiness && obj.Damaged() {
				return true
			}
		}
		return false
	}
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			p := Build(snap, actorID, warrants)
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			if strings.Contains(out, "## The town's works") {
				sawSection = true
				if !subjectIsWorker(a) && !isConstableSnapshot(a) && !keepsDamagedShop(snap, actorID) {
					t.Errorf("%q is neither a hand, the constable nor a damaged shop's keeper and hears of the town's works:\n%s", a.DisplayName, out)
				}
			}
			if strings.Contains(out, "Call repair to take the work") {
				sawRepair = true
				if !p.PublicWorks.OffersRepair() {
					t.Errorf("%q is told to call repair but the tool gate would not offer it", a.DisplayName)
				}
			}
		})
	}
	if !sawSection || !sawRepair {
		t.Fatalf("invariant is vacuous: section seen=%v, repair line seen=%v", sawSection, sawRepair)
	}
}

// TestBrokenWellEasesNothing — the shared magnitude core the satiation and
// recovery scans read reports 0 for a broken well, so it is never offered.
func TestBrokenWellEasesNothing(t *testing.T) {
	snap := publicWorksSnapshot(100)
	if got := objectRefreshMagnitude(snap.VillageObjects["mill_well"], "thirst"); got != 0 {
		t.Errorf("broken well eases thirst by %d, want 0", got)
	}
	if got := objectRefreshMagnitude(snap.VillageObjects["centre_well"], "thirst"); got <= 0 {
		t.Errorf("sound well eases thirst by %d, want > 0", got)
	}
}

// TestDamagedShopKeeperIsNotAskedToMend (LLM-675) — the keeper of a damaged
// shop (wear under the mend line) is told of the town's work but is offered no
// mend of his own and no repair tool, and his shop reads out of trade — the
// "## Restocking" suppression keys on it.
func TestDamagedShopKeeperIsNotAskedToMend(t *testing.T) {
	snap, actorID, _ := keeperOfDamagedShopScenario()
	p := Build(snap, actorID, nil)
	if p.PublicWorks == nil || !p.PublicWorks.Keeper {
		t.Fatalf("keeper view = %+v, want the keeper's town's-works view", p.PublicWorks)
	}
	if p.PublicWorks.OffersRepair() || p.StallRepair != nil {
		t.Errorf("keeper offered a mend: town repair %v, own mend %+v", p.PublicWorks.OffersRepair(), p.StallRepair)
	}
	if !ownerBusinessDegraded(snap, actorID) {
		t.Error("a damaged shop does not read out of trade")
	}
	snap.VillageObjects["store"].DamagedAt = time.Time{}
	if ownerBusinessDegraded(snap, actorID) {
		t.Error("control: a sound shop under the degrade line reads out of trade")
	}
}

// TestPublicWorksSilentAtOwnStall — a hand who owns a stall and stands at it is
// offered no town's work there, even with the broken well at the same spot:
// StartRepair takes the own-stall branch first, so "call repair" would mend the
// stall, not the well. With the stall elsewhere the offer stands.
func TestPublicWorksSilentAtOwnStall(t *testing.T) {
	snap, actorID, _ := handAtBrokenWellScenario()
	zero := 0
	stall := &sim.VillageObject{ID: "anne_stall", DisplayName: "Anne's Stall", OwnerActorID: actorID,
		Tags: []string{sim.TagBusiness}, Pos: sim.WorldPos{X: 700, Y: 400}, LoiterOffsetX: &zero, LoiterOffsetY: &zero}
	snap.VillageObjects["anne_stall"] = stall
	if v := buildPublicWorks(snap, actorID, snap.Actors[actorID], false); v != nil {
		t.Errorf("hand at her own stall offered the town's work: %+v", v)
	}
	stall.Pos = sim.WorldPos{X: 6000, Y: 6000}
	if v := buildPublicWorks(snap, actorID, snap.Actors[actorID], false); !v.OffersRepair() {
		t.Errorf("control: with her stall elsewhere she should be offered repair, got %+v", v)
	}
}

// TestPublicWorksSilentWhileBusy — a hand at the broken well who is mid another
// activity (here, a harvest) is not offered the work or the repair tool:
// StartRepair would bounce the second window as busy.
func TestPublicWorksSilentWhileBusy(t *testing.T) {
	snap, actorID, _ := handAtBrokenWellScenario()
	if v := buildPublicWorks(snap, actorID, snap.Actors[actorID], false); !v.OffersRepair() {
		t.Fatalf("control: an idle hand at the well should be offered repair, got %+v", v)
	}
	snap.Actors[actorID].SourceActivityKind = sim.SourceActivityHarvest
	if v := buildPublicWorks(snap, actorID, snap.Actors[actorID], false); v != nil {
		t.Errorf("busy hand offered the town's work: %+v", v)
	}
}

// fallenTreeSnapshot is publicWorksSnapshot with both wells sound and a fallen
// maple (LLM-677, LLM-678) lying across the road about twelve tiles south of the
// Mill, on the road terms (10 coins, an hour). The asset carries the live 3x3
// footprint, so its loiter pin is three tiles south of the anchor.
func fallenTreeSnapshot(chest int) *sim.Snapshot {
	snap := publicWorksSnapshot(chest)
	snap.VillageObjects["mill_well"].DamagedAt = time.Time{}
	snap.Assets = map[sim.AssetID]*sim.Asset{
		sim.FallenMapleAssetID: {ID: sim.FallenMapleAssetID, Name: "Fallen Maple", IsObstacle: true,
			FootprintLeft: 1, FootprintRight: 1, FootprintTop: 1, FootprintBottom: 1},
	}
	snap.VillageObjects["fallen_maple"] = &sim.VillageObject{ID: "fallen_maple", DisplayName: "Fallen maple",
		AssetID: sim.FallenMapleAssetID, CurrentState: "bare", Pos: sim.WorldPos{X: 600, Y: 800}, Tags: []string{sim.TagRoadObstacle},
		DamagedAt: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
	snap.PublicWorksRoadBounty = 10
	snap.PublicWorksRoadRepairSeconds = 3600
	return snap
}

func handHearsOfFallenTreeScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return fallenTreeSnapshot(100), pwAnne, nil
}

func handAtFallenTreeScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := fallenTreeSnapshot(100)
	a := snap.Actors[pwAnne]
	anchor := snap.VillageObjects["fallen_maple"].Pos.Tile()
	a.Pos = sim.TilePos{X: anchor.X, Y: anchor.Y + 3}
	a.InsideStructureID = ""
	return snap, pwAnne, nil
}

func constableKnowsRoadIsBlockedScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return fallenTreeSnapshot(100), pwGideon, nil
}
