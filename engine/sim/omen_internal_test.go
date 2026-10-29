package sim

import (
	"strings"
	"testing"
	"time"
)

// omen_internal_test.go — LLM-843: the daily omen roll, the claim, and the two
// rumor-rule exceptions the omen topics carry.

// seqRoller replays fixed rolls, then repeats the last.
type seqRoller struct {
	rolls []float64
	i     int
}

func (r *seqRoller) Float64() float64 {
	if r.i >= len(r.rolls) {
		return r.rolls[len(r.rolls)-1]
	}
	v := r.rolls[r.i]
	r.i++
	return v
}

// omenTestWorld: three residents — a milker, a hearth owner with a home, a
// plain villager — plus a cow, a visitor and a player who must never be chosen.
func omenTestWorld() *World {
	avail, max := 4, 4
	w := &World{
		Actors: map[ActorID]*Actor{
			"elizabeth": {ID: "elizabeth", DisplayName: "Elizabeth Ellis", Kind: KindNPCShared,
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "milk", Source: RestockSourceForage, Max: 30}}}},
			"hannah": {ID: "hannah", DisplayName: "Hannah Boggs", Kind: KindNPCStateful, HomeStructureID: "tavern"},
			"john":   {ID: "john", DisplayName: "John Ellis", Kind: KindNPCShared},
			"cow":    {ID: "cow", Kind: KindDecorative, Yield: &LivestockYield{Row: ObjectRefresh{GatherItem: "milk", AvailableQuantity: &avail, MaxQuantity: &max}}},
			"vstr-1": {ID: "vstr-1", Kind: KindNPCShared, VisitorState: &VisitorState{}},
			"pc":     {ID: "pc", Kind: KindPC},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"tavern": {ID: "tavern", OwnerActorID: "hannah", Tags: []string{TagHearth}},
		},
		Huddles: map[HuddleID]*Huddle{},
	}
	w.Settings.OmenChancePermille = 1000
	return w
}

func TestOmenCandidates(t *testing.T) {
	w := omenTestWorld()
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	got := omenCandidates(w, now)
	want := map[OmenKind][]ActorID{
		OmenSourMilk:   {"elizabeth"},
		OmenColdHearth: {"hannah"},
		OmenFlatBread:  {"hannah"},
		OmenFoulWell:   {"elizabeth", "hannah", "john"},
	}
	for _, k := range omenKinds {
		if strings.Join(idStrings(got[k]), ",") != strings.Join(idStrings(want[k]), ",") {
			t.Errorf("%s candidates = %v, want %v", k, got[k], want[k])
		}
	}

	// Cooldown and an already-armed omen both take a resident out of the draw.
	w.Environment.Omens.LastAt = map[ActorID]time.Time{"john": now.Add(-48 * time.Hour)}
	w.Environment.Omens.Armed = []ArmedOmen{{Kind: OmenFoulWell, SubjectID: "elizabeth", LapsesAt: now.Add(time.Hour)}}
	got = omenCandidates(w, now)
	if ids := idStrings(got[OmenFoulWell]); strings.Join(ids, ",") != "hannah" {
		t.Errorf("well candidates with john cooling down and elizabeth armed = %v, want [hannah]", ids)
	}
	if len(got[OmenSourMilk]) != 0 {
		t.Errorf("sour milk candidates = %v, want none (elizabeth already armed)", got[OmenSourMilk])
	}
	// The cooldown ends.
	w.Environment.Omens.LastAt["john"] = now.Add(-8 * 24 * time.Hour)
	if ids := idStrings(omenCandidates(w, now)[OmenFoulWell]); strings.Join(ids, ",") != "hannah,john" {
		t.Errorf("well candidates after john's cooldown = %v, want [hannah john]", ids)
	}
}

func idStrings(ids []ActorID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func TestRollDailyOmen(t *testing.T) {
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

	t.Run("off at chance 0", func(t *testing.T) {
		w := omenTestWorld()
		w.Settings.OmenChancePermille = 0
		if got := rollDailyOmen(w, &seqRoller{rolls: []float64{0}}, now); got != nil {
			t.Fatalf("armed %+v with omens off", got)
		}
	})

	t.Run("a roll above the chance arms nothing", func(t *testing.T) {
		w := omenTestWorld()
		w.Settings.OmenChancePermille = 250
		if got := rollDailyOmen(w, &seqRoller{rolls: []float64{0.25}}, now); got != nil {
			t.Fatalf("armed %+v on a 250 roll against 250 permille", got)
		}
	})

	t.Run("kind first, then subject", func(t *testing.T) {
		w := omenTestWorld()
		// Rolls: chance, kind index, subject index. Kinds with candidates, in
		// omenKinds order: sour_milk, cold_hearth, flat_bread, foul_well — 0.99
		// picks foul_well; 0.5 of its three subjects picks hannah.
		got := rollDailyOmen(w, &seqRoller{rolls: []float64{0, 0.99, 0.5}}, now)
		if got == nil || got.Kind != OmenFoulWell || got.SubjectID != "hannah" {
			t.Fatalf("armed %+v, want foul_well against hannah", got)
		}
		if !got.LapsesAt.Equal(now.Add(DefaultOmenLapseHours * time.Hour)) {
			t.Errorf("LapsesAt = %v, want the default lapse", got.LapsesAt)
		}
		if len(w.Environment.Omens.Armed) != 1 {
			t.Errorf("armed record = %+v, want the one omen", w.Environment.Omens.Armed)
		}
	})

	t.Run("a lapsed omen is pruned at the next roll", func(t *testing.T) {
		w := omenTestWorld()
		w.Settings.OmenChancePermille = 0
		w.Environment.Omens.Armed = []ArmedOmen{{Kind: OmenSourMilk, SubjectID: "elizabeth", LapsesAt: now}}
		rollDailyOmen(w, &seqRoller{rolls: []float64{0}}, now)
		if len(w.Environment.Omens.Armed) != 0 {
			t.Errorf("lapsed omen survived: %+v", w.Environment.Omens.Armed)
		}
	})
}

func TestTakeArmedOmen(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	w := omenTestWorld()
	w.Environment.Omens.Armed = []ArmedOmen{
		{Kind: OmenSourMilk, SubjectID: "elizabeth", LapsesAt: now.Add(time.Hour)},
		{Kind: OmenFoulWell, SubjectID: "john", LapsesAt: now.Add(time.Hour)},
	}
	if takeArmedOmen(w, OmenFoulWell, "elizabeth", now) {
		t.Fatal("claimed a kind not armed against her")
	}
	if !takeArmedOmen(w, OmenSourMilk, "elizabeth", now) {
		t.Fatal("did not claim her armed omen")
	}
	if takeArmedOmen(w, OmenSourMilk, "elizabeth", now) {
		t.Fatal("claimed the same omen twice")
	}
	if len(w.Environment.Omens.Armed) != 1 || w.Environment.Omens.Armed[0].SubjectID != "john" {
		t.Errorf("armed after claim = %+v, want john's alone", w.Environment.Omens.Armed)
	}
	if !w.Environment.Omens.LastAt["elizabeth"].Equal(now) {
		t.Errorf("cooldown stamp = %v, want %v", w.Environment.Omens.LastAt["elizabeth"], now)
	}
	if takeArmedOmen(w, OmenFoulWell, "john", now.Add(2*time.Hour)) {
		t.Error("claimed a lapsed omen")
	}
}

func TestOmenRecordCloneIsDeep(t *testing.T) {
	src := OmenRecord{
		Armed:  []ArmedOmen{{Kind: OmenSourMilk, SubjectID: "elizabeth"}},
		LastAt: map[ActorID]time.Time{"john": time.Unix(1, 0)},
	}
	cp := src.Clone()
	src.Armed[0].SubjectID = "changed"
	src.LastAt["john"] = time.Unix(2, 0)
	if cp.Armed[0].SubjectID != "elizabeth" || !cp.LastAt["john"].Equal(time.Unix(1, 0)) {
		t.Fatalf("clone aliases the source: %+v", cp)
	}
}

// TestOmenRumorExceptions — the two rumor rules relax for omen topics only: the
// subject may hold its own omen (worded to them), and an omen is shared with its
// subject standing in the huddle. short_on_coin keeps both rules.
func TestOmenRumorExceptions(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	w := omenTestWorld()
	eliz := w.Actors["elizabeth"]

	seedOmenRumor(w, OmenSourMilk, eliz, "elizabeth", now)
	if len(eliz.Rumors) != 1 || !eliz.Rumors[0].FirstHand {
		t.Fatalf("witness-subject rumors = %+v, want her own omen first-hand", eliz.Rumors)
	}
	if got := eliz.Rumors[0].ClauseFor("elizabeth"); got != "your milk turned sour in the pail" {
		t.Errorf("self clause = %q", got)
	}
	if got := eliz.Rumors[0].ClauseFor("john"); got != "Elizabeth Ellis's milk turned sour in the pail" {
		t.Errorf("other clause = %q", got)
	}

	coin := newShortOnCoinRumor("elizabeth", "Elizabeth Ellis", now)
	if eliz.learnRumor(coin, now) {
		t.Error("a short_on_coin rumor about herself was learned")
	}

	h := &Huddle{Members: map[ActorID]struct{}{"elizabeth": {}, "john": {}}}
	if r, ok := eliz.salientRumorToShare(h, now); !ok || r.Topic != RumorTopicOmenSourMilk {
		t.Errorf("omen not shared with its subject present: %+v ok=%v", r, ok)
	}
	john := w.Actors["john"]
	john.learnRumor(coin, now)
	if _, ok := john.salientRumorToShare(h, now); ok {
		t.Error("short_on_coin shared to its subject's face")
	}
}

// TestSeedOmenRumorReachesTheHuddle — everyone in the witness's conversation saw
// it too, and carries it first-hand.
func TestSeedOmenRumorReachesTheHuddle(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	w := omenTestWorld()
	w.Huddles["h1"] = &Huddle{Members: map[ActorID]struct{}{"hannah": {}, "john": {}, "cow": {}}}
	w.Actors["hannah"].CurrentHuddleID = "h1"
	seedOmenRumor(w, OmenColdHearth, w.Actors["hannah"], "hannah", now)
	if r := w.Actors["john"].Rumors; len(r) != 1 || !r[0].FirstHand || r[0].SubjectID != "hannah" {
		t.Errorf("john rumors = %+v, want hannah's cold hearth first-hand", r)
	}
	if len(w.Actors["cow"].Rumors) != 0 {
		t.Errorf("a decorative actor learned the omen: %+v", w.Actors["cow"].Rumors)
	}
}

// TestOmenTextNeverSaysWitch — the engine never names witchcraft (LLM-841's
// rule; the same word messenger news refuses). Covers every line the engine
// writes for an omen: the witness line, the log text, and both ladders.
func TestOmenTextNeverSaysWitch(t *testing.T) {
	var texts []string
	for _, k := range omenKinds {
		texts = append(texts, OmenWitnessLine(k, "the well"), OmenWitnessLine(k, ""), OmenLogText(k))
		topic := omenRumorTopic(k)
		if ladders[topic] == nil || selfLadders[topic] == nil {
			t.Fatalf("omen %s has no rumor ladder", k)
		}
		for rung := 0; rung <= MaxRumorRung; rung++ {
			texts = append(texts, ladders[topic][rung], selfLadders[topic][rung])
		}
		if OmenWitnessLine(k, "") == "" || OmenLogText(k) == "" {
			t.Errorf("omen %s has no witness line or log text", k)
		}
	}
	for _, s := range texts {
		if strings.Contains(strings.ToLower(s), "witch") {
			t.Errorf("omen text names witchcraft: %q", s)
		}
	}
}

// TestOmenSettingsOnTheUmbilical — the three knobs are registry settings, so GET
// /umbilical/settings reports them under `all`, POST /settings/set can change
// them, and the checkpoint persists them.
func TestOmenSettingsOnTheUmbilical(t *testing.T) {
	ws := &WorldSettings{OmenChancePermille: 250, OmenCooldownDays: 7, OmenLapseHours: 24}
	all := ReadAllSettings(ws)
	persisted := PersistableSettingRows(ws)
	for key, want := range map[string]string{"omen_chance_permille": "250", "omen_cooldown_days": "7", "omen_lapse_hours": "24"} {
		if all[key] != want {
			t.Errorf("settings `all`[%s] = %q, want %q", key, all[key], want)
		}
		if _, ok := persisted[key]; !ok {
			t.Errorf("%s is not persisted at the checkpoint", key)
		}
		spec, ok := SettingSpecByKey(key)
		if !ok {
			t.Fatalf("%s is not settable", key)
		}
		if err := spec.Apply(ws, "3"); err != nil || spec.Read(ws) != "3" {
			t.Errorf("settings/set %s = 3 failed: err=%v read=%q", key, err, spec.Read(ws))
		}
	}
}

// TestOmenCandidatesHouseholdFairness — the cooldown and the armed exclusion are
// by household: once one Ellis had an omen (or has one armed), the other is out
// of the draw too, while a neighbour in another house is not.
func TestOmenCandidatesHouseholdFairness(t *testing.T) {
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	w := omenTestWorld()
	w.Actors["elizabeth"].HomeStructureID = "ellis_farm"
	w.Actors["john"].HomeStructureID = "ellis_farm"

	w.Environment.Omens.LastAt = map[ActorID]time.Time{"elizabeth": now.Add(-24 * time.Hour)}
	if ids := idStrings(omenCandidates(w, now)[OmenFoulWell]); strings.Join(ids, ",") != "hannah" {
		t.Errorf("well candidates a day after Elizabeth's omen = %v, want [hannah] (John shares her house)", ids)
	}

	w.Environment.Omens.LastAt = nil
	w.Environment.Omens.Armed = []ArmedOmen{{Kind: OmenSourMilk, SubjectID: "john", LapsesAt: now.Add(time.Hour)}}
	if ids := idStrings(omenCandidates(w, now)[OmenFoulWell]); strings.Join(ids, ",") != "hannah" {
		t.Errorf("well candidates with John armed = %v, want [hannah]", ids)
	}
}

// TestCheckpointCopiesTheOmenRecord — SaveWorld encodes the checkpoint off the
// world goroutine, so the record it carries must not share the live map/slice.
func TestCheckpointCopiesTheOmenRecord(t *testing.T) {
	w := omenTestWorld()
	w.Environment.Omens = OmenRecord{
		Armed:  []ArmedOmen{{Kind: OmenSourMilk, SubjectID: "elizabeth"}},
		LastAt: map[ActorID]time.Time{"john": time.Unix(1, 0)},
	}
	cp := w.BuildCheckpointSnapshot()
	w.Environment.Omens.Armed[0].SubjectID = "changed"
	w.Environment.Omens.LastAt["hannah"] = time.Unix(2, 0)
	if cp.Environment.Omens.Armed[0].SubjectID != "elizabeth" || len(cp.Environment.Omens.LastAt) != 1 {
		t.Fatalf("checkpoint shares the live omen record: %+v", cp.Environment.Omens)
	}
}
