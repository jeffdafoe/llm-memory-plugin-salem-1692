package sim

import (
	"fmt"
	"strings"
	"time"
)

// let_in.go — LLM-20. A resident lets a visitor in. Someone who may not enter
// an owner-only home and knocks (EnterOrKnock) stands at its door in a
// conversation across the doorway with whoever is in (EnsureKnockServiceHuddle).
// A resident or the owner inside may let them in: the visitor gets a guest pass
// (GuestPassDuration) — one more leg of structureMembershipAllows — and steps
// through the door at once.
//
// The step in is a snap onto the door tile, not a walk: a walker leaves their
// conversation (the locomotion ticker's mover-leave rule, ZBBS-HOME-340), and
// nothing forms one inside a home, so a walk in would end the talk at the
// threshold. Snapped inside, the visitor is within the conversation's
// structure scene and stays in it.
//
// The pass only decides entry. Nothing turns a guest out when it runs out; it
// lets them walk back in a while after stepping out.

// GuestPassDuration is how long a let-in visitor may come and go.
const GuestPassDuration = 30 * time.Minute

// GuestPass is a let-in visitor's leave to enter a home: who let them in and
// until when.
type GuestPass struct {
	Host  ActorID
	Until time.Time
}

// LetInResult answers LetIn: who stepped in, and where.
type LetInResult struct {
	VisitorID   ActorID
	VisitorName string
	StructureID StructureID
}

// guestPassAt reports whether actorID holds an unexpired pass for structureID.
func guestPassAt(w *World, actorID ActorID, structureID StructureID, now time.Time) bool {
	pass, ok := w.GuestPasses[structureID][actorID]
	return ok && now.Before(pass.Until)
}

// mayLetIn reports whether host may let people into structureID: they are
// inside it, and it is their home or theirs to own. Staff, lodgers, hired
// hands and guests may come and go but not open the door to others.
func mayLetIn(w *World, host *Actor, structureID StructureID) bool {
	if host == nil || structureID == "" || host.InsideStructureID != structureID {
		return false
	}
	if host.HomeStructureID == structureID {
		return true
	}
	vobj, _, ok := villageObjectForStructure(w, structureID)
	return ok && vobj.OwnerActorID != "" && vobj.OwnerActorID == host.ID
}

// AtDoorOf reports whether someone standing outdoors at pos is at the door of
// structureID — at its loiter pin, where a knock stops. Pure over its map
// inputs so the live world and the published snapshot ask the same question.
func AtDoorOf(objects map[VillageObjectID]*VillageObject, assets map[AssetID]*Asset, insideStructureID StructureID, pos TilePos, structureID StructureID) bool {
	return insideStructureID == "" && ActorAtObjectPin(objects, assets, pos, VillageObjectID(structureID))
}

// LetIn returns the Command for hostID letting visitorName in: the visitor
// must be in the host's conversation, outdoors at the door of the home the
// host is in. They are snapped onto the door tile, inside, and — unless already
// allowed in — get a GuestPass. Nothing changes unless the step in succeeds.
func LetIn(hostID ActorID, visitorName string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			host := w.Actors[hostID]
			if host == nil {
				return nil, fmt.Errorf("LetIn: actor %q not found", hostID)
			}
			sid := host.InsideStructureID
			if !mayLetIn(w, host, sid) {
				return nil, ModelFacingError{Msg: "you can only let someone into your own home, from inside it."}
			}
			vobj, _, ok := villageObjectForStructure(w, sid)
			if !ok {
				return nil, fmt.Errorf("LetIn: structure %q has no placement", sid)
			}
			if effectiveEntryPolicy(w, sid, vobj, now) != EntryPolicyOwner {
				return nil, ModelFacingError{Msg: "anyone may walk in here already."}
			}
			visitor := doorstepVisitorNamed(w, host, sid, visitorName)
			if visitor == nil {
				return nil, ModelFacingError{Msg: strings.TrimSpace(visitorName) + " is not at your door."}
			}
			// Preflight before any change: the door tile must be inside this
			// house, or the step in would leave the visitor outside.
			entry, ok := structureEntryTile(w, sid)
			if !ok {
				return nil, fmt.Errorf("LetIn: structure %q has no door tile", sid)
			}
			if at, inside := structureContainingTile(w, entry); !inside || at != sid {
				return nil, fmt.Errorf("LetIn: the door tile of %q is not inside it", sid)
			}
			// Someone already allowed in (staff, a lodger, a guest still on their
			// pass) is simply stepped in; anyone else needs a pass. Decided before
			// the step, which moves them.
			needsPass := !structureMembershipAllows(w, visitor, sid, now)
			if _, err := SetActorPosition(visitor.ID, entry, now).Fn(w); err != nil {
				return nil, fmt.Errorf("LetIn: step %q in: %w", visitor.ID, err)
			}
			if visitor.InsideStructureID != sid {
				return nil, fmt.Errorf("LetIn: %q is not inside %q after stepping in", visitor.ID, sid)
			}
			if needsPass {
				if w.GuestPasses == nil {
					w.GuestPasses = map[StructureID]map[ActorID]GuestPass{}
				}
				if w.GuestPasses[sid] == nil {
					w.GuestPasses[sid] = map[ActorID]GuestPass{}
				}
				w.GuestPasses[sid][visitor.ID] = GuestPass{Host: host.ID, Until: now.Add(GuestPassDuration)}
			}
			return LetInResult{VisitorID: visitor.ID, VisitorName: visitor.DisplayName, StructureID: sid}, nil
		},
	}
}

// doorstepVisitorNamed: of the members of host's active conversation standing
// at the door of structureID, the one named name (case and outer spaces
// ignored) when exactly one has that name — or, when exactly one stands there
// at all, that one whatever the name: a resident who does not know the knocker
// sees "a stranger" at the door and has no name to give. Nil otherwise (two at
// the door sharing the name, or several and none named): the members map has
// no order, so a pick among equals would be arbitrary.
func doorstepVisitorNamed(w *World, host *Actor, structureID StructureID, name string) *Actor {
	h, ok := w.Huddles[host.CurrentHuddleID]
	if !ok || h.ConcludedAt != nil {
		return nil
	}
	want := strings.TrimSpace(name)
	var atDoor, named []*Actor
	for id := range h.Members {
		a := w.Actors[id]
		if id == host.ID || a == nil || !AtDoorOf(w.VillageObjects, w.Assets, a.InsideStructureID, a.Pos, structureID) {
			continue
		}
		atDoor = append(atDoor, a)
		if strings.EqualFold(strings.TrimSpace(a.DisplayName), want) {
			named = append(named, a)
		}
	}
	if len(named) == 1 {
		return named[0]
	}
	if len(atDoor) == 1 {
		return atDoor[0]
	}
	return nil
}
