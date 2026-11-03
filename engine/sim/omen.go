package sim

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"
)

// omen.go — LLM-843 (slice 2 of the omens-and-the-dark epic, LLM-841). Now and
// then something small and wrong happens to a villager — the milk turns in the
// pail, the fire won't hold, the bread won't rise — and the village notices.
//
// The rule of the epic is dread and doubt, not monsters: every omen has a plain
// explanation (a hot day, damp wood, bad yeast), and nothing the engine writes
// ever says it was witchcraft. The engine owns the cadence; what the village
// makes of it is the villagers' own talk.
//
// Shape: the daily rotation (checkAndRotate) ARMS at most one omen against one
// resident, and the omen happens at that person's next natural moment for it —
// their next milking, the next stoke of their hearth, their next bake, their next
// berry harvest, their next draw at a well. That moment's hook CLAIMS the armed
// omen (takeArmedOmen), applies its effect, tells the witness in their
// completion line, and emits OmenNoticed, which the action log and the rumor
// layer carry onward. An armed omen whose moment never comes lapses unseen.
//
// Fairness (no absorbing state of bad luck): a HOUSEHOLD whose member had an
// omen is skipped for OmenCooldownDays, and one with an omen already armed is
// skipped until it happens or lapses — so Elizabeth's sour milk does not land
// John's foul well on the same house the next morning. The kind is picked
// before the person so a kind everyone qualifies for (the well) can't crowd out
// the rest, and the person is picked at random rather than in id order.
//
// OmenChancePermille 0 is the off switch for both halves: no omen is armed, and
// one already armed does not happen (it lapses).
//
// The record (who is armed, who had one when) is durable in world_state.omens:
// the cooldown is counted in days and the village restarts several times a day.
//
// Players (LLM-845) have omens of their own once the story's chapter 2 is done:
// a daily roll each (rollDailyPCOmens), at the moments a player has — a drink at
// the well, eating at a bush, lighting the camp fire or the lantern. Before
// that, chapter 2's first beat brings the first one on purpose (storyOmenDue).
// A ward at a household's home lowers its chance (omenWardPermille), players'
// and villagers' alike; the game never says so.

// OmenKind is the closed vocabulary of omens.
type OmenKind string

const (
	// OmenSourMilk — the subject's next milking yields nothing usable.
	OmenSourMilk OmenKind = "sour_milk"
	// OmenColdHearth — the next stoke of the subject's hearth burns only briefly.
	OmenColdHearth OmenKind = "cold_hearth"
	// OmenFlatBread — the subject's next evening bake spends the flour and
	// yields no bread.
	OmenFlatBread OmenKind = "flat_bread"
	// OmenWitheredBush — the subject's next berry harvest finds the bush
	// withered: nothing picked, and the bush stripped to regrow as usual.
	OmenWitheredBush OmenKind = "withered_bush"
	// OmenFoulWell — the subject's next draw at a well tastes of iron. Scene
	// only: water is the free relief path, so this omen takes nothing away.
	OmenFoulWell OmenKind = "foul_well"
	// OmenCampFireGutters — a player's next camp fire catches, then sinks: it
	// burns only omenCampFireGutter (LLM-845).
	OmenCampFireGutters OmenKind = "camp_fire_gutters"
	// OmenLanternGutters — the candle a player's next Light spends burns out in
	// omenLanternGutter (LLM-845).
	OmenLanternGutters OmenKind = "lantern_gutters"
)

// omenKinds is every kind a resident can be set against, in a fixed order —
// the roll's deterministic candidate order, and the iteration order tests rely
// on.
var omenKinds = []OmenKind{OmenSourMilk, OmenColdHearth, OmenFlatBread, OmenWitheredBush, OmenFoulWell}

// pcOmenKinds is every kind a player can be set against (LLM-845), in the
// player roll's fixed order. Each has a hook a player reaches: a drink at a
// well, eating at a bush, lighting the camp fire, lighting the lantern.
var pcOmenKinds = []OmenKind{OmenFoulWell, OmenWitheredBush, OmenCampFireGutters, OmenLanternGutters}

// AllOmenKinds is every omen kind, residents' then players' own.
var AllOmenKinds = []OmenKind{OmenSourMilk, OmenColdHearth, OmenFlatBread, OmenWitheredBush, OmenFoulWell, OmenCampFireGutters, OmenLanternGutters}

// Default WorldSettings knobs (LLM-843), live-tunable through the umbilical
// (GET /settings `all`, POST /settings/set). The chance is per game-day; 0
// turns omens off. A resident who had one is passed over for the cooldown, and
// an armed omen whose moment never comes lapses after OmenLapseHours.
const (
	DefaultOmenChancePermille = 250
	DefaultOmenCooldownDays   = 7
	DefaultOmenLapseHours     = 24
)

// Player omens (LLM-845). Each player whose omens have opened (the story's
// chapter 2 finished, pcOmensOpen) rolls OmenPCChancePermille once a game-day;
// 0 turns player omens off. A ward at a household's home scales its chance:
// a horseshoe keeps OmenWardHorseshoePermille of it, a witch bottle
// OmenWardWitchBottlePermille, both multiply. The ward settings read 0 or less
// as the default and above 1000 as 1000. The game never says a ward works.
const (
	DefaultOmenPCChancePermille        = 150
	DefaultOmenWardHorseshoePermille   = 500
	DefaultOmenWardWitchBottlePermille = 500
)

// omenHearthGutter is how long a stoke burns when the cold-hearth omen takes
// it: the fire catches, then sinks. Long enough to read as a fire that went out
// rather than one that never lit.
const omenHearthGutter = 15 * time.Minute

// omenCampFireGutter is how long a player's camp fire burns when the
// camp-fire omen takes it, and omenLanternGutter how long the lantern's candle.
const (
	omenCampFireGutter = 15 * time.Minute
	omenLanternGutter  = 5 * time.Minute
)

// pcOmensOpenAfter is the story beat whose finish opens a player's random
// omens: the last beat of chapter 2 (migration LLM-898). Before it, a player
// meets omens only through the story's own beat.
const pcOmensOpenAfter StoryMissionID = "signs-4"

// maxOmenPopups caps how many unseen omen popups a player keeps; the oldest
// goes first.
const maxOmenPopups = 5

// ArmedOmen is an omen the daily roll has set against a subject and that has
// not yet happened.
type ArmedOmen struct {
	Kind      OmenKind  `json:"kind"`
	SubjectID ActorID   `json:"subject_id"`
	ArmedAt   time.Time `json:"armed_at"`
	LapsesAt  time.Time `json:"lapses_at"`
}

// OmenRecord is the durable omen state on WorldEnvironment. LastAt is when each
// subject last had an omen HAPPEN (the cooldown anchor); an omen that lapsed
// unseen does not count.
//
// Popups are the omens a player has met and not yet closed in the story popup
// (LLM-845): kept here, in the same checkpoint as the omen, so a player who
// left sees them at the next login.
type OmenRecord struct {
	Armed  []ArmedOmen             `json:"armed,omitempty"`
	LastAt map[ActorID]time.Time   `json:"last_at,omitempty"`
	Popups map[ActorID][]OmenPopup `json:"popups,omitempty"`
}

// OmenPopup is one omen a player met, as their story popup shows it.
type OmenPopup struct {
	Kind OmenKind  `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Clone returns a deep copy, so a published snapshot never aliases the world's
// record.
func (r OmenRecord) Clone() OmenRecord {
	out := OmenRecord{Armed: slices.Clone(r.Armed), LastAt: maps.Clone(r.LastAt)}
	if len(r.Popups) > 0 {
		out.Popups = make(map[ActorID][]OmenPopup, len(r.Popups))
		for id, list := range r.Popups {
			out.Popups[id] = slices.Clone(list)
		}
	}
	return out
}

// OmenNoticed is emitted when an armed omen happens. WitnessID is who saw it
// (the milker, the stoker); SubjectID is whose omen it was — the same person
// except for a hearth stoked by a hired hand. Subscribers: the action log
// (a noticed_omen row on the witness).
type OmenNoticed struct {
	EventBase
	Kind      OmenKind
	WitnessID ActorID
	SubjectID ActorID
	HuddleID  HuddleID
	At        time.Time
}

func (OmenNoticed) isSimEvent() {}

// RollDailyOmen is the rotation-boundary omen roll. Bound to the durable
// boundary in checkAndRotate for the same reasons as the damage roll: the
// umbilical force-rotate never rolls it, and a restart does not roll twice.
// Returns the armed omen, or nil.
func RollDailyOmen(boundary time.Time, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		return rollDailyOmen(w, rng, boundary), nil
	}}
}

func rollDailyOmen(w *World, rng DamageRoller, now time.Time) *ArmedOmen {
	pruneLapsedOmens(w, now)
	chance := w.Settings.OmenChancePermille
	if chance <= 0 || rng == nil || rng.Float64()*1000 >= float64(chance) {
		return nil
	}
	byKind := omenCandidates(w, now)
	var kinds []OmenKind
	for _, k := range omenKinds {
		if len(byKind[k]) > 0 {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return nil
	}
	kind := kinds[pickIndex(rng, len(kinds))]
	subjects := byKind[kind]
	subject := subjects[pickIndex(rng, len(subjects))]
	// LLM-845: a ward keeps only part of a household's chance. The village rolls
	// once, so a warded subject is passed over by a second roll rather than by
	// thinning the pick, which would push the household's share onto the rest.
	if keep := omenWardPermille(w, subject); keep < 1000 && rng.Float64()*1000 >= float64(keep) {
		return nil
	}
	lapse := time.Duration(orDefault(w.Settings.OmenLapseHours, DefaultOmenLapseHours)) * time.Hour
	armed := ArmedOmen{Kind: kind, SubjectID: subject, ArmedAt: now, LapsesAt: now.Add(lapse)}
	w.Environment.Omens.Armed = append(w.Environment.Omens.Armed, armed)
	return &armed
}

// RollDailyPCOmens is the rotation-boundary roll for players (LLM-845): each
// player whose omens have opened rolls on their own, so a player's chance does
// not shrink as the village grows. Bound to the durable boundary next to
// RollDailyOmen. Returns the omens armed, in player id order.
func RollDailyPCOmens(boundary time.Time, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		return rollDailyPCOmens(w, rng, boundary), nil
	}}
}

func rollDailyPCOmens(w *World, rng DamageRoller, now time.Time) []ArmedOmen {
	pruneLapsedOmens(w, now)
	pruneOmenPopups(w)
	chance := w.Settings.OmenPCChancePermille
	if chance <= 0 || rng == nil {
		return nil
	}
	blocked := omenBlockedHouseholds(w, now)
	lapse := time.Duration(orDefault(w.Settings.OmenLapseHours, DefaultOmenLapseHours)) * time.Hour
	var out []ArmedOmen
	for _, a := range w.sortedStoryPlayers() {
		household := omenHousehold(w, a.ID)
		if !pcOmensOpen(w, a) || blocked[household] {
			continue
		}
		keep := float64(chance) * float64(omenWardPermille(w, a.ID)) / 1000
		if rng.Float64()*1000 >= keep {
			continue
		}
		var kinds []OmenKind
		for _, k := range pcOmenKinds {
			if pcOmenEligible(w, a, k) {
				kinds = append(kinds, k)
			}
		}
		if len(kinds) == 0 {
			continue
		}
		armed := ArmedOmen{Kind: kinds[pickIndex(rng, len(kinds))], SubjectID: a.ID, ArmedAt: now, LapsesAt: now.Add(lapse)}
		w.Environment.Omens.Armed = append(w.Environment.Omens.Armed, armed)
		blocked[household] = true
		out = append(out, armed)
	}
	return out
}

// pcOmensOpen reports whether a player's random omens have opened: they have
// finished the story's chapter 2 (pcOmensOpenAfter).
func pcOmensOpen(w *World, a *Actor) bool {
	p := w.StoryProgress[a.ID][pcOmensOpenAfter]
	return p != nil && !p.Open()
}

// pcOmenEligible reports whether the player has the moment kind needs: everyone
// drinks and eats berries; the camp fire needs a pitched camp with its ring
// down, the lantern a lantern in the pack.
func pcOmenEligible(w *World, a *Actor, kind OmenKind) bool {
	switch kind {
	case OmenFoulWell, OmenWitheredBush:
		return true
	case OmenCampFireGutters:
		camp := FindCamp(w.VillageObjects, a.ID)
		return camp != nil && camp.Fire != nil
	case OmenLanternGutters:
		return a.Inventory[HornLanternKind] > 0
	}
	return false
}

// omenWardPermille is how much of its chance the subject's household keeps for
// the wards at its home (LLM-845): 1000 with none. The ward is a tag on the
// home's object — a player's tent today, a villager's house once villagers buy
// wards. Never said in game.
func omenWardPermille(w *World, subject ActorID) int {
	a := w.Actors[subject]
	if a == nil || a.HomeStructureID == "" {
		return 1000
	}
	home := w.VillageObjects[VillageObjectID(a.HomeStructureID)]
	if home == nil {
		return 1000
	}
	keep := 1000
	if home.HasTag(TagWardHorseshoe) {
		keep = keep * omenWardSetting(w.Settings.OmenWardHorseshoePermille, DefaultOmenWardHorseshoePermille) / 1000
	}
	if home.HasTag(TagWardWitchBottle) {
		keep = keep * omenWardSetting(w.Settings.OmenWardWitchBottlePermille, DefaultOmenWardWitchBottlePermille) / 1000
	}
	return keep
}

func omenWardSetting(v, def int) int {
	v = orDefault(v, def)
	if v > 1000 {
		return 1000
	}
	return v
}

// omenBlockedHouseholds is the set of households passed over today: one with an
// omen armed, or one whose member had an omen within the cooldown.
func omenBlockedHouseholds(w *World, now time.Time) map[string]bool {
	cooldown := time.Duration(orDefault(w.Settings.OmenCooldownDays, DefaultOmenCooldownDays)) * 24 * time.Hour
	blocked := map[string]bool{}
	for _, a := range w.Environment.Omens.Armed {
		blocked[omenHousehold(w, a.SubjectID)] = true
	}
	for id, last := range w.Environment.Omens.LastAt {
		if now.Sub(last) < cooldown {
			blocked[omenHousehold(w, id)] = true
		}
	}
	return blocked
}

// omenCandidates lists, per kind, the residents an omen of that kind could be
// set against now: eligible for the kind, and of a household with no omen armed
// and none within the cooldown. Each list is sorted by id so a seeded roll is
// reproducible.
func omenCandidates(w *World, now time.Time) map[OmenKind][]ActorID {
	blocked := omenBlockedHouseholds(w, now)
	yields := animalYieldItems(w)
	out := make(map[OmenKind][]ActorID)
	for id, a := range w.Actors {
		if !omenResident(a) || blocked[omenHousehold(w, id)] {
			continue
		}
		for _, k := range omenKinds {
			if omenEligible(w, a, k, yields) {
				out[k] = append(out[k], id)
			}
		}
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i] < out[k][j] })
	}
	return out
}

// omenHousehold is the fairness unit for the cooldown: the resident's home, or
// the resident alone when they have none (or no longer exist).
func omenHousehold(w *World, id ActorID) string {
	if a := w.Actors[id]; a != nil && a.HomeStructureID != "" {
		return "home:" + string(a.HomeStructureID)
	}
	return "actor:" + string(id)
}

// omenResident reports whether a is a resident NPC — the subjects of the
// village roll. Players have a roll of their own (rollDailyPCOmens), and a
// visitor is gone before the village could talk it over.
func omenResident(a *Actor) bool {
	if a == nil || a.VisitorState != nil {
		return false
	}
	return a.Kind == KindNPCStateful || a.Kind == KindNPCShared
}

// omenEligible reports whether a has the moment kind needs — someone who milks,
// keeps a hearth, bakes at home, picks berries — so an armed omen is likely to
// happen before it lapses. Everyone draws water.
func omenEligible(w *World, a *Actor, kind OmenKind, yields map[ItemKind]bool) bool {
	switch kind {
	case OmenSourMilk:
		for item := range ForageItems(a.RestockPolicy) {
			if yields[item] {
				return true
			}
		}
		return false
	case OmenColdHearth:
		return OwnedHearth(w.VillageObjects, a.ID) != nil
	case OmenFlatBread:
		return a.HomeStructureID != ""
	case OmenWitheredBush:
		for item := range ForageItems(a.RestockPolicy) {
			if !yields[item] {
				return true
			}
		}
		return false
	case OmenFoulWell:
		return true
	}
	return false
}

// animalYieldItems is the set of items the village's animals give (milk), so
// the sour-milk and withered-bush omens can split a forager's policy between
// what comes from an animal and what comes off a bush.
func animalYieldItems(w *World) map[ItemKind]bool {
	out := map[ItemKind]bool{}
	for _, a := range w.Actors {
		if a != nil && a.Yield != nil {
			out[a.Yield.Item()] = true
		}
	}
	return out
}

// pruneLapsedOmens drops armed omens whose moment never came.
func pruneLapsedOmens(w *World, now time.Time) {
	armed := w.Environment.Omens.Armed
	if len(armed) == 0 {
		return
	}
	kept := armed[:0]
	for _, a := range armed {
		if now.Before(a.LapsesAt) {
			kept = append(kept, a)
		}
	}
	w.Environment.Omens.Armed = kept
}

// takeArmedOmen claims the armed omen of kind set against subject, if there is
// one and it has not lapsed: removes it, stamps the subject's cooldown, and
// reports true so the caller applies the effect. With omens switched off for
// the subject (OmenChancePermille 0 for a resident, OmenPCChancePermille 0 for
// a player) nothing is claimed — an armed omen just lapses.
//
// A player whose open story beat is waiting for this kind (storyOmenDue,
// LLM-845) gets it whatever is armed and whatever the settings: the open beat
// is the arming, so the story never stalls. MUST be called from inside a
// Command.Fn.
func takeArmedOmen(w *World, kind OmenKind, subject ActorID, now time.Time) bool {
	if w == nil || subject == "" {
		return false
	}
	if storyOmenDue(w, subject, kind) {
		stampOmenLastAt(w, subject, now)
		return true
	}
	chance := w.Settings.OmenChancePermille
	if a := w.Actors[subject]; a != nil && a.Kind == KindPC {
		chance = w.Settings.OmenPCChancePermille
	}
	if chance <= 0 {
		return false
	}
	pruneLapsedOmens(w, now)
	for i, a := range w.Environment.Omens.Armed {
		if a.Kind != kind || a.SubjectID != subject {
			continue
		}
		w.Environment.Omens.Armed = append(w.Environment.Omens.Armed[:i], w.Environment.Omens.Armed[i+1:]...)
		stampOmenLastAt(w, subject, now)
		return true
	}
	return false
}

func stampOmenLastAt(w *World, subject ActorID, now time.Time) {
	if w.Environment.Omens.LastAt == nil {
		w.Environment.Omens.LastAt = map[ActorID]time.Time{}
	}
	w.Environment.Omens.LastAt[subject] = now
}

// noticeOmen records an omen that just happened: the first-hand rumor for the
// witness and whoever stands in their conversation, and OmenNoticed for the
// action log and the story. Called by each omen's hook after its effect lands.
//
// A player witness (LLM-845) also gets the scene twice over: a popup kept for
// their story view (OmenRecord.Popups) and a private talk-box line
// (PCOmenNarrated). The story watcher sends the view on OmenNoticed.
func noticeOmen(w *World, kind OmenKind, witness *Actor, subjectID ActorID, now time.Time) {
	if w == nil || witness == nil {
		return
	}
	seedOmenRumor(w, kind, witness, subjectID, now)
	if storyPlayer(w, witness.ID) != nil {
		if line := OmenWitnessLine(kind, ""); line != "" {
			addOmenPopup(w, witness.ID, OmenPopup{Kind: kind, Text: line, At: now})
			w.emit(&PCOmenNarrated{ActorID: witness.ID, Kind: kind, Text: line, At: now})
		}
	}
	w.emit(&OmenNoticed{
		Kind:      kind,
		WitnessID: witness.ID,
		SubjectID: subjectID,
		HuddleID:  witness.CurrentHuddleID,
		At:        now,
	})
}

// PCOmenNarrated is a WIRE-ONLY event (no engine subscriber): the line a player
// reads in their talk box when an omen happens to them (LLM-845). TranslateEvent
// maps it to a private room_event addressed to that player, the
// StallConditionNarrated carrier.
type PCOmenNarrated struct {
	EventBase
	ActorID ActorID
	Kind    OmenKind
	Text    string
	At      time.Time
}

func (PCOmenNarrated) isSimEvent() {}

// addOmenPopup keeps an omen for the player's story view, dropping the oldest
// past maxOmenPopups.
func addOmenPopup(w *World, actorID ActorID, p OmenPopup) {
	if w.Environment.Omens.Popups == nil {
		w.Environment.Omens.Popups = map[ActorID][]OmenPopup{}
	}
	list := append(w.Environment.Omens.Popups[actorID], p)
	if len(list) > maxOmenPopups {
		list = list[len(list)-maxOmenPopups:]
	}
	w.Environment.Omens.Popups[actorID] = list
}

// pruneOmenPopups drops the popups of a player who is gone. Run by the daily
// player roll.
func pruneOmenPopups(w *World) {
	for id := range w.Environment.Omens.Popups {
		if storyPlayer(w, id) == nil {
			delete(w.Environment.Omens.Popups, id)
		}
	}
}

// OmenWitnessLine is what the witness reads at the moment the omen happens, in
// place of the activity's usual completion line (the well's is added to it,
// since the drink itself still did its work). sourceName names the cow, bush,
// hearth or well when it resolves.
func OmenWitnessLine(kind OmenKind, sourceName string) string {
	switch kind {
	case OmenSourMilk:
		if sourceName == "" {
			sourceName = "the cow"
		}
		return fmt.Sprintf("You finish milking %s, but the milk has turned in the pail — sour through, and none of it fit to keep.", sourceName)
	case OmenColdHearth:
		if sourceName != "" {
			return fmt.Sprintf("You feed the fire at %s, but the wood only smoulders and sinks — the flame will not hold for long.", sourceName)
		}
		return "You feed the fire, but the wood only smoulders and sinks — the flame will not hold for long."
	case OmenFlatBread:
		return "You finish the baking, but the loaves come out flat and heavy as stone — the flour is spent and there is no bread fit to eat."
	case OmenWitheredBush:
		if sourceName != "" {
			return fmt.Sprintf("You reach for the berries at %s, but they have withered on the stem — there is nothing worth picking, and the bush will have to grow again.", sourceName)
		}
		return "You reach for the berries, but they have withered on the stem — there is nothing worth picking, and the bush will have to grow again."
	case OmenFoulWell:
		return "The water tastes of iron, though it was sweet enough before."
	case OmenCampFireGutters:
		return "The fire catches, then sinks to a sullen smoulder, though the wood was dry enough — it will not burn long."
	case OmenLanternGutters:
		return "The candle takes the flame, then gutters and drowns in its own tallow — it is burning out already."
	}
	return ""
}

// OmenLogText is the witness's action-log text: what they found, in the past
// tense, for the day notes and the dream distiller.
func OmenLogText(kind OmenKind) string {
	switch kind {
	case OmenSourMilk:
		return "found the milk turned sour in the pail"
	case OmenColdHearth:
		return "found the hearth fire would not stay lit"
	case OmenFlatBread:
		return "found the bread would not rise"
	case OmenWitheredBush:
		return "found the berries withered on the bush"
	case OmenFoulWell:
		return "found the well water tasting of iron"
	case OmenCampFireGutters:
		return "found the camp fire would not stay lit"
	case OmenLanternGutters:
		return "found the lantern candle guttering out"
	}
	return ""
}
