package sim

import (
	"fmt"
	"log"
	"time"
)

// bake.go — LLM-454. The evening bake-bread occupation: a shared, per-home activity
// that fills the empty evening (the gap LLM-447's night bed-down left, and that
// LLM-453's disperse verb failed to close — the models never took an exit; they
// wanted the task). A resident at home in the evening starts the household's bread;
// others home lend a hand at the SAME batch. Everyone is occupied (BusyAtSource
// shelves the LLM tick, so no looping) until their bed cue; then the batch lands to
// the initiator, who shares it around before turning in.
//
// Built on the SourceActivity dwell substrate (produce is workplace-locked): a
// SourceActivityBake window per participant, plus one HomeBake session per home
// holding the shared batch. Both are transient — a restart drops them, which costs
// nothing because flour is consumed only at completion (the household just re-forms
// the bake and still finishes by bedtime; persistent inventory stays consistent).

// Bake tuning — deliberately low. This is an evening TIME SINK, not an economy: the
// output that matters is "the evening is spent, not looped," and the loaves are a
// byproduct the household eats. All tunable constants.
const (
	BakeFlourItem = ItemKind("flour")
	BakeBreadItem = ItemKind("bread")
	// BakeFlourCost is the flour the INITIATOR provides — checked at start, consumed
	// at completion (so a restart mid-bake forfeits nothing).
	BakeFlourCost = 2
	// BakeBatchQty is the loaves minted to the initiator when the bake lands.
	BakeBatchQty = 3
	// MinBakeWindow is the least evening that must remain before bedtime for a bake
	// to be worth starting.
	MinBakeWindow = 30 * time.Minute
)

// HomeBake is the single shared bake in progress at one home structure (keyed by
// StructureID in World.HomeBakes). Transient — NOT checkpointed; a restart drops it
// along with the participants' SourceActivity, and since flour is consumed only at
// completion the household simply re-forms the bake, forfeiting nothing.
type HomeBake struct {
	StructureID StructureID
	InitiatorID ActorID
	BatchQty    int
	FlourCost   int
	StartedAt   time.Time
	DoneAt      time.Time // the initiator's bed cue — when the batch lands
}

// bedtimeInstant returns the next lodger-bedtime instant (LodgingBedtimeHour in the
// world timezone) strictly after now — the bed cue an evening bake runs until.
// Modeled on nextDaybreak; fails open to now+4h so a bad clock can't mint a
// never-ending bake.
func bedtimeInstant(w *World, now time.Time) time.Time {
	start, _, ok := lodgerNightWindow(w) // [bedtime, dawn) minutes
	loc := w.Settings.Location
	if !ok || loc == nil {
		return now.Add(4 * time.Hour)
	}
	local := now.In(loc)
	bed := time.Date(local.Year(), local.Month(), local.Day(), start/60, start%60, 0, 0, loc)
	if !bed.After(now) {
		bed = bed.AddDate(0, 0, 1)
	}
	return bed
}

// StartOrJoinBake is the commit for the bake tool. If a HomeBake is already going at
// the actor's home it JOINS it (no flour, no new batch — the shared household bake);
// otherwise it STARTS one (the initiator provides the flour). Either way the actor
// breaks off any conversation and is occupied at a SourceActivityBake window until
// its bed cue. MUST run on the world goroutine.
func StartOrJoinBake(actorID ActorID, say string, hasNewNews bool, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("StartOrJoinBake: actor %q not in world", actorID)
			}
			if actor.MoveIntent != nil {
				return nil, ModelFacingError{Msg: "you are walking — get home before you start baking."}
			}
			// Land a finished-but-not-yet-swept window first so a stale activity
			// doesn't spuriously read as "still busy" (matches StartStoke).
			completeIfDue(w, actorID, actor, now)
			if actor.SourceActivity != nil {
				return nil, ModelFacingError{Msg: "you are already busy — finish what you're doing first."}
			}
			home := actor.HomeStructureID
			if home == "" || actor.InsideStructureID != home {
				return nil, ModelFacingError{Msg: "you can only bake in your own home."}
			}
			doneAt := bedtimeInstant(w, now)
			if !doneAt.After(now.Add(MinBakeWindow)) {
				return nil, ModelFacingError{Msg: "there's not enough of the evening left to bake before bed."}
			}

			session := w.HomeBakes[home]
			if session == nil {
				// START — the initiator provides the flour (checked now, consumed at
				// completion).
				if actor.Inventory[BakeFlourItem] < BakeFlourCost {
					return nil, ModelFacingError{Msg: fmt.Sprintf(
						"baking a batch takes %d bags of flour and you have %d — buy more from the store first.",
						BakeFlourCost, actor.Inventory[BakeFlourItem])}
				}
				session = &HomeBake{
					StructureID: home,
					InitiatorID: actorID,
					BatchQty:    BakeBatchQty,
					FlourCost:   BakeFlourCost,
					StartedAt:   now,
					DoneAt:      doneAt,
				}
				w.HomeBakes[home] = session
			}

			// Break off any conversation to go to the hearth — the parting word (if
			// any) rides the tool, spoken to the room before leaving (best-effort,
			// like scene_quote's say). Baking then shelves the tick until bed.
			if actor.CurrentHuddleID != "" {
				if say != "" {
					if _, serr := SpeakTo(actorID, say, "", nil, hasNewNews, now).Fn(w); serr != nil {
						log.Printf("sim: bake %q announced but the say was refused: %v", actorID, serr)
					}
				}
				leaveCurrentHuddle(w, actor, now)
			}

			homeObj := w.VillageObjects[VillageObjectID(home)]
			actor.SourceActivity = &SourceActivity{
				Kind:      SourceActivityBake,
				ObjectID:  VillageObjectID(home),
				StartedAt: now,
				Until:     doneAt,
			}
			w.emit(&SourceActivityStarted{
				ActorID:  actorID,
				ObjectID: VillageObjectID(home),
				Kind:     SourceActivityBake,
				Until:    doneAt,
				At:       now,
			})
			return SourceActivityStartResult{
				Started:    true,
				Kind:       SourceActivityBake,
				ObjectID:   VillageObjectID(home),
				SourceName: sourceActivityObjectName(w, homeObj),
				Until:      doneAt,
			}, nil
		},
	}
}

// completeHomeBake lands the shared batch for the session at home when its INITIATOR
// finishes: it consumes the initiator's flour (re-checked; the baker was shelved so
// it is still on hand) and mints the loaves to it. Returns the minted qty (0 for a
// joiner, an orphaned/concluded session, or a vanished-flour edge) so the caller's
// completion beat can name the yield. Deletes the session on the initiator's landing.
func completeHomeBake(w *World, actorID ActorID, actor *Actor, home StructureID) int {
	session := w.HomeBakes[home]
	if session == nil || session.InitiatorID != actorID {
		return 0 // a joiner, or the session already concluded — a hand lent, no batch
	}
	delete(w.HomeBakes, home)
	if actor.Inventory[BakeFlourItem] < session.FlourCost {
		return 0 // flour gone (shouldn't happen while shelved) — nothing to show for it
	}
	actor.Inventory[BakeFlourItem] -= session.FlourCost
	if actor.Inventory[BakeFlourItem] == 0 {
		delete(actor.Inventory, BakeFlourItem)
	}
	if actor.Inventory == nil {
		actor.Inventory = map[ItemKind]int{}
	}
	actor.Inventory[BakeBreadItem] += session.BatchQty
	return session.BatchQty
}

// concludeAbandonedBake ends the household bake if the actor walking away WAS its
// initiator (its SourceActivityBake was just move-cancelled): the initiator carried
// the flour and the yield, so nothing lands. Joiners still baking finish their own
// windows to no batch. A no-op for a joiner's departure or when no bake is running.
func concludeAbandonedBake(w *World, actorID ActorID, home StructureID) {
	if session := w.HomeBakes[home]; session != nil && session.InitiatorID == actorID {
		delete(w.HomeBakes, home)
	}
}

// homeBakeInProgress reports whether a shared bake is already running at home — the
// signal that a fresh bake tool call JOINS rather than STARTS (so it needs no flour).
// Read on the world goroutine.
func homeBakeInProgress(w *World, home StructureID) bool {
	return home != "" && w.HomeBakes[home] != nil
}

// homeBakesActiveSet projects the in-progress home bakes into a snapshot-ready set of
// structure ids (nil when none) — the read side perception's buildBakeChoice uses to
// know a bake is already going at a home, so a co-resident can JOIN it without flour.
func homeBakesActiveSet(w *World) map[StructureID]bool {
	if len(w.HomeBakes) == 0 {
		return nil
	}
	out := make(map[StructureID]bool, len(w.HomeBakes))
	for id := range w.HomeBakes {
		out[id] = true
	}
	return out
}
