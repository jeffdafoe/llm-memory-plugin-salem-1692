package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_sprite.go — POST /api/village/pc/sprite, the PC sprite-swap route. The
// sprite picker (and a future settings affordance) lets a player change their
// character sprite. v1 broadcast pc_appeared; v2 instead lets the change ride
// the published snapshot — other clients pick up the new sprite_id from
// agentsFromSnapshot on their next /agents poll, and the changing client
// re-polls pc/me — so no WS event is needed. The PC must already exist
// (pc/create materializes it); a session with no PC gets 404.

const maxSpriteBodyBytes = 4 << 10

// errUnknownSprite — the requested sprite_id isn't in the catalog. Returned by
// the command so the handler maps it to 400 by identity, not string match.
var errUnknownSprite = errors.New("unknown sprite_id")

type pcSpriteRequest struct {
	SpriteID string `json:"sprite_id"`
}

type pcSpriteResponse struct {
	SpriteID string `json:"sprite_id"`
}

// handlePCSprite sets the caller's PC sprite. requireAuth has populated the
// session AuthUser; the PC is resolved from it inside the command (world
// goroutine, no TOCTOU). The command validates the sprite against the catalog
// and mutates the live actor; the change surfaces on the next published snapshot.
func (s *Server) handlePCSprite(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSpriteBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcSpriteRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	spriteID := strings.TrimSpace(req.SpriteID)
	if spriteID == "" {
		writeError(w, http.StatusBadRequest, "sprite_id is required")
		return
	}

	_, err := s.world.SendContext(r.Context(), setPCSpriteCommand(user.Username, spriteID))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, "pc not found — create a character first")
			return
		}
		if errors.Is(err, errUnknownSprite) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to set sprite")
		return
	}

	writeJSON(w, pcSpriteResponse{SpriteID: spriteID})
}

// setPCSpriteCommand resolves the session username to the PC actor (world
// goroutine) and sets its SpriteID after validating the id against the sprite
// catalog. Same session->actor identity rule as the other pc/* commands.
func setPCSpriteCommand(username, spriteID string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			if world.Sprites[sim.SpriteID(spriteID)] == nil {
				return nil, errUnknownSprite
			}
			world.Actors[actorID].SpriteID = sim.SpriteID(spriteID)
			return nil, nil
		},
	}
}
