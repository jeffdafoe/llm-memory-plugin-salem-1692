package main

// Take_break eviction sequence — close-shop-with-vendor-inside model.
//
// When a vendor calls take_break, the engine no longer walks them home.
// Instead the structure becomes closed (derived from break_until),
// any non-exempt occupants get a polite ask, then an assertive ask,
// then a force-eject if they're still around. The vendor recovers
// tiredness while the door is shut.
//
// Three phases (wall-clock):
//
//   Phase 1 — Polite ask. The vendor's announcement (npc_spoke from
//             the take_break commit) hits everyone's perception. To
//             make sure occupants tick within seconds (not 1-3 min on
//             the natural cost-guarded schedule), the engine fires a
//             forced co-located tick on every non-exempt agent NPC
//             inside. PCs already poll their state, so they see the
//             closed-state on their next perception refresh. Wait
//             eviction_grace_seconds.
//
//   Phase 2 — Assertive ask. If anyone non-exempt is still inside, fire
//             a one-shot LLM call on the vendor for an assertive line
//             ("OUT, all of you, NOW"). Broadcast as npc_spoke. Forced
//             ticks again on remaining occupants. Wait
//             eviction_assertive_seconds.
//
//   Phase 3 — Force-eject. Anyone still inside and non-exempt:
//             setNPCInside(actor, false) (clear the inside flag),
//             apply +eviction_tiredness_penalty (capped at 24),
//             broadcast a per-actor pc_evicted event so clients can
//             show the kicked PC their fade-out.
//
// The state machine is wall-clock and not persisted. If the engine
// crashes during eviction, the closed-door state remains (break_until
// is on the actor row), but the active sequence is lost — remaining
// occupants stay until they leave on their own. Acceptable degradation
// for v1.
//
// Lodger and home/work exemption: every "non-exempt" check goes through
// canEnter / wouldBeEvictionExempt in lodging.go. Lodgers ride out
// the break unaffected, vendors and family at home stay put.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// evictionPhase configures one phase of the eviction state machine.
// The waitDuration is how long after the previous step the next check
// runs; for phase 1 this is the post-broadcast grace period, for
// phase 2 the post-assertive-ask hard deadline.
type evictionPhase struct {
	name     string
	wait     time.Duration
	loadKey  string // settings key
	loadDef  int    // default seconds when setting missing
}

// startEvictionSequence kicks off the wall-clock state machine after a
// take_break commit on an interior structure with non-exempt
// occupants. Spawns a goroutine; non-blocking. Returns immediately.
//
// vendorID — the actor on break (used for the assertive-ask LLM call,
//            also as the seller for the forced co-located tick scenes).
// vendorName — display name for the assertive-ask prompt.
// vendorAgent — llm_memory_agent name for the LLM call (empty = skip
//               assertive-ask phase, jump to force-eject).
// structureID — the structure being closed.
// reason — the reason text from the take_break commit (folded into
//          the assertive-ask prompt for character consistency).
func (app *App) startEvictionSequence(ctx context.Context, vendorID, vendorName, vendorAgent, structureID, reason string) {
	go app.runEvictionSequence(context.Background(), vendorID, vendorName, vendorAgent, structureID, reason)
}

func (app *App) runEvictionSequence(ctx context.Context, vendorID, vendorName, vendorAgent, structureID, reason string) {
	graceSec := app.loadIntSetting(ctx, "take_break.eviction_grace_seconds", 180)
	assertiveSec := app.loadIntSetting(ctx, "take_break.eviction_assertive_seconds", 300)
	tiredPenalty := app.loadIntSetting(ctx, "take_break.eviction_tiredness_penalty", 3)

	// Phase 0 — initial co-located ticks on non-exempt occupants so the
	// closed-state hits their perception within seconds, not minutes.
	// PCs aren't ticked (they poll their own state); only agent NPCs
	// get the forced tick.
	occupants, err := app.findEvictableOccupants(ctx, vendorID, structureID)
	if err != nil {
		log.Printf("eviction: find occupants %s: %v", structureID, err)
		return
	}
	if len(occupants) == 0 {
		return // nobody to evict; closed-door is the entire signal.
	}
	app.forceTickOccupants(ctx, occupants, structureID, vendorID, "vendor-take-break")

	// Phase 1 wait.
	time.Sleep(time.Duration(graceSec) * time.Second)

	occupants, err = app.findEvictableOccupants(ctx, vendorID, structureID)
	if err != nil {
		log.Printf("eviction: find occupants (post-grace) %s: %v", structureID, err)
		return
	}
	if len(occupants) == 0 {
		return
	}

	// Phase 2 — assertive ask. Real LLM call on the vendor; the reply
	// becomes a synthetic npc_spoke so the room hears the more pointed
	// language in the vendor's own voice. If the vendor has no llm
	// agent (PC keeper, decorative NPC, edge cases), skip the call and
	// proceed directly to phase 3.
	if vendorAgent != "" {
		askText := app.runEvictionAssertiveAsk(ctx, vendorAgent, vendorName, reason, occupants)
		if askText != "" {
			// Eviction asks are room-directed (multiple addressees), not 1:1.
			// Pass empty addressee fields so the talk panel falls back to
			// the unaddressed render — "Vendor: <ask>" without a "(to X):"
			// parenthetical that would be misleading.
			app.broadcastDeliberationSpeak(ctx, vendorID, vendorName, "", "", askText)
			app.forceTickOccupants(ctx, occupants, structureID, vendorID, "vendor-assertive-ask")
		}
	}

	// Phase 2 wait.
	time.Sleep(time.Duration(assertiveSec) * time.Second)

	occupants, err = app.findEvictableOccupants(ctx, vendorID, structureID)
	if err != nil {
		log.Printf("eviction: find occupants (post-assertive) %s: %v", structureID, err)
		return
	}
	if len(occupants) == 0 {
		return
	}

	// Phase 3 — force-eject. Clear inside_structure_id, apply tiredness
	// penalty, broadcast per-actor pc_evicted event for client UI.
	for _, o := range occupants {
		app.setNPCInside(ctx, o.ActorID, false, "")
		app.applyEvictionTirednessPenalty(ctx, o.ActorID, tiredPenalty)
		app.Hub.Broadcast(WorldEvent{
			Type: "pc_evicted",
			Data: map[string]interface{}{
				"actor_id":     o.ActorID,
				"structure_id": structureID,
				"reason":       "take_break",
				"at":           time.Now().UTC().Format(time.RFC3339),
			},
		})
		log.Printf("eviction: force-ejected %s (%s) from structure %s, +%d tiredness",
			o.DisplayName, o.ActorID, structureID, tiredPenalty)
	}
}

// occupantInfo is one actor flagged for the eviction sequence.
type occupantInfo struct {
	ActorID     string
	DisplayName string
	IsAgentNPC  bool // has llm_memory_agent set
	AgentName   string
}

// findEvictableOccupants returns the non-exempt actors currently inside
// the structure, excluding the vendor themselves. Each call re-runs
// the exemption check so a lodger whose lodger_until expires mid-
// eviction transitions naturally to evictable on the next phase.
func (app *App) findEvictableOccupants(ctx context.Context, vendorID, structureID string) ([]occupantInfo, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text, a.display_name,
		        COALESCE(a.llm_memory_agent, '')
		   FROM actor a
		  WHERE a.inside_structure_id = $1::uuid
		    AND a.id <> $2::uuid`,
		structureID, vendorID,
	)
	if err != nil {
		return nil, fmt.Errorf("query inside actors: %w", err)
	}
	type rawOccupant struct {
		ID, Name, Agent string
	}
	var raw []rawOccupant
	for rows.Next() {
		var ro rawOccupant
		if err := rows.Scan(&ro.ID, &ro.Name, &ro.Agent); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan: %w", err)
		}
		raw = append(raw, ro)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter: %w", err)
	}

	out := make([]occupantInfo, 0, len(raw))
	for _, ro := range raw {
		exempt, err := app.wouldBeEvictionExempt(ctx, ro.ID, structureID)
		if err != nil {
			log.Printf("eviction: exemption check %s: %v (treating as evictable)", ro.ID, err)
		}
		if exempt {
			continue
		}
		out = append(out, occupantInfo{
			ActorID:     ro.ID,
			DisplayName: ro.Name,
			IsAgentNPC:  ro.Agent != "",
			AgentName:   ro.Agent,
		})
	}
	return out, nil
}

// forceTickOccupants fires a forced co-located tick on each agent-NPC
// occupant so they perceive the closed-state within seconds (bypasses
// the cost guard). PCs poll their own state and don't get a forced
// tick. Best-effort: a tick failure is logged and the eviction
// continues.
func (app *App) forceTickOccupants(ctx context.Context, occupants []occupantInfo, structureID, vendorID, reason string) {
	for _, o := range occupants {
		if !o.IsAgentNPC {
			continue
		}
		actorID := o.ActorID
		bg := context.Background()
		go app.triggerImmediateTick(bg, actorID, reason, true, app.newScene(bg, structureID), vendorID)
	}
}

// runEvictionAssertiveAsk fires a one-shot LLM call on the vendor and
// returns a single line of speech for the room. Lenient default —
// timeout / error / empty reply returns empty string and the caller
// skips the broadcast (force-eject still fires at phase 3).
//
// No tools surfaced. The vendor's reply text is taken as-is and used
// as the synthetic npc_spoke. Capped at 200 chars defensively to
// avoid runaway prompt-injected text.
func (app *App) runEvictionAssertiveAsk(ctx context.Context, vendorAgent, vendorName, reason string, occupants []occupantInfo) string {
	if app.npcChatClient == nil {
		return ""
	}
	if len(occupants) == 0 {
		return ""
	}

	// Prompt: name the lingering occupants, lean on the announced reason,
	// ask in your own voice for a more pointed line. Personality rides
	// in via the per-agent context the API loads.
	var names []string
	for _, o := range occupants {
		names = append(names, o.DisplayName)
	}
	nameList := strings.Join(names, ", ")
	var prompt strings.Builder
	fmt.Fprintf(&prompt,
		"You announced you were closing for now (\"%s\"). %s %s still inside your shop and not heading out. Speak now to ask them to leave more firmly — in your own voice and personality, single line, no narration wrapping.",
		strings.TrimSpace(reason),
		nameList,
		pluralIs(len(occupants)),
	)

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reply, err := app.npcChatClient.sendChat(
		timeoutCtx,
		vendorAgent,
		prompt.String(),
		"", "", "",
		nil, // no tools — we want the reply text directly.
	)
	if err != nil {
		log.Printf("eviction assertive-ask %s: chat error: %v", vendorName, err)
		return ""
	}
	if reply == nil {
		return ""
	}
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return ""
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// pluralIs picks "is" / "are" by count.
func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// applyEvictionTirednessPenalty adds penalty to the actor's tiredness
// need value, capping at the schema-allowed max (24 for tiredness;
// matches design Q3). Best-effort — errors logged but eviction
// continues.
func (app *App) applyEvictionTirednessPenalty(ctx context.Context, actorID string, penalty int) {
	if penalty <= 0 {
		return
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO actor_need (actor_id, key, value)
		 VALUES ($1::uuid, 'tiredness', LEAST($2, 24))
		 ON CONFLICT (actor_id, key)
		 DO UPDATE SET value = LEAST(actor_need.value + EXCLUDED.value, 24)`,
		actorID, penalty,
	); err != nil {
		log.Printf("eviction tiredness penalty %s: %v", actorID, err)
	}
}
