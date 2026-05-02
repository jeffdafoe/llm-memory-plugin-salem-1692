package main

// Summon tool — an NPC asks the engine to fetch another villager. The
// world-side framing is a child or apprentice sent with a message, or
// hollering over the fence. Mechanically:
//
//   1. Validate the target exists, isn't the summoner, and isn't already
//      co-located in the same huddle (no point summoning the person
//      sitting next to you).
//   2. Per-pair cooldown — same summoner re-summoning the same target
//      within summonCooldown is rejected, so a model that decides to
//      "send another messenger" every tick doesn't loop. The stamp lives
//      in the audit log itself; we look back rather than carry a
//      side-table.
//   3. Clear the target's agent_override_until and break_until and null
//      their last_agent_tick_at so they're free to react now rather
//      than after their current scheduled commitment.
//   4. Fire triggerImmediateTick on the target with a "summoned" reason.
//      That goroutine builds the target's perception, which sees the
//      pending summons via summonsTargeting() and hands the model a
//      decision: walk over, send a verbal refusal once they're there, or
//      ignore.
//
// The audit row + room_event broadcast happen back in executeAgentCommit
// (the universal write path) so the narration mirrors every other commit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// summonCooldown bounds the per-(summoner, target) re-summon rate. The
// LLM sometimes "tries again" when its first call didn't produce an
// arrival within the same tick — without this guard it'll fire summon
// every iteration of its own harness loop. Three minutes is enough
// breathing room that an honest follow-up after a failed walk is fine.
const summonCooldown = 3 * time.Minute

type summonResult struct {
	Result string // "ok" | "rejected" | "failed"
	Err    string // human-readable, empty when ok
	// Resolved fields used for narration / audit / target tick.
	TargetID          string
	TargetDisplayName string
}

type summonRequest struct {
	TargetName string
	Reason     string
}

// executeSummon validates the request, applies cooldown, wakes the
// target. The audit row is written by the dispatcher; we only return
// the resolution outcome.
func (app *App) executeSummon(ctx context.Context, summoner *agentNPCRow, req summonRequest) summonResult {
	targetName := strings.TrimSpace(req.TargetName)
	if targetName == "" {
		return summonResult{Result: "rejected", Err: "missing target"}
	}

	// Look up the target by display name.
	var targetID, targetDisplayName string
	var targetHuddle, targetUsername *string
	err := app.DB.QueryRow(ctx,
		`SELECT id, display_name, current_huddle_id::text, login_username
		   FROM actor
		  WHERE LOWER(display_name) = LOWER($1)
		  LIMIT 1`,
		targetName,
	).Scan(&targetID, &targetDisplayName, &targetHuddle, &targetUsername)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return summonResult{Result: "rejected", Err: fmt.Sprintf("no villager named %q", targetName)}
		}
		return summonResult{Result: "failed", Err: fmt.Sprintf("look up target: %v", err)}
	}

	if targetID == summoner.ID {
		return summonResult{Result: "rejected", Err: "cannot summon yourself"}
	}

	// PCs can't be summoned by the engine — they have no LLM tool surface
	// to perceive the request. The model can speak to a PC if they're
	// nearby, but summoning a player is meaningless.
	if targetUsername != nil && *targetUsername != "" {
		return summonResult{Result: "rejected", Err: fmt.Sprintf("%s is a person, not a villager you can send for", targetDisplayName)}
	}

	// Co-located check. If the target is in the summoner's current
	// huddle, they're already here — speak to them instead. The summoner
	// row doesn't carry current_huddle_id (agentNPCRow keeps the load
	// minimal), so look it up directly.
	var summonerHuddle *string
	_ = app.DB.QueryRow(ctx,
		`SELECT current_huddle_id::text FROM actor WHERE id = $1`,
		summoner.ID,
	).Scan(&summonerHuddle)
	if summonerHuddle != nil && targetHuddle != nil && *summonerHuddle == *targetHuddle {
		return summonResult{Result: "rejected", Err: fmt.Sprintf("%s is already here with you", targetDisplayName)}
	}

	// Cooldown: same summoner re-summoning the same target within
	// summonCooldown is rejected. Reads the audit log directly.
	var recentAt *time.Time
	if err := app.DB.QueryRow(ctx,
		`SELECT MAX(occurred_at) FROM agent_action_log
		  WHERE actor_id = $1 AND action_type = 'summon'
		    AND result = 'ok'
		    AND payload->>'target' = $2
		    AND occurred_at > NOW() - $3::interval`,
		summoner.ID, targetDisplayName,
		fmt.Sprintf("%d seconds", int(summonCooldown.Seconds())),
	).Scan(&recentAt); err == nil && recentAt != nil {
		return summonResult{Result: "rejected", Err: fmt.Sprintf("a messenger is still on their way to %s", targetDisplayName)}
	}

	// Wake the target. Clearing override/break lets them respond now
	// instead of after their current scheduled commitment; nulling the
	// tick stamp removes the cost-guard floor for the upcoming
	// triggerImmediateTick. agent_override_until is cleared even if the
	// target is mid-walk — they can re-decide what to do on perception.
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor
		    SET agent_override_until = NULL,
		        break_until = NULL,
		        last_agent_tick_at = NULL
		  WHERE id = $1`,
		targetID,
	); err != nil {
		return summonResult{Result: "failed", Err: fmt.Sprintf("wake target: %v", err)}
	}

	return summonResult{
		Result:            "ok",
		TargetID:          targetID,
		TargetDisplayName: targetDisplayName,
	}
}

// summonsTargetingPerceiver returns recent summons addressed to this
// NPC that haven't been resolved yet. A summons is unresolved when the
// target hasn't committed any move_to, take_break, or speak after the
// summons landed — those are the three ways an NPC can answer the
// fetch (come, decline by closing, or yell back). Capped to the most
// recent N within summonsLookback so a long backlog doesn't crowd the
// perception.
//
// Each entry is one perception line; the caller wraps with a section
// header.
func (app *App) summonsTargetingPerceiver(ctx context.Context, perceiverID, perceiverDisplayName string) []string {
	rows, err := app.DB.Query(ctx, `
		WITH last_response AS (
		    SELECT MAX(occurred_at) AS at
		      FROM agent_action_log
		     WHERE actor_id = $1
		       AND action_type IN ('move_to', 'take_break', 'speak')
		       AND occurred_at > NOW() - INTERVAL '15 minutes'
		)
		SELECT al.speaker_name, al.payload, al.occurred_at,
		       COALESCE(o.display_name, a.name) AS summoner_location
		  FROM agent_action_log al
		  LEFT JOIN actor ac ON ac.id = al.actor_id
		  LEFT JOIN village_object o ON o.id = ac.inside_structure_id
		  LEFT JOIN asset a ON a.id = o.asset_id
		 WHERE al.action_type = 'summon'
		   AND al.result = 'ok'
		   AND al.payload->>'target' = $2
		   AND al.occurred_at > NOW() - INTERVAL '15 minutes'
		   AND al.occurred_at > COALESCE((SELECT at FROM last_response), '-infinity'::timestamptz)
		 ORDER BY al.occurred_at DESC
		 LIMIT 3
	`, perceiverID, perceiverDisplayName)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var summoner, location string
		var payload []byte
		var at time.Time
		if err := rows.Scan(&summoner, &payload, &at, &location); err != nil {
			continue
		}
		reason := decodeSummonReason(payload)
		locationPhrase := "the open village"
		if strings.TrimSpace(location) != "" {
			locationPhrase = "the " + location
		}
		var line string
		if reason != "" {
			line = fmt.Sprintf("  A messenger has come from %s at %s. They say: %q",
				summoner, locationPhrase, reason)
		} else {
			line = fmt.Sprintf("  A messenger has come from %s at %s, asking you to come.",
				summoner, locationPhrase)
		}
		lines = append(lines, line)
	}
	// Reverse so oldest summons reads first — same chronological feel as
	// the recent-activity block.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// decodeSummonReason pulls the optional reason field from a summon
// payload. Defensive against a missing or non-string entry — the
// perception falls back to a generic line.
func decodeSummonReason(payload []byte) string {
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return ""
	}
	s, _ := m["reason"].(string)
	return strings.TrimSpace(s)
}
