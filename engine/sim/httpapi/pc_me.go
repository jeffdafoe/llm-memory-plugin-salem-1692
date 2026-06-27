package httpapi

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_me.go — the PC bootstrap read. The v2 Godot client polls
// POST /api/village/pc/me to enter the village: the response tells it whether
// the caller's PC exists, where it is, what it's carrying, who it's talking to,
// and what's been said nearby. Until this route the v2 engine served no pc/me,
// so a human player could never bootstrap into v2 — this closes the keystone
// PC-surface parity gap.
//
// The response is built as a read over s.world.Published() (snapshot aggregate
// + DTO mapper, like agentsFromSnapshot). The one world mutation is a fire-and-
// forget presence stamp (StampPCSeen, ZBBS-WORK-326): this poll IS the PC's
// "still here" heartbeat, so the handler sends a tiny command to record it. The
// stamp is best-effort and never affects the response. The v1 handler
// (engine/pc_handlers.go handlePCMe) queried the live DB directly. Three pieces
// of v1's response are
// re-expressed in the v2-native shape rather than ported literally:
//
//   - Audience structure scope uses sim.ResolveLoiteringObject (the exported
//     v1 resolveLoiteringStructure port) instead of a hand-rolled loiter scan.
//   - Audience room scope (v1 actorPrivateRoomScope) resolves the PC's
//     InsideRoomID against its structure's Rooms on the snapshot — a private/
//     staff room scopes speech; common/outdoor/stale resolves to public.
//   - Recent-speech backload is huddle-scoped. v2's ActionLog is keyed by
//     HuddleID (it carries no structure/room coords and stores raw, un-rendered
//     Text), so this handler filters by the PC's current huddle and renders the
//     prose itself from ActionType + the speaker's DisplayName.
//
// POST (not GET) matches the v1 verb and the client (main.gd METHOD_POST). The
// body is ignored — there are no request parameters; the caller is identified
// by the authenticated session.

// pcRecentSpeechLimit / pcRecentSpeechCutoff bound the talk-panel backload: at
// most N entries within the last cutoff window, so a quiet huddle doesn't dredge
// up day-old chatter and an active one only surfaces the recent thread. Mirrors
// the v1 constants.
const (
	pcRecentSpeechLimit  = 20
	pcRecentSpeechCutoff = 24 * time.Hour
	// pcOutdoorRosterTiles is the Chebyshev radius for the outdoor proximity
	// roster (no huddle, outdoors). Matches v1's GREATEST(ABS dx, ABS dy) <= 6
	// and the client's outdoor npc_spoke distance filter so the launcher chip
	// strip and the speech audience agree on "near."
	pcOutdoorRosterTiles = 6
)

// pcMeResponse is the PC bootstrap payload. Field names + json tags mirror the
// v1 pcMeResponse so the existing Godot client (main.gd / talk_panel.gd /
// inventory_panel.gd) consumes it unchanged. x/y are TILE coordinates (the v2
// wire contract — same as AgentDTO; the client converts to world pixels via
// VillageApi.tile_to_world), NOT v1's pixel floats.
type pcMeResponse struct {
	LoginUsername string `json:"login_username"`
	// CanEdit gates the client's editor + config tools. It mirrors the
	// operator capability (plugins/administer) — the same gate requireOperator
	// uses for the umbilical. The Godot client reads this on token-verify
	// (auth.gd) to show/hide the edit UI; it is identity-level, so it's set
	// whether or not the caller has a PC yet.
	CanEdit bool `json:"can_edit"`
	Exists  bool `json:"exists"`
	// ActorID is the PC's actor id, surfaced so the client can recognize its
	// own PC in WS broadcasts (npc_arrived etc., which carry actor id, not
	// login). Empty when the PC hasn't been created yet.
	ActorID           string  `json:"actor_id,omitempty"`
	CharacterName     string  `json:"character_name,omitempty"`
	X                 int     `json:"x"`
	Y                 int     `json:"y"`
	InsideStructureID *string `json:"inside_structure_id,omitempty"`
	// AudienceStructureID scopes the talk panel. Indoors it equals
	// InsideStructureID; loitering at a booth/doorway it's the nearest named
	// object's loiter pin within AudienceScopeTiles while InsideStructureID
	// stays nil. Absent when in transit / open road.
	AudienceStructureID *string `json:"audience_structure_id,omitempty"`
	// AudienceRoomID pairs with AudienceStructureID to scope the talk panel to
	// one subspace: set to the PC's room id only when in a private/staff room,
	// nil for common-room or outdoor PCs (public scope).
	AudienceRoomID  *string          `json:"audience_room_id,omitempty"`
	StructureName   string           `json:"structure_name,omitempty"`
	HomeStructureID *string          `json:"home_structure_id,omitempty"`
	HomeName        string           `json:"home_name,omitempty"`
	CurrentHuddleID *string          `json:"current_huddle_id,omitempty"`
	HuddleMembers   []pcHuddleMember `json:"huddle_members"`
	// DormantMembers lists co-present actors that are asleep (ZBBS-WORK-427) —
	// the talk panel renders them as passive "(asleep)" chips so an indoor
	// sleeper, whose map sprite is hidden behind the structure, is still legible.
	// SEPARATE from HuddleMembers on purpose: a sleeper is out of the audience
	// (scope 1), so it must never enter the talk/pay target roster.
	DormantMembers []pcHuddleMember   `json:"dormant_members,omitempty"`
	RecentSpeech   []pcRecentSpeech   `json:"recent_speech,omitempty"`
	Coins          int                `json:"coins"`
	Inventory      []pcInventoryEntry `json:"inventory"`
	// Needs is the PC's current need snapshot (keys hunger/thirst/tiredness).
	// Always a non-nil map so it serializes as {} (not null) for a PC with no
	// need rows yet.
	Needs map[string]int `json:"needs"`
	// NeedThresholds is the engine's per-need red-line so the HUD colors with
	// the same boundaries the engine uses. Omitted when unset (client falls
	// back to its own defaults).
	NeedThresholds map[string]int `json:"need_thresholds,omitempty"`
	// DwellingAttributes lists the needs the PC is actively recovering via a
	// non-stale dwell credit right now, so the HUD pulse engages immediately on
	// bootstrap rather than waiting for client-side decrease detection.
	DwellingAttributes []string `json:"dwelling_attributes,omitempty"`
	// SpriteID is nil until the player picks one — the client uses the nil
	// state to open the sprite picker on first login. Sprite inlines the
	// resolved catalog row (same shape as AgentDTO.Sprite) so a fresh client
	// can render the PC without a follow-up catalog fetch.
	SpriteID *string         `json:"sprite_id,omitempty"`
	Sprite   *AgentSpriteDTO `json:"sprite,omitempty"`
	// Lodging is the PC's active lodging when it holds a room (LLM-38) — so the
	// client can show a "lodged here" indicator and the Pay modal can explain an
	// empty quote list ("you already have a room…") instead of dead-ending.
	// Absent (omitempty) when the PC holds no active ledger grant.
	Lodging *pcLodgingDTO `json:"lodging,omitempty"`
}

// pcLodgingDTO is the PC's active-lodging surface (LLM-38).
type pcLodgingDTO struct {
	// InnName is the display name of the structure the held room is in.
	InnName string `json:"inn_name"`
	// KeeperName is the display name of the inn's keeper, when one works there —
	// so the client can tell whether the PC's own innkeeper is the co-present
	// pay recipient and scope the held-room empty-state to that case. Empty when
	// the inn has no resolvable keeper.
	KeeperName string `json:"keeper_name,omitempty"`
	// UntilLabel is a duration-voiced phrase for the time left on the stay
	// ("for about 2 more nights"), computed engine-side so the client needs no
	// timezone math — the snapshot carries no Location (perception is
	// duration-based for the same reason).
	UntilLabel string `json:"until_label"`
	// ExpiresAt is the held grant's expiry instant, surfaced raw (RFC3339) for
	// any future precise client use; the human-facing string is UntilLabel.
	ExpiresAt time.Time `json:"expires_at"`
}

// pcInventoryEntry is one stack of items the PC carries, enriched from the
// item-kind catalog so the UI renders "Bread" not "bread" and the pay modal
// can read capabilities without a second fetch.
type pcInventoryEntry struct {
	ItemKind     string   `json:"item_kind"`
	DisplayLabel string   `json:"display_label"`
	Quantity     int      `json:"quantity"`
	Category     string   `json:"category,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// pcHuddleMember is one co-present actor in the PC's conversational scope.
// Kind is "npc" or "pc"; TargetAgent carries the NPC's backing VA slug (the
// chat_send recipient) and is absent for PCs / VA-less actors.
type pcHuddleMember struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Role        *string `json:"role,omitempty"`
	TargetAgent *string `json:"target_agent,omitempty"`
	// Status marks a non-addressable rest state for the chip suffix — "asleep"
	// for a co-present sleeper carried in DormantMembers. Empty for ordinary
	// (addressable) HuddleMembers; the client renders the "(asleep)" suffix off it.
	Status string `json:"status,omitempty"`
}

// pcRecentSpeech is one historical entry backloaded into the talk panel. Kind
// drives client rendering: "speech_npc"/"speech_player" render as quoted,
// color-coded dialogue; every other kind ("act") renders as italic narration
// with the actor's name already embedded in Text.
type pcRecentSpeech struct {
	SpeakerName string    `json:"speaker_name"`
	Text        string    `json:"text"`
	Kind        string    `json:"kind"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// handlePCMe serves the PC bootstrap read for the caller's own PC, resolved
// from the authenticated session against the published snapshot. requireAuth
// has already populated the session AuthUser. A caller with no PC gets
// exists=false at 200 (the client branches on it to open creation/sprite flow).
func (s *Server) handlePCMe(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		// requireAuth always populates this; guard rather than nil-deref.
		writeAuthError(w, "invalid")
		return
	}

	snap := s.world.Published()
	resp := pcMeResponse{
		LoginUsername: user.Username,
		CanEdit:       hasPermission(user, permResourcePlugins, permActionAdminister),
		HuddleMembers: []pcHuddleMember{},
		Inventory:     []pcInventoryEntry{},
		Needs:         map[string]int{},
	}

	pcID, pc, ok := findPCSnapshotByLogin(snap, user.Username)
	if !ok {
		// No PC for this session yet — exists=false, 200. Empty huddle/
		// inventory/needs already set above so the shape is stable.
		resp.Exists = false
		writeJSON(w, resp)
		return
	}

	// Record this poll as a presence heartbeat (ZBBS-WORK-326): the client
	// polls /pc/me every 10s, so stamping LastPCSeenAt here is what keeps the PC
	// "present" — when the tab closes the polls stop and the presence sweep
	// reclaims the ghost. Best-effort: a stamp failure must not fail the read
	// (worst case the PC looks stale a beat longer). The id came off the snapshot;
	// StampPCSeen no-ops if the live actor is gone/non-PC, and stamps the
	// execution-time instant (not now) so a backed-up channel can't backdate it.
	if _, err := s.world.SendContext(r.Context(), sim.StampPCSeen(pcID)); err != nil {
		log.Printf("httpapi: pc/me presence stamp for %s failed: %v", pcID, err)
	}

	resp.Exists = true
	resp.ActorID = string(pcID)
	resp.CharacterName = pc.DisplayName
	resp.X, resp.Y = pc.Pos.X, pc.Pos.Y
	resp.Coins = pc.Coins

	if pc.InsideStructureID != "" {
		id := string(pc.InsideStructureID)
		resp.InsideStructureID = &id
		resp.StructureName = objectDisplayName(snap, pc.InsideStructureID)
	}
	if pc.HomeStructureID != "" {
		id := string(pc.HomeStructureID)
		resp.HomeStructureID = &id
		resp.HomeName = objectDisplayName(snap, pc.HomeStructureID)
	}
	if pc.CurrentHuddleID != "" {
		id := string(pc.CurrentHuddleID)
		resp.CurrentHuddleID = &id
	}

	// Resolve the conversational scope once and feed BOTH the talk-panel scope
	// field and the roster (ZBBS-HOME-378): a customer at an owner-only stall's
	// loiter point is scoped to that stall, so the roster must list the owner
	// working inside it — not fall through to the players-only outdoor roster.
	audienceStructureID, hasAudience := pcAudienceStructure(snap, pc, s.world.Assets)
	if hasAudience {
		resp.AudienceStructureID = &audienceStructureID
	}
	if room, ok := pcAudienceRoom(snap, pc); ok {
		resp.AudienceRoomID = &room
	}

	resp.HuddleMembers = pcHuddleRoster(snap, pc, pcID, audienceStructureID, snap.PublishedAt, sim.PCPresenceStaleAfter(s.world))
	resp.DormantMembers = pcDormantRoster(snap, pc, pcID, audienceStructureID)
	resp.RecentSpeech = pcRecentSpeechBackload(snap, pc, audienceStructureID)
	resp.Inventory = pcInventoryEntries(pc.Inventory, s.world.ItemKinds)

	for k, v := range pc.Needs {
		resp.Needs[string(k)] = v
	}
	if len(snap.NeedThresholds) > 0 {
		th := make(map[string]int, len(snap.NeedThresholds))
		for k, v := range snap.NeedThresholds {
			th[string(k)] = v
		}
		resp.NeedThresholds = th
	}
	resp.DwellingAttributes = pcDwellingAttributes(pc, snap.PublishedAt)

	if pc.SpriteID != "" {
		id := string(pc.SpriteID)
		resp.SpriteID = &id
		resp.Sprite = resolveAgentSprite(pc.SpriteID, s.world.Sprites)
	}

	resp.Lodging = pcLodgingSurface(snap, pc)

	writeJSON(w, resp)
}

// pcLodgingSurface returns the PC's active-lodging surface, or nil when the PC
// holds no active ledger grant. Selects the soonest-expiring grant (the stay
// the PC would renew first), resolves the inn via the room's structure, and
// voices the remaining time as a duration phrase. Mirrors the perception
// buildLodgingView grant selection on the httpapi side. LLM-38.
func pcLodgingSurface(snap *sim.Snapshot, pc *sim.ActorSnapshot) *pcLodgingDTO {
	if snap == nil || pc == nil {
		return nil
	}
	now := snap.PublishedAt
	var best *sim.RoomAccess
	for _, ra := range pc.RoomAccess {
		if !sim.IsActiveLedgerGrant(ra, now) {
			continue
		}
		if best == nil || ra.ExpiresAt.Before(*best.ExpiresAt) {
			best = ra
		}
	}
	if best == nil {
		return nil
	}
	innName := "the inn"
	keeperName := ""
	if sid, ok := structureIDForRoom(snap, best.RoomID); ok {
		if name := objectDisplayName(snap, sid); name != "" {
			innName = name
		}
		keeperName = lodgingKeeperName(snap, sid)
	}
	return &pcLodgingDTO{
		InnName:    innName,
		KeeperName: keeperName,
		UntilLabel: pcLodgingUntilLabel(best.ExpiresAt.Sub(now)),
		ExpiresAt:  *best.ExpiresAt,
	}
}

// lodgingKeeperName returns the display name of the actor working at
// structureID (its keeper), or "" when none. Deterministic smallest-id tiebreak
// for the rare multi-worker case, mirroring sim.keeperForStructure on the
// snapshot.
func lodgingKeeperName(snap *sim.Snapshot, structureID sim.StructureID) string {
	var bestID sim.ActorID
	var best *sim.ActorSnapshot
	for id, a := range snap.Actors {
		if a == nil || a.WorkStructureID != structureID {
			continue
		}
		if best == nil || id < bestID {
			bestID = id
			best = a
		}
	}
	if best == nil {
		return ""
	}
	return best.DisplayName
}

// structureIDForRoom finds the structure that declares roomID. RoomID is a
// globally unique per-instance id, so the first match is authoritative;
// ok=false when no structure declares it.
func structureIDForRoom(snap *sim.Snapshot, roomID sim.RoomID) (sim.StructureID, bool) {
	for id, s := range snap.Structures {
		if s == nil {
			continue
		}
		for _, r := range s.Rooms {
			if r != nil && r.ID == roomID {
				return id, true
			}
		}
	}
	return "", false
}

// pcLodgingUntilLabel voices the time left on a lodging grant as a duration
// phrase — no timezone, since the snapshot carries no Location. Three tiers
// matching the perception lodging cue (lodgingStatusLine).
func pcLodgingUntilLabel(d time.Duration) string {
	switch {
	case d <= 24*time.Hour:
		return "through the day"
	case d <= 48*time.Hour:
		return "through tomorrow"
	default:
		nights := int(d / (24 * time.Hour))
		return fmt.Sprintf("for about %d more nights", nights)
	}
}

// findPCSnapshotByLogin returns the PC actor (id + snapshot) whose LoginUsername
// matches loginUsername. Snapshot analog of write_handlers.go findPCByLogin —
// login_username is unique by construction (it mirrors the llm-memory-api
// account), so the first match is authoritative. ok=false when no PC matches.
func findPCSnapshotByLogin(snap *sim.Snapshot, loginUsername string) (sim.ActorID, *sim.ActorSnapshot, bool) {
	for id, a := range snap.Actors {
		if a == nil {
			continue
		}
		if a.Kind == sim.KindPC && a.LoginUsername == loginUsername {
			return id, a, true
		}
	}
	return "", nil, false
}

// objectDisplayName returns the display name of a placed village object (a
// building is a village object under the shared-identity bridge), or "" when
// the id doesn't resolve. Used for structure_name / home_name.
func objectDisplayName(snap *sim.Snapshot, id sim.StructureID) string {
	if o := snap.VillageObjects[sim.VillageObjectID(id)]; o != nil {
		return o.DisplayName
	}
	return ""
}

// pcAudienceStructure resolves the PC's conversational structure scope. Indoors
// it's the literal InsideStructureID; outdoors it's the nearest named object
// whose loiter pin is within AudienceScopeTiles of the PC's tile (the v1
// "loitering at a booth" 64px ring). ok=false when in transit (nothing in
// range and not inside) → the talk panel shows no room scope.
func pcAudienceStructure(snap *sim.Snapshot, pc *sim.ActorSnapshot, assets map[sim.AssetID]*sim.Asset) (string, bool) {
	if pc.InsideStructureID != "" {
		return string(pc.InsideStructureID), true
	}
	if id, ok := sim.ResolveLoiteringObject(snap.VillageObjects, assets, pc.Pos, sim.AudienceScopeTiles); ok {
		return string(id), true
	}
	return "", false
}

// pcAudienceRoom is the v2 snapshot port of v1 actorPrivateRoomScope: the PC's
// room id is the audience room ONLY when the PC is in a private/staff room that
// belongs to its current structure. Looking the room up in that structure's
// Rooms gives v1's structure-bound JOIN for free — a stale InsideRoomID that
// doesn't belong to the current structure simply isn't found and resolves to
// public scope (ok=false). Common rooms and outdoors are public too. Failing
// closed to public is the safe direction: over-scoped speech (heard a little
// more broadly) beats speech that vanishes for the right audience.
func pcAudienceRoom(snap *sim.Snapshot, pc *sim.ActorSnapshot) (string, bool) {
	if pc.InsideRoomID == 0 {
		return "", false
	}
	st := snap.Structures[pc.InsideStructureID]
	if st == nil {
		return "", false
	}
	for _, rm := range st.Rooms {
		if rm == nil || rm.ID != pc.InsideRoomID {
			continue
		}
		// Whitelist private/staff explicitly — an unrecognized kind (future
		// enum addition, corrupt data) under-scopes to public rather than
		// inventing a private bucket nobody can match. Mirrors v1's switch.
		if rm.Kind == sim.RoomKindPrivate || rm.Kind == sim.RoomKindStaff {
			return strconv.FormatInt(int64(pc.InsideRoomID), 10), true
		}
		return "", false
	}
	return "", false
}

// pcHuddleRoster lists the actors conversationally co-present with the PC,
// excluding the PC itself. When the PC is in a huddle, that's the huddle's
// members (the huddle IS the conversational pocket — membership is canonical on
// Huddle.Members). With no huddle and standing INSIDE a structure, it's that
// structure's conversational occupants. Outdoors with no huddle it's the nearby
// PCs within pcOutdoorRosterTiles — PLUS (ZBBS-HOME-378) the conversational
// occupants of an owner-only stall the PC is loitering at, so a customer outside
// a market stall sees the owner working within while still seeing nearby players.
// Sorted by name for a deterministic, stable response.
func pcHuddleRoster(snap *sim.Snapshot, pc *sim.ActorSnapshot, selfID sim.ActorID, audienceStructureID string, now time.Time, staleAfter time.Duration) []pcHuddleMember {
	out := []pcHuddleMember{}

	switch {
	case pc.CurrentHuddleID != "":
		hud := snap.Huddles[pc.CurrentHuddleID]
		if hud != nil {
			for mid := range hud.Members {
				if mid == selfID {
					continue
				}
				if m := snap.Actors[mid]; m != nil {
					out = append(out, pcHuddleMemberOf(m))
				}
			}
		}
	case pc.InsideStructureID != "":
		// Indoor roster: the conversational occupants of the structure the PC
		// stands in. Without this a PC standing in (say) the Tavern with NPCs
		// gets an empty roster, so the talk-panel launcher stays hidden
		// (talk_panel.gd gates it on huddle_members) and the player can never
		// speak — yet HOME-358's EnsureColocatedHuddle forms the real huddle only
		// ON the PC's first speak (the HOME-371 chicken-and-egg).
		out = append(out, structureConversationalOccupants(snap, string(pc.InsideStructureID), selfID, now, staleAfter)...)
	default:
		// Outdoor roster: nearby PCs, no last-seen filter (a logged-out PC
		// phantoms in until presence-staleness is its own feature — v1 accepts
		// the same).
		for id, a := range snap.Actors {
			if a == nil || id == selfID {
				continue
			}
			if a.LoginUsername == "" || a.InsideStructureID != "" {
				continue
			}
			if pc.Pos.Chebyshev(a.Pos) > pcOutdoorRosterTiles {
				continue
			}
			out = append(out, pcHuddleMember{Kind: "pc", Name: a.DisplayName})
		}
		// ZBBS-HOME-378: a customer loitering at an owner-only stall's loiter
		// point ALSO sees the owner working inside it. audienceStructureID is the
		// loiter-resolved stall (pcAudienceStructure); add its conversational
		// occupants — the read-path mirror of conversationalScopeStructure, whose
		// speak path forms the customer↔owner huddle on first address. A loitered
		// object with no occupants (a well, a sign) adds nothing, leaving the
		// nearby-PC roster intact. (Inside-occupants and outdoor PCs are disjoint
		// — InsideStructureID is set for one and empty for the other — so no dup.)
		if audienceStructureID != "" {
			out = append(out, structureConversationalOccupants(snap, audienceStructureID, selfID, now, staleAfter)...)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pcDormantRoster returns the co-present actors in the PC's conversational scope
// that are ASLEEP — surfaced to the talk panel as passive "(asleep)" chips
// (ZBBS-WORK-427) so an indoor sleeper, whose map sprite is hidden behind the
// structure, is still legible. Deliberately distinct from pcHuddleRoster: a
// sleeper is out of the audience (scope 1) and feeds neither the talk nor the pay
// target list, so it must NOT appear in HuddleMembers. Sleeping is an indoor
// state (StateSleeping is bedded-down indoors; outdoors the dormant state is
// StateResting, which stays an addressable, map-visible member), so only the
// structure-scoped cases can hold one: the PC's own structure and a loitered
// owner-only stall (audienceStructureID, deduped against InsideStructureID).
func pcDormantRoster(snap *sim.Snapshot, pc *sim.ActorSnapshot, selfID sim.ActorID, audienceStructureID string) []pcHuddleMember {
	out := []pcHuddleMember{}
	if pc.InsideStructureID != "" {
		out = append(out, structureSleepingOccupants(snap, string(pc.InsideStructureID), selfID)...)
	}
	if audienceStructureID != "" && audienceStructureID != string(pc.InsideStructureID) {
		out = append(out, structureSleepingOccupants(snap, audienceStructureID, selfID)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// structureSleepingOccupants returns the actors asleep inside structureID
// (excluding selfID) as roster members tagged Status "asleep" — the sleeping
// counterpart of structureConversationalOccupants: same structure-membership
// scoping, opposite state selection. A sleeper has already left any huddle on
// bed-down, so no huddle-membership filter is needed.
func structureSleepingOccupants(snap *sim.Snapshot, structureID string, selfID sim.ActorID) []pcHuddleMember {
	var out []pcHuddleMember
	for id, a := range snap.Actors {
		if a == nil || id == selfID {
			continue
		}
		if string(a.InsideStructureID) != structureID {
			continue
		}
		if a.State != sim.StateSleeping {
			continue
		}
		m := pcHuddleMemberOf(a)
		m.Status = "asleep"
		out = append(out, m)
	}
	return out
}

// structureConversationalOccupants returns talk-roster entries for the
// conversational actors INSIDE structureID (excluding selfID): a stateful/shared
// NPC or non-stale PC that is awake, and either unhuddled or already in THIS
// structure's huddle. An actor conversing in a DIFFERENT structure (a stale
// cross-structure back-ref, or a nil/missing huddle) is excluded — the speaker's
// join won't pull it in (ZBBS-HOME-363). Eligibility mirrors EnsureColocatedHuddle
// / colocatedConversationalActors so the roster never advertises a target the
// speak path won't include. Shared by the indoor roster and the HOME-378
// loiter-stall roster.
func structureConversationalOccupants(snap *sim.Snapshot, structureID string, selfID sim.ActorID, now time.Time, staleAfter time.Duration) []pcHuddleMember {
	var out []pcHuddleMember
	for id, a := range snap.Actors {
		if a == nil || id == selfID {
			continue
		}
		if string(a.InsideStructureID) != structureID {
			continue
		}
		if a.CurrentHuddleID != "" {
			h := snap.Huddles[a.CurrentHuddleID]
			if h == nil || string(h.StructureID) != structureID {
				continue
			}
		}
		if !snapshotConversational(a) {
			continue
		}
		if a.Kind == sim.KindPC && sim.PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
			continue // absent player — the speak path excludes stale PCs too
		}
		out = append(out, pcHuddleMemberOf(a))
	}
	return out
}

// snapshotConversational mirrors the non-presence part of
// sim.colocatedConversational against a read-path ActorSnapshot: a
// conversational kind (stateful/shared NPC or PC) that is not asleep;
// decorative NPCs and sleepers are excluded. PC presence-staleness is applied
// separately by callers (the indoor roster) that need parity with
// EnsureColocatedHuddle — see pcHuddleRoster.
func snapshotConversational(a *sim.ActorSnapshot) bool {
	switch a.Kind {
	case sim.KindNPCStateful, sim.KindNPCShared, sim.KindPC:
		return a.State != sim.StateSleeping
	default:
		return false // decorative / unknown
	}
}

// pcHuddleMemberOf maps an actor snapshot to a roster member. An actor with a
// LoginUsername is a PC; otherwise an NPC, whose backing VA slug (LLMAgent) is
// surfaced as the chat_send target. Role/TargetAgent are pointers (omitempty)
// so they're absent when unset.
func pcHuddleMemberOf(a *sim.ActorSnapshot) pcHuddleMember {
	m := pcHuddleMember{Kind: "npc", Name: a.DisplayName}
	if a.LoginUsername != "" {
		m.Kind = "pc"
	}
	if a.Role != "" {
		role := a.Role
		m.Role = &role
	}
	if a.LLMAgent != "" {
		agent := a.LLMAgent
		m.TargetAgent = &agent
	}
	return m
}

// pcRecentSpeechBackload renders the talk-panel backload from the action log.
// A huddled PC gets its huddle's thread (the conversation is the pocket); a
// huddle-less PC standing in a conversational scope gets the entries stamped
// with that STRUCTURE scope and a matching room subspace (ZBBS-HOME-437) — so
// walking into the Tavern after an NPC↔NPC sale still shows what the room
// recently heard, even though those huddles have concluded and their ids no
// longer resolve. A PC in a private/staff room only backloads that room's
// entries; a public-scope PC only public ones — mirroring the live npc_spoke
// filter. Out of any scope (open ground, in transit) → no backload; the panel
// fills from live WS instead.
// ActionLog is append-ordered (oldest→newest); the client appends in that order,
// so the last pcRecentSpeechLimit entries are returned oldest→newest.
func pcRecentSpeechBackload(snap *sim.Snapshot, pc *sim.ActorSnapshot, audienceStructureID string) []pcRecentSpeech {
	if pc.CurrentHuddleID == "" && audienceStructureID == "" {
		return nil
	}
	// The PC's room subspace for structure-scope matching: its room id when
	// that room is private/staff (pcAudienceRoom ok), else 0 = public.
	pcRoomScope := sim.RoomID(0)
	if _, ok := pcAudienceRoom(snap, pc); ok {
		pcRoomScope = pc.InsideRoomID
	}
	cutoff := snap.PublishedAt.Add(-pcRecentSpeechCutoff)
	out := []pcRecentSpeech{}
	for i := range snap.ActionLog {
		e := snap.ActionLog[i]
		if pc.CurrentHuddleID != "" {
			if e.HuddleID != pc.CurrentHuddleID {
				continue
			}
		} else if string(e.StructureID) != audienceStructureID || e.RoomID != pcRoomScope {
			continue
		}
		if e.OccurredAt.Before(cutoff) {
			continue
		}
		speaker, text, kind, ok := renderActionLogEntry(snap, e)
		if !ok {
			continue
		}
		out = append(out, pcRecentSpeech{
			SpeakerName: speaker,
			Text:        text,
			Kind:        kind,
			OccurredAt:  e.OccurredAt,
		})
	}
	if len(out) > pcRecentSpeechLimit {
		out = out[len(out)-pcRecentSpeechLimit:]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// renderActionLogEntry turns one ActionLogEntry into a talk-panel line:
// (speaker_name, text, kind, ok). Speech keeps its raw utterance and a speech_*
// kind (color-coded, quoted client-side); every other action renders as "act"
// narration with the actor's name embedded in the text (the client's narration
// path shows no separate name label, but still requires a non-empty speaker, so
// it's always set). ok=false when the entry can't render (unknown speaker, empty
// utterance/item) and is skipped.
func renderActionLogEntry(snap *sim.Snapshot, e sim.ActionLogEntry) (speaker, text, kind string, ok bool) {
	actor := snap.Actors[e.ActorID]
	if actor == nil || actor.DisplayName == "" {
		return "", "", "", false
	}
	name := actor.DisplayName
	switch e.ActionType {
	case sim.ActionTypeSpoke:
		if e.Text == "" {
			return "", "", "", false
		}
		k := "speech_npc"
		if actor.LoginUsername != "" {
			k = "speech_player"
		}
		return name, e.Text, k, true
	case sim.ActionTypePaid:
		// ZBBS-WORK-377: narrate recipient + amount, degrading gracefully —
		// "X pays Y N coins for Z." → "X pays Y N coins." → "X pays Y." →
		// "X makes a payment." (counterparty unknown).
		if e.CounterpartyName == "" {
			return name, name + " makes a payment.", "act", true
		}
		line := name + " pays " + e.CounterpartyName
		if e.Amount > 0 {
			line += " " + formatCoins(e.Amount)
		}
		if e.Text != "" {
			line += " for " + e.Text
		}
		return name, line + ".", "act", true
	case sim.ActionTypeConsumed:
		if e.Text == "" {
			return "", "", "", false
		}
		return name, name + " consumes " + e.Text + ".", "act", true
	case sim.ActionTypeDelivered:
		if e.Text == "" {
			return "", "", "", false
		}
		// ZBBS-HOME-432: a lodging delivery is a check-in, not a parcel
		// handoff — "delivers nights_stay to Jefferey" read as the keeper
		// handing over a sack labeled lodging. A qty-1 delivered Text is the
		// raw item kind verbatim (formatItemQty), so a catalog hit with the
		// lodging capability identifies it; multi-qty ("2x …") falls back to
		// the generic line.
		if def := snap.ItemKinds[sim.ItemKind(e.Text)]; def != nil && def.HasCapability("lodging") {
			if e.CounterpartyName != "" {
				return name, name + " shows " + e.CounterpartyName + " to a room — it's theirs for the night.", "act", true
			}
			return name, name + " readies a room for a guest.", "act", true
		}
		// ZBBS-WORK-377: name the recipient when known.
		line := name + " delivers " + e.Text
		if e.CounterpartyName != "" {
			line += " to " + e.CounterpartyName
		}
		return name, line + ".", "act", true
	case sim.ActionTypeWalked:
		// WithDefiniteArticle adds "the" for a common-noun place ("the Tavern") and
		// leaves possessives / already-articled names alone ("Hannah's Inn", "the
		// Village Well"). Kept in sync with the live arrival line (emitArrivalNarration).
		if e.Text != "" {
			return name, name + " arrives at " + sim.WithDefiniteArticle(e.Text) + ".", "act", true
		}
		return name, name + " arrives.", "act", true
	case sim.ActionTypeDeparted:
		// The inverse of ActionTypeWalked: Text is the structure the actor left.
		// Same article treatment, kept in sync with the live emitDepartureNarration line.
		if e.Text != "" {
			return name, name + " leaves " + sim.WithDefiniteArticle(e.Text) + ".", "act", true
		}
		return name, name + " leaves.", "act", true
	case sim.ActionTypeTookBreak:
		return name, name + " steps away.", "act", true
	default:
		return "", "", "", false
	}
}

// formatCoins renders a whole-coin amount for talk-panel narration, singularizing
// "1 coin". Caller gates on amount > 0 (zero means "no amount to show").
func formatCoins(n int) string {
	if n == 1 {
		return "1 coin"
	}
	return strconv.Itoa(n) + " coins"
}

// pcInventoryEntries maps the PC's item-kind→quantity map to enriched DTO
// entries, joining the catalog for display label / category / capabilities. A
// kind absent from the catalog still serializes (raw kind, no label). Sorted by
// item kind for a deterministic response; always non-nil so it's [] not null.
func pcInventoryEntries(inv map[sim.ItemKind]int, catalog map[sim.ItemKind]*sim.ItemKindDef) []pcInventoryEntry {
	out := make([]pcInventoryEntry, 0, len(inv))
	for kind, qty := range inv {
		e := pcInventoryEntry{ItemKind: string(kind), Quantity: qty}
		if def := catalog[kind]; def != nil {
			e.DisplayLabel = def.DisplayLabel
			e.Category = string(def.Category)
			e.Capabilities = def.Capabilities
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemKind < out[j].ItemKind })
	return out
}

// pcDwellingAttributes lists the distinct needs the PC is actively recovering
// via a non-stale dwell credit. "Non-stale" = LastCreditedAt within the credit's
// DwellPeriodMinutes window relative to the snapshot's publish time; an actor
// who walked away has the row deleted on the next dwell tick, so staleness is
// bounded. Sorted for determinism; nil (→ omitempty) when none.
func pcDwellingAttributes(pc *sim.ActorSnapshot, now time.Time) []string {
	seen := map[string]bool{}
	var attrs []string
	for _, dc := range pc.DwellCredits {
		if dc == nil || dc.DwellPeriodMinutes <= 0 {
			continue
		}
		window := time.Duration(dc.DwellPeriodMinutes) * time.Minute
		if now.Sub(dc.LastCreditedAt) > window {
			continue
		}
		a := string(dc.Attribute)
		if !seen[a] {
			seen[a] = true
			attrs = append(attrs, a)
		}
	}
	sort.Strings(attrs)
	return attrs
}
