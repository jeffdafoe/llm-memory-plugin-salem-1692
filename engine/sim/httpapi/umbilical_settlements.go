package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_settlements.go — the GET /api/village/umbilical/settlements read route
// (LLM-105). The durable operator audit lens for "did a free-food settlement
// happen": accepted pay-with-item settlements read off the agent_action_log `paid`
// beat, most-recent first, optionally narrowed by buyer / time window / ledger id.
//
// The companion to /transcript: /transcript?huddle= gives one conversation's full
// trail, but a settlement that happened OUTSIDE a huddle (a solo tick, conversation
// _id NULL) never lands in a huddle transcript — this route reaches it by buyer +
// time. The headline audit signal is `free` (amount 0 AND no goods), which the
// enriched payload (LLM-105) makes unambiguous; a pre-LLM-105 row never recorded a
// goods leg, so its `free` cannot be trusted — those rows carry has_legacy=true.
//
// Operator-gated like every umbilical route; 503 when no settlement store is wired
// (a headless engine with no pg pool), mirroring /transcript.

// SettlementStore reads durable accepted settlements off the `paid` audit beat.
// *pg.ActionLogRepo satisfies it (LoadSettlements); the narrow seam keeps httpapi
// off the pg package, the same dependency-inversion HuddleTranscriptStore uses.
type SettlementStore interface {
	LoadSettlements(ctx context.Context, filter sim.SettlementFilter, limit int) ([]sim.SettlementRow, error)
}

// SettlementPayItemDTO is one barter good on the wire.
type SettlementPayItemDTO struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// SettlementEntryDTO is one accepted settlement: who paid whom, the coins + goods,
// and the at-a-glance `free` flag (a give-away — amount 0 AND no goods). ledger_id /
// consume_now / free are only trustworthy when has_legacy is false; a has_legacy row
// predates the LLM-105 payload enrichment, so its goods leg was never recorded and a
// 0-coin barter there is indistinguishable from a give-away.
type SettlementEntryDTO struct {
	OccurredAt time.Time              `json:"occurred_at"`
	BuyerID    string                 `json:"buyer_id"`
	BuyerName  string                 `json:"buyer_name,omitempty"`
	SellerName string                 `json:"seller_name,omitempty"`
	Item       string                 `json:"item,omitempty"`
	Amount     int                    `json:"amount"`
	PayItems   []SettlementPayItemDTO `json:"pay_items,omitempty"`
	ConsumeNow bool                   `json:"consume_now"`
	LedgerID   uint64                 `json:"ledger_id,omitempty"`
	HuddleID   string                 `json:"huddle_id,omitempty"`
	Free       bool                   `json:"free"`
	HasLegacy  bool                   `json:"has_legacy,omitempty"`
}

// UmbilicalSettlementsDTO is the GET /umbilical/settlements response, most-recent
// first. has_more is true only if the read hit the cap, so truncation is never
// silent (the same anti-silent-truncation contract /transcript carries).
type UmbilicalSettlementsDTO struct {
	ContractVersion int                  `json:"contract_version"`
	Returned        int                  `json:"returned"`
	HasMore         bool                 `json:"has_more"`
	Settlements     []SettlementEntryDTO `json:"settlements"`
}

// handleUmbilicalSettlements serves durable accepted settlements, most-recent first.
// Optional query params: actor (buyer id), since / until (RFC3339, occurred_at
// window [since, until)), ledger (a ledger id; matches post-LLM-105 rows only),
// limit (same parse + cap as /actions). 400 on an unparseable since/until/ledger;
// 503 when no store is wired. Over-fetches one row past the cap to set has_more
// without a COUNT, then trims — the same trick /transcript uses.
func (s *Server) handleUmbilicalSettlements(w http.ResponseWriter, r *http.Request) {
	if s.settlements == nil {
		writeError(w, http.StatusServiceUnavailable, "settlement store not configured")
		return
	}
	q := r.URL.Query()
	filter := sim.SettlementFilter{ActorID: sim.ActorID(q.Get("actor"))}
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		filter.Since = t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "until must be RFC3339")
			return
		}
		filter.Until = t
	}
	if raw := q.Get("ledger"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ledger must be a positive integer")
			return
		}
		filter.LedgerID = sim.LedgerID(id)
	}

	limit := parseActionsLimit(q.Get("limit"))
	rows, err := s.settlements.LoadSettlements(r.Context(), filter, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load settlements")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	out := UmbilicalSettlementsDTO{
		ContractVersion: ContractVersion,
		Returned:        len(rows),
		HasMore:         hasMore,
		Settlements:     make([]SettlementEntryDTO, 0, len(rows)),
	}
	for _, row := range rows {
		entry := SettlementEntryDTO{
			OccurredAt: row.OccurredAt,
			BuyerID:    string(row.BuyerID),
			BuyerName:  row.BuyerName,
			SellerName: row.SellerName,
			Item:       row.Item,
			Amount:     row.Amount,
			ConsumeNow: row.ConsumeNow,
			LedgerID:   uint64(row.LedgerID),
			HuddleID:   row.HuddleID,
			Free:       row.Amount == 0 && len(row.PayItems) == 0,
			HasLegacy:  row.LedgerID == 0, // ledger ids start at 1; 0 ⇒ a pre-LLM-105 row
		}
		for _, pi := range row.PayItems {
			entry.PayItems = append(entry.PayItems, SettlementPayItemDTO{Item: string(pi.Kind), Qty: pi.Qty})
		}
		out.Settlements = append(out.Settlements, entry)
	}
	writeJSON(w, out)
}
