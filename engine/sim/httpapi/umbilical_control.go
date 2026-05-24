package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_control.go — the CONTROL half of the umbilical: a small WHITELIST of
// named, world-mutating operator commands. These are the most privileged routes
// in the system, so they carry three protections beyond the read surface:
//
//   1. requireOperator (plugins/administer) — same gate as the read routes.
//   2. A second opt-in: registered only when control is enabled
//      (UMBILICAL_CONTROL_ENABLED), so read-only is the default even with the
//      umbilical on.
//   3. Every invocation is audited (auditUmbilical) BEFORE the command runs, so
//      even a rejected/erroring attempt is recorded with the operator's identity.
//
// There is deliberately NO arbitrary-command path. Each handler issues exactly
// one named sim Command via SendContext. Unlike the /admin/* routes (gated by
// adminCommand → an in-world actor's IsAdmin), the umbilical does NOT resolve an
// in-world actor: operators (work/home/jeff) have no salem actor row, so authz
// is entirely the requireOperator capability check, and the command is issued
// directly as an out-of-world action.

// maxUmbilicalBodyBytes bounds a control request body. These bodies are tiny
// (an actor id, a phase string); the cap is a cheap abuse guard.
const maxUmbilicalBodyBytes = 4 << 10 // 4 KiB

// auditUmbilical records one umbilical control invocation to the engine log —
// the dedicated, OUT-OF-WORLD audit channel. Deliberately NOT sim.ActionLog:
// ActionLog feeds the in-world atmosphere + narrative-consolidation cascades, so
// writing operator actions there would leak debug control into NPC perception
// and violate the umbilical's strictly-additive invariant. A structured log line
// is out-of-band, accountable, and greppable in journald; a durable audit table
// can be added later if the umbilical graduates from a debug tool to a standing
// ops surface.
func auditUmbilical(operator, action, detail string) {
	log.Printf("umbilical AUDIT: operator=%q action=%q %s", operator, action, detail)
}

// decodeUmbilicalBody reads exactly one JSON object of T from r into dst, with
// the body-size cap + strict trailing-content check the admin routes use. Writes
// a 400 and returns false on any malformed input.
func decodeUmbilicalBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxUmbilicalBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// umbilicalNudgeRequest is the POST /api/village/umbilical/nudge body: which
// actor to force a deliberation tick for.
type umbilicalNudgeRequest struct {
	ActorID string `json:"actor_id"`
}

// umbilicalNudgeResponse echoes the target + whether the stamp opened a fresh
// warrant cycle (false = appended to one already in flight).
type umbilicalNudgeResponse struct {
	ActorID string `json:"actor_id"`
	Stamped bool   `json:"stamped"`
}

// handleUmbilicalNudge forces a reactor tick for a named actor by stamping an
// admin warrant (the "gently nudge an NPC to take a turn" primitive). Issues
// sim.StampWarrant with WarrantKindAdmin + Force. 400 missing actor_id; 422 when
// the actor is unknown (the command rejects it); 200 with the stamp result.
func (s *Server) handleUmbilicalNudge(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalNudgeRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}

	// Audit the attempt up front so a later rejection is still on the record.
	auditUmbilical(user.Username, "nudge", "actor="+req.ActorID)

	meta := sim.WarrantMeta{Force: true, Reason: sim.BasicWarrantReason{K: sim.WarrantKindAdmin}}
	res, err := s.world.SendContext(r.Context(), sim.StampWarrant(sim.ActorID(req.ActorID), meta, time.Now()))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.StampWarrantResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected nudge result")
		return
	}
	writeJSON(w, umbilicalNudgeResponse{ActorID: req.ActorID, Stamped: out.Stamped})
}

// umbilicalPhaseRequest is the POST /api/village/umbilical/phase body.
type umbilicalPhaseRequest struct {
	Phase string `json:"phase"` // day | night
}

// umbilicalPhaseResponse reports the applied transition (lighting flips reach
// clients via the world_phase_changed WS broadcast PhaseApplied triggers).
type umbilicalPhaseResponse struct {
	From            string `json:"from"`
	To              string `json:"to"`
	ObjectsAffected int    `json:"objects_affected"`
}

// handleUmbilicalPhase forces the world's day/night phase. The operator-reachable
// counterpart to /admin/phase (which is gated on an in-world admin actor the
// operators don't have). Issues sim.ApplyPhaseTransition directly. 400 invalid
// phase; 422 on a command error; 200 with the transition. Forcing the current
// phase is allowed (idempotent — From == To).
func (s *Server) handleUmbilicalPhase(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalPhaseRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	phase := sim.Phase(req.Phase)
	if phase != sim.PhaseDay && phase != sim.PhaseNight {
		writeError(w, http.StatusBadRequest, `phase must be "day" or "night"`)
		return
	}

	auditUmbilical(user.Username, "phase", "to="+req.Phase)

	res, err := s.world.SendContext(r.Context(), sim.ApplyPhaseTransition(phase))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.PhaseTransitionResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected phase result")
		return
	}
	writeJSON(w, umbilicalPhaseResponse{
		From:            string(out.From),
		To:              string(out.To),
		ObjectsAffected: out.ObjectsAffected,
	})
}
