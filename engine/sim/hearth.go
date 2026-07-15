package sim

import "time"

// hearth.go — LLM-412. The hearth: a structure's fireplace, the superior
// relief for the cold need (cold.go) and the first sink for firewood. Built
// on the business wear/repair shape (stall_wear.go) with a different input:
// the fire burns down where the stall wears, firewood restores it where nails
// do, and stoking is a timed source-activity where repair is.
//
// A hearth is the STRUCTURE'S OWN village_object tagged TagHearth — the same
// structure-backed convention businesses use (structure and object share an
// id), and the same operator opt-in flow (/object/add-tag). The fire state is
// one durable timestamp, HearthLitUntil: lit means "in the future," and there
// is no burn-down sweep — stoking pushes the instant out, the clock brings
// the fire down. A structure with a lit hearth is warm: its occupants take no
// cold and recover what chill they carry (coldRatePerMinuteX100).

// TagHearth marks a structure-backed village_object as having a fireplace
// (applied by an operator via the editor / umbilical /object/add-tag, the
// same flow as TagBusiness). Only tagged objects hold fire state, render
// hearth cues, or accept the stoke tool.
const TagHearth = "hearth"

// FirewoodItemKind is the canonical fuel a stoke consumes — foraged by
// Ezekiel today, tradeable by anyone. Seeded in the item catalog; before
// LLM-412 it satisfied no need and fed no recipe (a dead item — the point of
// the ticket is to give it this sink).
const FirewoodItemKind ItemKind = "firewood"

// Default WorldSettings knobs for the hearth (LLM-412). Live-tunable via the
// setting table. At the defaults one stick of firewood keeps a fire in for
// three hours, a stoke feeds it one stick over half a minute, a fire can be
// banked at most twelve hours ahead (about an evening plus a night — you
// can't buy a season of warmth in an afternoon), and "the fire is low" means
// under an hour left on the clock.
const (
	DefaultHearthBurnMinutesPerWood = 180
	DefaultHearthMaxBankMinutes     = 720
	DefaultHearthLowMinutes         = 60
	DefaultStokeWoodPerStoke        = 1
	DefaultStokeDurationSeconds     = 30
)

// IsHearth reports whether obj is a hearth-bearing structure object — the
// scope gate for all fire state, the stoke tool, and the hearth cues.
// Nil-safe. Unlike IsWearableStall it does NOT require an owner: an ownerless
// hearth still warms its structure (relief must stay free); ownership only
// decides who is cued/warranted to stoke it.
func IsHearth(obj *VillageObject) bool {
	return obj != nil && obj.HasTag(TagHearth)
}

// HearthRemaining returns how much burn time the fire has left as of now.
// Zero (never negative) when the fire is out or obj isn't a hearth.
func HearthRemaining(obj *VillageObject, now time.Time) time.Duration {
	if !IsHearth(obj) || !obj.HearthLitUntil.After(now) {
		return 0
	}
	return obj.HearthLitUntil.Sub(now)
}

// HearthLit reports whether the hearth's fire is burning as of now.
func HearthLit(obj *VillageObject, now time.Time) bool {
	return HearthRemaining(obj, now) > 0
}

// HearthNeedsStoking reports whether the fire is out or within lowMinutes of
// burning out — the boundary the stoke cue, the stoke command's "worth
// stoking" gate, and the storm-time warrant all share so they can't drift.
// A non-positive lowMinutes still treats an OUT fire as needing stoking (a
// dead fire always wants wood; the knob only tunes how early "low" starts).
func HearthNeedsStoking(obj *VillageObject, now time.Time, lowMinutes int) bool {
	if !IsHearth(obj) {
		return false
	}
	remaining := HearthRemaining(obj, now)
	if remaining <= 0 {
		return true // a dead fire always wants wood, whatever the low knob says
	}
	return remaining < time.Duration(lowMinutes)*time.Minute
}

// StructureHearth returns the hearth object of the structure, or nil when the
// structure has none. Structure-backed convention: the hearth is the
// structure's own village_object (shared id) tagged TagHearth — the same
// id-sharing every business surface relies on (sim.AtBusiness). Takes the
// object map so it serves both the live World and a perception Snapshot.
func StructureHearth(objects map[VillageObjectID]*VillageObject, structureID StructureID) *VillageObject {
	if structureID == "" {
		return nil
	}
	obj := objects[VillageObjectID(structureID)]
	if !IsHearth(obj) {
		return nil
	}
	return obj
}

// OwnedHearth returns the hearth object owned by actorID, or nil when they
// own none. Same one-per-owner convention as OwnedWearableStall (first match
// wins; every live keeper has at most one premises).
func OwnedHearth(objects map[VillageObjectID]*VillageObject, actorID ActorID) *VillageObject {
	if actorID == "" {
		return nil
	}
	for _, obj := range objects {
		if obj.OwnerActorID == actorID && IsHearth(obj) {
			return obj
		}
	}
	return nil
}

// HearthToStoke returns the hearth the actor is responsible for keeping in,
// and whether they reach it through a hire rather than ownership — the exact
// shape of WearableStallToMend (LLM-271), because the responsibility question
// is the same: the owner first, else a worker actively Working a hired job
// stokes the hearth their EMPLOYER owns ("tend the fire" is the labor design
// note's own worked example — the contract stays {employer, reward, duration},
// no task field; the worker simply keeps his hands). Only the Working state
// qualifies; deterministic lowest-LaborID tie-break for the same reason as the
// stall resolver. Shared by the perception cue, the stoke command, and the
// hired warrant stamp so they can't drift on who may stoke.
func HearthToStoke(objects map[VillageObjectID]*VillageObject, ledger map[LaborID]*LaborOffer, actorID ActorID) (hearth *VillageObject, hired bool) {
	if own := OwnedHearth(objects, actorID); own != nil {
		return own, false
	}
	var best *LaborOffer
	for _, o := range ledger {
		if o == nil || o.State != LaborStateWorking || o.WorkerID != actorID {
			continue
		}
		if best == nil || o.ID < best.ID {
			best = o
		}
	}
	if best == nil {
		return nil, false
	}
	if employerHearth := OwnedHearth(objects, best.EmployerID); employerHearth != nil {
		return employerHearth, true
	}
	return nil, false
}

// StokeFireOn extends the hearth's fire by wood sticks' worth of burn time,
// from now if the fire is out or from its current end if still burning,
// capped at maxBankMinutes ahead of now. Returns the new HearthLitUntil.
// Pure helper over the object — the stoke completion (source_activity.go)
// applies it on the world goroutine.
func StokeFireOn(obj *VillageObject, wood int, now time.Time, burnMinutesPerWood, maxBankMinutes int) time.Time {
	if obj == nil || wood <= 0 || burnMinutesPerWood <= 0 {
		return obj.HearthLitUntil
	}
	base := now
	if obj.HearthLitUntil.After(now) {
		base = obj.HearthLitUntil
	}
	lit := base.Add(time.Duration(wood*burnMinutesPerWood) * time.Minute)
	if maxBankMinutes > 0 {
		if bank := now.Add(time.Duration(maxBankMinutes) * time.Minute); lit.After(bank) {
			lit = bank
		}
	}
	obj.HearthLitUntil = lit
	return lit
}

// HearthLowWarrantReason is stamped on a hearth's owner while a storm is
// running and their fire is out or low (LLM-412) — the wake that gets fires
// lit when the sky turns, since organic cold accrual indoors is deliberately
// too slow to wake anyone inside a default-length storm. Level-triggered from
// the cold exposure sweep (AdjustCold), bounded by the per-actor
// WarrantedSince gate exactly like the need-threshold producer. HearthID is
// the hearth object, carried so the warrant line can name the place.
// DedupDiscriminator returns 0 — a low fire is a state condition, not an
// event — matching StallRepairWarrantReason.
type HearthLowWarrantReason struct {
	HearthID VillageObjectID
}

func (HearthLowWarrantReason) isWarrantReason()           {}
func (HearthLowWarrantReason) Kind() WarrantKind          { return WarrantKindHearthLow }
func (HearthLowWarrantReason) DedupDiscriminator() uint64 { return 0 }

// HearthStokeHiredWarrantReason is the hired-worker twin of
// HearthLowWarrantReason — the exact shape of StallRepairHiredWarrantReason
// (LLM-271), for the same two reasons: the reactor's laboring shelve-gate
// singles it out as an interrupt (a StateLaboring worker is otherwise
// shelved, so the surfaced stoke tool would never draw a tick), and the
// warrant line renders hired-framed. Stamped one-shot at startLaborWork when
// the employer's hearth already wants stoking during a storm.
type HearthStokeHiredWarrantReason struct {
	HearthID VillageObjectID
}

func (HearthStokeHiredWarrantReason) isWarrantReason()           {}
func (HearthStokeHiredWarrantReason) Kind() WarrantKind          { return WarrantKindHearthStokeHired }
func (HearthStokeHiredWarrantReason) DedupDiscriminator() uint64 { return 0 }

// maybeStampHiredHearthWarrant wakes a just-hired worker to stoke their
// employer's hearth when a storm is running and the fire wants wood (LLM-412)
// — the hearth twin of maybeStampHiredRepairWarrant. Called from
// startLaborWork once the worker is on-post. Storm-gated where the repair
// twin is not: a worn stall matters in any weather, but a dead fire only
// matters while the sky is pressing cold into the room. One-shot; the
// standing hearth cue still reminds them on any later tick they draw.
// World-goroutine-only.
func maybeStampHiredHearthWarrant(w *World, worker, employer *Actor, at time.Time) {
	if w == nil || worker == nil || employer == nil {
		return
	}
	if w.Environment.Weather != WeatherStorm {
		return
	}
	hearth := OwnedHearth(w.VillageObjects, employer.ID)
	if hearth == nil || !HearthNeedsStoking(hearth, at, w.Settings.HearthLowMinutes) {
		return
	}
	tryStampWarrant(w, worker, WarrantMeta{
		TriggerActorID: worker.ID,
		Reason:         HearthStokeHiredWarrantReason{HearthID: hearth.ID},
	}, at)
}
