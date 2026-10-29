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
)

// omenKinds is every kind in a fixed order — the roll's deterministic
// candidate order, and the iteration order tests rely on.
var omenKinds = []OmenKind{OmenSourMilk, OmenColdHearth, OmenFlatBread, OmenWitheredBush, OmenFoulWell}

// Default WorldSettings knobs (LLM-843), live-tunable through the umbilical
// (GET /settings `all`, POST /settings/set). The chance is per game-day; 0
// turns omens off. A resident who had one is passed over for the cooldown, and
// an armed omen whose moment never comes lapses after OmenLapseHours.
const (
	DefaultOmenChancePermille = 250
	DefaultOmenCooldownDays   = 7
	DefaultOmenLapseHours     = 24
)

// omenHearthGutter is how long a stoke burns when the cold-hearth omen takes
// it: the fire catches, then sinks. Long enough to read as a fire that went out
// rather than one that never lit.
const omenHearthGutter = 15 * time.Minute

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
type OmenRecord struct {
	Armed  []ArmedOmen           `json:"armed,omitempty"`
	LastAt map[ActorID]time.Time `json:"last_at,omitempty"`
}

// Clone returns a deep copy, so a published snapshot never aliases the world's
// record.
func (r OmenRecord) Clone() OmenRecord {
	return OmenRecord{Armed: slices.Clone(r.Armed), LastAt: maps.Clone(r.LastAt)}
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
	lapse := time.Duration(orDefault(w.Settings.OmenLapseHours, DefaultOmenLapseHours)) * time.Hour
	armed := ArmedOmen{Kind: kind, SubjectID: subject, ArmedAt: now, LapsesAt: now.Add(lapse)}
	w.Environment.Omens.Armed = append(w.Environment.Omens.Armed, armed)
	return &armed
}

// omenCandidates lists, per kind, the residents an omen of that kind could be
// set against now: eligible for the kind, and of a household with no omen armed
// and none within the cooldown. Each list is sorted by id so a seeded roll is
// reproducible.
func omenCandidates(w *World, now time.Time) map[OmenKind][]ActorID {
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

// omenResident reports whether a is a resident NPC — the only subjects an omen
// is set against. Players hear of omens through villagers (LLM-843 design), and
// a visitor is gone before the village could talk it over.
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
// reports true so the caller applies the effect. With omens switched off
// (OmenChancePermille 0) nothing is claimed — an armed omen just lapses. MUST
// be called from inside a Command.Fn.
func takeArmedOmen(w *World, kind OmenKind, subject ActorID, now time.Time) bool {
	if w == nil || subject == "" || w.Settings.OmenChancePermille <= 0 {
		return false
	}
	pruneLapsedOmens(w, now)
	for i, a := range w.Environment.Omens.Armed {
		if a.Kind != kind || a.SubjectID != subject {
			continue
		}
		w.Environment.Omens.Armed = append(w.Environment.Omens.Armed[:i], w.Environment.Omens.Armed[i+1:]...)
		if w.Environment.Omens.LastAt == nil {
			w.Environment.Omens.LastAt = map[ActorID]time.Time{}
		}
		w.Environment.Omens.LastAt[subject] = now
		return true
	}
	return false
}

// noticeOmen records an omen that just happened: the first-hand rumor for the
// witness and whoever stands in their conversation, and OmenNoticed for the
// action log. Called by each omen's hook after its effect lands.
func noticeOmen(w *World, kind OmenKind, witness *Actor, subjectID ActorID, now time.Time) {
	if w == nil || witness == nil {
		return
	}
	seedOmenRumor(w, kind, witness, subjectID, now)
	w.emit(&OmenNoticed{
		Kind:      kind,
		WitnessID: witness.ID,
		SubjectID: subjectID,
		HuddleID:  witness.CurrentHuddleID,
		At:        now,
	})
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
	}
	return ""
}
