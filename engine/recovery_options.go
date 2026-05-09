package main

// Recovery options perception (ZBBS-172, expanded ZBBS-HOME-206).
//
// When a tired NPC's tick fires, this block surfaces the actor's real
// tiredness-decision surface as one unified list — rest spots
// (outdoor, home, inn) AND nearby consumable remedies (vendors with
// tiredness-satisfying items like coca tea). All annotated with
// recovery magnitude (numeric effect), cost, distance/direction, and
// time-of-day appropriateness.
//
// The block exists to force a real tradeoff. A tired actor weighs:
//
//   - Walk to a free outdoor spot and absorb the round-trip movement
//     fatigue, vs.
//   - Buy a remedy from a nearby vendor (faster, costs coins), vs.
//   - Spend coins at the inn for proper sleep, vs.
//   - Power through and stay at work.
//
// Time-of-day predicates filter out inappropriate options. On-shift
// vendors don't see "go home and sleep" (which would mean abandoning
// the shift), unless tiredness has reached critical (90% of needMax),
// at which point all gates lift and the LLM gets to decide whether
// the work hours are worth pushing through.
//
// Cost is never surfaced as ground truth from a vendor's price config —
// NPCs only know what they've personally paid for. lastPaidPrice
// (pay_history.go) returns the buyer's most recent accepted price for
// this seller × item_kind. No history → "ask the keeper." Knowledge
// of price is earned by patronage.
//
// Bullet format is benefit-first: name — quality (magnitude), cost,
// distance/direction[ caveat][ — annotation]. The pre-ZBBS-HOME-206
// format led with the distance ("Picnic Area (a long walk — fatigue
// cost both ways north) — a proper sit, free"), which framed every
// option as a negative ("here's a long walk you have to take") with
// the freebie hidden behind it. The new format leads with what the
// option does FOR the actor, then qualifies with cost and friction.
//
// Tiredness used to also be a need branch in satiation.go. ZBBS-HOME-206
// dropped that branch because the satiation rendering — own-stock line
// plus "Nearby satisfiers" bullets — duplicated the recovery list and
// the model saw two parallel sections it had to reconcile. Recovery
// options now owns the tiredness perception end-to-end. Hunger and
// thirst stay in satiation because the primary resolution path for
// those is consume-from-stock; tiredness's primary path is spatial
// (walk to a rest spot, lie down at home, drink a brew) so a unified
// spatial list reads better.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// restSpot is one entry in the recovery options list. Covers all four
// option types (outdoor rest, home, inn, consumable remedy) — the
// fields are general enough that one struct + one formatter handles
// every kind. Type-specific helpers populate the fields differently:
// outdoor has CostText="free" and a magnitude over rest, consumables
// have CostText from lastPaidPrice and an immediate magnitude.
type restSpot struct {
	Name                string
	StructureID         string
	Distance            float64 // tiles
	Direction           string  // "north", "northeast", etc.
	RecoveryQualitative string  // "a brief easing", "a proper sit", "a thorough waking", "a full sleep at home", "a room for the night"
	MagnitudeNote       string  // "-3 tiredness over a short rest", "-12 tiredness immediate". Empty for sleep/lodging where minute-rate recovery doesn't fit a single number.
	CostText            string  // "free", "2 coins", "you paid 5 coins last time, three days ago", "ask the keeper"
	Annotation          string  // "" or "would mean leaving your shift early"
	DistanceCaveat      string  // "" or "costs fatigue both ways" — only set for far walks where the round-trip cost is a real friction the model should weigh.
}

// buildRecoveryOptionsSection returns the perception lines surfacing
// the actor's real recovery choices, or "" when the actor isn't yet
// tired enough to warrant the block.
//
// onShift / shiftHasWork are computed by the caller from the same
// schedule logic the existing shift-line uses; passing them in keeps
// the block aligned with the rest of the perception.
func (app *App) buildRecoveryOptionsSection(
	ctx context.Context,
	r *agentNPCRow,
	tiredT int,
	criticalT int,
	onShift bool,
	shiftHasWork bool,
) string {
	if r.Tiredness < tiredT {
		return ""
	}
	isCritical := r.Tiredness >= criticalT

	// Free outdoor rest spots: tiredness-bearing object_refresh rows on
	// named structures, sorted by distance.
	free := app.loadFreeRestSpots(ctx, r, 5)

	// Nearby vendors with tiredness-satisfying consumables (coca tea,
	// future stimulant brews). Always shown — these aren't shift-gated
	// because buying and drinking is a cheap interaction that doesn't
	// require abandoning the post.
	consumables := app.loadConsumableRemedies(ctx, r, 3)

	// Owned home: only when off-shift OR critical. On-shift workers
	// who go home would be abandoning their post; the gate prevents
	// the LLM from picking the easy option in normal hours.
	var home *restSpot
	if r.HomeStructureID.Valid && (!onShift || isCritical) {
		home = app.loadHomeRestSpot(ctx, r, onShift, isCritical)
	}

	// Inns (tavern-tagged structures with a tavernkeeper who has sold
	// nights_stay). Only when off-shift OR critical, same reasoning
	// as home.
	var inns []restSpot
	if !onShift || isCritical {
		inns = app.loadInnRestSpots(ctx, r, 3, onShift, isCritical)
	}

	// Own-stock tiredness consumables (e.g. the actor is carrying tea
	// from a prior buy). Surfaced as an inventory-style line outside
	// the bulleted spatial list because the resolution doesn't involve
	// walking anywhere — they can consume() in place.
	ownStockLine := app.loadOwnTirednessStockLine(ctx, r.ID)

	if len(free) == 0 && len(consumables) == 0 && home == nil && len(inns) == 0 && ownStockLine == "" {
		return ""
	}

	var lines []string
	if isCritical {
		lines = append(lines, "You may stumble if you don't rest soon.")
	} else {
		lines = append(lines, "You feel weary enough to weigh a real rest.")
	}
	lines = append(lines, fmt.Sprintf("You have %d coins.", r.Coins))
	_ = shiftHasWork

	// Merge spatial bullets into one list and sort by distance so the
	// closest option (whatever its type) reads first. The model can
	// then weigh magnitude vs friction across types — e.g. a -12
	// thorough waking 5 tiles away beats a -3 brief easing 2 tiles
	// away despite being slightly farther.
	var bullets []restSpot
	bullets = append(bullets, free...)
	bullets = append(bullets, consumables...)
	if home != nil {
		bullets = append(bullets, *home)
	}
	bullets = append(bullets, inns...)
	sortRestSpotsByDistance(bullets)

	if len(bullets) > 0 {
		lines = append(lines, "Recovery options nearby:")
		for _, s := range bullets {
			lines = append(lines, "  - "+formatRestSpot(s))
		}
	}
	if ownStockLine != "" {
		lines = append(lines, ownStockLine)
	}
	return strings.Join(lines, "\n")
}

// sortRestSpotsByDistance sorts bullets in place by tile distance,
// closest first. Stable so that within a tier, the original
// per-loader ordering (free by SQL distance, etc.) survives ties.
func sortRestSpotsByDistance(spots []restSpot) {
	// Insertion sort — stable, fine for small N (~10).
	for i := 1; i < len(spots); i++ {
		for j := i; j > 0 && spots[j-1].Distance > spots[j].Distance; j-- {
			spots[j-1], spots[j] = spots[j], spots[j-1]
		}
	}
}

// formatRestSpot composes one bullet line from a restSpot.
//
// Format: "Name — descriptor[, cost], distance direction[ — caveat][ — annotation]"
//
//	"PW Apothecary — coca tea, a thorough waking (-12 tiredness immediate), 2 coins, short walk northwest"
//	"Shade Tree — a brief easing (-3 tiredness over a short rest), free, long walk southeast"
//	"Picnic Area — a proper sit (-6 tiredness over a short rest), free, long walk north — costs fatigue both ways"
//	"Thorne Residence — a full sleep at home, free, fair walk east — would mean leaving your shift early"
func formatRestSpot(s restSpot) string {
	descriptor := s.RecoveryQualitative
	if s.MagnitudeNote != "" {
		descriptor += " (" + s.MagnitudeNote + ")"
	}
	distancePhrase := qualitativeDistance(s.Distance) + " " + s.Direction
	parts := []string{s.Name + " — " + descriptor}
	if s.CostText != "" {
		parts = append(parts, s.CostText)
	}
	parts = append(parts, distancePhrase)
	line := strings.Join(parts, ", ")
	if s.DistanceCaveat != "" {
		line += " — " + s.DistanceCaveat
	}
	if s.Annotation != "" {
		line += " — " + s.Annotation
	}
	return line
}

// qualitativeDistance maps a tile distance to a phrase the perception
// can render without surfacing a number. The "long walk" tier has its
// fatigue caveat moved to a separate DistanceCaveat field rather than
// being baked into the phrase, so the formatter can lead with the
// option's benefit and append the friction as a trailing qualifier
// instead of fronting it.
func qualitativeDistance(tiles float64) string {
	switch {
	case tiles <= 5:
		return "short walk"
	case tiles <= 15:
		return "fair walk"
	default:
		return "long walk"
	}
}

// distanceCaveat returns the friction qualifier for a far walk, or ""
// for shorter distances where the round-trip cost is negligible. The
// >30-tile threshold matches the prior qualitativeDistance "long walk
// — fatigue cost both ways" tier from before the format split.
func distanceCaveat(tiles float64) string {
	if tiles > 30 {
		return "costs fatigue both ways"
	}
	return ""
}

// cardinalDirection returns "north"/"northeast"/etc. for the vector
// from (fromX,fromY) to (toX,toY) in world coordinates. Uses 8-point
// granularity since perception readers don't need finer than that.
// Y-positive is south (Salem's coordinate convention — see
// pickWalkTarget and gather.go).
func cardinalDirection(fromX, fromY, toX, toY float64) string {
	dx := toX - fromX
	dy := toY - fromY
	// atan2 with dy first puts north (-y) at +90 degrees from east (+x).
	// Convert to the 8 compass bins.
	angle := math.Atan2(dy, dx) * 180 / math.Pi // -180..180; 0=east, 90=south, -90=north
	// Normalize to 0..360 with 0=east.
	if angle < 0 {
		angle += 360
	}
	switch {
	case angle < 22.5:
		return "east"
	case angle < 67.5:
		return "southeast"
	case angle < 112.5:
		return "south"
	case angle < 157.5:
		return "southwest"
	case angle < 202.5:
		return "west"
	case angle < 247.5:
		return "northwest"
	case angle < 292.5:
		return "north"
	case angle < 337.5:
		return "northeast"
	default:
		return "east"
	}
}

// loadFreeRestSpots collects the nearest tiredness-bearing free
// recovery sources (rest-trees, etc.) from object_refresh, capped at
// `limit`. Filters to rows whose object has a display_name (so the
// perception can name it) and whose attribute is tiredness with a
// negative amount (positive amounts would NOT recover; defensive).
//
// Recovery quality is qualitative and derived from |amount|+|dwell|:
// strong arrival hits or strong dwell read as "a proper sit",
// otherwise "a brief easing". MagnitudeNote surfaces the same total
// as a numeric "-N tiredness over a short rest" so the model can
// compare to consumable remedies that hit immediately for a known
// magnitude.
func (app *App) loadFreeRestSpots(ctx context.Context, r *agentNPCRow, limit int) []restSpot {
	rows, err := app.DB.Query(ctx,
		`SELECT vo.id::text, COALESCE(vo.display_name, a.name), vo.x, vo.y,
		        ore.amount,
		        COALESCE(ore.dwell_amount, 0)
		   FROM village_object vo
		   JOIN asset a              ON a.id = vo.asset_id
		   JOIN object_refresh ore   ON ore.object_id = vo.id
		  WHERE vo.display_name IS NOT NULL
		    AND ore.attribute = 'tiredness'
		    AND ore.amount    < 0
		  ORDER BY (vo.x - $1) * (vo.x - $1) + (vo.y - $2) * (vo.y - $2)
		  LIMIT $3`,
		r.CurrentX, r.CurrentY, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []restSpot
	for rows.Next() {
		var id, name string
		var x, y float64
		var amount, dwellAmount int
		if err := rows.Scan(&id, &name, &x, &y, &amount, &dwellAmount); err != nil {
			continue
		}
		const tileSize = 32.0
		dx := (x - r.CurrentX) / tileSize
		dy := (y - r.CurrentY) / tileSize
		dist := math.Sqrt(dx*dx + dy*dy)
		strength := -amount + -dwellAmount // both negative; magnitude
		quality := "a brief easing"
		if strength >= 4 {
			quality = "a proper sit"
		}
		out = append(out, restSpot{
			Name:                name,
			StructureID:         id,
			Distance:            dist,
			Direction:           cardinalDirection(r.CurrentX, r.CurrentY, x, y),
			RecoveryQualitative: quality,
			MagnitudeNote:       fmt.Sprintf("-%d tiredness over a short rest", strength),
			CostText:            "free",
			DistanceCaveat:      distanceCaveat(dist),
		})
	}
	return out
}

// loadConsumableRemedies finds nearby vendors stocking tiredness-
// satisfying items, returning one bullet per (vendor, item) pair.
// Mirrors satiationNearbyVendors's vendor-resolution shape (actor at
// inside_structure_id with serve attribute, on-station inventory) but
// flattens to per-item rows with prices so each remedy is a discrete
// option the model can pick.
//
// Cost: lastPaidPrice for the (buyer, seller, item_kind) triple. A
// repeat customer reads "2 coins"; a first-timer reads "ask the
// keeper" — same patronage-knowledge convention as inn lodging.
//
// Closed-shop suppression: vendors on take_break drop out, same as
// satiationNearbyVendors. The break_until check is in the SQL.
func (app *App) loadConsumableRemedies(ctx context.Context, r *agentNPCRow, limit int) []restSpot {
	rows, err := app.DB.Query(ctx, `
		SELECT s.id::text, COALESCE(s.display_name, ass.name) AS structure_name,
		       a.id::text AS vendor_id, a.display_name AS vendor_name,
		       s.x, s.y,
		       ik.display_label, ik.name AS item_kind, isf.amount
		  FROM village_object s
		  JOIN asset ass ON ass.id = s.asset_id
		  JOIN actor a ON a.inside_structure_id = s.id
		  JOIN actor_inventory inv ON inv.actor_id = a.id
		  JOIN item_kind ik ON ik.name = inv.item_kind
		  JOIN item_satisfies isf ON isf.item_kind = ik.name
		 WHERE a.id != $1::uuid
		   AND a.login_username IS NULL
		   AND EXISTS (
		       SELECT 1
		         FROM actor_attribute aa
		         JOIN attribute_definition ad ON ad.slug = aa.slug
		        WHERE aa.actor_id = a.id
		          AND ad.tools ? 'serve'
		   )
		   AND inv.quantity > 0
		   AND isf.attribute = 'tiredness'
		   AND (a.break_until IS NULL OR a.break_until <= NOW())
		 ORDER BY (s.x - $2) * (s.x - $2) + (s.y - $3) * (s.y - $3),
		          isf.amount DESC
		 LIMIT $4
	`, r.ID, r.CurrentX, r.CurrentY, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []restSpot
	for rows.Next() {
		var sid, sname, vendorID, vendorName, label, itemKind string
		var x, y float64
		var amount int
		if err := rows.Scan(&sid, &sname, &vendorID, &vendorName, &x, &y, &label, &itemKind, &amount); err != nil {
			continue
		}
		const tileSize = 32.0
		dx := (x - r.CurrentX) / tileSize
		dy := (y - r.CurrentY) / tileSize
		dist := math.Sqrt(dx*dx + dy*dy)

		costText := "ask the keeper"
		if paid, _, ok, _ := app.lastPaidPrice(ctx, r.ID, vendorID, itemKind); ok {
			costText = fmt.Sprintf("%d coins", paid)
		}
		out = append(out, restSpot{
			Name:                sname,
			StructureID:         sid,
			Distance:            dist,
			Direction:           cardinalDirection(r.CurrentX, r.CurrentY, x, y),
			RecoveryQualitative: fmt.Sprintf("%s from %s", strings.ToLower(label), vendorName),
			MagnitudeNote:       fmt.Sprintf("-%d tiredness immediate", amount),
			CostText:            costText,
			DistanceCaveat:      distanceCaveat(dist),
		})
	}
	return out
}

// loadOwnTirednessStockLine returns an inventory-style line summarizing
// the actor's own carry of tiredness-satisfying consumables, or "" if
// they're carrying none. Format mirrors satiation's own-stock line so
// a tired actor with tea on hand reads "You have 2 coca tea in your
// stock — consume to drink." next to the spatial recovery list.
func (app *App) loadOwnTirednessStockLine(ctx context.Context, actorID string) string {
	rows, err := app.DB.Query(ctx, `
		SELECT ik.display_label, inv.quantity
		  FROM actor_inventory inv
		  JOIN item_kind ik ON ik.name = inv.item_kind
		  JOIN item_satisfies isf ON isf.item_kind = ik.name
		 WHERE inv.actor_id = $1::uuid
		   AND inv.quantity > 0
		   AND isf.attribute = 'tiredness'
		 ORDER BY isf.amount DESC, ik.display_label
	`, actorID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var fragments []string
	for rows.Next() {
		var label string
		var qty int
		if err := rows.Scan(&label, &qty); err != nil {
			continue
		}
		fragments = append(fragments, fmt.Sprintf("%d %s", qty, strings.ToLower(label)))
	}
	if len(fragments) == 0 {
		return ""
	}
	return "You have " + strings.Join(fragments, ", ") + " in your stock — consume to drink."
}

// loadHomeRestSpot resolves the actor's home structure into a
// restSpot. Annotation explains why the option is being shown given
// the time of day.
func (app *App) loadHomeRestSpot(ctx context.Context, r *agentNPCRow, onShift bool, isCritical bool) *restSpot {
	if !r.HomeStructureID.Valid {
		return nil
	}
	var name string
	var x, y float64
	if err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.display_name, a.name), o.x, o.y
		   FROM village_object o
		   JOIN asset a ON a.id = o.asset_id
		  WHERE o.id = $1`,
		r.HomeStructureID.String,
	).Scan(&name, &x, &y); err != nil {
		return nil
	}
	const tileSize = 32.0
	dx := (x - r.CurrentX) / tileSize
	dy := (y - r.CurrentY) / tileSize
	dist := math.Sqrt(dx*dx + dy*dy)

	annotation := ""
	if onShift && isCritical {
		annotation = "would mean leaving your shift early"
	}
	return &restSpot{
		Name:                name,
		StructureID:         r.HomeStructureID.String,
		Distance:            dist,
		Direction:           cardinalDirection(r.CurrentX, r.CurrentY, x, y),
		RecoveryQualitative: "a full sleep at home",
		Annotation:          annotation,
		CostText:            "free",
		DistanceCaveat:      distanceCaveat(dist),
	}
}

// loadInnRestSpots returns nearby lodging-tagged structures that
// have at least one keeper who has previously sold nights_stay. Two
// filters stack: (a) the structure carries a 'lodging' tag in
// village_object_tag — rules out non-lodging structures whose
// work-actor happens to have a stray nights_stay row from some edge
// case (Tavern carries 'lodging' alongside 'tavern' as of ZBBS-180);
// (b) at least one accepted nights_stay row exists from an actor
// whose work_structure_id matches — rules out decorative tavern-
// tagged placements that nobody actually runs as lodging. DISTINCT
// ON dedupes structures with multiple keepers so each inn surfaces
// once. Sorted by distance, capped at limit.
//
// Cost is per-buyer-per-seller via lastPaidPrice — the actor only
// "knows" the price if they've personally bought a night before. New
// arrivals see "you don't know what they charge." When multiple
// keepers work the same inn, lastPaidPrice is queried for the most-
// recent seller the buyer has interacted with at this structure
// (DISTINCT ON ordering favors the actor's prior counterparty when
// available).
func (app *App) loadInnRestSpots(ctx context.Context, r *agentNPCRow, limit int, onShift bool, isCritical bool) []restSpot {
	rows, err := app.DB.Query(ctx,
		`SELECT structure_id, name, x, y, seller_id
		   FROM (
		       SELECT DISTINCT ON (vo.id)
		              vo.id::text                                  AS structure_id,
		              COALESCE(vo.display_name, asset.name)        AS name,
		              vo.x                                         AS x,
		              vo.y                                         AS y,
		              a.id::text                                   AS seller_id
		         FROM village_object vo
		         JOIN asset             asset ON asset.id = vo.asset_id
		         JOIN village_object_tag vot ON vot.object_id = vo.id
		                                    AND vot.tag = 'lodging'
		         JOIN actor              a   ON a.work_structure_id = vo.id
		         JOIN pay_ledger         pl  ON pl.seller_id = a.id
		                                    AND pl.item_kind = 'nights_stay'
		                                    AND pl.state     = 'accepted'
		        ORDER BY vo.id,
		                 (CASE WHEN pl.buyer_id = $4 THEN 0 ELSE 1 END),
		                 pl.created_at DESC
		   ) inns
		  ORDER BY (x - $1) * (x - $1) + (y - $2) * (y - $2)
		  LIMIT $3`,
		r.CurrentX, r.CurrentY, limit, r.ID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type innRow struct {
		structureID, name, sellerID string
		x, y                        float64
	}
	var raw []innRow
	for rows.Next() {
		var ir innRow
		if err := rows.Scan(&ir.structureID, &ir.name, &ir.x, &ir.y, &ir.sellerID); err != nil {
			continue
		}
		raw = append(raw, ir)
	}
	if err := rows.Err(); err != nil {
		return nil
	}

	var out []restSpot
	for _, ir := range raw {
		const tileSize = 32.0
		dx := (ir.x - r.CurrentX) / tileSize
		dy := (ir.y - r.CurrentY) / tileSize
		dist := math.Sqrt(dx*dx + dy*dy)

		costText := "you don't know what they charge"
		if amount, paidAt, ok, _ := app.lastPaidPrice(ctx, r.ID, ir.sellerID, "nights_stay"); ok {
			costText = fmt.Sprintf("you paid %d coins last time, %s", amount, daysAgoPhrase(paidAt))
		}
		annotation := ""
		if onShift && isCritical {
			annotation = "would mean leaving your shift early"
		}
		out = append(out, restSpot{
			Name:                ir.name,
			StructureID:         ir.structureID,
			Distance:            dist,
			Direction:           cardinalDirection(r.CurrentX, r.CurrentY, ir.x, ir.y),
			RecoveryQualitative: "a room for the night",
			Annotation:          annotation,
			CostText:            costText,
			DistanceCaveat:      distanceCaveat(dist),
		})
	}
	return out
}

// daysAgoPhrase renders a past timestamp as a human-readable distance
// in days. Capped at "weeks ago" so a year-old purchase doesn't
// surface as a precise day count the LLM would over-anchor on.
func daysAgoPhrase(t time.Time) string {
	d := time.Since(t)
	hours := int(d.Hours())
	switch {
	case hours < 24:
		return "earlier today"
	case hours < 48:
		return "yesterday"
	case hours < 24*7:
		return fmt.Sprintf("%d days ago", hours/24)
	case hours < 24*21:
		return fmt.Sprintf("%d weeks ago", hours/(24*7))
	default:
		return "a while ago"
	}
}
