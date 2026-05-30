package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Build turns a published snapshot plus an actor's consumed warrant batch
// into a Payload. It is a pure function: it reads only the immutable
// *sim.Snapshot and the passed warrants, mutates nothing, and never
// touches the world goroutine.
//
// The warrants are the batch the reactor evaluator consumed and carried in
// the ReactorTickDue event (handlers copies them onto its tickJob, since
// consume clears them from the live actor). Build takes them directly
// rather than the unexported handlers.tickJob so this package stays free
// of any handlers dependency.
//
// Build is total — it never panics and always returns a usable Payload.
// A nil snapshot or an actor absent from the snapshot yields a degraded
// Payload (empty views, Baseline == BaselineMissingNoScene) with the
// reason recorded in SelectionReason; the harness decides what to do with
// a degraded perception.
func Build(snap *sim.Snapshot, actorID sim.ActorID, warrants []sim.WarrantMeta) Payload {
	p := Payload{
		ActorID:  actorID,
		Warrants: orderWarrants(warrants),
		Baseline: BaselineMissingNoScene,
	}

	if snap == nil {
		p.SelectionReason = "nil snapshot — no scene resolvable"
		return p
	}

	actorSnap := snap.Actors[actorID]
	if actorSnap == nil {
		p.SelectionReason = fmt.Sprintf("actor %q not in snapshot — no perception context", actorID)
		return p
	}

	p.Actor = buildActorView(snap, actorSnap)
	p.WarrantActorNames = buildWarrantActorNames(snap, actorSnap, actorID, p.Warrants)
	p.Surroundings = buildSurroundings(snap, actorID, actorSnap)
	p.Anchors = buildAnchors(snap, actorSnap)
	p.NarrativeState = buildNarrativeState(actorSnap)
	p.Relationships = buildRelationships(actorSnap, p.Surroundings.HuddleMembers)
	p.PendingDeliveriesFromMe, p.PendingDeliveriesToMe = buildPendingOrderViews(snap, actorID)
	p.RecoveryOptions = buildRecoveryOptions(snap, actorID, actorSnap)
	p.Satiation = buildSatiation(snap, actorID, actorSnap)
	p.Restocking = buildRestocking(snap, actorID, actorSnap)
	p.Lodging = buildLodgingView(snap, actorSnap)
	p.KeeperLodging = buildKeeperLodgingView(snap, actorSnap)
	p.SummonsForYou = buildSummonsForYou(actorSnap)
	p.SummonRefusal = buildSummonRefusal(actorSnap)

	// Group the consumed warrants by the scene they reference. Only event-
	// sourced warrants carry a scene (the zero-lineage invariant: full
	// lineage or none), so a non-empty SceneID always rides a nonzero
	// SourceEventID — which makes the max-SourceEventID primary selection
	// below well defined and unique.
	sceneGroups := groupBySceneID(p.Warrants)
	p.MultiSceneWarrantCount = len(sceneGroups)

	primarySceneID, reason := resolvePrimaryScene(snap, actorSnap, p.Warrants, sceneGroups)
	p.SelectionReason = reason

	if primarySceneID == "" {
		// No scene resolved. Every scene-bearing warrant (if any) just
		// renders in the flat Warrants list; there is nothing to diff.
		p.Baseline = BaselineMissingNoScene
		return p
	}

	scene := snap.Scenes[primarySceneID]
	if scene == nil {
		// resolvePrimaryScene only returns IDs backed by a non-nil scene,
		// so this is unreachable today — but the guard keeps Build's "never
		// panics" contract locally obvious rather than dependent on
		// reasoning about resolvePrimaryScene.
		p.Baseline = BaselineMissingNoScene
		p.SelectionReason += " — resolved scene was nil in the snapshot"
		return p
	}
	p.Baseline, p.Primary = buildPrimaryScene(scene, actorID, actorSnap, sceneGroups[primarySceneID])
	p.Secondary = buildSecondary(snap, sceneGroups, primarySceneID)
	return p
}

// orderWarrants returns a copy of the batch ordered by SourceEventID
// ascending — PR 3a's monotonic EventID is the authoritative causal order.
// Zero-lineage warrants (SourceEventID == 0) sort first; the sort is
// stable so ties hold the evaluator's input order. A copy is returned so
// Build never mutates the caller's slice.
func orderWarrants(in []sim.WarrantMeta) []sim.WarrantMeta {
	if len(in) == 0 {
		return nil
	}
	out := make([]sim.WarrantMeta, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SourceEventID < out[j].SourceEventID
	})
	return out
}

// groupBySceneID buckets warrants by their (non-empty) SceneID, preserving
// each bucket's incoming order — since the input is already ordered by
// SourceEventID, each bucket is too. Warrants with no SceneID are not
// bucketed (they are not scene-scoped).
func groupBySceneID(ordered []sim.WarrantMeta) map[sim.SceneID][]sim.WarrantMeta {
	groups := make(map[sim.SceneID][]sim.WarrantMeta)
	for _, w := range ordered {
		if w.SceneID == "" {
			continue
		}
		groups[w.SceneID] = append(groups[w.SceneID], w)
	}
	return groups
}

// resolvePrimaryScene applies the scene-resolution order from the PR 3
// design note:
//
//  1. the scene of the consumed warrant with the maximum SourceEventID
//     (the most recent causal signal) — but skip a warrant whose scene is
//     absent from the snapshot (a stale reference, e.g. an area scene
//     deleted when its huddle concluded) and fall through to the next;
//  2. else the actor's active-huddle scene, if the huddle is observed by
//     exactly resolvable scene state;
//  3. else none.
//
// It returns the resolved SceneID ("" when none) and a human-readable
// SelectionReason.
func resolvePrimaryScene(
	snap *sim.Snapshot,
	actorSnap *sim.ActorSnapshot,
	ordered []sim.WarrantMeta,
	sceneGroups map[sim.SceneID][]sim.WarrantMeta,
) (sim.SceneID, string) {
	// Step 1: scene-bearing warrants, highest SourceEventID first.
	if len(sceneGroups) > 0 {
		byEventDesc := make([]sim.WarrantMeta, 0, len(ordered))
		for _, w := range ordered {
			if w.SceneID != "" {
				byEventDesc = append(byEventDesc, w)
			}
		}
		sort.SliceStable(byEventDesc, func(i, j int) bool {
			return byEventDesc[i].SourceEventID > byEventDesc[j].SourceEventID
		})
		for _, w := range byEventDesc {
			// A present-but-nil map entry counts as absent — buildPrimaryScene
			// would dereference it.
			if sc := snap.Scenes[w.SceneID]; sc != nil {
				return w.SceneID, fmt.Sprintf(
					"primary scene %q from warrant (SourceEventID %d, max among %d scene-bearing warrant(s) across %d scene(s))",
					w.SceneID, w.SourceEventID, len(byEventDesc), len(sceneGroups))
			}
		}
		// Every scene-bearing warrant pointed at a scene no longer in the
		// snapshot — fall through to huddle resolution.
	}

	// Step 2: the actor's active-huddle scene. A huddle can be observed by
	// more than one scene over its lifetime (Scene→Huddles is many-to-many),
	// so pick deterministically — the lexicographically lowest SceneID.
	if actorSnap.CurrentHuddleID != "" {
		var candidates []sim.SceneID
		for id, sc := range snap.Scenes {
			if sc == nil {
				continue
			}
			if _, ok := sc.Huddles[actorSnap.CurrentHuddleID]; ok {
				candidates = append(candidates, id)
			}
		}
		if len(candidates) > 0 {
			sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
			chosen := candidates[0]
			if len(sceneGroups) > 0 {
				return chosen, fmt.Sprintf(
					"primary scene %q from actor's active huddle %q (no scene-bearing warrant resolved to a live scene; %d candidate scene(s))",
					chosen, actorSnap.CurrentHuddleID, len(candidates))
			}
			return chosen, fmt.Sprintf(
				"primary scene %q from actor's active huddle %q (no scene-bearing warrants; %d candidate scene(s))",
				chosen, actorSnap.CurrentHuddleID, len(candidates))
		}
	}

	// Step 3: nothing resolved.
	if len(sceneGroups) > 0 {
		return "", "no scene resolved — scene-bearing warrant(s) reference scenes absent from the snapshot, and the actor's huddle resolved no scene"
	}
	if actorSnap.CurrentHuddleID != "" {
		return "", "no scene resolved — no scene-bearing warrants, and the actor's active huddle is observed by no scene in the snapshot"
	}
	return "", "no scene resolved — no scene-bearing warrants and the actor is not in a huddle"
}

// buildPrimaryScene resolves the BaselineStatus for the subject actor
// against the primary scene and assembles its SceneView. The "unknown,
// never no-change" contract is enforced here: a Diff is attached ONLY when
// the actor has a genuine origin snapshot in the scene.
func buildPrimaryScene(
	scene *sim.Scene,
	actorID sim.ActorID,
	actorSnap *sim.ActorSnapshot,
	sceneWarrants []sim.WarrantMeta,
) (BaselineStatus, *SceneView) {
	view := &SceneView{
		SceneID:    scene.ID,
		OriginKind: scene.OriginKind,
		OriginAt:   scene.OriginAt,
		Warrants:   sceneWarrants,
	}

	switch {
	case len(scene.ParticipantStateAtOrigin) == 0:
		// The scene captured no participant baseline at all (e.g. an
		// unbounded atmosphere-refresh scene). Absence here carries no
		// "joined after" signal — no one has a baseline.
		return BaselineMissingNoOriginSnapshot, view

	case scene.ParticipantStateAtOrigin[actorID] == nil:
		// Other participants were captured at origin but this actor was
		// not — so it joined after the scene was minted.
		return BaselineMissingJoinedAfterOrigin, view

	default:
		origin := scene.ParticipantStateAtOrigin[actorID]
		view.Diff = computeDiff(origin, actorSnap)
		return BaselinePresent, view
	}
}

// buildSecondary turns every scene group other than the primary into a
// SceneSignal, sorted by SceneID for determinism. A secondary scene
// carries no baseline diff by design — see SceneSignal's doc comment.
// Groups whose scene is absent from the snapshot are skipped (their
// warrants still appear in the flat Payload.Warrants list).
func buildSecondary(
	snap *sim.Snapshot,
	sceneGroups map[sim.SceneID][]sim.WarrantMeta,
	primary sim.SceneID,
) []SceneSignal {
	var out []SceneSignal
	for sceneID, group := range sceneGroups {
		if sceneID == primary {
			continue
		}
		// A present-but-nil entry counts as absent, same as in resolvePrimaryScene.
		if sc := snap.Scenes[sceneID]; sc == nil {
			continue
		}
		out = append(out, SceneSignal{
			SceneID:  sceneID,
			HuddleID: representativeHuddle(group),
			Warrants: group,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SceneID < out[j].SceneID })
	return out
}

// representativeHuddle picks the HuddleID of the highest-SourceEventID
// warrant in a group — the most recent signal — as the group's
// representative huddle. Warrants in one scene group usually share a
// huddle, but a scene can observe several; the most-recent one is the
// deterministic choice.
func representativeHuddle(group []sim.WarrantMeta) sim.HuddleID {
	var best sim.WarrantMeta
	for i, w := range group {
		if i == 0 || w.SourceEventID > best.SourceEventID {
			best = w
		}
	}
	return best.HuddleID
}

// computeDiff is the loop-detection seam — it compares the actor's frozen
// origin snapshot against its current snapshot field by field. AnyChange
// is the OR every consumer reads: false across consecutive ticks is the
// "this actor is stuck" signal.
func computeDiff(origin, current *sim.ActorSnapshot) *Diff {
	d := &Diff{
		StateChanged:     origin.State != current.State,
		PositionChanged:  origin.Pos.X != current.Pos.X || origin.Pos.Y != current.Pos.Y,
		StructureChanged: origin.InsideStructureID != current.InsideStructureID,
		HuddleChanged:    origin.CurrentHuddleID != current.CurrentHuddleID,
		CoinsChanged:     origin.Coins != current.Coins,
		InventoryChanged: origin.InventoryHash != current.InventoryHash,
		NeedsChanged:     !needsEqual(origin.Needs, current.Needs),
	}
	d.AnyChange = d.StateChanged || d.PositionChanged || d.StructureChanged ||
		d.HuddleChanged || d.CoinsChanged || d.InventoryChanged || d.NeedsChanged
	return d
}

// needsEqual reports whether two need maps carry the same key/value set.
// A missing key and a zero value are treated as distinct — needs are
// always fully populated by snapshotActor, so a key appearing or
// disappearing is itself a real change worth surfacing.
func needsEqual(a, b map[sim.NeedKey]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bv, ok := b[k]
		if !ok || bv != v {
			return false
		}
	}
	return true
}

// buildActorView lifts the subject actor's own decision-relevant state out
// of its ActorSnapshot. The Needs map is copied so the Payload does not
// alias the snapshot's map. Active dwell credits are projected from
// a.DwellCredits with StructureLabel resolved against snap.Structures
// (preferred) or snap.VillageObjects (fallback for object-source
// credits whose pin is a free-standing object like a well or shade
// tree, not a structure).
func buildActorView(snap *sim.Snapshot, a *sim.ActorSnapshot) ActorView {
	var needs map[sim.NeedKey]int
	if len(a.Needs) > 0 {
		needs = make(map[sim.NeedKey]int, len(a.Needs))
		for k, v := range a.Needs {
			needs[k] = v
		}
	}
	return ActorView{
		State:              a.State,
		InsideStructureID:  a.InsideStructureID,
		Position:           sim.Position{X: a.Pos.X, Y: a.Pos.Y},
		CurrentHuddleID:    a.CurrentHuddleID,
		Coins:              a.Coins,
		Needs:              needs,
		NeedThresholds:     snap.NeedThresholds,
		ActiveDwellCredits: buildActiveDwellCredits(snap, a),
		InFlightMove:       buildInFlightMove(snap, a),
	}
}

// buildInFlightMove projects the subject's in-flight MoveIntent (the
// read-path destination fields on the snapshot) into a render-ready view, or
// nil when the actor isn't moving. The label resolves the same way dwell-pin
// labels do — structure DisplayName first, then village-object DisplayName —
// except a bare Position move has no pin to name, so it renders its tile
// coordinate. ZBBS-HOME-336.
func buildInFlightMove(snap *sim.Snapshot, a *sim.ActorSnapshot) *InFlightMoveView {
	if a.MoveDestKind == "" {
		return nil
	}
	var label string
	switch a.MoveDestKind {
	case sim.MoveDestinationStructureEnter, sim.MoveDestinationStructureVisit:
		label = resolveDwellPinLabel(snap, sim.VillageObjectID(a.MoveDestStructureID))
	case sim.MoveDestinationObjectVisit:
		label = resolveDwellPinLabel(snap, a.MoveDestObjectID)
	case sim.MoveDestinationPosition:
		label = fmt.Sprintf("(%d, %d)", a.MoveDestPos.X, a.MoveDestPos.Y)
	default:
		// Unrecognized kind — a corrupt snapshot or a destination kind added
		// to the engine but not yet wired into perception. Don't render a
		// vague "walking to your destination" that masks the gap; surface it
		// as not-moving so the omission is visible rather than papered over.
		return nil
	}
	return &InFlightMoveView{Kind: a.MoveDestKind, DestinationLabel: label}
}

// buildActiveDwellCredits projects the actor's DwellCredits map into a
// deterministic, render-ready slice. Returns nil for an empty map.
// StructureLabel resolution:
//
//   - Look up the credit's ObjectID in snap.Structures first — for
//     item-source credits the pin is usually the structure (tavern,
//     bakery) where the actor ate.
//   - Fall back to snap.VillageObjects.DisplayName — covers
//     object-source credits whose pin is a free-standing object (well,
//     shade tree).
//   - Empty when neither resolves.
//
// Order: (Source ascending, Attribute ascending, ObjectID ascending)
// — stable for golden tests and admin replay.
func buildActiveDwellCredits(snap *sim.Snapshot, a *sim.ActorSnapshot) []DwellCreditView {
	if len(a.DwellCredits) == 0 {
		return nil
	}
	out := make([]DwellCreditView, 0, len(a.DwellCredits))
	for _, c := range a.DwellCredits {
		if c == nil {
			continue
		}
		view := DwellCreditView{
			ObjectID:       c.ObjectID,
			StructureLabel: resolveDwellPinLabel(snap, c.ObjectID),
			Source:         c.Source,
			Kind:           c.Kind,
			Attribute:      c.Attribute,
			PeriodMinutes:  c.DwellPeriodMinutes,
			DwellDelta:     c.DwellDelta,
			LastCreditedAt: c.LastCreditedAt,
		}
		if c.RemainingTicks != nil {
			rt := *c.RemainingTicks
			view.RemainingTicks = &rt
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].Attribute != out[j].Attribute {
			return out[i].Attribute < out[j].Attribute
		}
		return out[i].ObjectID < out[j].ObjectID
	})
	return out
}

// resolveDwellPinLabel resolves the human-facing label for a dwell
// pin. The pin's ObjectID may be either a StructureID (item-source
// credits pin to the structure where the actor ate) or a
// VillageObjectID (object-source credits pin to a free-standing
// object — a well, a shade tree). Try structure first, then village
// object, and return "" when neither has a label so render can fall
// back to a generic phrasing.
func resolveDwellPinLabel(snap *sim.Snapshot, objID sim.VillageObjectID) string {
	if objID == "" {
		return ""
	}
	if st := snap.Structures[sim.StructureID(objID)]; st != nil && st.DisplayName != "" {
		return st.DisplayName
	}
	if obj := snap.VillageObjects[objID]; obj != nil && obj.DisplayName != "" {
		return obj.DisplayName
	}
	return ""
}

// buildAnchors projects the actor's own home and work structures into the
// always-on move-target view. Returns nil when the actor has neither anchor (a
// PC, or an unanchored NPC) so Render omits the line. The structure_ids are
// surfaced verbatim — they're what the model passes to move_to; the labels are
// best-effort (a structure with no DisplayName yields an empty label, which
// Render replaces with a generic phrase while still carrying the id).
func buildAnchors(snap *sim.Snapshot, a *sim.ActorSnapshot) *AnchorsView {
	v := &AnchorsView{}
	// Only surface an anchor whose id actually RESOLVES to a structure in the
	// snapshot. Surfacing an id that isn't in the world would render an
	// actionable-looking move_to target the engine then rejects — the exact
	// "bouncing target" failure this change exists to remove. A resolved
	// structure with no DisplayName still surfaces (the id is what move_to
	// needs; render uses a generic phrase for the empty label).
	if label, ok := resolveStructureLabel(snap, a.WorkStructureID); ok {
		v.WorkID = a.WorkStructureID
		v.WorkLabel = label
	}
	if label, ok := resolveStructureLabel(snap, a.HomeStructureID); ok {
		v.HomeID = a.HomeStructureID
		v.HomeLabel = label
	}
	if v.WorkID == "" && v.HomeID == "" {
		return nil
	}
	v.SamePlace = v.WorkID != "" && v.WorkID == v.HomeID
	return v
}

// resolveStructureLabel resolves a StructureID to its human label. ok is true
// when the id names a structure (or shared village_object — structures share
// ids with village_objects) PRESENT in the snapshot, false when the id is empty
// or absent. A present structure with no DisplayName returns ("", true): the
// caller still surfaces the actionable id and renders a generic phrase.
func resolveStructureLabel(snap *sim.Snapshot, sid sim.StructureID) (string, bool) {
	if sid == "" {
		return "", false
	}
	if st := snap.Structures[sid]; st != nil {
		return st.DisplayName, true
	}
	if obj := snap.VillageObjects[sim.VillageObjectID(sid)]; obj != nil {
		return obj.DisplayName, true
	}
	return "", false
}

// buildSurroundings assembles the actor's immediate context — the
// structure it occupies and the other members of its current huddle.
// Per-member acquaintance status is resolved against the subject
// actor's Acquaintances map so Render can swap name vs. descriptor
// without re-reading the snapshot.
func buildSurroundings(snap *sim.Snapshot, actorID sim.ActorID, a *sim.ActorSnapshot) SurroundingsView {
	s := SurroundingsView{
		InsideStructureID: a.InsideStructureID,
		HuddleID:          a.CurrentHuddleID,
		Atmosphere:        snap.Environment.Atmosphere,
		LocalMinuteOfDay:  snap.LocalMinuteOfDay,
	}
	if item, source, ok := findGatherableCue(snap, a); ok {
		s.GatherableItem = item
		s.GatherableSource = source
	}
	if a.InsideStructureID != "" {
		if st := snap.Structures[a.InsideStructureID]; st != nil {
			s.StructureName = st.DisplayName
		}
	} else {
		// Outdoors: name the structure whose loiter slot the actor is standing
		// at (a keeper at their own stall, a customer outside a shop), so Render
		// can say "outdoors by the General Store" rather than dumping coords.
		s.NearbyStructureName = findLoiterStructure(snap, a)
	}
	if a.CurrentHuddleID != "" {
		if h := snap.Huddles[a.CurrentHuddleID]; h != nil {
			for memberID := range h.Members {
				if memberID == actorID {
					continue
				}
				m := HuddleMember{ID: memberID}
				if peer := snap.Actors[memberID]; peer != nil {
					m.DisplayName = peer.DisplayName
					m.Role = peer.Role
				}
				if m.DisplayName != "" {
					_, m.Acquainted = a.Acquaintances[m.DisplayName]
				}
				s.HuddleMembers = append(s.HuddleMembers, m)
			}
			sort.Slice(s.HuddleMembers, func(i, j int) bool {
				return s.HuddleMembers[i].ID < s.HuddleMembers[j].ID
			})
		}
	}
	return s
}

// findGatherableCue resolves the nearest refresh-bearing VillageObject the
// subject is loitering at (within sim.LoiterAttributionTiles, Chebyshev) and,
// if THAT object is gatherable, returns (item, sourceName, true). Resolve-then-
// check, mirroring the sim.Gather Command's findRefreshObjectNear: only the
// nearest refresh object owns the tile, so a closer non-gatherable refresh
// object (e.g. a shade oak nearer than a well) correctly suppresses the cue.
// Returns ("", "", false) when no refresh object is in range or the nearest one
// isn't gatherable.
//
// ASSET-FREE by necessity (perception builds off the snapshot, no asset
// catalog), so two divergences from the authoritative resolver remain — both
// acceptable because the Command is the real gate and rejects cleanly:
//   - FALSE-POSITIVE: a closer NON-refresh named object (a bench) would own the
//     tile in the asset-aware command, which then rejects with ErrNoGatherSource
//     — but this scan only sees refresh objects, so it may still cue. The model
//     calls gather and gets a clean reject.
//   - FALSE-NEGATIVE: an offset-less gatherable object whose authoritative pin
//     is asset-footprint-derived (computeLoiterTile) may fall outside this
//     anchor-based pin; the cue won't show though the command would accept.
//     Gatherable props (wells, bushes) carry explicit loiter offsets, so this is
//     rare in practice.
//
// The gate and the rendered cue read the SAME SurroundingsView fields, so those
// two never drift; only this heuristic vs. the command can, per the above.
func findGatherableCue(snap *sim.Snapshot, a *sim.ActorSnapshot) (sim.ItemKind, string, bool) {
	bestCheb := -1
	var bestObj *sim.VillageObject
	var bestID sim.VillageObjectID
	for id, obj := range snap.VillageObjects {
		if obj == nil || len(obj.Refreshes) == 0 {
			continue
		}
		pin := obj.Pos.Tile()
		off := sim.TileOffset{}
		if obj.LoiterOffsetX != nil {
			off.DX = *obj.LoiterOffsetX
		}
		if obj.LoiterOffsetY != nil {
			off.DY = *obj.LoiterOffsetY
		}
		pin = pin.Add(off)
		cheb := a.Pos.Chebyshev(pin)
		if cheb > sim.LoiterAttributionTiles {
			continue
		}
		// Nearest refresh object wins; tie-break by ID for determinism.
		if bestCheb == -1 || cheb < bestCheb || (cheb == bestCheb && id < bestID) {
			bestCheb = cheb
			bestObj = obj
			bestID = id
		}
	}
	if bestObj == nil {
		return "", "", false
	}
	for _, r := range bestObj.Refreshes {
		if r.IsGatherable() {
			return sim.ItemKind(strings.TrimSpace(string(r.GatherItem))), bestObj.DisplayName, true
		}
	}
	return "", "", false
}

// findLoiterStructure returns the DisplayName of the structure whose loiter
// slot the subject is standing at (nearest within sim.LoiterAttributionTiles,
// Chebyshev), or "" when none. A structure shares its id with a VillageObject
// (the placement), so the loiter pin is that object's tile anchor plus its
// loiter offset — the same asset-free derivation findGatherableCue uses for
// gatherable props. Used only for the OUTDOORS position phrasing; when the
// actor is genuinely inside a structure the caller reads InsideStructureID
// instead. Ties break by lowest structure id for determinism.
func findLoiterStructure(snap *sim.Snapshot, a *sim.ActorSnapshot) string {
	bestCheb := -1
	var bestName string
	var bestID sim.StructureID
	for stID, st := range snap.Structures {
		if st == nil || st.DisplayName == "" {
			continue
		}
		vobj := snap.VillageObjects[sim.VillageObjectID(stID)]
		if vobj == nil {
			continue
		}
		pin := vobj.Pos.Tile()
		off := sim.TileOffset{}
		if vobj.LoiterOffsetX != nil {
			off.DX = *vobj.LoiterOffsetX
		}
		if vobj.LoiterOffsetY != nil {
			off.DY = *vobj.LoiterOffsetY
		}
		pin = pin.Add(off)
		cheb := a.Pos.Chebyshev(pin)
		if cheb > sim.LoiterAttributionTiles {
			continue
		}
		if bestCheb == -1 || cheb < bestCheb || (cheb == bestCheb && stID < bestID) {
			bestCheb = cheb
			bestName = st.DisplayName
			bestID = stID
		}
	}
	return bestName
}

// descriptorLabel renders an actor reference as the subject would name them:
// their DisplayName when acquainted, else "the <role>" (e.g. "the blacksmith")
// for a known trade, else "a stranger". The single source of truth for the
// name-vs-descriptor swap shared by HuddleMembers (renderHuddleMember) and the
// warrant actor-name map (buildWarrantActorNames) — ZBBS-HOME-339.
func descriptorLabel(displayName, role string, acquainted bool) string {
	if acquainted && displayName != "" {
		return displayName
	}
	if role != "" {
		return "the " + role
	}
	return "a stranger"
}

// buildWarrantActorNames resolves every OTHER actor referenced by a warrant in
// the batch to its acquaintance-gated label, so Render never leaks a raw actor
// UUID into the "## What just happened" lines (ZBBS-HOME-339). The subject's
// own ID is excluded — Render resolves self to "you". Returns nil when no
// warrant references another actor (the common single-actor tick).
func buildWarrantActorNames(snap *sim.Snapshot, subject *sim.ActorSnapshot, subjectID sim.ActorID, warrants []sim.WarrantMeta) map[sim.ActorID]string {
	var names map[sim.ActorID]string
	add := func(id sim.ActorID) {
		if id == "" || id == subjectID {
			return
		}
		if names == nil {
			names = make(map[sim.ActorID]string)
		}
		if _, done := names[id]; done {
			return
		}
		peer := snap.Actors[id]
		if peer == nil {
			// Actor gone from the snapshot (e.g. deleted between event and
			// publish). Leave it out; Render falls back to a neutral label.
			return
		}
		acquainted := false
		if peer.DisplayName != "" {
			_, acquainted = subject.Acquaintances[peer.DisplayName]
		}
		names[id] = descriptorLabel(peer.DisplayName, peer.Role, acquainted)
	}
	for _, w := range warrants {
		add(w.TriggerActorID)
		switch r := w.Reason.(type) {
		case sim.PCSpeechWarrantReason:
			add(r.Speaker)
		case sim.NPCSpeechWarrantReason:
			add(r.Speaker)
		case sim.PaidWarrantReason:
			add(r.Buyer)
		case sim.PayOfferWarrantReason:
			add(r.Buyer)
		case sim.SceneQuoteTargetedWarrantReason:
			add(r.SellerID)
		case sim.PayResolvedWarrantReason:
			add(r.Seller)
		}
	}
	return names
}

// buildNarrativeState returns the kind-aware "Who you are:" content
// for shared-VA actors, or nil otherwise. Stateful and PC actors get
// no engine-side narrative — their identity comes from their own VA's
// <Self> block (stateful) or from the player (PC).
//
// Returns nil for an empty NarrativeState too — Render is content-
// gated, so a nil view skips the section cleanly.
func buildNarrativeState(a *sim.ActorSnapshot) *NarrativeStateView {
	if a.Kind != sim.KindNPCShared || a.Narrative == nil {
		return nil
	}
	if a.Narrative.SeedText == "" && a.Narrative.EvolvingSummary == "" {
		return nil
	}
	return &NarrativeStateView{
		SeedText:        a.Narrative.SeedText,
		EvolvingSummary: a.Narrative.EvolvingSummary,
	}
}

// recentSalientFactsPerPeer is the per-peer ceiling on facts surfaced
// into perception. Mirrors v1's formatRelationshipsPerception which
// renders the most-recent 3. RecentFacts is the slice end of the
// stored oldest-first SalientFacts, reversed to most-recent-first.
const recentSalientFactsPerPeer = 3

// buildRelationships projects per-co-huddle-peer relationship views
// from the subject actor's Relationships map. Populated only for
// shared-VA actors. Peers in the huddle without a Relationship row
// (e.g. just-met strangers — the Relationship is only written by
// speech/pay/serve/deliver handlers, not first-encounter) are omitted
// silently rather than rendered as empty views.
//
// Ordering: by PeerID, matching SurroundingsView.HuddleMembers'
// sort order, so a reader of both blocks sees the same peer order.
func buildRelationships(a *sim.ActorSnapshot, members []HuddleMember) []RelationshipPeerView {
	if a.Kind != sim.KindNPCShared || len(a.Relationships) == 0 || len(members) == 0 {
		return nil
	}
	out := make([]RelationshipPeerView, 0, len(members))
	for _, m := range members {
		rel := a.Relationships[m.ID]
		if rel == nil {
			continue
		}
		out = append(out, RelationshipPeerView{
			PeerID:      m.ID,
			PeerName:    m.DisplayName,
			SummaryText: rel.SummaryText,
			RecentFacts: recentFactsMostRecentFirst(rel.SalientFacts, recentSalientFactsPerPeer),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildPendingOrderViews scans snap.Orders for open Orders touching
// the subject and returns two slices:
//   - fromMe: Orders where subject is the seller (handed-over duty).
//   - toMe:   Orders where subject is the buyer OR a consumer
//     (incoming delivery).
//
// Only OrderStateReady entries appear; terminal Orders are filtered
// out (no actionable signal). Returns nil for empty results so render
// can content-gate cheaply.
//
// Names are resolved via snap.Actors; missing actors fall back to the
// stringified ActorID (defensive, e.g. if an actor was deleted between
// Order creation and the next snapshot).
//
// ConsumerNames is populated only when ConsumerIDs differs from
// [BuyerID] — the implicit "buyer is the consumer" case leaves it
// empty so render skips the "and others" embellishment.
//
// Ordering: by Order.ID ascending. Deterministic across runs.
func buildPendingOrderViews(snap *sim.Snapshot, subject sim.ActorID) (fromMe, toMe []OrderView) {
	if snap == nil || len(snap.Orders) == 0 {
		return nil, nil
	}
	resolveName := func(id sim.ActorID) string {
		if a := snap.Actors[id]; a != nil && a.DisplayName != "" {
			return a.DisplayName
		}
		return string(id)
	}
	// Pre-collect IDs so we can sort deterministically before
	// resolving names + building views.
	var fromIDs, toIDs []sim.OrderID
	for id, o := range snap.Orders {
		if o == nil || o.State != sim.OrderStateReady {
			continue
		}
		if o.SellerID == subject {
			fromIDs = append(fromIDs, id)
			continue
		}
		// toMe: subject is buyer or in ConsumerIDs.
		if o.BuyerID == subject {
			toIDs = append(toIDs, id)
			continue
		}
		for _, cid := range o.ConsumerIDs {
			if cid == subject {
				toIDs = append(toIDs, id)
				break
			}
		}
	}
	sort.Slice(fromIDs, func(i, j int) bool { return fromIDs[i] < fromIDs[j] })
	sort.Slice(toIDs, func(i, j int) bool { return toIDs[i] < toIDs[j] })

	toView := func(o *sim.Order) OrderView {
		v := OrderView{
			ID:         o.ID,
			Item:       o.Item,
			Qty:        o.Qty,
			BuyerName:  resolveName(o.BuyerID),
			SellerName: resolveName(o.SellerID),
			CreatedAt:  o.CreatedAt,
			ExpiresAt:  o.ExpiresAt,
		}
		// Only populate ConsumerNames when there's more than just
		// the implicit buyer-as-consumer entry.
		if len(o.ConsumerIDs) > 1 || (len(o.ConsumerIDs) == 1 && o.ConsumerIDs[0] != o.BuyerID) {
			v.ConsumerNames = make([]string, 0, len(o.ConsumerIDs))
			for _, cid := range o.ConsumerIDs {
				v.ConsumerNames = append(v.ConsumerNames, resolveName(cid))
			}
		}
		return v
	}
	if len(fromIDs) > 0 {
		fromMe = make([]OrderView, 0, len(fromIDs))
		for _, id := range fromIDs {
			fromMe = append(fromMe, toView(snap.Orders[id]))
		}
	}
	if len(toIDs) > 0 {
		toMe = make([]OrderView, 0, len(toIDs))
		for _, id := range toIDs {
			toMe = append(toMe, toView(snap.Orders[id]))
		}
	}
	return fromMe, toMe
}

// recentFactsMostRecentFirst returns up to n facts from the tail of
// the oldest-first stored slice, reversed so the most-recent is first.
// Returns nil for an empty input.
func recentFactsMostRecentFirst(facts []sim.SalientFact, n int) []sim.SalientFact {
	if len(facts) == 0 || n <= 0 {
		return nil
	}
	start := len(facts) - n
	if start < 0 {
		start = 0
	}
	tail := facts[start:]
	out := make([]sim.SalientFact, len(tail))
	for i, f := range tail {
		out[len(tail)-1-i] = f
	}
	return out
}
