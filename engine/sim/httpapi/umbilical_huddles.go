package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_huddles.go — the live huddle-inspection read routes (ZBBS-WORK-431):
//
//   /umbilical/huddles      — list ACTIVE huddles (the "spot a stuck/one-sided/
//                             idle conversation" sweep).
//   /umbilical/huddle?id=   — one huddle's detail + its recent-conversation ring.
//
// Both read LIVE world state via SendContext (same pattern as /agent and /actors):
// the closure copies plain values into the DTO on the world goroutine and returns
// them — no *Huddle / *Actor pointer escapes to the HTTP goroutine, so there's no
// shared-state race. Pure reads: they mutate nothing.
//
// These complement the Tier-1 /turns?conversation= filter: huddles are IN-MEMORY
// and boot-cleared (see engine/sim/huddle.go), so these routes only see huddles in
// the CURRENTLY-running engine — they cannot resurrect a pre-restart conversation.
// For a PAST huddle, /turns?conversation=<id> is the durable lookup (it reads
// memory-api's virtual_agent_calls rows, which survive a restart). The huddle id
// IS the conversation_id, so /umbilical/huddle surfaces it as the pivot.
//
// Gated by requireOperator (salem realm + plugins/administer) and registered only
// when the umbilical is enabled — both inherited from the umbilicalRoutes()
// descriptor table, the same as every other umbilical read route.

// UmbilicalHuddleMemberDTO is one member of a huddle on the wire, with the count
// of that member's lines in the (capped-at-8) recent-utterance ring. The
// per-member count is the one-sided-conversation signal: a member with 0 in a
// huddle whose ring is otherwise full is the tell (the worked example — John
// pitching Ezekiel a room six times while Ezekiel never speaks). It is a RECENT
// count (the ring holds only the last MaxRecentUtterancesPerHuddle lines), not a
// lifetime total — there is no lifetime utterance counter in memory.
type UmbilicalHuddleMemberDTO struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	RecentUtterances int    `json:"recent_utterances"`
}

// UmbilicalHuddleRowDTO is one active huddle on the /huddles list. Carries member
// identities + per-member recent counts (not the utterance text — that's the
// detail view) so the list stays compact while still surfacing the silent member.
type UmbilicalHuddleRowDTO struct {
	ID             string                     `json:"id"`
	StructureID    string                     `json:"structure_id,omitempty"`
	StructureName  string                     `json:"structure_name,omitempty"`
	MemberCount    int                        `json:"member_count"`
	Members        []UmbilicalHuddleMemberDTO `json:"members"`
	StartedAt      time.Time                  `json:"started_at"`
	LastActivityAt time.Time                  `json:"last_activity_at"`
	// RecentUtteranceCount is the number of lines currently in the ring (<= 8) —
	// a coarse "how chatty / how stale" signal alongside last_activity_at.
	RecentUtteranceCount int `json:"recent_utterance_count"`
}

// UmbilicalHuddlesDTO is the GET /api/village/umbilical/huddles response: every
// ACTIVE huddle (concluded ones are filtered out), most-recently-active first.
type UmbilicalHuddlesDTO struct {
	ContractVersion int                     `json:"contract_version"`
	Now             time.Time               `json:"now"`
	Total           int                     `json:"total"`
	Huddles         []UmbilicalHuddleRowDTO `json:"huddles"`
}

// UmbilicalUtteranceDTO is one spoken line from a huddle's recent-conversation
// ring on the wire. SpeakerName is denormalized in the ring at write time.
type UmbilicalUtteranceDTO struct {
	SpeakerID   string    `json:"speaker_id"`
	SpeakerName string    `json:"speaker_name,omitempty"`
	Text        string    `json:"text"`
	At          time.Time `json:"at"`
}

// UmbilicalHuddleDetailDTO is the GET /api/village/umbilical/huddle?id= response:
// one huddle's full live picture, including the recent-conversation ring so an
// operator reads the actual back-and-forth. ConversationID equals ID — the value
// to pass to /turns?conversation= for the full raw LLM turns of this conversation.
type UmbilicalHuddleDetailDTO struct {
	ContractVersion  int                        `json:"contract_version"`
	ID               string                     `json:"id"`
	ConversationID   string                     `json:"conversation_id"`
	StructureID      string                     `json:"structure_id,omitempty"`
	StructureName    string                     `json:"structure_name,omitempty"`
	StartedAt        time.Time                  `json:"started_at"`
	LastActivityAt   time.Time                  `json:"last_activity_at"`
	ConcludedAt      *time.Time                 `json:"concluded_at,omitempty"`
	MemberCount      int                        `json:"member_count"`
	Members          []UmbilicalHuddleMemberDTO `json:"members"`
	RecentUtterances []UmbilicalUtteranceDTO    `json:"recent_utterances"`
}

// errHuddleNotFound is returned by the huddle-detail command when the id is unknown.
var errHuddleNotFound = errors.New("huddle not found")

// handleUmbilicalHuddles serves the list of active huddles. Read live on the world
// goroutine (no *Huddle escapes), sorted most-recently-active first with an id
// tiebreak for a stable read. Pure read.
func (s *Server) handleUmbilicalHuddles(w http.ResponseWriter, r *http.Request) {
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		dto := UmbilicalHuddlesDTO{
			ContractVersion: ContractVersion,
			Now:             time.Now().UTC(),
			Huddles:         make([]UmbilicalHuddleRowDTO, 0, len(world.Huddles)),
		}
		for _, h := range world.Huddles {
			// List shows ACTIVE huddles only — a concluded huddle that lingers in
			// the map before boot-clear is not a live conversation.
			if h == nil || h.ConcludedAt != nil {
				continue
			}
			dto.Huddles = append(dto.Huddles, huddleRowDTO(world, h))
		}
		dto.Total = len(dto.Huddles)
		sort.Slice(dto.Huddles, func(i, j int) bool {
			li, lj := dto.Huddles[i].LastActivityAt, dto.Huddles[j].LastActivityAt
			if !li.Equal(lj) {
				return li.After(lj) // most-recently-active first
			}
			return dto.Huddles[i].ID < dto.Huddles[j].ID
		})
		return dto, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dto, ok := res.(UmbilicalHuddlesDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected huddles result")
		return
	}
	writeJSON(w, dto)
}

// handleUmbilicalHuddle serves one huddle's detail + recent-conversation ring.
// Query param `id` (required) is the HuddleID. 400 missing id, 404 unknown huddle,
// 200 ok. Returns a concluded-but-not-yet-cleared huddle too (with concluded_at
// set) so an operator can still read a just-ended conversation's ring.
func (s *Server) handleUmbilicalHuddle(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[sim.HuddleID(id)]
		if !ok || h == nil {
			return nil, errHuddleNotFound
		}
		dto := UmbilicalHuddleDetailDTO{
			ContractVersion:  ContractVersion,
			ID:               string(h.ID),
			ConversationID:   string(h.ID),
			StructureID:      string(h.StructureID),
			StructureName:    huddleStructureName(world, h.StructureID),
			StartedAt:        h.StartedAt,
			LastActivityAt:   h.LastActivityAt,
			ConcludedAt:      clonePtrTime(h.ConcludedAt),
			MemberCount:      len(h.Members),
			Members:          huddleMemberDTOs(world, h),
			RecentUtterances: make([]UmbilicalUtteranceDTO, 0, len(h.RecentUtterances)),
		}
		for _, u := range h.RecentUtterances {
			dto.RecentUtterances = append(dto.RecentUtterances, UmbilicalUtteranceDTO{
				SpeakerID:   string(u.SpeakerID),
				SpeakerName: u.SpeakerName,
				Text:        u.Text,
				At:          u.At,
			})
		}
		return dto, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errHuddleNotFound) {
			// Static message — the unknown id is untrusted input, never reflected.
			writeError(w, http.StatusNotFound, "huddle not found")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dto, ok := res.(UmbilicalHuddleDetailDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected huddle result")
		return
	}
	writeJSON(w, dto)
}

// huddleRowDTO copies one live huddle's list-row fields into a value DTO. Must run
// on the world goroutine (reads *Huddle + *Actor); no pointer escapes.
func huddleRowDTO(world *sim.World, h *sim.Huddle) UmbilicalHuddleRowDTO {
	return UmbilicalHuddleRowDTO{
		ID:                   string(h.ID),
		StructureID:          string(h.StructureID),
		StructureName:        huddleStructureName(world, h.StructureID),
		MemberCount:          len(h.Members),
		Members:              huddleMemberDTOs(world, h),
		StartedAt:            h.StartedAt,
		LastActivityAt:       h.LastActivityAt,
		RecentUtteranceCount: len(h.RecentUtterances),
	}
}

// huddleMemberDTOs resolves each current member's display name and counts that
// member's lines in the recent-utterance ring, sorted by id for a stable read.
// Utterances from a speaker who has since LEFT the huddle still live in the ring
// (and show in the detail view) but are not attributed to a current member here —
// the per-member count is "how much has each PRESENT member said recently", which
// is exactly the silent-member signal.
func huddleMemberDTOs(world *sim.World, h *sim.Huddle) []UmbilicalHuddleMemberDTO {
	counts := make(map[sim.ActorID]int, len(h.Members))
	for _, u := range h.RecentUtterances {
		counts[u.SpeakerID]++
	}
	members := make([]UmbilicalHuddleMemberDTO, 0, len(h.Members))
	for id := range h.Members {
		name := ""
		if a, ok := world.Actors[id]; ok && a != nil {
			name = a.DisplayName
		}
		members = append(members, UmbilicalHuddleMemberDTO{
			ID:               string(id),
			Name:             name,
			RecentUtterances: counts[id],
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return members
}

// huddleStructureName resolves a structure's display name, empty for an
// outdoor/structureless huddle (StartOutdoorHuddle mints those) or an unknown id.
func huddleStructureName(world *sim.World, id sim.StructureID) string {
	if id == "" {
		return ""
	}
	if st, ok := world.Structures[id]; ok && st != nil {
		return st.DisplayName
	}
	return ""
}
