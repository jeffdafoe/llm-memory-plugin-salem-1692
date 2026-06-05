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
