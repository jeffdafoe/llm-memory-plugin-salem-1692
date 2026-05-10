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
	"log"
	"strings"
	"time"

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

// salientTextMaxLen caps a single salient_facts text entry. Prevents
// a verbose speak from blowing up the JSONB. The cap mirrors the
// "Recent:" perception block's per-line length budget so a fact-line
// reads at the same density as a real-time speech entry.
const salientTextMaxLen = 220

// truncateForSalient returns the text trimmed to salientTextMaxLen
// runes, with an ellipsis when truncated. Ensures the cap is rune-
// safe (no torn multi-byte sequences).
func truncateForSalient(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	runes := []rune(t)
	if len(runes) <= salientTextMaxLen {
		return t
	}
	return string(runes[:salientTextMaxLen-1]) + "…"
}

// recordInteraction UPSERTs an actor_relationship row, appending the
// {at, kind, text} entry to salient_facts and bumping
// interaction_count + last_interaction_at. Skip-writes when actorID
// is not shared-VA-backed (callers don't have to gate themselves —
// this helper is the single chokepoint for write-side gating).
//
// kind values today (extend as new event hooks land):
//   - "spoke"          — actor said something the other heard
//   - "heard"          — other said something the actor heard
//
// salient_facts is appended unbounded for now; Phase 3 consolidation
// will compress the trail. If growth becomes a concern before Phase 3
// lands, add a cap here (read-trim-write or a SQL-side window).
//
// Errors are returned for caller logging — write failures here
// shouldn't block the speech itself, so callers log + fall through.
func (app *App) recordInteraction(ctx context.Context, actorID, actorAgent, otherActorID, kind, text string, at time.Time) error {
	if !isSharedVAAgent(actorAgent) {
		return nil
	}
	text = truncateForSalient(text)
	if text == "" || actorID == "" || otherActorID == "" || actorID == otherActorID {
		return nil
	}
	fact := map[string]interface{}{
		"at":   at.UTC().Format(time.RFC3339),
		"kind": kind,
		"text": text,
	}
	factBytes, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("marshal salient fact: %w", err)
	}
	_, err = app.DB.Exec(ctx, `
		INSERT INTO actor_relationship
		    (actor_id, other_actor_id, salient_facts, interaction_count, last_interaction_at)
		VALUES
		    ($1, $2, jsonb_build_array($3::jsonb), 1, $4)
		ON CONFLICT (actor_id, other_actor_id)
		DO UPDATE SET
		    salient_facts        = actor_relationship.salient_facts || EXCLUDED.salient_facts,
		    interaction_count    = actor_relationship.interaction_count + 1,
		    last_interaction_at  = EXCLUDED.last_interaction_at,
		    updated_at           = NOW()
	`, actorID, otherActorID, factBytes, at)
	if err != nil {
		return fmt.Errorf("upsert actor_relationship: %w", err)
	}
	return nil
}

// recordSpeechInteractions writes one pair of relationship updates
// per (speaker, listener) in the speaker's huddle: the speaker sees
// "spoke" toward each peer, each peer sees "heard" from the speaker.
// Both sides are gated by recordInteraction's shared-VA check, so no
// rows are created for VA-attached actors who get continuity from
// llm-memory's own soul.
//
// Called from both NPC speak commit (agent_tick.go executeAgentCommit
// case "speak") and PC speech (pc_handlers.go handlePCSpeak). Errors
// are logged and swallowed — a relationship-write failure shouldn't
// abort the speech the player or NPC just produced.
func (app *App) recordSpeechInteractions(ctx context.Context, speakerID, speakerName, text string, at time.Time) {
	if speakerID == "" || strings.TrimSpace(text) == "" {
		return
	}
	rows, err := app.DB.Query(ctx, `
		SELECT id::text, display_name, COALESCE(llm_memory_agent, '')
		  FROM actor
		 WHERE current_huddle_id IS NOT NULL
		   AND current_huddle_id = (SELECT current_huddle_id FROM actor WHERE id = $1)
		   AND id::text <> $1
	`, speakerID)
	if err != nil {
		log.Printf("recordSpeechInteractions query peers (speaker=%s): %v", speakerID, err)
		return
	}
	defer rows.Close()

	type peer struct {
		id    string
		name  string
		agent string
	}
	var peers []peer
	for rows.Next() {
		var p peer
		if err := rows.Scan(&p.id, &p.name, &p.agent); err != nil {
			log.Printf("recordSpeechInteractions scan peer (speaker=%s): %v", speakerID, err)
			continue
		}
		peers = append(peers, p)
	}
	if err := rows.Err(); err != nil {
		log.Printf("recordSpeechInteractions iterate peers (speaker=%s): %v", speakerID, err)
		// Fall through with whatever peers we did collect.
	}

	speakerAgent := ""
	_ = app.DB.QueryRow(ctx, `SELECT COALESCE(llm_memory_agent, '') FROM actor WHERE id = $1`, speakerID).Scan(&speakerAgent)

	for _, p := range peers {
		// Speaker's row toward peer: "I said: ..."
		if err := app.recordInteraction(ctx, speakerID, speakerAgent, p.id, "spoke", text, at); err != nil {
			log.Printf("recordSpeechInteractions write speaker→%s: %v", p.name, err)
		}
		// Peer's row toward speaker: "<speaker> said: ..."
		listenerText := fmt.Sprintf("%s said: %s", speakerName, text)
		if err := app.recordInteraction(ctx, p.id, p.agent, speakerID, "heard", listenerText, at); err != nil {
			log.Printf("recordSpeechInteractions write %s→speaker: %v", p.name, err)
		}
	}
}

// loadActorIdentities looks up display_name + llm_memory_agent for a
// pair of actor ids in one round-trip. Returns a map keyed by actor
// id. Used by recordPayInteractions and other multi-actor hooks that
// need both sides' name + agent without making the caller pass them
// in. Missing rows are simply absent from the map; callers handle the
// empty-slug case naturally via recordInteraction's gating.
func (app *App) loadActorIdentities(ctx context.Context, ids ...string) map[string]struct {
	Name  string
	Agent string
} {
	out := map[string]struct {
		Name  string
		Agent string
	}{}
	if len(ids) == 0 {
		return out
	}
	rows, err := app.DB.Query(ctx, `
		SELECT id::text, display_name, COALESCE(llm_memory_agent, '')
		  FROM actor
		 WHERE id::text = ANY($1::text[])
	`, ids)
	if err != nil {
		log.Printf("loadActorIdentities query: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, agent string
		if err := rows.Scan(&id, &name, &agent); err != nil {
			continue
		}
		out[id] = struct {
			Name  string
			Agent string
		}{Name: name, Agent: agent}
	}
	return out
}

// payItemDescriptor renders the item portion of a pay narrative —
// "<qty> <item>" when qty > 1, bare item otherwise, empty for
// item-less coin-only pays. Used by both buyer and seller text
// generators so they stay consistent.
func payItemDescriptor(item string, qty int) string {
	item = strings.TrimSpace(item)
	if item == "" {
		return ""
	}
	if qty > 1 {
		return fmt.Sprintf("%d %s", qty, item)
	}
	return item
}

// recordPayInteractions writes the buyer↔seller relationship facts
// after a pay attempt resolves. Skips on rejected/failed/withdrawn
// outcomes (validation rejects and system errors aren't narrative
// beats); records the three real outcomes:
//
//   - "ok"        — buyer paid, seller accepted
//   - "declined"  — buyer offered, seller refused (with reason)
//   - "countered" — seller proposed a different total
//
// Both rows are written; recordInteraction's shared-VA gate decides
// which actually persist. The helper looks up display_name + agent
// for both ids in a single query so callers don't have to thread
// that state.
func (app *App) recordPayInteractions(ctx context.Context, buyerID, sellerID string, req payRequest, result payResult) {
	if buyerID == "" || sellerID == "" {
		return
	}
	switch result.Result {
	case "ok", "declined", "countered":
	default:
		return
	}
	ids := app.loadActorIdentities(ctx, buyerID, sellerID)
	buyer, ok := ids[buyerID]
	if !ok {
		return
	}
	seller, ok := ids[sellerID]
	if !ok {
		return
	}
	itemPart := payItemDescriptor(req.Item, req.Qty)
	now := time.Now()
	var buyerKind, sellerKind, buyerText, sellerText string
	switch result.Result {
	case "ok":
		buyerKind, sellerKind = "paid", "paid_by"
		if itemPart != "" {
			buyerText = fmt.Sprintf("I paid %s %d coins for %s.", seller.Name, req.Amount, itemPart)
			sellerText = fmt.Sprintf("%s paid me %d coins for %s.", buyer.Name, req.Amount, itemPart)
		} else {
			buyerText = fmt.Sprintf("I paid %s %d coins.", seller.Name, req.Amount)
			sellerText = fmt.Sprintf("%s paid me %d coins.", buyer.Name, req.Amount)
		}
	case "declined":
		buyerKind, sellerKind = "pay_declined_by", "declined_pay"
		decline := strings.TrimSpace(result.Message)
		reasonSuffix := ""
		if decline != "" {
			reasonSuffix = fmt.Sprintf(" Their reason: %q", decline)
		}
		if itemPart != "" {
			buyerText = fmt.Sprintf("I offered %s %d coins for %s; they declined.%s", seller.Name, req.Amount, itemPart, reasonSuffix)
			sellerText = fmt.Sprintf("%s offered me %d coins for %s; I declined.%s", buyer.Name, req.Amount, itemPart, reasonSuffix)
		} else {
			buyerText = fmt.Sprintf("I offered %s %d coins; they declined.%s", seller.Name, req.Amount, reasonSuffix)
			sellerText = fmt.Sprintf("%s offered me %d coins; I declined.%s", buyer.Name, req.Amount, reasonSuffix)
		}
	case "countered":
		buyerKind, sellerKind = "countered_by", "countered"
		msg := strings.TrimSpace(result.Message)
		wordsSuffix := ""
		if msg != "" {
			wordsSuffix = fmt.Sprintf(" Their words: %q", msg)
		}
		if itemPart != "" {
			buyerText = fmt.Sprintf("I offered %s %d coins for %s; they countered with %d.%s", seller.Name, req.Amount, itemPart, result.CounterAmount, wordsSuffix)
			sellerText = fmt.Sprintf("%s offered me %d coins for %s; I countered with %d.%s", buyer.Name, req.Amount, itemPart, result.CounterAmount, wordsSuffix)
		} else {
			buyerText = fmt.Sprintf("I offered %s %d coins; they countered with %d.%s", seller.Name, req.Amount, result.CounterAmount, wordsSuffix)
			sellerText = fmt.Sprintf("%s offered me %d coins; I countered with %d.%s", buyer.Name, req.Amount, result.CounterAmount, wordsSuffix)
		}
	}
	if err := app.recordInteraction(ctx, buyerID, buyer.Agent, sellerID, buyerKind, buyerText, now); err != nil {
		log.Printf("recordPayInteractions buyer→seller: %v", err)
	}
	if err := app.recordInteraction(ctx, sellerID, seller.Agent, buyerID, sellerKind, sellerText, now); err != nil {
		log.Printf("recordPayInteractions seller→buyer: %v", err)
	}
}

// recordServeInteractions writes salient_facts for each (server,
// recipient) pair after a successful serve. Multi-recipient serves
// fan out one write per direction per recipient. Skips on
// rejected/failed (the model didn't actually hand anything over).
//
// consume-now path uses sr.NeedUpdates (already has ActorID +
// DisplayName per recipient). Take-home path uses sr.TakeHomeRecipientIDs
// and resolves names via loadActorIdentities.
func (app *App) recordServeInteractions(ctx context.Context, serverID string, sr serveResult) {
	if serverID == "" || sr.Result != "ok" {
		return
	}
	type recipient struct {
		id   string
		name string
	}
	var recips []recipient
	if len(sr.NeedUpdates) > 0 {
		for _, nu := range sr.NeedUpdates {
			if nu.ActorID != "" {
				recips = append(recips, recipient{id: nu.ActorID, name: nu.DisplayName})
			}
		}
	} else if len(sr.TakeHomeRecipientIDs) > 0 {
		ids := append([]string{serverID}, sr.TakeHomeRecipientIDs...)
		idents := app.loadActorIdentities(ctx, ids...)
		for _, id := range sr.TakeHomeRecipientIDs {
			if ident, ok := idents[id]; ok {
				recips = append(recips, recipient{id: id, name: ident.Name})
			}
		}
	}
	if len(recips) == 0 {
		return
	}
	// One identity lookup for the server (and any unresolved recipients).
	lookupIDs := []string{serverID}
	for _, r := range recips {
		lookupIDs = append(lookupIDs, r.id)
	}
	idents := app.loadActorIdentities(ctx, lookupIDs...)
	server, ok := idents[serverID]
	if !ok {
		return
	}
	// serveResult doesn't carry per-recipient qty separately from the
	// per-recipient need delta — the serve tool is "qty per recipient,"
	// uniform across the list. For the salient-fact text the unit count
	// matters less than the item itself, so render just the item kind.
	// Add a Qty field on serveResult if Phase 3 consolidation needs it.
	itemDesc := strings.TrimSpace(sr.Item)
	if itemDesc == "" {
		itemDesc = "something"
	}
	now := time.Now()
	for _, recip := range recips {
		recipIdent, ok := idents[recip.id]
		recipAgent := ""
		if ok {
			recipAgent = recipIdent.Agent
		}
		serverText := fmt.Sprintf("I served %s — %s.", recip.name, itemDesc)
		recipText := fmt.Sprintf("%s served me %s.", server.Name, itemDesc)
		if err := app.recordInteraction(ctx, serverID, server.Agent, recip.id, "served", serverText, now); err != nil {
			log.Printf("recordServeInteractions server→%s: %v", recip.name, err)
		}
		if err := app.recordInteraction(ctx, recip.id, recipAgent, serverID, "served_by", recipText, now); err != nil {
			log.Printf("recordServeInteractions %s→server: %v", recip.name, err)
		}
	}
}

// recordDeliverOrderInteractions writes the seller↔buyer pair after a
// successful deliver_order. Skips on rejected/failed. Single recipient
// (the order's buyer) — multi-consumer orders still resolve to one
// pay_ledger row with one buyer.
func (app *App) recordDeliverOrderInteractions(ctx context.Context, sellerID string, dr deliverOrderResult) {
	if sellerID == "" || dr.Result != "ok" || dr.BuyerID == "" {
		return
	}
	idents := app.loadActorIdentities(ctx, sellerID, dr.BuyerID)
	seller, ok := idents[sellerID]
	if !ok {
		return
	}
	buyer, ok := idents[dr.BuyerID]
	if !ok {
		return
	}
	itemPart := payItemDescriptor(dr.ItemKind, dr.Qty)
	if itemPart == "" {
		itemPart = "their order"
	}
	now := time.Now()
	sellerText := fmt.Sprintf("I delivered %s to %s.", itemPart, buyer.Name)
	buyerText := fmt.Sprintf("%s delivered %s to me.", seller.Name, itemPart)
	if err := app.recordInteraction(ctx, sellerID, seller.Agent, dr.BuyerID, "delivered", sellerText, now); err != nil {
		log.Printf("recordDeliverOrderInteractions seller→buyer: %v", err)
	}
	if err := app.recordInteraction(ctx, dr.BuyerID, buyer.Agent, sellerID, "received", buyerText, now); err != nil {
		log.Printf("recordDeliverOrderInteractions buyer→seller: %v", err)
	}
}
