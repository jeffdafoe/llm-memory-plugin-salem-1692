package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// continuation_render_test.go — LLM-468. RenderedPrompt.ContinuationEphemeralText
// is the body sent on rounds after the first of a tick: the full ephemeral minus
// the static "## Who you are" soul prose, which the per-turn ephemeral protocol
// would otherwise re-ship in full on every round of every tick.

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

func TestRender_ContinuationDropsSoulProseKeepsName(t *testing.T) {
	out := Render(Build(narrativeSnapshot(testSoulProse), "moses", nil), DefaultRenderConfig())

	if !strings.Contains(out.EphemeralText, testSoulProse) {
		t.Fatalf("round-0 ephemeral must carry the soul prose, got:\n%s", out.EphemeralText)
	}
	if strings.Contains(out.ContinuationEphemeralText, testSoulProse) {
		t.Errorf("continuation ephemeral must NOT carry the soul prose, got:\n%s", out.ContinuationEphemeralText)
	}
	// The self-name line stays: it is what lets the model tell whether overheard
	// second-person speech is addressed to it (LLM-432), and it costs ~30 bytes.
	if !strings.Contains(out.ContinuationEphemeralText, "You are Moses James.") {
		t.Errorf("continuation ephemeral must keep the self-name line, got:\n%s", out.ContinuationEphemeralText)
	}
	if !strings.Contains(out.ContinuationEphemeralText, "## Who you are") {
		t.Errorf("continuation ephemeral must keep the section header holding the name, got:\n%s", out.ContinuationEphemeralText)
	}
	if len(out.ContinuationEphemeralText) >= len(out.EphemeralText) {
		t.Errorf("continuation ephemeral (%d bytes) must be shorter than the full body (%d bytes)",
			len(out.ContinuationEphemeralText), len(out.EphemeralText))
	}
}

func TestRender_ContinuationKeepsEverySectionButTheSoul(t *testing.T) {
	// The affordances are what drive the productive continuations (955/day
	// measured), so trimming must remove the soul prose and nothing else. Proven
	// structurally: deleting exactly the prose from the full body reproduces the
	// continuation body byte-for-byte.
	out := Render(Build(narrativeSnapshot(testSoulProse), "moses", nil), DefaultRenderConfig())
	want := strings.Replace(out.EphemeralText, testSoulProse+"\n", "", 1)
	if out.ContinuationEphemeralText != want {
		t.Errorf("continuation body must differ from the full body ONLY by the soul prose\n got:\n%s\nwant:\n%s",
			out.ContinuationEphemeralText, want)
	}
}

func TestRender_ContinuationIdenticalWhenNoSoul(t *testing.T) {
	// A shared VA whose soul has not been synthesized yet renders a name-only
	// section, and there is nothing to trim — the two bodies must be the same
	// string, not merely equivalent.
	out := Render(Build(narrativeSnapshot(""), "moses", nil), DefaultRenderConfig())
	if out.ContinuationEphemeralText != out.EphemeralText {
		t.Errorf("with no soul prose the two bodies must be identical\nfull:\n%s\ncontinuation:\n%s",
			out.EphemeralText, out.ContinuationEphemeralText)
	}
}

func TestRender_ContinuationIdenticalForStatefulActor(t *testing.T) {
	// Stateful NPCs and PCs get no engine-side narrative at all (their identity
	// lives in their own VA's system prompt), so the trim is a no-op for them.
	snap := narrativeSnapshot(testSoulProse)
	snap.Actors["moses"].Kind = sim.KindNPCStateful
	out := Render(Build(snap, "moses", nil), DefaultRenderConfig())
	if strings.Contains(out.EphemeralText, "## Who you are") {
		t.Fatalf("a stateful actor must not render the shared-VA identity section")
	}
	if out.ContinuationEphemeralText != out.EphemeralText {
		t.Errorf("with no identity section the two bodies must be identical")
	}
}

func TestWithoutRange(t *testing.T) {
	// The bounds guard is load-bearing: a future edit that writes to the ephemeral
	// builder between renderNarrativeState and the slice must degrade to "send the
	// whole body" (costs tokens) rather than corrupt the prompt (costs meaning).
	const s = "abcdefghij"
	cases := []struct {
		name       string
		start, end int
		want       string
	}{
		{"middle removed", 3, 6, "abcghij"},
		{"empty range", 4, 4, s},
		{"inverted range", 6, 3, s},
		{"zero start", 0, 5, s},
		{"end past bounds", 3, 99, s},
		{"negative start", -2, 5, s},
	}
	for _, tc := range cases {
		if got := withoutRange(s, tc.start, tc.end); got != tc.want {
			t.Errorf("%s: withoutRange(%q, %d, %d) = %q, want %q", tc.name, s, tc.start, tc.end, got, tc.want)
		}
	}
}
