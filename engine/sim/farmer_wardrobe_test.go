package sim

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	testBody   = FarmerSheetRoot + "01body/fbas_01body_human_00.png"
	testShirt  = FarmerSheetRoot + "05shrt/fbas_05shrt_longshirt_00a.png"
	testCurved = FarmerSheetRoot + "05shrt/fbas_05shrt_longshirtboobs_00a.png"
	testPants  = FarmerSheetRoot + "04lwr1/fbas_04lwr1_longpants_00a.png"
	testSkirt  = FarmerSheetRoot + "08lwr3/fbas_08lwr3_longskirt_00a.png"
	testBoots  = FarmerSheetRoot + "03fot1/fbas_03fot1_boots_00a.png"
	testCuffed = FarmerSheetRoot + "07fot2/fbas_07fot2_cuffedboots_00a.png"
	testCloakB = FarmerSheetRoot + "00undr/fbas_00undr_cloakplain_00d.png"
	testCloakF = FarmerSheetRoot + "11neck/fbas_11neck_cloakplain_00d.png"
	testHat    = FarmerSheetRoot + "14head/fbas_14head_strawhat_00d.png"
)

func outfitJSON(layers ...string) json.RawMessage {
	return json.RawMessage("[" + strings.Join(layers, ",") + "]")
}

func layerJSON(sheet, ramps string) string {
	return `{"sheet":"` + sheet + `","ramps":` + ramps + `}`
}

func TestValidateFarmerOutfit_Accepts(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"body only": outfitJSON(layerJSON(testBody, `{"skin":3}`)),
		"dressed": outfitJSON(
			layerJSON(testBody, `{"skin":0}`),
			`{"sheet":"`+testCloakB+`","ramps":{"c4":0,"c3":0},"behind":true}`,
			layerJSON(testBoots, `{"c3":0}`),
			layerJSON(testPants, `{"c3":32}`),
			layerJSON(testShirt, `{"c3":0}`),
			layerJSON(testCloakF, `{"c4":0,"c3":0}`),
			layerJSON(testHat, `{"c4":37,"c3":3}`),
		),
		"curved shirt": outfitJSON(layerJSON(testBody, `{"skin":0}`), layerJSON(testCurved, `{"c3":0}`)),
	} {
		out, err := ValidateFarmerOutfit(raw)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var back []outfitLayer
		if err := json.Unmarshal(out, &back); err != nil || len(back) == 0 {
			t.Errorf("%s: re-encoded layers unreadable: %s", name, out)
		}
	}
}

func TestValidateFarmerOutfit_Rejects(t *testing.T) {
	body := layerJSON(testBody, `{"skin":0}`)
	for name, c := range map[string]struct {
		raw  json.RawMessage
		want string
	}{
		"not an array":         {json.RawMessage(`{"sheet":"x"}`), "must be an array"},
		"empty":                {outfitJSON(), "between 1 and"},
		"unknown key":          {outfitJSON(`{"sheet":"` + testBody + `","ramps":{"skin":0},"tint":1}`), "must be an array"},
		"unknown sheet":        {outfitJSON(body, layerJSON("/etc/passwd", `{"c3":0}`)), "not in the wardrobe"},
		"modern sheet":         {outfitJSON(body, layerJSON(FarmerSheetRoot+"05shrt/fbas_05shrt_tanktop_00a.png", `{"c3":0}`)), "not in the wardrobe"},
		"no body first":        {outfitJSON(layerJSON(testShirt, `{"c3":0}`), body), "first layer must be the body"},
		"sheet twice":          {outfitJSON(body, layerJSON(testShirt, `{"c3":0}`), layerJSON(testShirt, `{"c3":0}`)), "worn twice"},
		"two lower":            {outfitJSON(body, layerJSON(testPants, `{"c3":0}`), layerJSON(testSkirt, `{"c3":0}`)), `two items in "lower"`},
		"two feet":             {outfitJSON(body, layerJSON(testBoots, `{"c3":0}`), layerJSON(testCuffed, `{"c3":0}`)), `two items in "feet"`},
		"out of order":         {outfitJSON(body, layerJSON(testShirt, `{"c3":0}`), layerJSON(testPants, `{"c3":0}`)), "out of order"},
		"behind not allowed":   {outfitJSON(body, `{"sheet":"`+testShirt+`","ramps":{"c3":0},"behind":true}`), "behind must be false"},
		"behind missing":       {outfitJSON(body, layerJSON(testCloakB, `{"c4":0,"c3":0}`)), "behind must be true"},
		"missing slot":         {outfitJSON(body, layerJSON(testHat, `{"c4":0}`)), "ramps must name"},
		"extra slot":           {outfitJSON(body, layerJSON(testShirt, `{"c3":0,"c4":0}`)), "ramps must name"},
		"wrong slot":           {outfitJSON(body, layerJSON(testShirt, `{"c4":0}`)), "ramps must name"},
		"colour not offered":   {outfitJSON(body, layerJSON(testShirt, `{"c3":40}`)), "colour 40 is not offered"},
		"colour out of range":  {outfitJSON(body, layerJSON(testShirt, `{"c3":-1}`)), "colour -1 is not offered"},
		"cloak front only":     {outfitJSON(body, layerJSON(testCloakF, `{"c4":0,"c3":0}`)), `"cloakplain" is missing a part`},
		"cloak back only":      {outfitJSON(body, `{"sheet":"`+testCloakB+`","ramps":{"c4":0,"c3":0},"behind":true}`), `"cloakplain" is missing a part`},
		"cloak colours differ": {outfitJSON(body, `{"sheet":"`+testCloakB+`","ramps":{"c4":0,"c3":0},"behind":true}`, layerJSON(testCloakF, `{"c4":2,"c3":0}`)), `every part of "cloakplain" takes the same colours`},
		"both shirt figures":   {outfitJSON(body, layerJSON(testShirt, `{"c3":0}`), layerJSON(testCurved, `{"c3":0}`)), "out of order"},
		"too many layers": {outfitJSON(body, body, body, body, body, body, body, body,
			body, body, body, body, body, body, body, body, body), "between 1 and"},
	} {
		_, err := ValidateFarmerOutfit(c.raw)
		if !errors.Is(err, ErrInvalidOutfit) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalidOutfit containing %q", name, err, c.want)
		}
	}
}

// TestPlayerWardrobe_Consistent: every item names a known category and only
// slots that offer colours, every offered colour indexes a real pack ramp (the
// client's FarmerPalettes family sizes), and every sheet sits under the served
// root with a layer order matching its folder.
func TestPlayerWardrobe_Consistent(t *testing.T) {
	familySize := map[string]int{"skin": 18, "hair": 58, "c3": 48, "c4": 59}
	wr := PlayerWardrobe(NewWorld(Repository{}))
	categories := map[string]bool{}
	for _, c := range wr.Categories {
		categories[c.ID] = true
	}
	for slot, list := range wr.Colours {
		for _, index := range list {
			if index < 0 || index >= familySize[slot] {
				t.Errorf("colour %s/%d is outside the pack's %d ramps", slot, index, familySize[slot])
			}
		}
	}
	seen := map[string]bool{}
	for _, item := range wr.Items {
		if !categories[item.Category] {
			t.Errorf("%s: unknown category %q", item.ID, item.Category)
		}
		if seen[item.ID] {
			t.Errorf("%s: duplicate item id", item.ID)
		}
		seen[item.ID] = true
		for _, slot := range item.Slots {
			if len(wr.Colours[slot]) == 0 {
				t.Errorf("%s: slot %q offers no colours", item.ID, slot)
			}
		}
		if len(item.Layers) == 0 {
			t.Errorf("%s: no layers", item.ID)
		}
		for _, layer := range item.Layers {
			for _, sheet := range []string{layer.Sheet, layer.Curved} {
				if sheet == "" {
					continue
				}
				rest, ok := strings.CutPrefix(sheet, FarmerSheetRoot)
				if !ok || !strings.HasSuffix(rest, ".png") {
					t.Errorf("%s: sheet %q is not under %s", item.ID, sheet, FarmerSheetRoot)
					continue
				}
				if rest[:2] != twoDigits(layer.Order) {
					t.Errorf("%s: sheet %q has order %d", item.ID, sheet, layer.Order)
				}
			}
		}
	}
}

func twoDigits(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestOutfitSpriteID(t *testing.T) {
	a := OutfitSpriteID("pc-1")
	if a != OutfitSpriteID("pc-1") {
		t.Fatal("not deterministic")
	}
	if a == OutfitSpriteID("pc-2") {
		t.Fatal("two PCs share an outfit id")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(string(a)) {
		t.Fatalf("%q is not a v5 UUID", a)
	}
}

// TestSetPCOutfit_InstallsWithoutWritingPublishedMap: the outfit lands in the
// catalog and on the PC, the swap emits npc_sprite_changed, and the map the
// last snapshot aliased is left untouched (HTTP goroutines read it unlocked).
func TestSetPCOutfit_InstallsWithoutWritingPublishedMap(t *testing.T) {
	w := NewWorld(Repository{})
	w.Sprites = map[SpriteID]*Sprite{"old": {ID: "old"}}
	w.Actors["pc-1"] = &Actor{ID: "pc-1", Kind: KindPC, LoginUsername: "tester", DisplayName: "Tess", SpriteID: "old"}
	before := w.Sprites

	layers, err := ValidateFarmerOutfit(outfitJSON(layerJSON(testBody, `{"skin":2}`)))
	if err != nil {
		t.Fatal(err)
	}
	sprite, err := NewOutfitSprite("pc-1", "Tess", layers)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	w.Subscribe(SubscriberFunc(func(_ *World, e Event) { events = append(events, e) }))
	if _, err := SetPCOutfit("tester", sprite).Fn(w); err != nil {
		t.Fatal(err)
	}
	installed := w.Sprites[sprite.ID]
	if w.Actors["pc-1"].SpriteID != sprite.ID || installed == nil || string(installed.Layers) != string(sprite.Layers) {
		t.Fatal("outfit not installed")
	}
	if _, leaked := before[sprite.ID]; leaked {
		t.Fatal("InstallSprite wrote the previously published map")
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one NPCSpriteChanged", len(events))
	}
	if e, ok := events[0].(*NPCSpriteChanged); !ok || e.ActorID != "pc-1" || e.Sprite != installed {
		t.Fatalf("event = %#v", events[0])
	}

	if _, err := SetPCOutfit("nobody", sprite).Fn(w); !errors.Is(err, ErrPCNotFound) {
		t.Fatalf("unknown login: err = %v", err)
	}
	other, _ := NewOutfitSprite("pc-2", "Other", layers)
	if _, err := SetPCOutfit("tester", other).Fn(w); !errors.Is(err, ErrPCNotFound) {
		t.Fatalf("another PC's sprite: err = %v", err)
	}
}

// TestSetPCOutfit_FirstOutfitAppears: a PC with no sprite has been drawn by
// no client, so its first outfit is announced as NPCCreated with where it
// stands — inside the inn, for a new PC.
func TestSetPCOutfit_FirstOutfitAppears(t *testing.T) {
	w := NewWorld(Repository{})
	w.Sprites = map[SpriteID]*Sprite{}
	w.Actors["pc-1"] = &Actor{ID: "pc-1", Kind: KindPC, LoginUsername: "tester", DisplayName: "Tess",
		Pos: TilePos{X: 12, Y: 7}, InsideStructureID: "inn"}
	layers, _ := ValidateFarmerOutfit(outfitJSON(layerJSON(testBody, `{"skin":2}`)))
	sprite, _ := NewOutfitSprite("pc-1", "Tess", layers)
	var events []Event
	w.Subscribe(SubscriberFunc(func(_ *World, e Event) { events = append(events, e) }))
	if _, err := SetPCOutfit("tester", sprite).Fn(w); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one", len(events))
	}
	e, ok := events[0].(*NPCCreated)
	if !ok {
		t.Fatalf("event = %T, want *NPCCreated", events[0])
	}
	if e.ActorID != "pc-1" || e.Kind != KindPC || e.X != 12 || e.Y != 7 || e.InsideStructureID != "inn" || e.Sprite != w.Sprites[sprite.ID] || e.DisplayName != "Tess" {
		t.Fatalf("event = %+v", e)
	}
}

// TestWardrobeDyes_Consistent: every dyed colour is one the wardrobe offers,
// no colour needs two dyes, the undyed fallbacks need none, and every good is
// sold once.
func TestWardrobeDyes_Consistent(t *testing.T) {
	owner := map[string]ItemKind{}
	for _, d := range farmerDyes {
		for slot, list := range d.Colours {
			if slot != "c3" && slot != "c4" {
				t.Errorf("%s: dyes slot %q; only cloth is dyed", d.Good, slot)
			}
			for _, index := range list {
				if !containsInt(farmerColours[slot], index) {
					t.Errorf("%s: colour %s/%d is not offered", d.Good, slot, index)
				}
				key := slot + "/" + string(rune('0'+index/10)) + string(rune('0'+index%10))
				if prev, ok := owner[key]; ok {
					t.Errorf("colour %s needs both %s and %s", key, prev, d.Good)
				}
				owner[key] = d.Good
			}
		}
	}
	for slot, index := range farmerUndyed {
		if !containsInt(farmerColours[slot], index) || dyeFor[slot][index] != "" {
			t.Errorf("fallback %s/%d must be offered and undyed", slot, index)
		}
	}
	seen := map[ItemKind]bool{}
	for _, kind := range WardrobeGoods() {
		if seen[kind] {
			t.Errorf("good %s is sold twice", kind)
		}
		seen[kind] = true
	}
	for _, free := range []string{"human", "longshirt", "longpants", "longskirt", "shoes"} {
		if item := wardrobeItemByID(free); item == nil || item.Good != "" {
			t.Errorf("%s must be a free starter piece", free)
		}
	}
}

func TestOutfitGoods(t *testing.T) {
	raw := outfitJSON(
		layerJSON(testBody, `{"skin":0}`),
		layerJSON(testShirt, `{"c3":4}`),
		layerJSON(testHat, `{"c4":5,"c3":32}`),
	)
	got := OutfitGoods(raw)
	want := []ItemKind{"indigo", "straw_hat"}
	if len(got) != len(want) || !containsKinds(got, want) {
		t.Fatalf("OutfitGoods = %v, want %v", got, want)
	}
	if got := OutfitGoods(outfitJSON(layerJSON(testBody, `{"skin":0}`), layerJSON(testPants, `{"c3":32}`))); len(got) != 0 {
		t.Fatalf("free outfit needs %v", got)
	}
}

func containsKinds(got, want []ItemKind) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			return false
		}
	}
	return true
}

const (
	testHair  = FarmerSheetRoot + "13hair/fbas_13hair_dapper_00.png"
	testScarf = FarmerSheetRoot + "14head/fbas_14head_headscarf_00b_e.png"
)

func TestVisibleOutfit(t *testing.T) {
	raw := outfitJSON(
		layerJSON(testBody, `{"skin":0}`),
		layerJSON(testShirt, `{"c3":4}`),
		layerJSON(testHair, `{"hair":3}`),
		layerJSON(testHat, `{"c4":5,"c3":32}`),
	)
	holding := func(kinds ...ItemKind) func(ItemKind) bool {
		return func(k ItemKind) bool { return slices.Contains(kinds, k) }
	}
	sheets := func(layers json.RawMessage) (out []string, ramps []map[string]int) {
		var parsed []outfitLayer
		if err := json.Unmarshal(layers, &parsed); err != nil {
			t.Fatal(err)
		}
		for _, l := range parsed {
			out = append(out, l.Sheet)
			ramps = append(ramps, l.Ramps)
		}
		return out, ramps
	}

	got, ramps := sheets(VisibleOutfit(raw, holding()))
	if len(got) != 3 || slices.Contains(got, testHat) {
		t.Fatalf("holding nothing: sheets %v, want the hat left off", got)
	}
	if ramps[1]["c3"] != farmerUndyed["c3"] {
		t.Fatalf("holding nothing: shirt colour %d, want undyed %d", ramps[1]["c3"], farmerUndyed["c3"])
	}

	got, ramps = sheets(VisibleOutfit(raw, holding("straw_hat", "indigo")))
	if len(got) != 4 || ramps[1]["c3"] != 4 || ramps[3]["c4"] != 5 {
		t.Fatalf("holding all: sheets %v ramps %v, want the outfit unchanged", got, ramps)
	}

	scarfed := outfitJSON(
		layerJSON(testBody, `{"skin":0}`),
		layerJSON(testHair, `{"hair":3}`),
		layerJSON(testScarf, `{"c4":0}`),
	)
	if got, _ := sheets(VisibleOutfit(scarfed, nil)); slices.Contains(got, testHair) || !slices.Contains(got, testScarf) {
		t.Fatalf("headscarf worn: sheets %v, want the hair under it left off", got)
	}
	if got, _ := sheets(VisibleOutfit(scarfed, holding())); !slices.Contains(got, testHair) || slices.Contains(got, testScarf) {
		t.Fatalf("headscarf not held: sheets %v, want the hair back", got)
	}
}

// TestPCForOutfit_RefusesWhatIsNotHeld: a save naming a piece or a dye the PC
// does not hold is refused, naming them; holding them, it passes.
func TestPCForOutfit_RefusesWhatIsNotHeld(t *testing.T) {
	w := NewWorld(Repository{})
	w.ItemKinds["straw_hat"] = &ItemKindDef{Name: "straw_hat", DisplayLabel: "Straw hat"}
	pc := &Actor{ID: "pc-1", Kind: KindPC, LoginUsername: "tester", DisplayName: "Tess", Inventory: map[ItemKind]int{}}
	w.Actors["pc-1"] = pc
	raw := outfitJSON(layerJSON(testBody, `{"skin":0}`), layerJSON(testHat, `{"c4":5,"c3":32}`))

	_, err := PCForOutfit("tester", raw).Fn(w)
	var notHeld *OutfitNotHeldError
	if !errors.As(err, &notHeld) || !slices.Contains(notHeld.Missing, "Straw hat") || !slices.Contains(notHeld.Missing, "indigo") {
		t.Fatalf("err = %v, want straw hat and indigo missing", err)
	}
	pc.Inventory["straw_hat"] = 1
	pc.Inventory["indigo"] = 1
	if _, err := PCForOutfit("tester", raw).Fn(w); err != nil {
		t.Fatalf("holding both: err = %v", err)
	}
}

// TestReconcilePCOutfits: a piece the PC stops holding comes off its sprite,
// and back on when held again; the chosen outfit is kept throughout.
func TestReconcilePCOutfits(t *testing.T) {
	w := NewWorld(Repository{})
	w.Sprites = map[SpriteID]*Sprite{}
	pc := &Actor{ID: "pc-1", Kind: KindPC, LoginUsername: "tester", DisplayName: "Tess",
		Inventory: map[ItemKind]int{"straw_hat": 1}}
	w.Actors["pc-1"] = pc
	chosen, err := ValidateFarmerOutfit(outfitJSON(layerJSON(testBody, `{"skin":0}`), layerJSON(testHat, `{"c4":27,"c3":32}`)))
	if err != nil {
		t.Fatal(err)
	}
	sprite, _ := NewOutfitSprite("pc-1", "Tess", chosen)
	if _, err := SetPCOutfit("tester", sprite).Fn(w); err != nil {
		t.Fatal(err)
	}
	var events []Event
	w.Subscribe(SubscriberFunc(func(_ *World, e Event) { events = append(events, e) }))

	if n, _ := ReconcilePCOutfits().Fn(w); n != 0 || len(events) != 0 {
		t.Fatalf("nothing lost: changed %v, events %d", n, len(events))
	}
	pc.Inventory["straw_hat"] = 0
	if n, _ := ReconcilePCOutfits().Fn(w); n != 1 || len(events) != 1 {
		t.Fatalf("hat sold: changed %v, events %d", n, len(events))
	}
	shown := w.Sprites[sprite.ID]
	if strings.Contains(string(shown.Layers), testHat) || string(shown.Chosen) != string(chosen) {
		t.Fatalf("hat sold: layers %s chosen %s", shown.Layers, shown.Chosen)
	}
	if got := w.chosenOutfit("pc-1"); string(got) != string(chosen) {
		t.Fatalf("chosenOutfit = %s", got)
	}
	pc.Inventory["straw_hat"] = 1
	if n, _ := ReconcilePCOutfits().Fn(w); n != 1 || !strings.Contains(string(w.Sprites[sprite.ID].Layers), testHat) {
		t.Fatalf("hat bought back: changed %v, layers %s", n, w.Sprites[sprite.ID].Layers)
	}
}

// TestPCWardrobe_Sellers: the wardrobe names the villagers in the PC's huddle
// who hold or stock each good (LLM-715) — a keeper with a buy line and none on
// hand is listed with Held 0; a villager outside the huddle, another PC, and
// two villagers sharing a display name are not.
func TestPCWardrobe_Sellers(t *testing.T) {
	w := NewWorld(Repository{})
	w.Actors["pc-1"] = &Actor{ID: "pc-1", Kind: KindPC, LoginUsername: "tester", DisplayName: "Tess",
		CurrentHuddleID: "h1", Inventory: map[ItemKind]int{"vest": 1}}
	w.Actors["josiah"] = &Actor{ID: "josiah", Kind: KindNPCStateful, DisplayName: "Josiah Thorne",
		CurrentHuddleID: "h1", Inventory: map[ItemKind]int{"straw_hat": 2},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "straw_hat", Source: RestockSourceBuy, Max: 2}, {Item: "felt_hat", Source: RestockSourceBuy, Max: 2}}}}
	w.Actors["hannah"] = &Actor{ID: "hannah", Kind: KindNPCStateful, DisplayName: "Hannah Boggs",
		CurrentHuddleID: "h1", Inventory: map[ItemKind]int{"vest": 1}}
	w.Actors["other-pc"] = &Actor{ID: "other-pc", Kind: KindPC, DisplayName: "Pat",
		CurrentHuddleID: "h1", Inventory: map[ItemKind]int{"boots": 1}}
	w.Actors["twin-a"] = &Actor{ID: "twin-a", Kind: KindNPCShared, DisplayName: "A Peddler",
		CurrentHuddleID: "h1", Inventory: map[ItemKind]int{"gloves": 1}}
	w.Actors["twin-b"] = &Actor{ID: "twin-b", Kind: KindNPCShared, DisplayName: "a peddler",
		CurrentHuddleID: "h1", Inventory: map[ItemKind]int{}}
	w.Actors["away"] = &Actor{ID: "away", Kind: KindNPCStateful, DisplayName: "Ezekiel Crane",
		CurrentHuddleID: "h2", Inventory: map[ItemKind]int{"cloak": 1}}
	w.actorsByHuddle["h1"] = map[ActorID]struct{}{"pc-1": {}, "josiah": {}, "hannah": {}, "other-pc": {}, "twin-a": {}, "twin-b": {}}
	w.actorsByHuddle["h2"] = map[ActorID]struct{}{"away": {}}

	res, err := PCWardrobe("tester").Fn(w)
	if err != nil {
		t.Fatal(err)
	}
	sellers := res.(FarmerWardrobe).Sellers
	want := map[ItemKind][]FarmerWardrobeSeller{
		"straw_hat": {{Name: "Josiah Thorne", Held: 2}},
		"felt_hat":  {{Name: "Josiah Thorne", Held: 0}},
		"vest":      {{Name: "Hannah Boggs", Held: 1}},
	}
	if !reflect.DeepEqual(sellers, want) {
		t.Fatalf("sellers = %+v, want %+v", sellers, want)
	}

	w.Actors["pc-1"].CurrentHuddleID = ""
	res, _ = PCWardrobe("tester").Fn(w)
	if got := res.(FarmerWardrobe).Sellers; len(got) != 0 {
		t.Fatalf("unhuddled PC: sellers = %+v, want none", got)
	}
}
