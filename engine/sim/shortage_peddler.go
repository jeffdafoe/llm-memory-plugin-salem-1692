package sim

import (
	"log"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// shortage_peddler.go — LLM-656. A keeper whose own product is short a required
// input that NO village supplier holds is silent to himself: `## Your trade`
// drops the input-short good (LLM-324), `## Restocking` names only suppliers
// with stock (LLM-216) and the runway line follows it (LLM-260). Live, John Ellis
// sat one cut of meat short of a stew batch for six weeks while Elizabeth made
// dairy, and no cue on either side could move it — the producer's demand read is
// sales, not asks, so a request she cannot fill never registers.
//
// This file is the out-of-town answer: the daily rotation sweeps every resident
// keeper for such a shortage (inputShortagesNow), remembers each one across
// game-days (WorldEnvironment.InputShortages, durable — the village restarts
// several times a day for deploys, so an in-memory count would rarely reach
// its threshold), and once a shortage has stood ShortagePeddlerDays game-days
// the visitor cascade brings in a PEDDLER: the wholesale factor's sell errand
// with two things changed — his pack is the missing good alone, sized to a
// couple of batches, and his counterparty is the short keeper's own shop, not
// the distributor. Everything downstream (arrival target, the `## Your rounds`
// steer, the keeper's `## A trader's come to deal` cue, errand confinement,
// shipment settle, dusk wind-down, daybreak departure) is the merchant-visitor
// machinery unchanged.
//
// Deliberately NOT built here: a line for the keeper naming the dry producer
// (walking there changes nothing — her production choice reads sales), and
// counting unfilled asks as producer demand (its own ticket).

const (
	// DefaultShortagePeddlerDays is how many consecutive daily sweeps must find
	// the same shortage before a peddler is sent, and the cooldown between
	// peddlers for one shortage. 0 is the off-switch (the farm-upkeep
	// convention). Three days separates a keeper who sold out last night from
	// one the village genuinely cannot supply.
	DefaultShortagePeddlerDays = 3
	// DefaultShortagePeddlerBatches sizes the peddler's pack: this many batches'
	// worth of the missing input at the keeper's own recipe quantity. Two lets
	// the keeper make one batch and hold the makings for the next.
	DefaultShortagePeddlerBatches = 2
)

// InputShortage is one keeper's standing lack of a required input for a good he
// makes, which no village supplier holds. Days counts the consecutive daily
// sweeps that found it (a sweep that does not find it drops the entry, so
// presence implies consecutive). LastSeenAt is the rotation boundary of the last
// sweep that found it — the idempotence guard. LastPeddlerAt is when a peddler
// was last sent for it (zero = never), the per-shortage cooldown.
type InputShortage struct {
	KeeperID      ActorID   `json:"keeper_id"`
	Item          ItemKind  `json:"item"`
	Days          int       `json:"days"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	LastPeddlerAt time.Time `json:"last_peddler_at"`
}

// shortageKey identifies a shortage independent of its counters.
type shortageKey struct {
	keeper ActorID
	item   ItemKind
}

// shortageKeeper reports whether a is a resident keeper the sweep assesses: an
// NPC (never a PC, a decorative, or a visitor) with a post, a restock policy to
// read entries from, and a claim on that post — its businessowner, or the owner
// of the business it stands at (LLM-657). The second form is the wright's: Lewis
// Walker owns and works his workshop (the LLM-648 ActorIsWright posture, keyed
// on OwnerActorID) but carries only a worker attribute, so BusinessownerState
// is nil for him — and his whetstone line is the buy line this sweep exists to
// notice. Everything downstream binds to him by id, so this is the one place
// keeper-ness is decided for the peddler.
func shortageKeeper(w *World, a *Actor) bool {
	if a == nil || a.VisitorState != nil {
		return false
	}
	if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
		return false
	}
	if a.WorkStructureID == "" || a.RestockPolicy == nil {
		return false
	}
	if a.BusinessownerState != nil {
		return true
	}
	obj := w.VillageObjects[VillageObjectID(a.WorkStructureID)]
	return obj != nil && obj.HasTag(TagBusiness) && obj.OwnerActorID == a.ID
}

// villageSupplierHolds reports whether any resident supplier of record for item
// — a producer or forager of it, or the village distributor — holds at least one
// unit. The same supplier notion `## Restocking` reads (perception
// isRestockSupplierOf, LLM-252): a fellow reseller's retail stock does not
// count, or a keeper down to his last carrot would read as the village's carrot
// supply. Visitors never count — a peddler standing in the village is the
// answer to a shortage, not evidence there is none.
func villageSupplierHolds(w *World, item ItemKind) bool {
	for _, v := range w.Actors {
		if v == nil || v.VisitorState != nil || v.Kind == KindPC || v.Kind == KindDecorative {
			continue
		}
		if v.Inventory[item] <= 0 {
			continue
		}
		// An ordinary resident with no policy at all can still carry the item
		// (code_review): the nil-receiver-safe method is called through an
		// explicit guard so the sweep never dereferences a policy that isn't there.
		producesOrForages := v.RestockPolicy != nil && v.RestockPolicy.ProducesOrForages(item)
		if producesOrForages || ActorIsDistributor(w.VillageObjects, v.WorkStructureID) {
			return true
		}
	}
	return false
}

// inputShortagesNow lists every (keeper, good) the village cannot supply him:
//
//   - a recipe input — the keeper makes a good that has room for another batch
//     and lacks a required input for one batch (LLM-656);
//   - a buy line he holds none of (LLM-657) — the wright's whetstone, consumed
//     by a service rather than a recipe, and any other bought good a trade runs
//     on. The distributor's buy lines are skipped: they are the wholesale
//     import channel the factor cascade already serves, and villageSupplierHolds
//     counts him as the village's supplier of record, so a peddler bound to him
//     would be a factor by another name.
//
// Both forms require that the keeper does not make or gather the good himself,
// that it is a carriable good (not a service or an eat-here-only kind), and
// that no village supplier holds any. This is precisely the situation the
// keeper's own cues go silent on (LLM-324 drops the good, LLM-216/LLM-260 drop
// the input, the wright's rounds say "buy one" with nowhere to buy), so nothing
// in the village can act on it. Sorted by (keeper, item), deduplicated across a
// keeper's recipes and lines (a tavern's meat buy line and its stew input are
// one shortage).
func inputShortagesNow(w *World) []shortageKey {
	seen := map[shortageKey]struct{}{}
	var keys []shortageKey
	for _, a := range w.Actors {
		if !shortageKeeper(w, a) {
			continue
		}
		// short records (keeper, item) once the gates the two forms share pass.
		short := func(item ItemKind) {
			if a.RestockPolicy.ProducesOrForages(item) {
				return // his own to make or gather (the LLM-614/616 posture)
			}
			if !KindBarterable(w.ItemKinds[item]) {
				return
			}
			if villageSupplierHolds(w, item) {
				return
			}
			k := shortageKey{keeper: a.ID, item: item}
			if _, dup := seen[k]; dup {
				return
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
		for _, e := range a.RestockPolicy.ProduceEntries() {
			if !makeableRecipe(w, e.Item) {
				continue
			}
			recipe := w.Recipes[e.Item]
			if !batchFitsCap(a.Inventory[e.Item], e.Cap(), recipeBatchQty(recipe)) {
				continue // full shelves — the shortage costs him nothing today
			}
			for _, in := range recipe.Inputs {
				if in.Qty <= 0 || a.Inventory[in.Item] >= in.Qty {
					continue
				}
				short(in.Item)
			}
		}
		if ActorIsDistributor(w.VillageObjects, a.WorkStructureID) {
			continue
		}
		for _, e := range a.RestockPolicy.BuyEntries() {
			if e.Item == "" || a.Inventory[e.Item] > 0 {
				continue // "short" of a bought good is holding none of it
			}
			short(e.Item)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].keeper != keys[j].keeper {
			return keys[i].keeper < keys[j].keeper
		}
		return keys[i].item < keys[j].item
	})
	return keys
}

// SweepInputShortages is the once-a-game-day command (checkAndRotate, beside the
// farm upkeep and the constable's wage) that re-reads the village's input
// shortages and advances the day count of each one that persists. Keyed to the
// rotation boundary: a re-run on the same boundary (a restart mid-day) neither
// double-counts nor drops. A shortage the sweep no longer finds is forgotten —
// a peddler who sold, a producer who made some, a keeper who bought elsewhere
// all clear it the same way.
func SweepInputShortages(boundary time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		return sweepInputShortages(w, boundary), nil
	}}
}

func sweepInputShortages(w *World, boundary time.Time) []InputShortage {
	prev := make(map[shortageKey]InputShortage, len(w.Environment.InputShortages))
	for _, s := range w.Environment.InputShortages {
		prev[shortageKey{keeper: s.KeeperID, item: s.Item}] = s
	}
	var next []InputShortage
	var names []string
	for _, k := range inputShortagesNow(w) {
		s, seen := prev[k]
		switch {
		case !seen:
			s = InputShortage{KeeperID: k.keeper, Item: k.item, Days: 1, LastSeenAt: boundary}
		case s.LastSeenAt.Before(boundary):
			s.Days++
			s.LastSeenAt = boundary
		}
		next = append(next, s)
		names = append(names, shortageLabel(w, s))
	}
	w.Environment.InputShortages = next
	if len(next) > 0 {
		log.Printf("sim/shortage_peddler: sweep at %s — %d standing shortage(s): %s",
			boundary.Format(time.RFC3339), len(next), strings.Join(names, "; "))
	}
	return next
}

// shortageLabel is the log form of a shortage: "John Ellis short meat (day 3)".
func shortageLabel(w *World, s InputShortage) string {
	who := string(s.KeeperID)
	if a := w.Actors[s.KeeperID]; a != nil && a.DisplayName != "" {
		who = a.DisplayName
	}
	return who + " short " + string(s.Item) + " (day " + strconv.Itoa(s.Days) + ")"
}

// dueShortagePeddler picks the shortage a peddler should be sent for right now,
// if any: the first (in (keeper, item) order) that has stood at least
// ShortagePeddlerDays sweeps, STILL stands this instant, has not had a peddler
// within that many days, and whose keeper is at his post so the peddler has
// someone to deal with on arrival (the bindBuyErrand posture — a shut shop is
// tried again next tick).
//
// The record is a midnight snapshot; hours pass before the afternoon spawn
// window, and in between the keeper may have bought the input, a producer may
// have made some, or the shelves may have filled (code_review). So the stored
// entries are re-read against inputShortagesNow here, and an entry the village
// has since resolved is dropped on the spot rather than left to send a peddler
// for a lack that no longer exists — the keeper's cue would then claim a want
// the village does not have. Returns the index into (the pruned)
// WorldEnvironment.InputShortages, for the LastPeddlerAt stamp once the spawn
// commits, and the bound errand. ok=false when the feature is off or nothing is
// due. Runs on the world goroutine.
func dueShortagePeddler(w *World, now time.Time) (int, *TradeErrand, bool) {
	days := w.Settings.ShortagePeddlerDays
	if days <= 0 {
		return -1, nil, false
	}
	if len(w.Environment.InputShortages) == 0 {
		return -1, nil, false
	}
	standing := map[shortageKey]struct{}{}
	for _, k := range inputShortagesNow(w) {
		standing[k] = struct{}{}
	}
	kept := w.Environment.InputShortages[:0]
	for _, s := range w.Environment.InputShortages {
		if _, ok := standing[shortageKey{keeper: s.KeeperID, item: s.Item}]; ok {
			kept = append(kept, s)
		} else {
			log.Printf("sim/shortage_peddler: %s resolved since the sweep; dropped", shortageLabel(w, s))
		}
	}
	w.Environment.InputShortages = kept

	cooldown := time.Duration(days) * 24 * time.Hour
	for i, s := range kept {
		if s.Days < days {
			continue
		}
		if !s.LastPeddlerAt.IsZero() && now.Sub(s.LastPeddlerAt) < cooldown {
			continue
		}
		// The carter supersedes the peddler when the village already holds a
		// batch of the good (carter.go): the peddler is the outside-supply
		// mechanism, and importing what sits three buildings away is the
		// defect the carter exists for. dueCarter runs ahead of this in the
		// spawn and takes the run; here the entry simply waits for him.
		if carterCoversShortage(w, s, now) {
			log.Printf("sim/shortage_peddler: %s — the village holds the goods; the carter's run", shortageLabel(w, s))
			continue
		}
		errand, ok := bindShortageErrand(w, s)
		if !ok {
			continue
		}
		return i, errand, true
	}
	return -1, nil, false
}

// bindShortageErrand binds the peddler's sell errand for one shortage: Good =
// the missing input, Counterparty = the short keeper's own shop, Keeper = the
// short keeper himself — the shipment is for a person, not a building, and a
// structure two keepers share must not hand it to the other one (code_review).
// ok=false when the keeper is gone, has no structure-backed post, or is not at
// it now.
func bindShortageErrand(w *World, s InputShortage) (*TradeErrand, bool) {
	keeper := w.Actors[s.KeeperID]
	if !shortageKeeper(w, keeper) {
		return nil, false
	}
	if !structureIDValid(w, VillageObjectID(keeper.WorkStructureID)) {
		return nil, false
	}
	if !keeperPresentAt(w, keeper.WorkStructureID) {
		return nil, false
	}
	return &TradeErrand{
		Direction:    TradeDirectionSell,
		Good:         s.Item,
		Counterparty: keeper.WorkStructureID,
		Keeper:       keeper.ID,
		Peddler:      true,
	}, true
}

// peddlerShipmentQty sizes the peddler's pack: batches × the largest per-batch
// quantity of item among the keeper's own recipes, floored at one unit per
// batch. Read off the keeper's recipes rather than a fixed knob so a good that
// takes two per batch (meat for stew) and one that takes five arrive in
// proportion to what the keeper can actually use. A buy line for the good caps
// the pack at the room left under its cap (LLM-657): the wright keeps four
// stones at most, so a peddler never brings more than he would hold — floored
// at one so a shortage that fired always has something to buy.
func peddlerShipmentQty(w *World, keeper *Actor, item ItemKind, batches int) int {
	if batches < 1 {
		batches = 1
	}
	perBatch := 1
	if keeper != nil && keeper.RestockPolicy != nil {
		for _, e := range keeper.RestockPolicy.ProduceEntries() {
			recipe := w.Recipes[e.Item]
			if recipe == nil {
				continue
			}
			for _, in := range recipe.Inputs {
				if in.Item == item && in.Qty > perBatch {
					perBatch = in.Qty
				}
			}
		}
	}
	qty := batches * perBatch
	if keeper != nil && keeper.RestockPolicy != nil {
		for _, e := range keeper.RestockPolicy.BuyEntries() {
			if e.Item != item || e.Cap() <= 0 {
				continue
			}
			if room := e.Cap() - keeper.Inventory[item]; room < qty {
				qty = max(room, 1)
			}
		}
	}
	return qty
}

// seedPeddlerPack returns the pack and purse a shortage peddler spawns carrying:
// the missing good alone, at peddlerShipmentQty, and an ordinary traveler's
// purse (enough for a room and a supper — he is here to sell, not to buy the
// village's surplus, so he carries no factor float). r is non-nil.
func seedPeddlerPack(r *rand.Rand, w *World, errand *TradeErrand, batches int) (map[ItemKind]int, int) {
	var keeper *Actor
	if errand != nil {
		keeper = w.Actors[errand.Keeper] // the short keeper by id — never a structure scan
	}
	pack := map[ItemKind]int{}
	if errand != nil && errand.Good != "" {
		pack[errand.Good] = peddlerShipmentQty(w, keeper, errand.Good, batches)
	}
	return pack, 30 + r.Intn(21) // 30..50, the seedVisitorPack purse
}
