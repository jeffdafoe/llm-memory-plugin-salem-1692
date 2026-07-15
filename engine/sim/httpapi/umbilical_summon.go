package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_summon.go — LLM-414. The summon-errand observability read: the
// errand FSM is in-memory only (World.SummonErrands) and its terminal paths
// DELETE the entry, so before this route the action log showed movement but
// never which state an errand was in — or, for a finished one, where it died.
// The live incident (keeper agreed, target went home, no meeting) was
// undiagnosable for exactly that reason. This surfaces both halves: the live
// errands with their state-transition history, and the bounded ring of
// recently finished errands with outcomes.

// UmbilicalSummonErrandsDTO is the GET /api/village/umbilical/summon-errands
// response. Active is dispatch-ordered; Recent is oldest-first (the ring's
// natural order), each entry carrying its outcome + full state history.
type UmbilicalSummonErrandsDTO struct {
	ContractVersion int                         `json:"contract_version"`
	Now             time.Time                   `json:"now"`
	Active          []sim.ActiveSummonErrandDTO `json:"active"`
	Recent          []sim.FinishedSummonErrand  `json:"recent"`
}

// handleUmbilicalSummonErrands serves the live + recent summon-errand state.
// Read live on the world goroutine via the exported report command (the
// errand types are unexported; the command returns wire-ready DTOs).
func (s *Server) handleUmbilicalSummonErrands(w http.ResponseWriter, r *http.Request) {
	res, err := s.world.SendContext(r.Context(), sim.SummonErrandsReport())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	report := res.(sim.SummonErrandsReportResult)
	writeJSON(w, UmbilicalSummonErrandsDTO{
		ContractVersion: ContractVersion,
		Now:             time.Now().UTC(),
		Active:          report.Active,
		Recent:          report.Recent,
	})
}
