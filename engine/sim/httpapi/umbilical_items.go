package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

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
	Name         string   `json:"name"`
	Label        string   `json:"label"`
	Category     string   `json:"category"`
	SortOrder    int      `json:"sort_order"`
	Capabilities []string `json:"capabilities,omitempty"`
	Description  string   `json:"description,omitempty"`
	EatHereOnly  bool     `json:"eat_here_only"`
	// DurabilityUses > 0 marks a durable tool: produce executions one unit
	// lasts (LLM-330). The read side of item/set's durability_uses knob.
	DurabilityUses int `json:"durability_uses,omitempty"`
	// WearMinutes > 0 marks a wearable garment: worked minutes one unit lasts
	// (LLM-422). The read side of item/set's wear_minutes knob.
	WearMinutes int                        `json:"wear_minutes,omitempty"`
	Satisfies   []umbilicalSatisfactionDTO `json:"satisfies"`
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
		Name:      string(def.Name),
		Label:     def.DisplayLabel,
		Category:  string(def.Category),
		SortOrder: def.SortOrder,
		// Deep-copy so the DTO retains no alias into World.ItemKinds — JSON
		// encoding runs after SendContext returns (off the world goroutine), so
		// an aliased slice could race a concurrent catalog writer. Same rationale
		// as the Satisfies copy below. nil in → nil out (omitempty drops it).
		Capabilities:   append([]string(nil), def.Capabilities...),
		Description:    def.Description,
		EatHereOnly:    def.EatHereOnly(),
		DurabilityUses: def.DurabilityUses,
		WearMinutes:    def.WearMinutes,
		Satisfies:      make([]umbilicalSatisfactionDTO, 0, len(def.Satisfies)),
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

// ---- Item definition write (LLM-200) -------------------------------------

// ItemKindWriter is the durable item_kind upsert, injected by cmd/engine
// (Server.SetItemKindWriter) so httpapi doesn't import the pg package. nil on a
// deploy without it wired → the route answers 503. pg.ItemKindsRepo satisfies it.
type ItemKindWriter interface {
	UpsertItemKind(ctx context.Context, def sim.ItemKindDef) error
}

// umbilicalItemSetRequest is the POST /api/village/umbilical/item/set body:
// upsert one item_kind definition. cap-free; an existing name is updated in
// place and its item_satisfies satiation rows are left intact. hours_per_unit is
// intentionally absent — v2 doesn't model it (production rate lives on the
// recipe, set via /recipe/set); see LLM-200.
type umbilicalItemSetRequest struct {
	Name                  string   `json:"name"`
	DisplayLabel          string   `json:"display_label"`
	Category              string   `json:"category"`
	SortOrder             int      `json:"sort_order"`
	Capabilities          []string `json:"capabilities"`
	DisplayLabelSingular  string   `json:"display_label_singular"`
	DisplayLabelPlural    string   `json:"display_label_plural"`
	ConsumeDwellNarration string   `json:"consume_dwell_narration"`
	// Description is optional flavor prose (LLM-410) — the item_kind.description
	// column. Free text (the column is unbounded TEXT), trimmed; blank clears it.
	Description string `json:"description"`
	// DurabilityUses > 0 makes the kind a durable tool lasting that many
	// produce executions (LLM-330) — the live tuning knob for tool lifetimes.
	// 0 (or omitted) keeps plain consumed-input semantics.
	DurabilityUses int `json:"durability_uses"`
	// WearMinutes > 0 makes the kind a wearable garment lasting that many
	// worked minutes (LLM-422) — the live tuning knob for garment lifetimes.
	// 0 (or omitted) keeps the good durable-forever.
	WearMinutes int `json:"wear_minutes"`
}

// handleUmbilicalItemSet upserts one item_kind definition — the create/edit leg
// of the all-live new-good flow (item/set → recipe/set → restock/set, LLM-200).
// Unlike recipe/set and item/set-satisfies it can CREATE a kind: the name is the
// new catalog key, so there is no pre-existence check. The durable item_kind
// write runs before the in-memory catalog update (recipe/set's ordering); on an
// update the item's satiation (item_satisfies) is preserved — this route writes
// only the definition row.
//
// category is free text by design (operators add new good classes without a
// deploy); the typed ItemCategory enum is a soft type, not a closed set, and the
// mismatch is tracked in LLM-204. 400 missing name/display_label/category,
// over-length field, out-of-range sort_order, or a blank capability token; 503
// when the writer isn't wired; 500 on a write failure; 200 with the applied
// catalog row (the same shape /items serves).
func (s *Server) handleUmbilicalItemSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalItemSetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}

	// Trim the free-text fields up front; the name becomes the catalog key, so
	// stray whitespace there would mint an unreachable kind.
	name := strings.TrimSpace(req.Name)
	displayLabel := strings.TrimSpace(req.DisplayLabel)
	category := strings.TrimSpace(req.Category)
	singular := strings.TrimSpace(req.DisplayLabelSingular)
	plural := strings.TrimSpace(req.DisplayLabelPlural)
	narration := strings.TrimSpace(req.ConsumeDwellNarration)
	description := strings.TrimSpace(req.Description)

	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if displayLabel == "" {
		writeError(w, http.StatusBadRequest, "display_label is required")
		return
	}
	if category == "" {
		writeError(w, http.StatusBadRequest, "category is required")
		return
	}
	// Lengths mirror the item_kind columns: name/category varchar(32), labels
	// varchar(64). Count runes — varchar(n) limits characters, not bytes.
	if utf8.RuneCountInString(name) > 32 {
		writeError(w, http.StatusBadRequest, "name exceeds 32 characters")
		return
	}
	if utf8.RuneCountInString(category) > 32 {
		writeError(w, http.StatusBadRequest, "category exceeds 32 characters")
		return
	}
	for _, label := range []string{displayLabel, singular, plural} {
		if utf8.RuneCountInString(label) > 64 {
			writeError(w, http.StatusBadRequest, "display labels must be 64 characters or fewer")
			return
		}
	}
	// description is free TEXT in the column, but bound it so a stray oversized body
	// isn't written wholesale — catalog flavor prose is short (the seed descriptions
	// run ~60 chars); 500 is generous headroom (code_review).
	if utf8.RuneCountInString(description) > 500 {
		writeError(w, http.StatusBadRequest, "description must be 500 characters or fewer")
		return
	}
	// sort_order maps to a SMALLINT column — keep it in range so a bad value
	// fails at the handler (400) rather than the pg write (500).
	if req.SortOrder < 0 || req.SortOrder > 32767 {
		writeError(w, http.StatusBadRequest, "sort_order must be between 0 and 32767")
		return
	}
	if req.DurabilityUses < 0 {
		writeError(w, http.StatusBadRequest, "durability_uses must be 0 or greater")
		return
	}
	if req.WearMinutes < 0 {
		writeError(w, http.StatusBadRequest, "wear_minutes must be 0 or greater")
		return
	}
	// capabilities is a soft token set; trim each and reject a blank token so a
	// stray "" can't pollute the array.
	capabilities := make([]string, 0, len(req.Capabilities))
	for _, c := range req.Capabilities {
		token := strings.TrimSpace(c)
		if token == "" {
			writeError(w, http.StatusBadRequest, "capability tokens must be non-empty")
			return
		}
		capabilities = append(capabilities, token)
	}

	if s.itemKindWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "item editing is not wired on this deploy")
		return
	}

	auditUmbilical(user.Username, "item.set",
		fmt.Sprintf("name=%s category=%s sort=%d caps=%d", name, category, req.SortOrder, len(capabilities)))

	def := sim.ItemKindDef{
		Name:                  sim.ItemKind(name),
		DisplayLabel:          displayLabel,
		DisplayLabelSingular:  singular,
		DisplayLabelPlural:    plural,
		Category:              sim.ItemCategory(category),
		SortOrder:             req.SortOrder,
		Capabilities:          capabilities,
		ConsumeDwellNarration: narration,
		Description:           description,
		DurabilityUses:        req.DurabilityUses,
		WearMinutes:           req.WearMinutes,
	}

	// 1) Durable write FIRST — item_kind is the source of truth (the catalog
	//    rebuilds from it on restart), so memory is only touched once it lands.
	if err := s.itemKindWriter.UpsertItemKind(r.Context(), def); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("umbilical item.set: name=%s: %v", def.Name, err)
		writeError(w, http.StatusInternalServerError, "item write failed")
		return
	}

	// 2) In-memory catalog update so perception and the produce/commerce paths
	//    see the new or edited kind immediately. An update preserves the live
	//    satiation entries (see sim.SetItemKind).
	res, err := s.world.SendContext(r.Context(), sim.SetItemKind(def))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// The durable write already landed; the live catalog will catch up on the
		// next reload. Don't imply nothing persisted.
		log.Printf("umbilical item.set: name=%s persisted but live-catalog update failed: %v", def.Name, err)
		writeError(w, http.StatusInternalServerError, "item persisted but live update failed; applies on next reload")
		return
	}
	stored, ok := res.(sim.ItemKindDef)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected item set result")
		return
	}

	writeJSON(w, itemRowDTO(&stored))
}
