package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_recipe.go — the recipe routes. The EDIT control route (LLM-97):
// live add/edit of an item recipe (the rate / inputs / output-batch a produce
// entry feeds off). Operator-gated, audited, armed only when control is enabled.
// Existing item kinds only — output and every input must already be in the
// catalog. The READ counterpart (LLM-110, handleUmbilicalRecipes at the bottom)
// dumps the same catalog so an operator can see current values before editing.
//
// Recipes are reference data with NO checkpoint path, so durability is a direct
// item_recipe write (the injected RecipeWriter) followed by an in-memory
// World.Recipes update (sim.SetRecipe). The DB write is the source of truth (the
// catalog rebuilds from item_recipe on restart), so it runs FIRST; the in-memory
// update only happens once it succeeds.

// RecipeWriter is the durable item_recipe upsert, injected by cmd/engine
// (Server.SetRecipeWriter) so httpapi doesn't import the pg package. nil on a
// deploy without it wired → the route answers 503. pg.RecipesRepo satisfies it.
type RecipeWriter interface {
	UpsertRecipe(ctx context.Context, recipe sim.ItemRecipe) error
}

// umbilicalRecipeInput is one recipe input on the wire.
type umbilicalRecipeInput struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// umbilicalRecipeBoostInput is one optional booster input on the wire (LLM-248):
// per production execution, Qty of Item consumed for BonusQty extra output.
type umbilicalRecipeBoostInput struct {
	Item     string `json:"item"`
	Qty      int    `json:"qty"`
	BonusQty int    `json:"bonus_qty"`
}

// umbilicalRecipeBoostState is one optional state-keyed booster on the wire
// (LLM-474): when State holds at landing, the batch mints BonusQty extra. No
// item, because nothing is consumed.
type umbilicalRecipeBoostState struct {
	State    string `json:"state"`
	BonusQty int    `json:"bonus_qty"`
}

// umbilicalRecipeSpeedInput is one optional speed booster on the wire (LLM-511):
// per cycle, Qty of Item consumed at START to run the cycle at RatePct scale
// (200 = half the time). Rate-side sibling of umbilicalRecipeBoostInput.
type umbilicalRecipeSpeedInput struct {
	Item    string `json:"item"`
	Qty     int    `json:"qty"`
	RatePct int    `json:"rate_pct"`
}

// umbilicalRecipeSetRequest is the POST /api/village/umbilical/recipe/set body:
// upsert one recipe. cap-free; an existing output_item is updated in place.
type umbilicalRecipeSetRequest struct {
	OutputItem     string                      `json:"output_item"`
	OutputQty      int                         `json:"output_qty"`
	RateQty        int                         `json:"rate_qty"`
	RatePerHours   int                         `json:"rate_per_hours"`
	Inputs         []umbilicalRecipeInput      `json:"inputs"`
	BoostInputs    []umbilicalRecipeBoostInput `json:"boost_inputs"`
	BoostState     []umbilicalRecipeBoostState `json:"boost_state"`
	SpeedInputs    []umbilicalRecipeSpeedInput `json:"speed_inputs"`
	WholesalePrice int                         `json:"wholesale_price"`
	RetailPrice    int                         `json:"retail_price"`
}

// umbilicalRecipeResponse echoes the applied recipe (item kinds canonicalized to
// their catalog keys).
type umbilicalRecipeResponse struct {
	OutputItem     string                      `json:"output_item"`
	OutputQty      int                         `json:"output_qty"`
	RateQty        int                         `json:"rate_qty"`
	RatePerHours   int                         `json:"rate_per_hours"`
	Inputs         []umbilicalRecipeInput      `json:"inputs"`
	BoostInputs    []umbilicalRecipeBoostInput `json:"boost_inputs"`
	BoostState     []umbilicalRecipeBoostState `json:"boost_state"`
	SpeedInputs    []umbilicalRecipeSpeedInput `json:"speed_inputs"`
	WholesalePrice int                         `json:"wholesale_price"`
	RetailPrice    int                         `json:"retail_price"`
}

// handleUmbilicalRecipeSet upserts one item recipe. Numeric validation is
// handler-side (400); item-kind existence is validated against the live catalog
// (422); the durable item_recipe write runs before the in-memory catalog update.
// 400 missing output_item / non-positive qty/rate/output / negative price / bad
// input; 422 unknown output or input item; 503 when the writer isn't wired; 500
// on a write failure; 200 with the applied recipe.
func (s *Server) handleUmbilicalRecipeSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalRecipeSetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.OutputItem == "" {
		writeError(w, http.StatusBadRequest, "output_item is required")
		return
	}
	if req.OutputQty < 1 || req.RateQty < 1 || req.RatePerHours < 1 {
		writeError(w, http.StatusBadRequest, "output_qty, rate_qty, and rate_per_hours must be >= 1")
		return
	}
	if req.WholesalePrice < 0 || req.RetailPrice < 0 {
		writeError(w, http.StatusBadRequest, "wholesale_price and retail_price must be >= 0")
		return
	}
	inputs := make([]sim.RecipeInput, 0, len(req.Inputs))
	for _, in := range req.Inputs {
		if in.Item == "" {
			writeError(w, http.StatusBadRequest, "input item is required")
			return
		}
		if in.Qty < 1 {
			writeError(w, http.StatusBadRequest, "input qty must be >= 1")
			return
		}
		inputs = append(inputs, sim.RecipeInput{Item: sim.ItemKind(in.Item), Qty: in.Qty})
	}
	boostInputs := make([]sim.BoostInput, 0, len(req.BoostInputs))
	for _, bi := range req.BoostInputs {
		if bi.Item == "" {
			writeError(w, http.StatusBadRequest, "boost input item is required")
			return
		}
		if bi.Qty < 1 || bi.BonusQty < 1 {
			writeError(w, http.StatusBadRequest, "boost input qty and bonus_qty must be >= 1")
			return
		}
		boostInputs = append(boostInputs, sim.BoostInput{Item: sim.ItemKind(bi.Item), Qty: bi.Qty, BonusQty: bi.BonusQty})
	}
	boostState := make([]sim.BoostState, 0, len(req.BoostState))
	seenStates := make(map[sim.RecipeBoostState]bool, len(req.BoostState))
	for _, bs := range req.BoostState {
		// The state name is checked here rather than deferred to ResolveRecipe so
		// a typo comes back 400 (client-correctable input) instead of 422
		// (unresolvable against the catalog) — the state vocabulary is fixed in
		// code, not looked up in the world.
		if !sim.ValidRecipeBoostState(sim.RecipeBoostState(bs.State)) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown boost state %q", bs.State))
			return
		}
		if bs.BonusQty < 1 {
			writeError(w, http.StatusBadRequest, "boost state bonus_qty must be >= 1")
			return
		}
		// ResolveRecipe rejects duplicates too, so this is not the only guard —
		// but it keeps the three validation layers saying the same thing, and
		// returns the malformed-input 400 for what is malformed input rather
		// than the 422 a resolve failure would produce.
		if seenStates[sim.RecipeBoostState(bs.State)] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("boost state %q listed more than once", bs.State))
			return
		}
		seenStates[sim.RecipeBoostState(bs.State)] = true
		boostState = append(boostState, sim.BoostState{State: sim.RecipeBoostState(bs.State), BonusQty: bs.BonusQty})
	}
	speedInputs := make([]sim.SpeedInput, 0, len(req.SpeedInputs))
	for _, si := range req.SpeedInputs {
		if si.Item == "" {
			writeError(w, http.StatusBadRequest, "speed input item is required")
			return
		}
		if si.Qty < 1 {
			writeError(w, http.StatusBadRequest, "speed input qty must be >= 1")
			return
		}
		// rate_pct must land in the speedup band (101..MaxSpeedInputRatePct): <= 100
		// would slow or freeze the work, and an unbounded value divides the cycle to
		// the 1s floor (an almost-instant batch) and risks overflowing the start-time
		// duration math. A 400 (client-correctable) rather than the 422 ResolveRecipe
		// returns for an unresolvable item; ResolveRecipe re-checks the band too,
		// keeping the three validation layers saying the same thing.
		if si.RatePct <= 100 || si.RatePct > sim.MaxSpeedInputRatePct {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("speed input rate_pct must be between 101 and %d", sim.MaxSpeedInputRatePct))
			return
		}
		speedInputs = append(speedInputs, sim.SpeedInput{Item: sim.ItemKind(si.Item), Qty: si.Qty, RatePct: si.RatePct})
	}
	if s.recipeWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "recipe editing is not wired on this deploy")
		return
	}

	auditUmbilical(user.Username, "recipe.set",
		fmt.Sprintf("output=%s output_qty=%d rate=%d/%dh inputs=%d boosts=%d states=%d speeds=%d", req.OutputItem, req.OutputQty, req.RateQty, req.RatePerHours, len(inputs), len(boostInputs), len(boostState), len(speedInputs)))

	requested := sim.ItemRecipe{
		OutputItem:     sim.ItemKind(req.OutputItem),
		OutputQty:      req.OutputQty,
		RateQty:        req.RateQty,
		RatePerHours:   req.RatePerHours,
		Inputs:         inputs,
		BoostInputs:    boostInputs,
		BoostState:     boostState,
		SpeedInputs:    speedInputs,
		WholesalePrice: req.WholesalePrice,
		RetailPrice:    req.RetailPrice,
	}

	// 1) Validate item references against the live catalog (output + every input
	//    must already exist) and canonicalize to catalog keys.
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveRecipe(world, requested)
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// Unknown output/input item — client-correctable; the message names it.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	recipe, ok := res.(sim.ItemRecipe)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected recipe resolve result")
		return
	}

	// 2) Durable write FIRST — item_recipe is the source of truth (the catalog
	//    rebuilds from it on restart), so memory is only touched once it lands.
	if err := s.recipeWriter.UpsertRecipe(r.Context(), recipe); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("umbilical recipe.set: output=%s: %v", recipe.OutputItem, err)
		writeError(w, http.StatusInternalServerError, "recipe write failed")
		return
	}

	// 3) In-memory catalog update so the produce tick sees it immediately.
	if _, err := s.world.SendContext(r.Context(), sim.SetRecipe(recipe)); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// The durable write already landed; the live catalog will catch up on the
		// next reload. Don't imply nothing persisted.
		log.Printf("umbilical recipe.set: output=%s persisted but live-catalog update failed: %v", recipe.OutputItem, err)
		writeError(w, http.StatusInternalServerError, "recipe persisted but live update failed; applies on next reload")
		return
	}

	writeJSON(w, recipeResponse(recipe))
}

// recipeResponse builds the wire response from the applied recipe.
func recipeResponse(r sim.ItemRecipe) umbilicalRecipeResponse {
	out := umbilicalRecipeResponse{
		OutputItem:     string(r.OutputItem),
		OutputQty:      r.OutputQty,
		RateQty:        r.RateQty,
		RatePerHours:   r.RatePerHours,
		Inputs:         make([]umbilicalRecipeInput, 0, len(r.Inputs)),
		BoostInputs:    make([]umbilicalRecipeBoostInput, 0, len(r.BoostInputs)),
		BoostState:     make([]umbilicalRecipeBoostState, 0, len(r.BoostState)),
		SpeedInputs:    make([]umbilicalRecipeSpeedInput, 0, len(r.SpeedInputs)),
		WholesalePrice: r.WholesalePrice,
		RetailPrice:    r.RetailPrice,
	}
	for _, in := range r.Inputs {
		out.Inputs = append(out.Inputs, umbilicalRecipeInput{Item: string(in.Item), Qty: in.Qty})
	}
	for _, bi := range r.BoostInputs {
		out.BoostInputs = append(out.BoostInputs, umbilicalRecipeBoostInput{Item: string(bi.Item), Qty: bi.Qty, BonusQty: bi.BonusQty})
	}
	for _, bs := range r.BoostState {
		out.BoostState = append(out.BoostState, umbilicalRecipeBoostState{State: string(bs.State), BonusQty: bs.BonusQty})
	}
	for _, si := range r.SpeedInputs {
		out.SpeedInputs = append(out.SpeedInputs, umbilicalRecipeSpeedInput{Item: string(si.Item), Qty: si.Qty, RatePct: si.RatePct})
	}
	return out
}

// ---- Recipe read (LLM-110) ----------------------------------------------

// UmbilicalRecipesDTO is the GET /api/village/umbilical/recipes response: the
// live item-recipe catalog (the read side of recipe/set), so an operator can see
// an item's current rate / inputs / wholesale+retail price before editing it.
// With ?item= it carries just the one matching recipe.
type UmbilicalRecipesDTO struct {
	ContractVersion int                       `json:"contract_version"`
	Total           int                       `json:"total"`
	Recipes         []umbilicalRecipeResponse `json:"recipes"`
}

// handleUmbilicalRecipes serves the live recipe catalog (World.Recipes). Read on
// the world goroutine via SendContext: recipe/set mutates World.Recipes in place
// (it writes a key), so reading it off the published snapshot's ALIASED Recipes
// reference would race the writer — the catalog isn't deep-cloned at publish.
// Optional ?item= filters to one recipe, matched case-insensitively against the
// canonical catalog key (empty list when unknown). Sorted by output item. Pure
// read — mutates nothing.
func (s *Server) handleUmbilicalRecipes(w http.ResponseWriter, r *http.Request) {
	item := strings.TrimSpace(r.URL.Query().Get("item"))
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		dto := UmbilicalRecipesDTO{
			ContractVersion: ContractVersion,
			Recipes:         make([]umbilicalRecipeResponse, 0, len(world.Recipes)),
		}
		for key, recipe := range world.Recipes {
			if recipe == nil {
				continue
			}
			if item != "" && !strings.EqualFold(string(key), item) {
				continue
			}
			dto.Recipes = append(dto.Recipes, recipeResponse(*recipe))
		}
		dto.Total = len(dto.Recipes)
		sort.Slice(dto.Recipes, func(i, j int) bool { return dto.Recipes[i].OutputItem < dto.Recipes[j].OutputItem })
		return dto, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dto, ok := res.(UmbilicalRecipesDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected recipes result")
		return
	}
	writeJSON(w, dto)
}
