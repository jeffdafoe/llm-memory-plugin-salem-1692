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
// ledger RoomAccess with a future ExpiresAt (see sim.hasActiveLedgerAccess,
// the canonical "is a lodger" predicate). The keeper-side occupancy section
// and the affordability cue (which needs the rent-rate setting) land in
// later slices on this same file.

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
}

// buildLodgingView returns the lodging view for actorSnap, or nil when the
// actor holds no active ledger RoomAccess (i.e. isn't a lodger). Pure over
// the snapshot. The gate mirrors sim.hasActiveLedgerAccess (active, ledger
// source, future ExpiresAt) but also selects the soonest-expiring grant so
// the rendered cue points at the most urgent renewal. ZBBS-HOME-296 PR2.
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
	return &LodgingView{InnName: innName, ExpiresAt: *best.ExpiresAt}
}

// KeeperLodgingView is the content-gated "## Your inn" section shown to an
// actor who keeps a lodging structure. nil means the subject doesn't keep an
// inn and render omits the section.
type KeeperLodgingView struct {
	InnName        string
	RoomsAvailable int
	RoomsTotal     int
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
	return &KeeperLodgingView{InnName: innLabel(s), RoomsAvailable: available, RoomsTotal: total}
}

// structureForRoom returns the structure that contains roomID, or nil when
// no structure declares it. Linear over the snapshot's structures/rooms —
// fine at Salem's scale (mirrors sim.findRoom, which works on the live world).
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

// renderLodging writes the "## Your lodging" section. Content-gated: a nil
// view writes nothing. The renewal tier is computed against time.Now() — the
// same wall-clock posture renderPendingDeliveriesToMe uses for order expiry
// (Render has no snapshot, so it can't read Snapshot.PublishedAt here).
func renderLodging(b *strings.Builder, v *LodgingView) {
	if v == nil {
		return
	}
	b.WriteString("## Your lodging\n")
	b.WriteString(lodgingStatusLine(v.InnName, v.ExpiresAt, time.Now()))
	b.WriteString("\n\n")
}

// renderKeeperLodging writes the "## Your inn" section for an inn-keeper.
// Content-gated: a nil view writes nothing.
func renderKeeperLodging(b *strings.Builder, v *KeeperLodgingView) {
	if v == nil {
		return
	}
	b.WriteString("## Your inn\n")
	fmt.Fprintf(b, "%d of %d rooms available tonight at %s.\n\n",
		v.RoomsAvailable, v.RoomsTotal, sanitizeInline(v.InnName))
}
