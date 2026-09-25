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
// Slice 2 (LLM-675) adds owned BUSINESSES on the same loop, each kind with its
// own guards, chances and terms:
//
//   - Hazard. The use factor is the business's stall wear (its owner's earned
//     margin since the last nail-mend) over a reference, so busy shops break
//     more. One damaged business at a time, its own min gap.
//   - Effect. Not out of use: a damaged business gets the degrade effect (no
//     shelf stock in, production slowed — BusinessOutOfTrade). The keeper's own
//     nail-mend stays wear-only (StallRepairable), so it can never clear the
//     town's damage and the town's repair never resets the keeper's wear.
//   - Visual. A Debris overlay (storm branches or split boards, by trigger) is
//     placed attached to the business and removed by the repair. It is unnamed,
//     so the loiter resolvers never attribute anyone to it; the repair site is
//     the business itself (AtBusiness — inside or at its pin).
//
// Coin record: the chest is not an actor, so the payout — like the constable's
// wage — writes a `collected` row (marker "public_works") and never calls
// RecordCoinPaid; the coin-record seed selects only paid/labored rows.

import (
	"errors"
	"log"
	"sort"
	"strings"
	"time"
)

var (
	// ErrNotDamageable — the operator tried to damage an object no damage kind
	// covers (only wells and owned businesses break).
	ErrNotDamageable = errors.New("only a well, an owned business or a road obstacle can be damaged")
	// ErrUnknownDamageAction — the operator control took an action other than
	// "damage", "storm" or "repair".
	ErrUnknownDamageAction = errors.New(`action must be "damage", "storm" or "repair"`)
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
	// DefaultWellDamageUseReference is the water drawn since the last repair, in
	// units (a drink is 1, a pail its size), at which the use factor is 1.
	DefaultWellDamageUseReference = 120
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

	// DefaultBusinessDamageChancePermille is the daily chance, per thousand, that
	// a business at the reference wear is damaged. 0 disables the daily roll.
	DefaultBusinessDamageChancePermille = 60
	// DefaultBusinessDamageStormChancePermille is the chance, per thousand, that
	// a business at the reference wear is damaged when a storm starts.
	DefaultBusinessDamageStormChancePermille = 150
	// DefaultBusinessDamageWearReference is the stall wear at which the use
	// factor is 1 — the nail-mend threshold's default, so a shop due its mend
	// carries the base chance.
	DefaultBusinessDamageWearReference = 180
	// DefaultBusinessDamageMinGapHours is the quiet time after a business repair
	// before any business may be damaged again.
	DefaultBusinessDamageMinGapHours = 24
	// DefaultPublicWorksBusinessBounty is what the chest pays for mending a
	// damaged business.
	DefaultPublicWorksBusinessBounty = 25
	// DefaultPublicWorksBusinessRepairSeconds is how long mending a business takes.
	DefaultPublicWorksBusinessRepairSeconds = 7200
)

// Public-works site kinds — what a damaged object is, for the terms, the
// guards and the wording.
const (
	PublicWorksWell     = "well"
	PublicWorksBusiness = "business"
)

// The Debris overlay a damaged business carries (LLM-675). The asset id is
// fixed by the migration; its states name the cause, so the wording survives a
// restart with the placement.
const (
	DebrisAssetID    AssetID = "019e5f00-c401-7a10-9e00-000000675001"
	TagDebris                = "debris"
	DebrisStateStorm         = "storm"
	DebrisStateWorn          = "worn"
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
func publicWorksForText(site string) string {
	if site == "" {
		site = "the well"
	}
	return "mending " + site + " at the town's charge"
}

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

// PublicWorksKind is the damage kind an object belongs to — PublicWorksWell,
// PublicWorksBusiness (an owned wearable business), PublicWorksRoad (a placed
// road obstacle, LLM-677), or "" for anything that never breaks. Nil-safe.
func PublicWorksKind(o *VillageObject) string {
	switch {
	case o.IsWell():
		return PublicWorksWell
	case IsWearableStall(o):
		return PublicWorksBusiness
	case o.IsRoadObstacle():
		return PublicWorksRoad
	}
	return ""
}

// IsDamagedSite reports whether o is a damaged object the town's works cover:
// a broken well, a damaged business or a road obstacle. Nil-safe.
func IsDamagedSite(o *VillageObject) bool {
	return PublicWorksKind(o) != "" && o.Damaged()
}

// BusinessOutOfTrade reports whether a wearable business is shut for shelf
// stock and slowed at its work: worn past the degrade threshold, or damaged by
// an event (LLM-675). The EFFECT predicate — restock, production and the
// "## Restocking" suppression read it. Deliberately NOT StallDegraded, which
// also gates the keeper's own nail-mend (StallRepairable): the town's damage
// must never turn into a mend the keeper pays for, nor be cleared by one.
func BusinessOutOfTrade(obj *VillageObject, degradeThreshold int) bool {
	return StallDegraded(obj, degradeThreshold) || (IsWearableStall(obj) && obj.Damaged())
}

// BusinessDamageCause reads why a business was damaged off its Debris overlay's
// state — DebrisStateStorm or DebrisStateWorn (also the fallback when the
// overlay is missing, e.g. removed in the editor). Pure over the object map.
func BusinessDamageCause(objects map[VillageObjectID]*VillageObject, businessID VillageObjectID) string {
	if d := debrisFor(objects, businessID); d != nil && d.CurrentState == DebrisStateStorm {
		return DebrisStateStorm
	}
	return DebrisStateWorn
}

// debrisFor returns the Debris overlay attached to businessID, or nil.
func debrisFor(objects map[VillageObjectID]*VillageObject, businessID VillageObjectID) *VillageObject {
	var best *VillageObject
	for _, o := range objects {
		if o == nil || o.AttachedTo != businessID || !o.HasTag(TagDebris) {
			continue
		}
		if best == nil || o.ID < best.ID {
			best = o
		}
	}
	return best
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

// accrueDamageUse counts units of water drawn at obj toward its damage hazard —
// 1 for a drink, the pail's size for a gather — so the well that gives the most
// water wears fastest (LLM-675; slice 1 counted trips, which made the mill well,
// drawn a full pail at a time, the slowest to wear). Only wells accrue here; a
// business's use factor is its stall wear.
func accrueDamageUse(obj *VillageObject, units int) {
	if units > 0 && obj.IsWell() && !obj.Damaged() {
		obj.UseSinceRepair += units
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

// anyDamaged reports whether an object of kind is already damaged — the
// one-active-event-per-kind guard.
func anyDamaged(w *World, kind string) bool {
	for _, obj := range w.VillageObjects {
		if obj.Damaged() && PublicWorksKind(obj) == kind {
			return true
		}
	}
	return false
}

// lastRepairAt returns the min-gap anchor for kind.
func lastRepairAt(w *World, kind string) time.Time {
	if kind == PublicWorksBusiness {
		return w.Environment.LastBusinessRepairAt
	}
	return w.Environment.LastWellRepairAt
}

// rollDamage rolls every sound object of kind once for trigger and damages at
// most one. permille is the chance at the reference use; the use factor is a
// well's water drawn or a business's stall wear. Objects are rolled in ID order
// so a seeded rng is reproducible. Returns the damaged object, or nil.
func rollDamage(w *World, kind, trigger string, permille int, rng DamageRoller, now time.Time) *VillageObject {
	if w == nil || permille <= 0 || rng == nil {
		return nil
	}
	if anyDamaged(w, kind) {
		return nil
	}
	gapHours, reference := w.Settings.WellDamageMinGapHours, w.Settings.WellDamageUseReference
	if kind == PublicWorksBusiness {
		gapHours, reference = w.Settings.BusinessDamageMinGapHours, w.Settings.BusinessDamageWearReference
	}
	if last := lastRepairAt(w, kind); gapHours > 0 && !last.IsZero() && now.Sub(last) < time.Duration(gapHours)*time.Hour {
		return nil
	}
	var candidates []*VillageObject
	for _, obj := range w.VillageObjects {
		if PublicWorksKind(obj) == kind {
			candidates = append(candidates, obj)
		}
	}
	sortObjectsByID(candidates)
	for _, obj := range candidates {
		use := obj.UseSinceRepair
		if kind == PublicWorksBusiness {
			use = obj.Wear
		}
		p := float64(permille) / 1000 * damageUseFactor(use, reference)
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
	if PublicWorksKind(obj) == PublicWorksBusiness {
		placeDebris(w, obj, trigger)
	}
	name := damageObjectName(w, obj)
	log.Printf("sim/damage: %s (%s) is out of use — trigger %s, use %d, wear %d",
		name, obj.ID, trigger, obj.UseSinceRepair, obj.Wear)
	w.emit(&ObjectDamaged{ObjectID: obj.ID, Name: name, Trigger: trigger, At: now})
	syncPublicWorksNews(w, now)
}

// placeDebris hangs the Debris overlay on a damaged business — storm branches
// for a storm, split boards otherwise. Unnamed on purpose: the loiter resolvers
// skip unnamed objects, so nobody standing at the shop is attributed to the
// debris instead of the shop. No-op when the catalog lacks the asset (a test
// world) or the business already carries one.
func placeDebris(w *World, business *VillageObject, trigger string) {
	asset := w.Assets[DebrisAssetID]
	if asset == nil || debrisFor(w.VillageObjects, business.ID) != nil {
		return
	}
	state := DebrisStateWorn
	if trigger == DamageTriggerStorm {
		state = DebrisStateStorm
	}
	if asset.FindState(state) == nil {
		state = asset.DefaultState
	}
	d := placeVillageObject(w, DebrisAssetID, asset, business.Pos, business.ID, "", state)
	d.Tags = []string{TagDebris}
}

// removeDebris deletes every Debris overlay attached to a business — normally
// one, but a stray second (hand-placed in the editor, say) goes too, so a
// mended shop never keeps a heap by its door. DeleteVillageObject fails only
// for a missing object or a structure, neither possible for an overlay found
// here; a failure is logged and the repair still stands.
func removeDebris(w *World, businessID VillageObjectID) {
	var ids []VillageObjectID
	for id, o := range w.VillageObjects {
		if o != nil && o.AttachedTo == businessID && o.HasTag(TagDebris) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if _, err := DeleteVillageObject(id).Fn(w); err != nil {
			log.Printf("sim/damage: removing debris %s from %s: %v", id, businessID, err)
		}
	}
}

// repairObject puts obj back in use: clears the damage and the use count,
// returns the asset to its sound state, removes a business's debris, stamps
// the kind's min-gap anchor, and emits ObjectRepaired. A business's stall wear
// is the keeper's and is left as it is. A road obstacle IS the damage, so
// mending a road deletes it (LLM-677). Paying the hand is the caller's job
// (completePublicWorksRepair); an operator reset pays nothing.
func repairObject(w *World, obj *VillageObject, repairerID ActorID, bounty int, now time.Time) {
	if obj == nil || !obj.Damaged() {
		return
	}
	name := damageObjectName(w, obj)
	obj.DamagedAt = time.Time{}
	obj.UseSinceRepair = 0
	// Only an object showing its damaged state goes back to a sound one — a
	// business has no damaged art, and its open/closed state is not ours to reset.
	if asset := w.Assets[obj.AssetID]; asset != nil {
		if cur := asset.FindState(obj.CurrentState); cur != nil && stateHasTag(cur, TagDamaged) {
			if st := soundAssetState(asset); st != nil {
				setVillageObjectStateInline(w, obj, st.State)
			}
		}
	}
	switch PublicWorksKind(obj) {
	case PublicWorksBusiness:
		removeDebris(w, obj.ID)
		w.Environment.LastBusinessRepairAt = now
	case PublicWorksRoad:
		removeRoadObstacle(w, obj, now)
	default:
		w.Environment.LastWellRepairAt = now
	}
	log.Printf("sim/damage: %s (%s) is mended (repairer %q, bounty %d)", name, obj.ID, repairerID, bounty)
	w.emit(&ObjectRepaired{ObjectID: obj.ID, Name: name, RepairerID: repairerID, Bounty: bounty, At: now})
	syncPublicWorksNews(w, now)
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
	// A building is its own landmark — "the Tavern", never "Tavern by the Inn".
	if _, ok := structures[StructureID(obj.ID)]; ok && name != "" {
		return name
	}
	landmark := damageSiteLandmark(objects, structures, obj)
	if landmark == "" {
		return name
	}
	return name + " by " + WithDefiniteArticle(landmark)
}

// damageSiteLandmark is the nearest named building within
// damageSiteLandmarkTiles of obj, or "" when none is near. Ties go to the
// lowest id.
func damageSiteLandmark(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, obj *VillageObject) string {
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
	return bestName
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
	if !IsDamagedSite(obj) {
		return
	}
	kind := PublicWorksKind(obj)
	text := "This well is broken — the windlass is down, and no water can be drawn here."
	switch kind {
	case PublicWorksBusiness:
		text = DamageFact(w.VillageObjects, w.Structures, w.Assets, obj) + " — it can take in no new stock until it is mended."
	case PublicWorksRoad:
		text = DamageFact(w.VillageObjects, w.Structures, w.Assets, obj) + " — walkers must go around it until it is cleared."
	}
	bounty, _ := w.Settings.publicWorksTerms(kind)
	if PublicWorksBountyOpen(w.Environment.TownChest, bounty, w.Settings.PublicWorksChestReserve) {
		text += " The town is paying " + coinsPhrase(bounty) + " to whoever " + PublicWorksMendVerb(kind) + " it."
	}
	w.emit(&ObjectConditionNarrated{ActorID: actor.ID, ObjectID: obj.ID, Text: text, At: now})
}

// DamageFact is the "what is broken" sentence opening for a damaged site —
// "The windlass at the Well by the Mill is down", "The storm has torn at the
// Tavern", "Boards have split and given way at the Tavern". Shared by the
// ticker, the boards, the PC's thought and the cue, so every surface names the
// damage the same way. Pure over the maps.
func DamageFact(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, assets map[AssetID]*Asset, obj *VillageObject) string {
	if PublicWorksKind(obj) == PublicWorksRoad {
		return roadObstacleFact(objects, structures, assets, obj)
	}
	site := DamageSiteLabel(objects, structures, assets, obj)
	if PublicWorksKind(obj) == PublicWorksBusiness {
		place := WithDefiniteArticle(site)
		if BusinessDamageCause(objects, obj.ID) == DebrisStateStorm {
			return "The storm has torn at " + place
		}
		return "Boards have split and given way at " + place
	}
	return "The windlass at the " + site + " is down"
}

// publicWorksTerms returns the engine-owned bounty and work seconds for
// mending an object of kind.
func (s WorldSettings) publicWorksTerms(kind string) (bounty, seconds int) {
	switch kind {
	case PublicWorksBusiness:
		return s.PublicWorksBusinessBounty, s.PublicWorksBusinessRepairSeconds
	case PublicWorksRoad:
		return s.PublicWorksRoadBounty, s.PublicWorksRoadRepairSeconds
	}
	return s.PublicWorksBounty, s.PublicWorksRepairSeconds
}

// PublicWorksTerms is publicWorksTerms over the snapshot's mirrors, so the cue
// and the ticker state the same terms StartRepair gates on.
func (s *Snapshot) PublicWorksTerms(kind string) (bounty, seconds int) {
	switch kind {
	case PublicWorksBusiness:
		return s.PublicWorksBusinessBounty, s.PublicWorksBusinessRepairSeconds
	case PublicWorksRoad:
		return s.PublicWorksRoadBounty, s.PublicWorksRoadRepairSeconds
	}
	return s.PublicWorksBounty, s.PublicWorksRepairSeconds
}

// PublicWorksMendVerb is what a hand does to a site of kind, third person —
// "mends" a well or a shop, "clears" a road.
func PublicWorksMendVerb(kind string) string {
	if kind == PublicWorksRoad {
		return "clears"
	}
	return "mends"
}

// PublicWorksMendNoun is the work itself — "the mending", or "the clearing" of
// a road.
func PublicWorksMendNoun(kind string) string {
	if kind == PublicWorksRoad {
		return "the clearing"
	}
	return "the mending"
}

// DamageTickerLine is one broken object as a line for the client's top ticker.
type DamageTickerLine struct {
	ObjectID VillageObjectID
	Text     string
}

// DamageTickerLines lists every damaged site (broken well, damaged business)
// as one ticker line, lowest id first. Pure over the snapshot (the public
// world read builds it).
func DamageTickerLines(s *Snapshot) []DamageTickerLine {
	var out []DamageTickerLine
	for _, obj := range s.VillageObjects {
		if !IsDamagedSite(obj) {
			continue
		}
		kind := PublicWorksKind(obj)
		text := DamageFact(s.VillageObjects, s.Structures, s.Assets, obj)
		bounty, _ := s.PublicWorksTerms(kind)
		switch {
		case PublicWorksBountyOpen(s.Environment.TownChest, bounty, s.PublicWorksChestReserve):
			text += " — the town pays " + coinsPhrase(bounty) + " to the hand who " + PublicWorksMendVerb(kind) + " it."
		case kind == PublicWorksRoad:
			text += " — walkers must go around it until it is cleared."
		case kind == PublicWorksBusiness:
			text += " — the town cannot pay for the mending just now."
		default:
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

// objectUnderRepair reports whether some actor already has a live town repair
// window on obj — so a second hand can't take the same job. A keeper's own
// nail-mend at a damaged shop is not the town's work and does not count.
func objectUnderRepair(w *World, objID VillageObjectID, except ActorID) bool {
	for id, a := range w.Actors {
		if id == except || a == nil || a.SourceActivity == nil {
			continue
		}
		act := a.SourceActivity
		if act.Kind == SourceActivityRepair && act.PublicWorks && act.ObjectID == objID {
			return true
		}
	}
	return false
}

// publicWorksSiteAt returns the damaged site the actor is standing at, or nil:
// a broken well by the drink path's loitering resolution, else a damaged
// business the actor is inside or at the pin of (AtBusiness — the same test the
// keeper's own mend uses), or a road obstacle the actor stands at
// (AtRoadObstacle). Lowest id wins if, against the guards, two qualify.
func publicWorksSiteAt(w *World, actor *Actor) *VillageObject {
	if _, obj := findRefreshObjectNear(w, actor.Pos); obj.IsWell() && obj.Damaged() {
		return obj
	}
	var best *VillageObject
	for _, obj := range w.VillageObjects {
		if !obj.Damaged() {
			continue
		}
		switch PublicWorksKind(obj) {
		case PublicWorksBusiness:
			pin, ok := effectiveObjectLoiterTile(w, obj.ID)
			if !AtBusiness(actor.Pos, actor.InsideStructureID, obj.ID, pin, ok) {
				continue
			}
		case PublicWorksRoad:
			if !AtRoadObstacle(actor.Pos, obj, w.Assets[obj.AssetID]) {
				continue
			}
		default:
			continue
		}
		if best == nil || obj.ID < best.ID {
			best = obj
		}
	}
	return best
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
	kind := PublicWorksKind(site)
	bounty, seconds := w.Settings.publicWorksTerms(kind)
	if !PublicWorksBountyOpen(w.Environment.TownChest, bounty, w.Settings.PublicWorksChestReserve) {
		return nil, errors.New("the town chest cannot pay for the mending just now.")
	}
	if seconds <= 0 {
		switch kind {
		case PublicWorksBusiness:
			seconds = DefaultPublicWorksBusinessRepairSeconds
		case PublicWorksRoad:
			seconds = DefaultPublicWorksRoadRepairSeconds
		default:
			seconds = DefaultPublicWorksRepairSeconds
		}
	}
	// The bounty is fixed now, on the terms the hand was offered — a live
	// retune during the work changes nothing they were promised. PublicWorks
	// marks the window as the town's, so completion pays from the chest even at
	// a business whose keeper could also mend (their own wear) there.
	actor.SourceActivity = &SourceActivity{
		Kind:        SourceActivityRepair,
		ObjectID:    site.ID,
		StartedAt:   now,
		Until:       now.Add(time.Duration(seconds) * time.Second),
		Bounty:      bounty,
		PublicWorks: true,
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
// pays the hand the bounty agreed at start from the chest (capped by what it
// holds — the start gate checked it could pay, but the wage may have drawn it
// down since). landed is false when there was nothing left to mend — the site
// was mended some other way mid-window (the operator), or is no damaged site
// at all — and then nothing is paid and the caller tells the hand nothing.
func completePublicWorksRepair(w *World, actor *Actor, obj *VillageObject, agreed int, now time.Time) (paid int, landed bool) {
	if actor == nil || !IsDamagedSite(obj) {
		return 0, false // the town pays only for a damaged well or business (nil-safe predicates)
	}
	forText := publicWorksForText(WithDefiniteArticle(damageObjectName(w, obj)))
	bounty := agreed
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
		return 0, true
	}
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          actor.ID,
		OccurredAt:       now,
		ActionType:       ActionTypeCollected,
		Text:             forText,
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
			"for":          forText,
			"public_works": true,
			"object_id":    string(obj.ID),
			"chest_after":  w.Environment.TownChest,
		},
		SpeakerName: actorDisplayNameOrID(actor),
		HuddleID:    actor.CurrentHuddleID,
		Source:      "engine",
	})
	return bounty, true
}

// PublicWorksCompletionNarration is the hand's completion beat for a town repair
// — the public-works sibling of SourceActivityCompletionNarration's stall line.
// kind is the site's PublicWorksKind; it picks what "working again" means.
func PublicWorksCompletionNarration(kind, sourceName string, paid int) string {
	place := "the well"
	restored := "it draws water again"
	work := "mending"
	switch kind {
	case PublicWorksBusiness:
		place = "the damage"
		restored = "the place is sound again"
	case PublicWorksRoad:
		place = "the fallen tree"
		restored = "the road is open again"
		work = "clearing"
		if sourceName != "" {
			sourceName = strings.ToLower(sourceName)
		}
	}
	if sourceName != "" {
		place = WithDefiniteArticle(sourceName)
	}
	if paid <= 0 {
		return "You finish " + work + " " + place + "; " + restored + ", but the town chest was empty and nobody paid you."
	}
	return "You finish " + work + " " + place + "; " + restored + ", and the town pays you " + coinsPhrase(paid) + " for the work."
}

// RollDailyDamage is the rotation-boundary hazard roll — wells and businesses,
// each on its own chance and guards. Bound to the durable boundary in
// checkAndRotate (not ApplyDailyRotation), so the umbilical force-rotate never
// rolls it and a restart does not roll twice. Returns what broke.
func RollDailyDamage(boundary time.Time, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		s := w.Settings
		return rollAllDamage(w, DamageTriggerWear, s.WellDamageChancePermille, s.BusinessDamageChancePermille, s.RoadDamageChancePermille, rng, boundary), nil
	}}
}

// RollStormDamage is the storm-start hazard roll; called inline from the
// WeatherChanged subscriber (cascade), which already runs on the world goroutine.
// Returns what broke — at most one well, one business and one road.
func RollStormDamage(w *World, rng DamageRoller, now time.Time) []*VillageObject {
	s := w.Settings
	return rollAllDamage(w, DamageTriggerStorm, s.WellDamageStormChancePermille, s.BusinessDamageStormChancePermille, s.RoadDamageStormChancePermille, rng, now)
}

func rollAllDamage(w *World, trigger string, wellPermille, businessPermille, roadPermille int, rng DamageRoller, now time.Time) []*VillageObject {
	var broke []*VillageObject
	if obj := rollDamage(w, PublicWorksWell, trigger, wellPermille, rng, now); obj != nil {
		broke = append(broke, obj)
	}
	if obj := rollDamage(w, PublicWorksBusiness, trigger, businessPermille, rng, now); obj != nil {
		broke = append(broke, obj)
	}
	if obj := rollRoadDamage(w, trigger, roadPermille, rng, now); obj != nil {
		broke = append(broke, obj)
	}
	return broke
}

// SetObjectDamage is the operator control over a well or an owned business:
// action "damage" breaks it now (no roll, no guards but the kind), "storm" the
// same with the storm trigger (a business gets the storm debris), and "repair"
// mends it without a bounty.
func SetObjectDamage(id VillageObjectID, action string) Command {
	return Command{Fn: func(w *World) (any, error) {
		obj := w.VillageObjects[id]
		if obj == nil {
			return nil, ErrVillageObjectNotFound
		}
		now := time.Now().UTC()
		if PublicWorksKind(obj) == "" {
			return nil, ErrNotDamageable
		}
		switch action {
		case "damage":
			damageObject(w, obj, DamageTriggerForce, now)
		case "storm":
			damageObject(w, obj, DamageTriggerStorm, now)
		case "repair":
			repairObject(w, obj, "", 0, now)
		default:
			return nil, ErrUnknownDamageAction
		}
		return obj.Damaged(), nil
	}}
}
