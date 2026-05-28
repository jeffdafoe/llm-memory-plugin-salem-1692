package httpapi

import "net/http"

// deadlocklog.go — operator-visible view onto the engine's recent
// locomotion-deadlock ring (ZBBS-WORK-340). Unlike errorlog.go, the ring
// itself lives on World (engine side, written inside the locomotion tick)
// because the data originates in the engine — this file just plumbs it to
// the umbilical surface. See engine/sim/deadlock_log.go for the ring shape
// and ZBBS-WORK-340 for the design.

// handleUmbilicalDeadlocks dumps the engine's recent-deadlock ring oldest→
// newest. Operator-gated like the rest of the umbilical read surface.
//
// Each entry carries the mover, the would-be destination, the occupant
// tile + best-effort occupant identity at record time, and the
// replan_failed flag — true when re-planning with the occupant tile
// masked off found no path at all (the sleeping-Abraham-in-the-doorway
// pattern), false when an alt path existed but its first tile was also
// occupied (mutual block / clogged corridor). Operators use the split to
// decide whether to nudge the occupant, relocate a sleeper, or tune
// DeadlockStuckThreshold.
func (s *Server) handleUmbilicalDeadlocks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.world.DeadlockSnapshot())
}
