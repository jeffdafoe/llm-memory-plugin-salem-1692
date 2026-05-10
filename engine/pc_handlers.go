package main

// Player-character (PC) HTTP handlers (M6.7).
//
// PCs are salem-realm llm-memory users who walk around the village,
// join scene huddles alongside NPCs, and converse with them. Their
// presence and position live in pc_position; their identity comes
// from the authenticated session.
//
// Two identities per PC:
//   - login_username: the llm-memory username, stable system identity.
//     Used for session lookup, chat_send sender attribution.
//   - character_name: the in-world identity NPCs perceive. Period-
//     appropriate, set by the player on first login. NPCs greet by
//     character_name; audit log records speech with character_name.
//
// Endpoints:
//
//   - POST /api/village/pc/me      → state read for the talk panel:
//                                     character_name, position, huddle
//                                     members, sprite. Returns
//                                     exists=false when the PC has
//                                     never been created (client
//                                     bootstrap pops the sprite picker
//                                     to drive a /pc/create call).
//   - POST /api/village/pc/create  → first-time creation. Body:
//                                     {character_name, sprite_id?}.
//                                     Auto-assigns home to the nearest
//                                     tavern. Idempotent on the
//                                     login_username (re-running
//                                     updates the name and, when
//                                     sprite_id is provided, the
//                                     sprite).
//   - POST /api/village/pc/sprite  → swap the PC's render sprite. Body:
//                                     {sprite_id}. Distinct from /create
//                                     so the picker can drive a sprite
//                                     change without re-asserting the
//                                     character_name.
//   - POST /api/village/pc/move    → click-to-walk. Body:
//                                     {target_x, target_y, speed?}.
//                                     Resolves session→actor.id and
//                                     defers to startNPCWalk; the
//                                     existing npc_walking /
//                                     npc_arrived broadcasts cover the
//                                     PC because they key on actor id,
//                                     not driver kind.
//   - POST /api/village/pc/say     → 1:1 whisper to one NPC. Proxies
//                                     to /v1/chat/send with the user's
//                                     auth header.
//   - POST /api/village/pc/speak   → broadcast to current huddle.
//                                     agent_action_log row with
//                                     speaker_name=character_name,
//                                     source='player'. Co-located NPCs
//                                     get an event tick.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type pcMeResponse struct {
	LoginUsername     string             `json:"login_username"`
	Exists            bool               `json:"exists"`
	// ActorID is the PC's actor.id. Surfaced so the client can recognize
	// its own PC in WS broadcasts like npc_arrived (which carries actor
	// id, not login). Empty when the PC hasn't been created yet.
	ActorID           string             `json:"actor_id,omitempty"`
	CharacterName     string             `json:"character_name,omitempty"`
	X                 float64            `json:"x"`
	Y                 float64            `json:"y"`
	InsideStructureID *string            `json:"inside_structure_id,omitempty"`
	// AudienceStructureID is the structure scoping the PC's current
	// conversation. When inside a structure (sat at the bar), this
	// matches inside_structure_id. When loitering at a booth or doorway
	// (huddle joined via the knock handler but not formally inside),
	// this is the huddle's structure_id while inside_structure_id stays
	// null. The talk panel uses this for backload + change detection so
	// outdoor-at-booth conversations get the same room view as indoor
	// ones; other consumers (visibility, positioning) keep using
	// inside_structure_id, which is the literal "am I inside?".
	AudienceStructureID *string            `json:"audience_structure_id,omitempty"`
	// AudienceRoomID is the PC's current private/staff room when in one,
	// "" otherwise. Pairs with AudienceStructureID to scope the talk panel
	// to a single subspace — without this, a PC in Tavern→bedroom_1 still
	// hears Tavern→common-room speech (structure_id matches, no further
	// filter). Empty for common-room or outdoor PCs (public scope).
	AudienceRoomID    *string            `json:"audience_room_id,omitempty"`
	StructureName     string             `json:"structure_name,omitempty"`
	HomeStructureID   *string            `json:"home_structure_id,omitempty"`
	HomeName          string             `json:"home_name,omitempty"`
	CurrentHuddleID   *string            `json:"current_huddle_id,omitempty"`
	HuddleMembers     []pcHuddleMember   `json:"huddle_members"`
	RecentSpeech      []pcRecentSpeech   `json:"recent_speech,omitempty"`
	// Purse and inventory — surfaced to the client so the top-bar coin
	// chip and the pay modal can render the player's resources without
	// a separate fetch.
	Coins         int                `json:"coins"`
	Inventory     []pcInventoryEntry `json:"inventory"`
	// Needs is the PC's current actor_need snapshot (ZBBS-123) — keys
	// 'hunger', 'thirst', 'tiredness'; values 0..24. Surfaced so the
	// HUD top-bar can render the player's body state alongside their
	// purse. Empty map (not nil) when the PC has no actor_need rows
	// yet — defensive against a PC created before the seed path.
	Needs NeedSet `json:"needs"`
	// NeedThresholds is the engine's per-need red-line, sourced from
	// the *_red_threshold settings. Surfaced so the HUD colors with the
	// same boundaries the engine uses for in-prompt felt language —
	// without this, an admin retuning a threshold drifts the HUD
	// silently. Client falls back to its own defaults when omitted.
	NeedThresholds NeedSet `json:"need_thresholds,omitempty"`
	// DwellingAttributes lists the need attributes the PC is actively
	// recovering RIGHT NOW via dwell — i.e. each attribute that has a
	// non-stale actor_dwell_credit row for this actor. ZBBS-HOME-218.
	// The HUD uses this to engage a continuous pulse on the relevant
	// segment immediately on /pc/me — without it, the pulse depended
	// on client-side decrease detection and wouldn't engage on a fresh
	// page load even when the player was already at a recovery slot.
	// "Non-stale" = last_credited_at within the dwell_period_minutes
	// window; an actor who walked away has their row deleted on the
	// next dwell tick, so the staleness is bounded.
	DwellingAttributes []string `json:"dwelling_attributes,omitempty"`
	// SpriteID is null until the player picks one. Client bootstrap uses
	// the null state as the trigger to open the sprite picker on first
	// login. Sprite is the inlined catalog row (sheet, frame dims,
	// animations, pack) so a freshly-arrived client can render the PC
	// without a follow-up catalog fetch — same shape as NPC.Sprite.
	SpriteID *string    `json:"sprite_id,omitempty"`
	Sprite   *NPCSprite `json:"sprite,omitempty"`
}

// pcInventoryEntry is one stack of items the PC carries. display_label
// is the human-readable name from item_kind so the UI can render
// "Bread" instead of "bread". capabilities is included so the pay-modal
// client can decide whether take-home is allowed for this item kind
// without a second lookup.
type pcInventoryEntry struct {
	ItemKind     string   `json:"item_kind"`
	DisplayLabel string   `json:"display_label"`
	Quantity     int      `json:"quantity"`
	Category     string   `json:"category,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type pcHuddleMember struct {
	Kind        string  `json:"kind"` // "npc" or "pc"
	Name        string  `json:"name"` // display_name (NPC) or character_name (PC)
	Role        *string `json:"role,omitempty"`
	TargetAgent *string `json:"target_agent,omitempty"` // llm_memory_agent for NPCs (chat_send recipient)
}

// pcRecentSpeech is one historical conversational/narrative event at the
// player's current inside_structure_id, surfaced so the talk panel can
// backload room context when opened. The room metaphor: walk in, you
// hear what's been happening here lately.
//
// Kind discriminates how the client renders the entry:
//   - "speech_npc" / "speech_player" — quoted dialogue, color-coded
//   - "act"                         — italic narration ("X poured ale.")
//   - "departure"                   — italic narration ("X left for home.")
//   - "serve"                       — italic narration ("X serves Y stew.")
//   - "pay"                         — italic narration ("Y pays X 9 coins.")
//   - "consume"                     — italic narration ("X drinks ale.")
//
// Text is pre-rendered server-side so the client doesn't have to know
// the verb-phrase grammar, destination wording, or item categories.
type pcRecentSpeech struct {
	SpeakerName string    `json:"speaker_name"`
	Text        string    `json:"text"`
	Kind        string    `json:"kind"`
	OccurredAt  time.Time `json:"occurred_at"`
}

const pcRecentSpeechLimit = 20
const pcRecentSpeechCutoff = 24 * time.Hour

func (app *App) handlePCMe(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	resp := pcMeResponse{
		LoginUsername: user.Username,
		HuddleMembers: []pcHuddleMember{},
	}

	var x, y float64
	var coins int
	var pcActorID string
	var charName, insideID, huddleID, structureName, homeID, homeName, spriteID sql.NullString
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.id::text, pc.display_name, pc.current_x, pc.current_y,
		        pc.inside_structure_id::text,
		        pc.current_huddle_id::text,
		        COALESCE(o.display_name, a.name) AS structure_name,
		        pc.home_structure_id::text,
		        COALESCE(ho.display_name, ha.name) AS home_name,
		        pc.sprite_id::text,
		        pc.coins
		   FROM actor pc
		   LEFT JOIN village_object o ON o.id = pc.inside_structure_id
		   LEFT JOIN asset a ON a.id = o.asset_id
		   LEFT JOIN village_object ho ON ho.id = pc.home_structure_id
		   LEFT JOIN asset ha ON ha.id = ho.asset_id
		  WHERE pc.login_username = $1`,
		user.Username,
	).Scan(&pcActorID, &charName, &x, &y, &insideID, &huddleID, &structureName, &homeID, &homeName, &spriteID, &coins)
	if err == sql.ErrNoRows {
		resp.Exists = false
		jsonResponse(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		log.Printf("pc/me query: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	resp.Exists = true
	resp.ActorID = pcActorID
	if charName.Valid {
		resp.CharacterName = charName.String
	}
	resp.X, resp.Y = x, y

	// Body needs for the HUD readout (ZBBS-123). needsSnapshot reads
	// every actor_need row for this actor and returns them keyed by
	// need.Key. On error we surface an empty set rather than failing
	// the whole /pc/me — the rest of the response is still useful and
	// the next poll retries.
	if ns, err := app.needsSnapshot(r.Context(), pcActorID); err == nil {
		resp.Needs = ns
	} else {
		log.Printf("pc/me needs snapshot %s: %v", pcActorID, err)
		resp.Needs = NeedSet{}
	}
	// Threshold reads use the registry-driven helper so a future
	// fourth need (e.g. social) flows into the HUD without touching
	// this handler. Cheap setting lookups; failures inside the helper
	// fall back to per-need defaults.
	resp.NeedThresholds = NeedSet(app.loadNeedThresholds(r.Context()))

	// ZBBS-HOME-218: dwelling attributes for the HUD pulse.
	// Distinct attributes from active actor_dwell_credit rows for
	// this actor. "Active" = last_credited_at within
	// dwell_period_minutes (the tick interval) — outside that window,
	// the actor has either walked away (next dwell tick deletes the
	// row) or the row is genuinely stale; the pulse should fade. On
	// query error we surface an empty array so a transient DB hiccup
	// just turns the pulse off briefly rather than failing the whole
	// /pc/me.
	insideForDwell := ""
	if insideID.Valid {
		insideForDwell = insideID.String
	}
	dwelling, dwErr := app.fetchDwellingAttributes(r.Context(), pcActorID, x, y, insideForDwell)
	if dwErr != nil {
		log.Printf("pc/me dwelling attributes %s: %v", pcActorID, dwErr)
	}
	resp.DwellingAttributes = dwelling
	if insideID.Valid {
		s := insideID.String
		resp.InsideStructureID = &s
	}
	if structureName.Valid {
		resp.StructureName = structureName.String
	}
	if homeID.Valid {
		s := homeID.String
		resp.HomeStructureID = &s
	}
	if homeName.Valid {
		resp.HomeName = homeName.String
	}
	if spriteID.Valid {
		s := spriteID.String
		resp.SpriteID = &s
		// Inline the catalog row so the client can render the PC without
		// a follow-up GET /api/village/npc-sprites — same shape NPCs use.
		// loadNPCSprites is the same lookup the NPC list endpoint uses;
		// the cached map is small, so the per-request fetch is cheap.
		if sprites, err := app.loadNPCSprites(r.Context()); err == nil {
			if sp, ok := sprites[s]; ok {
				resp.Sprite = sp
			}
		}
	}

	// Audience structure: position-based via actorStructureScope.
	// Indoor returns inside_structure_id; outdoor returns the nearest
	// structure whose loiter pin is within 64px (same tolerance as
	// huddle formation). Empty when in transit / open road. Position-
	// based rather than huddle-based so a pre-bound destination huddle
	// (knock click) doesn't flip the talk panel scope before the PC has
	// actually walked there — scope stays at the previous location
	// until the PC arrives at the new structure's loiter ring.
	if scope := app.actorStructureScope(r.Context(), insideID, x, y); scope != "" {
		resp.AudienceStructureID = &scope
	}

	// Audience room: only set when the PC is inside a non-common subspace
	// (private or staff room). Common-room and outdoor PCs leave it nil
	// so they hear public scope (events with no room_id). The talk panel
	// filter requires equality — bedroom listener only receives bedroom
	// events; common listener only receives common-or-outdoor events.
	if room := app.actorPrivateRoomScope(r.Context(), pcActorID); room != "" {
		resp.AudienceRoomID = &room
	}

	if huddleID.Valid {
		s := huddleID.String
		resp.CurrentHuddleID = &s
		// Co-located actors in the same huddle, scoped to the PC's
		// subspace. After PC knock at an owner-policy or closed structure,
		// the engine syncs every inside-the-structure actor into the same
		// huddle (pc_handlers.go:1115) so the talk panel has somebody to
		// address. The unified huddle then mixes indoor and outdoor
		// members — John inside the Tavern and Caleb at the loiter ring
		// share huddle 9858289d. Without the inside-match filter the talk
		// panel would list Caleb "in the Tavern" alongside John, and the
		// pay-modal recipient dropdown would default to him alphabetically
		// (defeating WORK-210's auto-prefill from the actual vendor's
		// scene_quote).
		//
		// COALESCE(...,'') normalizes NULL inside_structure_id to '' on
		// both sides so an outdoor PC matches outdoor members and an
		// indoor PC at structure X matches members inside X.
		var pcInsideID string
		if insideID.Valid {
			pcInsideID = insideID.String
		}
		rows, err := app.DB.Query(r.Context(),
			`SELECT CASE WHEN login_username IS NOT NULL THEN 'pc' ELSE 'npc' END AS kind,
			        display_name, role, llm_memory_agent
			   FROM actor
			  WHERE current_huddle_id::text = $1
			    AND (login_username IS NULL OR login_username != $2)
			    AND COALESCE(inside_structure_id::text, '') = $3
			  ORDER BY display_name`,
			huddleID.String, user.Username, pcInsideID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var kind, name string
				var role, llmAgent sql.NullString
				if err := rows.Scan(&kind, &name, &role, &llmAgent); err != nil {
					continue
				}
				m := pcHuddleMember{Kind: kind, Name: name}
				if role.Valid {
					rs := role.String
					m.Role = &rs
				}
				if llmAgent.Valid {
					la := llmAgent.String
					m.TargetAgent = &la
				}
				resp.HuddleMembers = append(resp.HuddleMembers, m)
			}
		}
	} else if !insideID.Valid {
		// Outdoor proximity roster. When the PC is outside with no huddle,
		// the talk panel still needs a nearby list to populate the
		// launcher chip strip and unlock its open gate (the launcher
		// stays hidden when huddle_members is empty). Chebyshev radius 6
		// matches the client's outdoor distance filter on incoming
		// npc_spoke broadcasts so the chip strip and the speech audience
		// agree on "near."
		//
		// No last_seen_at filter yet — a logged-out PC near you will
		// phantom into the strip until presence-staleness lands as its
		// own feature. v1 deliberately accepts that.
		rows, err := app.DB.Query(r.Context(),
			`SELECT display_name
			   FROM actor
			  WHERE login_username IS NOT NULL
			    AND login_username <> $1
			    AND inside_structure_id IS NULL
			    AND GREATEST(ABS(current_x - $2), ABS(current_y - $3)) <= 6
			  ORDER BY display_name`,
			user.Username, x, y)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					continue
				}
				resp.HuddleMembers = append(resp.HuddleMembers, pcHuddleMember{
					Kind: "pc",
					Name: name,
				})
			}
		}
	}

	// Purse and inventory. Coins came back on the same row as
	// position/structure to save a round-trip. Inventory is a separate
	// query — loadPCInventoryEntries joins actor_inventory with
	// item_kind so the client gets display labels and capabilities
	// without a follow-up fetch. Empty inventory still returns an empty
	// array (not nil) so JSON serializes as `[]`, not `null`.
	resp.Coins = coins
	resp.Inventory = app.loadPCInventoryEntries(r.Context(), pcActorID)
	if resp.Inventory == nil {
		resp.Inventory = []pcInventoryEntry{}
	}

	// Recent speech at the player's current conversational scope —
	// backloads the talk panel so a freshly-opened panel shows what the
	// room has been saying lately. Sources from audience_structure_id so
	// loitering-at-the-booth gets the same backload as sitting-at-the-
	// bar. Returned oldest→newest so the client can append in natural
	// reading order. Limited to pcRecentSpeechLimit rows in the last
	// pcRecentSpeechCutoff (24h) so a quiet room doesn't dredge up
	// week-old chatter and an active one only surfaces the recent thread.
	if resp.AudienceStructureID != nil {
		audienceRoom := ""
		if resp.AudienceRoomID != nil {
			audienceRoom = *resp.AudienceRoomID
		}
		resp.RecentSpeech = app.loadRecentSpeechAtScope(r.Context(), *resp.AudienceStructureID, audienceRoom)
	}

	jsonResponse(w, http.StatusOK, resp)
}

// loadRecentSpeechAtScope pulls the last N narration-worthy events at
// a (structure, room) scope: speech (dialogue), acts (verb-phrase
// narration), and move_to commits (departures). Filtered by both
// `payload->>'structure_id'` AND `payload->>'room_id'` so a PC entering
// a private bedroom doesn't backfill with tavern-common chatter.
//
// roomID semantics mirror the live audibility model: empty matches
// rows with no room_id stamped (public scope — common rooms or older
// data pre-Phase 1.5); a private room id matches only rows with the
// same id stamped. COALESCE handles the legacy-data case so a common-
// room PC still sees pre-Phase 1.5 history.
//
// Result='ok' filters out rejected/empty attempts so the panel doesn't
// surface failed actions to the player. Text is pre-rendered into a
// readable line per row so the client renders strings, not grammar.
func (app *App) loadRecentSpeechAtScope(ctx context.Context, structureID, roomID string) []pcRecentSpeech {
	cutoff := time.Now().Add(-pcRecentSpeechCutoff)
	// LEFT JOIN actor so move_to rows can rebuild the same departure
	// narration the live broadcast emits — narrateMoveDeparture needs
	// the speaker's home / work structure IDs to render "retired for
	// the evening" when home == work, otherwise "left for home". For
	// non-move_to rows the joined columns are unused.
	// LEFT JOIN pay_ledger so deliver_order rows can rebuild the engine-
	// authored handover narration ("X hands Y the Z.") on backload.
	// The audit payload only carries {ledger_id, structure_id, room_id};
	// item / qty / consume_now / buyer come from the ledger row itself.
	// Group orders surface as buyer-only here — multi-consumer recipient
	// names are not joined back; live broadcast handles the precise case.
	rows, err := app.DB.Query(ctx, `
		SELECT al.speaker_name, al.action_type, al.source, al.payload, al.occurred_at,
		       ac.home_structure_id, ac.work_structure_id,
		       pl.item_kind, pl.qty, pl.consume_now,
		       ba.display_name
		FROM agent_action_log al
		LEFT JOIN actor ac ON ac.id = al.actor_id
		LEFT JOIN pay_ledger pl ON al.action_type = 'deliver_order'
		                       AND pl.id = NULLIF(al.payload->>'ledger_id', '')::bigint
		LEFT JOIN actor ba ON ba.id = pl.buyer_id
		WHERE al.action_type IN ('speak', 'act', 'move_to', 'serve', 'pay', 'consume', 'deliver_order')
		  AND al.result = 'ok'
		  AND al.payload->>'structure_id' = $1
		  AND COALESCE(al.payload->>'room_id', '') = $2
		  AND al.occurred_at > $3
		ORDER BY al.occurred_at DESC
		LIMIT $4
	`, structureID, roomID, cutoff, pcRecentSpeechLimit)
	if err != nil {
		log.Printf("recent events: %v", err)
		return nil
	}
	defer rows.Close()

	// Collect newest-first, then reverse for natural reading order.
	var recent []pcRecentSpeech
	for rows.Next() {
		var speakerName, actionType, source string
		var payloadJSON []byte
		var occurredAt time.Time
		var homeStructureID, workStructureID sql.NullString
		var deliverItemKind sql.NullString
		var deliverQty sql.NullInt32
		var deliverConsumeNow sql.NullBool
		var deliverBuyerName sql.NullString
		if err := rows.Scan(&speakerName, &actionType, &source, &payloadJSON, &occurredAt,
			&homeStructureID, &workStructureID,
			&deliverItemKind, &deliverQty, &deliverConsumeNow, &deliverBuyerName); err != nil {
			continue
		}
		var payload map[string]interface{}
		_ = json.Unmarshal(payloadJSON, &payload)

		entry := pcRecentSpeech{SpeakerName: speakerName, OccurredAt: occurredAt}

		switch actionType {
		case "speak":
			text, _ := payload["text"].(string)
			if text == "" {
				continue
			}
			entry.Text = text
			if source == "player" {
				entry.Kind = "speech_player"
			} else {
				entry.Kind = "speech_npc"
			}
		case "act":
			verb, _ := payload["verb_phrase"].(string)
			if verb == "" {
				continue
			}
			entry.Text = fmt.Sprintf("%s %s.", speakerName, verb)
			entry.Kind = "act"
		case "move_to":
			dest, _ := payload["destination"].(string)
			if dest == "" {
				continue
			}
			entry.Text = app.narrateMoveDeparture(ctx, speakerName, homeStructureID, workStructureID, dest)
			entry.Kind = "departure"
		case "serve":
			entry.Text = narrateServe(speakerName, payload)
			if entry.Text == "" {
				continue
			}
			entry.Kind = "serve"
		case "pay":
			entry.Text = narratePay(speakerName, payload)
			if entry.Text == "" {
				continue
			}
			entry.Kind = "pay"
		case "consume":
			itemName, _ := payload["item"].(string)
			entry.Text = narrateConsume(speakerName, payload, app.itemAttributeFor(ctx, itemName))
			if entry.Text == "" {
				continue
			}
			entry.Kind = "consume"
		case "deliver_order":
			if !deliverItemKind.Valid || deliverItemKind.String == "" || !deliverBuyerName.Valid {
				continue
			}
			qty := 1
			if deliverQty.Valid && deliverQty.Int32 > 0 {
				qty = int(deliverQty.Int32)
			}
			consumeNow := true
			if deliverConsumeNow.Valid {
				consumeNow = deliverConsumeNow.Bool
			}
			entry.Text = narrateDeliverHandover(speakerName, []string{deliverBuyerName.String}, deliverItemKind.String, qty, consumeNow)
			if entry.Text == "" {
				continue
			}
			entry.Kind = "deliver"
		default:
			continue
		}
		recent = append(recent, entry)
	}
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	return recent
}

// loadPCInventoryEntries pulls the PC's inventory joined with
// item_kind metadata so the client gets display label, category, and
// capabilities for each stack in one fetch. Used by /pc/me to populate
// the inventory readout the pay modal and tooltip rely on. Empty
// inventory returns nil; the caller normalizes to an empty slice for
// JSON's sake.
func (app *App) loadPCInventoryEntries(ctx context.Context, actorID string) []pcInventoryEntry {
	rows, err := app.DB.Query(ctx, `
		SELECT ai.item_kind, ai.quantity,
		       ik.display_label, COALESCE(ik.category, ''),
		       COALESCE(ik.capabilities, ARRAY[]::varchar[])
		  FROM actor_inventory ai
		  JOIN item_kind ik ON ik.name = ai.item_kind
		 WHERE ai.actor_id = $1
		 ORDER BY ik.sort_order, ai.item_kind
	`, actorID)
	if err != nil {
		log.Printf("loadPCInventoryEntries: %v", err)
		return nil
	}
	defer rows.Close()
	var out []pcInventoryEntry
	for rows.Next() {
		var entry pcInventoryEntry
		if err := rows.Scan(&entry.ItemKind, &entry.Quantity, &entry.DisplayLabel, &entry.Category, &entry.Capabilities); err != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// handlePCCreate — first-time PC creation. Sets character_name and
// seeds the PC as a 1-day boarder at the village's lodging structure.
// Idempotent on re-call: updates character_name to the new value
// (lets a player rename mid-game if they want — UX decision deferred).
// Initial position is the lodging structure's anchor (or village
// center fallback).
//
// ZBBS-WORK-204: home_structure_id is no longer set at creation. PC
// boarding is a pay_ledger relationship, not a column-level "you live
// here." A starter `nights_stay` ledger row keyed to the picked
// lodging keeper anchors the PC's lodger status from minute one — so
// canEnter, the lodger perception cue, and auto-bed all work
// immediately, and the PC's first interaction with the keeper is the
// extension negotiation rather than initial check-in. Re-running
// /pc/create on an existing PC is a no-op for the starter row (no
// duplicate insert) so a sprite change or rename doesn't double-book.
func (app *App) handlePCCreate(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		CharacterName string  `json:"character_name"`
		SpriteID      *string `json:"sprite_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.CharacterName = strings.TrimSpace(req.CharacterName)
	if req.CharacterName == "" {
		jsonError(w, "character_name is required", http.StatusBadRequest)
		return
	}
	if len(req.CharacterName) > 100 {
		jsonError(w, "character_name too long", http.StatusBadRequest)
		return
	}
	// sprite_id is optional at creation. When provided, validate it exists
	// in the catalog so the FK update doesn't surface as a generic 500.
	// Empty string is treated the same as omitted — clients sometimes send
	// "" for a not-yet-picked field.
	var spriteID string
	if req.SpriteID != nil {
		spriteID = strings.TrimSpace(*req.SpriteID)
	}
	if spriteID != "" {
		var spriteCount int
		if err := app.DB.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM npc_sprite WHERE id = $1`, spriteID,
		).Scan(&spriteCount); err != nil || spriteCount == 0 {
			jsonError(w, "Unknown sprite_id", http.StatusBadRequest)
			return
		}
	}

	// Travelers lodge at the village's lodging structure. Prefer pure
	// inns (lodging without tavern) over tavern combos — historically
	// the "ordinary" was both, but a dedicated inn is the more
	// quintessential traveler's home. The IS_TAVERN ordering picks
	// pure inn first and falls back to tavern only when no pure inn
	// exists. Within a tier, oldest placement wins.
	//
	// ZBBS-WORK-204: this picked structure no longer becomes
	// home_structure_id; instead the PC is seeded as a boarder via a
	// pay_ledger nights_stay row tied to the structure's keeper. The
	// anchor coords still drive the spawn position so a fresh PC
	// arrives where they're about to lodge.
	var lodgingID sql.NullString
	var lodgingX, lodgingY sql.NullFloat64
	if err := app.DB.QueryRow(r.Context(),
		`SELECT o.id::text, o.x, o.y
		   FROM village_object o
		   JOIN village_object_tag vot ON vot.object_id = o.id AND vot.tag = 'lodging'
		  ORDER BY EXISTS (
		             SELECT 1 FROM village_object_tag t2
		              WHERE t2.object_id = o.id AND t2.tag = 'tavern'
		           ) ASC, o.created_at ASC
		  LIMIT 1`,
	).Scan(&lodgingID, &lodgingX, &lodgingY); err != nil && err != sql.ErrNoRows {
		log.Printf("pc/create lodging lookup: %v", err)
	}

	// Default starting position: the lodging anchor, or (0,0) when
	// no tavern/inn is placed yet (test environments).
	var startX, startY float64
	if lodgingX.Valid {
		startX = lodgingX.Float64
		startY = lodgingY.Float64
	}

	// Upsert. ON CONFLICT lets re-runs update display_name without
	// disturbing position. Post-ZBBS-084: display_name is the unified
	// in-world identity column (was character_name on pc_position),
	// current_x/current_y are the position columns (were x/y).
	//
	// sprite_id COALESCE: when the request supplied a sprite, the new
	// value wins; when it didn't, the existing value (if any) survives
	// the upsert. NULLIF($5, '')::uuid converts the empty fast-path to
	// SQL NULL so the COALESCE picks up the existing column.
	//
	// home_structure_id is intentionally NOT set here (ZBBS-WORK-204).
	// The starter nights_stay ledger row below is what gives the PC
	// boarder status and canEnter access to the lodging structure;
	// home stays NULL so the boarder/owner distinction reads honestly
	// on the actor row.
	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		log.Printf("pc/create begin tx: %v", err)
		jsonError(w, "Failed to create PC", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())
	var actorID, prevSpriteID sql.NullString
	if err := tx.QueryRow(r.Context(),
		`INSERT INTO actor (login_username, display_name, current_x, current_y, sprite_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid)
		 ON CONFLICT (login_username) DO UPDATE
		   SET display_name = EXCLUDED.display_name,
		       sprite_id = COALESCE(EXCLUDED.sprite_id, actor.sprite_id)
		 RETURNING id::text, sprite_id::text`,
		user.Username, req.CharacterName, startX, startY,
		spriteID,
	).Scan(&actorID, &prevSpriteID); err != nil {
		log.Printf("pc/create insert: %v", err)
		jsonError(w, "Failed to create PC", http.StatusInternalServerError)
		return
	}
	// Seed actor_need rows in the same tx (ZBBS-121 commit 5). The
	// upsert-shape INSERT above means this also covers re-logins where
	// the actor already exists; ON CONFLICT DO NOTHING in the helper
	// makes that case a no-op for already-seeded rows.
	if err := app.seedNeedRowsIfMissing(r.Context(), tx, actorID.String); err != nil {
		log.Printf("pc/create seed actor_need rows for %s: %v", actorID.String, err)
		jsonError(w, "Failed to create PC", http.StatusInternalServerError)
		return
	}
	// Seed a 1-day starter nights_stay row at the picked lodging
	// structure's keeper (ZBBS-WORK-204). Best-effort: if no lodging
	// is placed (test env) or no keeper sells nights_stay yet
	// (operator hasn't seeded the inn keeper), the PC is created
	// without lodger status. They can still walk into the village,
	// they just don't have a room until they pay one of the keepers
	// directly. The NOT EXISTS guard makes the seed idempotent on
	// /pc/create re-calls — no duplicate starter row when the PC
	// already has an active nights_stay ledger somewhere.
	if lodgingID.Valid && lodgingID.String != "" {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO pay_ledger (
			    buyer_id, seller_id, item_kind, qty, offered_amount,
			    quoted_unit_amount, consume_now, state, message,
			    ready_by, fulfillment_status, delivered_on, resolved_at
			 )
			 SELECT $1::uuid, keeper.id, 'nights_stay', 1, 0,
			        0, false, 'accepted', 'pc-create starter',
			        CURRENT_DATE, 'delivered', NOW(), NOW()
			   FROM actor keeper
			  WHERE keeper.work_structure_id = $2::uuid
			    AND keeper.llm_memory_agent IS NOT NULL
			    AND EXISTS (
			        SELECT 1 FROM actor_inventory ki
			         WHERE ki.actor_id = keeper.id
			           AND ki.item_kind = 'nights_stay'
			    )
			    AND NOT EXISTS (
			        SELECT 1 FROM pay_ledger pl
			         WHERE pl.buyer_id = $1::uuid
			           AND pl.item_kind = 'nights_stay'
			           AND pl.state = 'accepted'
			           AND pl.fulfillment_status = 'delivered'
			    )
			  ORDER BY keeper.created_at ASC
			  LIMIT 1`,
			actorID.String, lodgingID.String,
		); err != nil {
			log.Printf("pc/create seed nights_stay starter for %s: %v", actorID.String, err)
			// Soft-fail: PC is still created, just without a starter row.
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("pc/create commit: %v", err)
		jsonError(w, "Failed to create PC", http.StatusInternalServerError)
		return
	}

	// Broadcast pc_appeared when the create landed a sprite (either fresh
	// or re-set). Other connected clients render the PC from this single
	// event. Skipped when sprite_id is still null — nothing to render.
	if prevSpriteID.Valid {
		app.broadcastPCAppeared(r.Context(), actorID.String, prevSpriteID.String, req.CharacterName)
	}

	log.Printf("pc/create %s -> '%s' (lodging %v, sprite %v)", user.Username, req.CharacterName, lodgingID.String, prevSpriteID.String)
	w.WriteHeader(http.StatusNoContent)
}

// handlePCSprite — set or change the PC's render sprite. Same shape as
// the admin-only handleSetNPCSprite, but scoped to the authenticated
// player's own row (no admin role required, no path id — the session
// identifies which actor to update).
//
// Body: {sprite_id}. Validates the sprite exists, updates actor, and
// broadcasts pc_sprite_changed so every connected client re-renders the
// PC without a follow-up fetch.
func (app *App) handlePCSprite(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		SpriteID string `json:"sprite_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.SpriteID = strings.TrimSpace(req.SpriteID)
	if req.SpriteID == "" {
		jsonError(w, "sprite_id is required", http.StatusBadRequest)
		return
	}

	// Catalog FK pre-check so a typo returns 400 not 500.
	var spriteCount int
	if err := app.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM npc_sprite WHERE id = $1`, req.SpriteID,
	).Scan(&spriteCount); err != nil || spriteCount == 0 {
		jsonError(w, "Unknown sprite_id", http.StatusBadRequest)
		return
	}

	// Update + return id + display_name in one round-trip so the broadcast
	// payload is complete without a second query. Errors on missing PC row
	// (player called /sprite before /create) — the bootstrap should always
	// /create first, so this surfaces a client-side bug rather than
	// silently no-op'ing.
	var actorID, charName sql.NullString
	if err := app.DB.QueryRow(r.Context(),
		`UPDATE actor SET sprite_id = $1
		 WHERE login_username = $2
		 RETURNING id::text, display_name`,
		req.SpriteID, user.Username,
	).Scan(&actorID, &charName); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "PC not found — call /pc/create first", http.StatusNotFound)
			return
		}
		log.Printf("pc/sprite update: %v", err)
		jsonError(w, "Failed to update sprite", http.StatusInternalServerError)
		return
	}

	app.broadcastPCAppeared(r.Context(), actorID.String, req.SpriteID, charName.String)
	log.Printf("pc/sprite %s -> %s", user.Username, req.SpriteID)
	w.WriteHeader(http.StatusNoContent)
}

// handlePCMove — click-to-walk endpoint for the village viewer. Body:
// {target_x, target_y, speed?, target_structure_id?}. Resolves the
// session to the PC's actor.id, then walks. Two modes:
//
//   - Raw coords: {target_x, target_y}. Walk to that tile, no inside
//     flip on arrival. Used when the click lands on open ground.
//
//   - Structure: {target_structure_id}. Walk to the structure's door
//     (entry allowed) or loiter slot (knocked or no-entry). On arrival,
//     setNPCInside fires only when the policy permits this PC to enter
//     so the PC joins the scene_huddle and the talk panel can open.
//     Owner-only structures the PC isn't associated with resolve as a
//     knock — the response carries knock_narration the client renders
//     in the talk panel. Used when the client hit-detects a structure
//     under the click. target_x/y are ignored when target_structure_id
//     is set.
//
// Structure mode routes through startReturnWalk so the existing
// arrival-hook (advanceBehavior) handles the inside flip — same path
// NPC scheduler arrivals take. Raw mode stays on startNPCWalk since
// no post-arrival inside flip is needed.
func (app *App) handlePCMove(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		TargetX           float64 `json:"target_x"`
		TargetY           float64 `json:"target_y"`
		Speed             float64 `json:"speed,omitempty"`
		TargetStructureID string  `json:"target_structure_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var actorID string
	var curX, curY float64
	var pcInside bool
	var pcInsideID sql.NullString
	var pcCurrentHuddle sql.NullString
	var pcDisplayName string
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text, current_x, current_y, inside,
		        inside_structure_id::text,
		        current_huddle_id::text,
		        display_name
		   FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID, &curX, &curY, &pcInside, &pcInsideID, &pcCurrentHuddle, &pcDisplayName); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "PC not found — call /pc/create first", http.StatusNotFound)
			return
		}
		log.Printf("pc/move actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	app.touchPCInput(r.Context(), actorID)

	// Departure narration. If the PC is currently inside a structure and
	// is about to walk somewhere else, broadcast a room_event of kind
	// "departure" to the room they're leaving. Symmetric with the agent
	// move_to commit's departure broadcast in executeAgentCommit. Without
	// this, a PC walking out of the tavern leaves no trace in the room
	// log other agents (and other PCs in the same room) can see.
	if pcInside && pcInsideID.Valid {
		structName := app.lookupStructureName(r.Context(), pcInsideID.String)
		if structName == "" {
			structName = "the building"
		}
		text := fmt.Sprintf("%s left the %s.", pcDisplayName, structName)
		data := map[string]interface{}{
			"actor_id":     actorID,
			"actor_name":   pcDisplayName,
			"kind":         "departure",
			"text":         text,
			"structure_id": pcInsideID.String,
			"at":           time.Now().UTC().Format(time.RFC3339),
		}
		app.addRoomScopeToData(r.Context(), data, actorID)
		app.Hub.Broadcast(WorldEvent{Type: "room_event", Data: data})
	}

	// Service-huddle cleanup (ZBBS-101). A PC who isn't physically inside
	// any structure but holds a current_huddle_id is in a "service huddle"
	// joined via knock — clear it on every new walk so the conversation
	// dissolves when the PC chooses to walk away. PC moves that arrive
	// inside a structure (entry_policy='anyone' or owner-self) re-form
	// the huddle through the existing setNPCInside path on arrival, so
	// this cleanup doesn't tear down a normal indoor huddle.
	if !pcInside && pcCurrentHuddle.Valid {
		app.leaveHuddleForPC(r.Context(), user.Username)
	}

	speed := req.Speed
	if speed <= 0 {
		speed = defaultNPCSpeed
	}

	// Structure mode: resolve the click to a door tile (enter) or a
	// loiter slot (stand outside / knock). Resolution by entry_policy
	// (ZBBS-101):
	//   - 'none'   → loiter slot, no inside flip.
	//   - 'anyone' → door tile, inside flip on arrival.
	//   - 'owner'  → if the PC's actor has this structure as home or
	//                work, treat as 'anyone' (door + enter). Otherwise
	//                walk to the loiter slot; the response carries
	//                knocked=true so the client can render the knock
	//                affordance.
	if req.TargetStructureID != "" {
		var ox, oy float64
		var loiterX, loiterY sql.NullInt32
		var doorX, doorY sql.NullInt32
		var footprintBottom int
		var entryPolicy string
		err := app.DB.QueryRow(r.Context(),
			`SELECT o.x, o.y,
			        o.loiter_offset_x, o.loiter_offset_y,
			        a.door_offset_x, a.door_offset_y, a.footprint_bottom,
			        o.entry_policy
			   FROM village_object o
			   JOIN asset a ON a.id = o.asset_id
			  WHERE o.id::text = $1`,
			req.TargetStructureID,
		).Scan(&ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom, &entryPolicy)
		if err != nil {
			if err == sql.ErrNoRows {
				jsonError(w, "Structure not found", http.StatusNotFound)
				return
			}
			log.Printf("pc/move structure lookup: %v", err)
			jsonError(w, "Internal error", http.StatusInternalServerError)
			return
		}

		// Owner check for 'owner' policy. The same actor.id linkage
		// (home_structure_id / work_structure_id) is used for NPCs in
		// agentMoveShouldEnter — keep the rule single-sourced in
		// concept even though the queries are duplicated for context.
		isAssociated := false
		if entryPolicy == "owner" {
			var n int
			if err := app.DB.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM actor
				  WHERE id::text = $1
				    AND (home_structure_id::text = $2 OR work_structure_id::text = $2)`,
				actorID, req.TargetStructureID,
			).Scan(&n); err != nil {
				log.Printf("pc/move owner check: %v", err)
				jsonError(w, "Internal error", http.StatusInternalServerError)
				return
			}
			isAssociated = n > 0
		}

		const tileSize = 32.0
		var walkX, walkY float64
		canEnter := (entryPolicy == "anyone" || (entryPolicy == "owner" && isAssociated)) && doorX.Valid && doorY.Valid
		// ZBBS-133: closed-door gate. If a vendor at this structure is on
		// break, and the PC isn't exempt (home/work match or active
		// lodger), the door is locked — drop them at the loiter slot
		// like the knock case so the client can render the closed sign
		// instead of pulling them through the door.
		closedDoorLocked := false
		if canEnter {
			allowed, err := app.canEnter(r.Context(), actorID, req.TargetStructureID)
			if err != nil {
				log.Printf("pc/move closed-door check: %v (allowing entry)", err)
			} else if !allowed {
				canEnter = false
				closedDoorLocked = true
			}
		}
		// "knocked" means the PC is approaching a door they can't walk
		// through. Two cases land here: (1) entry_policy='owner' and
		// the PC isn't associated, the original ZBBS-101 case; (2) the
		// closed-door lock dropped canEnter, the ZBBS-133 case. Both
		// should form a knock-huddle so the keeper inside hears the
		// PC at the door — without that the lodger-booking flow has
		// no way to start when the keeper is on break (you can't
		// become a lodger while locked out).
		knocked := (entryPolicy == "owner" && !isAssociated) || closedDoorLocked
		log.Printf("knock-trace pc=%s structure=%s entry_policy=%s isAssociated=%v canEnter=%v knocked=%v",
			actorID, req.TargetStructureID, entryPolicy, isAssociated, canEnter, knocked)
		if canEnter {
			walkX = ox + float64(doorX.Int32)*tileSize
			walkY = oy + float64(doorY.Int32)*tileSize
		} else {
			lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
			walkX, walkY = app.pickVisitorSlot(r.Context(), actorID, ox, oy, lx, ly)
		}

		npc := &behaviorNPC{ID: actorID, CurX: curX, CurY: curY}
		if err := app.startReturnWalk(r.Context(), npc, walkX, walkY, req.TargetStructureID, "pc-move", canEnter); err != nil {
			if err.Error() == "no path" {
				jsonError(w, "No path to target", http.StatusBadRequest)
				return
			}
			log.Printf("pc/move structure walk: %v", err)
			jsonError(w, "Internal error", http.StatusInternalServerError)
			return
		}

		// Movement fatigue (ZBBS-123). PCs accrue tiredness on walk-commit
		// just like NPCs. curX/curY here is the pre-walk position read at
		// the top of the handler — no interpolation needed since PC walks
		// are user-initiated and don't preempt an in-flight walk.
		//
		// Detached context with a 2s timeout: the walk has already been
		// committed via startReturnWalk, so a client disconnect right
		// after the response is composed shouldn't cancel the fatigue
		// accrual mid-flight. 2s is generous — the actual write is a
		// single tx with one SELECT + one UPDATE — but bounded so we
		// never leak the tx if the DB is wedged.
		fatCtx, fatCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer fatCancel()
		app.applyMovementFatigue(fatCtx, actorID, curX, curY, walkX, walkY)

		// Knock (ZBBS-101): PC clicked an owner-only structure they don't
		// belong to. Compose narration now and, when an associated NPC is
		// currently inside, join the PC into the structure's scene_huddle
		// so the talk panel becomes visible with the vendor as an
		// addressee. The PC stays physically outside (inside=false) — the
		// huddle is the conversational scope, not a presence flip. PC's
		// next /pc/move dissolves it via the service-huddle cleanup at
		// the top of this handler.
		var knockNarration string
		var knockHuddleJoined bool
		if knocked {
			var insideAssociated int
			if err := app.DB.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM actor
				  WHERE inside = true
				    AND inside_structure_id::text = $1
				    AND (home_structure_id::text = $1 OR work_structure_id::text = $1)`,
				req.TargetStructureID,
			).Scan(&insideAssociated); err != nil {
				log.Printf("pc/move knock huddle precheck: %v", err)
			} else if insideAssociated > 0 {
				log.Printf("knock-trace precheck pc=%s structure=%s insideAssociated=%d (proceed to join)", actorID, req.TargetStructureID, insideAssociated)
				if huddleID, err := app.joinOrCreateHuddleForPC(r.Context(), user.Username, req.TargetStructureID); err != nil {
					log.Printf("pc/move knock huddle join: %v", err)
				} else {
					knockHuddleJoined = true
					log.Printf("knock-trace huddle-joined pc=%s structure=%s huddle=%s", actorID, req.TargetStructureID, huddleID)
					// Sync any inside-the-structure actors into this huddle.
					// Covers two cases: (1) the historical pgx.ErrNoRows bug
					// prevented joinOrCreateHuddle from creating huddles for
					// NPCs who entered before the fix — they sit inside with
					// current_huddle_id=NULL and need adoption now;
					// (2) defense in depth so the talk-panel huddle_members
					// query is never empty when a vendor is physically there.
					if _, err := app.DB.Exec(r.Context(),
						`UPDATE actor SET current_huddle_id = $1::uuid
						  WHERE inside = true
						    AND inside_structure_id::text = $2
						    AND (current_huddle_id IS NULL OR current_huddle_id::text != $1)`,
						huddleID, req.TargetStructureID,
					); err != nil {
						log.Printf("pc/move knock huddle sync inside actors: %v", err)
					}
					app.fireKnockPerception(r.Context(), actorID, huddleID, req.TargetStructureID)
				}
			}
			if !knockHuddleJoined {
				log.Printf("knock-trace no-huddle pc=%s structure=%s (insideAssociated=0 or join failed) — narration only", actorID, req.TargetStructureID)
				knockNarration = app.composeKnockNarration(r.Context(), req.TargetStructureID)
			}
		}

		// Outdoor loiter-point huddle formation. When the PC walks to a
		// structure's loiter slot (canEnter=false) and there are
		// agentized NPCs already at that ring, form/join the structure's
		// huddle so the talk panel opens with the NPCs as addressees.
		// Same machinery as the knock huddle above, just triggered by
		// loiter co-location instead of inside-the-structure NPCs.
		// Covers entry_policy='none' structures (well, lamp post,
		// market square) where there's no inside flip but real
		// conversations should still be possible — Jeff arrived next to
		// Prudence at the well and had no UI surface to talk to her
		// before this fix.
		//
		// Skipped when the knock branch already formed a huddle (an
		// owner-policy door knock with the keeper inside doesn't also
		// need a loiter huddle).
		if !canEnter && !knockHuddleJoined && loiterX.Valid && loiterY.Valid {
			lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
			loiterCenterX := ox + float64(lx)*tileSize
			loiterCenterY := oy + float64(ly)*tileSize
			// Loiter ring slots are within 32 px Chebyshev of the loiter
			// center; 64 px tolerance covers slot snap and minor drift.
			var atLoiterCount int
			if err := app.DB.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM actor
				  WHERE llm_memory_agent IS NOT NULL
				    AND inside_structure_id IS NULL
				    AND GREATEST(ABS(current_x - $1), ABS(current_y - $2)) <= 64`,
				loiterCenterX, loiterCenterY,
			).Scan(&atLoiterCount); err != nil {
				log.Printf("pc/move loiter huddle precheck: %v", err)
			} else if atLoiterCount > 0 {
				if huddleID, err := app.joinOrCreateHuddleForPC(r.Context(), user.Username, req.TargetStructureID); err != nil {
					log.Printf("pc/move loiter huddle join: %v", err)
				} else {
					knockHuddleJoined = true
					log.Printf("loiter-huddle pc=%s structure=%s huddle=%s nearby_npcs=%d", actorID, req.TargetStructureID, huddleID, atLoiterCount)
					// Sync NPCs at the loiter ring into this huddle so
					// the talk-panel members query lands them as
					// addressees. Same predicate as the precheck — only
					// outdoor agentized NPCs within 64 px Chebyshev.
					if _, err := app.DB.Exec(r.Context(),
						`UPDATE actor SET current_huddle_id = $1::uuid
						  WHERE llm_memory_agent IS NOT NULL
						    AND inside_structure_id IS NULL
						    AND GREATEST(ABS(current_x - $2), ABS(current_y - $3)) <= 64
						    AND (current_huddle_id IS NULL OR current_huddle_id::text != $1)`,
						huddleID, loiterCenterX, loiterCenterY,
					); err != nil {
						log.Printf("pc/move loiter huddle sync nearby actors: %v", err)
					}
					// Same arrival fanout as the door knock: the NPC
					// gets a "X approaches the well and waits at the
					// loiter slot" perception line and a forced tick.
					app.fireKnockPerception(r.Context(), actorID, huddleID, req.TargetStructureID)
				}
			}
		}

		jsonResponse(w, http.StatusOK, map[string]any{
			"ok":              true,
			"structure":       true,
			"knocked":         knocked,
			"knock_narration": knockNarration,
			"huddle_joined":   knockHuddleJoined,
		})
		return
	}

	// Raw-coord mode (clicking on open ground): walk to the tile, no
	// arrival inside-flip. startNPCWalk surfaces typed-string errors
	// that the HTTP layer translates to user-readable codes.
	//
	// Clear any prior route the PC might still have from a superseded
	// structure-click. Without this, walk #2 (raw-coord click)'s
	// arrival fires advanceBehavior, which reads the stale route from
	// walk #1 (structure-click) and flips inside_structure_id to that
	// structure even though the PC is standing somewhere else. Symptom
	// observed: PC clicked Tavern, then re-clicked the Well 33s later;
	// the well-arrival cascade ran a tavern enter_huddle and the
	// tavernkeeper greeted the PC at the well. The route belongs to
	// the walk it was installed for; cancelling that walk should
	// dissolve the route too.
	app.clearBehavior(actorID)
	result, err := app.startNPCWalk(r.Context(), actorID, req.TargetX, req.TargetY, speed)
	if err != nil {
		if err.Error() == "npc not found" {
			jsonError(w, "PC actor missing", http.StatusInternalServerError)
			return
		}
		if err.Error() == "no path" {
			jsonError(w, "No path to target", http.StatusBadRequest)
			return
		}
		log.Printf("pc/move walk: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Movement fatigue (ZBBS-123). Raw-coord walks accrue fatigue same
	// as structure-mode walks above. Same detached-context rationale
	// — the walk committed in startNPCWalk above, fatigue shouldn't
	// unwind on a late client disconnect.
	fatCtx, fatCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer fatCancel()
	app.applyMovementFatigue(fatCtx, actorID, curX, curY, req.TargetX, req.TargetY)

	jsonResponse(w, http.StatusOK, result)
}

// fireKnockPerception writes an act-style perception row to
// agent_action_log and triggers immediate ticks on every agentized NPC
// inside the structure (typically the lone vendor). The row is shaped
// as an `act` so the perception loader at agent_tick.go:1219 renders
// it as "Mary approaches Ezekiel's Stall and waits at the door" in
// the vendor's next perception block — they get a concrete cue to
// react to without the PC having to type first.
//
// force=true on the immediate tick: PC-initiated, bypass the agent
// cost guard. sceneID is fresh per knock — the cascade is the knock
// itself; subsequent PC speech in the same conversation gets its own
// scene from handlePCSay's existing path.
func (app *App) fireKnockPerception(ctx context.Context, pcActorID, huddleID, structureID string) {
	var pcName, structureName, assetName string
	if err := app.DB.QueryRow(ctx,
		`SELECT a.display_name, COALESCE(o.display_name, ''), ast.name
		   FROM actor a, village_object o JOIN asset ast ON ast.id = o.asset_id
		  WHERE a.id::text = $1 AND o.id::text = $2`,
		pcActorID, structureID,
	).Scan(&pcName, &structureName, &assetName); err != nil {
		log.Printf("knock-perception lookup: %v", err)
		return
	}
	name := structureName
	if name == "" {
		name = assetName
	}

	payload, err := json.Marshal(map[string]interface{}{
		"text":         fmt.Sprintf("approaches %s and waits at the door", name),
		"structure_id": structureID,
	})
	if err != nil {
		log.Printf("knock-perception marshal: %v", err)
		return
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, huddle_id)
		 VALUES ($1, $2, 'player', 'act', $3, 'ok', $4::uuid)`,
		pcActorID, pcName, payload, huddleID,
	); err != nil {
		log.Printf("knock-perception audit insert: %v", err)
	}

	sceneID := app.newScene(ctx, structureID)
	log.Printf("knock-trace fireKnockPerception pc=%s structure=%s huddle=%s scene=%s — triggering co-located ticks",
		pcActorID, structureID, huddleID, sceneID)
	app.triggerCoLocatedTicks(ctx, structureID, "", "pc-knocked", true, sceneID, pcActorID)
}

// composeKnockNarration returns talk-panel text for a PC knock that
// produced no live conversation — i.e. nobody associated with the
// structure is currently inside. When someone IS inside, the click
// handler joins the PC into a service huddle instead and the talk
// panel surfaces the vendor as an addressee, so this narration is
// suppressed (returns ""). Only the unattended path needs explanatory
// text.
func (app *App) composeKnockNarration(ctx context.Context, structureID string) string {
	var displayName, assetName string
	if err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.display_name, ''), a.name
		   FROM village_object o JOIN asset a ON a.id = o.asset_id
		  WHERE o.id::text = $1`,
		structureID,
	).Scan(&displayName, &assetName); err != nil {
		return ""
	}
	name := displayName
	if name == "" {
		name = assetName
	}

	// Break-aware variant: if a would-be associated NPC (this structure is
	// their home or work) is currently on a take_break (break_until in the
	// future), surface that. ZBBS-102 split break_until from
	// agent_override_until so a routine move_to (which bumps override)
	// doesn't misrepresent the vendor as on break.
	var vendorName sql.NullString
	var breakUntil sql.NullTime
	if err := app.DB.QueryRow(ctx,
		`SELECT a.display_name, a.break_until
		   FROM actor a
		  WHERE (a.home_structure_id::text = $1 OR a.work_structure_id::text = $1)
		    AND a.break_until IS NOT NULL
		    AND a.break_until > NOW()
		  ORDER BY a.break_until DESC
		  LIMIT 1`,
		structureID,
	).Scan(&vendorName, &breakUntil); err == nil && breakUntil.Valid {
		who := strings.TrimSpace(vendorName.String)
		if who == "" {
			who = "the keeper"
		}
		return fmt.Sprintf("%s has stepped away — expected back around %s.",
			who, breakUntil.Time.Local().Format("3:04 PM"))
	}

	return fmt.Sprintf("%s stands unattended. No one is here.", name)
}

// broadcastPCAppeared emits pc_appeared with the full sprite catalog row
// and current world position inlined, so a fresh client can render the
// PC's AnimatedSprite2D without a follow-up REST fetch. Same event fires
// for first-time appearance and subsequent sprite swaps — the client's
// pc_appeared handler treats "already rendered" as a sprite swap and
// "not yet rendered" as a fresh add.
//
// Best-effort: a missing catalog row or position lookup logs and ships
// the partial payload; the next /pc/me poll or WS reconnect resync will
// fill any gaps.
// fetchDwellingAttributes returns the distinct attributes the actor
// is currently recovering via dwell — one entry per attribute with a
// non-stale actor_dwell_credit row whose object_id matches the
// actor's current location. Used by /pc/me to drive the HUD pulse
// (ZBBS-HOME-218) so the player sees the recovery glow as soon as
// their session reads state, even if no value-change has been
// detected client-side.
//
// "Non-stale" = last_credited_at + dwell_period_minutes >= NOW().
// The dwell sweep deletes rows when the actor walks away from the
// loiter slot, so under normal operation a row is either fresh
// (within its period) or gone. The freshness guard is belt-and-
// suspenders against the window before the next sweep.
//
// ZBBS-HOME-239: position filter — without it, the row stays "fresh"
// for the full dwell_period_minutes after the actor walks off
// (10 min for tree, 2 min for stew), so the HUD keeps strobing long
// after recovery has actually stopped. The dwell tick already does
// the position check + delete on the next overdue tick; this filter
// just hides the stale row from /pc/me until then so the pulse
// fades on the next poll (~10s) instead of waiting for the dwell
// period to expire.
//
// object_id semantics:
//   - source='object' (tree, well, etc.): object_id IS the loiter
//     structure. Match against resolved current loiter pin.
//   - source='item' (stew at the tavern, apple at fruit stand):
//     object_id is the structure where the item was consumed
//     (resolveLoiterStructureLocked or the seller's
//     work_structure_id). Match against either the actor's
//     inside_structure_id (indoor consume) or their resolved
//     loiter pin (outdoor consume).
//
// Empty slice (not nil) on success-with-no-rows so the JSON encodes
// as `[]` and the client sees an unambiguous "not dwelling" signal.
func (app *App) fetchDwellingAttributes(ctx context.Context, actorID string, x, y float64, insideStructureID string) ([]string, error) {
	loiterID, _ := app.resolveLoiteringStructure(ctx, x, y)
	if loiterID == "" && insideStructureID == "" {
		return []string{}, nil
	}
	rows, err := app.DB.Query(ctx, `
		SELECT DISTINCT attribute
		  FROM actor_dwell_credit
		 WHERE actor_id = $1::uuid
		   AND last_credited_at + (dwell_period_minutes || ' minutes')::interval >= NOW()
		   AND (object_id::text = NULLIF($2, '') OR object_id::text = NULLIF($3, ''))
	`, actorID, loiterID, insideStructureID)
	if err != nil {
		return []string{}, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var attr string
		if err := rows.Scan(&attr); err != nil {
			return []string{}, err
		}
		out = append(out, attr)
	}
	return out, rows.Err()
}

func (app *App) broadcastPCAppeared(ctx context.Context, actorID, spriteID, characterName string) {
	// display_name (rather than character_name) so the broadcast lines up
	// with the npc_created shape that world.gd's add_npc_from_broadcast
	// already consumes — letting one client-side render path handle both.
	data := map[string]interface{}{
		"id":           actorID,
		"sprite_id":    spriteID,
		"display_name": characterName,
	}
	if sprites, err := app.loadNPCSprites(ctx); err == nil {
		if sp, ok := sprites[spriteID]; ok {
			data["sprite"] = sp
		}
	} else {
		log.Printf("broadcastPCAppeared: sprite catalog load failed: %v", err)
	}
	// Position + presence so a fresh client renders at the right tile
	// with the right facing on the first frame. inside_structure_id
	// drives the inside-hide logic in world.gd's NPC renderer.
	var x, y float64
	var facing string
	var inside bool
	var insideID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y, facing, inside, inside_structure_id::text
		   FROM actor WHERE id = $1`,
		actorID,
	).Scan(&x, &y, &facing, &inside, &insideID); err == nil {
		data["current_x"] = x
		data["current_y"] = y
		data["facing"] = facing
		data["inside"] = inside
		if insideID.Valid {
			data["inside_structure_id"] = insideID.String
		}
	} else {
		log.Printf("broadcastPCAppeared: position lookup failed: %v", err)
	}
	app.Hub.Broadcast(WorldEvent{Type: "pc_appeared", Data: data})
}

// handlePCSay — addressed speech. Two writes happen:
//   1. /v1/chat/send to the addressee for the inline LLM reply.
//   2. agent_action_log + WS broadcast so others in the room
//      overhear (same path as /pc/speak's broadcast).
// This is NOT a private whisper — that name was misleading. It's
// directed in-room speech.
func (app *App) handlePCSay(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Target string `json:"target"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Target == "" || req.Text == "" {
		jsonError(w, "target and text are required", http.StatusBadRequest)
		return
	}

	// Verify the target is an agentized NPC. Non-agent villagers (npc rows
	// without llm_memory_agent) are physically present but conversationally
	// invisible — addressing them silently fails because there's no virtual
	// agent on the memory-api side to receive the chat.
	var hasAgent bool
	if err := app.DB.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM actor WHERE llm_memory_agent = $1)`,
		req.Target,
	).Scan(&hasAgent); err != nil {
		log.Printf("pc/say target check: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !hasAgent {
		jsonError(w, "target is not addressable", http.StatusBadRequest)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		jsonError(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}

	// Look up PC's actor.id + display_name + structure for the audit-log
	// overhear. After ZBBS-084 the PC has its own actor row, so the audit
	// trail can record actor_id properly (was NULL pre-refactor when PCs
	// lived in pc_position and weren't rows in npc).
	var actorID, charName, structureID sql.NullString
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text, display_name, inside_structure_id::text
		   FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID, &charName, &structureID); err != nil {
		log.Printf("pc/say lookup actor: %v", err)
	}
	if actorID.Valid {
		app.touchPCInput(r.Context(), actorID.String)
	}

	// Audit-log entry for the room to overhear. Includes target_name in
	// payload so future perception passes can render "Jefferey said to
	// John Ellis: '...'" instead of an unaddressed line. Best-effort:
	// failure here doesn't block the chat.
	if charName.Valid && structureID.Valid {
		auditMap := map[string]interface{}{
			"text":         req.Text,
			"structure_id": structureID.String,
			"target_name":  req.Target,
		}
		// Stamp room_id into the audit payload so the talk-panel backload
		// query can scope to the same room the PC is currently in. Pairs
		// with loadRecentSpeechAtScope's `payload->>'room_id'` filter.
		roomScope := app.actorPrivateRoomScope(r.Context(), actorID.String)
		if roomScope != "" {
			auditMap["room_id"] = roomScope
		}
		audit, _ := json.Marshal(auditMap)
		if _, err := app.DB.Exec(r.Context(),
			`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, huddle_id)
			 VALUES ($1, $2, 'player', 'speak', $3, 'ok',
			         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
			actorID, charName.String, audit,
		); err != nil {
			log.Printf("pc/say audit insert: %v", err)
		}
		// ZBBS-WORK-214 Phase 2 — same as /pc/speak: record salient
		// speech facts on shared-VA peers' relationship rows.
		app.recordSpeechInteractions(r.Context(), actorID.String, charName.String, req.Text, time.Now())
		spokeData := map[string]interface{}{
			"npc_id":       actorID.String,
			"name":         charName.String,
			"text":         req.Text,
			"at":           time.Now().UTC().Format(time.RFC3339),
			"kind":         "player",
			"structure_id": structureID.String,
		}
		if roomScope != "" {
			spokeData["room_id"] = roomScope
		}
		app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: spokeData})
	}

	// from_agent is required for user-session auth (not auto-derived
	// like it is for agent API key auth). Set it to the authenticated
	// user's actor name so the chat is recorded as from the player.
	body, _ := json.Marshal(map[string]interface{}{
		"from_agent": user.Username,
		"to_agents":  []string{req.Target},
		"message":    req.Text,
		"wait":       true,
	})

	upstreamURL := strings.TrimRight(app.LLMMemoryURL, "/") + "/v1/chat/send"
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", upstreamURL, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", authHeader)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		jsonError(w, "Upstream chat unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)

	log.Printf("pc/say %s -> %s: %.60q (status %d)", user.Username, req.Target, req.Text, resp.StatusCode)
}

// handlePCSpeak — broadcast to everyone in the PC's current huddle, or
// to nearby outdoor PCs when the speaker has no huddle and is outside.
// Records as agent_action_log row with source='player', action_type='speak',
// speaker_name=character_name.
//
// Reaction cascades:
//   - Indoor speech triggers event-tick on co-located agentized NPCs
//     (force=true, bypassing the cost guard) and a chronicler scene.
//   - Outdoor speech triggers agentized NPCs within 6 tiles (192 px
//     Chebyshev) of the speaker WHOSE FIRST NAME APPEARS IN THE
//     SPEECH. "Hi Prudence" at the well wakes Prudence; ambient
//     "good evening" with no name wakes nobody. Outdoor speech is
//     direct-address by definition, and the name filter prevents the
//     loiter-point echo where multiple NPCs around a shared resource
//     all reply to a single "hello." No chronicler — open-village
//     chat stays cheap; the chronicler's natural unit is the indoor
//     scene.
func (app *App) handlePCSpeak(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		jsonError(w, "text is required", http.StatusBadRequest)
		return
	}

	// Single actor read carrying both the conversational scope (the
	// huddle's structure) and the "am I inside?" signal. Indoor speech
	// routes through the huddle (booth/loiter joins use this — see
	// ZBBS-101 — and the huddle scope, not inside_structure_id, is what
	// the talk panel filters on). Outdoor speech (no huddle and no
	// inside_structure_id) routes through a position-stamped proximity
	// broadcast: other PCs filter by Chebyshev distance from their own
	// tile, so the audience is "anyone within ~6 tiles" without needing
	// a formal outdoor huddle.
	var actorID, charName, huddleStructureID, insideStructureID sql.NullString
	var currentX, currentY float64
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.id::text, pc.display_name,
		        sh.structure_id::text,
		        pc.inside_structure_id::text,
		        pc.current_x, pc.current_y
		   FROM actor pc
		   LEFT JOIN scene_huddle sh ON sh.id = pc.current_huddle_id
		  WHERE pc.login_username = $1`,
		user.Username,
	).Scan(&actorID, &charName, &huddleStructureID, &insideStructureID, &currentX, &currentY)
	if err != nil || !actorID.Valid || !charName.Valid {
		jsonError(w, "No character", http.StatusBadRequest)
		return
	}
	app.touchPCInput(r.Context(), actorID.String)

	indoor := huddleStructureID.Valid
	outdoor := !huddleStructureID.Valid && !insideStructureID.Valid
	if !indoor && !outdoor {
		// Inside a structure but no huddle: the booth/loiter join path
		// should have placed them in one. Treat as a real error rather
		// than silently downgrading to "outdoor proximity speech."
		jsonError(w, "Not in a huddle — nobody to hear you", http.StatusBadRequest)
		return
	}

	structureID := ""
	if indoor {
		structureID = huddleStructureID.String
	}

	payloadMap := map[string]interface{}{
		"text":         req.Text,
		"structure_id": structureID,
		"x":            currentX,
		"y":            currentY,
	}
	// Stamp room_id into the audit payload so the talk-panel backload
	// query scopes to the same room when the PC is in a private bedroom.
	if roomScope := app.actorPrivateRoomScope(r.Context(), actorID.String); roomScope != "" {
		payloadMap["room_id"] = roomScope
	}
	payload, _ := json.Marshal(payloadMap)

	// Outdoor row gets huddle_id=NULL via the subquery (current_huddle_id
	// is NULL for the actor); indoor row gets the active huddle. One
	// statement covers both paths.
	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, huddle_id)
		 VALUES ($1, $2, 'player', 'speak', $3, 'ok',
		         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
		actorID, charName.String, payload,
	); err != nil {
		log.Printf("pc/speak audit insert: %v", err)
		jsonError(w, "Failed to log speech", http.StatusInternalServerError)
		return
	}

	// ZBBS-WORK-214 Phase 2: record this speech as a salient interaction
	// on each (PC, peer) actor_relationship row where the peer is
	// shared-VA-backed. The PC themselves isn't shared-VA so their own
	// row writes get gated out — only the peer's row toward the PC is
	// written. Errors are logged inside the helper; nothing blocks
	// here.
	app.recordSpeechInteractions(r.Context(), actorID.String, charName.String, req.Text, time.Now())

	// kind="player" matches the audit row's source='player' and the
	// talk panel's render-as-player branch. speaker_x/speaker_y let
	// outdoor recipients filter by tile distance; indoor recipients
	// ignore them (structure_id already scopes the audience).
	spokeData := map[string]interface{}{
		"npc_id":       actorID.String,
		"name":         charName.String,
		"text":         req.Text,
		"at":           time.Now().UTC().Format(time.RFC3339),
		"kind":         "player",
		"structure_id": structureID,
		"speaker_x":    currentX,
		"speaker_y":    currentY,
	}
	if roomScope := app.actorPrivateRoomScope(r.Context(), actorID.String); roomScope != "" {
		spokeData["room_id"] = roomScope
	}
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: spokeData})

	if indoor {
		// PC-initiated → force=true so cost guard doesn't suppress
		// reactions. Storm risk is bounded by human typing speed.
		//
		// Cascade origin (MEM-121): mint a fresh scene UUID. Every NPC's
		// reaction tick to this PC speech, every nested speak fan-out from
		// those ticks, will inherit the same UUID. Walks initiated during
		// reactions don't carry it forward — when the NPC arrives somewhere
		// later, that arrival is its own new scene.
		app.triggerCoLocatedTicks(context.Background(), structureID, "", fmt.Sprintf("pc-spoke (%s)", charName.String), true, app.newScene(context.Background(), structureID), actorID.String)
	} else {
		// Outdoor speech: trigger agentized NPCs within 6-tile Chebyshev
		// (192 px) of the speaker WHOSE FIRST NAME APPEARS IN THE SPEECH.
		// "Hi Prudence" at the well wakes Prudence; "good evening" at the
		// well, with no name, wakes nobody. Cuts the loiter-point echo
		// where 3 NPCs around a shared resource all reply to a single
		// "hello" — outdoor speech is direct-address by definition.
		//
		// Match logic mirrors findVocativeStaleAddressees: case-sensitive
		// whole-word match on the actor's first display-name token. Same
		// rationale (case-insensitive matched verbs like "hope" against
		// "Hope James").
		//
		// force=true matches the indoor branch: a co-located NPC who
		// just ticked shouldn't be cost-gated out of replying when a
		// player addresses them by name. Storm risk bounded by the
		// proximity radius and the name match.
		//
		// Chronicler fire stays OFF for outdoor speech — keeps casual
		// passing-through chat cheap. Indoor scenes are the chronicler's
		// natural unit; the open village isn't.
		const outdoorProximityPx = 192.0
		rows, err := app.DB.Query(context.Background(),
			`SELECT id::text, display_name FROM actor
			  WHERE llm_memory_agent IS NOT NULL
			    AND inside_structure_id IS NULL
			    AND id::text != $1
			    AND GREATEST(ABS(current_x - $2), ABS(current_y - $3)) <= $4`,
			actorID.String, currentX, currentY, outdoorProximityPx)
		if err != nil {
			log.Printf("pc/speak outdoor proximity query: %v", err)
		} else {
			type candidate struct {
				ID, Name string
			}
			var candidates []candidate
			for rows.Next() {
				var c candidate
				if err := rows.Scan(&c.ID, &c.Name); err != nil {
					continue
				}
				candidates = append(candidates, c)
			}
			rows.Close()

			var sceneID string
			reason := fmt.Sprintf("pc-spoke-outdoor (%s)", charName.String)
			for _, c := range candidates {
				first := strings.SplitN(strings.TrimSpace(c.Name), " ", 2)[0]
				if first == "" {
					continue
				}
				re, err := regexp.Compile(`\b` + regexp.QuoteMeta(first) + `\b`)
				if err != nil {
					continue
				}
				if !re.MatchString(req.Text) {
					continue
				}
				if sceneID == "" {
					sceneID = app.newScene(context.Background(), "")
				}
				go app.triggerImmediateTick(context.Background(), c.ID, reason, true, sceneID, actorID.String)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// pcPayRequest mirrors the fields the NPC pay tool accepts so the
// client → server contract is symmetric. Recipient is the merchant or
// NPC being paid; amount is the negotiated total in coins (not
// per-unit). Optional item/qty/consume_now/for behave exactly as in
// the NPC tool.
type pcPayRequest struct {
	Recipient  string `json:"recipient"`
	Amount     int    `json:"amount"`
	ForText    string `json:"for,omitempty"`
	Item       string   `json:"item,omitempty"`
	Qty        int      `json:"qty,omitempty"`
	ConsumeNow *bool    `json:"consume_now,omitempty"`
	Consumers  []string `json:"consumers,omitempty"` // Phase C: at-source group orders
	// InResponseTo (ZBBS-128 step 3) — when paying after a vendor
	// counters, the client passes the parent ledger row id here so the
	// new pay extends the haggling chain (parent_id + depth+1). Optional.
	InResponseTo int64 `json:"in_response_to,omitempty"`
}

type pcPayResponse struct {
	Result        string `json:"result"`
	Error         string `json:"error,omitempty"`
	BuyerNewCoins int    `json:"buyer_new_coins"`
	// LedgerID + CounterAmount + Message (ZBBS-128 step 3) surface the
	// pay_ledger row id and the recipient's counter for the haggling
	// UI. Populated for result=countered so a Godot client can offer
	// "pay 5 instead?" without re-prompting from scratch; LedgerID is
	// also populated for declined/accepted as audit context. Zero /
	// empty for paths that didn't reach the ledger insert (early arg
	// rejections before pre-Tx-A).
	//
	// ZBBS-129 step 2 dropped item_transferred / item_consumed —
	// pay-accept no longer transfers items or applies consumption.
	// Inventory and consumption events now fire at deliver_order time
	// via the npc_delivered / actor_inventory_changed / npc_needs_changed
	// broadcasts the Godot client already listens for.
	LedgerID      int64  `json:"ledger_id,omitempty"`
	CounterAmount int    `json:"counter_amount,omitempty"`
	Message       string `json:"message,omitempty"`
}

// handlePCPay routes a player's pay request through the same
// executePay path NPCs use. The PC's actor row carries the coins
// column; on success the buyer's coins decrement, the recipient's
// increment, and any optional item/consumption side-effect lands the
// same way it would for an NPC buyer. Audit row written here (mirrors
// the NPC dispatch in executeAgentCommit), and the standard npc_paid
// + room_event broadcasts fan out so the room view shows the
// transaction.
func (app *App) handlePCPay(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req pcPayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Look up the PC's actor row. We need id, display_name, coins, and
	// the position fields executePay reads on its agentNPCRow argument
	// (it reuses the same struct for buyers regardless of PC/NPC).
	// Need values come from actor_need (ZBBS-121 commit 4) via a
	// follow-up snapshot call, since the columns are scheduled to drop
	// in commit 6.
	var actor agentNPCRow
	err := app.DB.QueryRow(r.Context(),
		`SELECT id::text, display_name, coins,
		        current_x, current_y, inside_structure_id
		   FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actor.ID, &actor.DisplayName, &actor.Coins,
		&actor.CurrentX, &actor.CurrentY, &actor.InsideStructureID,
	)
	if err == sql.ErrNoRows {
		jsonError(w, "No character", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("pc/pay actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	app.touchPCInput(r.Context(), actor.ID)
	// ZBBS-129 step 2: needs snapshot is no longer required here. It
	// previously informed executePay's HungerReduction display field,
	// which moved to executeDeliverOrder along with the consumption
	// itself. applyConsumption locks and reads under its own tx when
	// delivery actually fires.

	consumeNow := true
	if req.ConsumeNow != nil {
		consumeNow = *req.ConsumeNow
	}

	payReq := payRequest{
		RecipientName: req.Recipient,
		Amount:        req.Amount,
		ForText:       req.ForText,
		Item:          req.Item,
		Qty:           req.Qty,
		ConsumeNow:    consumeNow,
		ConsumerNames: req.Consumers,
		InResponseTo:  req.InResponseTo,
	}
	pr := app.executePay(r.Context(), &actor, payReq)
	// ZBBS-WORK-215 Phase 2B: pay event hook — same gate as NPC path.
	// Buyer is the PC (no llm_memory_agent → buyer-side write skipped),
	// seller's row is written when shared-VA-backed.
	app.recordPayInteractions(r.Context(), actor.ID, pr.RecipientID, payReq, pr)

	// Mirror executeAgentCommit's audit + room_event broadcast for the
	// pay path so PC pays surface in the talk panel and recent-events
	// readbacks the same way NPC pays do. Failed pays still get an
	// audit row so the trail is complete; rejection messages are
	// returned to the client for UI feedback.
	payload := map[string]interface{}{
		"recipient":   req.Recipient,
		"amount":      req.Amount,
		"for":         req.ForText,
		"item":        req.Item,
		"qty":         req.Qty,
		"consume_now": consumeNow,
	}
	if len(req.Consumers) > 0 {
		payload["consumers"] = req.Consumers
	}
	if actor.InsideStructureID.Valid {
		payload["structure_id"] = actor.InsideStructureID.String
	}
	payloadJSON, _ := json.Marshal(payload)
	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, error, huddle_id)
		 VALUES ($1, $2, 'player', 'pay', $3, $4, NULLIF($5, ''),
		         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
		actor.ID, actor.DisplayName, payloadJSON, pr.Result, pr.Err,
	); err != nil {
		log.Printf("pc/pay audit insert: %v", err)
	}

	if pr.Result == "ok" {
		// Effective structure scope for the room_event broadcast and
		// the post-pay reactor tick. Indoor PC → inside_structure_id.
		// Outdoor PC in a knock-huddle (e.g. paid for nights_stay
		// while standing at the closed tavern's loiter slot, ZBBS-117
		// knock-on-closed-door) → huddle's structure_id. Without the
		// huddle fallback, the reactor tick is skipped — the keeper
		// never sees the pay, never calls deliver_order, the lodger
		// never gets checked in.
		effectiveStructureID := ""
		if actor.InsideStructureID.Valid {
			effectiveStructureID = actor.InsideStructureID.String
		} else {
			var huddleStructure sql.NullString
			if err := app.DB.QueryRow(r.Context(),
				`SELECT sh.structure_id::text
				   FROM scene_huddle sh
				   JOIN actor a ON a.current_huddle_id = sh.id
				  WHERE a.id::text = $1 AND sh.concluded_at IS NULL`,
				actor.ID).Scan(&huddleStructure); err == nil && huddleStructure.Valid {
				effectiveStructureID = huddleStructure.String
			}
		}
		if effectiveStructureID != "" {
			text := narratePay(actor.DisplayName, payload)
			if text != "" {
				data := map[string]interface{}{
					"actor_id":     actor.ID,
					"actor_name":   actor.DisplayName,
					"kind":         "pay",
					"text":         text,
					"structure_id": effectiveStructureID,
					"at":           time.Now().UTC().Format(time.RFC3339),
				}
				app.addRoomScopeToData(r.Context(), data, actor.ID)
				app.Hub.Broadcast(WorldEvent{Type: "room_event", Data: data})
			}
			// ZBBS-129 step 2: the felt-language consume narration that used
			// to fire here (private "you eat the stew" line) moved to
			// executeDeliverOrder, since consumption now happens at deliver
			// time, not pay-accept. The Godot client's brown-box renders the
			// same room_event kind="consume" private payload from the new
			// callsite.
			// Post-pay reactor tick (ZBBS-126). Give the recipient a chance
			// to acknowledge the transaction — "thanks, friend" / "enjoy the
			// stew" / "come again." Without this hook the room goes silent
			// after a PC pay even though a vendor would naturally respond.
			// Mints a fresh scene UUID like the PC-speak path; force=true
			// because the customer just handed over coins, this isn't a
			// background reaction we should cost-guard. Goroutine because
			// the LLM call shouldn't block the PC's pay HTTP response.
			if pr.RecipientIsAgent && pr.RecipientID != "" {
				structureID := effectiveStructureID
				recipientID := pr.RecipientID
				pcActorID := actor.ID
				go func() {
					bg := context.Background()
					app.triggerImmediateTick(bg, recipientID, "pc-paid-you", true, app.newScene(bg, structureID), pcActorID)
				}()
			}
		}
	}

	jsonResponse(w, http.StatusOK, pcPayResponse{
		Result:        pr.Result,
		Error:         pr.Err,
		BuyerNewCoins: pr.BuyerNewCoins,
		LedgerID:      pr.LedgerID,
		CounterAmount: pr.CounterAmount,
		Message:       pr.Message,
	})
}

// pcSleepResponse carries the outcome of /pc/sleep. wake_at is the
// computed next-dawn timestamp for clients that want to render a
// countdown ("Sleeping at the Tavern, wake at 06:00"). Empty/zero on
// rejection or when the PC was already sleeping.
type pcSleepResponse struct {
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
	WakeAt string `json:"wake_at,omitempty"`
}

// handlePCSleep beds the authenticated PC. Validates that they have
// active lodger status at their current inside_structure_id, then sets
// sleeping_until = next dawn and broadcasts pc_sleep_started. Returns
// the wake timestamp for client rendering.
//
// Rejection cases:
//   - PC is not inside any structure (no place to sleep).
//   - PC has no active lodger status at this structure (not paid up
//     OR keeper hasn't checked them in OR home_structure_id doesn't
//     match — wouldBeEvictionExempt covers all three).
//   - PC is already sleeping (no-op rather than failure; result=ok
//     but no wake_at to indicate "no fresh transition").
func (app *App) handlePCSleep(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var actorID string
	var insideStructureID sql.NullString
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text, inside_structure_id::text
		   FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID, &insideStructureID); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "No character", http.StatusBadRequest)
			return
		}
		log.Printf("pc/sleep actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	// ZBBS-150: stamp last_pc_input_at directly. touchPCInput would
	// also clear sleeping_until + broadcast pc_sleep_ended (input)
	// which would race executePCSleep below for an already-sleeping
	// PC who re-calls /sleep. The /sleep handler manages sleep state
	// authoritatively via executePCSleep. login_username gate matches
	// touchPCInput's PC-only contract — a future/admin caller passing
	// an NPC id can't accidentally stamp this column.
	if _, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET last_pc_input_at = NOW()
		  WHERE id = $1::uuid AND login_username IS NOT NULL`,
		actorID,
	); err != nil {
		log.Printf("pc/sleep stamp input: %v", err)
	}

	if !insideStructureID.Valid {
		jsonResponse(w, http.StatusOK, pcSleepResponse{
			Result: "rejected",
			Error:  "you must be inside a structure to sleep",
		})
		return
	}

	// ZBBS-150: PC must be in a private room WITH valid access to
	// sleep. Tightens the pre-ZBBS-150 wouldBeEvictionExempt gate so
	// /pc/sleep can't fire from the bar (common room) — matches
	// the autoBedIdleLodgers gate. wouldBeEvictionExempt's
	// home_structure_id / work_structure_id branches don't apply to
	// current PCs (no PC has a home or work structure assigned); if
	// that changes, this gate will need to widen accordingly.
	var canSleep bool
	if err := app.DB.QueryRow(r.Context(),
		// ZBBS-163 round-2: assert a.inside_structure_id matches the
		// joined room's structure_id as defensive depth — actor's
		// inside_room_id should always belong to a room in
		// inside_structure_id, but admin manipulation or partial
		// updates could leave them out of sync; this constraint
		// fails closed in that state. active=true keeps in lockstep
		// with ux_room_access_one_private_active.
		`SELECT EXISTS (
		    SELECT 1
		      FROM actor a
		      JOIN structure_room ss ON ss.id = a.inside_room_id
		      JOIN room_access sa
		        ON sa.room_id = a.inside_room_id
		       AND sa.actor_id = a.id
		     WHERE a.id = $1::uuid
		       AND a.inside_structure_id = ss.structure_id
		       AND ss.kind = 'private'
		       AND sa.active = true
		 )`,
		actorID,
	).Scan(&canSleep); err != nil {
		log.Printf("pc/sleep gate check: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !canSleep {
		jsonResponse(w, http.StatusOK, pcSleepResponse{
			Result: "rejected",
			Error:  "you need to be in a paid bedroom to sleep — speak to the keeper for a night's stay",
		})
		return
	}

	res, err := app.executePCSleep(r.Context(), actorID)
	if err != nil {
		log.Printf("pc/sleep execute: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	resp := pcSleepResponse{Result: "ok"}
	if !res.WakeAt.IsZero() {
		resp.WakeAt = res.WakeAt.Format(time.RFC3339)
	}
	jsonResponse(w, http.StatusOK, resp)
}

// handlePCWake clears the authenticated PC's sleeping_until and
// broadcasts pc_sleep_ended (reason "manual"). Idempotent — returns
// result=ok whether or not the PC was actually sleeping.
//
// No early-wake tiredness penalty in v1: the natural penalty is that
// world rotation hasn't fired yet, so resetSleptTiredness hasn't reset
// their tiredness yet. They wake with whatever they had at sleep time.
func (app *App) handlePCWake(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var actorID string
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "No character", http.StatusBadRequest)
			return
		}
		log.Printf("pc/wake actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	// ZBBS-150: stamp directly. touchPCInput would broadcast
	// pc_sleep_ended (input) which would preempt executePCWake's
	// authoritative reason "manual" broadcast. /wake is a deliberate
	// player action, not an incidental input-wake. login_username
	// gate matches touchPCInput's PC-only contract.
	if _, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET last_pc_input_at = NOW()
		  WHERE id = $1::uuid AND login_username IS NOT NULL`,
		actorID,
	); err != nil {
		log.Printf("pc/wake stamp input: %v", err)
	}

	if err := app.executePCWake(r.Context(), actorID); err != nil {
		log.Printf("pc/wake execute: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"result": "ok"})
}

// handlePCMoveRoom transitions the authenticated PC between
// rooms within their current structure. Accepts a 'kind' in the
// body — 'common' to saunter back down to the bar, 'private' to enter
// the bedroom you're a lodger in. ZBBS-149.
//
// Resolution: pick the first room_kind=$kind room in the PC's
// current structure that they have access to (canEnterRoom).
// Common always passes; private requires an unexpired access row from
// deliver_order(nights_stay). Staff requires work_structure_id match.
//
// Rejection cases:
//   - PC is not inside any structure (no room to move within).
//   - No room of the requested kind in this structure.
//   - PC has no access to any room of that kind here (e.g. asked
//     for 'private' without lodger status).
//
// On success: sets inside_room_id, broadcasts pc_room_changed.
// touchPCInput fires (this is real player intent, should clear AFK
// auto-bed timers — relevant once ZBBS-150 lands).
func (app *App) handlePCMoveRoom(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var actorID string
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, "No character", http.StatusBadRequest)
			return
		}
		log.Printf("pc/move-room actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	result, err := app.executePCMoveRoom(r.Context(), actorID, req.Kind)
	if err != nil {
		log.Printf("pc/move-room: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if result.Result == "ok" {
		app.touchPCInput(r.Context(), actorID)
		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"result":    "ok",
			"room_id":   result.RoomID,
			"room_name": result.RoomName,
		})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"result": result.Result,
		"error":  result.Err,
	})
}

// moveRoomResult is the structured outcome of executePCMoveRoom.
// "ok" means inside_room_id updated and pc_room_changed broadcast.
// "rejected" means the request was well-formed but couldn't proceed
// (no such kind, no access, PC moved mid-flight) — Err carries a
// human-readable reason. A non-nil error is reserved for unexpected
// DB failures the caller should surface as 500 / log loudly.
type moveRoomResult struct {
	Result   string
	Err      string
	RoomID   int64
	RoomName string
	RoomKind string
}

// executePCMoveRoom is the engine-internal transition between rooms
// within a PC's current structure. Mirrors the body of /pc/move-room
// without HTTP / auth / touchPCInput — touchPCInput is intentionally
// deferred to the HTTP handler so server-side callers (autoBedIdleLodgers
// in particular) don't reset the idle clock they're acting on.
//
// kind: "common" / "private" / "staff" — picks the first matching
// room in the PC's inside_structure_id where canEnterRoom passes.
// Idempotent when the PC is already in a room of the requested kind:
// the UPDATE re-applies inside_room_id with the same value, RowsAffected
// stays 1 (the PK match still triggers), pc_room_changed re-broadcasts.
// Callers that want to skip the no-op should compare the PC's current
// inside_room_id to the picked room first.
func (app *App) executePCMoveRoom(ctx context.Context, actorID string, kind string) (moveRoomResult, error) {
	if kind != "common" && kind != "private" && kind != "staff" {
		return moveRoomResult{Result: "rejected", Err: "kind must be 'common', 'private', or 'staff'"}, nil
	}
	var (
		insideStructureID sql.NullString
		insideRoomID      sql.NullInt64
		actorName         string
	)
	if err := app.DB.QueryRow(ctx,
		`SELECT inside_structure_id::text, inside_room_id, COALESCE(display_name, '')
		   FROM actor WHERE id = $1::uuid`,
		actorID,
	).Scan(&insideStructureID, &insideRoomID, &actorName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return moveRoomResult{Result: "rejected", Err: "no such actor"}, nil
		}
		return moveRoomResult{}, fmt.Errorf("actor lookup: %w", err)
	}
	if !insideStructureID.Valid {
		return moveRoomResult{Result: "rejected", Err: "you must be inside a structure to move between rooms"}, nil
	}

	// Enumerate candidate rooms in this structure of the requested
	// kind, then pick the first one canEnterRoom allows. Done in
	// two queries (list + per-row gate) rather than one fancy join so
	// the gate logic stays consistent with future kinds (e.g. a 'guest'
	// kind with personal invite lists) drop in cleanly.
	rows, err := app.DB.Query(ctx,
		`SELECT id, name FROM structure_room
		  WHERE structure_id = $1::uuid AND kind = $2
		  ORDER BY name ASC`,
		insideStructureID.String, kind,
	)
	if err != nil {
		return moveRoomResult{}, fmt.Errorf("candidate query: %w", err)
	}
	type cand struct {
		ID   int64
		Name string
	}
	var candidates []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			rows.Close()
			return moveRoomResult{}, fmt.Errorf("candidate scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if len(candidates) == 0 {
		return moveRoomResult{Result: "rejected", Err: "no " + kind + " room exists here"}, nil
	}

	var pickedID int64
	var pickedName string
	for _, c := range candidates {
		ok, err := app.canEnterRoom(ctx, actorID, c.ID)
		if err != nil {
			log.Printf("executePCMoveRoom canEnter %d: %v", c.ID, err)
			continue
		}
		if ok {
			pickedID = c.ID
			pickedName = c.Name
			break
		}
	}
	if pickedID == 0 {
		return moveRoomResult{Result: "rejected", Err: "you don't have access to any " + kind + " room here"}, nil
	}

	// Conditional UPDATE — guard on inside_structure_id so a concurrent
	// move/teleport/admin action between the candidate read and this
	// write doesn't land the PC in a room of a structure they've
	// already left. RowsAffected==0 means the PC moved between the
	// two queries; surface that as a soft rejection so the caller can
	// re-evaluate rather than retry blindly.
	tag, err := app.DB.Exec(ctx,
		`UPDATE actor
		    SET inside_room_id = $1
		  WHERE id = $2::uuid
		    AND inside_structure_id = $3::uuid`,
		pickedID, actorID, insideStructureID.String,
	)
	if err != nil {
		return moveRoomResult{}, fmt.Errorf("update: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return moveRoomResult{Result: "rejected", Err: "you moved before changing room — try again"}, nil
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "pc_room_changed",
		Data: map[string]interface{}{
			"actor_id":  actorID,
			"room_id":   pickedID,
			"room_kind": kind,
			"room_name": pickedName,
			"at":        time.Now().UTC().Format(time.RFC3339),
		},
	})

	// ZBBS-169 narration: when a PC transitions into their private
	// room (manually or via auto-bed), emit a public room_event so
	// anyone else still in the source area sees them retire. The
	// source-room kind drives whether this fires — moving private→
	// private (rare, multi-bedroom) or anything→common doesn't merit
	// the line. Skipped when no display_name (defensive — every PC
	// should have one).
	if kind == "private" && actorName != "" && pickedID != insideRoomID.Int64 {
		app.Hub.Broadcast(WorldEvent{
			Type: "room_event",
			Data: map[string]interface{}{
				"actor_id":     actorID,
				"actor_name":   actorName,
				"kind":         "move_room",
				"text":         fmt.Sprintf("%s heads to their room.", actorName),
				"structure_id": insideStructureID.String,
				"at":           time.Now().UTC().Format(time.RFC3339),
			},
		})
	}

	return moveRoomResult{Result: "ok", RoomID: pickedID, RoomName: pickedName, RoomKind: kind}, nil
}
