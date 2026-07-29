package httpapi

import (
	"encoding/json"
	"strings"
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

// TestUmbilicalObjectsCountersOmitEmpty pins the WIRE shape of the LLM-559
// counters, which the struct-level assertions above cannot see: a roster of
// ordinary placements (no business, no hearth — most of the village) must not
// carry wear / rate_owed / hearth_lit_until keys at all. hearth_lit_until is the
// fragile one — a value time.Time would serialize the Go zero date "0001-01-01"
// on every object in the village, because omitempty does not consider a struct
// empty; only the *time.Time keeps it absent.
func TestUmbilicalObjectsCountersOmitEmpty(t *testing.T) {
	snap := &sim.Snapshot{
		PublishedAt: time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC),
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"well": {ID: "well", AssetID: "asset_well", Pos: sim.WorldPos{X: 128, Y: 128}},
		},
	}

	body, err := json.Marshal(umbilicalObjectsFromSnapshot(snap, objectsFilter{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"wear", "rate_owed", "hearth_lit_until"} {
		if strings.Contains(string(body), key) {
			t.Errorf("counter-free object emitted %q: %s", key, body)
		}
	}

	// ...and a hearth that IS lit emits the timestamp, so the absence above is
	// omitempty doing its job rather than the field being dropped outright.
	snap.VillageObjects["hearth"] = &sim.VillageObject{
		ID: "hearth", AssetID: "asset_hearth", Tags: []string{sim.TagHearth},
		HearthLitUntil: snap.PublishedAt.Add(time.Hour),
	}
	body, err = json.Marshal(umbilicalObjectsFromSnapshot(snap, objectsFilter{id: "hearth"}))
	if err != nil {
		t.Fatalf("marshal lit: %v", err)
	}
	if !strings.Contains(string(body), `"hearth_lit_until":"2026-06-25T13:00:00Z"`) {
		t.Errorf("lit hearth missing its timestamp: %s", body)
	}
}
