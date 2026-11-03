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

// umbilical_story.go — LLM-892. GET /umbilical/story shows the Standing ladder
// and the players' story (Standing, rank, the view the client gets, and the raw
// rows); POST /umbilical/story/reset wipes one player's story and starts it
// again, for testing. LLM-895: POST /umbilical/story/standing moves a player's
// Standing up or down, and POST /umbilical/story/rank/set moves a rank's
// threshold live.

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
	StandingRankSeen string                 `json:"standing_rank_seen"`
	Story            *storyWireDTO          `json:"story"`
	Rows             []umbilicalStoryRowDTO `json:"rows"`
}

// umbilicalStoryRankDTO is one rung of the ladder, lowest first.
type umbilicalStoryRankDTO struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	At       int      `json:"at"`
	Text     string   `json:"text,omitempty"`
	DownText string   `json:"down_text,omitempty"`
	Prose    []string `json:"prose"`
}

type umbilicalStoryResponse struct {
	Ranks   []umbilicalStoryRankDTO   `json:"ranks"`
	Players []umbilicalStoryPlayerDTO `json:"players"`
}

func umbilicalStoryRanks(ranks []sim.StandingRank) []umbilicalStoryRankDTO {
	out := make([]umbilicalStoryRankDTO, 0, len(ranks))
	for _, r := range ranks {
		prose := r.Prose
		if prose == nil {
			prose = []string{}
		}
		out = append(out, umbilicalStoryRankDTO{ID: r.ID, Name: r.Name, At: r.At, Text: r.Text, DownText: r.DownText, Prose: prose})
	}
	return out
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
	readout, ok := res.(sim.StoryReadoutResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected story readout")
		return
	}
	entries := readout.Players
	out := umbilicalStoryResponse{
		Ranks:   umbilicalStoryRanks(readout.Ranks),
		Players: make([]umbilicalStoryPlayerDTO, 0, len(entries)),
	}
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

// StandingRankWriter is the durable half of the threshold edit, injected by
// cmd/engine (Server.SetStandingRankWriter) so httpapi doesn't import the pg
// package. nil on a deploy without it wired → the route answers 503.
// pg.StoryRepo satisfies it.
type StandingRankWriter interface {
	SetStandingRankThreshold(ctx context.Context, id string, at int) error
	LoadRanks(ctx context.Context) ([]sim.StandingRank, error)
}

type umbilicalStoryStandingRequest struct {
	ActorID string `json:"actor_id"`
	Delta   int    `json:"delta"`
	Reason  string `json:"reason"`
}

type umbilicalStoryStandingResponse struct {
	Applied int           `json:"applied"`
	Story   *storyWireDTO `json:"story"`
}

// handleUmbilicalStoryStanding — POST /umbilical/story/standing {actor_id,
// delta, reason}. Moves the player's Standing by delta (negative lowers it),
// never below 0; the rank follows and the player sees a rank-up or rank-down
// popup. Answers with the change applied and the new story. 400 missing
// actor_id or a zero delta, 404 unknown actor, 422 not a player.
func (s *Server) handleUmbilicalStoryStanding(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalStoryStandingRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	req.ActorID = strings.TrimSpace(req.ActorID)
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	if req.Delta == 0 || req.Delta > sim.MaxStanding || req.Delta < -sim.MaxStanding {
		writeError(w, http.StatusBadRequest, "delta must be non-zero and within the most Standing a player can hold")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "operator"
	}
	auditUmbilical(user.Username, "story-standing", fmt.Sprintf("actor=%s delta=%d reason=%q", req.ActorID, req.Delta, reason))
	res, err := s.world.SendContext(r.Context(), sim.AdjustStanding(sim.ActorID(req.ActorID), req.Delta, "umbilical: "+reason, time.Now().UTC()))
	if err != nil {
		writeStoryOperatorError(w, "story/standing", req.ActorID, err)
		return
	}
	adj, ok := res.(sim.StandingAdjusted)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected story standing result")
		return
	}
	writeJSON(w, umbilicalStoryStandingResponse{Applied: adj.Applied, Story: storyWire(adj.View)})
}

// rankReconcileTimeout bounds the read-back and live swap after a committed
// threshold write.
const rankReconcileTimeout = 10 * time.Second

type umbilicalStoryRankSetRequest struct {
	ID string `json:"id"`
	At int    `json:"at"`
}

// handleUmbilicalStoryRankSet — POST /umbilical/story/rank/set {id, at}. Moves
// one rank's threshold: standing_rank first (the source of truth), then the
// whole table is read back and the live ladder is rebuilt from it, so the live
// ladder is always what the next boot would build. The write, the read-back and
// the swap run under rankEditMu, so two edits cannot cross and leave the table
// and the live ladder disagreeing. The bottom rank stays at 0. Answers with the
// ladder. 400 missing id or at outside 1..MaxStanding, 404 no such rank above
// the bottom, 422 when ties would push a rank past MaxStanding, 503 when the
// writer isn't wired.
//
// Once the write lands, the read-back and the swap run on a server-owned
// context (rankReconcileTimeout, not cancelled by the client going away), so a
// committed edit always reaches the live ladder or its failure is logged.
func (s *Server) handleUmbilicalStoryRankSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalStoryRankSetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.At < 1 || req.At > sim.MaxStanding {
		writeError(w, http.StatusBadRequest, "at must be 1 or more, and within the most Standing a player can hold; the bottom rank is always at 0")
		return
	}
	if s.standingRankWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "rank editing is not wired on this deploy")
		return
	}
	auditUmbilical(user.Username, "story-rank-set", fmt.Sprintf("id=%s at=%d", req.ID, req.At))
	s.rankEditMu.Lock()
	defer s.rankEditMu.Unlock()
	current, err := s.standingRankWriter.LoadRanks(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("umbilical story/rank/set: id=%s reading the ladder: %v", req.ID, err)
		writeError(w, http.StatusInternalServerError, "rank read failed")
		return
	}
	for i := range current {
		if current[i].ID == req.ID {
			current[i].At = req.At
		}
	}
	if !sim.StandingRanksFit(current) {
		writeError(w, http.StatusUnprocessableEntity, "that threshold leaves a rank no room below the most Standing a player can hold")
		return
	}
	if err := s.standingRankWriter.SetStandingRankThreshold(r.Context(), req.ID, req.At); err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		case errors.Is(err, sim.ErrStandingRankUnknown):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			log.Printf("umbilical story/rank/set: id=%s: %v", req.ID, err)
			writeError(w, http.StatusInternalServerError, "rank write failed")
		}
		return
	}
	// The write is committed: finish on a context the client cannot cancel.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), rankReconcileTimeout)
	defer cancel()
	raw, err := s.standingRankWriter.LoadRanks(ctx)
	if err != nil {
		log.Printf("umbilical story/rank/set: id=%s persisted but the read-back failed: %v", req.ID, err)
		writeError(w, http.StatusInternalServerError, "rank persisted but live update failed; applies on next load")
		return
	}
	res, err := s.world.SendContext(ctx, sim.ReplaceStandingRanks(raw, time.Now().UTC()))
	if err != nil {
		// The durable write landed; the ladder picks it up on the next load.
		log.Printf("umbilical story/rank/set: id=%s persisted but live update failed: %v", req.ID, err)
		writeError(w, http.StatusInternalServerError, "rank persisted but live update failed; applies on next load")
		return
	}
	ranks, ok := res.([]sim.StandingRank)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected rank set result")
		return
	}
	writeJSON(w, umbilicalStoryRanks(ranks))
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
