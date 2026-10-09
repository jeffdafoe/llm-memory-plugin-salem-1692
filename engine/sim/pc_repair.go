package sim

// pc_repair.go — a player takes the town's repair work (LLM-690).
//
// A PC has no income route but this: the town's repair work (damage.go) pays a
// hand from the chest to mend a broken well, a damaged business or a road
// blocked by a fallen tree, and a player may take it on the same terms. Where a
// hand's repair is a timed window, a player's is a mini-game: the client plays
// one round per step and posts each to StepPCRepair, and the repair lands on the
// last step through the same completePublicWorksRepair a hand's does — the same
// fixed bounty, chest cap and `collected` row.
//
// The window is a SourceActivity with Steps set (startPublicWorksRepair). Its
// Until is the idle deadline, which each step moves on; a window that reaches it
// is given up (completeIfDue), so a closed tab never holds a site a hand could
// take. A committed move gives it up too (commands_move.go), and so does going
// to bed (executePCSleep).
//
// The engine cannot verify skill. StepGap is the least time between two steps,
// so a script can do no better than steps at that rate for a fixed, chest-capped
// bounty — accepted, as accounts are hand-provisioned.

import (
	"errors"
	"fmt"
	"time"
)

// Defaults for the live-tunable PC repair settings. Each sized so the game
// takes about 2–3 minutes; the gap is a floor well under an honest round.
const (
	DefaultPCRepairWellSteps         = 10
	DefaultPCRepairWellStepGapMs     = 4000
	DefaultPCRepairBusinessSteps     = 12
	DefaultPCRepairBusinessStepGapMs = 3000
	DefaultPCRepairRoadSteps         = 10
	DefaultPCRepairRoadStepGapMs     = 4000
	DefaultPCRepairIdleSeconds       = 90
	// DefaultPCRepairHardCoins — a purse this full plays the hardest game.
	DefaultPCRepairHardCoins = 300
)

var (
	// ErrNoRepairSite — the PC is not standing at a damaged site.
	ErrNoRepairSite = errors.New("there is nothing here the town pays to mend.")
	// ErrNoPCRepair — a step came with no repair under way (never started,
	// walked off, given up, or already finished).
	ErrNoPCRepair = errors.New("you are not mending anything.")
	// ErrPCRepairStepTooSoon — a step came sooner than the step gap after the
	// last one (or the start). The step is not counted; the client may send it
	// again after the gap.
	ErrPCRepairStepTooSoon = errors.New("too soon — the work cannot go faster than that.")
)

// pcRepairTerms returns the steps and the least time between two steps for a
// player's repair of a site of kind, each falling back to its default when
// unset.
func (s WorldSettings) pcRepairTerms(kind string) (steps int, gap time.Duration) {
	steps, gapMs := s.PCRepairWellSteps, s.PCRepairWellStepGapMs
	defSteps, defGapMs := DefaultPCRepairWellSteps, DefaultPCRepairWellStepGapMs
	switch kind {
	case PublicWorksBusiness:
		steps, gapMs = s.PCRepairBusinessSteps, s.PCRepairBusinessStepGapMs
		defSteps, defGapMs = DefaultPCRepairBusinessSteps, DefaultPCRepairBusinessStepGapMs
	case PublicWorksRoad:
		steps, gapMs = s.PCRepairRoadSteps, s.PCRepairRoadStepGapMs
		defSteps, defGapMs = DefaultPCRepairRoadSteps, DefaultPCRepairRoadStepGapMs
	case PublicWorksMinor:
		steps, gapMs = s.PCRepairMinorSteps, s.PCRepairMinorStepGapMs
		defSteps, defGapMs = DefaultPCRepairMinorSteps, DefaultPCRepairMinorStepGapMs
	}
	if steps <= 0 {
		steps = defSteps
	}
	if gapMs <= 0 {
		gapMs = defGapMs
	}
	return steps, time.Duration(gapMs) * time.Millisecond
}

// pcRepairIdle is how long a player's repair may go with no step before it is
// given up.
func (s WorldSettings) pcRepairIdle() time.Duration {
	secs := s.PCRepairIdleSeconds
	if secs <= 0 {
		secs = DefaultPCRepairIdleSeconds
	}
	return time.Duration(secs) * time.Second
}

// pcRepairDeadline is the idle deadline a step (or the start) at now sets. Never
// less than two step gaps, so a live retune of the idle time below the gap
// cannot give up a repair before its next step is allowed.
func (s WorldSettings) pcRepairDeadline(now time.Time, gap time.Duration) time.Time {
	return now.Add(max(s.pcRepairIdle(), 2*gap))
}

// pcRepairDifficulty is how hard a player's repair game plays for a purse of
// coins (LLM-712): 0 for an empty purse, rising evenly to 1 at
// PCRepairHardCoins. The client reads it to set the game's speed and margins;
// the engine sets nothing by it.
func (s WorldSettings) pcRepairDifficulty(coins int) float64 {
	if s.PCRepairHardCoins <= 0 || coins <= 0 {
		return 0
	}
	return min(1, float64(coins)/float64(s.PCRepairHardCoins))
}

// isPCRepair reports whether a window is a player's stepped town repair — the
// one predicate the step, the idle give-up and the offer share.
func (act *SourceActivity) isPCRepair() bool {
	return act != nil && act.Kind == SourceActivityRepair && act.PublicWorks && act.Steps > 0
}

// IsHandTownRepair reports whether a window is a hand's town repair — the
// clocked kind that lands at Until. It is the one window the checkpoint keeps
// across a restart (LLM-737): it runs an hour or two, so a deploy that drops it
// costs the whole job. A player's stepped repair is excluded — its Until is an
// idle deadline, and a restart already drops the player's session.
func (act *SourceActivity) IsHandTownRepair() bool {
	return act != nil && act.Kind == SourceActivityRepair && act.PublicWorks && act.Steps == 0
}

// RestoredHandTownRepair rebuilds a hand's town repair from the checkpoint
// (LLM-737). The original Until is kept, so a restart neither adds nor takes
// away work; a window whose Until passed while the engine was down lands on the
// first completion sweep, which re-resolves the site as any landing does.
func RestoredHandTownRepair(objectID VillageObjectID, bounty int, startedAt, until time.Time) *SourceActivity {
	return &SourceActivity{
		Kind:        SourceActivityRepair,
		ObjectID:    objectID,
		StartedAt:   startedAt,
		Until:       until,
		Bounty:      bounty,
		PublicWorks: true,
	}
}

// PCRepairOffer is the town's repair work at one damaged site, as a player sees
// it — the one source for the repair dialog, whether it opens on the arrival
// thought (ObjectConditionNarrated.Offer) or on a click (PCRepairOfferAt).
type PCRepairOffer struct {
	ObjectID VillageObjectID
	SiteKind string // PublicWorksKind — picks the mini-game
	Form     string // a minor work's form (MinorFormFence, …) — picks its mini-game; "" otherwise
	Fact     string // DamageFact: "The windlass at the Well by the Mill is down"
	Bounty   int
	// ChestCanPay — the chest holds the bounty plus the reserve, so the work is
	// on offer. Always true for the player's own repair under way (the bounty
	// was fixed when it started).
	ChestCanPay bool
	// MenderName — someone else is already mending it, so it is not on offer.
	MenderName string
	Steps      int
	StepGap    time.Duration
	// Yours — the player has this repair under way; StepsDone is how far it has
	// gone, so a reloaded client can resume the game.
	Yours     bool
	StepsDone int
	// Difficulty is how hard the game plays, 0..1 (pcRepairDifficulty): the
	// player's purse now, or for their repair under way the one fixed at start.
	Difficulty float64
}

// pcRepairOfferFor builds the offer at site for actor, or nil when site is not
// a damaged site. The terms are the live ones, except for the actor's own
// repair under way, whose bounty and steps were fixed at start.
func pcRepairOfferFor(w *World, actor *Actor, site *VillageObject) *PCRepairOffer {
	if !IsRepairSite(site) {
		return nil
	}
	kind := PublicWorksKind(site)
	bounty, _ := w.Settings.publicWorksTerms(kind)
	steps, gap := w.Settings.pcRepairTerms(kind)
	offer := &PCRepairOffer{
		ObjectID:    site.ID,
		SiteKind:    kind,
		Form:        MinorWorkForm(w.Assets, site),
		Fact:        DamageFact(w.VillageObjects, w.Structures, w.Assets, site),
		Bounty:      bounty,
		ChestCanPay: PublicWorksBountyOpen(w.Environment.TownChest, bounty, w.Settings.PublicWorksChestReserve),
		Steps:       steps,
		StepGap:     gap,
		Difficulty:  w.Settings.pcRepairDifficulty(actor.Coins),
	}
	if act := actor.SourceActivity; act.isPCRepair() && act.ObjectID == site.ID {
		offer.Yours = true
		offer.Bounty = act.Bounty
		offer.ChestCanPay = true
		offer.Steps = act.Steps
		offer.StepGap = act.StepGap
		offer.StepsDone = act.StepsDone
		offer.Difficulty = act.Difficulty
		return offer
	}
	if m := objectRepairer(w, site.ID, actor.ID); m != nil {
		offer.MenderName = actorDisplayNameOrID(m)
	}
	return offer
}

// pcActor resolves a player's actor, refusing a non-player.
func pcActor(w *World, actorID ActorID) (*Actor, error) {
	actor, ok := w.Actors[actorID]
	if !ok {
		return nil, fmt.Errorf("actor %q not in world", actorID)
	}
	if actor.Kind != KindPC {
		return nil, fmt.Errorf("actor %q is not a player", actorID)
	}
	return actor, nil
}

// PCRepairOfferAt returns the repair work at the damaged site the player stands
// at (*PCRepairOffer), or a nil *PCRepairOffer when there is none — the read
// behind a click on a damaged object.
func PCRepairOfferAt(actorID ActorID) Command {
	return Command{Fn: func(w *World) (any, error) {
		actor, err := pcActor(w, actorID)
		if err != nil {
			return nil, err
		}
		return pcRepairOfferFor(w, actor, publicWorksSiteAt(w, actor)), nil
	}}
}

// StartPCRepair starts the player's repair of the damaged site they stand at —
// StartPCRepairAt with no site named.
func StartPCRepair(actorID ActorID, now time.Time) Command {
	return StartPCRepairAt(actorID, "", now)
}

// StartPCRepairAt starts the player's repair of siteID — the site their dialog
// offered — or, with siteID empty, of the damaged site they stand at, through
// the same gates a hand's repair passes (startPublicWorksRepair): not already
// taken, and the chest can pay. A named site the player is not at is refused
// (ErrNoRepairSite), never swapped for another in reach. Returns the player's
// offer (Yours) — the terms the client's game runs on.
func StartPCRepairAt(actorID ActorID, siteID VillageObjectID, now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		actor, err := pcActor(w, actorID)
		if err != nil {
			return nil, err
		}
		if actor.MoveIntent != nil {
			return nil, errors.New("you are walking — arrive before you start the mending.")
		}
		completeIfDue(w, actorID, actor, now)
		if actor.SourceActivity != nil {
			return nil, errors.New("you are already busy — finish what you're doing first.")
		}
		site := publicWorksSiteAt(w, actor)
		if siteID != "" {
			site = w.VillageObjects[siteID]
			if !atPublicWorksSite(w, actor, site) {
				site = nil
			}
		}
		if site == nil {
			return nil, ErrNoRepairSite
		}
		if _, err := startPublicWorksRepair(w, actor, site, now); err != nil {
			return nil, err
		}
		return pcRepairOfferFor(w, actor, site), nil
	}}
}

// PCRepairStepResult is a step's outcome. Done is true on the last step; Landed
// then says whether the site was mended and paid for (false when it was mended
// some other way while the player worked), and Paid is the coin received.
type PCRepairStepResult struct {
	StepsDone int
	Steps     int
	Done      bool
	Landed    bool
	Paid      int
}

// StepPCRepair counts one mini-game round of the player's repair. A step
// sooner than the step gap after the last one is refused and not counted
// (ErrPCRepairStepTooSoon). The last step lands the repair (landPublicWorksRepair).
func StepPCRepair(actorID ActorID, now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		actor, err := pcActor(w, actorID)
		if err != nil {
			return nil, err
		}
		// A window past its idle deadline is given up here, before the step.
		completeIfDue(w, actorID, actor, now)
		act := actor.SourceActivity
		if !act.isPCRepair() {
			return nil, ErrNoPCRepair
		}
		last := act.LastStepAt
		if last.IsZero() {
			last = act.StartedAt
		}
		if now.Sub(last) < act.StepGap {
			return nil, ErrPCRepairStepTooSoon
		}
		act.StepsDone++
		act.LastStepAt = now
		act.Until = w.Settings.pcRepairDeadline(now, act.StepGap)
		res := PCRepairStepResult{StepsDone: act.StepsDone, Steps: act.Steps}
		if act.StepsDone < act.Steps {
			return res, nil
		}
		actor.SourceActivity = nil
		res.Done = true
		res.Paid, res.Landed = landPublicWorksRepair(w, actorID, actor, act, now)
		return res, nil
	}}
}

// PCRepairNarrated is the player's own line when their town repair lands
// (LLM-690) — PublicWorksCompletionNarration, the line a hand's completion beat
// carries. TranslateEvent maps it to a private room_event; ObjectID rides as the
// frame's structure_id, as on ObjectConditionNarrated.
type PCRepairNarrated struct {
	EventBase
	ActorID  ActorID
	ObjectID VillageObjectID
	Text     string
	Paid     int
	At       time.Time
}

func (PCRepairNarrated) isSimEvent() {}
