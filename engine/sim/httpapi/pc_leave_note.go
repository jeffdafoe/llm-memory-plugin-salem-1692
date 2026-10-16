package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_leave_note.go — POST /api/village/pc/leave-note (LLM-19): the note window
// the door popup opens after a knock nobody answered. sim.LeaveDoorNote owns
// the checks (still at that door, nobody answering, the recipient one of the
// house) and the note itself. The text is checked here like player speech, and
// capped at sim.MaxDoorNoteChars.

const maxLeaveNoteBodyBytes = 8 << 10

type pcLeaveNoteRequest struct {
	StructureID string `json:"structure_id"`
	RecipientID string `json:"recipient_id"`
	Text        string `json:"text"`
}

func (s *Server) handlePCLeaveNote(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLeaveNoteBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcLeaveNoteRequest
	if err := dec.Decode(&req); err != nil || req.StructureID == "" || req.RecipientID == "" {
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
	if utf8.RuneCountInString(text) > sim.MaxDoorNoteChars {
		writeError(w, http.StatusBadRequest, sim.ErrDoorNoteText.Error())
		return
	}
	_, err := s.world.SendContext(r.Context(), leaveNotePCCommand(user.Username,
		sim.StructureID(req.StructureID), sim.ActorID(req.RecipientID), text))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		status, msg := leaveNoteErrorResponse(err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func leaveNoteErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, errPCNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, sim.ErrDoorNoteText):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, sim.ErrDoorNoteRefused):
		return http.StatusUnprocessableEntity, err.Error()
	}
	return http.StatusInternalServerError, "leaving the note failed"
}

func leaveNotePCCommand(username string, structureID sim.StructureID, recipientID sim.ActorID, text string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			return sim.LeaveDoorNote(actorID, structureID, recipientID, text, now).Fn(world)
		},
	}
}
