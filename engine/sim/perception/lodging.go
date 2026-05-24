package perception

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// lodging.go — ZBBS-HOME-296 PR2. The lodger-side "## Your lodging"
// perception section: tells an NPC who is renting a room which inn it's at
// and how close the grant is to expiring, so the LLM can renew with the
// keeper before it lapses. Pure over the published Snapshot — it reads
// ActorSnapshot.RoomAccess (carried on the snapshot for exactly this) and
// resolves the inn name via Snapshot.Structures.
//
// The grant the section describes IS the lodger relationship: an active
// ledger RoomAccess with a future ExpiresAt (see sim.IsActiveLedgerGrant,
// the canonical per-grant lodging predicate). This file also carries the
// keeper-side occupancy section and, near renewal, the affordability cue —
// the lever (HOME-296 §6) that makes a broke lodger perceive a rent shortfall
// in time to act, before the engine-auto rebook backstop fires at 6h.

// LodgingView is the content-gated "## Your lodging" section. nil means the
// actor holds no active lodging grant and render omits the section.
type LodgingView struct {
	// InnName is the display name of the structure the rented room is in
	// ("Hannah's Inn"), or a generic fallback when the structure has no name.
	InnName string

	// ExpiresAt is the soonest-expiring active ledger grant's expiry instant.
	// When an actor somehow holds more than one active lodging grant, the
	// nearest expiry is surfaced — that's the one the lodger must act on first.
	ExpiresAt time.Time

	// NightlyRate is the per-night rent the keeper advertises
	// (sim.LodgingNightlyRate of the world's weekly rate); 0 when the lodging
	// rate is unset/disabled, which suppresses both the rate hint and the
	// affordability cue.
	NightlyRate int

	// Coins is the lodger's coin balance at snapshot time — the affordability
	// cue compares it against NightlyRate to decide whether to warn of a
	// shortfall.
	Coins int
}

// buildLodgingView returns the lodging view for actorSnap, or nil when the
// actor holds no active ledger RoomAccess (i.e. isn't a lodger). Pure over
// the snapshot. The gate is sim.IsActiveLedgerGrant (active, ledger source,
// future ExpiresAt); it also selects the soonest-expiring grant so the
// rendered cue points at the most urgent renewal. ZBBS-HOME-296 PR2.
func buildLodgingView(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) *LodgingView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	now := snap.PublishedAt

	var best *sim.RoomAccess
	for _, ra := range actorSnap.RoomAccess {
		if !sim.IsActiveLedgerGrant(ra, now) {
			continue
		}
		if best == nil || ra.ExpiresAt.Before(*best.ExpiresAt) {
			best = ra
		}
	}
	if best == nil {
		return nil
	}

	innName := "the inn"
	if s := structureForRoom(snap, best.RoomID); s != nil {
		innName = innLabel(s) // shared with the recovery-options inn finder
	}
	return &LodgingView{
		InnName:     innName,
		ExpiresAt:   *best.ExpiresAt,
		NightlyRate: sim.LodgingNightlyRate(snap.LodgingDefaultWeeklyRate),
		Coins:       actorSnap.Coins,
	}
}

// KeeperLodgingView is the content-gated "## Your inn" section shown to an
// actor who keeps a lodging structure. nil means the subject doesn't keep an
// inn and render omits the section.
type KeeperLodgingView struct {
	InnName        string
	RoomsAvailable int
	RoomsTotal     int

	// NightlyRate is the per-night rent the keeper quotes; surfaced only when
	// a room is free to sell. 0 when the lodging rate is unset/disabled.
	NightlyRate int
}

// buildKeeperLodgingView returns the keeper occupancy view when actorSnap
// works at a structure that has private bedrooms (an inn), or nil otherwise.
// RoomsAvailable = private rooms in the structure minus the distinct rooms
// currently held by an active ledger grant (any actor's). Pure over the
// snapshot. ZBBS-HOME-296 PR2.
func buildKeeperLodgingView(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) *KeeperLodgingView {
	if snap == nil || actorSnap == nil || actorSnap.WorkStructureID == "" {
		return nil
	}
	s := snap.Structures[actorSnap.WorkStructureID]
	if s == nil {
		return nil
	}

	privateRooms := make(map[sim.RoomID]bool)
	for _, r := range s.Rooms {
		if r != nil && r.Kind == sim.RoomKindPrivate {
			privateRooms[r.ID] = true
		}
	}
	total := len(privateRooms)
	if total == 0 {
		return nil // not a lodging structure — no keeper section
	}

	now := snap.PublishedAt
	occupied := make(map[sim.RoomID]bool)
	for _, other := range snap.Actors {
		if other == nil {
			continue
		}
		for _, ra := range other.RoomAccess {
			if sim.IsActiveLedgerGrant(ra, now) && privateRooms[ra.RoomID] {
				occupied[ra.RoomID] = true
			}
		}
	}

	available := total - len(occupied)
	if available < 0 {
		available = 0
	}
	return &KeeperLodgingView{
		InnName:        innLabel(s),
		RoomsAvailable: available,
		RoomsTotal:     total,
		NightlyRate:    sim.LodgingNightlyRate(snap.LodgingDefaultWeeklyRate),
	}
}

// structureForRoom returns the structure that contains roomID, or nil when
// no structure declares it. Linear over the snapshot's structures/rooms —
// fine at Salem's scale (mirrors sim.findRoom, which works on the live world).
// RoomID is a globally unique per-instance id (legacy BIGSERIAL — see
// sim.RoomID), so at most one structure declares a given room and the
// first match is unambiguous despite the map-iteration order.
func structureForRoom(snap *sim.Snapshot, roomID sim.RoomID) *sim.Structure {
	for _, s := range snap.Structures {
		if s == nil {
			continue
		}
		for _, r := range s.Rooms {
			if r != nil && r.ID == roomID {
				return s
			}
		}
	}
	return nil
}

// lodgingStatusLine renders the escalating renewal cue from the time left on
// the grant. Three tiers (calm → soon → urgent), driven by duration so no
// timezone is needed: paid-for-nights, expires-in-about-a-day, expires-today.
// Pure; `now` is a parameter so callers control the clock for tests.
func lodgingStatusLine(innName string, expiresAt, now time.Time) string {
	inn := sanitizeInline(innName)
	d := expiresAt.Sub(now)
	switch {
	case d <= 24*time.Hour:
		return fmt.Sprintf("Your room at %s expires within the day — see the keeper before sundown to renew.", inn)
	case d <= 48*time.Hour:
		return fmt.Sprintf("Your room at %s expires in about a day — see the keeper soon to renew.", inn)
	default:
		nights := int(d / (24 * time.Hour))
		return fmt.Sprintf("Your room at %s is paid for about %d more nights.", inn, nights)
	}
}

// lodgingAffordabilityCue returns the rent-shortfall warning, or "" when it
// shouldn't fire. The lever of HOME-296 §6: it only fires inside the renewal
// window (<= 48h to expiry, while there's still runway to earn before the 6h
// engine-auto backstop) and only when the lodger can't cover a night
// (Coins < NightlyRate). Suppressed entirely when the rate is disabled. Pure;
// `now` is a parameter for tests.
func lodgingAffordabilityCue(v *LodgingView, now time.Time) string {
	if v.NightlyRate <= 0 {
		return ""
	}
	remaining := v.ExpiresAt.Sub(now)
	// Fire only inside the renewal window: > 0 (an expired-but-unswept grant
	// has negative remaining — don't warn "before your room lapses" after it
	// already lapsed) and within 48h (runway before the 6h backstop). The <=0
	// guard matters because render uses time.Now() while the build gate used
	// the snapshot clock — staleness can briefly push remaining negative.
	if remaining <= 0 || remaining > 48*time.Hour {
		return ""
	}
	if v.Coins >= v.NightlyRate {
		return ""
	}
	return fmt.Sprintf("You have only %d coins — short of the %d for another night. Earn or sell something before your room lapses.",
		v.Coins, v.NightlyRate)
}

// renderLodging writes the "## Your lodging" section. Content-gated: a nil
// view writes nothing. The renewal tier and affordability cue are computed
// against time.Now() — the same wall-clock posture renderPendingDeliveriesToMe
// uses for order expiry (Render has no snapshot, so it can't read
// Snapshot.PublishedAt here).
func renderLodging(b *strings.Builder, v *LodgingView) {
	if v == nil {
		return
	}
	now := time.Now()
	b.WriteString("## Your lodging\n")
	b.WriteString(lodgingStatusLine(v.InnName, v.ExpiresAt, now))
	if v.NightlyRate > 0 {
		fmt.Fprintf(b, " Renewing is %d coins a night.", v.NightlyRate)
	}
	b.WriteString("\n")
	if cue := lodgingAffordabilityCue(v, now); cue != "" {
		b.WriteString(cue)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// renderKeeperLodging writes the "## Your inn" section for an inn-keeper.
// Content-gated: a nil view writes nothing. The nightly rate is appended only
// when a room is free to sell and the rate is set.
func renderKeeperLodging(b *strings.Builder, v *KeeperLodgingView) {
	if v == nil {
		return
	}
	b.WriteString("## Your inn\n")
	fmt.Fprintf(b, "%d of %d rooms available tonight at %s",
		v.RoomsAvailable, v.RoomsTotal, sanitizeInline(v.InnName))
	if v.RoomsAvailable > 0 && v.NightlyRate > 0 {
		fmt.Fprintf(b, ", %d coins a night", v.NightlyRate)
	}
	b.WriteString(".\n\n")
}
