package main

// Summon tool — an NPC asks the engine to fetch another villager.
//
// Post-ZBBS-107 the implementation is a multi-leg messenger errand
// (see summon_errand.go for the state machine). executeSummon is a
// thin shim that delegates to dispatchSummonErrand. summonsTargetingPerceiver
// stays here because it's read at perception-build time by agent_tick.go,
// and the row it reads (action_type='summon' in agent_action_log) is
// now written at delivery rather than at dispatch.
//
// summonsTargetingPerceiver is the perception-side reader unchanged
// from v1: it filters action_type='summon' rows for the target NPC's
// display name. The audit row is now written when the messenger
// arrives at the target (state messenger_at_target → messenger_returning,
// VA branch) rather than at dispatch — the perception fragment lands
// in the target's next tick after delivery.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

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

// executeSummon dispatches a summon errand (ZBBS-105/107). The
// previous implementation was a teleport: validated, woke the target,
// fired triggerImmediateTick. The new path inserts a summon_errand row
// and walks the summoner to the nearest summon_point — the messenger
// flow plays out from there via the arrival hook and errand ticker
// (see summon_errand.go for the full lifecycle).
//
// The audit row is NOT written here. It's written when the messenger
// actually delivers (state messenger_at_target → messenger_returning,
// VA branch). summonsTargetingPerceiver continues to read from
// agent_action_log unchanged.
func (app *App) executeSummon(ctx context.Context, summoner *agentNPCRow, req summonRequest) summonResult {
	return app.dispatchSummonErrand(ctx, summoner, req)
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
		log.Printf("summons-perception query for %s (%s): %v", perceiverID, perceiverDisplayName, err)
		return nil
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var summoner, location string
		var payload []byte
		var at time.Time
		if err := rows.Scan(&summoner, &payload, &at, &location); err != nil {
			log.Printf("summons-perception scan for %s (%s): %v", perceiverID, perceiverDisplayName, err)
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
	if err := rows.Err(); err != nil {
		log.Printf("summons-perception iter for %s (%s): %v", perceiverID, perceiverDisplayName, err)
	}

	// Diagnostic: when the main query returned 0 rows AND this perceiver
	// is currently expected to be the target of a summon, run a parallel
	// loose-filter probe to distinguish "no summon row exists" from "row
	// exists but the main query missed it." If the probe finds a row that
	// the main query did not, the visibility / filter logic is wrong.
	//
	// ZBBS-140: gated on `summon_errand` existence so the diagnostic only
	// fires for perceivers actually owed a summon. Earlier, every NPC
	// tick whose main query returned 0 logged a probe — i.e. every tick
	// for an NPC nobody had recently summoned. ~9 lines/hour of pure
	// noise. Now the gate matches the intent: surface only the cases
	// where a summon_errand SHOULD have produced an audit row visible
	// to this perceiver but didn't.
	//
	// "Recently relevant" = errand still active (state NOT IN done/failed)
	// OR resolved within the last 5 minutes (covers the audit-row-just-
	// committed-but-not-yet-visible window the original bug captured).
	//
	// Lifetime: keep until the recurring "Prudence didn't come" issue has
	// been root-caused, then remove the diagnostic block entirely.
	if len(lines) == 0 {
		var expectsSummon bool
		if err := app.DB.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM summon_errand
			     WHERE LOWER(target_name) = LOWER($1)
			       AND (
			           state NOT IN ('done', 'failed')
			           OR updated_at > NOW() - INTERVAL '5 minutes'
			       )
			)
		`, perceiverDisplayName).Scan(&expectsSummon); err != nil {
			log.Printf("summons-perception expectation check for %s (%s): %v", perceiverID, perceiverDisplayName, err)
		} else if expectsSummon {
			var probeCount int
			var probeOccurredAt sql.NullTime
			var probeResult sql.NullString
			var probeTarget sql.NullString
			if err := app.DB.QueryRow(ctx, `
				SELECT COUNT(*),
				       MAX(occurred_at),
				       MAX(result),
				       MAX(payload->>'target')
				  FROM agent_action_log
				 WHERE action_type = 'summon'
				   AND payload->>'target' = $1
				   AND occurred_at > NOW() - INTERVAL '15 minutes'
			`, perceiverDisplayName).Scan(&probeCount, &probeOccurredAt, &probeResult, &probeTarget); err != nil {
				log.Printf("summons-perception probe for %s (%s): %v", perceiverID, perceiverDisplayName, err)
			} else {
				log.Printf("summons-perception empty for %s (%s): probe found %d row(s) target=%q result=%q latest=%v",
					perceiverID, perceiverDisplayName,
					probeCount, probeTarget.String, probeResult.String, probeOccurredAt.Time)
			}
		}
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

// summonFailedForPerceiver returns recent summon_failed audit rows
// for this NPC's own dispatched-then-refused summons that they
// haven't yet acted on. Used by the perception build to surface
// "Your messenger returned without finding X" so the summoner can
// react in their next tick (apologize to a customer who was waiting,
// try a different villager, give up gracefully, etc.).
//
// Same shape and "fade after response" logic as summonsTargetingPerceiver
// — once the summoner has committed any move_to / take_break / speak
// after the failed summon landed, the line drops from perception so
// the model isn't reminded of the same failure forever.
func (app *App) summonFailedForPerceiver(ctx context.Context, perceiverID string) []string {
	rows, err := app.DB.Query(ctx, `
		WITH last_response AS (
		    SELECT MAX(occurred_at) AS at
		      FROM agent_action_log
		     WHERE actor_id = $1
		       AND action_type IN ('move_to', 'take_break', 'speak')
		       AND occurred_at > NOW() - INTERVAL '15 minutes'
		)
		SELECT al.payload, al.occurred_at
		  FROM agent_action_log al
		 WHERE al.actor_id = $1
		   AND al.action_type = 'summon_failed'
		   AND al.occurred_at > NOW() - INTERVAL '15 minutes'
		   AND al.occurred_at > COALESCE((SELECT at FROM last_response), '-infinity'::timestamptz)
		 ORDER BY al.occurred_at DESC
		 LIMIT 3
	`, perceiverID)
	if err != nil {
		log.Printf("summon-failed perception query for %s: %v", perceiverID, err)
		return nil
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var payload []byte
		var at time.Time
		if err := rows.Scan(&payload, &at); err != nil {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(payload, &m); err != nil {
			continue
		}
		target, _ := m["target"].(string)
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		reason, _ := m["reason"].(string)
		reason = strings.TrimSpace(reason)
		var line string
		if reason != "" {
			line = fmt.Sprintf("  Your messenger returned: %s is not to be found. Your message was: %q.",
				target, reason)
		} else {
			line = fmt.Sprintf("  Your messenger returned: %s is not to be found.", target)
		}
		lines = append(lines, line)
	}
	// Reverse so oldest-failed reads first.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}
