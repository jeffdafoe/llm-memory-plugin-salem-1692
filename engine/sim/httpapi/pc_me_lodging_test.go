package httpapi

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_me_lodging_test.go — LLM-38 PC lodging surface. pcLodgingSurface is pure
// over (snapshot, PC actor), so it's exercised directly rather than through the
// full handlePCMe HTTP harness.

func ledgerGrant(roomID sim.RoomID, expires time.Time) *sim.RoomAccess {
	return &sim.RoomAccess{RoomID: roomID, Source: sim.AccessSourceLedger, LedgerID: 1, Active: true, ExpiresAt: &expires}
}

func TestPCLodgingSurface_ActiveGrant(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	expires := now.Add(72 * time.Hour)
	snap := &sim.Snapshot{
		PublishedAt: now,
		Structures: map[sim.StructureID]*sim.Structure{
			"inn": {ID: "inn", Rooms: []*sim.Room{{ID: 7, StructureID: "inn", Kind: sim.RoomKindPrivate}}},
		},
		// The inn name resolves via the village-object bridge (objectDisplayName),
		// not the structure row.
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"inn": {DisplayName: "The Tavern"},
		},
		// The keeper works at the inn — surfaced so the client can scope the
		// held-room empty-state to "my own innkeeper is co-present".
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"keeper": {WorkStructureID: "inn", DisplayName: "John Ellis"},
		},
	}
	pc := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 7, Source: sim.AccessSourceLedger}: ledgerGrant(7, expires),
	}}

	v := pcLodgingSurface(snap, pc)
	if v == nil {
		t.Fatal("want a lodging surface for a PC holding an active grant, got nil")
	}
	if v.InnName != "The Tavern" {
		t.Errorf("InnName = %q, want The Tavern", v.InnName)
	}
	if v.KeeperName != "John Ellis" {
		t.Errorf("KeeperName = %q, want John Ellis", v.KeeperName)
	}
	if v.UntilLabel != "for about 3 more nights" {
		t.Errorf("UntilLabel = %q, want 'for about 3 more nights'", v.UntilLabel)
	}
	if !v.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", v.ExpiresAt, expires)
	}
}

func TestPCLodgingSurface_NoGrant_Nil(t *testing.T) {
	snap := &sim.Snapshot{PublishedAt: time.Now()}
	if v := pcLodgingSurface(snap, &sim.ActorSnapshot{}); v != nil {
		t.Errorf("a PC with no grant has no lodging surface — want nil, got %+v", v)
	}
}

func TestPCLodgingSurface_ExpiredGrant_Nil(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	pc := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 7, Source: sim.AccessSourceLedger}: ledgerGrant(7, now.Add(-time.Hour)),
	}}
	if v := pcLodgingSurface(&sim.Snapshot{PublishedAt: now}, pc); v != nil {
		t.Errorf("an expired grant is not active lodging — want nil, got %+v", v)
	}
}

func TestPCLodgingUntilLabel_Tiers(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{72 * time.Hour, "for about 3 more nights"},
		{30 * time.Hour, "through tomorrow"},
		{2 * time.Hour, "through the day"},
	}
	for _, c := range cases {
		if got := pcLodgingUntilLabel(c.d); got != c.want {
			t.Errorf("pcLodgingUntilLabel(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
