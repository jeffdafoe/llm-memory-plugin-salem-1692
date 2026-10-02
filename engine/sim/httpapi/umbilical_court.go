package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_court.go — the operator's window on the magistrates (LLM-695).
//
//   GET  /umbilical/court/cases — every case, newest first, with its ruling.
//   POST /umbilical/court/file  — put a matter on the docket as if the named
//                                 villager brought it (control; no daily limit).
//                                 How the ledger case is seeded as the first case.
//   POST /umbilical/court/sit   — hear every pending case now, regardless of the
//                                 sitting time (control). Runs in the background.

// CourtSitter is the court runner as the umbilical sees it.
type CourtSitter interface {
	SitNow() ([]sim.CourtCaseID, error)
}

// SetCourt wires the court runner behind /umbilical/court/sit. Unset → 503.
func (s *Server) SetCourt(c CourtSitter) {
	s.court = c
}

type umbilicalCourtParty struct {
	ActorID string `json:"actor_id"`
	Name    string `json:"name"`
}

type umbilicalCourtCase struct {
	ID            string                `json:"id"`
	FiledAt       time.Time             `json:"filed_at"`
	FiledBy       string                `json:"filed_by"`
	Parties       []umbilicalCourtParty `json:"parties"`
	Complaint     string                `json:"complaint"`
	Status        string                `json:"status"`
	RuledAt       *time.Time            `json:"ruled_at,omitempty"`
	Result        string                `json:"result,omitempty"`
	FoundFor      string                `json:"found_for,omitempty"`
	Payer         string                `json:"payer,omitempty"`
	Payee         string                `json:"payee,omitempty"`
	AmountOrdered int                   `json:"amount_ordered,omitempty"`
	AmountPaid    int                   `json:"amount_paid,omitempty"`
	Words         string                `json:"words,omitempty"`
}

func toUmbilicalCourtCase(c *sim.CourtCase) umbilicalCourtCase {
	out := umbilicalCourtCase{
		ID:        string(c.ID),
		FiledAt:   c.FiledAt,
		FiledBy:   c.FiledByName,
		Complaint: c.Complaint,
		Status:    c.Status,
		Result:    string(c.Result),
		Words:     c.Words,
	}
	for _, p := range c.Parties {
		out.Parties = append(out.Parties, umbilicalCourtParty{ActorID: string(p.ActorID), Name: p.Name})
	}
	if c.Status == sim.CourtCaseStatusRuled {
		t := c.RuledAt
		out.RuledAt = &t
		out.FoundFor = c.PartyName(c.FoundForID)
		if c.Result == sim.CourtResultPay {
			out.Payer = c.PartyName(c.PayerID)
			out.Payee = c.PartyName(c.PayeeID)
			out.AmountOrdered = c.AmountOrdered
			out.AmountPaid = c.AmountPaid
		}
	}
	return out
}

func (s *Server) handleUmbilicalCourtCases(w http.ResponseWriter, r *http.Request) {
	res, err := s.world.SendContext(r.Context(), sim.CourtCaseList())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cases, _ := res.([]*sim.CourtCase)
	out := make([]umbilicalCourtCase, 0, len(cases))
	for _, c := range cases {
		out = append(out, toUmbilicalCourtCase(c))
	}
	writeJSON(w, map[string]any{"cases": out})
}

type umbilicalCourtFileRequest struct {
	FiledBy   string   `json:"filed_by"`
	Parties   []string `json:"parties"`
	Complaint string   `json:"complaint"`
}

func (s *Server) handleUmbilicalCourtFile(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalCourtFileRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FiledBy) == "" {
		writeError(w, http.StatusBadRequest, "filed_by is required")
		return
	}
	auditUmbilical(user.Username, "court.file", req.FiledBy)

	res, err := s.world.SendContext(r.Context(), sim.CourtResolveVillager(req.FiledBy))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	filer, _ := res.(sim.CourtVillager)
	res, err = s.world.SendContext(r.Context(), sim.FileCourtCase(filer.ID, req.Parties, req.Complaint, time.Now().UTC(), true))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	filed, _ := res.(sim.CourtFileResult)
	writeJSON(w, map[string]any{
		"case":     toUmbilicalCourtCase(filed.Case),
		"heard_at": filed.HeardAt,
	})
}

func (s *Server) handleUmbilicalCourtSit(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	if s.court == nil {
		writeError(w, http.StatusServiceUnavailable, "the court runner is not wired")
		return
	}
	auditUmbilical(user.Username, "court.sit", "")
	ids, err := s.court.SitNow()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	writeJSON(w, map[string]any{"hearing": out})
}
