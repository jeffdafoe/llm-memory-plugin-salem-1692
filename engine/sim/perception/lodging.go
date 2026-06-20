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

	// KeeperAsleep is true when the inn's keeper is asleep at snapshot time, so
	// the "see the keeper to renew" cue is unactionable right now. Render flags it
	// (within the renewal window) so the lodger waits rather than walking to a
	// sleeping keeper. ZBBS-WORK-416.
	KeeperAsleep bool
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
	keeperAsleep := false
	if s := structureForRoom(snap, best.RoomID); s != nil {
		innName = innLabel(s) // shared with the recovery-options inn finder
		keeperAsleep = vendorKeeperAsleep(snap, keeperOf(snap, s.ID))
	}
	return &LodgingView{
		InnName:      innName,
		ExpiresAt:    *best.ExpiresAt,
		NightlyRate:  sim.LodgingNightlyRate(snap.LodgingDefaultWeeklyRate),
		Coins:        actorSnap.Coins,
		KeeperAsleep: keeperAsleep,
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
// works at a structure that has private bedrooms (an inn) AND has an awake
// huddle peer present to hear about it, or nil otherwise. RoomsAvailable =
// private rooms in the structure minus the distinct rooms currently held by an
// active ledger grant (any actor's). Pure over the snapshot. ZBBS-HOME-296 PR2.
//
// The awake-peer gate is LLM-22: the "## Your inn" line is a standing,
// every-tick sell cue, and with no awake listener it drove keepers to re-pitch
// rooms into an empty or all-asleep huddle (John Ellis pitching a sleeping
// Ezekiel a room six times). Gating the whole view here also keeps the
// downstream "## A room to let" offer cue correct: that cue only fires on an
// awake co-present seeker, which is a strict subset of "an awake peer is here,"
// so suppressing the shared view when no one is awake never starves it.
func buildKeeperLodgingView(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *KeeperLodgingView {
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
	if !anyHuddlePeerAwake(snap, members) {
		return nil // no awake listener — don't cue a pitch into a dead room (LLM-22)
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

// LodgingOfferView is the seller-side "offer a room" cue (ZBBS-WORK-382). When
// a lodging keeper (works at a structure with free private bedrooms at a
// configured nightly rate) shares a huddle with a structural lodging-seeker —
// an actor with no home AND no active lodging grant, i.e. someone with nowhere
// to sleep — this surfaces that seeker by name alongside the scene_quote call
// for a nights_stay, so the keeper LLM proactively offers the room instead of
// narrating a stock greeting. It is the Finding-1 legibility idiom applied to
// lodging: scene_quote is the only seller path for a room, but the weak model
// never connects "asked about a room" to it (and scene_quote's own schema
// frames qty as per-consumer goods, which misleads for a nights booking). The
// decision stays with the model and the buyer keeps full accept/decline agency
// via pay_with_item. nil/empty skips the section.
type LodgingOfferView struct {
	// SeekerNames are the acquaintance-gated labels (descriptorLabel) of the
	// co-present lodging-seekers the keeper may offer a room to.
	SeekerNames []string

	// InnName is the keeper's inn display name (from KeeperLodgingView).
	InnName string

	// RoomsAvailable is the keeper's current vacancy (from KeeperLodgingView);
	// the cue only builds when this is > 0.
	RoomsAvailable int

	// NightlyRate is the per-night rent the keeper quotes; the cue only builds
	// when this is > 0 (a 0 rate means lodging pricing is disabled).
	NightlyRate int
}

// buildLodgingOfferCue builds the seller-side "offer a room" cue. It reuses the
// already-computed KeeperLodgingView (so the subject is confirmed a keeper with
// vacancy + a live nightly rate) and scans the keeper's co-present huddle for
// structural lodging-seekers — actors with no home and no active lodging grant.
// Mirrors buildOfferableCustomers' two storm guards so a stuck cue can't drive
// a re-offer loop: a seeker already mid-pay-offer with this keeper, or one the
// keeper already has a live quote out to, is dropped. Returns nil (Render
// content-gates) when the subject isn't a keeper-with-vacancy or no seeker is
// co-present. Pure over the snapshot. ZBBS-WORK-382.
func buildLodgingOfferCue(snap *sim.Snapshot, subject sim.ActorID, keeper *KeeperLodgingView, members []HuddleMember) *LodgingOfferView {
	if snap == nil || keeper == nil || keeper.RoomsAvailable <= 0 || keeper.NightlyRate <= 0 {
		return nil
	}
	now := snap.PublishedAt
	// members is already sorted by ID (buildSurroundings), so names is deterministic.
	var seekers []string
	for _, m := range members {
		if m.ID == subject {
			continue // never offer a room to yourself
		}
		if !actorSnapIsLodgingSeeker(snap.Actors[m.ID], now) {
			continue
		}
		if customerHasPendingOfferWithSeller(snap, m.ID, subject) {
			continue // already booking — renderPayOffers cues accept/decline/counter
		}
		if sellerHasActiveQuoteToBuyer(snap, subject, m.ID) {
			continue // already offered, awaiting the buyer — don't re-post every tick
		}
		seekers = append(seekers, descriptorLabel(m.DisplayName, m.Role, m.Acquainted))
	}
	if len(seekers) == 0 {
		return nil
	}
	return &LodgingOfferView{
		SeekerNames:    seekers,
		InnName:        keeper.InnName,
		RoomsAvailable: keeper.RoomsAvailable,
		NightlyRate:    keeper.NightlyRate,
	}
}

// actorSnapIsLodgingSeeker reports whether a snapshot actor has nowhere of its
// own to sleep — no HomeStructureID AND no active ledger RoomAccess. Such an
// actor beds nowhere via the auto-sleep machine (the home arm needs a home, the
// lodger arm needs a grant), so it is a candidate to be offered a room. Nil-safe
// (a missing snapshot is not a seeker). ZBBS-WORK-382.
func actorSnapIsLodgingSeeker(as *sim.ActorSnapshot, now time.Time) bool {
	if as == nil || as.HomeStructureID != "" {
		return false
	}
	for _, ra := range as.RoomAccess {
		if sim.IsActiveLedgerGrant(ra, now) {
			return false
		}
	}
	return true
}

// anyHuddlePeerAwake reports whether at least one of the subject's huddle peers
// is awake at snapshot time. members already excludes the subject
// (buildSurroundings), so any awake member is a co-present listener. Sleepers
// stay in the huddle roster (membership doesn't drop on bed-down), so the
// per-member State check is what separates an empty/all-asleep room from a live
// audience. StateSleeping is the snapshot-side asleep proxy (mirrors
// vendorKeeperAsleep); a resting/on-break peer still counts as a listener. LLM-22.
func anyHuddlePeerAwake(snap *sim.Snapshot, members []HuddleMember) bool {
	if snap == nil {
		return false
	}
	for _, m := range members {
		if a := snap.Actors[m.ID]; a != nil && a.State != sim.StateSleeping {
			return true
		}
	}
	return false
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
	// An asleep keeper can't take the renewal right now — within the renewal
	// window, flag it so the lodger waits rather than walking to a sleeping
	// keeper (ZBBS-WORK-416).
	if v.KeeperAsleep && v.ExpiresAt.Sub(now) <= 48*time.Hour {
		b.WriteString(" The keeper is abed just now — renew once they are next tending the desk.")
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

// renderLodgingOffer writes the seller-side "offer a room" cue (ZBBS-WORK-382):
// a co-present lodging-seeker by name plus the scene_quote call for a
// nights_stay with its lodging-specific args spelled out — qty is the number of
// NIGHTS (not rooms or guests), consume_now is false (a booking, not eat-here),
// and the room is for the single guest (omit consumers). scene_quote's own
// schema frames qty as "per consumer" goods, so without this spelled out the
// weak keeper model misreads a nights booking. Phrased conditionally ("if they
// want a room") so it respects the vendor block's "don't pitch unless they show
// interest" rule. Content-gated: a nil/empty view skips the section.
func renderLodgingOffer(b *strings.Builder, v *LodgingOfferView) {
	if v == nil || len(v.SeekerNames) == 0 {
		return
	}
	b.WriteString("## A room to let\n")
	who := joinNames(v.SeekerNames) // sanitizes each name inline
	beVerb, haveVerb := "is", "has"
	if len(v.SeekerNames) > 1 {
		beVerb, haveVerb = "are", "have"
	}
	fmt.Fprintf(b, "%s %s here with you and %s nowhere to stay. If they want a room, offer it — call scene_quote with item \"nights_stay\", consume_now false, qty set to the number of nights, and amount set to nights × %d coins (your nightly rate). A room is for the one guest, so leave consumers empty; use target_buyer only if you know the guest's name, otherwise leave it empty and anyone here may take the offer. They are then free to take it or leave it.\n", who, beVerb, haveVerb, v.NightlyRate)
	roomWord := "rooms"
	if v.RoomsAvailable == 1 {
		roomWord = "room"
	}
	fmt.Fprintf(b, "You have %d %s free at %s.\n\n", v.RoomsAvailable, roomWord, sanitizeInline(v.InnName))
}

// KeeperHeldLodgersView is the keeper-side "this guest already lodges here"
// signal (LLM-38). The offer cue (buildLodgingOfferCue) is correctly suppressed
// for an actor who already holds a grant — actorSnapIsLodgingSeeker returns
// false — but suppression alone leaves the keeper free-forming an offer off the
// passive "## Your inn" vacancy line ("1 of 4 rooms available…"), so the keeper
// re-offers a room the guest is already in. This is the positive counter-signal:
// it names each co-present actor holding an active ledger grant at THIS inn so
// the keeper LLM affirms ("you're already settled") instead of re-pitching.
// nil/empty skips the section. Pure over the snapshot.
type KeeperHeldLodgersView struct {
	// Lodgers are the co-present actors already holding a room at this inn.
	Lodgers []HeldLodger
}

// HeldLodger is one co-present actor already lodging at the keeper's inn.
type HeldLodger struct {
	// Name is the acquaintance-gated label (descriptorLabel).
	Name string

	// TenureLabel voices the time left on the stay ("paid for about 2 more
	// nights"). Computed at build time against snap.PublishedAt so the rendered
	// cue is deterministic against the snapshot rather than a render-time clock.
	TenureLabel string
}

// buildKeeperHeldLodgers builds the keeper-side held-lodger signal (LLM-38). It
// reuses the already-computed KeeperLodgingView (so the subject is confirmed a
// keeper of a lodging structure) and scans the keeper's co-present huddle for
// actors holding an active ledger grant at a private room IN the keeper's own
// structure — the guests the keeper must NOT re-offer a room to. Unlike the
// offer cue this is informational, not an act-now instruction, so it is NOT
// location-gated (it mirrors the ungated "## Your inn" status): a keeper who
// runs into their lodger should affirm rather than re-pitch wherever they meet.
// Returns nil (Render content-gates) when the subject isn't a keeper or no held
// lodger is co-present. Pure over the snapshot.
func buildKeeperHeldLodgers(snap *sim.Snapshot, subject sim.ActorID, keeper *KeeperLodgingView, members []HuddleMember) *KeeperHeldLodgersView {
	if snap == nil || keeper == nil {
		return nil
	}
	subj := snap.Actors[subject]
	if subj == nil || subj.WorkStructureID == "" {
		return nil
	}
	s := snap.Structures[subj.WorkStructureID]
	if s == nil {
		return nil
	}
	// The keeper's own private rooms — the held-grant rooms that count as
	// "lodging here". Same room set buildKeeperLodgingView scans for occupancy.
	privateRooms := make(map[sim.RoomID]bool)
	for _, r := range s.Rooms {
		if r != nil && r.Kind == sim.RoomKindPrivate {
			privateRooms[r.ID] = true
		}
	}
	if len(privateRooms) == 0 {
		return nil
	}

	now := snap.PublishedAt
	var held []HeldLodger
	for _, m := range members {
		if m.ID == subject {
			continue
		}
		as := snap.Actors[m.ID]
		if as == nil {
			continue
		}
		// The soonest-expiring active grant this peer holds at THIS inn (a guest
		// could in principle hold more than one; the nearest expiry is the one
		// that speaks to "how long are they settled").
		var best *sim.RoomAccess
		for _, ra := range as.RoomAccess {
			if !sim.IsActiveLedgerGrant(ra, now) || !privateRooms[ra.RoomID] {
				continue
			}
			if best == nil || ra.ExpiresAt.Before(*best.ExpiresAt) {
				best = ra
			}
		}
		if best == nil {
			continue
		}
		held = append(held, HeldLodger{
			Name:        descriptorLabel(m.DisplayName, m.Role, m.Acquainted),
			TenureLabel: heldLodgerTenure(*best.ExpiresAt, now),
		})
	}
	if len(held) == 0 {
		return nil
	}
	return &KeeperHeldLodgersView{Lodgers: held}
}

// renderKeeperHeldLodgers writes the "## Already lodging here" section: each
// co-present guest who already holds a room at the keeper's inn, the time left
// on their stay, and an explicit "do not offer another" steer so the keeper
// affirms instead of re-pitching off the "## Your inn" vacancy line. Content-
// gated: a nil/empty view writes nothing. The tenure phrase is precomputed at
// build time (TenureLabel) so this stays deterministic against the snapshot.
func renderKeeperHeldLodgers(b *strings.Builder, v *KeeperHeldLodgersView) {
	if v == nil || len(v.Lodgers) == 0 {
		return
	}
	b.WriteString("## Already lodging here\n")
	for _, l := range v.Lodgers {
		fmt.Fprintf(b, "%s already holds a room here, %s. Do not offer another — if they ask about lodging, tell them they are already settled.\n",
			sanitizeInline(l.Name), l.TenureLabel)
	}
	b.WriteString("\n")
}

// heldLodgerTenure phrases the time left on a held grant for the keeper cue.
// Three tiers mirroring lodgingStatusLine, voiced from the keeper's side. A
// just-expired grant (clock drift past the snapshot gate) falls into the first
// tier — "paid through the day" — which is harmless.
func heldLodgerTenure(expiresAt, now time.Time) string {
	d := expiresAt.Sub(now)
	switch {
	case d <= 24*time.Hour:
		return "paid through the day"
	case d <= 48*time.Hour:
		return "paid through tomorrow"
	default:
		nights := int(d / (24 * time.Hour))
		return fmt.Sprintf("paid for about %d more nights", nights)
	}
}

// RetireView is the content-gated "## Turn in for the night" section: the
// bedtime nudge for a lodger who has wound down to its rented inn and reached
// the lodger night hour (LLM-36). It hands the goodnight to the MODEL — the
// lodger winds down its conversation and turns in deliberately — rather than the
// engine silently bedding it (and speaking the engine-authored retire line). The
// engine bed-down stays the backstop: AutoBedAtHomeNPCs holds off briefly for a
// still-conversing lodger (lodgerAwaitingDeliberateRetire) and beds it once idle
// or past the grace margin. nil means the actor isn't a lodger at its inn within
// the night window and render omits the section.
type RetireView struct {
	// InnName is the display name of the inn the lodger's rented room is in.
	InnName string
}

// buildRetireCue returns the lodger bedtime "turn in" cue, or nil when it
// shouldn't fire. Fires when actorSnap holds an active ledger grant whose inn is
// the structure it is currently INSIDE (it has already wound down to its inn via
// the off-shift DutySteer) AND the village clock is within the lodger night
// window [LodgingBedtimeMinute, DawnMinute) — the same window the sim bed/wake
// gates use, so the cue fires exactly when the engine would otherwise bed it.
// Awake-only, and suppressed mid-business by windDownSuppressed (the inn doubles
// as the tavern — a lodger still eating dinner isn't told to go to bed).
//
// Gated on a co-present huddle companion (members non-empty — members already
// excludes the subject): a lodger alone has no goodnight to voice and is bedded
// silently by the backstop, so prompting it to "turn in" would be a cue it can't
// act on. This mirrors the engine hold (npc_sleep.go huddleWithCompanion), so
// the cue is shown exactly when AutoBedAtHomeNPCs defers the bed-down. Pure over
// the snapshot. LLM-36.
func buildRetireCue(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *RetireView {
	if snap == nil || actorSnap == nil || snap.LocalMinuteOfDay == nil {
		return nil
	}
	if len(members) == 0 {
		return nil // no companion to bid goodnight to — the backstop beds it silently
	}
	if !snap.DawnDuskMinuteOK {
		return nil // no usable dawn boundary — can't bound the night window
	}
	if actorSnap.State == sim.StateSleeping {
		return nil // already abed
	}
	now := snap.PublishedAt
	// The soonest-expiring active grant — the lodger relationship (mirrors
	// buildLodgingView's selection).
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
		return nil // not a lodger
	}
	s := structureForRoom(snap, best.RoomID)
	if s == nil || actorSnap.InsideStructureID != s.ID {
		return nil // not yet inside its rented inn — the wind-down cue routes it here first
	}
	if !minuteInWindow(snap.LodgingBedtimeMinute, snap.DawnMinute, *snap.LocalMinuteOfDay) {
		return nil // not bedtime yet
	}
	if windDownSuppressed(actorSnap, snap) {
		return nil // mid-meal at the inn-as-tavern — don't send a diner to bed
	}
	return &RetireView{InnName: innLabel(s)}
}

// renderRetire writes the "## Turn in for the night" bedtime nudge (LLM-36).
// Content-gated: a nil view writes nothing. The steer is situational, NOT a tool
// call — there is no NPC sleep verb; the lodger retires by winding down its
// conversation and ending its turn, and the engine beds it once idle. Phrasing
// invites the goodnight ("bid any companions here goodnight") only conditionally,
// so a lodger alone simply turns in.
func renderRetire(b *strings.Builder, v *RetireView) {
	if v == nil {
		return
	}
	fmt.Fprintf(b, "## Turn in for the night\nThe hour grows late. Bid any companions here goodnight and turn in for the night — you will rest in your room at %s once you have settled.\n\n", sanitizeInline(v.InnName))
}
