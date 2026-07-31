package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_npc_schedule.go — LLM-577. Set an NPC's work-shift window on the
// running engine.
//
// The command already existed (sim.SetActorSchedule) but was reachable only
// through POST /api/village/admin/npc/set-schedule, which wraps it in
// adminCommand — a gate that resolves an IN-WORLD admin (an actor row with
// is_admin whose login_username matches the caller). Operator agents have no
// actor row by design, so that route 403s for them and the only way to retune an
// NPC's hours was to stop the engine, UPDATE the actor row, and start it again —
// the stop being mandatory because the checkpoint writes the schedule columns
// back from memory every minute and would otherwise clobber the edit. That cost
// a production restart (and the in-memory observed store with it) for a
// two-integer change.
//
// This is the operator-authenticated sibling. Same command, same validation,
// same emitted NPCScheduleChanged event; only the gate differs. The admin route
// is untouched.

// umbilicalNPCScheduleRequest is the body of POST /api/village/umbilical/npc/set-schedule.
//
// Both bounds are minute-of-day in the WORLD timezone (setting world_timezone,
// America/New_York in production) — NOT UTC. 570 is 9:00 AM there.
//
// Both nil clears the window, which makes the NPC inherit the world dawn/dusk
// hours; supplying exactly one is an error rather than a partial update, so a
// caller cannot half-move a shift by forgetting a field. start == end is legal
// and means "never on shift".
type umbilicalNPCScheduleRequest struct {
	NPCID            string `json:"npc_id"`
	ScheduleStartMin *int   `json:"schedule_start_minute"`
	ScheduleEndMin   *int   `json:"schedule_end_minute"`
}

// umbilicalNPCScheduleResponse echoes the post-change window. Null bounds mean
// the NPC now inherits the world dawn/dusk hours.
type umbilicalNPCScheduleResponse struct {
	ID               string `json:"id"`
	ScheduleStartMin *int   `json:"schedule_start_minute"`
	ScheduleEndMin   *int   `json:"schedule_end_minute"`
}

// handleUmbilicalNPCSetSchedule applies a live schedule change. Operator-gated +
// audited like the rest of the umbilical control surface.
func (s *Server) handleUmbilicalNPCSetSchedule(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalNPCScheduleRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	auditUmbilical(user.Username, "npc.set-schedule", req.NPCID)

	res, err := s.world.SendContext(r.Context(), sim.SetActorSchedule(
		sim.ActorID(req.NPCID), req.ScheduleStartMin, req.ScheduleEndMin,
	))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidSchedule) {
			writeError(w, http.StatusBadRequest,
				"schedule_start_minute and schedule_end_minute must both be omitted (inherit the world dawn/dusk hours) or both be minute-of-day values in [0,1439], interpreted in the world timezone")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.ActorScheduleResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-schedule result")
		return
	}
	writeJSON(w, umbilicalNPCScheduleResponse{
		ID:               string(out.ID),
		ScheduleStartMin: out.ScheduleStartMin,
		ScheduleEndMin:   out.ScheduleEndMin,
	})
}
