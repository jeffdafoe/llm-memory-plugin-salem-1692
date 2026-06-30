package sim

import "time"

// observed_state.go — LLM-80 (epic LLM-76, Half B). One decaying, in-memory
// store for an NPC's experiential "I observed this place in this condition just
// now" memories. It folds together the two bespoke maps that grew independently
// for the same shape:
//
//   - "found it shut" — a business arrived at with no keeper present (HOME-353,
//     was Actor.ClosedBusinessObs, whole-structure).
//   - "found it dry"  — a (vendor, item) a buy failed on for stock (HOME-363,
//     was Actor.OutOfStockObs, per-item).
//
// Both share the same lifecycle: stamped with the observation time, decayed by a
// per-condition TTL, self-cleared when re-observed otherwise, and read by
// perception to deprioritize a cue. A new volatile observation ("picked clean",
// "price seen") is now a new ObservedCondition value + a TTL arm — not another
// map, clone helper, and surface reader.
//
// Restart-lossy by design: these are negative, quickly-re-observed beliefs, not
// durable knowledge (contrast Actor.KnownPlaces, the durable Half-A substrate,
// which IS persisted). Cloned into snapshots, never written to Postgres. The TTL
// decay is applied at READ time (Active) so a stale belief fades without the
// world goroutine sweeping the map.
//
// Capture stays with each fact's triggering event — closed-business on
// ActorArrived (closed_business.go), out-of-stock on PayWithItemResolved + the
// quote-payment fast path (out_of_stock.go) — because those events genuinely
// differ. Those subscribers now write through this one store instead of owning
// their own map. The surface half lives in perception (consumable_vendors.go).

// ObservedCondition enumerates the volatile place-conditions an NPC can remember
// observing. Adding a fact is a new value here plus a TTL arm in the ttl method.
type ObservedCondition uint8

const (
	// ObservedClosed — arrived at a business and found no keeper present
	// (HOME-353). Whole-structure: the key's ItemKind is empty.
	ObservedClosed ObservedCondition = iota
	// ObservedOutOfStock — tried to buy an item and the vendor was out of stock
	// (HOME-363). Per-item: the key carries the ItemKind alongside the structure.
	ObservedOutOfStock
	// ObservedDeclinedWork — solicited work from an employer and was declined
	// (LLM-198). Whole-structure (empty ItemKind), keyed by the employer's
	// WORKPLACE — the business named in the seek-work directory. Perception drops
	// that business from the worker's directory for the TTL so it stops walking
	// back to a door that just turned it away.
	ObservedDeclinedWork
)

// ttl is how long an observation of this condition stays actionable before
// perception ignores it (the read-time decay applied by Active). Both are 4
// game-hours today, carried as the existing named consts so each fact's TTL
// stays documented next to its capture code (closed_business.go / out_of_stock.go).
func (c ObservedCondition) ttl() time.Duration {
	switch c {
	case ObservedClosed:
		return ClosedBusinessMemoryTTL
	case ObservedOutOfStock:
		return OutOfStockMemoryTTL
	case ObservedDeclinedWork:
		return DeclinedWorkMemoryTTL
	}
	return 0
}

// ObservedStateKey identifies one observation: the structure it is about, the
// condition observed, and — for per-item conditions like ObservedOutOfStock —
// the item (empty for whole-structure conditions like ObservedClosed). The
// structure is the buy-menu / move_to handle (a vendor's WORKPLACE), matching
// what the cue names and the actor walks to. All fields are comparable, so the
// key is usable as a map key.
type ObservedStateKey struct {
	StructureID StructureID
	ItemKind    ItemKind
	Condition   ObservedCondition
}

// ObservedStates is an actor's decaying experiential memory of observed place
// conditions. The zero value is ready to use (a nil backing map until the first
// Observe). In-memory + restart-lossy by design.
type ObservedStates struct {
	// at maps each observation to the wall-clock time it was observed. nil until
	// the first Observe; unexported so all decay/TTL logic funnels through the
	// methods below rather than callers poking the map directly.
	at map[ObservedStateKey]time.Time
}

// NewObservedStates builds a store from a literal map of observations — for test
// fixtures and a-priori seeding. Copies the input, so the caller's map is not
// aliased. A nil/empty map yields an empty (nil-backed) store.
func NewObservedStates(entries map[ObservedStateKey]time.Time) ObservedStates {
	if len(entries) == 0 {
		return ObservedStates{}
	}
	at := make(map[ObservedStateKey]time.Time, len(entries))
	for k, v := range entries {
		at[k] = v
	}
	return ObservedStates{at: at}
}

// Observe records (or refreshes) an observation of key as of t, allocating the
// backing map on first use. Pointer receiver so the lazy allocation persists on
// the actor's field.
func (o *ObservedStates) Observe(key ObservedStateKey, t time.Time) {
	if o.at == nil {
		o.at = make(map[ObservedStateKey]time.Time)
	}
	o.at[key] = t
}

// Clear drops a single observation — the self-clear path (found it open again /
// bought it after all). nil-safe (delete on a nil map is a no-op).
func (o *ObservedStates) Clear(key ObservedStateKey) {
	delete(o.at, key)
}

// ForgetStructure drops every observation about structureID, across all
// conditions and items. This is the destination-scoped clear applied when an
// actor commits to walk somewhere (move_to): deciding to GO supersedes stale
// beliefs about that place. nil-safe; deleting keys mid-range is permitted by
// the Go spec.
func (o *ObservedStates) ForgetStructure(structureID StructureID) {
	for key := range o.at {
		if key.StructureID == structureID {
			delete(o.at, key)
		}
	}
}

// Active reports whether key holds an observation still within its condition's
// TTL as of now (the snapshot clock at read time). The age >= 0 guard rejects a
// future-stamped observation (clock skew / test setup) that would otherwise read
// as "fresh forever". False on an empty store or an absent/expired key.
func (o ObservedStates) Active(key ObservedStateKey, now time.Time) bool {
	if len(o.at) == 0 {
		return false
	}
	observedAt, ok := o.at[key]
	if !ok {
		return false
	}
	age := now.Sub(observedAt)
	return age >= 0 && age < key.Condition.ttl()
}

// At returns the raw observation time for key (and whether it is present),
// ignoring TTL. For tests and introspection; the live decay check is Active.
func (o ObservedStates) At(key ObservedStateKey) (time.Time, bool) {
	t, ok := o.at[key]
	return t, ok
}

// Len is the number of observations held across all conditions. For tests.
func (o ObservedStates) Len() int {
	return len(o.at)
}

// Clone returns a deep copy. ObservedStateKey and time.Time are both value types,
// so a per-entry copy is a full clone. The result is empty (nil-backed) when the
// source is empty, matching the snapshot-clone posture of the maps this replaced.
func (o ObservedStates) Clone() ObservedStates {
	if len(o.at) == 0 {
		return ObservedStates{}
	}
	dst := make(map[ObservedStateKey]time.Time, len(o.at))
	for k, v := range o.at {
		dst[k] = v
	}
	return ObservedStates{at: dst}
}
