package sim

import (
	"reflect"
	"testing"
	"time"
)

// known_place_test.go — LLM-77 durable world-memory capture. White-box (package
// sim) so it drives the recorder, capture subscribers, and ownership seed
// directly, mirroring out_of_stock_test.go.

func kpWorld() *World {
	return &World{Actors: make(map[ActorID]*Actor)}
}

func TestKnownPlace_RecordIdempotentUnionsAffordances(t *testing.T) {
	a := &Actor{ID: "prudence", Kind: KindNPCStateful}
	t0 := time.Now().Add(-time.Hour)
	t1 := t0.Add(30 * time.Minute)

	recordKnownPlace(a, "bush1", PlaceKindObject, "gather:raspberries", t0)
	recordKnownPlace(a, "bush1", PlaceKindObject, "gather:raspberries", t1) // same affordance again
	recordKnownPlace(a, "bush1", PlaceKindObject, "free_source:hunger", t1) // a second affordance

	if len(a.KnownPlaces) != 1 {
		t.Fatalf("expected 1 known place (idempotent), got %d", len(a.KnownPlaces))
	}
	kp := a.KnownPlaces["bush1"]
	if kp == nil {
		t.Fatal("bush1 not recorded")
	}
	wantAff := []string{"free_source:hunger", "gather:raspberries"} // sorted, de-duped
	if !reflect.DeepEqual(kp.Affordances, wantAff) {
		t.Fatalf("affordances = %v, want %v", kp.Affordances, wantAff)
	}
	if !kp.FirstLearnedAt.Equal(t0) {
		t.Fatalf("FirstLearnedAt = %v, want first-record time %v", kp.FirstLearnedAt, t0)
	}
	if !kp.LastExperiencedAt.Equal(t1) {
		t.Fatalf("LastExperiencedAt = %v, want latest experience %v", kp.LastExperiencedAt, t1)
	}
}

func TestKnownPlace_RecordReconcilesKindMismatch(t *testing.T) {
	a := &Actor{ID: "prudence", Kind: KindNPCStateful}
	now := time.Now()
	recordKnownPlace(a, "shared_id", PlaceKindObject, "gather:raspberries", now)
	// A later authoritative capture sees the same ref as a structure: the kind
	// reconciles to the latest observation rather than staying stuck on the
	// first, and affordances union across both.
	recordKnownPlace(a, "shared_id", PlaceKindStructure, "vendor:bread", now)
	kp := a.KnownPlaces["shared_id"]
	if kp.Kind != PlaceKindStructure {
		t.Fatalf("kind = %q, want structure (reconciled to latest)", kp.Kind)
	}
	if len(kp.Affordances) != 2 {
		t.Fatalf("affordances should union across both captures, got %v", kp.Affordances)
	}
}

func TestKnownPlace_GatherSubscriberRecordsObject(t *testing.T) {
	w := kpWorld()
	now := time.Now()
	a := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = a

	handleKnownPlaceOnGather(w, &ItemGathered{ActorID: "prudence", ObjectID: "bush1", Item: "raspberries", Qty: 3, At: now})

	kp := a.KnownPlaces["bush1"]
	if kp == nil || kp.Kind != PlaceKindObject {
		t.Fatalf("expected object known-place bush1, got %+v", kp)
	}
	if len(kp.Affordances) != 1 || kp.Affordances[0] != "gather:raspberries" {
		t.Fatalf("affordances = %v, want [gather:raspberries]", kp.Affordances)
	}
}

func TestKnownPlace_GatherSubscriberSkipsPC(t *testing.T) {
	w := kpWorld()
	a := &Actor{ID: "player", Kind: KindPC}
	w.Actors["player"] = a
	handleKnownPlaceOnGather(w, &ItemGathered{ActorID: "player", ObjectID: "bush1", Item: "raspberries", At: time.Now()})
	if len(a.KnownPlaces) != 0 {
		t.Fatalf("PC gather should record nothing, got %v", a.KnownPlaces)
	}
}

func TestKnownPlace_PurchaseSubscriberRecordsVendorWorkplace(t *testing.T) {
	w := kpWorld()
	now := time.Now()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = buyer
	w.Actors["baker"] = &Actor{ID: "baker", Kind: KindNPCStateful, WorkStructureID: "bakery"}

	handleKnownPlaceOnPurchase(w, &PayWithItemResolved{BuyerID: "prudence", SellerID: "baker", ItemKind: "bread", TerminalState: PayTerminalStateAccepted, At: now})

	kp := buyer.KnownPlaces["bakery"]
	if kp == nil || kp.Kind != PlaceKindStructure {
		t.Fatalf("expected structure known-place bakery, got %+v", kp)
	}
	if len(kp.Affordances) != 1 || kp.Affordances[0] != "vendor:bread" {
		t.Fatalf("affordances = %v, want [vendor:bread]", kp.Affordances)
	}
}

func TestKnownPlace_PurchaseSubscriberSkipsNonAccepted(t *testing.T) {
	w := kpWorld()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = buyer
	w.Actors["baker"] = &Actor{ID: "baker", Kind: KindNPCStateful, WorkStructureID: "bakery"}
	handleKnownPlaceOnPurchase(w, &PayWithItemResolved{BuyerID: "prudence", SellerID: "baker", ItemKind: "bread", TerminalState: PayTerminalStateFailedInsufficientStock, At: time.Now()})
	if len(buyer.KnownPlaces) != 0 {
		t.Fatalf("a non-accepted purchase should record nothing, got %v", buyer.KnownPlaces)
	}
}

func TestKnownPlace_PurchaseSubscriberSkipsCoPresentPeer(t *testing.T) {
	w := kpWorld()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = buyer
	w.Actors["peer"] = &Actor{ID: "peer", Kind: KindNPCStateful} // no workplace
	handleKnownPlaceOnPurchase(w, &PayWithItemResolved{BuyerID: "prudence", SellerID: "peer", ItemKind: "bread", TerminalState: PayTerminalStateAccepted, At: time.Now()})
	if len(buyer.KnownPlaces) != 0 {
		t.Fatalf("a no-workplace seller has no place to remember, got %v", buyer.KnownPlaces)
	}
}

func TestKnownPlace_FreeSourceRecordsPerNeed(t *testing.T) {
	a := &Actor{ID: "prudence", Kind: KindNPCStateful}
	now := time.Now()
	hits := []RefreshHit{
		{ObjectID: "well", Attribute: "thirst", Amount: 8, NewValue: 24},
	}
	recordFreeSourceExperience(a, "well", hits, now)
	kp := a.KnownPlaces["well"]
	if kp == nil || kp.Kind != PlaceKindObject {
		t.Fatalf("expected object known-place well, got %+v", kp)
	}
	if len(kp.Affordances) != 1 || kp.Affordances[0] != "free_source:thirst" {
		t.Fatalf("affordances = %v, want [free_source:thirst]", kp.Affordances)
	}
}

func TestKnownPlace_FreeSourceSkipsEmptyHitsAndPC(t *testing.T) {
	a := &Actor{ID: "prudence", Kind: KindNPCStateful}
	recordFreeSourceExperience(a, "well", nil, time.Now())
	if len(a.KnownPlaces) != 0 {
		t.Fatalf("no hits should record nothing, got %v", a.KnownPlaces)
	}
	pc := &Actor{ID: "player", Kind: KindPC}
	recordFreeSourceExperience(pc, "well", []RefreshHit{{ObjectID: "well", Attribute: "thirst"}}, time.Now())
	if len(pc.KnownPlaces) != 0 {
		t.Fatalf("PC should record nothing, got %v", pc.KnownPlaces)
	}
}

func TestSeedOwnedKnownPlaces_OwnedBushAndAnchors(t *testing.T) {
	now := time.Now()
	prudence := &Actor{ID: "prudence", Kind: KindNPCStateful, HomeStructureID: "ward_house", WorkStructureID: "herbalist"}
	actors := map[ActorID]*Actor{"prudence": prudence}
	objects := map[VillageObjectID]*VillageObject{
		"bush1": {OwnerActorID: "prudence", Refreshes: []*ObjectRefresh{{GatherItem: "raspberries"}}},
		"wild":  {OwnerActorID: "", Refreshes: []*ObjectRefresh{{GatherItem: "blueberries"}}}, // unowned — commons, not seeded
	}

	SeedOwnedKnownPlaces(actors, objects, now)

	if kp := prudence.KnownPlaces["bush1"]; kp == nil || kp.Kind != PlaceKindObject || len(kp.Affordances) != 1 || kp.Affordances[0] != "gather:raspberries" {
		t.Fatalf("owned bush not seeded correctly: %+v", kp)
	}
	if kp := prudence.KnownPlaces["ward_house"]; kp == nil || kp.Kind != PlaceKindStructure || kp.Affordances[0] != "own_anchor:home" {
		t.Fatalf("home anchor not seeded: %+v", kp)
	}
	if kp := prudence.KnownPlaces["herbalist"]; kp == nil || kp.Kind != PlaceKindStructure || kp.Affordances[0] != "own_anchor:work" {
		t.Fatalf("work anchor not seeded: %+v", kp)
	}
	if _, ok := prudence.KnownPlaces["wild"]; ok {
		t.Fatal("an unowned object must not be seeded")
	}
}

func TestSeedOwnedKnownPlaces_MergesWithoutBumpingExperienced(t *testing.T) {
	loadTime := time.Now()
	experienced := loadTime.Add(-24 * time.Hour)
	// A place already loaded from pg (experienced yesterday) that the owner also owns.
	prudence := &Actor{
		ID: "prudence", Kind: KindNPCStateful,
		KnownPlaces: map[PlaceRef]*KnownPlace{
			"bush1": {Ref: "bush1", Kind: PlaceKindObject, Affordances: []string{"gather:raspberries"}, FirstLearnedAt: experienced, LastExperiencedAt: experienced},
		},
	}
	actors := map[ActorID]*Actor{"prudence": prudence}
	objects := map[VillageObjectID]*VillageObject{
		"bush1": {OwnerActorID: "prudence", Refreshes: []*ObjectRefresh{{GatherItem: "raspberries"}}},
	}

	SeedOwnedKnownPlaces(actors, objects, loadTime)

	kp := prudence.KnownPlaces["bush1"]
	if !kp.LastExperiencedAt.Equal(experienced) {
		t.Fatalf("seed must NOT bump LastExperiencedAt of a loaded row: got %v, want %v", kp.LastExperiencedAt, experienced)
	}
	if !kp.FirstLearnedAt.Equal(experienced) {
		t.Fatalf("seed must preserve FirstLearnedAt of a loaded row: got %v", kp.FirstLearnedAt)
	}
}

func TestSeedOwnedKnownPlaces_SkipsPCOwner(t *testing.T) {
	player := &Actor{ID: "player", Kind: KindPC, HomeStructureID: "ward_house"}
	actors := map[ActorID]*Actor{"player": player}
	objects := map[VillageObjectID]*VillageObject{
		"bush1": {OwnerActorID: "player", Refreshes: []*ObjectRefresh{{GatherItem: "raspberries"}}},
	}
	SeedOwnedKnownPlaces(actors, objects, time.Now())
	if len(player.KnownPlaces) != 0 {
		t.Fatalf("a PC owner must not be seeded, got %v", player.KnownPlaces)
	}
}

func TestCloneKnownPlaces_DeepCopy(t *testing.T) {
	src := map[PlaceRef]*KnownPlace{
		"bush1": {Ref: "bush1", Kind: PlaceKindObject, Affordances: []string{"gather:raspberries"}, FirstLearnedAt: time.Now(), LastExperiencedAt: time.Now()},
	}
	dst := cloneKnownPlaces(src)
	// Mutating the clone's affordance slice + struct must not touch the source.
	dst["bush1"].Affordances[0] = "MUTATED"
	dst["bush1"].Kind = PlaceKindStructure
	if src["bush1"].Affordances[0] != "gather:raspberries" {
		t.Fatalf("clone aliased the affordance slice: source mutated to %v", src["bush1"].Affordances)
	}
	if src["bush1"].Kind != PlaceKindObject {
		t.Fatalf("clone aliased the struct: source Kind mutated to %v", src["bush1"].Kind)
	}
	if cloneKnownPlaces(nil) != nil {
		t.Fatal("cloneKnownPlaces(nil) should be nil")
	}
}

// TestSnapshotActor_CarriesKnownPlaces locks the published-snapshot wiring: the
// snapshotActor builder must mirror KnownPlaces (deep-cloned) onto ActorSnapshot
// so the LLM-78/79 perception readers see the durable memory. Regression guard
// for the easy-to-miss third clone site (world.go).
func TestSnapshotActor_CarriesKnownPlaces(t *testing.T) {
	a := &Actor{ID: "prudence", Kind: KindNPCStateful}
	recordKnownPlace(a, "bush1", PlaceKindObject, "gather:raspberries", time.Now())

	snap := snapshotActor(a, 0)
	if snap.KnownPlaces == nil || snap.KnownPlaces["bush1"] == nil {
		t.Fatalf("published snapshot must carry KnownPlaces, got %v", snap.KnownPlaces)
	}
	// Deep copy: mutating the snapshot must not touch the live actor.
	snap.KnownPlaces["bush1"].Affordances[0] = "MUTATED"
	if a.KnownPlaces["bush1"].Affordances[0] != "gather:raspberries" {
		t.Fatal("snapshot aliased the live actor's known-place affordances")
	}
}

func TestPlaceKind_Valid(t *testing.T) {
	if !PlaceKindObject.Valid() || !PlaceKindStructure.Valid() {
		t.Fatal("object/structure should be valid")
	}
	if PlaceKind("bogus").Valid() {
		t.Fatal("bogus should be invalid")
	}
}
