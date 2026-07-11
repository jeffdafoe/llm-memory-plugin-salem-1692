package sim

import (
	"testing"
	"time"
)

// huddle_close_evict_test.go — LLM-360. When a live-in keeper beds down and its
// shop closes, a cross-threshold huddle formed while the shop was open (an NPC at
// the loiter pin conversing with whoever is inside) must be torn down for the
// members standing OUTSIDE — otherwise the stranded loiterer keeps perceiving and
// addressing the inside occupant through the shut wall (the live case: an NPC
// talking to a lodging PC through a closed-for-the-night tavern's door). Members
// physically inside are left alone — two actors inside a closed building still
// converse, the boundary LLM-359 draws.

// seatHuddle wires an already-formed structure huddle directly into the world,
// bypassing JoinHuddle's scene/warrant machinery so the bed-down teardown is
// exercised in isolation. Every id is stamped into the huddle roster, the
// actorsByHuddle index, and its own CurrentHuddleID back-ref — the invariant
// JoinHuddle maintains.
func seatHuddle(w *World, huddleID HuddleID, structureID StructureID, now time.Time, memberIDs ...ActorID) {
	if w.Huddles == nil {
		w.Huddles = map[HuddleID]*Huddle{}
	}
	if w.actorsByHuddle == nil {
		w.actorsByHuddle = map[HuddleID]map[ActorID]struct{}{}
	}
	if w.Scenes == nil {
		w.Scenes = map[SceneID]*Scene{}
	}
	members := map[ActorID]struct{}{}
	index := map[ActorID]struct{}{}
	for _, id := range memberIDs {
		members[id] = struct{}{}
		index[id] = struct{}{}
		w.Actors[id].CurrentHuddleID = huddleID
	}
	w.Huddles[huddleID] = &Huddle{
		ID:             huddleID,
		StructureID:    structureID,
		Members:        members,
		StartedAt:      now,
		LastActivityAt: now,
		LastProgressAt: now,
	}
	w.actorsByHuddle[huddleID] = index
}

// TestExecuteNPCSleep_KeeperBedDown_EvictsCrossThresholdLoiterer is the headline
// LLM-360 fix, built on the reported scene: a tavern open through the evening, a
// PC inside (a lodger who spent the night), and an NPC at the loiter pin
// conversing across the threshold — one huddle. When the keeper beds down the shop
// shuts, and the outside NPC must be dropped while the inside PC keeps it.
func TestExecuteNPCSleep_KeeperBedDown_EvictsCrossThresholdLoiterer(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC) // small hours — the keeper's off-shift bed-down
	keeper := liveInKeeper("john")
	pc := &Actor{ID: "pc", Kind: KindPC, InsideStructureID: "tavern"}
	npc := &Actor{ID: "patience", Kind: KindNPCShared} // InsideStructureID "" — at the loiter pin, outside
	w := keeperTavernWorld(false, keeper, pc, npc)
	seatHuddle(w, "hud-1", "tavern", now, "john", "pc", "patience")

	if !executeNPCSleep(w, keeper, now) {
		t.Fatal("executeNPCSleep should bed the off-shift keeper")
	}

	if npc.CurrentHuddleID != "" {
		t.Errorf("outside NPC still huddled (%q) — the shut wall must drop the cross-threshold loiterer", npc.CurrentHuddleID)
	}
	if pc.CurrentHuddleID != "hud-1" {
		t.Errorf("inside PC dropped from the huddle (%q, want hud-1) — two actors inside a closed building still converse", pc.CurrentHuddleID)
	}
	if keeper.CurrentHuddleID != "" {
		t.Errorf("keeper still huddled (%q) — it bedded down and left the conversation", keeper.CurrentHuddleID)
	}
	if keeper.State != StateSleeping {
		t.Errorf("keeper State = %v, want StateSleeping", keeper.State)
	}
}

// TestExecuteNPCSleep_CoKeeperPresent_KeepsCrossThresholdHuddle guards the
// open/closed gate: when one of two keepers beds down but the other stays awake at
// post, the shop is still OPEN, so the cross-threshold loiterer is NOT evicted —
// its conversation across the threshold is legitimate while a keeper is present.
func TestExecuteNPCSleep_CoKeeperPresent_KeepsCrossThresholdHuddle(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	john := liveInKeeper("john")
	mary := liveInKeeper("mary") // second keeper of the same tavern, stays awake
	npc := &Actor{ID: "patience", Kind: KindNPCShared} // at the loiter pin, outside
	w := keeperTavernWorld(false, john, mary, npc)
	seatHuddle(w, "hud-1", "tavern", now, "john", "mary", "patience")

	if !executeNPCSleep(w, john, now) {
		t.Fatal("executeNPCSleep should bed john")
	}

	if npc.CurrentHuddleID != "hud-1" {
		t.Errorf("outside NPC evicted (%q) while a co-keeper is still on post — the shop is open, the threshold carries", npc.CurrentHuddleID)
	}
	if john.CurrentHuddleID != "" {
		t.Errorf("john still huddled (%q) — it bedded down and left the conversation", john.CurrentHuddleID)
	}
}
