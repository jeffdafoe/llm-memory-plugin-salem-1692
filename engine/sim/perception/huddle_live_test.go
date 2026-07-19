package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_live_test.go — LLM-467 build wiring. Surroundings.HuddleLive answers
// "is anyone still talking" where HuddleMembers answers "is anyone standing
// here". The noop-skip preflight reads it to stop billing a full LLM call per
// idle backstop for the up-to-2h a finished conversation's huddle survives.

func huddleLiveSnapshot(publishedAt, lastActivity time.Time, window time.Duration) *sim.Snapshot {
	return &sim.Snapshot{
		PublishedAt:      publishedAt,
		HuddleLiveWindow: window,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": {Kind: sim.KindNPCShared, InsideStructureID: "inn", CurrentHuddleID: "h1"},
			"john":     {DisplayName: "John Ellis"},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {
				ID:             "h1",
				StartedAt:      publishedAt.Add(-90 * time.Minute),
				LastActivityAt: lastActivity,
				Members:        map[sim.ActorID]struct{}{"prudence": {}, "john": {}},
			},
		},
	}
}

func TestBuild_HuddleLiveWhenRecentlySpoken(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	p := Build(huddleLiveSnapshot(now, now.Add(-20*time.Second), sim.HuddleLiveWindowDefault), "prudence", nil)
	if len(p.Surroundings.HuddleMembers) != 1 {
		t.Fatalf("HuddleMembers = %d, want 1", len(p.Surroundings.HuddleMembers))
	}
	if !p.Surroundings.HuddleLive {
		t.Fatalf("a huddle spoken in 20s ago must build HuddleLive=true")
	}
}

func TestBuild_HuddleDormantWhenSilent(t *testing.T) {
	// The peer is still standing there — HuddleMembers is unchanged — but the
	// conversation is over. The two signals must come apart here; that split IS
	// the fix.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	p := Build(huddleLiveSnapshot(now, now.Add(-45*time.Minute), sim.HuddleLiveWindowDefault), "prudence", nil)
	if len(p.Surroundings.HuddleMembers) != 1 {
		t.Fatalf("a dormant conversation must not drop the peer from HuddleMembers, got %d", len(p.Surroundings.HuddleMembers))
	}
	if p.Surroundings.HuddleLive {
		t.Fatalf("a huddle silent for 45m must build HuddleLive=false")
	}
}

func TestBuild_HuddleLiveUnsetWindowKeepsLegacyBehavior(t *testing.T) {
	// A directly-constructed test snapshot omits HuddleLiveWindow, which reads 0
	// — the same convention PCPresenceStaleAfter and SeekWorkCoinCeiling use, so
	// the many fixtures that predate this field keep their pre-LLM-467 behavior
	// instead of silently starting to skip.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	p := Build(huddleLiveSnapshot(now, now.Add(-3*time.Hour), 0), "prudence", nil)
	if !p.Surroundings.HuddleLive {
		t.Fatalf("an unset HuddleLiveWindow must read live regardless of staleness")
	}
}

func TestBuild_HuddleLiveFalseWhenUnhuddled(t *testing.T) {
	snap := &sim.Snapshot{
		PublishedAt:      time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		HuddleLiveWindow: sim.HuddleLiveWindowDefault,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": {Kind: sim.KindNPCShared, InsideStructureID: "inn", ColocatedAudienceIDs: []sim.ActorID{"john"}},
			"john":     {DisplayName: "John Ellis"},
		},
	}
	p := Build(snap, "prudence", nil)
	if p.Surroundings.HuddleLive {
		t.Fatalf("an unhuddled actor has no conversation to be live")
	}
}
