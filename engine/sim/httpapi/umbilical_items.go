package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_items.go — the item-catalog read route (LLM-119): the live item_kind
// catalog including each kind's per-need satiation entries (ItemKindDef.
// Satisfies). The read side of /item/set-satisfies, exactly as /recipes is the
// read side of /recipe/set — so an operator can see an item's current satiation
// (and dwell triple, where authored) before editing it. Until this route the
// satiation values surfaced only indirectly inside rendered NPC perception.
//
// Production economics (rate / inputs / wholesale+retail price) live on /recipes,
// not here: item_kind has no price column and prices already have their own read
// route. This route is the item-DEFINITION + satiation view.

// umbilicalSatisfactionDTO is one per-need satiation entry on the wire. Amount is
// the immediate per-unit magnitude; the dwell triple is included when authored
// (read-only here — the write route sets the immediate amount only).
type umbilicalSatisfactionDTO struct {
	Attribute          string `json:"attribute"`
	Amount             int    `json:"amount"`
	DwellAmount        int    `json:"dwell_amount,omitempty"`
	DwellPeriodMinutes int    `json:"dwell_period_minutes,omitempty"`
	DwellTotalTicks    int    `json:"dwell_total_ticks,omitempty"`
}

// umbilicalItemDTO is one item kind on the wire: its definition fields plus the
// satiation entries. eat_here_only flags a consumable that can't leave the
// seller's premises (stew, a poured drink) — derived, not stored.
type umbilicalItemDTO struct {
	Name         string                     `json:"name"`
	Label        string                     `json:"label"`
	Category     string                     `json:"category"`
	SortOrder    int                        `json:"sort_order"`
	Capabilities []string                   `json:"capabilities,omitempty"`
	EatHereOnly  bool                       `json:"eat_here_only"`
	Satisfies    []umbilicalSatisfactionDTO `json:"satisfies"`
}

// UmbilicalItemsDTO is the GET /api/village/umbilical/items response: the live
// item catalog (the read side of item/set-satisfies). With ?item= it carries
// just the one matching kind.
type UmbilicalItemsDTO struct {
	ContractVersion int                `json:"contract_version"`
	Total           int                `json:"total"`
	Items           []umbilicalItemDTO `json:"items"`
}

// itemRowDTO builds the wire row from a catalog def.
func itemRowDTO(def *sim.ItemKindDef) umbilicalItemDTO {
	out := umbilicalItemDTO{
		Name:         string(def.Name),
		Label:        def.DisplayLabel,
		Category:     string(def.Category),
		SortOrder:    def.SortOrder,
		Capabilities: def.Capabilities,
		EatHereOnly:  def.EatHereOnly(),
		Satisfies:    make([]umbilicalSatisfactionDTO, 0, len(def.Satisfies)),
	}
	for _, s := range def.Satisfies {
		out.Satisfies = append(out.Satisfies, umbilicalSatisfactionDTO{
			Attribute:          string(s.Attribute),
			Amount:             s.Immediate,
			DwellAmount:        s.DwellAmount,
			DwellPeriodMinutes: s.DwellPeriodMinutes,
			DwellTotalTicks:    s.DwellTotalTicks,
		})
	}
	return out
}

// handleUmbilicalItems serves the live item catalog (World.ItemKinds). Read on
// the world goroutine via SendContext: item/set-satisfies swaps a key in
// World.ItemKinds, so reading it off the published snapshot's ALIASED ItemKinds
// reference would race the writer — the catalog isn't deep-cloned at publish
// (same rationale /recipes documents). Optional ?item= filters to one kind,
// matched case-insensitively against the canonical catalog key (empty list when
// unknown). Sorted by name. Pure read — mutates nothing.
func (s *Server) handleUmbilicalItems(w http.ResponseWriter, r *http.Request) {
	item := strings.TrimSpace(r.URL.Query().Get("item"))
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		dto := UmbilicalItemsDTO{
			ContractVersion: ContractVersion,
			Items:           make([]umbilicalItemDTO, 0, len(world.ItemKinds)),
		}
		for key, def := range world.ItemKinds {
			if def == nil {
				continue
			}
			if item != "" && !strings.EqualFold(string(key), item) {
				continue
			}
			dto.Items = append(dto.Items, itemRowDTO(def))
		}
		dto.Total = len(dto.Items)
		sort.Slice(dto.Items, func(i, j int) bool { return dto.Items[i].Name < dto.Items[j].Name })
		return dto, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dto, ok := res.(UmbilicalItemsDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected items result")
		return
	}
	writeJSON(w, dto)
}
