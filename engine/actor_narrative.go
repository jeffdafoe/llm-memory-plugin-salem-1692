package main

// actor_narrative — engine-side continuity layer for shared-VA-backed
// NPCs (ZBBS-WORK-212, Phase 1A).
//
// Persistent-VA actors (Ezekiel, John Ellis, Prudence) get continuity
// from llm-memory: dreams, evolving "soul" docs, full chat history per
// peer, learnings. Their character arcs accumulate naturally in the
// API's storage and are injected back into prompts via the same VA
// session.
//
// Shared-VA actors (Hannah on salem-vendor, transient visitors on
// salem-visitor) run with cache_prompts=false / dream_mode=none /
// learning_enabled=false. The shared agent slug carries no per-actor
// memory and the API doesn't persist anything it could later inject.
// Without an engine-side counterpart, every tick is a fresh slate and
// the character can't have an arc.
//
// This file holds the read path for that counterpart. Phase 1A is
// read-only — a per-actor seed text authored via SQL, surfaced into
// perception each tick. Phase 1B (next) adds per-pair relationship
// state. Phase 2 wires event hooks. Phase 3 runs periodic
// consolidation that compresses recent salient events into the
// evolving_summary column.
//
// Gating: the perception caller checks the actor's llm_memory_agent
// against a closed list of shared-VA slugs before calling load. VA-
// attached actors with their own dedicated agent skip the injection —
// they already get richer context from llm-memory's per-actor session
// and another engine-side layer would over-stuff the prompt.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// sharedVAAgents is the closed set of agent slugs that route through a
// stateless shared VA on memory-api. Actors backed by these agents get
// engine-side narrative state injected; everyone else gets nothing
// from this subsystem (their own VA session carries continuity).
//
// Add new shared agents here when they're provisioned. Mistakenly
// missing from this list means the actor silently goes without
// continuity injection — visible in play as a character who can't
// remember their own arc.
var sharedVAAgents = map[string]bool{
	"salem-vendor":  true,
	"salem-visitor": true,
}

// isSharedVAAgent reports whether the given llm_memory_agent slug
// points at a stateless shared VA (vs. an actor's dedicated VA).
// NULL/empty inputs return false — those actors have no agent at all
// and don't tick through the LLM path.
func isSharedVAAgent(agent string) bool {
	return sharedVAAgents[agent]
}

// loadNarrativeStateForActor fetches the per-actor narrative backbone.
// Returns ("", "", false, nil) when the actor has no row (no narrative
// state seeded for them yet) or both fields are empty (row exists but
// nothing to inject). Errors are returned so callers can decide
// whether to log + fall through or surface; perception callers should
// log + fall through (a query hiccup shouldn't blank an actor's
// entire perception).
func (app *App) loadNarrativeStateForActor(ctx context.Context, actorID string) (seed, evolving string, ok bool, err error) {
	row := app.DB.QueryRow(ctx, `
		SELECT seed_text, evolving_summary
		  FROM actor_narrative_state
		 WHERE actor_id = $1
	`, actorID)
	if err := row.Scan(&seed, &evolving); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("scan actor_narrative_state: %w", err)
	}
	if strings.TrimSpace(seed) == "" && strings.TrimSpace(evolving) == "" {
		return "", "", false, nil
	}
	return seed, evolving, true, nil
}

// formatNarrativeStatePerception renders the seed + evolving summary
// as a single perception section. Returns "" when nothing's worth
// rendering, so callers can skip appending without checking.
//
// Section header reads "Who you are:" — frames the content as the
// character's own self-knowledge, not a third-person dossier. Both
// pieces flow into the same section so the LLM sees them as one
// coherent identity block rather than two distinct lists.
func formatNarrativeStatePerception(seed, evolving string) string {
	seed = strings.TrimSpace(seed)
	evolving = strings.TrimSpace(evolving)
	if seed == "" && evolving == "" {
		return ""
	}
	var parts []string
	if seed != "" {
		parts = append(parts, seed)
	}
	if evolving != "" {
		parts = append(parts, evolving)
	}
	return "Who you are:\n" + strings.Join(parts, "\n\n")
}
