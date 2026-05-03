package main

// Object content (notice boards and other state-driven content surfaces).
//
// Generic primitive: an asset state can declare a content capacity via an
// asset_state_tag of the form "content-capacity-N". When a placement's
// current_state advances to a state with capacity > 0, AND the placement
// carries a per-instance opt-in tag for content generation, the engine
// asks the chronicler for prose and stores it on the instance. Cycling
// back to a state with no capacity tag clears prior content.
//
// Today's only opt-in tag is "noticeboard_content"; the chronicler is the
// only writer. Future surfaces (wanted posters, market signage, etc.)
// can reuse the column by introducing a new opt-in tag and pointing it
// at a different writer in onObjectStateAdvanced.
//
// Hook site: advanceBehavior, after the rotation flip's UPDATE / WS
// broadcast. The chronicler call runs on its own goroutine so a slow
// LLM round-trip doesn't stall the crier's onward walk.
//
// Storage: village_object.content_text + content_posted_at. Single blob
// per instance; rotation replaces it. Multi-blob history would be a
// separate table later if a use case justifies it.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// objectContentCapacityTagPrefix is the asset_state_tag prefix the
	// engine reads to determine how much content a state should hold.
	// Suffix is the integer line count, parsed at lookup time.
	objectContentCapacityTagPrefix = "content-capacity-"

	// tagNoticeBoardInstance is the per-instance village_object_tag that
	// opts a placement into noticeboard content generation. Distinct from
	// the asset-state-level "notice-board" tag (ZBBS-025) used by the
	// crier to know which boards to rotate. A placement can carry
	// "noticeboard_content" without rotation, or rotation without content
	// generation; both must be present for the chronicler to write.
	// Underscore form follows the existing allowedObjectTags convention
	// (cf. summon_point) and disambiguates from the hyphenated asset-
	// state-level tag.
	tagNoticeBoardInstance = "noticeboard_content"

	// noticeContentCallTimeout bounds the chronicler one-shot. The chat
	// client itself uses a 90s timeout (LLM call); this caps the goroutine
	// so a stuck call doesn't leak past one rotation cycle.
	noticeContentCallTimeout = 120 * time.Second

	// noticeMaxLineLen caps each post-clamp line. Defends NPC perception
	// and the editor preview against a verbose chronicler reply that
	// ignores the "short" instruction in the prompt.
	noticeMaxLineLen = 240
)

// objectContentCapacityForState returns the line capacity declared by a
// state's content-capacity-N tag, or 0 when the state has no such tag.
// Treats duplicate or malformed capacity tags as data errors — silent
// max-of-multiple-values would mask a bad seed/editor write at the cost
// of generating against the wrong shape.
func (app *App) objectContentCapacityForState(ctx context.Context, assetID, state string) (int, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT t.tag
		 FROM asset_state s
		 JOIN asset_state_tag t ON t.state_id = s.id
		 WHERE s.asset_id = $1::uuid AND s.state = $2
		   AND t.tag LIKE $3`,
		assetID, state, objectContentCapacityTagPrefix+"%")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	caps := []int{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return 0, err
		}
		suffix := strings.TrimPrefix(tag, objectContentCapacityTagPrefix)
		n, err := strconv.Atoi(suffix)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid content capacity tag %q on %s/%s", tag, assetID, state)
		}
		caps = append(caps, n)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(caps) == 0 {
		return 0, nil
	}
	if len(caps) > 1 {
		return 0, fmt.Errorf("multiple content-capacity tags on %s/%s: %v", assetID, state, caps)
	}
	return caps[0], nil
}

// objectHasInstanceTag reports whether the placement carries the given
// village_object_tag. Used to gate content generation on the per-instance
// opt-in; the asset-level rotation tagging is independent.
func (app *App) objectHasInstanceTag(ctx context.Context, objectID, tag string) (bool, error) {
	var ok bool
	err := app.DB.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM village_object_tag
		  WHERE object_id = $1::uuid AND tag = $2)`,
		objectID, tag,
	).Scan(&ok)
	return ok, err
}

// onObjectStateAdvanced is the post-flip hook. Called from advanceBehavior
// after the crier rotates a placement to its next variant. Decides whether
// to (re)generate content, clear it, or do nothing based on the new state's
// capacity and the instance's opt-in tags.
//
// Today only the "noticeboard_content" instance tag dispatches generation;
// future surfaces add their own (objectID, tag) → writer mapping here.
//
// Runs synchronously to read state but spawns a goroutine for the LLM
// call. The crier's onward walk doesn't wait on chronicler latency. The
// goroutine threads newState forward so the conditional UPDATE in
// saveObjectContent rejects a stale write when the crier has flipped
// the board again before the chronicler responded.
func (app *App) onObjectStateAdvanced(ctx context.Context, objectID, newState string) {
	var assetID, displayName, priorContent string
	if err := app.DB.QueryRow(ctx,
		`SELECT asset_id::text, COALESCE(display_name, ''), COALESCE(content_text, '')
		   FROM village_object WHERE id = $1::uuid`, objectID,
	).Scan(&assetID, &displayName, &priorContent); err != nil {
		log.Printf("object_content: read placement %s: %v", objectID, err)
		return
	}

	capacity, err := app.objectContentCapacityForState(ctx, assetID, newState)
	if err != nil {
		log.Printf("object_content: capacity lookup %s/%s: %v", assetID, newState, err)
		return
	}

	// Cycled to an empty state (or one without content semantics) — clear
	// any prior content. Non-noticeboard placements never had content set,
	// so this is a no-op for them.
	if capacity <= 0 {
		if priorContent != "" {
			app.clearObjectContent(ctx, objectID)
		}
		return
	}

	// Capacity > 0 — only write when the placement opted in. An admin can
	// keep a noticeboard rotation purely visual by leaving the instance
	// tag off.
	hasNoticeTag, err := app.objectHasInstanceTag(ctx, objectID, tagNoticeBoardInstance)
	if err != nil {
		log.Printf("object_content: instance tag lookup %s: %v", objectID, err)
		return
	}
	if !hasNoticeTag {
		return
	}

	// Resolve a human-readable location label. Display name when set,
	// otherwise fall back to a generic descriptor so logs trace cleanly.
	location := displayName
	if location == "" {
		location = "the noticeboard"
	}

	go app.generateAndSaveBoardContent(objectID, newState, location, capacity, priorContent)
}

// generateAndSaveBoardContent runs the chronicler one-shot and persists
// the result. Goroutine entry point — never returns errors to the
// caller; logs and moves on. A failed generation leaves prior content
// in place (clearing on transition to capacity 0 happens elsewhere), so
// a transient chronicler outage just means the board stays "stale" for
// one rotation cycle.
//
// expectedState is the state the board was in at dispatch. saveObjectContent
// rejects the write if current_state has changed since — defends against
// a stale LLM reply landing on a board the crier has since flipped.
func (app *App) generateAndSaveBoardContent(objectID, expectedState, location string, capacity int, prior string) {
	ctx, cancel := context.WithTimeout(context.Background(), noticeContentCallTimeout)
	defer cancel()

	body, err := app.callChroniclerForBoard(ctx, location, capacity, prior)
	if err != nil {
		log.Printf("object_content: chronicler call for %s: %v", objectID, err)
		return
	}
	body = clampNoticeContent(body, capacity, noticeMaxLineLen)
	if body == "" {
		log.Printf("object_content: chronicler returned empty body for %s — leaving prior content in place", objectID)
		return
	}
	if err := app.saveObjectContent(ctx, objectID, expectedState, body); err != nil {
		// Stale-state rejections are expected when the crier flips the
		// same board twice before the LLM responds — log at info, not
		// error, so it doesn't read as a failure.
		log.Printf("object_content: save %s: %v", objectID, err)
		return
	}
	log.Printf("object_content: posted %d-line notice for %s (%s)", capacity, objectID, location)
}

// clampNoticeContent normalizes the chronicler's reply to the contract
// declared by the state's capacity tag. Trims surrounding whitespace,
// drops empty lines, caps the count to maxLines, and truncates each
// surviving line to maxLineLen runes (rune-aware to avoid splitting a
// multi-byte UTF-8 sequence). Returns "" when the trimmed reply has no
// content.
func clampNoticeContent(body string, maxLines, maxLineLen int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	out := make([]string, 0, maxLines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if maxLineLen > 0 {
			runes := []rune(trimmed)
			if len(runes) > maxLineLen {
				trimmed = string(runes[:maxLineLen])
			}
		}
		out = append(out, trimmed)
		if len(out) == maxLines {
			break
		}
	}
	return strings.Join(out, "\n")
}

// callChroniclerForBoard issues a single wait=true chat to the chronicler
// agent with no tools offered. The chronicler returns plain prose which
// becomes the notice text verbatim. Distinct from fireChronicler — that
// one runs the directorial harness (set_environment / record_event /
// recall / attend_to / done); this one is a focused content gen.
//
// The chronicler agent already has the village context loaded (recent
// events, atmosphere, NPC list) so the prompt only carries what's
// specific to this board: location, capacity, and the prior text being
// replaced.
//
// sceneID: fresh per call so admin chat-grouping renders these as their
// own one-row scenes rather than folding into whatever scene happens to
// be in flight.
func (app *App) callChroniclerForBoard(ctx context.Context, location string, capacity int, prior string) (string, error) {
	if app.npcChatClient == nil {
		return "", errors.New("npc chat client is not configured")
	}

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Write fresh notice text for the village noticeboard at %s.\n", location)
	noun := "notice"
	if capacity > 1 {
		noun = "notices"
	}
	fmt.Fprintf(&prompt, "The board has space for %d short %s.\n", capacity, noun)
	if prior != "" {
		prompt.WriteString("\nThe previous text on the board, now coming down, read:\n")
		prompt.WriteString(prior)
		prompt.WriteString("\n")
	}
	prompt.WriteString("\nReply with the notice text only — no preamble, no quotation marks, no headings. ")
	prompt.WriteString("One notice per line. Period-correct prose for Salem in 1692. ")
	prompt.WriteString("Draw from village events you remember when the moment calls for it; otherwise write atmosphere appropriate to a small Puritan village (announcements, sermons, lost-and-found, market notices, ordinances).")

	sceneID := newUUIDv7()
	reply, err := app.npcChatClient.sendChat(ctx, chroniclerAgent, prompt.String(), "", sceneID, nil)
	if err != nil {
		return "", err
	}
	if reply == nil {
		return "", errors.New("chronicler returned no reply")
	}
	return reply.Text, nil
}

// saveObjectContent writes content_text + content_posted_at and broadcasts
// the change. The UPDATE is conditional on (a) the placement's
// current_state still matching the state at dispatch and (b) the
// noticeboard_content instance tag still being attached — either changing
// in flight means a stale chronicler reply for a now-different board,
// and silently dropping the write is the right move. RowsAffected==0 is
// reported as an error so generateAndSaveBoardContent can log it; not a
// failure mode, just a race outcome.
//
// Timestamps come from NOW() inside the UPDATE rather than time.Now() in
// Go, so the broadcast value matches the stored value exactly even if
// the app server clock and the DB clock differ.
func (app *App) saveObjectContent(ctx context.Context, objectID, expectedState, body string) error {
	var postedAt time.Time
	err := app.DB.QueryRow(ctx,
		`UPDATE village_object o
		    SET content_text = $3,
		        content_posted_at = NOW()
		  WHERE o.id = $1::uuid
		    AND o.current_state = $2
		    AND EXISTS (
		        SELECT 1 FROM village_object_tag t
		        WHERE t.object_id = o.id AND t.tag = $4
		    )
		  RETURNING content_posted_at`,
		objectID, expectedState, body, tagNoticeBoardInstance,
	).Scan(&postedAt)
	if err != nil {
		// pgx returns ErrNoRows when the UPDATE matches zero rows.
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("stale state — board %s no longer in %q or no longer tagged", objectID, expectedState)
		}
		return err
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "object_content_changed",
		Data: map[string]any{
			"id":                objectID,
			"content_text":      body,
			"content_posted_at": postedAt.UTC().Format(time.RFC3339),
		},
	})
	return nil
}

// clearObjectContent NULLs the content fields and broadcasts the change.
// Called when a placement cycles back to a no-capacity state (variant-1
// for the seeded noticeboard) so the prior message visibly comes down.
//
// The UPDATE is gated on the row still having content — calling this
// against a row whose content is already NULL produces no DB write and
// no broadcast, so a redundant clear (e.g. a duplicate state transition)
// stays quiet on the wire.
func (app *App) clearObjectContent(ctx context.Context, objectID string) {
	res, err := app.DB.Exec(ctx,
		`UPDATE village_object
		    SET content_text = NULL,
		        content_posted_at = NULL
		  WHERE id = $1::uuid
		    AND (content_text IS NOT NULL OR content_posted_at IS NOT NULL)`,
		objectID)
	if err != nil {
		log.Printf("object_content: clear %s: %v", objectID, err)
		return
	}
	if res.RowsAffected() == 0 {
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "object_content_changed",
		Data: map[string]any{
			"id":                objectID,
			"content_text":      nil,
			"content_posted_at": nil,
		},
	})
}

// noticeBoardLineForLoiterer returns a perception line for an NPC parked
// at a noticeboard, surfacing the board's current content so the LLM has
// a chance to react ("Goodman Ezra reads the notice and …"). Returns ""
// when the structure isn't a noticeboard, isn't tagged, or has no
// content posted.
func (app *App) noticeBoardLineForLoiterer(ctx context.Context, structureID string) string {
	var content string
	err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.content_text, '')
		   FROM village_object o
		   JOIN village_object_tag t ON t.object_id = o.id AND t.tag = $2
		  WHERE o.id = $1::uuid`,
		structureID, tagNoticeBoardInstance,
	).Scan(&content)
	if err != nil || content == "" {
		return ""
	}
	return "Posted on the board: " + content
}
