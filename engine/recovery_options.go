package main

// Recovery options perception (ZBBS-172).
//
// When a tired NPC's tick fires, the perception used to surface
// "Address now: tiredness" with no spatial information. The fix to
// salem-tiredness-recovery-without-lodging adds a "Recovery options"
// block that lists the actor's real choices — outdoor rest spots, the
// actor's home (if owned), nearby inns — annotated with distance,
// cost (recalled from prior purchases), and time-of-day appropriateness.
//
// The block exists to force a real tradeoff. A tired actor weighs:
//
//   - Walk far to a free spot and absorb the round-trip movement
//     fatigue, vs.
//   - Spend coins at the inn for proper sleep, vs.
//   - Power through and stay at work.
//
// Time-of-day predicates filter out inappropriate options. On-shift
// vendors don't see "go home and sleep" (which would mean abandoning
// the shift), unless tiredness has reached critical (90% of needMax),
// at which point all gates lift and the LLM gets to decide whether
// the work hours are worth pushing through.
//
// Cost is never surfaced as ground truth from the inn's price config —
// NPCs only know what they've personally paid for. lastPaidPrice
// (pay_history.go) returns the buyer's most recent accepted price for
// this seller × item_kind. No history → "you don't know what they
// charge." Knowledge of price is earned by patronage.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// restSpot is one entry in the recovery options list.
type restSpot struct {
	Name              string
	StructureID       string
	Distance          float64 // tiles
	Direction         string  // "north", "northeast", etc.
	RecoveryQualitative string // "a brief easing", "a proper rest", "full sleep"
	Annotation        string  // "" or "would mean leaving your shift early" etc.
	CostText          string  // "free", "you paid 5 coins last time, three days ago", "you don't know what they charge"
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

	if len(free) == 0 && home == nil && len(inns) == 0 {
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

	lines = append(lines, "Recovery options nearby:")
	for _, s := range free {
		lines = append(lines, "  - "+formatRestSpot(s))
	}
	if home != nil {
		lines = append(lines, "  - "+formatRestSpot(*home))
	}
	for _, s := range inns {
		lines = append(lines, "  - "+formatRestSpot(s))
	}
	return strings.Join(lines, "\n")
}

// formatRestSpot composes one bullet line from a restSpot.
func formatRestSpot(s restSpot) string {
	parts := []string{fmt.Sprintf("%s (%s %s)", s.Name, qualitativeDistance(s.Distance), s.Direction)}
	parts = append(parts, "— "+s.RecoveryQualitative)
	if s.CostText != "" {
		parts = append(parts, ", "+s.CostText)
	}
	if s.Annotation != "" {
		parts = append(parts, " — "+s.Annotation)
	}
	return strings.Join(parts, " ")
}

// qualitativeDistance maps a tile distance to a phrase the perception
// can render without surfacing a number. The "long walk" tier
// explicitly flags the round-trip fatigue cost so the LLM weighs it.
func qualitativeDistance(tiles float64) string {
	switch {
	case tiles <= 5:
		return "a short walk"
	case tiles <= 15:
		return "a fair walk"
	case tiles <= 30:
		return "a long walk"
	default:
		return "a long walk — fatigue cost both ways"
	}
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
// strong arrival hits or strong dwell read as "a proper rest",
// otherwise "a brief easing".
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
			CostText:            "free",
		})
	}
	return out
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
	}
}

// loadInnRestSpots returns nearby tavern/inn structures that have at
// least one tavernkeeper who has previously sold nights_stay. The
// "has sold" filter avoids surfacing decorative tavern-tagged
// placements that nobody actually runs as lodging. Sorted by distance.
//
// Cost is per-buyer-per-seller via lastPaidPrice — the actor only
// "knows" the price if they've personally bought a night before. New
// arrivals see "you don't know what they charge."
func (app *App) loadInnRestSpots(ctx context.Context, r *agentNPCRow, limit int, onShift bool, isCritical bool) []restSpot {
	rows, err := app.DB.Query(ctx,
		`WITH lodging_sellers AS (
		     SELECT DISTINCT a.work_structure_id, a.id AS seller_id
		       FROM actor a
		       JOIN pay_ledger pl
		         ON pl.seller_id = a.id
		      WHERE a.work_structure_id IS NOT NULL
		        AND pl.item_kind = 'nights_stay'
		        AND pl.state     = 'accepted'
		 )
		 SELECT vo.id::text,
		        COALESCE(vo.display_name, asset.name),
		        vo.x, vo.y,
		        ls.seller_id::text
		   FROM village_object vo
		   JOIN asset asset       ON asset.id = vo.asset_id
		   JOIN lodging_sellers ls ON ls.work_structure_id = vo.id
		  ORDER BY (vo.x - $1) * (vo.x - $1) + (vo.y - $2) * (vo.y - $2)
		  LIMIT $3`,
		r.CurrentX, r.CurrentY, limit,
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
