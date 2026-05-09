package main

// Town Crier announcement reading — content layer for the rotation
// behavior. ZBBS-156. Schema: migrations/ZBBS-156-town-crier-announcement_up.sql.
//
// At each notice-board arrival on the crier's rotation route, the
// engine claims the oldest unexpired announcement with posted_count <
// max_posts, increments posted_count atomically, and broadcasts the
// text as the crier's npc_spoke. No LLM call — the announcement text
// is read verbatim. Future ZBBS could add an LLM stylization pass
// (e.g., chronicler authors the body, crier delivers in stylized
// voice), but v1 keeps the pipeline simple and observable.
//
// Authoring path is intentionally agnostic — anyone with INSERT on
// the table can post. ZBBS-156 shipped with seed-only authoring; the
// chronicler-driven record_announcement authoring path was removed in
// ZBBS-WORK-202 along with the rest of the chronicler narrative tools
// beyond set_environment. The crier reads through the seeded rows
// (and any future admin-authored rows) and goes silent when none
// remain unexpired with posted_count < max_posts.

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
)

// cryNextAnnouncement is the per-arrival hook called from
// advanceBehavior when the route label is "town_crier". Picks one
// announcement, increments its posted_count atomically, broadcasts
// as crier speech.
//
// Best-effort: errors logged, not surfaced. The route continues to
// the next stop regardless. Empty table / all retired → silent no-op.
func (app *App) cryNextAnnouncement(ctx context.Context, crierID string) {
	// Atomic claim: select + increment in one statement so two
	// concurrent crier arrivals (unlikely but possible during dev or
	// rapid rotation testing) don't both bump the same row past
	// max_posts.
	var (
		announcementID int64
		text           string
	)
	err := app.DB.QueryRow(ctx,
		`UPDATE town_crier_announcement
		    SET posted_count = posted_count + 1
		  WHERE id = (
		      SELECT id FROM town_crier_announcement
		       WHERE posted_count < max_posts
		         AND (expires_at IS NULL OR expires_at > NOW())
		       ORDER BY authored_at ASC, id ASC
		       LIMIT 1
		       FOR UPDATE SKIP LOCKED
		  )
		 RETURNING id, text`,
	).Scan(&announcementID, &text)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("town_crier: pick announcement: %v", err)
		}
		return
	}

	// Resolve the crier's display name + position for the speak
	// broadcast. Single round-trip; matches the data shape used by
	// other npc_spoke emitters (take_break excuse, deliberation
	// speak).
	var (
		displayName       string
		insideStructureID string
		x, y              float64
	)
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name,
		        COALESCE(inside_structure_id::text, ''),
		        current_x, current_y
		   FROM actor WHERE id = $1`,
		crierID,
	).Scan(&displayName, &insideStructureID, &x, &y); err != nil {
		log.Printf("town_crier: load crier %s: %v", crierID, err)
		return
	}

	// Compose the npc_spoke broadcast. structure_id is included only
	// when the crier is inside one — most stops are outdoors at
	// notice-board posts, where listeners scope by world position.
	spoke := map[string]any{
		"npc_id": crierID,
		"name":   displayName,
		"text":   text,
		"x":      x,
		"y":      y,
	}
	if insideStructureID != "" {
		spoke["structure_id"] = insideStructureID
	}
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: spoke})

	// Engine log mirrors agent_tick.go's npc_spoke log so the cry is
	// visible in journalctl alongside other speech events.
	log.Printf("npc_spoke: %s says %q (town_crier announcement_id=%d)",
		displayName, text, announcementID)

	// Audit row in agent_action_log so the announcement is searchable
	// later. action_type='speak' to share the existing speak filter
	// in admin queries; payload carries the announcement metadata.
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result)
		 VALUES ($1::uuid, $2, 'engine', 'speak', $3::jsonb, 'ok')`,
		crierID, displayName,
		fmt.Sprintf(`{"text":%q,"kind":"town_crier","announcement_id":%d}`, text, announcementID),
	); err != nil {
		log.Printf("town_crier: audit insert: %v", err)
	}
}

