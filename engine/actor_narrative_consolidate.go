package main

// actor_narrative_consolidate — periodic compression of the per-pair
// salient_facts trail into a rewritten summary_text (ZBBS-WORK-218,
// Phase 3 of the engine-side continuity layer for shared-VA NPCs).
//
// Phase 2 hooks (WORK-214/215/216) wrote salient_facts append-only on
// speech / pay / serve / deliver_order events. Phase 1B's perception
// renderer caps display at the most-recent 3 facts per peer, which
// keeps the LLM's context bounded but hides long-term arc — "she's
// been buying berries weekly for months" is invisible behind the
// recent-3 window.
//
// Phase 3 closes the gap: a periodic sweep finds (actor, other) pairs
// whose facts trail has grown enough or sat long enough, calls the
// actor's own VA with a distillation prompt, takes the prose response
// as the new summary_text, and prunes the consolidated entries from
// the trail. Subsequent ticks render summary_text + the most recent
// (post-consolidation) salient_facts — a coherent impression plus
// the latest beats.
//
// Why the actor's own VA: Hannah reflects "in voice" using the same
// salem-vendor agent (Llama 3.3 70B on OpenRouter) that runs her
// regular ticks. No new VA provisioning, no new system prompt to
// maintain, character voice consistent across reflection and live
// tick. The salem-vendor system prompt is vendor-economic but the
// distillation user message overrides intent ("you are reflecting
// privately, no tools available, prose only"); the LLM follows
// the user-message intent over the loose tool-discipline boilerplate.
//
// Cadence: hybrid floor + ceiling. Daily floor ensures every active
// pair gets at least one consolidation per 24h; threshold ceiling
// forces a mid-cycle pass when salient_facts crosses 20 entries so
// busy days don't accumulate unbounded.
//
// Cost guard: per-sweep limit of 5 consolidations × 4 sweeps/hour =
// 20/hour hard cap. At Hannah-scale (a few peers) this is never close
// to the cap; the cap is a circuit breaker against accidental N²
// blow-up if the relationship table ever grows large.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// consolidationSweepInterval is how often the goroutine scans for
// candidates. 15 min × 5 per sweep == 20/hour with the cap below.
const consolidationSweepInterval = 15 * time.Minute

// consolidationsPerSweep caps how many pairs can be consolidated in
// one sweep cycle. Combined with consolidationSweepInterval this is
// the rate-limit circuit breaker.
const consolidationsPerSweep = 5

// consolidationCeiling is the salient_facts entry count that forces
// a mid-cycle pass even if the daily floor hasn't elapsed. Pairs at
// or above this rank ahead of pairs below.
const consolidationCeiling = 20

// consolidationFloor is the minimum age past which a pair gets
// re-consolidated regardless of facts count. Daily cadence.
const consolidationFloor = 24 * time.Hour

// candidatePair carries the minimum data the consolidation step needs
// to decide whether to proceed and to build the prompt. The full
// row is re-loaded inside consolidateRelationship to avoid TOCTOU on
// salient_facts that may have grown between selection and the LLM
// call (snapshot is taken inside the call for the prune-by-length
// race-safety pattern).
type candidatePair struct {
	ActorID      string
	OtherID      string
	ActorName    string
	OtherName    string
	ActorAgent   string
	FactCount    int
	LastConsoAt  *time.Time
}

// findConsolidationCandidates scans actor_relationship for pairs that
// need a pass. Selection rules:
//   - salient_facts non-empty (nothing to consolidate otherwise)
//   - actor's llm_memory_agent is shared-VA (only those need this
//     layer; VA-attached actors get continuity from llm-memory's own
//     soul)
//   - fact count >= ceiling OR last_consolidated_at is NULL OR older
//     than the daily floor
//
// Order: ceiling-overdue first (sorted by fact count DESC), then
// untouched / oldest-consolidated. Limit applied at the SQL level so
// a single sweep can't run away.
func (app *App) findConsolidationCandidates(ctx context.Context, limit int) ([]candidatePair, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT r.actor_id::text,
		       r.other_actor_id::text,
		       a.display_name,
		       o.display_name,
		       COALESCE(a.llm_memory_agent, ''),
		       jsonb_array_length(r.salient_facts) AS facts_count,
		       r.last_consolidated_at
		  FROM actor_relationship r
		  JOIN actor a ON a.id = r.actor_id
		  JOIN actor o ON o.id = r.other_actor_id
		 WHERE jsonb_array_length(r.salient_facts) > 0
		   AND COALESCE(a.llm_memory_agent, '') IN ('salem-vendor', 'salem-visitor')
		   AND (
		       jsonb_array_length(r.salient_facts) >= $1
		    OR r.last_consolidated_at IS NULL
		    OR r.last_consolidated_at < NOW() - $2::interval
		   )
		 ORDER BY (jsonb_array_length(r.salient_facts) >= $1) DESC,
		          r.last_consolidated_at NULLS FIRST,
		          jsonb_array_length(r.salient_facts) DESC
		 LIMIT $3
	`, consolidationCeiling, fmt.Sprintf("%d seconds", int(consolidationFloor.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("query consolidation candidates: %w", err)
	}
	defer rows.Close()
	var out []candidatePair
	for rows.Next() {
		var c candidatePair
		var lastAt *time.Time
		if err := rows.Scan(&c.ActorID, &c.OtherID, &c.ActorName, &c.OtherName,
			&c.ActorAgent, &c.FactCount, &lastAt); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		c.LastConsoAt = lastAt
		out = append(out, c)
	}
	return out, rows.Err()
}

// consolidateRelationship runs the full pass on one (actor, other)
// pair. Loads the row fresh, snapshots the salient_facts array,
// constructs the distillation prompt, calls the actor's VA, writes
// the new summary_text + prunes consolidated entries, stamps the
// last_consolidated_at marker.
//
// Race-safety: between snapshot and write, more facts may be appended
// by event hooks. Pruning by snapshot length (drop the first N
// elements where N == snapshot length) keeps the post-snapshot
// appends intact in salient_facts. The LLM saw the snapshot; the
// prune matches.
//
// Failure modes:
//   - LLM call errors / non-200       → return error, leave row alone
//   - Reply text empty / whitespace   → return error, leave row alone
//   - Reply contains tool calls only  → return error, leave row alone
//
// The sweep logs and skips on any error; the row gets retried next
// cycle. No partial-state writes.
func (app *App) consolidateRelationship(ctx context.Context, c candidatePair) error {
	var summaryText string
	var salientBytes []byte
	err := app.DB.QueryRow(ctx, `
		SELECT summary_text, salient_facts
		  FROM actor_relationship
		 WHERE actor_id = $1 AND other_actor_id = $2
	`, c.ActorID, c.OtherID).Scan(&summaryText, &salientBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load actor_relationship: %w", err)
	}

	var facts []map[string]interface{}
	if err := json.Unmarshal(salientBytes, &facts); err != nil {
		return fmt.Errorf("unmarshal salient_facts: %w", err)
	}
	snapshotLen := len(facts)
	if snapshotLen == 0 {
		return nil
	}

	prompt := buildConsolidationPrompt(c.ActorName, c.OtherName, summaryText, facts)

	// Empty sceneID → companion-mode call, doesn't accumulate into
	// any of Hannah's regular tick scenes. Empty scene structure too.
	// No tools offered — the LLM has no escape into a tool call and
	// must respond with text.
	reply, err := app.npcChatClient.sendChat(ctx, c.ActorAgent, prompt, nil, "", "", nil)
	if err != nil {
		return fmt.Errorf("consolidate sendChat: %w", err)
	}
	newSummary := strings.TrimSpace(reply.Text)
	if newSummary == "" {
		return fmt.Errorf("consolidate: empty reply text (tool_calls=%d)", len(reply.ToolCalls))
	}

	// Prune the first snapshotLen entries, keeping anything appended
	// during the LLM call. Single UPDATE keeps the write atomic.
	_, err = app.DB.Exec(ctx, `
		UPDATE actor_relationship
		   SET summary_text          = $3,
		       salient_facts         = COALESCE((
		           SELECT jsonb_agg(elem)
		             FROM jsonb_array_elements(salient_facts)
		             WITH ORDINALITY x(elem, ord)
		            WHERE ord > $4
		       ), '[]'::jsonb),
		       last_consolidated_at  = NOW(),
		       updated_at            = NOW()
		 WHERE actor_id = $1 AND other_actor_id = $2
	`, c.ActorID, c.OtherID, newSummary, snapshotLen)
	if err != nil {
		return fmt.Errorf("write consolidation: %w", err)
	}
	log.Printf("consolidate ok: %s↔%s (pruned=%d, new_summary_len=%d)",
		c.ActorName, c.OtherName, snapshotLen, len(newSummary))
	return nil
}

// buildConsolidationPrompt composes the user-message text the actor's
// VA reads for a distillation. Frames the task as private reflection
// (not a scene), explicitly disclaims tools (overrides the salem-
// vendor system prompt's tool-discipline directive), and asks for
// prose synthesis rather than a list.
//
// Word-count cap (~200 words) is a soft target — the LLM tends to
// honor "brief" but not strict counts. Long replies still parse fine;
// they just consume more perception budget on subsequent ticks.
func buildConsolidationPrompt(actorName, otherName, priorSummary string, facts []map[string]interface{}) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s. This is not a scene — you are reflecting privately on your acquaintance with %s. There are no tools available for this turn; respond with prose only.\n\n",
		actorName, otherName)
	if s := strings.TrimSpace(priorSummary); s != "" {
		b.WriteString("Your prior reflection on them:\n")
		b.WriteString(s)
		b.WriteString("\n\n")
	} else {
		fmt.Fprintf(&b, "You haven't formed a reflection on %s before now.\n\n", otherName)
	}
	b.WriteString("Recent interactions, oldest first:\n")
	for _, f := range facts {
		text, _ := f["text"].(string)
		if t := strings.TrimSpace(text); t != "" {
			b.WriteString("- ")
			b.WriteString(t)
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\nWrite a brief paragraph (under 200 words) capturing your current sense of %s — a coherent impression, not a list of events. Past or present tense, whichever fits. Just the paragraph, no preamble or sign-off.",
		otherName)
	return b.String()
}

// runConsolidationSweep is the periodic goroutine. Spawned once from
// main.go alongside the other sweeps. Find candidates, process up to
// the per-sweep cap, log and continue on per-pair failure.
func (app *App) runConsolidationSweep(ctx context.Context) {
	t := time.NewTicker(consolidationSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			candidates, err := app.findConsolidationCandidates(ctx, consolidationsPerSweep)
			if err != nil {
				log.Printf("consolidation sweep: find candidates: %v", err)
				continue
			}
			for _, c := range candidates {
				if err := app.consolidateRelationship(ctx, c); err != nil {
					log.Printf("consolidate %s↔%s: %v", c.ActorName, c.OtherName, err)
					// Swallow — next sweep retries the row (still
					// qualifies under the same selection rules).
				}
			}
		}
	}
}
