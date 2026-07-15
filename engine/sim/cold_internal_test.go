package sim

import (
	"testing"
	"time"
)

// cold_internal_test.go — LLM-412. Internal tests for the cold exposure model
// (coldRatePerMinuteX100 / AdjustCold), the hearth substrate helpers, the
// registry integration (IncrementNeedsTick skip + actionable-red-need guard),
// and the production sap.

// coldTestWorld builds a hand-rolled world with one agent NPC, an optional
// hearth-tagged tavern object, and the LLM-412 default rates.
func coldTestWorld(weather string, phase Phase) (*World, *Actor, *VillageObject) {
	actor := &Actor{
		ID: "lewis", Kind: KindNPCStateful, LLMAgent: "lewis",
		Needs: map[NeedKey]int{ColdNeedKey: 0},
	}
	hearth := &VillageObject{
		ID:           "tavern",
		OwnerActorID: "hannah",
		Tags:         []string{TagBusiness, TagHearth},
	}
	w := &World{
		Settings: WorldSettings{
			ColdStormOutdoorsPerMinuteX100: DefaultColdStormOutdoorsPerMinuteX100,
			ColdStormIndoorsPerMinuteX100:  DefaultColdStormIndoorsPerMinuteX100,
			ColdWarmGarmentPerMinuteX100:   DefaultColdWarmGarmentPerMinuteX100,
			ColdNightMultiplierX100:        DefaultColdNightMultiplierX100,
			ColdWarmRecoveryPerMinuteX100:  DefaultColdWarmRecoveryPerMinuteX100,
			ColdClearRecoveryPerMinuteX100: DefaultColdClearRecoveryPerMinuteX100,
			HearthLowMinutes:               DefaultHearthLowMinutes,
			NeedThresholds:                 DefaultNeedThresholds(),
			MaxWarrantsPerActor:            16,
		},
		Environment:    WorldEnvironment{Weather: weather},
		Phase:          phase,
		Actors:         map[ActorID]*Actor{actor.ID: actor},
		VillageObjects: map[VillageObjectID]*VillageObject{hearth.ID: hearth},
	}
	return w, actor, hearth
}

func TestColdRatePerMinuteX100_ExposureMatrix(t *testing.T) {
	now := time.Now().UTC()

	// Storm, outdoors, day: full accrual.
	w, a, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormOutdoorsPerMinuteX100 {
		t.Errorf("storm outdoors day = %d, want %d", got, DefaultColdStormOutdoorsPerMinuteX100)
	}

	// Storm, outdoors, night: multiplied.
	w.Phase = PhaseNight
	wantNight := DefaultColdStormOutdoorsPerMinuteX100 * DefaultColdNightMultiplierX100 / 100
	if got := coldRatePerMinuteX100(w, a, now); got != wantNight {
		t.Errorf("storm outdoors night = %d, want %d", got, wantNight)
	}

	// Storm, indoors unheated: the slow roofed rate.
	w.Phase = PhaseDay
	a.InsideStructureID = "tavern"
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormIndoorsPerMinuteX100 {
		t.Errorf("storm indoors unheated = %d, want %d", got, DefaultColdStormIndoorsPerMinuteX100)
	}

	// Storm, indoors, hearth LIT: warm — fast recovery beats the weather.
	hearth.HearthLitUntil = now.Add(time.Hour)
	if got := coldRatePerMinuteX100(w, a, now); got != -DefaultColdWarmRecoveryPerMinuteX100 {
		t.Errorf("warm by fire = %d, want %d", got, -DefaultColdWarmRecoveryPerMinuteX100)
	}

	// Clear sky, not warm: slow ambient recovery.
	hearth.HearthLitUntil = time.Time{}
	w.Environment.Weather = WeatherClear
	if got := coldRatePerMinuteX100(w, a, now); got != -DefaultColdClearRecoveryPerMinuteX100 {
		t.Errorf("clear unwarmed = %d, want %d", got, -DefaultColdClearRecoveryPerMinuteX100)
	}
}

// warmGarmentCatalog is the minimal item catalog for the warm-garment tests:
// one kind carrying the warms capability, one without. Local to the test so the
// cold mechanic's coverage never depends on the production clothing catalog
// (LLM-410 slice 2 seeds that) — the relief is data-driven on the capability,
// not on any named kind.
func warmGarmentCatalog() map[ItemKind]*ItemKindDef {
	return map[ItemKind]*ItemKindDef{
		"coat":  {Name: "coat", DisplayLabel: "coat", Category: "clothing", Capabilities: []string{CapabilityWarms}},
		"bread": {Name: "bread", DisplayLabel: "bread", Category: ItemCategoryFood},
	}
}

func TestActorHasWarmGarment(t *testing.T) {
	w, a, _ := coldTestWorld(WeatherStorm, PhaseDay)
	w.ItemKinds = warmGarmentCatalog()

	// No inventory → false.
	if actorHasWarmGarment(w, a) {
		t.Errorf("empty inventory reads as holding a warm garment")
	}
	// A non-warms good → false.
	a.Inventory = map[ItemKind]int{"bread": 3}
	if actorHasWarmGarment(w, a) {
		t.Errorf("bread counted as a warm garment")
	}
	// A warms garment at qty>0 → true.
	a.Inventory = map[ItemKind]int{"coat": 1}
	if !actorHasWarmGarment(w, a) {
		t.Errorf("a held coat not recognized as a warm garment")
	}
	// A spent line (qty 0) → false.
	a.Inventory = map[ItemKind]int{"coat": 0}
	if actorHasWarmGarment(w, a) {
		t.Errorf("a zero-qty coat counted")
	}
	// A kind absent from the catalog → false (no def carries the capability).
	a.Inventory = map[ItemKind]int{"phantom": 2}
	if actorHasWarmGarment(w, a) {
		t.Errorf("an uncatalogued kind counted as a warm garment")
	}
}

// TestColdRatePerMinuteX100_WarmGarment pins the LLM-410 relief branch: a warm
// garment caps outdoor storm accrual at the garment rate (min-only — never
// raises), stacks with the night multiplier, is moot under a roof that already
// caps lower, and loses to a lit fire; the 0-rate off switch and an above-range
// misconfig are both covered.
func TestColdRatePerMinuteX100_WarmGarment(t *testing.T) {
	now := time.Now().UTC()
	w, a, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	w.ItemKinds = warmGarmentCatalog()
	a.Inventory = map[ItemKind]int{"coat": 1}

	// Storm, outdoors, coated: capped at the garment rate, not the full outdoor rate.
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdWarmGarmentPerMinuteX100 {
		t.Errorf("coated storm outdoors = %d, want %d (the coat is your roof)", got, DefaultColdWarmGarmentPerMinuteX100)
	}
	// Same spot, uncoated: full outdoor accrual — the coat is doing the work.
	a.Inventory = nil
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormOutdoorsPerMinuteX100 {
		t.Errorf("uncoated storm outdoors = %d, want %d", got, DefaultColdStormOutdoorsPerMinuteX100)
	}

	// Coated, outdoors, night: the garment rate takes the night multiplier like any
	// accrual (25 * 150 / 100 = 37) — still far below the uncoated night rate.
	a.Inventory = map[ItemKind]int{"coat": 1}
	w.Phase = PhaseNight
	wantNight := DefaultColdWarmGarmentPerMinuteX100 * DefaultColdNightMultiplierX100 / 100
	if got := coldRatePerMinuteX100(w, a, now); got != wantNight {
		t.Errorf("coated storm outdoors night = %d, want %d", got, wantNight)
	}
	w.Phase = PhaseDay

	// Coated, indoors under a roof: OUTDOORS-ONLY, so the coat doesn't apply — the
	// rate is the plain indoor rate (a roof is not a coat's job).
	a.InsideStructureID = "tavern"
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormIndoorsPerMinuteX100 {
		t.Errorf("coated storm indoors = %d, want %d (coat is outdoors-only)", got, DefaultColdStormIndoorsPerMinuteX100)
	}
	// Pin the outdoors-only gate where it would otherwise bite: an indoor rate ABOVE
	// the garment rate must NOT be lowered by a coat.
	w.Settings.ColdStormIndoorsPerMinuteX100 = 50
	if got := coldRatePerMinuteX100(w, a, now); got != 50 {
		t.Errorf("coated indoors, indoor rate 50 = %d, want 50 (coat must not lower an indoor rate)", got)
	}
	w.Settings.ColdStormIndoorsPerMinuteX100 = DefaultColdStormIndoorsPerMinuteX100

	// Coated, indoors, hearth lit: warmth (fast recovery) still beats a coat.
	hearth.HearthLitUntil = now.Add(time.Hour)
	if got := coldRatePerMinuteX100(w, a, now); got != -DefaultColdWarmRecoveryPerMinuteX100 {
		t.Errorf("coated by a lit fire = %d, want %d (a fire beats a coat)", got, -DefaultColdWarmRecoveryPerMinuteX100)
	}
	hearth.HearthLitUntil = time.Time{}
	a.InsideStructureID = ""

	// Off-switch: a garment rate of 0 makes a coat FULL outdoor relief (rate 0),
	// mirroring the outdoors==0 off switch.
	w.Settings.ColdWarmGarmentPerMinuteX100 = 0
	if got := coldRatePerMinuteX100(w, a, now); got != 0 {
		t.Errorf("garment-rate-0 coated outdoors = %d, want 0 (full relief)", got)
	}

	// Misconfigured ABOVE the outdoor rate: a coat only ever LOWERS accrual, so it's
	// ignored here — a garment never makes the wearer colder.
	w.Settings.ColdWarmGarmentPerMinuteX100 = DefaultColdStormOutdoorsPerMinuteX100 + 50
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormOutdoorsPerMinuteX100 {
		t.Errorf("garment rate above outdoors = %d, want %d (a coat never raises accrual)", got, DefaultColdStormOutdoorsPerMinuteX100)
	}

	// Misconfigured NEGATIVE: a coat can't turn a storm into active recovery — the
	// negative setting is ignored, leaving the full outdoor accrual.
	w.Settings.ColdWarmGarmentPerMinuteX100 = -10
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormOutdoorsPerMinuteX100 {
		t.Errorf("negative garment rate = %d, want %d (ignored, never recovery)", got, DefaultColdStormOutdoorsPerMinuteX100)
	}
}

// TestAdjustCold_CoatKeepsWorkerOutside runs a coated and an uncoated worker
// through a default 15-minute storm outdoors, end to end through the sweep: the
// uncoated one climbs toward red while the coated one barely chills — the
// LLM-410 "keep working outside" demand loop.
func TestAdjustCold_CoatKeepsWorkerOutside(t *testing.T) {
	w, uncoated, _ := coldTestWorld(WeatherStorm, PhaseDay)
	w.ItemKinds = warmGarmentCatalog()
	coated := &Actor{
		ID: "coated", Kind: KindNPCStateful, LLMAgent: "coated",
		Needs: map[NeedKey]int{ColdNeedKey: 0}, Inventory: map[ItemKind]int{"coat": 1},
	}
	w.Actors[coated.ID] = coated

	// 15 outdoor storm minutes: uncoated accrues 15 (100 x100/min), coated accrues 3
	// (25 x100/min → 3 whole units + 75 carry). Same storm, same spot — the coat is
	// the whole difference.
	if _, err := AdjustCold(15).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if uncoated.Needs[ColdNeedKey] != 15 {
		t.Errorf("uncoated after 15 storm min = %d, want 15", uncoated.Needs[ColdNeedKey])
	}
	if coated.Needs[ColdNeedKey] != 3 {
		t.Errorf("coated after 15 storm min = %d, want 3 (the coat holds the chill off)", coated.Needs[ColdNeedKey])
	}
}

func TestAdjustCold_AccrualCarryAndClamp(t *testing.T) {
	w, a, _ := coldTestWorld(WeatherStorm, PhaseDay)
	a.InsideStructureID = "tavern" // indoors unheated: 25 x100/min

	// One minute: +25 carry, no whole unit yet.
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 0 || a.ColdCarryX100 != 25 {
		t.Fatalf("after 1 min: cold=%d carry=%d, want 0/25", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}
	// Three more minutes: carry reaches 100 → one unit lands, carry resets.
	for i := 0; i < 3; i++ {
		if _, err := AdjustCold(1).Fn(w); err != nil {
			t.Fatalf("AdjustCold: %v", err)
		}
	}
	if a.Needs[ColdNeedKey] != 1 || a.ColdCarryX100 != 0 {
		t.Fatalf("after 4 min: cold=%d carry=%d, want 1/0", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}

	// Outdoors at the full rate, a long stretch clamps at NeedMax.
	a.InsideStructureID = ""
	if _, err := AdjustCold(60).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != NeedMax {
		t.Fatalf("after 60 storm minutes outdoors: cold=%d, want clamped %d", a.Needs[ColdNeedKey], NeedMax)
	}
}

func TestAdjustCold_RecoveryWhenWarmAndFloor(t *testing.T) {
	w, a, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	a.InsideStructureID = "tavern"
	hearth.HearthLitUntil = time.Now().UTC().Add(2 * time.Hour)
	a.Needs[ColdNeedKey] = 4

	// 2/min recovery: two minutes strip four units and floor at 0.
	if _, err := AdjustCold(2).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 0 {
		t.Fatalf("cold=%d, want 0 after warm recovery", a.Needs[ColdNeedKey])
	}
	// Already at 0 under a clear sky: the carry stays reset, no drift below 0.
	w.Environment.Weather = WeatherClear
	hearth.HearthLitUntil = time.Time{}
	if _, err := AdjustCold(10).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 0 || a.ColdCarryX100 != 0 {
		t.Fatalf("cold=%d carry=%d, want 0/0 (no recovery drift below zero)", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}
}

// TestAdjustCold_CarrySignFlip pins the carry's behavior when the rate
// changes sign with a non-zero remainder (code_review): Go's integer division
// truncates toward zero, so the remainder keeps the carry's sign, |carry|
// stays < 100 after every apply, and a direction flip works the leftover
// fraction off in the new direction rather than minting a spurious unit.
func TestAdjustCold_CarrySignFlip(t *testing.T) {
	w, a, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	a.InsideStructureID = "tavern" // unheated: +25 x100/min

	// Accrue a positive remainder: 2 min → carry +50, no unit.
	if _, err := AdjustCold(2).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	a.Needs[ColdNeedKey] = 5 // give it something to recover
	if a.ColdCarryX100 != 50 {
		t.Fatalf("carry = %d, want +50", a.ColdCarryX100)
	}

	// Flip to warm recovery (-200/min): +50 - 200 = -150 → one whole unit down,
	// remainder -50. No spurious unit from the flip; carry bounded.
	hearth.HearthLitUntil = time.Now().UTC().Add(time.Hour)
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 4 || a.ColdCarryX100 != -50 {
		t.Fatalf("after flip to recovery: cold=%d carry=%d, want 4/-50", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}

	// Flip back to accrual mid-negative-remainder: -50 + 25 = -25 → no unit,
	// remainder still negative and bounded.
	hearth.HearthLitUntil = time.Time{}
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 4 || a.ColdCarryX100 != -25 {
		t.Fatalf("after flip to accrual: cold=%d carry=%d, want 4/-25", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}

	// Large clamped interval still leaves a bounded remainder: outdoors at
	// night (+150/min) for 30 min = +4500 → +45 units minus the -25 remainder
	// carried in: 4475 → 44 units + carry 75; the need clamps at NeedMax.
	a.InsideStructureID = ""
	w.Phase = PhaseNight
	if _, err := AdjustCold(30).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != NeedMax {
		t.Errorf("cold = %d, want clamped %d", a.Needs[ColdNeedKey], NeedMax)
	}
	if a.ColdCarryX100 <= -100 || a.ColdCarryX100 >= 100 {
		t.Errorf("carry = %d, want bounded in (-100, 100)", a.ColdCarryX100)
	}

	// Recovery from zero with a stale positive remainder is discarded (the
	// carry-reset branch), so cold can never drift below 0 nor re-mint from a
	// leftover fraction.
	a.Needs[ColdNeedKey] = 0
	a.ColdCarryX100 = 99
	w.Environment.Weather = WeatherClear
	w.Phase = PhaseDay
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 0 || a.ColdCarryX100 != 0 {
		t.Errorf("at-zero recovery: cold=%d carry=%d, want 0/0 (stale remainder discarded)", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}
}

func TestAdjustCold_SkipsDecoratives(t *testing.T) {
	w, a, _ := coldTestWorld(WeatherStorm, PhaseDay)
	a.LLMAgent = "" // decorative: no agent, no login
	if _, err := AdjustCold(10).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if a.Needs[ColdNeedKey] != 0 || a.ColdCarryX100 != 0 {
		t.Fatalf("decorative accrued cold: %d/%d", a.Needs[ColdNeedKey], a.ColdCarryX100)
	}
}

func TestAdjustCold_StormWakesHearthOwner(t *testing.T) {
	w, _, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	owner := &Actor{ID: "hannah", Kind: KindNPCStateful, LLMAgent: "hannah", Needs: map[NeedKey]int{}}
	w.Actors[owner.ID] = owner

	// Storm + fire out → the owner is warranted to stoke.
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if !hasWarrantKind(owner, WarrantKindHearthLow) {
		t.Fatalf("storm + dead hearth: owner not warranted")
	}

	// Already warranted → the WarrantedSince gate suppresses a re-stamp (no growth).
	warrants := len(owner.Warrants)
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if len(owner.Warrants) != warrants {
		t.Fatalf("re-stamped while WarrantedSince open: %d -> %d warrants", warrants, len(owner.Warrants))
	}

	// Fire burning well → no warrant.
	owner.Warrants = nil
	owner.WarrantedSince = nil
	hearth.HearthLitUntil = time.Now().UTC().Add(3 * time.Hour)
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if hasWarrantKind(owner, WarrantKindHearthLow) {
		t.Fatalf("lit hearth: owner warranted anyway")
	}

	// Clear sky → no warrant even with the fire out.
	hearth.HearthLitUntil = time.Time{}
	w.Environment.Weather = WeatherClear
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if hasWarrantKind(owner, WarrantKindHearthLow) {
		t.Fatalf("clear sky: owner warranted for a dead fire")
	}

	// Cleared-and-consumed → the level trigger RE-stamps on a later sweep while
	// the condition holds (code_review: the zero DedupDiscriminator must read
	// as "no discriminator" — bypassing source-key dedup — not as a shared key
	// that would collapse or suppress a fresh stamp after the first is gone).
	w.Environment.Weather = WeatherStorm
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if !hasWarrantKind(owner, WarrantKindHearthLow) {
		t.Fatalf("storm resumed: owner not warranted")
	}
	owner.Warrants = nil
	owner.WarrantedSince = nil // the tick consumed the warrant and closed the cycle
	if _, err := AdjustCold(1).Fn(w); err != nil {
		t.Fatalf("AdjustCold: %v", err)
	}
	if !hasWarrantKind(owner, WarrantKindHearthLow) {
		t.Fatalf("condition still holds after consume: owner not RE-warranted (zero discriminator collapsed?)")
	}
}

func TestIncrementNeedsTick_SkipsExternallyDrivenCold(t *testing.T) {
	w, a, _ := coldTestWorld(WeatherClear, PhaseDay)
	w.Settings.NeedsTickAmount = 1
	a.Needs["hunger"] = 5
	a.Needs[ColdNeedKey] = 5
	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if a.Needs["hunger"] != 6 {
		t.Errorf("hunger = %d, want 6 (normal hourly accrual)", a.Needs["hunger"])
	}
	if a.Needs[ColdNeedKey] != 5 {
		t.Errorf("cold = %d, want 5 (externally driven — the hourly tick must not touch it)", a.Needs[ColdNeedKey])
	}
}

func TestActorActionableRedNeed_ColdOnlyWhileAccruing(t *testing.T) {
	now := time.Now().UTC()
	w, a, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	a.Needs[ColdNeedKey] = NeedMax // red-or-worse however thresholds are set

	// Storm + outdoors: accruing → actionable.
	if key, ok := actorActionableRedNeed(w, a, now, 720); !ok || key != ColdNeedKey {
		t.Fatalf("accruing red cold not actionable: key=%q ok=%v", key, ok)
	}
	// Warm by a lit fire: recovering → NOT actionable (no churn wake).
	a.InsideStructureID = "tavern"
	hearth.HearthLitUntil = now.Add(time.Hour)
	if key, ok := actorActionableRedNeed(w, a, now, 720); ok {
		t.Fatalf("recovering red cold actionable: key=%q", key)
	}
	// Clear sky outdoors: recovering → NOT actionable.
	a.InsideStructureID = ""
	hearth.HearthLitUntil = time.Time{}
	w.Environment.Weather = WeatherClear
	if key, ok := actorActionableRedNeed(w, a, now, 720); ok {
		t.Fatalf("clear-sky red cold actionable: key=%q", key)
	}
}

func TestProduceRateScalePct_ColdSap(t *testing.T) {
	w, a, _ := coldTestWorld(WeatherStorm, PhaseDay)
	w.Settings.ColdProduceSapPct = DefaultColdProduceSapPct

	// Not cold: base rate.
	if got := produceRateScalePct(w, a.ID, a); got != 100 {
		t.Errorf("warm keeper scale = %d, want 100", got)
	}
	// Red-or-worse cold: sapped to half.
	a.Needs[ColdNeedKey] = w.Settings.NeedThresholds.Get(ColdNeedKey)
	if got := produceRateScalePct(w, a.ID, a); got != 50 {
		t.Errorf("cold keeper scale = %d, want 50", got)
	}
	// Sap off (0): untouched.
	w.Settings.ColdProduceSapPct = 0
	if got := produceRateScalePct(w, a.ID, a); got != 100 {
		t.Errorf("sap-off scale = %d, want 100", got)
	}
}

// --- hearth substrate helpers -------------------------------------------

func TestHearthHelpers_Boundaries(t *testing.T) {
	now := time.Now().UTC()
	hearth := &VillageObject{ID: "tavern", Tags: []string{TagHearth}}
	plain := &VillageObject{ID: "shed"}

	if HearthLit(plain, now) || HearthLit(nil, now) {
		t.Errorf("non-hearth/nil reads lit")
	}
	if HearthLit(hearth, now) {
		t.Errorf("zero HearthLitUntil reads lit")
	}
	if !HearthNeedsStoking(hearth, now, 60) {
		t.Errorf("dead fire doesn't need stoking")
	}
	if HearthNeedsStoking(plain, now, 60) {
		t.Errorf("non-hearth needs stoking")
	}
	// Low boundary: remaining < lowMinutes.
	hearth.HearthLitUntil = now.Add(30 * time.Minute)
	if !HearthLit(hearth, now) || !HearthNeedsStoking(hearth, now, 60) {
		t.Errorf("embers (30m left, low=60m): lit=%v needsStoking=%v, want true/true",
			HearthLit(hearth, now), HearthNeedsStoking(hearth, now, 60))
	}
	hearth.HearthLitUntil = now.Add(90 * time.Minute)
	if HearthNeedsStoking(hearth, now, 60) {
		t.Errorf("well-banked fire (90m left) needs stoking")
	}
	// Non-positive lowMinutes: only an OUT fire wants wood.
	if HearthNeedsStoking(hearth, now, 0) {
		t.Errorf("lit fire needs stoking at lowMinutes=0")
	}
	hearth.HearthLitUntil = time.Time{}
	if !HearthNeedsStoking(hearth, now, 0) {
		t.Errorf("dead fire doesn't need stoking at lowMinutes=0")
	}
}

func TestStokeFireOn_ExtendAndCap(t *testing.T) {
	now := time.Now().UTC()
	hearth := &VillageObject{ID: "tavern", Tags: []string{TagHearth}}

	// Dead fire: extends from now.
	got := StokeFireOn(hearth, 1, now, 180, 720)
	if want := now.Add(180 * time.Minute); !got.Equal(want) {
		t.Errorf("stoke from dead = %v, want %v", got, want)
	}
	// Burning fire: extends from its current end.
	got = StokeFireOn(hearth, 1, now, 180, 720)
	if want := now.Add(360 * time.Minute); !got.Equal(want) {
		t.Errorf("stoke while lit = %v, want %v", got, want)
	}
	// Bank cap: can't push past now + maxBank.
	got = StokeFireOn(hearth, 10, now, 180, 720)
	if want := now.Add(720 * time.Minute); !got.Equal(want) {
		t.Errorf("stoke past cap = %v, want capped %v", got, want)
	}
	// No-op guards.
	before := hearth.HearthLitUntil
	if got := StokeFireOn(hearth, 0, now, 180, 720); !got.Equal(before) {
		t.Errorf("zero-wood stoke moved the fire")
	}
}

func TestHearthToStoke_OwnerThenHire(t *testing.T) {
	hearth := &VillageObject{ID: "tavern", OwnerActorID: "hannah", Tags: []string{TagHearth}}
	objects := map[VillageObjectID]*VillageObject{hearth.ID: hearth}
	ledger := map[LaborID]*LaborOffer{
		1: {ID: 1, WorkerID: "anne", EmployerID: "hannah", State: LaborStateWorking},
		2: {ID: 2, WorkerID: "lewis", EmployerID: "hannah", State: LaborStatePending},
	}

	if got, hired := HearthToStoke(objects, ledger, "hannah"); got != hearth || hired {
		t.Errorf("owner: got %v hired=%v, want own hearth hired=false", got, hired)
	}
	if got, hired := HearthToStoke(objects, ledger, "anne"); got != hearth || !hired {
		t.Errorf("working hire: got %v hired=%v, want employer hearth hired=true", got, hired)
	}
	if got, _ := HearthToStoke(objects, ledger, "lewis"); got != nil {
		t.Errorf("pending offer resolved a hearth: %v", got)
	}
	// nil-safety: a stray nil map entry must not panic (code_review).
	objects["ghost"] = nil
	if got, hired := HearthToStoke(objects, ledger, "hannah"); got != hearth || hired {
		t.Errorf("nil map entry broke owner resolution: got %v hired=%v", got, hired)
	}
}

// TestHearthToStoke_MultiOfferFollowsStallResolver pins the tie-break under a
// broken single-live-job invariant (code_review): with two simultaneous
// Working offers, the resolver follows the LOWEST LaborID's employer — the
// exact WearableStallToMend semantics — even when that employer owns no
// hearth and a higher-ID offer's employer does. One job, one post, one set of
// responsibilities; the repair and stoke resolvers must never pick different
// employers for the same worker.
func TestHearthToStoke_MultiOfferFollowsStallResolver(t *testing.T) {
	hearth := &VillageObject{ID: "tavern", OwnerActorID: "hannah", Tags: []string{TagHearth}}
	objects := map[VillageObjectID]*VillageObject{hearth.ID: hearth}
	ledger := map[LaborID]*LaborOffer{
		// Lowest-ID job: employer josiah owns NO hearth.
		1: {ID: 1, WorkerID: "anne", EmployerID: "josiah", State: LaborStateWorking},
		// Higher-ID job: employer hannah owns one.
		2: {ID: 2, WorkerID: "anne", EmployerID: "hannah", State: LaborStateWorking},
	}
	if got, _ := HearthToStoke(objects, ledger, "anne"); got != nil {
		t.Errorf("multi-offer resolver scanned past the lowest-ID job: got %v, want nil (josiah owns no hearth)", got)
	}
}

func TestMaybeStampHiredHearthWarrant_StormGated(t *testing.T) {
	now := time.Now().UTC()
	w, _, hearth := coldTestWorld(WeatherStorm, PhaseDay)
	worker := &Actor{ID: "anne", Kind: KindNPCStateful, LLMAgent: "anne", Needs: map[NeedKey]int{}}
	employer := &Actor{ID: "hannah", Kind: KindNPCStateful, LLMAgent: "hannah", Needs: map[NeedKey]int{}}
	w.Actors[worker.ID] = worker
	w.Actors[employer.ID] = employer
	_ = hearth // dead fire, owned by hannah

	// Storm + dead employer hearth → stamped.
	maybeStampHiredHearthWarrant(w, worker, employer, now)
	if !hasWarrantKind(worker, WarrantKindHearthStokeHired) {
		t.Fatalf("storm + dead hearth: hired worker not warranted")
	}

	// Clear sky → not stamped (a dead fire only matters while the sky presses).
	worker2 := &Actor{ID: "mary", Kind: KindNPCStateful, LLMAgent: "mary", Needs: map[NeedKey]int{}}
	w.Actors[worker2.ID] = worker2
	w.Environment.Weather = WeatherClear
	maybeStampHiredHearthWarrant(w, worker2, employer, now)
	if hasWarrantKind(worker2, WarrantKindHearthStokeHired) {
		t.Fatalf("clear sky: hired worker warranted anyway")
	}
}
