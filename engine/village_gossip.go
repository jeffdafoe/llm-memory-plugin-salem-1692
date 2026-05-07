package main

// village_gossip — shared observations NPCs reference in speak.
//
// ZBBS-157. Companion to ZBBS-117 retained-concerns. Schema:
// migrations/ZBBS-157-village-gossip_up.sql.
//
// The perception builder calls visibleGossipLines() to render the
// "Around the village:" block. Filtered to exclude rows where the
// perceiver is the subject (you don't overhear gossip about
// yourself). Capped at the caller's limit (3 in production) so the
// section stays terse.
//
// v1 authoring: direct INSERT (admin / seed). chronicler tool
// integration deferred to a future ZBBS — schema is intentionally
// agnostic so any author can post.

import (
	"context"
	"log"
)

// visibleGossipLines returns up to `limit` recent unexpired gossip
// lines NOT about the perceiver. Newest first by authored_at.
//
// Best-effort: query errors logged + nil returned. Empty result is
// the no-section signal at the caller (perception suppresses the
// "Around the village:" block entirely when there's nothing to say).
func (app *App) visibleGossipLines(ctx context.Context, perceiverID string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	rows, err := app.DB.Query(ctx,
		`SELECT text
		   FROM village_gossip
		  WHERE (expires_at IS NULL OR expires_at > NOW())
		    AND (subject_actor_id IS NULL OR subject_actor_id::text != $1)
		  ORDER BY authored_at DESC, id DESC
		  LIMIT $2`,
		perceiverID, limit,
	)
	if err != nil {
		log.Printf("village_gossip: query for %s: %v", perceiverID, err)
		return nil
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			continue
		}
		lines = append(lines, "  "+text)
	}
	return lines
}
