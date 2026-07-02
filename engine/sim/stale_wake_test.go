package sim

import (
	"testing"
	"time"
)

// stale_wake_test.go — LLM-233 unit coverage: the backoff curve, the
// situation fingerprint's change/no-change surface, the ledger advance, and
// the defer decision. The EvaluateReactors gate itself is covered end-to-end
// in stale_wake_gate_test.go (package sim_test).

func TestStaleWakeBackoff(t *testing.T) {
	s := WorldSettings{StaleWakeDecayBase: time.Minute}
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, time.Minute},
		{1, 2 * time.Minute},
		{2, 4 * time.Minute},
		{4, 16 * time.Minute},
		{5, 30 * time.Minute},  // 32m capped to the 30m default
		{50, 30 * time.Minute}, // deep streak stays capped (no overflow)
	}
	for _, c := range cases {
		if got := staleWakeBackoff(s, c.streak); got != c.want {
			t.Errorf("backoff(streak=%d) = %v, want %v", c.streak, got, c.want)
		}
	}

	if got := staleWakeBackoff(WorldSettings{}, 3); got != 0 {
		t.Errorf("disabled base: backoff = %v, want 0", got)
	}

	custom := WorldSettings{StaleWakeDecayBase: time.Minute, StaleWakeDecayCap: 5 * time.Minute}
	if got := staleWakeBackoff(custom, 10); got != 5*time.Minute {
		t.Errorf("custom cap: backoff = %v, want 5m", got)
	}
}

// fingerprintWorld builds a minimal world + actor sharing a huddle with a
// peer, for exercising what the fingerprint does and does not react to.
func fingerprintWorld() (*World, *Actor) {
	a := &Actor{
		ID:                "john",
		InsideStructureID: "tavern",
		Pos:               TilePos{X: 103, Y: 130},
		State:             StateIdle,
		Coins:             77,
		Inventory:         map[ItemKind]int{"cheese": 9, "carrots": 1},
		CurrentHuddleID:   "hud-1",
		Needs:             map[NeedKey]int{"hunger": 5},
	}
	w := &World{
		Huddles: map[HuddleID]*Huddle{
			"hud-1": {
				Members: map[ActorID]struct{}{"john": {}, "patience": {}},
				RecentUtterances: []Utterance{
					{SpeakerID: "patience", At: time.Unix(1000, 0).UTC()},
				},
			},
		},
	}
	return w, a
}

func TestActorSituationFingerprint_ChangesOnRealDevelopments(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(w *World, a *Actor)
	}{
		{"coins", func(_ *World, a *Actor) { a.Coins++ }},
		{"inventory qty", func(_ *World, a *Actor) { a.Inventory["carrots"] = 5 }},
		{"inventory new kind", func(_ *World, a *Actor) { a.Inventory["stew"] = 1 }},
		{"position", func(_ *World, a *Actor) { a.Pos.X++ }},
		{"structure", func(_ *World, a *Actor) { a.InsideStructureID = "inn" }},
		{"state", func(_ *World, a *Actor) { a.State = StateLaboring }},
		{"huddle id", func(_ *World, a *Actor) { a.CurrentHuddleID = "" }},
		{"huddle member joins", func(w *World, _ *Actor) {
			w.Huddles["hud-1"].Members["josiah"] = struct{}{}
		}},
		{"peer speaks", func(w *World, _ *Actor) {
			h := w.Huddles["hud-1"]
			h.RecentUtterances = append(h.RecentUtterances,
				Utterance{SpeakerID: "patience", At: time.Unix(2000, 0).UTC()})
		}},
		{"pc speaks", func(w *World, _ *Actor) {
			w.Huddles["hud-1"].LastPCUtteranceAt = time.Unix(3000, 0).UTC()
		}},
	}
	for _, m := range mutations {
		w, a := fingerprintWorld()
		before := actorSituationFingerprint(w, a)
		m.mutate(w, a)
		if after := actorSituationFingerprint(w, a); after == before {
			t.Errorf("%s: fingerprint unchanged, want change", m.name)
		}
	}
}

func TestActorSituationFingerprint_IgnoresSelfSpeechAndNeeds(t *testing.T) {
	w, a := fingerprintWorld()
	before := actorSituationFingerprint(w, a)

	// The actor's own re-pitch must not read as change — it would reset the
	// decay the actor's own looping caused.
	h := w.Huddles["hud-1"]
	h.RecentUtterances = append(h.RecentUtterances,
		Utterance{SpeakerID: "john", At: time.Unix(5000, 0).UTC()})
	if got := actorSituationFingerprint(w, a); got != before {
		t.Error("own utterance changed the fingerprint, want unchanged")
	}

	// Needs drift every minute by design; they are deliberately excluded
	// (red thresholds have their own salient warrants).
	a.Needs["hunger"] = 20
	if got := actorSituationFingerprint(w, a); got != before {
		t.Error("needs change altered the fingerprint, want unchanged")
	}
}

func TestActorSituationFingerprint_StableAcrossMapOrder(t *testing.T) {
	w, a := fingerprintWorld()
	first := actorSituationFingerprint(w, a)
	for i := 0; i < 20; i++ {
		if got := actorSituationFingerprint(w, a); got != first {
			t.Fatal("fingerprint not deterministic across recomputation")
		}
	}
}

func TestRecordStaleWake(t *testing.T) {
	a := &Actor{ID: "a1"}
	restock := WarrantMeta{Reason: RestockWarrantReason{Item: "carrots", Source: RestockSourceBuy}}
	salient := WarrantMeta{Reason: BasicWarrantReason{K: WarrantKindHuddleJoined}}
	t0 := time.Unix(9000, 0).UTC()

	recordStaleWake(a, []WarrantMeta{restock}, 111, t0)
	e := a.StaleWake[WarrantKindRestock]
	if e == nil || e.Streak != 1 || e.Fingerprint != 111 || !e.LastEmitAt.Equal(t0) {
		t.Fatalf("first record: %+v, want streak 1 fp 111", e)
	}

	t1 := t0.Add(time.Minute)
	recordStaleWake(a, []WarrantMeta{restock}, 111, t1)
	if e.Streak != 2 || !e.LastEmitAt.Equal(t1) {
		t.Errorf("same-fp record: streak=%d lastAt=%v, want 2 / %v", e.Streak, e.LastEmitAt, t1)
	}

	recordStaleWake(a, []WarrantMeta{restock}, 222, t1.Add(time.Minute))
	if e2 := a.StaleWake[WarrantKindRestock]; e2.Streak != 1 || e2.Fingerprint != 222 {
		t.Errorf("changed-fp record: %+v, want fresh streak 1 fp 222", e2)
	}

	recordStaleWake(a, []WarrantMeta{salient}, 222, t1)
	if _, ok := a.StaleWake[WarrantKindHuddleJoined]; ok {
		t.Error("salient kind recorded in the ledger, want ambient-only")
	}
}

func TestStaleWakeDeferUntil(t *testing.T) {
	s := WorldSettings{StaleWakeDecayBase: time.Minute}
	restock := WarrantMeta{Reason: RestockWarrantReason{Item: "carrots", Source: RestockSourceBuy}}
	duty := WarrantMeta{Reason: BasicWarrantReason{K: WarrantKindShiftDuty}}
	t0 := time.Unix(9000, 0).UTC()

	// No ledger entry: fresh kind, never deferred.
	a := &Actor{ID: "a1", Warrants: []WarrantMeta{restock}}
	if _, stale := staleWakeDeferUntil(s, a, 111, t0); stale {
		t.Error("no-entry cycle deferred, want full rate")
	}

	// Entry under the same fingerprint, inside the backoff: deferred to
	// lastEmit + 2·base (streak 1).
	a.StaleWake = map[WarrantKind]*StaleWakeEntry{
		WarrantKindRestock: {Fingerprint: 111, Streak: 1, LastEmitAt: t0},
	}
	next, stale := staleWakeDeferUntil(s, a, 111, t0.Add(30*time.Second))
	if !stale {
		t.Fatal("same-fp cycle inside backoff not deferred")
	}
	if want := t0.Add(2 * time.Minute); !next.Equal(want) {
		t.Errorf("defer until %v, want %v", next, want)
	}

	// Backoff elapsed: allowed.
	if _, stale := staleWakeDeferUntil(s, a, 111, t0.Add(2*time.Minute)); stale {
		t.Error("elapsed backoff still deferred, want allowed")
	}

	// Fingerprint changed: allowed immediately.
	if _, stale := staleWakeDeferUntil(s, a, 999, t0.Add(time.Second)); stale {
		t.Error("changed-fp cycle deferred, want full rate")
	}

	// Mixed cycle where one kind has no entry: the fresh kind lets the whole
	// cycle through (the day's first shift_duty must not be absorbed by a
	// decayed restock).
	a.Warrants = []WarrantMeta{restock, duty}
	if _, stale := staleWakeDeferUntil(s, a, 111, t0.Add(time.Second)); stale {
		t.Error("cycle with a fresh kind deferred, want full rate")
	}

	// Both kinds stale: deferred to the LATEST allowed time.
	a.StaleWake[WarrantKindShiftDuty] = &StaleWakeEntry{Fingerprint: 111, Streak: 2, LastEmitAt: t0}
	next, stale = staleWakeDeferUntil(s, a, 111, t0.Add(time.Second))
	if !stale {
		t.Fatal("all-stale multi-kind cycle not deferred")
	}
	if want := t0.Add(4 * time.Minute); !next.Equal(want) {
		t.Errorf("multi-kind defer until %v, want %v (max across kinds)", next, want)
	}

	// Empty cycle: defensive, never deferred.
	a.Warrants = nil
	if _, stale := staleWakeDeferUntil(s, a, 111, t0); stale {
		t.Error("empty cycle deferred")
	}
}
