package main

// PC presence cleanup — engine sweep that clears actor.inside_structure_id
// and actor.current_huddle_id for PCs whose last_pc_seen_at is past the
// configured threshold (ZBBS-HOME-267 introduced the sweep; ZBBS-HOME-274
// moved it off last_pc_input_at to a dedicated client-liveness column).
//
// Why the engine needs this. The PC WebSocket idle-timeout (ZBBS-082) only
// removes the client from Hub.clients — it does NOT touch the actor row.
// PC state-change endpoints (/pc/move, /pc/enter, /pc/say, etc.) write
// inside_structure_id and current_huddle_id when the player ACTS, but
// there is no symmetric "player went away" writer. So a PC who closes
// their browser, loses network, or crashes leaves a permanent footprint:
// inside_structure_id pinned to whatever structure they were in when they
// stopped acting, and current_huddle_id pinned to the active huddle there.
//
// Cost. NPCs in that huddle keep perceiving the PC on every reactor tick.
// The shared-VA LLM, seeing a co-located customer name in its perception,
// generates greetings / hospitality lines / small-talk addressed to a
// ghost. Observed in prod on 2026-05-11 (Wendy stuck in Ellis Residence
// for 12 days, Elizabeth → 9 identical "Good evening, Wendy..." lines)
// and 2026-05-12 (Jefferey stuck in the Inn for 9 hours, Hannah Boggs
// 4× identical "Good evening, gentlemen. How may I assist you tonight?"
// at ~40-minute idle-sweep cadence). Each repeat costs a real LLM call.
//
// Liveness signal — last_pc_seen_at, not last_pc_input_at. The original
// implementation gated the sweep on last_pc_input_at, the timestamp of
// the player's last explicit HTTP action. That column has two other
// consumers (pc_idle_sleep_minutes auto-bed; sleeping_until input-wake)
// that legitimately want "did the player just act?" semantics. Presence
// cleanup wants something different: "is this PC's client connected?"
// A PC who is mid-dwell (eating stew, resting at a tree, sitting
// through a long NPC reply) is alive and engaged but takes no input —
// the old column conflated those, so the sweep yanked actively-playing
// PCs out of their scenes mid-conversation (2026-05-12 Jefferey-at-Tavern
// repro). ZBBS-HOME-274 added a dedicated last_pc_seen_at column stamped
// by the /pc/me handler on every poll (the Godot client polls every
// 10s from the talk panel for the lifetime of the open browser tab,
// independent of player action). Closed tab → polls stop → seen-at
// ages → sweep correctly identifies the ghost. Active player → polls
// continue → seen-at stays fresh → never swept.
//
// Design — sweep, not disconnect hook. The WS upgrade path doesn't carry
// an actor_id (the connection is anonymous; only the session token is
// known), so wiring an explicit "actor X just disconnected" hook would
// require mapping token→actor at upgrade time and threading it through
// removeClient. That's a fine v2 — it would tighten the latency from
// "up to pc_presence_clear_minutes after the last /pc/me poll" to "as
// soon as the WS read deadline expires". For v1 the sweep alone is
// enough: bounded staleness at a few minutes, no new connection-state
// plumbing, self-healing through crash and reboot.
//
// Setting. pc_presence_clear_minutes (default 5, seeded by
// ZBBS-HOME-267 migration). Distinct from pc_idle_sleep_minutes (15):
// auto-bed is a lodger-only state-machine entry, presence-clear covers
// every PC and runs faster. 0 disables the sweep.
//
// Idempotency. setNPCInside and leaveHuddle are both safe to call on a
// PC already in the cleared state (early-return on equality), so a
// missed or doubled tick is harmless. The threshold guard means a PC
// whose client is polling /pc/me never enters the sweep's candidate set.

import (
	"context"
	"log"
	"time"
)

const defaultPCPresenceClearMinutes = 5

// dispatchPCPresenceCleanup runs once per server tick (60s cadence,
// registered in runServerTickOnce). Finds PCs whose last_pc_seen_at is
// older than the configured threshold and who still carry a non-NULL
// inside_structure_id or current_huddle_id, then clears both via the
// existing helpers so the npc_inside_changed broadcast, huddle
// conclusion, and structure occupancy refresh all fire normally.
//
// PCs are identified by llm_memory_agent IS NULL — same convention used
// elsewhere in the engine (PCs have a login_username, no agent slug).
// last_pc_seen_at IS NOT NULL guards against actors whose client has
// never polled /pc/me this lifetime (otherwise every freshly-seeded PC
// would be a candidate, which is wrong shape). The ZBBS-HOME-274
// migration backfills existing PCs to COALESCE(last_pc_input_at, NOW())
// so the deploy preserves the prior sweep behavior on day one.
func (app *App) dispatchPCPresenceCleanup(ctx context.Context) {
	threshold := app.loadNonNegativeIntSetting(ctx, "pc_presence_clear_minutes", defaultPCPresenceClearMinutes)
	if threshold == 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(threshold) * time.Minute)

	rows, err := app.DB.Query(ctx, `
		SELECT id::text,
		       COALESCE(display_name, login_username, '<unnamed>'),
		       COALESCE(inside_structure_id::text, ''),
		       COALESCE(current_huddle_id::text, ''),
		       EXTRACT(EPOCH FROM (NOW() - last_pc_seen_at))::int
		  FROM actor
		 WHERE llm_memory_agent IS NULL
		   AND last_pc_seen_at IS NOT NULL
		   AND last_pc_seen_at < $1
		   AND (inside_structure_id IS NOT NULL OR current_huddle_id IS NOT NULL)`,
		cutoff,
	)
	if err != nil {
		log.Printf("pc-presence-cleanup: query: %v", err)
		return
	}
	type ghost struct {
		ID        string
		Name      string
		Structure string
		Huddle    string
		IdleSec   int
	}
	var ghosts []ghost
	for rows.Next() {
		var g ghost
		if err := rows.Scan(&g.ID, &g.Name, &g.Structure, &g.Huddle, &g.IdleSec); err != nil {
			log.Printf("pc-presence-cleanup: scan: %v", err)
			continue
		}
		ghosts = append(ghosts, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("pc-presence-cleanup: rows: %v", err)
	}
	if len(ghosts) == 0 {
		return
	}

	for _, g := range ghosts {
		// Clear presence in the structure (and inside_room_id, which
		// setNPCInside also wipes). The function broadcasts
		// npc_inside_changed and re-evaluates structure occupancy for
		// any keeper still inside whose shop-open state depends on the
		// roster. Safe on PCs — the function queries by actor.id and
		// skips the canEnter/entry_policy checks when inside=false.
		if g.Structure != "" {
			app.setNPCInside(ctx, g.ID, false, "")
		}
		// Clear the huddle membership. leaveHuddle counts remaining
		// participants after the clear and concludes the huddle if it
		// just emptied, which is the right shape: the PC was the last
		// human-or-NPC tying the scene together, the huddle should
		// close out instead of lingering for the next arrival to
		// re-adopt.
		if g.Huddle != "" {
			app.leaveHuddle(ctx, g.ID)
		}
		log.Printf("pc-presence-cleanup: cleared %s (idle %ds, structure=%q huddle=%q)",
			g.Name, g.IdleSec, g.Structure, g.Huddle)
	}
}

// touchPCSeen stamps actor.last_pc_seen_at = NOW for the given PC.
// Called from /pc/me on every poll — the Godot client polls every 10s
// from the talk panel for the lifetime of the open browser tab, so a
// fresh last_pc_seen_at means the client is alive and connected.
//
// PC-only: gated on login_username IS NOT NULL inside the SQL so a
// stray non-PC actor ID (admin slip, future caller bug) can't write to
// the column on an NPC row. NPC rows have last_pc_seen_at = NULL and
// stay that way; the presence sweep filters on IS NOT NULL anyway.
//
// Errors logged but not propagated — failing to stamp the timestamp
// is a soft failure (worst case: the PC's session ages an extra cycle
// before its next /pc/me poll re-stamps; if all polls fail, the
// 5-minute sweep window eventually catches them, which is correct).
func (app *App) touchPCSeen(ctx context.Context, actorID string) {
	if actorID == "" {
		return
	}
	_, err := app.DB.Exec(ctx,
		`UPDATE actor
		    SET last_pc_seen_at = NOW()
		  WHERE id = $1::uuid
		    AND login_username IS NOT NULL`,
		actorID,
	)
	if err != nil {
		log.Printf("touchPCSeen(%s): %v", actorID, err)
	}
}
