package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// need_redirect_test.go — LLM-176. Unit coverage for the need-driven loop fix:
// the no-food-here dead end (consumableDeadEndHere), the redirect target
// selection (needRedirectFor / buildNeedRedirect), the rendered coda
// (renderNeedRedirect), and the dead-end clause phrasing.

// TestNeedRedirectFor pins the target-selection order for one felt need: consume
// what's carried > nearest free source > nearest usable vendor, with experiential
// affordability (a remembered price the actor can't meet skips the vendor; an
// unknown price does not) and out-of-stock vendors skipped. Remembered-shut
// vendors don't need a case here — the satiation build gate drops them upstream
// (LLM-222), so they never reach this list.
func TestNeedRedirectFor(t *testing.T) {
	free := SatiationFreeSource{Label: "Well", ObjectID: "well1"}
	cheapKnown := SatiationVendor{StructureLabel: "Tavern", StructureID: "tav", ItemLabel: "stew", CostText: "~2 coins", costCoins: 2}
	dearKnown := SatiationVendor{StructureLabel: "Inn", StructureID: "inn", ItemLabel: "pie", CostText: "~9 coins", costCoins: 9}
	unknown := SatiationVendor{StructureLabel: "Store", StructureID: "store", ItemLabel: "cheese", CostText: "ask the seller", costCoins: 0}
	oos := SatiationVendor{StructureLabel: "Cart", StructureID: "cart", ItemLabel: "apple", costCoins: 1, OutOfStock: true}

	cases := []struct {
		name     string
		nv       SatiationNeedView
		coins    int
		wantKind NeedRedirectKind
		wantID   string // TargetID for free/buy
	}{
		{"own stock beats everything", SatiationNeedView{Need: "hunger", Verb: "eat", OwnStock: []OwnStockItem{{Label: "bread"}}, FreeSources: []SatiationFreeSource{free}}, 0, NeedRedirectConsume, ""},
		{"free source over vendor", SatiationNeedView{Need: "hunger", Verb: "eat", FreeSources: []SatiationFreeSource{free}, Vendors: []SatiationVendor{cheapKnown}}, 0, NeedRedirectFree, "well1"},
		{"affordable known vendor", SatiationNeedView{Need: "hunger", Verb: "eat", Vendors: []SatiationVendor{cheapKnown}}, 5, NeedRedirectBuy, "tav"},
		{"unknown-price vendor eligible while broke", SatiationNeedView{Need: "hunger", Verb: "eat", Vendors: []SatiationVendor{unknown}}, 0, NeedRedirectBuy, "store"},
		{"skip known-unaffordable, take next eligible", SatiationNeedView{Need: "hunger", Verb: "eat", Vendors: []SatiationVendor{dearKnown, unknown}}, 1, NeedRedirectBuy, "store"},
		{"skip out-of-stock", SatiationNeedView{Need: "hunger", Verb: "eat", Vendors: []SatiationVendor{oos, cheapKnown}}, 5, NeedRedirectBuy, "tav"},
		{"nothing resolvable → nil", SatiationNeedView{Need: "hunger", Verb: "eat", Vendors: []SatiationVendor{dearKnown}}, 1, "", ""},
	}
	for _, c := range cases {
		got := needRedirectFor(c.nv, c.coins)
		if c.wantKind == "" {
			if got != nil {
				t.Errorf("%s: want nil, got %+v", c.name, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: want kind %s, got nil", c.name, c.wantKind)
			continue
		}
		if got.Kind != c.wantKind {
			t.Errorf("%s: kind = %s, want %s", c.name, got.Kind, c.wantKind)
		}
		if c.wantKind != NeedRedirectConsume && got.TargetID != c.wantID {
			t.Errorf("%s: targetID = %q, want %q", c.name, got.TargetID, c.wantID)
		}
	}
}

// TestBuildNeedRedirect_Gating: the redirect is built only for a looping actor
// with a non-nil satiation view, and resolves the listed free source.
func TestBuildNeedRedirect_Gating(t *testing.T) {
	snap := &sim.Snapshot{NeedThresholds: sim.NeedThresholds{}}
	sat := &SatiationView{Needs: []SatiationNeedView{{
		Need: "hunger", Verb: "eat", Level: sim.DefaultHungerRedThreshold,
		FreeSources: []SatiationFreeSource{{Label: "Well", ObjectID: "well1"}},
	}}}

	if got := buildNeedRedirect(snap, &sim.ActorSnapshot{ConversationLooping: false}, sat); got != nil {
		t.Errorf("non-looping actor: want nil, got %+v", got)
	}
	if got := buildNeedRedirect(snap, &sim.ActorSnapshot{ConversationLooping: true}, nil); got != nil {
		t.Errorf("nil satiation: want nil, got %+v", got)
	}
	got := buildNeedRedirect(snap, &sim.ActorSnapshot{ConversationLooping: true}, sat)
	if got == nil || got.Kind != NeedRedirectFree || got.TargetID != "well1" {
		t.Errorf("looping actor: want free redirect to well1, got %+v", got)
	}
}

// TestRenderNeedRedirect pins the three rendered imperatives, including the inline
// structure_id move_to handle and the need-agnostic verb.
func TestRenderNeedRedirect(t *testing.T) {
	cases := []struct {
		name string
		v    NeedRedirectView
		want []string
	}{
		{"consume", NeedRedirectView{Kind: NeedRedirectConsume, Verb: "eat", ItemLabel: "bread"},
			[]string{"you already carry bread", "consume it now to eat"}},
		{"free", NeedRedirectView{Kind: NeedRedirectFree, Verb: "eat", TargetLabel: "Raspberry Bush", TargetID: "wild_bush"},
			[]string{"nothing to eat here", "go to Raspberry Bush (structure_id: wild_bush) now and eat"}},
		{"buy", NeedRedirectView{Kind: NeedRedirectBuy, Verb: "drink", ItemLabel: "a tankard of ale", TargetLabel: "Blacksmith", TargetID: "smith"},
			[]string{"nothing to drink here", "go to Blacksmith (structure_id: smith) now and buy a tankard of ale to drink"}},
	}
	for _, c := range cases {
		got := renderNeedRedirect(c.v)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: render = %q, missing %q", c.name, got, w)
			}
		}
	}
}

// TestDeadEndClause_NoConsumableHere pins the LLM-176 phrasing keyed to which felt
// need has no source here.
func TestDeadEndClause_NoConsumableHere(t *testing.T) {
	cases := []struct {
		name string
		view SurroundingsView
		want string
	}{
		{"hunger only", SurroundingsView{LocationDeadEnd: DeadEndNoConsumableHere, DeadEndHunger: true}, "There's no food to be had here — you'll need to forage or buy a meal elsewhere."},
		{"thirst only", SurroundingsView{LocationDeadEnd: DeadEndNoConsumableHere, DeadEndThirst: true}, "There's nothing to drink here — you'll need to find a well or buy a drink elsewhere."},
		{"both", SurroundingsView{LocationDeadEnd: DeadEndNoConsumableHere, DeadEndHunger: true, DeadEndThirst: true}, "There's nothing to eat or drink here — you'll need to forage or buy it elsewhere."},
	}
	for _, c := range cases {
		if got := deadEndClause(c.view); got != c.want {
			t.Errorf("%s: deadEndClause = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestConsumableDeadEndHere pins the gate: a felt consumable need with nothing
// held and no source at THIS structure is a dead end — but only while inside a
// structure, only while felt, and never when the actor carries a satisfier or a
// source is co-located.
func TestConsumableDeadEndHere(t *testing.T) {
	const (
		actorID = sim.ActorID("a")
		homeID  = sim.StructureID("home")
	)
	zero := 0
	hungry := map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold}
	rasp := map[sim.ItemKind]*sim.ItemKindDef{"raspberries": {
		Name: "raspberries", DisplayLabel: "Raspberries", Category: sim.ItemCategoryFood,
		Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 2}},
	}}
	bushAt := func(x, y float64) map[sim.VillageObjectID]*sim.VillageObject {
		return map[sim.VillageObjectID]*sim.VillageObject{"bush": {
			ID: "bush", DisplayName: "Raspberry Bush", Pos: sim.WorldPos{X: x, Y: y},
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -2}},
		}}
	}
	mkActor := func(inside sim.StructureID, needs map[sim.NeedKey]int, inv map[sim.ItemKind]int) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{InsideStructureID: inside, Pos: sim.WorldPos{X: 10, Y: 10}.Tile(), Needs: needs, Inventory: inv}
	}
	mkSnap := func(objs map[sim.VillageObjectID]*sim.VillageObject, a *sim.ActorSnapshot) *sim.Snapshot {
		return &sim.Snapshot{
			NeedThresholds: sim.NeedThresholds{}, ItemKinds: rasp,
			Actors:         map[sim.ActorID]*sim.ActorSnapshot{actorID: a},
			Structures:     map[sim.StructureID]*sim.Structure{homeID: plainStructure(homeID, "Home")},
			VillageObjects: objs,
		}
	}

	cases := []struct {
		name       string
		snap       *sim.Snapshot
		wantHunger bool
	}{
		{"inside, hungry, nothing held, bush far → dead end", mkSnap(bushAt(400, 400), mkActor(homeID, hungry, nil)), true},
		{"outdoors → no dead end (not in a structure)", mkSnap(bushAt(400, 400), mkActor("", hungry, nil)), false},
		{"not hungry → no dead end", mkSnap(bushAt(400, 400), mkActor(homeID, map[sim.NeedKey]int{}, nil)), false},
		{"carries a satisfier → no dead end", mkSnap(bushAt(400, 400), mkActor(homeID, hungry, map[sim.ItemKind]int{"raspberries": 1})), false},
		{"bush co-located → no dead end", mkSnap(bushAt(10, 10), mkActor(homeID, hungry, nil)), false},
	}
	for _, c := range cases {
		gotHunger, _ := consumableDeadEndHere(c.snap, actorID, c.snap.Actors[actorID])
		if gotHunger != c.wantHunger {
			t.Errorf("%s: hunger dead end = %v, want %v", c.name, gotHunger, c.wantHunger)
		}
	}
}

// TestNeedRedirectNamesConcreteTarget is the LLM-176 cross-scenario invariant:
// whenever a scenario builds a NeedRedirect, the actor is in a conversational loop
// AND the rendered coda names the concrete action — a move_to handle (structure_id)
// for a free/buy target, or a consume imperative for carried stock. So a need-
// driven loop is always pointed at a real affordance, never the generic line.
func TestNeedRedirectNamesConcreteTarget(t *testing.T) {
	var saw bool
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		if p.NeedRedirect == nil {
			continue
		}
		saw = true
		if actor := snap.Actors[actorID]; actor == nil || !actor.ConversationLooping {
			t.Errorf("scenario %q: NeedRedirect built but actor is not looping", sc.name)
		}
		coda := renderScenario(sc)
		switch p.NeedRedirect.Kind {
		case NeedRedirectConsume:
			if !strings.Contains(coda, "consume it now") {
				t.Errorf("scenario %q: consume redirect missing the consume imperative:\n%s", sc.name, coda)
			}
		default:
			if !strings.Contains(coda, "(structure_id: "+p.NeedRedirect.TargetID+")") {
				t.Errorf("scenario %q: move redirect missing structure_id %q:\n%s", sc.name, p.NeedRedirect.TargetID, coda)
			}
			if !strings.Contains(coda, "go to "+p.NeedRedirect.TargetLabel) {
				t.Errorf("scenario %q: move redirect missing target label %q:\n%s", sc.name, p.NeedRedirect.TargetLabel, coda)
			}
		}
	}
	if !saw {
		t.Error("no scenario exercised the need-redirect coda — add one (hungry_looper_at_foodless_home)")
	}
}
