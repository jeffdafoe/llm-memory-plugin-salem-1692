package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// continuation_render_test.go — LLM-501 (supersedes the LLM-468 continuation
// trim these tests used to pin). RenderedPrompt.StableText carries the
// "## Who you are" identity section — the daily-stable soul the adapter routes
// to the provider-cached system zone — and the volatile EphemeralText must not
// carry it at all.

func narrativeSnapshot(aboutMe string) *sim.Snapshot {
	subj := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Moses James",
		InsideStructureID: "farm",
		Coins:             40,
		Needs:             map[sim.NeedKey]int{"hunger": 3},
	}
	if aboutMe != "" {
		subj.Narrative = &sim.NarrativeState{AboutMe: aboutMe}
	}
	return &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"moses": subj},
		Structures: map[sim.StructureID]*sim.Structure{"farm": {ID: "farm", DisplayName: "Ellis Farm"}},
	}
}

const testSoulProse = "I have farmed this ground since my father's day, and I do not hurry it."

func TestRender_IdentityRendersToStableStreamOnly(t *testing.T) {
	out := Render(Build(narrativeSnapshot(testSoulProse), "moses", nil), DefaultRenderConfig())

	if !strings.Contains(out.StableText, testSoulProse) {
		t.Fatalf("StableText must carry the soul prose, got:\n%s", out.StableText)
	}
	if !strings.Contains(out.StableText, "## Who you are") {
		t.Errorf("StableText must carry the identity header, got:\n%s", out.StableText)
	}
	// The self-name line rides the stable stream too — it is what lets the
	// model tell whether overheard second-person speech is addressed to it
	// (LLM-432), delivered every call via the cached system zone.
	if !strings.Contains(out.StableText, "You are Moses James.") {
		t.Errorf("StableText must carry the self-name line, got:\n%s", out.StableText)
	}
	// The volatile streams must NOT duplicate any of it — a byte of identity
	// in the ephemeral body is a byte re-billed cold every call.
	if strings.Contains(out.EphemeralText, "## Who you are") || strings.Contains(out.EphemeralText, testSoulProse) {
		t.Errorf("EphemeralText must not carry the identity section, got:\n%s", out.EphemeralText)
	}
	if strings.Contains(out.Text, testSoulProse) {
		t.Errorf("durable Text must not carry the soul prose, got:\n%s", out.Text)
	}
}

func TestRender_StableStreamNameOnlyWhenNoSoul(t *testing.T) {
	// A shared VA whose soul has not been synthesized yet still gets the
	// name-line section (the LLM-432 addressee gate), just without prose.
	out := Render(Build(narrativeSnapshot(""), "moses", nil), DefaultRenderConfig())
	if !strings.Contains(out.StableText, "You are Moses James.") {
		t.Errorf("StableText must carry the name line for a soul-less shared villager, got:\n%s", out.StableText)
	}
}

func TestRender_StableStreamEmptyForStatefulActor(t *testing.T) {
	// Stateful NPCs and PCs get no engine-side narrative at all (their identity
	// lives in their own VA's system prompt), so the stable stream is empty —
	// the adapter's stable_context field is omitted entirely.
	snap := narrativeSnapshot(testSoulProse)
	snap.Actors["moses"].Kind = sim.KindNPCStateful
	out := Render(Build(snap, "moses", nil), DefaultRenderConfig())
	if strings.Contains(out.EphemeralText, "## Who you are") {
		t.Fatalf("a stateful actor must not render the shared-VA identity section")
	}
	if out.StableText != "" {
		t.Errorf("a stateful actor's StableText must be empty, got:\n%s", out.StableText)
	}
}
