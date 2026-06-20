package sim

import (
	"testing"
	"time"
)

// lodging_renewal_internal_test.go — LLM-47. advancePastHeldLodging is the shared
// helper behind the accept-time renewal advance (PayWithItem) and the deliver-time
// backstop (transferOrderGoods). "Held" is read from the buyer's durable
// RoomAccess grants (NOT w.Orders, which drops delivered lodging orders at the
// prune + the restart-load filter), scoped to the seller's own private rooms; the
// helper advances a same-night booking to the first un-held night so a renewal
// can't mint a second delivered nights_stay for a held night and wedge
// checkpointing (the Ezekiel↔John incident, 2026-06-19).

func ledgerGrant(roomID RoomID, expiresAt time.Time, active bool) *RoomAccess {
	exp := expiresAt
	return &RoomAccess{RoomID: roomID, Source: AccessSourceLedger, LedgerID: 1, ExpiresAt: &exp, Active: active}
}

// grantWorld builds John (seller at "inn", which has private room 2) and Ezekiel
// (buyer) holding the given grants.
func grantWorld(grants ...*RoomAccess) *World {
	roomAccess := make(map[RoomAccessKey]*RoomAccess)
	for _, g := range grants {
		roomAccess[RoomAccessKey{RoomID: g.RoomID, Source: g.Source}] = g
	}
	return &World{
		Actors: map[ActorID]*Actor{
			"john":    {ID: "john", WorkStructureID: "inn"},
			"ezekiel": {ID: "ezekiel", RoomAccess: roomAccess},
		},
		Structures: map[StructureID]*Structure{
			"inn": {ID: "inn", Rooms: []*Room{
				{ID: 1, StructureID: "inn", Kind: RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_1"},
				{ID: 3, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_2"},
			}},
		},
	}
}

func renewalYMD(t time.Time) string { return t.UTC().Format("2006-01-02") }

func night(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// The renewal case: the buyer holds tonight (grant checks out tomorrow afternoon),
// so a same-night booking advances to the next night.
func TestAdvancePastHeldLodging_RenewSameNightBumpsToNext(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	checkout := time.Date(2026, 6, 20, 15, 0, 0, 0, time.UTC) // grant through the night of 06-19
	w := grantWorld(ledgerGrant(2, checkout, true))
	if got := advancePastHeldLodging(w, "ezekiel", "john", night(2026, 6, 19), now); renewalYMD(got) != "2026-06-20" {
		t.Errorf("renew of a held night = %s, want 2026-06-20", renewalYMD(got))
	}
}

// No grant → the requested night is unchanged.
func TestAdvancePastHeldLodging_NoGrantUnchanged(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	req := night(2026, 6, 19)
	if got := advancePastHeldLodging(grantWorld(), "ezekiel", "john", req, now); !got.Equal(req) {
		t.Errorf("no grant = %s, want unchanged 2026-06-19", renewalYMD(got))
	}
}

// A grant at a room that is NOT one of this seller's private rooms (another inn)
// does not count.
func TestAdvancePastHeldLodging_ForeignRoomIgnored(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	checkout := time.Date(2026, 6, 20, 15, 0, 0, 0, time.UTC)
	req := night(2026, 6, 19)
	w := grantWorld(ledgerGrant(99, checkout, true)) // room 99 isn't in "inn"
	if got := advancePastHeldLodging(w, "ezekiel", "john", req, now); !got.Equal(req) {
		t.Errorf("foreign-room grant shouldn't bump: got %s, want 2026-06-19", renewalYMD(got))
	}
}

// An inactive grant, or one already expired relative to now, does not count.
func TestAdvancePastHeldLodging_InactiveOrExpiredIgnored(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	checkout := time.Date(2026, 6, 20, 15, 0, 0, 0, time.UTC)
	req := night(2026, 6, 19)
	inactive := grantWorld(ledgerGrant(2, checkout, false))
	if got := advancePastHeldLodging(inactive, "ezekiel", "john", req, now); !got.Equal(req) {
		t.Errorf("inactive grant shouldn't bump: got %s", renewalYMD(got))
	}
	expired := grantWorld(ledgerGrant(2, time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC), true))
	if got := advancePastHeldLodging(expired, "ezekiel", "john", req, now); !got.Equal(req) {
		t.Errorf("expired grant shouldn't bump: got %s", renewalYMD(got))
	}
}

// A multi-night grant (checks out 06-22) pushes a renew of 06-19 to 06-22.
func TestAdvancePastHeldLodging_MultiNightGrant(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	w := grantWorld(ledgerGrant(2, time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC), true))
	if got := advancePastHeldLodging(w, "ezekiel", "john", night(2026, 6, 19), now); renewalYMD(got) != "2026-06-22" {
		t.Errorf("multi-night renew = %s, want 2026-06-22", renewalYMD(got))
	}
}

// An explicit future booking past the held coverage is NOT pushed further out.
func TestAdvancePastHeldLodging_ExplicitFuturePreserved(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	checkout := time.Date(2026, 6, 20, 15, 0, 0, 0, time.UTC) // covers through 06-19
	w := grantWorld(ledgerGrant(2, checkout, true))
	jun25 := night(2026, 6, 25)
	if got := advancePastHeldLodging(w, "ezekiel", "john", jun25, now); !got.Equal(jun25) {
		t.Errorf("explicit future booking moved: got %s, want 2026-06-25", renewalYMD(got))
	}
}

// The latest ExpiresAt wins across multiple grants at this seller.
func TestAdvancePastHeldLodging_LatestGrantWins(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	w := grantWorld(
		ledgerGrant(2, time.Date(2026, 6, 20, 15, 0, 0, 0, time.UTC), true),
		ledgerGrant(3, time.Date(2026, 6, 23, 15, 0, 0, 0, time.UTC), true), // both private rooms at this inn
	)
	if got := advancePastHeldLodging(w, "ezekiel", "john", night(2026, 6, 19), now); renewalYMD(got) != "2026-06-23" {
		t.Errorf("latest grant should win: got %s, want 2026-06-23", renewalYMD(got))
	}
}

// The checkout instant is mapped to a calendar date through w.Settings.Location, so
// a grant whose UTC date differs from its local date resolves to the LOCAL checkout
// date (materialized as UTC midnight). FixedZone keeps this off the host tz db.
func TestAdvancePastHeldLodging_NonUTCLocation(t *testing.T) {
	loc := time.FixedZone("test-edt", -4*3600) // UTC-4
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// 2026-06-20 02:00 UTC == 2026-06-19 22:00 local → the local checkout date is 06-19.
	checkout := time.Date(2026, 6, 20, 2, 0, 0, 0, time.UTC)
	w := grantWorld(ledgerGrant(2, checkout, true))
	w.Settings.Location = loc
	if got := advancePastHeldLodging(w, "ezekiel", "john", night(2026, 6, 18), now); renewalYMD(got) != "2026-06-19" {
		t.Errorf("non-UTC checkout date = %s, want 2026-06-19 (local date, not the 06-20 UTC date)", renewalYMD(got))
	}
}
