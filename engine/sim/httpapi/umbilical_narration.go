package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_narration.go — the narration-pool registry read (LLM-538):
//
//	/umbilical/narration[?key=] — every phrase pool the engine speaks from, its
//	                              seed lines and its LLM-generated expansions
//	                              kept apart, plus the description that steers
//	                              expansion and the draw/cap state.
//
// Why it exists. A pool's live content is its compile-time seed lines PLUS
// whatever the expansion cascade has generated for it, merged at boot from
// narration_pool_expansion. Before this route the merged pool was readable from
// nowhere: World.NarrationPools is not published on the snapshot, and no handler
// reached it. The two available substitutes are both worse than they look —
// watching the village until an NPC happens to speak, and querying
// narration_pool_expansion over SSH, which shows the persisted ROWS rather than
// the merged pool the engine draws from and says nothing about the counters.
//
// The cost of that was concrete. LLM-535 (a keeper's reserved farewell drifting
// from a leave-taking into a dismissal — "Off with you, now.") could establish
// that the offending lines were expansions only by noting they were absent from
// the seed table, so the ticket's central finding shipped labelled INFERRED. Its
// AC "the drifted expansions are gone from the live registry" then could not be
// checked at all after the deleting migration ran. Pool drift is a class of
// defect, not a one-off: every pool has seeds, and a seed at the edge of its
// register is what teaches the model to go further.
//
// `seed` and `extra` are separate arrays on purpose. "Is this line ours or the
// model's" is the question the route is answered against, and one merged list
// throws away the answer. `description` sits beside them for the same reason —
// LLM-535's root cause was a description ("in as few words as possible") pulling
// its expansions out of register, and the drifted lines next to the wording that
// produced them are legible in one screen.
//
// Read LIVE via SendContext, NOT the published snapshot — NarrationPools is not
// on Snapshot at all, and narrationDraw mutates Draws on every draw while
// FinishNarrationExpansion appends to Extra, both on the world goroutine, so an
// off-goroutine read would race the writer (the rationale /recipes and /items
// document for the in-place-mutated catalogs). The command closure copies plain
// strings out; no *NarrationPool escapes.
//
// Gated by requireOperator and registered only when the umbilical is enabled,
// both inherited from the umbilicalRoutes() descriptor table like every read.

// UmbilicalNarrationPoolDTO is one phrase pool on the wire.
type UmbilicalNarrationPoolDTO struct {
	// Key is the registry key — the same string narration_pool_expansion.pool_key
	// carries, so a row in the DB and a pool here join on it directly.
	Key string `json:"key"`
	// Description is the meta prose handed to the expansion prompt as "the moment
	// these lines narrate". It is the strongest lever on what gets generated after
	// the seeds themselves, and reading it beside `extra` is how a drifted pool is
	// diagnosed rather than just observed.
	Description string `json:"description"`
	// CustomerToken reports whether this pool's vocabulary admits the literal
	// {customer} token. False means generated lines may carry no brace token at
	// all (sim.ValidateNarrationPhrase enforces it).
	CustomerToken bool `json:"customer_token"`
	// Seed is the compile-time authoring table — engine-authored, identical on
	// every deploy of this build, and never removable at runtime.
	Seed []string `json:"seed"`
	// Extra is the LLM-generated half, in merge order: whatever was loaded from
	// narration_pool_expansion at boot, then anything this process has generated
	// since. Empty is the ordinary state of a young pool AND the post-fix state of
	// a pool whose rows were deleted — the LLM-535 verification that had no read.
	Extra []string `json:"extra"`

	SeedCount  int `json:"seed_count"`
	ExtraCount int `json:"extra_count"`
	// TotalCount is the merged size — what a draw indexes into and what the cap
	// and the expansion threshold are both measured against.
	TotalCount int `json:"total_count"`
	// MaxCount is sim.NarrationPoolMaxPhrases, the cap every pool shares.
	MaxCount int `json:"max_count"`

	// Draws counts draws since boot or since the last expansion nudge, whichever
	// is later. TRANSIENT by design — the counters are not checkpointed, so this
	// resets on every restart and the village restarts often. A low number on a
	// busy pool means the engine restarted recently, not that nobody spoke.
	Draws int `json:"draws"`
	// ExpandsAtDraws is the draw count at which this pool next nudges the
	// expansion cascade (cycle factor x TotalCount, both re-read at draw time).
	// Without it `draws` is unreadable — the threshold moves as the pool grows.
	//
	// ABSENT means the pool will not expand at all: it is at cap, or (degenerate,
	// and unreachable while every seed table is non-empty) it has no lines, which
	// narrationDraw bails on before counting anything. A pointer rather than a
	// plain int precisely so "never" cannot be confused with a threshold of 0,
	// which would read as "an expansion is due right now" (code_review).
	ExpandsAtDraws *int `json:"expands_at_draws,omitempty"`
	// InFlight marks an expansion attempt already dispatched for this pool. It
	// blocks a second nudge until the cascade lands the attempt (every failure
	// path clears it), so a pool stuck in-flight has an expansion goroutine that
	// never came back.
	InFlight bool `json:"in_flight"`
	// AtCap marks a pool at or past MaxCount. It never expands again, whatever
	// the draw counter says.
	AtCap bool `json:"at_cap"`
}

// UmbilicalNarrationDTO is the GET /api/village/umbilical/narration response:
// every pool in the registry, sorted by key for a stable read.
type UmbilicalNarrationDTO struct {
	ContractVersion int                         `json:"contract_version"`
	Total           int                         `json:"total"`
	Pools           []UmbilicalNarrationPoolDTO `json:"pools"`
}

// handleUmbilicalNarration serves the live narration registry. Optional `key`
// filters to one pool, matched case-insensitively against the registry key; an
// unknown key yields an EMPTY list rather than a 404 — the /recipes + /items
// optional-filter posture. Pure read: it does not count a draw, so looking at a
// pool cannot push it over its own expansion threshold.
func (s *Server) handleUmbilicalNarration(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	res, err := s.world.SendContext(r.Context(), sim.FetchNarrationPools(key))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	views, ok := res.([]sim.NarrationPoolView)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected narration result")
		return
	}
	dto := UmbilicalNarrationDTO{
		ContractVersion: ContractVersion,
		Total:           len(views),
		Pools:           make([]UmbilicalNarrationPoolDTO, 0, len(views)),
	}
	for _, v := range views {
		dto.Pools = append(dto.Pools, narrationPoolRowDTO(v))
	}
	writeJSON(w, dto)
}

// narrationPoolRowDTO builds the wire row from a registry view. The two line
// slices are rebuilt with make so an empty half serializes as [] rather than
// null — `extra` is empty for most pools most of the time, and a null there is
// a decoding hazard for anything that ranges over it.
func narrationPoolRowDTO(v sim.NarrationPoolView) UmbilicalNarrationPoolDTO {
	total := len(v.Seed) + len(v.Extra)
	atCap := total >= sim.NarrationPoolMaxPhrases
	row := UmbilicalNarrationPoolDTO{
		Key:           v.Key,
		Description:   v.Description,
		CustomerToken: v.CustomerToken,
		Seed:          append(make([]string, 0, len(v.Seed)), v.Seed...),
		Extra:         append(make([]string, 0, len(v.Extra)), v.Extra...),
		SeedCount:     len(v.Seed),
		ExtraCount:    len(v.Extra),
		TotalCount:    total,
		MaxCount:      sim.NarrationPoolMaxPhrases,
		Draws:         v.Draws,
		InFlight:      v.InFlight,
		AtCap:         atCap,
	}
	if !atCap && total > 0 {
		threshold := sim.NarrationExpansionCycleFactor * total
		row.ExpandsAtDraws = &threshold
	}
	return row
}
