package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// work_clothes_test.go — LLM-589. The working-clothes cue's gates, in the order
// they can go wrong: who hears it, when it is silent, and which tier it grades.

// workClothesCatalog is the garment slice of the catalog: two working garments
// (no `warms`) and one outerwear kind that must stay with the cold self-line.
func workClothesCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"linens":  {Name: "linens", DisplayLabel: "Linens", DisplayLabelSingular: "set of linens", DisplayLabelPlural: "sets of linens", WearMinutes: 10800},
		"woolens": {Name: "woolens", DisplayLabel: "Woolens", DisplayLabelSingular: "set of woolens", DisplayLabelPlural: "sets of woolens", WearMinutes: 14400},
		"coat":    {Name: "coat", DisplayLabel: "coats", WearMinutes: 18000, Capabilities: []string{string(sim.CapabilityWarms)}},
		"nail":    {Name: "nail", DisplayLabel: "nails"},
	}
}

// workClothesSnapshot builds a worker with the given inventory/wear alongside a
// cloth seller stationed far away (a supplier of record, never co-present).
func workClothesSnapshot(inv, wear map[sim.ItemKind]int, state sim.ActorState) (*sim.Snapshot, sim.ActorID) {
	const actorID = sim.ActorID("worker")
	start, end := 360, 1080
	now := 600
	worker := &sim.ActorSnapshot{
		Kind:             sim.KindNPCShared,
		DisplayName:      "Silence Walker",
		State:            state,
		Pos:              sim.WorldPos{X: 100, Y: 100}.Tile(),
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            40,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        inv,
		GarmentWear:      wear,
	}
	seller := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Josiah Thorne",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 4000, Y: 4000}.Tile(),
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		WorkStructureID:  "general_store",
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"linens": 3, "woolens": 2},
		RestockPolicy:    producePolicy("linens", 20),
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:              &now,
		NeedThresholds:                sim.NeedThresholds{},
		Assets:                        emptyAssetSet,
		GarmentThreadbareFractionX100: 20,
		ItemKinds:                     workClothesCatalog(),
		Recipes:                       map[sim.ItemKind]*sim.ItemRecipe{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			actorID:  worker,
			"josiah": seller,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"general_store": {ID: "general_store", DisplayName: "General Store"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{},
	}
	return snap, actorID
}

func TestWorkClothes_SoundGarmentIsSilent(t *testing.T) {
	// A fresh shift (no wear entry) is sound, and silence is a valid volume —
	// the cue must not narrate a non-problem on every tick of every worker.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{"linens": 1}, nil, sim.StateWorking)
	if v := buildWorkClothes(snap, id, snap.Actors[id]); v != nil {
		t.Fatalf("sound garment should render nothing, got %+v", v)
	}
}

func TestWorkClothes_NothingFitToWorkIn(t *testing.T) {
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, sim.StateWorking)
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil {
		t.Fatal("a worker owning no working garment should get the cue")
	}
	if v.Tier != sim.WarmGarmentNone {
		t.Fatalf("tier = %v, want none", v.Tier)
	}
	// breeches sorts first but is SKIPPED: the seller holds them without producing
	// them and this fixture leaves his shop untagged, so isRestockSupplierOf refuses
	// him as a supplier of record for that kind (LLM-252). The cue walks on to shift,
	// which he does produce — the never-send-them-to-a-dead-end behaviour.
	if v.Item != "linens" {
		t.Fatalf("item = %q, want the first working kind with a RESOLVABLE supplier (shift)", v.Item)
	}
	var b strings.Builder
	renderWorkClothes(&b, v)
	out := b.String()
	// The register: a want of WORKING clothes, never a claim of being unclothed —
	// every villager is obviously dressed and the other reading is a retcon.
	if !strings.Contains(out, "nothing fit to work in") {
		t.Fatalf("missing the none-tier scene:\n%s", out)
	}
	if strings.Contains(out, "General Store") == false {
		t.Fatalf("expected the supplier named as a walk-to destination:\n%s", out)
	}
}

func TestWorkClothes_ThreadbareTier(t *testing.T) {
	// One shift, worn into the last 20% of its 10800-minute budget.
	snap, id := workClothesSnapshot(
		map[sim.ItemKind]int{"linens": 1},
		map[sim.ItemKind]int{"linens": 500},
		sim.StateWorking,
	)
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil || v.Tier != sim.WarmGarmentThreadbare {
		t.Fatalf("expected the threadbare tier, got %+v", v)
	}
	var b strings.Builder
	renderWorkClothes(&b, v)
	if out := b.String(); !strings.Contains(out, "worn thin at the elbows") {
		t.Fatalf("missing the threadbare scene:\n%s", out)
	}
}

func TestWorkClothes_SpareUnitReadsSound(t *testing.T) {
	// qty >= 2 means a fresh spare is on the shelf — only the in-use unit wears,
	// so a worn one with a spare behind it is not a want. Mirrors the warms
	// resolver exactly, which is the point of sharing the tier type.
	snap, id := workClothesSnapshot(
		map[sim.ItemKind]int{"linens": 2},
		map[sim.ItemKind]int{"linens": 10},
		sim.StateWorking,
	)
	if v := buildWorkClothes(snap, id, snap.Actors[id]); v != nil {
		t.Fatalf("a fresh spare should read sound, got %+v", v)
	}
}

func TestWorkClothes_OuterwearDoesNotSatisfyIt(t *testing.T) {
	// A coat carries `warms`, so it belongs to the cold self-line and cannot
	// stand in for a working garment. Without this the two cues would silently
	// cover for each other and a worker in a coat and rags would hear nothing.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{"coat": 1}, nil, sim.StateWorking)
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil || v.Tier != sim.WarmGarmentNone {
		t.Fatalf("a coat must not satisfy the working-garment want, got %+v", v)
	}
}

func TestWorkClothes_OnlyForWorkers(t *testing.T) {
	// The audience must match sim.actorWearsGarments — the set whose garments the
	// wear sweep actually touches. Nagging an idle actor about a garment that is
	// not wearing down is noise, and you notice a worn sleeve at the work.
	for _, state := range []sim.ActorState{sim.StateIdle, sim.StateSleeping} {
		snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, state)
		if v := buildWorkClothes(snap, id, snap.Actors[id]); v != nil {
			t.Fatalf("state %v should get no cue, got %+v", state, v)
		}
	}
	// Laboring for someone else still wears clothes out, so it still hears it.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, sim.StateLaboring)
	if v := buildWorkClothes(snap, id, snap.Actors[id]); v == nil {
		t.Fatal("a laboring worker should get the cue")
	}
}

func TestWorkClothes_SilentWithNoSupplier(t *testing.T) {
	// Clothing is import-only (LLM-410), so "nobody has cloth" is an ordinary
	// state rather than a resolution failure. A standing line naming a problem
	// with no action attached would ride every tick of every worker in the
	// village — exactly the noise the scenes-not-stats register exists to avoid.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, sim.StateWorking)
	snap.Actors["josiah"].Inventory = map[sim.ItemKind]int{}
	if v := buildWorkClothes(snap, id, snap.Actors[id]); v != nil {
		t.Fatalf("no supplier anywhere should be silent, got %+v", v)
	}
}

func TestWorkClothes_DistributorResolvesAsTheClothingSupplier(t *testing.T) {
	// The production path, and the one that decides whether this cue does anything
	// at all: clothing is import-only (LLM-410), so NOBODY produces it and every
	// garment seller is a reseller. isRestockSupplierOf admits the distributor for
	// any kind he holds, which is the only reason the cue can name a destination.
	// If that arm ever narrows to producers, this cue goes silent village-wide.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, sim.StateWorking)
	snap.Actors["josiah"].RestockPolicy = nil // a pure reseller, as in production
	snap.VillageObjects["general_store"] = &sim.VillageObject{
		ID:          "general_store",
		DisplayName: "General Store",
		Tags:        []string{sim.TagDistributor},
	}
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil || len(v.Vendors) == 0 {
		t.Fatalf("the distributor must resolve as a clothing supplier, got %+v", v)
	}
	// The subject owns nothing, so like-for-like has nothing to match and the
	// fallback takes the first working kind with a resolvable supplier. That is
	// linens under the LLM-596 names (it sorts before woolens, where the old
	// breeches sorted before shift) — the assertion is about the fallback and the
	// sort being deterministic, not about which noun wins.
	if v.Item != "linens" {
		t.Fatalf("item = %q, want linens (sorts first, and the distributor stocks it)", v.Item)
	}
}

func TestWorkClothes_DistributorIsNotToldToBuyHisOwnStock(t *testing.T) {
	// The distributor's garments are sale stock, not clothing — sim.actorWearsGarments
	// excludes him from the wear sweep for that reason, and the cue has to make the
	// same exclusion or he is steered to buy the shifts already on his shelf.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, sim.StateWorking)
	snap.Actors[id].WorkStructureID = "general_store"
	snap.VillageObjects["general_store"] = &sim.VillageObject{
		ID:          "general_store",
		DisplayName: "General Store",
		Tags:        []string{sim.TagDistributor},
	}
	if v := buildWorkClothes(snap, id, snap.Actors[id]); v != nil {
		t.Fatalf("the distributor should get no clothing cue, got %+v", v)
	}
}

func TestWorkClothes_PrefersTheKindHeAlreadyWears(t *testing.T) {
	// The live case the LLM-592 seed created: the distributor stocks gowns and
	// shifts and no breeches, so the plain first-purchasable pick lands on a GOWN
	// for every threadbare worker in the village — including Constable Gideon
	// Marsh, who is threadbare in shift and breeches. The engine models no sex and
	// the catalog does, so the cue reads the WARDROBE instead: he owns a shift, the
	// shop sells shifts, the cue says shift.
	snap, id := workClothesSnapshot(
		map[sim.ItemKind]int{"linens": 1, "woolens": 1},
		map[sim.ItemKind]int{"linens": 500, "woolens": 600},
		sim.StateWorking,
	)
	// The seller holds shifts but no breeches — breeches is unpurchasable, which is
	// what makes the two passes distinguishable.
	snap.Actors["josiah"].Inventory = map[sim.ItemKind]int{"linens": 3}
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil {
		t.Fatal("a worker threadbare in every working garment should get the cue")
	}
	if v.Item != "linens" {
		t.Fatalf("item = %q, want shift — the kind he already wears and the shop actually sells", v.Item)
	}
}

func TestWorkClothes_OwnedKindLosesToWhatIsActuallyForSale(t *testing.T) {
	// Like-for-like is a PREFERENCE, not a constraint. A wearer whose own kind has
	// no supplier must still be steered to something he can buy — being sent
	// nowhere because the shop is out of his usual is worse than a different
	// garment that fits the need.
	snap, id := workClothesSnapshot(
		map[sim.ItemKind]int{"woolens": 1},
		map[sim.ItemKind]int{"woolens": 600},
		sim.StateWorking,
	)
	snap.Actors["josiah"].Inventory = map[sim.ItemKind]int{"linens": 3} // no breeches to be had
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil || v.Item != "linens" {
		t.Fatalf("expected the fallback to the purchasable kind, got %+v", v)
	}
}

func TestWorkClothes_NoneTierFallsBackToWhateverIsSold(t *testing.T) {
	// The none tier owns no working garment at all, so like-for-like has nothing to
	// match and the fallback is the ONLY path. Anything fit to work in beats
	// nothing, so the cue takes what the shop has.
	snap, id := workClothesSnapshot(map[sim.ItemKind]int{}, nil, sim.StateWorking)
	snap.Actors["josiah"].Inventory = map[sim.ItemKind]int{"linens": 3}
	v := buildWorkClothes(snap, id, snap.Actors[id])
	if v == nil || v.Tier != sim.WarmGarmentNone || v.Item != "linens" {
		t.Fatalf("expected the none tier steered to the only purchasable kind, got %+v", v)
	}
}
