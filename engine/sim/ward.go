package sim

import (
	"log"
	"time"
)

// ward.go — LLM-845. Wards: folk practice against witchcraft. A player hangs a
// horseshoe over their tent door or buries a witch bottle by its hearth, from
// Your Pack. The ward is kept as a tag on the tent object (TagWardHorseshoe,
// TagWardWitchBottle): tags are checkpointed with the object and reach every
// client in the village_object_tags_updated frame, so the client draws the
// horseshoe on the tent from the tag. Striking the tent puts the wards back in
// the pack (dismantleCamp → returnTentWards).
//
// What a ward DOES is the omens' business (the omen roll weighs a warded
// household down); the game never says so. Villagers notice a horseshoe over
// a tent door (perception buildCampSightings).

// CapabilityWard marks a good that can be placed at the player's home as a
// ward (migration LLM-845).
const CapabilityWard = "ward"

// The ward item-action slugs.
const (
	ItemActionHang = "hang"
	ItemActionBury = "bury"
)

// The ward tags on a tent object.
const (
	TagWardHorseshoe   = "ward_horseshoe"
	TagWardWitchBottle = "ward_witch_bottle"
)

// The ward goods.
const (
	HorseshoeKind   = ItemKind("horseshoe")
	WitchBottleKind = ItemKind("witch_bottle")
)

// WardKind is one ward good: the pack action that places it, the tag it leaves
// on the tent, and the line the player reads when it is placed.
type WardKind struct {
	Item   ItemKind
	Action string
	Tag    string
	Placed string
}

// WardKinds are the ward goods, in a fixed order.
var WardKinds = []WardKind{
	{Item: HorseshoeKind, Action: ItemActionHang, Tag: TagWardHorseshoe,
		Placed: "You nail the horseshoe over the door of your tent, points up."},
	{Item: WitchBottleKind, Action: ItemActionBury, Tag: TagWardWitchBottle,
		Placed: "You bury the witch bottle by your fire ring and tread the earth down over it."},
}

// wardKindFor is the ward entry for a good, or nil.
func wardKindFor(kind ItemKind) *WardKind {
	for i := range WardKinds {
		if WardKinds[i].Item == kind {
			return &WardKinds[i]
		}
	}
	return nil
}

// WardPlaced is emitted when a player places a ward at their tent. The story's
// "hang it over your door" beat listens for it.
type WardPlaced struct {
	EventBase
	ActorID  ActorID
	Item     ItemKind
	ObjectID VillageObjectID
	Text     string
	At       time.Time
}

func (WardPlaced) isSimEvent() {}

// WithCamp fills the pack state's view of the player's camp: whether their
// tent stands (pitched, not a packed bundle) and which wards it carries.
func (st PackState) WithCamp(objects map[VillageObjectID]*VillageObject, ownerID ActorID) PackState {
	st.TentPitched = false
	st.TentWards = nil
	camp := FindCamp(objects, ownerID)
	if camp == nil || camp.Tent == nil || camp.Tent.CurrentState == TentStatePacked {
		return st
	}
	st.TentPitched = true
	for _, wk := range WardKinds {
		if camp.Tent.HasTag(wk.Tag) {
			if st.TentWards == nil {
				st.TentWards = map[ItemKind]bool{}
			}
			st.TentWards[wk.Item] = true
		}
	}
	return st
}

// wardAction is the action a ward good offers: its own, while the tent stands
// and does not carry one already. "" otherwise.
func wardAction(def *ItemKindDef, st PackState) string {
	if !def.HasCapability(CapabilityWard) {
		return ""
	}
	wk := wardKindFor(def.Name)
	if wk == nil || !st.TentPitched || st.TentWards[wk.Item] {
		return ""
	}
	return wk.Action
}

// placeWard puts one ward from a's pack on their standing tent: the tag on the
// tent object (AddVillageObjectTag, which emits the tags frame), one unit out
// of the pack, and WardPlaced. ItemActions has already checked the tent stands
// and carries no ward of this kind. Refused while walking, while busy, or away
// from the tent; a refusal changes nothing.
func placeWard(w *World, a *Actor, kind ItemKind, now time.Time) error {
	wk := wardKindFor(kind)
	camp := FindCamp(w.VillageObjects, a.ID)
	if wk == nil || camp == nil || camp.Tent == nil {
		return refuseItemAction("Your tent is not pitched.")
	}
	if a.MoveIntent != nil {
		return refuseItemAction("Stop walking first.")
	}
	// Not while pitching or striking (or any other work): a ward goes on a
	// finished camp, so it is never on a tent a restart or an undo is about
	// to settle.
	if a.SourceActivity != nil {
		return refuseItemAction("Finish what you are doing first.")
	}
	// The strike's reach: a ward goes up with the player at the tent.
	if a.InsideStructureID != StructureID(camp.Tent.ID) && !nearTent(w, a.Pos, camp.Tent) {
		return refuseItemAction("Walk up to your tent first.")
	}
	if _, err := AddVillageObjectTag(camp.Tent.ID, wk.Tag).Fn(w); err != nil {
		return err
	}
	a.Inventory[kind]--
	if a.Inventory[kind] <= 0 {
		delete(a.Inventory, kind)
	}
	log.Printf("sim/ward: %s placed %s at tent %s", a.ID, kind, camp.Tent.ID)
	w.emit(&WardPlaced{ActorID: a.ID, Item: kind, ObjectID: camp.Tent.ID, Text: wk.Placed, At: now})
	return nil
}

// returnTentWards gives the owner back every ward their tent carries; called
// as the tent is taken down (dismantleCamp), so a struck camp's wards ride in
// the pack and the player hangs them again at the next pitch.
func returnTentWards(w *World, ownerID ActorID, tent *VillageObject) {
	if tent == nil {
		return
	}
	owner := w.Actors[ownerID]
	for _, wk := range WardKinds {
		if !tent.HasTag(wk.Tag) {
			continue
		}
		if owner == nil {
			log.Printf("sim/ward: tent %s carries %s but its owner %s is gone; the ward is lost", tent.ID, wk.Item, ownerID)
			continue
		}
		if owner.Inventory == nil {
			owner.Inventory = map[ItemKind]int{}
		}
		owner.Inventory[wk.Item]++
	}
}
