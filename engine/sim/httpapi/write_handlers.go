package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// maxMoveBodyBytes / maxSpeakBodyBytes cap the write request bodies. The
// payloads are tiny (a move is a kind tag + coord/structure id; a speak is
// <=1000 chars of text); 64 KiB is generous headroom while still rejecting an
// attacker-controlled flood before it's buffered/decoded.
const (
	maxMoveBodyBytes  = 64 << 10
	maxSpeakBodyBytes = 64 << 10
)

// maxSpeakTextChars mirrors sim.Speak's documented precondition (the same cap
// handlers.MaxSpeakTextChars enforces on the LLM tool path): speech text is
// capped at 1000 Unicode characters. sim.Speak does NOT re-check text, so the
// caller (this handler) owns it.
const maxSpeakTextChars = 1000

// write_handlers.go — the client surface's write routes. Unlike the reads
// (which serve the published snapshot lock-free), a write goes through the
// world's command channel: the handler builds a sim.Command and submits it via
// w.SendContext, so the mutation runs serialized on the world goroutine and any
// events it emits (e.g. ActorMoveStarted → npc_walking) fan out over the hub.
//
// pc/move is the first write route. Design + the registry of deferred v1
// behaviors lives in work note tasks/engine-http-api/pc-move-design.

// errPCNotFound / errStructureNotFound are sentinels the move command returns so
// the handler maps them to 404 by identity (errors.Is), not by string-matching
// the message. Every other sim rejection (no path, closed, members-only, in an
// active huddle) is a 422 — the request named a real entity but the move isn't
// valid in the current world state.
var (
	errPCNotFound        = errors.New("pc not found for session")
	errStructureNotFound = errors.New("structure not found")
)

// pcMoveRequest is the POST /api/village/pc/move body. There is deliberately no
// actor_id: the PC is resolved from the authenticated session's username (the
// actor whose LoginUsername matches), so a caller can only ever move their own
// PC — ownership is structural, not a checked field.
type pcMoveRequest struct {
	Destination      moveDestinationRequest `json:"destination"`
	LeaveHuddleFirst bool                   `json:"leave_huddle_first"`
}

// moveDestinationRequest mirrors sim.MoveDestination on the wire: a kind tag
// plus exactly one payload. Coordinates are internal-grid TILE coords including
// pad — the same space the read surface emits for agent x/y, so the client
// echoes back what it received with no conversion.
type moveDestinationRequest struct {
	Kind        string           `json:"kind"` // position | structure_enter | structure_visit
	Position    *positionRequest `json:"position,omitempty"`
	StructureID string           `json:"structure_id,omitempty"`
}

type positionRequest struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// pcMoveResponse is the accepted-move outcome (sim.MoveActorResult on the wire).
type pcMoveResponse struct {
	MovementAttemptID   uint64 `json:"movement_attempt_id"`
	SupersededAttemptID uint64 `json:"superseded_attempt_id"`
	LeftHuddleID        string `json:"left_huddle_id,omitempty"`
}

// handlePCMove walks the caller's PC to a destination. The route is wrapped in
// requireAuth, so an authenticated salem-realm AuthUser is in the request
// context. The PC is resolved from that user inside the command (on the world
// goroutine) — see movePCCommand.
func (s *Server) handlePCMove(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		// requireAuth always populates this; guard defensively rather than nil-deref.
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMoveBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcMoveRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject trailing content after the JSON object — a write route shouldn't
	// silently accept `{...} garbage` or a second object. A clean body leaves
	// exactly io.EOF on the next read.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	dest, status, msg := buildMoveDestination(req.Destination)
	if msg != "" {
		writeError(w, status, msg)
		return
	}

	res, err := s.world.SendContext(r.Context(), movePCCommand(user.Username, dest, req.LeaveHuddleFirst))
	if err != nil {
		// The client disconnected (or the deadline lapsed) before the world
		// accepted the command or replied — there's nothing useful to write back.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) || errors.Is(err, errStructureNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Any other rejection is a sim state-validation failure: the move can't
		// happen right now (no path, closed door, members-only, in a huddle).
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.MoveActorResult)
	if !ok {
		// movePCCommand always returns a MoveActorResult on success; a mismatch
		// is a wiring bug, not a client error.
		writeError(w, http.StatusInternalServerError, "unexpected move result")
		return
	}
	writeJSON(w, pcMoveResponse{
		MovementAttemptID:   uint64(out.MovementAttemptID),
		SupersededAttemptID: uint64(out.SupersededAttemptID),
		LeftHuddleID:        string(out.LeftHuddleID),
	})
}

// buildMoveDestination validates the wire destination and converts it to a
// sim.MoveDestination. On rejection it returns a non-empty msg with the HTTP
// status to use (400 for a malformed/empty payload, 422 for an out-of-bounds
// tile — well-formed but unreachable).
func buildMoveDestination(d moveDestinationRequest) (sim.MoveDestination, int, string) {
	switch sim.MoveDestinationKind(d.Kind) {
	case sim.MoveDestinationPosition:
		if d.Position == nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "position destination requires a position"
		}
		if d.StructureID != "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "position destination must not include structure_id"
		}
		if d.Position.X < 0 || d.Position.X >= sim.MapW || d.Position.Y < 0 || d.Position.Y >= sim.MapH {
			return sim.MoveDestination{}, http.StatusUnprocessableEntity, "position is outside the map"
		}
		return sim.NewPositionDestination(sim.Position{X: d.Position.X, Y: d.Position.Y}), 0, ""
	case sim.MoveDestinationStructureEnter:
		if d.StructureID == "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_enter destination requires a structure_id"
		}
		if d.Position != nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_enter destination must not include position"
		}
		return sim.NewStructureEnterDestination(sim.StructureID(d.StructureID)), 0, ""
	case sim.MoveDestinationStructureVisit:
		if d.StructureID == "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination requires a structure_id"
		}
		if d.Position != nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination must not include position"
		}
		return sim.NewStructureVisitDestination(sim.StructureID(d.StructureID)), 0, ""
	default:
		return sim.MoveDestination{}, http.StatusBadRequest, "unknown destination kind"
	}
}

// movePCCommand resolves username → PC actor and delegates to sim.MoveActor.
// The resolution runs inside the command Fn (on the world goroutine) so it
// reads authoritative live state with no TOCTOU and needs no LoginUsername on
// the read DTO. Keeping it here — not in sim — means the session→actor identity
// rule lives in the auth-aware httpapi layer; sim.MoveActor stays a pure
// actor-id operation.
//
// The move timestamp is captured INSIDE the Fn (not at request receipt) so the
// stamped MoveIntent + the ActorMoveStarted it emits reflect the command's
// execution time, not how long it sat in a backed-up command channel.
func movePCCommand(username string, dest sim.MoveDestination, leaveHuddleFirst bool) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return sim.MoveActorResult{}, errPCNotFound
			}
			// Map a missing structure to our sentinel (→ 404) before MoveActor
			// rejects it with a generic message; MoveActor's own checks still
			// cover the rest (closed/owner/door/path → 422).
			if dest.StructureID != nil {
				if _, ok := world.Structures[*dest.StructureID]; !ok {
					return sim.MoveActorResult{}, errStructureNotFound
				}
			}
			return sim.MoveActor(actorID, dest, leaveHuddleFirst, time.Now().UTC()).Fn(world)
		},
	}
}

// findPCByLogin returns the id of the PC actor bound to loginUsername. Only
// KindPC actors carry a login binding, so the kind check guards against an
// NPC that somehow shares the value. Runs on the world goroutine (called from a
// command Fn), so the map read is safe.
func findPCByLogin(world *sim.World, loginUsername string) (sim.ActorID, bool) {
	if loginUsername == "" {
		return "", false
	}
	for id, a := range world.Actors {
		if a.Kind == sim.KindPC && a.LoginUsername == loginUsername {
			return id, true
		}
	}
	return "", false
}

// pcSpeakRequest is the POST /api/village/pc/speak body. Like pc/move there's
// no actor_id — the speaker is the caller's own PC, resolved from the session.
type pcSpeakRequest struct {
	Text string `json:"text"`
}

// pcSpeakResponse acks an accepted speak. The speech itself reaches every
// connected client (the speaker's own included) via the npc_spoke WS broadcast
// the Spoke event triggers, so the HTTP body is just a minimal confirmation.
type pcSpeakResponse struct {
	Status string `json:"status"`
}

// handlePCSpeak makes the caller's PC speak to its current huddle. Text
// validation (trim / non-empty / length / control-char) happens here because
// sim.Speak's contract makes the caller responsible for it; the world-state
// checks (not-walking, vocative-stale-addressee) run inside sim.Speak. A
// successful speak emits sim.Spoke → the speech reactor (NPC reactions) and the
// hub's npc_spoke broadcast.
func (s *Server) handlePCSpeak(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSpeakBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcSpeakRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	text, msg := validateSpeakText(req.Text)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	_, err := s.world.SendContext(r.Context(), speakPCCommand(user.Username, text))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// sim.Speak rejections (walking, vocative-stale-addressee) are
		// state-validation failures — the speak can't happen right now.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, pcSpeakResponse{Status: "ok"})
}

// speakPCCommand resolves username → PC actor (on the world goroutine) and
// delegates to sim.Speak. Same session→actor identity rule as movePCCommand;
// the clock is captured inside the Fn so the Spoke timestamp reflects execution.
func speakPCCommand(username, text string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			return sim.Speak(actorID, text, time.Now().UTC()).Fn(world)
		},
	}
}

// validateSpeakText applies sim.Speak's caller-owned text precondition and
// returns the trimmed text, or a non-empty msg describing the rejection (→ 400).
// Mirrors handlers.HandleSpeak / handlers.DecodeSpeakArgs (the LLM tool path);
// kept local rather than imported because that contract lives in the heavy
// handlers package and relocating it to sim would churn freshly-shipped code.
// The cap is character-based (utf8.RuneCountInString) to agree with the rune
// cap the tool-path schema enforces — a byte cap would reject multi-byte text
// the model side lets through.
func validateSpeakText(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", "text is required"
	}
	if utf8.RuneCountInString(text) > maxSpeakTextChars {
		return "", "text exceeds the length limit"
	}
	if hasInvalidControlChar(text) {
		return "", "text contains a disallowed control character"
	}
	return text, ""
}

// hasInvalidControlChar reports whether text contains a control character
// outside the allowed \n \r \t. Rejects the C0 range (except those three),
// DEL (0x7F), and the C1 range (0x80..0x9F) — these would derail the speech
// bubble + downstream perception-prompt rendering. Invalid UTF-8 is rejected up
// front via utf8.ValidString; the per-rune loop does NOT special-case
// utf8.RuneError, because ranging a string yields RuneError for BOTH a decode
// error AND the legitimate replacement character U+FFFD ("�") — guarding
// on it would wrongly reject valid text containing that printable code point.
func hasInvalidControlChar(text string) bool {
	if !utf8.ValidString(text) {
		return true
	}
	for _, rn := range text {
		switch {
		case rn == '\n' || rn == '\r' || rn == '\t':
			continue
		case rn >= 0x20 && rn < 0x7F:
			continue
		case rn == 0x7F, rn < 0x20, rn >= 0x80 && rn <= 0x9F:
			return true
		}
	}
	return false
}

// maxAdminBodyBytes caps admin write request bodies. Admin payloads are tiny
// (force-phase is a single phase tag); 64 KiB is generous headroom.
const maxAdminBodyBytes = 64 << 10

// errAdminForbidden is the sentinel an admin-gated command returns when the
// caller is not an admin — the handler maps it to 403 by identity. A single
// error for both "no actor matches this login" and "matched but not admin" so
// the response never reveals whether a given login maps to a real actor.
var errAdminForbidden = errors.New("admin privileges required")

// findAdminByLogin returns the id of the admin actor bound to loginUsername.
// Runs on the world goroutine (called from a command Fn), so the map read is
// safe. Admin is an actor-row flag (sim.Actor.IsAdmin), set out-of-band in the
// DB for the human operators — see migration ZBBS-WORK-271.
//
// login_username is expected to be unique (it mirrors the unique llm-memory-api
// actors.name the session token authenticates as). This being an authorization
// gate, it FAILS CLOSED on a duplicate: if two actors share loginUsername it
// denies rather than guess which one's admin flag governs — a stricter posture
// than findPCByLogin's first-match (ownership resolution, not a privilege gate).
func findAdminByLogin(world *sim.World, loginUsername string) (sim.ActorID, bool) {
	if loginUsername == "" {
		return "", false
	}
	var matched sim.ActorID
	var found *sim.Actor
	for id, a := range world.Actors {
		if a.LoginUsername != loginUsername {
			continue
		}
		if found != nil {
			return "", false // ambiguous login binding → fail closed
		}
		matched = id
		found = a
	}
	if found == nil || !found.IsAdmin {
		return "", false
	}
	return matched, true
}

// adminCommand wraps an admin-gated world mutation. It resolves the caller's
// actor by login_username and requires IsAdmin (on the world goroutine, so the
// check reads authoritative live state with no TOCTOU) BEFORE running action; a
// non-admin caller — or one with no matching actor — gets errAdminForbidden →
// 403, and action never runs. This is the reusable gate for every admin route
// (force-phase today; object reposition/delete next).
func adminCommand(username string, action func(*sim.World) (any, error)) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			if _, ok := findAdminByLogin(world, username); !ok {
				return nil, errAdminForbidden
			}
			return action(world)
		},
	}
}

// adminPhaseRequest is the POST /api/village/admin/phase body: the phase to
// force the world into. Forcing to the current phase is allowed (idempotent —
// sim.ApplyPhaseTransition still emits PhaseApplied with From == To).
type adminPhaseRequest struct {
	Phase string `json:"phase"` // day | night
}

// adminPhaseResponse reports the transition that applied. The visible canvas
// update (lighting flip) reaches all clients via the world_phase_changed WS
// broadcast PhaseApplied triggers, so the HTTP body is just the bracketing
// phases + how many objects the bulk pass flipped.
type adminPhaseResponse struct {
	From            string `json:"from"`
	To              string `json:"to"`
	ObjectsAffected int    `json:"objects_affected"`
}

// handleAdminPhase forces the world's day/night phase. Admin-only: wrapped in
// requireAuth (valid salem session) and gated again by adminCommand (the
// caller's actor must have admin = true). Delegates to sim.ApplyPhaseTransition,
// which flips the day/night object states and emits PhaseApplied → the
// world_phase_changed broadcast.
func (s *Server) handleAdminPhase(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminPhaseRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	phase := sim.Phase(req.Phase)
	if phase != sim.PhaseDay && phase != sim.PhaseNight {
		writeError(w, http.StatusBadRequest, `phase must be "day" or "night"`)
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.ApplyPhaseTransition(phase).Fn(world)
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
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
	writeJSON(w, adminPhaseResponse{
		From:            string(out.From),
		To:              string(out.To),
		ObjectsAffected: out.ObjectsAffected,
	})
}

// adminObjectMoveRequest is the POST /api/village/admin/object/move body: the
// target object id + its new absolute world-pixel anchor. Coordinates are the
// ObjectDTO space (float world-pixels), NOT the integer tile space pc/move
// uses — objects are placed at fractional pixel positions, so the editor sends
// pixels and we echo them back without conversion.
type adminObjectMoveRequest struct {
	ObjectID string  `json:"object_id"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

// adminObjectMoveResponse reports the applied position. The visible canvas
// update reaches all clients via the object_moved WS broadcast the
// VillageObjectMoved event triggers, so the HTTP body is just the new anchor.
type adminObjectMoveResponse struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

// handleAdminObjectMove repositions a placed village object. Admin-only:
// wrapped in requireAuth (valid salem session) and gated again by adminCommand
// (the caller's actor must have admin = true). Delegates to
// sim.MoveVillageObject. 400 malformed / missing id / non-finite or off-map
// position; 403 not admin; 404 object not found; 200 ok.
func (s *Server) handleAdminObjectMove(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectMoveRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}
	if status, msg := validateObjectPosition(req.X, req.Y); msg != "" {
		writeError(w, status, msg)
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.MoveVillageObject(sim.VillageObjectID(req.ObjectID), req.X, req.Y).Fn(world)
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if errors.Is(err, sim.ErrVillageObjectNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// sim.ErrInvalidObjectPosition is the command's own non-finite guard;
		// the handler's validateObjectPosition already rejects these at 400
		// before the command runs, so this maps the defense-in-depth path
		// consistently for completeness.
		if errors.Is(err, sim.ErrInvalidObjectPosition) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.MoveObjectResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected move result")
		return
	}
	writeJSON(w, adminObjectMoveResponse{ID: string(out.ID), X: out.X, Y: out.Y})
}

// validateObjectPosition checks an object move target. Objects live in absolute
// world-pixel coordinates: the renderable grid spans tiles [0, MapW) × [0, MapH)
// and world (0,0) sits at tile (PadX, PadY), so world-pixel x = (tile-PadX)*
// TileSize. The full padded grid therefore covers [-PadX*TileSize,
// (MapW-PadX)*TileSize] in x and the PadY/MapH analog in y; a target outside
// that rectangle would place the object off the map (422). A non-finite
// coordinate is a malformed request (400). Returns (0, "") when valid.
func validateObjectPosition(x, y float64) (int, string) {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return http.StatusBadRequest, "position must be a finite coordinate"
	}
	minX := -float64(sim.PadX) * sim.TileSize
	maxX := float64(sim.MapW-sim.PadX) * sim.TileSize
	minY := -float64(sim.PadY) * sim.TileSize
	maxY := float64(sim.MapH-sim.PadY) * sim.TileSize
	if x < minX || x > maxX || y < minY || y > maxY {
		return http.StatusUnprocessableEntity, "position is outside the map"
	}
	return 0, ""
}

// adminObjectDeleteRequest is the POST /api/village/admin/object/delete body:
// the id of the object to remove.
type adminObjectDeleteRequest struct {
	ObjectID string `json:"object_id"`
}

// adminObjectDeleteResponse lists every removed id — the target plus any
// overlay objects cascade-removed with it (attached_to chain), children first.
// Each removed object also reaches all clients as its own object_deleted WS
// broadcast.
type adminObjectDeleteResponse struct {
	DeletedIDs []string `json:"deleted_ids"`
}

// handleAdminObjectDelete removes a placed village object (and its attached
// overlays). Admin-only: requireAuth + adminCommand. Delegates to
// sim.DeleteVillageObject. 400 malformed / missing id; 403 not admin; 404
// object not found; 422 the object backs a structure (refused — structure
// teardown is a separate operation); 200 ok.
func (s *Server) handleAdminObjectDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectDeleteRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.DeleteVillageObject(sim.VillageObjectID(req.ObjectID)).Fn(world)
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if errors.Is(err, sim.ErrVillageObjectNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// sim.ErrVillageObjectIsStructure + any other rejection → 422.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.DeleteObjectResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected delete result")
		return
	}
	ids := make([]string, len(out.DeletedIDs))
	for i, id := range out.DeletedIDs {
		ids[i] = string(id)
	}
	writeJSON(w, adminObjectDeleteResponse{DeletedIDs: ids})
}

// writeError writes a JSON {error} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
