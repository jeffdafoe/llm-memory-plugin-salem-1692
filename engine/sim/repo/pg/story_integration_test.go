package pg

// Real-pg integration tests for story missions (LLM-892): the migrations seed
// chapters 1 and 2 (LLM-898), progress and Standing round-trip through
// SaveWorld → LoadWorld, and a player's rows dropped from the world are swept
// at the next checkpoint.

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestIntegration_Story_ChaptersAreSeeded(t *testing.T) {
	f := newFixture(t)
	repo := NewRepository(f.Pool)
	missions, err := repo.Story.LoadMissions(t.Context())
	if err != nil {
		t.Fatalf("LoadMissions: %v", err)
	}
	want := map[sim.StoryMissionID]struct {
		chapter string
		trigger sim.StoryTriggerKind
		next    sim.StoryMissionID
	}{
		"arrival-1": {"Arrival", sim.StoryTriggerCampPitched, "arrival-2"},
		"arrival-2": {"Arrival", sim.StoryTriggerCampFireLit, "arrival-3"},
		"arrival-3": {"Arrival", sim.StoryTriggerSleptAtHome, "arrival-4"},
		"arrival-4": {"Arrival", sim.StoryTriggerMealAt, "arrival-5"},
		"arrival-5": {"Arrival", sim.StoryTriggerSpokeWithVillager, "signs-1"},
		"signs-1":   {"Signs", sim.StoryTriggerOmenWitnessed, "signs-2"},
		"signs-2":   {"Signs", sim.StoryTriggerSpokeWithVillager, "signs-3"},
		"signs-3":   {"Signs", sim.StoryTriggerItemAcquired, "signs-4"},
		"signs-4":   {"Signs", sim.StoryTriggerWardPlaced, ""},
	}
	if len(missions) != len(want) {
		t.Fatalf("loaded %d missions, want %d", len(missions), len(want))
	}
	totals := map[string]int{}
	for id, w := range want {
		m := missions[id]
		if m == nil || m.Trigger != w.trigger || m.NextID != w.next || m.Chapter != w.chapter || m.Title == "" || m.OfferText == "" || m.DoneText == "" {
			t.Errorf("%s = %+v", id, m)
			continue
		}
		totals[m.Chapter] += m.StandingReward
	}
	if got := missions["signs-1"].TriggerArgs.Omens; len(got) != 2 || got[0] != sim.OmenCampFireGutters || got[1] != sim.OmenFoulWell {
		t.Errorf("signs-1 brings on %v, want the camp fire and the well", got)
	}
	if missions["signs-3"].TriggerArgs.Item != sim.HorseshoeKind || missions["signs-4"].TriggerArgs.Item != sim.HorseshoeKind {
		t.Error("signs-3 and signs-4 name the horseshoe")
	}
	if totals["Signs"] != 50 {
		t.Errorf("chapter 2 gives %d Standing, want 50", totals["Signs"])
	}
	total := totals["Arrival"]
	if !missions["arrival-1"].IsStart || missions["arrival-2"].IsStart {
		t.Error("only arrival-1 starts the chapter")
	}
	if missions["arrival-4"].TriggerArgs.Tag != "lodging" {
		t.Errorf("arrival-4 tag = %q, want lodging", missions["arrival-4"].TriggerArgs.Tag)
	}
	if missions["arrival-1"].AlreadyText == "" || missions["arrival-2"].AlreadyText == "" {
		t.Error("the state-checked beats carry their catch-up clause")
	}
	if total != 50 {
		t.Errorf("chapter 1 gives %d Standing, want exactly Sojourner (50)", total)
	}
}

func TestIntegration_Story_ProgressRoundTripAndSweep(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const pcID = "55555555-5555-5555-5555-555555555555"

	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		pcID: {ID: pcID, DisplayName: "Jefferey", Kind: sim.KindPC, LoginUsername: "jeff", State: sim.StateIdle,
			Inventory: map[sim.ItemKind]int{}, Standing: 10, StandingRankSeen: "stranger"},
	}
	w.StoryProgress = map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress{
		pcID: {
			"arrival-1": {MissionID: "arrival-1", OfferedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-time.Hour), CaughtUp: true},
			"arrival-2": {MissionID: "arrival-2", OfferedAt: now.Add(-time.Hour), OfferSeenAt: now},
		},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if got := loaded.Actors[pcID].Standing; got != 10 {
		t.Errorf("Standing = %d, want 10", got)
	}
	if len(loaded.StoryMissions) != 9 {
		t.Errorf("loaded %d missions, want the 9 seeded (chapters 1 and 2)", len(loaded.StoryMissions))
	}
	rows := loaded.StoryProgress[pcID]
	if len(rows) != 2 {
		t.Fatalf("loaded rows = %v, want 2", rows)
	}
	a1, a2 := rows["arrival-1"], rows["arrival-2"]
	if a1 == nil || !a1.CaughtUp || !a1.FinishedAt.Equal(now.Add(-time.Hour)) || !a1.FinishSeenAt.IsZero() {
		t.Errorf("arrival-1 = %+v", a1)
	}
	if a2 == nil || !a2.Open() || !a2.OfferSeenAt.Equal(now) || !a2.OfferedAt.Equal(now.Add(-time.Hour)) {
		t.Errorf("arrival-2 = %+v", a2)
	}

	// A reset drops the player's rows; the next checkpoint sweeps them.
	w.StoryProgress = map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress{}
	w.Actors[pcID].Standing = 0
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("second SaveWorld: %v", err)
	}
	var n int
	if err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM pc_story_mission`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Errorf("pc_story_mission has %d rows after the sweep, want 0", n)
	}
}

func TestIntegration_Story_ChecksRefuseABadMission(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for label, q := range map[string]string{
		"bad id":       `INSERT INTO story_mission (id, chapter, seq, title, offer_text, done_text, trigger_kind) VALUES ('Bad ID', 'C', 1, 't', 'o', 'd', 'camp_pitched')`,
		"bad trigger":  `INSERT INTO story_mission (id, chapter, seq, title, offer_text, done_text, trigger_kind) VALUES ('x-1', 'C', 1, 't', 'o', 'd', 'fly')`,
		"bad reward":   `INSERT INTO story_mission (id, chapter, seq, title, offer_text, done_text, trigger_kind, standing_reward) VALUES ('x-2', 'C', 1, 't', 'o', 'd', 'camp_pitched', -1)`,
		"unknown next": `INSERT INTO story_mission (id, chapter, seq, title, offer_text, done_text, trigger_kind, next_mission_id) VALUES ('x-3', 'C', 1, 't', 'o', 'd', 'camp_pitched', 'nowhere')`,
	} {
		if _, err := f.Pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: insert succeeded, want a constraint violation", label)
		}
	}
}
