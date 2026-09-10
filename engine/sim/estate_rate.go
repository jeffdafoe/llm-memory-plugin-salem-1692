package sim

import (
	"log"
	"sort"
	"time"
)

// estate_rate.go — LLM-652 (the rate) and its collection slice. The town's rate on
// estate: a resident NPC holding coin above a floor pays a share of the excess
// into the town chest, and the chest pays the constable his wage.
//
// Why it exists. Resident coin concentrates: the village's flows run one way —
// wages out of the producers, spent at the shops, which buy their flour and water
// from the same producers — and the loop closes on two or three purses with a
// standing surplus and nothing that takes it back. Four months of levers acted on
// the edges (the visitor coin band and factor purse) or as flat per-business fees
// (the LLM-557 day's rate, LLM-648 equipment service, stall wear); none touched
// where the coin sat. On 2026-09-08 three purses held ~1700 of ~1960 resident coin
// while the shopkeeper, the innkeeper and the dairykeeper ended every day near
// zero. This is a fiscal loop: a progressive rate assessed on coin held, and the
// chest it fills paying the one villager with no trade of his own.
//
// THE CONSTABLE'S ARRIVAL IS THE COLLECTION. The first slice assessed every purse
// at the midnight rotation. That worked as arithmetic and failed as a scene: the
// rotation lands while every payer is asleep, the self-action trail that carried
// "You paid the town 38 coins" reaches back thirty minutes, and by the time Joseph
// Scott woke the line was gone — his purse simply read lower, with nothing saying
// why, which is the exact shape LLM-572 exists to prevent. So the levy now falls
// when the constable calls at the payer's business on his rounds and the payer is
// standing there: the engine takes the coin at that moment, both parties see it
// under "What you've recently done", and the payer's relationship record says what
// it was. The constable is the trigger, never the holder — the coin goes straight
// to the chest, so there is no forty-coin purse for him to hand back as a refund
// the way he handed back the day's rate (LLM-607).
//
// THE ENGINE MOVES THE COIN. That is a deliberate departure from the LLM-557 day's
// rate, whose central choice was that the engine only accrues an obligation and the
// keeper hands the coin over himself through pay. That channel is right for one
// coin a day and wrong for this: it depends on the model choosing to pay, and an
// assessment of forty coins refused or forgotten is arrears that either pile into a
// shock bill or get capped — and a cap on a progressive rate means the rich pay
// less. The precedent for engine-moved coin is the LLM-615 lodging auto-charge
// (lodger_rebook.go): the coin moves and the records are written in the same
// command, so the two can never disagree.
//
// NO CARRY-FORWARD. The due is a pure function of the purse at the moment of the
// visit — EstateRateDue on what the payer holds right then — and the only stored
// state is Actor.EstateRateAssessedAt, the stamp that makes it once per game-day.
// An owed balance that accrued at midnight and drained on the visit would go
// partial whenever the purse was spent down in between, for a gain that hardly
// exists: the rounds run every two hours across a ten-hour watch, so a keeper on
// shift is caught most days, and a missed day only slows the drain, which the rate
// knob already sets (Jeff, 2026-09-09).
//
// Progressive, not flat. The obligation is a share of coin held ABOVE
// EstateRateFloor, so a purse at or under the floor pays nothing and the incidence
// falls on the surplus alone. Equilibrium for any actor is
// floor + net_income / rate: with a 5% rate and a floor of 100, a miller netting
// 18 coins a day settles near 480 rather than climbing without limit; at 10% near
// 290. The rate is the knob and it is live (umbilical /settings/set).
//
// THE CHEST PAYS THE CONSTABLE. Once a game-day on the rotation boundary the
// constable draws ConstableWagePerDay from the chest, capped by what the chest
// holds — nothing is minted. This is the constable's ONE levy: the LLM-557
// coin-a-day town rate he used to collect by hand was retired in LLM-655, so the
// estate rate alone feeds the chest, and the chest feeds the constable and, in a
// later slice, public works and provisions for the poor.
//
// Coin-neutral against the village as a whole: Σ resident coin + chest changes
// only by visitor legs, grants and wages to visitors, which is the invariant the
// coin sweep can check. The chest is durable (world_state.town_chest_coins) — the
// coin has left the purses, so losing it on restart would destroy it.
//
// Seams: collectEstateRateAtStop hangs off the beat credit in advanceBeatRoute
// (npc_route.go) — the moment the rounds machinery records the constable at a
// business; payConstableWage fires once per game-day from checkAndRotate
// (world_rotation.go), beside ApplyFarmUpkeep on the same durable LastRotationAt
// boundary; the chest rides WorldEnvironment through the checkpoint; the knobs
// are registry settings (settings_registry_table.go). The payer has no perception
// cue — the two action rings and the relationship fact carry the explanation; the
// constable's one line about the levy is perception/estate_rate.go.

const (
	// DefaultEstateRateFloor is the coin an actor keeps untouched. Sized above every
	// working purse in the village on 2026-09-08 (the largest non-surplus purse was
	// 47) so the levy reaches the three pooled purses and nobody else.
	DefaultEstateRateFloor = 100

	// DefaultEstateRatePctPerDay is the share of coin above the floor taken each
	// game-day, in whole percent. A non-positive value disables the levy (the
	// per-feature off-switch, mirroring FarmUpkeepCoinsPerShovel==0).
	DefaultEstateRatePctPerDay = 5

	// DefaultConstableWagePerDay is what the chest pays each constable per
	// game-day. Sized on his own two weeks of living before the wage (2026-08-26
	// → 09-09): the day's rate brought him 7–11 coins a day and he spent 5–11,
	// on porridge, journeycake, ale and berries — a hot meal and a drink twice a
	// day is 7 or 8. Eight keeps him eating as he does; five would put him back
	// on berries. A non-positive value disables the wage (the off-switch); the
	// chest's balance caps it regardless, so it never mints.
	DefaultConstableWagePerDay = 8

	// estateRateForText is the payment's stated purpose on the payer's records —
	// the in-memory ring ("You paid Constable Gideon Marsh 38 coins for the rate on
	// your estate") and the durable row's `for`.
	estateRateForText = "the rate on your estate"

	// estateRateCollectedForText is the purpose on the collector's records ("You
	// collected 38 coins from Joseph Scott for the rate on their estate").
	estateRateCollectedForText = "the rate on their estate"

	// estateRateRecipientName is the counterparty name on the payer's DURABLE row.
	// It names no actor on purpose: the chest is not a peer, so the coin-record
	// boot seed must not resolve it to anyone (see loadPaymentsSinceSQL, which
	// excludes these rows outright by the estate_rate marker rather than by
	// failing the name lookup). The in-memory ring names the constable instead,
	// because that is who the payer saw take it.
	estateRateRecipientName = "the town"

	// constableWageForText is the purpose on the wage records ("You collected 8
	// coins from the town for your wage as constable").
	constableWageForText = "your wage as constable"
)

// EstateRateDue returns what a purse owes this assessment: pct percent of the coin
// held strictly above floor, floored to whole coins. A non-positive pct disables
// the levy (returns 0 — the off-switch); a balance at or below the floor owes
// nothing. Pure, so the assessment and anything reasoning about the rate read the
// same rule.
//
// The due never exceeds the excess over the floor. The setter refuses a pct above
// 100 (pctSetting), but the loader does not (parseIntSetting), so a malformed
// persisted row could still reach here — and a levy that debits a purse below its
// own floor is the one outcome the floor exists to rule out. Clamping the pct also
// bounds the multiplication, so no value can overflow it into a negative due.
func EstateRateDue(coins, floor, pct int) int {
	if pct <= 0 || coins <= floor {
		return 0
	}
	if pct > 100 {
		pct = 100
	}
	return (coins - floor) * pct / 100
}

// estateRateAssessable gates who the levy falls on: a resident NPC. Visitors carry
// their purse in and out of the village, PCs are players, decoratives are never
// ticked and hold a seed purse nobody spends, and the constable is the collector
// rather than a ratepayer. Nil-safe.
func estateRateAssessable(a *Actor) bool {
	if a == nil {
		return false
	}
	if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
		return false
	}
	if a.VisitorState != nil || IsVisitorActorID(a.ID) {
		return false
	}
	return !ActorIsConstable(a)
}

// gameDayStart returns the start of the game-day `now` falls in: the most recent
// daily-rotation boundary at or before now, in the world's timezone. The same
// boundary checkAndRotate keys the levies to, so "once a game-day" here means the
// same day the rotation means. Falls back to the compiled default rotation time
// and to time.Local exactly as checkAndRotate does; a malformed RotationTime is
// treated as midnight rather than an error, since this is a read on a hot path
// and the rotation driver already logs the misconfiguration once a minute.
func gameDayStart(w *World, now time.Time) time.Time {
	spec := w.Settings.RotationTime
	if spec == "" {
		spec = DefaultRotationTime
	}
	hour, minute, err := ParseHM(spec)
	if err != nil {
		hour, minute = 0, 0
	}
	loc := w.Settings.Location
	if loc == nil {
		loc = time.Local
	}
	return MostRecentRotationBoundary(now.In(loc), hour, minute)
}

// estateRateAssessedToday reports whether the actor's purse has already been
// assessed in the game-day `now` falls in. The stamp is durable (actor.
// estate_rate_assessed_at), so a deploy restart between two rounds cannot collect
// twice in a day.
func estateRateAssessedToday(w *World, a *Actor, now time.Time) bool {
	return a.EstateRateAssessedAt != nil && !a.EstateRateAssessedAt.Before(gameDayStart(w, now))
}

// collectEstateRateAtStop levies the estate rate on the owner of the business the
// constable has just been credited at, when the owner is standing there. Called
// from advanceBeatRoute the moment a beat stop is credited, so the collection and
// the round's own record of the visit are one event.
//
// The gate, in order: the stop is an owned business; its owner is a resident NPC
// (estateRateAssessable); the constable really is one (defensive — the beat
// carrier set is the constable today, but the route label is the route's claim,
// not the actor's); the owner is AT the business by the same predicate the wear
// and repair surfaces use (AtBusiness — inside the structure, or at its loiter
// pin); and the owner has not been assessed this game-day. An owner who is away
// buying water or off shift is simply not collected from on this visit; the next
// round, two hours on, tries again, and a day with no catch is a day without the
// levy (no carry-forward — see the file header).
//
// The stamp is set when the assessment HAPPENS, due or no due: a purse at or under
// the floor when the constable calls owes nothing today, even if it fills up by
// the afternoon round. That is what makes the levy once a day rather than "once a
// day, unless you got richer".
//
// Records, all written in the same command as the debit, for the LLM-572 reason:
// coin that leaves a purse with nothing saying why gets read as a debt someone
// owes back.
//
//   - The payer's ring entry names the constable ("You paid Constable Gideon
//     Marsh 38 coins for the rate on your estate") — that is who he saw take it.
//   - The constable's ring entry is a `collected` beat ("You collected 38 coins
//     from Joseph Scott for the rate on their estate"), so his own scene shows the
//     coin passed through his hands without landing in his purse.
//   - The payer's durable row carries the estate_rate marker and NO recipient
//     actor id (recipient "the town", collected_by the constable), so the coin-
//     record seed never credits the constable with coin he does not hold.
//   - A relationship fact on both sides (RecordInteraction, which gates itself to
//     shared-VA actors) says the coin was the town's due, taken into the chest,
//     and that nothing is owed back — the Moses James refund (LLM-607) was
//     consolidation reading a rate with no delivery as an order never filled, and
//     this is the same defence the day's rate carries.
//
// RecordCoinPaid is deliberately NOT called: the chest is not an actor, and
// crediting the constable's pair record would tell him he holds coin he does not.
func collectEstateRateAtStop(w *World, constable *Actor, stop RouteStop, now time.Time) {
	if w == nil || constable == nil || w.Settings.EstateRatePctPerDay <= 0 {
		return
	}
	if !ActorIsConstable(constable) {
		return
	}
	business := w.VillageObjects[stop.ObjectID]
	if !IsRateableBusiness(business) {
		return
	}
	owner := w.Actors[business.OwnerActorID]
	if !estateRateAssessable(owner) {
		return
	}
	pin, pinOK := effectiveObjectLoiterTile(w, business.ID)
	if !AtBusiness(owner.Pos, owner.InsideStructureID, business.ID, pin, pinOK) {
		return
	}
	if estateRateAssessedToday(w, owner, now) {
		return
	}
	assessedAt := now
	owner.EstateRateAssessedAt = &assessedAt

	due := EstateRateDue(owner.Coins, w.Settings.EstateRateFloor, w.Settings.EstateRatePctPerDay)
	if due <= 0 {
		return
	}
	owner.Coins -= due
	w.Environment.TownChest += due

	ownerName := actorDisplayNameOrID(owner)
	constableName := actorDisplayNameOrID(constable)

	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          owner.ID,
		OccurredAt:       now,
		ActionType:       ActionTypePaid,
		Text:             estateRateForText,
		HuddleID:         owner.CurrentHuddleID,
		CounterpartyName: constableName,
		Amount:           due,
	}).Fn(w); err != nil {
		// Append failed (empty ActorID / zero time — caller bug). The coin has
		// moved; log loudly rather than roll back, so the chest and the purses
		// stay consistent with each other.
		log.Printf("sim/estate_rate: action-log append failed for payer %q: %v", owner.ID, err)
	}
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          constable.ID,
		OccurredAt:       now,
		ActionType:       ActionTypeCollected,
		Text:             estateRateCollectedForText,
		HuddleID:         constable.CurrentHuddleID,
		CounterpartyName: ownerName,
		Amount:           due,
	}).Fn(w); err != nil {
		log.Printf("sim/estate_rate: action-log append failed for collector %q: %v", constable.ID, err)
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    owner.ID,
		OccurredAt: now,
		ActionType: ActionTypePaid,
		Payload: map[string]any{
			"recipient":             estateRateRecipientName,
			"amount":                due,
			"for":                   estateRateForText,
			"estate_rate":           true,
			"chest_after":           w.Environment.TownChest,
			"collected_by":          constableName,
			"collected_by_actor_id": string(constable.ID),
		},
		SpeakerName: ownerName,
		HuddleID:    owner.CurrentHuddleID,
		Source:      "engine",
	})
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    constable.ID,
		OccurredAt: now,
		ActionType: ActionTypeCollected,
		Payload: map[string]any{
			"payer":          ownerName,
			"payer_actor_id": string(owner.ID),
			"amount":         due,
			"for":            estateRateCollectedForText,
			"estate_rate":    true,
			"chest_after":    w.Environment.TownChest,
		},
		SpeakerName: constableName,
		HuddleID:    constable.CurrentHuddleID,
		Source:      "engine",
	})

	payerFact := estateRatePaidFactText(constableName, due)
	collectorFact := estateRateCollectedFactText(ownerName, due)
	if _, err := RecordInteraction(owner.ID, constable.ID, InteractionPaid, payerFact, now).Fn(w); err != nil {
		log.Printf("sim/estate_rate: RecordInteraction payer→constable %q→%q: %v", owner.ID, constable.ID, err)
	}
	if _, err := RecordInteraction(constable.ID, owner.ID, InteractionPaidBy, collectorFact, now).Fn(w); err != nil {
		log.Printf("sim/estate_rate: RecordInteraction constable→payer %q→%q: %v", constable.ID, owner.ID, err)
	}
	log.Printf("sim/estate_rate: %q collected %d from %q for the rate on their estate (chest now %d)",
		constable.ID, due, owner.ID, w.Environment.TownChest)
}

// estateRateFactClosing is the load-bearing clause both relationship facts end on
// (the retired town rate's fact carried the same one, LLM-572): consolidation is told to
// trust the ledger over what was said (LLM-499), so a payment with a stated
// purpose and no delivery ever recorded against it reads as an order placed and
// never filled unless the record itself says it was a levy.
const estateRateFactClosing = "No goods were bought and none are owed in return."

// estateRatePaidFactText is the payer's side: his estate, his coin, the town's
// chest.
func estateRatePaidFactText(constableName string, amount int) string {
	return "I paid " + constableName + " the rate on my estate, " + coinsPhrase(amount) +
		" — the town's due, taken into the town chest. " + estateRateFactClosing
}

// estateRateCollectedFactText is the collector's side, worded from where the coin
// actually went: the payer's estate, and the chest rather than the constable's
// purse — "paid me" would tell him he received it (code_review, LLM-653).
func estateRateCollectedFactText(ownerName string, amount int) string {
	return ownerName + " paid the rate on their estate, " + coinsPhrase(amount) +
		" — the town's due; I collected it into the town chest. " + estateRateFactClosing
}

// actorDisplayNameOrID is the durable-row name fallback the auto-charge paths
// share: a persisted audit row must never carry a blank speaker or counterparty.
func actorDisplayNameOrID(a *Actor) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return string(a.ID)
}

// CollectEstateRate wraps one collection as a Command — the operator and test
// entry to the same code the beat credit runs, without walking a route. The
// constable must be a constable and the stop an owned business; every other gate
// (owner present, not yet assessed today, something due) is the collection's own
// and a no-op leaves nothing behind.
func CollectEstateRate(constableID ActorID, businessID VillageObjectID, now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		constable := w.Actors[constableID]
		if constable == nil {
			return nil, ErrActorNotFound
		}
		collectEstateRateAtStop(w, constable, RouteStop{ObjectID: businessID}, now)
		return nil, nil
	}}
}

// payConstableWage pays each constable his day's wage out of the town chest. Runs
// once per game-day on the rotation boundary (checkAndRotate), beside the day's-
// rate accrual. Capped by the chest: the wage is coin the rates already took out
// of purses, never minted, so an empty chest pays nothing and a thin one pays what
// it has. A non-positive ConstableWagePerDay disables the wage (the off-switch).
//
// Records mirror the collection's: a `collected` ring entry on the constable ("You
// collected 8 coins from the town for your wage as constable") and a durable row
// with the town_wage marker. The ring entry lands at midnight and is gone before he
// wakes; it is audit, and a purse going UP unexplained is not the debt-shaped
// misreading LLM-572 guards against. Nothing pairs the wage with an actor: the
// row type is not one the coin-record seed selects, and RecordCoinPaid is not
// called.
//
// Constables are paid in ActorID order so two of them share a thin chest the same
// way on every boot.
func payConstableWage(w *World, now time.Time) {
	if w == nil || w.Settings.ConstableWagePerDay <= 0 {
		return
	}
	var constables []*Actor
	for _, a := range w.Actors {
		if ActorIsConstable(a) {
			constables = append(constables, a)
		}
	}
	sort.Slice(constables, func(i, j int) bool { return constables[i].ID < constables[j].ID })
	for _, a := range constables {
		wage := w.Settings.ConstableWagePerDay
		if wage > w.Environment.TownChest {
			wage = w.Environment.TownChest
		}
		if wage <= 0 {
			log.Printf("sim/estate_rate: the chest is empty — no wage for %q today", a.ID)
			continue
		}
		w.Environment.TownChest -= wage
		a.Coins += wage

		if _, err := AppendActionLogEntry(ActionLogEntry{
			ActorID:          a.ID,
			OccurredAt:       now,
			ActionType:       ActionTypeCollected,
			Text:             constableWageForText,
			HuddleID:         a.CurrentHuddleID,
			CounterpartyName: estateRateRecipientName,
			Amount:           wage,
		}).Fn(w); err != nil {
			log.Printf("sim/estate_rate: action-log append failed for wage to %q: %v", a.ID, err)
		}
		w.AppendActionLogDurable(DurableActionLogRow{
			ActorID:    a.ID,
			OccurredAt: now,
			ActionType: ActionTypeCollected,
			Payload: map[string]any{
				"payer":       estateRateRecipientName,
				"amount":      wage,
				"for":         constableWageForText,
				"town_wage":   true,
				"chest_after": w.Environment.TownChest,
			},
			SpeakerName: actorDisplayNameOrID(a),
			HuddleID:    a.CurrentHuddleID,
			Source:      "engine",
		})
	}
}

// ApplyConstableWage wraps the daily wage as a Command so the rotation driver can
// run it on the world goroutine. Mirrors ApplyFarmUpkeep.
func ApplyConstableWage(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		payConstableWage(w, now)
		return nil, nil
	}}
}

// IsRateableBusiness reports whether obj is a place the estate rate is collected at:
// an OWNED business. Nil-safe.
//
// Deliberately the same gate as IsWearableStall (owned + TagBusiness) rather than a
// new scope of its own — the levy is collected at exactly the set of places that are
// somebody's shop, which is also the set the constable's rounds call at
// (buildConstableRoundsCandidates takes every TagBusiness object). The owner
// requirement is what makes the stop collectable: an unowned business has nobody
// whose purse the rate could come from.
func IsRateableBusiness(obj *VillageObject) bool {
	return obj != nil && obj.OwnerActorID != "" && obj.HasTag(TagBusiness)
}

// RateableBusinessOf returns the rateable business owned by ownerID, or nil when
// they own none. Takes the object map so it serves both the live World
// (w.VillageObjects) and a perception Snapshot (snap.VillageObjects).
//
// Picks the LOWEST VillageObjectID rather than the map-iteration first, so the result
// is deterministic even if the one-business-per-owner data convention (shared with
// OwnedWearableStall / OwnedFarm) is ever broken by a live re-tag or a bad seed.
//
// The tie-break was load-bearing for the retired town rate, whose keeper cue and
// settle path each resolved the business independently and could disagree under map
// order (LLM-557). Today the function only gates the constable's perception line
// (perception/estate_rate.go asks whether a co-present actor owns ANY rateable
// business), so a wobble would change nothing — the determinism is kept because it
// costs nothing and the next caller may care which shop, as the first one did. Same
// reasoning as WearableStallToMend's lowest-LaborID tie-break.
func RateableBusinessOf(objects map[VillageObjectID]*VillageObject, ownerID ActorID) *VillageObject {
	if ownerID == "" {
		return nil
	}
	var best *VillageObject
	for _, obj := range objects {
		// nil-safe: also runs over hand-built perception/test maps where a stray
		// nil entry must not panic the world (the OwnedHearth / OwnedFarm guard).
		if obj == nil || obj.OwnerActorID != ownerID || !IsRateableBusiness(obj) {
			continue
		}
		if best == nil || obj.ID < best.ID {
			best = obj
		}
	}
	return best
}

// ActorIsConstable reports whether a carries the constable attribute — the
// collector side of the levy. The engine keys behaviour on attribute PRESENCE only
// (the value is unused), matching findActorsWithAttribute in the rounds driver.
// Nil-safe.
func ActorIsConstable(a *Actor) bool {
	if a == nil {
		return false
	}
	_, ok := a.Attributes[AttrConstable]
	return ok
}
