package httpapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestTranslateEvent_MoveStarted(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorMoveStarted{
		ActorID:        "hannah",
		FromPosition:   sim.Position{X: 3, Y: 4},
		TargetPosition: sim.Position{X: 5, Y: 4},
		Path: []sim.GridPoint{
			{X: 3, Y: 4}, {X: 4, Y: 4}, {X: 5, Y: 4},
		},
		DestinationKind:   sim.MoveDestinationStructureEnter,
		StructureID:       "tavern",
		MovementAttemptID: 7,
	})
	if !ok {
		t.Fatal("ActorMoveStarted should translate")
	}
	if frame.Type != "npc_walking" {
		t.Fatalf("type = %q, want npc_walking", frame.Type)
	}
	d, isType := frame.Data.(walkWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want walkWireDTO", frame.Data)
	}
	if d.ID != "hannah" || d.DestKind != "structure_enter" || d.StructureID != "tavern" || d.AttemptID != 7 {
		t.Errorf("walk payload scalar fields = %+v", d)
	}
	wantPath := []tilePointDTO{{X: 3, Y: 4}, {X: 4, Y: 4}, {X: 5, Y: 4}}
	if !reflect.DeepEqual(d.Path, wantPath) {
		t.Errorf("walk path = %+v, want %+v", d.Path, wantPath)
	}
}

func TestTranslateEvent_ObjectMoved(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectMoved{
		ObjectID: "bench-1",
		X:        150.5,
		Y:        175.25,
	})
	if !ok {
		t.Fatal("VillageObjectMoved should translate")
	}
	if frame.Type != "object_moved" {
		t.Fatalf("type = %q, want object_moved", frame.Type)
	}
	d := frame.Data.(objectMovedWireDTO)
	want := objectMovedWireDTO{ID: "bench-1", X: 150.5, Y: 175.25}
	if d != want {
		t.Errorf("object_moved payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_NoticeboardContentChanged(t *testing.T) {
	postedAt := time.Date(2026, 5, 22, 18, 0, 0, 0, time.UTC)
	frame, ok := TranslateEvent(&sim.NoticeboardContentChanged{
		ObjectID: "board-1",
		Text:     "Town meeting at dusk.",
		PostedAt: postedAt,
		At:       time.Now().UTC(),
	})
	if !ok {
		t.Fatal("NoticeboardContentChanged should translate")
	}
	if frame.Type != "noticeboard_content_changed" {
		t.Fatalf("type = %q, want noticeboard_content_changed", frame.Type)
	}
	d := frame.Data.(noticeboardContentChangedWireDTO)
	want := noticeboardContentChangedWireDTO{ID: "board-1", ContentText: "Town meeting at dusk.", ContentPostedAt: postedAt}
	if d != want {
		t.Errorf("payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_ObjectDeleted(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectDeleted{ObjectID: "bench-1"})
	if !ok {
		t.Fatal("VillageObjectDeleted should translate")
	}
	if frame.Type != "object_deleted" {
		t.Fatalf("type = %q, want object_deleted", frame.Type)
	}
	d := frame.Data.(objectDeletedWireDTO)
	if d != (objectDeletedWireDTO{ID: "bench-1"}) {
		t.Errorf("object_deleted payload = %+v, want {bench-1}", d)
	}
}

func TestTranslateEvent_Arrived(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorArrived{
		ActorID:           "hannah",
		FinalPosition:     sim.Position{X: 10, Y: 12},
		FinalStructureID:  "tavern",
		MovementAttemptID: 7,
	})
	if !ok {
		t.Fatal("ActorArrived should translate")
	}
	if frame.Type != "npc_arrived" {
		t.Fatalf("type = %q, want npc_arrived", frame.Type)
	}
	d := frame.Data.(arrivedWireDTO)
	want := arrivedWireDTO{ID: "hannah", X: 10, Y: 12, StructureID: "tavern", AttemptID: 7}
	if d != want {
		t.Errorf("arrived payload = %+v, want %+v", d, want)
	}
}

// TestTranslateEvent_Teleported covers the ZBBS-HOME-448 operator teleport:
// ActorTeleported reuses the npc_arrived frame (the client's authoritative
// snap-to-tile) with AttemptID 0 — there was no movement attempt.
func TestTranslateEvent_Teleported(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorTeleported{
		ActorID:           "grace",
		FromPosition:      sim.Position{X: 85, Y: 143},
		ToPosition:        sim.Position{X: 87, Y: 145},
		InsideStructureID: "",
	})
	if !ok {
		t.Fatal("ActorTeleported should translate")
	}
	if frame.Type != "npc_arrived" {
		t.Fatalf("type = %q, want npc_arrived", frame.Type)
	}
	d := frame.Data.(arrivedWireDTO)
	want := arrivedWireDTO{ID: "grace", X: 87, Y: 145, StructureID: "", AttemptID: 0}
	if d != want {
		t.Errorf("teleported payload = %+v, want %+v", d, want)
	}
}

// TestTranslateEvent_InsideChanged covers the ZBBS-WORK-373 inside-state push:
// an actor inside a structure maps to npc_inside_changed with inside=true and
// the structure id, the exact frame the client's apply_npc_inside_change handler
// consumes to render sprite visibility + the see-through-structure stand offset.
func TestTranslateEvent_InsideChanged(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorInsideChanged{
		ActorID:           "john",
		InsideStructureID: "tavern",
		X:                 12,
		Y:                 34,
	})
	if !ok {
		t.Fatal("ActorInsideChanged should translate")
	}
	if frame.Type != "npc_inside_changed" {
		t.Fatalf("type = %q, want npc_inside_changed", frame.Type)
	}
	d := frame.Data.(insideChangedWireDTO)
	// X/Y carry the actor's tile so the client snaps the sprite to engine truth
	// before flipping visibility (ZBBS-HOME-464).
	want := insideChangedWireDTO{ID: "john", Inside: true, InsideStructureID: "tavern", X: 12, Y: 34}
	if d != want {
		t.Errorf("inside_changed payload = %+v, want %+v", d, want)
	}
}

// TestTranslateEvent_InsideChangedOutdoors covers the leave case (the Finding-6
// fix): an empty InsideStructureID maps to inside=false — the bool the client
// reads to un-hide / drop the stand offset and let the sprite walk away — with
// the structure id omitted from the wire (the client reads a missing value as "").
func TestTranslateEvent_InsideChangedOutdoors(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorInsideChanged{
		ActorID:           "john",
		InsideStructureID: "",
	})
	if !ok {
		t.Fatal("ActorInsideChanged should translate")
	}
	d := frame.Data.(insideChangedWireDTO)
	want := insideChangedWireDTO{ID: "john", Inside: false, InsideStructureID: ""}
	if d != want {
		t.Errorf("inside_changed payload = %+v, want %+v", d, want)
	}
}

// TestTranslateEvent_InsideChangedWireOmitsStructureOutdoors verifies the
// MARSHALED npc_inside_changed frame: inside is always present, and
// inside_structure_id is omitted when outdoors (the contract relies on the
// omitempty tag — a struct type-assertion wouldn't catch a tag/remarshal
// regression). code_review follow-up.
func TestTranslateEvent_InsideChangedWireOmitsStructureOutdoors(t *testing.T) {
	// Outdoors: inside:false present, inside_structure_id absent, x/y present.
	// X:0 proves a zero tile is still sent — omitempty would wrongly drop a real
	// edge-of-grid coordinate (ZBBS-HOME-464).
	outFrame, _ := TranslateEvent(&sim.ActorInsideChanged{ActorID: "john", InsideStructureID: "", X: 0, Y: 5})
	out, err := json.Marshal(outFrame.Data)
	if err != nil {
		t.Fatalf("marshal outdoors: %v", err)
	}
	if got := string(out); !strings.Contains(got, `"inside":false`) || strings.Contains(got, "inside_structure_id") {
		t.Errorf("outdoors frame should carry \"inside\":false and omit inside_structure_id; got %s", got)
	}
	if got := string(out); !strings.Contains(got, `"x":0`) || !strings.Contains(got, `"y":5`) {
		t.Errorf("outdoors frame should carry x/y (including a zero tile); got %s", got)
	}
	// Inside: inside:true + the structure id present.
	inFrame, _ := TranslateEvent(&sim.ActorInsideChanged{ActorID: "john", InsideStructureID: "tavern"})
	in, err := json.Marshal(inFrame.Data)
	if err != nil {
		t.Fatalf("marshal inside: %v", err)
	}
	if got := string(in); !strings.Contains(got, `"inside":true`) || !strings.Contains(got, `"inside_structure_id":"tavern"`) {
		t.Errorf("inside frame should carry \"inside\":true + structure id; got %s", got)
	}
}

func TestTranslateEvent_MoveStopped(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ActorMoveStopped{
		ActorID:           "hannah",
		Position:          sim.Position{X: 5, Y: 6},
		Reason:            sim.MoveStoppedBlocked,
		MovementAttemptID: 7,
	})
	if !ok {
		t.Fatal("ActorMoveStopped should translate")
	}
	if frame.Type != "npc_move_stopped" {
		t.Fatalf("type = %q, want npc_move_stopped", frame.Type)
	}
	d := frame.Data.(moveStoppedWireDTO)
	want := moveStoppedWireDTO{ID: "hannah", X: 5, Y: 6, Reason: "blocked", AttemptID: 7}
	if d != want {
		t.Errorf("stopped payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_Spoke(t *testing.T) {
	at := time.Date(1692, 5, 14, 12, 30, 0, 0, time.FixedZone("local", -4*3600))
	frame, ok := TranslateEvent(&sim.Spoke{
		SpeakerID:    "hannah",
		HuddleID:     "huddle-1",
		RecipientIDs: []sim.ActorID{"bram", "ezekiel"},
		Text:         "Good evening.",
		At:           at,
	})
	if !ok {
		t.Fatal("Spoke should translate")
	}
	if frame.Type != "npc_spoke" {
		t.Fatalf("type = %q, want npc_spoke", frame.Type)
	}
	d, isType := frame.Data.(spokeWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want spokeWireDTO", frame.Data)
	}
	if d.ID != "hannah" || d.HuddleID != "huddle-1" || d.Text != "Good evening." {
		t.Errorf("spoke scalar fields = %+v", d)
	}
	if !reflect.DeepEqual(d.RecipientIDs, []string{"bram", "ezekiel"}) {
		t.Errorf("recipients = %+v, want [bram ezekiel]", d.RecipientIDs)
	}
	if d.At != at.UTC().Format(time.RFC3339) {
		t.Errorf("at = %q, want %q", d.At, at.UTC().Format(time.RFC3339))
	}

	// ZBBS-HOME-437: PC bystanders merge into the frame's recipient_ids (the
	// client's render-audience check) after the huddle members. The hub
	// broadcasts every frame to every client; this merged list is what lets a
	// non-member PC's talk panel overhear the line.
	frame2, ok2 := TranslateEvent(&sim.Spoke{
		SpeakerID:      "hannah",
		HuddleID:       "huddle-1",
		RecipientIDs:   []sim.ActorID{"bram", "ezekiel"},
		PCBystanderIDs: []sim.ActorID{"pc-1"},
		Text:           "Good evening.",
		At:             at,
	})
	if !ok2 {
		t.Fatal("Spoke with bystanders should translate")
	}
	d2 := frame2.Data.(spokeWireDTO)
	if !reflect.DeepEqual(d2.RecipientIDs, []string{"bram", "ezekiel", "pc-1"}) {
		t.Errorf("recipients with bystander = %+v, want [bram ezekiel pc-1]", d2.RecipientIDs)
	}

	// recipient_ids stays a SET: an id appearing in both lists (can't happen
	// from pcBystanders, but the translator defines the wire contract) must
	// not duplicate (code_review).
	frame3, _ := TranslateEvent(&sim.Spoke{
		SpeakerID:      "hannah",
		HuddleID:       "huddle-1",
		RecipientIDs:   []sim.ActorID{"bram", "pc-1"},
		PCBystanderIDs: []sim.ActorID{"pc-1", "pc-2"},
		Text:           "Good evening.",
		At:             at,
	})
	d3 := frame3.Data.(spokeWireDTO)
	if !reflect.DeepEqual(d3.RecipientIDs, []string{"bram", "pc-1", "pc-2"}) {
		t.Errorf("recipients with overlap = %+v, want [bram pc-1 pc-2] (deduped)", d3.RecipientIDs)
	}
	// SpeechID aliases the event's EventID, which is 0 until World.emit stamps
	// it. This event was constructed directly (never emitted), so 0 is expected
	// here; the live path stamps it via emit. Asserted so a mapping change is caught.
	if d.SpeechID != 0 {
		t.Errorf("speech_id = %d, want 0 for an un-emitted event", d.SpeechID)
	}
}

// TestTranslateEvent_SpokeEmptyHuddle: speaking to no one still translates (a
// valid v2 state). Asserts the WIRE shape (marshal-level), since omitempty only
// applies during marshaling — checking the typed DTO wouldn't catch a contract
// regression: huddle_id must be absent, recipient_ids must be [] (not null).
func TestTranslateEvent_SpokeEmptyHuddle(t *testing.T) {
	frame, ok := TranslateEvent(&sim.Spoke{SpeakerID: "bram", Text: "Anyone here?"})
	if !ok {
		t.Fatal("Spoke with empty huddle should still translate")
	}
	b, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(b, []byte(`"huddle_id"`)) {
		t.Errorf("huddle_id should be omitted for empty huddle: %s", b)
	}
	if !bytes.Contains(b, []byte(`"recipient_ids":[]`)) {
		t.Errorf("recipient_ids should marshal as [], got: %s", b)
	}
}

func TestTranslateEvent_PhaseApplied(t *testing.T) {
	frame, ok := TranslateEvent(&sim.PhaseApplied{From: sim.PhaseDay, To: sim.PhaseNight})
	if !ok {
		t.Fatal("PhaseApplied should translate")
	}
	if frame.Type != "world_phase_changed" {
		t.Fatalf("type = %q, want world_phase_changed", frame.Type)
	}
	d, isType := frame.Data.(phaseChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want phaseChangedWireDTO", frame.Data)
	}
	if d.Phase != "night" {
		t.Errorf("phase = %q, want night", d.Phase)
	}
}

// TestTranslateEvent_PhaseAppliedIdempotent: an admin force-phase to the current
// phase emits with From == To and still translates (the client treats it as a
// harmless no-op set).
func TestTranslateEvent_PhaseAppliedIdempotent(t *testing.T) {
	frame, ok := TranslateEvent(&sim.PhaseApplied{From: sim.PhaseDay, To: sim.PhaseDay})
	if !ok {
		t.Fatal("idempotent PhaseApplied should still translate")
	}
	if d := frame.Data.(phaseChangedWireDTO); d.Phase != "day" {
		t.Errorf("phase = %q, want day", d.Phase)
	}
}

func TestTranslateEvent_VillageObjectStateChanged(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectStateChanged{
		ObjectID:  "lamp-3",
		FromState: "unlit",
		ToState:   "lit",
	})
	if !ok {
		t.Fatal("VillageObjectStateChanged should translate")
	}
	if frame.Type != "object_state_changed" {
		t.Fatalf("type = %q, want object_state_changed", frame.Type)
	}
	d, isType := frame.Data.(objectStateChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want objectStateChangedWireDTO", frame.Data)
	}
	if d.ID != "lamp-3" || d.State != "lit" {
		t.Errorf("object state fields = %+v, want {lamp-3 lit}", d)
	}
}

func TestTranslateEvent_NPCDormancyChanged(t *testing.T) {
	frame, ok := TranslateEvent(&sim.NPCDormancyChanged{ActorID: "ezekiel", State: "sleeping"})
	if !ok {
		t.Fatal("NPCDormancyChanged should translate")
	}
	if frame.Type != "npc_dormancy_changed" {
		t.Fatalf("type = %q, want npc_dormancy_changed", frame.Type)
	}
	d, isType := frame.Data.(npcDormancyChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want npcDormancyChangedWireDTO", frame.Data)
	}
	if d.ID != "ezekiel" || d.State != "sleeping" {
		t.Errorf("dormancy fields = %+v, want {ezekiel sleeping}", d)
	}
}

func TestTranslateEvent_VillageObjectDisplayNameChanged(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectDisplayNameChanged{
		ObjectID:    "tavern",
		DisplayName: "The Crow's Nest",
	})
	if !ok {
		t.Fatal("VillageObjectDisplayNameChanged should translate")
	}
	if frame.Type != "object_display_name_changed" {
		t.Fatalf("type = %q, want object_display_name_changed", frame.Type)
	}
	d, isType := frame.Data.(objectDisplayNameChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want objectDisplayNameChangedWireDTO", frame.Data)
	}
	if d.ID != "tavern" || d.DisplayName != "The Crow's Nest" {
		t.Errorf("display-name fields = %+v, want {tavern The Crow's Nest}", d)
	}
}

func TestTranslateEvent_VillageObjectTagsUpdated(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectTagsUpdated{
		ObjectID: "tavern",
		Tags:     []string{"vendor", "innkeeper"},
	})
	if !ok {
		t.Fatal("VillageObjectTagsUpdated should translate")
	}
	if frame.Type != "village_object_tags_updated" {
		t.Fatalf("type = %q, want village_object_tags_updated", frame.Type)
	}
	d, isType := frame.Data.(objectTagsUpdatedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want objectTagsUpdatedWireDTO", frame.Data)
	}
	if d.ID != "tavern" || len(d.Tags) != 2 || d.Tags[0] != "vendor" || d.Tags[1] != "innkeeper" {
		t.Errorf("tags fields = %+v, want {tavern [vendor innkeeper]}", d)
	}
}

// TestTranslateEvent_VillageObjectTagsUpdated_NilIsEmptyArray pins the
// "always an array, never null" wire contract: a nil tag set (last tag removed)
// must marshal as [] so the client tag handler doesn't choke on a JSON null.
func TestTranslateEvent_VillageObjectTagsUpdated_NilIsEmptyArray(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectTagsUpdated{ObjectID: "tavern", Tags: nil})
	if !ok {
		t.Fatal("VillageObjectTagsUpdated should translate")
	}
	d := frame.Data.(objectTagsUpdatedWireDTO)
	if d.Tags == nil {
		t.Fatal("Tags is nil; want a non-nil empty slice so it marshals as []")
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"tags":[]`) {
		t.Errorf("marshaled = %s, want tags as []", b)
	}
}

func TestTranslateEvent_VillageObjectLoiterOffsetChanged(t *testing.T) {
	x, y := 4, -3
	frame, ok := TranslateEvent(&sim.VillageObjectLoiterOffsetChanged{
		ObjectID:               "bench-1",
		LoiterOffsetX:          &x,
		LoiterOffsetY:          &y,
		EffectiveLoiterOffsetX: 4,
		EffectiveLoiterOffsetY: -3,
	})
	if !ok {
		t.Fatal("VillageObjectLoiterOffsetChanged should translate")
	}
	if frame.Type != "object_loiter_offset_changed" {
		t.Fatalf("type = %q, want object_loiter_offset_changed", frame.Type)
	}
	d, isType := frame.Data.(objectLoiterOffsetChangedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want objectLoiterOffsetChangedWireDTO", frame.Data)
	}
	if d.ID != "bench-1" || d.LoiterOffsetX == nil || *d.LoiterOffsetX != 4 ||
		d.EffectiveLoiterOffsetX != 4 || d.EffectiveLoiterOffsetY != -3 {
		t.Errorf("loiter payload = %+v, want bench-1 raw(4,-3) eff(4,-3)", d)
	}
}

// TestTranslateEvent_VillageObjectLoiterOffsetChanged_ClearedIsNull pins that a
// cleared override (nil raw) marshals loiter_offset_x/y as JSON null (so the
// editor can tell "cleared" from absent) while effective still carries the
// fallback value.
func TestTranslateEvent_VillageObjectLoiterOffsetChanged_ClearedIsNull(t *testing.T) {
	frame, ok := TranslateEvent(&sim.VillageObjectLoiterOffsetChanged{
		ObjectID:               "bench-1",
		LoiterOffsetX:          nil,
		LoiterOffsetY:          nil,
		EffectiveLoiterOffsetX: 0,
		EffectiveLoiterOffsetY: 2,
	})
	if !ok {
		t.Fatal("VillageObjectLoiterOffsetChanged should translate")
	}
	b, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"loiter_offset_x":null`) || !strings.Contains(string(b), `"effective_loiter_offset_y":2`) {
		t.Errorf("marshaled = %s, want loiter_offset_x null + effective_loiter_offset_y 2", b)
	}
}

func TestTranslateEvent_PayOffer(t *testing.T) {
	at := time.Date(1692, 5, 14, 12, 30, 0, 0, time.FixedZone("local", -4*3600))
	frame, ok := TranslateEvent(&sim.PayOfferReceived{
		LedgerID:       42,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 2,
		Amount:         5,
		ConsumeNow:     true,
		HuddleID:       "huddle-1",
		SceneID:        "sc1",
		At:             at,
	})
	if !ok {
		t.Fatal("PayOfferReceived should translate")
	}
	if frame.Type != "pay_offer" {
		t.Fatalf("type = %q, want pay_offer", frame.Type)
	}
	d, isType := frame.Data.(payOfferWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want payOfferWireDTO", frame.Data)
	}
	want := payOfferWireDTO{
		LedgerID: 42, BuyerID: "alice", SellerID: "bob", Item: "stew",
		Qty: 2, Amount: 5, ConsumeNow: true, HuddleID: "huddle-1", SceneID: "sc1",
		At: at.UTC().Format(time.RFC3339),
	}
	if d != want {
		t.Errorf("pay_offer payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_PayCountered(t *testing.T) {
	at := time.Date(1692, 5, 14, 12, 30, 0, 0, time.FixedZone("local", -4*3600))
	frame, ok := TranslateEvent(&sim.PayCountered{
		ParentID:       42,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 1,
		OriginalAmount: 4,
		CounterAmount:  7,
		Message:        "how about seven",
		HuddleID:       "huddle-1",
		SceneID:        "sc1",
		At:             at,
	})
	if !ok {
		t.Fatal("PayCountered should translate")
	}
	if frame.Type != "pay_countered" {
		t.Fatalf("type = %q, want pay_countered", frame.Type)
	}
	d, isType := frame.Data.(payCounteredWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want payCounteredWireDTO", frame.Data)
	}
	// ledger_id carries the PARENT (countered) entry's id.
	want := payCounteredWireDTO{
		LedgerID: 42, BuyerID: "alice", SellerID: "bob", Item: "stew", Qty: 1,
		OriginalAmount: 4, CounterAmount: 7, Message: "how about seven",
		HuddleID: "huddle-1", SceneID: "sc1", At: at.UTC().Format(time.RFC3339),
	}
	if d != want {
		t.Errorf("pay_countered payload = %+v, want %+v", d, want)
	}
}

func TestTranslateEvent_PayResolved(t *testing.T) {
	at := time.Date(1692, 5, 14, 12, 30, 0, 0, time.FixedZone("local", -4*3600))
	frame, ok := TranslateEvent(&sim.PayWithItemResolved{
		LedgerID:       42,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 1,
		Amount:         4,
		TerminalState:  sim.PayTerminalStateDeclined,
		Message:        "not today",
		HuddleID:       "huddle-1",
		SceneID:        "sc1",
		At:             at,
	})
	if !ok {
		t.Fatal("PayWithItemResolved should translate")
	}
	if frame.Type != "pay_resolved" {
		t.Fatalf("type = %q, want pay_resolved", frame.Type)
	}
	d, isType := frame.Data.(payResolvedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want payResolvedWireDTO", frame.Data)
	}
	want := payResolvedWireDTO{
		LedgerID: 42, BuyerID: "alice", SellerID: "bob", Item: "stew", Qty: 1, Amount: 4,
		TerminalState: string(sim.PayTerminalStateDeclined), Message: "not today",
		HuddleID: "huddle-1", SceneID: "sc1", At: at.UTC().Format(time.RFC3339),
	}
	if d != want {
		t.Errorf("pay_resolved payload = %+v, want %+v", d, want)
	}
}

// ZBBS-WORK-420: an instant quote-take fast-path accept carries
// BuyerTookQuote=true through to the wire so the client words it "you took
// their offer" rather than the backwards "they accepted your offer".
func TestTranslateEvent_PayResolved_BuyerTookQuote(t *testing.T) {
	at := time.Date(1692, 5, 14, 12, 30, 0, 0, time.FixedZone("local", -4*3600))
	frame, ok := TranslateEvent(&sim.PayWithItemResolved{
		LedgerID:       7,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 1,
		Amount:         8,
		TerminalState:  sim.PayTerminalStateAccepted,
		BuyerTookQuote: true,
		HuddleID:       "huddle-1",
		SceneID:        "sc1",
		At:             at,
	})
	if !ok {
		t.Fatal("PayWithItemResolved should translate")
	}
	d := frame.Data.(payResolvedWireDTO)
	if !d.BuyerTookQuote {
		t.Errorf("buyer_took_quote = false, want true (fast-path take)")
	}
	if d.TerminalState != string(sim.PayTerminalStateAccepted) {
		t.Errorf("terminal_state = %q, want accepted", d.TerminalState)
	}
}

func TestTranslateEvent_PCSleepStarted(t *testing.T) {
	at := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)
	wakeAt := at.Add(12 * time.Hour)
	frame, ok := TranslateEvent(&sim.PCSleepStarted{ActorID: "player-1", WakeAt: wakeAt, At: at})
	if !ok {
		t.Fatal("PCSleepStarted should translate")
	}
	if frame.Type != "pc_sleep_started" {
		t.Fatalf("type = %q, want pc_sleep_started", frame.Type)
	}
	d := frame.Data.(pcSleepStartedWireDTO)
	want := pcSleepStartedWireDTO{
		ActorID: "player-1",
		WakeAt:  wakeAt.UTC().Format(time.RFC3339),
		At:      at.UTC().Format(time.RFC3339),
	}
	if d != want {
		t.Errorf("pc_sleep_started payload = %+v, want %+v", d, want)
	}
	// The client reads actor_id + wake_at (event_client.gd) — assert the json
	// keys, not just the struct fields.
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"actor_id"`, `"wake_at"`, `"at"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("pc_sleep_started json missing %s: %s", key, b)
		}
	}
}

func TestTranslateEvent_PCSleepEnded(t *testing.T) {
	at := time.Date(2026, 5, 25, 23, 0, 0, 0, time.UTC)
	frame, ok := TranslateEvent(&sim.PCSleepEnded{ActorID: "player-1", Reason: "manual", At: at})
	if !ok {
		t.Fatal("PCSleepEnded should translate")
	}
	if frame.Type != "pc_sleep_ended" {
		t.Fatalf("type = %q, want pc_sleep_ended", frame.Type)
	}
	d := frame.Data.(pcSleepEndedWireDTO)
	want := pcSleepEndedWireDTO{ActorID: "player-1", Reason: "manual", At: at.UTC().Format(time.RFC3339)}
	if d != want {
		t.Errorf("pc_sleep_ended payload = %+v, want %+v", d, want)
	}
	// The client reads actor_id + reason (event_client.gd).
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"actor_id"`, `"reason"`, `"at"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("pc_sleep_ended json missing %s: %s", key, b)
		}
	}
}

func TestTranslateEvent_PCRelocatedToCommon(t *testing.T) {
	at := time.Date(2026, 5, 25, 11, 0, 0, 0, time.UTC)
	frame, ok := TranslateEvent(&sim.PCRelocatedToCommon{
		ActorID:     "player-1",
		StructureID: "inn",
		Reason:      sim.LodgingReasonCheckout,
		Text:        "Your stay has ended — you head down to the common area.",
		At:          at,
	})
	if !ok {
		t.Fatal("PCRelocatedToCommon should translate")
	}
	if frame.Type != "room_event" {
		t.Fatalf("type = %q, want room_event", frame.Type)
	}
	d, isType := frame.Data.(roomEventWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want roomEventWireDTO", frame.Data)
	}
	want := roomEventWireDTO{
		ActorID:     "player-1",
		ActorName:   "",
		Kind:        sim.LodgingReasonCheckout,
		Text:        "Your stay has ended — you head down to the common area.",
		Private:     true,
		StructureID: "inn",
		At:          at.UTC().Format(time.RFC3339),
	}
	if d != want {
		t.Errorf("room_event payload = %+v, want %+v", d, want)
	}
	// The client's _on_room_event matches private events by actor_id and reads
	// text/structure_id (world.gd / talk_panel.gd) — assert the exact keys + that
	// private marshals true (a missing/false private would drop the narration).
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"actor_id"`, `"actor_name"`, `"kind"`, `"text"`, `"private":true`, `"structure_id"`, `"at"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("room_event json missing %s: %s", key, b)
		}
	}
}

// TestTranslateEvent_PCRelocatedToCommonEmptyTextDropped covers the empty-text
// guard: a relocation with no narration line is dropped, never sent as a blank
// room_event the client would discard anyway.
func TestTranslateEvent_PCRelocatedToCommonEmptyTextDropped(t *testing.T) {
	_, ok := TranslateEvent(&sim.PCRelocatedToCommon{
		ActorID:     "player-1",
		StructureID: "inn",
		Reason:      sim.LodgingReasonCheckout,
		Text:        "",
		At:          time.Now().UTC(),
	})
	if ok {
		t.Error("PCRelocatedToCommon with empty Text should be dropped, not translated")
	}
}

// TestTranslateEvent_ActorArrivalNarrated (ZBBS-WORK-422): a peer arrival renders
// as a NON-private, structure-scoped room_event so the talk panel surfaces it to
// co-present PCs as a narration line.
func TestTranslateEvent_ActorArrivalNarrated(t *testing.T) {
	at := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	frame, ok := TranslateEvent(&sim.ActorArrivalNarrated{
		ActorID:     "ezekiel",
		ActorName:   "Ezekiel Cheever",
		StructureID: "tavern",
		Text:        "Ezekiel Cheever arrives at the Tavern.",
		At:          at,
	})
	if !ok {
		t.Fatal("ActorArrivalNarrated should translate")
	}
	if frame.Type != "room_event" {
		t.Fatalf("type = %q, want room_event", frame.Type)
	}
	d, isType := frame.Data.(roomEventWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want roomEventWireDTO", frame.Data)
	}
	want := roomEventWireDTO{
		ActorID:     "ezekiel",
		ActorName:   "Ezekiel Cheever",
		Kind:        "peer_arrival",
		Text:        "Ezekiel Cheever arrives at the Tavern.",
		Private:     false,
		StructureID: "tavern",
		At:          at.UTC().Format(time.RFC3339),
	}
	if d != want {
		t.Errorf("room_event payload = %+v, want %+v", d, want)
	}
	// private MUST marshal false: a true would route this through the client's
	// actor_id-scoped private path and never reach co-present peers.
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"actor_name":"Ezekiel Cheever"`, `"kind":"peer_arrival"`, `"private":false`, `"structure_id":"tavern"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("room_event json missing %s: %s", key, b)
		}
	}
}

// TestTranslateEvent_UnmappedDropped covers the default case: per-tile
// ActorMoved is engine-internal and must not reach the client.
func TestTranslateEvent_UnmappedDropped(t *testing.T) {
	if _, ok := TranslateEvent(&sim.ActorMoved{ActorID: "hannah"}); ok {
		t.Error("ActorMoved should be dropped (engine-internal), not translated")
	}
}

// TestTranslateEvent_SceneQuoteCreated_NoFrame: a posted quote produces NO
// client wire frame (ZBBS-HOME-470). A PC learns the offer from the seller's
// own spoken price and the Pay modal's /pc/quotes read; the old buyer-facing
// npc_spoke (ZBBS-HOME-408) duplicated the seller's speech and was removed.
func TestTranslateEvent_SceneQuoteCreated_NoFrame(t *testing.T) {
	if _, ok := TranslateEvent(&sim.SceneQuoteCreated{
		QuoteID:  7,
		SceneID:  "sc1",
		SellerID: "josiah",
		HuddleID: "h1",
		ItemKind: "Bread",
		Qty:      1,
		Amount:   1,
		At:       time.Now().UTC(),
	}); ok {
		t.Error("SceneQuoteCreated should produce no wire frame (ZBBS-HOME-470)")
	}
}

// TestTranslateEvent_SpokeWithMentions — ZBBS-WORK-400: commit-time-filtered
// mentions ride the npc_spoke frame on the same fields the scene_quote frame
// uses. A price of 0 means "no price named": the item appears in mentions
// but gets no mention_prices row.
func TestTranslateEvent_SpokeWithMentions(t *testing.T) {
	frame, ok := TranslateEvent(&sim.Spoke{
		SpeakerID:    "john",
		HuddleID:     "huddle-1",
		RecipientIDs: []sim.ActorID{"pc"},
		Text:         "Stew tonight, three coins. Bread as well.",
		At:           time.Date(2026, 6, 11, 18, 0, 0, 0, time.UTC),
		Mentions: []sim.SpeakMention{
			{Item: "stew", Price: 3},
			{Item: "bread", Price: 0},
		},
	})
	if !ok {
		t.Fatal("Spoke should translate")
	}
	d, isType := frame.Data.(spokeWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want spokeWireDTO", frame.Data)
	}
	if !reflect.DeepEqual(d.Mentions, []string{"stew", "bread"}) {
		t.Errorf("mentions = %+v, want [stew bread]", d.Mentions)
	}
	if !reflect.DeepEqual(d.MentionPrices, map[string]int{"stew": 3}) {
		t.Errorf("mention_prices = %+v, want map[stew:3] (price-0 bread omitted)", d.MentionPrices)
	}
}

// TestTranslateEvent_SpokeNoMentions — a mention-less Spoke keeps both wire
// fields nil so omitempty drops them from the frame (the pre-WORK-400 shape).
func TestTranslateEvent_SpokeNoMentions(t *testing.T) {
	frame, ok := TranslateEvent(&sim.Spoke{SpeakerID: "hannah", Text: "Good evening."})
	if !ok {
		t.Fatal("Spoke should translate")
	}
	d := frame.Data.(spokeWireDTO)
	if d.Mentions != nil || d.MentionPrices != nil {
		t.Errorf("mentions/mention_prices = %+v/%+v, want nil/nil", d.Mentions, d.MentionPrices)
	}
}
