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
