package sim

import (
	"testing"
	"time"
)

// observed_state_test.go — LLM-80. Direct unit tests for the unified
// observed-state store: per-condition round-trip + decay, the future-stamp
// guard, self-clear semantics (Clear / ForgetStructure), and deep Clone. The
// behavior-parity tests for the two facts that feed it live alongside their
// capture (closed_business_test.go / out_of_stock_test.go) and surface
// (perception/closed_business_test.go) code.

func TestObservedStates_RoundTripPerCondition(t *testing.T) {
	now := time.Now()
	var o ObservedStates
	closed := ObservedStateKey{StructureID: "farm", Condition: ObservedClosed}
	dry := ObservedStateKey{StructureID: "tavern", ItemKind: "stew", Condition: ObservedOutOfStock}

	o.Observe(closed, now)
	o.Observe(dry, now)

	if o.Len() != 2 {
		t.Fatalf("want 2 observations, got %d", o.Len())
	}
	if !o.Active(closed, now) {
		t.Error("a just-stamped closed observation should be active")
	}
	if !o.Active(dry, now) {
		t.Error("a just-stamped out-of-stock observation should be active")
	}
	// A whole-structure (closed) key and a per-item (dry) key are distinct
	// entries even at the same structure — the condition is part of the key.
	if o.Active(ObservedStateKey{StructureID: "tavern", Condition: ObservedClosed}, now) {
		t.Error("a closed key must not match an out-of-stock entry at the same structure")
	}
}

func TestObservedStates_DecayByCondition(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		key  ObservedStateKey
		ttl  time.Duration
	}{
		{"closed", ObservedStateKey{StructureID: "farm", Condition: ObservedClosed}, ClosedBusinessMemoryTTL},
		{"dry", ObservedStateKey{StructureID: "tavern", ItemKind: "stew", Condition: ObservedOutOfStock}, OutOfStockMemoryTTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o ObservedStates
			o.Observe(tc.key, now.Add(-tc.ttl+time.Minute)) // just inside the window
			if !o.Active(tc.key, now) {
				t.Errorf("an observation just inside its TTL (%v) should still be active", tc.ttl)
			}
			o.Observe(tc.key, now.Add(-tc.ttl-time.Minute)) // just past the window
			if o.Active(tc.key, now) {
				t.Errorf("an observation older than its TTL (%v) should have decayed", tc.ttl)
			}
		})
	}
}

func TestObservedStates_FutureStampGuard(t *testing.T) {
	now := time.Now()
	var o ObservedStates
	key := ObservedStateKey{StructureID: "farm", Condition: ObservedClosed}
	o.Observe(key, now.Add(time.Hour)) // clock skew / bad test setup
	if o.Active(key, now) {
		t.Error("a future-stamped observation must not read as active (the age >= 0 guard)")
	}
}

func TestObservedStates_ClearAndForgetStructure(t *testing.T) {
	now := time.Now()
	var o ObservedStates
	closedInn := ObservedStateKey{StructureID: "inn", Condition: ObservedClosed}
	dryInnMeat := ObservedStateKey{StructureID: "inn", ItemKind: "meat", Condition: ObservedOutOfStock}
	dryInnMilk := ObservedStateKey{StructureID: "inn", ItemKind: "milk", Condition: ObservedOutOfStock}
	closedGazebo := ObservedStateKey{StructureID: "gazebo", Condition: ObservedClosed}
	for _, k := range []ObservedStateKey{closedInn, dryInnMeat, dryInnMilk, closedGazebo} {
		o.Observe(k, now)
	}

	// Clear drops exactly one observation (the per-condition self-clear path).
	o.Clear(dryInnMeat)
	if _, ok := o.At(dryInnMeat); ok {
		t.Error("Clear should drop exactly the one observation")
	}
	if _, ok := o.At(dryInnMilk); !ok {
		t.Error("Clear must not touch a sibling (inn, milk) entry")
	}

	// ForgetStructure drops every condition + item for that structure, leaving
	// other structures intact — the move_to destination-scoped clear.
	o.ForgetStructure("inn")
	if _, ok := o.At(closedInn); ok {
		t.Error("ForgetStructure(inn) should drop the closed-inn observation")
	}
	if _, ok := o.At(dryInnMilk); ok {
		t.Error("ForgetStructure(inn) should drop the remaining out-of-stock inn observation")
	}
	if _, ok := o.At(closedGazebo); !ok {
		t.Error("ForgetStructure(inn) must leave other structures (gazebo) intact")
	}
}

func TestObservedStates_PeerKeyedDistinctFromPlace(t *testing.T) {
	// LLM-228: a person-keyed condition (ObservedHelpedByWorker, keyed by PeerID)
	// shares the store with the place-keyed conditions. The PeerID is part of the
	// key, so per-worker memories are distinct and never collide with a
	// place-keyed entry.
	now := time.Now()
	var o ObservedStates
	helpedAnne := ObservedStateKey{PeerID: "anne", Condition: ObservedHelpedByWorker}
	helpedLewis := ObservedStateKey{PeerID: "lewis", Condition: ObservedHelpedByWorker}
	closedInn := ObservedStateKey{StructureID: "inn", Condition: ObservedClosed}
	o.Observe(helpedAnne, now)
	o.Observe(closedInn, now)

	if !o.Active(helpedAnne, now) {
		t.Error("a just-stamped helped-by-worker memory should be active")
	}
	if o.Active(helpedLewis, now) {
		t.Error("a memory of Anne must not match a query about Lewis — PeerID is part of the key")
	}
	// Decays on its own TTL.
	o.Observe(helpedAnne, now.Add(-HelpedByWorkerMemoryTTL-time.Minute))
	if o.Active(helpedAnne, now) {
		t.Error("a helped-by-worker memory older than its TTL should have decayed")
	}
}

func TestObservedStates_ForgetStructureLeavesPeerMemory(t *testing.T) {
	// LLM-228: ForgetStructure is the move_to destination-scoped place clear. A
	// person-keyed memory carries an empty StructureID; the empty-arg guard keeps a
	// stray ForgetStructure("") from wiping it, and forgetting a real place never
	// touches it either.
	now := time.Now()
	var o ObservedStates
	helpedAnne := ObservedStateKey{PeerID: "anne", Condition: ObservedHelpedByWorker}
	closedInn := ObservedStateKey{StructureID: "inn", Condition: ObservedClosed}
	o.Observe(helpedAnne, now)
	o.Observe(closedInn, now)

	o.ForgetStructure("") // must be a no-op, not a wipe of the empty-structure peer key
	if _, ok := o.At(helpedAnne); !ok {
		t.Error("ForgetStructure(\"\") must not wipe a person-keyed memory")
	}
	o.ForgetStructure("inn")
	if _, ok := o.At(closedInn); ok {
		t.Error("ForgetStructure(inn) should drop the place memory")
	}
	if _, ok := o.At(helpedAnne); !ok {
		t.Error("forgetting a place must not touch a person-keyed memory")
	}
}

func TestObservedStates_CloneIsDeep(t *testing.T) {
	now := time.Now()
	var src ObservedStates
	key := ObservedStateKey{StructureID: "farm", Condition: ObservedClosed}
	src.Observe(key, now)

	clone := src.Clone()

	// Mutating the source after cloning must not bleed into the clone.
	src.Clear(key)
	if _, ok := clone.At(key); !ok {
		t.Error("clearing the source must not affect a prior Clone (deep copy)")
	}
	// And mutating the clone must not bleed back into the source.
	clone.Observe(ObservedStateKey{StructureID: "tavern", Condition: ObservedClosed}, now)
	if src.Len() != 0 {
		t.Error("observing on the clone must not affect the source")
	}

	// Clone of an empty store is empty and independent.
	var empty ObservedStates
	ec := empty.Clone()
	if ec.Len() != 0 {
		t.Error("clone of an empty store should be empty")
	}
	ec.Observe(key, now)
	if empty.Len() != 0 {
		t.Error("observing on an empty store's clone must not affect the original")
	}
}
