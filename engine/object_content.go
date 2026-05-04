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
	"encoding/json"
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
//
// ZBBS-117: the chronicler may also return concerns (named-entity facts)
// alongside the prose. Concerns are persisted after a successful save
// and tagged with the new content_generation so they share the prose's
// lifetime. A concern targeting an unknown name is logged and dropped —
// the prose still posts.
func (app *App) generateAndSaveBoardContent(objectID, expectedState, location string, capacity int, prior string) {
	ctx, cancel := context.WithTimeout(context.Background(), noticeContentCallTimeout)
	defer cancel()

	reply, err := app.callChroniclerForBoard(ctx, location, capacity, prior)
	if err != nil {
		log.Printf("object_content: chronicler call for %s: %v", objectID, err)
		return
	}
	body := clampNoticeContent(reply.Notice, capacity, noticeMaxLineLen)
	if body == "" {
		log.Printf("object_content: chronicler returned empty body for %s — leaving prior content in place", objectID)
		return
	}
	newGen, err := app.saveObjectContent(ctx, objectID, expectedState, body)
	if err != nil {
		// Stale-state rejections are expected when the crier flips the
		// same board twice before the LLM responds — log at info, not
		// error, so it doesn't read as a failure.
		log.Printf("object_content: save %s: %v", objectID, err)
		return
	}

	// Persist concerns. Each is best-effort — a single bad target name
	// shouldn't suppress the others. Tagged with newGen so the perception
	// join finds them this generation and stops finding them on the next
	// rotation.
	for _, c := range reply.Concerns {
		kind, id, resolveErr := app.resolveTargetByName(ctx, c.Target)
		if resolveErr != nil {
			log.Printf("object_content: concern target %q for %s unresolved: %v", c.Target, objectID, resolveErr)
			continue
		}
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if err := app.recordConcern(ctx,
			concernSourceVillageObjectContent, objectID, newGen,
			kind, id, text,
		); err != nil {
			log.Printf("object_content: record concern %q→%s for %s: %v", c.Target, kind, objectID, err)
			continue
		}
	}

	log.Printf("object_content: posted %d-line notice for %s (%s) gen=%d, %d concerns", capacity, objectID, location, newGen, len(reply.Concerns))
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

// chroniclerBoardReply is the parsed output of one noticeboard authoring
// call. Notice is the prose to post; Concerns is the list of named-entity
// facts the chronicler attached (ZBBS-117). The reply is parsed from a
// JSON envelope; on parse failure the whole reply text is treated as
// raw notice prose with zero concerns (graceful degradation).
type chroniclerBoardReply struct {
	Notice   string                  `json:"notice"`
	Concerns []chroniclerBoardConcern `json:"concerns"`
}

type chroniclerBoardConcern struct {
	Target string `json:"target"` // structure or actor display_name
	Text   string `json:"text"`
}

// callChroniclerForBoard issues a single wait=true chat to the chronicler
// agent and parses a structured JSON reply containing the notice prose
// and any concerns the chronicler attached. Distinct from fireChronicler
// — that one runs the directorial harness (set_environment / record_event
// / recall / attend_to / done); this one is a focused content gen with a
// JSON-shaped reply contract.
//
// The chronicler agent already has the village context loaded (recent
// events, atmosphere, NPC list) so the prompt only carries what's
// specific to this board: location, capacity, the prior text being
// replaced, and the available targets the chronicler may attach concerns
// to.
//
// JSON shape contract:
//
//	{
//	  "notice": "Line 1\nLine 2",
//	  "concerns": [
//	    {"target": "Tavern", "text": "A plain woolen shawl was left here."}
//	  ]
//	}
//
// Concerns is optional; missing/empty array means no facts to attach.
// On JSON parse failure the entire reply is treated as raw notice prose
// — better to post a notice without concerns than to drop the rotation
// because the model wrapped its reply in markdown fences.
//
// sceneID: fresh per call so admin chat-grouping renders these as their
// own one-row scenes rather than folding into whatever scene happens to
// be in flight.
func (app *App) callChroniclerForBoard(ctx context.Context, location string, capacity int, prior string) (chroniclerBoardReply, error) {
	if app.npcChatClient == nil {
		return chroniclerBoardReply{}, errors.New("npc chat client is not configured")
	}

	targets, err := app.loadAvailableConcernTargets(ctx)
	if err != nil {
		// Don't fail the whole call — the chronicler can still write prose
		// without target enrichment, just won't be able to attach valid
		// concerns. Worth surfacing as a log line so we notice if this
		// breaks regularly.
		log.Printf("object_content: load concern targets for %s: %v", location, err)
	}

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Write fresh notice text for the village noticeboard at %s.\n", location)
	noun := "notice"
	if capacity > 1 {
		noun = "notices"
	}
	fmt.Fprintf(&prompt, "The board has space for %d short %s.\n", capacity, noun)

	prompt.WriteString(`
A noticeboard in Salem 1692 carries things villagers actually post — items pinned up with a reason. Examples of what belongs:

  - Civic announcements: a town meeting, a militia muster, sermon hours, the next court day.
  - Lost & found: a misplaced shawl, a strayed cow, a recovered ring.
  - Wares and services: apothecary stock arrived, the smith shoeing horses Tuesday, ale freshly brewed at the Tavern, the inn taking lodgers.
  - Warnings: wolves heard near the wood pile, strangers seen about at dusk, a rotted plank on the bridge.
  - News: a visiting minister expected, banns for a wedding, a death in the parish, a ship arrived at port.
  - Petitions and grievances: a fence in disrepair, a swine taken up at the well, a debt unpaid.

Do NOT write surveillance about who is presently at what location, what they are doing right now, or what they feel or want in this moment. The board is not a status feed — by the time anyone reads it, that moment has passed. Write things a townsperson would post with intent: to inform, warn, advertise, announce, summon, or petition.

Voice: as a 1692 villager would post it. Formal for civic announcements, plain for offerings and warnings. One notice per line. No numbering, bullets, or headings.
`)

	if prior != "" {
		prompt.WriteString("\nFor your reference, the board carried the following before — these notices have served their purpose and are being replaced. Do NOT repeat them; find different things to post:\n")
		prompt.WriteString(prior)
		prompt.WriteString("\n")
	}

	if len(targets) > 0 {
		prompt.WriteString("\nIf a notice does carry a durable named-party fact (see concerns rule below), use a name from this list verbatim:\n")
		for _, t := range targets {
			prompt.WriteString("  - ")
			prompt.WriteString(t)
			prompt.WriteString("\n")
		}
	}

	prompt.WriteString("\nReply with a single JSON object and nothing else — no preamble, no markdown fences:\n")
	prompt.WriteString("{\n")
	prompt.WriteString("  \"notice\": \"the prose, one notice per line, period-correct Salem 1692\",\n")
	prompt.WriteString("  \"concerns\": [\n")
	prompt.WriteString("    {\"target\": \"<name from the list above>\", \"text\": \"<one-sentence durable fact>\"}\n")
	prompt.WriteString("  ]\n")
	prompt.WriteString("}\n")
	prompt.WriteString("\nA concern is a *durable fact* a named party might later discover or act on — a lost item left at the Tavern, posted service hours at the smithy, a visitor expected at the Mansion, a debt owed to a named villager. Most notices produce zero or one concern, not many. Do NOT attach concerns for transient state such as \"X is at Y\", \"X is at work today\", \"X is hungry\", or \"X is here.\" If no notice carries a durable named-party fact, omit the concerns array entirely.")

	// Noticeboard content generation is village-wide chronicler authoring,
	// not a place-anchored cascade — pass empty structure so the scenes
	// row records NULL and the admin UI omits the location chip.
	sceneID := app.newScene(ctx, "")
	reply, err := app.npcChatClient.sendChat(ctx, chroniclerAgent, prompt.String(), "", sceneID, nil)
	if err != nil {
		return chroniclerBoardReply{}, err
	}
	if reply == nil {
		return chroniclerBoardReply{}, errors.New("chronicler returned no reply")
	}

	parsed := parseChroniclerBoardReply(reply.Text)
	return parsed, nil
}

// parseChroniclerBoardReply extracts the notice prose and concerns from
// the chronicler's response. Tolerates markdown code fences (the LLM
// occasionally wraps its JSON in ```json … ``` despite the instruction).
// Falls back to treating the whole reply as raw notice prose with no
// concerns when the JSON parse fails.
func parseChroniclerBoardReply(raw string) chroniclerBoardReply {
	stripped := stripJSONFences(raw)
	var out chroniclerBoardReply
	if err := json.Unmarshal([]byte(stripped), &out); err != nil {
		// Graceful degradation — the prose still goes up, no concerns.
		return chroniclerBoardReply{Notice: raw}
	}
	if strings.TrimSpace(out.Notice) == "" {
		// Parsed JSON but empty notice — fall back to raw so the board
		// at least gets the chronicler's text in some form.
		return chroniclerBoardReply{Notice: raw}
	}
	return out
}

// stripJSONFences removes common markdown code-fence wrappings so a reply
// like "```json\n{...}\n```" parses cleanly. Idempotent on already-clean
// input.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Drop opening fence line.
		if idx := strings.Index(s, "\n"); idx > -1 {
			s = s[idx+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
		}
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

// loadAvailableConcernTargets builds the human-readable name list shown
// to the chronicler in the noticeboard prompt. Includes every village
// structure with a non-empty display_name and every named actor with a
// home or work assignment in the village (excluding the unnamed/decorative
// actor rows). Returned as plain strings — IDs are resolved at write time
// via resolveTargetByName.
func (app *App) loadAvailableConcernTargets(ctx context.Context) ([]string, error) {
	var names []string

	// Structures with display names.
	structRows, err := app.DB.Query(ctx,
		`SELECT display_name FROM village_object
		  WHERE display_name IS NOT NULL AND display_name <> ''
		  ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	for structRows.Next() {
		var n string
		if scanErr := structRows.Scan(&n); scanErr == nil {
			names = append(names, n)
		}
	}
	structRows.Close()

	// Named actors (anyone with home or work in the village). Decorative
	// actors with neither set are excluded — they're rarely referenced
	// by name in notices.
	actorRows, err := app.DB.Query(ctx,
		`SELECT display_name FROM actor
		  WHERE display_name IS NOT NULL AND display_name <> ''
		    AND (home_structure_id IS NOT NULL OR work_structure_id IS NOT NULL)
		  ORDER BY display_name`)
	if err != nil {
		return names, err
	}
	for actorRows.Next() {
		var n string
		if scanErr := actorRows.Scan(&n); scanErr == nil {
			names = append(names, n)
		}
	}
	actorRows.Close()
	return names, nil
}

// saveObjectContent writes content_text + content_posted_at, bumps
// content_generation, and broadcasts the change. The UPDATE is conditional
// on (a) the placement's current_state still matching the state at
// dispatch and (b) the noticeboard_content instance tag still being
// attached — either changing in flight means a stale chronicler reply for
// a now-different board, and silently dropping the write is the right
// move. RowsAffected==0 is reported as an error so
// generateAndSaveBoardContent can log it; not a failure mode, just a
// race outcome.
//
// Returns the new content_generation so the caller can persist concerns
// (ZBBS-117) tagged with the same generation the prose was written under.
// Concerns from prior generations age out of perception automatically
// once this UPDATE bumps the generation.
//
// Timestamps come from NOW() inside the UPDATE rather than time.Now() in
// Go, so the broadcast value matches the stored value exactly even if
// the app server clock and the DB clock differ.
func (app *App) saveObjectContent(ctx context.Context, objectID, expectedState, body string) (int, error) {
	var postedAt time.Time
	var newGeneration int
	err := app.DB.QueryRow(ctx,
		`UPDATE village_object o
		    SET content_text = $3,
		        content_posted_at = NOW(),
		        content_generation = o.content_generation + 1
		  WHERE o.id = $1::uuid
		    AND o.current_state = $2
		    AND EXISTS (
		        SELECT 1 FROM village_object_tag t
		        WHERE t.object_id = o.id AND t.tag = $4
		    )
		  RETURNING content_posted_at, content_generation`,
		objectID, expectedState, body, tagNoticeBoardInstance,
	).Scan(&postedAt, &newGeneration)
	if err != nil {
		// pgx returns ErrNoRows when the UPDATE matches zero rows.
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("stale state — board %s no longer in %q or no longer tagged", objectID, expectedState)
		}
		return 0, err
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "object_content_changed",
		Data: map[string]any{
			"id":                objectID,
			"content_text":      body,
			"content_posted_at": postedAt.UTC().Format(time.RFC3339),
		},
	})
	return newGeneration, nil
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
		        content_posted_at = NULL,
		        content_generation = content_generation + 1
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
	// Cleared without replacement — drop concerns from the prior posting
	// rather than letting them linger until lazy janitor sweep. The
	// generation bump above already makes them invisible to perception;
	// this is just hygiene.
	if err := app.clearConcernsForSource(ctx, concernSourceVillageObjectContent, objectID); err != nil {
		log.Printf("object_content: clear concerns for %s: %v", objectID, err)
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
