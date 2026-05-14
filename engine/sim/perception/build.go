package perception

import (
	"fmt"
	"sort"

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

	p.Actor = buildActorView(actorSnap)
	p.Surroundings = buildSurroundings(snap, actorID, actorSnap)

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
		PositionChanged:  origin.CurrentX != current.CurrentX || origin.CurrentY != current.CurrentY,
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
// alias the snapshot's map.
func buildActorView(a *sim.ActorSnapshot) ActorView {
	var needs map[sim.NeedKey]int
	if len(a.Needs) > 0 {
		needs = make(map[sim.NeedKey]int, len(a.Needs))
		for k, v := range a.Needs {
			needs[k] = v
		}
	}
	return ActorView{
		State:             a.State,
		InsideStructureID: a.InsideStructureID,
		Position:          sim.Position{X: a.CurrentX, Y: a.CurrentY},
		CurrentHuddleID:   a.CurrentHuddleID,
		Coins:             a.Coins,
		Needs:             needs,
	}
}

// buildSurroundings assembles the actor's immediate context — the
// structure it occupies and the other members of its current huddle.
func buildSurroundings(snap *sim.Snapshot, actorID sim.ActorID, a *sim.ActorSnapshot) SurroundingsView {
	s := SurroundingsView{
		InsideStructureID: a.InsideStructureID,
		HuddleID:          a.CurrentHuddleID,
	}
	if a.InsideStructureID != "" {
		if st := snap.Structures[a.InsideStructureID]; st != nil {
			s.StructureName = st.DisplayName
		}
	}
	if a.CurrentHuddleID != "" {
		if h := snap.Huddles[a.CurrentHuddleID]; h != nil {
			for member := range h.Members {
				if member == actorID {
					continue
				}
				s.HuddleMembers = append(s.HuddleMembers, member)
			}
			sort.Slice(s.HuddleMembers, func(i, j int) bool {
				return s.HuddleMembers[i] < s.HuddleMembers[j]
			})
		}
	}
	return s
}
