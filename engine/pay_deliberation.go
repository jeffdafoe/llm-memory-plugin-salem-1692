package main

// Pay deliberation — held-transaction LLM tick on the recipient when a
// pay arrives that doesn't match a quote (underpayment or no quote on
// file for an item purchase).
//
// Origin: ZBBS-124 introduced structural quote tracking via scene_quote
// + speak.price; the engine now hard-rejects pays below quoted price.
// That fixed the silent-underpayment loophole but produced a flat
// "rejected" outcome that gives the recipient NPC no in-character
// voice. The mismatched-pay case is exactly when an NPC SHOULD speak —
// "I asked three coins, not two", "make it five and we have a deal",
// "I'd rather not sell at that price." Greenlit 2026-05-06 via mail
// a21f7148 from work.
//
// Flow:
//
//   pay arrives ──┬─ matches quote? ──→ existing fast path (commit)
//                 └─ mismatch / no quote ─→ runPayDeliberation
//                                               ├─ accept_pay      → commit
//                                               ├─ decline_pay     → rollback + speak reason
//                                               └─ counter_pay     → rollback + speak counter
//
// Synchronous: the buyer's pay() blocks for the deliberation latency
// (~1-2 s typical, 5 s hard timeout). Lenient default: timeout/error/
// malformed reply ⇒ accept, so a flaky LLM doesn't lock customers out
// of the village economy. The latency is acceptable for haggling
// flows; pure coin transfers (no item) skip deliberation entirely so
// tips and gifts stay snappy.
//
// Out of scope (per design / Jeff's greenlight):
//   - multi-turn haggling chains (each pay deliberation is independent)
//   - PC recipients (PCs accept everything; the LLM tick is for NPCs)
//   - pure coin transfers / generic gifts
//   - the talk panel pending-state UI (Godot client work, separate)

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// payDeliberationOutcome enumerates how the recipient resolved the
// held transaction. timeoutAccept records the lenient-default case
// (LLM call failed or response was malformed) so callers can log the
// fallback distinctly from a real explicit accept.
type payDeliberationOutcome string

const (
	payDeliberationAccept        payDeliberationOutcome = "accept"
	payDeliberationDecline       payDeliberationOutcome = "decline"
	payDeliberationCounter       payDeliberationOutcome = "counter"
	payDeliberationTimeoutAccept payDeliberationOutcome = "timeout-accept"
)

// payDeliberationDecision is the return shape of runPayDeliberation.
// Reason is populated for decline; NewAmount and Message are populated
// for counter. The other outcomes leave them zero / empty.
type payDeliberationDecision struct {
	Outcome   payDeliberationOutcome
	Reason    string
	NewAmount int
	Message   string
}

// payDeliberationTimeoutSeconds is the hard cap on the recipient's
// LLM round-trip. Past this, fall back to lenient accept. Tuned
// against typical sendChat latency: chat/send + a small tool-use
// LLM call comes back in ~1-2 s on the chosen models, so 5 s gives
// 2-3x headroom before the buyer's UI feels stuck.
const payDeliberationTimeoutSeconds = 5

// payDeliberationTools returns the focused tool set for a pay
// deliberation tick. The recipient sees ONLY these three options —
// no speak, no move_to, no done — so the LLM commits to a decision in
// one tool call. The reason / message strings on decline / counter
// become a synthetic speak event the caller emits, so the dialogue
// still surfaces in the room log without the recipient having to
// produce a separate speak() call.
func payDeliberationTools() []agentToolDef {
	return []agentToolDef{
		{
			Name:        "accept_pay",
			Description: "Accept the buyer's offer as stated. The transaction completes: their coins transfer to you, the goods leave your stock or the buyer consumes them at the source, and your inventory updates. Use when the offer is fair or you're willing to take it.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "decline_pay",
			Description: "Refuse the buyer's offer. The transaction does not complete: their coins are returned, your goods stay in stock. Your `reason` is spoken to the buyer in your voice as a normal speech act — they hear it and the room observes it. Use when the offer is too low to bother with, when you don't want to sell at all, or when something about the moment makes you balk.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "What you say to the buyer as you decline. In your voice, in character. The buyer and any onlookers hear this exactly as written — no narration wrapping, no quotes around it.",
					},
				},
				"required": []string{"reason"},
			},
		},
		{
			Name:        "counter_pay",
			Description: "Reject the offered amount but propose a different one. The current transaction does not complete (their coins are returned), and your message is spoken to the buyer naming your counter. The buyer can pay the new amount with a fresh pay() if they choose to accept. Use when you're willing to deal but not at THIS price.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"new_amount": map[string]interface{}{
						"type":        "integer",
						"description": "Your proposed total in coins for the same item and quantity the buyer offered for. Whole numbers only.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "What you say to the buyer naming the new price. In your voice, in character. The buyer and any onlookers hear this exactly as written — name the price plainly so the buyer knows what to pay if they want to retry.",
					},
				},
				"required": []string{"new_amount", "message"},
			},
		},
	}
}

// runPayDeliberation fires a synchronous LLM tick on the recipient
// with the focused tool set above. Returns a decision the caller can
// branch on. Never returns a usable error — every failure mode falls
// back to the lenient accept default so a flaky upstream doesn't lock
// the village economy. (Errors are logged for observability.)
//
// Arguments:
//
//   recipientAgent — llm_memory_agent name (e.g. "zbbs-john-ellis")
//   recipientName  — display name for the prompt ("John Ellis")
//   buyerName      — display name of the offeror
//   item           — item_kind being purchased
//   qty            — units the buyer wants
//   offered        — total coins the buyer is offering
//   quoted         — per-unit price the recipient previously quoted
//   hasQuote       — true when a quote is on file; false when the
//                    deliberation fires for the no-quote-on-file gate
//
// The prompt frames the offer in concrete terms and tells the LLM the
// only legal moves are accept_pay / decline_pay / counter_pay. The
// recipient's normal personality + role + memory ride along via the
// API's per-agent context load — no separate system prompt here.
func (app *App) runPayDeliberation(
	ctx context.Context,
	recipientAgent, recipientName, buyerName, item string,
	qty, offered, quoted int,
	hasQuote bool,
) payDeliberationDecision {
	if recipientAgent == "" {
		// PC recipient or non-agent NPC — no LLM to ask. Lenient default.
		return payDeliberationDecision{Outcome: payDeliberationAccept}
	}
	if app.npcChatClient == nil {
		log.Printf("pay-deliberation: chat client not initialized — defaulting to accept")
		return payDeliberationDecision{Outcome: payDeliberationTimeoutAccept}
	}

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "%s has placed an offer of %d coin(s) for %d %s.",
		buyerName, offered, qty, item)
	if hasQuote {
		required := quoted * qty
		fmt.Fprintf(&prompt, " You earlier quoted %d coin(s) per %s — for %d unit(s) that comes to %d.",
			quoted, item, qty, required)
		if offered < required {
			fmt.Fprintf(&prompt, " They are offering %d short of your quote.", required-offered)
		} else {
			// Defensive — caller should only invoke deliberation on a
			// real mismatch. Note it but still defer to the LLM.
			prompt.WriteString(" The offer meets or exceeds your quote.")
		}
	} else {
		fmt.Fprintf(&prompt, " You have not quoted a price for %s in this conversation.", item)
	}
	prompt.WriteString(" Decide now: call accept_pay to take the offer as stated, decline_pay with a spoken reason to refuse, or counter_pay with a new amount and a spoken message to propose your own price. The buyer is waiting on your reply — choose exactly one.")

	timeoutCtx, cancel := context.WithTimeout(ctx, payDeliberationTimeoutSeconds*time.Second)
	defer cancel()

	reply, err := app.npcChatClient.sendChat(
		timeoutCtx,
		recipientAgent,
		prompt.String(),
		"",                   // toolCallID — fresh perception, not a follow-up
		"",                   // sceneID — deliberation rides outside the cascade scene
		"",                   // sceneStructure — same
		payDeliberationTools(),
	)
	if err != nil {
		log.Printf("pay-deliberation: %s reply error (%v) — defaulting to accept", recipientName, err)
		return payDeliberationDecision{Outcome: payDeliberationTimeoutAccept}
	}
	if reply == nil || len(reply.ToolCalls) == 0 {
		log.Printf("pay-deliberation: %s returned no tool call — defaulting to accept", recipientName)
		return payDeliberationDecision{Outcome: payDeliberationTimeoutAccept}
	}

	// Take the first tool call; ignore any extras. The deliberation tools
	// have no `done` and the LLM gets one shot — additional calls would
	// be incoherent (you can't accept and decline the same offer).
	tc := reply.ToolCalls[0]
	switch tc.Name {
	case "accept_pay":
		return payDeliberationDecision{Outcome: payDeliberationAccept}

	case "decline_pay":
		reason, _ := tc.Input["reason"].(string)
		reason = strings.TrimSpace(reason)
		if reason == "" {
			// Malformed: no reason text. Default to a generic line so
			// the buyer still sees a refusal instead of a silent rollback.
			reason = "I'd rather not sell at that price."
		}
		return payDeliberationDecision{Outcome: payDeliberationDecline, Reason: reason}

	case "counter_pay":
		newAmount := coerceIntInput(tc.Input["new_amount"])
		message, _ := tc.Input["message"].(string)
		message = strings.TrimSpace(message)
		if newAmount <= 0 || message == "" {
			// Malformed counter — degrade to a decline rather than ship
			// a bogus counter price. Use a generic decline reason.
			log.Printf("pay-deliberation: %s emitted malformed counter (amount=%d msg=%q) — degrading to decline",
				recipientName, newAmount, message)
			return payDeliberationDecision{
				Outcome: payDeliberationDecline,
				Reason:  "That doesn't work for me.",
			}
		}
		return payDeliberationDecision{
			Outcome:   payDeliberationCounter,
			NewAmount: newAmount,
			Message:   message,
		}

	default:
		log.Printf("pay-deliberation: %s emitted unknown tool %q — defaulting to accept",
			recipientName, tc.Name)
		return payDeliberationDecision{Outcome: payDeliberationTimeoutAccept}
	}
}

// broadcastDeliberationSpeak emits an npc_spoke WS event for the
// recipient's deliberation reply (decline reason or counter message)
// so connected clients render the speech bubble in real time. Mirrors
// the speak commit's Hub.Broadcast in agent_tick.go but skips the
// extras (audit row write, co-located event ticks, stale-addressee
// checks) — the deliberation chat row already records the recipient's
// tool call, and triggering more co-located ticks during a held pay
// risks loops if a nearby NPC reacts to the speech with another pay.
//
// Carries structure_id when the recipient is inside a structure so
// the talk panel's room-scoped log catches it. World-view speech
// bubbles ignore that field and render every npc_spoke regardless.
//
// Errors looking up the recipient's structure are logged and the
// event is broadcast without it — better to surface the speech
// village-wide than to silently drop the bubble.
func (app *App) broadcastDeliberationSpeak(ctx context.Context, recipientID, recipientName, spokenText string) {
	if spokenText == "" {
		return
	}
	data := map[string]interface{}{
		"npc_id": recipientID,
		"name":   recipientName,
		"text":   spokenText,
		"at":     time.Now().UTC().Format(time.RFC3339),
	}
	var insideStructure string
	if err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(inside_structure_id::text, '') FROM actor WHERE id = $1`,
		recipientID,
	).Scan(&insideStructure); err != nil {
		log.Printf("pay-deliberation: lookup recipient %s structure: %v", recipientName, err)
	}
	if insideStructure != "" {
		data["structure_id"] = insideStructure
	}
	log.Printf("npc_spoke (deliberation): %s says %q", recipientName, spokenText)
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: data})
}
