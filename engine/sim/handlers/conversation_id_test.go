package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestConversationIDFromPayload pins the conversation_id source (ZBBS-HOME-397):
// the primary narrative-beat scene id when the tick resolved a scene, and empty
// when it didn't (a solo tick with no active huddle) so that row stays ungrouped
// in the admin chat viewer rather than collapsing into a bogus conversation.
func TestConversationIDFromPayload(t *testing.T) {
	withScene := perception.Payload{Primary: &perception.SceneView{SceneID: sim.SceneID("sc-abc123")}}
	if got := conversationIDFromPayload(withScene); got != "sc-abc123" {
		t.Errorf("with primary scene: got %q, want sc-abc123", got)
	}

	noScene := perception.Payload{Primary: nil}
	if got := conversationIDFromPayload(noScene); got != "" {
		t.Errorf("no primary scene: got %q, want empty", got)
	}
}

// TestConversationIDForChat pins the ZBBS-HOME-417 re-key: the chat grouping key
// is the actor's huddle id (the actual conversation unit, which rotates per
// exchange once the silence sweep concludes it) when the actor is in a huddle,
// and falls back to the durable scene id only when the actor is huddle-less
// (a solo tick), preserving HOME-397's behaviour for that case.
func TestConversationIDForChat(t *testing.T) {
	payload := perception.Payload{Primary: &perception.SceneView{SceneID: sim.SceneID("sc-tavern-durable")}}

	// In a huddle: key off the huddle, NOT the durable structure scene (the
	// whole point — the scene is reused across conversations, the huddle is not).
	inHuddle := &sim.ActorSnapshot{CurrentHuddleID: sim.HuddleID("hud-xyz")}
	if got := conversationIDForChat(inHuddle, payload); got != "hud-xyz" {
		t.Errorf("in huddle: got %q, want hud-xyz (huddle id, not scene)", got)
	}

	// Huddle-less: fall back to the scene id so the row still groups like before.
	huddleless := &sim.ActorSnapshot{CurrentHuddleID: ""}
	if got := conversationIDForChat(huddleless, payload); got != "sc-tavern-durable" {
		t.Errorf("huddle-less: got %q, want sc-tavern-durable (scene fallback)", got)
	}

	// Huddle-less with no scene either: empty (ungrouped), like companion chat.
	if got := conversationIDForChat(huddleless, perception.Payload{Primary: nil}); got != "" {
		t.Errorf("huddle-less, no scene: got %q, want empty", got)
	}

	// Nil actor (defensive): fall back to the scene id.
	if got := conversationIDForChat(nil, payload); got != "sc-tavern-durable" {
		t.Errorf("nil actor: got %q, want sc-tavern-durable (scene fallback)", got)
	}
}
