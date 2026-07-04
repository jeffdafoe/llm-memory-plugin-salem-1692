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

// --- Weighted starvation-age fairness (LLM-258) ---

// servedActorOn builds an actor on a slug whose most recent SERVED tick is
// lastTick, so servedStarvationAge/lastReactorTickAt see a real wait.
func servedActorOn(slug string, lastTick time.Time) *Actor {
	a := &Actor{LLMAgent: slug}
	a.RecentReactorTicks = NewRingBuffer[time.Time](defaultRecentReactorTicksCap)
	a.RecentReactorTicks.Push(lastTick)
	return a
}

// neverServedActorOn builds an actor on a slug that has never ticked (nil ring) —
// the Moses/Joseph "0 ticks since boot" case.
func neverServedActorOn(slug string) *Actor { return &Actor{LLMAgent: slug} }

// vendorWorld returns a world with salem-vendor gated at Cap/60s and default
// fairness tunables (reserve 2, threshold 45s, ceiling 2m).
func vendorWorld(cap int) *World {
	w := &World{}
	w.SetAgentRateLimits(map[string]AgentRateLimit{"salem-vendor": {Cap: cap, Window: time.Minute}})
	return w
}

// fillAgentBucket records n in-window ticks on the slug (simulating other actors
// of the pool having ticked).
func fillAgentBucket(w *World, slug string, n int, now time.Time) {
	for i := 0; i < n; i++ {
		recordAgentTick(w, slug, now)
	}
}

func TestAdmitAgentRateFair_UngatedAlwaysAdmits(t *testing.T) {
	w := &World{} // no caps installed
	now := time.Now().UTC()
	if !admitAgentRateFair(w, servedActorOn("salem-vendor", now), now) {
		t.Error("an ungated slug has no pacing, so fairness must always admit")
	}
}

func TestAdmitAgentRateFair_GeneralBandAdmitsFreshChatter(t *testing.T) {
	// Below the general boundary (cap-reserve = 6): even an actor that ticked a
	// second ago passes, so live conversation keeps the bulk of the budget.
	w := vendorWorld(8)
	now := time.Now().UTC()
	fillAgentBucket(w, "salem-vendor", 5, now) // 5 < 6
	if !admitAgentRateFair(w, servedActorOn("salem-vendor", now.Add(-time.Second)), now) {
		t.Error("general band should admit even a just-ticked chatter")
	}
}

func TestAdmitAgentRateFair_ReservedBandGatesChatterAdmitsStarved(t *testing.T) {
	// In the reserved band [6, 8): a fresh chatter is gated so it can't consume
	// the tail, but a producer starved past the 45s threshold takes the slot.
	w := vendorWorld(8)
	now := time.Now().UTC()
	fillAgentBucket(w, "salem-vendor", 6, now) // in reserved band
	if admitAgentRateFair(w, servedActorOn("salem-vendor", now.Add(-time.Second)), now) {
		t.Error("reserved band must gate a fresh chatter")
	}
	if !admitAgentRateFair(w, servedActorOn("salem-vendor", now.Add(-90*time.Second)), now) {
		t.Error("reserved band must admit a producer starved past the threshold")
	}
}

func TestAdmitAgentRateFair_NeverServedTakesReservedSlot(t *testing.T) {
	// A never-served producer (0 ticks since boot) is maximally starved and takes
	// a reserved slot — the direct fix for the reported salem-vendor freeze.
	w := vendorWorld(8)
	now := time.Now().UTC()
	fillAgentBucket(w, "salem-vendor", 7, now) // reserved band, still < cap
	if !admitAgentRateFair(w, neverServedActorOn("salem-vendor"), now) {
		t.Error("never-served producer should claim a reserved slot")
	}
}

func TestAdmitAgentRateFair_AtCapOnlyServedPastCeilingBursts(t *testing.T) {
	// At/over the cap the only admit is a SERVED actor starved past the 2m ceiling
	// (the accepted one-tick overage). A never-served actor and a served-but-not-
	// yet-starved actor both hold, so the bucket isn't pushed past cap by them.
	w := vendorWorld(8)
	now := time.Now().UTC()
	fillAgentBucket(w, "salem-vendor", 8, now) // == cap
	if admitAgentRateFair(w, neverServedActorOn("salem-vendor"), now) {
		t.Error("never-served must NOT burst past the cap")
	}
	if admitAgentRateFair(w, servedActorOn("salem-vendor", now.Add(-60*time.Second)), now) {
		t.Error("served but below the ceiling must NOT burst past the cap")
	}
	if !admitAgentRateFair(w, servedActorOn("salem-vendor", now.Add(-3*time.Minute)), now) {
		t.Error("served past the ceiling gets the unconditional bypass")
	}
}

func TestAdmitAgentRateFair_ReserveClampLeavesOneGeneralSlot(t *testing.T) {
	// A reserve >= cap would leave zero general slots and starve conversation
	// entirely; it clamps to cap-1 so the first slot is always general.
	w := vendorWorld(1)
	w.Settings.AgentRateStarvationReserve = 5 // >= cap → clamp to 0 reserved, 1 general
	now := time.Now().UTC()
	if !admitAgentRateFair(w, servedActorOn("salem-vendor", now), now) {
		t.Error("clamp must keep >=1 general slot so a fresh actor still passes at count 0")
	}
}

func TestAdmitAgentRateFair_HonorsCustomThreshold(t *testing.T) {
	// The reserved-band threshold reads WorldSettings, not just the default: a 20s
	// wait clears a custom 10s threshold though it is under the 45s default.
	w := vendorWorld(8)
	w.Settings.AgentRateReserveAgeThreshold = 10 * time.Second
	now := time.Now().UTC()
	fillAgentBucket(w, "salem-vendor", 6, now)
	if !admitAgentRateFair(w, servedActorOn("salem-vendor", now.Add(-20*time.Second)), now) {
		t.Error("a custom 10s threshold should admit a 20s-starved actor in the reserved band")
	}
}
