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

// consolidationFirstMinFacts (ZBBS-WORK-233) is the minimum
// salient_facts count required to qualify a FIRST consolidation
// (last_consolidated_at IS NULL). Pre-fix, any new pair with >= 1
// fact qualified — producing fake-deep "coherent impressions"
// distilled from a single "Good morrow" exchange (observed
// 2026-05-12: Moses↔Elizabeth first-reflection both fired on one
// shared line of dialog). Subsequent (>= 1 hour old) consolidations
// still qualify on the daily-floor branch regardless of fact count;
// this gate only affects the never-consolidated path.
const consolidationFirstMinFacts = 5

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
		    OR (r.last_consolidated_at IS NULL AND jsonb_array_length(r.salient_facts) >= $4)
		    OR r.last_consolidated_at < NOW() - $2::interval
		   )
		 ORDER BY (jsonb_array_length(r.salient_facts) >= $1) DESC,
		          r.last_consolidated_at NULLS FIRST,
		          jsonb_array_length(r.salient_facts) DESC
		 LIMIT $3
	`, consolidationCeiling, fmt.Sprintf("%d seconds", int(consolidationFloor.Seconds())), limit, consolidationFirstMinFacts)
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
	// ZBBS-WORK-233: dedupe identical fact text lines before emitting.
	// Pre-fix, polluted history (e.g. Elizabeth↔Wendy's repeat-text
	// presence-ghost trail) produced consolidation prompts with the
	// same sentence listed N times — the LLM was asked to distill 3
	// copies of one "Good evening, Wendy" line. Dedup keeps the first
	// occurrence so chronology is preserved (the list is oldest-first)
	// and silently drops subsequent identical lines. Independent of any
	// upstream pollution cleanup — protects the prompt quality
	// regardless of fact-trail provenance.
	b.WriteString("Recent interactions, oldest first:\n")
	seen := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		text, _ := f["text"].(string)
		t := strings.TrimSpace(text)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		b.WriteString("- ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nWrite a brief paragraph (under 200 words) capturing your current sense of %s — a coherent impression, not a list of events. Past or present tense, whichever fits. Just the paragraph, no preamble or sign-off.",
		otherName)
	return b.String()
}

// runConsolidationSweep is the periodic goroutine. Spawned once from
// main.go alongside the other sweeps. Find candidates, process up to
// the per-sweep cap, log and continue on per-pair failure.
//
// Two passes per sweep:
//   1. Per-pair relationship consolidation (Phase 3) — picks up to
//      consolidationsPerSweep candidates ordered by ceiling-overdue
//      then oldest-consolidated.
//   2. Per-actor narrative consolidation (Phase 4) — picks up to
//      narrativeConsolidationsPerSweep candidates ordered by
//      last_consolidated_at NULLS FIRST. Pairs always run first; if
//      the per-pair pass burned the rate budget, per-actor still
//      gets its own (smaller) budget so it isn't starved.
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
			} else {
				for _, c := range candidates {
					if err := app.consolidateRelationship(ctx, c); err != nil {
						log.Printf("consolidate %s↔%s: %v", c.ActorName, c.OtherName, err)
					}
				}
			}
			actors, err := app.findNarrativeConsolidationCandidates(ctx, narrativeConsolidationsPerSweep)
			if err != nil {
				log.Printf("narrative consolidation sweep: find candidates: %v", err)
				continue
			}
			for _, a := range actors {
				if err := app.consolidateActorNarrative(ctx, a); err != nil {
					log.Printf("consolidate-narrative %s: %v", a.ActorName, err)
				}
			}
		}
	}
}

// narrativeConsolidationsPerSweep caps the per-actor narrative pass.
// Daily floor only (no ceiling), so the load is at most one call per
// shared-VA actor per day. Cap protects against an unexpected influx
// of new shared-VA actors all due at once.
const narrativeConsolidationsPerSweep = 2

// narrativeConsolidationFloor is the minimum age past which an
// actor's narrative gets re-consolidated. Daily cadence — slower than
// per-pair (24h) intentionally. Per-actor reflection is broader and
// shouldn't churn on every tick-cluster.
const narrativeConsolidationFloor = 24 * time.Hour

// narrativeRecentEventsWindow bounds how far back the consolidation
// reads agent_action_log for the actor's "what happened to you
// recently" prompt. 7 days lets a quiet day's reflection still
// reference last-week's beats; longer would make the prompt too
// noisy.
const narrativeRecentEventsWindow = 7 * 24 * time.Hour

// narrativeRecentEventsLimit caps the events the prompt includes.
// Engine logs can be high-volume on busy days; the LLM doesn't need
// every line to synthesize a coherent self-reflection.
const narrativeRecentEventsLimit = 40

// narrativeCandidate is the per-actor variant of candidatePair. Only
// the actor side matters here — there's no peer.
type narrativeCandidate struct {
	ActorID     string
	ActorName   string
	ActorAgent  string
	LastConsoAt *time.Time
}

// findNarrativeConsolidationCandidates scans actor_narrative_state for
// shared-VA-backed actors whose evolving_summary is due for a refresh.
// Selection: actor is shared-VA, last_consolidated_at NULL or older
// than the daily floor. Order: NULLS FIRST then oldest-consolidated.
//
// LIMIT applied SQL-side. No ceiling-trigger because there's no
// append-pressure on the per-actor side — events flow into
// agent_action_log regardless of consolidation cadence.
func (app *App) findNarrativeConsolidationCandidates(ctx context.Context, limit int) ([]narrativeCandidate, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT s.actor_id::text,
		       a.display_name,
		       COALESCE(a.llm_memory_agent, ''),
		       s.last_consolidated_at
		  FROM actor_narrative_state s
		  JOIN actor a ON a.id = s.actor_id
		 WHERE COALESCE(a.llm_memory_agent, '') IN ('salem-vendor', 'salem-visitor')
		   AND (
		       s.last_consolidated_at IS NULL
		    OR s.last_consolidated_at < NOW() - $1::interval
		   )
		 ORDER BY s.last_consolidated_at NULLS FIRST
		 LIMIT $2
	`, fmt.Sprintf("%d seconds", int(narrativeConsolidationFloor.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("query narrative candidates: %w", err)
	}
	defer rows.Close()
	var out []narrativeCandidate
	for rows.Next() {
		var c narrativeCandidate
		if err := rows.Scan(&c.ActorID, &c.ActorName, &c.ActorAgent, &c.LastConsoAt); err != nil {
			return nil, fmt.Errorf("scan narrative candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// narrativeEvent is one row pulled from agent_action_log for the
// reflection prompt. Kept narrow — the LLM doesn't need full payload
// JSON, just the action_type, an optional speaker hint, and the time.
type narrativeEvent struct {
	OccurredAt   time.Time
	ActionType   string
	Result       string
	SpeakerName  string
	PayloadText  string
}

// loadRecentEventsForNarrative pulls the actor's own log rows from
// agent_action_log over the configured window, plus a small window
// of peer rows from the same huddles where the actor was present.
// Cross-actor inclusion is deliberately conservative — Phase 3
// already captured peer interactions via per-pair consolidation;
// Phase 4 supplements with the actor's own actions (their tool calls
// and what they spoke) for the self-reflection cut.
//
// Returns rows oldest-first so the LLM reads the trail in
// chronological order.
func (app *App) loadRecentEventsForNarrative(ctx context.Context, actorID string) ([]narrativeEvent, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT occurred_at,
		       action_type,
		       COALESCE(result, ''),
		       COALESCE(speaker_name, ''),
		       COALESCE(payload->>'text', payload->>'verb_phrase', '')
		  FROM agent_action_log
		 WHERE actor_id = $1
		   AND occurred_at > NOW() - $2::interval
		   AND COALESCE(result, '') IN ('ok', '')
		 ORDER BY occurred_at ASC, id ASC
		 LIMIT $3
	`, actorID,
		fmt.Sprintf("%d seconds", int(narrativeRecentEventsWindow.Seconds())),
		narrativeRecentEventsLimit)
	if err != nil {
		return nil, fmt.Errorf("query agent_action_log: %w", err)
	}
	defer rows.Close()
	var out []narrativeEvent
	for rows.Next() {
		var e narrativeEvent
		if err := rows.Scan(&e.OccurredAt, &e.ActionType, &e.Result, &e.SpeakerName, &e.PayloadText); err != nil {
			return nil, fmt.Errorf("scan agent_action_log: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// loadRelationshipSummariesForActor returns one summary line per peer
// the actor has a non-empty summary_text for. Used as the "people in
// your story" half of the per-actor reflection prompt. Skips rows
// with empty summaries (no consolidated impression yet — Phase 3
// hasn't covered them).
func (app *App) loadRelationshipSummariesForActor(ctx context.Context, actorID string) (map[string]string, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT o.display_name, r.summary_text
		  FROM actor_relationship r
		  JOIN actor o ON o.id = r.other_actor_id
		 WHERE r.actor_id = $1
		   AND TRIM(r.summary_text) <> ''
		 ORDER BY o.display_name
	`, actorID)
	if err != nil {
		return nil, fmt.Errorf("query relationship summaries: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, summary string
		if err := rows.Scan(&name, &summary); err != nil {
			return nil, fmt.Errorf("scan relationship summary: %w", err)
		}
		out[name] = summary
	}
	return out, rows.Err()
}

// buildNarrativeConsolidationPrompt composes the reflection user
// message. Frames as a private end-of-day check-in rather than a
// scene; provides the events trail + per-peer summaries; asks for a
// brief paragraph synthesizing where the actor is in their own story.
//
// The prompt is intentionally not symmetric with buildConsolidationPrompt
// — that one is about ONE peer; this one is about SELF given many
// peers + a week's events. Different ask, different framing.
func buildNarrativeConsolidationPrompt(actorName, priorSummary string, events []narrativeEvent, peerSummaries map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s. This is not a scene — you are reflecting privately on your own days, the people you've been seeing, and where you find yourself right now. There are no tools available for this turn; respond with prose only.\n\n", actorName)
	if s := strings.TrimSpace(priorSummary); s != "" {
		b.WriteString("Your prior reflection on yourself:\n")
		b.WriteString(s)
		b.WriteString("\n\n")
	} else {
		b.WriteString("You haven't reflected on yourself in this way before.\n\n")
	}
	if len(events) > 0 {
		b.WriteString("Things you did or said in the past week, oldest first:\n")
		for _, e := range events {
			line := fmt.Sprintf("- [%s] %s", e.OccurredAt.Format("Jan 2"), e.ActionType)
			if t := strings.TrimSpace(e.PayloadText); t != "" {
				line += ": " + t
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(peerSummaries) > 0 {
		b.WriteString("People you have an impression of:\n")
		for name, summary := range peerSummaries {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(strings.TrimSpace(summary))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Write a brief paragraph (under 250 words) on where you are in your own story right now — disposition, rhythm, what you've been noticing about the village or yourself. Synthesize, don't list. Past or present tense, whichever fits. Just the paragraph, no preamble or sign-off.")
	return b.String()
}

// consolidateActorNarrative runs the daily reflection on one actor.
// Reads recent events + peer summaries, builds the prompt, calls the
// actor's VA in companion mode (no scene pollution), takes the prose
// reply as the new evolving_summary, stamps last_consolidated_at.
//
// Does NOT prune anything — agent_action_log is shared infrastructure
// (other consumers read it: dream pipeline, perception "Recent:" block,
// pay history). The actor_relationship summaries are inputs, not
// inputs-to-consume. Phase 4's only writes are to actor_narrative_state.
func (app *App) consolidateActorNarrative(ctx context.Context, c narrativeCandidate) error {
	var seed, prior string
	err := app.DB.QueryRow(ctx, `
		SELECT seed_text, evolving_summary
		  FROM actor_narrative_state
		 WHERE actor_id = $1
	`, c.ActorID).Scan(&seed, &prior)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load actor_narrative_state: %w", err)
	}

	events, err := app.loadRecentEventsForNarrative(ctx, c.ActorID)
	if err != nil {
		return fmt.Errorf("load recent events: %w", err)
	}
	peers, err := app.loadRelationshipSummariesForActor(ctx, c.ActorID)
	if err != nil {
		return fmt.Errorf("load peer summaries: %w", err)
	}
	// If there's truly nothing to reflect on (no events AND no peer
	// summaries AND no prior reflection), skip — write the marker
	// anyway so the sweep doesn't keep retrying a row with no source
	// material. The marker just means "checked, nothing to say."
	if len(events) == 0 && len(peers) == 0 && strings.TrimSpace(prior) == "" {
		_, _ = app.DB.Exec(ctx, `
			UPDATE actor_narrative_state
			   SET last_consolidated_at = NOW()
			 WHERE actor_id = $1
		`, c.ActorID)
		return nil
	}

	prompt := buildNarrativeConsolidationPrompt(c.ActorName, prior, events, peers)
	reply, err := app.npcChatClient.sendChat(ctx, c.ActorAgent, prompt, nil, "", "", nil)
	if err != nil {
		return fmt.Errorf("consolidate-narrative sendChat: %w", err)
	}
	newSummary := strings.TrimSpace(reply.Text)
	if newSummary == "" {
		return fmt.Errorf("consolidate-narrative: empty reply text (tool_calls=%d)", len(reply.ToolCalls))
	}

	_, err = app.DB.Exec(ctx, `
		UPDATE actor_narrative_state
		   SET evolving_summary    = $2,
		       last_consolidated_at = NOW(),
		       updated_at           = NOW()
		 WHERE actor_id = $1
	`, c.ActorID, newSummary)
	if err != nil {
		return fmt.Errorf("write narrative consolidation: %w", err)
	}
	log.Printf("consolidate-narrative ok: %s (events=%d, peers=%d, new_summary_len=%d)",
		c.ActorName, len(events), len(peers), len(newSummary))
	return nil
}
