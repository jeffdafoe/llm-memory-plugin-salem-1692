package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
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
// actor to force a deliberation tick for, and an optional operator directive to
// inject into that tick.
//
// Message is the optional in-world directive (ZBBS-WORK-329). Empty = today's
// bare forced tick (the actor deliberates with its normal perception, no
// operator content). Non-empty = the forced tick additionally perceives the
// directive as an in-world felt impulse (see sim.AdminDirectiveWarrantReason /
// perception.renderImpulseWarrantLine). The directive is one-shot: it lives only
// for that single warranted tick.
type umbilicalNudgeRequest struct {
	ActorID string `json:"actor_id"`
	Message string `json:"message,omitempty"`
}

// umbilicalNudgeResponse echoes the target, whether the stamp opened a fresh
// warrant cycle (false = appended to one already in flight), and whether an
// operator directive was attached (so the caller can confirm the directive path
// fired rather than silently falling back to a bare nudge on a mistyped field).
type umbilicalNudgeResponse struct {
	ActorID   string `json:"actor_id"`
	Stamped   bool   `json:"stamped"`
	Directive bool   `json:"directive"`
}

// handleUmbilicalNudge forces a reactor tick for a named actor by stamping a
// forced warrant (the "gently nudge an NPC to take a turn" primitive). With no
// message it stamps a bare WarrantKindAdmin warrant; with a message it stamps an
// AdminDirectiveWarrantReason (WarrantKindImpulse) so the directive surfaces in
// the forced tick's perception as an in-world felt impulse. Force is set either
// way. The directive is inert on actors that do not deliberate (PCs, decorative
// NPCs) — same as a bare nudge: the warrant is stamped but never rendered into a
// deliberation prompt. 400 missing actor_id; 422 when the actor is unknown (the
// command rejects it); 200 with the stamp result.
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

	// Build the warrant reason: a bare admin force-tick by default, or a
	// directive-bearing impulse reason when the operator supplied a message.
	detail := "actor=" + req.ActorID
	reason := sim.WarrantReason(sim.BasicWarrantReason{K: sim.WarrantKindAdmin})
	directive := req.Message != ""
	if directive {
		reason = sim.AdminDirectiveWarrantReason{Message: req.Message}
		detail += fmt.Sprintf(" directive=%q", req.Message)
	}

	// Audit the attempt up front so a later rejection is still on the record.
	auditUmbilical(user.Username, "nudge", detail)

	meta := sim.WarrantMeta{Force: true, Reason: reason}
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
	writeJSON(w, umbilicalNudgeResponse{ActorID: req.ActorID, Stamped: out.Stamped, Directive: directive})
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

// umbilicalSettleRequest is the POST /api/village/umbilical/settle body: which
// actor to clear the pending warrant cycle for.
type umbilicalSettleRequest struct {
	ActorID string `json:"actor_id"`
}

// umbilicalSettleResponse reports whether the actor had a pending warrant that
// was cleared.
type umbilicalSettleResponse struct {
	ActorID      string `json:"actor_id"`
	WasWarranted bool   `json:"was_warranted"`
}

// handleUmbilicalSettle clears an actor's pending warrant cycle — the reactive
// counterpart to nudge. When an NPC is spiraling (oscillating, double-talking),
// settling it cancels the queued tick so it stops deliberating until a fresh
// signal re-warrants it. Clears WarrantedSince/WarrantDueAt/Warrants only;
// leaves an already-in-flight LLM tick to complete normally (the attempt-id
// machinery handles that). Reversible, mutates no persistent state. 400 missing
// actor_id; 404 unknown actor; 200 ok.
func (s *Server) handleUmbilicalSettle(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalSettleRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}

	auditUmbilical(user.Username, "settle", "actor="+req.ActorID)

	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[sim.ActorID(req.ActorID)]
		if !ok {
			return nil, errAgentNotFound
		}
		was := a.WarrantedSince != nil
		a.WarrantedSince = nil
		a.WarrantDueAt = nil
		a.Warrants = nil
		return was, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAgentNotFound) {
			writeError(w, http.StatusNotFound, "actor not found")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	was, _ := res.(bool)
	writeJSON(w, umbilicalSettleResponse{ActorID: req.ActorID, WasWarranted: was})
}

// umbilicalRotateRequest is the POST /api/village/umbilical/rotate body. Tag is
// optional — empty rotates the whole village; a tag scopes the rotation to
// objects carrying it.
type umbilicalRotateRequest struct {
	Tag string `json:"tag,omitempty"`
}

// umbilicalRotateResponse reports the applied rotation (objects flipped + the
// event generation stamp).
type umbilicalRotateResponse struct {
	At              time.Time `json:"at"`
	Gen             uint64    `json:"gen"`
	ObjectsAffected int       `json:"objects_affected"`
}

// handleUmbilicalRotate forces a daily-rotation pass (the operator-reachable
// equivalent of the rotation ticker firing). Constructs the rotation inputs
// out-of-band: a fresh wall-clock Now + a seeded RNG for random_per_object
// picks. Idempotent against a converged world (0 flips). 200 with the result.
func (s *Server) handleUmbilicalRotate(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalRotateRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}

	auditUmbilical(user.Username, "rotate", "tag="+req.Tag)

	inputs := sim.RotationTickInputs{
		Now:  time.Now().UTC(),
		Rand: mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
	}
	res, err := s.world.SendContext(r.Context(), sim.ApplyDailyRotation(inputs, sim.RotationScope{Tag: req.Tag}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.RotationResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected rotate result")
		return
	}
	writeJSON(w, umbilicalRotateResponse{At: out.At, Gen: out.Gen, ObjectsAffected: out.ObjectsAffected})
}

// umbilicalNeedThresholdRequest is the POST /api/village/umbilical/settings/need-threshold
// body: tune one need's red-line threshold (the boundary that drives the
// need-threshold warrant + the perception distress cue).
type umbilicalNeedThresholdRequest struct {
	Need  string `json:"need"`
	Value int    `json:"value"`
}

// umbilicalNeedThresholdResponse echoes the applied threshold.
type umbilicalNeedThresholdResponse struct {
	Need  string `json:"need"`
	Value int    `json:"value"`
}

// handleUmbilicalNeedThreshold live-tunes one need's red-line threshold in
// WorldSettings. Scoped deliberately tight: only an ALREADY-CONFIGURED need key
// (no inventing phantom needs), value clamped to [0, NeedMax]. WorldSettings is
// load-only (not persisted by SaveWorld), so the change is EPHEMERAL — it
// resets to the env-configured value on restart, and cannot corrupt persistent
// state. The next snapshot republish carries the new threshold to perception +
// the reactor evaluator. 400 unknown need / out-of-range; 200 ok.
func (s *Server) handleUmbilicalNeedThreshold(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalNeedThresholdRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.Need == "" {
		writeError(w, http.StatusBadRequest, "need is required")
		return
	}
	if req.Value < 0 || req.Value > sim.NeedMax {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("value must be in [0, %d]", sim.NeedMax))
		return
	}

	auditUmbilical(user.Username, "settings.need-threshold", fmt.Sprintf("need=%s value=%d", req.Need, req.Value))

	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		key := sim.NeedKey(req.Need)
		if _, ok := world.Settings.NeedThresholds[key]; !ok {
			return nil, errUnknownNeed
		}
		world.Settings.NeedThresholds[key] = req.Value
		return nil, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errUnknownNeed) {
			writeError(w, http.StatusBadRequest, "unknown need (only already-configured needs can be tuned)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	_ = res
	writeJSON(w, umbilicalNeedThresholdResponse{Need: req.Need, Value: req.Value})
}

// errUnknownNeed is returned by the need-threshold tune command for a key that
// isn't already configured in WorldSettings.NeedThresholds.
var errUnknownNeed = errors.New("unknown need")
