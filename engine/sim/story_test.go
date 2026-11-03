package sim

import (
	"errors"
	"math"
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

// storyTestLadder is a four-rung ladder (the shape standing_rank loads) with
// the thresholds the tests reckon from.
func storyTestLadder() []StandingRank {
	return NormalizeStandingRanks([]StandingRank{
		{ID: "stranger", Name: "Stranger", Prose: []string{
			"No one here knows your name.",
			"A few villagers have started to know your face.",
			"People nod to you on the road now.",
		}},
		{ID: "sojourner", Name: "Sojourner", At: 50, Text: "The town has marked you a Sojourner.",
			DownText: "You are a stranger here again.", Prose: []string{
				"The village tolerates you, for now.",
				"Folk have started to ask after you.",
				"You are spoken of as one who may stay.",
			}},
		{ID: "inhabitant", Name: "Inhabitant", At: 150, Text: "Admitted as an Inhabitant.",
			DownText: "No longer among its Inhabitants.", Prose: []string{"Counted among the town's people."}},
		{ID: "freeman", Name: "Freeman", At: 750, Text: "A Freeman now.",
			DownText: "Your voice at town meeting is taken away.", Prose: []string{"You have a voice at town meeting."}},
	})
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
	w.StandingRanks = storyTestLadder()
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
	if pc.StandingRankSeen != "sojourner" {
		t.Errorf("StandingRankSeen = %q, want sojourner", pc.StandingRankSeen)
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
	ranks := storyTestLadder()
	cases := []struct {
		standing int
		rank     string
		prose    string
	}{
		{0, "stranger", "No one here knows your name."},
		{20, "stranger", "A few villagers have started to know your face."},
		{45, "stranger", "People nod to you on the road now."},
		{50, "sojourner", "The village tolerates you, for now."},
		{149, "sojourner", "You are spoken of as one who may stay."},
		{750, "freeman", "You have a voice at town meeting."},
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

}

// LLM-895: whatever the table holds, the ladder comes out lowest first,
// strictly ascending, with a bottom rank at 0.
func TestNormalizeStandingRanks(t *testing.T) {
	ids := func(rs []StandingRank) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	got := NormalizeStandingRanks([]StandingRank{
		{ID: "freeman", At: 750}, {ID: "stranger", At: 0}, {ID: "b", At: 50}, {ID: "a", At: 50},
	})
	if !reflect.DeepEqual(ids(got), []string{"stranger", "a", "b", "freeman"}) {
		t.Fatalf("order = %v, want by threshold then id", ids(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At <= got[i-1].At {
			t.Errorf("%s at %d is not above %s at %d", got[i].ID, got[i].At, got[i-1].ID, got[i-1].At)
		}
	}
	if got[2].At != 51 {
		t.Errorf("a tie is lifted one above: b at %d, want 51", got[2].At)
	}
	if got := NormalizeStandingRanks([]StandingRank{{ID: "sojourner", At: 50}}); !reflect.DeepEqual(ids(got), []string{"stranger", "sojourner"}) || got[0].At != 0 {
		t.Errorf("no rank at 0: %+v, want a Stranger added at 0", got)
	}
	if got := NormalizeStandingRanks(nil); len(got) != 1 || got[0].ID != StandingRankStranger {
		t.Errorf("empty ladder = %+v, want the bottom rank alone", got)
	}
	// Two ranks at MaxStanding: the second cannot be lifted, so it is left out
	// rather than placed where no Standing can reach (code_review R2).
	top := []StandingRank{{ID: "stranger"}, {ID: "a", At: MaxStanding}, {ID: "b", At: MaxStanding}}
	if got := NormalizeStandingRanks(top); !reflect.DeepEqual(ids(got), []string{"stranger", "a"}) || got[1].At != MaxStanding {
		t.Errorf("ranks at the top = %+v, want b left out", got)
	}
	if StandingRanksFit(top) || !StandingRanksFit(storyTestLadder()) {
		t.Error("StandingRanksFit: false only when a rank is left out")
	}
	w, _, _ := storyTestWorld(t)
	w.StandingRanks = nil
	if r := w.standingRanks(); len(r) != 1 || r[0].ID != StandingRankStranger {
		t.Errorf("a world with no ladder = %+v", r)
	}
}

// rankPopup is the view's rank-up or rank-down popup, or the zero item.
func rankPopup(v StoryView) StoryUnseenItem {
	for _, u := range v.Unseen {
		if u.Kind == StoryUnseenRank || u.Kind == StoryUnseenRankDown {
			return u
		}
	}
	return StoryUnseenItem{}
}

// LLM-895: Standing falls, the rank follows, and the player hears it once.
// Climbing back is announced again.
func TestStandingFallsAndRankFollows(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	pc.Standing = 160
	pc.StandingRankSeen = "inhabitant"
	rec.events = nil

	res, err := AdjustStanding(pc.ID, -120, "test", storyNow).Fn(w)
	if err != nil {
		t.Fatal(err)
	}
	adj := res.(StandingAdjusted)
	if adj.Applied != -120 || pc.Standing != 40 || adj.View.Rank.ID != "stranger" {
		t.Fatalf("applied %d standing %d rank %s", adj.Applied, pc.Standing, adj.View.Rank.ID)
	}
	if len(storyChangedEvents(rec)) != 1 {
		t.Error("a fall emits StoryChanged")
	}
	down := rankPopup(adj.View)
	if down.Key != "rank_down:stranger" || down.Kind != StoryUnseenRankDown || down.Title != "Stranger" ||
		down.Text != "No longer among its Inhabitants." {
		t.Fatalf("rank-down popup = %+v, want the Inhabitant line lost, titled for the rank now held", down)
	}

	// A stale key (the rank they no longer hold) changes nothing.
	if _, err := MarkStorySeen(pc.ID, "rank:inhabitant", storyNow).Fn(w); err != nil || pc.StandingRankSeen != "inhabitant" {
		t.Errorf("stale key moved the mark to %q (err %v)", pc.StandingRankSeen, err)
	}
	if _, err := MarkStorySeen(pc.ID, "rank_down:stranger", storyNow).Fn(w); err != nil || pc.StandingRankSeen != "stranger" {
		t.Fatalf("closing the popup: mark %q err %v", pc.StandingRankSeen, err)
	}
	if u := rankPopup(w.storyViewFor(pc)); u.Key != "" {
		t.Errorf("rank popup still waiting: %+v", u)
	}

	// Below 0 is clamped; the change applied is what it could be.
	res, _ = AdjustStanding(pc.ID, -500, "test", storyNow).Fn(w)
	if a := res.(StandingAdjusted); a.Applied != -40 || pc.Standing != 0 {
		t.Errorf("clamped: applied %d standing %d, want -40 and 0", a.Applied, pc.Standing)
	}
	rec.events = nil
	res, _ = AdjustStanding(pc.ID, -5, "test", storyNow).Fn(w)
	if a := res.(StandingAdjusted); a.Applied != 0 || len(storyChangedEvents(rec)) != 0 {
		t.Errorf("no change: applied %d, %d events", a.Applied, len(storyChangedEvents(rec)))
	}

	// Back up two ranks: one rank-up popup, for where they stand now.
	res, _ = AdjustStanding(pc.ID, 200, "test", storyNow).Fn(w)
	v := res.(StandingAdjusted).View
	if up := rankPopup(v); up.Key != "rank:inhabitant" || up.Text != "Admitted as an Inhabitant." {
		t.Errorf("climb back = %+v, want the Inhabitant rank-up", up)
	}

	if _, err := AdjustStanding("npc", 5, "test", storyNow).Fn(w); !errors.Is(err, ErrStoryNotPlayer) {
		t.Errorf("a villager: err %v", err)
	}
	if _, err := AdjustStanding("nobody", 5, "test", storyNow).Fn(w); !errors.Is(err, ErrActorNotFound) {
		t.Errorf("unknown actor: err %v", err)
	}
}

// LLM-895: the mark is a rank id, so a rank added or removed never moves a
// player onto the wrong one.
func TestRankSeenIsAnID(t *testing.T) {
	ladder := storyTestLadder()
	view := func(standing int, seen string, ranks []StandingRank) []string {
		var out []string
		for _, u := range BuildStoryView(nil, nil, standing, seen, ranks, nil).Unseen {
			out = append(out, u.Key)
		}
		return out
	}
	if got := view(60, "sojourner", ladder); len(got) != 0 {
		t.Errorf("at the rank seen: %v", got)
	}
	if got := view(60, "", ladder); !reflect.DeepEqual(got, []string{"rank:sojourner"}) {
		t.Errorf("empty mark reads as the bottom rank: %v", got)
	}
	if got := view(0, "", ladder); len(got) != 0 {
		t.Errorf("a new player has nothing to hear: %v", got)
	}
	if got := view(60, "gone", ladder); len(got) != 0 {
		t.Errorf("a removed rank never shows a popup by itself: %v", got)
	}
	// A rank inserted below the one seen changes nothing for a player above it.
	inserted := NormalizeStandingRanks(append(append([]StandingRank(nil), ladder...), StandingRank{ID: "newcomer", Name: "Newcomer", At: 20}))
	if got := view(160, "inhabitant", inserted); len(got) != 0 {
		t.Errorf("after an insert below: %v", got)
	}
}

// LLM-895: the live ladder is rebuilt from the raw table rows, so successive
// edits that collide end where a fresh load of the same rows would (code_review
// R1: reusing lifted values made a restart move players between ranks).
func TestReplaceStandingRanksMatchesALoad(t *testing.T) {
	w, pc, rec := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	pc.Standing = 150
	pc.StandingRankSeen = "inhabitant"
	raw := []StandingRank{
		{ID: "stranger", Name: "Stranger"},
		{ID: "sojourner", Name: "Sojourner", At: 50},
		{ID: "inhabitant", Name: "Inhabitant", At: 150},
		{ID: "freeman", Name: "Freeman", At: 750},
	}
	rec.events = nil

	edit := func(id string, at int) []StandingRank {
		t.Helper()
		for i := range raw {
			if raw[i].ID == id {
				raw[i].At = at
			}
		}
		res, err := ReplaceStandingRanks(append([]StandingRank(nil), raw...), storyNow).Fn(w)
		if err != nil {
			t.Fatal(err)
		}
		return res.([]StandingRank)
	}
	edit("sojourner", 150)
	live := edit("inhabitant", 200)
	if want := NormalizeStandingRanks(raw); !reflect.DeepEqual(live, want) {
		t.Fatalf("live ladder %+v, a load of the same rows gives %+v", live, want)
	}
	if v := w.storyViewFor(pc); v.Rank.ID != "sojourner" {
		t.Errorf("at 150 with Sojourner at 150 and Inhabitant at 200: rank %s", v.Rank.ID)
	}
	if evs := storyChangedEvents(rec); len(evs) != 2 {
		t.Errorf("each replace re-sends the player's story: %d events", len(evs))
	}
}

// LLM-895 (code_review R1): a mark naming a rank the ladder lost is rebased to
// the current rank at load and when the ladder is replaced, so later changes
// are announced again.
func TestRankSeenRebasedWhenTheRankIsGone(t *testing.T) {
	w, pc, _ := storyTestWorld(t)
	w.StartStoryForPC(pc, storyNow)
	pc.Standing = 60
	pc.StandingRankSeen = "gone"
	if _, err := ReplaceStandingRanks(storyTestLadder(), storyNow).Fn(w); err != nil {
		t.Fatal(err)
	}
	if pc.StandingRankSeen != "sojourner" {
		t.Fatalf("rebased to %q, want the current rank", pc.StandingRankSeen)
	}
	if u := rankPopup(w.storyViewFor(pc)); u.Key != "" {
		t.Errorf("the rebase announces nothing: %+v", u)
	}
	_, _ = AdjustStanding(pc.ID, 100, "test", storyNow).Fn(w)
	if u := rankPopup(w.storyViewFor(pc)); u.Key != "rank:inhabitant" {
		t.Errorf("a rise after the rebase = %+v, want the Inhabitant rank-up", u)
	}
}

// LLM-895 (code_review R1): Standing never wraps; a rise saturates.
func TestAddStandingBounds(t *testing.T) {
	for _, c := range []struct{ standing, delta, want int }{
		{10, 5, 15},
		{10, -15, 0},
		{10, math.MaxInt, MaxStanding},
		{MaxStanding - 1, 5, MaxStanding},
		{10, math.MinInt, 0},
		{0, MaxStanding, MaxStanding},
	} {
		if got := addStanding(c.standing, c.delta); got != c.want {
			t.Errorf("addStanding(%d, %d) = %d, want %d", c.standing, c.delta, got, c.want)
		}
	}
	w, pc, _ := storyTestWorld(t)
	pc.Standing = 10
	res, _ := AdjustStanding(pc.ID, math.MaxInt, "test", storyNow).Fn(w)
	if a := res.(StandingAdjusted); pc.Standing != MaxStanding || a.Applied != MaxStanding-10 {
		t.Errorf("a huge rise: standing %d applied %d", pc.Standing, a.Applied)
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
	ranks := storyTestLadder()
	for i := 0; i < 50; i++ {
		v := BuildStoryView(missions, rows, 0, "", ranks, nil)
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
