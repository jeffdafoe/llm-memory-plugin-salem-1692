package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pcMeWorld stands up a running mem-backed world seeded for the pc/me read: a
// login-bound PC ("tester") inside an inn, in a huddle with an NPC and a second
// PC, carrying inventory + needs + a sprite, with an action log scoped across
// two huddles and a stale entry. insideRoomID selects which inn room the PC
// occupies (2 = private bedroom → scoped audience room; 1 = common → public).
func pcMeWorld(t *testing.T, insideRoomID sim.RoomID) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	now := time.Now().UTC()
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.NeedThresholds = sim.NeedThresholds{"hunger": 6}

		world.Actors["p1"] = &sim.Actor{
			ID: "p1", DisplayName: "Tester", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester",
			Pos: sim.TilePos{X: 10, Y: 10}, Coins: 42,
			InsideStructureID: "inn", InsideRoomID: insideRoomID,
			HomeStructureID: "cottage", CurrentHuddleID: "h1",
			SpriteID:  "sprite-1",
			Needs:     map[sim.NeedKey]int{"hunger": 5, "thirst": 3},
			Inventory: map[sim.ItemKind]int{"bread": 2, "ale": 1, "mystery": 1},
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceObject}: {
					ObjectID: "inn", Attribute: "hunger", Source: sim.DwellSourceObject,
					LastCreditedAt: now, DwellPeriodMinutes: 10,
				},
				{ObjectID: "inn", Attribute: "thirst", Source: sim.DwellSourceObject}: {
					ObjectID: "inn", Attribute: "thirst", Source: sim.DwellSourceObject,
					LastCreditedAt: now.Add(-time.Hour), DwellPeriodMinutes: 10,
				},
			},
		}
		world.Actors["hannah"] = &sim.Actor{
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Role: "innkeeper", LLMAgent: "hannah-va",
			Pos: sim.TilePos{X: 10, Y: 10}, InsideStructureID: "inn",
			CurrentHuddleID: "h1",
		}
		world.Actors["p2"] = &sim.Actor{
			ID: "p2", DisplayName: "Otherguy", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "other",
			Pos: sim.TilePos{X: 10, Y: 10}, InsideStructureID: "inn",
			CurrentHuddleID: "h1",
		}

		world.Structures["inn"] = &sim.Structure{
			ID: "inn", DisplayName: "The Inn",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		}
		world.VillageObjects["inn"] = &sim.VillageObject{
			ID: "inn", AssetID: "inn-asset", DisplayName: "The Inn",
		}
		world.VillageObjects["cottage"] = &sim.VillageObject{
			ID: "cottage", AssetID: "cottage-asset", DisplayName: "Tester's Cottage",
		}

		world.Huddles["h1"] = &sim.Huddle{
			ID: "h1", StructureID: "inn",
			Members: map[sim.ActorID]struct{}{"p1": {}, "hannah": {}, "p2": {}},
		}

		world.Sprites = map[sim.SpriteID]*sim.Sprite{
			"sprite-1": {
				ID: "sprite-1", Name: "Woman A", Sheet: "npc/woman_A.png",
				FrameWidth: 64, FrameHeight: 64,
				Animations: []sim.SpriteAnimation{
					{Direction: "south", Animation: "idle", RowIndex: 0, FrameCount: 1, FrameRate: 6},
				},
			},
		}
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"bread": {Name: "bread", DisplayLabel: "Bread", Category: sim.ItemCategoryFood, Capabilities: []string{"portable"}},
			"ale":   {Name: "ale", DisplayLabel: "Ale", Category: sim.ItemCategoryDrink},
		}

		world.ActionLog = []sim.ActionLogEntry{
			// Stale (beyond the 24h cutoff) — excluded even though in h1.
			{ActorID: "hannah", OccurredAt: now.Add(-48 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "old chatter", HuddleID: "h1"},
			// Different huddle — excluded.
			{ActorID: "hannah", OccurredAt: now, ActionType: sim.ActionTypeSpoke, Text: "elsewhere", HuddleID: "h2"},
			// In-scope, oldest→newest.
			{ActorID: "hannah", OccurredAt: now.Add(-3 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "Welcome traveler", HuddleID: "h1"},
			{ActorID: "p1", OccurredAt: now.Add(-2 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "Hello", HuddleID: "h1"},
			{ActorID: "p1", OccurredAt: now.Add(-1 * time.Minute), ActionType: sim.ActionTypeConsumed, Text: "stew", HuddleID: "h1"},
			{ActorID: "p1", OccurredAt: now, ActionType: sim.ActionTypePaid, Text: "a round", HuddleID: "h1", CounterpartyName: "Hannah", Amount: 3},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed pc/me world: %v", err)
	}
	return w
}

// pcMe issues an authenticated POST /api/village/pc/me and decodes the response.
func pcMe(t *testing.T, srv *Server) pcMeResponse {
	t.Helper()
	rec := post(t, srv, "/api/village/pc/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp pcMeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestHandlePCMe_NoPC(t *testing.T) {
	// Base seeded world: bram is a PC but has no LoginUsername, so "tester"
	// resolves to no PC → exists=false at 200.
	srv := NewServer(seededWorld(t), okAuth{})
	resp := pcMe(t, srv)
	if resp.Exists {
		t.Fatalf("exists = true, want false for a session with no PC")
	}
	if resp.LoginUsername != "tester" {
		t.Errorf("login_username = %q, want tester", resp.LoginUsername)
	}
	// Stable empty shapes even with no PC.
	if resp.HuddleMembers == nil {
		t.Errorf("huddle_members = nil, want [] for the no-PC shape")
	}
}

// TestHandlePCMe_CanEdit locks the can_edit gate (ZBBS-HOME-316): it mirrors the
// operator capability (plugins/administer) and drives the Godot client's editor
// visibility, which the client reads on token-verify. Set regardless of whether
// the session has a PC.
func TestHandlePCMe_CanEdit(t *testing.T) {
	// Non-operator session → can_edit false.
	plain := pcMe(t, NewServer(seededWorld(t), okAuth{}))
	if plain.CanEdit {
		t.Error("can_edit = true for a non-operator session, want false")
	}
	// Operator (plugins/administer) → can_edit true.
	op := pcMe(t, NewServer(seededWorld(t), permAuth{map[string][]string{"plugins": {"administer"}}}))
	if !op.CanEdit {
		t.Error("can_edit = false for an operator session, want true")
	}
}

func TestHandlePCMe_FullIndoor(t *testing.T) {
	srv := NewServer(pcMeWorld(t, 2), okAuth{})
	resp := pcMe(t, srv)

	if !resp.Exists {
		t.Fatal("exists = false, want true")
	}
	if resp.ActorID != "p1" {
		t.Errorf("actor_id = %q, want p1", resp.ActorID)
	}
	if resp.CharacterName != "Tester" {
		t.Errorf("character_name = %q, want Tester", resp.CharacterName)
	}
	if resp.X != 10 || resp.Y != 10 {
		t.Errorf("x,y = %d,%d, want 10,10 (tile coords)", resp.X, resp.Y)
	}
	if resp.Coins != 42 {
		t.Errorf("coins = %d, want 42", resp.Coins)
	}
	if resp.InsideStructureID == nil || *resp.InsideStructureID != "inn" {
		t.Errorf("inside_structure_id = %v, want inn", resp.InsideStructureID)
	}
	if resp.StructureName != "The Inn" {
		t.Errorf("structure_name = %q, want The Inn", resp.StructureName)
	}
	if resp.HomeStructureID == nil || *resp.HomeStructureID != "cottage" {
		t.Errorf("home_structure_id = %v, want cottage", resp.HomeStructureID)
	}
	if resp.HomeName != "Tester's Cottage" {
		t.Errorf("home_name = %q, want Tester's Cottage", resp.HomeName)
	}
	if resp.CurrentHuddleID == nil || *resp.CurrentHuddleID != "h1" {
		t.Errorf("current_huddle_id = %v, want h1", resp.CurrentHuddleID)
	}
	// Indoors → audience structure is the literal inside structure.
	if resp.AudienceStructureID == nil || *resp.AudienceStructureID != "inn" {
		t.Errorf("audience_structure_id = %v, want inn", resp.AudienceStructureID)
	}
	// Private bedroom → scoped audience room.
	if resp.AudienceRoomID == nil || *resp.AudienceRoomID != "2" {
		t.Errorf("audience_room_id = %v, want \"2\"", resp.AudienceRoomID)
	}

	// Needs is a non-nil map carrying the PC's snapshot; thresholds present.
	if resp.Needs["hunger"] != 5 || resp.Needs["thirst"] != 3 {
		t.Errorf("needs = %v, want hunger:5 thirst:3", resp.Needs)
	}
	if resp.NeedThresholds["hunger"] != 6 {
		t.Errorf("need_thresholds = %v, want hunger:6", resp.NeedThresholds)
	}

	// Only the fresh dwell credit's attribute surfaces (thirst is stale).
	if len(resp.DwellingAttributes) != 1 || resp.DwellingAttributes[0] != "hunger" {
		t.Errorf("dwelling_attributes = %v, want [hunger]", resp.DwellingAttributes)
	}

	// Sprite resolved + inlined.
	if resp.SpriteID == nil || *resp.SpriteID != "sprite-1" {
		t.Fatalf("sprite_id = %v, want sprite-1", resp.SpriteID)
	}
	if resp.Sprite == nil || resp.Sprite.Name != "Woman A" {
		t.Errorf("sprite = %v, want inlined Woman A", resp.Sprite)
	}

	// Inventory: enriched + sorted by item_kind; unknown kind keeps raw kind.
	if len(resp.Inventory) != 3 {
		t.Fatalf("len(inventory) = %d, want 3", len(resp.Inventory))
	}
	wantInv := []pcInventoryEntry{
		{ItemKind: "ale", DisplayLabel: "Ale", Quantity: 1, Category: "drink"},
		{ItemKind: "bread", DisplayLabel: "Bread", Quantity: 2, Category: "food", Capabilities: []string{"portable"}},
		{ItemKind: "mystery", Quantity: 1},
	}
	for i, want := range wantInv {
		got := resp.Inventory[i]
		if got.ItemKind != want.ItemKind || got.DisplayLabel != want.DisplayLabel ||
			got.Quantity != want.Quantity || got.Category != want.Category {
			t.Errorf("inventory[%d] = %+v, want %+v", i, got, want)
		}
	}

	// Huddle roster: hannah + p2 (self p1 excluded), sorted by name.
	if len(resp.HuddleMembers) != 2 {
		t.Fatalf("len(huddle_members) = %d, want 2", len(resp.HuddleMembers))
	}
	h := resp.HuddleMembers[0]
	if h.Kind != "npc" || h.Name != "Hannah" || h.Role == nil || *h.Role != "innkeeper" ||
		h.TargetAgent == nil || *h.TargetAgent != "hannah-va" {
		t.Errorf("huddle_members[0] = %+v, want NPC Hannah innkeeper/hannah-va", h)
	}
	o := resp.HuddleMembers[1]
	if o.Kind != "pc" || o.Name != "Otherguy" || o.TargetAgent != nil {
		t.Errorf("huddle_members[1] = %+v, want PC Otherguy with no target_agent", o)
	}

	// Recent speech: huddle-scoped, oldest→newest, stale + other-huddle excluded.
	wantSpeech := []pcRecentSpeech{
		{SpeakerName: "Hannah", Text: "Welcome traveler", Kind: "speech_npc"},
		{SpeakerName: "Tester", Text: "Hello", Kind: "speech_player"},
		{SpeakerName: "Tester", Text: "Tester consumes stew.", Kind: "act"},
		{SpeakerName: "Tester", Text: "Tester pays Hannah 3 coins for a round.", Kind: "act"},
	}
	if len(resp.RecentSpeech) != len(wantSpeech) {
		t.Fatalf("len(recent_speech) = %d, want %d; got %+v", len(resp.RecentSpeech), len(wantSpeech), resp.RecentSpeech)
	}
	for i, want := range wantSpeech {
		got := resp.RecentSpeech[i]
		if got.SpeakerName != want.SpeakerName || got.Text != want.Text || got.Kind != want.Kind {
			t.Errorf("recent_speech[%d] = %+v, want %+v", i, got, want)
		}
	}
}

func TestHandlePCMe_CommonRoomPublicScope(t *testing.T) {
	// PC in the inn's common room (id 1) → public scope, no audience room.
	srv := NewServer(pcMeWorld(t, 1), okAuth{})
	resp := pcMe(t, srv)
	if resp.AudienceRoomID != nil {
		t.Errorf("audience_room_id = %v, want nil for a common-room PC", *resp.AudienceRoomID)
	}
	// Still scoped to the structure.
	if resp.AudienceStructureID == nil || *resp.AudienceStructureID != "inn" {
		t.Errorf("audience_structure_id = %v, want inn", resp.AudienceStructureID)
	}
}

// TestHandlePCMe_HuddlelessStructureBackload (ZBBS-HOME-437): a PC standing
// in a structure WITHOUT a huddle backloads the entries stamped with that
// structure's scope and a matching (public) room subspace — what the room
// recently heard — instead of the pre-437 nil. Entries from another
// structure, a private room, or with no stamp (pre-437 rows, open ground)
// stay out.
func TestHandlePCMe_HuddlelessStructureBackload(t *testing.T) {
	w := pcMeWorld(t, 1)
	now := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["p1"].CurrentHuddleID = ""
		world.ActionLog = []sim.ActionLogEntry{
			// In scope: the inn's public space, huddle long concluded.
			{ActorID: "hannah", OccurredAt: now.Add(-3 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "Fresh meat today", HuddleID: "gone-1", StructureID: "inn"},
			{ActorID: "p2", OccurredAt: now.Add(-2 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "I will take one", HuddleID: "gone-1", StructureID: "inn"},
			// Different structure — out.
			{ActorID: "hannah", OccurredAt: now, ActionType: sim.ActionTypeSpoke, Text: "tavern talk", HuddleID: "gone-2", StructureID: "tavern"},
			// Same structure, private room — out for a common-room PC.
			{ActorID: "hannah", OccurredAt: now, ActionType: sim.ActionTypeSpoke, Text: "bedroom whisper", HuddleID: "gone-3", StructureID: "inn", RoomID: 2},
			// Unstamped (pre-437 row / open ground) — out.
			{ActorID: "hannah", OccurredAt: now, ActionType: sim.ActionTypeSpoke, Text: "road chatter", HuddleID: "gone-4"},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("reseed: %v", err)
	}

	srv := NewServer(w, okAuth{})
	resp := pcMe(t, srv)
	want := []pcRecentSpeech{
		{SpeakerName: "Hannah", Text: "Fresh meat today", Kind: "speech_npc"},
		{SpeakerName: "Otherguy", Text: "I will take one", Kind: "speech_player"},
	}
	if len(resp.RecentSpeech) != len(want) {
		t.Fatalf("len(recent_speech) = %d, want %d; got %+v", len(resp.RecentSpeech), len(want), resp.RecentSpeech)
	}
	for i, w := range want {
		got := resp.RecentSpeech[i]
		if got.SpeakerName != w.SpeakerName || got.Text != w.Text || got.Kind != w.Kind {
			t.Errorf("recent_speech[%d] = %+v, want %+v", i, got, w)
		}
	}
}

func TestHandlePCMe_StaleRoomPublicScope(t *testing.T) {
	// InsideRoomID points at a room not in the PC's structure (stale ref after
	// a transition) → fails closed to public scope, no audience room.
	srv := NewServer(pcMeWorld(t, 999), okAuth{})
	resp := pcMe(t, srv)
	if resp.AudienceRoomID != nil {
		t.Errorf("audience_room_id = %v, want nil for a stale room ref", *resp.AudienceRoomID)
	}
}

func TestHandlePCMe_OutdoorAudienceScopeAndRoster(t *testing.T) {
	w := outdoorPCMeWorld(t)
	srv := NewServer(w, okAuth{})
	resp := pcMe(t, srv)

	if resp.InsideStructureID != nil {
		t.Errorf("inside_structure_id = %v, want nil outdoors", resp.InsideStructureID)
	}
	// Loiter pin of the well sits on the PC's tile → audience scope is the well.
	if resp.AudienceStructureID == nil || *resp.AudienceStructureID != "well" {
		t.Errorf("audience_structure_id = %v, want well", resp.AudienceStructureID)
	}
	if resp.AudienceRoomID != nil {
		t.Errorf("audience_room_id = %v, want nil outdoors", *resp.AudienceRoomID)
	}
	// Outdoor proximity roster: the nearby PC, not the far one.
	if len(resp.HuddleMembers) != 1 || resp.HuddleMembers[0].Name != "Nearby" {
		t.Errorf("huddle_members = %+v, want just [Nearby]", resp.HuddleMembers)
	}
	// No huddle and no entries stamped with the well's scope → no backload.
	// (Post-437 a huddle-less PC DOES backload its structure scope's entries
	// — TestHandlePCMe_HuddlelessStructureBackload — but this world's log has
	// none for "well".)
	if resp.RecentSpeech != nil {
		t.Errorf("recent_speech = %+v, want nil with no in-scope entries", resp.RecentSpeech)
	}
}

// TestHandlePCMe_LoiterStallRosterShowsOwner: ZBBS-HOME-378 — a PC standing at an
// owner-only stall's loiter point (outdoors, no huddle) sees the OWNER working
// inside the stall in its talk roster, plus any nearby player. Before the fix the
// outdoor roster listed only players, so a customer could never address the
// keeper of a market stall they were standing at.
func TestHandlePCMe_LoiterStallRosterShowsOwner(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	pin := sim.TilePos{X: 70, Y: 120}
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// Customer loitering at the stall's pin — outdoors (no InsideStructureID).
		world.Actors["p1"] = &sim.Actor{
			ID: "p1", DisplayName: "Tester", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester", Pos: pin,
		}
		// The stall owner, working INSIDE the stall.
		world.Actors["smith"] = &sim.Actor{
			ID: "smith", DisplayName: "Smith", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, InsideStructureID: "stall",
		}
		// A nearby player — must still appear alongside the owner.
		world.Actors["near"] = &sim.Actor{
			ID: "near", DisplayName: "Nearby", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "near",
			Pos: sim.TilePos{X: 72, Y: 121}, // Chebyshev 2 <= 6
		}
		// An asleep NPC inside the stall must NOT show (snapshotConversational).
		world.Actors["sleeper"] = &sim.Actor{
			ID: "sleeper", DisplayName: "Dozer", Kind: sim.KindNPCStateful,
			State: sim.StateSleeping, InsideStructureID: "stall",
		}
		zero := 0
		world.VillageObjects["stall"] = &sim.VillageObject{
			ID: "stall", AssetID: "stall-asset", DisplayName: "Blacksmith",
			Pos:           pin.Center(), // loiter pin == anchor == PC tile
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		}
		world.Assets = map[sim.AssetID]*sim.Asset{
			"stall-asset": {ID: "stall-asset", Name: "Stall", Category: "structure"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed stall pc/me world: %v", err)
	}

	srv := NewServer(w, okAuth{})
	resp := pcMe(t, srv)

	if resp.AudienceStructureID == nil || *resp.AudienceStructureID != "stall" {
		t.Errorf("audience_structure_id = %v, want stall", resp.AudienceStructureID)
	}
	names := map[string]bool{}
	for _, m := range resp.HuddleMembers {
		names[m.Name] = true
	}
	if !names["Smith"] {
		t.Errorf("roster missing the stall owner Smith: %+v", resp.HuddleMembers)
	}
	if !names["Nearby"] {
		t.Errorf("roster dropped the nearby player Nearby: %+v", resp.HuddleMembers)
	}
	if names["Dozer"] {
		t.Errorf("roster included the asleep NPC Dozer: %+v", resp.HuddleMembers)
	}
}

// TestHandlePCMe_LoiterShutStructureNoAudienceScope: LLM-359. A PC standing at a
// STRUCTURE's loiter pin is scoped to it (cross-threshold) only while the shop is
// OPEN — a keeper present and awake. When the keeper is abed the shop is shut and
// the PC gets no audience scope: the read-path mirror of the engine's
// conversationalScopeStructure gate, so a PC can no more talk through a closed
// shop's wall than an NPC can. (A bare prop like a well is exempt — see
// TestHandlePCMe_OutdoorAudienceScopeAndRoster, which keeps its well scope.)
func TestHandlePCMe_LoiterShutStructureNoAudienceScope(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	pin := sim.TilePos{X: 70, Y: 120}
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// A real structure (has a Structure entry → the open/closed gate applies).
		world.Structures["shop"] = &sim.Structure{ID: "shop", DisplayName: "Tavern"}
		world.Actors["p1"] = &sim.Actor{
			ID: "p1", DisplayName: "Tester", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester", Pos: pin,
		}
		// The shop's only keeper, abed inside → shop shut.
		world.Actors["keeper"] = &sim.Actor{
			ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCStateful,
			State: sim.StateSleeping, WorkStructureID: "shop", InsideStructureID: "shop",
		}
		zero := 0
		world.VillageObjects["shop"] = &sim.VillageObject{
			ID: "shop", AssetID: "shop-asset", DisplayName: "Tavern",
			Pos:           pin.Center(), // loiter pin == anchor == PC tile
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		}
		world.Assets = map[sim.AssetID]*sim.Asset{
			"shop-asset": {ID: "shop-asset", Name: "Tavern", Category: "structure"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed shut-shop pc/me world: %v", err)
	}

	srv := NewServer(w, okAuth{})
	if resp := pcMe(t, srv); resp.AudienceStructureID != nil {
		t.Errorf("shut shop: audience_structure_id = %v, want nil (the wall blocks the PC)", *resp.AudienceStructureID)
	}

	// Wake the keeper → shop OPEN → the PC is now scoped across the threshold.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["keeper"].State = sim.StateIdle
		return nil, nil
	}}); err != nil {
		t.Fatalf("wake keeper: %v", err)
	}
	if resp := pcMe(t, srv); resp.AudienceStructureID == nil || *resp.AudienceStructureID != "shop" {
		t.Errorf("open shop: audience_structure_id = %v, want shop", resp.AudienceStructureID)
	}
}

// indoorNoHuddlePCMeWorld stands up a PC inside the inn with NO huddle, plus
// co-located/nearby actors exercising every indoor-roster eligibility rule:
// a conversational NPC and a fresh PC (included); a decorative NPC, a sleeping
// NPC, an already-huddled NPC, and a presence-stale PC (excluded); and a
// conversational NPC in a different structure (excluded).
func indoorNoHuddlePCMeWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	now := time.Now().UTC()
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// PC indoors in the inn, CurrentHuddleID unset (no huddle yet).
		world.Actors["p1"] = &sim.Actor{
			ID: "p1", DisplayName: "Tester", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester",
			Pos: sim.TilePos{X: 10, Y: 10}, InsideStructureID: "inn",
		}
		// Co-located conversational NPC → surfaces in the roster.
		world.Actors["hannah"] = &sim.Actor{
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Role: "innkeeper", LLMAgent: "hannah-va",
			Pos: sim.TilePos{X: 10, Y: 10}, InsideStructureID: "inn",
		}
		// Co-located fresh PC → surfaces (PCs are conversational too).
		world.Actors["wanderer"] = &sim.Actor{
			ID: "wanderer", DisplayName: "Wanderer", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "wanderer",
			InsideStructureID: "inn", LastPCSeenAt: &now,
		}
		// Decorative → excluded.
		world.Actors["deco"] = &sim.Actor{
			ID: "deco", DisplayName: "Statue", Kind: sim.KindDecorative,
			State: sim.StateIdle, InsideStructureID: "inn",
		}
		// Asleep → excluded even though conversational + co-located.
		world.Actors["sleeper"] = &sim.Actor{
			ID: "sleeper", DisplayName: "Dozer", Kind: sim.KindNPCStateful,
			State: sim.StateSleeping, InsideStructureID: "inn",
		}
		// Already in THIS structure's active huddle → INCLUDED (ZBBS-HOME-363):
		// the PC's speak joins h1 (find-or-create at the inn), so busy is a peer
		// the player can talk to and must surface in the roster.
		world.Actors["busy"] = &sim.Actor{
			ID: "busy", DisplayName: "Busy", Kind: sim.KindNPCShared,
			State: sim.StateIdle, LLMAgent: "busy-va",
			InsideStructureID: "inn", CurrentHuddleID: "h1",
		}
		world.Huddles["h1"] = &sim.Huddle{
			ID: "h1", StructureID: "inn",
			Members: map[sim.ActorID]struct{}{"busy": {}},
		}
		// Co-located in the inn but huddled in a DIFFERENT structure's
		// conversation → excluded (ZBBS-HOME-363): the PC's join at the inn
		// won't pull them out of the barn's huddle, so they're not talkable.
		world.Actors["crossstruct"] = &sim.Actor{
			ID: "crossstruct", DisplayName: "Crosser", Kind: sim.KindNPCShared,
			State: sim.StateIdle, LLMAgent: "cross-va",
			InsideStructureID: "inn", CurrentHuddleID: "h2",
		}
		world.Huddles["h2"] = &sim.Huddle{
			ID: "h2", StructureID: "barn",
			Members: map[sim.ActorID]struct{}{"crossstruct": {}},
		}
		// Presence-stale PC (never polled → nil stamp is stale) → excluded,
		// mirroring the speak path's stale-PC exclusion.
		world.Actors["ghost"] = &sim.Actor{
			ID: "ghost", DisplayName: "Ghost", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "ghost",
			InsideStructureID: "inn", LastPCSeenAt: nil,
		}
		// Conversational NPC in a DIFFERENT structure → excluded.
		world.Actors["faraway"] = &sim.Actor{
			ID: "faraway", DisplayName: "Distant", Kind: sim.KindNPCShared,
			State: sim.StateIdle, InsideStructureID: "barn",
		}
		world.Structures["inn"] = &sim.Structure{ID: "inn", DisplayName: "The Inn"}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed indoor-no-huddle world: %v", err)
	}
	return w
}

// TestHandlePCMe_IndoorNoHuddleRoster covers ZBBS-HOME-371: a PC standing inside
// a structure with NPCs but not yet in a huddle must still get a non-empty
// roster, or the talk-panel launcher stays hidden and the player can never
// speak to form the huddle (HOME-358 forms it on speak). Only the co-located
// conversational NPC surfaces; decorative, sleeping, and other-structure actors
// are excluded.
func TestHandlePCMe_IndoorNoHuddleRoster(t *testing.T) {
	srv := NewServer(indoorNoHuddlePCMeWorld(t), okAuth{})
	resp := pcMe(t, srv)

	if resp.CurrentHuddleID != nil {
		t.Errorf("current_huddle_id = %v, want nil (PC has no huddle)", *resp.CurrentHuddleID)
	}
	// Eligible co-located, sorted by name: Busy (in THIS structure's huddle —
	// ZBBS-HOME-363 includes it), Hannah (NPC), Wanderer (fresh PC).
	// Excluded: Statue (decorative), Dozer (asleep), Crosser (huddled in a
	// different structure), Ghost (presence-stale PC), Distant (other structure).
	if len(resp.HuddleMembers) != 3 {
		t.Fatalf("len(huddle_members) = %d, want 3; got %+v", len(resp.HuddleMembers), resp.HuddleMembers)
	}
	if b := resp.HuddleMembers[0]; b.Kind != "npc" || b.Name != "Busy" || b.TargetAgent == nil || *b.TargetAgent != "busy-va" {
		t.Errorf("huddle_members[0] = %+v, want NPC Busy/busy-va (in this structure's huddle)", b)
	}
	if h := resp.HuddleMembers[1]; h.Kind != "npc" || h.Name != "Hannah" || h.TargetAgent == nil || *h.TargetAgent != "hannah-va" {
		t.Errorf("huddle_members[1] = %+v, want NPC Hannah/hannah-va", h)
	}
	if p := resp.HuddleMembers[2]; p.Kind != "pc" || p.Name != "Wanderer" || p.TargetAgent != nil {
		t.Errorf("huddle_members[2] = %+v, want PC Wanderer with no target_agent", p)
	}
}

// TestHandlePCMe_IndoorDormantMembers covers ZBBS-WORK-427: a co-present sleeper
// inside the PC's structure stays out of the talk/pay roster (HuddleMembers) but
// surfaces in DormantMembers tagged "asleep", so the panel can show an "(asleep)"
// chip for an indoor sleeper that has no visible map sprite. Reuses the indoor
// harness, whose only sleeping occupant is Dozer (Statue/Crosser/Ghost/Distant
// are awake, so none of them leak into the dormant list).
func TestHandlePCMe_IndoorDormantMembers(t *testing.T) {
	srv := NewServer(indoorNoHuddlePCMeWorld(t), okAuth{})
	resp := pcMe(t, srv)

	for _, m := range resp.HuddleMembers {
		if m.Name == "Dozer" {
			t.Fatalf("asleep NPC Dozer must not be in huddle_members: %+v", resp.HuddleMembers)
		}
	}
	if len(resp.DormantMembers) != 1 {
		t.Fatalf("len(dormant_members) = %d, want 1; got %+v", len(resp.DormantMembers), resp.DormantMembers)
	}
	if d := resp.DormantMembers[0]; d.Kind != "npc" || d.Name != "Dozer" || d.Status != "asleep" {
		t.Errorf("dormant_members[0] = %+v, want NPC Dozer tagged asleep", d)
	}
}

func TestHandlePCMe_InTransit(t *testing.T) {
	// PC outdoors with no loiter object in range → no audience structure.
	w := seededWorld(t)
	seedPC(t, w, "p1", "tester", 200, 200)
	srv := NewServer(w, okAuth{})
	resp := pcMe(t, srv)
	if resp.AudienceStructureID != nil {
		t.Errorf("audience_structure_id = %v, want nil in transit", *resp.AudienceStructureID)
	}
}

// outdoorPCMeWorld seeds an outdoor PC standing on a well's loiter pin, with one
// nearby PC (within the roster radius) and one far PC (outside it).
func outdoorPCMeWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	pin := sim.TilePos{X: 20, Y: 20}
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["p1"] = &sim.Actor{
			ID: "p1", DisplayName: "Tester", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester", Pos: pin,
		}
		world.Actors["near"] = &sim.Actor{
			ID: "near", DisplayName: "Nearby", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "near",
			Pos: sim.TilePos{X: 23, Y: 22}, // Chebyshev 3 <= 6
		}
		world.Actors["far"] = &sim.Actor{
			ID: "far", DisplayName: "Faraway", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "far",
			Pos: sim.TilePos{X: 40, Y: 40}, // Chebyshev 20 > 6
		}
		// Well with both loiter offsets zero → pin == anchor tile (20,20).
		zero := 0
		world.VillageObjects["well"] = &sim.VillageObject{
			ID: "well", AssetID: "well-asset", DisplayName: "Old Well",
			Pos:           pin.Center(),
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		}
		world.Assets = map[sim.AssetID]*sim.Asset{
			"well-asset": {ID: "well-asset", Name: "Well", Category: "nature"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed outdoor pc/me world: %v", err)
	}
	return w
}

func TestRenderActionLogEntry(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"npc": {DisplayName: "Hannah"},
			"pc":  {DisplayName: "Tester", LoginUsername: "tester"},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nights_stay": {Name: "nights_stay", DisplayLabel: "Night's Stay", Capabilities: []string{"service", "lodging"}},
			"ale":         {Name: "ale", DisplayLabel: "Ale"},
		},
	}
	cases := []struct {
		name        string
		entry       sim.ActionLogEntry
		wantSpeaker string
		wantText    string
		wantKind    string
		wantOK      bool
	}{
		{"npc speech", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeSpoke, Text: "Hi"}, "Hannah", "Hi", "speech_npc", true},
		{"pc speech", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeSpoke, Text: "Yo"}, "Tester", "Yo", "speech_player", true},
		{"empty speech skipped", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeSpoke, Text: ""}, "", "", "", false},
		{"paid recipient amount for", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypePaid, CounterpartyName: "Hannah", Amount: 3, Text: "a round"}, "Tester", "Tester pays Hannah 3 coins for a round.", "act", true},
		{"paid recipient amount no for", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypePaid, CounterpartyName: "Hannah", Amount: 3, Text: ""}, "Tester", "Tester pays Hannah 3 coins.", "act", true},
		{"paid recipient one coin", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypePaid, CounterpartyName: "Hannah", Amount: 1, Text: "bread"}, "Tester", "Tester pays Hannah 1 coin for bread.", "act", true},
		{"paid recipient no amount", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypePaid, CounterpartyName: "Hannah", Amount: 0, Text: ""}, "Tester", "Tester pays Hannah.", "act", true},
		{"paid no recipient", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypePaid, CounterpartyName: "", Amount: 5, Text: "a round"}, "Tester", "Tester makes a payment.", "act", true},
		{"consumed", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeConsumed, Text: "stew"}, "Tester", "Tester consumes stew.", "act", true},
		{"consumed empty skipped", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeConsumed, Text: ""}, "", "", "", false},
		// LLM-273: gather narrates the harvest and names the source object,
		// with the walked-line WithDefiniteArticle treatment.
		{"gathered names common-noun source", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeGathered, Text: "20x water", CounterpartyName: "Well"}, "Tester", "Tester gathers 20x water from the Well.", "act", true},
		{"gathered qty1 already-articled source", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeGathered, Text: "water", CounterpartyName: "the Village Well"}, "Tester", "Tester gathers water from the Village Well.", "act", true},
		{"gathered no source drops clause", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeGathered, Text: "5x berries", CounterpartyName: ""}, "Tester", "Tester gathers 5x berries.", "act", true},
		{"gathered empty skipped", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeGathered, Text: ""}, "", "", "", false},
		// LLM-354: a started mend names the business (never "his stall" — a hired
		// hand mends someone else's), with the walked-line article treatment.
		{"repairing names common-noun business", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeRepairing, Text: "General Store"}, "Hannah", "Hannah is mending the General Store.", "act", true},
		{"repairing possessive keeps no article", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeRepairing, Text: "Hannah's Inn"}, "Hannah", "Hannah is mending Hannah's Inn.", "act", true},
		{"repairing no business drops clause", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeRepairing, Text: ""}, "Hannah", "Hannah is making repairs.", "act", true},
		{"delivered with recipient", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDelivered, Text: "ale", CounterpartyName: "Tester"}, "Hannah", "Hannah delivers ale to Tester.", "act", true},
		{"delivered no recipient", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDelivered, Text: "ale"}, "Hannah", "Hannah delivers ale.", "act", true},
		// ZBBS-HOME-432: a lodging-capability delivery narrates as a check-in,
		// not a parcel handoff.
		{"delivered lodging is a check-in", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDelivered, Text: "nights_stay", CounterpartyName: "Tester"}, "Hannah", "Hannah shows Tester to a room — it's theirs for the night.", "act", true},
		{"delivered lodging no recipient", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDelivered, Text: "nights_stay"}, "Hannah", "Hannah readies a room for a guest.", "act", true},
		// Multi-qty lodging text ("2x nights_stay") misses the catalog and
		// falls back to the generic delivery line.
		{"delivered multi-qty lodging falls back", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDelivered, Text: "2x nights_stay", CounterpartyName: "Tester"}, "Hannah", "Hannah delivers 2x nights_stay to Tester.", "act", true},
		{"walked to dest", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeWalked, Text: "The Inn"}, "Tester", "Tester arrives at The Inn.", "act", true},
		{"walked no dest", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeWalked, Text: ""}, "Tester", "Tester arrives.", "act", true},
		{"walked common-noun dest gets article", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeWalked, Text: "Tavern"}, "Tester", "Tester arrives at the Tavern.", "act", true},
		{"departed common-noun place", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeDeparted, Text: "Tavern"}, "Tester", "Tester leaves the Tavern.", "act", true},
		{"departed possessive keeps no article", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeDeparted, Text: "Hannah's Inn"}, "Tester", "Tester leaves Hannah's Inn.", "act", true},
		{"departed no place", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeDeparted, Text: ""}, "Tester", "Tester leaves.", "act", true},
		{"took break", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeTookBreak, Text: "tired"}, "Tester", "Tester steps away.", "act", true},
		// LLM-213: labor conversational beats. Amount shown when present (consistent
		// with the paid/labored lines); degrade on missing counterparty/amount.
		{"solicited work amount recipient", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeSolicitedWork, CounterpartyName: "Tester", Amount: 4}, "Hannah", "Hannah offers to work for Tester for 4 coins.", "act", true},
		{"solicited work one coin", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeSolicitedWork, CounterpartyName: "Tester", Amount: 1}, "Hannah", "Hannah offers to work for Tester for 1 coin.", "act", true},
		{"solicited work recipient no amount", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeSolicitedWork, CounterpartyName: "Tester", Amount: 0}, "Hannah", "Hannah offers to work for Tester.", "act", true},
		{"solicited work no recipient", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeSolicitedWork, CounterpartyName: "", Amount: 4}, "Hannah", "Hannah offers to work for coin.", "act", true},
		{"hired amount recipient", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeHired, CounterpartyName: "Hannah", Amount: 4}, "Tester", "Tester hires Hannah for 4 coins.", "act", true},
		{"hired recipient no amount", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeHired, CounterpartyName: "Hannah", Amount: 0}, "Tester", "Tester hires Hannah.", "act", true},
		{"hired no recipient", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeHired, CounterpartyName: "", Amount: 4}, "Tester", "Tester takes someone on.", "act", true},
		// LLM-283: pay-ledger negotiation beats (feed-only). Offered degrades like
		// the paid line; declined/countered name the counterparty.
		{"offered amount item", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeOffered, CounterpartyName: "Hannah", Amount: 3, Text: "3x milk"}, "Tester", "Tester offers Hannah 3 coins for 3x milk.", "act", true},
		{"offered one coin", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeOffered, CounterpartyName: "Hannah", Amount: 1, Text: "bread"}, "Tester", "Tester offers Hannah 1 coin for bread.", "act", true},
		{"offered goods-only drops amount", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeOffered, CounterpartyName: "Hannah", Amount: 0, Text: "3x milk"}, "Tester", "Tester offers Hannah for 3x milk.", "act", true},
		{"offered no counterparty", sim.ActionLogEntry{ActorID: "pc", ActionType: sim.ActionTypeOffered, CounterpartyName: "", Amount: 3, Text: "3x milk"}, "Tester", "Tester makes an offer.", "act", true},
		{"declined with buyer", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDeclined, CounterpartyName: "Tester"}, "Hannah", "Hannah declines Tester's offer.", "act", true},
		{"declined no counterparty", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeDeclined, CounterpartyName: ""}, "Hannah", "Hannah declines an offer.", "act", true},
		{"countered amount", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeCountered, CounterpartyName: "Tester", Amount: 5}, "Hannah", "Hannah counters at 5 coins.", "act", true},
		{"countered one coin", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeCountered, CounterpartyName: "Tester", Amount: 1}, "Hannah", "Hannah counters at 1 coin.", "act", true},
		{"countered goods-only names buyer", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeCountered, CounterpartyName: "Tester", Amount: 0}, "Hannah", "Hannah counters Tester's offer.", "act", true},
		{"countered goods-only no counterparty", sim.ActionLogEntry{ActorID: "npc", ActionType: sim.ActionTypeCountered, CounterpartyName: "", Amount: 0}, "Hannah", "Hannah makes a counteroffer.", "act", true},
		{"unknown actor skipped", sim.ActionLogEntry{ActorID: "ghost", ActionType: sim.ActionTypeSpoke, Text: "boo"}, "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			speaker, text, kind, ok := renderActionLogEntry(snap, tc.entry)
			if speaker != tc.wantSpeaker || text != tc.wantText || kind != tc.wantKind || ok != tc.wantOK {
				t.Errorf("got (%q,%q,%q,%v), want (%q,%q,%q,%v)",
					speaker, text, kind, ok, tc.wantSpeaker, tc.wantText, tc.wantKind, tc.wantOK)
			}
		})
	}
}
