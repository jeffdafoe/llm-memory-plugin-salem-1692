package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// StoryRepo reads the story catalog (story_mission, reference data the engine
// never writes) and reads + checkpoints each player's story progress
// (pc_story_mission) — LLM-892.
//
// SaveSnapshot uses the generation-marker pattern (same shape as
// labor_contract): advisory lock → nextval(gen) → per-row UPSERT stamping
// snapshot_gen → DELETE WHERE snapshot_gen < gen. A player's rows dropped by a
// story reset are absent from the map, so the trailing delete sweeps them. It
// writes inside the SaveWorld Tx, so a finished beat and the player's
// actor.standing persist together.
//
// actor_id / mission_id are soft TEXT refs (no FK), validated in Go at
// FinalizeLoad (rehydrateStoryOnLoad).
type StoryRepo struct {
	pool Pool
}

// NewStoryRepo constructs a StoryRepo. Normal wiring is pg.NewRepository.
func NewStoryRepo(pool Pool) *StoryRepo {
	return &StoryRepo{pool: pool}
}

const loadStoryMissionsSQL = `
SELECT id, chapter, seq, title, offer_text, done_text, already_text,
       trigger_kind, trigger_args, standing_reward, next_mission_id, is_start
  FROM story_mission`

const loadStoryProgressSQL = `
SELECT actor_id, mission_id, offered_at, finished_at, offer_seen_at, finish_seen_at, caught_up
  FROM pc_story_mission`

const upsertStoryProgressSQL = `
INSERT INTO pc_story_mission (
    actor_id, mission_id, offered_at, finished_at, offer_seen_at, finish_seen_at, caught_up, snapshot_gen
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (actor_id, mission_id) DO UPDATE SET
    offered_at     = EXCLUDED.offered_at,
    finished_at    = EXCLUDED.finished_at,
    offer_seen_at  = EXCLUDED.offer_seen_at,
    finish_seen_at = EXCLUDED.finish_seen_at,
    caught_up      = EXCLUDED.caught_up,
    snapshot_gen   = EXCLUDED.snapshot_gen`

const deleteStaleStoryProgressSQL = `DELETE FROM pc_story_mission WHERE snapshot_gen < $1`

const nextGenStoryProgressSQL = `SELECT nextval('pc_story_mission_snapshot_gen_seq')`

const advisoryLockStoryProgressSQL = `SELECT pg_advisory_xact_lock(hashtext('pc_story_mission_snapshot'), 0)`

// LoadMissions loads the whole catalog.
func (r *StoryRepo) LoadMissions(ctx context.Context) (map[sim.StoryMissionID]*sim.StoryMission, error) {
	rows, err := r.pool.Query(ctx, loadStoryMissionsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg story LoadMissions query: %w", err)
	}
	defer rows.Close()
	out := make(map[sim.StoryMissionID]*sim.StoryMission)
	for rows.Next() {
		var (
			id, chapter, title, offerText, doneText, trigger string
			alreadyText, nextID                              *string
			seq, reward                                      int
			argsRaw                                          []byte
			isStart                                          bool
		)
		if err := rows.Scan(&id, &chapter, &seq, &title, &offerText, &doneText, &alreadyText,
			&trigger, &argsRaw, &reward, &nextID, &isStart); err != nil {
			return nil, fmt.Errorf("pg story LoadMissions scan: %w", err)
		}
		var args sim.StoryTriggerArgs
		if len(argsRaw) > 0 {
			if err := json.Unmarshal(argsRaw, &args); err != nil {
				return nil, fmt.Errorf("pg story LoadMissions: mission %s trigger_args: %w", id, err)
			}
		}
		out[sim.StoryMissionID(id)] = &sim.StoryMission{
			ID:             sim.StoryMissionID(id),
			Chapter:        chapter,
			Seq:            seq,
			Title:          title,
			OfferText:      offerText,
			DoneText:       doneText,
			AlreadyText:    deref(alreadyText),
			Trigger:        sim.StoryTriggerKind(trigger),
			TriggerArgs:    args,
			StandingReward: reward,
			NextID:         sim.StoryMissionID(deref(nextID)),
			IsStart:        isStart,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg story LoadMissions iter: %w", err)
	}
	return out, nil
}

// LoadProgress loads every player's progress rows.
func (r *StoryRepo) LoadProgress(ctx context.Context) (map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress, error) {
	rows, err := r.pool.Query(ctx, loadStoryProgressSQL)
	if err != nil {
		return nil, fmt.Errorf("pg story LoadProgress query: %w", err)
	}
	defer rows.Close()
	out := make(map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress)
	for rows.Next() {
		var (
			actorID, missionID                    string
			offeredAt                             time.Time
			finishedAt, offerSeenAt, finishSeenAt *time.Time
			caughtUp                              bool
		)
		if err := rows.Scan(&actorID, &missionID, &offeredAt, &finishedAt, &offerSeenAt, &finishSeenAt, &caughtUp); err != nil {
			return nil, fmt.Errorf("pg story LoadProgress scan: %w", err)
		}
		aid := sim.ActorID(actorID)
		if out[aid] == nil {
			out[aid] = make(map[sim.StoryMissionID]*sim.StoryProgress)
		}
		out[aid][sim.StoryMissionID(missionID)] = &sim.StoryProgress{
			MissionID:    sim.StoryMissionID(missionID),
			OfferedAt:    offeredAt,
			FinishedAt:   derefTime(finishedAt),
			OfferSeenAt:  derefTime(offerSeenAt),
			FinishSeenAt: derefTime(finishSeenAt),
			CaughtUp:     caughtUp,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg story LoadProgress iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot mirrors the progress map into pc_story_mission inside the
// checkpoint Tx. An empty map still bumps the gen, so the sweep empties the
// table.
func (r *StoryRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, progress map[sim.ActorID]map[sim.StoryMissionID]*sim.StoryProgress) error {
	if tx == nil {
		return fmt.Errorf("pg story SaveSnapshot: nil tx")
	}
	if _, err := tx.Exec(ctx, advisoryLockStoryProgressSQL); err != nil {
		return fmt.Errorf("pg story SaveSnapshot: advisory lock: %w", err)
	}
	var gen int64
	if err := tx.QueryRow(ctx, nextGenStoryProgressSQL).Scan(&gen); err != nil {
		return fmt.Errorf("pg story SaveSnapshot: nextval: %w", err)
	}
	for actorID, rows := range progress {
		for missionID, p := range rows {
			if p == nil {
				continue
			}
			if p.MissionID != missionID || actorID == "" || missionID == "" {
				return fmt.Errorf("pg story SaveSnapshot: bad row actor=%q mission=%q (key %q)", actorID, p.MissionID, missionID)
			}
			if _, err := tx.Exec(ctx, upsertStoryProgressSQL,
				string(actorID),
				string(missionID),
				p.OfferedAt,
				nilOnZeroTime(p.FinishedAt),
				nilOnZeroTime(p.OfferSeenAt),
				nilOnZeroTime(p.FinishSeenAt),
				p.CaughtUp,
				gen,
			); err != nil {
				return fmt.Errorf("pg story SaveSnapshot: upsert actor=%s mission=%s: %w", actorID, missionID, err)
			}
		}
	}
	if _, err := tx.Exec(ctx, deleteStaleStoryProgressSQL, gen); err != nil {
		return fmt.Errorf("pg story SaveSnapshot: sweep: %w", err)
	}
	return nil
}
