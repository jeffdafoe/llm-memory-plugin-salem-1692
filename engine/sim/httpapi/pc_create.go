package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_create.go — POST /api/village/pc/create, PC onboarding. Thin
// validate-and-delegate shell over sim.CreatePC (which owns the materialization
// on the world goroutine — see engine/sim/create_pc.go). The caller is the
// authenticated session; there is no actor_id field (a caller can only create
// their own PC). Idempotent: re-creating an existing PC updates name/sprite.
// The client calls this on first login after the sprite picker; it then
// re-polls pc/me.

const maxCreateBodyBytes = 4 << 10

// maxCharacterNameChars mirrors v1's 100-char cap on the in-world display name.
const maxCharacterNameChars = 100

// pcCreateRequest is the POST body. character_name is required; sprite_id is
// optional ("" / omitted leaves the sprite unset so pc/me later opens the
// picker), matching v1's create-then-maybe-pick flow.
type pcCreateRequest struct {
	CharacterName string  `json:"character_name"`
	SpriteID      *string `json:"sprite_id,omitempty"`
}

// pcCreateResponse reports the PC's actor id and whether this call created it
// (vs. updated an already-existing PC). The client re-polls pc/me regardless.
type pcCreateResponse struct {
	ActorID string `json:"actor_id"`
	Created bool   `json:"created"`
}

func (s *Server) handlePCCreate(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCreateBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcCreateRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.CharacterName)
	if name == "" {
		writeError(w, http.StatusBadRequest, "character_name is required")
		return
	}
	if utf8.RuneCountInString(name) > maxCharacterNameChars {
		writeError(w, http.StatusBadRequest, "character_name exceeds the length limit")
		return
	}
	if hasInvalidControlChar(name) {
		writeError(w, http.StatusBadRequest, "character_name contains a disallowed control character")
		return
	}

	var spriteID string
	if req.SpriteID != nil {
		spriteID = strings.TrimSpace(*req.SpriteID)
	}

	res, err := s.world.SendContext(r.Context(), sim.CreatePC(user.Username, name, spriteID, time.Now().UTC()))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrUnknownSprite) {
			writeError(w, http.StatusBadRequest, "unknown sprite_id")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create PC")
		return
	}

	out, ok := res.(sim.CreatePCResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected create result")
		return
	}
	writeJSON(w, pcCreateResponse{ActorID: string(out.ActorID), Created: out.Created})
}
