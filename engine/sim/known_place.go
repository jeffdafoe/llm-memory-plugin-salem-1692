package sim

import (
	"sort"
	"time"
)

// known_place.go — LLM-77 (epic LLM-76, World-memory Half A — foundation).
//
// Durable, per-actor memory of WHICH places/sources an actor knows and WHAT
// each is good for. Unlike the experiential ClosedBusinessObs / OutOfStockObs
// (closed_business.go / out_of_stock.go) — in-memory, decaying NEGATIVE
// observations ("this was shut / dry just now") — a known place is PERMANENT
// POSITIVE knowledge: a location doesn't move and you don't un-know your own
// farm. It is checkpointed to actor_known_place at the same durability tier as
// actor_relationship.salient_facts.
//
// This file is the CAPTURE half plus the in-world recorder. It ships INERT: the
// snapshot carries the known-places set (actor.go), the repo persists it
// (repo/pg/actors.go), but NO renderer or move_to resolver reads it yet —
// navigation is LLM-78, perception cues are LLM-79. Rows accumulate; nothing
// consumes them.
//
// Capture is keyed on AFFORDANCE-BEARING EXPERIENCE, not bare arrival: a place
// becomes known when the actor exercises an affordance there (gathered from it,
// bought at it, ate/drank at it) or owns it (seeded at load). A bare arrival at
// an arbitrary tile carries no role to remember, so it is not recorded.

// PlaceRef is the move_to handle for a known place: a village_object id, or a
// structure id (structures share their id with their village_object placement —
// see closed_business.go). Stored as the uuid-as-text the rest of v2 uses.
type PlaceRef string

// PlaceKind discriminates what a known place physically is — a plain placed
// object (a bush, a well) or a structure (a shop, a home). Go owns this
// allowlist (the schema has no CHECK, per the v2 "schema is a dumb mirror"
// posture — a CHECK refusing engine output would wedge every checkpoint Tx);
// Load + Save validate it symmetrically in the repo.
type PlaceKind string

const (
	PlaceKindObject    PlaceKind = "object"
	PlaceKindStructure PlaceKind = "structure"
)

// Valid reports whether k is a recognized place kind.
func (k PlaceKind) Valid() bool {
	switch k {
	case PlaceKindObject, PlaceKindStructure:
		return true
	}
	return false
}

// KnownPlace is one durable entry in an actor's world-memory: a place the actor
// has experienced (or owns), and what it knows the place is good for. Permanent
// — no decay (decaying observed-state is Half B, LLM-80). Affordances are
// capability tokens of the form "<capability>:<detail>" — e.g.
// "gather:raspberries", "vendor:bread", "free_source:thirst", "own_anchor:home"
// — kept sorted + de-duplicated so a re-experience never grows the slice with a
// duplicate and the persisted JSON array round-trips stably.
type KnownPlace struct {
	Ref               PlaceRef
	Kind              PlaceKind
	Affordances       []string
	FirstLearnedAt    time.Time
	LastExperiencedAt time.Time
}

// addAffordance unions one capability token into the entry's affordance set,
// keeping it sorted + de-duplicated. No-op for the empty string or a token
// already present.
func (kp *KnownPlace) addAffordance(affordance string) {
	if affordance == "" {
		return
	}
	for _, a := range kp.Affordances {
		if a == affordance {
			return
		}
	}
	kp.Affordances = append(kp.Affordances, affordance)
	sort.Strings(kp.Affordances)
}

// recordKnownPlace upserts the actor's memory that it EXPERIENCED the place at
// ref (of kind) with the given affordance, stamped at `at`. Idempotent:
// re-experiencing an already-known place refreshes LastExperiencedAt and unions
// the affordance into the set WITHOUT duplicating the entry. Lazily allocates
// the map (nil until the actor's first known place). MUST run on the world
// goroutine (it mutates the live Actor).
func recordKnownPlace(a *Actor, ref PlaceRef, kind PlaceKind, affordance string, at time.Time) {
	if a == nil || ref == "" {
		return
	}
	if a.KnownPlaces == nil {
		a.KnownPlaces = make(map[PlaceRef]*KnownPlace)
	}
	kp := a.KnownPlaces[ref]
	if kp == nil {
		kp = &KnownPlace{Ref: ref, Kind: kind, FirstLearnedAt: at}
		a.KnownPlaces[ref] = kp
	} else if kp.Kind != kind {
		// A place_ref is deterministically one kind at each capture site; a
		// mismatch on an existing row (a stale or out-of-band entry) reconciles
		// to the latest authoritative observation rather than leaving the first
		// kind stuck forever.
		kp.Kind = kind
	}
	// Capture runs on the world goroutine; `at` is the event/effect wall-clock,
	// delivered in order, so LastExperiencedAt advances monotonically in practice.
	kp.LastExperiencedAt = at
	kp.addAffordance(affordance)
}

// seedKnownPlace adds an a-priori (ownership-derived) known place. Like
// recordKnownPlace, but it does NOT bump LastExperiencedAt for an already-known
// place — ownership at load is not a fresh "experience," and a row loaded from
// pg already carries the LastExperiencedAt of the last real visit. A brand-new
// entry is stamped at `at` (load time) for both timestamps.
func seedKnownPlace(a *Actor, ref PlaceRef, kind PlaceKind, affordance string, at time.Time) {
	if a == nil || ref == "" {
		return
	}
	if a.KnownPlaces == nil {
		a.KnownPlaces = make(map[PlaceRef]*KnownPlace)
	}
	kp := a.KnownPlaces[ref]
	if kp == nil {
		kp = &KnownPlace{Ref: ref, Kind: kind, FirstLearnedAt: at, LastExperiencedAt: at}
		a.KnownPlaces[ref] = kp
	} else if kp.Kind != kind {
		// Reconcile a stale kind on an already-loaded row (see recordKnownPlace).
		kp.Kind = kind
	}
	kp.addAffordance(affordance)
}

// handleKnownPlaceOnGather is the ItemGathered subscriber: harvesting from a
// source teaches the NPC that the source (a placed object) is a gatherable
// place for that item. No-op for non-agent gatherers (a PC gathering builds no
// experiential memory).
func handleKnownPlaceOnGather(w *World, evt Event) {
	g, ok := evt.(*ItemGathered)
	if !ok {
		return
	}
	a := w.Actors[g.ActorID]
	if a == nil || !isAgentNPC(a) {
		return
	}
	recordKnownPlace(a, PlaceRef(g.ObjectID), PlaceKindObject, "gather:"+string(g.Item), g.At)
}

// handleKnownPlaceOnPurchase is the PayWithItemResolved subscriber: a completed
// purchase teaches the buyer that the seller's workplace vends that item. Keyed
// by the seller's WORKPLACE structure (what the buy cue names and move_to walks
// to — matching the out-of-stock memory). No-op for a non-agent buyer, a non-
// accepted terminal, or a co-present peer seller with no workplace (no place to
// remember-and-return-to).
func handleKnownPlaceOnPurchase(w *World, evt Event) {
	res, ok := evt.(*PayWithItemResolved)
	if !ok || res.TerminalState != PayTerminalStateAccepted {
		return
	}
	buyer := w.Actors[res.BuyerID]
	if buyer == nil || !isAgentNPC(buyer) {
		return
	}
	seller := w.Actors[res.SellerID]
	if seller == nil || seller.WorkStructureID == "" {
		return
	}
	recordKnownPlace(buyer, PlaceRef(seller.WorkStructureID), PlaceKindStructure, "vendor:"+string(res.ItemKind), res.At)
}

// recordFreeSourceExperience is the consume-at-source capture: an NPC that ate
// or drank in place at a (free or its own) source learns it as a free_source
// for each need it satisfied. Called inline from applyObjectRefreshEffect —
// that path emits no event — for the hits it just applied. No-op for a non-
// agent actor or empty hits (a yield-only farm bush produces no hits, so it is
// never recorded as a free source here). MUST run on the world goroutine.
func recordFreeSourceExperience(a *Actor, objID VillageObjectID, hits []RefreshHit, at time.Time) {
	if a == nil || !isAgentNPC(a) || len(hits) == 0 {
		return
	}
	for _, h := range hits {
		recordKnownPlace(a, PlaceRef(objID), PlaceKindObject, "free_source:"+string(h.Attribute), at)
	}
}

// SeedOwnedKnownPlaces seeds the a-priori known places every NPC owner knows
// without having to walk to them first: their owned gatherable objects (their
// farm bushes) and their home/work structure anchors. Run at LoadWorld AFTER
// the persisted known-places set is loaded, so it MERGES onto (never clobbers)
// loaded rows. Ownership is re-derived from live world state each load, so an
// owner always "knows" their currently-owned sources — including on a fresh DB
// with no persisted rows yet (the specific thing that lets Prudence's farm be
// remembered rather than god-cued). NPC-only; PCs navigate via the client. MUST
// run on the world goroutine / before snapshot publish.
func SeedOwnedKnownPlaces(actors map[ActorID]*Actor, objects map[VillageObjectID]*VillageObject, at time.Time) {
	for id, obj := range objects {
		if obj == nil || obj.OwnerActorID == "" {
			continue
		}
		owner := actors[obj.OwnerActorID]
		if owner == nil || !isAgentNPC(owner) {
			continue
		}
		for _, r := range obj.Refreshes {
			if r.IsGatherable() {
				seedKnownPlace(owner, PlaceRef(id), PlaceKindObject, "gather:"+string(r.GatherItem), at)
			}
		}
	}
	for _, a := range actors {
		if a == nil || !isAgentNPC(a) {
			continue
		}
		if a.HomeStructureID != "" {
			seedKnownPlace(a, PlaceRef(a.HomeStructureID), PlaceKindStructure, "own_anchor:home", at)
		}
		if a.WorkStructureID != "" {
			seedKnownPlace(a, PlaceRef(a.WorkStructureID), PlaceKindStructure, "own_anchor:work", at)
		}
	}
}

// RegisterKnownPlaceSubscriber wires the known-place capture subscribers (gather
// + purchase). The consume-at-source capture is an inline call in
// object_refresh.go (that path emits no event). Call before World.Run or from
// inside a Command (world-goroutine-safe). Mirrors RegisterClosedBusinessSubscriber.
func RegisterKnownPlaceSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterKnownPlaceSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleKnownPlaceOnGather))
	w.Subscribe(SubscriberFunc(handleKnownPlaceOnPurchase))
}

// RememberedPlaces is an actor's durable known-places set split BY KIND into the
// structure ids and object ids the move_to name-resolver can fall back on
// (LLM-78). It is the memory-backed counterpart to perception.PerceivedPlaces:
// PerceivedPlaces is what a tick SHOWED the actor; RememberedPlaces is what the
// actor has personally experienced across its life (LLM-77). Both are threaded
// to the move_to name-resolver off the world goroutine — live (shown) sources
// win a shared name, memory is the fallback. Deterministically ordered so
// resolution is stable.
type RememberedPlaces struct {
	StructureIDs []StructureID
	ObjectIDs    []VillageObjectID
}

// CollectRememberedPlaces splits an actor's known-places set by Kind into the
// sorted, de-duplicated structure-id and object-id slices the move_to
// name-resolver consults as its memory-backed FALLBACK source (LLM-78). Pure
// over the map; the harness calls it once per tick (off the world goroutine,
// from the published ActorSnapshot) and threads the result to the move_to
// handler, mirroring perception.CollectPerceivedPlaces. nil slices when the
// actor knows no places of a kind. The map key already de-dups by ref and a ref
// is exactly one kind, so no id repeats. An empty ref or an unrecognized kind is
// dropped (defense-in-depth — the repo already validates both on load, LLM-77).
func CollectRememberedPlaces(known map[PlaceRef]*KnownPlace) RememberedPlaces {
	if len(known) == 0 {
		return RememberedPlaces{}
	}
	structures := make([]StructureID, 0, len(known))
	objects := make([]VillageObjectID, 0, len(known))
	for ref, kp := range known {
		if kp == nil || ref == "" {
			continue
		}
		switch kp.Kind {
		case PlaceKindStructure:
			structures = append(structures, StructureID(ref))
		case PlaceKindObject:
			objects = append(objects, VillageObjectID(ref))
		}
	}
	sort.Slice(structures, func(i, j int) bool { return structures[i] < structures[j] })
	sort.Slice(objects, func(i, j int) bool { return objects[i] < objects[j] })
	if len(structures) == 0 {
		structures = nil
	}
	if len(objects) == 0 {
		objects = nil
	}
	return RememberedPlaces{StructureIDs: structures, ObjectIDs: objects}
}
