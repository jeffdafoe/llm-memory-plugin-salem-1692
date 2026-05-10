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
	"database/sql"
	"encoding/json"
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

// relationshipRow holds one actor_relationship row joined with the
// peer's display name. salient_facts is the raw JSONB bytes; the
// renderer parses on demand.
type relationshipRow struct {
	OtherID           string
	OtherDisplayName  string
	SummaryText       string
	SalientFacts      []byte
	InteractionCount  int
	LastInteractionAt sql.NullTime
}

// relationshipFactsRecentN is how many salient_facts entries get
// rendered into the perception. Most-recent-first; older entries are
// retained in the row for future consolidation passes but don't ride
// the prompt every tick. Three is enough to give context without
// flooding.
const relationshipFactsRecentN = 3

// loadRelationshipsForHuddle returns the perceiver's actor_relationship
// rows for every co-huddle peer who has one. Empty when the perceiver
// has no huddle, no peers, or no rows pointing at peers in the huddle.
//
// Joining peer display_name in the same query keeps perception build
// to one round-trip per actor; without the join we'd query per peer.
// The huddle filter (peer.current_huddle_id = perceiver's huddle)
// applied SQL-side means peers who LEFT the huddle since the last
// poll don't surface — we render only what's relevant right now.
func (app *App) loadRelationshipsForHuddle(ctx context.Context, actorID string) ([]relationshipRow, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT r.other_actor_id::text,
		       peer.display_name,
		       r.summary_text,
		       r.salient_facts,
		       r.interaction_count,
		       r.last_interaction_at
		  FROM actor_relationship r
		  JOIN actor peer ON peer.id = r.other_actor_id
		 WHERE r.actor_id = $1
		   AND peer.current_huddle_id IS NOT NULL
		   AND peer.current_huddle_id = (
		       SELECT current_huddle_id FROM actor WHERE id = $1
		   )
		 ORDER BY peer.display_name
	`, actorID)
	if err != nil {
		return nil, fmt.Errorf("query actor_relationship: %w", err)
	}
	defer rows.Close()
	var out []relationshipRow
	for rows.Next() {
		var rr relationshipRow
		if err := rows.Scan(
			&rr.OtherID,
			&rr.OtherDisplayName,
			&rr.SummaryText,
			&rr.SalientFacts,
			&rr.InteractionCount,
			&rr.LastInteractionAt,
		); err != nil {
			return nil, fmt.Errorf("scan actor_relationship: %w", err)
		}
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate actor_relationship: %w", err)
	}
	return out, nil
}

// formatRelationshipsPerception renders one "What you remember of
// those here:" section combining all peers' rows. Each peer gets a
// subsection headed by their name, with the summary_text first and
// the most-recent-N salient_facts as bulleted lines.
//
// Salient facts are stored chronologically (Phase 2 will append on
// each event); rendering reverses to most-recent-first because that's
// the slice the LLM most needs context for. Older facts age out of
// the visible window but stay in the row for consolidation.
//
// Returns "" when no peer rows have any renderable content — the
// caller can skip appending without checking. A peer row whose
// summary is empty AND whose salient_facts are empty is skipped at
// the subsection level.
func formatRelationshipsPerception(rows []relationshipRow) string {
	if len(rows) == 0 {
		return ""
	}
	var subsections []string
	for _, r := range rows {
		var lines []string
		if summary := strings.TrimSpace(r.SummaryText); summary != "" {
			lines = append(lines, summary)
		}
		var facts []map[string]interface{}
		if len(r.SalientFacts) > 0 {
			if err := json.Unmarshal(r.SalientFacts, &facts); err != nil {
				// Skip the salient facts on this row; summary still
				// renders if present. A malformed JSONB shouldn't
				// blank the rest of the perception.
				facts = nil
			}
		}
		if n := len(facts); n > 0 {
			start := n - relationshipFactsRecentN
			if start < 0 {
				start = 0
			}
			// Most-recent-first walk from end to start.
			for i := n - 1; i >= start; i-- {
				if text, ok := facts[i]["text"].(string); ok {
					if t := strings.TrimSpace(text); t != "" {
						lines = append(lines, "- "+t)
					}
				}
			}
		}
		if len(lines) == 0 {
			continue
		}
		subsections = append(subsections, r.OtherDisplayName+":\n"+strings.Join(lines, "\n"))
	}
	if len(subsections) == 0 {
		return ""
	}
	return "What you remember of those here:\n\n" + strings.Join(subsections, "\n\n")
}
