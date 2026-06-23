package sim

import (
	"fmt"
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
// structure_id the actor could plausibly know, then walks there exactly as
// MoveToStructure does. It exists because prose perception (ZBBS-HOME-351..355)
// makes the model answer in prose-shaped calls — move_to("the Tavern") — which
// the id-only form rejected, punishing the model's correct instinct (the live
// John Ellis case: he had the closed farm's id from a cue but no id for his own
// Tavern, so move_to by name bounced). ZBBS-HOME-356.
//
// Resolution scope is what the actor can plausibly know (NOT every structure in
// the village — a villager doesn't know a place exists just because the engine
// does): its own home/work anchors (always, any distance) PLUS any named
// structure within its scene radius (DefaultOutdoorSceneRadius — the same "what
// is around me" radius perception uses). Matches are case-insensitive and
// tolerate a leading article on either side, so move_to("the Tavern") resolves
// the structure named "Tavern" (placeNameMatches). ZBBS-WORK-417.
//
// DUPLICATE NAMES resolve to the NEAREST match (Chebyshev), not an ambiguity
// reject — unlike the pay path's findHuddlePeerByDisplayName. Places legitimately
// share names ("the well"), and "walk to the well" means the closest one; a
// money transfer to an ambiguous person does not have a safe default, but a walk
// does. Ties beyond distance break by structure_id for determinism.
//
// PLUS — ZBBS-HOME-389 — any structure the tick's PERCEPTION surfaced as a move
// target (a vendor / rest / restock cue named it, with its id), at any distance.
// The cue showed it to the actor, so it is perceivable for this tick — the same
// "things you were just told about" justification as anchors. This closes the
// recurring "model emits the distant cue's NAME, move_to rejects it, the NPC
// starves in place" hole: those cues always carried the structure_id inline, but
// name-resolution never consulted them — only anchors + scene radius — so a far
// shop named by the model bounced. The shown id set is threaded in by the
// harness (perception.CollectPerceivedPlaces) through the move_to handler.
//
// A name that matches no structure falls through to a bare refresh source — a
// well, a fruit tree the actor saw in a "free to drink/eat nearby" cue — via
// resolveObjectByPerceivableName, so "walk to the well" reaches a placement that
// has no Structure shell (ZBBS-HOME-359). Structures win on a name collision:
// the structure resolver runs first, so "the Tavern" still enters rather than
// stops outside its placement.
//
// PLUS — LLM-78 — the actor's DURABLE known places (LLM-77's experiential
// world-memory: places it has gathered at, bought from, drank at, or owns) as a
// FOURTH name-resolution source, threaded in as `remembered` the same way the
// shown set is. This is the no-omniscience guard widened from "shown this tick"
// to "shown this tick OR personally experienced" — still not omniscient (a place
// never visited and not owned stays unresolvable). It is tried ONLY after the
// live sources (anchors + scene-radius + shown) miss, so a live cue always wins a
// name it shares with a remembered place: prefer live, fall back to memory. The
// remembered resolvers enforce liveness against the live world, so a remembered
// place since removed is skipped and falls through to the steer below — a clean
// reject, never a walk to a ghost.
func MoveToStructureByName(actorID ActorID, name string, shownStructures []StructureID, shownObjects []VillageObjectID, remembered RememberedPlaces, now time.Time) Command {
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
			// Live sources first — anchors + scene-radius + shown-this-tick
			// (ZBBS-HOME-356/389). A structure wins a name it shares with a bare
			// object: the structure resolver runs first.
			if structureID, ok := resolveStructureByPerceivableName(w, a, target, shownStructures); ok {
				return MoveToStructure(actorID, structureID, now).Fn(w)
			}
			// No structure by that name — try a bare refresh source (a well, a
			// fruit tree). ZBBS-HOME-359.
			if objID, ok := resolveObjectByPerceivableName(w, a, target, shownObjects); ok {
				return MoveToObject(actorID, objID, now).Fn(w)
			}
			// Memory fallback (LLM-78) — a place the actor has personally
			// experienced but that THIS tick's perception did not surface. Same
			// structure-before-object precedence; tried only because the live
			// sources missed, so live always wins a shared name.
			if structureID, ok := resolveStructureByRememberedName(w, a, target, remembered.StructureIDs); ok {
				return MoveToStructure(actorID, structureID, now).Fn(w)
			}
			if objID, ok := resolveObjectByRememberedName(w, a, target, remembered.ObjectIDs); ok {
				return MoveToObject(actorID, objID, now).Fn(w)
			}
			return MoveActorResult{}, fmt.Errorf(
				"there is no place called %q that you can see or remember — use a structure_id from your perception, or name a place you were shown this tick or have been to before", target)
		},
	}
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

// resolveStructureByPerceivableName resolves a place name to a structure_id the
// actor a could plausibly know — its home/work anchors (any distance), named
// structures within DefaultOutdoorSceneRadius, AND any id in `shown` (the move
// targets this tick's perception surfaced, ZBBS-HOME-389; any distance) —
// case-insensitively and tolerant of a leading article (placeNameMatches),
// nearest-wins on duplicate names (Chebyshev to the actor;
// ties break by structure_id for determinism). ok=false when no perceivable
// structure matches. MUST be called from inside a Command.Fn. ZBBS-HOME-356.
func resolveStructureByPerceivableName(w *World, a *Actor, name string, shown []StructureID) (StructureID, bool) {
	radius := w.Settings.DefaultOutdoorSceneRadius
	if radius <= 0 {
		radius = DefaultOutdoorSceneRadiusValue
	}

	// nameMatches reports whether structureID resolves to a structure whose
	// DisplayName equals name (case-insensitive). Returns the placement tile too
	// (for the distance tie-break) when it resolves.
	bestID := StructureID("")
	bestDist := -1
	consider := func(structureID StructureID) {
		st := w.Structures[structureID]
		if st == nil || !placeNameMatches(st.DisplayName, name) {
			return
		}
		vobj, ok := villageObjectForStructureOnly(w, structureID)
		if !ok {
			return // no placement → can't walk there (and can't measure distance)
		}
		dist := a.Pos.Chebyshev(vobj.Pos.Tile())
		// Closer wins; equal distance breaks by lower structure_id for a stable
		// result (map iteration + duplicate names would otherwise be nondeterministic).
		if bestDist == -1 || dist < bestDist || (dist == bestDist && structureID < bestID) {
			bestID, bestDist = structureID, dist
		}
	}

	// Anchors are always perceivable regardless of distance (the actor knows its
	// own home and workplace), so consider them unconditionally.
	if a.HomeStructureID != "" {
		consider(a.HomeStructureID)
	}
	if a.WorkStructureID != "" {
		consider(a.WorkStructureID)
	}
	// Plus any named structure within scene radius — "what is around me."
	for structureID, st := range w.Structures {
		if st == nil {
			continue
		}
		vobj, ok := villageObjectForStructureOnly(w, structureID)
		if !ok {
			continue
		}
		if a.Pos.Chebyshev(vobj.Pos.Tile()) > radius {
			continue
		}
		consider(structureID)
	}
	// Plus any structure the actor was SHOWN this tick (a vendor/rest/restock cue
	// named it with its id) — at any distance, like an anchor. ZBBS-HOME-389.
	for _, id := range shown {
		consider(id)
	}

	if bestDist == -1 {
		return "", false
	}
	return bestID, true
}

// resolveObjectByPerceivableName resolves a place name to a bare refresh-source
// VillageObject the actor could plausibly reach — a free public source (a well,
// a fruit tree) within DefaultOutdoorSceneRadius — case-insensitively and
// tolerant of a leading article (placeNameMatches), nearest-wins on duplicate
// names (Chebyshev to the actor; ties break by object
// id for determinism). The object-keyed sibling of
// resolveStructureByPerceivableName and the move_to name path's fallthrough when
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

// resolveStructureByRememberedName resolves a place name against the actor's
// DURABLE known-places set (LLM-78) — a structure it has personally experienced
// (a vendor it bought from, its own anchors) but that THIS tick's perception did
// not surface. The memory-backed counterpart to resolveStructureByPerceivableName
// and the move_to name path's FALLBACK when the live structure resolver misses,
// so a live cue always wins a name shared with a remembered place (prefer live,
// fall back to memory). Considers ONLY the threaded remembered ids, at any
// distance (like an anchor); liveness is enforced here — a remembered structure
// since removed from the world, or one with no placement to walk to, is skipped
// (it falls through to a clean steer, never a walk to a ghost). Case-insensitive
// + article-tolerant (placeNameMatches), nearest-wins on duplicate names
// (Chebyshev to the actor; ties break by structure_id for determinism, so the
// result is stable regardless of the remembered slice's order). ok=false when no
// live remembered structure matches. MUST be called from inside a Command.Fn.
func resolveStructureByRememberedName(w *World, a *Actor, name string, remembered []StructureID) (StructureID, bool) {
	bestID := StructureID("")
	bestDist := -1
	for _, structureID := range remembered {
		st := w.Structures[structureID]
		if st == nil || !placeNameMatches(st.DisplayName, name) {
			continue
		}
		vobj, ok := villageObjectForStructureOnly(w, structureID)
		if !ok {
			continue // no placement → can't walk there (and can't measure distance)
		}
		dist := a.Pos.Chebyshev(vobj.Pos.Tile())
		if bestDist == -1 || dist < bestDist || (dist == bestDist && structureID < bestID) {
			bestID, bestDist = structureID, dist
		}
	}
	if bestDist == -1 {
		return "", false
	}
	return bestID, true
}

// resolveObjectByRememberedName resolves a place name against the actor's DURABLE
// known-places set (LLM-78) — a bare placement it has personally experienced (a
// berry patch it gathered, a well it drank at) that THIS tick's perception did
// not surface. The object-keyed sibling of resolveStructureByRememberedName and
// move_to's memory fallthrough when no remembered structure matches. Considers
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
				return MoveActorResult{}, fmt.Errorf(
					"you are already at %q — no need to move there; pick a different action this turn", objID)
			}
			if a.MoveIntent != nil && a.MoveIntent.Destination.ObjectID != nil &&
				*a.MoveIntent.Destination.ObjectID == objID {
				return MoveActorResult{}, fmt.Errorf(
					"you are already on your way to %q — keep walking; pick a different action this turn", objID)
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
				// Not a structure — it may be a bare refresh-bearing placement (a
				// well, a fruit tree) the actor saw in a free-source cue, whose id
				// rides the same structure_id field. Fall through to an object visit
				// so move_to(structure_id=<well>) reaches it. ZBBS-HOME-359.
				if obj := w.VillageObjects[VillageObjectID(structureID)]; obj != nil && objectIsRefreshSource(obj) {
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
				return MoveActorResult{}, fmt.Errorf(
					"you are already at %q — no need to move there; pick a different action this turn", structureID)
			}
			if a.MoveIntent != nil && a.MoveIntent.Destination.StructureID != nil &&
				*a.MoveIntent.Destination.StructureID == structureID {
				return MoveActorResult{}, fmt.Errorf(
					"you are already on your way to %q — keep walking; pick a different action this turn", structureID)
			}
			// The actor has chosen to walk to structureID — deciding to GO there
			// supersedes any stale "I found it shut/dry" belief about it, so drop
			// that experiential memory now (ZBBS-HOME-405). Placed after the
			// guards above so it fires only on a genuinely new walk.
			forgetSupplierStaleMemory(a, structureID)
			dest := moveToDestinationFor(w, a, structureID, now)
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
	if vobj.EntryPolicy == EntryPolicyClosed {
		return false
	}
	if vobj.EntryPolicy == EntryPolicyOwner && !structureMembershipAllows(w, actor, structureID, now) {
		return false
	}
	if _, ok := structureEntryTile(w, structureID); !ok {
		return false
	}
	return true
}
