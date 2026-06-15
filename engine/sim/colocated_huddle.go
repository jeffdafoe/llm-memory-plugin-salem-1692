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
// scoped to: the structure it stands inside, or — when it is standing at a stall's
// loiter point (the commerce position for an owner-only shop, where the customer
// stays outside) — that stall, so a loitering customer is scoped to the owner
// working within. Empty when the actor is neither inside a structure nor within
// AudienceScopeTiles of a named object's loiter pin.
//
// ZBBS-HOME-378: the engine-side mirror of httpapi.pcAudienceStructure, so the
// speak/huddle WRITE path and the talk-roster READ path agree on who a loitering
// customer can address. (StructureID and VillageObjectID share an id under the
// WORK-342 shared-identity bridge, so the cast is exact.)
func conversationalScopeStructure(w *World, a *Actor) StructureID {
	if a.InsideStructureID != "" {
		return a.InsideStructureID
	}
	if id, ok := ResolveLoiteringObject(w.VillageObjects, w.Assets, a.Pos, AudienceScopeTiles); ok {
		return StructureID(string(id))
	}
	return ""
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
		if !colocatedConversational(a, now, staleAfter) {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// colocatedConversational reports whether a can be pulled into a co-located
// huddle: a conversational kind (stateful/shared NPC or PC) that is not asleep,
// and — for a PC — not stale/absent (a closed-tab player whose presence stamp
// has gone stale must not be resurrected into a conversation, ZBBS-WORK-326 /
// code_review).
func colocatedConversational(a *Actor, now time.Time, staleAfter time.Duration) bool {
	if a == nil {
		return false
	}
	switch a.Kind {
	case KindNPCStateful, KindNPCShared:
		// conversational NPC kinds
	case KindPC:
		if PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
			return false // absent player — do not pull into a huddle
		}
	default:
		return false // decorative / unknown
	}
	return a.State != StateSleeping
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
