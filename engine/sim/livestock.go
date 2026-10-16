package sim

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// livestock.go — LLM-797. Milk comes from the cows: a milking animal is a berry
// bush that walks. It carries the same finite gather row a bush does (an
// ObjectRefresh with GatherItem set, Amount 0 — yield-only), the same regen tick
// refills it, and the same yield-only rule decides who may take from it (a
// `forage` restock entry for the item; animals are nobody's property). What
// differs is only that the source moves, so reach is measured to the animal's
// current tile instead of a loiter pin, and the animal stands still while it is
// being milked or while its milker walks over to it.
//
// Feeding: an animal fed LivestockFeedItem gives LivestockFeedBonus extra units
// on its next refill (Jeff, 2026-10-16). An NPC feeds automatically at the end of
// a milking when it holds the feed; a player feeds by clicking the animal
// (POST /pc/feed). Either way FeedLivestock lands it and emits LivestockFed, which
// the client answers with a moo.

const (
	// LivestockFeedItem is what an animal is fed to raise its yield.
	LivestockFeedItem ItemKind = "sage"
	// LivestockFeedBonus is the extra units a fed animal's next refill adds on top
	// of its MaxQuantity. A first guess (the old milk recipe's sage bonus).
	LivestockFeedBonus = 4
	// MilkingReach is how close (Chebyshev tiles) a milker or feeder must stand to
	// the animal. 1 = beside it.
	MilkingReach = 1
	// MilkActivityDuration is the milking window — longer than picking a bush.
	MilkActivityDuration = 8 * time.Second
)

var (
	// ErrNotLivestock — the animal carries no yield row (a heifer, a sheep).
	ErrNotLivestock = errors.New("that animal gives nothing to take")
	// ErrAlreadyFed — the animal's next refill is already boosted. Only a
	// player meets it (an NPC milker checks first); the text is what they read,
	// after the animal's name: "Cow 2 doesn't seem hungry".
	ErrAlreadyFed = errors.New("doesn't seem hungry")
	// ErrNoFeed — the feeder holds none of LivestockFeedItem.
	ErrNoFeed = errors.New("you have nothing to feed her")
	// ErrLivestockOutOfReach — the feeder is not beside the animal.
	ErrLivestockOutOfReach = errors.New("you are not close enough — stand beside her first")
)

// SourceActivityMilk is a milker drawing milk from an animal (LLM-797): the
// harvest window with an animal, not a village object, as its source.
const SourceActivityMilk SourceActivityKind = "milk"

// LivestockYield is the gather row a milking animal carries (Actor.Yield). Row is
// exactly a bush's row; FedBonus is the units its next refill adds on top of
// MaxQuantity, set by feeding and cleared by that refill. Persisted on the actor
// row (actor.livestock_yield) so a restart keeps both the stock and the bonus —
// the feed is spent the moment it is given.
type LivestockYield struct {
	Row      ObjectRefresh
	FedBonus int
}

// HasStock reports whether the animal has anything to give.
func (y *LivestockYield) HasStock() bool {
	return y != nil && y.Row.HasStock()
}

// Fed reports whether the animal's next refill is already boosted.
func (y *LivestockYield) Fed() bool {
	return y != nil && y.FedBonus > 0
}

// Item is the yield's item kind, trimmed the way IsGatherable reads it.
func (y *LivestockYield) Item() ItemKind {
	if y == nil {
		return ""
	}
	return ItemKind(strings.TrimSpace(string(y.Row.GatherItem)))
}

// cloneLivestockYield deep-copies y (its scalar pointers included) so a snapshot
// never aliases the live row.
func cloneLivestockYield(y *LivestockYield) *LivestockYield {
	if y == nil {
		return nil
	}
	cp := *y
	cp.Row = *cloneObjectRefresh(&y.Row)
	return &cp
}

// ValidateLivestockYield checks a yield row loaded from storage or set by an
// operator: a finite, regenerating, yield-only gather row and a non-negative
// bonus.
func ValidateLivestockYield(y *LivestockYield) error {
	if y == nil {
		return nil
	}
	r := &y.Row
	if !r.IsGatherable() {
		return fmt.Errorf("%w: livestock yield needs a gather_item", ErrInvalidRefresh)
	}
	if r.Amount != 0 {
		return fmt.Errorf("%w: livestock yield is yield-only (amount 0)", ErrInvalidRefresh)
	}
	if r.AvailableQuantity == nil || r.MaxQuantity == nil || *r.MaxQuantity <= 0 || *r.AvailableQuantity < 0 {
		return fmt.Errorf("%w: livestock yield needs available >= 0 and max > 0", ErrInvalidRefresh)
	}
	if r.RefreshPeriodHours == nil || *r.RefreshPeriodHours <= 0 {
		return fmt.Errorf("%w: livestock yield needs a refresh period", ErrInvalidRefresh)
	}
	if r.RefreshMode != RefreshModePeriodic && r.RefreshMode != RefreshModeContinuous {
		return fmt.Errorf("%w: livestock yield has unknown refresh mode %q", ErrInvalidRefresh, r.RefreshMode)
	}
	if y.FedBonus < 0 {
		return fmt.Errorf("%w: livestock fed bonus must be >= 0", ErrInvalidRefresh)
	}
	return nil
}

// regenLivestockYield applies one regen step to an animal's row, then lands a
// pending feed bonus on the refill that brought the row back to full. Reports
// whether the stock changed.
func regenLivestockYield(y *LivestockYield, now time.Time) bool {
	if y == nil {
		return false
	}
	r := &y.Row
	wasBelowMax := r.AvailableQuantity != nil && r.MaxQuantity != nil && *r.AvailableQuantity < *r.MaxQuantity
	changed := regenRefreshRow(r, now)
	if changed && wasBelowMax && y.FedBonus > 0 && *r.AvailableQuantity >= *r.MaxQuantity {
		next := *r.AvailableQuantity + y.FedBonus
		r.AvailableQuantity = &next
		y.FedBonus = 0
	}
	return changed
}

// LivestockCandidate is one animal a milker might take from, in the shape both
// the world (commands) and the snapshot (perception) can build, so the cue and
// the command pick the same animal.
type LivestockCandidate struct {
	ID    ActorID
	Name  string
	Pos   TilePos
	Yield *LivestockYield
}

// PickLivestockSource chooses the animal within MilkingReach of at that the actor
// may take from: a yield it holds a forage entry for (animals are unowned, so the
// yield-only rule is the whole permission). Prefers the animal it walked to
// (targetID) when that one has stock, then one with stock, then the nearer, then
// the lower id. ok=false when no permitted animal is in reach. The returned
// candidate may have no stock; the caller decides whether that is an error
// (command) or silence (cue).
func PickLivestockSource(cands []LivestockCandidate, at TilePos, targetID ActorID, foragable map[ItemKind]bool) (LivestockCandidate, bool) {
	var best LivestockCandidate
	found := false
	bestCheb := 0
	for _, c := range cands {
		if c.Yield == nil || !foragable[c.Yield.Item()] {
			continue
		}
		cheb := at.Chebyshev(c.Pos)
		if cheb > MilkingReach {
			continue
		}
		if !found || betterLivestock(c, cheb, best, bestCheb, targetID) {
			best, bestCheb, found = c, cheb, true
		}
	}
	return best, found
}

func betterLivestock(a LivestockCandidate, aCheb int, b LivestockCandidate, bCheb int, targetID ActorID) bool {
	aTarget := targetID != "" && a.ID == targetID && a.Yield.HasStock()
	bTarget := targetID != "" && b.ID == targetID && b.Yield.HasStock()
	if aTarget != bTarget {
		return aTarget
	}
	if a.Yield.HasStock() != b.Yield.HasStock() {
		return a.Yield.HasStock()
	}
	if aCheb != bCheb {
		return aCheb < bCheb
	}
	return a.ID < b.ID
}

// worldLivestock lists every live animal carrying a yield row.
// MUST be called from inside a Command.Fn.
func worldLivestock(w *World) []LivestockCandidate {
	var out []LivestockCandidate
	for id, a := range w.Actors {
		if a == nil || a.Yield == nil {
			continue
		}
		out = append(out, LivestockCandidate{ID: id, Name: a.DisplayName, Pos: a.Pos, Yield: a.Yield})
	}
	return out
}

// SnapshotLivestock is worldLivestock over a published snapshot.
func SnapshotLivestock(snap *Snapshot) []LivestockCandidate {
	if snap == nil {
		return nil
	}
	var out []LivestockCandidate
	for id, a := range snap.Actors {
		if a == nil || a.Yield == nil {
			continue
		}
		out = append(out, LivestockCandidate{ID: id, Name: a.DisplayName, Pos: a.Pos, Yield: a.Yield})
	}
	return out
}

// herdGives reports whether any animal gives item; herdHasStock whether any has
// some to give now. The forage wake's herd ground (forageGroundFor).
// MUST be called from inside a Command.Fn.
func herdGives(w *World, item ItemKind) bool {
	for _, a := range w.Actors {
		if a != nil && a.Yield != nil && a.Yield.Item() == item {
			return true
		}
	}
	return false
}

func herdHasStock(w *World, item ItemKind) bool {
	for _, a := range w.Actors {
		if a != nil && a.Yield.HasStock() && a.Yield.Item() == item {
			return true
		}
	}
	return false
}

// findLivestockNear resolves the animal actor may milk from where it stands.
// MUST be called from inside a Command.Fn.
func findLivestockNear(w *World, actor *Actor) (*Actor, bool) {
	c, ok := PickLivestockSource(worldLivestock(w), actor.Pos, actor.HerdTargetID, ForageItems(actor.RestockPolicy))
	if !ok {
		return nil, false
	}
	return w.Actors[c.ID], true
}

// startMilking opens a milking window at the animal beside actor, or returns
// ErrNoGatherSource when none it may milk is in reach. Called by StartHarvest
// when no village object source is in reach, so the gather tool covers both.
// MUST be called from inside a Command.Fn, after the caller's busy/walking gates.
func startMilking(w *World, actorID ActorID, actor *Actor, now time.Time) (SourceActivityStartResult, error) {
	animal, ok := findLivestockNear(w, actor)
	if !ok {
		return SourceActivityStartResult{}, fmt.Errorf("StartHarvest: %w", ErrNoGatherSource)
	}
	if _, ok := resolveItemKind(w, string(animal.Yield.Item())); !ok {
		return SourceActivityStartResult{}, fmt.Errorf("StartHarvest: %w %q (animal %s yield)", ErrUnknownItemKind, animal.Yield.Item(), animal.ID)
	}
	if !animal.Yield.HasStock() {
		return SourceActivityStartResult{}, fmt.Errorf("StartHarvest: %s has no %s to give just now — she fills again in time", animal.DisplayName, animal.Yield.Item())
	}
	// The animal must not walk off mid-milking: halt a leg in progress now; the
	// grazer tick leaves her be while the window is open (livestockHeld).
	if animal.MoveIntent != nil {
		_, _ = StopMove(animal.ID, now).Fn(w)
	}
	actor.HerdTargetID = animal.ID
	actor.SourceActivity = &SourceActivity{
		Kind:      SourceActivityMilk,
		AnimalID:  animal.ID,
		StartedAt: now,
		Until:     now.Add(MilkActivityDuration),
		Qty:       *animal.Yield.Row.AvailableQuantity,
	}
	w.emit(&SourceActivityStarted{
		ActorID:  actorID,
		AnimalID: animal.ID,
		Kind:     SourceActivityMilk,
		Until:    actor.SourceActivity.Until,
		At:       now,
	})
	return SourceActivityStartResult{
		Started:    true,
		Kind:       SourceActivityMilk,
		SourceName: animal.DisplayName,
		Until:      actor.SourceActivity.Until,
	}, nil
}

// completeMilking lands a finished milking window: the milk moves from the animal
// to the milker, and a milker holding the feed feeds an unfed animal on the spot.
// Bound to the animal the window began at, which must still be in reach. Reports
// whether SourceActivityCompleted was emitted.
// MUST be called from inside a Command.Fn.
func completeMilking(w *World, actorID ActorID, actor *Actor, act *SourceActivity, now time.Time) bool {
	// The visit is over whatever lands: the animal is free to graze again.
	actor.HerdTargetID = ""
	actor.HerdHoldUntil = time.Time{}
	animal := w.Actors[act.AnimalID]
	if animal == nil || animal.Yield == nil || actor.Pos.Chebyshev(animal.Pos) > MilkingReach {
		return false
	}
	if !ForageItems(actor.RestockPolicy)[animal.Yield.Item()] {
		return false
	}
	kind, ok := resolveItemKind(w, string(animal.Yield.Item()))
	if !ok {
		return false
	}
	row := &animal.Yield.Row
	avail := *row.AvailableQuantity
	if avail <= 0 {
		return false
	}
	qty := act.Qty
	if qty <= 0 || qty > avail {
		qty = avail
	}
	cur := actor.Inventory[kind]
	if cur > math.MaxInt-qty {
		return false
	}
	drawDownStock(row, qty, now)
	if actor.Inventory == nil {
		actor.Inventory = make(map[ItemKind]int)
	}
	actor.Inventory[kind] = cur + qty
	w.emit(&ItemGathered{
		ActorID:  actorID,
		AnimalID: animal.ID,
		Item:     kind,
		Qty:      qty,
		At:       now,
	})
	// Feeding at the pail is a body habit, not a decision (brain/body model): an
	// NPC holding the feed gives it to an unfed animal without a tool or a turn.
	fed := false
	if actor.Kind != KindPC && actor.Inventory[LivestockFeedItem] > 0 && !animal.Yield.Fed() {
		fed = feedLivestock(w, actor, animal, now) == nil
	}
	w.emit(&SourceActivityCompleted{
		ActorID:    actorID,
		AnimalID:   animal.ID,
		Kind:       SourceActivityMilk,
		Item:       kind,
		Qty:        qty,
		SourceName: animal.DisplayName,
		Fed:        fed,
		At:         now,
	})
	return true
}

// LivestockFed is emitted when an animal is fed. Wire-only (the client moos);
// no engine subscriber.
type LivestockFed struct {
	EventBase
	AnimalID ActorID
	FeederID ActorID
	At       time.Time
}

func (LivestockFed) isSimEvent() {}

// LivestockFedNarration is the milker's line for the feed given at the end of a
// milking.
func LivestockFedNarration(animalName string) string {
	if animalName == "" {
		animalName = "her"
	}
	return fmt.Sprintf("You give %s a handful of %s.", animalName, LivestockFeedItem)
}

// FeedResult is FeedLivestock's reply.
type FeedResult struct {
	AnimalName string
}

// FeedLivestock feeds animalID one LivestockFeedItem from feederID's pack. Anyone
// holding the feed may feed (it only helps the herd); milking stays limited to
// the yield-only rule.
func FeedLivestock(feederID, animalID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			feeder := w.Actors[feederID]
			if feeder == nil {
				return nil, fmt.Errorf("FeedLivestock: actor %q not in world", feederID)
			}
			animal := w.Actors[animalID]
			if animal == nil {
				return nil, fmt.Errorf("FeedLivestock: animal %q not in world", animalID)
			}
			if err := feedLivestock(w, feeder, animal, now); err != nil {
				return nil, err
			}
			return FeedResult{AnimalName: animal.DisplayName}, nil
		},
	}
}

// feedLivestock validates and lands one feed. MUST be called from inside a
// Command.Fn.
func feedLivestock(w *World, feeder, animal *Actor, now time.Time) error {
	if animal.Yield == nil {
		return fmt.Errorf("%s: %w", animal.DisplayName, ErrNotLivestock)
	}
	if animal.Yield.Fed() {
		return fmt.Errorf("%s %w", animal.DisplayName, ErrAlreadyFed)
	}
	if feeder.Inventory[LivestockFeedItem] <= 0 {
		return ErrNoFeed
	}
	if feeder.Pos.Chebyshev(animal.Pos) > MilkingReach {
		return ErrLivestockOutOfReach
	}
	feeder.Inventory[LivestockFeedItem]--
	if feeder.Inventory[LivestockFeedItem] <= 0 {
		delete(feeder.Inventory, LivestockFeedItem)
	}
	animal.Yield.FedBonus = LivestockFeedBonus
	w.emit(&LivestockFed{AnimalID: animal.ID, FeederID: feeder.ID, At: now})
	return nil
}

// HerdArrivalHold is how long an animal waits for the actor who walked over to
// her (move_to, or a player's walk to feed) once that actor arrives — time for
// the arrival turn to start the milking or the feed. After it she grazes again,
// so an actor who arrives and does nothing cannot pin her (code_review).
const HerdArrivalHold = 2 * time.Minute

// livestockHeld reports whether an animal must stand still: someone is milking
// her, is walking over to her (HerdTargetID with a walk in flight), or arrived
// beside her less than HerdArrivalHold ago (HerdHoldUntil, stamped on arrival).
// Committing any other move clears HerdTargetID, so a milker who walks away
// frees her at once.
// MUST be called from inside a Command.Fn.
func livestockHeld(w *World, animal *Actor, now time.Time) bool {
	if animal == nil || animal.Yield == nil {
		return false
	}
	for _, a := range w.Actors {
		if a == nil || a.ID == animal.ID {
			continue
		}
		if act := a.SourceActivity; act != nil && act.AnimalID == animal.ID {
			return true
		}
		if a.HerdTargetID != animal.ID {
			continue
		}
		if a.MoveIntent != nil {
			return true
		}
		if now.Before(a.HerdHoldUntil) && a.Pos.Chebyshev(animal.Pos) <= MilkingReach+1 {
			return true
		}
	}
	return false
}

// handleHerdTargetOnArrival starts the arrival hold for an actor who walked to
// an animal (HerdTargetID is still set: committing the walk was the last move).
// Every actor kind — a player walking over to feed is held for too.
func handleHerdTargetOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	if a := w.Actors[arr.ActorID]; a != nil && a.HerdTargetID != "" {
		a.HerdHoldUntil = arr.At.Add(HerdArrivalHold)
	}
}

// ErrNotLivestockDestination — the id names no animal that gives anything.
var ErrNotLivestockDestination = errors.New("that is not an animal you can walk over to")

// MoveToLivestock walks actorID to a tile beside the animal (the player's walk
// to feed a cow, LLM-797) — the same walk, and the same hold on the animal, as
// an NPC's move_to by her name.
func MoveToLivestock(actorID, animalID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if w.Actors[actorID] == nil {
				return MoveActorResult{}, fmt.Errorf("MoveToLivestock: actor %q not in world", actorID)
			}
			animal := w.Actors[animalID]
			if animal == nil || animal.Yield == nil {
				return MoveActorResult{}, ErrNotLivestockDestination
			}
			return moveToLivestockLabeled(w, actorID, animal, "", now)
		},
	}
}

// moveToLivestockLabeled walks actorID to a walkable tile beside the animal and
// stamps it as the actor's herd target, so the animal waits for her.
// MUST be called from inside a Command.Fn (used by MoveToStructureByName and
// MoveToLivestock).
func moveToLivestockLabeled(w *World, actorID ActorID, animal *Actor, spokenAs string, now time.Time) (any, error) {
	a := w.Actors[actorID]
	if a.Pos.Chebyshev(animal.Pos) <= MilkingReach {
		a.HerdTargetID = animal.ID
		a.HerdHoldUntil = now.Add(HerdArrivalHold)
		return MoveActorResult{}, TerminalNoOpError{Msg: fmt.Sprintf(
			"you are already beside %q — you're right where you meant to be.", moveToLabel(spokenAs, animal.DisplayName))}
	}
	grid, err := buildWalkGrid(w)
	if err != nil {
		return MoveActorResult{}, fmt.Errorf("move_to: %w", err)
	}
	dest, ok := tileBesideLivestock(grid, animal.Pos, a.Pos)
	if !ok {
		return MoveActorResult{}, fmt.Errorf("there is no way to reach %q just now — try again in a moment", moveToLabel(spokenAs, animal.DisplayName))
	}
	res, err := MoveActor(actorID, NewPositionDestination(dest), true, now).Fn(w)
	if err != nil {
		return res, err
	}
	a.HerdTargetID = animal.ID
	return res, nil
}

// tileBesideLivestock picks the walkable tile within MilkingReach of the animal
// for the milker to stand on: level with her (left or right) before above or
// below — the milking pose is drawn side-on — then nearest the walker, then the
// lower y and x for determinism.
func tileBesideLivestock(grid *WalkGrid, animalPos, from TilePos) (TilePos, bool) {
	var best TilePos
	found := false
	bestSide, bestDist := false, 0
	for dy := -MilkingReach; dy <= MilkingReach; dy++ {
		for dx := -MilkingReach; dx <= MilkingReach; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			t := TilePos{X: animalPos.X + dx, Y: animalPos.Y + dy}
			if !grid.CanWalk(t.X, t.Y) {
				continue
			}
			side := dy == 0
			d := t.Chebyshev(from)
			better := !found ||
				(side && !bestSide) ||
				(side == bestSide && (d < bestDist || (d == bestDist && (t.Y < best.Y || (t.Y == best.Y && t.X < best.X)))))
			if better {
				best, bestSide, bestDist, found = t, side, d, true
			}
		}
	}
	return best, found
}

// resolveLivestockByName finds the animal carrying a yield whose display name
// matches name (article-stripped, case-folded — placeNameMatches), lowest id on a
// tie. Only a yielding animal is a destination: a heifer is not somewhere to go.
// MUST be called from inside a Command.Fn.
func resolveLivestockByName(w *World, name string) (*Actor, bool) {
	var best *Actor
	for _, a := range w.Actors {
		if a == nil || a.Yield == nil || !placeNameMatches(a.DisplayName, name) {
			continue
		}
		if best == nil || a.ID < best.ID {
			best = a
		}
	}
	return best, best != nil
}
