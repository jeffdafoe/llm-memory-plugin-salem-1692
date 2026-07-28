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
// map, clone helper, and surface reader. Most conditions are place-keyed, but
// the key also carries an optional PeerID, so a person-scoped belief — an
// employer's memory of a worker who helped them (ObservedHelpedByWorker,
// LLM-228) — rides the same store and lifecycle.
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
	// ObservedNoHiring — arrived at a business whose keeper was present but on
	// break (StateResting), so it could not be solicited for work (LLM-210).
	// Whole-structure (empty ItemKind), keyed by the business the seek-work
	// directory names. Distinct from ObservedClosed (keeperLESS — the keeper is
	// asleep or has wandered off) and ObservedDeclinedWork (an explicit refusal):
	// a resting keeper is "open" for lodging/consumption but cannot take on a
	// worker, so this drops the business from the seek-work directory ONLY, on a
	// shorter TTL since a break ends soon.
	ObservedNoHiring
	// ObservedSaleStandoff — offered for an item at a shop and the negotiation
	// dead-ended (LLM-525). Per-item like ObservedOutOfStock: the key carries the
	// ItemKind alongside the seller's WORKPLACE. Distinct from ObservedOutOfStock
	// (the shelves were empty — a fact about stock) — a standoff is about the DEAL
	// not closing, and stamps only once the buyer has been turned down
	// SaleStandoffDeclineThreshold times in one conversation. Perception drops that
	// (structure, item) from the buy directory for the TTL, so the buyer stops
	// walking back to a counter that just told her no.
	ObservedSaleStandoff
	// ObservedSoldToPeer — sold an item to a specific person a short while ago
	// (LLM-555). PERSON-keyed like ObservedHelpedByWorker, and additionally
	// per-item: the key carries the buyer's PeerID and the ItemKind, with
	// StructureID empty. Lives on the SELLER's store and is DIRECTIONAL — it
	// answers "did I sell this to them", so it suppresses only the reverse leg
	// (buying that same good back off that same person) and never a repeat sale
	// or a buy from anyone else. Perception drops that partner from the buy
	// directory for the TTL and the reverse-pay role-gate refuses the offer, so
	// two actors stop churning one good back and forth across successive
	// conversations.
	ObservedSoldToPeer
	// ObservedHelpedByWorker — an employer's memory that a specific worker
	// COMPLETED A PAID job for them (LLM-228). PERSON-keyed (the only such
	// condition): the key carries the worker's PeerID with StructureID/ItemKind
	// empty, and the memory lives on the EMPLOYER's store — the mirror of
	// ObservedDeclinedWork, which lives on the worker keyed by the employer's
	// workplace. Stamped at labor settle (helped_by_worker.go); perception
	// surfaces it at the "## Work offers awaiting your decision" section when that
	// worker solicits the employer again, so the re-hire choice recalls the past
	// help rather than being pitched a hire-value argument (LLM-224 / #690-#691).
	ObservedHelpedByWorker
)

// ttl is how long an observation of this condition stays actionable before
// perception ignores it (the read-time decay applied by Active). Each arm reads
// the named const kept next to that fact's capture code, so a TTL stays
// documented where the fact is stamped (closed_business.go / out_of_stock.go /
// declined_work.go / no_hiring.go / sale_standoff.go / helped_by_worker.go). An
// unknown condition
// returns 0 → Active false (safe default).
func (c ObservedCondition) ttl() time.Duration {
	switch c {
	case ObservedClosed:
		return ClosedBusinessMemoryTTL
	case ObservedOutOfStock:
		return OutOfStockMemoryTTL
	case ObservedDeclinedWork:
		return DeclinedWorkMemoryTTL
	case ObservedNoHiring:
		return NoHiringMemoryTTL
	case ObservedSaleStandoff:
		return SaleStandoffMemoryTTL
	case ObservedSoldToPeer:
		return SoldToPeerMemoryTTL
	case ObservedHelpedByWorker:
		return HelpedByWorkerMemoryTTL
	}
	return 0
}

// ObservedStateKey identifies one observation: the structure it is about, the
// condition observed, and — for per-item conditions like ObservedOutOfStock —
// the item (empty for whole-structure conditions like ObservedClosed). The
// structure is the buy-menu / move_to handle (a vendor's WORKPLACE), matching
// what the cue names and the actor walks to. Two conditions are instead about a
// PERSON: ObservedHelpedByWorker carries the worker's PeerID (structure/item
// empty) — an employer's memory of who helped them — and ObservedSoldToPeer
// carries the buyer's PeerID alongside the ItemKind (structure empty), a
// seller's memory of what they just sold that person. All fields are comparable,
// so the key is usable as a map key.
type ObservedStateKey struct {
	StructureID StructureID
	ItemKind    ItemKind
	// PeerID scopes a person-keyed condition (ObservedHelpedByWorker,
	// ObservedSoldToPeer) to the remembered actor. Empty for the place-keyed
	// conditions.
	PeerID    ActorID
	Condition ObservedCondition
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
	if structureID == "" {
		// Person-keyed conditions (ObservedHelpedByWorker) carry an empty
		// StructureID; a walk-commit only ever clears a real destination, so this
		// guard keeps a stray ForgetStructure("") from wiping those memories.
		return
	}
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
