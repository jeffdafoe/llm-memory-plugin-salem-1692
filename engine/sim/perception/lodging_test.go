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
		// Bedtime 22:00 + checkout 11:00 → a 13h renewal window (LLM-96), so a
		// grant expiring more than 13h out is settled and one expiring within it is
		// renewal-due. Without these the window falls back to 48h.
		LodgingBedtimeMinute:  22 * 60,
		LodgingCheckOutMinute: 11 * 60,
	}
}

// renewalSnap builds a lodger holding an active room grant at "inn", a keeper
// working that inn (keyed "other0" by lodgingSnap), and the item catalog with a
// lodging-capability nights_stay. The LLM-81 in-flight tests wire their offer /
// order with SellerID "other0" (the resolved keeper).
func renewalSnap() (*sim.Snapshot, *sim.ActorSnapshot) {
	subj := &sim.ActorSnapshot{
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 20*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	snap := lodgingSnap(subj, structs, keeper)
	snap.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"nights_stay": {Name: "nights_stay", Capabilities: []string{"lodging"}},
	}
	return snap, subj
}

// TestBuildLodgingView_RenewalInFlight_PendingOffer: a still-pending nights_stay
// offer to the inn keeper sets RenewalInFlight, so render can defer the renewal
// cue instead of re-prompting payment. LLM-81.
func TestBuildLodgingView_RenewalInFlight_PendingOffer(t *testing.T) {
	snap, subj := renewalSnap()
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: "ezekiel", SellerID: "other0", ItemKind: "nights_stay", State: sim.PayLedgerStatePending},
	}
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || !v.RenewalInFlight {
		t.Fatalf("want RenewalInFlight for a pending nights_stay offer to the keeper, got %+v", v)
	}
}

// TestBuildLodgingView_RenewalInFlight_AcceptedOrder: an accepted-but-undelivered
// nights_stay order (OrderStateReady) from the keeper sets RenewalInFlight — the
// case that actually drove the double-pay, since the duplicate fired AFTER accept,
// when the pending offer is already gone. LLM-81.
func TestBuildLodgingView_RenewalInFlight_AcceptedOrder(t *testing.T) {
	snap, subj := renewalSnap()
	snap.Orders = map[sim.OrderID]*sim.Order{
		5: {ID: 5, State: sim.OrderStateReady, BuyerID: "ezekiel", SellerID: "other0", Item: "nights_stay"},
	}
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || !v.RenewalInFlight {
		t.Fatalf("want RenewalInFlight for an accepted-but-undelivered nights_stay order, got %+v", v)
	}
}

// TestBuildLodgingView_RenewalToOtherSeller_NotInFlight: a pending nights_stay
// offer to a DIFFERENT seller than the inn keeper must NOT set the flag — the
// renewal cue for THIS inn still applies. Guards the keeper narrowing. LLM-81.
func TestBuildLodgingView_RenewalToOtherSeller_NotInFlight(t *testing.T) {
	snap, subj := renewalSnap()
	snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
		1: {ID: 1, BuyerID: "ezekiel", SellerID: "someone_else", ItemKind: "nights_stay", State: sim.PayLedgerStatePending},
	}
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || v.RenewalInFlight {
		t.Fatalf("RenewalInFlight must be false for an offer to a non-keeper seller, got %+v", v)
	}
}

// TestBuildLodgingView_DeliveredOrder_NotInFlight: a Delivered (terminal) order
// must NOT keep the cue suppressed — once the grant has extended the renewal cue
// should return on the next window. LLM-81.
func TestBuildLodgingView_DeliveredOrder_NotInFlight(t *testing.T) {
	snap, subj := renewalSnap()
	snap.Orders = map[sim.OrderID]*sim.Order{
		5: {ID: 5, State: sim.OrderStateDelivered, BuyerID: "ezekiel", SellerID: "other0", Item: "nights_stay"},
	}
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || v.RenewalInFlight {
		t.Fatalf("RenewalInFlight must be false for a delivered order, got %+v", v)
	}
}

// TestRenderLodging_RenewalInFlight_WaitSteer: with RenewalInFlight set, render
// drops the expiry/renew line, the rate hint, and the shortfall cue for a
// stay-and-wait steer — so the lodger bides instead of paying twice. LLM-81.
func TestRenderLodging_RenewalInFlight_WaitSteer(t *testing.T) {
	v := &LodgingView{
		InnName:         "Hannah's Inn",
		ExpiresAt:       lodgingNow.Add(20 * time.Hour),
		NightlyRate:     4,
		Coins:           0,
		RenewalInFlight: true,
	}
	var b strings.Builder
	renderLodging(&b, v)
	out := b.String()
	if !strings.Contains(out, "paid and with the keeper") || !strings.Contains(out, "Do not pay for it again") {
		t.Errorf("missing the in-flight wait-steer:\n%s", out)
	}
	if strings.Contains(out, "settled here") || strings.Contains(out, "nearly up") || strings.Contains(out, "Renewing is") {
		t.Errorf("in-flight render must suppress the settled/renew lines + rate hint:\n%s", out)
	}
	if strings.Contains(out, "short of the") {
		t.Errorf("in-flight render must suppress the affordability shortfall cue:\n%s", out)
	}
}

// --- lodger view gating ---

func TestBuildLodgingView_NoAccess_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{}
	if v := buildLodgingView(lodgingSnap(subj, nil), "ezekiel", subj, nil); v != nil {
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
	v := buildLodgingView(lodgingSnap(subj, structs), "ezekiel", subj, nil)
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
	if v := buildLodgingView(lodgingSnap(subj, structs), "ezekiel", subj, nil); v != nil {
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
	if v := buildLodgingView(lodgingSnap(subj, structs), "ezekiel", subj, nil); v != nil {
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
	if v := buildLodgingView(lodgingSnap(subj, structs), "ezekiel", subj, nil); v != nil {
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
	v := buildLodgingView(lodgingSnap(subj, structs), "ezekiel", subj, nil)
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
	v := buildLodgingView(lodgingSnap(subj, nil), "ezekiel", subj, nil)
	if v == nil || v.InnName != "the inn" {
		t.Fatalf("want generic fallback name, got %+v", v)
	}
}

// TestBuildLodgingView_DeskRememberedShutFlagged — LLM-126. When the lodger has a
// decaying experiential memory of finding its inn's keeper-desk shut, the lodging
// view flags it (DeskRememberedShut) and renderLodging steers the lodger to wait
// rather than walking back to an unattended desk — the experiential replacement
// for the retired omniscient keeper-asleep read. The keeper here is awake: the cue
// is driven by the memory, not the keeper's live state.
func TestBuildLodgingView_DeskRememberedShutFlagged(t *testing.T) {
	subj := &sim.ActorSnapshot{
		// Renewal-due (within the 13h window) so the renewal cue — and thus the
		// desk-shut caveat that rides it — actually renders.
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 8*time.Hour),
		},
		// He went to the inn within the decay window and found the desk untended.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "inn", Condition: sim.ObservedClosed}: lodgingNow.Add(-time.Hour),
		}),
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn", State: sim.StateIdle}
	v := buildLodgingView(lodgingSnap(subj, structs, keeper), "ezekiel", subj, nil)
	if v == nil || !v.DeskRememberedShut || !v.RenewalDue {
		t.Fatalf("want DeskRememberedShut + RenewalDue set when the lodger remembers the desk shut, got %+v", v)
	}
	var b strings.Builder
	renderLodging(&b, v)
	if !strings.Contains(b.String(), "found the keeper's desk shut") {
		t.Errorf("rendered lodging missing the experiential desk-shut caveat:\n%s", b.String())
	}
}

// --- escalation tiers ---

func TestLodgingStatusLine_ThreeState(t *testing.T) {
	// Settled: confirm the room, do NOT nudge a renewal (the LLM-96 fix — a
	// settled lodger re-told to renew is what drove the double-buy).
	settled := lodgingStatusLine("Hannah's Inn", false, false)
	if !strings.Contains(settled, "settled here") || strings.Contains(settled, "renew") {
		t.Errorf("settled line = %q, want a settled confirmation with no renew nudge", settled)
	}
	// Renewal-due (final night before checkout): steer to renew now.
	due := lodgingStatusLine("Hannah's Inn", true, false)
	if !strings.Contains(due, "nearly up") || !strings.Contains(due, "if you wish to stay on") {
		t.Errorf("renewal-due line = %q, want an active renew steer", due)
	}
	// Renewal-due but deferred (gate 3, LLM-127): still flags "nearly up" but steers
	// to renew at the inn rather than walking off-post now.
	deferred := lodgingStatusLine("Hannah's Inn", true, true)
	if !strings.Contains(deferred, "nearly up") || !strings.Contains(deferred, "when you are next back at the inn") {
		t.Errorf("deferred line = %q, want a defer-to-the-inn steer", deferred)
	}
	if strings.Contains(deferred, "if you wish to stay on") {
		t.Errorf("deferred line = %q, must drop the active walk-pull phrasing", deferred)
	}
	for _, l := range []string{settled, due, deferred} {
		if !strings.Contains(l, "Hannah's Inn") {
			t.Errorf("line %q missing inn name", l)
		}
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

// --- LLM-127 renewal-pull suppression gates ---

// gateLodger builds a renewal-due lodger (room 2 of "inn", expiring inside the
// 13h window) standing inside `inside`, scheduled 06:00–18:00, at local minute
// nowMin, optionally sharing a huddle with a peer that is awake or asleep. Returns
// the built view so a test can assert the gate flags. renewalDue is computed off
// PublishedAt (lodgingNow), so nowMin only drives the on-shift check.
func gateLodger(t *testing.T, inside sim.StructureID, nowMin int, withPeer, peerAwake bool) *LodgingView {
	t.Helper()
	start, end := 6*60, 18*60
	subj := &sim.ActorSnapshot{
		InsideStructureID: inside,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 8*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	var others []*sim.ActorSnapshot
	var members []HuddleMember
	if withPeer {
		st := sim.StateSleeping
		if peerAwake {
			st = sim.StateIdle
		}
		others = append(others, &sim.ActorSnapshot{State: st})
		members = []HuddleMember{{ID: "other0"}} // lodgingSnap keys the first extra actor "other0"
	}
	snap := lodgingSnap(subj, structs, others...)
	snap.LocalMinuteOfDay = &nowMin
	v := buildLodgingView(snap, "ezekiel", subj, members)
	if v == nil || !v.RenewalDue {
		t.Fatalf("fixture must be a renewal-due lodger, got %+v", v)
	}
	return v
}

// Gate 1 (conversation): an awake huddle peer is a live conversation; a sleeper or
// an empty huddle is not. now=20:00 is off-shift so the defer flag stays isolated.
func TestBuildLodgingView_InConversation(t *testing.T) {
	if v := gateLodger(t, "market", 20*60, true, true); !v.InConversation {
		t.Errorf("an awake huddle peer must set InConversation, got %+v", v)
	}
	if v := gateLodger(t, "market", 20*60, true, false); v.InConversation {
		t.Errorf("a sleeping huddle peer is no conversation — want InConversation=false, got %+v", v)
	}
	if v := gateLodger(t, "market", 20*60, false, false); v.InConversation {
		t.Errorf("no huddle peer → InConversation must be false, got %+v", v)
	}
}

// Gate 3 (defer): the pull is deferred only when on-shift AND away from the inn.
// withPeer=false isolates the defer flag from the conversation gate.
func TestBuildLodgingView_RenewalPullDeferred(t *testing.T) {
	if v := gateLodger(t, "blacksmith", 12*60, false, false); !v.RenewalPullDeferred {
		t.Errorf("on-shift away from the inn must defer the pull, got %+v", v)
	}
	if v := gateLodger(t, "inn", 12*60, false, false); v.RenewalPullDeferred {
		t.Errorf("at the inn the renewal is actionable — want no defer, got %+v", v)
	}
	if v := gateLodger(t, "blacksmith", 20*60, false, false); v.RenewalPullDeferred {
		t.Errorf("off-shift the lodger is free to walk over — want no defer, got %+v", v)
	}
}

// A settled (not renewal-due) lodger never carries the deferred flag, even when
// on-shift away from the inn — the flag means "deferred renewal pull", and there is
// no pull to defer when settled (RenewalPullDeferred is gated on RenewalDue so the
// view stays internally consistent for any future caller). (code_review)
func TestBuildLodgingView_SettledOnShiftAway_NoDefer(t *testing.T) {
	start, end := 6*60, 18*60
	nowMin := 12 * 60 // on-shift
	subj := &sim.ActorSnapshot{
		InsideStructureID: "blacksmith", // away from the inn
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour), // beyond the 13h window → settled
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	snap := lodgingSnap(subj, structs)
	snap.LocalMinuteOfDay = &nowMin
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || v.RenewalDue {
		t.Fatalf("a 72h grant must be settled, got %+v", v)
	}
	if v.RenewalPullDeferred {
		t.Errorf("a settled lodger must not carry RenewalPullDeferred, got %+v", v)
	}
}

// An unscheduled lodger (nil schedule) is always off-shift, so the pull is never
// deferred even when away from the inn (matches sim.isActorOnShift).
func TestBuildLodgingView_RenewalPullDeferred_Unscheduled_False(t *testing.T) {
	subj := &sim.ActorSnapshot{
		InsideStructureID: "blacksmith",
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 8*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
	nowMin := 12 * 60
	snap := lodgingSnap(subj, structs)
	snap.LocalMinuteOfDay = &nowMin
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || v.RenewalPullDeferred {
		t.Fatalf("an unscheduled lodger is off-shift → no defer, got %+v", v)
	}
}

// Gate 1 render: renewal-due + mid-conversation drops the whole section; a settled
// lodger mid-conversation still gets the harmless confirmation line.
func TestRenderLodging_Gate1_MidConversationDropsBlock(t *testing.T) {
	var b strings.Builder
	renderLodging(&b, &LodgingView{InnName: "Hannah's Inn", RenewalDue: true, NightlyRate: 4, Coins: 0, InConversation: true})
	if b.String() != "" {
		t.Errorf("renewal-due + in-conversation must drop the whole block, got %q", b.String())
	}
	b.Reset()
	renderLodging(&b, &LodgingView{InnName: "Hannah's Inn", RenewalDue: false, InConversation: true})
	if !strings.Contains(b.String(), "settled here") {
		t.Errorf("settled lodger mid-conversation should still confirm the room, got %q", b.String())
	}
}

// Gate 3 render: deferred steers to renew at the inn, drops the active walk-pull
// and the now-redundant desk-shut note, but keeps the earn cue (gate 2 was
// intentionally not added — a broke lodger may still barter for the room).
func TestRenderLodging_Gate3_DeferredPhrasing(t *testing.T) {
	var b strings.Builder
	renderLodging(&b, &LodgingView{InnName: "Hannah's Inn", RenewalDue: true, NightlyRate: 4, Coins: 0, RenewalPullDeferred: true, DeskRememberedShut: true})
	out := b.String()
	if !strings.Contains(out, "when you are next back at the inn") {
		t.Errorf("deferred render must steer to renew at the inn, got %q", out)
	}
	if strings.Contains(out, "if you wish to stay on") {
		t.Errorf("deferred render must drop the active walk-pull, got %q", out)
	}
	if strings.Contains(out, "found the keeper's desk shut") {
		t.Errorf("deferred render should suppress the redundant desk-shut note, got %q", out)
	}
	// LLM-136: the cue now names the barter path explicitly (offer_trade) alongside
	// the earn-coins fallback — a broke lodger can renew with its wares.
	if !strings.Contains(out, "offer_trade") || !strings.Contains(out, "earn coins") {
		t.Errorf("a broke deferred lodger should get the barter-or-earn cue, got %q", out)
	}
}

// TestRenderLodging_WalkPullInvariant is the LLM-127 cross-cutting guarantee: the
// active renewal walk-pull appears IFF the lodger is renewal-due AND not in a
// conversation AND the pull is not deferred. Exhaustive over the three gate booleans.
func TestRenderLodging_WalkPullInvariant(t *testing.T) {
	const walkPull = "if you wish to stay on, see the keeper to renew"
	for _, due := range []bool{false, true} {
		for _, conv := range []bool{false, true} {
			for _, deferred := range []bool{false, true} {
				var b strings.Builder
				renderLodging(&b, &LodgingView{
					InnName:             "Hannah's Inn",
					RenewalDue:          due,
					InConversation:      conv,
					RenewalPullDeferred: deferred,
				})
				wantPull := due && !conv && !deferred
				if gotPull := strings.Contains(b.String(), walkPull); gotPull != wantPull {
					t.Errorf("due=%v conv=%v deferred=%v: walk-pull present=%v, want %v\n%q",
						due, conv, deferred, gotPull, wantPull, b.String())
				}
			}
		}
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
	v := buildLodgingView(snap, "ezekiel", subj, nil)
	if v == nil || v.NightlyRate != 4 || v.Coins != 11 {
		t.Fatalf("want NightlyRate=4 Coins=11, got %+v", v)
	}
}

func TestLodgingAffordabilityCue(t *testing.T) {
	// renewal-due + broke → cue
	due := &LodgingView{InnName: "Hannah's Inn", RenewalDue: true, NightlyRate: 4, Coins: 1}
	if cue := lodgingAffordabilityCue(due); cue == "" {
		t.Error("renewal-due + broke must produce the affordability cue")
	}
	// renewal-due + affordable → no cue
	flush := &LodgingView{InnName: "Hannah's Inn", RenewalDue: true, NightlyRate: 4, Coins: 10}
	if cue := lodgingAffordabilityCue(flush); cue != "" {
		t.Errorf("affordable lodger must get no cue, got %q", cue)
	}
	// settled (not renewal-due) → no cue even when broke (LLM-96: don't nag a
	// lodger about rent the moment it pays for the room)
	settled := &LodgingView{InnName: "Hannah's Inn", RenewalDue: false, NightlyRate: 4, Coins: 1}
	if cue := lodgingAffordabilityCue(settled); cue != "" {
		t.Errorf("a settled lodger must not be nagged about rent, got %q", cue)
	}
	// rate disabled → no cue
	off := &LodgingView{InnName: "Hannah's Inn", RenewalDue: true, NightlyRate: 0, Coins: 0}
	if cue := lodgingAffordabilityCue(off); cue != "" {
		t.Errorf("disabled rate must suppress the cue, got %q", cue)
	}
}

func TestRenderLodging_RateHintAndCue(t *testing.T) {
	var b strings.Builder
	// Renewal-due (final night): the renew branch carries the rate hint + cue.
	renderLodging(&b, &LodgingView{InnName: "Hannah's Inn", RenewalDue: true, NightlyRate: 4, Coins: 1})
	out := b.String()
	if !strings.Contains(out, "4 coins a night") {
		t.Errorf("want nightly-rate hint, got %q", out)
	}
	if !strings.Contains(out, "only 1 coins") {
		t.Errorf("want affordability cue, got %q", out)
	}
}

func TestLodgingRenewalWindow(t *testing.T) {
	// bedtime 22:00 → checkout 11:00 next morning = 13h.
	got := lodgingRenewalWindow(&sim.Snapshot{LodgingBedtimeMinute: 22 * 60, LodgingCheckOutMinute: 11 * 60})
	if got != 13*time.Hour {
		t.Errorf("window = %s, want 13h (bedtime 22:00 → checkout 11:00)", got)
	}
	// No clock on the snapshot → 48h fallback (a hand-built snapshot leaves the
	// minutes at 0, which the guard catches).
	if got := lodgingRenewalWindow(&sim.Snapshot{}); got != 48*time.Hour {
		t.Errorf("hand-built snapshot must fall back to 48h, got %s", got)
	}
	if got := lodgingRenewalWindow(nil); got != 48*time.Hour {
		t.Errorf("nil snapshot must fall back to 48h, got %s", got)
	}
}

// TestBuildLodgingView_RenewalDue_FinalNightOnly is the LLM-96 regression: a
// freshly-bought single-night room (expiring ~20h out, beyond the 13h
// bedtime-to-checkout window) is NOT renewal-due — the lodger is told it is
// settled rather than nudged to renew the room it just paid for. It flips to
// renewal-due only once the grant is within the final-night window.
func TestBuildLodgingView_RenewalDue_FinalNightOnly(t *testing.T) {
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}

	settledSubj := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 20*time.Hour),
	}}
	sv := buildLodgingView(lodgingSnap(settledSubj, structs), "ezekiel", settledSubj, nil)
	if sv == nil || sv.RenewalDue {
		t.Fatalf("a room expiring ~20h out must be settled, not renewal-due, got %+v", sv)
	}

	dueSubj := &sim.ActorSnapshot{RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 8*time.Hour),
	}}
	dv := buildLodgingView(lodgingSnap(dueSubj, structs), "ezekiel", dueSubj, nil)
	if dv == nil || !dv.RenewalDue {
		t.Fatalf("a room expiring within the final-night window (8h) must be renewal-due, got %+v", dv)
	}

	// The settled render must confirm the room without inviting a re-buy.
	var b strings.Builder
	renderLodging(&b, sv)
	out := b.String()
	if strings.Contains(out, "renew") || strings.Contains(out, "Renewing is") {
		t.Errorf("settled lodger render must not nudge renewal, got %q", out)
	}
	if !strings.Contains(out, "settled here") {
		t.Errorf("settled lodger render must confirm the room, got %q", out)
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

// TestRenderKeeperLodging_SellMechanic covers the how-to-let-a-room cue: a
// keeper with vacancy and a rate is told the nights_stay sell call, so a guest
// who asks for a room but is neither a homeless seeker (renderLodgingOffer) nor
// an existing resident (renderKeeperHeldLodgers) can still be sold one. Without
// it the weak keeper model invents item_kind "room" and the sale fails.
func TestRenderKeeperLodging_SellMechanic(t *testing.T) {
	var b strings.Builder
	renderKeeperLodging(&b, &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 2, RoomsTotal: 3, NightlyRate: 4})
	out := b.String()
	for _, want := range []string{"If a guest asks to lodge", "nights_stay", "consume_now false"} {
		if !strings.Contains(out, want) {
			t.Errorf("keeper with a free room must spell out the nights_stay sell mechanic; missing %q in %q", want, out)
		}
	}

	// No vacancy: nothing to sell, so no sell mechanic.
	b.Reset()
	renderKeeperLodging(&b, &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 0, RoomsTotal: 3, NightlyRate: 4})
	if strings.Contains(b.String(), "nights_stay") {
		t.Errorf("full inn must not show the sell mechanic, got %q", b.String())
	}

	// No rate (lodging pricing disabled): can't price a room, so no sell mechanic.
	b.Reset()
	renderKeeperLodging(&b, &KeeperLodgingView{InnName: "Hannah's Inn", RoomsAvailable: 2, RoomsTotal: 3, NightlyRate: 0})
	if strings.Contains(b.String(), "nights_stay") {
		t.Errorf("disabled rate must not show the sell mechanic, got %q", b.String())
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
	if v := buildKeeperLodgingView(lodgingSnap(subj, nil), subj, nil); v != nil {
		t.Errorf("want nil for a non-keeper, got %+v", v)
	}
}

func TestBuildKeeperLodgingView_WorkStructureHasNoPrivateRooms_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{WorkStructureID: "smithy"}
	structs := map[sim.StructureID]*sim.Structure{
		"smithy": {ID: "smithy", DisplayName: "The Smithy", Rooms: []*sim.Room{{ID: 1, StructureID: "smithy", Kind: sim.RoomKindCommon}}},
	}
	if v := buildKeeperLodgingView(lodgingSnap(subj, structs), subj, nil); v != nil {
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
	// lodgerA ("other0") is an awake huddle peer, satisfying the LLM-22 audience gate.
	members := []HuddleMember{{ID: "other0"}}
	v := buildKeeperLodgingView(lodgingSnap(keeper, structs, lodgerA, lodgerB), keeper, members)
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
	members := []HuddleMember{{ID: "other0"}} // awake huddle peer (LLM-22 gate)
	v := buildKeeperLodgingView(lodgingSnap(keeper, structs, lodger), keeper, members)
	if v == nil {
		t.Fatal("want a keeper view, got nil")
	}
	if v.RoomsAvailable != 2 {
		t.Errorf("RoomsAvailable = %d, want 2 (expired + foreign-room grants ignored)", v.RoomsAvailable)
	}
}

// --- keeper audience gate (LLM-22) ---

func TestBuildKeeperLodgingView_NoAwakePeer_Nil(t *testing.T) {
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 2)}
	// An innkeeper alone in his inn has no one to be cued to pitch a room to.
	if v := buildKeeperLodgingView(lodgingSnap(keeper, structs), keeper, nil); v != nil {
		t.Errorf("want nil for a keeper with no awake huddle peer, got %+v", v)
	}
}

func TestBuildKeeperLodgingView_OnlySleepingPeer_Nil(t *testing.T) {
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 2)}
	// The only huddle peer ("other0") is asleep — the live John-Ellis-pitches-a-
	// sleeping-Ezekiel bug. A sleeper stays in the huddle roster but is no audience.
	sleeper := &sim.ActorSnapshot{State: sim.StateSleeping}
	members := []HuddleMember{{ID: "other0"}}
	if v := buildKeeperLodgingView(lodgingSnap(keeper, structs, sleeper), keeper, members); v != nil {
		t.Errorf("want nil when the only huddle peer is asleep, got %+v", v)
	}
}

func TestBuildKeeperLodgingView_AwakePeer_View(t *testing.T) {
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 2)}
	// An awake bystander ("other0", State unset) is present — the section renders.
	bystander := &sim.ActorSnapshot{}
	members := []HuddleMember{{ID: "other0"}}
	v := buildKeeperLodgingView(lodgingSnap(keeper, structs, bystander), keeper, members)
	if v == nil {
		t.Fatal("want a keeper view when an awake huddle peer is present, got nil")
	}
	if v.RoomsTotal != 2 || v.RoomsAvailable != 2 {
		t.Errorf("occupancy = %d/%d, want 2/2", v.RoomsAvailable, v.RoomsTotal)
	}
}

// A huddle member whose ID is absent from the snapshot (stale membership) is
// not an audience — fail closed, same as an all-asleep room. LLM-22.
func TestBuildKeeperLodgingView_MissingHuddleMember_Nil(t *testing.T) {
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 2)}
	members := []HuddleMember{{ID: "missing"}}
	if v := buildKeeperLodgingView(lodgingSnap(keeper, structs), keeper, members); v != nil {
		t.Errorf("want nil when the huddle member is absent from the snapshot, got %+v", v)
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

// TestBuild_LodgingOfferCue_GatedOnAtOwnStructure (ZBBS-HOME-424): the room-
// to-let cue fires only while the keeper is physically at the structure
// whose rooms they keep — a keeper who is a guest in someone else's
// establishment must not be steered to sell rooms into that huddle (observed
// live: Hannah pitching her Inn's rooms from inside John's Tavern). The
// informational "## Your inn" keeper view stays location-independent.
func TestBuild_LodgingOfferCue_GatedOnAtOwnStructure(t *testing.T) {
	newSnap := func(inside sim.StructureID) *sim.Snapshot {
		keeper := &sim.ActorSnapshot{
			DisplayName:       "Hannah Boggs",
			Kind:              sim.KindNPCShared,
			CurrentHuddleID:   "h1",
			WorkStructureID:   "inn",
			InsideStructureID: inside,
			Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}},
		}
		// No home, no room access → a structural lodging seeker.
		seeker := &sim.ActorSnapshot{
			DisplayName:     "Ezekiel Crane",
			Kind:            sim.KindNPCStateful,
			CurrentHuddleID: "h1",
		}
		snap := &sim.Snapshot{
			PublishedAt: lodgingNow,
			Actors:      map[sim.ActorID]*sim.ActorSnapshot{"hannah": keeper, "ezekiel": seeker},
			Huddles: map[sim.HuddleID]*sim.Huddle{
				"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "ezekiel": {}}},
			},
			Structures: map[sim.StructureID]*sim.Structure{
				"inn": innStructureN("inn", "Hannah's Inn", 2),
			},
		}
		snap.LodgingDefaultWeeklyRate = 28 // nightly 4
		return snap
	}

	off := Build(newSnap("tavern"), "hannah", nil)
	if off.LodgingOffer != nil {
		t.Errorf("keeper away from her inn must not get the room-to-let cue, got %+v", off.LodgingOffer)
	}
	if off.KeeperLodging == nil {
		t.Error("the informational keeper view should stay, location-independent")
	}

	on := Build(newSnap("inn"), "hannah", nil)
	if on.LodgingOffer == nil {
		t.Fatal("keeper at her inn with a co-present seeker should get the room-to-let cue")
	}
	if len(on.LodgingOffer.SeekerNames) != 1 || on.LodgingOffer.SeekerNames[0] != "Ezekiel Crane" {
		t.Errorf("SeekerNames = %v, want [Ezekiel Crane]", on.LodgingOffer.SeekerNames)
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
	for _, want := range []string{"## A room to let", "Ezekiel Crane", "nights_stay", "call sell", "number of nights", "consume_now false", "target_buyer only if you know"} {
		if !strings.Contains(out, want) {
			t.Errorf("offer cue missing %q, got %q", want, out)
		}
	}
}

// --- keeper held-lodger signal (LLM-38) ---

// heldLodgerSnap builds a snapshot with a keeper working at "inn" and one
// co-present member holding memberAccess (may be nil). Actor ids are explicit so
// buildKeeperHeldLodgers can resolve the keeper's WorkStructureID and the
// member's RoomAccess by the ids the members slice references.
func heldLodgerSnap(memberAccess map[sim.RoomAccessKey]*sim.RoomAccess) *sim.Snapshot {
	return &sim.Snapshot{
		PublishedAt: lodgingNow,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"keeper": {WorkStructureID: "inn"},
			"guest":  {RoomAccess: memberAccess},
		},
		Structures: map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 3)},
		// 13h renewal window (LLM-96); see lodgingSnap.
		LodgingBedtimeMinute:  22 * 60,
		LodgingCheckOutMinute: 11 * 60,
	}
}

var heldLodgerKeeperView = &KeeperLodgingView{InnName: "Hannah's Inn", RoomsTotal: 3, RoomsAvailable: 2}
var heldLodgerMembers = []HuddleMember{{ID: "guest", DisplayName: "Goodman Jefferey", Acquainted: true}}

func TestBuildKeeperHeldLodgers_CoPresentHeldLodger(t *testing.T) {
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
	})
	v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperView, heldLodgerMembers)
	if v == nil {
		t.Fatal("want a held-lodger view for a co-present guest holding a grant here, got nil")
	}
	if len(v.Lodgers) != 1 || v.Lodgers[0].Name != "Goodman Jefferey" {
		t.Fatalf("Lodgers = %+v, want one named Goodman Jefferey", v.Lodgers)
	}
	// Tenure is precomputed against snap.PublishedAt (lodgingNow), so a +72h grant
	// is deterministically "about 3 more nights".
	if v.Lodgers[0].TenureLabel != "paid for about 3 more nights" {
		t.Errorf("TenureLabel = %q, want 'paid for about 3 more nights'", v.Lodgers[0].TenureLabel)
	}
}

// TestBuildKeeperHeldLodgers_RenewalDueFinalNight is the keeper side of the
// LLM-96 fix: a guest with plenty of stay left is "settled" (the keeper must not
// re-offer), and only once the guest is into its final night before checkout
// does the cue flip to a renewal offer.
func TestBuildKeeperHeldLodgers_RenewalDueFinalNight(t *testing.T) {
	keeperView := &KeeperLodgingView{InnName: "Hannah's Inn", RoomsTotal: 3, RoomsAvailable: 2, NightlyRate: 4}

	settled := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
	})
	sv := buildKeeperHeldLodgers(settled, "keeper", keeperView, heldLodgerMembers)
	if sv == nil || sv.Lodgers[0].RenewalDue {
		t.Fatalf("a guest with 72h left must be settled, not renewal-due, got %+v", sv)
	}

	due := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 8*time.Hour),
	})
	dv := buildKeeperHeldLodgers(due, "keeper", keeperView, heldLodgerMembers)
	if dv == nil || !dv.Lodgers[0].RenewalDue || !dv.Lodgers[0].OfferRenewal {
		t.Fatalf("a guest into its final night must be renewal-due with an offer, got %+v", dv)
	}
}

func TestBuildKeeperHeldLodgers_NoGrant_Nil(t *testing.T) {
	snap := heldLodgerSnap(nil) // the co-present guest holds no grant
	if v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperView, heldLodgerMembers); v != nil {
		t.Errorf("a guest with no grant is a seeker, not a held lodger — want nil, got %+v", v)
	}
}

func TestBuildKeeperHeldLodgers_GrantAtAnotherInn_Nil(t *testing.T) {
	// Room 50 belongs to no room in "inn" (private rooms are 2..4), so the grant
	// is at another inn and must not register as lodging here.
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 50, Source: sim.AccessSourceLedger}: ledgerAccess(50, 72*time.Hour),
	})
	if v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperView, heldLodgerMembers); v != nil {
		t.Errorf("a grant at a foreign inn must not count — want nil, got %+v", v)
	}
}

func TestBuildKeeperHeldLodgers_ExpiredGrant_Nil(t *testing.T) {
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, -time.Hour),
	})
	if v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperView, heldLodgerMembers); v != nil {
		t.Errorf("an expired grant is not active lodging — want nil, got %+v", v)
	}
}

func TestBuildKeeperHeldLodgers_NonKeeper_Nil(t *testing.T) {
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
	})
	if v := buildKeeperHeldLodgers(snap, "keeper", nil, heldLodgerMembers); v != nil {
		t.Errorf("a non-keeper (nil KeeperLodgingView) must not surface lodgers — want nil, got %+v", v)
	}
}

func TestRenderKeeperHeldLodgers_Gated(t *testing.T) {
	var b strings.Builder
	renderKeeperHeldLodgers(&b, nil)
	if b.String() != "" {
		t.Errorf("nil view must render nothing, got %q", b.String())
	}
	b.Reset()
	renderKeeperHeldLodgers(&b, &KeeperHeldLodgersView{Lodgers: []HeldLodger{
		{Name: "Goodman Jefferey", TenureLabel: "paid for about 3 more nights"},
	}})
	out := b.String()
	for _, want := range []string{"## Already lodging here", "Goodman Jefferey", "paid for about 3 more nights", "Do not offer another", "already settled"} {
		if !strings.Contains(out, want) {
			t.Errorf("held-lodger render missing %q, got %q", want, out)
		}
	}
}

func TestHeldLodgerTenure_Tiers(t *testing.T) {
	now := lodgingNow
	cases := []struct {
		d    time.Duration
		want string
	}{
		{72 * time.Hour, "paid for about 3 more nights"},
		{30 * time.Hour, "paid through tomorrow"},
		{2 * time.Hour, "paid through the day"},
	}
	for _, c := range cases {
		if got := heldLodgerTenure(now.Add(c.d), now); got != c.want {
			t.Errorf("heldLodgerTenure(+%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// --- shared bedtime fixtures (originally the LLM-36 retire cue; the cue itself
// is gone, superseded by turn_in — see turn_in_gate_test.go, which uses these) ---

// retireSnap builds a snapshot carrying the village clock + night-window fields
// a bedtime cue reads: dawn 07:00, bedtime 22:00, and a caller-supplied local
// minute-of-day. Subject keyed "ezekiel" like lodgingSnap.
func retireSnap(subj *sim.ActorSnapshot, localMin int, structures map[sim.StructureID]*sim.Structure) *sim.Snapshot {
	s := lodgingSnap(subj, structures)
	s.LocalMinuteOfDay = &localMin
	s.DawnMinute = 7 * 60
	s.DuskMinute = 19 * 60
	s.DawnDuskMinuteOK = true
	s.LodgingBedtimeMinute = 22 * 60
	return s
}

// retireLodger is an awake boarder standing inside the inn it rents (room 2 of
// innStructure), holding a grant that expires well in the future.
func retireLodger() *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		InsideStructureID: "inn",
		State:             sim.StateIdle,
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
		},
	}
}

func retireStructs() map[sim.StructureID]*sim.Structure {
	return map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")}
}

// retireMembers is a single co-present huddle companion — the audience the cue
// requires (members excludes the subject).
func retireMembers() []HuddleMember { return []HuddleMember{{ID: "companion"}} }

// --- keeper renewal-due flip (LLM-46) ---

// heldLodgerKeeperRated mirrors heldLodgerKeeperView but with a live nightly
// rate, so the renewal-due branch (which can't price a renewal without one) can
// fire.
var heldLodgerKeeperRated = &KeeperLodgingView{InnName: "Hannah's Inn", RoomsTotal: 3, RoomsAvailable: 2, NightlyRate: 4}

func TestBuildKeeperHeldLodgers_RenewalDue_Offers(t *testing.T) {
	// A grant 12h from expiry is inside the 48h renewal window.
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 12*time.Hour),
	})
	v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperRated, heldLodgerMembers)
	if v == nil || len(v.Lodgers) != 1 {
		t.Fatalf("want one held lodger, got %+v", v)
	}
	if !v.Lodgers[0].RenewalDue || !v.Lodgers[0].OfferRenewal {
		t.Errorf("a grant in the renewal window with a rate should offer a renewal; got RenewalDue=%v OfferRenewal=%v",
			v.Lodgers[0].RenewalDue, v.Lodgers[0].OfferRenewal)
	}
	if v.NightlyRate != 4 {
		t.Errorf("view NightlyRate = %d, want 4 (so the render can price the quote)", v.NightlyRate)
	}
}

func TestBuildKeeperHeldLodgers_RenewalDue_NoRate_Settled(t *testing.T) {
	// In the window, but heldLodgerKeeperView has no nightly rate (0) — a renewal
	// can't be priced, so fall back to the settled affirm.
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 12*time.Hour),
	})
	v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperView, heldLodgerMembers)
	if v == nil || len(v.Lodgers) != 1 {
		t.Fatalf("want one held lodger, got %+v", v)
	}
	if v.Lodgers[0].RenewalDue || v.Lodgers[0].OfferRenewal {
		t.Errorf("no nightly rate → no renewal offer; got RenewalDue=%v OfferRenewal=%v",
			v.Lodgers[0].RenewalDue, v.Lodgers[0].OfferRenewal)
	}
}

func TestBuildKeeperHeldLodgers_OutsideWindow_Settled(t *testing.T) {
	// 72h out is beyond the 48h window — still settled, no renewal pressure.
	snap := heldLodgerSnap(map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
	})
	v := buildKeeperHeldLodgers(snap, "keeper", heldLodgerKeeperRated, heldLodgerMembers)
	if v == nil || len(v.Lodgers) != 1 {
		t.Fatalf("want one held lodger, got %+v", v)
	}
	if v.Lodgers[0].RenewalDue {
		t.Errorf("a 72h grant is outside the 48h renewal window — want settled, got RenewalDue=true")
	}
}

func TestRenderKeeperHeldLodgers_RenewalOffer(t *testing.T) {
	var b strings.Builder
	renderKeeperHeldLodgers(&b, &KeeperHeldLodgersView{
		NightlyRate: 4,
		Lodgers:     []HeldLodger{{Name: "Ezekiel Crane", TenureLabel: "paid through the day", RenewalDue: true, OfferRenewal: true}},
	})
	out := b.String()
	for _, want := range []string{"## Already lodging here", "Ezekiel Crane", "stay is ending", "call sell", "nights_stay", "4 coins", "target_buyer"} {
		if !strings.Contains(out, want) {
			t.Errorf("renewal-offer render missing %q, got %q", want, out)
		}
	}
	if strings.Contains(out, "Do not offer another") {
		t.Errorf("a renewal-due guest must not get the 'do not offer' steer; got %q", out)
	}
}

func TestRenderKeeperHeldLodgers_RenewalAwait(t *testing.T) {
	var b strings.Builder
	renderKeeperHeldLodgers(&b, &KeeperHeldLodgersView{
		NightlyRate: 4,
		Lodgers:     []HeldLodger{{Name: "Ezekiel Crane", TenureLabel: "paid through the day", RenewalDue: true, OfferRenewal: false}},
	})
	out := b.String()
	if !strings.Contains(out, "Await their answer") {
		t.Errorf("a renewal in flight should steer to await; got %q", out)
	}
	for _, notWant := range []string{"call sell", "Do not offer another"} {
		if strings.Contains(out, notWant) {
			t.Errorf("the await render should not contain %q; got %q", notWant, out)
		}
	}
}
