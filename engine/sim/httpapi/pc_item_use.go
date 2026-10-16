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

// pc_item_use.go — POST /api/village/pc/item/use (LLM-800): one item action
// from the Your Pack list, e.g. {"item_kind":"duck_pendant","action":"wear"}.
// The actions a row offers come from /pc/me (pcInventoryEntry.Actions);
// sim.UseItem owns the checks and the effect. Success returns the pack as it now
// stands; a refusal comes back 422 with a message the pack shows as it is.

const maxItemUseBodyBytes = 1 << 10

type pcItemUseRequest struct {
	ItemKind string `json:"item_kind"`
	Action   string `json:"action"`
}

func (s *Server) handlePCItemUse(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxItemUseBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcItemUseRequest
	if err := dec.Decode(&req); err != nil || req.ItemKind == "" || req.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := s.world.SendContext(r.Context(), itemUsePCCommand(user.Username, sim.ItemKind(req.ItemKind), req.Action))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		status, msg := itemUseErrorResponse(err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, pcItemUseResponse{OK: true, Inventory: res.([]pcInventoryEntry)})
}

// pcItemUseResponse carries the pack as it stands after the action, so the
// Your Pack list redraws from it at once instead of waiting on /pc/me.
type pcItemUseResponse struct {
	OK        bool               `json:"ok"`
	Inventory []pcInventoryEntry `json:"inventory"`
}

func itemUseErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, errPCNotFound), errors.Is(err, sim.ErrPCNotFound):
		return http.StatusNotFound, "pc not found"
	case errors.Is(err, sim.ErrItemAction):
		return http.StatusUnprocessableEntity, err.Error()
	}
	return http.StatusInternalServerError, "that did not work"
}

func itemUsePCCommand(username string, kind sim.ItemKind, action string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			if _, err := sim.UseItem(actorID, kind, action, now).Fn(world); err != nil {
				return nil, err
			}
			a := world.Actors[actorID]
			return pcPackEntries(a.Inventory, a.Wearing, world.ItemKinds, world.VillageObjects, world.Structures, actorID), nil
		},
	}
}
