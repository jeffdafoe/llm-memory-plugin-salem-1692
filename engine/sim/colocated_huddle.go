package sim

import (
	"log"
	"sort"
	"time"
)

// colocated_huddle.go — ZBBS-HOME-358 (PC) + ZBBS-HOME-363 (NPC). The
// explicit-talk huddle bootstrap for an actor standing inside an OPEN structure.
//
// An actor who walks into an open structure forms NO huddle on arrival: the
// arrival-encounter cascade (cascade/arrival_encounter.go) is OUTDOOR-only
// (it skips any arriver with InsideStructureID != "") and forms huddles via
// StartOutdoorHuddle. The indoor counterpart was the explicit talk/knock path —
// but the knock bootstrap only forms a huddle on a KNOCK (owner-only structure,
// non-member; since ZBBS-HOME-445 it runs on arrival, in
// EnsureKnockServiceHuddle); a plain walk-in through an open door joins nobody. So an actor
// standing in the Tavern with others had CurrentHuddleID == "", and sim.Speak
// (audience = huddle peers) either rejected a name-address (the vocative gate
// sees the other as a non-peer → 422) or emitted to no one — and the
// transaction paths (pay / order / scene_quote), which all gate on
// CurrentHuddleID, rejected with "you're not in a conversation."
//
// EnsureColocatedHuddle closes that gap: run from the speak path, it forms the
// conversation ON the talk action. It delegates to JoinHuddle, which
// find-or-creates the single active huddle at a structure (the same primitive
// the knock bootstrap EnsureKnockServiceHuddle uses, called with an empty
// sceneID) — so an actor whose structure already has an active huddle JOINS it
// rather than minting a second.
//
// Scope: the actor must be inside a structure OR standing at a stall's loiter
// point — ZBBS-HOME-378. An owner-only shop (the Blacksmith, etc.) is never
// entered by customers; the owner works inside and customers conduct commerce
// from the loiter point outside, conversing across the threshold. So the
// conversational scope is conversationalScopeStructure: the structure the actor
// is inside, or the stall whose loiter pin it stands within AudienceScopeTiles
// of. A loitering customer joins the owner's structure huddle WITHOUT entering
// (JoinHuddle only sets CurrentHuddleID — it never moves the actor inside).
// General open-ground speech among two outdoor actors with no structure nearby
// would mirror the cascade's StartOutdoorHuddle with a speak radius; that is
// still not handled here.
//
// ZBBS-HOME-363 widened the original PC-only restriction to conversational NPCs
// (stateful + shared). The live Tavern bug: a starving NPC walked in to buy from
// the keeper, but with no huddle every `pay`/`speak` died — there was NO indoor
// NPC huddle-formation path at all (the encounter cascade is outdoor-only;
// EnterOrKnock only fires on an owner-only knock). The original "NPC conversation
// forms through the cascade and the reactor" reasoning held only outdoors. The
// trigger boundary stays tight: this runs only from a deliberate speak (a PC
// click-to-talk or an NPC's own speak tool), only indoors, and is idempotent +
// pulls in only UNHUDDLED co-located actors, so it can't churn or mint a second
// huddle.

// EnsureColocatedHuddle joins actorID (a PC or conversational NPC) into the
// active huddle at the structure it is standing in, together with the other
// co-located conversational actors, when it has no huddle of its own. No-op when
// the actor is missing, not a conversational kind, already in a huddle, not
// inside a structure, or alone inside. Idempotent. MUST run on the world
// goroutine (call inside a Command.Fn).
func EnsureColocatedHuddle(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, nil
			}
			// Conversational kinds only (ZBBS-HOME-363: PC + stateful/shared
			// NPC). A decorative NPC speaking is not a real conversation, so it
			// must not mint a huddle.
			switch actor.Kind {
			case KindPC, KindNPCStateful, KindNPCShared:
			default:
				return nil, nil
			}
			if actor.CurrentHuddleID != "" {
				return nil, nil // already conversing — leave the existing huddle intact
			}
			// ZBBS-HOME-378: scope is the structure the actor stands inside, OR —
			// for a customer at an owner-only stall's loiter point — that stall, so
			// the loitering customer forms/joins the owner's huddle and can transact
			// without ever entering. "" only when neither holds (open ground, no
			// structure within the loiter ring).
			structureID := conversationalScopeStructure(w, actor)
			if structureID == "" {
				return nil, nil
			}
			// LLM-453: an actor that just took its leave of a wound-down conversation
			// here must not immediately re-form or re-join one at the same structure —
			// its own speak (or a housemate's, via the pull below) would otherwise draw
			// it right back into the loop it left. The cooldown is structure-scoped, so
			// it can still converse anywhere else it walks to.
			if dispersedFrom(actor, structureID, now) {
				return nil, nil
			}
			others := colocatedConversationalActors(w, actor, structureID, now)
			// ZBBS-HOME-363: the speaker must also join an ALREADY-ACTIVE
			// structure huddle even when there are no UNHUDDLED co-located
			// actors to pull in. This was the live Tavern bug: John + Ezekiel
			// were already huddled, so colocatedConversationalActors (which
			// excludes already-huddled actors, to avoid leave-first yanking
			// them) returned empty — and the old `len(others) == 0` early
			// return made Prudence bail, never joining the conversation she was
			// standing in, so she could never transact. find-or-create returns
			// that existing huddle, so the speaker joins the people already
			// here. Only bail when there is genuinely nothing to join: no
			// active huddle AND no unhuddled peer to start one with.
			_, hasActiveHuddle := findActiveHuddleAt(w, structureID)
			if len(others) == 0 && !hasActiveHuddle {
				return nil, nil // genuinely alone inside — speak-to-no-one stays valid
			}

			// ZBBS-HOME-375: anchor the indoor huddle to a structure-bound
			// scene so the transaction tools (scene_quote / pay_with_item)
			// can resolve one — they reject "isn't anchored to a scene"
			// otherwise, which killed indoor commerce even with a keeper
			// present in the huddle (the scene check runs before seller
			// resolution). The outdoor path (StartOutdoorHuddle) already
			// mints+attaches an area scene; the indoor explicit-talk huddle
			// previously joined with an empty sceneID and stayed scene-less.
			// find-or-create yields a single durable structure scene per
			// structure, reused across conversations — matching the
			// pay-ledger's model of the scene as context that outlives any
			// one huddle (pay_ledger.go).
			sceneID, sceneErr := findOrCreateStructureScene(w, structureID, now)
			if sceneErr != nil {
				log.Printf("sim: EnsureColocatedHuddle scene for %q: %v", structureID, sceneErr)
				return nil, nil
			}

			// Join the SPEAKER first and bail on failure (code_review): the
			// speaker's join is load-bearing, and joining the others when the
			// speaker stayed out would pollute conversation state among NPCs while
			// the speaker still falls back to speak-to-no-one — worse than not
			// bootstrapping at all. JoinHuddle find-or-creates the structure's
			// active huddle and attaches it to the structure scene resolved above.
			if _, err := JoinHuddle(actor.ID, structureID, sceneID, now).Fn(w); err != nil {
				log.Printf("sim: EnsureColocatedHuddle join speaker %q at %q: %v", actor.ID, structureID, err)
				return nil, nil
			}
			// Pull in each co-located other. JoinHuddle is find-or-create +
			// idempotent, so ordering only affects HuddleJoined/ActorMet "who was
			// already here" payloads. A per-other failure is logged and skipped —
			// the speaker is already in, so the speak reaches whoever did join.
			for _, id := range others {
				if _, err := JoinHuddle(id, structureID, "", now).Fn(w); err != nil {
					log.Printf("sim: EnsureColocatedHuddle join %q at %q: %v", id, structureID, err)
				}
			}
			return nil, nil
		},
	}
}

// conversationalScopeStructure returns the structure an actor is conversationally
// scoped to: the structure it stands inside, or — when it is standing at an OPEN
// stall's loiter point (the commerce position for an owner-only shop, where the
// customer stays outside) — that stall, so a loitering customer is scoped to the
// owner working within. Empty when the actor is neither inside a structure nor
// within AudienceScopeTiles of a loiter pin whose object is conversable.
//
// ZBBS-HOME-378: the engine-side mirror of httpapi.pcAudienceStructure, so the
// speak/huddle WRITE path and the talk-roster READ path agree on who a loitering
// customer can address. (StructureID and VillageObjectID share an id under the
// WORK-342 shared-identity bridge, so the cast is exact.)
//
// LLM-359: the loiter-pin (cross-threshold) branch is gated by
// loiterScopeConversable — a STRUCTURE must be OPEN (a keeper present and awake)
// for its threshold to carry conversation, since a shut shop's wall blocks an
// outside loiterer from whoever is inside (the live case: an idle NPC at a closed
// Tavern's pin greeting the player standing inside, through the wall). A shut shop
// resolves to open-ground scope ("") instead, so no cross-threshold huddle forms
// and the inside occupant never enters the loiterer's audience. The inside-
// structure branch above is untouched: two actors inside the same building
// converse whether it is open or closed.
func conversationalScopeStructure(w *World, a *Actor) StructureID {
	if a.InsideStructureID != "" {
		return a.InsideStructureID
	}
	if id, ok := ResolveLoiteringObject(w.VillageObjects, w.Assets, a.Pos, AudienceScopeTiles); ok &&
		loiterScopeConversable(w, StructureID(string(id))) {
		return StructureID(string(id))
	}
	return ""
}

// InOpenLoiterStructureScope reports whether actor a is standing OUTDOORS at the
// loiter pin of an OPEN worked structure — a shop, inn, workshop, or any building
// whose keeper is present and awake — so a is conversationally scoped across the
// threshold to the keeper working within (conversationalScopeStructure resolves to
// that structure). The arrival-encounter cascade excludes such an actor from an
// open-ground encounter (LLM-375): a second customer walking up must not grab the
// two co-loiterers into a peer huddle that shadows the structure — after which
// neither could resolve the keeper for a pay/quote/greet. Their conversation
// belongs to the keeper's structure huddle, which their own speak/transaction
// forms/joins via EnsureColocatedHuddle. The breadth is intended: the same shadow
// afflicts any open cross-threshold structure, not just an owner-only stall. A SHUT
// structure resolves to "" (LLM-359) and is NOT in scope here, so loiterers at a
// closed shop still meet on open ground as before. Read-only; safe on the world
// goroutine.
func InOpenLoiterStructureScope(w *World, a *Actor) bool {
	if a == nil || a.InsideStructureID != "" {
		return false
	}
	return conversationalScopeStructure(w, a) != ""
}

// loiterScopeConversable reports whether an actor standing at the loiter pin of
// the resolved object sid should be conversationally scoped to it (LLM-359). A
// bare named prop — a well, a shade tree, anything with no Structure entry — has
// no interior and no open/closed state, so its loiter scope always holds: it is
// only a location label, and nobody is ever "inside" it to be reached across a
// wall. A STRUCTURE must be OPEN (a keeper present and awake, keeperPresentAt) for
// its threshold to carry conversation; a shut shop blocks the cross-threshold
// scope. Live-world twin of LoiterScopeConversableInSnapshot — the two must gate a
// shut shop identically or the huddle WRITE scope and the talk-roster READ scope
// drift.
func loiterScopeConversable(w *World, sid StructureID) bool {
	if w.Structures[sid] == nil {
		return true
	}
	return keeperPresentAt(w, sid)
}

// LoiterScopeConversableInSnapshot is loiterScopeConversable over a published
// Snapshot — the read-path (httpapi pcAudienceStructure) twin. assets is the
// reference catalog keeperPresentInSnapshot needs (the snapshot doesn't carry it
// inline). A nil snapshot or non-structure resolved object keeps the loiter scope
// (nothing to gate); a real structure must be open.
func LoiterScopeConversableInSnapshot(snap *Snapshot, assets map[AssetID]*Asset, sid StructureID) bool {
	if snap == nil || snap.Structures[sid] == nil {
		return true
	}
	return keeperPresentInSnapshot(snap, assets, sid)
}

// evictLoiterMembersOnClose is the teardown twin of the LLM-359 formation gate
// (loiterScopeConversable): when a structure's shop closes mid-conversation — a
// keeper beds down / leaves post so keeperPresentAt flips false — any huddle
// member conversing across its threshold from the loiter pin loses the open-shop
// scope that let it join. LLM-359 gates cross-threshold huddle FORMATION on the
// shop being open but does nothing to one already formed: perception feeds a
// huddled actor its peers straight from the roster (perception/build.go), with no
// closed-shop re-check, so a stranded loiterer keeps perceiving and addressing
// whoever is inside through the shut wall (the live case: an NPC at the pin
// talking to a lodging PC through a closed-for-the-night tavern's door, LLM-360).
//
// Evicts each active-huddle member standing OUTSIDE the structure
// (InsideStructureID != structureID) — the cross-threshold loiter members — and
// leaves members physically inside alone: two actors inside a closed building
// still converse, the same boundary LLM-359 draws. leaveCurrentHuddle concludes
// the huddle if the eviction empties it and handles the degenerate
// lone-resting-member case.
//
// A fast no-op when the shop is still open (keeperPresentAt — another keeper still
// on post), the structure has no active huddle, or no member is cross-threshold.
// Call AFTER the presence-ending mutation is applied (in executeNPCSleep, after
// State is set to StateSleeping) so keeperPresentAt reads the post-close state.
// MUST run on the world goroutine.
//
// The CALLER must invoke this only for a genuine shop-close — the sleeper being a
// worker of the structure it beds down in (WorkStructureID == InsideStructureID),
// the one actor whose bed-down can flip keeperPresentAt. keeperPresentAt is
// trivially false for a never-keeper-gated structure (a private home answering a
// knock), so calling this on any keeperless structure would wrongly evict a
// legitimate cross-threshold visitor.
//
// Scope: the live-in bed-down close (executeNPCSleep). A keeper who instead walks
// off-shift makes an owner-only shop shut, but customers never enter those, so no
// one is left inside for a stranded loiterer to reach through the wall — that path
// is deliberately not hooked here.
func evictLoiterMembersOnClose(w *World, structureID StructureID, now time.Time) {
	if structureID == "" || keeperPresentAt(w, structureID) {
		return
	}
	huddleID, ok := findActiveHuddleAt(w, structureID)
	if !ok {
		return
	}
	huddle := w.Huddles[huddleID]
	if huddle == nil {
		return
	}
	// Snapshot the cross-threshold ids before mutating — leaveCurrentHuddle deletes
	// from huddle.Members, so ranging it live would be unsafe. Sorted for a
	// deterministic eviction order (and reproducible tests).
	var strandedOutside []ActorID
	for id := range huddle.Members {
		a := w.Actors[id]
		if a == nil {
			continue // nil/stale roster entry — no actor to evict; skip
		}
		if a.InsideStructureID != structureID {
			strandedOutside = append(strandedOutside, id)
		}
	}
	sort.Slice(strandedOutside, func(i, j int) bool { return strandedOutside[i] < strandedOutside[j] })
	for _, id := range strandedOutside {
		a := w.Actors[id]
		// Re-check membership: an earlier eviction may have concluded the huddle out
		// from under this id, or leaveCurrentHuddle's lone-resting-member sweep may
		// have already dropped it.
		if a == nil || a.CurrentHuddleID != huddleID {
			continue
		}
		leaveCurrentHuddle(w, a, now)
	}
}

// colocatedConversationalActors returns the ids (sorted) of conversational,
// currently-unhuddled actors other than self inside structureID. Conversational
// = a stateful/shared NPC or a PC, not asleep. Decorative NPCs and sleepers are
// excluded. Sorted for deterministic huddle-join order and reproducible tests.
//
// CurrentHuddleID == "" is REQUIRED (code_review, ZBBS-HOME-358): JoinHuddle is
// leave-first, so pulling an actor who is ALREADY in a huddle (a knock service-
// huddle with a keeper, another PC's talk-huddle, a not-yet-cleared outdoor
// huddle) into this speaker's huddle would yank them out of their existing
// conversation. We only pull in genuinely unattached co-located actors; an
// already-conversing actor is left alone. (The speaker itself is guaranteed
// unhuddled by EnsureColocatedHuddle's early return, so its own find-or-create
// join is safe.)
func colocatedConversationalActors(w *World, self *Actor, structureID StructureID, now time.Time) []ActorID {
	staleAfter := PCPresenceStaleAfter(w)
	var out []ActorID
	for id, a := range w.Actors {
		if id == self.ID || a == nil {
			continue
		}
		if a.InsideStructureID != structureID {
			continue
		}
		if a.CurrentHuddleID != "" {
			continue // already conversing — never leave-first them out (code_review)
		}
		if self.Kind != KindPC && dispersedFrom(a, structureID, now) {
			// Just took its leave of this structure's conversation — don't let
			// ANOTHER NPC's speak re-pull it into the loop it left (LLM-453). A PC
			// speaker is exempt: a player may always re-engage a dispersed NPC, so
			// the cooldown never blocks player-facing conversation or commerce.
			continue
		}
		if !colocatedConversational(a, now, staleAfter) {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// colocatedConversationalKind reports whether a is a conversational kind that is
// present enough to converse — a stateful/shared NPC, or a PC whose presence
// stamp is still fresh (a closed-tab player whose stamp has gone stale must not
// be resurrected into a conversation, ZBBS-WORK-326 / code_review). Sleep state
// is deliberately NOT considered here: the awake-audience scan and the
// asleep-co-presence scan share this kind/presence rule and each adds its own
// sleep test, so the two can't drift on who counts as a conversational peer
// (ZBBS-WORK-426).
func colocatedConversationalKind(a *Actor, now time.Time, staleAfter time.Duration) bool {
	if a == nil {
		return false
	}
	switch a.Kind {
	case KindNPCStateful, KindNPCShared:
		return true
	case KindPC:
		return !PCPresenceStale(a.LastPCSeenAt, now, staleAfter) // absent player — do not pull into a huddle
	default:
		return false // decorative / unknown
	}
}

// colocatedConversational reports whether a can be pulled into a co-located
// huddle: a conversational, present actor (colocatedConversationalKind) that is
// also awake. A sleeper is excluded from the audience — it can't hold up its end
// of a conversation — and is surfaced separately by colocatedSleeperIDs so
// perception can mark it "(asleep)" rather than dropping it entirely
// (ZBBS-WORK-426).
func colocatedConversational(a *Actor, now time.Time, staleAfter time.Duration) bool {
	return colocatedConversationalKind(a, now, staleAfter) && a.State != StateSleeping
}

// pcBystanders (ZBBS-HOME-437) returns the ids (sorted) of PCs within earshot
// of a speaker who are NOT in peerSet (huddle members already receive the
// Spoke via RecipientIDs) — the wire-frame overhearing audience carried on
// Spoke.PCBystanderIDs.
//
// Earshot:
//   - structure scope: the PC's conversationalScopeStructure equals the
//     speaker's (inside↔inside, inside↔loiter-pin, loiter↔loiter at the same
//     stall) AND the room subspaces match — common-room speech stays out of
//     bedrooms and vice versa, mirroring the client's v1 room filter.
//   - open ground (neither has a structure scope): Chebyshev within
//     OutdoorEarshotTiles, the same radius the client's outdoor filter and
//     the talk roster use.
//
// Deliberately NOT gated on sleep or PC-presence staleness: this list only
// widens a broadcast frame's render audience. A sleeping player still gets the
// room's chatter in their log (the sleep gate on colocatedConversational is
// about huddle MEMBERSHIP — never drag a sleeper into a conversation), and a
// stale PC's client has no socket to render on, so inclusion is inert. No
// engine-side consumer reads this list, so nothing else can over-trigger.
func pcBystanders(w *World, speaker *Actor, peerSet map[ActorID]struct{}) []ActorID {
	speakerScope := conversationalScopeStructure(w, speaker)
	var out []ActorID
	for id, a := range w.Actors {
		if a == nil || id == speaker.ID || a.Kind != KindPC {
			continue
		}
		if _, isPeer := peerSet[id]; isPeer {
			continue
		}
		if speakerScope != "" {
			if conversationalScopeStructure(w, a) != speakerScope {
				continue
			}
			if audienceRoomScope(w, a) != audienceRoomScope(w, speaker) {
				continue
			}
		} else {
			// Open ground means NEITHER side has a structure scope — a PC
			// loitering at a stall's pin is conversationally scoped to that
			// stall (it hears the stall, not the road), so this must check
			// the full conversationalScopeStructure, not just
			// InsideStructureID (code_review).
			if conversationalScopeStructure(w, a) != "" {
				continue
			}
			if speaker.Pos.Chebyshev(a.Pos) > OutdoorEarshotTiles {
				continue
			}
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// audienceRoomScope returns the room id that scopes an actor's speech
// audience, or 0 for public scope. Only a private/staff room scopes — a
// common room, an unknown/stale room id, and outdoors all resolve to public
// (0), so two actors in the same structure's public space match even when one
// carries the common room's id and the other carries none. The live-world
// mirror of httpapi.pcAudienceRoom / v1's actorPrivateRoomScope; failing open
// to public is the safe direction (slightly-over-heard speech beats speech
// that vanishes for the right audience). ZBBS-HOME-437.
func audienceRoomScope(w *World, a *Actor) RoomID {
	if a.InsideRoomID == 0 || a.InsideStructureID == "" {
		return 0
	}
	st := w.Structures[a.InsideStructureID]
	if st == nil {
		return 0
	}
	for _, rm := range st.Rooms {
		if rm == nil || rm.ID != a.InsideRoomID {
			continue
		}
		if rm.Kind == RoomKindPrivate || rm.Kind == RoomKindStaff {
			return a.InsideRoomID
		}
		return 0
	}
	return 0
}

// colocatedHuddleSceneOrigin is the Scene.OriginKind stamped on the
// structure-bound scene that anchors an indoor explicit-talk huddle
// (ZBBS-HOME-375). The outdoor counterpart is outdoorEncounterOriginKind.
const colocatedHuddleSceneOrigin = "colocated_talk"

// findOrCreateStructureScene returns the SceneID of the structure-bound
// scene for structureID, minting one (origin colocatedHuddleSceneOrigin)
// when none exists yet. This is the only minter of indoor structure scenes
// and it never re-mints when one is found, so a structure accrues at most
// one durable commerce-context scene, reused across conversations — the
// bounded-accumulation property the persist (vs conclude-on-orphan)
// lifecycle choice rests on, and a match for the pay-ledger's "scene
// persists across huddle churn" model (pay_ledger.go). The lexicographically
// smallest match is returned for determinism in the not-expected event of
// stale duplicates, mirroring resolveSellerScene. MUST run on the world
// goroutine.
func findOrCreateStructureScene(w *World, structureID StructureID, now time.Time) (SceneID, error) {
	var found SceneID
	for id, scene := range w.Scenes {
		if scene == nil || scene.Bound.Kind != SceneBoundStructure {
			continue
		}
		if scene.Bound.StructureID == nil || *scene.Bound.StructureID != structureID {
			continue
		}
		if found == "" || id < found {
			found = id
		}
	}
	if found != "" {
		return found, nil
	}
	sceneAny, err := CreateScene(colocatedHuddleSceneOrigin, NewStructureBound(structureID), now).Fn(w)
	if err != nil {
		return "", err
	}
	return sceneAny.(SceneID), nil
}

// colocatedAudienceIDs returns the conversational actors an UNHUDDLED speaker
// would reach if it spoke from its current position right now — a sorted,
// self-excluded id slice. It is the non-mutating read mirror of the huddle the
// speak path forms on a speak: EnsureColocatedHuddle joins the speaker into the
// structure's huddle (pulling in co-located unhuddled actors), and the audience
// is then that huddle's peers (buildHuddlePeerSet). Surfacing it in perception
// (ZBBS-WORK-407) lets the "## Around you" co-presence line and the speak
// "there is no one here to hear you" gate derive from ONE scope rule — both go
// through conversationalScopeStructure + colocatedConversationalActors, so they
// cannot drift.
//
// An empty result means the speaker is genuinely alone in scope, so a speak
// would trip the no-audience gate. Open ground with no stall loiter-scope
// (structureID == "") is always empty, matching EnsureColocatedHuddle's own bail.
//
// Only meaningful for an UNHUDDLED speaker: a huddled actor's audience is its
// existing huddle peers (already surfaced by the huddle roster), so the caller
// (republish) computes this only when CurrentHuddleID == "". MUST run on the
// world goroutine.
func colocatedAudienceIDs(w *World, speaker *Actor, now time.Time) []ActorID {
	structureID := conversationalScopeStructure(w, speaker)
	if structureID == "" {
		return nil
	}
	seen := make(map[ActorID]struct{})
	var out []ActorID
	add := func(id ActorID) {
		if id == speaker.ID {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	// The unhuddled co-located conversationalists EnsureColocatedHuddle would pull
	// into the huddle on the speak.
	for _, id := range colocatedConversationalActors(w, speaker, structureID, now) {
		add(id)
	}
	// Anyone ALREADY in an active huddle at this structure: EnsureColocatedHuddle
	// joins the speaker into that huddle (find-or-create), so its members are part
	// of the reachable audience too. colocatedConversationalActors deliberately
	// skips already-huddled actors (to avoid leave-first yanking them), so this is
	// the arm that covers the live "walk into a room where two are already talking"
	// case. Read from actorsByHuddle — the same membership index buildHuddlePeerSet
	// uses to commit the speak audience.
	if hid, ok := findActiveHuddleAt(w, structureID); ok {
		for id := range w.actorsByHuddle[hid] {
			add(id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// colocatedSleeperIDs returns the ids (sorted, self-excluded) of co-present
// SLEEPING conversational actors in the speaker's structure scope — the asleep
// counterpart to colocatedAudienceIDs. The audience scan omits sleepers (not a
// valid speak target, and they don't count toward the no-audience gate), which
// used to make a sleeping co-present actor vanish from the speaker's
// "## Around you" entirely: the speaker couldn't tell anyone was there and
// addressed them anyway, reading the silence as rudeness (ZBBS-WORK-426,
// residual of HOME-436). This surfaces them so perception can mark them
// "(asleep)"; they stay OUT of colocatedAudienceIDs, so the speak audience and
// its no-audience gate are unchanged.
//
// Same scope rule as colocatedAudienceIDs (conversationalScopeStructure) and the
// same kind/presence gate (colocatedConversationalKind), so a sleeper is surfaced
// exactly where an awake peer would have been a valid audience member.
// Already-huddled actors are skipped to match the audience scan (a sleeper has
// left its huddle on bedding, HOME-435, so this is belt-and-suspenders). MUST run
// on the world goroutine.
func colocatedSleeperIDs(w *World, speaker *Actor, now time.Time) []ActorID {
	structureID := conversationalScopeStructure(w, speaker)
	if structureID == "" {
		return nil
	}
	staleAfter := PCPresenceStaleAfter(w)
	var out []ActorID
	for id, a := range w.Actors {
		if id == speaker.ID || a == nil {
			continue
		}
		if a.InsideStructureID != structureID {
			continue
		}
		if a.CurrentHuddleID != "" {
			continue
		}
		if a.State != StateSleeping {
			continue
		}
		if !colocatedConversationalKind(a, now, staleAfter) {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
