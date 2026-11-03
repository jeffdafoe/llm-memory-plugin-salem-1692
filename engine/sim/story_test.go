package sim

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var storyNow = time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)

// storyChapterOne mirrors the LLM-892 migration's chapter 1 rows.
func storyChapterOne() map[StoryMissionID]*StoryMission {
	ms := []*StoryMission{
		{ID: "arrival-1", Chapter: "Arrival", Seq: 1, Title: "Make Camp", OfferText: "pitch it", DoneText: "tent stands",
			AlreadyText: "your tent stands", Trigger: StoryTriggerCampPitched, StandingReward: 10, NextID: "arrival-2", IsStart: true},
		{ID: "arrival-2", Chapter: "Arrival", Seq: 2, Title: "A Fire of Your Own", OfferText: "light it", DoneText: "fire holds",
			AlreadyText: "your fire burns", Trigger: StoryTriggerCampFireLit, StandingReward: 10, NextID: "arrival-3"},
		{ID: "arrival-3", Chapter: "Arrival", Seq: 3, Title: "A Night Under Canvas", OfferText: "sleep there", DoneText: "you wake",
			Trigger: StoryTriggerSleptAtHome, StandingReward: 10, NextID: "arrival-4"},
		{ID: "arrival-4", Chapter: "Arrival", Seq: 4, Title: "Bread and a Bench", OfferText: "eat", DoneText: "full",
			Trigger: StoryTriggerMealAt, TriggerArgs: StoryTriggerArgs{Tag: "lodging"}, StandingReward: 10, NextID: "arrival-5"},
		{ID: "arrival-5", Chapter: "Arrival", Seq: 5, Title: "A Word with Your Neighbors", OfferText: "talk", DoneText: "word travels",
			Trigger: StoryTriggerSpokeWithVillager, StandingReward: 10},
	}
	out := make(map[StoryMissionID]*StoryMission, len(ms))
	for _, m := range ms {
		out[m.ID] = m
	}
	return out
}

// storyTestWorld builds a world with one player ("pc", login alice), one
// villager ("npc"), the chapter 1 catalog and the story watcher.
func storyTestWorld(t *testing.T) (*World, *Actor, *pcEventRecorder) {
	t.Helper()
	pc := &Actor{ID: "pc", Kind: KindPC, LoginUsername: "alice", DisplayName: "Alice"}
	villager := &Actor{ID: "npc", Kind: KindNPCShared, LLMAgent: "npc-va", DisplayName: "Hannah"}
	w := sleepTestWorld(pc, villager)
	w.VillageObjects = make(map[VillageObjectID]*VillageObject)
	w.StoryMissions = storyChapterOne()
	w.StoryProgress = make(map[ActorID]map[StoryMissionID]*StoryProgress)
	RegisterStoryWatcher(w)
	rec := &pcEventRecorder{}
	w.Subscribe(SubscriberFunc(rec.handle))
	return w, pc, rec
}

// pitchTestTent puts the player's standing tent down and makes it their home.
func pitchTestTent(w *World, pc *Actor) *VillageObject {
	tent := &VillageObject{ID: "tent-1", AssetID: TentAssetID, OwnerActorID: pc.ID, CurrentState: TentStateClosed}
	w.VillageObjects[tent.ID] = tent
	pc.HomeStructureID = StructureID(tent.ID)
	return tent
}

func lightTestFire(w *World, pc *Actor) *VillageObject {
	fire := &VillageObject{ID: "fire-1", AssetID: CampFireAssetID, OwnerActorID: pc.ID,
		CurrentState: CampFireStateBurning, HearthLitUntil: storyNow.Add(3 * time.Hour)}
	w.VillageObjects[fire.ID] = fire
	return fire
}

func storyChangedEvents(rec *pcEventRecorder) []*StoryChanged {
	var out []*StoryChanged
	for _, e := range rec.events {
		if sc, ok := e.(*StoryChanged); ok {
			out = append(out, sc)
		}
	}
	return out
}

func unseenKeys(v StoryView) []string {
	keys := []string{}
	for _, u := range v.Unseen {
		keys = append(keys, u.Key)
	}
	return keys
}

func TestStoryStartOffersTheFirstBeat(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)

	p := w.StoryProgress[pc.ID]["arrival-1"]
	if p == nil || !p.Open() {
		t.Fatalf("arrival-1 should be offered and open, got %+v", p)
	}
	evs := storyChangedEvents(rec)
	if len(evs) != 1 || evs[0].Login != "alice" {
		t.Fatalf("want one StoryChanged for alice, got %+v", evs)
	}
	v := evs[0].View
	if v.Mission == nil || v.Mission.ID != "arrival-1" {
		t.Fatalf("open mission = %+v, want arrival-1", v.Mission)
	}
	if got := unseenKeys(v); !reflect.DeepEqual(got, []string{"offer:arrival-1"}) {
		t.Errorf("unseen = %v", got)
	}
	if v.Rank.ID != "stranger" || v.NextRank == nil || v.NextRank.ID != "sojourner" || v.NextRank.At != 50 {
		t.Errorf("rank = %+v next = %+v", v.Rank, v.NextRank)
	}

	// A second start is a no-op.
	w.StartStoryForPC(pc, storyNow)
	if len(storyChangedEvents(rec)) != 1 {
		t.Error("a second start should change nothing")
	}
}

func TestStoryStartIgnoresNonPlayers(t *testing.T) {
	w, _, rec := storyTestWorld(t)
	w.StartStoryForPC(w.Actors["npc"], storyNow)
	if len(w.StoryProgress) != 0 || len(rec.events) != 0 {
		t.Error("a villager gets no story")
	}
}

func TestStoryTentPitchedFinishesTheBeatLive(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	rec.events = nil

	pitchTestTent(w, pc)
	w.emit(&NPCHomeStructureChanged{ActorID: pc.ID, StructureID: "tent-1", At: storyNow.Add(time.Minute)})

	p := w.StoryProgress[pc.ID]["arrival-1"]
	if p.Open() || p.CaughtUp {
		t.Fatalf("arrival-1 should be finished live, got %+v", p)
	}
	if pc.Standing != 10 {
		t.Errorf("Standing = %d, want 10", pc.Standing)
	}
	if next := w.StoryProgress[pc.ID]["arrival-2"]; next == nil || !next.Open() {
		t.Fatalf("arrival-2 should be offered, got %+v", next)
	}
	evs := storyChangedEvents(rec)
	if len(evs) != 1 {
		t.Fatalf("want one StoryChanged, got %d", len(evs))
	}
	if got := unseenKeys(evs[0].View); !reflect.DeepEqual(got, []string{"finish:arrival-1", "offer:arrival-2"}) {
		t.Errorf("unseen = %v", got)
	}
	if evs[0].View.Unseen[0].StandingGain != 10 {
		t.Errorf("finish gain = %d", evs[0].View.Unseen[0].StandingGain)
	}
}

func TestStoryPackedTentDoesNotCount(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	tent := pitchTestTent(w, pc)
	tent.CurrentState = TentStatePacked
	w.emit(&NPCHomeStructureChanged{ActorID: pc.ID, StructureID: "tent-1", At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-1"].Open() {
		t.Error("a packed tent is not a pitched camp")
	}
}

func TestStoryCatchUpFinishesWhatIsAlreadyDone(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	pitchTestTent(w, pc)
	lightTestFire(w, pc)
	w.StartStoryForPC(pc, storyNow)

	for _, id := range []StoryMissionID{"arrival-1", "arrival-2"} {
		p := w.StoryProgress[pc.ID][id]
		if p == nil || p.Open() || !p.CaughtUp {
			t.Fatalf("%s should be caught up, got %+v", id, p)
		}
	}
	if !w.StoryProgress[pc.ID]["arrival-3"].Open() {
		t.Fatal("arrival-3 should be the open beat")
	}
	if pc.Standing != 20 {
		t.Errorf("Standing = %d, want 20", pc.Standing)
	}
	evs := storyChangedEvents(rec)
	if len(evs) != 1 {
		t.Fatalf("want one StoryChanged, got %d", len(evs))
	}
	v := evs[0].View
	if got := unseenKeys(v); !reflect.DeepEqual(got, []string{"catch_up", "offer:arrival-3"}) {
		t.Fatalf("unseen = %v", got)
	}
	cu := v.Unseen[0]
	if cu.StandingGain != 20 || !reflect.DeepEqual(cu.Titles, []string{"Make Camp", "A Fire of Your Own"}) {
		t.Errorf("catch_up = %+v", cu)
	}
	if want := "You have already made a start here: your tent stands and your fire burns."; cu.Text != want {
		t.Errorf("catch_up text = %q, want %q", cu.Text, want)
	}
}

func TestStoryFireLitFinishesTheBeat(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	pitchTestTent(w, pc)
	w.StartStoryForPC(pc, storyNow) // arrival-1 caught up, arrival-2 open
	fire := lightTestFire(w, pc)

	// Another player's fire, or a fire dying down, does not count.
	w.emit(&VillageObjectStateChanged{ObjectID: fire.ID, FromState: CampFireStateBurning, ToState: CampFireStateDying, At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-2"].Open() {
		t.Fatal("a fire dying down is not a fire lit")
	}
	w.emit(&VillageObjectStateChanged{ObjectID: fire.ID, FromState: CampFireStateUnlit, ToState: CampFireStateBurning, At: storyNow})
	p := w.StoryProgress[pc.ID]["arrival-2"]
	if p.Open() || p.CaughtUp {
		t.Fatalf("arrival-2 should be finished live, got %+v", p)
	}
}

func TestStorySleepAtHomeFinishesTheBeat(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	pitchTestTent(w, pc)
	lightTestFire(w, pc)
	w.StartStoryForPC(pc, storyNow) // arrival-3 open

	pc.InsideStructureID = "inn"
	w.emit(&PCSleepStarted{ActorID: pc.ID, At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-3"].Open() {
		t.Fatal("a night at the inn is not a night in the tent")
	}
	pc.InsideStructureID = pc.HomeStructureID
	w.emit(&PCSleepStarted{ActorID: pc.ID, At: storyNow})
	if w.StoryProgress[pc.ID]["arrival-3"].Open() {
		t.Fatal("arrival-3 should be finished")
	}
}

func TestStoryMealAtTheInnFinishesTheBeat(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	w.StoryProgress[pc.ID] = map[StoryMissionID]*StoryProgress{"arrival-4": {MissionID: "arrival-4", OfferedAt: storyNow}}
	w.VillageObjects["tavern"] = &VillageObject{ID: "tavern", Tags: []string{"tavern"}}
	w.VillageObjects["inn"] = &VillageObject{ID: "inn", Tags: []string{"lodging"}}

	w.emit(&DwellStarted{ActorID: pc.ID, Kind: "stew", StructureID: "tavern", At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-4"].Open() {
		t.Fatal("a meal at a place without the lodging tag does not count")
	}
	w.emit(&DwellStarted{ActorID: pc.ID, Kind: "stew", StructureID: "inn", At: storyNow})
	if w.StoryProgress[pc.ID]["arrival-4"].Open() {
		t.Fatal("arrival-4 should be finished")
	}
	if next := w.StoryProgress[pc.ID]["arrival-5"]; next == nil || !next.Open() {
		t.Fatal("arrival-5 should be offered")
	}
}

func TestStorySpeakingToAVillagerFinishesTheBeat(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	other := &Actor{ID: "pc2", Kind: KindPC, LoginUsername: "bob"}
	w.Actors[other.ID] = other
	w.StoryProgress[pc.ID] = map[StoryMissionID]*StoryProgress{"arrival-5": {MissionID: "arrival-5", OfferedAt: storyNow}}

	w.emit(&Spoke{SpeakerID: pc.ID, RecipientIDs: []ActorID{other.ID}, Text: "hello", At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-5"].Open() {
		t.Fatal("talking only to another player does not count")
	}
	w.emit(&Spoke{SpeakerID: "npc", RecipientIDs: []ActorID{pc.ID}, Text: "good day", At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-5"].Open() {
		t.Fatal("being spoken to does not count; the player must speak")
	}
	w.emit(&Spoke{SpeakerID: pc.ID, RecipientIDs: []ActorID{other.ID, "npc"}, Text: "good day", At: storyNow})
	if w.StoryProgress[pc.ID]["arrival-5"].Open() {
		t.Fatal("arrival-5 should be finished")
	}
}

func TestStoryRankUpAndSeen(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	pitchTestTent(w, pc)
	lightTestFire(w, pc)
	w.StartStoryForPC(pc, storyNow) // +20, arrival-3 open
	pc.InsideStructureID = pc.HomeStructureID
	w.emit(&PCSleepStarted{ActorID: pc.ID, At: storyNow.Add(time.Hour)})
	w.VillageObjects["inn"] = &VillageObject{ID: "inn", Tags: []string{"lodging"}}
	w.emit(&DwellStarted{ActorID: pc.ID, Kind: "stew", StructureID: "inn", At: storyNow.Add(2 * time.Hour)})
	w.emit(&Spoke{SpeakerID: pc.ID, RecipientIDs: []ActorID{"npc"}, At: storyNow.Add(3 * time.Hour)})

	if pc.Standing != 50 {
		t.Fatalf("Standing = %d, want 50", pc.Standing)
	}
	v := w.storyViewFor(pc)
	if v.Rank.ID != "sojourner" || v.Mission != nil {
		t.Fatalf("rank = %s mission = %+v", v.Rank.ID, v.Mission)
	}
	want := []string{"catch_up", "finish:arrival-3", "finish:arrival-4", "finish:arrival-5", "rank:sojourner"}
	if got := unseenKeys(v); !reflect.DeepEqual(got, want) {
		t.Fatalf("unseen = %v, want %v", got, want)
	}

	for _, key := range want {
		if _, err := MarkStorySeen(pc.ID, key, storyNow.Add(4*time.Hour)).Fn(w); err != nil {
			t.Fatalf("MarkStorySeen(%q): %v", key, err)
		}
	}
	if got := unseenKeys(w.storyViewFor(pc)); len(got) != 0 {
		t.Errorf("unseen after closing everything = %v", got)
	}
	if pc.StandingRankSeen != 1 {
		t.Errorf("StandingRankSeen = %d, want 1", pc.StandingRankSeen)
	}
}

func TestMarkStorySeen(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	rec.events = nil

	res, err := MarkStorySeen(pc.ID, " offer:arrival-1 ", storyNow).Fn(w)
	if err != nil {
		t.Fatal(err)
	}
	if v := res.(StoryView); len(v.Unseen) != 0 || v.Mission == nil {
		t.Errorf("after seen: %+v", v)
	}
	if len(storyChangedEvents(rec)) != 1 {
		t.Error("a real change emits StoryChanged")
	}

	// Already seen, unknown mission, a rank not reached: no-ops.
	for _, key := range []string{"offer:arrival-1", "finish:arrival-9", "rank:freeman", "catch_up"} {
		if _, err := MarkStorySeen(pc.ID, key, storyNow).Fn(w); err != nil {
			t.Errorf("%q: %v", key, err)
		}
	}
	if len(storyChangedEvents(rec)) != 1 {
		t.Error("a no-op emits nothing")
	}

	for _, key := range []string{"", "offer", "offer:", "bogus:arrival-1"} {
		if _, err := MarkStorySeen(pc.ID, key, storyNow).Fn(w); !errors.Is(err, ErrStoryBadKey) {
			t.Errorf("%q: err = %v, want ErrStoryBadKey", key, err)
		}
	}
	if _, err := MarkStorySeen("npc", "catch_up", storyNow).Fn(w); err == nil {
		t.Error("a villager has no story to mark")
	}
}

func TestStandingRanksAndProse(t *testing.T) {
	ranks := (&WorldSettings{}).standingRanks()
	cases := []struct {
		standing int
		rank     string
		prose    string
	}{
		{0, "stranger", "No one here knows your name."},
		{20, "stranger", "A few villagers have started to know your face."},
		{45, "stranger", "People nod to you on the road now."},
		{50, "sojourner", "The village tolerates you, for now."},
		{600, "freeman", "You have a voice at town meeting."},
		{5000, "freeman", "You have a voice at town meeting."},
	}
	for _, c := range cases {
		idx := standingRankIndex(ranks, c.standing)
		if ranks[idx].ID != c.rank {
			t.Errorf("%d: rank %s, want %s", c.standing, ranks[idx].ID, c.rank)
		}
		if got := standingProse(ranks, idx, c.standing); got != c.prose {
			t.Errorf("%d: prose %q, want %q", c.standing, got, c.prose)
		}
	}

	// A threshold set out of order is lifted, so the ladder stays ascending.
	odd := (&WorldSettings{StandingRankSojourner: 300, StandingRankInhabitant: 100, StandingRankFreeman: 200}).standingRanks()
	for i := 1; i < len(odd); i++ {
		if odd[i].At <= odd[i-1].At {
			t.Errorf("rank %s at %d is not above %s at %d", odd[i].ID, odd[i].At, odd[i-1].ID, odd[i-1].At)
		}
	}
}

func TestStoryNoCatalogIsInert(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StoryMissions = map[StoryMissionID]*StoryMission{}
	w.StartStoryForPC(pc, storyNow)
	pitchTestTent(w, pc)
	w.emit(&NPCHomeStructureChanged{ActorID: pc.ID, StructureID: "tent-1", At: storyNow})
	if len(storyChangedEvents(rec)) != 0 || pc.Standing != 0 {
		t.Error("with no catalog the story does nothing")
	}
}

// A chapter appended after a player finished the last beat reaches them at the
// next load: the finished beat's new next beat is offered.
func TestStoryStartOffersANextBeatAddedLater(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	w.StoryMissions = map[StoryMissionID]*StoryMission{
		"arrival-5": {ID: "arrival-5", Chapter: "Arrival", Seq: 5, Trigger: StoryTriggerSpokeWithVillager, StandingReward: 10, NextID: "parish-1"},
		"parish-1":  {ID: "parish-1", Chapter: "Parish", Seq: 1, Trigger: StoryTriggerSpokeWithVillager, StandingReward: 10},
	}
	w.StoryProgress[pc.ID] = map[StoryMissionID]*StoryProgress{
		"arrival-5": {MissionID: "arrival-5", OfferedAt: storyNow.Add(-time.Hour), FinishedAt: storyNow.Add(-time.Minute), FinishSeenAt: storyNow},
	}
	if !w.startStory(pc, storyNow) {
		t.Fatal("startStory should report the new offer")
	}
	if p := w.StoryProgress[pc.ID]["parish-1"]; p == nil || !p.Open() {
		t.Fatalf("parish-1 = %+v, want offered", p)
	}
	if w.startStory(pc, storyNow) {
		t.Error("a second pass changes nothing")
	}
}

// A dwell with no item is not a meal (code_review; no emitter sends one today).
func TestStoryMealNeedsAnItem(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	w.StoryProgress[pc.ID] = map[StoryMissionID]*StoryProgress{"arrival-4": {MissionID: "arrival-4", OfferedAt: storyNow}}
	w.VillageObjects["inn"] = &VillageObject{ID: "inn", Tags: []string{"lodging"}}
	w.emit(&DwellStarted{ActorID: pc.ID, StructureID: "inn", At: storyNow})
	if !w.StoryProgress[pc.ID]["arrival-4"].Open() {
		t.Error("a dwell with no item kind should not finish the meal beat")
	}
}

// Two chapters with equal seq and equal timestamps order the same way every
// time: by chapter, then seq, then id (code_review).
func TestStoryViewOrderIsDeterministic(t *testing.T) {
	missions := map[StoryMissionID]*StoryMission{
		"b-1": {ID: "b-1", Chapter: "Bridge", Seq: 1, Title: "B one", AlreadyText: "b"},
		"a-1": {ID: "a-1", Chapter: "Arrival", Seq: 1, Title: "A one", AlreadyText: "a"},
		"b-2": {ID: "b-2", Chapter: "Bridge", Seq: 2, Title: "B two"},
		"a-2": {ID: "a-2", Chapter: "Arrival", Seq: 2, Title: "A two"},
	}
	rows := map[StoryMissionID]*StoryProgress{
		"a-1": {MissionID: "a-1", OfferedAt: storyNow, FinishedAt: storyNow, CaughtUp: true},
		"b-1": {MissionID: "b-1", OfferedAt: storyNow, FinishedAt: storyNow, CaughtUp: true},
		"a-2": {MissionID: "a-2", OfferedAt: storyNow},
		"b-2": {MissionID: "b-2", OfferedAt: storyNow},
	}
	ranks := (&WorldSettings{}).standingRanks()
	for i := 0; i < 50; i++ {
		v := BuildStoryView(missions, rows, 0, 0, ranks)
		if v.Mission == nil || v.Mission.ID != "a-2" {
			t.Fatalf("run %d: open mission = %+v, want a-2", i, v.Mission)
		}
		if got := unseenKeys(v); !reflect.DeepEqual(got, []string{"catch_up", "offer:a-2", "offer:b-2"}) {
			t.Fatalf("run %d: unseen = %v", i, got)
		}
		if !reflect.DeepEqual(v.Unseen[0].Titles, []string{"A one", "B one"}) {
			t.Fatalf("run %d: catch-up titles = %v", i, v.Unseen[0].Titles)
		}
	}
}
