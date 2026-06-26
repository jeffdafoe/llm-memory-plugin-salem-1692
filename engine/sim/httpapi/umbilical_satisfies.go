package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_satisfies.go — the item-satiation write control route (LLM-119):
// live add/edit of how much consuming one unit of a food/drink item eases a
// need (the item_satisfies "amount"). Operator-gated, audited, armed only when
// control is enabled. Existing item kinds only (the item must already be in the
// catalog); the need attribute must be a tracked need. The READ counterpart is
// /items (umbilical_items.go).
//
// item_satisfies is reference data with NO checkpoint path, so durability is a
// direct item_satisfies write (the injected SatisfiesWriter) followed by an
// in-memory ItemKindDef.Satisfies update (sim.SetItemSatisfaction). The DB write
// is the source of truth (the catalog rebuilds from item_satisfies on restart),
// so it runs FIRST; the in-memory update only happens once it succeeds. This is
// the satiation twin of the recipe-edit route (umbilical_recipe.go).
//
// MVP surface: the immediate per-unit magnitude only. The slow-burn dwell triple
// on item_satisfies is preserved on edit (the upsert touches only the amount
// column) but is not settable here — it can be added as optional fields in the
// same route shape later.

// SatisfiesWriter is the durable item_satisfies upsert, injected by cmd/engine
// (Server.SetSatisfiesWriter) so httpapi doesn't import the pg package. nil on a
// deploy without it wired → the route answers 503. pg.ItemKindsRepo satisfies it.
type SatisfiesWriter interface {
	UpsertItemSatisfies(ctx context.Context, kind sim.ItemKind, attribute sim.NeedKey, amount int) error
}

// umbilicalSetSatisfiesRequest is the POST /api/village/umbilical/item/set-satisfies
// body: upsert one item's immediate per-unit need-ease magnitude.
type umbilicalSetSatisfiesRequest struct {
	Item      string `json:"item"`
	Attribute string `json:"attribute"`
	Amount    int    `json:"amount"`
}

// umbilicalSatisfiesResponse echoes the applied satiation entry (item
// canonicalized to its catalog key).
type umbilicalSatisfiesResponse struct {
	Item      string `json:"item"`
	Attribute string `json:"attribute"`
	Amount    int    `json:"amount"`
}

// handleUmbilicalSetSatisfies upserts one item's immediate need-ease magnitude.
// Numeric + need-key validation is handler-side (400); item-kind existence is
// validated against the live catalog (422); the durable item_satisfies write
// runs before the in-memory catalog update. 400 missing item / missing or
// unknown attribute / non-positive amount; 422 unknown item; 503 when the writer
// isn't wired; 500 on a write failure; 200 with the applied entry.
func (s *Server) handleUmbilicalSetSatisfies(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalSetSatisfiesRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.Item == "" {
		writeError(w, http.StatusBadRequest, "item is required")
		return
	}
	if req.Attribute == "" {
		writeError(w, http.StatusBadRequest, "attribute is required")
		return
	}
	if req.Amount < 1 {
		writeError(w, http.StatusBadRequest, "amount must be >= 1")
		return
	}
	// The attribute must be a tracked need (hunger/thirst/tiredness). A fixed
	// registry lookup — no world goroutine needed — so a typo fails 400 here
	// rather than persisting a phantom-need row (matches the need-threshold +
	// set-needs routes, which 400 an unknown need key).
	attr := sim.NeedKey(req.Attribute)
	if _, ok := sim.FindNeed(attr); !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown need attribute %q", req.Attribute))
		return
	}
	if s.satisfiesWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "item satiation editing is not wired on this deploy")
		return
	}

	auditUmbilical(user.Username, "item.set-satisfies",
		fmt.Sprintf("item=%s attribute=%s amount=%d", req.Item, req.Attribute, req.Amount))

	// 1) Validate the item against the live catalog and canonicalize to its key.
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveSatisfaction(world, req.Item)
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// Unknown item — client-correctable; the message names it.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	kind, ok := res.(sim.ItemKind)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected satisfaction resolve result")
		return
	}

	// 2) Durable write FIRST — item_satisfies is the source of truth (the catalog
	//    rebuilds from it on restart), so memory is only touched once it lands.
	if err := s.satisfiesWriter.UpsertItemSatisfies(r.Context(), kind, attr, req.Amount); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("umbilical item.set-satisfies: item=%s attribute=%s: %v", kind, attr, err)
		writeError(w, http.StatusInternalServerError, "item satiation write failed")
		return
	}

	// 3) In-memory catalog update so perception + consume see it on the next tick.
	applied, err := s.world.SendContext(r.Context(), sim.SetItemSatisfaction(kind, attr, req.Amount))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// The durable write already landed; the live catalog will catch up on the
		// next reload. Don't imply nothing persisted.
		log.Printf("umbilical item.set-satisfies: item=%s attribute=%s persisted but live-catalog update failed: %v", kind, attr, err)
		writeError(w, http.StatusInternalServerError, "item satiation persisted but live update failed; applies on next reload")
		return
	}
	sat, ok := applied.(sim.ItemSatisfaction)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected satisfaction result")
		return
	}

	writeJSON(w, umbilicalSatisfiesResponse{
		Item:      string(kind),
		Attribute: string(sat.Attribute),
		Amount:    sat.Immediate,
	})
}
