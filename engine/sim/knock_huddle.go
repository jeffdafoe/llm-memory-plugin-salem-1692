package sim

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"time"
)

// knock_huddle.go — ZBBS-HOME-445. The arrival-time half of the ZBBS-101
// knock: EnterOrKnock routes an owner-only non-member to the loiter slot
// with the visit destination stamped Knock=true, and when that walk
// ARRIVES, this command forms the across-the-doorway service huddle with
// whoever is inside to receive it.
//
// The join deliberately does not happen at click time. A mid-walk huddle
// membership is destroyed by the locomotion ticker's mover-leave rule
// (ZBBS-HOME-340) within a tick, and the businessowner farewell cascade
// read that eviction as a customer departure — a keeper said "Until next
// time" to a customer still walking IN, and the knocker arrived
// huddle-less with the keeper stranded in a one-member huddle. The
// arrived knocker is stationary, so nothing evicts an arrival-formed
// membership: the ticker rule only touches movers, and the drift check
// (whose structure-scene bound a doorway knocker can never satisfy) only
// runs after a position mutation.
//
// Receiver scope is insideAssociatedActors — home OR work anchor — NOT
// the businessowner-only gates of EnsureArrivalBusinessHuddle. A knock on
// a private home with the resident in must still open the conversation,
// exactly as the click-time join always had it; only shops get the
// hospitality greet on top (the greet subscriber applies its own keeper
// gates to the HuddleJoined this emits).
//
// Join order is load-bearing, same as EnsureArrivalBusinessHuddle:
// receivers FIRST, the knocker LAST, so the knocker's HuddleJoined
// carries the receivers in OtherMembers and the businessowner greet
// fires for a staffed shop — the knock now ends in "Welcome, {customer}"
// instead of the spurious farewell.

// receptiveKnockReceivers returns the actors inside structureID who are
// associated with it (home or work anchor) and available to answer a
// knock. A sleeping or resting receiver is no receiver — the door stays
// unanswered. A MOVING receiver (MoveIntent in flight — a keeper heading
// out the door) is no receiver either: joining a walker would hand the
// HOME-340 mover-leave rule a fresh membership to evict, recreating the
// phantom-farewell bug on the receiver's side (code_review). Sorted for
// deterministic join order.
//
// An already-huddled receiver IS receptive — they're mid-conversation at
// this structure (with another customer), and the knocker joins that same
// huddle; JoinHuddle is idempotent for them.
//
// Shared by EnterOrKnock (click-time "no one answers" narration) and
// EnsureKnockServiceHuddle (the arrival join), so the click-time
// prediction and the arrival outcome apply the same standard.
func receptiveKnockReceivers(w *World, structureID StructureID) []ActorID {
	var ids []ActorID
	for _, id := range insideAssociatedActors(w, structureID) {
		a := w.Actors[id]
		if a == nil || a.State == StateSleeping || a.State == StateResting || a.MoveIntent != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// EnsureKnockServiceHuddle joins the arrived knocker and the structure's
// receptive receivers into the structure huddle. All gating runs against
// LIVE world state — a stale ActorArrived degrades to a no-op rather than
// acting on event coordinates (same posture as EnsureArrivalBusinessHuddle).
func EnsureKnockServiceHuddle(actorID ActorID, structureID StructureID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, nil
			}
			// Conversational kinds only — mirrors the arrival-business gate.
			switch actor.Kind {
			case KindPC, KindNPCStateful, KindNPCShared:
			default:
				return nil, nil
			}
			if actor.CurrentHuddleID != "" {
				return nil, nil // already conversing
			}
			// Stale-arrival guards: the knocker must still be standing at the
			// door — outside any structure and not already walking somewhere
			// else (a new click supersedes the MoveIntent, and the abandoned
			// knock must not yank the player back into a doorway huddle).
			if structureID == "" || actor.InsideStructureID != "" || actor.MoveIntent != nil {
				return nil, nil
			}
			if _, ok := w.Structures[structureID]; !ok {
				return nil, nil
			}
			// A ghost PC (closed tab, stale presence stamp) must not knock —
			// same gate as the encounter cascade (ZBBS-WORK-326).
			if actor.Kind == KindPC && PCPresenceStale(actor.LastPCSeenAt, now, PCPresenceStaleAfter(w)) {
				return nil, nil
			}

			receivers := receptiveKnockReceivers(w, structureID)
			if len(receivers) == 0 {
				// The door goes unanswered — no lone-knocker huddle; the knocker is
				// told so now, after the knock (LLM-784).
				narrateUnansweredKnock(w, actor, structureID, now)
				return nil, nil
			}

			// Same scene anchoring as the other huddle bootstraps — the
			// transaction tools need it (ZBBS-HOME-375).
			sceneID, sceneErr := findOrCreateStructureScene(w, structureID, now)
			if sceneErr != nil {
				log.Printf("sim: EnsureKnockServiceHuddle scene for %q: %v", structureID, sceneErr)
				return nil, nil
			}

			// Receivers first, knocker last (greet ordering — see file
			// comment). JoinHuddle is idempotent for a receiver already in
			// the structure's active huddle (e.g. mid-conversation with
			// another customer), so this never churns an existing huddle.
			// Each earlier join emits synchronously (subscribers dispatch
			// inline), so re-check a receiver's availability right before
			// its own join — a cascade reaction to a prior join must not
			// pull in a receiver that just stopped qualifying (code_review).
			joined := 0
			for _, id := range receivers {
				a := w.Actors[id]
				if a == nil || a.InsideStructureID != structureID ||
					a.State == StateSleeping || a.State == StateResting || a.MoveIntent != nil {
					continue
				}
				if _, err := JoinHuddle(id, structureID, sceneID, now).Fn(w); err != nil {
					log.Printf("sim: EnsureKnockServiceHuddle join receiver %q at %q: %v", id, structureID, err)
					continue
				}
				joined++
			}
			// Every receiver dropped out between the pre-check and its join
			// (or all joins failed) — don't mint a lone-knocker huddle.
			if joined == 0 {
				narrateUnansweredKnock(w, actor, structureID, now)
				return nil, nil
			}
			if _, err := JoinHuddle(actor.ID, structureID, sceneID, now).Fn(w); err != nil {
				log.Printf("sim: EnsureKnockServiceHuddle join knocker %q at %q: %v", actor.ID, structureID, err)
			}
			return nil, nil
		},
	}
}

// knockUnansweredText is what a player hears when nobody comes to the door.
const knockUnansweredText = "You knock, but the door is shut fast and no one answers."

// KnockUnansweredNarrated tells a player their knock went unanswered
// (LLM-784): emitted on ARRIVAL at the door, after the knock, when nobody inside
// can answer — not at the click, when it would only be a guess about who will
// be in by the time they get there. TranslateEvent sends it as a PRIVATE
// room_event addressed to that player; the client renders it in the talk panel
// and over the house.
type KnockUnansweredNarrated struct {
	EventBase
	ActorID     ActorID
	StructureID StructureID
	Text        string
	At          time.Time
}

func (KnockUnansweredNarrated) isSimEvent() {}

// narrateUnansweredKnock emits KnockUnansweredNarrated for a player knocker.
// A villager knocking needs no line; their next turn reads the closed door.
func narrateUnansweredKnock(w *World, actor *Actor, structureID StructureID, now time.Time) {
	if actor.Kind != KindPC {
		return
	}
	w.emit(&KnockUnansweredNarrated{ActorID: actor.ID, StructureID: structureID, Text: knockUnansweredText, At: now})
}

// ErrKnockRefused: a knock that cannot happen — the caller is not standing at
// that door, may walk straight in, or the structure has no door to knock on.
var ErrKnockRefused = errors.New("you cannot knock here")

// KnockResult answers KnockAtDoor: whether someone came to the door, and —
// when nobody did — who of the house a note may be left for (LLM-19).
type KnockResult struct {
	Answered bool
	NoteFor  []DoorNoteRecipient
}

// KnockAtDoor is the player's knock (LLM-785, the door popup's Knock button →
// POST /pc/knock). The click on a house they may not enter only walks them to
// its door; standing there, they choose to knock. Then it goes as the knock
// always has: whoever inside is awake comes to the door (the doorway
// conversation, EnsureKnockServiceHuddle) or nobody does
// (KnockUnansweredNarrated).
//
// Refused (ErrKnockRefused) unless the player is outdoors and still at the
// door of structureID (AtDoorOf, not walking), and it is a door they would
// have to knock on — owner-only, they are not a member, it has a door, and it
// is not a market stall (mustKnockAt, EnterOrKnock's own test). A player already in another conversation leaves
// it to knock; one already in the doorway conversation has been answered.
func KnockAtDoor(actorID ActorID, structureID StructureID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor := w.Actors[actorID]
			if actor == nil || actor.Kind != KindPC {
				return nil, fmt.Errorf("KnockAtDoor: %q is not a player's character", actorID)
			}
			if err := mayKnockHere(w, actor, structureID, now); err != nil {
				return nil, err
			}
			if actorInActiveHuddle(w, actor) {
				if doorConversationAt(w, actor, structureID) {
					return KnockResult{Answered: true}, nil
				}
				leaveCurrentHuddle(w, actor, now)
			}
			if _, err := EnsureKnockServiceHuddle(actorID, structureID, now).Fn(w); err != nil {
				return nil, err
			}
			if actor.CurrentHuddleID != "" {
				return KnockResult{Answered: true}, nil
			}
			return KnockResult{NoteFor: doorNoteRecipients(w, structureID)}, nil
		},
	}
}

// doorConversationAt reports whether actor's conversation already has someone
// of structureID inside it — the door was answered.
func doorConversationAt(w *World, actor *Actor, structureID StructureID) bool {
	h := w.Huddles[actor.CurrentHuddleID]
	if h == nil {
		return false
	}
	for id := range h.Members {
		if other := w.Actors[id]; other != nil && id != actor.ID && other.InsideStructureID == structureID {
			return true
		}
	}
	return false
}
