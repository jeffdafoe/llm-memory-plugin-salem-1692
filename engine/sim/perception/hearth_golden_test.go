package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// hearth_golden_test.go — LLM-412 golden fixtures: the cold need's situated
// self line (every exposure branch) and the hearth stoke cue (owner + hired),
// plus the cross-scenario free-relief invariant.

// hearthClock is the fixed publish instant every LLM-412 fixture uses as the
// fire clock (Snapshot.PublishedAt), so HearthLitUntil offsets are
// deterministic across re-renders.
var hearthClock = time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)

// coldOutdoorsInStorm: red cold, outdoors, storm — the roof steer with the
// subject's own home as the concrete free destination.
func coldOutdoorsInStorm() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720 // 12:00
	lewis := &sim.ActorSnapshot{
		Kind:        sim.KindNPCStateful,
		DisplayName: "Lewis Walker",
		State:       sim.StateIdle,
		Pos:         sim.WorldPos{X: 500, Y: 500}.Tile(),
		// Outdoors: InsideStructureID empty.
		HomeStructureID: "walker_house",
		Coins:           3,
		Needs:           map[sim.NeedKey]int{sim.ColdNeedKey: 17}, // past the default red 16
	}
	snap := &sim.Snapshot{
		PublishedAt:      hearthClock,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		HearthLowMinutes: 60,
		Environment:      sim.WorldEnvironment{Weather: sim.WeatherStorm},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"lewis": lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			"walker_house": plainStructure("walker_house", "Walker House"),
		},
	}
	return snap, "lewis", nil
}

// coldInUnheatedRoomStorm: mild cold, inside a plain hearthless structure,
// storm — the roof-is-easing-it branch.
func coldInUnheatedRoomStorm() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "meeting_house",
		Coins:             3,
		Needs:             map[sim.NeedKey]int{sim.ColdNeedKey: 11}, // mild ("chilled")
	}
	snap := &sim.Snapshot{
		PublishedAt:      hearthClock,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		HearthLowMinutes: 60,
		Environment:      sim.WorldEnvironment{Weather: sim.WeatherStorm},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"lewis": lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			"meeting_house": plainStructure("meeting_house", "Meeting House"),
		},
	}
	return snap, "lewis", nil
}

// warmByLitFire: a chilled subject inside the tavern whose hearth is LIT —
// the warm branch, and no hearth cue (not his fire, and it needs no wood).
func warmByLitFire() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "tavern",
		Coins:             3,
		Needs:             map[sim.NeedKey]int{sim.ColdNeedKey: 11},
	}
	snap := &sim.Snapshot{
		PublishedAt:      hearthClock,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		HearthLowMinutes: 60,
		Environment:      sim.WorldEnvironment{Weather: sim.WeatherStorm},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"lewis": lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:             "tavern",
				DisplayName:    "Tavern",
				Pos:            sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:   "hannah",
				Tags:           []string{sim.TagBusiness, sim.TagHearth},
				HearthLitUntil: hearthClock.Add(3 * time.Hour), // burning well
			},
		},
	}
	return snap, "lewis", nil
}

// warmGarmentGoldenCatalog is the minimal item catalog the LLM-410 warm-garment
// fixtures need: coat + cloak carrying the warms capability, so the cold self-line's
// garment branch (the carry-check and the vendor finder) resolves. Labels match the
// seed migration so the rendered cue reads as it will in production.
func warmGarmentGoldenCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"coat":  {Name: "coat", DisplayLabel: "Coat", DisplayLabelSingular: "coat", DisplayLabelPlural: "coats", Category: "clothing", Capabilities: []string{sim.CapabilityWarms}},
		"cloak": {Name: "cloak", DisplayLabel: "Cloak", DisplayLabelSingular: "cloak", DisplayLabelPlural: "cloaks", Category: "clothing", Capabilities: []string{sim.CapabilityWarms}},
	}
}

// coldOutdoorsInStormCoated: the same red-cold storm-outdoors scene as
// coldOutdoorsInStorm, but Lewis is CARRYING a coat (CapabilityWarms). The garment
// confirming line renders after the unconditional free-relief steer, and no buy
// nudge (he already has one).
func coldOutdoorsInStormCoated() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	lewis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Lewis Walker",
		State:           sim.StateIdle,
		Pos:             sim.WorldPos{X: 500, Y: 500}.Tile(),
		HomeStructureID: "walker_house",
		Coins:           3,
		Needs:           map[sim.NeedKey]int{sim.ColdNeedKey: 17}, // past the default red 16
		Inventory:       map[sim.ItemKind]int{"coat": 1},
	}
	snap := &sim.Snapshot{
		PublishedAt:      hearthClock,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		HearthLowMinutes: 60,
		Environment:      sim.WorldEnvironment{Weather: sim.WeatherStorm},
		ItemKinds:        warmGarmentGoldenCatalog(),
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"lewis": lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			"walker_house": plainStructure("walker_house", "Walker House"),
		},
	}
	return snap, "lewis", nil
}

// coldOutdoorsInStormCoatForSale: red cold, outdoors, storm, Lewis carrying NO
// coat — but Josiah Thorne holds coats + a cloak at the General Store. The
// vendor-gated buy nudge renders after the free-relief steer; contrast
// cold_outdoors_in_storm, whose world has no seller, so no nudge renders.
func coldOutdoorsInStormCoatForSale() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	start, end := 360, 1080
	lewis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Lewis Walker",
		State:           sim.StateIdle,
		Pos:             sim.WorldPos{X: 500, Y: 500}.Tile(),
		HomeStructureID: "walker_house",
		Coins:           20,
		Needs:           map[sim.NeedKey]int{sim.ColdNeedKey: 17},
	}
	// Far from Lewis — a seller of record, not co-present (the firewood-supplier
	// pattern): the cue names the WORKPLACE, not proximity.
	josiah := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Josiah Thorne",
		Role:             "merchant",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 2000, Y: 2000}.Tile(),
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		WorkStructureID:  "general_store",
		Coins:            40,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"coat": 2, "cloak": 1},
	}
	snap := &sim.Snapshot{
		PublishedAt:      hearthClock,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		HearthLowMinutes: 60,
		Environment:      sim.WorldEnvironment{Weather: sim.WeatherStorm},
		ItemKinds:        warmGarmentGoldenCatalog(),
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"lewis": lewis, "josiah": josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			"walker_house":  plainStructure("walker_house", "Walker House"),
			"general_store": plainStructure("general_store", "General Store"),
		},
	}
	return snap, "lewis", nil
}

// keeperAtDeadHearthStorm: the tavern keeper at her post, storm running, fire
// OUT, enough firewood in hand, a chilled guest in the room — the full-dress
// "## Your hearth" cue plus the HearthLow warrant line.
func keeperAtDeadHearthStorm() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	start, end := 360, 1080
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "tavern",
		WorkStructureID:   "tavern",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             25,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{sim.FirewoodItemKind: 2},
	}
	lewis := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Lewis Walker",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 101, Y: 100}.Tile(),
		InsideStructureID: "tavern",
		Needs:             map[sim.NeedKey]int{sim.ColdNeedKey: 12}, // a chilled guest — the escalation beat
	}
	snap := &sim.Snapshot{
		PublishedAt:       hearthClock,
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		HearthLowMinutes:  60,
		StokeWoodPerStoke: 1,
		Environment:       sim.WorldEnvironment{Weather: sim.WeatherStorm},
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"hannah": hannah, "lewis": lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:           "tavern",
				DisplayName:  "Tavern",
				Pos:          sim.WorldPos{X: 100, Y: 100},
				OwnerActorID: "hannah",
				Tags:         []string{sim.TagBusiness, sim.TagHearth},
				// HearthLitUntil zero — the fire is out.
			},
		},
	}
	warrants := []sim.WarrantMeta{{
		TriggerActorID: "hannah",
		Reason:         sim.HearthLowWarrantReason{HearthID: "tavern"},
	}}
	return snap, "hannah", warrants
}

// keeperMidStokeNoRestoke: Hannah mid-stoke of her own tavern hearth (a stoke
// SourceActivity in flight), the fire still reading OUT because the extension
// lands only at completion, wood still in hand under a storm. The "## Your
// hearth" cue and the stoke tool must both be SUPPRESSED — re-advertising stoke
// to an actor already mid-stoke baits the "already busy — finish what you're
// doing before tending the fire" reject (StartStoke gate). What renders instead
// is the mid-activity coda that holds her in place to done(). LLM-435.
func keeperMidStokeNoRestoke() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	start, end := 360, 1080
	hannah := &sim.ActorSnapshot{
		Kind:                   sim.KindNPCStateful,
		DisplayName:            "Hannah Boggs",
		Role:                   "innkeeper",
		State:                  sim.StateIdle,
		Pos:                    sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID:      "tavern",
		WorkStructureID:        "tavern",
		ScheduleStartMin:       &start,
		ScheduleEndMin:         &end,
		Coins:                  25,
		Needs:                  map[sim.NeedKey]int{},
		Inventory:              map[sim.ItemKind]int{sim.FirewoodItemKind: 2},
		SourceActivityKind:     sim.SourceActivityStoke,
		SourceActivityObjectID: "tavern",
	}
	snap := &sim.Snapshot{
		PublishedAt:       hearthClock,
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		HearthLowMinutes:  60,
		StokeWoodPerStoke: 1,
		Environment:       sim.WorldEnvironment{Weather: sim.WeatherStorm},
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"hannah": hannah},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:           "tavern",
				DisplayName:  "Tavern",
				Pos:          sim.WorldPos{X: 100, Y: 100},
				OwnerActorID: "hannah",
				Tags:         []string{sim.TagBusiness, sim.TagHearth},
				// HearthLitUntil zero — fire out mid-window; the extension lands at completion.
			},
		},
	}
	return snap, "hannah", nil
}

// keeperLowHearthShortWoodWithSupplier: calm sky, embers, no wood in hand, and
// a firewood supplier of record (Ezekiel at the Blacksmith) — the
// destination-bearing buy steer, the firewood twin of the LLM-274 nail steer.
func keeperLowHearthShortWoodWithSupplier() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	start, end := 360, 1080
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "tavern",
		WorkStructureID:   "tavern",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             25,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Ezekiel Crane",
		Role:             "blacksmith",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 2000, Y: 2000}.Tile(), // far away — a supplier of record, not co-present
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		WorkStructureID:  "blacksmith",
		Coins:            0,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{sim.FirewoodItemKind: 8},
		RestockPolicy:    producePolicy(sim.FirewoodItemKind, 10),
	}
	snap := &sim.Snapshot{
		PublishedAt:       hearthClock,
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		HearthLowMinutes:  60,
		StokeWoodPerStoke: 1,
		Environment:       sim.WorldEnvironment{Weather: sim.WeatherClear},
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"hannah": hannah, "ezekiel": ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern":     plainStructure("tavern", "Tavern"),
			"blacksmith": plainStructure("blacksmith", "Blacksmith"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:             "tavern",
				DisplayName:    "Tavern",
				Pos:            sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:   "hannah",
				Tags:           []string{sim.TagBusiness, sim.TagHearth},
				HearthLitUntil: hearthClock.Add(30 * time.Minute), // embers — under the 60-min low line
			},
		},
	}
	return snap, "hannah", nil
}

// hiredWorkerAtEmployerLowHearth: a Working hire inside the employer's tavern
// during a storm, embers, wood in hand — the hired framing + the hired wake.
func hiredWorkerAtEmployerLowHearth() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	anne := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Anne Walker",
		State:             sim.StateLaboring,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "tavern",
		Coins:             4,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{sim.FirewoodItemKind: 1},
	}
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 101, Y: 100}.Tile(),
		InsideStructureID: "tavern",
		WorkStructureID:   "tavern",
		Coins:             25,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:       hearthClock,
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		HearthLowMinutes:  60,
		StokeWoodPerStoke: 1,
		Environment:       sim.WorldEnvironment{Weather: sim.WeatherStorm},
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"anne": anne, "hannah": hannah},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": plainStructure("tavern", "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {
				ID:             "tavern",
				DisplayName:    "Tavern",
				Pos:            sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:   "hannah",
				Tags:           []string{sim.TagBusiness, sim.TagHearth},
				HearthLitUntil: hearthClock.Add(30 * time.Minute), // embers
			},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "anne", EmployerID: "hannah", State: sim.LaborStateWorking},
		},
	}
	warrants := []sim.WarrantMeta{{
		TriggerActorID: "anne",
		Reason:         sim.HearthStokeHiredWarrantReason{HearthID: "tavern"},
	}}
	return snap, "anne", warrants
}

// TestGoldensColdAlwaysShowsFreeRelief is the LLM-412 cross-scenario
// invariant: whenever a rendered prompt surfaces the subject's cold (any tier
// phrase), it must also name at least one FREE relief path — a roof, the fire
// it is already standing by, or the clearing sky. A cold line with no free way
// out is an absorbing-state generator (the LLM-406 trap), so this runs over
// the whole matrix: a future exposure branch can't ship a dead end.
func TestGoldensColdAlwaysShowsFreeRelief(t *testing.T) {
	coldLeads := []string{
		"You're chilled",
		"You're cold through to your clothes",
		"You're perished with cold",
	}
	freeRelief := []string{
		"Any roof will stop the worst of it",     // outdoors: shelter, free
		"staying in is easing it",                // indoors unheated: already sheltering
		"working the chill out of you",           // by a lit fire: already warming
		"the chill is easing off you on its own", // clear sky: passing on its own
	}
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			got := renderScenario(sc)
			surfaced := false
			for _, lead := range coldLeads {
				if strings.Contains(got, lead) {
					surfaced = true
					break
				}
			}
			if !surfaced {
				return
			}
			for _, relief := range freeRelief {
				if strings.Contains(got, relief) {
					return
				}
			}
			t.Errorf("scenario %q surfaces cold but names no FREE relief path:\n%s", sc.name, got)
		})
	}
}
