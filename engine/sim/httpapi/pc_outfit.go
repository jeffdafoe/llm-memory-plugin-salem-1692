package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_outfit.go — the character creator's two routes (LLM-691).
//
//	POST /api/village/pc/wardrobe — what a player may wear (sim.PlayerWardrobe).
//	POST /api/village/pc/outfit   — dress the caller's PC in a farmer_base layer
//	                                list composed from that wardrobe.
//
// An outfit is a rig sprite of its own: one npc_sprite row per PC, its id fixed
// by sim.PCOutfitSpriteID so every save rewrites the same row. The row is
// written BEFORE the world points the PC at it — actor.sprite_id has a foreign
// key to npc_sprite, and the checkpoint that saves the actor must never see an
// id the table lacks. Saves are serialized (Server.outfitMu) so the table and
// the live catalog see them in the same order.

const maxOutfitBodyBytes = 16 << 10

// SpriteWriter is the durable half of a PC outfit save, injected by cmd/engine
// (Server.SetSpriteWriter) so httpapi doesn't import the pg package. nil on a
// mem-backed deploy → pc/outfit answers 503. pg.SpritesRepo satisfies it.
type SpriteWriter interface {
	UpsertRigSprite(ctx context.Context, sp *sim.Sprite) error
}

type pcOutfitRequest struct {
	Layers json.RawMessage `json:"layers"`
}

type pcOutfitResponse struct {
	SpriteID string `json:"sprite_id"`
}

func (s *Server) handlePCWardrobe(w http.ResponseWriter, r *http.Request) {
	if userFromContext(r.Context()) == nil {
		writeAuthError(w, "invalid")
		return
	}
	writeJSON(w, sim.PlayerWardrobe())
}

func (s *Server) handlePCOutfit(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	if s.spriteWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "outfits cannot be saved on this server")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxOutfitBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcOutfitRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	layers, err := sim.ValidateFarmerOutfit(req.Layers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	res, err := s.world.SendContext(r.Context(), sim.PCForLogin(user.Username))
	if err != nil {
		writeOutfitError(w, err)
		return
	}
	pc, ok := res.(sim.PCIdentity)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected pc lookup result")
		return
	}
	sprite, err := sim.NewPCOutfitSprite(pc.ID, pc.DisplayName, layers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The write and the install happen under one lock, in that order, and the
	// install ignores the request's cancellation: once the row holds the new
	// layers, the live catalog must hold them too, or the outfit changes on
	// the next restart.
	s.outfitMu.Lock()
	defer s.outfitMu.Unlock()
	if err := s.spriteWriter.UpsertRigSprite(r.Context(), sprite); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("pc outfit write: pc=%s sprite=%s: %v", pc.ID, sprite.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to save outfit")
		return
	}
	if _, err := s.world.SendContext(context.WithoutCancel(r.Context()), sim.SetPCOutfit(user.Username, sprite)); err != nil {
		writeOutfitError(w, err)
		return
	}
	writeJSON(w, pcOutfitResponse{SpriteID: string(sprite.ID)})
}

func writeOutfitError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if errors.Is(err, sim.ErrPCNotFound) {
		writeError(w, http.StatusNotFound, "pc not found — create a character first")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to save outfit")
}
