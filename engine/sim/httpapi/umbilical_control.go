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
	"sort"
	"strings"
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

// umbilicalGrantItem is one signed inventory delta on the /grant wire: a positive
// qty gives, a negative qty claws back.
type umbilicalGrantItem struct {
	ItemKind string `json:"item_kind"`
	Qty      int    `json:"qty"`
}

// umbilicalGrantRequest is the POST /api/village/umbilical/grant body: a signed
// give-or-take of coins and/or inventory items to/from ANY actor (PC or NPC) —
// the economic-adjustment lever for remote village management. Coins is a signed
// delta (omitted / 0 = no coin change); Items is a list of signed per-kind
// deltas. At least one of the two must be non-empty (an all-zero grant is a 400 —
// almost certainly an operator mistake).
type umbilicalGrantRequest struct {
	ActorID string               `json:"actor_id"`
	Coins   int                  `json:"coins,omitempty"`
	Items   []umbilicalGrantItem `json:"items,omitempty"`
}

// umbilicalGrantResponse echoes the actor's authoritative post-mutation holdings
// (new coin balance + full inventory, sorted), so the operator sees the applied
// result without a separate read.
type umbilicalGrantResponse struct {
	ActorID string               `json:"actor_id"`
	Coins   int                  `json:"coins"`
	Items   []umbilicalGrantItem `json:"items"`
}

// handleUmbilicalGrant gives or claws back coins + inventory items to/from any
// actor via sim.AdjustActorHoldings — the only holdings path that accepts a PC
// (the editor's SetActorInventory rejects PCs and is whole-set-replace, not an
// additive signed delta; and no coins-adjust command exists otherwise). The
// adjustment is atomic: a single bad row (unknown item, underflow) rejects the
// whole call and applies nothing. 400 missing actor_id / empty grant / duplicate
// item_kind; 404 unknown actor; 422 unknown item_kind / underflow / overflow; 200
// with the post-mutation holdings.
func (s *Server) handleUmbilicalGrant(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalGrantRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	if req.Coins == 0 && len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "specify a coins delta and/or items to grant")
		return
	}

	deltas := make([]sim.ActorInventoryRow, 0, len(req.Items))
	for _, it := range req.Items {
		deltas = append(deltas, sim.ActorInventoryRow{ItemKind: it.ItemKind, Quantity: it.Qty})
	}

	// Audit the attempt up front so a later rejection is still on the record.
	auditUmbilical(user.Username, "grant", fmt.Sprintf("actor=%s coins=%+d items=%d", req.ActorID, req.Coins, len(req.Items)))

	res, err := s.world.SendContext(r.Context(), sim.AdjustActorHoldings(sim.ActorID(req.ActorID), req.Coins, deltas))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, sim.ErrActorNotFound):
			writeError(w, http.StatusNotFound, "actor not found")
		case errors.Is(err, sim.ErrInvalidInventory):
			writeError(w, http.StatusBadRequest, "duplicate item_kind in items")
		default:
			// Unknown item kind, underflow, overflow — all client-correctable; the
			// command's error message names the specific failure.
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	out, ok := res.(sim.ActorHoldingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected grant result")
		return
	}
	items := make([]umbilicalGrantItem, 0, len(out.Rows))
	for _, row := range out.Rows {
		items = append(items, umbilicalGrantItem{ItemKind: row.ItemKind, Qty: row.Quantity})
	}
	writeJSON(w, umbilicalGrantResponse{ActorID: req.ActorID, Coins: out.Coins, Items: items})
}

// umbilicalSetNeedsRequest is the POST /api/village/umbilical/set-needs body.
// Target exactly one of: a single ActorID, or All (every actor). Needs maps need
// keys to ABSOLUTE values in [0, NeedMax] — each listed need is set to that value
// on the target(s), leaving unlisted needs untouched. The lever for staging a
// test: dial hunger/thirst up to drive food/water seeking, set tiredness to 0 so
// rest-loops don't mask it, or zero everything to recover a starved village. An
// OMITTED/empty Needs map means "set every tracked need to 0" — the back-to-0
// shortcut (e.g. {all:true}). Unknown keys / out-of-range values are rejected
// (400), not silently ignored.
type umbilicalSetNeedsRequest struct {
	ActorID string         `json:"actor_id,omitempty"`
	All     bool           `json:"all,omitempty"`
	Needs   map[string]int `json:"needs,omitempty"`
}

// umbilicalSetNeedsResponse reports how many actors were changed and their
// post-change roster rows (so the operator sees the applied result without a
// follow-up /actors read).
type umbilicalSetNeedsResponse struct {
	Set    int                    `json:"set"`
	Actors []UmbilicalActorRowDTO `json:"actors"`
}

// handleUmbilicalSetNeeds sets an actor's needs to ABSOLUTE values. The body
// targets either one actor_id or all:true; the two are mutually exclusive (a body
// with both is a 400, so an operator who means "this one" can't silently change
// the whole village). Unlike v1's NPC-only editor guard, this deliberately applies
// to ANY actor (PC or NPC), mirroring the /grant route's any-actor philosophy — a
// PC's HUD just re-reads the new values on its next /pc/me poll.
//
// `needs` maps need keys to absolute values in [0, NeedMax]; each listed need is
// set to that value on the target(s), leaving unlisted needs untouched. An
// omitted/empty `needs` sets EVERY tracked need to 0 — the back-to-0 / mass-reset
// shortcut (and, since 0 drains need-warrant pressure, it also settles need-driven
// deliberation churn). Setting tiredness to 0 (explicitly, or via the zero-all
// shortcut) also clears any active rest window so the actor isn't left parked (see
// setActorNeeds). 400 missing/conflicting target / unknown need / out-of-range
// value; 404 unknown actor; 200 with the post-change rows.
func (s *Server) handleUmbilicalSetNeeds(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalSetNeedsRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.All && req.ActorID != "" {
		writeError(w, http.StatusBadRequest, "specify either actor_id or all:true, not both")
		return
	}
	if !req.All && req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "specify actor_id or all:true")
		return
	}
	// Validate + convert the need values up front (handler-side — the canonical
	// registry doesn't need the world goroutine). An unknown key or out-of-range
	// value is a 400 here so a typo can't silently misfire. An empty/omitted map
	// returns zeroAll=true ("set every need to 0").
	values, zeroAll, err := validateNeedValues(req.Needs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	target := "actor=" + req.ActorID
	if req.All {
		target = "all"
	}
	if zeroAll {
		target += " needs=ALL:0"
	} else {
		// Audit the validated/canonical key:value pairs (sorted for a stable
		// trail) so the record reflects exactly what ran.
		parts := make([]string, 0, len(values))
		for k, v := range values {
			parts = append(parts, fmt.Sprintf("%s:%d", k, v))
		}
		sort.Strings(parts)
		target += " needs=" + strings.Join(parts, ",")
	}
	// Audit the attempt up front so a later rejection is still on the record.
	auditUmbilical(user.Username, "set-needs", target)

	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		out := umbilicalSetNeedsResponse{Actors: []UmbilicalActorRowDTO{}}
		if req.All {
			for _, a := range world.Actors {
				setActorNeeds(world, a, values, zeroAll)
				out.Actors = append(out.Actors, actorRowDTO(a))
			}
		} else {
			a, ok := world.Actors[sim.ActorID(req.ActorID)]
			if !ok {
				return nil, errAgentNotFound
			}
			setActorNeeds(world, a, values, zeroAll)
			out.Actors = append(out.Actors, actorRowDTO(a))
		}
		out.Set = len(out.Actors)
		sort.Slice(out.Actors, func(i, j int) bool { return out.Actors[i].ID < out.Actors[j].ID })
		return out, nil
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
	out, ok := res.(umbilicalSetNeedsResponse)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-needs result")
		return
	}
	writeJSON(w, out)
}

// needKeyTiredness is the canonical tiredness need key. Setting tiredness to 0 is
// special: it also clears the actor's rest windows (see setActorNeeds).
const needKeyTiredness = sim.NeedKey("tiredness")

// setActorNeeds applies absolute need values to the actor. When zeroAll is true
// (the operator sent no needs map) every tracked need is set to 0; otherwise each
// key in values that the actor actually tracks is set to that value, leaving
// unlisted needs untouched. A nil/empty Needs map means the actor tracks no needs,
// so the writes are a no-op for it.
//
// Setting tiredness to 0 ALSO ends any active rest (BreakUntil / SleepingUntil):
// those windows are tiredness-recovery state, so at 0 tiredness the actor has no
// reason to stay parked. Without this, an actor mid-break stays `resting` despite
// the change (the live Elizabeth Ellis case — pinned on a break_until the old
// reset couldn't touch), which would defeat a food/water-seeking test. The reset
// routes through sim.ClearRestForReset, which for an agent NPC ends the rest
// PROPERLY (macro-state → idle, occupancy refresh) rather than nil-ing the window
// alone — leaving the StateResting enum behind would strand the actor as a
// reactor-shelved orphan (ZBBS-HOME-410). A non-zero tiredness leaves rest alone.
// Must run on the world goroutine (it mutates the live *Actor).
func setActorNeeds(world *sim.World, a *sim.Actor, values map[sim.NeedKey]int, zeroAll bool) {
	clearRest := false
	if zeroAll {
		for k := range a.Needs {
			a.Needs[k] = 0
			if k == needKeyTiredness {
				clearRest = true
			}
		}
	} else {
		for k, v := range values {
			if _, ok := a.Needs[k]; ok {
				a.Needs[k] = v
			}
			if k == needKeyTiredness && v == 0 {
				clearRest = true
			}
		}
	}
	if clearRest {
		// End any active rest PROPERLY (ZBBS-HOME-410): reset the macro-state and
		// refresh occupancy, not just nil the window. Nil-ing the window alone
		// stranded an agent NPC in StateResting / StateSleeping with no window —
		// invisible to the expiry sweeps and shelved by the reactor rest gate
		// forever (the live Ezekiel Crane / Prudence Ward stuck-stall case).
		sim.ClearRestForReset(world, a)
	}
}

// validateNeedValues converts the request need map to canonical NeedKeys with
// range-checked values: each key must be in the canonical needs registry
// (sim.FindNeed) and each value in [0, NeedMax], else a 400-worthy error. An
// empty/omitted map returns zeroAll=true (set every need to 0) with a nil values
// map, which setActorNeeds reads as the whole-actor zero.
func validateNeedValues(needs map[string]int) (map[sim.NeedKey]int, bool, error) {
	if len(needs) == 0 {
		return nil, true, nil
	}
	values := make(map[sim.NeedKey]int, len(needs))
	for name, v := range needs {
		key := sim.NeedKey(name)
		if _, ok := sim.FindNeed(key); !ok {
			return nil, false, fmt.Errorf("unknown need %q", name)
		}
		if v < 0 || v > sim.NeedMax {
			return nil, false, fmt.Errorf("need %q value %d out of range [0, %d]", name, v, sim.NeedMax)
		}
		values[key] = v
	}
	return values, false, nil
}
