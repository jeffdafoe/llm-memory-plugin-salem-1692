package sim

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// omen_pc_internal_test.go — LLM-845: the players' daily roll, the ward
// weighting, and story chapter 2 (the omen it brings on, the horseshoe bought
// and hung, random omens opening after it).

// storyChapterTwo mirrors the LLM-898 migration's chapter 2 rows, linked after
// chapter 1.
func storyChapterTwo() map[StoryMissionID]*StoryMission {
	out := storyChapterOne()
	out["arrival-5"].NextID = "signs-1"
	for _, m := range []*StoryMission{
		{ID: "signs-1", Chapter: "Signs", Seq: 1, Title: "Go About Your Days", OfferText: "keep camp", DoneText: "it sits ill",
			Trigger: StoryTriggerOmenWitnessed, TriggerArgs: StoryTriggerArgs{Omens: []OmenKind{OmenCampFireGutters, OmenFoulWell}},
			StandingReward: 15, NextID: "signs-2"},
		{ID: "signs-2", Chapter: "Signs", Seq: 2, Title: "Ask About It", OfferText: "ask", DoneText: "a horseshoe, for luck",
			Trigger: StoryTriggerSpokeWithVillager, StandingReward: 10, NextID: "signs-3"},
		{ID: "signs-3", Chapter: "Signs", Seq: 3, Title: "A Shoe for the Door", OfferText: "buy one", DoneText: "cold iron",
			AlreadyText: "you have a horseshoe", Trigger: StoryTriggerItemAcquired, TriggerArgs: StoryTriggerArgs{Item: HorseshoeKind},
			StandingReward: 10, NextID: "signs-4"},
		{ID: "signs-4", Chapter: "Signs", Seq: 4, Title: "Over the Door", OfferText: "hang it", DoneText: "it hangs there",
			AlreadyText: "a horseshoe hangs over your door", Trigger: StoryTriggerWardPlaced, TriggerArgs: StoryTriggerArgs{Item: HorseshoeKind},
			StandingReward: 15},
	} {
		out[m.ID] = m
	}
	return out
}

// chapterTwoWorld is the story test world with both chapters, the player having
// finished chapter 1, chapter 2's first beat open, and every omen setting off.
func chapterTwoWorld(t *testing.T) (*World, *Actor, *pcEventRecorder) {
	t.Helper()
	w, pc, rec := storyTestWorld(t)
	w.StoryMissions = storyChapterTwo()
	for _, id := range []StoryMissionID{"arrival-1", "arrival-2", "arrival-3", "arrival-4", "arrival-5"} {
		if w.StoryProgress[pc.ID] == nil {
			w.StoryProgress[pc.ID] = map[StoryMissionID]*StoryProgress{}
		}
		w.StoryProgress[pc.ID][id] = &StoryProgress{MissionID: id, OfferedAt: storyNow.Add(-time.Hour),
			FinishedAt: storyNow.Add(-time.Hour), OfferSeenAt: storyNow, FinishSeenAt: storyNow}
	}
	pc.Standing = 50
	pc.StandingRankSeen = "sojourner"
	w.startStory(pc, storyNow)
	if p := w.StoryProgress[pc.ID]["signs-1"]; !p.Open() {
		t.Fatalf("signs-1 = %+v, want open after chapter 1", p)
	}
	return w, pc, rec
}

func TestStoryOmenComesWhateverTheSettings(t *testing.T) {
	w, pc, rec := chapterTwoWorld(t)
	if w.Settings.OmenChancePermille != 0 || w.Settings.OmenPCChancePermille != 0 {
		t.Fatal("the test wants every omen setting off")
	}
	if storyOmenDue(w, pc.ID, OmenLanternGutters) || storyOmenDue(w, "npc", OmenFoulWell) {
		t.Fatal("the beat brings on only its own kinds, for the player alone")
	}
	if !storyOmenDue(w, pc.ID, OmenFoulWell) || !storyOmenDue(w, pc.ID, OmenCampFireGutters) {
		t.Fatal("both of the beat's kinds are due")
	}

	at := storyNow.Add(time.Minute)
	if !takeArmedOmen(w, OmenFoulWell, pc.ID, at) {
		t.Fatal("the story omen was not claimed with the settings off")
	}
	noticeOmen(w, OmenFoulWell, pc, pc.ID, at)

	if !w.Environment.Omens.LastAt[pc.ID].Equal(at) {
		t.Errorf("cooldown stamp = %v, want %v", w.Environment.Omens.LastAt[pc.ID], at)
	}
	if p := w.StoryProgress[pc.ID]["signs-1"]; p.Open() || p.CaughtUp {
		t.Errorf("signs-1 = %+v, want finished live", p)
	}
	if !w.StoryProgress[pc.ID]["signs-2"].Open() {
		t.Error("signs-2 was not offered")
	}
	if pc.Standing != 65 {
		t.Errorf("Standing = %d, want 65", pc.Standing)
	}
	// The other kind is disarmed with the beat.
	if storyOmenDue(w, pc.ID, OmenCampFireGutters) || takeArmedOmen(w, OmenCampFireGutters, pc.ID, at) {
		t.Error("the camp fire is still due after the well claimed the beat")
	}

	// The scene first, then the beat it finished, then the next offer.
	v := w.storyViewFor(pc)
	omenKey := omenPopupKey(OmenPopup{At: at})
	if got, want := unseenKeys(v), []string{omenKey, "finish:signs-1", "offer:signs-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unseen = %v, want %v", got, want)
	}
	if v.Unseen[0].Kind != StoryUnseenOmen || v.Unseen[0].Title != "" || v.Unseen[0].Text != OmenWitnessLine(OmenFoulWell, "") {
		t.Errorf("omen popup = %+v, want the scene with no title", v.Unseen[0])
	}
	if n := len(storyChangedEvents(rec)); n != 1 {
		t.Errorf("StoryChanged sent %d times, want 1", n)
	}

	// Closing it removes it; closing it again changes nothing.
	if changed, err := w.markStorySeen(pc, omenKey, at); err != nil || !changed {
		t.Fatalf("mark seen = %v, %v", changed, err)
	}
	if changed, err := w.markStorySeen(pc, omenKey, at); err != nil || changed {
		t.Errorf("second mark seen = %v, %v; want a no-op", changed, err)
	}
	if len(w.Environment.Omens.Popups) != 0 {
		t.Errorf("popups after seen = %+v", w.Environment.Omens.Popups)
	}
}

// A random omen (no beat to finish) still sends the view, so the popup shows.
func TestRandomPlayerOmenSendsTheView(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	before := len(storyChangedEvents(rec))
	noticeOmen(w, OmenLanternGutters, pc, pc.ID, storyNow)
	changes := storyChangedEvents(rec)[before:]
	if len(changes) != 1 {
		t.Fatalf("StoryChanged sent %d times, want 1", len(changes))
	}
	if keys := unseenKeys(changes[0].View); !slices.Contains(keys, omenPopupKey(OmenPopup{At: storyNow})) {
		t.Errorf("view unseen = %v, want the omen popup", keys)
	}
}

func TestStoryHorseshoeBoughtAndHung(t *testing.T) {
	w, pc, _ := chapterTwoWorld(t)
	at := storyNow.Add(time.Minute)
	noticeOmen(w, OmenCampFireGutters, pc, pc.ID, at)
	w.emit(&Spoke{SpeakerID: pc.ID, RecipientIDs: []ActorID{"npc"}, At: at})
	if !w.StoryProgress[pc.ID]["signs-3"].Open() {
		t.Fatal("signs-3 was not offered")
	}

	// A sale that left no horseshoe in the pack does not count.
	w.emit(&PayWithItemResolved{BuyerID: pc.ID, SellerID: "npc", ItemKind: "bread", TerminalState: PayTerminalStateAccepted, At: at})
	if !w.StoryProgress[pc.ID]["signs-3"].Open() {
		t.Fatal("signs-3 finished without a horseshoe")
	}
	pc.Inventory = map[ItemKind]int{HorseshoeKind: 1}
	w.emit(&PayWithItemResolved{BuyerID: pc.ID, SellerID: "npc", ItemKind: HorseshoeKind, TerminalState: PayTerminalStateDeclined, At: at})
	if !w.StoryProgress[pc.ID]["signs-3"].Open() {
		t.Fatal("a declined sale finished signs-3")
	}
	w.emit(&PayWithItemResolved{BuyerID: pc.ID, SellerID: "npc", ItemKind: HorseshoeKind, TerminalState: PayTerminalStateAccepted, At: at})
	if p := w.StoryProgress[pc.ID]["signs-3"]; p.Open() || p.CaughtUp {
		t.Fatalf("signs-3 = %+v, want finished live", p)
	}
	if pcOmensOpen(w, pc) {
		t.Fatal("random omens opened before chapter 2 ended")
	}

	// A witch bottle is not the horseshoe the beat asks for.
	w.emit(&WardPlaced{ActorID: pc.ID, Item: WitchBottleKind, At: at})
	if !w.StoryProgress[pc.ID]["signs-4"].Open() {
		t.Fatal("a witch bottle finished signs-4")
	}
	w.emit(&WardPlaced{ActorID: pc.ID, Item: HorseshoeKind, At: at})
	if p := w.StoryProgress[pc.ID]["signs-4"]; p.Open() {
		t.Fatalf("signs-4 = %+v, want finished", p)
	}
	if pc.Standing != 100 {
		t.Errorf("Standing = %d, want 100 at the end of chapter 2", pc.Standing)
	}
	if !pcOmensOpen(w, pc) {
		t.Error("random omens did not open after chapter 2")
	}
}

// A horseshoe already in the pack, or already hung, catches the beats up when
// they are offered.
func TestStoryHorseshoeCaughtUp(t *testing.T) {
	w, pc, _ := chapterTwoWorld(t)
	tent := pitchTestTent(w, pc)
	tent.Tags = []string{TagWardHorseshoe}
	noticeOmen(w, OmenFoulWell, pc, pc.ID, storyNow)
	w.emit(&Spoke{SpeakerID: pc.ID, RecipientIDs: []ActorID{"npc"}, At: storyNow})
	for _, id := range []StoryMissionID{"signs-3", "signs-4"} {
		if p := w.StoryProgress[pc.ID][id]; p.Open() || !p.CaughtUp {
			t.Errorf("%s = %+v, want caught up", id, p)
		}
	}
	if !storyStateHolds(w, pc, w.StoryMissions["signs-3"], storyNow) {
		t.Error("a hung horseshoe does not count as one acquired")
	}
	tent.CurrentState = TentStatePacked
	if storyTentWard(w, pc, HorseshoeKind) {
		t.Error("a packed tent's ward counts as hung")
	}
}

func TestOmenWardPermille(t *testing.T) {
	w := omenTestWorld()
	tavern := w.VillageObjects["tavern"]
	cases := []struct {
		tags         []string
		shoe, bottle int
		want         int
	}{
		{nil, 0, 0, 1000},
		{[]string{TagWardHorseshoe}, 0, 0, 500},
		{[]string{TagWardWitchBottle}, 0, 0, 500},
		{[]string{TagWardHorseshoe, TagWardWitchBottle}, 0, 0, 250},
		{[]string{TagWardHorseshoe, TagWardWitchBottle}, 800, 2000, 800},
		{[]string{TagWardHorseshoe}, -5, 0, 500},
	}
	for _, c := range cases {
		tavern.Tags = append([]string{TagHearth}, c.tags...)
		w.Settings.OmenWardHorseshoePermille = c.shoe
		w.Settings.OmenWardWitchBottlePermille = c.bottle
		if got := omenWardPermille(w, "hannah"); got != c.want {
			t.Errorf("tags %v settings %d/%d: keep = %d, want %d", c.tags, c.shoe, c.bottle, got, c.want)
		}
	}
	if got := omenWardPermille(w, "john"); got != 1000 {
		t.Errorf("no home: keep = %d, want 1000", got)
	}
}

// The village's one roll passes a warded household over on a second roll, so
// the household keeps only its ward's share of the chance.
func TestRollDailyOmenWardPassesOver(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ward  float64
		armed bool
	}{{0.6, false}, {0.4, true}} {
		w := omenTestWorld()
		w.VillageObjects["tavern"].Tags = []string{TagHearth, TagWardHorseshoe}
		// chance, kind (cold_hearth: hannah alone), subject, ward.
		got := rollDailyOmen(w, &seqRoller{rolls: []float64{0, 0.25, 0, c.ward}}, now)
		if (got != nil) != c.armed {
			t.Errorf("ward roll %v: armed %+v, want armed=%v", c.ward, got, c.armed)
		}
		if got != nil && (got.Kind != OmenColdHearth || got.SubjectID != "hannah") {
			t.Errorf("armed %+v, want hannah's cold hearth", got)
		}
	}
}

func TestRollDailyPCOmens(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	finished := map[StoryMissionID]*StoryProgress{
		pcOmensOpenAfter: {MissionID: pcOmensOpenAfter, OfferedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Hour)},
	}
	build := func() *World {
		w := &World{
			Actors: map[ActorID]*Actor{
				"a-open":   {ID: "a-open", Kind: KindPC, LoginUsername: "a"},
				"b-warded": {ID: "b-warded", Kind: KindPC, LoginUsername: "b", HomeStructureID: "tent-b"},
				"c-new":    {ID: "c-new", Kind: KindPC, LoginUsername: "c"},
				"d-camp":   {ID: "d-camp", Kind: KindPC, LoginUsername: "d", Inventory: map[ItemKind]int{HornLanternKind: 1}},
			},
			VillageObjects: map[VillageObjectID]*VillageObject{
				"tent-b": {ID: "tent-b", AssetID: TentAssetID, OwnerActorID: "b-warded", CurrentState: TentStateClosed, Tags: []string{TagWardHorseshoe}},
				"tent-d": {ID: "tent-d", AssetID: TentAssetID, OwnerActorID: "d-camp", CurrentState: TentStateClosed},
				"fire-d": {ID: "fire-d", AssetID: CampFireAssetID, OwnerActorID: "d-camp"},
			},
			StoryProgress: map[ActorID]map[StoryMissionID]*StoryProgress{
				"a-open": finished, "b-warded": finished, "d-camp": finished,
				"c-new": {"signs-1": {MissionID: "signs-1", OfferedAt: now}},
			},
		}
		w.Settings.OmenPCChancePermille = 150
		return w
	}

	// Rolls by player id: a, b, d (c has not finished chapter 2). Each armed
	// player takes one more roll for the kind.
	w := build()
	got := rollDailyPCOmens(w, &seqRoller{rolls: []float64{0.1, 0.0, 0.1, 0.99}}, now)
	// a: 100 < 150, armed; kind roll 0.0 → well (well, bush). b: 100 >= 75
	// (the horseshoe halves 150), passed over. d: 990 >= 150, no omen.
	if len(got) != 1 || got[0].SubjectID != "a-open" || got[0].Kind != OmenFoulWell {
		t.Fatalf("armed = %+v, want a's foul well alone", got)
	}
	if want := now.Add(DefaultOmenLapseHours * time.Hour); !got[0].LapsesAt.Equal(want) {
		t.Errorf("lapses at %v, want %v", got[0].LapsesAt, want)
	}

	w = build()
	got = rollDailyPCOmens(w, &seqRoller{rolls: []float64{0.5, 0.05, 0.99, 0.0, 0.99}}, now)
	// a: 500, no. b: 50 < 75, armed; kind 0.99 → bush. d: 0 < 150; kind
	// 0.99 of (well, bush, camp fire, lantern) → lantern.
	if len(got) != 2 || got[0].SubjectID != "b-warded" || got[0].Kind != OmenWitheredBush ||
		got[1].SubjectID != "d-camp" || got[1].Kind != OmenLanternGutters {
		t.Fatalf("armed = %+v, want b's bush and d's lantern", got)
	}

	// Cooldown and an armed omen pass a player over; the setting at 0 is off.
	w = build()
	w.Environment.Omens.LastAt = map[ActorID]time.Time{"a-open": now.Add(-48 * time.Hour)}
	w.Environment.Omens.Armed = []ArmedOmen{{Kind: OmenFoulWell, SubjectID: "d-camp", LapsesAt: now.Add(time.Hour)}}
	if got := rollDailyPCOmens(w, &seqRoller{rolls: []float64{0}}, now); len(got) != 1 || got[0].SubjectID != "b-warded" {
		t.Errorf("armed = %+v, want b alone (a cooling down, d armed)", got)
	}
	w = build()
	w.Settings.OmenPCChancePermille = 0
	if got := rollDailyPCOmens(w, &seqRoller{rolls: []float64{0}}, now); len(got) != 0 {
		t.Errorf("armed %+v with player omens off", got)
	}
}

func TestOmenPopupsCappedAndPruned(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	for i := 0; i < maxOmenPopups+2; i++ {
		addOmenPopup(w, pc.ID, OmenPopup{Kind: OmenFoulWell, Text: "iron", At: storyNow.Add(time.Duration(i) * time.Minute)})
	}
	list := w.Environment.Omens.Popups[pc.ID]
	if len(list) != maxOmenPopups || !list[0].At.Equal(storyNow.Add(2*time.Minute)) {
		t.Errorf("popups = %+v, want the newest %d", list, maxOmenPopups)
	}
	addOmenPopup(w, "gone", OmenPopup{Kind: OmenFoulWell, At: storyNow})
	pruneOmenPopups(w)
	if _, ok := w.Environment.Omens.Popups["gone"]; ok || len(w.Environment.Omens.Popups[pc.ID]) == 0 {
		t.Errorf("prune = %+v, want the departed player's popups gone and the player's kept", w.Environment.Omens.Popups)
	}
}

func TestOmenRecordCloneCopiesPopups(t *testing.T) {
	src := OmenRecord{Popups: map[ActorID][]OmenPopup{"pc": {{Kind: OmenFoulWell, Text: "iron"}}}}
	cp := src.Clone()
	src.Popups["pc"][0].Text = "changed"
	src.Popups["other"] = nil
	if cp.Popups["pc"][0].Text != "iron" || len(cp.Popups) != 1 {
		t.Fatalf("clone aliases the source: %+v", cp)
	}
}

// A player's omen is word in the village like a villager's: the witness and
// their huddle hold it first-hand, about the player.
func TestSeedOmenRumorAboutAPlayer(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	w.Huddles = map[HuddleID]*Huddle{"h1": {ID: "h1", Members: map[ActorID]struct{}{pc.ID: {}, "npc": {}}}}
	pc.CurrentHuddleID = "h1"
	seedOmenRumor(w, OmenCampFireGutters, pc, pc.ID, storyNow)
	villager := w.Actors["npc"]
	if len(villager.Rumors) != 1 || villager.Rumors[0].SubjectID != pc.ID || villager.Rumors[0].Clause() != "Alice's camp fire would not stay lit" {
		t.Errorf("villager rumors = %+v, want Alice's camp fire", villager.Rumors)
	}
	if len(pc.Rumors) != 1 || pc.Rumors[0].ClauseFor(pc.ID) != "your camp fire would not stay lit" {
		t.Errorf("player rumors = %+v, want their own, worded to them", pc.Rumors)
	}
}
