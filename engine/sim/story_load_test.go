package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestStoryLoad_KeepsValidRowsAndStartsEveryPlayer: at load the story keeps a
// player's rows, drops a row on an unknown mission and the rows of a non-player,
// and offers the start beat to a player who had none (existing players start
// chapter 1 too, LLM-892).
func TestStoryLoad_KeepsValidRowsAndStartsEveryPlayer(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"pc-old": {ID: "pc-old", Kind: sim.KindPC, LoginUsername: "old", DisplayName: "Old Hand", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}, Standing: 10},
		"pc-new": {ID: "pc-new", Kind: sim.KindPC, LoginUsername: "new", DisplayName: "Newcomer", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY}},
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared, LLMAgent: "hannah-va", DisplayName: "Hannah", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY}},
	})
	handles.Story.SeedMissions([]*sim.StoryMission{
		{ID: "arrival-1", Chapter: "Arrival", Seq: 1, Title: "Make Camp", Trigger: sim.StoryTriggerCampPitched, StandingReward: 10, NextID: "arrival-2", IsStart: true},
		{ID: "arrival-2", Chapter: "Arrival", Seq: 2, Title: "A Fire of Your Own", Trigger: sim.StoryTriggerCampFireLit, StandingReward: 10},
	})
	offered := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	handles.Story.SeedProgress(map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress{
		"pc-old": {
			"arrival-1": {MissionID: "arrival-1", OfferedAt: offered, FinishedAt: offered.Add(time.Hour), FinishSeenAt: offered.Add(2 * time.Hour)},
			"arrival-2": {MissionID: "arrival-2", OfferedAt: offered.Add(time.Hour)},
			"gone-9":    {MissionID: "gone-9", OfferedAt: offered},
		},
		"hannah": {"arrival-1": {MissionID: "arrival-1", OfferedAt: offered}},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	old := w.StoryProgress["pc-old"]
	if len(old) != 2 || old["gone-9"] != nil {
		t.Fatalf("pc-old rows = %v, want arrival-1 and arrival-2 only", old)
	}
	if p := old["arrival-2"]; p == nil || !p.Open() || !p.OfferedAt.Equal(offered.Add(time.Hour)) {
		t.Errorf("pc-old arrival-2 = %+v, want the loaded open row", p)
	}
	if _, ok := w.StoryProgress["hannah"]; ok {
		t.Error("a villager's rows are dropped")
	}
	if p := w.StoryProgress["pc-new"]["arrival-1"]; p == nil || !p.Open() {
		t.Errorf("pc-new arrival-1 = %+v, want offered at load", p)
	}
	if got := w.Actors["pc-old"].Standing; got != 10 {
		t.Errorf("pc-old Standing = %d, want 10", got)
	}

	// The checkpoint mirrors what the world holds.
	cp := w.BuildCheckpointSnapshot()
	if len(cp.StoryProgress["pc-old"]) != 2 || len(cp.StoryProgress["pc-new"]) != 1 {
		t.Errorf("checkpoint story rows = %v", cp.StoryProgress)
	}
}
