package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestUmbilicalObjectsFromSnapshot covers the LLM-112 /objects snapshot→DTO map:
// full field mapping (position pixel+tile, loiter override, refresh policy,
// structure_backed, attached_to) and each filter (id / owner / tag / structure).
func TestUmbilicalObjectsFromSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	lit := now.Add(90 * time.Minute)
	snap := &sim.Snapshot{
		PublishedAt: now,
		// Only "inn" backs a Structure (shared UUID), so only it is structure_backed.
		Structures: map[sim.StructureID]*sim.Structure{
			"inn": {ID: "inn", DisplayName: "The Inn"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"inn": {
				ID: "inn", AssetID: "asset_tavern", CurrentState: "lit",
				Pos: sim.WorldPos{X: 320, Y: 256}, DisplayName: "The Inn",
				EntryPolicy: sim.EntryPolicyOpen,
			},
			"lamp": {
				ID: "lamp", AssetID: "asset_lamp", CurrentState: "on",
				Pos: sim.WorldPos{X: 336, Y: 256}, AttachedTo: "inn",
				OwnerActorID:  "hannah",
				Tags:          []string{"lamplighter-stop"},
				LoiterOffsetX: intPtr(1), LoiterOffsetY: intPtr(2),
			},
			"bush": {
				ID: "bush", AssetID: "asset_bush", CurrentState: "ripe",
				Pos: sim.WorldPos{X: 64, Y: 64}, OwnerActorID: "prudence",
				Tags: []string{"forage"},
				Refreshes: []*sim.ObjectRefresh{
					{Attribute: "hunger", Amount: -2, AvailableQuantity: intPtr(3), MaxQuantity: intPtr(3), GatherItem: "berries"},
				},
			},
			"well": {
				ID: "well", AssetID: "asset_well", CurrentState: "full",
				Pos: sim.WorldPos{X: 128, Y: 128}, Tags: []string{"water"},
			},
			// The store carries all three LLM-559 runtime counters at once: an
			// owned business (wear + rate_owed) that is also the hearth object.
			"store": {
				ID: "store", AssetID: "asset_store", CurrentState: "open",
				Pos: sim.WorldPos{X: 512, Y: 512}, OwnerActorID: "josiah",
				Tags:           []string{sim.TagBusiness, sim.TagHearth},
				Wear:           37,
				RateOwed:       2,
				HearthLitUntil: lit,
			},
		},
	}

	// No filter → all five, sorted by id.
	all := umbilicalObjectsFromSnapshot(snap, objectsFilter{})
	if all.Total != 5 || len(all.Objects) != 5 {
		t.Fatalf("total = %d, want 5", all.Total)
	}
	if !all.PublishedAt.Equal(now) {
		t.Errorf("published_at = %v, want %v", all.PublishedAt, now)
	}
	if all.Objects[0].ID != "bush" || all.Objects[1].ID != "inn" ||
		all.Objects[2].ID != "lamp" || all.Objects[3].ID != "store" ||
		all.Objects[4].ID != "well" {
		t.Fatalf("order = %s/%s/%s/%s/%s, want bush/inn/lamp/store/well",
			all.Objects[0].ID, all.Objects[1].ID, all.Objects[2].ID,
			all.Objects[3].ID, all.Objects[4].ID)
	}

	// Field mapping — the lamp: pixel + resolved tile, owner, attached_to,
	// loiter override, tags; NOT structure-backed.
	lamp := all.Objects[2]
	wantTile := (sim.WorldPos{X: 336, Y: 256}).Tile()
	if lamp.Position.X != 336 || lamp.Position.Y != 256 ||
		lamp.Position.TileX != wantTile.X || lamp.Position.TileY != wantTile.Y {
		t.Errorf("lamp position = %+v, want pixel 336,256 tile %d,%d", lamp.Position, wantTile.X, wantTile.Y)
	}
	if lamp.OwnerActorID != "hannah" || lamp.AttachedTo != "inn" || lamp.StructureBacked {
		t.Errorf("lamp meta = %+v, want owner hannah / attached inn / not structure-backed", lamp)
	}
	if lamp.LoiterOffset == nil || lamp.LoiterOffset.X == nil || *lamp.LoiterOffset.X != 1 ||
		lamp.LoiterOffset.Y == nil || *lamp.LoiterOffset.Y != 2 {
		t.Errorf("lamp loiter = %+v, want x1 y2", lamp.LoiterOffset)
	}
	if len(lamp.Tags) != 1 || lamp.Tags[0] != "lamplighter-stop" {
		t.Errorf("lamp tags = %v, want [lamplighter-stop]", lamp.Tags)
	}

	// The inn is structure-backed, no loiter override, carries name + entry policy.
	inn := all.Objects[1]
	if !inn.StructureBacked || inn.LoiterOffset != nil ||
		inn.DisplayName != "The Inn" || inn.EntryPolicy != "open" {
		t.Errorf("inn = %+v, want structure-backed / no loiter / The Inn / open", inn)
	}

	// The bush carries its refresh policy; the well has a non-nil tags slice and
	// no refresh_policy.
	bush := all.Objects[0]
	if len(bush.RefreshPolicy) != 1 || bush.RefreshPolicy[0].Attribute != "hunger" ||
		bush.RefreshPolicy[0].Amount != -2 {
		t.Fatalf("bush refresh_policy = %+v, want one hunger/-2 row", bush.RefreshPolicy)
	}
	well := all.Objects[4]
	if well.Tags == nil || len(well.Tags) != 1 || well.RefreshPolicy != nil {
		t.Errorf("well = %+v, want tags[water] + no refresh_policy", well)
	}

	// LLM-559 runtime counters — carried raw on the store, which holds all three.
	store := all.Objects[3]
	if store.Wear != 37 || store.RateOwed != 2 {
		t.Errorf("store counters = wear %d / rate_owed %d, want 37 / 2", store.Wear, store.RateOwed)
	}
	if store.HearthLitUntil == nil || !store.HearthLitUntil.Equal(lit) {
		t.Errorf("store hearth_lit_until = %v, want %v", store.HearthLitUntil, lit)
	}

	// An object with no counters leaves all three at their omitted zero — the
	// well is neither a business nor a hearth, so the roster stays quiet for it.
	if well.Wear != 0 || well.RateOwed != 0 || well.HearthLitUntil != nil {
		t.Errorf("well counters = %+v, want all zero/nil", well)
	}

	// Filters (each AND-combined, empty = wildcard).
	if got := umbilicalObjectsFromSnapshot(snap, objectsFilter{id: "bush"}); got.Total != 1 || got.Objects[0].ID != "bush" {
		t.Errorf("id filter = %+v, want only bush", got.Objects)
	}
	if got := umbilicalObjectsFromSnapshot(snap, objectsFilter{owner: "prudence"}); got.Total != 1 || got.Objects[0].ID != "bush" {
		t.Errorf("owner filter = %+v, want only bush", got.Objects)
	}
	if got := umbilicalObjectsFromSnapshot(snap, objectsFilter{tag: "water"}); got.Total != 1 || got.Objects[0].ID != "well" {
		t.Errorf("tag filter = %+v, want only well", got.Objects)
	}
	// structure filter → the backing object (inn) + its overlay (lamp).
	st := umbilicalObjectsFromSnapshot(snap, objectsFilter{structure: "inn"})
	if st.Total != 2 || st.Objects[0].ID != "inn" || st.Objects[1].ID != "lamp" {
		t.Fatalf("structure filter = %+v, want [inn, lamp]", st.Objects)
	}

	// Unmatched filter → empty NON-NIL list; nil snapshot → empty roster.
	if got := umbilicalObjectsFromSnapshot(snap, objectsFilter{id: "ghost"}); got.Total != 0 || got.Objects == nil {
		t.Errorf("unmatched id = %+v, want empty non-nil list", got)
	}
	if got := umbilicalObjectsFromSnapshot(nil, objectsFilter{}); got.Total != 0 || got.Objects == nil {
		t.Errorf("nil snapshot = %+v, want empty roster", got)
	}

	// The ticket's open question 2: `?tag=business` is the whole answer to "show
	// me every business and what it owes" — no convenience filter needed.
	biz := umbilicalObjectsFromSnapshot(snap, objectsFilter{tag: sim.TagBusiness})
	if biz.Total != 1 || biz.Objects[0].ID != "store" || biz.Objects[0].RateOwed != 2 {
		t.Errorf("business filter = %+v, want only store owing 2", biz.Objects)
	}
}

// marshalledObjects round-trips the roster through JSON and returns the per-object
// key maps — so an assertion can ask what the WIRE carries rather than what the Go
// struct holds. Decoding into map[string]any (rather than substring-matching the
// body) is what makes key absence provable: a display_name or asset_id containing
// the word "wear" would satisfy a strings.Contains check.
func marshalledObjects(t *testing.T, dto UmbilicalObjectsDTO) []map[string]any {
	t.Helper()
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Objects []map[string]any `json:"objects"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return decoded.Objects
}

// TestUmbilicalObjectsCountersOmitEmpty pins the WIRE shape of the LLM-559
// counters, which the struct-level assertions above cannot see, in BOTH
// directions: absent on the ordinary placements that have no counters (most of
// the village is neither a business nor a hearth), present with their values on
// the ones that do. hearth_lit_until is the fragile half — a value time.Time
// would serialize the Go zero date "0001-01-01" onto every object in the
// village, because omitempty does not consider a struct empty; only the
// *time.Time keeps it absent.
func TestUmbilicalObjectsCountersOmitEmpty(t *testing.T) {
	published := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	counterKeys := []string{"wear", "rate_owed", "hearth_lit_until"}

	// A plain placement: no owner, no business tag, no hearth, no fire.
	snap := &sim.Snapshot{
		PublishedAt: published,
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"well": {ID: "well", AssetID: "asset_well", Pos: sim.WorldPos{X: 128, Y: 128}},
		},
	}
	well := marshalledObjects(t, umbilicalObjectsFromSnapshot(snap, objectsFilter{}))[0]
	for _, key := range counterKeys {
		if _, present := well[key]; present {
			t.Errorf("counter-free object carries %q = %v, want the key absent", key, well[key])
		}
	}

	// A worn, indebted, lit business carries all three — so the absences above
	// are omitempty working, not the fields being dropped outright.
	snap.VillageObjects["store"] = &sim.VillageObject{
		ID: "store", AssetID: "asset_store", OwnerActorID: "josiah",
		Tags:           []string{sim.TagBusiness, sim.TagHearth},
		Wear:           37,
		RateOwed:       2,
		HearthLitUntil: published.Add(time.Hour),
	}
	store := marshalledObjects(t, umbilicalObjectsFromSnapshot(snap, objectsFilter{id: "store"}))[0]
	for _, key := range counterKeys {
		if _, present := store[key]; !present {
			t.Errorf("store missing %q on the wire: %+v", key, store)
		}
	}
	// JSON numbers decode as float64.
	if store["wear"] != float64(37) || store["rate_owed"] != float64(2) {
		t.Errorf("store wire counters = wear %v / rate_owed %v, want 37 / 2", store["wear"], store["rate_owed"])
	}
	if store["hearth_lit_until"] != "2026-06-25T13:00:00Z" {
		t.Errorf("store hearth_lit_until = %v, want 2026-06-25T13:00:00Z", store["hearth_lit_until"])
	}

	// A fire that has already gone out still serializes: hearth_lit_until is
	// omitted only when UNSET, not when past. This is the distinction the route
	// descriptor spells out, and the reason "omitted at zero" would be wrong for
	// it — a stale timestamp is exactly what tells an operator the fire is dead
	// rather than never lit.
	snap.VillageObjects["cold"] = &sim.VillageObject{
		ID: "cold", AssetID: "asset_hearth", Tags: []string{sim.TagHearth},
		HearthLitUntil: published.Add(-2 * time.Hour),
	}
	cold := marshalledObjects(t, umbilicalObjectsFromSnapshot(snap, objectsFilter{id: "cold"}))[0]
	if cold["hearth_lit_until"] != "2026-06-25T10:00:00Z" {
		t.Errorf("burnt-out hearth = %v, want the past timestamp kept", cold["hearth_lit_until"])
	}
}
