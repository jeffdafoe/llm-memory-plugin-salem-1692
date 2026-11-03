package sim

import (
	"fmt"
	"log"
	"time"
)

// ward_household.go — LLM-897. Villagers' own wards. After an omen happens to a
// household, its residents want a ward for the house for a while
// (OmenWardWantDays); perception turns that into a felt line and a buy errand
// (perception/ward.go), and a resident at home with the good in their pack puts
// it up with the place_ward tool (PlaceHomeWard). The ward is the same tag
// LLM-845 puts on a player's tent, here on the house's village object, so
// omenWardPermille already weighs the household down.
//
// One ward is enough: a household whose house carries any ward, or one of whose
// residents holds a ward good, wants no more (D2 on LLM-897).
//
// Villagers deal only in the horseshoe. The witch bottle stays a player good:
// nothing the engine writes into a villager's prompt names witchcraft
// (TestGoldensNoEngineTextSaysWitch), and a cue or tool that sold or buried one
// would have to name it.

// DefaultOmenWardWantDays is how many days after its latest omen a household
// wants a ward (WorldSettings.OmenWardWantDays; 0 or less reads as this).
const DefaultOmenWardWantDays = 7

// VillagerWardKinds are the ward goods a villager buys and puts up at home, in a
// fixed order.
var VillagerWardKinds = []ItemKind{HorseshoeKind}

// IsVillagerWardKind reports whether kind is a ward good a villager deals in.
func IsVillagerWardKind(kind ItemKind) bool {
	for _, k := range VillagerWardKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// HomeWarded reports whether a home object carries any ward (a player's tent or
// a villager's house).
func HomeWarded(home *VillageObject) bool {
	if home == nil {
		return false
	}
	for _, wk := range WardKinds {
		if home.HasTag(wk.Tag) {
			return true
		}
	}
	return false
}

// HomeWardTag is the tag kind leaves on a home, or "" when kind is no ward.
func HomeWardTag(kind ItemKind) string {
	if wk := wardKindFor(kind); wk != nil {
		return wk.Tag
	}
	return ""
}

// OmenWardWantWindow is how long after its latest omen a household wants a
// ward, from the setting (0 or less reads as the default).
func OmenWardWantWindow(days int) time.Duration {
	return time.Duration(orDefault(days, DefaultOmenWardWantDays)) * 24 * time.Hour
}

// LatestHouseholdOmen is the household want's core, shared by the perception
// cue and PlaceHomeWard so the two cannot drift: of members, the one whose
// latest omen (lastAt) happened inside window before now, the most recent one
// (an exact tie goes to the lower id). ok false when none did. The caller has
// already checked the house carries no ward.
func LatestHouseholdOmen(lastAt map[ActorID]time.Time, members []ActorID, now time.Time, window time.Duration) (ActorID, bool) {
	var latest time.Time
	var latestID ActorID
	for _, id := range members {
		at, ok := lastAt[id]
		if !ok || now.Sub(at) >= window {
			continue
		}
		if latestID != "" && (at.Before(latest) || (at.Equal(latest) && id > latestID)) {
			continue
		}
		latest, latestID = at, id
	}
	return latestID, latestID != ""
}

// householdWardWantLive reports whether a's household wants a ward right now:
// a resident with a home whose house carries no ward, and some resident of it
// had an omen inside the want window. The world-side twin of perception's
// buildHouseholdWardWant (both rest on LatestHouseholdOmen).
func householdWardWantLive(w *World, a *Actor, now time.Time) bool {
	if !omenResident(a) || a.HomeStructureID == "" {
		return false
	}
	if HomeWarded(w.VillageObjects[VillageObjectID(a.HomeStructureID)]) {
		return false
	}
	var members []ActorID
	for id, m := range w.Actors {
		if omenResident(m) && m.HomeStructureID == a.HomeStructureID {
			members = append(members, id)
		}
	}
	_, ok := LatestHouseholdOmen(w.Environment.Omens.LastAt, members, now,
		OmenWardWantWindow(w.Settings.OmenWardWantDays))
	return ok
}

// OmenHouseholdClause is what went wrong, as the household tells it: "the milk
// turned sour in the pail". "" for a kind with no household telling (the
// players' camp kinds) or an unknown one, so a caller falls back to a plain
// line.
func OmenHouseholdClause(kind OmenKind) string {
	switch kind {
	case OmenSourMilk:
		return "the milk turned sour in the pail"
	case OmenColdHearth:
		return "the hearth fire would not stay lit"
	case OmenFlatBread:
		return "the bread would not rise"
	case OmenWitheredBush:
		return "the berries withered on the bush"
	case OmenFoulWell:
		return "the well water tasted of iron"
	}
	return ""
}

// homeWardPlacedText is the line a villager reads when the ward goes up.
func homeWardPlacedText(kind ItemKind) string {
	switch kind {
	case HorseshoeKind:
		return "You nail the horseshoe over the door, points up."
	}
	return "You put it up at home."
}

// WardLogText is the villager's action-log text for a ward put up at home, in
// the past tense, for the day notes and the dream distiller.
func WardLogText(kind ItemKind) string {
	switch kind {
	case HorseshoeKind:
		return "nailed a horseshoe over the door at home"
	}
	return ""
}

// HomeWardPlacedResult is what PlaceHomeWard returns: the good and the line the
// villager reads.
type HomeWardPlacedResult struct {
	Item ItemKind
	Text string
}

// PlaceHomeWard puts one ward good from a villager's pack up at their own home:
// the ward tag on the house's village object (AddVillageObjectTag, which emits
// the tags frame and is checkpointed with the object), one unit out of the pack,
// and WardPlaced. It re-checks what the perception place choice checked
// (buildWardPlaceChoice), so a stale or forged call changes nothing. MUST run on
// the world goroutine.
func PlaceHomeWard(actorID ActorID, item ItemKind, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok || a == nil {
				return nil, fmt.Errorf("PlaceHomeWard: actor %q not in world", actorID)
			}
			if !IsVillagerWardKind(item) {
				return nil, ModelFacingError{Msg: "that is not something to put up over the door."}
			}
			if !omenResident(a) {
				return nil, ModelFacingError{Msg: "only a household puts a ward up at home this way."}
			}
			if a.State == StateSleeping {
				return nil, ModelFacingError{Msg: "you are asleep."}
			}
			if a.MoveIntent != nil {
				return nil, ModelFacingError{Msg: "you are walking — get home first."}
			}
			// Land a finished-but-not-yet-swept window first so a stale activity
			// doesn't read as busy (matches StartOrJoinBake).
			completeIfDue(w, actorID, a, now)
			if a.SourceActivity != nil {
				return nil, ModelFacingError{Msg: "you are busy — finish what you're doing first."}
			}
			home := a.HomeStructureID
			if home == "" || a.InsideStructureID != home {
				return nil, ModelFacingError{Msg: "you can only put it up at your own home, from inside it."}
			}
			if a.Inventory[item] <= 0 {
				return nil, ModelFacingError{Msg: fmt.Sprintf("you have no %s with you.", item)}
			}
			house := w.VillageObjects[VillageObjectID(home)]
			if house == nil {
				return nil, fmt.Errorf("PlaceHomeWard: home %q has no village object", home)
			}
			// One ward is enough (D2): any ward already up ends the want, and the
			// want itself must still be live — the same gate the place choice
			// read, so a stale call after the window or after a housemate's ward
			// spends nothing.
			if HomeWarded(house) {
				return nil, ModelFacingError{Msg: "there is a ward up at your home already."}
			}
			if !householdWardWantLive(w, a, now) {
				return nil, ModelFacingError{Msg: "there is no call for that at home just now."}
			}
			tag := HomeWardTag(item)
			if _, err := AddVillageObjectTag(house.ID, tag).Fn(w); err != nil {
				return nil, fmt.Errorf("PlaceHomeWard: tag %q: %w", house.ID, err)
			}
			a.Inventory[item]--
			if a.Inventory[item] <= 0 {
				delete(a.Inventory, item)
			}
			text := homeWardPlacedText(item)
			log.Printf("sim/ward: %s placed %s at home %s", a.ID, item, house.ID)
			w.emit(&WardPlaced{ActorID: a.ID, Item: item, ObjectID: house.ID, Text: text, At: now})
			return HomeWardPlacedResult{Item: item, Text: text}, nil
		},
	}
}
