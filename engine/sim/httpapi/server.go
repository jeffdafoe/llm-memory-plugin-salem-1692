package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Server serves the read surface for one world. It holds the *sim.World and
// reads world.Published() per request — lock-free, no command channel. Safe
// for concurrent requests: every handler only reads the immutable snapshot.
type Server struct {
	world *sim.World
}

// NewServer builds a Server for w. Panics on nil w — a wiring bug.
func NewServer(w *sim.World) *Server {
	if w == nil {
		panic("httpapi: NewServer requires a non-nil world")
	}
	return &Server{world: w}
}

// Handler returns the read-surface routes. Slice 2 phase 1 — the static-render
// read set (world / agents / objects). Terrain, assets, the WS /events
// endpoint, and write routes land in later phases. Reads are unauthenticated
// during the validation phase; auth middleware ports with the write routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/village/world", s.handleWorld)
	mux.HandleFunc("GET /api/village/agents", s.handleAgents)
	mux.HandleFunc("GET /api/village/objects", s.handleObjects)
	return mux
}

func (s *Server) handleWorld(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, worldStateFromSnapshot(snap))
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, agentsFromSnapshot(snap))
}

func (s *Server) handleObjects(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, objectsFromSnapshot(snap))
}

// worldStateFromSnapshot maps the snapshot's world-level state to the wire DTO.
func worldStateFromSnapshot(s *sim.Snapshot) WorldStateDTO {
	return WorldStateDTO{
		ContractVersion: ContractVersion,
		Phase:           string(s.Phase),
		Tick:            s.AtTick,
		Now:             s.Environment.Now,
		Weather:         s.Environment.Weather,
		Atmosphere:      s.Environment.Atmosphere,
	}
}

// agentsFromSnapshot maps every actor to an AgentDTO, sorted by ID so the
// response is deterministic (stable client diffs + testable).
func agentsFromSnapshot(s *sim.Snapshot) []AgentDTO {
	out := make([]AgentDTO, 0, len(s.Actors))
	for id, a := range s.Actors {
		out = append(out, AgentDTO{
			ID:                string(id),
			DisplayName:       a.DisplayName,
			Kind:              actorKindString(a.Kind),
			State:             string(a.State),
			Role:              a.Role,
			X:                 a.CurrentX,
			Y:                 a.CurrentY,
			InsideStructureID: string(a.InsideStructureID),
			CurrentHuddleID:   string(a.CurrentHuddleID),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// objectsFromSnapshot maps every village object to an ObjectDTO, sorted by ID.
func objectsFromSnapshot(s *sim.Snapshot) []ObjectDTO {
	out := make([]ObjectDTO, 0, len(s.VillageObjects))
	for id, o := range s.VillageObjects {
		if o == nil {
			continue
		}
		out = append(out, ObjectDTO{
			ID:           string(id),
			AssetID:      string(o.AssetID),
			X:            o.X,
			Y:            o.Y,
			CurrentState: o.CurrentState,
			DisplayName:  o.DisplayName,
			Tags:         o.Tags,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// writeJSON encodes v as the JSON response body. A late encode error (after
// the 200 header is sent) can't be recovered into a status code, so it's
// logged — the client sees a truncated body and re-syncs via its next fetch.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
