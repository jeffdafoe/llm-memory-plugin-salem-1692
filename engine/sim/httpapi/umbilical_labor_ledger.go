package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_labor_ledger.go — LLM-272. The /api/village/umbilical/labor-ledger
// operator read route: dumps the LIVE labor ledger off the published snapshot
// (deep-cloned at publish via CloneLaborOffer, so this read is lock-free +
// race-free), most-recent first. It is the labor-side companion to /pay-ledger.
//
// Why it exists: a labor reward only transfers at job COMPLETION (the completion
// sweep in labor_settle.go), so an in-progress hire is invisible everywhere else
// — it is not in /settlements (not completed yet), and /agent?id= carried no
// labor fields (the enrichment below closes that too). A Pending / EnRoute /
// Working offer that hasn't settled reads as "the hire went missing" when the
// data is fine and just unobservable. This route is that missing window: per
// offer the worker + employer (id AND resolved display name), the state, both
// reward legs (coins + the LLM-225 in-kind goods), the duration, the co-presence
// huddle/scene ids, and every lifecycle timestamp (created / expires / accepted /
// work-started / working-until / en-route-deadline / resolved). Pure
// observability — no behavior change, and the LaborLedger has no durable
// projection (it is restart-lossy, like PayLedger), so in-memory is the only
// place these offers ever exist.

// LaborRewardItemDTO is one in-kind reward good on the wire — the LLM-225 goods
// leg of a labor reward (canonical item kind + qty). Mirrors the settlements
// route's pay-item shape so the two ledger views read the same.
type LaborRewardItemDTO struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// LaborOfferDTO is one labor offer on the wire — sim.LaborOffer's
// decision-relevant fields, plus the worker/employer display names resolved off
// the snapshot's actor roster (the ids alone are opaque when diagnosing a live
// hire). The causal-trail event ids (RootEventID / SourceEventID) are omitted as
// noise, matching /pay-ledger.
type LaborOfferDTO struct {
	ID           uint64 `json:"labor_id"`
	WorkerID     string `json:"worker_id"`
	WorkerName   string `json:"worker_name,omitempty"`
	EmployerID   string `json:"employer_id"`
	EmployerName string `json:"employer_name,omitempty"`
	// InitiatedBy names which party minted the offer — "worker" (solicit_work) or
	// "employer" (offer_work). ResponderID is the other one: the actor whose
	// accept_work / decline_work the offer is waiting on while Pending. Both are
	// derived rather than raw, because "who owes an answer" is the question you
	// actually ask this endpoint when a hire has stalled (LLM-346). A contract
	// restored from labor_contract across a restart carries no initiator (the
	// column is not persisted), so it reports "worker".
	InitiatedBy string `json:"initiated_by"`
	ResponderID string `json:"responder_id"`
	State       string `json:"state"`

	RewardCoins int                  `json:"reward_coins"`
	RewardItems []LaborRewardItemDTO `json:"reward_items,omitempty"`
	DurationMin int                  `json:"duration_min"`

	HuddleID string `json:"huddle_id,omitempty"`
	SceneID  string `json:"scene_id,omitempty"`

	// CreatedAt / ExpiresAt are always set at solicitation (mint + pending-TTL
	// deadline). The rest are reached progressively and stay nil / omitted until
	// their leg happens, so a still-Pending offer renders null rather than a zero
	// "0001-…" timestamp — the same convention /pay-ledger uses.
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	WorkStartedAt   *time.Time `json:"work_started_at,omitempty"`
	WorkingUntil    *time.Time `json:"working_until,omitempty"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	EnRouteDeadline *time.Time `json:"en_route_deadline,omitempty"` // set only for a relocated (EnRoute) hire
	EnRouteWaiting  bool       `json:"en_route_waiting,omitempty"`  // true once a relocating worker has arrived and is waiting for the owner
}

// UmbilicalLaborLedgerDTO is the GET /labor-ledger response.
type UmbilicalLaborLedgerDTO struct {
	ContractVersion int             `json:"contract_version"`
	Total           int             `json:"total"`
	Returned        int             `json:"returned"`
	Offers          []LaborOfferDTO `json:"offers"`
}

// handleUmbilicalLaborLedger dumps the live labor ledger off the published
// snapshot, most-recent (highest id) first. Query param: limit (optional, same
// parse as /actions). A nil snapshot (nothing published yet) yields an empty
// list.
func (s *Server) handleUmbilicalLaborLedger(w http.ResponseWriter, r *http.Request) {
	limit := parseActionsLimit(r.URL.Query().Get("limit"))
	writeJSON(w, umbilicalLaborLedgerFromSnapshot(s.world.Published(), limit))
}

// umbilicalLaborLedgerFromSnapshot maps the published snapshot's labor ledger to
// the DTO, most-recent (highest id) first, capped to limit. Pure (no Server /
// world access) so it's unit-testable against a hand-built snapshot. A nil
// snapshot (nothing published yet) yields an empty list, not a panic. Worker /
// employer names are resolved off snap.Actors; an unknown id just leaves the
// name empty (omitted).
func umbilicalLaborLedgerFromSnapshot(snap *sim.Snapshot, limit int) UmbilicalLaborLedgerDTO {
	out := UmbilicalLaborLedgerDTO{
		ContractVersion: ContractVersion,
		Offers:          []LaborOfferDTO{},
	}
	if snap == nil {
		return out
	}
	offers := make([]*sim.LaborOffer, 0, len(snap.LaborLedger))
	for _, o := range snap.LaborLedger {
		if o != nil {
			offers = append(offers, o)
		}
	}
	out.Total = len(offers)

	// Most-recent first — the debugging entry point is "what just happened".
	// IDs are unique, so id-desc gives a deterministic total order.
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID > offers[j].ID })

	if limit > 0 && len(offers) > limit {
		offers = offers[:limit]
	}
	out.Returned = len(offers)
	out.Offers = make([]LaborOfferDTO, 0, len(offers))
	for _, o := range offers {
		out.Offers = append(out.Offers, laborOfferDTO(o, snap.Actors))
	}
	return out
}

// laborOfferDTO maps one offer to its wire shape, resolving the worker / employer
// display names off the actor roster and the offer's direction off InitiatedBy.
func laborOfferDTO(o *sim.LaborOffer, actors map[sim.ActorID]*sim.ActorSnapshot) LaborOfferDTO {
	initiatedBy := "worker"
	if o.EmployerInitiated() {
		initiatedBy = "employer"
	}
	dto := LaborOfferDTO{
		ID:             uint64(o.ID),
		WorkerID:       string(o.WorkerID),
		WorkerName:     snapshotActorName(actors, o.WorkerID),
		EmployerID:     string(o.EmployerID),
		EmployerName:   snapshotActorName(actors, o.EmployerID),
		InitiatedBy:    initiatedBy,
		ResponderID:    string(o.Responder()),
		State:          string(o.State),
		RewardCoins:    o.Reward,
		DurationMin:    o.DurationMin,
		HuddleID:       string(o.HuddleID),
		SceneID:        string(o.SceneID),
		CreatedAt:      o.CreatedAt,
		ExpiresAt:      o.ExpiresAt,
		AcceptedAt:     clonePtrTime(o.AcceptedAt),
		WorkStartedAt:  clonePtrTime(o.WorkStartedAt),
		WorkingUntil:   clonePtrTime(o.WorkingUntil),
		ResolvedAt:     clonePtrTime(o.ResolvedAt),
		EnRouteWaiting: o.EnRouteWaiting,
	}
	for _, ri := range o.RewardItems {
		dto.RewardItems = append(dto.RewardItems, LaborRewardItemDTO{Item: string(ri.Kind), Qty: ri.Qty})
	}
	// EnRouteDeadline is a value (zero for an on-site hire that started work
	// immediately) — surface nil (→ omitted) rather than a zero timestamp.
	if !o.EnRouteDeadline.IsZero() {
		d := o.EnRouteDeadline
		dto.EnRouteDeadline = &d
	}
	return dto
}

// snapshotActorName resolves an actor's display name off the published snapshot
// roster, returning "" when the id is unknown (a terminal offer whose actor was
// removed, or a hand-built test snapshot with no roster).
func snapshotActorName(actors map[sim.ActorID]*sim.ActorSnapshot, id sim.ActorID) string {
	if a := actors[id]; a != nil {
		return a.DisplayName
	}
	return ""
}
