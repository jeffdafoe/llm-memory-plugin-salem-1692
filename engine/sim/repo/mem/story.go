package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// StoryRepo is an in-memory sim.StoryRepo (LLM-892). SeedMissions sets the
// catalog; SaveSnapshot replaces the progress with what the checkpoint hands
// it, matching the pg tier's sweep.
type StoryRepo struct {
	ranks    []sim.StandingRank
	missions map[sim.StoryMissionID]*sim.StoryMission
	progress map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress
}

func NewStoryRepo() *StoryRepo {
	return &StoryRepo{
		missions: make(map[sim.StoryMissionID]*sim.StoryMission),
		progress: make(map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress),
	}
}

func (r *StoryRepo) SeedMissions(missions []*sim.StoryMission) {
	for _, m := range missions {
		c := *m
		r.missions[m.ID] = &c
	}
}

func (r *StoryRepo) SeedProgress(progress map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress) {
	for actorID, rows := range sim.CloneStoryProgress(progress) {
		r.progress[actorID] = rows
	}
}

// SeedRanks sets the Standing ladder (LLM-895).
func (r *StoryRepo) SeedRanks(ranks []sim.StandingRank) {
	r.ranks = append([]sim.StandingRank(nil), ranks...)
}

func (r *StoryRepo) LoadRanks(_ context.Context) ([]sim.StandingRank, error) {
	return append([]sim.StandingRank(nil), r.ranks...), nil
}

func (r *StoryRepo) LoadMissions(_ context.Context) (map[sim.StoryMissionID]*sim.StoryMission, error) {
	out := make(map[sim.StoryMissionID]*sim.StoryMission, len(r.missions))
	for id, m := range r.missions {
		c := *m
		out[id] = &c
	}
	return out, nil
}

func (r *StoryRepo) LoadProgress(_ context.Context) (map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress, error) {
	out := sim.CloneStoryProgress(r.progress)
	if out == nil {
		out = make(map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress)
	}
	return out, nil
}

func (r *StoryRepo) SaveSnapshot(_ context.Context, _ sim.Tx, progress map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress) error {
	r.progress = sim.CloneStoryProgress(progress)
	if r.progress == nil {
		r.progress = make(map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress)
	}
	return nil
}
