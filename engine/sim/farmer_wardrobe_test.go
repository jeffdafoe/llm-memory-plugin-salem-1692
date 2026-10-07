package sim

import (
	"encoding/json"
	"errors"
	"regexp"
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
	wr := PlayerWardrobe()
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

func TestPCOutfitSpriteID(t *testing.T) {
	a := PCOutfitSpriteID("pc-1")
	if a != PCOutfitSpriteID("pc-1") {
		t.Fatal("not deterministic")
	}
	if a == PCOutfitSpriteID("pc-2") {
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
	sprite, err := NewPCOutfitSprite("pc-1", "Tess", layers)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	w.Subscribe(SubscriberFunc(func(_ *World, e Event) { events = append(events, e) }))
	if _, err := SetPCOutfit("tester", sprite).Fn(w); err != nil {
		t.Fatal(err)
	}
	if w.Actors["pc-1"].SpriteID != sprite.ID || w.Sprites[sprite.ID] != sprite {
		t.Fatal("outfit not installed")
	}
	if _, leaked := before[sprite.ID]; leaked {
		t.Fatal("InstallSprite wrote the previously published map")
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one NPCSpriteChanged", len(events))
	}
	if e, ok := events[0].(*NPCSpriteChanged); !ok || e.ActorID != "pc-1" || e.Sprite != sprite {
		t.Fatalf("event = %#v", events[0])
	}

	if _, err := SetPCOutfit("nobody", sprite).Fn(w); !errors.Is(err, ErrPCNotFound) {
		t.Fatalf("unknown login: err = %v", err)
	}
	other, _ := NewPCOutfitSprite("pc-2", "Other", layers)
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
	sprite, _ := NewPCOutfitSprite("pc-1", "Tess", layers)
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
	if e.ActorID != "pc-1" || e.Kind != KindPC || e.X != 12 || e.Y != 7 || e.InsideStructureID != "inn" || e.Sprite != sprite || e.DisplayName != "Tess" {
		t.Fatalf("event = %+v", e)
	}
}
