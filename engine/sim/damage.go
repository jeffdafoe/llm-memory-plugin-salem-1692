package sim

// damage.go — random damage events and the public works that mend them (LLM-654).
//
// The town chest (estate_rate.go) takes coin out of the top purses and, before
// this, spent it only on the constable's wage. Damage events give it something
// to buy: a commons object breaks, the town posts a bounty, a workless hand
// mends it, and the chest pays the hand. The coin comes back into the village
// through work at a real place with a real effect.
//
// Slice 1 covers WELLS only:
//
//   - Hazard. Each sound well is rolled once per game-day (at the rotation
//     boundary) and once per storm start. The chance is base × use factor, where
//     the use factor is the draws since the last repair over a reference count
//     (capped). A busy well breaks sooner; a quiet one rarely. Rolling on a
//     storm gives the SimCity spike without coupling frequency to the chest.
//   - Guards. At most one damaged well at a time (the other always works), a
//     minimum gap after a repair before the next break, and nothing breaks while
//     the feature is off (base chances 0).
//   - Effect. A damaged well is OUT OF USE: no drinking, no drawing water. The
//     one predicate is VillageObject.Damaged(); every drink/gather/offer path
//     checks it, so the scene and the engine agree. The asset flips to its
//     "damaged" state (the well with its windlass gone) so the client shows it.
//   - Repair. Any AttrWorker with no live job may walk to the well and call
//     `repair` (StartRepair's public-works branch). The terms are the engine's:
//     PublicWorksRepairSeconds of work, PublicWorksBounty coins from the chest.
//     The bounty is only on offer while the chest holds the bounty plus
//     PublicWorksChestReserve — a broke town leaves its well broken.
//
// Coin record: the chest is not an actor, so the payout — like the constable's
// wage — writes a `collected` row (marker "public_works") and never calls
// RecordCoinPaid; the coin-record seed selects only paid/labored rows.

import (
	"errors"
	"log"
	"sort"
	"time"
)

var (
	// ErrNotDamageable — the operator tried to damage an object slice 1 does not
	// cover (only wells break).
	ErrNotDamageable = errors.New("only a well can be damaged")
	// ErrUnknownDamageAction — the operator control took an action other than
	// "damage" or "repair".
	ErrUnknownDamageAction = errors.New(`action must be "damage" or "repair"`)
)

// TagDamaged marks the asset state that renders an object out of use. Resolved
// through Asset.StateForTag, so the tag — not the state name — is the contract.
const TagDamaged = "damaged"

// Defaults for the live-tunable damage and public-works settings.
const (
	// DefaultWellDamageChancePermille is the daily chance, per thousand, that a
	// well at the reference use breaks. 0 disables the daily roll.
	DefaultWellDamageChancePermille = 100
	// DefaultWellDamageStormChancePermille is the chance, per thousand, that a
	// well at the reference use breaks when a storm starts. 0 disables it.
	DefaultWellDamageStormChancePermille = 150
	// DefaultWellDamageUseReference is the number of draws since the last repair
	// at which the use factor is 1.
	DefaultWellDamageUseReference = 60
	// DefaultWellDamageMinGapHours is the quiet time after a repair before any
	// well may break again.
	DefaultWellDamageMinGapHours = 48
	// DefaultPublicWorksBounty is what the chest pays for one repair.
	DefaultPublicWorksBounty = 12
	// DefaultPublicWorksRepairSeconds is how long one repair takes.
	DefaultPublicWorksRepairSeconds = 3600
	// DefaultPublicWorksChestReserve is what the chest keeps back; the bounty is
	// on offer only while the chest holds bounty + reserve.
	DefaultPublicWorksChestReserve = 50
)

// DamageRoller is the randomness a hazard roll needs. Both math/rand and
// math/rand/v2 *Rand satisfy it (the rotation ticker holds the first, the storm
// cascade the second), and a test can pass a fixed sequence.
type DamageRoller interface {
	Float64() float64
}

// damageUseFactorCap bounds the use factor so a well that has gone a long time
// without a break does not become near-certain to fail.
const damageUseFactorCap = 3.0

// publicWorksForText is the "for" clause on the hand's `collected` row.
const publicWorksForText = "mending the well at the town's charge"

// Damage triggers, carried on ObjectDamaged and in the log.
const (
	DamageTriggerWear  = "wear"
	DamageTriggerStorm = "storm"
	DamageTriggerForce = "force"
)

// Damaged reports whether the object is out of use after a damage event.
// Nil-safe.
func (o *VillageObject) Damaged() bool {
	return o != nil && !o.DamagedAt.IsZero()
}

// IsWell reports whether the object is a well. Nil-safe.
func (o *VillageObject) IsWell() bool {
	return o != nil && o.HasTag(TagWell)
}

// ObjectDamaged is emitted when a damage event puts an object out of use.
type ObjectDamaged struct {
	EventBase
	ObjectID VillageObjectID
	Name     string
	Trigger  string
	At       time.Time
}

func (ObjectDamaged) isSimEvent() {}

// ObjectRepaired is emitted when a public-works repair puts an object back in
// use. RepairerID is the hand who mended it ("" for an operator reset).
type ObjectRepaired struct {
	EventBase
	ObjectID   VillageObjectID
	Name       string
	RepairerID ActorID
	Bounty     int
	At         time.Time
}

func (ObjectRepaired) isSimEvent() {}

// accrueDamageUse counts one draw at obj toward its damage hazard. Only wells
// accrue in slice 1.
func accrueDamageUse(obj *VillageObject) {
	if obj.IsWell() && !obj.Damaged() {
		obj.UseSinceRepair++
	}
}

// damageUseFactor turns the draws since the last repair into the hazard
// multiplier: use / reference, capped. A reference of 0 or less means "use does
// not matter" (factor 1).
func damageUseFactor(use, reference int) float64 {
	if reference <= 0 {
		return 1
	}
	f := float64(use) / float64(reference)
	if f > damageUseFactorCap {
		return damageUseFactorCap
	}
	return f
}

// anyWellDamaged reports whether a well is already out of use — the
// one-active-event guard.
func anyWellDamaged(w *World) bool {
	for _, obj := range w.VillageObjects {
		if obj.IsWell() && obj.Damaged() {
			return true
		}
	}
	return false
}

// rollWellDamage rolls every sound well once for trigger and damages at most one.
// permille is the chance at the reference use. Wells are rolled in ID order so
// a seeded rng is reproducible. Returns the damaged well, or nil.
func rollWellDamage(w *World, trigger string, permille int, rng DamageRoller, now time.Time) *VillageObject {
	if w == nil || permille <= 0 || rng == nil {
		return nil
	}
	if anyWellDamaged(w) {
		return nil
	}
	if gap := time.Duration(w.Settings.WellDamageMinGapHours) * time.Hour; gap > 0 &&
		!w.Environment.LastRepairAt.IsZero() && now.Sub(w.Environment.LastRepairAt) < gap {
		return nil
	}
	var wells []*VillageObject
	for _, obj := range w.VillageObjects {
		if obj.IsWell() {
			wells = append(wells, obj)
		}
	}
	sort.Slice(wells, func(i, j int) bool { return wells[i].ID < wells[j].ID })
	for _, obj := range wells {
		p := float64(permille) / 1000 * damageUseFactor(obj.UseSinceRepair, w.Settings.WellDamageUseReference)
		if rng.Float64() < p {
			damageObject(w, obj, trigger, now)
			return obj
		}
	}
	return nil
}

// damageObject puts obj out of use: stamps DamagedAt, flips the asset to its
// damaged state when it has one, and emits ObjectDamaged. Idempotent on an
// already-damaged object.
func damageObject(w *World, obj *VillageObject, trigger string, now time.Time) {
	if obj == nil || obj.Damaged() {
		return
	}
	obj.DamagedAt = now
	if asset := w.Assets[obj.AssetID]; asset != nil {
		if st := asset.StateForTag(TagDamaged); st != nil && obj.CurrentState != st.State {
			setVillageObjectStateInline(w, obj, st.State)
		}
	}
	name := damageObjectName(w, obj)
	log.Printf("sim/damage: %s (%s) is out of use — trigger %s, %d draws since last repair",
		name, obj.ID, trigger, obj.UseSinceRepair)
	w.emit(&ObjectDamaged{ObjectID: obj.ID, Name: name, Trigger: trigger, At: now})
	repostPublicWorksNotices(w, now)
}

// repairObject puts obj back in use: clears the damage and the use count,
// returns the asset to its sound state, stamps the min-gap anchor, and emits
// ObjectRepaired. Paying the hand is the caller's job (completePublicWorksRepair);
// an operator reset pays nothing.
func repairObject(w *World, obj *VillageObject, repairerID ActorID, bounty int, now time.Time) {
	if obj == nil || !obj.Damaged() {
		return
	}
	obj.DamagedAt = time.Time{}
	obj.UseSinceRepair = 0
	if asset := w.Assets[obj.AssetID]; asset != nil {
		if st := soundAssetState(asset); st != nil && obj.CurrentState != st.State {
			setVillageObjectStateInline(w, obj, st.State)
		}
	}
	w.Environment.LastRepairAt = now
	name := damageObjectName(w, obj)
	log.Printf("sim/damage: %s (%s) is mended (repairer %q, bounty %d)", name, obj.ID, repairerID, bounty)
	w.emit(&ObjectRepaired{ObjectID: obj.ID, Name: name, RepairerID: repairerID, Bounty: bounty, At: now})
	repostPublicWorksNotices(w, now)
}

// soundAssetState returns the lowest-ID state that is not the damaged one — the
// state a repaired object returns to.
func soundAssetState(a *Asset) *AssetState {
	var best *AssetState
	for i := range a.States {
		s := &a.States[i]
		if stateHasTag(s, TagDamaged) {
			continue
		}
		if best == nil || s.ID < best.ID {
			best = s
		}
	}
	return best
}

func stateHasTag(s *AssetState, tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// damageObjectName is the display name used in logs, events and cues.
func damageObjectName(w *World, obj *VillageObject) string {
	return DamageSiteLabel(w.VillageObjects, w.Structures, w.Assets, obj)
}

// damageSiteLandmarkTiles bounds how far a building may be to name a broken
// object by it.
const damageSiteLandmarkTiles = 16

// DamageSiteLabel names a broken object so two alike can be told apart — both
// village wells are called "Well": the object's name plus the nearest named
// building within damageSiteLandmarkTiles ("Well by the Mill"), or the bare
// name when none is near. Pure over the maps so the world (notices, ticker) and
// perception (the cue) label a site the same way. Ties go to the lowest id.
func DamageSiteLabel(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, assets map[AssetID]*Asset, obj *VillageObject) string {
	if obj == nil {
		return ""
	}
	catalog := ""
	if a := assets[obj.AssetID]; a != nil {
		catalog = a.Name
	}
	name := obj.EffectiveDisplayName(catalog)
	here := obj.Pos.Tile()
	var bestName string
	var bestID VillageObjectID
	bestDist := damageSiteLandmarkTiles + 1
	for id, other := range objects {
		if other == nil || id == obj.ID || other.DisplayName == "" {
			continue
		}
		if _, ok := structures[StructureID(id)]; !ok {
			continue
		}
		d := here.Chebyshev(other.Pos.Tile())
		if d < bestDist || (d == bestDist && id < bestID) {
			bestName, bestID, bestDist = other.DisplayName, id, d
		}
	}
	if bestName == "" {
		return name
	}
	return name + " by " + WithDefiniteArticle(bestName)
}

// ObjectConditionNarrated is the player's thought on walking up to a broken
// object (LLM-654) — the damage twin of StallConditionNarrated. TranslateEvent
// maps it to a PRIVATE room_event addressed to the PC; ObjectID rides as the
// frame's structure_id so the client can float the thought over the well.
type ObjectConditionNarrated struct {
	EventBase
	ActorID  ActorID
	ObjectID VillageObjectID
	Text     string
	At       time.Time
}

func (ObjectConditionNarrated) isSimEvent() {}

// emitDamagedObjectNarration tells a PC who just arrived at a broken well that
// it is broken. PC-only: NPCs never see a broken well offered, and a hand gets
// the "## The town's works" cue instead. Once per arrival, so it cannot spam.
func emitDamagedObjectNarration(w *World, actor *Actor, arrivedEvt *ActorArrived, now time.Time) {
	if w == nil || actor == nil || actor.Kind != KindPC {
		return
	}
	var obj *VillageObject
	if arrivedEvt != nil && arrivedEvt.DestObjectID != "" {
		obj = w.VillageObjects[arrivedEvt.DestObjectID]
	}
	if !obj.Damaged() {
		if id, ok := resolveLoiteringObject(w, actor.Pos, LoiterAttributionTiles); ok {
			obj = w.VillageObjects[id]
		}
	}
	if !obj.IsWell() || !obj.Damaged() {
		return
	}
	text := "This well is broken — the windlass is down, and no water can be drawn here."
	if PublicWorksBountyOpen(w.Environment.TownChest, w.Settings.PublicWorksBounty, w.Settings.PublicWorksChestReserve) {
		text += " The town is paying " + coinsPhrase(w.Settings.PublicWorksBounty) + " to whoever mends it."
	}
	w.emit(&ObjectConditionNarrated{ActorID: actor.ID, ObjectID: obj.ID, Text: text, At: now})
}

// DamageTickerLine is one broken object as a line for the client's top ticker.
type DamageTickerLine struct {
	ObjectID VillageObjectID
	Text     string
}

// DamageTickerLines lists every broken well as one ticker line, lowest id
// first. Pure over the snapshot (the public world read builds it).
func DamageTickerLines(s *Snapshot) []DamageTickerLine {
	var out []DamageTickerLine
	for _, obj := range s.VillageObjects {
		if !obj.IsWell() || !obj.Damaged() {
			continue
		}
		site := DamageSiteLabel(s.VillageObjects, s.Structures, s.Assets, obj)
		text := "The windlass at the " + site + " is down"
		if PublicWorksBountyOpen(s.Environment.TownChest, s.PublicWorksBounty, s.PublicWorksChestReserve) {
			text += " — the town pays " + coinsPhrase(s.PublicWorksBounty) + " to the hand who mends it."
		} else {
			text += " — draw your water at the other well."
		}
		out = append(out, DamageTickerLine{ObjectID: obj.ID, Text: text})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ObjectID < out[j].ObjectID })
	return out
}

// PublicWorksBountyOpen reports whether the chest can pay a bounty now: it holds
// the bounty plus the reserve, and the bounty is positive. The one predicate the
// bounty cue (over the snapshot mirrors) and StartRepair (over w.Settings) share.
func PublicWorksBountyOpen(chest, bounty, reserve int) bool {
	return bounty > 0 && chest >= bounty+reserve
}

// objectUnderRepair reports whether some actor already has a live repair window
// on obj — so a second hand can't take the same job.
func objectUnderRepair(w *World, objID VillageObjectID, except ActorID) bool {
	for id, a := range w.Actors {
		if id == except || a == nil || a.SourceActivity == nil {
			continue
		}
		if a.SourceActivity.Kind == SourceActivityRepair && a.SourceActivity.ObjectID == objID {
			return true
		}
	}
	return false
}

// publicWorksSiteAt returns the damaged object the actor is standing at (the
// same loitering resolution the drink path uses), or nil.
func publicWorksSiteAt(w *World, actor *Actor) *VillageObject {
	_, obj := findRefreshObjectNear(w, actor.Pos)
	if obj.Damaged() {
		return obj
	}
	return nil
}

// MayTakePublicWorks reports whether an actor may take the town's repair work:
// a worker (AttrWorker) who is not a visitor. The live-job gate is separate
// (workerHasLiveJob sim-side, the laboring views perception-side).
func MayTakePublicWorks(a *Actor) bool {
	return actorIsWorker(a) && !IsVisitorActorID(a.ID)
}

// startPublicWorksRepair opens a public-works repair window on site for actor.
// The caller (StartRepair) has checked the actor is not walking or busy and
// found no stall of their own to mend. Terms are the engine's: the window is
// PublicWorksRepairSeconds, and the bounty is paid at completion.
func startPublicWorksRepair(w *World, actor *Actor, site *VillageObject, now time.Time) (any, error) {
	if !MayTakePublicWorks(actor) {
		return nil, errors.New("the town's repair work is for hands seeking work — it is not yours to take.")
	}
	if workerHasLiveJob(w, actor.ID) {
		return nil, errors.New("you already have a job to see through — finish it before taking the town's work.")
	}
	if objectUnderRepair(w, site.ID, actor.ID) {
		return nil, errors.New("someone is already mending it.")
	}
	if !PublicWorksBountyOpen(w.Environment.TownChest, w.Settings.PublicWorksBounty, w.Settings.PublicWorksChestReserve) {
		return nil, errors.New("the town chest cannot pay for the mending just now.")
	}
	seconds := w.Settings.PublicWorksRepairSeconds
	if seconds <= 0 {
		seconds = DefaultPublicWorksRepairSeconds
	}
	actor.SourceActivity = &SourceActivity{
		Kind:      SourceActivityRepair,
		ObjectID:  site.ID,
		StartedAt: now,
		Until:     now.Add(time.Duration(seconds) * time.Second),
	}
	name := sourceActivityObjectName(w, site)
	w.emit(&SourceActivityStarted{
		ActorID:    actor.ID,
		ObjectID:   site.ID,
		Kind:       SourceActivityRepair,
		Until:      actor.SourceActivity.Until,
		At:         now,
		SourceName: sourceActivityWireLabel(w, site.ID),
	})
	emitRepairNarration(w, actor, name, now)
	log.Printf("sim/damage: %q began mending %s (%s) for the town", actor.ID, name, site.ID)
	return SourceActivityStartResult{
		Started:    true,
		Kind:       SourceActivityRepair,
		ObjectID:   site.ID,
		SourceName: name,
		Until:      actor.SourceActivity.Until,
	}, nil
}

// completePublicWorksRepair lands a finished public-works repair: mends obj and
// pays the hand from the chest (capped by what it holds — the gate at start
// already checked it could pay, but the wage may have drawn it down since).
func completePublicWorksRepair(w *World, actor *Actor, obj *VillageObject, now time.Time) int {
	if actor == nil || !obj.Damaged() {
		return 0
	}
	bounty := w.Settings.PublicWorksBounty
	if bounty > w.Environment.TownChest {
		bounty = w.Environment.TownChest
	}
	if bounty < 0 {
		bounty = 0
	}
	w.Environment.TownChest -= bounty
	actor.Coins += bounty
	repairObject(w, obj, actor.ID, bounty, now)
	if bounty == 0 {
		log.Printf("sim/damage: the chest is empty — %q mended %s unpaid", actor.ID, obj.ID)
		return 0
	}
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          actor.ID,
		OccurredAt:       now,
		ActionType:       ActionTypeCollected,
		Text:             publicWorksForText,
		HuddleID:         actor.CurrentHuddleID,
		CounterpartyName: estateRateRecipientName,
		Amount:           bounty,
	}).Fn(w); err != nil {
		log.Printf("sim/damage: action-log append failed for bounty to %q: %v", actor.ID, err)
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    actor.ID,
		OccurredAt: now,
		ActionType: ActionTypeCollected,
		Payload: map[string]any{
			"payer":        estateRateRecipientName,
			"amount":       bounty,
			"for":          publicWorksForText,
			"public_works": true,
			"object_id":    string(obj.ID),
			"chest_after":  w.Environment.TownChest,
		},
		SpeakerName: actorDisplayNameOrID(actor),
		HuddleID:    actor.CurrentHuddleID,
		Source:      "engine",
	})
	return bounty
}

// PublicWorksCompletionNarration is the hand's completion beat for a town repair
// — the public-works sibling of SourceActivityCompletionNarration's stall line.
func PublicWorksCompletionNarration(sourceName string, paid int) string {
	place := "the well"
	if sourceName != "" {
		place = WithDefiniteArticle(sourceName)
	}
	if paid <= 0 {
		return "You finish mending " + place + "; it draws water again, but the town chest was empty and nobody paid you."
	}
	return "You finish mending " + place + "; it draws water again, and the town pays you " + coinsPhrase(paid) + " for the work."
}

// RollDailyDamage is the rotation-boundary hazard roll. Bound to the durable
// boundary in checkAndRotate (not ApplyDailyRotation), so the umbilical
// force-rotate never rolls it and a restart does not roll twice.
func RollDailyDamage(boundary time.Time, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		return rollWellDamage(w, DamageTriggerWear, w.Settings.WellDamageChancePermille, rng, boundary), nil
	}}
}

// RollStormDamage is the storm-start hazard roll; called inline from the
// WeatherChanged subscriber (cascade), which already runs on the world goroutine.
func RollStormDamage(w *World, rng DamageRoller, now time.Time) *VillageObject {
	return rollWellDamage(w, DamageTriggerStorm, w.Settings.WellDamageStormChancePermille, rng, now)
}

// SetObjectDamage is the operator control: action "damage" puts a well out of
// use now (no roll, no guards but "is a well"), "repair" mends it without a
// bounty.
func SetObjectDamage(id VillageObjectID, action string) Command {
	return Command{Fn: func(w *World) (any, error) {
		obj := w.VillageObjects[id]
		if obj == nil {
			return nil, ErrVillageObjectNotFound
		}
		now := time.Now().UTC()
		switch action {
		case "damage":
			if !obj.IsWell() {
				return nil, ErrNotDamageable
			}
			damageObject(w, obj, DamageTriggerForce, now)
		case "repair":
			repairObject(w, obj, "", 0, now)
		default:
			return nil, ErrUnknownDamageAction
		}
		return obj.Damaged(), nil
	}}
}
