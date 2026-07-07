package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// move_to.go — the move_to agent tool's substrate half, ZBBS-HOME-285.
//
// What move_to is: the agent NPC's self-directed movement verb. The model
// names a structure it can see in its perception (its own home/work, a shop,
// the meeting house) and the engine walks it there. It is the wired-tool
// surface over the EXISTING MoveActor command (commands_move.go) — move_to
// builds nothing new about locomotion; it derives the right arrival kind and
// delegates.
//
// Why it exists: v2 agent NPCs could talk and transact but had no movement
// verb (MoveActor existed but was never in registerTools). This is the agent
// half of work's shift/duty producer seam — a "head to your workplace / go
// home" duty warrant is inert without a tool to act on. Arrival fires
// ActorArrived, which is what closes the auto-sleep loop
// (handleAutoSleepOnArrival, npc_sleep.go): an off-shift NPC that move_to's
// home ENTERS, lands InsideStructureID == HomeStructureID, and is bedded.
//
// Enter-vs-visit is DERIVED here, not passed by the model — see
// moveToDestinationFor. The model-facing tool stays a single structure_id arg;
// the engine picks enter or visit the same way the PC-move client chooses a
// kind, but without making the model reason about it.
//
// Terminal: move_to ends the tick (registered terminalOnSuccess=true). A model
// that wants to announce its exit does speak FIRST ("heading to the smithy
// now", to its current companions, broadcast at the FROM location) then
// move_to to end the turn — the speak-then-move ordering v1 enforced
// (ZBBS-HOME-237), which avoids a post-move speak landing at the room the
// actor just left. Confirmed with work for the duty-warrant seam.

// MoveToStructureByName returns a Command that resolves a place NAME to a
// structure_id and walks there exactly as MoveToStructure does. It exists
// because prose perception (ZBBS-HOME-351..355) makes the model answer in
// prose-shaped calls — move_to("the Tavern") — which the id-only form rejected,
// punishing the model's correct instinct (the live John Ellis case: he had a
// closed farm's id from a cue but no id for his own Tavern, so move_to by name
// bounced).
//
// STRUCTURES ARE COMMON-KNOWLEDGE GEOGRAPHY (LLM-142). A resident knows where
// every building in their own village is, so a structure_name resolves against
// EVERY named structure in the world, at any distance — there is no anchor /
// scene-radius / shown-this-tick / personally-visited gate. The earlier gating
// (HOME-356 scene-radius, HOME-389 shown-this-tick, LLM-78 remembered known
// places) put the world-memory no-omniscience guard on the wrong axis: knowing a
// place EXISTS is not omniscience. Only a place's dynamic STATUS (open/closed,
// keeper asleep, stock on hand) is earned, and that lives in observed_state.go +
// the perception cues, untouched by this path. Entry is gated downstream
// (entry_policy, room_access, the keeper-abed lock) — existence-known is not
// enter-allowed, so a villager can walk to a private home's door without being
// let inside.
//
// Matches are case-insensitive and tolerate a leading article on either side, so
// move_to("the Tavern") resolves the structure named "Tavern" (placeNameMatches).
// DUPLICATE NAMES resolve to the NEAREST match (Chebyshev), not an ambiguity
// reject — places legitimately share names ("the well") and "walk to X" means the
// closest one; ties beyond distance break by structure_id for determinism.
//
// OBJECTS ARE STILL DISCOVERED. A name that matches no structure falls through to
// a bare refresh source — a well, a fruit tree (resolveObjectByPerceivableName,
// ZBBS-HOME-359) — and those stay gated on what the tick SHOWED (shownObjects) or
// the actor has personally experienced (remembered.ObjectIDs, LLM-78): a wild
// bush in the woods is not common knowledge the way a building is. Structures win
// a name collision: the structure resolver runs first, so "the Tavern" enters
// rather than stopping outside its placement.
func MoveToStructureByName(actorID ActorID, name string, shownObjects []VillageObjectID, remembered RememberedPlaces, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return MoveActorResult{}, fmt.Errorf("MoveToStructureByName: actor %q not in world", actorID)
			}
			target := strings.TrimSpace(name)
			if target == "" {
				return MoveActorResult{}, fmt.Errorf("move_to: structure_name is required")
			}
			// Structures are common-knowledge geography (LLM-142): resolve against
			// every named structure in the village. A structure wins a name it
			// shares with a bare object — the structure resolver runs first.
			if structureID, ok := resolveStructureByVillageName(w, a, target); ok {
				return MoveToStructure(actorID, structureID, now).Fn(w)
			}
			// No structure by that name — try a bare refresh source (a well, a
			// fruit tree), which IS still discovered: shown this tick (ZBBS-HOME-359)
			// or personally experienced (LLM-78), live winning a shared name.
			if objID, ok := resolveObjectByPerceivableName(w, a, target, shownObjects); ok {
				return MoveToObject(actorID, objID, now).Fn(w)
			}
			if objID, ok := resolveObjectByRememberedName(w, a, target, remembered.ObjectIDs); ok {
				return MoveToObject(actorID, objID, now).Fn(w)
			}
			// LLM-212: reserved relationship keywords ("home"/"work" + synonyms), as a
			// FALLBACK only — after no real structure or object matched the word, so a
			// place actually named "home"/"my shop" wins the name and the keyword fires
			// solely when nothing else matches. An NPC can say move_to("home") the way
			// it talks; it resolves to its own anchor (composing with the LLM-209
			// already-there terminal no-op when it is already there). Homeless/workless
			// — or a stale anchor — yields a plain retryable steer.
			if id, matched, err := anchorKeywordTarget(w, a, target); matched {
				if err != nil {
					return MoveActorResult{}, err
				}
				return MoveToStructure(actorID, id, now).Fn(w)
			}
			// LLM-317: "the kitchen" is a confabulated interior room — weak models
			// routinely try to walk to it (the LLM-176 "food in the kitchen"
			// hallucination) though it names no navigable place. Indulge the fiction
			// with a NON-terminal no-op: the actor does not move, but "arrives" and
			// keeps its tick (it may be about to produce/act). A reserved-keyword
			// fallback like anchorKeywordTarget above, so a real structure actually
			// named "kitchen" (none ship today) would still win.
			if isKitchenPhantom(target) {
				return MoveActorResult{}, NonTerminalNoOpError{Msg: kitchenPhantomMessage}
			}
			// Nothing matched — the model named a place that doesn't exist (a weak
			// model's hallucinated destination, LLM-306). Hand it a bounded set of
			// REAL public structure names it CAN pick, so its next turn corrects to a
			// valid target instead of mutating the same bad string. When the world
			// has no public destination to name (a degenerate/minimal world), fall
			// back to the original generic hint so the message stays coherent.
			if names, more := namedVillageDestinations(w, a, moveToDestinationNameCap); len(names) > 0 {
				// %q each name: a display name is admin/agent-authored, so escaping it
				// (matching the target/id rendering in this file) keeps a stray newline
				// or control char from forging the tool-feedback line, and reads to the
				// model as a literal token to copy.
				quoted := make([]string, len(names))
				for i, n := range names {
					quoted[i] = fmt.Sprintf("%q", n)
				}
				list := strings.Join(quoted, ", ")
				if more {
					list += " (and more)"
				}
				return MoveActorResult{}, fmt.Errorf(
					"there is no place called %q — the village includes %s; name one of those, or a source (a well, a bush) you can see or have visited", target, list)
			}
			return MoveActorResult{}, fmt.Errorf(
				"there is no place called %q — name a structure in the village, or a source (a well, a bush) you can see or have visited", target)
		},
	}
}

// kitchenPhantomMessage is the model-facing line the LLM-317 kitchen no-op
// echoes. The actor has not moved — this is a deliberate fiction, since the
// "kitchen" it names does not exist as a place in the village.
const kitchenPhantomMessage = "You are now in the kitchen."

// isKitchenPhantom reports whether target names the confabulated "kitchen" — an
// interior room weak models hallucinate and try to move_to, which is not a
// navigable place in the village. Article-stripped + case-folded (reusing
// stripLeadingArticle), so "kitchen", "the kitchen", "a kitchen" all match.
// A match is answered with a NON-terminal no-op (kitchenPhantomMessage), not the
// "no such place" error a weak model loops on. Only "kitchen" for now (LLM-317).
func isKitchenPhantom(target string) bool {
	return strings.EqualFold(stripLeadingArticle(strings.TrimSpace(target)), "kitchen")
}

// anchorKeywordTarget resolves a reserved relationship KEYWORD — the way an NPC
// refers to its own home or post in speech ("home", "work", "my shop") rather
// than by the building's proper name — to the actor's own anchor structure
// (LLM-212). Article-stripped and case-insensitive, mirroring placeNameMatches.
// It validates the resolved anchor still names a LIVE structure (liveAnchor), so
// a stale/corrupt anchor id yields a retryable steer rather than a bad dispatch —
// and a keyword-shaped anchor id can't drive MoveToStructure into unbounded
// recursion (the re-dispatched id is guaranteed to hit the structures map).
// Returns:
//   - (id, true, nil):  keyword matched, the actor HAS that anchor and it is live.
//   - ("", true, err):  keyword matched but the actor has no such anchor
//     (homeless/workless) or it no longer names a structure — a plain RETRYABLE
//     steer, NOT a TerminalNoOpError (nothing there to no-op against).
//   - ("", false, nil): not a keyword — the caller falls through to normal
//     structure-name / bare-object resolution. This runs as a FALLBACK after
//     name resolution in MoveToStructureByName, so a real place actually named
//     "home"/"my shop" wins the word; the keyword only fires when nothing matches.
//
// MUST be called from inside a Command.Fn (reads the live actor + world).
// Unexported.
func anchorKeywordTarget(w *World, a *Actor, name string) (StructureID, bool, error) {
	if a == nil {
		return "", false, nil
	}
	switch strings.ToLower(stripLeadingArticle(strings.TrimSpace(name))) {
	case "home", "my home", "my house":
		return liveAnchor(w, a.HomeStructureID, "home")
	case "work", "my work", "my workplace", "my post", "my shop", "my stall":
		return liveAnchor(w, a.WorkStructureID, "workplace")
	}
	return "", false, nil
}

// liveAnchor turns a resolved anchor id into an anchorKeywordTarget result: an
// empty anchor (homeless/workless) or a stale one (no longer a live structure)
// both become a retryable steer; a live id passes through. label is the felt
// noun for the steer ("home"/"workplace").
func liveAnchor(w *World, id StructureID, label string) (StructureID, bool, error) {
	if id == "" {
		return "", true, fmt.Errorf("you have no %s to go to", label)
	}
	if _, ok := w.Structures[id]; !ok {
		return "", true, fmt.Errorf("your %s is no longer somewhere you can walk to", label)
	}
	return id, true, nil
}

// stripLeadingArticle removes a single leading article ("the"/"a"/"an") from a
// place name, case-insensitively and only at a whole-word boundary (the article
// must be followed by more text), never stripping to empty. So "the Tavern" ->
// "Tavern", while "Theater" and "Anvil" are untouched. An NPC (or PC) names a
// place the way a person would; the resolver shouldn't punish the article.
func stripLeadingArticle(s string) string {
	for _, art := range []string{"the ", "an ", "a "} {
		if len(s) > len(art) && strings.EqualFold(s[:len(art)], art) {
			rest := strings.TrimSpace(s[len(art):])
			if rest != "" {
				return rest
			}
		}
	}
	return s
}

// placeNameMatches reports whether a place's display name matches a query name,
// case-insensitively and tolerant of a leading article on either side
// ("the Tavern" <-> "Tavern"). Used by both move_to name resolvers so a structure
// and a bare refresh-source object resolve a name the same forgiving way.
func placeNameMatches(displayName, query string) bool {
	return strings.EqualFold(
		stripLeadingArticle(strings.TrimSpace(displayName)),
		stripLeadingArticle(strings.TrimSpace(query)),
	)
}

// resolveStructureByVillageName resolves a place name to a structure_id against
// EVERY named structure in the village (LLM-142): a resident knows where every
// building is, so geography is common knowledge — there is no anchor /
// scene-radius / shown / remembered gate. Salem is a SINGLE-SETTLEMENT engine —
// `w.Structures` IS the village (one settlement per World; the cross-realm
// `Structure.LeadsToRealm` is empty in v1), so scanning the whole world is
// village-scoped today. A future multi-village orchestrator would filter
// candidates to the actor's own settlement here. Matches are case-insensitive and
// tolerant of a leading article (placeNameMatches); duplicate names resolve
// nearest-wins (Chebyshev to the actor; ties break by structure_id for
// determinism). A structure with no walkable placement is skipped (can't walk
// there, can't measure distance). ok=false when no named structure matches. MUST
// be called from inside a Command.Fn.
func resolveStructureByVillageName(w *World, a *Actor, name string) (StructureID, bool) {
	bestID := StructureID("")
	bestDist := -1
	for structureID, st := range w.Structures {
		if st == nil || !placeNameMatches(st.DisplayName, name) {
			continue
		}
		vobj, ok := villageObjectForStructureOnly(w, structureID)
		if !ok {
			continue // no placement → can't walk there (and can't measure distance)
		}
		dist := a.Pos.Chebyshev(vobj.Pos.Tile())
		// Closer wins; equal distance breaks by lower structure_id for a stable
		// result (map iteration + duplicate names would otherwise be nondeterministic).
		if bestDist == -1 || dist < bestDist || (dist == bestDist && structureID < bestID) {
			bestID, bestDist = structureID, dist
		}
	}
	if bestDist == -1 {
		return "", false
	}
	return bestID, true
}

// moveToDestinationNameCap bounds how many real structure names the unknown-place
// error lists (LLM-306). Weak-model prompt discipline: enough distinct names to
// unstick a hallucinated destination without dumping the whole village directory
// into a retry message.
const moveToDestinationNameCap = 5

// namedVillageDestinations returns up to limit PUBLIC structure names the actor
// could pick as a move_to target, nearest-first, plus whether more existed than
// the cap (so the caller can append an "(and more)" hint instead of implying the
// list is exhaustive). It backs the unknown-place error in MoveToStructureByName:
// when the model names a place no structure matches, this hands it a bounded set
// of REAL names it CAN use, so its next turn corrects to a valid target rather
// than mutating the same bad string (LLM-306).
//
// The set is drawn VILLAGE-WIDE, not from the actor's known/visited places:
// structures are common-knowledge geography (LLM-142), so every named structure
// with a placement is already a valid target — a "known places" filter would omit
// reachable buildings (and structure known-places aren't even threaded to this
// call; RememberedPlaces carries objects only). This mirrors the set
// resolveStructureByVillageName resolves against.
//
// PUBLIC = entry policy neither owner-only (private homes, which the actor can
// walk to but not usefully enter) nor closed (wells, decoratives — already
// covered by the "a well, a bush" source hint). This mirrors the engine's own
// enter logic and points the model at shops / the tavern / civic buildings, which
// is what a hallucinated destination ("Market", "the Smithy") is reaching for. A
// structure with no walkable placement is skipped (can't walk there, can't
// measure distance), matching the resolver.
//
// Order is nearest-first (Chebyshev to the actor; ties break by structure_id),
// deduplicated by display name (leading-article-insensitive) so a shared name is
// listed once. Deterministic for a fixed world + actor position, so the error
// string is byte-stable. MUST be called from inside a Command.Fn.
func namedVillageDestinations(w *World, a *Actor, limit int) (names []string, more bool) {
	type candidate struct {
		id   StructureID
		name string
		dist int
	}
	candidates := make([]candidate, 0, len(w.Structures))
	for structureID, st := range w.Structures {
		if st == nil || strings.TrimSpace(st.DisplayName) == "" {
			continue
		}
		vobj, ok := villageObjectForStructureOnly(w, structureID)
		if !ok {
			continue // no placement → can't walk there (and can't measure distance)
		}
		if vobj.EntryPolicy == EntryPolicyOwner || vobj.EntryPolicy == EntryPolicyClosed {
			continue // private home / decorative — not a public destination to name
		}
		candidates = append(candidates, candidate{
			id:   structureID,
			name: strings.TrimSpace(st.DisplayName),
			dist: a.Pos.Chebyshev(vobj.Pos.Tile()),
		})
	}
	// Nearest wins; equal distance breaks by lower structure_id, so the list is
	// stable across map-iteration order (matching resolveStructureByVillageName).
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dist != candidates[j].dist {
			return candidates[i].dist < candidates[j].dist
		}
		return candidates[i].id < candidates[j].id
	})
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		key := strings.ToLower(stripLeadingArticle(c.name))
		if seen[key] {
			continue // a shared name (nearest instance already listed)
		}
		seen[key] = true
		names = append(names, c.name)
	}
	if limit <= 0 {
		// Degenerate cap — name nothing (avoids a negative-slice panic on
		// names[:limit]); still report whether any destination existed.
		return nil, len(names) > 0
	}
	if len(names) > limit {
		return names[:limit], true
	}
	return names, false
}

// resolveObjectByPerceivableName resolves a place name to a bare refresh-source
// VillageObject the actor could plausibly reach — a free public source (a well,
// a fruit tree) within DefaultOutdoorSceneRadius — case-insensitively and
// tolerant of a leading article (placeNameMatches), nearest-wins on duplicate
// names (Chebyshev to the actor; ties break by object
// id for determinism). The object-keyed sibling of
// resolveStructureByVillageName and the move_to name path's fallthrough when
// no structure matches. ZBBS-HOME-359.
//
// Structure-backed placements are excluded: those resolve through the structure
// path, so a name shared with a building never routes to an object visit that
// would skip enter logic. ok=false when no perceivable free source matches. MUST
// be called from inside a Command.Fn.
func resolveObjectByPerceivableName(w *World, a *Actor, name string, shown []VillageObjectID) (VillageObjectID, bool) {
	radius := w.Settings.DefaultOutdoorSceneRadius
	if radius <= 0 {
		radius = DefaultOutdoorSceneRadiusValue
	}
	bestID := VillageObjectID("")
	bestDist := -1
	// consider folds one candidate object into the nearest-match running best: it
	// must be a usable refresh source, name-match (case-insensitive), and NOT back
	// a structure (those route via the structure path so a shared name never skips
	// the enter-vs-visit derivation). No radius gate here — the CALLER chooses the
	// scope (the scene-radius scan vs an at-any-distance shown id).
	consider := func(id VillageObjectID) {
		obj := w.VillageObjects[id]
		if obj == nil || !objectIsRefreshSource(obj) {
			return
		}
		if !placeNameMatches(obj.DisplayName, name) {
			return
		}
		if _, isStructure := w.Structures[StructureID(id)]; isStructure {
			return
		}
		dist := a.Pos.Chebyshev(obj.Pos.Tile())
		if bestDist == -1 || dist < bestDist || (dist == bestDist && id < bestID) {
			bestID, bestDist = id, dist
		}
	}
	// Free sources within scene radius — "what is around me."
	for id, obj := range w.VillageObjects {
		if obj == nil {
			continue
		}
		if a.Pos.Chebyshev(obj.Pos.Tile()) > radius {
			continue
		}
		consider(id)
	}
	// Plus any object the actor was SHOWN this tick (a free source / rest spot a
	// cue named) — at any distance. ZBBS-HOME-389.
	for _, id := range shown {
		consider(id)
	}
	if bestDist == -1 {
		return "", false
	}
	return bestID, true
}

// resolveObjectByRememberedName resolves a place name against the actor's DURABLE
// known-places set (LLM-78) — a bare placement it has personally experienced (a
// berry patch it gathered, a well it drank at) that THIS tick's perception did
// not surface. The object-keyed memory fallthrough when no structure matches
// (structures are common-knowledge geography and resolve directly, LLM-142).
// Considers
// ONLY the threaded remembered ids, at any distance; liveness = the placement
// still exists (w.VillageObjects), so a remembered source since removed is
// skipped and the model gets a steer, not a crash.
//
// UNLIKE the live resolveObjectByPerceivableName this does NOT gate on
// objectIsRefreshSource. The live path is scoped to free refresh sources a
// satiation cue surfaced this tick; the memory path covers ANY personally-
// experienced placement — a gather patch (which is not a dwell-refresh source at
// all) included. The affordance that earned the memory is the warrant to walk
// back, not a this-tick refresh cue; the only liveness question is whether the
// placement still exists. Structure-backed ids are excluded (they route via the
// structure path so a shared name never skips the enter-vs-visit derivation),
// matching the live resolver's invariant. Case-insensitive + article-tolerant,
// nearest-wins on duplicates (ties break by object id). ok=false when no live
// remembered object matches. MUST be called from inside a Command.Fn.
func resolveObjectByRememberedName(w *World, a *Actor, name string, remembered []VillageObjectID) (VillageObjectID, bool) {
	bestID := VillageObjectID("")
	bestDist := -1
	for _, id := range remembered {
		obj := w.VillageObjects[id]
		if obj == nil || !placeNameMatches(obj.DisplayName, name) {
			continue
		}
		if _, isStructure := w.Structures[StructureID(id)]; isStructure {
			continue
		}
		dist := a.Pos.Chebyshev(obj.Pos.Tile())
		if bestDist == -1 || dist < bestDist || (dist == bestDist && id < bestID) {
			bestID, bestDist = id, dist
		}
	}
	if bestDist == -1 {
		return "", false
	}
	return bestID, true
}

// objectIsRefreshSource reports whether obj carries at least one still-usable
// refresh row that NET EASES a need — a free, public need-easing placement (a
// well, a fruit tree, a shade oak) move_to can walk an actor to via an object
// visit. A row whose finite supply is exhausted, or whose arrival + dwell delta
// nets to zero-or-worse (a row that doesn't actually help, or worsens the need),
// does not count — so move_to never routes to a non-helpful prop. The easing
// magnitude mirrors perception's objectRefreshMagnitude (kept here rather than
// shared because that lives in the perception package). ZBBS-HOME-359.
func objectIsRefreshSource(obj *VillageObject) bool {
	if obj == nil {
		return false
	}
	for _, r := range obj.Refreshes {
		if r == nil {
			continue
		}
		if r.IsFinite() && r.AvailableQuantity != nil && *r.AvailableQuantity <= 0 {
			continue
		}
		mag := -r.Amount // Amount is the negative arrival decrement (easing > 0)
		if r.DwellDelta != nil {
			mag += -*r.DwellDelta
		}
		if mag > 0 {
			return true
		}
	}
	return false
}

// MoveToObject returns a Command that walks actorID to a bare village object's
// visitor slot — the object-keyed sibling of MoveToStructure for a placement
// with no Structure shell (a well, a fruit tree, a shade oak). It is how the
// move_to tool reaches a free refresh source the actor saw in a "free to
// drink/eat nearby" cue: the satiation free-source bullet carries the object id,
// and both the id and name paths funnel a non-structure placement here.
// ZBBS-HOME-359.
//
// Always an ObjectVisit — a bare prop has no interior to enter; the actor
// stands beside it and the arrival-refresh / gather machinery does the rest.
// Rejections mirror MoveToStructure's, object-keyed (so the model gets a crisp,
// retry-anchored error rather than MoveActor's generic resolve failure):
//   - actor not in world.
//   - no such object — the id doesn't name a village object placement.
//   - already at the object — standing within LoiterAttributionTiles of its
//     loiter pin makes the walk a no-op (it can act on the source in place).
//   - already walking to the SAME object — re-issuing an in-flight object
//     destination is a no-op; a different destination supersedes silently.
//   - destination unreachable — surfaced by MoveActor's path check.
//
// Runs on the world goroutine.
func MoveToObject(actorID ActorID, objID VillageObjectID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return MoveActorResult{}, fmt.Errorf("MoveToObject: actor %q not in world", actorID)
			}
			if objID == "" {
				return MoveActorResult{}, fmt.Errorf("move_to: object id is required")
			}
			if _, ok := w.VillageObjects[objID]; !ok {
				return MoveActorResult{}, fmt.Errorf(
					"there is no place %q to walk to — use a structure_id or name you can see in your perception", objID)
			}
			if pin, ok := effectiveObjectLoiterTile(w, objID); ok && a.Pos.Chebyshev(pin) <= LoiterAttributionTiles {
				// LLM-209: a no-op walk — TerminalNoOpError so the harness ends the
				// tick rather than the model re-firing move_to(here) to the budget.
				return MoveActorResult{}, TerminalNoOpError{Msg: fmt.Sprintf(
					"you are already at %q — you're right where you meant to be; nothing more to do here.", objID)}
			}
			if a.MoveIntent != nil && a.MoveIntent.Destination.ObjectID != nil &&
				*a.MoveIntent.Destination.ObjectID == objID {
				return MoveActorResult{}, TerminalNoOpError{Msg: fmt.Sprintf(
					"you are already on your way to %q — keep walking; you'll arrive shortly.", objID)}
			}
			// leaveHuddleFirst=true mirrors MoveToStructure: choosing to walk
			// somewhere ends any conversation the actor is in.
			return MoveActor(actorID, NewObjectVisitDestination(objID), true, now).Fn(w)
		},
	}
}

// MoveToStructure returns a Command that walks actorID to structureID, derives
// enter-vs-visit from the structure's entry policy, and dispatches MoveActor.
// Runs on the world goroutine.
//
// Rejections (surfaced to the model as tool errors so it can retry / pick
// another action):
//   - actor not in world.
//   - empty structure_id (defense-in-depth — the handler also checks).
//   - no such structure — the id doesn't name a structure in the world.
//   - already inside the target — a no-op walk (mirrors v1's
//     errMoveAlreadyAtDest); checked before the enter/visit decision because
//     being inside the target makes the distinction moot.
//   - already walking to the SAME structure — re-issuing an in-flight
//     destination is a no-op. Changing to a DIFFERENT destination is allowed
//     (MoveActor silently supersedes). This guard keeps a level-triggered duty
//     warrant that re-deliberates mid-walk from restamping the intent (and
//     re-emitting ActorMoveStarted) every tick.
//   - destination unreachable — surfaced by MoveActor's path check.
func MoveToStructure(actorID ActorID, structureID StructureID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return MoveActorResult{}, fmt.Errorf("MoveToStructure: actor %q not in world", actorID)
			}
			if structureID == "" {
				return MoveActorResult{}, fmt.Errorf("move_to: structure_id is required")
			}
			if _, ok := w.Structures[structureID]; !ok {
				// LLM-212: a reserved relationship keyword ("home"/"work"/…) can ride
				// the structure_id field when the model names its own anchor as a word
				// rather than an id. Resolve it to the actor's home/work structure
				// before the bare-object fallthrough (the name path handles the same
				// keywords in MoveToStructureByName). The resolved id is a real
				// structure, so the re-dispatch can't re-enter this keyword branch.
				if id, matched, err := anchorKeywordTarget(w, a, string(structureID)); matched {
					if err != nil {
						return MoveActorResult{}, err
					}
					return MoveToStructure(actorID, id, now).Fn(w)
				}
				// Not a structure — it may be a bare placement the actor saw in a cue,
				// whose id rides the same structure_id field: a need-easing refresh
				// source (a well, a fruit tree — a free-source cue) OR a forage-to-sell
				// bush (the "## Your bushes to harvest" cue). Fall through to an object
				// visit so move_to(structure_id=<source>) reaches it. The forage bush is
				// gatherable but NOT need-easing (Amount 0, no dwell → not an
				// objectIsRefreshSource), so it needs the IsFiniteGatherableSource arm —
				// the by-id parity with the name path, which already walks to a
				// remembered gather patch ungated (LLM-78/92). ZBBS-HOME-359.
				if obj := w.VillageObjects[VillageObjectID(structureID)]; obj != nil &&
					(objectIsRefreshSource(obj) || obj.IsFiniteGatherableSource()) {
					return MoveToObject(actorID, VillageObjectID(structureID), now).Fn(w)
				}
				return MoveActorResult{}, fmt.Errorf(
					"there is no structure %q to walk to — use a structure_id you can see in your perception", structureID)
			}
			// A structure record with no backing VillageObject placement can't
			// be walked to: moveToCanEnter would silently fall back to a visit
			// and MoveActor's visitor-slot resolve (pickVisitorSlot reads the
			// placement's loiter pin) would then fail with a generic
			// "destination cannot be resolved". Catch the missing placement here
			// so the model gets a crisp, retry-anchored error instead. The
			// shared-identity bridge keeps these in lockstep in practice; this
			// guards partial/desynced world state (code_review, ZBBS-HOME-285).
			if _, _, ok := villageObjectForStructure(w, structureID); !ok {
				return MoveActorResult{}, fmt.Errorf(
					"structure %q has no placement in the village to walk to — pick a different structure_id from your perception", structureID)
			}
			if a.InsideStructureID == structureID {
				// LLM-209: a no-op walk — TerminalNoOpError ends the tick (the model
				// was re-firing move_to(home) here to the budget while already home).
				return MoveActorResult{}, TerminalNoOpError{Msg: fmt.Sprintf(
					"you are already at %q — you're right where you meant to be; nothing more to do here.", structureID)}
			}
			if a.MoveIntent != nil && a.MoveIntent.Destination.StructureID != nil &&
				*a.MoveIntent.Destination.StructureID == structureID {
				return MoveActorResult{}, TerminalNoOpError{Msg: fmt.Sprintf(
					"you are already on your way to %q — keep walking; you'll arrive shortly.", structureID)}
			}
			dest := moveToDestinationFor(w, a, structureID, now)
			// A move that resolves to a VISIT of a structure the actor already
			// stands at is a no-op walk — reject it before MoveActor tears the
			// huddle down. The InsideStructureID guard above catches the ENTER
			// no-op (already inside); this catches its VISIT sibling: a co-present
			// actor loitering at the structure's slot (e.g. a worker at an
			// owner-only shop's loiter pin, in a huddle with the keeper). Without
			// it, re-issuing move_to here runs MoveActor with leaveHuddleFirst —
			// emitting a spurious HuddleLeft (and the keeper's businessowner
			// farewell) and re-forming the huddle on instant arrival,
			// mid-negotiation (LLM-196). Mirrors MoveToObject's loiter-pin no-op
			// guard; LoiterAttributionTiles = "standing AT" the pin.
			if dest.Kind == MoveDestinationStructureVisit {
				if pin, ok := effectiveLoiterTile(w, structureID); ok && a.Pos.Chebyshev(pin) <= LoiterAttributionTiles {
					return MoveActorResult{}, TerminalNoOpError{Msg: fmt.Sprintf(
						"you are already at %q — you're right where you meant to be; nothing more to do here.", structureID)}
				}
			}
			// The actor has chosen to walk to structureID — deciding to GO there
			// supersedes any stale "I found it shut/dry" belief about it, so drop
			// that experiential memory now (ZBBS-HOME-405). Placed after the
			// guards above so it fires only on a genuinely new walk.
			forgetSupplierStaleMemory(a, structureID)
			// leaveHuddleFirst=true: choosing to walk somewhere ends any
			// conversation the actor is in (ZBBS-HOME-285 — matches v1's
			// move=leave, confirmed with work for the duty-warrant seam). The
			// duty warrant is level-triggered, so the model isn't yanked
			// mid-sentence: it can keep talking over ticks and leaves the
			// huddle cleanly only when it chooses to move.
			return MoveActor(actorID, dest, true, now).Fn(w)
		},
	}
}

// forgetSupplierStaleMemory drops every observed-state memory the actor holds
// about structureID — "found it shut" (ObservedClosed, ZBBS-HOME-353) and "found
// it dry" (ObservedOutOfStock, ZBBS-HOME-363), across all items — for the
// destination the actor is now committing to walk to (ZBBS-HOME-405).
//
// Deciding to GO somewhere supersedes a stale belief about it. Without this, a
// mid-walk reactor tick re-reads the old "shut" annotation and steers the actor
// AWAY from the very destination it is en route to (the live Josiah↔Ellis Farm
// thrash: he arrived just as the keeper was present, but a re-decision off the
// stale shut label had already redirected him, yanking him out of the
// just-formed huddle before he could buy). The deprioritization still applies at
// DECISION time — the cue shows the annotation when the actor first weighs the
// trip — and the arrival subscribers re-stamp the memory if the place really is
// shut/dry on arrival; we clear it only once the actor has chosen to go.
//
// Destination-scoped on purpose (Jeff, 2026-06-06): clearing memory for OTHER
// businesses would make the actor re-attempt shops it legitimately knows are
// shut. nil-safe (ForgetStructure ranges a possibly-empty store).
func forgetSupplierStaleMemory(a *Actor, structureID StructureID) {
	a.Observed.ForgetStructure(structureID)
}

// moveToDestinationFor derives the MoveDestination for a move_to: a
// StructureEnter when the actor can actually enter structureID, else a
// StructureVisit (walk to a visitor slot and stand outside).
//
// The enter predicate mirrors MoveActor's StructureEnter validation
// (commands_move.go) and v1's agentMoveShouldEnter (engine/agent_tick.go):
//
//   - entry policy "closed"     → never enter (wells, fountains, decoratives).
//   - entry policy "owner-only" → enter only if the actor is a member
//     (resident / staff / owner / lodger — structureMembershipAllows).
//   - no door tile              → never enter (a doorless structure has no
//     interior to stand in); visit its loiter slot instead.
//   - otherwise (open / type-default, door present) → enter.
//
// The auto-sleep loop depends on the enter case: move_to(home) must land
// InsideStructureID == HomeStructureID for handleAutoSleepOnArrival to bed an
// off-shift NPC (npc_sleep.go).
//
// MUST be called from inside a Command.Fn (reads world maps). Unexported.
func moveToDestinationFor(w *World, actor *Actor, structureID StructureID, now time.Time) MoveDestination {
	if moveToCanEnter(w, actor, structureID, now) {
		return NewStructureEnterDestination(structureID)
	}
	return NewStructureVisitDestination(structureID)
}

// moveToCanEnter reports whether actor may enter structureID's interior. The
// checks are the same invariants MoveActor re-validates on the StructureEnter
// path, so a true result here always survives MoveActor's own gate — the
// derivation never produces an enter destination MoveActor would reject for
// policy/door reasons (only reachability, which neither side can know without
// the walk grid, can still fail downstream).
//
// MUST be called from inside a Command.Fn (reads world maps). Unexported.
func moveToCanEnter(w *World, actor *Actor, structureID StructureID, now time.Time) bool {
	vobj, _, ok := villageObjectForStructure(w, structureID)
	if !ok {
		return false
	}
	policy := effectiveEntryPolicy(w, structureID, vobj, now)
	if policy == EntryPolicyClosed {
		return false
	}
	if policy == EntryPolicyOwner && !structureMembershipAllows(w, actor, structureID, now) {
		return false
	}
	if _, ok := structureEntryTile(w, structureID); !ok {
		return false
	}
	return true
}
