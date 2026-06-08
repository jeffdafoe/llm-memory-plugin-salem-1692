package perception

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// lodging_test.go — ZBBS-HOME-296 PR2. Covers the lodger view gating +
// escalation tiers, structure-name resolution, and the keeper occupancy
// count.

var lodgingNow = time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

func ptrTime(t time.Time) *time.Time { return &t }

// ledgerAccess builds an active ledger RoomAccess expiring at now+d.
func ledgerAccess(roomID sim.RoomID, d time.Duration) *sim.RoomAccess {
	return &sim.RoomAccess{
		RoomID:    roomID,
		Source:    sim.AccessSourceLedger,
		LedgerID:  1,
		ExpiresAt: ptrTime(lodgingNow.Add(d)),
		Active:    true,
	}
}

func lodgingSnap(subj *sim.ActorSnapshot, structures map[sim.StructureID]*sim.Structure, others ...*sim.ActorSnapshot) *sim.Snapshot {
	actors := map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj}
	for i, o := range others {
		actors[sim.ActorID(fmt.Sprintf("other%d", i))] = o
	}
	return &sim.Snapshot{
		PublishedAt: lodgingNow,
		Actors:      actors,
		Structures:  structures,
	}
}

// --- lodger view gating ---

func TestBuildLodgingView_NoAccess_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{}
	if v := buildLodgingView(lodgingSnap(subj, nil), subj); v != nil {
		t.Errorf("want nil for an actor with no room access, got %+v", v)
	}
}

func TestBuildLodgingView_ActiveLedger_View(t *testing.T) {
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	v := buildLodgingView(lodgingSnap(subj, structs), subj)
	if v == nil {
		t.Fatal("want a lodging view for an active ledger grant, got nil")
	}
	if v.InnName != "Hannah's Inn" {
		t.Errorf("InnName = %q, want %q", v.InnName, "Hannah's Inn")
	}
	if !v.ExpiresAt.Equal(lodgingNow.Add(72 * time.Hour)) {
		t.Errorf("ExpiresAt = %v, want %v", v.ExpiresAt, lodgingNow.Add(72*time.Hour))
	}
}

func TestBuildLodgingView_ExpiredLedger_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, -time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	if v := buildLodgingView(lodgingSnap(subj, structs), subj); v != nil {
		t.Errorf("want nil for an expired grant, got %+v", v)
	}
}

func TestBuildLodgingView_InactiveLedger_Nil(t *testing.T) {
	ra := ledgerAccess(2, 72*time.Hour)
	ra.Active = false
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ra,
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	if v := buildLodgingView(lodgingSnap(subj, structs), subj); v != nil {
		t.Errorf("want nil for an inactive grant, got %+v", v)
	}
}

func TestBuildLodgingView_StaffAccess_Nil(t *testing.T) {
	// A staff grant (never-expiring, non-ledger) is not lodging.
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceStaff}: {RoomID: 2, Source: sim.AccessSourceStaff, Active: true},
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	if v := buildLodgingView(lodgingSnap(subj, structs), subj); v != nil {
		t.Errorf("want nil for a staff grant, got %+v", v)
	}
}

func TestBuildLodgingView_PicksSoonestExpiry(t *testing.T) {
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 200*time.Hour),
			{RoomID: 3, Source: sim.AccessSourceLedger}: ledgerAccess(3, 30*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	v := buildLodgingView(lodgingSnap(subj, structs), subj)
	if v == nil {
		t.Fatal("want a view, got nil")
	}
	if !v.ExpiresAt.Equal(lodgingNow.Add(30 * time.Hour)) {
		t.Errorf("ExpiresAt = %v, want the soonest (now+30h)", v.ExpiresAt)
	}
}

func TestBuildLodgingView_UnknownStructure_GenericName(t *testing.T) {
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 99, Source: sim.AccessSourceLedger}: ledgerAccess(99, 72*time.Hour),
		},
	}
	// No structure declares room 99 → generic fallback name.
	v := buildLodgingView(lodgingSnap(subj, nil), subj)
	if v == nil || v.InnName != "the inn" {
		t.Fatalf("want generic fallback name, got %+v", v)
	}
}

// --- escalation tiers ---

func TestLodgingStatusLine_Tiers(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"calm", 72 * time.Hour, "is paid for about 3 more nights"},
		{"soon", 36 * time.Hour, "expires in about a day"},
		{"urgent", 6 * time.Hour, "expires within the day"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lodgingStatusLine("Hannah's Inn", lodgingNow.Add(tc.d), lodgingNow)
			if !strings.Contains(got, tc.want) {
				t.Errorf("line = %q, want substring %q", got, tc.want)
			}
			if !strings.Contains(got, "Hannah's Inn") {
				t.Errorf("line = %q, want inn name", got)
			}
		})
	}
}

func TestRenderLodging_GatedAndSectioned(t *testing.T) {
	var b strings.Builder
	renderLodging(&b, nil)
	if b.String() != "" {
		t.Errorf("nil view must render nothing, got %q", b.String())
	}
	b.Reset()
	renderLodging(&b, &LodgingView{InnName: "Hannah's Inn", ExpiresAt: time.Now().Add(72 * time.Hour)})
	if !strings.Contains(b.String(), "## Your lodging") {
		t.Errorf("want section header, got %q", b.String())
	}
}

// --- rate hint + affordability cue ---

func TestBuildLodgingView_CarriesRateAndCoins(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Coins: 11,
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 30*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	snap := lodgingSnap(subj, structs)
	snap.LodgingDefaultWeeklyRate = 28 // nightly 4
	v := buildLodgingView(snap, subj)
	if v == nil || v.NightlyRate != 4 || v.Coins != 11 {
		t.Fatalf("want NightlyRate=4 Coins=11, got %+v", v)
	}
}

func TestLodgingAffordabilityCue(t *testing.T) {
	near := &LodgingView{InnName: "Hannah's Inn", ExpiresAt: lodgingNow.Add(30 * time.Hour), NightlyRate: 4, Coins: 1}
	if cue := lodgingAffordabilityCue(near, lodgingNow); cue == "" {
		t.Error("near-expiry + broke must produce the affordability cue")
	}
	// affordable → no cue
	flush := &LodgingView{InnName: "Hannah's Inn", ExpiresAt: lodgingNow.Add(30 * time.Hour), NightlyRate: 4, Coins: 10}
	if cue := lodgingAffordabilityCue(flush, lodgingNow); cue != "" {
		t.Errorf("affordable lodger must get no cue, got %q", cue)
	}
	// calm (>48h) → no cue even when broke
	calm := &LodgingView{InnName: "Hannah's Inn", ExpiresAt: lodgingNow.Add(120 * time.Hour), NightlyRate: 4, Coins: 1}
	if cue := lodgingAffordabilityCue(calm, lodgingNow); cue != "" {
		t.Errorf("calm window must suppress the cue, got %q", cue)
	}
	// rate disabled → no cue
	off := &LodgingView{InnName: "Hannah's Inn", ExpiresAt: lodgingNow.Add(30 * time.Hour), NightlyRate: 0, Coins: 0}
	if cue := lodgingAffordabilityCue(off, lodgingNow); cue != "" {
		t.Errorf("disabled rate must suppress the cue, got %q", cue)
	}
	// already expired (negative remaining) → no cue ("before your room lapses"
	// would be wrong after it already lapsed)
	expired := &LodgingView{InnName: "Hannah's Inn", ExpiresAt: lodgingNow.Add(-time.Hour), NightlyRate: 4, Coins: 1}
	if cue := lodgingAffordabilityCue(expired, lodgingNow); cue != "" {
		t.Errorf("expired grant must suppress the cue, got %q", cue)
	}
}

func TestRenderLodging_RateHintAndCue(t *testing.T) {
	var b strings.Builder
	renderLodging(&b, &LodgingView{InnName: "Hannah's Inn", ExpiresAt: time.Now().Add(30 * time.Hour), NightlyRate: 4, Coins: 1})
	out := b.String()
	if !strings.Contains(out, "4 coins a night") {
		t.Errorf("want nightly-rate hint, got %q", out)
	}
	if !strings.Contains(out, "only 1 coins") {
		t.Errorf("want affordability cue, got %q", out)
	}
}

func TestRenderKeeperLodging_RateWhenAvailable(t *testing.T) {
	var b strings.Builder
	renderKeeperLodging(&b, &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 2, RoomsTotal: 3, NightlyRate: 4})
	if !strings.Contains(b.String(), "4 coins a night") {
		t.Errorf("keeper with a free room must quote the rate, got %q", b.String())
	}
	// no rate when full
	b.Reset()
	renderKeeperLodging(&b, &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 0, RoomsTotal: 3, NightlyRate: 4})
	if strings.Contains(b.String(), "coins a night") {
		t.Errorf("full inn must not quote a rate, got %q", b.String())
	}
}

// --- keeper occupancy ---

// innStructureN builds an inn with n private bedrooms (room IDs 2..n+1) plus a
// common room (ID 1).
func innStructureN(id sim.StructureID, name string, n int) *sim.Structure {
	rooms := []*sim.Room{{ID: 1, StructureID: id, Kind: sim.RoomKindCommon, Name: "common"}}
	for i := 0; i < n; i++ {
		rooms = append(rooms, &sim.Room{ID: sim.RoomID(2 + i), StructureID: id, Kind: sim.RoomKindPrivate})
	}
	return &sim.Structure{ID: id, DisplayName: name, Rooms: rooms}
}

func TestBuildKeeperLodgingView_NonKeeper_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{} // no WorkStructureID
	if v := buildKeeperLodgingView(lodgingSnap(subj, nil), subj); v != nil {
		t.Errorf("want nil for a non-keeper, got %+v", v)
	}
}

func TestBuildKeeperLodgingView_WorkStructureHasNoPrivateRooms_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{WorkStructureID: "smithy"}
	structs := map[sim.StructureID]*sim.Structure{
		"smithy": {ID: "smithy", DisplayName: "The Smithy", Rooms: []*sim.Room{{ID: 1, StructureID: "smithy", Kind: sim.RoomKindCommon}}},
	}
	if v := buildKeeperLodgingView(lodgingSnap(subj, structs), subj); v != nil {
		t.Errorf("want nil when the work structure has no private rooms, got %+v", v)
	}
}

func TestBuildKeeperLodgingView_CountsOccupancy(t *testing.T) {
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 3)}
	// Two lodgers occupy rooms 2 and 3; room 4 is free.
	lodgerA := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
	}}
	lodgerB := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 3, Source: sim.AccessSourceLedger}: ledgerAccess(3, 72*time.Hour),
	}}
	v := buildKeeperLodgingView(lodgingSnap(keeper, structs, lodgerA, lodgerB), keeper)
	if v == nil {
		t.Fatal("want a keeper view, got nil")
	}
	if v.RoomsTotal != 3 || v.RoomsAvailable != 1 {
		t.Errorf("occupancy = %d/%d available, want 1/3", v.RoomsAvailable, v.RoomsTotal)
	}
}

func TestBuildKeeperLodgingView_IgnoresExpiredAndOtherStructures(t *testing.T) {
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 2)}
	// An expired grant on room 2 must NOT count as occupied; a grant on a
	// room id belonging to another structure (room 50) must not count either.
	expired := ledgerAccess(2, -time.Hour)
	lodger := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}:  expired,
		{RoomID: 50, Source: sim.AccessSourceLedger}: ledgerAccess(50, 72*time.Hour),
	}}
	v := buildKeeperLodgingView(lodgingSnap(keeper, structs, lodger), keeper)
	if v == nil {
		t.Fatal("want a keeper view, got nil")
	}
	if v.RoomsAvailable != 2 {
		t.Errorf("RoomsAvailable = %d, want 2 (expired + foreign-room grants ignored)", v.RoomsAvailable)
	}
}

func TestRenderKeeperLodging_Gated(t *testing.T) {
	var b strings.Builder
	renderKeeperLodging(&b, nil)
	if b.String() != "" {
		t.Errorf("nil view must render nothing, got %q", b.String())
	}
	b.Reset()
	renderKeeperLodging(&b, &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 1, RoomsTotal: 3})
	out := b.String()
	if !strings.Contains(out, "## Your inn") || !strings.Contains(out, "1 of 3 rooms available") {
		t.Errorf("keeper render = %q, want header + occupancy", out)
	}
}

// --- lodging offer cue (ZBBS-WORK-382) ---

func TestBuildLodgingOfferCue_HomelessSeeker_Offers(t *testing.T) {
	seeker := &sim.ActorSnapshot{} // no home, no room access → nowhere to sleep
	snap := lodgingSnap(seeker, nil)
	keeper := &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 2, NightlyRate: 4}
	members := []HuddleMember{{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true}}
	v := buildLodgingOfferCue(snap, "hannah", keeper, members)
	if v == nil {
		t.Fatal("want an offer cue for a homeless co-present seeker, got nil")
	}
	if len(v.SeekerNames) != 1 || v.SeekerNames[0] != "Ezekiel Crane" {
		t.Errorf("SeekerNames = %v, want [Ezekiel Crane]", v.SeekerNames)
	}
}

func TestBuildLodgingOfferCue_SeekerHasHome_Nil(t *testing.T) {
	seeker := &sim.ActorSnapshot{HomeStructureID: "cottage"}
	snap := lodgingSnap(seeker, nil)
	keeper := &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 2, NightlyRate: 4}
	members := []HuddleMember{{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true}}
	if v := buildLodgingOfferCue(snap, "hannah", keeper, members); v != nil {
		t.Errorf("a seeker with a home sleeps there — want nil, got %+v", v)
	}
}

func TestBuildLodgingOfferCue_SeekerAlreadyLodging_Nil(t *testing.T) {
	seeker := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
	}}
	snap := lodgingSnap(seeker, nil)
	keeper := &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 2, NightlyRate: 4}
	members := []HuddleMember{{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true}}
	if v := buildLodgingOfferCue(snap, "hannah", keeper, members); v != nil {
		t.Errorf("a seeker already holding a grant beds there — want nil, got %+v", v)
	}
}

func TestBuildLodgingOfferCue_NoVacancyNoRateNonKeeper_Nil(t *testing.T) {
	seeker := &sim.ActorSnapshot{}
	snap := lodgingSnap(seeker, nil)
	members := []HuddleMember{{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true}}
	if v := buildLodgingOfferCue(snap, "hannah", &KeeperLodgingView{InnName: "X", RoomsAvailable: 0, NightlyRate: 4}, members); v != nil {
		t.Errorf("full inn must not offer — want nil, got %+v", v)
	}
	if v := buildLodgingOfferCue(snap, "hannah", &KeeperLodgingView{InnName: "X", RoomsAvailable: 2, NightlyRate: 0}, members); v != nil {
		t.Errorf("disabled rate (0) must not offer — want nil, got %+v", v)
	}
	if v := buildLodgingOfferCue(snap, "hannah", nil, members); v != nil {
		t.Errorf("a non-keeper must not offer — want nil, got %+v", v)
	}
}

func TestRenderLodgingOffer_NamesActionAndNights(t *testing.T) {
	var b strings.Builder
	renderLodgingOffer(&b, &LodgingOfferView{
		SeekerNames:    []string{"Ezekiel Crane"},
		InnName:        "Hannah's Inn",
		RoomsAvailable: 2,
		NightlyRate:    4,
	})
	out := b.String()
	for _, want := range []string{"## A room to let", "Ezekiel Crane", "nights_stay", "scene_quote", "number of nights", "consume_now false", "target_buyer only if you know"} {
		if !strings.Contains(out, want) {
			t.Errorf("offer cue missing %q, got %q", want, out)
		}
	}
}
