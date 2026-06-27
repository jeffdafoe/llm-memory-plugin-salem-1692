package sim

// Internal unit tests for the per-shared-VA tick rate gate (LLM-156) — the
// pacing that keeps a shared agent (salem-vendor backs many NPCs) from bursting
// the pool's aggregate ticks past memory-api's per-agent rate limit and dropping
// the whole pool into a silent cooldown. These exercise the gate primitives
// directly; the emit-loop wiring is covered in reactor_test.go (sim_test).

import (
	"testing"
	"time"
)

func TestSetAgentRateLimits_FiltersUnusableEntries(t *testing.T) {
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{
		"salem-vendor": {Cap: 24, Window: time.Minute},
		"":             {Cap: 10, Window: time.Minute}, // empty slug
		"salem-zero":   {Cap: 0, Window: time.Minute},  // non-positive cap
		"salem-nowin":  {Cap: 5, Window: 0},            // non-positive window
	})
	if _, ok := w.agentRateCapFor("salem-vendor"); !ok {
		t.Error("salem-vendor should be gated")
	}
	for _, slug := range []string{"", "salem-zero", "salem-nowin", "salem-unknown"} {
		if _, ok := w.agentRateCapFor(slug); ok {
			t.Errorf("%q should be ungated", slug)
		}
	}
	// An empty map clears all pacing (fail-open).
	w.SetAgentRateLimits(nil)
	if _, ok := w.agentRateCapFor("salem-vendor"); ok {
		t.Error("nil map should clear pacing")
	}
}

func TestAgentRateGate_AllowsUpToCapThenBlocks(t *testing.T) {
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 3, Window: time.Minute}})
	now := time.Now().UTC()

	// An ungated slug always passes and never allocates a ring.
	if !checkAgentRateGate(w, "other", now) {
		t.Error("ungated slug should pass the gate")
	}
	recordAgentTick(w, "other", now)
	if w.agentRecentTicks["other"] != nil {
		t.Error("recordAgentTick must not allocate a ring for an ungated slug")
	}

	// The gate passes for the first Cap ticks, then blocks.
	for i := 0; i < 3; i++ {
		if !checkAgentRateGate(w, "salem-vendor", now) {
			t.Fatalf("tick %d should pass (under cap)", i)
		}
		recordAgentTick(w, "salem-vendor", now)
	}
	if checkAgentRateGate(w, "salem-vendor", now) {
		t.Error("gate should block once the cap is reached")
	}
}

func TestAgentRateGate_StaleTicksOutsideWindowDoNotCount(t *testing.T) {
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 2, Window: time.Minute}})
	now := time.Now().UTC()

	// Two ticks 90s ago — outside the 60s window, so the cap is clear.
	old := now.Add(-90 * time.Second)
	recordAgentTick(w, "salem-vendor", old)
	recordAgentTick(w, "salem-vendor", old.Add(time.Second))
	if !checkAgentRateGate(w, "salem-vendor", now) {
		t.Error("ticks outside the window must not count toward the cap")
	}
}

func TestAgentRateGate_AggregatesAcrossActorsOfOneSlug(t *testing.T) {
	// The whole point of LLM-156: the ring is per SLUG, not per actor, so ticks
	// from different actors sharing salem-vendor accumulate into one bucket.
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 2, Window: time.Minute}})
	now := time.Now().UTC()

	recordAgentTick(w, "salem-vendor", now) // as if actor A ticked
	recordAgentTick(w, "salem-vendor", now) // as if actor B ticked
	if checkAgentRateGate(w, "salem-vendor", now) {
		t.Error("two ticks from different actors of one slug should reach the cap")
	}
}

func TestNextAgentRateAllowedAt_AtOldestPlusWindow(t *testing.T) {
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 2, Window: time.Minute}})
	now := time.Now().UTC()

	// Two in-window ticks at -40s and -20s; cap 2 → next allowed when the oldest
	// (-40s) leaves the window: -40s + 60s = now+20s (plus a small forward jitter).
	recordAgentTick(w, "salem-vendor", now.Add(-40*time.Second))
	recordAgentTick(w, "salem-vendor", now.Add(-20*time.Second))
	got := nextAgentRateAllowedAt(w, "salem-vendor", now)
	if got.Before(now.Add(20 * time.Second)) {
		t.Errorf("next allowed %v should be at/after oldest+window (now+20s)", got.Sub(now))
	}
}

func TestNextAgentRateAllowedAt_UngatedReturnsNow(t *testing.T) {
	w := &World{}
	now := time.Now().UTC()
	if got := nextAgentRateAllowedAt(w, "salem-vendor", now); !got.Equal(now) {
		t.Errorf("ungated slug should return now, got %v", got.Sub(now))
	}
}

func TestAgentRateGate_ForcedBurstStaysBlocked(t *testing.T) {
	// Forced warrants bypass the gate but are still recorded (they count at the
	// server). A forced burst can overflow the ring past cap*2; the gate must
	// stay blocked — never undercount and reopen — and must not panic.
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 3, Window: time.Minute}})
	now := time.Now().UTC()
	for i := 0; i < 100; i++ {
		recordAgentTick(w, "salem-vendor", now)
	}
	if checkAgentRateGate(w, "salem-vendor", now) {
		t.Error("gate must stay blocked after a forced-tick burst, not reopen")
	}
	if got := nextAgentRateAllowedAt(w, "salem-vendor", now); !got.After(now) {
		t.Errorf("next-allowed should be in the future while saturated, got %v", got.Sub(now))
	}
}

func TestNextAgentRateAllowedAt_OverCapReturnsExpiryOfTickThatUncaps(t *testing.T) {
	// len(inWindow) > Cap: the tick that must expire to bring the count below
	// the cap is inWindow[len-Cap], not the oldest. Cap 2, three in-window ticks
	// at -50s/-40s/-30s → the -40s tick (index 1) must clear: -40s+60s = now+20s.
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 2, Window: time.Minute}})
	now := time.Now().UTC()
	recordAgentTick(w, "salem-vendor", now.Add(-50*time.Second))
	recordAgentTick(w, "salem-vendor", now.Add(-40*time.Second))
	recordAgentTick(w, "salem-vendor", now.Add(-30*time.Second))
	got := nextAgentRateAllowedAt(w, "salem-vendor", now)
	if got.Before(now.Add(20*time.Second)) || got.After(now.Add(21*time.Second)) {
		t.Errorf("over-cap next-allowed = %v, want ~now+20s (the -40s tick's expiry)", got.Sub(now))
	}
}

func TestAgentRateGate_TickAtExactWindowEdgeIsExclusive(t *testing.T) {
	// A tick exactly at now-window is treated as OUT (t.After(cutoff) is false),
	// matching the per-actor gate. memory-api's window edge is inclusive, so the
	// engine reopens at most one sub-millisecond tick earlier — covered by the
	// always-positive reopen jitter, so the engine can't beat the server.
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: 1, Window: time.Minute}})
	now := time.Now().UTC()
	recordAgentTick(w, "salem-vendor", now.Add(-time.Minute))
	if !checkAgentRateGate(w, "salem-vendor", now) {
		t.Error("a tick exactly at now-window should not count (edge is exclusive)")
	}
}
