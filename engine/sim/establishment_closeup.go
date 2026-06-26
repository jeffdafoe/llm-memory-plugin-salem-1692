package sim

import (
	"log"
	"sort"
	"time"
)

// establishment_closeup.go — LLM-129. Turning out the house at closing time.
//
// When a live-in keeper beds down for the night (executeNPCSleep, npc_sleep.go),
// the establishment closes. v2 already darkens the building (occupancy.go) and
// gates commerce on a present keeper, but it does NOTHING about bodies left
// standing on the common floor — a customer or passer-by who wandered in stays
// put indefinitely, because the sleep machine only moves the keeper and the
// stranded-actor backstop (idle_backstop_commands.go) exempts anyone inside a
// structure. zbbs-133 dropped the v1 lock-and-evict machine on the assumption
// that "a lingering customer disperses on its own"; live observation showed it
// doesn't (a PC idling in the closed Tavern, keeper asleep in back).
//
// This restores the v1 close-up as a two-beat sequence, scoped to what v2 now
// has that v1 didn't: real tenants (lodger private-room grants LLM-14, keeper
// staff quarters LLM-29), so "evict the non-tenants, the tenants keep their
// rooms" is a well-defined distinction.
//
//	keeper beds down at its own establishment
//	  → announce closing to any non-tenant still inside (a canned Spoke)
//	  → arm a grace timer (establishmentCloseupGrace)
//	  → on fire, walk every remaining non-tenant out to the loiter slot
//	    (MoveActor StructureVisit — the same exit-and-stand-outside path a
//	    normal walk-away takes), unless a keeper re-opened in the meantime.
//
// "Non-tenant" is the complement of structureMembershipAllows (resident / staff
// keeper / owner / lodger) — exactly "who is entitled to be in here", so the
// keeper asleep in the staff room and a lodger asleep in a private room both
// stay; everyone else is turned out.
//
// The grace timer is an in-memory time.AfterFunc (the summon-errand TTL pattern,
// summon.go) — restart-lossy by design: a process restart during the ~5-minute
// window just drops a pending eviction, and the next bed-down re-arms it. No
// durable state, no Postgres (GUIDELINES: transient timers stay in-process).

// establishmentCloseupGrace is how long after the keeper beds down the engine
// waits before turning out the stragglers — the announce-then-wait-then-eject
// margin that lets a lingerer leave under its own power first. Salem game time
// IS wall-clock, so this is five real minutes and five in-world minutes alike.
const establishmentCloseupGrace = 5 * time.Minute

// closingLines is the deterministic vocab pool for the keeper's closing call —
// the engine-authored "we're shut, head home" beat, same class as the retire
// farewell (npc_sleep.go retireLines) and the businessowner hospitality pools.
// Drawn through the narration registry (NarrationKeyEstablishmentClosing), so it
// is LLM-expandable like the others.
var closingLines = []string{
	"We're closing up for the night — off you go now, mind how you go.",
	"That's the house shut for the evening. Time to be heading home.",
	"I'm shutting up now — make your way out, if you'd be so kind.",
	"We're done for the night. Off home with you, now.",
	"Closing time, friends — I'll thank you to be on your way.",
}

// renderClosingLine picks a closing line deterministically from the pool, hashed
// on the keeper plus the bed-down minute — the same no-rand-threaded selection
// renderRetireLine uses, so a given (keeper, minute) is stable for tests yet the
// same keeper doesn't repeat one line every night. Empty when the pool is empty
// (a literal-built world with no narration registry) — the caller then announces
// silently (the eviction still fires). World-goroutine-only.
func (w *World) renderClosingLine(actorID ActorID, now time.Time) string {
	pool := w.narrationDraw(NarrationKeyEstablishmentClosing)
	if len(pool) == 0 {
		return ""
	}
	minute := uint32(now.Unix() / 60)
	idx := (hashActorID(actorID) + minute) % uint32(len(pool))
	return pool[idx]
}

// maybeBeginEstablishmentCloseup starts the close-up sequence when keeper has
// just bedded down AT ITS OWN ESTABLISHMENT — i.e. it is the keeper of the
// structure it is sleeping in (WorkStructureID == InsideStructureID, the live-in
// Tavern/Inn keeper; a lodger bedding at the same inn has a different workplace
// and is filtered out here). Called inline from executeNPCSleep.
//
// A no-op unless the bed-down actually closes the house: another keeper still
// awake and present means the establishment is still open, so nothing is
// announced or armed. And with no non-tenant inside there is no one to address
// or evict, so the announce + timer are skipped — a plain home==work farm whose
// farmer turns in to an empty barn arms nothing.
//
// MUST be called from inside a Command.Fn / inline subscriber (reads world maps,
// emits, arms a timer).
func maybeBeginEstablishmentCloseup(w *World, keeper *Actor, now time.Time) {
	if keeper == nil {
		return
	}
	structureID := keeper.InsideStructureID
	if structureID == "" || keeper.WorkStructureID != structureID {
		return // not the keeper of the place it's sleeping in — not a close-up
	}
	// Another keeper still on the floor and awake → the house is still attended;
	// don't close it out from under a co-keeper. (Excludes the just-bedded keeper,
	// who is already resting and so wouldn't count anyway.)
	if establishmentHasAwakeKeeperPresent(w, structureID, keeper.ID, now) {
		return
	}
	npcIDs, pcIDs := nonTenantOccupants(w, structureID, now)
	if len(npcIDs) == 0 && len(pcIDs) == 0 {
		return // empty house — nothing to announce, nothing to turn out
	}
	announceEstablishmentClosing(w, keeper, npcIDs, pcIDs, now)
	armEstablishmentCloseupEviction(w, structureID, now)
}

// establishmentHasAwakeKeeperPresent reports whether any worker of structureID
// (other than excludeID) is currently inside it AND awake — i.e. the place is
// still attended. A resting keeper (asleep / on break) does not count: the whole
// point of the close-up is that the keeper going to bed shuts the house. Used
// both to gate the close-up at bed-down and to re-check at eviction time (a
// keeper who woke and came back during the grace window re-opened the house).
//
// excludeID skips a specific actor (the just-bedded keeper at trigger time); pass
// "" to consider every worker (the eviction-time re-check). MUST be called from
// inside a Command.Fn.
func establishmentHasAwakeKeeperPresent(w *World, structureID StructureID, excludeID ActorID, now time.Time) bool {
	for id := range w.actorsByStructure[structureID] {
		if id == excludeID {
			continue
		}
		a := w.Actors[id]
		if a == nil || a.WorkStructureID != structureID {
			continue // not a keeper of this structure
		}
		if actorIsResting(a, now) {
			continue // present but asleep / on break — not attending
		}
		return true
	}
	return false
}

// nonTenantOccupants returns the actors currently inside structureID who are NOT
// entitled to remain once it closes — the complement of structureMembershipAllows
// (resident / staff keeper / owner / lodger). Split by kind so the caller can put
// agent NPCs on a Spoke's RecipientIDs (they get a speech warrant and react) and
// PCs on PCBystanderIDs (their talk panel overhears, the engine stamps them no
// warrant). Decorative scenery NPCs and transient nothings are excluded — only a
// PC or an agent NPC is a body worth turning out.
//
// Both slices are sorted by ActorID so the announce recipient set and the
// eviction order are deterministic (the actorsByStructure index is a map). MUST
// be called from inside a Command.Fn (structureMembershipAllows reads w maps).
func nonTenantOccupants(w *World, structureID StructureID, now time.Time) (npcIDs, pcIDs []ActorID) {
	for id := range w.actorsByStructure[structureID] {
		a := w.Actors[id]
		if a == nil {
			continue
		}
		if structureMembershipAllows(w, a, structureID, now) {
			continue // entitled to stay (keeper / lodger / resident / owner)
		}
		switch {
		case a.Kind == KindPC:
			pcIDs = append(pcIDs, id)
		case isAgentNPC(a):
			npcIDs = append(npcIDs, id)
		}
	}
	sort.Slice(npcIDs, func(i, j int) bool { return npcIDs[i] < npcIDs[j] })
	sort.Slice(pcIDs, func(i, j int) bool { return pcIDs[i] < pcIDs[j] })
	return npcIDs, pcIDs
}

// announceEstablishmentClosing emits the keeper's canned closing call to the
// non-tenants still inside. A single engine-authored Spoke (HuddleID empty — a
// room-wide announcement, not a huddle line): the agent NPCs ride RecipientIDs
// so the speech reactor stamps each a warrant and they perceive the call and may
// leave under their own power before the grace runs out; co-present PCs ride
// PCBystanderIDs so their talk panel overhears it. Mirrors the businessowner /
// retire engine-Spoke path — emitted directly so the standard subscribers fan it
// out, and RecordInteraction is deliberately not called (engine boilerplate
// doesn't belong in salient-fact trails).
//
// Silent (no emit) when the narration pool yields no line — the eviction still
// fires; the announcement is the courtesy, not the mechanism. MUST be called
// from inside a Command.Fn.
func announceEstablishmentClosing(w *World, keeper *Actor, npcIDs, pcIDs []ActorID, now time.Time) {
	text := w.renderClosingLine(keeper.ID, now)
	if text == "" {
		return
	}
	recipients := npcIDs
	if recipients == nil {
		recipients = []ActorID{}
	}
	w.emit(&Spoke{
		SpeakerID:      keeper.ID,
		HuddleID:       "",
		RecipientIDs:   recipients,
		PCBystanderIDs: pcIDs,
		Text:           text,
		At:             now,
	})
}

// armEstablishmentCloseupEviction starts the one-shot grace timer for
// structureID. Mirrors armSummonErrandTTL (summon.go): a time.AfterFunc hops back
// onto the world goroutine via SendContext, guarded by LifecycleContext so a
// shutdown-while-armed aborts cleanly instead of parking on a dead cmds channel.
// The fire re-resolves live world state, so no explicit cancel is needed if the
// house re-opens — the eviction Command itself no-ops that case. MUST be called
// from inside a Command.Fn / inline subscriber.
func armEstablishmentCloseupEviction(w *World, structureID StructureID, now time.Time) {
	time.AfterFunc(establishmentCloseupGrace, func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return
		}
		if _, err := w.SendContext(ctx, evictNonTenantsAtClose(structureID, time.Now().UTC())); err != nil && ctx.Err() == nil {
			log.Printf("sim/establishment_closeup: eviction sweep for %s failed: %v", structureID, err)
		}
	})
}

// evictNonTenantsAtClose is the body of the grace timer, run on the world
// goroutine. Level-triggered: it re-reads live state rather than trusting the
// bed-down snapshot. If a keeper is back on the floor and awake — the keeper
// woke (shift start, cap) and re-opened during the grace window — it is a no-op.
// Otherwise it walks every non-tenant still inside out to the establishment's
// loiter slot: a StructureVisit destination resolves to a visitor slot OUTSIDE
// the footprint (commands_move.go resolvePathTarget → pickVisitorSlot), so the
// locomotion ticker paths the actor out through the door and stands it at the
// pin — the same "appear leaving the building, walk to the loiter" motion any
// walk-away takes, no teleport. MoveActor supersedes whatever the actor was
// doing (forceful by design) and wakes an agent NPC that had somehow bedded on
// the common floor.
//
// Returns the count evicted. A per-actor MoveActor failure (no path, etc.) is
// logged and skipped — that body stays inside this pass; one-shot, so it is not
// retried until the next bed-down re-arms.
func evictNonTenantsAtClose(structureID StructureID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if establishmentHasAwakeKeeperPresent(w, structureID, "", now) {
				return 0, nil // re-opened during the grace window — leave everyone be
			}
			npcIDs, pcIDs := nonTenantOccupants(w, structureID, now)
			evicted := 0
			for _, id := range append(npcIDs, pcIDs...) {
				if _, err := MoveActor(id, NewStructureVisitDestination(structureID), true, now).Fn(w); err != nil {
					log.Printf("sim/establishment_closeup: turn out %s from %s: %v", id, structureID, err)
					continue
				}
				evicted++
			}
			return evicted, nil
		},
	}
}
