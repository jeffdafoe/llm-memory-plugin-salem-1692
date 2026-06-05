package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_pay_ledger.go — ZBBS-HOME-392. The /api/village/umbilical/pay-ledger
// operator read route: dumps the LIVE pay-ledger off the published snapshot
// (deep-cloned at publish, so this read is lock-free + race-free), most-recent
// first. Built to debug the economy from in-memory state — the DB `pay_ledger`
// lags via the periodic checkpoint, so a just-committed (or post-restart)
// transaction is invisible there (the ZBBS-HOME-391 phantom-consume dig was
// blocked precisely on this). The per-entry buyer / seller / CONSUMER split plus
// coins + state is what distinguishes a legitimate paid purchase from a
// non-paying-consumer ride or a phantom.

// PayLedgerEntryDTO is one pay-ledger entry on the wire — sim.PayLedgerEntry's
// decision-relevant fields. The causal-trail event ids and co-presence scene/
// huddle ids are deliberately omitted (noise for economy debugging).
type PayLedgerEntryDTO struct {
	ID            int64     `json:"id"`
	BuyerID       string    `json:"buyer_id"`
	SellerID      string    `json:"seller_id"`
	ItemKind      string    `json:"item_kind"`
	Qty           int       `json:"qty"`
	ConsumeNow    bool      `json:"consume_now"`
	ConsumerIDs   []string  `json:"consumer_ids,omitempty"`
	Amount        int       `json:"amount"` // coins offered
	State         string    `json:"state"`
	QuoteID       int64     `json:"quote_id,omitempty"`
	ParentID      int64     `json:"parent_id,omitempty"`
	Depth         int       `json:"depth,omitempty"`
	CounterAmount int       `json:"counter_amount,omitempty"`
	Message       string    `json:"message,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	ResolvedAt    time.Time `json:"resolved_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// UmbilicalPayLedgerDTO is the GET /pay-ledger response.
type UmbilicalPayLedgerDTO struct {
	ContractVersion int                 `json:"contract_version"`
	Total           int                 `json:"total"`
	Returned        int                 `json:"returned"`
	Entries         []PayLedgerEntryDTO `json:"entries"`
}

// handleUmbilicalPayLedger dumps the live pay-ledger off the published snapshot,
// most-recent (highest id) first. Query param: limit (optional, same parse as
// /actions). A nil snapshot (nothing published yet) yields an empty list.
func (s *Server) handleUmbilicalPayLedger(w http.ResponseWriter, r *http.Request) {
	limit := parseActionsLimit(r.URL.Query().Get("limit"))
	writeJSON(w, umbilicalPayLedgerFromSnapshot(s.world.Published(), limit))
}

// umbilicalPayLedgerFromSnapshot maps the published snapshot's pay-ledger to the
// DTO, most-recent (highest id) first, capped to limit. Pure (no Server / world
// access) so it's unit-testable against a hand-built snapshot. A nil snapshot
// (nothing published yet) yields an empty list, not a panic.
func umbilicalPayLedgerFromSnapshot(snap *sim.Snapshot, limit int) UmbilicalPayLedgerDTO {
	out := UmbilicalPayLedgerDTO{
		ContractVersion: ContractVersion,
		Entries:         []PayLedgerEntryDTO{},
	}
	if snap == nil {
		return out
	}
	entries := make([]*sim.PayLedgerEntry, 0, len(snap.PayLedger))
	for _, e := range snap.PayLedger {
		if e != nil {
			entries = append(entries, e)
		}
	}
	out.Total = len(entries)

	// Most-recent first — the debugging entry point is "what just happened".
	// Ids are unique, so an id-desc sort is total + stable.
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID > entries[j].ID })

	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	out.Returned = len(entries)
	out.Entries = make([]PayLedgerEntryDTO, 0, len(entries))
	for _, e := range entries {
		out.Entries = append(out.Entries, payLedgerEntryDTO(e))
	}
	return out
}

// payLedgerEntryDTO maps one entry to its wire shape.
func payLedgerEntryDTO(e *sim.PayLedgerEntry) PayLedgerEntryDTO {
	dto := PayLedgerEntryDTO{
		ID:            int64(e.ID),
		BuyerID:       string(e.BuyerID),
		SellerID:      string(e.SellerID),
		ItemKind:      string(e.ItemKind),
		Qty:           e.Qty,
		ConsumeNow:    e.ConsumeNow,
		Amount:        e.Amount,
		State:         string(e.State),
		QuoteID:       int64(e.QuoteID),
		ParentID:      int64(e.ParentID),
		Depth:         e.Depth,
		CounterAmount: e.CounterAmount,
		Message:       e.Message,
		CreatedAt:     e.CreatedAt,
		ResolvedAt:    e.ResolvedAt,
		ExpiresAt:     e.ExpiresAt,
	}
	for _, c := range e.ConsumerIDs {
		dto.ConsumerIDs = append(dto.ConsumerIDs, string(c))
	}
	return dto
}
