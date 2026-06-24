package perception

import (
	"fmt"
	"sort"
	"strings"
	"time"

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

	// ZBBS-HOME-413: drop any seller-side pay-offer warrant whose ledger entry
	// is no longer pending. The PayOfferWarrant is stamped once (on
	// PayOfferReceived) and only cleared when the seller's next reactor tick
	// consumes it; resolution stamps the BUYER, never the seller's standing
	// warrant. So an offer that resolves out-of-band (buyer withdrew, TTL
	// expired, or the seller was rest-gated and couldn't tick) before the
	// seller consumes it would otherwise render a dead "what just happened"
	// line. Build runs on the immutable snapshot, so checking live ledger
	// state is race-free here. (Since ZBBS-HOME-453 the decision section and
	// the tool gate no longer read the warrant batch — see PayOffersForMe
	// below — so this filter now only protects the generic warrant render.)
	p.Warrants = filterStalePayOfferWarrants(p.Warrants, snap)

	// ZBBS-HOME-453: the standing seller-side offer view, scanned from
	// snap.PayLedger every tick. The warrant above only WAKES the seller's
	// first tick; this scan is what keeps "## Offers awaiting your decision"
	// and the accept/decline/counter tools present until the entry leaves
	// Pending, so a seller who speaks through the warranted tick instead of
	// resolving can still settle the offer on any later tick.
	p.PayOffersForMe = buildPayOffersForMe(snap, actorID)
	p.RoomAlreadySoldOrderByLedger = buildRoomAlreadySold(snap, actorID, p.PayOffersForMe)

	p.Actor = buildActorView(snap, actorSnap)
	p.WarrantActorNames = buildWarrantActorNames(snap, actorSnap, actorID, p.Warrants, p.PayOffersForMe)
	p.WarrantPlaceNames = buildWarrantPlaceNames(snap, p.Warrants)
	p.EatHereKinds = buildEatHereKinds(snap)
	p.Surroundings = buildSurroundings(snap, actorID, actorSnap)
	p.TurnState = buildTurnState(snap, actorID, actorSnap, p.Surroundings.HuddleMembers)
	p.Anchors = buildAnchors(snap, actorSnap)
	p.NarrativeState = buildNarrativeState(actorSnap)
	p.Businessowner = actorSnap.BusinessownerState != nil
	// AtOwnBusiness narrows Businessowner to "at my own post" — a businessowner is
	// only open for trade while physically at their business structure (the
	// WorkStructureID anchor that renders as "you keep your trade at X"). The vendor
	// cues gate on this, not bare Businessowner, so a keeper who is a customer
	// elsewhere (Prudence pitching Water mid-meal in John Ellis's tavern) isn't told
	// to sell. ZBBS-WORK-385.
	p.AtOwnBusiness = p.Businessowner && actorSnap.WorkStructureID != "" && actorSnap.InsideStructureID == actorSnap.WorkStructureID
	heardNow := currentHeardExcerpts(p.Warrants)
	p.Relationships = buildRelationships(actorSnap, p.Surroundings.HuddleMembers, heardNow)
	p.RecentConversation = buildRecentConversation(snap, actorID, actorSnap, heardNow)
	p.OfferableCustomers = buildOfferableCustomers(snap, actorID, p.AtOwnBusiness, p.Surroundings.HuddleMembers, p.Actor.Inventory)
	p.StandingQuotesFromMe = buildStandingQuotesFromMe(snap, actorID, actorSnap)
	p.PendingDeliveriesFromMe, p.PendingDeliveriesToMe = buildPendingOrderViews(snap, actorID)
	p.PendingOffersFromMe = buildPendingOffersFromMe(snap, actorID, actorSnap)
	p.RecentlyResolvedOffersFromMe = buildRecentlyResolvedOffersFromMe(snap, actorID, actorSnap)
	p.CountersAwaitingMyResponse = buildCountersAwaitingMyResponse(snap, actorID, actorSnap)
	p.LocalDateUTC = snap.LocalDateUTC // world "today" for the order-book date split (ZBBS-HOME-403)
	p.RecoveryOptions = buildRecoveryOptions(snap, actorID, actorSnap)
	p.Satiation = buildSatiation(snap, actorID, actorSnap)
	p.Restocking = buildRestocking(snap, actorID, actorSnap)
	// customerEngaged (LLM-90): the seller-side "someone's at my stall right now"
	// signal — a buyer's pending offer awaiting my decision (PayOffersForMe), a
	// quote I have standing out to a buyer (StandingQuotesFromMe), or simply a
	// co-present companion while I'm at my own post (a live interaction at the
	// stall). buildForage defers the harvest cue on it so a grower finishes the
	// encounter before walking off to her bushes, rather than abandoning someone
	// mid-transaction. The co-presence arm is the raw at-own-post huddle check, NOT
	// p.OfferableCustomers — that view needs goods on hand to fire, so an empty-
	// shelf grower (exactly when the harvest cue triggers) with a customer in front
	// of her would slip through it.
	customerEngaged := len(p.PayOffersForMe) > 0 ||
		len(p.StandingQuotesFromMe) > 0 ||
		(p.AtOwnBusiness && len(p.Surroundings.HuddleMembers) > 0)
	p.Forage = buildForage(snap, actorID, actorSnap, customerEngaged)
	// DutySteer is built AFTER Restocking + Forage (ZBBS-HOME-400 Option B /
	// LLM-90): the return-to-post cue is suppressed while a restock OR forage
	// errand is active, and the at-post stabilizer flips to a step-out line under a
	// forage errand — p.Restocking != nil and p.Forage != nil are exactly those
	// signals. (p.Forage already encodes "not mid-customer" via customerEngaged.)
	p.DutySteer = buildDutySteer(snap, actorID, actorSnap, p.Anchors, p.Restocking != nil, p.Forage != nil)
	p.DutyPending = buildDutyPending(snap, actorSnap, p.Anchors)
	// Stay-open choice (ZBBS-WORK-387): a keeper standing at its own post on an
	// off-shift wind-down may keep its business open instead of closing up. Surface
	// the option, and encourage it when a concrete reason is present (the hybrid
	// gate — an owed order, a co-present buyer, or a pending offer; the same class
	// of "unfinished business" signal the HOME-400 to-work gate reads). Computed
	// here, after buildDutySteer, off the already-built order/offer/customer views.
	if p.DutySteer != nil && !p.DutySteer.ToWork && !p.DutySteer.AtPost && p.AtOwnBusiness {
		p.DutySteer.OfferStayOpen = true
		p.DutySteer.StayOpenReason = stayOpenReason(
			len(p.PendingDeliveriesFromMe) > 0,
			p.OfferableCustomers != nil,
			len(p.PendingOffersFromMe) > 0,
		)
	}
	p.Lodging = buildLodgingView(snap, actorID, actorSnap)
	// LLM-36: the lodger bedtime nudge — fires for a lodger that has wound down
	// to its rented inn once the night window opens, with a co-present companion
	// to bid goodnight to, so it retires deliberately. Gated on the same audience
	// as the engine backstop hold (npc_sleep.go huddleWithCompanion).
	p.Retire = buildRetireCue(snap, actorSnap, p.Surroundings.HuddleMembers)
	p.KeeperLodging = buildKeeperLodgingView(snap, actorSnap, p.Surroundings.HuddleMembers)
	// The held-lodger signal is informational, like "## Your inn" — ungated by
	// location so a keeper affirms a settled guest wherever they meet (LLM-38).
	// It keys off KeeperLodging, so it inherits the LLM-22 awake-peer gate: a
	// co-present held lodger conversing IS an awake peer, so the cue still fires
	// exactly when there's someone to affirm to.
	p.KeeperHeldLodgers = buildKeeperHeldLodgers(snap, actorID, p.KeeperLodging, p.Surroundings.HuddleMembers)
	// The offer cue is location-bound the way vendor cues are (ZBBS-WORK-385's
	// at-own-post principle): a keeper drinking at someone ELSE's
	// establishment must not be steered to sell their own rooms into that
	// huddle (observed live: Hannah pitching her Inn's rooms from inside
	// John's Tavern, and a guest buying one there — ZBBS-HOME-424). Gated on
	// the location predicate directly rather than p.AtOwnBusiness because the
	// keeper-lodging views key on WorkStructureID alone, not on
	// BusinessownerState — an innkeeper without vendor state still keeps
	// rooms. The informational "## Your inn" status section stays ungated by
	// LOCATION (a keeper sees their own inn's vacancy from anywhere); it has its
	// own audience gate inside buildKeeperLodgingView (LLM-22 — no awake peer,
	// no section). Only the act-now offer instruction is location-bound.
	if actorSnap.WorkStructureID != "" && actorSnap.InsideStructureID == actorSnap.WorkStructureID {
		p.LodgingOffer = buildLodgingOfferCue(snap, actorID, p.KeeperLodging, p.Surroundings.HuddleMembers)
	}
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
		State:                  a.State,
		InsideStructureID:      a.InsideStructureID,
		Position:               sim.Position{X: a.Pos.X, Y: a.Pos.Y},
		CurrentHuddleID:        a.CurrentHuddleID,
		Coins:                  a.Coins,
		Needs:                  needs,
		NeedThresholds:         snap.NeedThresholds,
		ActiveDwellCredits:     buildActiveDwellCredits(snap, a),
		InFlightMove:           buildInFlightMove(snap, a),
		InFlightSourceActivity: buildInFlightSourceActivity(snap, a),
		Inventory:              buildInventoryView(snap, a),
		HoursAwake:             computeHoursAwake(snap.LocalMinuteOfDay, a.ScheduleStartMin, a.ScheduleEndMin),
	}
}

// computeHoursAwake returns whole hours the actor has been awake, measured from
// its shift-start — but ONLY while it is on-shift. On-shift guarantees
// continuous wakefulness since shift-start: NPCs wake at shift-start
// (ZBBS-HOME-435) and only auto-sleep off-shift, so the elapsed-since-start is
// true hours-awake. Off-shift the schedule alone can't tell "still up since this
// morning" from "slept, now awake before the next shift" — the modular elapsed
// would overstate (e.g. ~23h for a day-shift NPC awake before dawn) — so the
// tail is dropped and renderTiredness falls back to the bare tier phrase. The
// modulo handles wrap-midnight shifts (start 16:00, now 02:00 → 10h on-shift).
// Returns nil off-shift, unscheduled (nil bounds), or with no clock. LLM-85.
func computeHoursAwake(nowMin, startMin, endMin *int) *int {
	if nowMin == nil {
		return nil
	}
	// OnShiftAtMinute returns false for nil bounds, so a true result also
	// guarantees startMin is non-nil for the deref below.
	if !sim.OnShiftAtMinute(startMin, endMin, *nowMin) {
		return nil
	}
	minutesAwake := ((*nowMin-*startMin)%1440 + 1440) % 1440
	hours := minutesAwake / 60
	return &hours
}

// buildInFlightSourceActivity projects the subject's in-flight SourceActivity
// (the read-path Kind/ObjectID/Attribute fields the snapshot carries while the
// window is live) into a render-ready view, or nil when the actor isn't engaged
// at a source. SourceLabel resolves the same way dwell-pin and move labels do
// (resolveDwellPinLabel against snap.Structures / snap.VillageObjects). LLM-69.
func buildInFlightSourceActivity(snap *sim.Snapshot, a *sim.ActorSnapshot) *InFlightSourceActivityView {
	if a.SourceActivityKind == "" {
		return nil
	}
	return &InFlightSourceActivityView{
		Kind:        a.SourceActivityKind,
		SourceLabel: resolveDwellPinLabel(snap, a.SourceActivityObjectID),
		Attribute:   a.SourceActivityAttribute,
	}
}

// buildInventoryView resolves the actor's carried goods (positive quantities)
// into the standing inventory readout — display labels via itemDisplayLabel,
// sorted by label then ItemKind so the line is deterministic (Inventory is a
// map). Returns nil for an empty inventory so Render omits the line.
// ZBBS-HOME-361.
func buildInventoryView(snap *sim.Snapshot, a *sim.ActorSnapshot) []InventoryItem {
	if len(a.Inventory) == 0 {
		return nil
	}
	out := make([]InventoryItem, 0, len(a.Inventory))
	for kind, qty := range a.Inventory {
		if qty <= 0 {
			continue
		}
		out = append(out, InventoryItem{Label: itemDisplayLabel(snap, kind), Qty: qty, kind: kind})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].kind < out[j].kind
	})
	return out
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
		// Co-location gate (LLM-68): render a credit as an active dwell only
		// while the actor is still at its pin. The credit lingers in the map
		// until the next dwell-tick walk-away sweep deletes it; without this
		// gate perception keeps asserting "you are resting at X" after the actor
		// has walked off, steering the model to stay put and do nothing. Mirrors
		// the dwell-tick walk-away check actorAtCreditObject (ok && id ==
		// credit.ObjectID): an empty CurrentLoiterObjectID means the actor
		// stands at no pin (resolver returned !ok), so every credit drops.
		if a.CurrentLoiterObjectID == "" || c.ObjectID != a.CurrentLoiterObjectID {
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
	// Every credit may have been co-location-gated out (the actor walked off
	// all its pins) — return nil so this renders identically to the no-credits
	// case and Render omits the line, matching buildInventoryView's posture.
	if len(out) == 0 {
		return nil
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
	if item, source, ok := findGatherableCue(snap, actorID, a); ok {
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
				s.HuddleMembers = append(s.HuddleMembers, resolveCoPresentMember(snap, a, memberID))
			}
			sort.Slice(s.HuddleMembers, func(i, j int) bool {
				return s.HuddleMembers[i].ID < s.HuddleMembers[j].ID
			})
		}
	} else {
		// Not huddled: surface who is within earshot — the set the speak path would
		// reach if the actor spoke now (ActorSnapshot.ColocatedAudienceIDs, computed
		// world-side so this line and the speak no-audience gate share one scope
		// rule). Same acquaintance gating as the huddle roster; the IDs arrive
		// pre-sorted from the world-side helper. ZBBS-WORK-407.
		for _, id := range a.ColocatedAudienceIDs {
			m := resolveCoPresentMember(snap, a, id)
			// A resting audience member can't be roused by THIS NPC's speech
			// (NPC-to-NPC speech doesn't interrupt rest — reactor.go
			// actorCanReactNow; only a PC / red-tier need / operator nudge does),
			// so it would sit silent if addressed. Route it to the not-addressable
			// clause like a sleeper. It stays in ColocatedAudienceIDs (the shared
			// audience / speak-gate set), so a PC — who CAN wake a rester — is
			// unaffected (ZBBS-WORK-426).
			if peer := snap.Actors[id]; peer != nil && peer.State == sim.StateResting {
				s.CoPresentResting = append(s.CoPresentResting, m)
				continue
			}
			m.JustArrived = coPresentJustArrived(snap, id)
			s.CoPresent = append(s.CoPresent, m)
		}
		// Co-present sleepers (ZBBS-WORK-426): excluded from the audience entirely
		// (colocatedSleeperIDs), surfaced here so Render marks them not-addressable
		// rather than dropping them from the actor's view.
		for _, id := range a.ColocatedSleeperIDs {
			s.CoPresentAsleep = append(s.CoPresentAsleep, resolveCoPresentMember(snap, a, id))
		}
	}
	return s
}

// resolveCoPresentMember builds a HuddleMember view for memberID: display name +
// role from the snapshot, acquaintance status from the subject's Acquaintances
// map. Shared by the huddle roster and the co-presence line (ZBBS-WORK-407) so
// both render with identical name-vs-descriptor gating.
func resolveCoPresentMember(snap *sim.Snapshot, subj *sim.ActorSnapshot, memberID sim.ActorID) HuddleMember {
	m := HuddleMember{ID: memberID}
	if peer := snap.Actors[memberID]; peer != nil {
		m.DisplayName = peer.DisplayName
		m.Role = peer.Role
	}
	if m.DisplayName != "" {
		_, m.Acquainted = subj.Acquaintances[m.DisplayName]
	}
	return m
}

// coPresentJustArrivedWindow bounds how long after an actor's arrival a
// co-present observer still reads it as "just arrived" in "## Around you"
// (ZBBS-WORK-422). The window trades catch-rate against staleness: a peer
// arrival stamps NO warrant on observers (the deliberate no-force-wake choice —
// greet/encounter huddles already cover the must-react cases), so an unhuddled
// observer only sees the tag when it ticks for its own reasons. The window must
// comfortably exceed the gap to that next organic tick, yet stay short enough
// that "just arrived" doesn't linger on someone who has clearly settled in.
const coPresentJustArrivedWindow = 90 * time.Second

// coPresentJustArrived reports whether memberID reached its current spot within
// coPresentJustArrivedWindow of the snapshot's publish time. It reads the
// arrival straight from the snapshot action log (every arrival is recorded as
// an ActionTypeWalked entry — see the action-log substrate), so it needs no new
// per-actor state and no checkpoint column for what is a transient signal. A
// member's most recent ActionTypeWalked IS its arrival at the current spot
// (moving away mints a fresh entry), so any such entry within the window means
// it just got here. O(log) per member; the log is small (capped retention).
func coPresentJustArrived(snap *sim.Snapshot, memberID sim.ActorID) bool {
	cutoff := snap.PublishedAt.Add(-coPresentJustArrivedWindow)
	for i := range snap.ActionLog {
		e := snap.ActionLog[i]
		if e.ActorID == memberID && e.ActionType == sim.ActionTypeWalked && !e.OccurredAt.Before(cutoff) {
			return true
		}
	}
	return false
}

// buildTurnState derives the subject's conversation turn-state (ZBBS-WORK-370)
// from the directed awaiting-reply edges among its present huddle peers. For
// each peer it answers two questions off the snapshot maps, applying the
// addressee-kind liveness window (snap.PublishedAt as the clock) so a lapsed
// edge is ignored — keeping the rendered nudge in lockstep with the sim.Speak
// backstop's expiry:
//
//   - does the SUBJECT await a live reply FROM this peer?  (subject's own edge,
//     window keyed on the peer = the addressee) -> AwaitingReplyFrom: "you spoke
//     to them, wait."
//   - does this PEER await a live reply from the SUBJECT? (peer's edge to me,
//     window keyed on the subject = the addressee) -> OwedReplyTo: "they are
//     waiting for your reply."
//
// Names are the same acquaintance-gated labels the huddle roster renders
// (descriptorLabel). members is already sorted by ID (buildSurroundings), so the
// output slices are deterministic. Returns the zero value (no lines) when the
// actor has no present peers or no live edges.
func buildTurnState(snap *sim.Snapshot, actorID sim.ActorID, subj *sim.ActorSnapshot, members []HuddleMember) TurnStateView {
	var ts TurnStateView
	if snap == nil || subj == nil || len(members) == 0 {
		return ts
	}
	now := snap.PublishedAt
	subjWindow := awaitWindowForKind(snap, subj.Kind)
	for _, m := range members {
		peer := snap.Actors[m.ID]
		if peer == nil {
			continue
		}
		label := descriptorLabel(m.DisplayName, m.Role, m.Acquainted)
		// Subject addressed this peer and awaits their reply — the addressee is
		// the peer, so the window is keyed on the peer's kind.
		if awaitEdgeLive(subj.AwaitingReplyFrom, m.ID, now, awaitWindowForKind(snap, peer.Kind)) {
			ts.AwaitingReplyFrom = append(ts.AwaitingReplyFrom, label)
		}
		// This peer addressed the subject and awaits the subject's reply — the
		// addressee is the subject, so the window is keyed on the subject's kind.
		if awaitEdgeLive(peer.AwaitingReplyFrom, actorID, now, subjWindow) {
			ts.OwedReplyTo = append(ts.OwedReplyTo, label)
		}
	}
	return ts
}

// awaitWindowForKind picks the turn-state liveness window for an edge whose
// ADDRESSEE is of the given kind, off the resolved snapshot windows (the
// Default*AwaitReplyWindow fallback is already applied at publish). PC addressee
// → the long window; every NPC kind → the short one.
func awaitWindowForKind(snap *sim.Snapshot, addresseeKind sim.ActorKind) time.Duration {
	if addresseeKind == sim.KindPC {
		return snap.PCAwaitReplyWindow
	}
	return snap.NPCAwaitReplyWindow
}

// awaitEdgeLive reports whether `edges` holds an entry for `key` that is still
// live at `now` under `window`. A missing entry, or one older than the window,
// is not live. window <= 0 means "no expiry configured" → an existing entry
// counts as live (the hand-built-snapshot posture; a published snapshot always
// carries a positive resolved window).
func awaitEdgeLive(edges map[sim.ActorID]time.Time, key sim.ActorID, now time.Time, window time.Duration) bool {
	stamp, ok := edges[key]
	if !ok {
		return false
	}
	if window <= 0 {
		return true
	}
	return now.Sub(stamp) < window
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
func findGatherableCue(snap *sim.Snapshot, subjectID sim.ActorID, a *sim.ActorSnapshot) (sim.ItemKind, string, bool) {
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
			// Strict owner-gate (LLM-50 D2): don't dangle an owned source to a
			// non-owner — the Gather command rejects it (ErrNotYourSource), and
			// suppressing the cue here also keeps the gather tool unadvertised
			// (gateTools reads this same SurroundingsView field). The owner
			// still sees their own bush.
			if bestObj.OwnedByOther(subjectID) {
				return "", "", false
			}
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

// buildWarrantPlaceNames resolves the destination named by each
// ArrivalWarrantReason in the batch — a structure (StructureEnter/Visit) or a
// village object (ObjectVisit) — to its display name, so Render can say "You
// arrived at <place>" without the vacuous "arrived nearby" (ZBBS-WORK-358).
// Keyed by the raw id string (structure + object ids share one space under the
// shared-identity bridge). Returns nil when no arrival warrant names a place
// (the common non-arrival tick).
func buildWarrantPlaceNames(snap *sim.Snapshot, warrants []sim.WarrantMeta) map[string]string {
	// Build already returns early on a nil snapshot before reaching here, but
	// keep the helper independently safe for direct callers/tests (code_review).
	if snap == nil {
		return nil
	}
	var names map[string]string
	put := func(id, name string) {
		if id == "" || name == "" {
			return
		}
		if names == nil {
			names = make(map[string]string)
		}
		names[id] = name
	}
	for _, w := range warrants {
		r, ok := w.Reason.(sim.ArrivalWarrantReason)
		if !ok {
			continue
		}
		if r.AtStructureID != "" {
			if st := snap.Structures[r.AtStructureID]; st != nil {
				put(string(r.AtStructureID), st.DisplayName)
			}
		}
		if r.AtObjectID != "" {
			if o := snap.VillageObjects[r.AtObjectID]; o != nil {
				put(string(r.AtObjectID), o.DisplayName)
			}
		}
	}
	return names
}

// buildEatHereKinds collects the kinds that always settle eat-here
// (ItemKindDef.EatHereOnly — consumable, neither service nor portable),
// so Render can state the disposition fact on a quote warrant line
// instead of leaving the model to discover the WORK-405 clamp by
// tripping it. Returns nil when the catalog has no eat-here-only kind.
func buildEatHereKinds(snap *sim.Snapshot) map[sim.ItemKind]bool {
	if snap == nil {
		return nil
	}
	var kinds map[sim.ItemKind]bool
	for kind, def := range snap.ItemKinds {
		if def.EatHereOnly() {
			if kinds == nil {
				kinds = make(map[sim.ItemKind]bool)
			}
			kinds[kind] = true
		}
	}
	return kinds
}

// buildWarrantActorNames resolves every OTHER actor referenced by a warrant in
// the batch to its acquaintance-gated label, so Render never leaks a raw actor
// UUID into the "## What just happened" lines (ZBBS-HOME-339). The subject's
// own ID is excluded — Render resolves self to "you". Returns nil when no
// warrant references another actor (the common single-actor tick).
func buildWarrantActorNames(snap *sim.Snapshot, subject *sim.ActorSnapshot, subjectID sim.ActorID, warrants []sim.WarrantMeta, payOffers []sim.PayOfferWarrantReason) map[sim.ActorID]string {
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
		case sim.ServeHandoverWarrantReason:
			add(r.Buyer)
		}
	}
	// The standing offer view renders buyers on ticks that carry no offer
	// warrant (ZBBS-HOME-453), so their labels must resolve here too —
	// otherwise renderPayOffers falls back to "someone" the moment the
	// warranted tick has passed.
	for _, o := range payOffers {
		add(o.Buyer)
	}
	return names
}

// minuteInWindow reports whether now (0..1439) falls in [start, end), handling
// wrap-midnight windows (start > end, e.g. a 16:00–03:00 tavern shift). Mirrors
// sim.minuteInShiftWindow / isActorOnShift — kept consistent on purpose:
// start == end is an EMPTY window (never on shift), not all-day. Replicated
// here (rather than calling into sim) so perception stays a pure reader of the
// snapshot and doesn't couple to the work-domain shift producer. ZBBS-HOME-352.
func minuteInWindow(start, end, now int) bool {
	if start == end {
		// Empty window — never on shift. Kept explicit (not folded into the
		// start<=end arm) so a later "simplify" can't turn it into an all-day
		// window; matches sim.minuteInShiftWindow's start==end rule.
		return false
	}
	if start < end {
		return now >= start && now < end
	}
	return now >= start || now < end
}

// buildDutySteer computes the standing return-to-post cue: an agent NPC that is
// on-shift away from its workplace, or off-shift away from home. The shift
// window is the actor's own schedule when both bounds are set, else the world
// day-active (dawn/dusk) window from the snapshot — mirroring sim's
// effectiveShiftWindow. Position uses InsideStructureID (matching the engine's
// shiftDutyTarget notion of "at post"). Unlike the engine warrant it is NOT
// need-suppressed: it surfaces the duty and lets the model weigh it against any
// pressing need (the model-prioritizes design). Returns nil when at-post, out
// of scope, or the clock/anchors/window can't be resolved. ZBBS-HOME-352.
//
// a is guaranteed non-nil by Build's early return on a missing actor snapshot —
// the same invariant buildAnchors and the other sub-builders rely on.
func buildDutySteer(snap *sim.Snapshot, actorID sim.ActorID, a *sim.ActorSnapshot, anchors *AnchorsView, hasRestockErrand, hasForageErrand bool) *DutySteerView {
	// Nil guard FIRST, so the a.Kind / clock dereferences below are safe even
	// when buildDutySteer is called directly (Build never passes a nil actor
	// snapshot, but the unit tests do). No anchors → no work/home to steer
	// toward; no clock → can't tell the hour. (code_review, HOME-400 Option B.)
	if snap == nil || a == nil || anchors == nil || snap.LocalMinuteOfDay == nil {
		return nil
	}
	// Agent NPCs only — PCs are player-driven; decoratives are walked directly
	// by the shift ticker and never get a perception prompt.
	if a.Kind != sim.KindNPCStateful && a.Kind != sim.KindNPCShared {
		return nil
	}
	// A pressing (red) need outranks duty: don't march an exhausted/starving NPC
	// to its post (or home) before it has addressed the need. Without this an
	// on-shift vendor with maxed tiredness deadlocks — at the stall the only rest
	// cue points elsewhere so it walks home to sleep, then this steer drags it
	// back before it ever rests, and the need never clears. Suppressing the steer
	// lets the recovery/satiation cues win this turn; once the need clears the
	// steer resumes next tick. The complementary "rest at your post" cue
	// (recovery_options.go) keeps an at-post vendor from leaving in the first
	// place, so the post stays manned. ZBBS-HOME-362. (The TO-WORK arm carries an
	// ADDITIONAL, softer mild-or-worse gate — ZBBS-HOME-400 Option B — in the
	// switch below; this red gate is the stronger one that also defers go-home.)
	if hasRedNeed(a, snap) {
		return nil
	}
	nowMin := *snap.LocalMinuteOfDay

	start, end, windowOK := shiftWindowBounds(snap, a)
	if !windowOK {
		return nil // unscheduled and no usable day-active window
	}

	onShift := minuteInWindow(start, end, nowMin)
	atWork := anchors.WorkID != "" && a.InsideStructureID == anchors.WorkID
	atHome := anchors.HomeID != "" && a.InsideStructureID == anchors.HomeID

	switch {
	case onShift && anchors.WorkID != "" && !atWork:
		// ZBBS-HOME-400 Option B: don't yank an agent back to its post while it's
		// mid-business — an active restock errand or a pending outgoing offer
		// awaiting the seller's accept_pay (matching the shift-duty WARRANT). A RED
		// need already suppresses BOTH arms above (HOME-362). The mild-but-not-red
		// need gate HOME-400 also added here was REMOVED (ZBBS-HOME-463): a merely
		// peckish NPC should still clock in, and the mild gate stranded chronically-
		// needy NPCs (blocked from work yet not red enough to be driven to resolve —
		// e.g. a homeless blacksmith parked at the inn all shift). Scope: the to-work
		// arm ONLY — the go-home arm stays unsuppressed (going home is how an NPC rests).
		//
		// hasForageErrand (LLM-90): a grower stepping out to her OWN bushes to
		// restock a bare sell-shelf is the harvest-side twin of the restock errand —
		// the trip away from post IS the errand, so the to-work yank must defer it
		// too or it drags her back before she reaches the bushes (the buy-side
		// Josiah-Thorne oscillation, on the forage side). p.Forage is nil while a
		// customer is engaged (buildForage), so this never pulls her off a live sale.
		//
		// atResolvableSatiationSource (Moses James cycle, 2026-06-24): also don't
		// yank an agent that left its post for a felt hunger/thirst and has ARRIVED
		// at a source it can use right here — let it finish, or it ping-pongs
		// post<->source without ever consuming until the need goes red. Unlike the
		// removed HOME-463 mild gate this is LOCATION-gated (fires only once AT a
		// usable source) and coins-gated for paid vendors, so it can't re-strand the
		// homeless-blacksmith case — that NPC, broke and not yet at a free source,
		// still gets marched to work.
		if hasRestockErrand || hasForageErrand || hasPendingOutgoingOffer(snap, actorID) || hasOfferedQuote(snap, actorID) || atResolvableSatiationSource(snap, actorID, a) {
			return nil
		}
		return &DutySteerView{ToWork: true, TargetID: anchors.WorkID, TargetLabel: anchors.WorkLabel}
	case onShift && anchors.WorkID != "" && atWork:
		// At-post stabilizer (ZBBS-WORK-431): the symmetric complement to the
		// to-work yank above. On-shift and standing at its own post, an agent
		// previously got NO duty cue at all — and an idle owner with no custom
		// then read the anchors "head home whenever you wish" line as license to
		// wander, whereupon the away-from-post arm dragged it back, and it
		// oscillated (Prudence shop↔house, 2026-06-17). This view renders the
		// "stay put, don't wander" line and reframes the anchors invite. It is
		// render-only — excluded from shouldSkipNoop (AtPost), so an idle at-post
		// NPC with nothing happening still skips its idle-backstops (HOME-441).
		// Carry the effective close time (schedule end, else dusk fallback) so the
		// stabilizer can state when the shift ends — LLM-40.
		//
		// ForageErrand (LLM-90): when this same at-post grower has a bare sell-shelf
		// and ripe own bushes (hasForageErrand → p.Forage != nil, which already
		// excludes the mid-customer case), render flips the "wait here rather than
		// wandering off" line for a "step out to your bushes and return" line, so the
		// stabilizer agrees with the "## Your bushes to harvest" cue instead of
		// contradicting it. She's woken by the (now forage-aware) restock warrant,
		// so this still renders only on a tick that already runs.
		endMin := end
		return &DutySteerView{AtPost: true, ShiftEndMin: &endMin, ForageErrand: hasForageErrand}
	case !onShift:
		// Off-shift wind-down (ZBBS-WORK-387) — housing-dependent target. The
		// suppressors (windDownSuppressed: a mid-meal item dwell — WORK-386; an
		// unlapsed stay_open "open until" commitment while not peak-exhausted)
		// mirror shiftDutyTarget's go-home arm so cue and warrant agree.
		switch {
		case anchors.HomeID != "":
			// Homed → head home (the long-standing behavior).
			if atHome {
				return nil
			}
			if windDownSuppressed(a, snap) {
				return nil
			}
			return &DutySteerView{ToWork: false, TargetID: anchors.HomeID, TargetLabel: anchors.HomeLabel}
		default:
			// No home. Lodger → head to the rented room at the inn it lodges in
			// (the same soonest-active-grant the engine's windDownTarget resolves,
			// so cue and warrant agree).
			if innID, innName, ok := lodgerInn(snap, a); ok {
				if a.InsideStructureID == innID {
					return nil
				}
				if windDownSuppressed(a, snap) {
					return nil
				}
				return &DutySteerView{ToWork: false, TargetID: innID, TargetLabel: innName, Lodging: true}
			}
			// Homeless → a directionless "find your rest for the night" nudge, fired
			// only while still lingering at the work post (atWork); once off the post
			// recovery_options + the homeless rest floor take over, and there is no
			// fixed place to march to. No TargetID — render gives the placeless line.
			if !atWork {
				return nil
			}
			if windDownSuppressed(a, snap) {
				return nil
			}
			return &DutySteerView{ToWork: false}
		}
	default:
		return nil
	}
}

// atResolvableSatiationSource reports whether the actor is standing AT a source
// that can satisfy a currently-felt hunger/thirst need right here — a co-present
// peer holding a satisfier, a free public source at its tile, or a vendor
// structure it is at and has coins to pay. It gates the to-work duty yank so an
// on-shift NPC that left its post to slake a need and has arrived at the source
// is allowed to finish (the Moses James post<->stall cycle). The felt-need gate
// matches buildSatiation's (NeedSilent floor), so the suppressor fires exactly
// when the eat/drink cue is offering a usable option here.
//
// Why this doesn't reopen ZBBS-HOME-463 (the removed mild gate that stranded the
// homeless blacksmith at the inn): it is LOCATION-gated — it fires only once the
// NPC is AT a usable source, never while it merely feels peckish somewhere with
// no resolution — and the paid-vendor arm is coins-gated, so a broke NPC at a
// stall it can't transact at is still marched to work. It self-clears the instant
// the need eases.
func atResolvableSatiationSource(snap *sim.Snapshot, actorID sim.ActorID, a *sim.ActorSnapshot) bool {
	if snap == nil || a == nil {
		return false
	}
	for _, need := range satiationNeeds {
		if sim.NeedLabelTier(a.Needs[need], snap.NeedThresholds.Get(need)) == sim.NeedSilent {
			continue
		}
		// A co-present huddle peer carrying a satisfier — already beside the actor.
		if len(gatherCoPresentPeerOffers(snap, actorID, a, need)) > 0 {
			return true
		}
		// A free public source the actor is standing at (its loiter pin — the tile
		// locomotion parks a visitor on, which may be offset from the base tile).
		for _, obj := range snap.VillageObjects {
			if obj == nil || objectRefreshMagnitude(obj, need) <= 0 || obj.OwnedByOther(actorID) {
				continue
			}
			if a.Pos.Chebyshev(objectLoiterPin(obj)) <= sim.LoiterAttributionTiles {
				return true
			}
		}
		// A vendor structure the actor is standing at and can pay for (coins>0).
		// Pricing in v2 is negotiated per-transaction with no fixed retail price,
		// so coins>0 is the affordability proxy — it cleanly excludes the broke
		// homeless-blacksmith case while admitting the ordinary "I have money, let
		// me buy a drink" one.
		if a.Coins > 0 {
			for _, vc := range findVendorConsumables(snap, actorID, need, "") {
				if vc.StructureID != "" && actorAtStructure(snap, a, vc.StructureID) {
					return true
				}
			}
		}
	}
	return false
}

// actorAtStructure reports whether the actor is at a structure: inside it, or
// standing within LoiterAttributionTiles of its loiter pin (the same "outdoors
// by X" attribution findLoiterStructure uses for the location line).
func actorAtStructure(snap *sim.Snapshot, a *sim.ActorSnapshot, stID sim.StructureID) bool {
	if snap == nil || a == nil || stID == "" {
		return false
	}
	if a.InsideStructureID == stID {
		return true
	}
	vobj := snap.VillageObjects[sim.VillageObjectID(stID)]
	if vobj == nil {
		return false
	}
	return a.Pos.Chebyshev(objectLoiterPin(vobj)) <= sim.LoiterAttributionTiles
}

// objectLoiterPin returns the tile an actor stands on when "at" obj — its base
// tile plus any loiter offset. This is the pin locomotion parks visitors on and
// the pin findLoiterStructure attributes "outdoors by X" to, so co-location
// checks must measure to it, not the bare base tile.
func objectLoiterPin(vobj *sim.VillageObject) sim.TilePos {
	pin := vobj.Pos.Tile()
	off := sim.TileOffset{}
	if vobj.LoiterOffsetX != nil {
		off.DX = *vobj.LoiterOffsetX
	}
	if vobj.LoiterOffsetY != nil {
		off.DY = *vobj.LoiterOffsetY
	}
	return pin.Add(off)
}

// shiftWindowBounds resolves the actor's effective shift window: its own
// schedule when both bounds are set, else the world day-active (dawn/dusk)
// window from the snapshot. ok=false when neither is usable. Shared by
// buildDutySteer and buildDutyPending so the cue and the gate signal agree
// on what "the shift window" is.
//
// The day-active fallback: DawnDuskMinuteOK rejects a partial/failed parse
// (which would otherwise derive a bogus window from one good + one zero
// bound); the inequality rejects a degenerate dawn==dusk empty window that
// reads as off-shift all day and would emit a perpetual "head home" cue
// (code_review).
func shiftWindowBounds(snap *sim.Snapshot, a *sim.ActorSnapshot) (start, end int, ok bool) {
	// Nil-safe on its own: both current callers pre-check, but the helper's
	// contract shouldn't depend on that — a future caller skipping the guard
	// would panic on the field derefs below (code_review, HOME-442).
	if snap == nil || a == nil {
		return 0, 0, false
	}
	switch {
	case a.ScheduleStartMin != nil && a.ScheduleEndMin != nil:
		return *a.ScheduleStartMin, *a.ScheduleEndMin, true
	case snap.DawnDuskMinuteOK && snap.DawnMinute != snap.DuskMinute:
		return snap.DawnMinute, snap.DuskMinute, true
	default:
		return 0, 0, false
	}
}

// buildDutyPending reports whether the actor is off-post inside its shift
// window — to-work duty APPLIES this minute — computed WITHOUT the cue-side
// suppressors that can nil buildDutySteer's to-work arm (the HOME-362
// red-need gate; HOME-400 Option B's mild-need / restock-errand /
// pending-offer gate). The noop-skip gate consumes it (ZBBS-HOME-442): an
// off-post on-shift keeper with a need in the mild band had NO rendered
// steer (Option B) and NO red need, so the gate ate its idle-backstops and
// it stood skip-locked for hours (the live Josiah case the HOME-441 steer
// condition turned out not to cover). The signal opens the gate; the cue
// stays suppressed — the tick that runs voices the mild need with no
// to-work line, the model addresses the need, and once every need drops
// below mild the steer renders and the next tick walks the actor to post.
//
// Strictly the TO-WORK arm: the go-home/wind-down side keeps its existing
// behavior (a rendered go-home steer already opens the gate via DutySteer;
// its suppressors — mid-meal dwell, stay-open — describe an actor that is
// mid-action, not stuck).
func buildDutyPending(snap *sim.Snapshot, a *sim.ActorSnapshot, anchors *AnchorsView) bool {
	if snap == nil || a == nil || anchors == nil || snap.LocalMinuteOfDay == nil {
		return false
	}
	if a.Kind != sim.KindNPCStateful && a.Kind != sim.KindNPCShared {
		return false
	}
	start, end, ok := shiftWindowBounds(snap, a)
	if !ok {
		return false
	}
	if !minuteInWindow(start, end, *snap.LocalMinuteOfDay) {
		return false
	}
	return anchors.WorkID != "" && a.InsideStructureID != anchors.WorkID
}

// windDownSuppressed reports whether the off-shift wind-down cue should be held
// back this tick despite the actor being off-shift and not yet wound down.
// Mirrors shiftDutyTarget's go-home suppressors so the perception cue and the
// engine warrant agree (ZBBS-WORK-386 + ZBBS-WORK-387):
//   - a live item-source dwell credit (mid-meal) — don't prompt it to abandon
//     the meal mid-dwell; the cue re-fires once the dwell ends.
//   - an unlapsed stay_open "open until" commitment — the keeper has chosen to
//     stay open, so suppress the routine wind-down.
//
// The peak-exhaustion override shiftDutyTarget applies (OpenUntil yields to peak,
// so the engine still force-beds an exhausted keeper) is deliberately NOT
// mirrored here: buildDutySteer already returns nil for ANY red-or-worse need
// (hasRedNeed, HOME-362) before this is reached, and peak is a strict subset of
// red — so a peak-exhausted keeper's wind-down cue is already silenced upstream
// and the engine's force-bed floor (MarchHome) / recovery_options drive the rest.
// When this runs, the actor is always sub-red.
func windDownSuppressed(a *sim.ActorSnapshot, snap *sim.Snapshot) bool {
	if sim.HasActiveItemDwell(a.DwellCredits) {
		return true
	}
	if a.OpenUntil != nil && a.OpenUntil.After(snap.PublishedAt) {
		return true
	}
	return false
}

// lodgerInn resolves the inn structure (id + display label) where the actor
// holds its soonest-expiring active ledger room grant, or ok=false when it holds
// none (i.e. isn't a lodger). The grant selection matches buildLodgingView and
// the engine's soonestActiveLedgerGrant, so the wind-down cue, the lodging
// section, and the engine warrant all point at the same inn. ZBBS-WORK-387.
func lodgerInn(snap *sim.Snapshot, a *sim.ActorSnapshot) (sim.StructureID, string, bool) {
	now := snap.PublishedAt
	var best *sim.RoomAccess
	for _, ra := range a.RoomAccess {
		if !sim.IsActiveLedgerGrant(ra, now) {
			continue
		}
		// Tie-break equal expiries by RoomID — deterministic across the map's
		// randomized iteration order, and matching the engine's
		// soonestActiveLedgerGrant so the cue and the warrant pick the same inn
		// when an actor holds two equally-expiring grants (ZBBS-WORK-387).
		if best == nil || ra.ExpiresAt.Before(*best.ExpiresAt) ||
			(ra.ExpiresAt.Equal(*best.ExpiresAt) && ra.RoomID < best.RoomID) {
			best = ra
		}
	}
	if best == nil {
		return "", "", false
	}
	s := structureForRoom(snap, best.RoomID)
	if s == nil {
		return "", "", false
	}
	return s.ID, innLabel(s), true
}

// stayOpenReason returns the concrete reason to ENCOURAGE a wind-down keeper to
// stay open (the hybrid gate, ZBBS-WORK-387 design C), or "" when none is present
// (the keeper is still OFFERED stay_open, just not actively encouraged). Ordered
// most-concrete-commitment first.
func stayOpenReason(hasOwedOrders, hasCoPresentBuyer, hasPendingOffer bool) string {
	switch {
	case hasOwedOrders:
		return "you still have orders to deliver"
	case hasCoPresentBuyer:
		return "a customer is still here with you"
	case hasPendingOffer:
		return "you have an offer still awaiting payment"
	}
	return ""
}

// hasRedNeed reports whether any of the actor's tracked needs is at or over its
// configured red-tier threshold. Iterates the canonical need registry (sim.Needs)
// and reads the same per-need boundary the recovery/satiation cues and the
// need-threshold warrant use (snap.NeedThresholds.Get, which falls back to the
// registry default when unset) so "red" means one thing across the prompt.
// Nil-safe (perception builders elsewhere have hit nil-snapshot edges).
// ZBBS-HOME-362.
func hasRedNeed(a *sim.ActorSnapshot, snap *sim.Snapshot) bool {
	if a == nil || snap == nil {
		return false
	}
	for _, n := range sim.Needs {
		if a.Needs[n.Key] >= snap.NeedThresholds.Get(n.Key) {
			return true
		}
	}
	return false
}

// hasPendingOutgoingOffer reports whether actorID has a pay-with-item offer it
// made (as buyer) still awaiting the seller's response. While one is pending, the
// return-to-post cue is suppressed so the buyer isn't pulled out of the
// conversation before the seller can accept_pay — acceptance re-checks that both
// parties are still co-present, so walking away fails the trade (ZBBS-HOME-400
// Option B). Scans the published pay ledger, which is bounded by the TTL sweep
// (RunPayLedgerSweep); if terminal entries are ever found to accumulate, index
// pending outgoing offers at snapshot build time instead (code_review). Nil-safe.
func hasPendingOutgoingOffer(snap *sim.Snapshot, actorID sim.ActorID) bool {
	if snap == nil {
		return false
	}
	for _, e := range snap.PayLedger {
		if e != nil && e.BuyerID == actorID && e.State == sim.PayLedgerStatePending {
			return true
		}
	}
	return false
}

// hasOfferedQuote reports whether a seller has an active scene_quote addressed to
// actorID (as buyer) still standing. The buyer-side complement to
// hasPendingOutgoingOffer: a quote a seller has put in front of the buyer is an
// in-progress purchase, so the return-to-post cue is suppressed rather than
// yanking the buyer out of the deal before they can take it — pay_with_item
// re-checks co-presence, so walking off to the post loses the trade (the
// Prudence shop↔General-Store bounce, 2026-06-17, where the to-work yank fired
// every tick she stood at the stall mid-purchase because her mild hunger wasn't
// red and a settling consume_now buy never sits pending). Targeted quotes only
// (TargetBuyer == actorID): a public quote (TargetBuyer == "") isn't addressed to
// this buyer in particular, so it shouldn't pin a passer-by to the stall. Scans
// the published quote map, bounded by the TTL sweep (RunSceneQuoteSweep). Nil-safe.
func hasOfferedQuote(snap *sim.Snapshot, actorID sim.ActorID) bool {
	if snap == nil {
		return false
	}
	for _, q := range snap.Quotes {
		if q != nil && q.TargetBuyer == actorID && q.State == sim.SceneQuoteStateActive {
			return true
		}
	}
	return false
}

// buildOfferableCustomers builds the seller-side "offer your wares" cue
// (ZBBS-HOME-404). When a businessowner is co-present with one or more
// customers, it surfaces those customers by name alongside the seller's
// sellable goods, so the keeper LLM can proactively offer a sale via
// scene_quote rather than only reacting to a buyer's pay_with_item. This makes
// the existing seller-initiated path LEGIBLE (the Finding-1 lesson applied to
// the sell side); it does not auto-complete anything — the seller decides
// whether/what/at-what-price and the buyer keeps full accept/decline agency, so
// any future "this seller won't deal" reason needs no new mechanism.
//
// Co-presence is the huddle (an active interaction), not mere loiter-presence —
// so the cue doesn't fire at someone merely passing the stall, and the vendor
// block's "don't pitch unless they show interest" rule still governs whether
// the model actually offers.
//
// Returns nil — Render content-gates — when the subject isn't a businessowner,
// has no co-present customer, or carries nothing to sell.
//
// Two storm guards drop a customer from the cue (the pay-offer / order-chase
// dedup discipline, so a stuck cue can't drive a re-offer loop):
//   - the customer already has a pending pay_with_item offer with this seller —
//     renderPayOffers already cues accept/decline/counter, so don't also drive
//     the seller to offer them; and
//   - the seller already has a live (Active) scene_quote out to that customer —
//     they've offered and await the buyer; re-cueing would re-post every tick.
func buildOfferableCustomers(snap *sim.Snapshot, subject sim.ActorID, atOwnBusiness bool, members []HuddleMember, inventory []InventoryItem) *OfferableCustomersView {
	if !atOwnBusiness || len(members) == 0 || len(inventory) == 0 {
		return nil
	}
	goods := make([]OfferableGood, 0, len(inventory))
	for _, it := range inventory {
		if it.Label == "" {
			continue
		}
		goods = append(goods, OfferableGood{Label: it.Label, OnHand: it.Qty})
	}
	if len(goods) == 0 {
		return nil
	}
	// members is already sorted by ID (buildSurroundings), so names is deterministic.
	var names []string
	for _, m := range members {
		if customerHasPendingOfferWithSeller(snap, m.ID, subject) {
			continue
		}
		if sellerHasActiveQuoteToBuyer(snap, subject, m.ID) {
			continue
		}
		names = append(names, descriptorLabel(m.DisplayName, m.Role, m.Acquainted))
	}
	if len(names) == 0 {
		return nil
	}
	return &OfferableCustomersView{CustomerNames: names, Goods: goods}
}

// customerHasPendingOfferWithSeller reports whether `buyer` has a pending
// pay_with_item offer awaiting `seller`'s response. The reactive case is
// already cued by renderPayOffers (accept/decline/counter), so the proactive
// offer cue suppresses that customer to avoid double-driving the seller toward
// the same person. Nil-safe.
func customerHasPendingOfferWithSeller(snap *sim.Snapshot, buyer, seller sim.ActorID) bool {
	if snap == nil {
		return false
	}
	for _, e := range snap.PayLedger {
		if e != nil && e.BuyerID == buyer && e.SellerID == seller && e.State == sim.PayLedgerStatePending {
			return true
		}
	}
	return false
}

// sellerHasActiveQuoteToBuyer reports whether `seller` already has a live
// (Active) scene_quote targeted at `buyer`. While one stands, the buyer can take
// it via pay_with_item — re-cueing the offer would have the seller re-post the
// same quote every tick (the re-offer storm hard-capped in ZBBS-HOME-395/381). A
// public (untargeted) quote is deliberately NOT counted: it isn't directed at
// this customer, so it doesn't pre-empt a directed offer. Nil-safe.
func sellerHasActiveQuoteToBuyer(snap *sim.Snapshot, seller, buyer sim.ActorID) bool {
	if snap == nil {
		return false
	}
	for _, q := range snap.Quotes {
		if q != nil && q.SellerID == seller && q.TargetBuyer == buyer && q.State == sim.SceneQuoteStateActive {
			return true
		}
	}
	return false
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
//
// Same-tick de-dup (ZBBS-WORK-374): a just-heard utterance is recorded as a
// `heard` SalientFact on the listener AND surfaced as a speech warrant in this
// tick's "## What just happened". Rendering it in both places shows the model
// the same line twice (the live "Hello" duplication) and reinforces it. heardNow
// maps speaker → the utterances they spoke in THIS batch; we drop a peer's fact
// whose text matches before taking the recent-N, so the most-recent slot
// backfills with genuinely-older context instead of a duplicate. Done here (not
// in Render) per the package contract: Build decides content, Render is content-
// agnostic.
func buildRelationships(a *sim.ActorSnapshot, members []HuddleMember, heardNow map[sim.ActorID]map[string]bool) []RelationshipPeerView {
	if a.Kind != sim.KindNPCShared || len(a.Relationships) == 0 || len(members) == 0 {
		return nil
	}
	out := make([]RelationshipPeerView, 0, len(members))
	for _, m := range members {
		rel := a.Relationships[m.ID]
		if rel == nil {
			continue
		}
		facts := rel.SalientFacts
		if dups := heardNow[m.ID]; len(dups) > 0 {
			kept := make([]sim.SalientFact, 0, len(facts))
			for _, f := range facts {
				if f.Kind == sim.InteractionHeard && dups[f.Text] {
					continue // already in "## What just happened" this tick
				}
				kept = append(kept, f)
			}
			facts = kept
		}
		out = append(out, RelationshipPeerView{
			PeerID:      m.ID,
			PeerName:    m.DisplayName,
			SummaryText: rel.SummaryText,
			RecentFacts: recentFactsMostRecentFirst(facts, recentSalientFactsPerPeer),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// recentConversationDedupKey truncates an utterance to MaxSalientFactTextLen the
// same way the warrant Excerpt is, so a ring line can be matched against
// currentHeardExcerpts (which keys on the truncated form). Short lines (the
// common case) are returned unchanged.
func recentConversationDedupKey(text string) string {
	r := []rune(text)
	if len(r) > sim.MaxSalientFactTextLen {
		return string(r[:sim.MaxSalientFactTextLen])
	}
	return text
}

// buildRecentConversation projects the subject's current-huddle RecentUtterances
// ring into the "## Recent conversation here" view (ZBBS-HOME-412), oldest-first.
// Populated for EVERY actor with a live huddle — NOT gated to shared VAs like
// buildRelationships — so stateful NPCs and PC-facing vendors get cross-tick
// conversational continuity (they see their own prior lines and the player's).
// The subject's own lines are marked IsSelf. A line whose text matches an
// utterance already surfaced in this tick's "## What just happened" (heardNow) is
// dropped so the live turn isn't shown twice — the same de-dup buildRelationships
// applies to heard facts (ZBBS-WORK-374). Returns nil when the subject has no
// huddle or nothing survives the de-dup.
func buildRecentConversation(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, heardNow map[sim.ActorID]map[string]bool) []UtteranceView {
	huddleID := actorSnap.CurrentHuddleID
	if huddleID == "" {
		return nil
	}
	h := snap.Huddles[huddleID]
	if h == nil || len(h.RecentUtterances) == 0 {
		return nil
	}
	out := make([]UtteranceView, 0, len(h.RecentUtterances))
	for _, u := range h.RecentUtterances {
		if dups := heardNow[u.SpeakerID]; dups != nil && dups[recentConversationDedupKey(u.Text)] {
			continue // already rendered in "## What just happened" this tick
		}
		out = append(out, UtteranceView{
			SpeakerName: u.SpeakerName,
			Text:        u.Text,
			IsSelf:      u.SpeakerID == actorID,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// currentHeardExcerpts indexes the speech utterances in this tick's consumed
// warrant batch by speaker, so buildRelationships can drop a `heard` SalientFact
// that the "## What just happened" section already renders (ZBBS-WORK-374). The
// warrant Excerpt and the heard-fact Text are both truncateRunes(spoke.Text,
// MaxSalientFactTextLen), so an exact string match is reliable. Returns nil when
// the batch carries no speech (the common non-conversational tick).
func currentHeardExcerpts(warrants []sim.WarrantMeta) map[sim.ActorID]map[string]bool {
	var bySpeaker map[sim.ActorID]map[string]bool
	add := func(speaker sim.ActorID, excerpt string) {
		if speaker == "" || excerpt == "" {
			return
		}
		if bySpeaker == nil {
			bySpeaker = make(map[sim.ActorID]map[string]bool)
		}
		if bySpeaker[speaker] == nil {
			bySpeaker[speaker] = make(map[string]bool)
		}
		bySpeaker[speaker][excerpt] = true
	}
	for _, w := range warrants {
		switch r := w.Reason.(type) {
		case sim.PCSpeechWarrantReason:
			add(r.Speaker, r.Excerpt)
		case sim.NPCSpeechWarrantReason:
			add(r.Speaker, r.Excerpt)
		}
	}
	return bySpeaker
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
//
// AbsentRecipientNames is populated for the fromMe bucket only: the consumers
// not currently sharing the seller's huddle, whom DeliverOrder's gate-6
// co-presence check would reject. Empty => deliverable now. The toMe bucket
// leaves it nil (not meaningful buyer-side). ZBBS-WORK-373.
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
			ReadyBy:    o.ReadyBy,
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
		seller := snap.Actors[subject]
		fromMe = make([]OrderView, 0, len(fromIDs))
		for _, id := range fromIDs {
			o := snap.Orders[id]
			v := toView(o)
			v.AbsentRecipientNames = absentRecipientNames(snap, seller, o, resolveName)
			fromMe = append(fromMe, v)
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

// absentRecipientNames returns the display names (sorted) of an order's
// consumers who do NOT currently share the seller's huddle — the recipients
// DeliverOrder's gate-6 co-presence check (order_commands.go) would reject a
// handover to. An empty result means every recipient is here and the order is
// deliverable now. A nil seller or a seller in no huddle makes every consumer
// absent: a keeper in no conversation can hand nothing over. Seller-relative,
// so it is meaningful only for the seller-side PendingDeliveriesFromMe bucket.
// ZBBS-WORK-373 (boot-collapse Finding 6 bundle).
func absentRecipientNames(snap *sim.Snapshot, seller *sim.ActorSnapshot, o *sim.Order, resolveName func(sim.ActorID) string) []string {
	var sellerHuddle sim.HuddleID
	if seller != nil {
		sellerHuddle = seller.CurrentHuddleID
	}
	var absent []string
	for _, cid := range o.ConsumerIDs {
		coPresent := sellerHuddle != ""
		if coPresent {
			c := snap.Actors[cid]
			coPresent = c != nil && c.CurrentHuddleID == sellerHuddle
		}
		if !coPresent {
			absent = append(absent, resolveName(cid))
		}
	}
	sort.Strings(absent)
	return absent
}

// recentlyResolvedOfferWindow bounds how long a just-settled offer stays in the
// buyer's "## Recently settled offers" view. Short — the view bridges the gap
// until the buyer's next deliberation registers the resolution, it is not a
// purchase log. ~3 min matches the pending-offer TTL scale (the conversational
// moment); the terminal ledger entry itself lingers up to
// PayLedgerTerminalRetention (1h), far longer than we want to keep narrating it.
const recentlyResolvedOfferWindow = 3 * time.Minute

// buildRecentlyResolvedOffersFromMe scans snap.PayLedger for the subject's OWN
// offers that left Pending within recentlyResolvedOfferWindow of
// snap.PublishedAt — entries where the subject is the BUYER and the state is a
// terminal resolution (Countered excluded: it is an active flow the buyer must
// still answer, not a closed deal — surfaced separately by
// buildCountersAwaitingMyResponse). It is the buyer-side
// resolution companion to buildPendingOffersFromMe: it closes the blind window
// between an offer leaving the pending scan and the PayResolvedWarrantReason
// event surfacing, which can lag a tick behind the buyer's in-flight
// deliberation and let the buyer re-buy a need already met. Sourced from the
// ledger, not a warrant, so it is robust to warrant emit timing. Seller name is
// acquaintance-gated like the pending view. Returns nil for none. Ordering: by
// LedgerID ascending, deterministic.
func buildRecentlyResolvedOffersFromMe(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []ResolvedOfferView {
	if snap == nil || len(snap.PayLedger) == 0 {
		return nil
	}
	var ids []sim.LedgerID
	for id, e := range snap.PayLedger {
		if e == nil || e.BuyerID != subject {
			continue
		}
		if e.State == sim.PayLedgerStatePending || e.State == sim.PayLedgerStateCountered {
			continue
		}
		if e.ResolvedAt.IsZero() {
			continue
		}
		if snap.PublishedAt.Sub(e.ResolvedAt) > recentlyResolvedOfferWindow {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	resolveSeller := func(id sim.ActorID) string {
		seller := snap.Actors[id]
		if seller == nil {
			return string(id)
		}
		acquainted := false
		if subjectSnap != nil && seller.DisplayName != "" {
			_, acquainted = subjectSnap.Acquaintances[seller.DisplayName]
		}
		return descriptorLabel(seller.DisplayName, seller.Role, acquainted)
	}

	views := make([]ResolvedOfferView, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		views = append(views, ResolvedOfferView{
			LedgerID:   e.ID,
			SellerName: resolveSeller(e.SellerID),
			Item:       e.ItemKind,
			Qty:        e.Qty,
			Amount:     e.Amount,
			PayItems:   e.PayItems,
			Accepted:   e.State == sim.PayLedgerStateAccepted,
			ConsumeNow: e.ConsumeNow,
		})
	}
	return views
}

// counterResponseWindow bounds how long a seller's counter is surfaced to the
// buyer as awaiting a response. A countered parent entry is terminal and lingers
// in the ledger up to PayLedgerTerminalRetention (1h); without a window the buyer
// would be nagged about a stale counter long after the moment passed. Matched to
// the pending-offer TTL scale (the window an un-acted offer would have expired
// in) so a counter stops reading as "live" on the same cadence.
const counterResponseWindow = 3 * time.Minute

// buildCountersAwaitingMyResponse scans snap.PayLedger for a seller's counter the
// subject (as buyer) has not yet answered: terminal Countered entries where the
// subject is the BUYER, resolved within counterResponseWindow of
// snap.PublishedAt, still below the counter-chain depth cap (validateInResponseTo
// rejects a response once parent.Depth reaches MaxPayCounterChainDepth, so a
// capped counter can't be taken — don't steer one), and with no child entry
// chained via ParentID (a buyer's response creates such a child, so a counter
// with one has been answered).
//
// It is the buyer-side standing decision view of a counter — the counterpart to
// the seller's buildPayOffersForMe standing scan (ZBBS-HOME-453). It reads the
// ledger rather than the PayResolvedWarrantReason{Countered} event because of
// LLM-21: that warrant can ride a tick behind the buyer's in-flight deliberation
// (a counter stamped mid-tick opens a fresh cycle the in-flight tick never
// carries), and unlike an accept/decline the recently-settled scan deliberately
// excludes Countered, so the warrant is the ONLY thing that surfaces a counter —
// a buyer could re-offer a need already in negotiation, or miss the counter
// entirely if the warrant is evicted while the buyer is shelved. The per-tick
// ledger scan is robust to that timing.
//
// Seller name acquaintance-gated like the pending view. Returns nil for none.
// Ordering: by LedgerID ascending, deterministic across runs.
func buildCountersAwaitingMyResponse(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []CounterOfferView {
	if snap == nil || len(snap.PayLedger) == 0 {
		return nil
	}
	// A buyer's response to a counter is a fresh entry chained via ParentID, so a
	// countered entry that already has such a child has been answered and must
	// not be re-surfaced. Collect answered parents in one pass.
	answered := make(map[sim.LedgerID]struct{})
	for _, e := range snap.PayLedger {
		if e != nil && e.ParentID != 0 {
			answered[e.ParentID] = struct{}{}
		}
	}
	var ids []sim.LedgerID
	for id, e := range snap.PayLedger {
		if e == nil || e.BuyerID != subject {
			continue
		}
		if e.State != sim.PayLedgerStateCountered {
			continue
		}
		if e.Depth >= sim.MaxPayCounterChainDepth {
			continue
		}
		if e.ResolvedAt.IsZero() {
			continue
		}
		if snap.PublishedAt.Sub(e.ResolvedAt) > counterResponseWindow {
			continue
		}
		if _, done := answered[id]; done {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	resolveSeller := func(id sim.ActorID) string {
		seller := snap.Actors[id]
		if seller == nil {
			return string(id)
		}
		acquainted := false
		if subjectSnap != nil && seller.DisplayName != "" {
			_, acquainted = subjectSnap.Acquaintances[seller.DisplayName]
		}
		return descriptorLabel(seller.DisplayName, seller.Role, acquainted)
	}

	views := make([]CounterOfferView, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		views = append(views, CounterOfferView{
			LedgerID:      e.ID,
			SellerName:    resolveSeller(e.SellerID),
			Item:          e.ItemKind,
			Qty:           e.Qty,
			CounterAmount: e.CounterAmount,
			// snap.PayLedger entries are deep-cloned at publish, so aliasing the
			// snapshot's CounterPayItems slice into the read-only, per-tick view
			// is safe — same posture as buildPendingOffersFromMe's PayItems.
			CounterPayItems: e.CounterPayItems,
		})
	}
	return views
}

// buildPendingOffersFromMe scans snap.PayLedger for the subject's OWN still-
// pending pay-with-item offers — entries where the subject is the BUYER and the
// state is Pending (the only non-terminal pay-ledger state) — and projects each
// to a PendingOfferView for the "## Offers you have standing" cue (ZBBS-HOME-413).
//
// This is the buyer-side counterpart to the seller's PayOfferWarrants: the
// seller learns of an offer via a warrant stamped on them, but the buyer gets
// NO warrant for an offer they placed, so without this scan a buyer has no
// cross-tick memory of an outstanding offer and re-stakes the same one every
// tick (the repeat-offer storm). The data comes from the ledger, not a warrant,
// for exactly that reason.
//
// The seller's name is acquaintance-gated (descriptorLabel against the
// subject's Acquaintances) — the same name-vs-descriptor gating the seller side
// applies to the buyer. Returns nil for no pending offers so render content-
// gates cheaply. Ordering: by LedgerID ascending, deterministic across runs.
func buildPendingOffersFromMe(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []PendingOfferView {
	if snap == nil || len(snap.PayLedger) == 0 {
		return nil
	}
	// Pre-collect IDs so the views sort deterministically by LedgerID.
	var ids []sim.LedgerID
	for id, e := range snap.PayLedger {
		if e == nil || e.State != sim.PayLedgerStatePending {
			continue
		}
		if e.BuyerID != subject {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	resolveSeller := func(id sim.ActorID) string {
		seller := snap.Actors[id]
		if seller == nil {
			return string(id)
		}
		acquainted := false
		if subjectSnap != nil && seller.DisplayName != "" {
			_, acquainted = subjectSnap.Acquaintances[seller.DisplayName]
		}
		return descriptorLabel(seller.DisplayName, seller.Role, acquainted)
	}

	views := make([]PendingOfferView, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		views = append(views, PendingOfferView{
			LedgerID:   e.ID,
			SellerName: resolveSeller(e.SellerID),
			Item:       e.ItemKind,
			Qty:        e.Qty,
			Amount:     e.Amount,
			// snap.PayLedger entries are deep-cloned at publish, so the
			// snapshot's PayItems slice is already isolated from world state;
			// aliasing it into the (read-only, per-tick) view is safe.
			PayItems: e.PayItems,
		})
	}
	return views
}

// buildStandingQuotesFromMe scans snap.Quotes for the subject's OWN still-active
// scene-quotes — the offers-to-sell it posted as SELLER (sell / scene_quote) —
// and projects each to a StandingQuoteView for the seller-side "## Offers you've
// put out" cue (LLM-45).
//
// This is the seller/scene_quote counterpart to buildPendingOffersFromMe (the
// buyer/pay_with_item HOME-413 scan), and it exists for the identical reason: a
// seller has NO cross-tick memory of an offer it posted. buildOfferableCustomers
// already suppresses a re-pitch once a quote stands (sellerHasActiveQuoteToBuyer),
// but nothing then tells the seller WHAT it offered to WHOM — so a weak model
// loses the thread, re-posts the same quote (the already_quoted thrash), and
// confabulates a queue between two co-present seekers ("I offered Ezekiel, you
// must wait") even as its own offer to the asker stands. The data comes from the
// live quote map (bounded by RunSceneQuoteSweep's TTL), not a warrant, for
// exactly that reason.
//
// Both targeted (TargetBuyer set) and public (TargetBuyer == "") quotes surface:
// sellerHasActiveQuoteToBuyer only tracks targeted quotes, so a public offer is
// otherwise invisible to its own author. The buyer's name is acquaintance-gated
// (descriptorLabel) like the buyer-side scan. Returns nil for none so render
// content-gates cheaply. Ordering: by QuoteID ascending, deterministic.
func buildStandingQuotesFromMe(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []StandingQuoteView {
	if snap == nil || len(snap.Quotes) == 0 {
		return nil
	}
	var ids []sim.QuoteID
	for id, q := range snap.Quotes {
		if q == nil || q.State != sim.SceneQuoteStateActive {
			continue
		}
		if q.SellerID != subject {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	resolveBuyer := func(id sim.ActorID) string {
		buyer := snap.Actors[id]
		if buyer == nil {
			// A targeted buyer who has left the snapshot (rare) falls back to a
			// generic descriptor rather than leaking the raw internal actor id
			// into the prompt — the same "someone" token the render layer uses.
			return "someone"
		}
		acquainted := false
		if subjectSnap != nil && buyer.DisplayName != "" {
			_, acquainted = subjectSnap.Acquaintances[buyer.DisplayName]
		}
		return descriptorLabel(buyer.DisplayName, buyer.Role, acquainted)
	}

	views := make([]StandingQuoteView, 0, len(ids))
	for _, id := range ids {
		q := snap.Quotes[id]
		buyerName := ""
		if q.TargetBuyer != "" {
			buyerName = resolveBuyer(q.TargetBuyer)
		}
		views = append(views, StandingQuoteView{
			QuoteID:   q.ID,
			BuyerName: buyerName,
			Item:      q.ItemKind,
			Qty:       q.Qty,
			Amount:    q.Amount,
		})
	}
	return views
}

// buildPayOffersForMe scans snap.PayLedger for the still-pending offers staked
// AGAINST the subject — entries where the subject is the SELLER and the state
// is Pending — and projects each to a sim.PayOfferWarrantReason for the
// standing "## Offers awaiting your decision" section and the
// accept/decline/counter tool gate (ZBBS-HOME-453).
//
// This is the seller-side counterpart to buildPendingOffersFromMe (the
// buyer's HOME-413 scan), and it exists for the same reason: a warrant is a
// one-shot wake-up, not cross-tick memory. The PayOfferWarrant is consumed by
// the first tick it triggers, so a seller who speaks through that tick
// instead of resolving used to lose the cue AND the response tools while the
// offer sat pending — structurally unable to accept until the TTL sweep
// expired the entry (the 2026-06-12 Ellis meat deadlock). The data comes
// from the ledger, not the warrant, for exactly that reason.
//
// The projection mirrors restartReStampPayOfferWarrants' entry → reason
// mapping. snap.PayLedger entries are deep-cloned at publish, so aliasing
// PayItems / ConsumerIDs into the (read-only, per-tick) view is safe — same
// posture as buildPendingOffersFromMe. Returns nil for no pending offers so
// render and gate content-gate cheaply. Ordering: by LedgerID ascending,
// deterministic across runs.
func buildPayOffersForMe(snap *sim.Snapshot, subject sim.ActorID) []sim.PayOfferWarrantReason {
	if snap == nil || len(snap.PayLedger) == 0 {
		return nil
	}
	var ids []sim.LedgerID
	for id, e := range snap.PayLedger {
		if e == nil || e.State != sim.PayLedgerStatePending {
			continue
		}
		if e.SellerID != subject {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	offers := make([]sim.PayOfferWarrantReason, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		offers = append(offers, sim.PayOfferWarrantReason{
			LedgerID:    e.ID,
			Buyer:       e.BuyerID,
			Item:        e.ItemKind,
			Qty:         e.Qty,
			Amount:      e.Amount,
			PayItems:    e.PayItems,
			ConsumeNow:  e.ConsumeNow,
			ConsumerIDs: e.ConsumerIDs,
			ExpiresAt:   e.ExpiresAt,
			Depth:       e.Depth,
		})
	}
	return offers
}

// buildRoomAlreadySold maps each pending lodging offer (by its LedgerID) to an
// existing Ready lodging order this keeper already owes the SAME buyer — the
// duplicate-room situation LLM-89's AcceptPay gate rejects (a nights_stay grant
// lands only at deliver_order, so accepting a second room before handing over
// the first double-charges the guest). renderPayOffers reads it to steer the
// keeper to deliver the room already sold rather than accept another. nil when
// no pending offer overlaps an undelivered room.
func buildRoomAlreadySold(snap *sim.Snapshot, keeper sim.ActorID, offers []sim.PayOfferWarrantReason) map[sim.LedgerID]sim.OrderID {
	if snap == nil || len(offers) == 0 || len(snap.Orders) == 0 {
		return nil
	}
	var out map[sim.LedgerID]sim.OrderID
	for _, o := range offers {
		if !itemGrantsLodging(snap, o.Item) {
			continue
		}
		oid, ok := readyLodgingOrderFor(snap, keeper, o.Buyer)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[sim.LedgerID]sim.OrderID)
		}
		out[o.LedgerID] = oid
	}
	return out
}

// readyLodgingOrderFor returns the ID of a Ready (undelivered) lodging order
// from keeper to buyer, and true, or (0, false) when none. The seller-side
// mirror of the engine's undeliveredLodgingOrderFor, read off the snapshot;
// buyer matches as the order's BuyerID or any of its ConsumerIDs.
func readyLodgingOrderFor(snap *sim.Snapshot, keeper, buyer sim.ActorID) (sim.OrderID, bool) {
	for _, o := range snap.Orders {
		if o == nil || o.State != sim.OrderStateReady || o.SellerID != keeper {
			continue
		}
		if !itemGrantsLodging(snap, o.Item) {
			continue
		}
		if o.BuyerID == buyer {
			return o.ID, true
		}
		for _, cid := range o.ConsumerIDs {
			if cid == buyer {
				return o.ID, true
			}
		}
	}
	return 0, false
}

// filterStalePayOfferWarrants removes PayOfferWarrantReason warrants whose
// pay-ledger entry is missing or no longer pending (ZBBS-HOME-413). See the
// callsite in Build for the why. All other warrant kinds pass through
// untouched, and the input slice is returned unchanged (same backing array)
// when nothing is stale — the common case, so the steady state allocates
// nothing.
func filterStalePayOfferWarrants(warrants []sim.WarrantMeta, snap *sim.Snapshot) []sim.WarrantMeta {
	if len(warrants) == 0 || snap == nil {
		return warrants
	}
	stale := func(w sim.WarrantMeta) bool {
		r, ok := w.Reason.(sim.PayOfferWarrantReason)
		if !ok {
			return false
		}
		e := snap.PayLedger[r.LedgerID]
		return e == nil || e.State != sim.PayLedgerStatePending
	}
	anyStale := false
	for _, w := range warrants {
		if stale(w) {
			anyStale = true
			break
		}
	}
	if !anyStale {
		return warrants
	}
	out := make([]sim.WarrantMeta, 0, len(warrants))
	for _, w := range warrants {
		if !stale(w) {
			out = append(out, w)
		}
	}
	return out
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
