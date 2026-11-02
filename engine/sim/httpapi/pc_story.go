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

// pc_story.go — LLM-892. The player's story on the wire: the `story` object
// that /pc/me carries, the login-scoped story_changed frame carries, and
// POST /api/village/pc/story/seen answers with. One DTO, one converter
// (storyWire), so the three never drift.

// storyWireDTO is a player's story view.
type storyWireDTO struct {
	Standing int                  `json:"standing"`
	Rank     storyRankWireDTO     `json:"rank"`
	NextRank *storyRankWireDTO    `json:"next_rank"`
	Prose    string               `json:"prose"`
	Mission  *storyMissionWireDTO `json:"mission"`
	Unseen   []storyUnseenWireDTO `json:"unseen"`
}

type storyRankWireDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	At   int    `json:"at"`
}

type storyMissionWireDTO struct {
	ID        string `json:"id"`
	Chapter   string `json:"chapter"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	OfferedAt string `json:"offered_at"`
}

// storyUnseenWireDTO is one popup to show. Fields a kind does not use are
// omitted.
type storyUnseenWireDTO struct {
	Key          string   `json:"key"`
	Kind         string   `json:"kind"`
	Chapter      string   `json:"chapter,omitempty"`
	Title        string   `json:"title,omitempty"`
	Titles       []string `json:"titles,omitempty"`
	Text         string   `json:"text"`
	StandingGain int      `json:"standing_gain,omitempty"`
	At           string   `json:"at,omitempty"`
}

func storyRankWire(r sim.StandingRank) storyRankWireDTO {
	return storyRankWireDTO{ID: r.ID, Name: r.Name, At: r.At}
}

func storyTimeWire(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// storyWire converts a sim.StoryView to its wire shape. unseen is always an
// array, never null.
func storyWire(v sim.StoryView) *storyWireDTO {
	out := &storyWireDTO{
		Standing: v.Standing,
		Rank:     storyRankWire(v.Rank),
		Prose:    v.Prose,
		Unseen:   make([]storyUnseenWireDTO, 0, len(v.Unseen)),
	}
	if v.NextRank != nil {
		next := storyRankWire(*v.NextRank)
		out.NextRank = &next
	}
	if m := v.Mission; m != nil {
		out.Mission = &storyMissionWireDTO{
			ID: string(m.ID), Chapter: m.Chapter, Title: m.Title, Text: m.Text, OfferedAt: storyTimeWire(m.OfferedAt),
		}
	}
	for _, u := range v.Unseen {
		out.Unseen = append(out.Unseen, storyUnseenWireDTO{
			Key: u.Key, Kind: u.Kind, Chapter: u.Chapter, Title: u.Title, Titles: u.Titles,
			Text: u.Text, StandingGain: u.StandingGain, At: storyTimeWire(u.At),
		})
	}
	return out
}

const maxStorySeenBodyBytes = 1 << 10

// storySeenRequest is the POST /api/village/pc/story/seen body.
type storySeenRequest struct {
	Key string `json:"key"`
}

// handlePCStorySeen — POST /api/village/pc/story/seen. Marks the popup with
// this key seen and answers with the player's story. Idempotent: a key already
// seen, or no longer unseen, is 200 with no change. 400 for an empty or badly
// formed key, 404 when the caller has no PC.
func (s *Server) handlePCStorySeen(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStorySeenBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req storySeenRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	now := time.Now().UTC()
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		actorID, ok := findPCByLogin(world, user.Username)
		if !ok {
			return nil, errPCNotFound
		}
		sim.TouchPCInput(world, actorID, now)
		return sim.MarkStorySeen(actorID, key, now).Fn(world)
	}})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, errPCNotFound):
			writeError(w, http.StatusNotFound, "no character")
		case errors.Is(err, sim.ErrStoryBadKey):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	view, ok := res.(sim.StoryView)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected result")
		return
	}
	writeJSON(w, storyWire(view))
}
