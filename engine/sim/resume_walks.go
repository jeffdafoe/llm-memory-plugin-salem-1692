package sim

import (
	"log"
	"sort"
	"time"
)

// resume_walks.go — ZBBS-HOME-449. Boot resume of walks the previous
// process had in flight at shutdown.
//
// MoveIntent is in-memory; what survives the restart is the intent's
// destination, checkpointed to actor.move_destination and loaded into
// Actor.ResumeDestination. This sweep re-dispatches each one through the
// normal MoveActor command: the path is re-planned from the checkpointed
// tile, the client gets the standard npc_walking frame, and arrival fires
// the usual arrival warrant — so the actor finishes the plan it was
// executing as if the restart never happened. (Live case 2026-06-12:
// Ezekiel Crane's Inn-bound walk was killed by a deploy restart 27s in,
// leaving him standing at the road midpoint indefinitely — off-shift, no
// duty steer, every subsequent tick noop-skipped.)

// ResumeWalksResult is the typed reply from ResumeCheckpointedWalks:
// how many checkpointed walks were re-dispatched and how many were
// dropped (destination no longer valid/reachable).
type ResumeWalksResult struct {
	Resumed int
	Dropped int
}

// ResumeCheckpointedWalks returns a Command that re-dispatches every
// actor's checkpointed walk destination. Run once at boot, after world
// load (cmd/engine wires it next to the other boot catch-ups).
//
// Per actor: ResumeDestination is consumed (cleared) unconditionally —
// it describes a walk from the PREVIOUS process and must not linger. An
// actor already moving is skipped (the mem repo round-trips MoveIntent
// wholesale, so under that repo the walk is already live). A dispatch
// rejection (structure deleted during downtime, destination unreachable
// from the checkpointed tile, actor now in an active huddle) logs and
// drops the walk — recovery for those actors is the anomalous-position
// backstop's job (ZBBS-HOME-450), not a boot hard-failure.
//
// LeaveHuddleFirst is deliberately false: huddle membership is persisted
// state, and a checkpointed actor who is somehow in an active huddle now
// should stay in it rather than have a stale walk yank them out.
func ResumeCheckpointedWalks(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Sorted iteration so multi-resume boots log deterministically.
			ids := make([]ActorID, 0, len(w.Actors))
			for id, a := range w.Actors {
				if a != nil && a.ResumeDestination != nil {
					ids = append(ids, id)
				}
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

			var res ResumeWalksResult
			for _, id := range ids {
				actor := w.Actors[id]
				dest := *actor.ResumeDestination
				actor.ResumeDestination = nil
				if actor.MoveIntent != nil {
					continue
				}
				moveCmd := MoveActor(id, cloneMoveDestination(dest), false, now)
				if _, err := moveCmd.Fn(w); err != nil {
					log.Printf("sim/resume_walks: %q checkpointed walk (kind=%s) not resumable: %v",
						id, dest.Kind, err)
					res.Dropped++
					continue
				}
				log.Printf("sim/resume_walks: %q resuming checkpointed walk (kind=%s)", id, dest.Kind)
				res.Resumed++
			}
			if res.Resumed > 0 || res.Dropped > 0 {
				log.Printf("sim/resume_walks: boot resume — %d walk(s) resumed, %d dropped", res.Resumed, res.Dropped)
			}
			return res, nil
		},
	}
}
