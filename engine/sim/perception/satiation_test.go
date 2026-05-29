package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// satiation_test.go — ZBBS-HOME-304. Covers the firing gate (per-need red
// threshold), the consume-first own-stock line, vendor seller cues via the
// shared finder, tiredness-isolation (tiredness items don't leak into the
// eat/drink section), and the render shape.

// foodDrinkCatalog: bread/stew ease hunger, water/ale ease thirst, coca_tea
// eases tiredness (the isolation control — must NOT appear in satiation).
func foodDrinkCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"bread":    {Name: "bread", DisplayLabel: "bread", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 6}}},
		"stew":     {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 12}}},
		"water":    {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink, Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 5}}},
		"coca_tea": {Name: "coca_tea", DisplayLabel: "coca tea", Category: sim.ItemCategoryDrink, Satisfies: []sim.ItemSatisfaction{{Attribute: "tiredness", Immediate: 12}}},
	}
}

func TestBuildSatiation_NotPressing_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": 1, "thirst": 1},
		Inventory: map[sim.ItemKind]int{"bread": 3},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	if v := buildSatiation(snap, "ezekiel", subj); v != nil {
		t.Errorf("want nil when no consumable need is pressing, got %+v", v)
	}
}

func TestBuildSatiation_OwnStockHunger(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Inventory: map[sim.ItemKind]int{"bread": 3, "stew": 1, "coca_tea": 5}, // coca_tea is tiredness — must not appear
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need (hunger), got %+v", v)
	}
	n := v.Needs[0]
	if n.Need != "hunger" || n.Verb != "eat" {
		t.Errorf("need/verb = %q/%q, want hunger/eat", n.Need, n.Verb)
	}
	if len(n.OwnStock) != 2 {
		t.Fatalf("want 2 own-stock satisfiers (bread, stew; coca tea excluded), got %+v", n.OwnStock)
	}
	// Strongest first: stew (12) before bread (6).
	if n.OwnStock[0].Label != "stew" || n.OwnStock[0].Magnitude != 12 || n.OwnStock[1].Label != "bread" {
		t.Errorf("own-stock order wrong (want stew then bread): %+v", n.OwnStock)
	}
}

// Two own-stock item kinds with the SAME display label and SAME magnitude must
// order deterministically via the ItemKind tie-break, since Inventory is a map.
// (code_review)
func TestBuildSatiation_OwnStockDeterministicTieBreak(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Inventory: map[sim.ItemKind]int{"ration_a": 2, "ration_b": 2},
	}
	// Both kinds: same label "ration", same hunger magnitude 5 — only ItemKind differs.
	cat := map[sim.ItemKind]*sim.ItemKindDef{
		"ration_a": {Name: "ration_a", DisplayLabel: "ration", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 5}}},
		"ration_b": {Name: "ration_b", DisplayLabel: "ration", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 5}}},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: cat,
	}
	var first []sim.ItemKind
	for i := 0; i < 25; i++ {
		v := buildSatiation(snap, "ezekiel", subj)
		if v == nil || len(v.Needs) != 1 || len(v.Needs[0].OwnStock) != 2 {
			t.Fatalf("want 2 own-stock items, got %+v", v)
		}
		got := []sim.ItemKind{v.Needs[0].OwnStock[0].kind, v.Needs[0].OwnStock[1].kind}
		if first == nil {
			first = got
			continue
		}
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("nondeterministic own-stock order: first=%v now=%v", first, got)
		}
	}
	// ItemKind ascending: ration_a before ration_b.
	if first[0] != "ration_a" || first[1] != "ration_b" {
		t.Errorf("tie-break order = %v, want [ration_a ration_b]", first)
	}
}

func TestBuildSatiation_VendorCueThirst(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold}}
	vendor := &sim.ActorSnapshot{WorkStructureID: "well_house", Inventory: map[sim.ItemKind]int{"water": 9}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wally": vendor},
		Structures: map[sim.StructureID]*sim.Structure{"well_house": {ID: "well_house", DisplayName: "Well House"}},
		ItemKinds:  foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need (thirst), got %+v", v)
	}
	n := v.Needs[0]
	if n.Need != "thirst" || n.Verb != "drink" {
		t.Errorf("need/verb = %q/%q, want thirst/drink", n.Need, n.Verb)
	}
	if len(n.OwnStock) != 0 {
		t.Errorf("want no own-stock (actor carries nothing), got %+v", n.OwnStock)
	}
	if len(n.Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", n.Vendors)
	}
	vd := n.Vendors[0]
	if vd.StructureLabel != "Well House" || vd.ItemLabel != "water" || vd.Magnitude != 5 || vd.CostText != "ask the seller" {
		t.Errorf("vendor cue wrong (no price history → ask the seller): %+v", vd)
	}
	if vd.StructureID != "well_house" {
		t.Errorf("vendor StructureID = %q, want 'well_house' (the move_to target)", vd.StructureID)
	}
}

func TestBuildSatiation_VendorPriceFromPriceBook(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold}}
	vendor := &sim.ActorSnapshot{WorkStructureID: "well_house", Inventory: map[sim.ItemKind]int{"water": 9}}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "ezekiel", Amount: 1, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wally": vendor},
		Structures: map[sim.StructureID]*sim.Structure{"well_house": {ID: "well_house", DisplayName: "Well House"}},
		ItemKinds:  foodDrinkCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "wally", Item: "water"}: pb,
		},
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 || len(v.Needs[0].Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", v)
	}
	if got := v.Needs[0].Vendors[0].CostText; got != "~1 coins" {
		t.Errorf("CostText = %q, want '~1 coins' (last-paid)", got)
	}
}

func TestBuildSatiation_BothNeeds_HungerFirst(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:     map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold, "thirst": sim.DefaultThirstRedThreshold},
		Inventory: map[sim.ItemKind]int{"bread": 2, "water": 2},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 2 {
		t.Fatalf("want 2 pressing needs, got %+v", v)
	}
	if v.Needs[0].Need != "hunger" || v.Needs[1].Need != "thirst" {
		t.Errorf("need order = %q,%q; want hunger,thirst", v.Needs[0].Need, v.Needs[1].Need)
	}
}

// huddleWith wires a subject + peers into a single huddle and returns the snapshot
// pieces a co-present-peer test needs: the subject's CurrentHuddleID is stamped
// and the Huddle.Members set lists everyone (subject self-excluded by the gather).
func huddleWith(members ...sim.ActorID) (sim.HuddleID, *sim.Huddle) {
	m := make(map[sim.ActorID]struct{}, len(members))
	for _, id := range members {
		m[id] = struct{}{}
	}
	return "huddle-1", &sim.Huddle{ID: "huddle-1", Members: m}
}

// TestBuildSatiation_CoPresentPeer_Acquainted: a huddle peer carrying a satisfier
// for a pressing need surfaces as a co-present offer with the felt amount and the
// peer's name (acquainted), and NO structure_id appears on the rendered line.
func TestBuildSatiation_CoPresentPeer_Acquainted(t *testing.T) {
	hid, h := huddleWith("ezekiel", "hannah")
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		CurrentHuddleID: hid,
		Acquaintances:   map[string]sim.Acquaintance{"Hannah": {}},
	}
	peer := &sim.ActorSnapshot{
		DisplayName: "Hannah", Role: "baker",
		Inventory: map[sim.ItemKind]int{"stew": 2, "coca_tea": 1}, // coca_tea is tiredness — must not appear
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "hannah": peer},
		Huddles:   map[sim.HuddleID]*sim.Huddle{hid: h},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need (hunger), got %+v", v)
	}
	n := v.Needs[0]
	if len(n.CoPresentPeers) != 1 {
		t.Fatalf("want 1 co-present peer offer (stew; coca tea excluded), got %+v", n.CoPresentPeers)
	}
	pr := n.CoPresentPeers[0]
	if pr.PeerLabel != "Hannah" {
		t.Errorf("peer label = %q, want acquaintance name 'Hannah'", pr.PeerLabel)
	}
	if pr.ItemLabel != "stew" || pr.Magnitude != 12 {
		t.Errorf("peer offer item/mag = %q/%d, want stew/12", pr.ItemLabel, pr.Magnitude)
	}

	var b strings.Builder
	renderSatiation(&b, v)
	out := b.String()
	want := "Hannah is here with you, carrying stew (a hearty meal) — you could offer to buy it from them now with pay_with_item. No need to walk anywhere."
	if !strings.Contains(out, want) {
		t.Errorf("co-present line missing/!exact:\nwant: %s\ngot:\n%s", want, out)
	}
	if strings.Contains(out, "structure_id") {
		t.Errorf("co-present peer line must carry NO structure_id, got:\n%s", out)
	}
}

// TestBuildSatiation_CoPresentPeer_Unacquainted: an unacquainted peer is named by
// the acquaintance-gated descriptor ("the <role>"), never their DisplayName.
func TestBuildSatiation_CoPresentPeer_Unacquainted(t *testing.T) {
	hid, h := huddleWith("ezekiel", "stranger")
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold},
		CurrentHuddleID: hid,
		// No Acquaintances — subject does not know the peer.
	}
	peer := &sim.ActorSnapshot{
		DisplayName: "Goodwife Mercy", Role: "herbalist",
		Inventory: map[sim.ItemKind]int{"water": 4},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "stranger": peer},
		Huddles:   map[sim.HuddleID]*sim.Huddle{hid: h},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 || len(v.Needs[0].CoPresentPeers) != 1 {
		t.Fatalf("want 1 co-present peer offer, got %+v", v)
	}
	pr := v.Needs[0].CoPresentPeers[0]
	if pr.PeerLabel != "the herbalist" {
		t.Errorf("unacquainted peer label = %q, want descriptor 'the herbalist'", pr.PeerLabel)
	}
	if strings.Contains(pr.PeerLabel, "Mercy") {
		t.Errorf("unacquainted peer must NOT be named by DisplayName, got %q", pr.PeerLabel)
	}
}

// TestBuildSatiation_CoPresentPeer_NoOfferWhenNoSatisfierOrNotPressing: a peer
// carrying only a non-satisfier yields no co-present offer; and the whole
// co-present scan is gated by the SAME pressing-need threshold as the rest of the
// section (a peer carrying a satisfier for a NON-pressing need surfaces nothing).
func TestBuildSatiation_CoPresentPeer_NoOfferWhenNoSatisfierOrNotPressing(t *testing.T) {
	// (a) Peer carries no satisfier for the pressing need → no offer (but the
	// need still presses via own-stock so the section can exist).
	hid, h := huddleWith("ezekiel", "hannah")
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		CurrentHuddleID: hid,
		Inventory:       map[sim.ItemKind]int{"bread": 1},
		Acquaintances:   map[string]sim.Acquaintance{"Hannah": {}},
	}
	peer := &sim.ActorSnapshot{DisplayName: "Hannah", Inventory: map[sim.ItemKind]int{"coca_tea": 3}} // tiredness only
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "hannah": peer},
		Huddles:   map[sim.HuddleID]*sim.Huddle{hid: h},
		ItemKinds: foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want hunger section (own stock), got %+v", v)
	}
	if len(v.Needs[0].CoPresentPeers) != 0 {
		t.Errorf("peer carries no hunger satisfier → want no co-present offer, got %+v", v.Needs[0].CoPresentPeers)
	}

	// (b) Need not pressing → whole section nil even though the peer carries a
	// satisfier.
	subj2 := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"hunger": 1},
		CurrentHuddleID: hid,
		Acquaintances:   map[string]sim.Acquaintance{"Hannah": {}},
	}
	peer2 := &sim.ActorSnapshot{DisplayName: "Hannah", Inventory: map[sim.ItemKind]int{"stew": 5}}
	snap2 := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj2, "hannah": peer2},
		Huddles:   map[sim.HuddleID]*sim.Huddle{hid: h},
		ItemKinds: foodDrinkCatalog(),
	}
	if v := buildSatiation(snap2, "ezekiel", subj2); v != nil {
		t.Errorf("hunger not pressing → want nil section, got %+v", v)
	}
}

// TestBuildSatiation_PeerAlsoVendor_BothAffordances: a peer who is ALSO a
// structural vendor (huddle peer + WorkStructureID + stock) surfaces in BOTH the
// co-present list AND the walk-to vendor list — they're different affordances —
// and the existing vendor cue is byte-for-byte unchanged by the new peer scan.
func TestBuildSatiation_PeerAlsoVendor_BothAffordances(t *testing.T) {
	hid, h := huddleWith("ezekiel", "wally")
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold},
		CurrentHuddleID: hid,
		Acquaintances:   map[string]sim.Acquaintance{"Wally": {}},
	}
	// Wally is co-present in the huddle AND stationed at a resolvable workplace
	// holding water — both a peer and a structural vendor.
	wally := &sim.ActorSnapshot{
		DisplayName: "Wally", WorkStructureID: "well_house",
		Inventory: map[sim.ItemKind]int{"water": 9},
	}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wally": wally},
		Huddles:    map[sim.HuddleID]*sim.Huddle{hid: h},
		Structures: map[sim.StructureID]*sim.Structure{"well_house": {ID: "well_house", DisplayName: "Well House"}},
		ItemKinds:  foodDrinkCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need (thirst), got %+v", v)
	}
	n := v.Needs[0]
	if len(n.CoPresentPeers) != 1 || n.CoPresentPeers[0].PeerLabel != "Wally" || n.CoPresentPeers[0].ItemLabel != "water" {
		t.Errorf("want co-present offer from Wally for water, got %+v", n.CoPresentPeers)
	}
	// The existing workplace-vendor cue is UNCHANGED — same assertions as
	// TestBuildSatiation_VendorCueThirst.
	if len(n.Vendors) != 1 {
		t.Fatalf("want the existing vendor cue intact (1), got %+v", n.Vendors)
	}
	vd := n.Vendors[0]
	if vd.StructureLabel != "Well House" || vd.ItemLabel != "water" || vd.Magnitude != 5 ||
		vd.CostText != "ask the seller" || vd.StructureID != "well_house" {
		t.Errorf("existing vendor cue changed by the peer scan: %+v", vd)
	}
}

func TestRenderSatiation_NilAndEmpty(t *testing.T) {
	var b strings.Builder
	renderSatiation(&b, nil)
	renderSatiation(&b, &SatiationView{})
	if b.String() != "" {
		t.Errorf("nil/empty view should render nothing, got %q", b.String())
	}
}

func TestRenderSatiation_Bullets(t *testing.T) {
	var b strings.Builder
	renderSatiation(&b, &SatiationView{Needs: []SatiationNeedView{
		{
			// Magnitudes match the live item catalog (item_satisfies) so the
			// expected felt phrases stay consistent with itemFeltAmount's
			// documented bands: cheese 8 → "a good meal", bread 4 → "a small
			// bite", meat 10 → "a hearty meal".
			Need: "hunger", Verb: "eat",
			OwnStock: []OwnStockItem{{Label: "cheese", Magnitude: 8}, {Label: "bread", Magnitude: 4}},
			Vendors:  []SatiationVendor{{StructureLabel: "The Tavern", ItemLabel: "meat", Magnitude: 10, CostText: "~2 coins"}},
		},
	}})
	out := b.String()
	if !strings.Contains(out, "## What you can eat or drink") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "You have cheese (a good meal), bread (a small bite) on hand — consume to eat.") {
		t.Errorf("own-stock line wrong: %q", out)
	}
	if !strings.Contains(out, "The Tavern — buy meat (a hearty meal), ~2 coins") {
		t.Errorf("vendor bullet wrong: %q", out)
	}
}

// TestRenderSatiation_StructureIDRendered pins the move_to contract for the
// eat/drink vendor bullets: a vendor whose workplace resolved renders a
// trailing (structure_id: …) the buyer passes straight to move_to (the tool
// rejects a bare name). An empty StructureID renders no suffix — and is only
// reachable via a malformed/manually-built view, since gatherSatiationVendors
// drops unactionable (no-workplace) vendors at build. Regression guard for the
// perception gap that starved NPCs by naming shops they could never walk to.
func TestRenderSatiation_StructureIDRendered(t *testing.T) {
	var b strings.Builder
	renderSatiation(&b, &SatiationView{Needs: []SatiationNeedView{
		{
			Need: "hunger", Verb: "eat",
			Vendors: []SatiationVendor{
				{StructureLabel: "The Tavern", StructureID: "tavern", ItemLabel: "ale", Magnitude: 4, CostText: "~2 coins"},
				{StructureLabel: "Roadside Stall", ItemLabel: "apple", Magnitude: 3, CostText: "ask the seller"},
			},
		},
	}})
	out := b.String()
	hasLine := func(want string) bool {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == want {
				return true
			}
		}
		return false
	}
	if !hasLine("- The Tavern — buy ale (a small bite), ~2 coins (structure_id: tavern)") {
		t.Errorf("vendor bullet missing/!exact structure_id in:\n%s", out)
	}
	// A vendor whose workplace didn't resolve carries no id — no dangling suffix
	// (this empty-id row only reaches render via a manual view; build filters it).
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- Roadside Stall") && strings.Contains(line, "structure_id") {
			t.Errorf("vendor with empty StructureID must not render a structure_id: %q", line)
		}
	}
}
