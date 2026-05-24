package httpapi

import (
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
// This is a pure read over s.world.Published() — no command channel, no world
// mutation. The v1 handler (engine/pc_handlers.go handlePCMe) queried the live
// DB directly; v2's read idiom is a snapshot aggregate + a DTO mapper, exactly
// like agentsFromSnapshot in server.go. Three pieces of v1's response are
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
	Exists        bool   `json:"exists"`
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
	AudienceRoomID  *string            `json:"audience_room_id,omitempty"`
	StructureName   string             `json:"structure_name,omitempty"`
	HomeStructureID *string            `json:"home_structure_id,omitempty"`
	HomeName        string             `json:"home_name,omitempty"`
	CurrentHuddleID *string            `json:"current_huddle_id,omitempty"`
	HuddleMembers   []pcHuddleMember   `json:"huddle_members"`
	RecentSpeech    []pcRecentSpeech   `json:"recent_speech,omitempty"`
	Coins           int                `json:"coins"`
	Inventory       []pcInventoryEntry `json:"inventory"`
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

	if scope, ok := pcAudienceStructure(snap, pc, s.world.Assets); ok {
		resp.AudienceStructureID = &scope
	}
	if room, ok := pcAudienceRoom(snap, pc); ok {
		resp.AudienceRoomID = &room
	}

	resp.HuddleMembers = pcHuddleRoster(snap, pc, pcID)
	resp.RecentSpeech = pcRecentSpeechBackload(snap, pc)
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

	writeJSON(w, resp)
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
// Huddle.Members). When the PC is outdoors with no huddle, it's the nearby PCs
// within pcOutdoorRosterTiles, so the talk-panel launcher still has a roster to
// populate its chip strip. Sorted by name for a deterministic, stable response.
func pcHuddleRoster(snap *sim.Snapshot, pc *sim.ActorSnapshot, selfID sim.ActorID) []pcHuddleMember {
	out := []pcHuddleMember{}

	if pc.CurrentHuddleID != "" {
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
	} else if pc.InsideStructureID == "" {
		// Outdoor proximity roster: nearby PCs, no last-seen filter (a
		// logged-out PC will phantom in until presence-staleness is its own
		// feature — v1 accepts the same).
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
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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

// pcRecentSpeechBackload renders the talk-panel backload from the action log,
// scoped to the PC's current huddle. v2's ActionLog is huddle-keyed and stores
// raw Text, so this filters Snapshot.ActionLog by the PC's HuddleID within the
// cutoff window and renders each entry's prose here. A huddle-less PC (just
// arrived / outdoors) gets no backload and fills the panel from live WS instead.
// ActionLog is append-ordered (oldest→newest); the client appends in that order,
// so the last pcRecentSpeechLimit entries are returned oldest→newest.
func pcRecentSpeechBackload(snap *sim.Snapshot, pc *sim.ActorSnapshot) []pcRecentSpeech {
	if pc.CurrentHuddleID == "" {
		return nil
	}
	cutoff := snap.PublishedAt.Add(-pcRecentSpeechCutoff)
	out := []pcRecentSpeech{}
	for i := range snap.ActionLog {
		e := snap.ActionLog[i]
		if e.HuddleID != pc.CurrentHuddleID {
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
		if e.Text != "" {
			return name, name + " pays for " + e.Text + ".", "act", true
		}
		return name, name + " makes a payment.", "act", true
	case sim.ActionTypeConsumed:
		if e.Text == "" {
			return "", "", "", false
		}
		return name, name + " consumes " + e.Text + ".", "act", true
	case sim.ActionTypeDelivered:
		if e.Text == "" {
			return "", "", "", false
		}
		return name, name + " delivers " + e.Text + ".", "act", true
	case sim.ActionTypeWalked:
		if e.Text != "" {
			return name, name + " arrives at " + e.Text + ".", "act", true
		}
		return name, name + " arrives.", "act", true
	case sim.ActionTypeTookBreak:
		return name, name + " steps away.", "act", true
	default:
		return "", "", "", false
	}
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
