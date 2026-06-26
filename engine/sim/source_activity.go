package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// source_activity.go — LLM-54. A timed, occupied-until action AT a village
// object: eating or drinking in place at a refresh source (the well/bush
// "refresh" arm), or harvesting a gatherable source into inventory. Replaces
// the two INSTANT interactions — arrival auto-eat (ApplyObjectRefreshAtArrival)
// and the gather tool (Gather) — with a "you start, you wait, it lands" model
// so eating and harvesting take time (Jeff, 2026-06-21).
//
// Lifecycle:
//   - START sets Actor.SourceActivity (a few seconds out). The effect is NOT
//     applied yet. The actor is "busy at a source": the reactor shelves its LLM
//     tick (reactor.go) and a deliberate move abandons the activity
//     (commands_move.go), exactly the way sleep/break shelve and a move wakes.
//   - COMPLETE: RunSourceActivityTicker wakes ~1s and applies every activity
//     whose Until has passed — the refresh need-drop (applyObjectRefreshEffect)
//     or the harvest mint (applyGatherMint) — then clears the window.
//
// The window is TRANSIENT (not checkpointed; see Actor.SourceActivity). The
// PERSISTENT mutation (needs/inventory/supply) lands atomically at completion
// only, so a crash mid-window leaves no torn state — it simply never ate.

// SourceActivityKind discriminates the two source actions. "refresh" is the
// eat/drink-in-place arm (it applies the object's ObjectRefresh rows — the verb
// is need-derived: a well is thirst, a bush is hunger); "harvest" mints a
// portable item into inventory. The name mirrors the engine's ObjectRefresh
// vocabulary and stays clear of the separate inventory `consume` tool.
type SourceActivityKind string

const (
	SourceActivityRefresh SourceActivityKind = "refresh"
	SourceActivityHarvest SourceActivityKind = "harvest"
	// SourceActivityRepair is an owner mending their worn market stall (LLM-118):
	// nails are consumed at start, the window runs StallRepairDurationSeconds, and
	// completion resets the stall's Wear to 0 so it trades again.
	SourceActivityRepair SourceActivityKind = "repair"
)

// Durations are tunable engine constants (Jeff approved eat ~3s / harvest ~5s
// as a starting point, 2026-06-21). A per-source override can ride the refresh
// row later if some sources should take longer; a constant is the right first
// cut.
const (
	RefreshActivityDuration = 3 * time.Second
	HarvestActivityDuration = 5 * time.Second
)

// SourceActivity is an actor's in-flight timed action at a village object. See
// Actor.SourceActivity. Value struct (no nested pointers) so CloneActor copies
// it shallowly.
type SourceActivity struct {
	Kind      SourceActivityKind
	ObjectID  VillageObjectID
	StartedAt time.Time
	Until     time.Time
	Qty       int // harvest only: units requested (clamped to stock at completion)
}

// SourceActivityStartResult is the Command reply for the START commands — what
// was begun (or a zero value with Started=false when the actor isn't at an
// applicable source). The handler/route turns it into the model/HTTP narration.
type SourceActivityStartResult struct {
	Started    bool
	Kind       SourceActivityKind
	ObjectID   VillageObjectID
	SourceName string
	Until      time.Time
}

// BusyAtSource reports whether the actor is mid-activity at a source as of now.
// Used by the reactor (shelve the LLM tick while busy) and as the "an in-flight
// bite is interruptible" signal. The window self-clears at completion, so this
// goes false on its own.
func (a *Actor) BusyAtSource(now time.Time) bool {
	return a.SourceActivity != nil && a.SourceActivity.Until.After(now)
}

// SourceActivityStarted / SourceActivityCompleted are the surfacing seams for
// LLM-56 (PC HUD feedback over the hub). They carry no payload a subscriber
// can't re-derive from the actor/object; LLM-54 emits them with no subscriber.
type SourceActivityStarted struct {
	EventBase
	ActorID  ActorID
	ObjectID VillageObjectID
	Kind     SourceActivityKind
	Until    time.Time
	At       time.Time
}

func (SourceActivityStarted) isSimEvent() {}

// SourceActivityCompleted carries, beyond the bare seam fields, enough to
// narrate the NPC completion beat without a subscriber re-reading world state
// (LLM-69) — the same self-contained-payload posture as the dwell-lifecycle
// events. Item/Qty are the harvest yield (the forage-to-sell closing signal);
// Attribute is the primary need a refresh eased (drives the eat/drink verb);
// SourceName is the resolved object display name. Continues marks a non-terminal
// auto-repeat bite (LLM-55): a finite refresh re-arms a fresh window after the
// emit, so only the terminal (Continues=false) completion mints a perception
// warrant — the repeat bites would otherwise stamp a redundant beat per bite
// while the actor is still shelved mid-meal.
type SourceActivityCompleted struct {
	EventBase
	ActorID    ActorID
	ObjectID   VillageObjectID
	Kind       SourceActivityKind
	Item       ItemKind // harvest only: the kind credited
	Qty        int      // harvest only: units actually gathered
	Attribute  NeedKey  // refresh only: the primary need eased
	SourceName string   // resolved object display name (both kinds)
	Continues  bool     // true when a refresh auto-repeat re-arms after this emit
	At         time.Time
}

func (SourceActivityCompleted) isSimEvent() {}

// SourceActivityCancelled fires when an in-flight activity is abandoned before
// completion — today only on a committed move (commands_move.go), the one place
// abandonment is centralized. The surfacing seam for LLM-56 to clear a PC HUD
// that was showing in-progress feedback (a start with no matching completion).
type SourceActivityCancelled struct {
	EventBase
	ActorID  ActorID
	ObjectID VillageObjectID
	Kind     SourceActivityKind
	At       time.Time
}

func (SourceActivityCancelled) isSimEvent() {}

// hasApplicableRefreshRow reports whether obj carries at least one refresh row
// an actor could actually consume in place right now: a need-bearing row
// (Amount < 0 — a yield-only forage-to-sell row is Amount == 0 and skipped), a
// known need attribute, and stock if finite. Mirrors the per-row skips in
// applyObjectRefreshEffect so START doesn't begin a bite that would no-op.
func hasApplicableRefreshRow(obj *VillageObject) bool {
	for _, r := range obj.Refreshes {
		if r.Amount == 0 {
			continue
		}
		if _, known := FindNeed(r.Attribute); !known {
			continue
		}
		if r.IsFinite() && *r.AvailableQuantity <= 0 {
			continue
		}
		return true
	}
	return false
}

// StartRefreshAtArrival begins a timed eat/drink at the refresh source the actor
// is loitering at, or no-ops (Started=false) when there's nothing to consume
// here. This is the arrival path's replacement for the instant
// ApplyObjectRefreshAtArrival: the cascade ActorArrived subscriber calls it, so
// an actor that walks onto a well/edible bush starts drinking/eating (the effect
// lands a few seconds later at completion).
//
// Skips (Started=false, nil error — arriving somewhere non-consumable is the
// common case): not at a refresh object, the object is owned by someone else
// (LLM-50 D2), it has no applicable refresh row (yield-only / depleted), or the
// actor is already engaged at a source (a re-arrival, or a harvest in flight —
// the running window owns the actor until it completes).
func StartRefreshAtArrival(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StartRefreshAtArrival: actor %q not found", actorID)
			}
			now := time.Now().UTC()
			// Land a finished-but-not-yet-swept window first (the ~1s sweep may
			// not have run since it expired) so a stale activity doesn't block a
			// fresh arrival. If one is still genuinely in flight, leave it be.
			completeIfDue(w, actorID, actor, now)
			if actor.SourceActivity != nil {
				return SourceActivityStartResult{}, nil
			}
			objID, obj := findRefreshObjectNear(w, actor.Pos)
			if obj == nil {
				return SourceActivityStartResult{}, nil
			}
			if obj.OwnedByOther(actorID) {
				return SourceActivityStartResult{}, nil
			}
			// LLM-87: an NPC at a BUSH (a finite gatherable source) eats it via
			// gather -> consume — it has the tools and decides for itself how much to
			// take — so it does NOT auto-eat on arrival. The PC (no tools) still eats
			// on arrival here. A WELL is gatherable too but INFINITE, so it's NOT a
			// bush: NPCs keep their arrival + dwell drink path there. This is the
			// "unify NPC eating to gather->consume" decision, scoped to bushes.
			if actor.Kind != KindPC && obj.IsFiniteGatherableSource() {
				return SourceActivityStartResult{}, nil
			}
			if !hasApplicableRefreshRow(obj) {
				return SourceActivityStartResult{}, nil
			}
			actor.SourceActivity = &SourceActivity{
				Kind:      SourceActivityRefresh,
				ObjectID:  objID,
				StartedAt: now,
				Until:     now.Add(RefreshActivityDuration),
			}
			w.emit(&SourceActivityStarted{
				ActorID:  actorID,
				ObjectID: objID,
				Kind:     SourceActivityRefresh,
				Until:    actor.SourceActivity.Until,
				At:       now,
			})
			return SourceActivityStartResult{
				Started:  true,
				Kind:     SourceActivityRefresh,
				ObjectID: objID,
				Until:    actor.SourceActivity.Until,
			}, nil
		},
	}
}

// StartHarvest begins a timed harvest of the gatherable source the actor is
// loitering at. The validating half of the old instant Gather: it resolves and
// gates (must have arrived, owns/commons, source has stock, not already busy)
// and sets the window; the mint lands at completion (applyGatherMint). Errors
// are the same family Gather raised so the gather tool / pc route narrate them
// unchanged.
//
// Picks the source CLEAN (LLM-87): the harvest takes ALL ripe units in one go,
// so the qty argument is ignored. This makes an NPC's eat loop move_to -> gather
// -> consume rather than a per-berry chain.
func StartHarvest(actorID ActorID, qty int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StartHarvest: actor %q not in world", actorID)
			}
			if actor.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before gathering. " +
						"Walk to the source and arrive, then gather.",
				)
			}
			now := time.Now().UTC()
			// Land a finished-but-not-yet-swept window first so a stale activity
			// doesn't spuriously read as "still busy" in the gap before the sweep.
			completeIfDue(w, actorID, actor, now)
			if actor.SourceActivity != nil {
				return nil, errors.New(
					"you are already busy at the source — wait until you finish before gathering again.",
				)
			}
			objID, obj, row := findGatherableObjectNear(w, actor)
			if row == nil {
				return nil, fmt.Errorf("StartHarvest: %w", ErrNoGatherSource)
			}
			if obj.OwnedByOther(actorID) {
				return nil, fmt.Errorf("StartHarvest: %w", ErrNotYourSource)
			}
			if _, ok := resolveItemKind(w, string(row.GatherItem)); !ok {
				return nil, fmt.Errorf("StartHarvest: %w %q (source %s gather_item)", ErrUnknownItemKind, row.GatherItem, objID)
			}
			if row.IsFinite() && *row.AvailableQuantity <= 0 {
				return nil, fmt.Errorf("StartHarvest: %w", ErrGatherableDepleted)
			}
			// LLM-87: gather picks the source CLEAN — one gather takes ALL ripe
			// units, so an NPC's eat loop is move_to -> gather -> consume rather than
			// a per-berry chain, and the bush flips to its bare sprite in one go. The
			// qty argument is ignored. An infinite gatherable source (none today) has
			// no "all" to take, so it falls back to a single unit.
			requested := 1
			if row.IsFinite() {
				requested = *row.AvailableQuantity
			}
			catalogName := ""
			if a := w.Assets[obj.AssetID]; a != nil {
				catalogName = a.Name
			}
			actor.SourceActivity = &SourceActivity{
				Kind:      SourceActivityHarvest,
				ObjectID:  objID,
				StartedAt: now,
				Until:     now.Add(HarvestActivityDuration),
				Qty:       requested,
			}
			w.emit(&SourceActivityStarted{
				ActorID:  actorID,
				ObjectID: objID,
				Kind:     SourceActivityHarvest,
				Until:    actor.SourceActivity.Until,
				At:       now,
			})
			return SourceActivityStartResult{
				Started:    true,
				Kind:       SourceActivityHarvest,
				ObjectID:   objID,
				SourceName: obj.EffectiveDisplayName(catalogName),
				Until:      actor.SourceActivity.Until,
			}, nil
		},
	}
}

// StartRepair begins a timed repair of the worn market stall the owner is
// standing at (LLM-118). Validates ownership + co-location + that the stall
// actually needs mending + that the owner holds enough nails, consumes the nails
// up front, and opens the SourceActivity window; the wear reset lands at
// completion (applyCompletedSourceActivity). The repair tool is gated to be
// advertised only in exactly this situation, but every gate is re-validated here
// because the substrate stays authoritative.
//
// Nails are consumed at START with no refund on an abandoned repair: the move-
// cancel belt clears the window if the owner walks off, but the wear isn't reset,
// so they simply buy/retry — simpler than a refund and the lost nails are the
// cost of starting and walking away.
func StartRepair(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StartRepair: actor %q not in world", actorID)
			}
			if actor.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — arrive at your stall before mending it.",
				)
			}
			now := time.Now().UTC()
			// Land a finished-but-not-yet-swept window first so a stale activity
			// doesn't spuriously read as "still busy".
			completeIfDue(w, actorID, actor, now)
			if actor.SourceActivity != nil {
				return nil, errors.New(
					"you are already busy — finish what you're doing before mending the stall.",
				)
			}
			objID, atPin := resolveLoiteringObject(w, actor.Pos, LoiterAttributionTiles)
			if !atPin {
				return nil, errors.New("walk to your stall before mending it.")
			}
			stall := w.VillageObjects[objID]
			if stall == nil || stall.OwnerActorID != actorID || !IsWearableStall(stall) {
				return nil, errors.New("there's no stall of yours to mend here.")
			}
			if !StallNeedsRepair(stall, w.Settings.StallWearRepairThreshold) {
				return nil, errors.New("your stall doesn't need mending yet.")
			}
			need := w.Settings.StallNailsPerRepair
			if actor.Inventory[NailItemKind] < need {
				return nil, fmt.Errorf(
					"mending the stall takes %d nails but you have %d — buy more nails first.",
					need, actor.Inventory[NailItemKind],
				)
			}
			// Consume the nails up front (delete-on-zero inventory invariant).
			actor.Inventory[NailItemKind] -= need
			if actor.Inventory[NailItemKind] == 0 {
				delete(actor.Inventory, NailItemKind)
			}
			actor.SourceActivity = &SourceActivity{
				Kind:      SourceActivityRepair,
				ObjectID:  objID,
				StartedAt: now,
				Until:     now.Add(time.Duration(w.Settings.StallRepairDurationSeconds) * time.Second),
			}
			w.emit(&SourceActivityStarted{
				ActorID:  actorID,
				ObjectID: objID,
				Kind:     SourceActivityRepair,
				Until:    actor.SourceActivity.Until,
				At:       now,
			})
			return SourceActivityStartResult{
				Started:    true,
				Kind:       SourceActivityRepair,
				ObjectID:   objID,
				SourceName: sourceActivityObjectName(w, stall),
				Until:      actor.SourceActivity.Until,
			}, nil
		},
	}
}

// applyCompletedSourceActivity lands the effect of a finished activity. It
// RE-RESOLVES the source off the actor's live tile and applies only if the actor
// is still at the SAME object it began at — a defensive guard mirroring the
// arrival freshness check (a deliberate move already abandons the window, so in
// practice the actor is still here). On a kind/source mismatch it simply does
// nothing; the window is already cleared by the caller.
func applyCompletedSourceActivity(w *World, actorID ActorID, actor *Actor, act *SourceActivity, now time.Time) {
	switch act.Kind {
	case SourceActivityRefresh:
		objID, obj := findRefreshObjectNear(w, actor.Pos)
		if obj == nil || objID != act.ObjectID || obj.OwnedByOther(actorID) {
			return
		}
		res := applyObjectRefreshEffect(w, actorID, objID, obj, now)
		// Compute the repeat decision BEFORE emitting so the completion event
		// can flag a non-terminal bite (Continues): the perception subscriber
		// only narrates the terminal completion, not each auto-repeat bite the
		// re-arm below schedules (LLM-69). Attribute is the primary need eased
		// (drives the eat/drink verb); SourceName the resolved display name.
		//
		// Auto-graze (LLM-55) is PC-ONLY (LLM-87): the human player has no
		// gather/consume tools, so the engine grazes a finite source down on its
		// behalf. An NPC HAS those tools, so it eats one bite and decides for
		// itself whether to gather more — it is never auto-re-armed. Its
		// completion beat (rendered from this event) surfaces the remaining stock
		// so that choice is informed.
		willRepeat := actor.Kind == KindPC && shouldRepeatRefresh(actor, obj)
		var refreshAttr NeedKey
		if len(res.Hits) > 0 {
			refreshAttr = res.Hits[0].Attribute
		}
		w.emit(&SourceActivityCompleted{
			ActorID:    actorID,
			ObjectID:   act.ObjectID,
			Kind:       act.Kind,
			Attribute:  refreshAttr,
			SourceName: sourceActivityObjectName(w, obj),
			Continues:  willRepeat,
			At:         now,
		})
		// Auto-repeat (LLM-55): keep eating a FINITE source while the actor is
		// still in need and stock remains, so a bush is eaten berry-by-berry
		// until full or picked clean — at which point applyObjectRefreshEffect
		// above has already flipped it to its bare state. Re-arm a fresh window;
		// the next completion sweep lands the next bite. Deliberately finite-only:
		// an INFINITE source (the well) is never re-armed, so it keeps its
		// arrival + dwell-drip behavior untouched (continuous drinking there is
		// the dwell tick's job, not this loop).
		if willRepeat {
			actor.SourceActivity = &SourceActivity{
				Kind:      SourceActivityRefresh,
				ObjectID:  objID,
				StartedAt: now,
				Until:     now.Add(RefreshActivityDuration),
			}
			w.emit(&SourceActivityStarted{
				ActorID:  actorID,
				ObjectID: objID,
				Kind:     SourceActivityRefresh,
				Until:    actor.SourceActivity.Until,
				At:       now,
			})
		}
	case SourceActivityHarvest:
		objID, obj, row := findGatherableObjectNear(w, actor)
		if row == nil || objID != act.ObjectID || obj.OwnedByOther(actorID) {
			return
		}
		kind, ok := resolveItemKind(w, string(row.GatherItem))
		if !ok {
			return
		}
		// Stock may have drained during the window: ErrGatherableDepleted is a
		// benign nothing-harvested completion (clamped to empty), so swallow it
		// — but surface any OTHER failure (e.g. inventory overflow) rather than
		// losing it silently. Either way no effect landed, so don't emit completion.
		res, err := applyGatherMint(w, actor, objID, obj, row, kind, act.Qty, now)
		if err != nil {
			if !errors.Is(err, ErrGatherableDepleted) {
				log.Printf("sim/source_activity: harvest completion failed for %q at %q: %v", actorID, objID, err)
			}
			return
		}
		w.emit(&SourceActivityCompleted{
			ActorID:    actorID,
			ObjectID:   act.ObjectID,
			Kind:       act.Kind,
			Item:       res.Item,
			Qty:        res.Qty,
			SourceName: res.SourceName,
			Continues:  false, // harvest never auto-repeats
			At:         now,
		})
	case SourceActivityRepair:
		// LLM-118: the mending lands — wear cleared, the stall trades again. The
		// waking repair warrant was already consumed by the deliberation tick
		// that chose repair; Wear=0 re-arms the edge-triggered warrant for the
		// next time the stall wears through the threshold. Re-resolve by the
		// object id the window began at (the move-cancel belt already aborts a
		// window if the owner walks off, so they are still here).
		stall := w.VillageObjects[act.ObjectID]
		if stall == nil || stall.OwnerActorID != actorID || !IsWearableStall(stall) {
			return
		}
		stall.Wear = 0
		w.emit(&SourceActivityCompleted{
			ActorID:    actorID,
			ObjectID:   act.ObjectID,
			Kind:       act.Kind,
			SourceName: sourceActivityObjectName(w, stall),
			Continues:  false,
			At:         now,
		})
	}
}

// shouldRepeatRefresh reports whether an eat/drink in place should immediately
// continue: the source carries a FINITE, need-bearing, in-stock row whose need
// the actor still feels. This is the "keep eating until full or empty" loop
// (LLM-55) — a bush is eaten berry-by-berry while the eater stays put. Finite by
// design: an infinite source (a well) is never re-armed here, so it keeps its
// arrival + dwell behavior; continuous drinking there is the dwell tick's job.
func shouldRepeatRefresh(actor *Actor, obj *VillageObject) bool {
	if actor == nil || obj == nil {
		return false
	}
	// Object-level: a bite applies the WHOLE object's refresh (every row) and
	// decrements each finite supply, so the eat repeats while ANY finite,
	// need-bearing row still has stock and a need the actor still feels.
	for _, r := range obj.Refreshes {
		if r == nil {
			continue
		}
		// >= 0 skips need-increasing rows AND yield-only (Amount == 0); the
		// schema forbids Amount > 0, but the predicate states the contract
		// (need-bearing = Amount < 0) outright. Infinite rows (a well) never
		// auto-repeat — continuous drinking there is the dwell tick's job.
		if r.Amount >= 0 || !r.IsFinite() {
			continue
		}
		if *r.AvailableQuantity <= 0 { // IsFinite guarantees AvailableQuantity != nil
			continue // picked clean
		}
		if _, known := FindNeed(r.Attribute); !known {
			continue
		}
		if actor.Needs[r.Attribute] > 0 {
			return true // still in need and stock remains → eat again
		}
	}
	return false
}

// completeDueSourceActivities lands every activity whose Until has passed as of
// now and clears the window; returns how many completed. A free function taking
// now explicitly (mirrors regenObjectRefresh) so the ticker drives it with the
// wall clock while tests drive it with a chosen instant. Collects the due ids in
// a first pass, applies in a second — applyCompletedSourceActivity emits
// (ItemGathered, SourceActivityCompleted) whose subscribers run inline and may
// touch w.Actors, so the apply must not run while ranging that map.
func completeDueSourceActivities(w *World, now time.Time) int {
	var due []ActorID
	for id, a := range w.Actors {
		if a.SourceActivity != nil && !a.SourceActivity.Until.After(now) {
			due = append(due, id)
		}
	}
	completed := 0
	for _, id := range due {
		if completeIfDue(w, id, w.Actors[id], now) {
			completed++
		}
	}
	return completed
}

// completeIfDue lands + clears the actor's activity if it has expired as of now,
// returning whether it did. The shared "finish one window" primitive: the sweep
// calls it per due actor (so the count reflects activities actually applied, not
// merely scanned), and the START gates call it to self-heal a finished-but-not-
// yet-swept window instead of treating it as still busy.
func completeIfDue(w *World, actorID ActorID, actor *Actor, now time.Time) bool {
	if actor == nil || actor.SourceActivity == nil || actor.SourceActivity.Until.After(now) {
		return false
	}
	act := actor.SourceActivity
	actor.SourceActivity = nil // clear before applying; the effect re-resolves off live state
	applyCompletedSourceActivity(w, actorID, actor, act, now)
	return true
}

// SourceActivityTickerInterval is how often RunSourceActivityTicker wakes. One
// second is fine-grained relative to the few-second durations — the worst-case
// completion lands within a second of its target — and a once-per-second scan of
// the actor set is trivially cheap.
const SourceActivityTickerInterval = time.Second

// SourceActivityCompletedWarrantReason captures the NPC completion beat for a
// finished timed eat/drink/harvest (LLM-69). Minted by the handlers-side
// SourceActivityCompleted subscriber onto the actor's reactor warrant list so
// the next-tick perception surfaces the cue — "you finish gathering; you now
// have 3 blueberries in your pack" — closing the loop the consume-at-source
// feature (LLM-54..57) left open on the NPC side. Mirrors DwellEndedWarrantReason:
// NarrationText is pre-rendered at the subscriber so render-time work stays
// cheap, and DedupDiscriminator returns 0 (lifecycle-stamp posture — each
// completion is 1:1 with its triggering sweep, so there's nothing to dedup, and
// (Kind, 0) bypass keeps unrelated completions from collapsing).
type SourceActivityCompletedWarrantReason struct {
	ActivityKind  SourceActivityKind
	Item          ItemKind
	Qty           int
	Attribute     NeedKey
	SourceName    string
	NarrationText string
}

func (SourceActivityCompletedWarrantReason) isWarrantReason()           {}
func (SourceActivityCompletedWarrantReason) Kind() WarrantKind          { return WarrantKindSourceActivityDone }
func (SourceActivityCompletedWarrantReason) DedupDiscriminator() uint64 { return 0 }

// SourceActivityCompletionNarration returns the felt-language self-perception
// line for a finished source activity (LLM-69), the SourceActivity sibling of
// DwellCompletionNarration. Harvest names the yield (the forage-to-sell closing
// signal); refresh reads the eased need. sourceName is the resolved object
// display name; an empty name drops the "at X" clause. Returns "" for an
// unhandled combination so the subscriber skips the warrant (matching the dwell
// empty-narration posture) rather than minting the vague fallback line.
func SourceActivityCompletionNarration(kind SourceActivityKind, item ItemKind, qty int, attribute NeedKey, sourceName string) string {
	at := ""
	if sourceName != "" {
		at = " at " + sourceName
	}
	switch kind {
	case SourceActivityHarvest:
		if qty <= 0 {
			return ""
		}
		yield := string(item)
		if yield == "" {
			yield = "what you gathered"
		}
		return fmt.Sprintf("You finish gathering%s; you now have %d %s in your pack.", at, qty, yield)
	case SourceActivityRefresh:
		switch attribute {
		case "hunger":
			return fmt.Sprintf("You finish eating%s; the gnawing eases.", at)
		case "thirst":
			return fmt.Sprintf("You finish drinking%s; the dryness fades.", at)
		case "tiredness":
			return fmt.Sprintf("You finish resting%s; the weariness eases.", at)
		}
	case SourceActivityRepair:
		return "You finish mending your stall; it is sound again."
	}
	return ""
}

// sourceActivityObjectName resolves obj's display name for a completion beat —
// the object's own override, falling back to the asset catalog name. Mirrors the
// name resolution in StartHarvest / applyGatherMint so every source-activity
// surface names a source the same way.
func sourceActivityObjectName(w *World, obj *VillageObject) string {
	if obj == nil {
		return ""
	}
	catalogName := ""
	if a := w.Assets[obj.AssetID]; a != nil {
		catalogName = a.Name
	}
	return obj.EffectiveDisplayName(catalogName)
}

// primaryRefreshNeed returns the first need-bearing, applicable refresh need an
// in-place eat/drink at obj eases — the attribute that drives the standing
// busy-state verb ("eating" vs "drinking") in perception (LLM-69). Mirrors the
// per-row skips in hasApplicableRefreshRow (yield-only Amount==0, unknown
// attribute, depleted finite stock). Returns "" when none applies.
func primaryRefreshNeed(obj *VillageObject) NeedKey {
	if obj == nil {
		return ""
	}
	for _, r := range obj.Refreshes {
		if r == nil || r.Amount == 0 {
			continue
		}
		if _, known := FindNeed(r.Attribute); !known {
			continue
		}
		if r.IsFinite() && *r.AvailableQuantity <= 0 {
			continue
		}
		return r.Attribute
	}
	return ""
}

// RunSourceActivityTicker owns the completion-sweep goroutine. Wakes every
// SourceActivityTickerInterval and submits completeDueSourceActivities. Same
// time.NewTicker idiom as RunObjectRefreshRegen / RunNeedsTicker.
func RunSourceActivityTicker(ctx context.Context, w *World) {
	t := time.NewTicker(SourceActivityTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("source_activity")
			_, err := w.SendContext(ctx, Command{Fn: func(world *World) (any, error) {
				return completeDueSourceActivities(world, time.Now().UTC()), nil
			}})
			if err != nil && ctx.Err() == nil {
				log.Printf("sim/source_activity: completion tick failed: %v", err)
			}
		}
	}
}
