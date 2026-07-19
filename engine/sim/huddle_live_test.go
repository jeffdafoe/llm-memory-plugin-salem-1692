package sim

import (
	"testing"
	"time"
)

// huddle_live_test.go — LLM-467. HuddleIsLive is the "is anyone still talking"
// predicate the noop-skip preflight uses to tell a live conversation apart from
// a room people simply haven't left. Its baselines deliberately mirror
// RunHuddleSilenceSweep's so the liveness read and the lifecycle sweep can never
// disagree about what an unstamped huddle means.

func TestHuddleIsLive_RecentActivityIsLive(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	h := &Huddle{StartedAt: now.Add(-90 * time.Minute), LastActivityAt: now.Add(-30 * time.Second)}
	if !HuddleIsLive(h, now, HuddleLiveWindowDefault) {
		t.Fatalf("a huddle spoken in 30s ago must read live")
	}
}

func TestHuddleIsLive_StaleActivityIsDormant(t *testing.T) {
	// The case the ticket exists for: the conversation ended long ago but the
	// huddle survives until the 2h silence sweep, so members keep standing there.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	h := &Huddle{StartedAt: now.Add(-90 * time.Minute), LastActivityAt: now.Add(-40 * time.Minute)}
	if HuddleIsLive(h, now, HuddleLiveWindowDefault) {
		t.Fatalf("a huddle silent for 40m must read dormant")
	}
}

func TestHuddleIsLive_WindowBoundaryIsInclusive(t *testing.T) {
	// Exactly at the window still counts as live — the same >=/<= posture the
	// need-threshold and silence-sweep comparisons take.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	h := &Huddle{LastActivityAt: now.Add(-HuddleLiveWindowDefault)}
	if !HuddleIsLive(h, now, HuddleLiveWindowDefault) {
		t.Fatalf("activity exactly at the window boundary must read live")
	}
	older := &Huddle{LastActivityAt: now.Add(-HuddleLiveWindowDefault - time.Second)}
	if HuddleIsLive(older, now, HuddleLiveWindowDefault) {
		t.Fatalf("activity one second past the window must read dormant")
	}
}

func TestHuddleIsLive_UnstampedActivityFallsBackToStartedAt(t *testing.T) {
	// Same fallback as the silence sweep: a creation site that forgets to stamp
	// LastActivityAt still gets a sane dormancy baseline rather than reading as
	// eternally live.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	fresh := &Huddle{StartedAt: now.Add(-time.Minute)}
	if !HuddleIsLive(fresh, now, HuddleLiveWindowDefault) {
		t.Fatalf("a just-started huddle with no activity stamp must read live")
	}
	old := &Huddle{StartedAt: now.Add(-time.Hour)}
	if HuddleIsLive(old, now, HuddleLiveWindowDefault) {
		t.Fatalf("an hour-old huddle with no activity stamp must read dormant")
	}
}

func TestHuddleIsLive_SafeDirectionsReadLive(t *testing.T) {
	// Both degenerate inputs resolve toward "live", which costs at most the
	// pre-LLM-467 tick. The opposite default would silently strand a real
	// conversation, which is the far more expensive mistake.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if !HuddleIsLive(&Huddle{}, now, HuddleLiveWindowDefault) {
		t.Errorf("a huddle with neither stamp (hand-built test snapshot) must read live")
	}
	if !HuddleIsLive(&Huddle{LastActivityAt: now.Add(-24 * time.Hour)}, now, 0) {
		t.Errorf("an unconfigured window must read live regardless of staleness")
	}
	if HuddleIsLive(nil, now, HuddleLiveWindowDefault) {
		t.Errorf("a nil huddle is not a live conversation")
	}
}

func TestEffectiveHuddleLiveWindow(t *testing.T) {
	if got := EffectiveHuddleLiveWindow(WorldSettings{}); got != HuddleLiveWindowDefault {
		t.Errorf("unset window = %v, want the default %v", got, HuddleLiveWindowDefault)
	}
	if got := EffectiveHuddleLiveWindow(WorldSettings{HuddleLiveWindow: 90 * time.Second}); got != 90*time.Second {
		t.Errorf("configured window = %v, want 90s", got)
	}
}
