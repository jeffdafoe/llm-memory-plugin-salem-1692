package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// Dwell tick — in-memory port of engine/dwell_tick.go's
// dispatchObjectRefreshDwell + applyDwellCredit.
//
// Per-minute handler converting dwell credits into need recovery for
// actors still present at the pinned object. Algorithm:
//
//   For each actor, for each DwellCredit:
//     1. If not ripe (LastCreditedAt + period > now), skip.
//     2. If actor is no longer at the credit's object (loiter check),
//        emit DwellEnded{WalkedAway}, delete the credit, continue.
//     3. If credit.Attribute is unknown to the Needs registry, emit
//        DwellEnded{CatalogUnknown}, delete the credit, continue.
//     4. Apply DwellDelta to actor.Needs[attribute] via ClampNeed.
//     5. Decide terminating reason (in order of precedence):
//          - item-exhausted: source=item && RemainingTicks <= 1
//          - floor-hit: pre>0 && post==0
//        Non-terminating: advance LastCreditedAt by exactly
//        DwellPeriodMinutes (residual time carries forward; phase
//        doesn't drift) and decrement RemainingTicks for item-source
//        credits.
//     6. Emit DwellTickApplied for the applied credit. If terminating,
//        emit DwellEnded with the appropriate reason and delete the
//        credit (floor-hit terminates the credit too — parity with v1's
//        "you feel full → meal done" intent). Stamp DwellCompletion in
//        the result for ALL actor kinds (NPCs included — perception
//        gating happens at subscriber layer, not at emit).
//
// LOITER LOOKUP STUB. Same as ApplyObjectRefreshAtArrival: legacy
// resolveLoiteringStructure isn't ported yet. Using Euclidean distance
// against ObjectRefreshArrivalTolerance until loitering lands.
//
// HUB BROADCAST STUB. The DwellTickResult.Completions slice carries the
// pre-rendered narration lines for callers/tests that want to observe
// the per-credit outcome without subscribing to events. The same
// narration is also available via DwellEnded subscribers (the proper
// channel post-Hub-port). PC-only gating dropped — every actor's
// completion is collected; render-time filtering (PC HUD vs LLM
// perception) lives in subscriber/Hub layers.

// DwellCompletion is a per-credit narration emission produced by
// ApplyDwellTick. Pre-substrate stub left in place for backward-compat
// with callers/tests that observed the result struct; new code should
// subscribe to DwellEnded events instead. Hub layer (when ported) will
// fan these out as private room_event broadcasts for PCs.
type DwellCompletion struct {
	ActorID       ActorID
	StructureID   StructureID // actor's InsideStructureID at apply time — scope for room_event broadcast
	Attribute     NeedKey
	Source        DwellCreditSource
	ItemExhausted bool
	FloorHit      bool
	WalkedAway    bool
	Text          string // pre-rendered narration, "" if no vocab matches
	At            time.Time
}

// DwellTickResult is what ApplyDwellTick returns: number of credits
// that fired plus any completion narrations.
type DwellTickResult struct {
	Applied     int
	Completions []DwellCompletion
}

// ApplyDwellTick walks every actor's DwellCredits, applies ripe ones,
// expires departed/exhausted ones, emits the per-credit event stream
// (DwellTickApplied + DwellEnded), and collects pre-rendered narration
// in the result for any actor.
//
// All work happens inside the world goroutine — the command channel
// serializes against concurrent state changes, so the legacy
// row-locking dance isn't needed here.
func ApplyDwellTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			res := DwellTickResult{}

			// Iterate actors in sorted order so DwellTickApplied /
			// DwellEnded event sequencing across actors is deterministic
			// — useful for golden-file tests and admin replay.
			actorIDs := make([]ActorID, 0, len(w.Actors))
			for id := range w.Actors {
				actorIDs = append(actorIDs, id)
			}
			sort.Slice(actorIDs, func(i, j int) bool { return actorIDs[i] < actorIDs[j] })

			for _, actorID := range actorIDs {
				actor := w.Actors[actorID]
				if actor == nil || actor.DwellCredits == nil {
					continue
				}
				processActorDwellTick(w, actor, now, &res)
			}
			return res, nil
		},
	}
}

// processActorDwellTick is the per-actor pass of ApplyDwellTick. Walks
// the actor's DwellCredits, applies ripe ones, emits per-credit events,
// and appends completion narrations to res. Extracted from the Command
// Fn for readability — the nested two-pass loop was the dominant cost
// of the original.
func processActorDwellTick(w *World, actor *Actor, now time.Time, res *DwellTickResult) {
	// Pass 1: collect terminating-without-apply credits (walked-away,
	// catalog-unknown) and ripe applies. The two-pass shape avoids
	// mutating the map mid-iteration.
	var walkedAway []DwellCreditKey
	var catalogUnknown []DwellCreditKey
	type fired struct {
		key         DwellCreditKey
		credit      *DwellCredit
		itemExhaust bool
		floorHit    bool
		preNeed     int
		postNeed    int
	}
	var fires []fired

	// Stable key order so events from one actor's tick fire in a
	// deterministic order — golden tests + admin replay sanity.
	keys := make([]DwellCreditKey, 0, len(actor.DwellCredits))
	for k := range actor.DwellCredits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Source != keys[j].Source {
			return keys[i].Source < keys[j].Source
		}
		if keys[i].Attribute != keys[j].Attribute {
			return keys[i].Attribute < keys[j].Attribute
		}
		return keys[i].ObjectID < keys[j].ObjectID
	})

	for _, key := range keys {
		credit := actor.DwellCredits[key]
		if credit == nil {
			continue
		}
		nextAt := credit.LastCreditedAt.Add(time.Duration(credit.DwellPeriodMinutes) * time.Minute)
		if nextAt.After(now) {
			continue // not ripe
		}
		if _, known := FindNeed(credit.Attribute); !known {
			log.Printf("sim/dwell_tick: actor %s credit unknown attribute %q (removing)",
				actor.ID, credit.Attribute)
			catalogUnknown = append(catalogUnknown, key)
			continue
		}
		if !actorAtCreditObject(w, actor, credit) {
			walkedAway = append(walkedAway, key)
			continue
		}

		preNeed := actor.Needs[credit.Attribute]
		if actor.Needs == nil {
			actor.Needs = make(map[NeedKey]int)
		}
		actor.Needs[credit.Attribute] = ClampNeed(preNeed + credit.DwellDelta)
		postNeed := actor.Needs[credit.Attribute]

		itemExhaust := credit.Source == DwellSourceItem &&
			credit.RemainingTicks != nil &&
			*credit.RemainingTicks <= 1
		floorHit := preNeed > 0 && postNeed == 0
		fires = append(fires, fired{
			key:         key,
			credit:      credit,
			itemExhaust: itemExhaust,
			floorHit:    floorHit,
			preNeed:     preNeed,
			postNeed:    postNeed,
		})
	}

	// Pass 2: emit walked-away terminals first (no apply event, no
	// payoff), then process ripe fires. The order matters only for
	// event-log readability and admin replay; subscribers don't depend
	// on it.
	for _, k := range walkedAway {
		credit := actor.DwellCredits[k]
		if credit == nil {
			continue
		}
		stampDwellEnd(w, actor, credit, DwellEndWalkedAway, now, res)
		delete(actor.DwellCredits, k)
	}
	for _, k := range catalogUnknown {
		credit := actor.DwellCredits[k]
		if credit == nil {
			continue
		}
		stampDwellEnd(w, actor, credit, DwellEndCatalogUnknown, now, res)
		delete(actor.DwellCredits, k)
	}

	for _, f := range fires {
		// Per the new design (call 4), floor-hit terminates the credit
		// the same way item-exhausted does — the meal is over once
		// you're full. Item-exhausted wins precedence when both apply
		// (parity with the legacy DwellCompletionNarration
		// precedence).
		terminating := f.itemExhaust || f.floorHit
		if !terminating {
			f.credit.LastCreditedAt = f.credit.LastCreditedAt.Add(
				time.Duration(f.credit.DwellPeriodMinutes) * time.Minute)
			if f.credit.Source == DwellSourceItem && f.credit.RemainingTicks != nil {
				rem := *f.credit.RemainingTicks - 1
				f.credit.RemainingTicks = &rem
			}
		}
		res.Applied++

		// Emit DwellTickApplied for the applied credit. RemainingTicks
		// is the POST-decrement count (so the final tick reports 0
		// alongside the DwellEnded{ItemExhausted} event below).
		var remaining *int
		if f.credit.RemainingTicks != nil {
			rt := *f.credit.RemainingTicks
			if terminating && f.itemExhaust {
				rt = 0
			}
			remaining = &rt
		}
		w.emit(&DwellTickApplied{
			ActorID:        actor.ID,
			ObjectID:       f.credit.ObjectID,
			Source:         f.credit.Source,
			Kind:           f.credit.Kind,
			Attribute:      f.credit.Attribute,
			NeedDelta:      f.postNeed - f.preNeed,
			NewNeedValue:   f.postNeed,
			RemainingTicks: remaining,
			PeriodMinutes:  f.credit.DwellPeriodMinutes,
			At:             now,
		})

		if terminating {
			reason := DwellEndItemExhausted
			if !f.itemExhaust && f.floorHit {
				reason = DwellEndFloorHit
			}
			stampDwellEnd(w, actor, f.credit, reason, now, res)
			delete(actor.DwellCredits, f.key)
		}
	}
}

// stampDwellEnd emits DwellEnded for a terminating credit and appends a
// DwellCompletion narration line to res. Shared by the walked-away
// (no-apply) and item-exhausted / floor-hit (apply-then-end) paths.
// The PC-only gating that the pre-substrate path used is gone —
// every actor produces a Completion; subscribers and Hub layer pick
// who gets the narration rendered.
func stampDwellEnd(w *World, actor *Actor, credit *DwellCredit, reason DwellEndReason, now time.Time, res *DwellTickResult) {
	w.emit(&DwellEnded{
		ActorID:   actor.ID,
		ObjectID:  credit.ObjectID,
		Source:    credit.Source,
		Kind:      credit.Kind,
		Attribute: credit.Attribute,
		Reason:    reason,
		At:        now,
	})
	text := DwellCompletionNarration(credit.Attribute, credit.Source,
		reason == DwellEndItemExhausted,
		reason == DwellEndFloorHit,
		reason == DwellEndWalkedAway,
	)
	res.Completions = append(res.Completions, DwellCompletion{
		ActorID:       actor.ID,
		StructureID:   actor.InsideStructureID,
		Attribute:     credit.Attribute,
		Source:        credit.Source,
		ItemExhausted: reason == DwellEndItemExhausted,
		FloorHit:      reason == DwellEndFloorHit,
		WalkedAway:    reason == DwellEndWalkedAway,
		Text:          text,
		At:            now,
	})
}

// actorAtCreditObject returns whether actor's current position is
// within ObjectRefreshArrivalTolerance of the credit's pinned object.
// Returns false if the object no longer exists.
func actorAtCreditObject(w *World, actor *Actor, credit *DwellCredit) bool {
	obj, ok := w.VillageObjects[credit.ObjectID]
	if !ok {
		return false
	}
	dx := obj.X - float64(actor.Pos.X)
	dy := obj.Y - float64(actor.Pos.Y)
	return dx*dx+dy*dy <= ObjectRefreshArrivalTolerance*ObjectRefreshArrivalTolerance
}

// DwellTickerInterval is how often RunDwellTicker wakes. Matches
// legacy 1-min cadence.
const DwellTickerInterval = time.Minute

// RunDwellTicker owns the dwell-tick goroutine. Wakes every
// DwellTickerInterval and submits an ApplyDwellTick command.
func RunDwellTicker(ctx context.Context, w *World) {
	t := time.NewTicker(DwellTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, err := w.SendContext(ctx, ApplyDwellTick(time.Now().UTC()))
			if err != nil && ctx.Err() == nil {
				log.Printf("sim/dwell_ticker: %v", err)
			}
		}
	}
}
