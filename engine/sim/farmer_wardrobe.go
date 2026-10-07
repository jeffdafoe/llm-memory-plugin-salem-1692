package sim

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// farmer_wardrobe.go — what a player may wear (LLM-691). The character creator
// dresses a player on the Mana Seed farmer base: it picks items from this
// wardrobe, composes their layer sheets into a farmer_base layer list, and
// sends it to pc/outfit. The client owns the composition (layer order, the
// figure variants, a hat hiding the hair); this file owns what is allowed — the
// sheets, the ramp slots each sheet is drawn in, and the colours each slot may
// take — and ValidateFarmerOutfit holds a submitted list to it, so a player can
// never point their sprite at an arbitrary sheet.
//
// The wardrobe is the pack's clothing filtered for Salem 1692: no shorts,
// tank tops, overalls, sandals, shades or cowboy hats. Colours are the pack's
// ramps filtered to natural dyes and natural hair colours; skin takes every
// ramp.

// FarmerSheetRoot is where the pack's farmer_base_sheets/ are served
// (operator-managed, see shared/notes/codebase/salem/farmer-base-sprites).
const FarmerSheetRoot = "/tilesets/mana-seed/farmer/sheets/"

// FarmerPackID is the tileset_pack row every farmer-base sprite belongs to.
const FarmerPackID = "mana-seed-farmer"

// farmerCellSize is the farmer-base cell; a rig sprite's frame size.
const farmerCellSize = 64

// maxOutfitLayers caps a submitted layer list: one layer per category plus a
// cloak's back half.
const maxOutfitLayers = 16

// ErrInvalidOutfit is returned by ValidateFarmerOutfit; the wrapped message
// names the offending layer.
var ErrInvalidOutfit = errors.New("invalid outfit")

// FarmerWardrobeCategory is one row of the creator. Required categories must
// be worn; the rest offer "none".
type FarmerWardrobeCategory struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Required bool   `json:"required,omitempty"`
}

// FarmerWardrobeLayer is one sheet of an item. Curved is the sheet for the
// curved figure when the pack draws one; Behind draws the layer under the
// body (the back half of a cloak). Order is the pack's layer number, the
// stacking order of the non-behind layers.
type FarmerWardrobeLayer struct {
	Sheet  string `json:"sheet"`
	Curved string `json:"curved,omitempty"`
	Behind bool   `json:"behind,omitempty"`
	Order  int    `json:"order"`
}

// FarmerWardrobeItem is one choice in a category. Slots are the ramp slots its
// sheets are drawn in, one colour each (every layer of the item takes the same
// colours). HidesHair: the hat covers the hair, so the hair layer is left out.
type FarmerWardrobeItem struct {
	ID        string                `json:"id"`
	Category  string                `json:"category"`
	Label     string                `json:"label"`
	Layers    []FarmerWardrobeLayer `json:"layers"`
	Slots     []string              `json:"slots"`
	HidesHair bool                  `json:"hides_hair,omitempty"`
}

// FarmerWardrobe is the pc/wardrobe payload. Colours maps a ramp slot to the
// indexes (into the client's FarmerPalettes family for that slot) a player
// may pick.
type FarmerWardrobe struct {
	Categories []FarmerWardrobeCategory `json:"categories"`
	Items      []FarmerWardrobeItem     `json:"items"`
	Colours    map[string][]int         `json:"colours"`
}

var farmerWardrobeCategories = []FarmerWardrobeCategory{
	{ID: "body", Label: "Body", Required: true},
	{ID: "hair", Label: "Hair"},
	{ID: "head", Label: "Hat"},
	{ID: "shirt", Label: "Shirt"},
	{ID: "over", Label: "Vest"},
	{ID: "neck", Label: "Cloak"},
	{ID: "lower", Label: "Breeches and skirts"},
	{ID: "legs", Label: "Stockings"},
	{ID: "feet", Label: "Shoes"},
	{ID: "hands", Label: "Gloves"},
}

// farmerColours: skin takes all 18 pack ramps; hair, c3 and c4 keep the
// natural ones (earth tones, greys, muted blues and greens — Jeff, 10-07).
var farmerColours = map[string][]int{
	"skin": {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17},
	"hair": {0, 1, 2, 3, 4, 5, 7, 16, 17, 19, 22, 23, 24, 26, 27, 29, 30, 31, 34, 36, 37, 39, 43, 45, 50, 53},
	"c3":   {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 11, 14, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 32, 34, 35, 43, 47},
	"c4":   {0, 1, 2, 3, 4, 5, 6, 7, 10, 11, 13, 14, 15, 16, 17, 19, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 40, 51, 52, 57},
}

// wardrobeItem builds an item from pack file names (relative to
// FarmerSheetRoot). A file name with a "|" names its curved variant after it;
// a leading "<" marks a behind layer.
func wardrobeItem(category, id, label string, slots []string, files ...string) FarmerWardrobeItem {
	item := FarmerWardrobeItem{ID: id, Category: category, Label: label, Slots: slots}
	for _, f := range files {
		var layer FarmerWardrobeLayer
		if strings.HasPrefix(f, "<") {
			layer.Behind = true
			f = f[1:]
		}
		plain, curved, _ := strings.Cut(f, "|")
		layer.Sheet = FarmerSheetRoot + plain
		if curved != "" {
			layer.Curved = FarmerSheetRoot + curved
		}
		layer.Order, _ = strconv.Atoi(plain[:2])
		item.Layers = append(item.Layers, layer)
	}
	return item
}

// hidesHair marks a hat the pack draws over the whole head (an "_e" sheet).
func hidesHair(item FarmerWardrobeItem) FarmerWardrobeItem {
	item.HidesHair = true
	return item
}

var (
	slotsSkin   = []string{"skin"}
	slotsHair   = []string{"hair"}
	slotsC3     = []string{"c3"}
	slotsC4     = []string{"c4"}
	slotsC4C3   = []string{"c4", "c3"}
	slotsC4Hair = []string{"c4", "hair"}
)

var farmerWardrobeItems = []FarmerWardrobeItem{
	wardrobeItem("body", "human", "Human", slotsSkin, "01body/fbas_01body_human_00.png"),

	wardrobeItem("hair", "afro", "Afro", slotsHair, "13hair/fbas_13hair_afro_00.png"),
	wardrobeItem("hair", "afropuffs", "Puffs", slotsHair, "13hair/fbas_13hair_afropuffs_00.png"),
	wardrobeItem("hair", "bob1", "Bob", slotsHair, "13hair/fbas_13hair_bob1_00.png"),
	wardrobeItem("hair", "bob2", "Short bob", slotsHair, "13hair/fbas_13hair_bob2_00.png"),
	wardrobeItem("hair", "bushy", "Bushy", slotsHair, "13hair/fbas_13hair_bushy_00.png"),
	wardrobeItem("hair", "dapper", "Parted", slotsHair, "13hair/fbas_13hair_dapper_00.png"),
	wardrobeItem("hair", "flattop", "Cropped", slotsHair, "13hair/fbas_13hair_flattop_00.png"),
	wardrobeItem("hair", "longbound", "Long, bound", slotsHair, "13hair/fbas_13hair_longbound_00.png"),
	wardrobeItem("hair", "longboundclasped", "Long, clasped", slotsC4Hair, "13hair/fbas_13hair_longboundclasped_00f.png"),
	wardrobeItem("hair", "longlush", "Long, loose", slotsHair, "13hair/fbas_13hair_longlush_00.png"),
	wardrobeItem("hair", "longwavy", "Long, wavy", slotsHair, "13hair/fbas_13hair_longwavy_00.png"),
	wardrobeItem("hair", "ponytail1", "Tail", slotsHair, "13hair/fbas_13hair_ponytail1_00.png"),
	wardrobeItem("hair", "topknot", "Topknot", slotsC4Hair, "13hair/fbas_13hair_topknot_00f.png"),
	wardrobeItem("hair", "twintail", "Two tails", slotsHair, "13hair/fbas_13hair_twintail_00.png"),
	wardrobeItem("hair", "twists", "Twists", slotsHair, "13hair/fbas_13hair_twists_00.png"),

	hidesHair(wardrobeItem("head", "headscarf", "Headscarf", slotsC4, "14head/fbas_14head_headscarf_00b_e.png")),
	wardrobeItem("head", "strawhat", "Straw hat", slotsC4C3, "14head/fbas_14head_strawhat_00d.png"),
	wardrobeItem("head", "floppyhat", "Felt hat", slotsC4C3, "14head/fbas_14head_floppyhat_00d.png"),
	wardrobeItem("head", "boaterhat", "Brimmed hat", slotsC4C3, "14head/fbas_14head_boaterhat_00d.png"),

	wardrobeItem("shirt", "longshirt", "Linen shirt", slotsC3, "05shrt/fbas_05shrt_longshirt_00a.png|05shrt/fbas_05shrt_longshirtboobs_00a.png"),

	wardrobeItem("over", "vest", "Vest", slotsC3, "10outr/fbas_10outr_vest_00a.png"),

	wardrobeItem("neck", "cloakplain", "Cloak", slotsC4C3, "<00undr/fbas_00undr_cloakplain_00d.png", "11neck/fbas_11neck_cloakplain_00d.png"),
	wardrobeItem("neck", "cloakwithmantleplain", "Cloak and mantle", slotsC4, "<00undr/fbas_00undr_cloakwithmantleplain_00b.png", "11neck/fbas_11neck_cloakwithmantleplain_00b.png"),
	wardrobeItem("neck", "mantleplain", "Mantle", slotsC4, "11neck/fbas_11neck_mantleplain_00b.png"),
	wardrobeItem("neck", "scarf", "Scarf", slotsC4, "11neck/fbas_11neck_scarf_00b.png"),

	wardrobeItem("lower", "longpants", "Breeches", slotsC3, "04lwr1/fbas_04lwr1_longpants_00a.png"),
	wardrobeItem("lower", "longskirt", "Long skirt", slotsC3, "08lwr3/fbas_08lwr3_longskirt_00a.png"),
	wardrobeItem("lower", "longdress", "Long dress", slotsC3, "08lwr3/fbas_08lwr3_longdress_00a.png|08lwr3/fbas_08lwr3_longdressboobs_00a.png"),

	wardrobeItem("legs", "sockshigh", "Knee socks", slotsC3, "02sock/fbas_02sock_sockshigh_00a.png"),
	wardrobeItem("legs", "stockings", "Stockings", slotsC3, "02sock/fbas_02sock_stockings_00a.png"),

	wardrobeItem("feet", "boots", "Boots", slotsC3, "03fot1/fbas_03fot1_boots_00a.png"),
	wardrobeItem("feet", "shoes", "Shoes", slotsC3, "03fot1/fbas_03fot1_shoes_00a.png"),
	wardrobeItem("feet", "cuffedboots", "Cuffed boots", slotsC3, "07fot2/fbas_07fot2_cuffedboots_00a.png"),

	wardrobeItem("hands", "gloves", "Gloves", slotsC3, "09hand/fbas_09hand_gloves_00a.png"),
}

// PlayerWardrobe returns the wardrobe the creator offers.
func PlayerWardrobe() FarmerWardrobe {
	return FarmerWardrobe{
		Categories: farmerWardrobeCategories,
		Items:      farmerWardrobeItems,
		Colours:    farmerColours,
	}
}

// wardrobeSheet is one allowed sheet, resolved back to its item and layer.
type wardrobeSheet struct {
	item  *FarmerWardrobeItem
	layer FarmerWardrobeLayer
}

var wardrobeSheets = func() map[string]wardrobeSheet {
	out := make(map[string]wardrobeSheet)
	for i := range farmerWardrobeItems {
		item := &farmerWardrobeItems[i]
		for _, layer := range item.Layers {
			out[layer.Sheet] = wardrobeSheet{item: item, layer: layer}
			if layer.Curved != "" {
				out[layer.Curved] = wardrobeSheet{item: item, layer: layer}
			}
		}
	}
	return out
}()

// outfitLayer is one entry of a farmer_base layer list (npc_sprite.layers).
type outfitLayer struct {
	Sheet  string         `json:"sheet"`
	Ramps  map[string]int `json:"ramps"`
	Behind bool           `json:"behind,omitempty"`
}

// ValidateFarmerOutfit holds a submitted layer list to the wardrobe and returns
// it re-encoded (unknown keys dropped). The body comes first; every sheet is a
// wardrobe sheet worn once, with the item's behind flag; each category is worn
// by one item; the front layers rise in the pack's layer order; and every
// layer names exactly its item's ramp slots, each with an allowed colour.
func ValidateFarmerOutfit(raw json.RawMessage) (json.RawMessage, error) {
	var layers []outfitLayer
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&layers); err != nil {
		return nil, fmt.Errorf("%w: layers must be an array of {sheet, ramps, behind?}", ErrInvalidOutfit)
	}
	if len(layers) == 0 || len(layers) > maxOutfitLayers {
		return nil, fmt.Errorf("%w: between 1 and %d layers", ErrInvalidOutfit, maxOutfitLayers)
	}
	seenSheets := make(map[string]bool, len(layers))
	wornBy := make(map[string]string, len(layers))
	lastOrder := -1
	for i, l := range layers {
		ws, ok := wardrobeSheets[l.Sheet]
		if !ok {
			return nil, fmt.Errorf("%w: layer %d: sheet %q is not in the wardrobe", ErrInvalidOutfit, i, l.Sheet)
		}
		if i == 0 && ws.item.Category != "body" {
			return nil, fmt.Errorf("%w: the first layer must be the body", ErrInvalidOutfit)
		}
		if seenSheets[l.Sheet] {
			return nil, fmt.Errorf("%w: layer %d: sheet %q is worn twice", ErrInvalidOutfit, i, l.Sheet)
		}
		seenSheets[l.Sheet] = true
		if prev, worn := wornBy[ws.item.Category]; worn && prev != ws.item.ID {
			return nil, fmt.Errorf("%w: layer %d: two items in %q", ErrInvalidOutfit, i, ws.item.Category)
		}
		wornBy[ws.item.Category] = ws.item.ID
		if l.Behind != ws.layer.Behind {
			return nil, fmt.Errorf("%w: layer %d: behind must be %t", ErrInvalidOutfit, i, ws.layer.Behind)
		}
		if !l.Behind {
			if ws.layer.Order <= lastOrder {
				return nil, fmt.Errorf("%w: layer %d is out of order", ErrInvalidOutfit, i)
			}
			lastOrder = ws.layer.Order
		}
		if len(l.Ramps) != len(ws.item.Slots) {
			return nil, fmt.Errorf("%w: layer %d: ramps must name %v", ErrInvalidOutfit, i, ws.item.Slots)
		}
		for _, slot := range ws.item.Slots {
			index, ok := l.Ramps[slot]
			if !ok {
				return nil, fmt.Errorf("%w: layer %d: ramps must name %v", ErrInvalidOutfit, i, ws.item.Slots)
			}
			if !containsInt(farmerColours[slot], index) {
				return nil, fmt.Errorf("%w: layer %d: colour %d is not offered for %s", ErrInvalidOutfit, i, index, slot)
			}
		}
	}
	if _, ok := wornBy["body"]; !ok {
		return nil, fmt.Errorf("%w: the body is required", ErrInvalidOutfit)
	}
	out, err := json.Marshal(layers)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidOutfit, err)
	}
	return out, nil
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// pcOutfitNamespace is the UUIDv5 namespace of PC outfit sprite ids.
var pcOutfitNamespace = [16]byte{0x69, 0x1f, 0x0c, 0x02, 0x7a, 0x3e, 0x4b, 0x5d, 0x9c, 0x81, 0x2e, 0x4f, 0x60, 0xa7, 0x13, 0xd5}

// PCOutfitSpriteID is the npc_sprite id of a PC's own outfit: a UUIDv5 of the
// actor id, so every save of the creator upserts the same row and a player
// never accumulates sprites.
func PCOutfitSpriteID(id ActorID) SpriteID {
	h := sha1.New()
	h.Write(pcOutfitNamespace[:])
	h.Write([]byte(id))
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return SpriteID(fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16]))
}

// NewPCOutfitSprite builds the rig sprite for a PC's outfit from a validated
// layer list. Sheet is the body layer's sheet (what a one-sheet consumer
// would show); the name is the character's, cut to npc_sprite.name's 100.
func NewPCOutfitSprite(id ActorID, characterName string, layers json.RawMessage) (*Sprite, error) {
	var parsed []outfitLayer
	if err := json.Unmarshal(layers, &parsed); err != nil || len(parsed) == 0 {
		return nil, fmt.Errorf("%w: no layers", ErrInvalidOutfit)
	}
	name := []rune(strings.TrimSpace(characterName))
	if len(name) > 100 {
		name = name[:100]
	}
	pack := FarmerPackID
	return &Sprite{
		ID:          PCOutfitSpriteID(id),
		Name:        string(name),
		Sheet:       parsed[0].Sheet,
		FrameWidth:  farmerCellSize,
		FrameHeight: farmerCellSize,
		PackID:      &pack,
		Animations:  []SpriteAnimation{},
		Behaviors:   []string{},
		RenderScale: 2.0,
		Rig:         "farmer_base",
		Layers:      layers,
	}, nil
}
