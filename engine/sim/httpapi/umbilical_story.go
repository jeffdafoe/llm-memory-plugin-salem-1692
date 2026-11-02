package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_story.go — LLM-892. GET /umbilical/story shows the players' story
// (Standing, rank, the view the client gets, and the raw rows); POST
// /umbilical/story/reset wipes one player's story and starts it again, for
// testing.

type umbilicalStoryRowDTO struct {
	MissionID    string `json:"mission_id"`
	OfferedAt    string `json:"offered_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
	OfferSeenAt  string `json:"offer_seen_at,omitempty"`
	FinishSeenAt string `json:"finish_seen_at,omitempty"`
	CaughtUp     bool   `json:"caught_up,omitempty"`
}

type umbilicalStoryPlayerDTO struct {
	ActorID          string                 `json:"actor_id"`
	Name             string                 `json:"name"`
	Login            string                 `json:"login"`
	StandingRankSeen int                    `json:"standing_rank_seen"`
	Story            *storyWireDTO          `json:"story"`
	Rows             []umbilicalStoryRowDTO `json:"rows"`
}

type umbilicalStoryResponse struct {
	Players []umbilicalStoryPlayerDTO `json:"players"`
}

// handleUmbilicalStory — GET /umbilical/story[?id=<actor>]. Every player with a
// login by name, or the one named. 404 unknown actor, 422 not a player.
func (s *Server) handleUmbilicalStory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	res, err := s.world.SendContext(r.Context(), sim.StoryReadout(sim.ActorID(id)))
	if err != nil {
		writeStoryOperatorError(w, "story", id, err)
		return
	}
	entries, ok := res.([]sim.StoryReadoutEntry)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected story readout")
		return
	}
	out := umbilicalStoryResponse{Players: make([]umbilicalStoryPlayerDTO, 0, len(entries))}
	for _, e := range entries {
		p := umbilicalStoryPlayerDTO{
			ActorID: string(e.ActorID), Name: e.Name, Login: e.Login,
			StandingRankSeen: e.StandingRankSeen, Story: storyWire(e.View),
			Rows: make([]umbilicalStoryRowDTO, 0, len(e.Rows)),
		}
		for _, row := range e.Rows {
			p.Rows = append(p.Rows, umbilicalStoryRowDTO{
				MissionID:    string(row.MissionID),
				OfferedAt:    storyTimeWire(row.OfferedAt),
				FinishedAt:   storyTimeWire(row.FinishedAt),
				OfferSeenAt:  storyTimeWire(row.OfferSeenAt),
				FinishSeenAt: storyTimeWire(row.FinishSeenAt),
				CaughtUp:     row.CaughtUp,
			})
		}
		out.Players = append(out.Players, p)
	}
	writeJSON(w, out)
}

type umbilicalStoryResetRequest struct {
	ActorID string `json:"actor_id"`
}

// handleUmbilicalStoryReset — POST /umbilical/story/reset {actor_id}. Wipes the
// player's story and starts it again; answers with the new story. 400 missing
// actor_id, 404 unknown actor, 422 not a player.
func (s *Server) handleUmbilicalStoryReset(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalStoryResetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	req.ActorID = strings.TrimSpace(req.ActorID)
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	auditUmbilical(user.Username, "story-reset", fmt.Sprintf("actor=%s", req.ActorID))
	res, err := s.world.SendContext(r.Context(), sim.ResetStory(sim.ActorID(req.ActorID), time.Now().UTC()))
	if err != nil {
		writeStoryOperatorError(w, "story/reset", req.ActorID, err)
		return
	}
	view, ok := res.(sim.StoryView)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected story reset result")
		return
	}
	writeJSON(w, storyWire(view))
}

func writeStoryOperatorError(w http.ResponseWriter, route, actorID string, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
	case errors.Is(err, sim.ErrActorNotFound):
		writeError(w, http.StatusNotFound, "actor not found")
	case errors.Is(err, sim.ErrStoryNotPlayer):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		log.Printf("umbilical %s: actor=%s: %v", route, actorID, err)
		writeError(w, http.StatusInternalServerError, route+" failed")
	}
}
