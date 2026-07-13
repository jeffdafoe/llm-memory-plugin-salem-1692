package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// NPC editor write routes (ZBBS-HOME-309) — the v2 home for v1's
// /api/village/npcs/{id}/{field} PATCH surface, reshaped to the v2 admin
// convention: POST /api/village/admin/npc/<field> with the target id in the
// body (mirroring admin/object/*). Each delegates to a SetActor* /
// {Add,Remove}ActorAttribute command (actor_admin.go) through adminCommand →
// World.SendContext, so the mutation runs on the world goroutine, is admin-gated
// twice (valid salem session + actor.IsAdmin), and emits its npc_* WS frame on an
// actual change. Read half: AgentDTO (ZBBS-HOME-290).

// decodeAdminBody reads exactly one JSON object from an admin request body into
// dst — MaxBytesReader-capped and rejecting trailing data (a second value after
// the object). On malformed/oversize/trailing input it writes a 400 and returns
// false; the caller must return.
func decodeAdminBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
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

// adminNPCRequest is the shared front half of every admin/npc/* handler: require
// a valid session and decode the body. Returns the caller's username (for the
// adminCommand gate) and ok=false (after writing the error) when either fails.
func (s *Server) adminNPCRequest(w http.ResponseWriter, r *http.Request, dst any) (string, bool) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return "", false
	}
	if !decodeAdminBody(w, r, dst) {
		return "", false
	}
	return user.Username, true
}

// writeActorAdminError maps an actor-admin command error to its HTTP status.
// Context cancellation (client gone) writes nothing. The input sentinels are
// disjoint across the eight commands, so one mapper serves all of them: the
// command is the validation authority and owns the 400/404/422 split.
func writeActorAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, errAdminForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, sim.ErrActorNotFound), errors.Is(err, sim.ErrStructureNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, sim.ErrInvalidDisplayName),
		errors.Is(err, sim.ErrInvalidAgentLink),
		errors.Is(err, sim.ErrInvalidSchedule):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, sim.ErrUnknownAttribute), errors.Is(err, sim.ErrUnknownItemKind),
		errors.Is(err, sim.ErrStructureNotHabitable):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, sim.ErrUnknownSprite), errors.Is(err, sim.ErrInvalidInventory):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}

// ---- display name ----

type adminNPCDisplayNameRequest struct {
	NPCID       string `json:"npc_id"`
	DisplayName string `json:"display_name"`
}

type adminNPCDisplayNameResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func (s *Server) handleAdminNPCSetDisplayName(w http.ResponseWriter, r *http.Request) {
	var req adminNPCDisplayNameRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetActorDisplayName(sim.ActorID(req.NPCID), req.DisplayName).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorDisplayNameResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-display-name result")
		return
	}
	writeJSON(w, adminNPCDisplayNameResponse{ID: string(out.ID), DisplayName: out.DisplayName})
}

// ---- agent link ----

// LLMAgent is a pointer so an explicit null (unlink) is distinguishable from an
// absent field; both collapse to "" → the command unlinks.
type adminNPCAgentRequest struct {
	NPCID    string  `json:"npc_id"`
	LLMAgent *string `json:"llm_memory_agent"`
}

type adminNPCAgentResponse struct {
	ID       string  `json:"id"`
	LLMAgent *string `json:"llm_memory_agent"`
}

func (s *Server) handleAdminNPCSetAgent(w http.ResponseWriter, r *http.Request) {
	var req adminNPCAgentRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	agent := ""
	if req.LLMAgent != nil {
		agent = *req.LLMAgent
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetActorAgentLink(sim.ActorID(req.NPCID), agent).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorAgentResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-agent result")
		return
	}
	writeJSON(w, adminNPCAgentResponse{ID: string(out.ID), LLMAgent: strPtrOrNil(out.LLMAgent)})
}

// ---- home / work structure ----

// StructureID is a pointer so an explicit null (clear the anchor) is
// distinguishable from an absent field; both collapse to "" → the command clears.
type adminNPCStructureRequest struct {
	NPCID       string  `json:"npc_id"`
	StructureID *string `json:"structure_id"`
}

type adminNPCStructureResponse struct {
	ID          string  `json:"id"`
	StructureID *string `json:"structure_id"`
}

func (s *Server) handleAdminNPCSetHomeStructure(w http.ResponseWriter, r *http.Request) {
	s.handleAdminNPCSetStructure(w, r, true)
}

func (s *Server) handleAdminNPCSetWorkStructure(w http.ResponseWriter, r *http.Request) {
	s.handleAdminNPCSetStructure(w, r, false)
}

func (s *Server) handleAdminNPCSetStructure(w http.ResponseWriter, r *http.Request, home bool) {
	var req adminNPCStructureRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	structureID := ""
	if req.StructureID != nil {
		structureID = *req.StructureID
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		if home {
			return sim.SetActorHomeStructure(sim.ActorID(req.NPCID), structureID).Fn(world)
		}
		return sim.SetActorWorkStructure(sim.ActorID(req.NPCID), structureID).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorStructureResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-structure result")
		return
	}
	writeJSON(w, adminNPCStructureResponse{ID: string(out.ID), StructureID: strPtrOrNil(out.StructureID)})
}

// ---- schedule ----

type adminNPCScheduleRequest struct {
	NPCID            string `json:"npc_id"`
	ScheduleStartMin *int   `json:"schedule_start_minute"`
	ScheduleEndMin   *int   `json:"schedule_end_minute"`
}

type adminNPCScheduleResponse struct {
	ID               string `json:"id"`
	ScheduleStartMin *int   `json:"schedule_start_minute"`
	ScheduleEndMin   *int   `json:"schedule_end_minute"`
}

func (s *Server) handleAdminNPCSetSchedule(w http.ResponseWriter, r *http.Request) {
	var req adminNPCScheduleRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetActorSchedule(sim.ActorID(req.NPCID), req.ScheduleStartMin, req.ScheduleEndMin).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorScheduleResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-schedule result")
		return
	}
	writeJSON(w, adminNPCScheduleResponse{
		ID:               string(out.ID),
		ScheduleStartMin: out.ScheduleStartMin,
		ScheduleEndMin:   out.ScheduleEndMin,
	})
}

// ---- attributes (add / remove) ----

type adminNPCAttributeRequest struct {
	NPCID string `json:"npc_id"`
	Slug  string `json:"slug"`
}

type adminNPCAttributeResponse struct {
	ID         string   `json:"id"`
	Attributes []string `json:"attributes"`
}

func (s *Server) handleAdminNPCAddAttribute(w http.ResponseWriter, r *http.Request) {
	s.handleAdminNPCAttributeMutation(w, r, true)
}

func (s *Server) handleAdminNPCRemoveAttribute(w http.ResponseWriter, r *http.Request) {
	s.handleAdminNPCAttributeMutation(w, r, false)
}

func (s *Server) handleAdminNPCAttributeMutation(w http.ResponseWriter, r *http.Request, add bool) {
	var req adminNPCAttributeRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		if add {
			return sim.AddActorAttribute(sim.ActorID(req.NPCID), req.Slug).Fn(world)
		}
		return sim.RemoveActorAttribute(sim.ActorID(req.NPCID), req.Slug).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorAttributesResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected attribute-mutation result")
		return
	}
	attrs := out.Attributes
	if attrs == nil {
		attrs = []string{}
	}
	writeJSON(w, adminNPCAttributeResponse{ID: string(out.ID), Attributes: attrs})
}

// ---- sprite ----

type adminNPCSpriteRequest struct {
	NPCID    string `json:"npc_id"`
	SpriteID string `json:"sprite_id"`
}

type adminNPCSpriteResponse struct {
	ID       string `json:"id"`
	SpriteID string `json:"sprite_id"`
}

func (s *Server) handleAdminNPCSetSprite(w http.ResponseWriter, r *http.Request) {
	var req adminNPCSpriteRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetActorSprite(sim.ActorID(req.NPCID), req.SpriteID).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorSpriteResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-sprite result")
		return
	}
	writeJSON(w, adminNPCSpriteResponse{ID: string(out.ID), SpriteID: out.SpriteID})
}

// ---- inventory (read + whole-set write) ----

type adminNPCInventoryRow struct {
	ItemKind string `json:"item_kind"`
	Quantity int    `json:"quantity"`
}

type adminNPCInventoryReadRequest struct {
	NPCID string `json:"npc_id"`
}

type adminNPCInventoryWriteRequest struct {
	NPCID string                 `json:"npc_id"`
	Rows  []adminNPCInventoryRow `json:"rows"`
}

func inventoryRowsToWire(rows []sim.ActorInventoryRow) []adminNPCInventoryRow {
	out := make([]adminNPCInventoryRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminNPCInventoryRow{ItemKind: row.ItemKind, Quantity: row.Quantity})
	}
	return out
}

// handleAdminNPCInventory reads an NPC's inventory (v2 port of v1's
// GET /api/village/npcs/{id}/inventory). Returns a bare JSON array of
// {item_kind, quantity}, sorted by the item catalog — matching the v1 wire the
// editor's _on_inventory_response already parses.
func (s *Server) handleAdminNPCInventory(w http.ResponseWriter, r *http.Request) {
	var req adminNPCInventoryReadRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.GetActorInventory(sim.ActorID(req.NPCID)).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.ActorInventoryResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected inventory result")
		return
	}
	writeJSON(w, inventoryRowsToWire(out.Rows))
}

// handleAdminNPCSetInventory replaces an NPC's inventory wholesale (v2 port of
// v1's PUT /api/village/npcs/{id}/inventory). Responds 204 on success (no body),
// matching the v1 contract the editor's _on_inventory_save_response expects.
func (s *Server) handleAdminNPCSetInventory(w http.ResponseWriter, r *http.Request) {
	var req adminNPCInventoryWriteRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	rows := make([]sim.ActorInventoryRow, 0, len(req.Rows))
	for _, row := range req.Rows {
		rows = append(rows, sim.ActorInventoryRow{ItemKind: row.ItemKind, Quantity: row.Quantity})
	}
	_, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetActorInventory(sim.ActorID(req.NPCID), rows).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- delete ----

type adminNPCDeleteRequest struct {
	NPCID string `json:"npc_id"`
}

type adminNPCDeleteResponse struct {
	ID string `json:"id"`
}

// handleAdminNPCDelete removes an NPC (v2 port of v1's
// DELETE /api/village/npcs/{id}). The placing/removing client drops the sprite
// off the npc_deleted WS broadcast (not this response). 404 if the id is absent
// or a PC; 200 with the removed id otherwise.
func (s *Server) handleAdminNPCDelete(w http.ResponseWriter, r *http.Request) {
	var req adminNPCDeleteRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	if req.NPCID == "" {
		writeError(w, http.StatusBadRequest, "npc_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.DeleteActor(sim.ActorID(req.NPCID)).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.DeleteActorResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected delete result")
		return
	}
	writeJSON(w, adminNPCDeleteResponse{ID: string(out.ID)})
}

// ---- create ----

// x/y are world-pixel coordinates (the editor's click point), matching v1's
// create payload; the command converts to a tile via WorldPos.Tile().
type adminNPCCreateRequest struct {
	Name     string  `json:"name"`
	SpriteID string  `json:"sprite_id"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

type adminNPCCreateResponse struct {
	ID string `json:"id"`
}

// handleAdminNPCCreate materializes a new villager (v2 port of v1's
// POST /api/village/npcs). The placing client renders the NPC off the
// npc_created WS broadcast (not this response), which is why the body is just
// the new id — but it's returned so a caller could adopt it. name defaults to
// "Villager" in the command; sprite_id is required (ErrUnknownSprite → 400).
func (s *Server) handleAdminNPCCreate(w http.ResponseWriter, r *http.Request) {
	var req adminNPCCreateRequest
	username, ok := s.adminNPCRequest(w, r, &req)
	if !ok {
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.CreateNPC(req.Name, req.SpriteID, sim.WorldPos{X: req.X, Y: req.Y}, time.Now()).Fn(world)
	}))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.CreateNPCResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected create result")
		return
	}
	writeJSON(w, adminNPCCreateResponse{ID: string(out.ActorID)})
}
