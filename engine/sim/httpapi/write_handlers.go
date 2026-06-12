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

// errPCNotFound / errStructureNotFound / errObjectNotFound are sentinels the
// move command returns so the handler maps them to 404 by identity (errors.Is),
// not by string-matching the message. Every other sim rejection (no path,
// closed, members-only, in an active huddle) is a 422 — the request named a
// real entity but the move isn't valid in the current world state.
var (
	errPCNotFound        = errors.New("pc not found for session")
	errStructureNotFound = errors.New("structure not found")
	errObjectNotFound    = errors.New("village object not found")
)

// pcMoveRequest is the POST /api/village/pc/move body. There is deliberately no
// actor_id: the PC is resolved from the authenticated session's username (the
// actor whose LoginUsername matches), so a caller can only ever move their own
// PC — ownership is structural, not a checked field.
type pcMoveRequest struct {
	Destination moveDestinationRequest `json:"destination"`
}

// moveDestinationRequest mirrors sim.MoveDestination on the wire: a kind tag
// plus exactly one payload. Coordinates are internal-grid TILE coords including
// pad — the same space the read surface emits for agent x/y, so the client
// echoes back what it received with no conversion.
type moveDestinationRequest struct {
	Kind        string           `json:"kind"` // position | structure_enter | structure_visit | object_visit
	Position    *positionRequest `json:"position,omitempty"`
	StructureID string           `json:"structure_id,omitempty"`
	ObjectID    string           `json:"object_id,omitempty"`
}

type positionRequest struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// pcMoveResponse is the accepted-move outcome (sim.MoveActorResult on the wire).
// The knock fields are populated only for a structure_enter that resolved to a
// loiter-slot knock (an owner-only structure the PC isn't a member of) — see
// sim.EnterOrKnock. The service huddle forms on ARRIVAL, not in this response
// (ZBBS-HOME-445): the client reads knocked to start watching its huddle state
// for the talk panel, and knock_narration to explain a knock that looks like
// it will go unanswered.
type pcMoveResponse struct {
	MovementAttemptID   uint64 `json:"movement_attempt_id"`
	SupersededAttemptID uint64 `json:"superseded_attempt_id"`
	LeftHuddleID        string `json:"left_huddle_id,omitempty"`
	Knocked             bool   `json:"knocked,omitempty"`
	KnockNarration      string `json:"knock_narration,omitempty"`
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

	// PC click-moves always leave the current huddle: a deliberate navigation
	// ends the current conversation (v1's service-huddle cleanup), and without
	// it a PC already in a huddle could not move at all — MoveActor rejects a
	// bare move out of an active huddle. The wire field is no longer consulted.
	const leaveHuddleFirst = true

	// A structure_enter routes through EnterOrKnock so an owner-only structure
	// the PC isn't a member of resolves to a loiter-slot knock (joining the
	// keeper's huddle) rather than a 422 — v1 ZBBS-101 parity. dest.StructureID
	// is non-nil here: buildMoveDestination rejects an empty structure_enter.
	// Every other destination kind keeps the plain move path.
	var cmd sim.Command
	if dest.Kind == sim.MoveDestinationStructureEnter {
		cmd = enterOrKnockPCCommand(user.Username, *dest.StructureID, leaveHuddleFirst)
	} else {
		cmd = movePCCommand(user.Username, dest, leaveHuddleFirst)
	}

	res, err := s.world.SendContext(r.Context(), cmd)
	if err != nil {
		// The client disconnected (or the deadline lapsed) before the world
		// accepted the command or replied — there's nothing useful to write back.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) || errors.Is(err, errStructureNotFound) || errors.Is(err, errObjectNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Any other rejection is a sim state-validation failure: the move can't
		// happen right now (no path, closed door, members-only, in a huddle).
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	resp, ok := pcMoveResponseFromResult(res)
	if !ok {
		// The PC move commands always return MoveActorResult / EnterOrKnockResult
		// on success; any other type is a wiring bug, not a client error.
		writeError(w, http.StatusInternalServerError, "unexpected move result")
		return
	}
	writeJSON(w, resp)
}

// pcMoveResponseFromResult maps either PC-move command result onto the wire
// response. EnterOrKnockResult embeds MoveActorResult, so the movement fields
// promote; only the structure_enter (knock) path carries the knock fields.
func pcMoveResponseFromResult(res any) (pcMoveResponse, bool) {
	switch out := res.(type) {
	case sim.EnterOrKnockResult:
		return pcMoveResponse{
			MovementAttemptID:   uint64(out.MovementAttemptID),
			SupersededAttemptID: uint64(out.SupersededAttemptID),
			LeftHuddleID:        string(out.LeftHuddleID),
			Knocked:             out.Knocked,
			KnockNarration:      out.KnockNarration,
		}, true
	case sim.MoveActorResult:
		return pcMoveResponse{
			MovementAttemptID:   uint64(out.MovementAttemptID),
			SupersededAttemptID: uint64(out.SupersededAttemptID),
			LeftHuddleID:        string(out.LeftHuddleID),
		}, true
	default:
		return pcMoveResponse{}, false
	}
}

// enterOrKnockPCCommand resolves the session username to the caller's PC, then
// delegates to sim.EnterOrKnock for a structure_enter. Mirrors movePCCommand's
// posture: PC identity resolves inside the Fn (on the world goroutine, no
// TOCTOU), the move clock is captured at execution time, and a missing
// structure maps to the 404 sentinel before EnterOrKnock's generic message.
func enterOrKnockPCCommand(username string, structureID sim.StructureID, leaveHuddleFirst bool) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return sim.EnterOrKnockResult{}, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			if _, ok := world.Structures[structureID]; !ok {
				return sim.EnterOrKnockResult{}, errStructureNotFound
			}
			return sim.EnterOrKnock(actorID, structureID, leaveHuddleFirst, now).Fn(world)
		},
	}
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
		if d.ObjectID != "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "position destination must not include object_id"
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
		if d.ObjectID != "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_enter destination must not include object_id"
		}
		return sim.NewStructureEnterDestination(sim.StructureID(d.StructureID)), 0, ""
	case sim.MoveDestinationStructureVisit:
		if d.StructureID == "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination requires a structure_id"
		}
		if d.Position != nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination must not include position"
		}
		if d.ObjectID != "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination must not include object_id"
		}
		return sim.NewStructureVisitDestination(sim.StructureID(d.StructureID)), 0, ""
	case sim.MoveDestinationObjectVisit:
		if d.ObjectID == "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "object_visit destination requires an object_id"
		}
		if d.Position != nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "object_visit destination must not include position"
		}
		if d.StructureID != "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "object_visit destination must not include structure_id"
		}
		return sim.NewObjectVisitDestination(sim.VillageObjectID(d.ObjectID)), 0, ""
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
			// A deliberate PC action: stamp the input cursor and input-wake the
			// PC if it was asleep (ZBBS-WORK-324). Before the move so the wake's
			// pc_sleep_ended precedes any movement frames. One clock for the wake
			// and the move so their timestamps/event order can't skew.
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			// Map a missing structure / village object to our sentinels (→ 404)
			// before MoveActor rejects them with a generic message; MoveActor's
			// own checks still cover the rest (closed/owner/door/path → 422).
			if dest.StructureID != nil {
				if _, ok := world.Structures[*dest.StructureID]; !ok {
					return sim.MoveActorResult{}, errStructureNotFound
				}
			}
			if dest.ObjectID != nil {
				// Tombstoned entries (a key present in the map with a nil
				// value) are not a valid placement either — MoveActor's own
				// guard treats `!ok || vobj == nil` as not-found, so the
				// sentinel mapping has to match or the nil case 422s
				// instead of 404ing (code_review round 1).
				vobj, ok := world.VillageObjects[*dest.ObjectID]
				if !ok || vobj == nil {
					return sim.MoveActorResult{}, errObjectNotFound
				}
			}
			return sim.MoveActor(actorID, dest, leaveHuddleFirst, now).Fn(world)
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
			// Deliberate PC action: stamp the input cursor + input-wake an asleep
			// PC (ZBBS-WORK-324) before the speak. One clock for both.
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			// ZBBS-HOME-358: form the conversation on the explicit talk action.
			// A PC who walked into an open structure has no huddle (the arrival-
			// encounter cascade is outdoor-only and a plain walk-in mints none),
			// so without this sim.Speak would reject a name-address (422) or emit
			// to no one. EnsureColocatedHuddle joins/forms the indoor huddle with
			// co-located actors; it is a no-op when the PC already has a huddle or
			// is alone, so it never disturbs an existing conversation. It logs and
			// swallows per-actor join errors internally, so it returns nil here.
			if _, err := sim.EnsureColocatedHuddle(actorID, now).Fn(world); err != nil {
				return nil, err
			}
			return sim.Speak(actorID, text, now).Fn(world)
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

// adminObjectCreateRequest is the POST /api/village/admin/object/create body —
// place a new object of asset_id at (x, y) in world-pixel space. attached_to is
// optional: when set, the placement hangs off an existing object as an overlay.
type adminObjectCreateRequest struct {
	AssetID    string  `json:"asset_id"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	AttachedTo *string `json:"attached_to"`
}

// adminObjectCreateResponse reports the created object's minted id + placement.
// The placing client reads id (to adopt its optimistic node) and placed_by;
// every client renders the new object via the object_created WS broadcast.
type adminObjectCreateResponse struct {
	ID           string  `json:"id"`
	AssetID      string  `json:"asset_id"`
	CurrentState string  `json:"current_state"`
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	PlacedBy     string  `json:"placed_by"`
	EntryPolicy  string  `json:"entry_policy"`
	AttachedTo   string  `json:"attached_to,omitempty"`
}

func (s *Server) handleAdminObjectCreate(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectCreateRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AssetID == "" {
		writeError(w, http.StatusBadRequest, "asset_id is required")
		return
	}
	// attached_to is optional (omitted = root placement); but an explicitly
	// present empty string is malformed — reject it rather than silently
	// treating it as a root create (the pointer lets us tell omitted from "").
	if req.AttachedTo != nil && *req.AttachedTo == "" {
		writeError(w, http.StatusBadRequest, "attached_to must be non-empty when provided")
		return
	}
	if status, msg := validateObjectPosition(req.X, req.Y); msg != "" {
		writeError(w, status, msg)
		return
	}
	var attachedTo sim.VillageObjectID
	if req.AttachedTo != nil {
		attachedTo = sim.VillageObjectID(*req.AttachedTo)
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.CreateVillageObject(sim.AssetID(req.AssetID), req.X, req.Y, attachedTo, user.Username).Fn(world)
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		// Unknown asset / bad attached_to / non-finite position are bad input.
		if errors.Is(err, sim.ErrUnknownAsset) || errors.Is(err, sim.ErrInvalidObjectPosition) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, sim.ErrVillageObjectNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.CreateObjectResult)
	if !ok || out.Object == nil {
		writeError(w, http.StatusInternalServerError, "unexpected create result")
		return
	}
	writeJSON(w, adminObjectCreateResponse{
		ID:           string(out.Object.ID),
		AssetID:      string(out.Object.AssetID),
		CurrentState: out.Object.CurrentState,
		X:            out.Object.Pos.X,
		Y:            out.Object.Pos.Y,
		PlacedBy:     out.Object.PlacedBy,
		EntryPolicy:  string(out.Object.EntryPolicy),
		AttachedTo:   string(out.Object.AttachedTo),
	})
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

// adminObjectStateRequest is the POST /api/village/admin/object/set-state body:
// the target object id + the asset-state name to set it to. State is a free-form
// catalog state string; an admin override is trusted and the engine does NOT
// reject an unknown state name here (matching the v1 PATCH state route — a state
// the asset doesn't define simply renders as the asset fallback). object_id and
// state are both required.
type adminObjectStateRequest struct {
	ObjectID string `json:"object_id"`
	State    string `json:"state"`
}

// adminObjectStateResponse reports the applied state. Applied is false when the
// object was already at the target state — an idempotent no-op that still
// returns 200. A real flip reaches all clients via the object_state_changed WS
// broadcast the VillageObjectStateChanged event triggers, so the body just
// carries the outcome.
type adminObjectStateResponse struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Applied bool   `json:"applied"`
}

// handleAdminObjectSetState sets a placed object's current_state. Admin-only:
// requireAuth + adminCommand. Delegates to sim.SetVillageObjectState with
// guardGen=0 (an admin override is unconditional — no generation gate, unlike a
// scheduled phase flip). 400 malformed / missing id or state; 403 not admin;
// 404 object not found; 200 ok (Applied=false when already at the target state).
func (s *Server) handleAdminObjectSetState(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectStateRequest
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
	if req.State == "" {
		writeError(w, http.StatusBadRequest, "state is required")
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		out, err := sim.SetVillageObjectState(sim.VillageObjectID(req.ObjectID), req.State).Fn(world)
		if err != nil {
			return nil, err
		}
		// SetVillageObjectState reports a missing object as a result Reason
		// (Applied=false, nil error), not an error — that shape suits its
		// scheduled-flip callers. Translate it to the shared sentinel so this
		// admin route maps a missing object to 404 like object/move + delete.
		sr := out.(sim.SetStateResult)
		if sr.Reason == "not_found" {
			return nil, sim.ErrVillageObjectNotFound
		}
		return sr, nil
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
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetStateResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-state result")
		return
	}
	writeJSON(w, adminObjectStateResponse{ID: req.ObjectID, State: req.State, Applied: out.Applied})
}

// adminObjectOwnerRequest is the POST /api/village/admin/object/set-owner body:
// the target object id + the owning actor id. An empty owner_actor_id clears
// ownership (unowned); a non-empty one must resolve to a live actor.
type adminObjectOwnerRequest struct {
	ObjectID     string `json:"object_id"`
	OwnerActorID string `json:"owner_actor_id"`
}

// adminObjectOwnerResponse echoes the applied owner. There's no WS broadcast —
// owner is not in ObjectDTO — so the body is the editor's only confirmation.
type adminObjectOwnerResponse struct {
	ID           string `json:"id"`
	OwnerActorID string `json:"owner_actor_id"`
}

// handleAdminObjectSetOwner sets (or clears) a placed object's owning actor.
// Admin-only: requireAuth + adminCommand. Delegates to
// sim.SetVillageObjectOwner. 400 malformed / missing id; 403 not admin; 404
// object not found; 422 owner actor not found (non-empty id with no live
// actor); 200 ok.
func (s *Server) handleAdminObjectSetOwner(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectOwnerRequest
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
		return sim.SetVillageObjectOwner(sim.VillageObjectID(req.ObjectID), sim.ActorID(req.OwnerActorID)).Fn(world)
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
		// sim.ErrOwnerActorNotFound (a dangling owner id) and any other
		// rejection are state-validation failures → 422.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetOwnerResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-owner result")
		return
	}
	writeJSON(w, adminObjectOwnerResponse{ID: string(out.ID), OwnerActorID: string(out.OwnerActorID)})
}

// adminObjectLoiterOffsetRequest is the POST .../set-loiter-offset body. X and Y
// are nullable tile-unit offsets: both present sets the override, both null (or
// omitted) clears it back to the catalog default. Exactly one set is rejected
// (400) — the offset is treated as an (x, y) pair on this route.
type adminObjectLoiterOffsetRequest struct {
	ObjectID string `json:"object_id"`
	X        *int   `json:"x"`
	Y        *int   `json:"y"`
}

// adminObjectLoiterOffsetResponse echoes the applied offset. A cleared axis
// serializes as null (no omitempty) so the editor can tell "cleared" from 0.
type adminObjectLoiterOffsetResponse struct {
	ID string `json:"id"`
	X  *int   `json:"x"`
	Y  *int   `json:"y"`
}

// handleAdminObjectSetLoiterOffset sets (or clears) a placed object's loiter
// offset. Admin-only: requireAuth + adminCommand. Delegates to
// sim.SetVillageObjectLoiterOffset. 400 malformed / missing id / only one of
// x,y; 403 not admin; 404 object not found; 200 ok.
func (s *Server) handleAdminObjectSetLoiterOffset(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectLoiterOffsetRequest
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
	// Both-or-neither: the offset is an (x, y) pair on the wire. A lone axis is
	// almost certainly a client mistake, so reject it rather than silently
	// clearing the other.
	if (req.X == nil) != (req.Y == nil) {
		writeError(w, http.StatusBadRequest, "x and y must both be set or both omitted")
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.SetVillageObjectLoiterOffset(sim.VillageObjectID(req.ObjectID), req.X, req.Y).Fn(world)
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
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetLoiterOffsetResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-loiter-offset result")
		return
	}
	writeJSON(w, adminObjectLoiterOffsetResponse{ID: string(out.ID), X: out.X, Y: out.Y})
}

// adminObjectEntryPolicyRequest is the POST .../set-entry-policy body: the
// target object id + the entry policy. Valid values are "" (type default),
// "open", "owner-only", "closed".
type adminObjectEntryPolicyRequest struct {
	ObjectID    string `json:"object_id"`
	EntryPolicy string `json:"entry_policy"`
}

// adminObjectEntryPolicyResponse echoes the applied policy.
type adminObjectEntryPolicyResponse struct {
	ID          string `json:"id"`
	EntryPolicy string `json:"entry_policy"`
}

// handleAdminObjectSetEntryPolicy sets a placed object's entry policy.
// Admin-only: requireAuth + adminCommand. The handler validates the enum (400)
// and sim.SetVillageObjectEntryPolicy guards it again (defense-in-depth). 400
// malformed / missing id / unknown policy; 403 not admin; 404 object not found;
// 200 ok.
func (s *Server) handleAdminObjectSetEntryPolicy(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectEntryPolicyRequest
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
	if !validEntryPolicy(req.EntryPolicy) {
		writeError(w, http.StatusBadRequest, `entry_policy must be "", "open", "owner-only", or "closed"`)
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.SetVillageObjectEntryPolicy(sim.VillageObjectID(req.ObjectID), sim.EntryPolicy(req.EntryPolicy)).Fn(world)
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
		// sim.ErrInvalidEntryPolicy is the command's own enum guard; the handler
		// rejects unknown values at 400 before the command runs, so map this
		// defense-in-depth path to 400 consistently rather than the 422 default.
		if errors.Is(err, sim.ErrInvalidEntryPolicy) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetEntryPolicyResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-entry-policy result")
		return
	}
	writeJSON(w, adminObjectEntryPolicyResponse{ID: string(out.ID), EntryPolicy: string(out.EntryPolicy)})
}

// validEntryPolicy reports whether s is one of the four accepted entry-policy
// values. Mirrors the sim.EntryPolicy consts (kept in sync with village_object.go).
func validEntryPolicy(s string) bool {
	switch sim.EntryPolicy(s) {
	case sim.EntryPolicyDefault, sim.EntryPolicyOpen, sim.EntryPolicyOwner, sim.EntryPolicyClosed:
		return true
	default:
		return false
	}
}

// adminObjectDisplayNameRequest is the POST .../set-display-name body: the
// target object id + the new display name. An empty display_name clears the
// override (the client falls back to the catalog name).
type adminObjectDisplayNameRequest struct {
	ObjectID    string `json:"object_id"`
	DisplayName string `json:"display_name"`
}

// adminObjectDisplayNameResponse echoes the applied (trimmed) display name.
type adminObjectDisplayNameResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// handleAdminObjectSetDisplayName sets (or clears) a placed object's display-name
// override. Admin-only: requireAuth + adminCommand. Delegates to
// sim.SetVillageObjectDisplayName, which trims + validates the name and emits
// VillageObjectDisplayNameChanged on an actual change (→ object_display_name_changed
// WS frame). 400 malformed / missing id / invalid name; 403 not admin; 404 object
// not found; 200 ok.
func (s *Server) handleAdminObjectSetDisplayName(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectDisplayNameRequest
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
		return sim.SetVillageObjectDisplayName(sim.VillageObjectID(req.ObjectID), req.DisplayName).Fn(world)
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
		// sim.ErrInvalidDisplayName is bad input (over-cap / control char) — the
		// command is the validation authority, so map its sentinel to 400 rather
		// than the 422 default.
		if errors.Is(err, sim.ErrInvalidDisplayName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetDisplayNameResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-display-name result")
		return
	}
	writeJSON(w, adminObjectDisplayNameResponse{ID: string(out.ID), DisplayName: out.DisplayName})
}

// adminObjectTagRequest is the POST .../add-tag and .../remove-tag body: the
// target object id + the single tag to add or remove.
type adminObjectTagRequest struct {
	ObjectID string `json:"object_id"`
	Tag      string `json:"tag"`
}

// adminObjectTagResponse echoes the authoritative full tag set after the
// mutation (always an array; an object with no tags serializes as []).
type adminObjectTagResponse struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}

// handleAdminObjectAddTag adds a per-instance tag to a placed object. Admin-only:
// requireAuth + adminCommand. Delegates to sim.AddVillageObjectTag (idempotent —
// a tag already present is a no-op that emits nothing). On an actual add the
// command emits VillageObjectTagsUpdated (→ village_object_tags_updated WS frame).
// 400 malformed / missing id / invalid tag; 403 not admin; 404 object not found;
// 200 ok.
func (s *Server) handleAdminObjectAddTag(w http.ResponseWriter, r *http.Request) {
	s.handleAdminObjectTagMutation(w, r, true)
}

// handleAdminObjectRemoveTag removes a per-instance tag from a placed object.
// Admin-only; delegates to sim.RemoveVillageObjectTag (idempotent — removing an
// absent tag is a no-op). Same status mapping as add-tag.
func (s *Server) handleAdminObjectRemoveTag(w http.ResponseWriter, r *http.Request) {
	s.handleAdminObjectTagMutation(w, r, false)
}

// handleAdminObjectTagMutation is the shared add/remove-tag handler — the two
// routes differ only in which sim command they dispatch, so the auth gate, body
// decode, validation, status mapping, and response shape live here once. add
// selects AddVillageObjectTag (true) or RemoveVillageObjectTag (false).
func (s *Server) handleAdminObjectTagMutation(w http.ResponseWriter, r *http.Request, add bool) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectTagRequest
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
		if add {
			return sim.AddVillageObjectTag(sim.VillageObjectID(req.ObjectID), req.Tag).Fn(world)
		}
		return sim.RemoveVillageObjectTag(sim.VillageObjectID(req.ObjectID), req.Tag).Fn(world)
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
		// sim.ErrInvalidTag is bad input (empty / over-cap / control char) — the
		// command is the validation authority, so map its sentinel to 400.
		if errors.Is(err, sim.ErrInvalidTag) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetTagsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected tag-mutation result")
		return
	}
	// Coerce nil → [] so the response body is a JSON array even when the last
	// tag was just removed (mirrors the WS frame's "always an array" contract).
	tags := out.Tags
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, adminObjectTagResponse{ID: string(out.ID), Tags: tags})
}

// adminObjectRefreshRow is one refresh policy on the .../set-refresh wire. It
// maps 1:1 to the persisted sim.ObjectRefresh fields; last_refresh_at is omitted
// because it is engine-managed (the regen tick anchors it). amount and
// dwell_delta are NEGATIVE — they are the decrement applied to an actor on
// arrival, matching the object_refresh CHECK constraints (amount < 0) and the
// sim/DB representation. available_quantity and max_quantity are both-or-neither
// (an infinite supply omits both); an infinite row must also omit refresh_mode
// and refresh_period_hours, since regen only applies to a finite supply.
type adminObjectRefreshRow struct {
	Attribute          string `json:"attribute"`
	Amount             int    `json:"amount"`
	AvailableQuantity  *int   `json:"available_quantity"`
	MaxQuantity        *int   `json:"max_quantity"`
	RefreshMode        string `json:"refresh_mode"`
	RefreshPeriodHours *int   `json:"refresh_period_hours"`
	DwellDelta         *int   `json:"dwell_delta"`
	DwellPeriodMinutes *int   `json:"dwell_period_minutes"`
}

// adminObjectRefreshRequest is the POST .../set-refresh body: the target object
// id + the full refresh set to apply. The set replaces the object's existing
// refresh policies wholesale; an empty or omitted rows clears them all.
type adminObjectRefreshRequest struct {
	ObjectID string                  `json:"object_id"`
	Rows     []adminObjectRefreshRow `json:"rows"`
}

// adminObjectRefreshResponse echoes the applied set. There's no WS broadcast —
// refresh config is not in ObjectDTO — so the body is the editor's confirmation
// and authoritative read-back. Rows is always an array (a cleared set is []).
type adminObjectRefreshResponse struct {
	ID   string                  `json:"id"`
	Rows []adminObjectRefreshRow `json:"rows"`
}

// handleAdminObjectSetRefresh replaces a placed object's refresh-policy set.
// Admin-only: requireAuth + adminCommand. Delegates to
// sim.SetVillageObjectRefreshes, which validates the set (mirroring the
// object_refresh CHECK constraints) before mutating. 400 malformed / missing id
// / invalid refresh row; 403 not admin; 404 object not found; 200 ok. Emits no
// event (refresh is not client-visible) — the response is the editor's read-back.
func (s *Server) handleAdminObjectSetRefresh(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminObjectRefreshRequest
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

	// Map the wire rows to sim.ObjectRefresh. The command deep-copies these
	// before storing, so passing the request's pointers straight through is safe.
	rows := make([]*sim.ObjectRefresh, 0, len(req.Rows))
	for _, row := range req.Rows {
		rows = append(rows, &sim.ObjectRefresh{
			Attribute:          sim.NeedKey(row.Attribute),
			Amount:             row.Amount,
			AvailableQuantity:  row.AvailableQuantity,
			MaxQuantity:        row.MaxQuantity,
			RefreshMode:        sim.RefreshMode(row.RefreshMode),
			RefreshPeriodHours: row.RefreshPeriodHours,
			DwellDelta:         row.DwellDelta,
			DwellPeriodMinutes: row.DwellPeriodMinutes,
		})
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.SetVillageObjectRefreshes(sim.VillageObjectID(req.ObjectID), rows).Fn(world)
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
		// sim.ErrInvalidRefresh is bad input (mirrors the pg CHECK constraints) —
		// the command is the validation authority, so map its sentinel to 400.
		if errors.Is(err, sim.ErrInvalidRefresh) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.SetRefreshesResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-refresh result")
		return
	}
	writeJSON(w, adminObjectRefreshResponse{ID: string(out.ID), Rows: refreshRowsToWire(out.Refreshes)})
}

// refreshRowsToWire maps the applied sim refresh set back to the wire shape
// (last_refresh_at is engine-internal and omitted). Always returns a non-nil
// slice so a cleared set serializes as [] rather than null.
func refreshRowsToWire(rows []*sim.ObjectRefresh) []adminObjectRefreshRow {
	out := make([]adminObjectRefreshRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminObjectRefreshRow{
			Attribute:          string(r.Attribute),
			Amount:             r.Amount,
			AvailableQuantity:  r.AvailableQuantity,
			MaxQuantity:        r.MaxQuantity,
			RefreshMode:        string(r.RefreshMode),
			RefreshPeriodHours: r.RefreshPeriodHours,
			DwellDelta:         r.DwellDelta,
			DwellPeriodMinutes: r.DwellPeriodMinutes,
		})
	}
	return out
}

// writeError writes a JSON {error} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
