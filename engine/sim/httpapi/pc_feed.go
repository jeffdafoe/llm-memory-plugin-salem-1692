package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_feed.go — LLM-797. A player feeds a milking animal: the client asks "Feed
// the cow some sage?" on a click and posts here on Yes. sim.FeedLivestock (the
// same command an NPC's milking completion uses) owns every world check — the
// animal gives milk, she is not fed already, the player holds the feed, and
// stands beside her.

const maxFeedBodyBytes = 4 << 10

// pcFeedRequest is the POST /api/village/pc/feed body: the animal's actor id.
// The feeder is the caller's own PC.
type pcFeedRequest struct {
	ActorID string `json:"actor_id"`
}

type pcFeedResponse struct {
	Animal string `json:"animal"`
	Item   string `json:"item"`
}

func (s *Server) handlePCFeed(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFeedBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcFeedRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	animalID := strings.TrimSpace(req.ActorID)
	if animalID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), feedPCCommand(user.Username, sim.ActorID(animalID)))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Not livestock / already fed / no feed / not beside her: the request was
		// well-formed but the world says no. The message is player-facing.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.FeedResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected feed result")
		return
	}
	writeJSON(w, pcFeedResponse{Animal: out.AnimalName, Item: string(sim.LivestockFeedItem)})
}

// feedPCCommand resolves username → PC on the world goroutine and feeds. A
// deliberate PC action, so it stamps the input cursor like pc/gather.
func feedPCCommand(username string, animalID sim.ActorID) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			return sim.FeedLivestock(actorID, animalID, now).Fn(world)
		},
	}
}
