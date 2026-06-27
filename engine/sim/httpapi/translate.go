package httpapi

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// translate.go — the production EventTranslator: it maps the v2 sim event bus
// to client wire frames ({type, data}, matching the client's _handle_message
// dispatch). Pass TranslateEvent to NewHub.
//
// Mapped so far: MOVEMENT (the engine announces a walk with the full
// cost-weighted tile path it computed — npc_walking — and is authoritative on
// the outcome via npc_arrived / npc_move_stopped; the client follows the path
// tile by tile and snaps to the engine's arrival; broadcasting the path vs only
// the destination keeps road-preferring / building-avoiding routing on screen
// without the client re-implementing the cost model), SPEECH (Spoke →
// npc_spoke, the huddle-scoped utterance the player's pc/speak and NPC speak
// both produce), PHASE (PhaseApplied → world_phase_changed, the day/night
// boundary the client uses to flip its lighting), and OBJECT STATE
// (VillageObjectStateChanged → object_state_changed, an object's sprite/light
// flip — e.g. a lamp lighting at dusk), OBJECT REPOSITION/DELETE
// (VillageObjectMoved → object_moved and VillageObjectDeleted → object_deleted,
// the admin object write routes — a deleted object's cascade-removed overlays
// each emit their own object_deleted), and PAY-WITH-ITEM COMMERCE
// (PayOfferReceived → pay_offer, PayCountered → pay_countered,
// PayWithItemResolved → pay_resolved — the buyer-initiated offer lifecycle a PC
// buyer drives via pc/pay and observes resolve; scoped client-side off huddle_id
// like npc_spoke, since the hub broadcasts every frame to every viewer), and PC
// SLEEP (PCSleepStarted → pc_sleep_started, PCSleepEnded → pc_sleep_ended — the
// player-facing bed-down / wake transitions driven by idle auto-bed + the
// pc/sleep + pc/wake routes; the client filters to its own PC and raises/clears
// the sleep-fade overlay + top-bar chip), and PC LODGING RELOCATION
// (PCRelocatedToCommon → room_event — the private brown-panel narration shown
// when the lodging day-cycle moves a PC from a private room to the common room,
// on checkout eviction or morning descent), and INSIDE STATE
// (ActorInsideChanged → npc_inside_changed — the sprite visibility +
// see-through-structure stand-offset flip when an actor enters or leaves a
// structure, restoring the v1 broadcast the rewrite dropped, ZBBS-WORK-373).
// Per-tile ActorMoved is deliberately NOT mapped — it stays internal; nor are
// spawn/despawn or object create (no sim bus source until those write routes
// exist). An unmapped event returns ok=false and is dropped, so adding cases
// later needs no change here or in the hub. Wire shapes:
// shared/notes/codebase/salem-engine-v2/client-contract.

// TranslateEvent maps a sim.Event to a client WireFrame. ok=false drops the
// event (the common case — most bus events are engine-internal). Pure and
// non-blocking: it runs on the world goroutine via Hub.Handle.
func TranslateEvent(evt sim.Event) (WireFrame, bool) {
	switch e := evt.(type) {
	case *sim.ActorMoveStarted:
		path := make([]tilePointDTO, len(e.Path))
		for i, p := range e.Path {
			path[i] = tilePointDTO{X: p.X, Y: p.Y}
		}
		return WireFrame{Type: "npc_walking", Data: walkWireDTO{
			ID:          string(e.ActorID),
			Path:        path,
			DestKind:    string(e.DestinationKind),
			StructureID: string(e.StructureID),
			ObjectID:    string(e.ObjectID),
			AttemptID:   uint64(e.MovementAttemptID),
		}}, true
	case *sim.PCNeedsChanged:
		needs := make(map[string]int, len(e.Needs))
		for k, v := range e.Needs {
			needs[string(k)] = v
		}
		return WireFrame{Type: "pc_needs_changed", Data: pcNeedsChangedWireDTO{
			ActorID: string(e.ActorID),
			Needs:   needs,
		}}, true
	case *sim.ActorArrived:
		return WireFrame{Type: "npc_arrived", Data: arrivedWireDTO{
			ID:          string(e.ActorID),
			X:           e.FinalPosition.X,
			Y:           e.FinalPosition.Y,
			StructureID: string(e.FinalStructureID),
			AttemptID:   uint64(e.MovementAttemptID),
		}}, true
	case *sim.ActorMoveStopped:
		return WireFrame{Type: "npc_move_stopped", Data: moveStoppedWireDTO{
			ID:        string(e.ActorID),
			X:         e.Position.X,
			Y:         e.Position.Y,
			Reason:    string(e.Reason),
			AttemptID: uint64(e.MovementAttemptID),
		}}, true
	case *sim.ActorInsideChanged:
		return WireFrame{Type: "npc_inside_changed", Data: insideChangedWireDTO{
			ID:                string(e.ActorID),
			Inside:            e.InsideStructureID != "",
			InsideStructureID: string(e.InsideStructureID),
			X:                 e.X,
			Y:                 e.Y,
		}}, true
	case *sim.ActorTeleported:
		// An operator teleport (ZBBS-HOME-448) reuses the npc_arrived frame —
		// the client's authoritative snap-to-tile — so the viewer updates with
		// no client change. AttemptID 0: there was no movement attempt, and the
		// client's arrived handler snaps unconditionally without correlating it.
		return WireFrame{Type: "npc_arrived", Data: arrivedWireDTO{
			ID:          string(e.ActorID),
			X:           e.ToPosition.X,
			Y:           e.ToPosition.Y,
			StructureID: string(e.InsideStructureID),
			AttemptID:   0,
		}}, true
	case *sim.Spoke:
		// recipient_ids is the frame's RENDER audience: huddle members plus
		// the PC bystanders within earshot (ZBBS-HOME-437). The hub
		// broadcasts every frame to every client; this list is what a
		// client's talk panel checks itself against, so merging bystanders
		// here is what lets a player overhear room conversation without
		// being a huddle member. Engine-side speech consumers read the
		// EVENT's RecipientIDs and never see this merge.
		// Dedup while preserving order (code_review): pcBystanders never
		// overlaps RecipientIDs today, but the translator defines the wire
		// contract and can be fed arbitrary events — recipient_ids must
		// stay a set.
		recipients := make([]string, 0, len(e.RecipientIDs)+len(e.PCBystanderIDs))
		seen := make(map[sim.ActorID]struct{}, len(e.RecipientIDs)+len(e.PCBystanderIDs))
		for _, list := range [][]sim.ActorID{e.RecipientIDs, e.PCBystanderIDs} {
			for _, id := range list {
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				recipients = append(recipients, string(id))
			}
		}
		// Structured sale hints (ZBBS-WORK-400): forward the commit-time-
		// filtered mentions on the same wire fields the scene_quote frame
		// below uses, so a vendor's VERBAL offer feeds the PC Pay UI's
		// offer rows without a formal quote. Price 0 = "no price named" —
		// the item still lands in mentions, just with no mention_prices row.
		var mentionItems []string
		var mentionPrices map[string]int
		for _, m := range e.Mentions {
			mentionItems = append(mentionItems, string(m.Item))
			if m.Price > 0 {
				if mentionPrices == nil {
					mentionPrices = make(map[string]int, len(e.Mentions))
				}
				mentionPrices[string(m.Item)] = m.Price
			}
		}
		return WireFrame{Type: "npc_spoke", Data: spokeWireDTO{
			ID:            string(e.SpeakerID),
			HuddleID:      string(e.HuddleID),
			RecipientIDs:  recipients,
			Text:          e.Text,
			Mentions:      mentionItems,
			MentionPrices: mentionPrices,
			SpeechID:      uint64(e.EventID()),
			At:            e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.SceneQuoteCreated:
		// ZBBS-HOME-470: a posted quote emits NO client frame. It reaches a PC
		// buyer two better ways now: the seller voices its own price in prose
		// (the stateful model reliably speaks it — "a penny a bowl", "12 coins a
		// unit"), and the Pay modal renders the take-able offer read straight
		// from the quote ledger via GET /pc/quotes (ZBBS-HOME-426). The old
		// buyer-facing npc_spoke (ZBBS-HOME-408) layered an engine-authored "I
		// can let you take N X for Y coins" line ON TOP of the seller's own
		// spoken offer — a duplicate the player read as the merchant quoting
		// twice. /pc/quotes did not exist when HOME-408 was written; it now
		// supersedes the frame's only remaining job (carrying the price to the
		// Pay modal). NPC-to-NPC commerce is untouched: NPCs learn a quote via
		// the warrant fan-out + pull-based perception, never this frame.
		return WireFrame{}, false
	case *sim.PhaseApplied:
		return WireFrame{Type: "world_phase_changed", Data: phaseChangedWireDTO{
			Phase: string(e.To),
		}}, true
	case *sim.WeatherChanged:
		return WireFrame{Type: "weather_changed", Data: weatherChangedWireDTO{
			Weather: e.Weather,
			At:      e.At,
		}}, true
	case *sim.VillageObjectStateChanged:
		return WireFrame{Type: "object_state_changed", Data: objectStateChangedWireDTO{
			ID:    string(e.ObjectID),
			State: e.ToState,
		}}, true
	case *sim.NoticeboardContentChanged:
		return WireFrame{Type: "noticeboard_content_changed", Data: noticeboardContentChangedWireDTO{
			ID:              string(e.ObjectID),
			ContentText:     e.Text,
			ContentPostedAt: e.PostedAt,
		}}, true
	case *sim.VillageObjectMoved:
		return WireFrame{Type: "object_moved", Data: objectMovedWireDTO{
			ID: string(e.ObjectID),
			X:  e.X,
			Y:  e.Y,
		}}, true
	case *sim.VillageObjectDeleted:
		return WireFrame{Type: "object_deleted", Data: objectDeletedWireDTO{
			ID: string(e.ObjectID),
		}}, true
	case *sim.VillageObjectCreated:
		return WireFrame{Type: "object_created", Data: objectCreatedWireDTO{
			ID:           string(e.ObjectID),
			AssetID:      string(e.AssetID),
			CurrentState: e.CurrentState,
			X:            e.X,
			Y:            e.Y,
			PlacedBy:     e.PlacedBy,
			EntryPolicy:  string(e.EntryPolicy),
			AttachedTo:   string(e.AttachedTo),
		}}, true
	case *sim.VillageObjectDisplayNameChanged:
		return WireFrame{Type: "object_display_name_changed", Data: objectDisplayNameChangedWireDTO{
			ID:          string(e.ObjectID),
			DisplayName: e.DisplayName,
		}}, true
	case *sim.VillageObjectTagsUpdated:
		// Copy the set rather than alias e.Tags. The production emitters already
		// pass a fresh slice, but the translator is a serialization boundary and
		// the hub may encode this frame asynchronously after the world goroutine
		// moves on — a future/test producer that hands us a live world slice must
		// not see it mutate out from under a queued frame (code_review,
		// ZBBS-HOME-283). The copy also satisfies the wire contract "always an
		// array, never null": a nil set copies to nil, which we then coerce to []
		// so it marshals as [] rather than a JSON null the client would choke on.
		tags := append([]string(nil), e.Tags...)
		if tags == nil {
			tags = []string{}
		}
		return WireFrame{Type: "village_object_tags_updated", Data: objectTagsUpdatedWireDTO{
			ID:   string(e.ObjectID),
			Tags: tags,
		}}, true
	case *sim.VillageObjectLoiterOffsetChanged:
		return WireFrame{Type: "object_loiter_offset_changed", Data: objectLoiterOffsetChangedWireDTO{
			ID:                     string(e.ObjectID),
			LoiterOffsetX:          e.LoiterOffsetX,
			LoiterOffsetY:          e.LoiterOffsetY,
			EffectiveLoiterOffsetX: e.EffectiveLoiterOffsetX,
			EffectiveLoiterOffsetY: e.EffectiveLoiterOffsetY,
		}}, true
	case *sim.ZoomSettingsChanged:
		// Keys match what the client's apply_zoom_floor_from_config reads
		// (world.gd) — the same shape the public /world DTO carries.
		return WireFrame{Type: "zoom_settings_changed", Data: zoomSettingsChangedWireDTO{
			ZoomMinAdmin:   e.ZoomMinAdmin,
			ZoomMinRegular: e.ZoomMinRegular,
		}}, true
	case *sim.AgentTicksPausedChanged:
		return WireFrame{Type: "agent_ticks_paused_changed", Data: agentTicksPausedChangedWireDTO{
			AgentTicksPaused: e.Paused,
		}}, true
	case *sim.NPCCreated:
		// Reuse AgentDTO so the frame is byte-identical to a per-NPC entry from
		// the /api/village/agents load the client already renders. A fresh NPC
		// has no editor metadata yet (agent/schedule/social/anchors/attributes all
		// zero), so only the render fields are populated; the sprite is resolved
		// from the *Sprite the event carried (no catalog lookup needed here).
		return WireFrame{Type: "npc_created", Data: AgentDTO{
			ID:          string(e.ActorID),
			DisplayName: e.DisplayName,
			Kind:        actorKindString(e.Kind),
			State:       string(sim.StateIdle),
			X:           e.X,
			Y:           e.Y,
			Facing:      normalizeFacing(e.Facing),
			Sprite:      agentSpriteDTOFromSprite(e.Sprite),
		}}, true
	case *sim.ActorDeparted:
		// An actor left World.Actors — admin delete (DeleteActor) or visitor
		// cleanup both emit this. The client's remove_npc_by_id handler is
		// idempotent (a no-op if the sprite is already gone), so emitting for
		// both paths is safe and also closes the latent visitor-despawn gap
		// where the client sprite was never told to disappear.
		//
		// INVARIANT this relies on: every ActorDeparted emitter today removes a
		// client-rendered actor (NPC or visitor, both drawn via placed_npcs) that
		// remove_npc_by_id is the correct cleanup for. PCs also live in
		// placed_npcs, but no PC-departure path emits ActorDeparted today (see the
		// event doc — VisitorContext-nil is "reserved for future"). If a
		// non-rendered or PC departure path is ever added, this must gain a
		// kind/reason discriminator (or admin delete must emit a distinct
		// NPCDeleted event) so it doesn't broadcast their ids as npc_deleted.
		return WireFrame{Type: "npc_deleted", Data: npcDeletedWireDTO{ID: string(e.ActorID)}}, true
	case *sim.NPCDisplayNameChanged:
		return WireFrame{Type: "npc_display_name_changed", Data: npcDisplayNameChangedWireDTO{
			ID:          string(e.ActorID),
			DisplayName: e.DisplayName,
		}}, true
	case *sim.NPCAgentChanged:
		// "" unlinks — the client treats a null llm_memory_agent as remove-meta,
		// so an empty link marshals as JSON null (not "").
		return WireFrame{Type: "npc_agent_changed", Data: npcAgentChangedWireDTO{
			ID:       string(e.ActorID),
			LLMAgent: strPtrOrNil(e.LLMAgent),
		}}, true
	case *sim.NPCHomeStructureChanged:
		return WireFrame{Type: "npc_home_structure_changed", Data: npcHomeStructureChangedWireDTO{
			ID:              string(e.ActorID),
			HomeStructureID: strPtrOrNil(e.StructureID),
		}}, true
	case *sim.NPCWorkStructureChanged:
		return WireFrame{Type: "npc_work_structure_changed", Data: npcWorkStructureChangedWireDTO{
			ID:              string(e.ActorID),
			WorkStructureID: strPtrOrNil(e.StructureID),
		}}, true
	case *sim.NPCScheduleChanged:
		// Nil bounds marshal as null — the client reads that as "inherit
		// dawn/dusk". lateness_window_minutes is intentionally absent (ZBBS-HOME-309
		// moved staggering to the global shift_lateness_window_minutes setting); the
		// client defaults the field to 0 when missing.
		return WireFrame{Type: "npc_schedule_changed", Data: npcScheduleChangedWireDTO{
			ID:               string(e.ActorID),
			ScheduleStartMin: e.ScheduleStartMin,
			ScheduleEndMin:   e.ScheduleEndMin,
		}}, true
	case *sim.NPCSocialUpdated:
		return WireFrame{Type: "npc_social_updated", Data: npcSocialUpdatedWireDTO{
			ID:             string(e.ActorID),
			SocialTag:      strPtrOrNil(e.SocialTag),
			SocialStartMin: e.SocialStartMin,
			SocialEndMin:   e.SocialEndMin,
		}}, true
	case *sim.NPCSpriteChanged:
		return WireFrame{Type: "npc_sprite_changed", Data: npcSpriteChangedWireDTO{
			ID:     string(e.ActorID),
			Sprite: agentSpriteDTOFromSprite(e.Sprite),
		}}, true
	case *sim.NPCAttributesChanged:
		// Copy + coerce nil→[] so the frame is always a JSON array, never null
		// (same boundary discipline as village_object_tags_updated above).
		attrs := append([]string(nil), e.Attributes...)
		if attrs == nil {
			attrs = []string{}
		}
		return WireFrame{Type: "npc_attributes_changed", Data: npcAttributesChangedWireDTO{
			ID:         string(e.ActorID),
			Attributes: attrs,
		}}, true
	case *sim.NPCNeedsChanged:
		// Full post-change triple (not a delta); the client's
		// apply_npc_needs_changed patches each meta and refreshes the editor
		// readout. See World.emitNeedsDeltas for the change-detection source.
		return WireFrame{Type: "npc_needs_changed", Data: npcNeedsChangedWireDTO{
			ID:        string(e.ActorID),
			Hunger:    e.Hunger,
			Thirst:    e.Thirst,
			Tiredness: e.Tiredness,
		}}, true
	case *sim.NPCDormancyChanged:
		// state is "sleeping"/"resting" while dormant (client shows a Zzz marker +
		// dims the sprite) or "" when the NPC woke (clear). Same dormancy vocabulary
		// AgentDTO.State sends on the REST snapshot. See World.emitDormancyDeltas.
		return WireFrame{Type: "npc_dormancy_changed", Data: npcDormancyChangedWireDTO{
			ID:    string(e.ActorID),
			State: e.State,
		}}, true
	case *sim.NPCCoinsChanged:
		// Full post-change balance (not a delta); the client's
		// apply_npc_coins_changed patches the coins meta and refreshes the editor
		// villager-row readout. See World.emitCoinsDeltas for the change-detection
		// source.
		return WireFrame{Type: "npc_coins_changed", Data: npcCoinsChangedWireDTO{
			ID:    string(e.ActorID),
			Coins: e.Coins,
		}}, true
	case *sim.PayOfferReceived:
		return WireFrame{Type: "pay_offer", Data: payOfferWireDTO{
			LedgerID:   uint64(e.LedgerID),
			BuyerID:    string(e.BuyerID),
			SellerID:   string(e.SellerID),
			Item:       string(e.ItemKind),
			Qty:        e.QtyPerConsumer,
			Amount:     e.Amount,
			ConsumeNow: e.ConsumeNow,
			HuddleID:   string(e.HuddleID),
			SceneID:    string(e.SceneID),
			At:         e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.PayCountered:
		return WireFrame{Type: "pay_countered", Data: payCounteredWireDTO{
			LedgerID:       uint64(e.ParentID),
			BuyerID:        string(e.BuyerID),
			SellerID:       string(e.SellerID),
			Item:           string(e.ItemKind),
			Qty:            e.QtyPerConsumer,
			OriginalAmount: e.OriginalAmount,
			CounterAmount:  e.CounterAmount,
			Message:        e.Message,
			HuddleID:       string(e.HuddleID),
			SceneID:        string(e.SceneID),
			At:             e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.PayWithItemResolved:
		return WireFrame{Type: "pay_resolved", Data: payResolvedWireDTO{
			LedgerID:       uint64(e.LedgerID),
			BuyerID:        string(e.BuyerID),
			SellerID:       string(e.SellerID),
			Item:           string(e.ItemKind),
			Qty:            e.QtyPerConsumer,
			Amount:         e.Amount,
			TerminalState:  string(e.TerminalState),
			BuyerTookQuote: e.BuyerTookQuote,
			Message:        e.Message,
			HuddleID:       string(e.HuddleID),
			SceneID:        string(e.SceneID),
			At:             e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.PCSleepStarted:
		return WireFrame{Type: "pc_sleep_started", Data: pcSleepStartedWireDTO{
			ActorID: string(e.ActorID),
			WakeAt:  e.WakeAt.UTC().Format(time.RFC3339),
			At:      e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.PCSleepEnded:
		return WireFrame{Type: "pc_sleep_ended", Data: pcSleepEndedWireDTO{
			ActorID: string(e.ActorID),
			Reason:  e.Reason,
			At:      e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.PCRelocatedToCommon:
		if e.Text == "" {
			// No narration line (empty pool) — drop rather than send a room_event
			// the client would discard for empty text. Emitters also skip emit on
			// empty text; this is the belt-and-suspenders so a blank frame can
			// never reach the client regardless of emit site.
			return WireFrame{}, false
		}
		// A PC was moved from a private room to its structure's common room by
		// the lodging day-cycle (checkout eviction or morning descent). Surfaced
		// as a private room_event narration — the client's _on_room_event matches
		// it to its own PC by actor_id and renders the brown-panel line. actor_name
		// is "" (the engine convention for second-person felt narration: there is
		// no speaker, just a line addressed to the PC); kind is the relocation
		// reason. private=true makes the client bypass room-scope filtering, so the
		// line lands even though the PC's loaded scope is mid-change.
		return WireFrame{Type: "room_event", Data: roomEventWireDTO{
			ActorID:     string(e.ActorID),
			ActorName:   "",
			Kind:        e.Reason,
			Text:        e.Text,
			Private:     true,
			StructureID: string(e.StructureID),
			At:          e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.ActorArrivalNarrated:
		// ZBBS-WORK-422: observer-facing "X arrives at Y" line for co-present PCs,
		// rendered by the talk panel's _on_room_event as a narration line. NON-private
		// + structure-scoped: the engine emits this only for a public-scope arrival,
		// so the client's structure filter delivers it to co-present common-room PCs
		// (no room_id needed — public scope is room_id=""). actor_name satisfies the
		// client's non-private non-empty-actor guard; the narration render shows Text.
		return WireFrame{Type: "room_event", Data: roomEventWireDTO{
			ActorID:     string(e.ActorID),
			ActorName:   e.ActorName,
			Kind:        "peer_arrival",
			Text:        e.Text,
			Private:     false,
			StructureID: string(e.StructureID),
			At:          e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.ActorDepartureNarrated:
		// Departure twin of ActorArrivalNarrated: observer-facing "X leaves Y" line
		// for co-present PCs, rendered by the talk panel's _on_room_event as a narration
		// line. NON-private + structure-scoped, emitted only for a public-scope
		// departure, so the client's structure filter delivers it to co-present
		// common-room PCs (no room_id needed — public scope is room_id="").
		return WireFrame{Type: "room_event", Data: roomEventWireDTO{
			ActorID:     string(e.ActorID),
			ActorName:   e.ActorName,
			Kind:        "peer_departure",
			Text:        e.Text,
			Private:     false,
			StructureID: string(e.StructureID),
			At:          e.At.UTC().Format(time.RFC3339),
		}}, true
	case *sim.StallConditionNarrated:
		// LLM-118: a PC walked up to a worn market stall — a second-person felt
		// atmosphere line ("The market stall here looks worn…"). PRIVATE +
		// actor-scoped (actor_name "" — no speaker, addressed to the PC), the same
		// carrier as the sleep / lodging narrations, so the client's _on_room_event
		// matches it to its own PC by actor_id and renders the brown-panel line.
		if e.Text == "" {
			return WireFrame{}, false
		}
		return WireFrame{Type: "room_event", Data: roomEventWireDTO{
			ActorID:     string(e.ActorID),
			ActorName:   "",
			Kind:        "stall_condition",
			Text:        e.Text,
			Private:     true,
			StructureID: string(e.StructureID),
			At:          e.At.UTC().Format(time.RFC3339),
		}}, true
	default:
		return WireFrame{}, false
	}
}

// pcNeedsChangedWireDTO is the pc_needs_changed frame payload (LLM-56): the PC
// whose needs changed and its full post-bite need snapshot (hunger/thirst/
// tiredness). The client matches ActorID against its own PC and feeds Needs
// straight into top_bar.set_needs, so a bite ticks the HUD without waiting on
// the ~10s /pc/me poll.
type pcNeedsChangedWireDTO struct {
	ActorID string         `json:"actor_id"`
	Needs   map[string]int `json:"needs"`
}

// walkWireDTO is the npc_walking payload — the engine's full cost-weighted tile
// path (roads preferred, buildings avoided), which the client follows tile by
// tile. Path is in TILE coordinates (matching AgentDTO's tile x/y convention);
// the client converts to world-pixels with the pad/tile_size it already gets
// from the terrain DTO. Path[0] is the walk start, Path[len-1] the resolved
// goal. dest_kind is structure_enter | structure_visit | object_visit | position.
// structure_id is present for the structure kinds; object_id is present for
// object_visit (ZBBS-WORK-351). attempt_id correlates with the npc_arrived /
// npc_move_stopped that conclude this walk; a fresh attempt_id for the same
// actor supersedes any earlier in-flight walk.
type walkWireDTO struct {
	ID          string         `json:"id"`
	Path        []tilePointDTO `json:"path"`
	DestKind    string         `json:"dest_kind"`
	StructureID string         `json:"structure_id,omitempty"`
	ObjectID    string         `json:"object_id,omitempty"`
	AttemptID   uint64         `json:"attempt_id"`
}

// tilePointDTO is a single tile waypoint in a walk path.
type tilePointDTO struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// arrivedWireDTO is the npc_arrived payload — the authoritative end of a walk.
// The client snaps the actor to (x, y) and goes idle regardless of where its
// local nav reached. structure_id is the structure the actor ended inside (empty
// for a bare position or a visitor slot). No facing — the client derives it from
// its movement delta, falling back to last-known.
type arrivedWireDTO struct {
	ID          string `json:"id"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	StructureID string `json:"structure_id,omitempty"`
	AttemptID   uint64 `json:"attempt_id"`
}

// moveStoppedWireDTO is the npc_move_stopped payload — an accepted walk that
// failed to reach its goal (blocked | unreachable | invalidated). The client
// stops its local nav and snaps to (x, y). Distinct from npc_arrived so a viewer
// doesn't render an arrival that never happened.
type moveStoppedWireDTO struct {
	ID        string `json:"id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Reason    string `json:"reason"`
	AttemptID uint64 `json:"attempt_id"`
}

// insideChangedWireDTO is the npc_inside_changed payload — an actor's
// inside-a-structure state flipped. The client (apply_npc_inside_change)
// re-derives sprite visibility (plain houses hide; see-through stalls stay
// visible) and the behind-the-counter stand offset from it. inside is sent
// explicitly; inside_structure_id is omitted when outdoors (the client reads a
// missing value as ""). x/y are the actor's padded-grid tile at the flip; the
// client snaps the sprite to it before changing visibility so a not-yet-rendered
// walk doesn't strand it at a stale spot (ZBBS-HOME-464). Restores the v1
// broadcast the rewrite dropped (ZBBS-WORK-373).
type insideChangedWireDTO struct {
	ID                string `json:"id"`
	Inside            bool   `json:"inside"`
	InsideStructureID string `json:"inside_structure_id,omitempty"`
	X                 int    `json:"x"`
	Y                 int    `json:"y"`
}

// spokeWireDTO is the npc_spoke payload — one utterance the client renders as a
// speech bubble over the speaker. id is the speaker's actor id (the client
// resolves the display name + position from its agent roster). huddle_id scopes
// the conversation; recipient_ids is the huddle audience at commit time (empty
// when speaking to no one — a valid v2 state). speech_id (= the event id) lets a
// client correlate its own optimistic bubble with this authoritative event and
// dedupe. No x/y — v2 speech is huddle-scoped, not proximity-based (unlike v1).
//
// mentions + mention_prices are the structured offer hints (ZBBS-HOME-408):
// the item kinds a vendor is offering and their unit price in coins. The
// client's _on_npc_spoke fills the Pay-modal item dropdown from mentions and
// pre-fills the amount from mention_prices. A real Spoke carries neither (both
// nil → omitempty drops them, leaving normal speech frames byte-identical);
// only the SceneQuoteCreated-derived frame populates them.
type spokeWireDTO struct {
	ID            string         `json:"id"`
	HuddleID      string         `json:"huddle_id,omitempty"`
	RecipientIDs  []string       `json:"recipient_ids"`
	Text          string         `json:"text"`
	Mentions      []string       `json:"mentions,omitempty"`
	MentionPrices map[string]int `json:"mention_prices,omitempty"`
	SpeechID      uint64         `json:"speech_id"`
	At            string         `json:"at"`
}

// phaseChangedWireDTO is the world_phase_changed payload — the day/night
// boundary. phase is the post-transition phase ("day" | "night", matching the
// world DTO's phase token); the client flips its global lighting on receipt
// (event_client.gd _on_world_phase_changed → world.set_phase). Idempotent
// re-applies (admin force-phase to the current phase) still emit with the same
// value, which the client treats as a harmless no-op set. The bulk per-object
// lamp flips the transition schedules surface separately as object_state_changed
// frames, so this carries only the scalar phase.
type phaseChangedWireDTO struct {
	Phase string `json:"phase"`
}

// weatherChangedWireDTO is the weather_changed payload — the storm boundary
// (LLM-117). weather is the post-transition value ("clear" | "storm", matching
// the world DTO's weather token); the client raises / tweens out the storm FX
// layer on receipt (event_client.gd _on_world_weather_changed → world.set_weather).
// at is the RFC3339 instant the change landed (carried for parity with the
// event; the client uses only weather). A client connecting / reconnecting
// mid-storm renders the storm from the world DTO's weather field instead of this
// frame, so the two share the same token vocabulary.
type weatherChangedWireDTO struct {
	Weather string    `json:"weather"`
	At      time.Time `json:"at"`
}

// objectStateChangedWireDTO is the object_state_changed payload — one placed
// object's CurrentState flipped (e.g. a streetlamp unlit→lit at dusk, a
// noticeboard gaining authored content). id is the village object id; state is
// the new state name, which the client resolves against the asset catalog to
// swap the sprite + light (event_client.gd _on_object_state_changed). The
// previous state is not carried — the client renders the new state outright.
type objectStateChangedWireDTO struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// noticeboardContentChangedWireDTO is the noticeboard_content_changed payload —
// a board's authored prose was (re)written (ZBBS-HOME-293, the live-update
// fast-follow to HOME-291's ObjectDTO read fields). id is the village object id;
// content_text is the new line; content_posted_at is when it landed (RFC3339).
// The client patches the object's content meta and refreshes an open content
// modal in place instead of waiting for the next full objects fetch. The fields
// mirror ObjectDTO.content_text / content_posted_at so the client reuses one
// apply path. Additive — no contract_version bump.
type noticeboardContentChangedWireDTO struct {
	ID              string    `json:"id"`
	ContentText     string    `json:"content_text"`
	ContentPostedAt time.Time `json:"content_posted_at"`
}

// objectMovedWireDTO is the object_moved payload — a placed object repositioned
// by an admin. id is the village object id; x / y are the new absolute
// world-pixel anchor (matching ObjectDTO's float coordinate space, NOT the
// integer tile space agents use). The client moves the rendered object to
// (x, y); an event for an object the client doesn't know is ignored client-side.
type objectMovedWireDTO struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

// objectDeletedWireDTO is the object_deleted payload — a placed object removed
// by an admin (or cascade-removed as an overlay attached to a deleted object).
// id is the village object id; the client removes the rendered object. An event
// for an object the client doesn't know is ignored client-side.
type objectDeletedWireDTO struct {
	ID string `json:"id"`
}

// objectCreatedWireDTO is the object_created payload — a placed object created
// by an admin via the editor. Carries the fields the client's _place_object
// reads to render the new object; absent metadata (owner, display_name, tags)
// defaults client-side. attached_to is omitted (→ client null) for a root
// placement. The placing client dedupes this against its optimistic node by id.
type objectCreatedWireDTO struct {
	ID           string  `json:"id"`
	AssetID      string  `json:"asset_id"`
	CurrentState string  `json:"current_state"`
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	PlacedBy     string  `json:"placed_by,omitempty"`
	EntryPolicy  string  `json:"entry_policy,omitempty"`
	AttachedTo   string  `json:"attached_to,omitempty"`
}

// objectDisplayNameChangedWireDTO is the object_display_name_changed payload — a
// placed object's display-name override edited by an admin. id is the village
// object id (keyed on id, matching ObjectDTO and the rest of the object_* frames);
// display_name is the new name, or "" when the override was cleared (the client
// falls back to the catalog name). ZBBS-HOME-283.
type objectDisplayNameChangedWireDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// zoomSettingsChangedWireDTO is the zoom_settings_changed payload — the
// post-change camera floors after an admin saves them (ZBBS-WORK-363). Keys
// match the public /world DTO + what the client's apply_zoom_floor_from_config
// reads, so the client reloads whichever floor applies to its role live.
type zoomSettingsChangedWireDTO struct {
	ZoomMinAdmin   float64 `json:"zoom_min_admin"`
	ZoomMinRegular float64 `json:"zoom_min_regular"`
}

// agentTicksPausedChangedWireDTO is the agent_ticks_paused_changed payload — the
// new global LLM-agent pause state (ZBBS-WORK-363), so the config panel's
// checkbox reflects an admin toggle without waiting for its next poll.
type agentTicksPausedChangedWireDTO struct {
	AgentTicksPaused bool `json:"agent_ticks_paused"`
}

// objectTagsUpdatedWireDTO is the village_object_tags_updated payload — a placed
// object's per-instance tag set after an admin add/remove. id is the village
// object id; tags is the AUTHORITATIVE full set (not a delta), always a JSON
// array (never null — see TranslateEvent). The client replaces its local tag set
// outright. ZBBS-HOME-283.
type objectTagsUpdatedWireDTO struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}

// NPC editor write frames (ZBBS-HOME-309). Each mirrors the field set the Godot
// editor's apply_npc_* handler reads (world.gd) — all keyed on `id`. Nullable
// fields (agent link, home/work anchors, schedule + social bounds) use pointers
// so an unset value marshals as JSON null, which the client reads as
// "unlinked / inherit" rather than the zero value.
type npcDisplayNameChangedWireDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type npcDeletedWireDTO struct {
	ID string `json:"id"`
}

type npcAgentChangedWireDTO struct {
	ID       string  `json:"id"`
	LLMAgent *string `json:"llm_memory_agent"`
}

type npcHomeStructureChangedWireDTO struct {
	ID              string  `json:"id"`
	HomeStructureID *string `json:"home_structure_id"`
}

type npcWorkStructureChangedWireDTO struct {
	ID              string  `json:"id"`
	WorkStructureID *string `json:"work_structure_id"`
}

type npcScheduleChangedWireDTO struct {
	ID               string `json:"id"`
	ScheduleStartMin *int   `json:"schedule_start_minute"`
	ScheduleEndMin   *int   `json:"schedule_end_minute"`
}

type npcSocialUpdatedWireDTO struct {
	ID             string  `json:"id"`
	SocialTag      *string `json:"social_tag"`
	SocialStartMin *int    `json:"social_start_minute"`
	SocialEndMin   *int    `json:"social_end_minute"`
}

type npcAttributesChangedWireDTO struct {
	ID         string   `json:"id"`
	Attributes []string `json:"attributes"`
}

type npcNeedsChangedWireDTO struct {
	ID        string `json:"id"`
	Hunger    int    `json:"hunger"`
	Thirst    int    `json:"thirst"`
	Tiredness int    `json:"tiredness"`
}

type npcSpriteChangedWireDTO struct {
	ID     string          `json:"id"`
	Sprite *AgentSpriteDTO `json:"sprite"`
}

// npcDormancyChangedWireDTO is the npc_dormancy_changed payload — an agent NPC
// entered or left a sleep/rest state. state is "sleeping"/"resting" while dormant
// (the client renders a Zzz marker + dims the sprite) or "" when awake (clear it).
type npcDormancyChangedWireDTO struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// npcCoinsChangedWireDTO is the npc_coins_changed payload — an actor's purse
// balance after a transaction. coins is the full post-change value (not a delta);
// the client patches the villager-row "Coins N" readout in place.
type npcCoinsChangedWireDTO struct {
	ID    string `json:"id"`
	Coins int    `json:"coins"`
}

// strPtrOrNil returns nil for an empty string, else a pointer to it — so a
// cleared link/anchor marshals as JSON null instead of "".
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// objectLoiterOffsetChangedWireDTO is the object_loiter_offset_changed payload —
// a placed object's loiter pin edited by an admin. id is the village object id;
// loiter_offset_x/y are the raw per-instance override (null when cleared);
// effective_loiter_offset_x/y are the SERVER-resolved offset (tile units relative
// to the anchor) the editor renders the pin at, so it lands exactly where the
// engine parks visitors. The client updates the pin on receipt. ZBBS-HOME-289
// (matches v1's object_loiter_offset_changed; both raw + effective carried).
type objectLoiterOffsetChangedWireDTO struct {
	ID                     string `json:"id"`
	LoiterOffsetX          *int   `json:"loiter_offset_x"`
	LoiterOffsetY          *int   `json:"loiter_offset_y"`
	EffectiveLoiterOffsetX int    `json:"effective_loiter_offset_x"`
	EffectiveLoiterOffsetY int    `json:"effective_loiter_offset_y"`
}

// payOfferWireDTO is the pay_offer payload — a buyer (PC or NPC) has
// staked a pending pay-with-item offer on a seller. The client renders it
// as an offer notice scoped to huddle_id (same client-side scoping model
// as npc_spoke; the hub broadcasts to every viewer and the client decides
// what to surface). buyer_id / seller_id are actor ids the client
// resolves to display names from its roster. ledger_id correlates this
// offer with the later pay_countered / pay_resolved frame. amount is the
// offered coin total; qty is per-consumer item count; consume_now
// distinguishes eat-now from a take-home order. Only the slow path emits
// this — a quote fast-path match resolves instantly and emits pay_resolved
// directly. No coins move on a pay_offer (pending reserves nothing).
type payOfferWireDTO struct {
	LedgerID   uint64 `json:"ledger_id"`
	BuyerID    string `json:"buyer_id"`
	SellerID   string `json:"seller_id"`
	Item       string `json:"item"`
	Qty        int    `json:"qty"`
	Amount     int    `json:"amount"`
	ConsumeNow bool   `json:"consume_now"`
	HuddleID   string `json:"huddle_id,omitempty"`
	SceneID    string `json:"scene_id,omitempty"`
	At         string `json:"at"`
}

// payCounteredWireDTO is the pay_countered payload — the seller proposed
// counter terms on a pending offer (the commerce is NOT ended; the buyer
// may respond with a fresh in_response_to offer). ledger_id is the parent
// (countered) entry's id. original_amount is the buyer's offer;
// counter_amount is the seller's proposed price. message is the seller's
// optional counter note. Item terms (item, qty) don't change across a
// counter — only the price. Client scopes by huddle_id like npc_spoke.
type payCounteredWireDTO struct {
	LedgerID       uint64 `json:"ledger_id"`
	BuyerID        string `json:"buyer_id"`
	SellerID       string `json:"seller_id"`
	Item           string `json:"item"`
	Qty            int    `json:"qty"`
	OriginalAmount int    `json:"original_amount"`
	CounterAmount  int    `json:"counter_amount"`
	Message        string `json:"message,omitempty"`
	HuddleID       string `json:"huddle_id,omitempty"`
	SceneID        string `json:"scene_id,omitempty"`
	At             string `json:"at"`
}

// payResolvedWireDTO is the pay_resolved payload — a pay-with-item offer
// reached a non-counter terminal. terminal_state is the outcome token
// (accepted | declined | withdrawn_by_buyer | expired |
// failed_unavailable | failed_insufficient_stock |
// failed_insufficient_funds), which the client maps to its outcome UI.
// message carries the seller's decline reason or the buyer's withdraw
// note (empty otherwise). On accepted, this is the frame that confirms the
// transfer; the goods themselves move at deliver_order time for a
// take-home order. ledger_id correlates with the originating pay_offer.
// buyer_took_quote is true only on an instant quote-take (the seller posted
// the offer, the buyer took it); the client uses it to word the accepted line
// ("you took their offer" vs the backwards "they accepted your offer").
type payResolvedWireDTO struct {
	LedgerID       uint64 `json:"ledger_id"`
	BuyerID        string `json:"buyer_id"`
	SellerID       string `json:"seller_id"`
	Item           string `json:"item"`
	Qty            int    `json:"qty"`
	Amount         int    `json:"amount"`
	TerminalState  string `json:"terminal_state"`
	BuyerTookQuote bool   `json:"buyer_took_quote,omitempty"`
	Message        string `json:"message,omitempty"`
	HuddleID       string `json:"huddle_id,omitempty"`
	SceneID        string `json:"scene_id,omitempty"`
	At             string `json:"at"`
}

// pcSleepStartedWireDTO is the pc_sleep_started payload — a PC bedded down
// (idle auto-bed or the /pc/sleep route). actor_id is the sleeping PC; the
// client filters to its own PC and raises the sleep-fade overlay + the
// "Sleeping — wake HH:MM" top-bar chip (event_client.gd → main.gd
// _on_pc_sleep_started). wake_at is the safety-cap instant (RFC3339) the chip
// renders as the wake time (the PC usually wakes earlier, when rested). at is
// the bed-down instant. Broadcast to all viewers; the client scopes by
// actor_id, like the other PC frames.
type pcSleepStartedWireDTO struct {
	ActorID string `json:"actor_id"`
	WakeAt  string `json:"wake_at"`
	At      string `json:"at"`
}

// pcSleepEndedWireDTO is the pc_sleep_ended payload — a PC woke. actor_id is the
// PC; the client clears the overlay + chip (main.gd _on_pc_sleep_ended). reason
// is "manual" (Wake button), "auto" (rested or the cap), or "input" (acted
// while asleep) — currently informational on the client (plumbed for a future
// "you woke because X" surface). at is the wake instant.
type pcSleepEndedWireDTO struct {
	ActorID string `json:"actor_id"`
	Reason  string `json:"reason"`
	At      string `json:"at"`
}

// roomEventWireDTO is the room_event payload — a private, second-person
// brown-panel narration line addressed to one PC (the v1 room_event shape the
// Godot client's _on_room_event already renders; see world.gd apply_room_event).
// private=true + actor_id scope it to the one PC client-side and bypass the
// talk-panel room-scope filter, so the line surfaces even while the PC's loaded
// scope is mid-change (e.g. just relocated). actor_name is "" by convention for
// these speaker-less felt lines. kind is informational (the relocation reason).
// structure_id is where the relocation happened. No room_id: private events skip
// the subspace filter, so it is not needed.
type roomEventWireDTO struct {
	ActorID     string `json:"actor_id"`
	ActorName   string `json:"actor_name"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	Private     bool   `json:"private"`
	StructureID string `json:"structure_id"`
	At          string `json:"at"`
}
