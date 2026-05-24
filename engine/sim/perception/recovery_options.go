package perception

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// recovery_options.go — ZBBS-HOME-297. The "## How you can rest" perception
// section: surfaces, to a tired or homeless NPC, free tiredness-bearing
// objects (shade trees) and inns where they could rent a room. Port of v1's
// engine/recovery_options.go, shelter-first scope — free rest + inns only;
// remedy-vendors, the home option, and the own-stock line are additive
// follow-ons that reuse this same bullet plumbing.
//
// The homeless branch (no HomeStructureID) fires every tick — it is the
// bootstrap cue that drives a homeless NPC to an inn to book its first room
// (ZBBS-HOME-296 option B). The free-rest finder is also the §8 shade-tree
// fallback finder HOME-296 reuses. Purely additive perception: the NPC acts
// via the existing move_to + pay_with_item tools.

const recoveryTirednessNeed = sim.NeedKey("tiredness")
const nightsStayItem = sim.ItemKind("nights_stay")

// RecoveryOptionsView is the content-gated "## How you can rest" section.
// A nil view (or empty Options) means render omits the section.
type RecoveryOptionsView struct {
	Options []RecoveryOption
}

// RecoveryOption is one rest-affordance bullet.
type RecoveryOption struct {
	Kind      string // "rest" (free object) | "inn" | "remedy" (vendor consumable)
	Label     string // "the old oak" | "Hannah's Inn" | the vendor's workplace
	ItemLabel string // remedy only: the consumable's display label ("coca tea"); "" otherwise
	Magnitude int    // tiredness eased (positive); 0/unused for inns
	CostText  string // "free" | "~28 coins" | "ask the keeper"
	Distance  string // qualitative ("a short walk"); "" when unknown (inns, remedies)
	Direction string // cardinal ("northeast"); "" when unknown (inns, remedies)

	// sortKey is the actor→option tile distance used to order bullets
	// (nearest first). Unexported — never rendered. Inns have no reliable
	// distance (grid vs pixel space) so they sort last via a large key.
	sortKey float64

	// sourceKey is the originating object/structure ID — a stable final
	// tie-breaker so equal-distance, equal-label options order
	// deterministically (snap.VillageObjects / snap.Structures are maps).
	// Unexported — never rendered.
	sourceKey string
}

// innSortKey parks inns after all distance-bearing rest spots, since their
// distance isn't computed in the shelter-first MVP.
const innSortKey = math.MaxFloat64

// buildRecoveryOptions builds the rest-options view for actorSnap, or nil
// when the firing gate doesn't pass or no options exist. Pure over the
// snapshot (no live-world reads). ZBBS-HOME-297.
func buildRecoveryOptions(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *RecoveryOptionsView {
	if snap == nil || actorSnap == nil {
		return nil
	}

	// Firing gate: tired (tiredness at/over the red threshold) OR homeless
	// (no home structure). The homeless arm fires every tick regardless of
	// tiredness — that's the lodging bootstrap cue. The tired arm reads the
	// configured red threshold off the snapshot (NeedThresholds.Get falls
	// back to the default when unset) so this satisfier-cue fires on the same
	// boundary as the need-threshold producer's warrant — if an admin tunes
	// tiredness_red_threshold, the warrant and the rest options stay in sync
	// rather than leaving a gap where the NPC is told "you're tired" with no
	// options.
	tired := actorSnap.Needs[recoveryTirednessNeed] >= snap.NeedThresholds.Get(recoveryTirednessNeed)
	homeless := actorSnap.HomeStructureID == ""
	if !tired && !homeless {
		return nil
	}

	var opts []RecoveryOption
	opts = append(opts, gatherFreeRestSpots(snap, actorSnap)...)
	opts = append(opts, gatherInnRestSpots(snap, actorID)...)
	// Consumable remedies are tiredness-gated, NOT homeless-gated: a not-yet-
	// tired homeless actor surveying where to shelter doesn't need stimulant-
	// brew prompts. Mirrors v1's "brews stay tiredness-gated since they're
	// maintenance, not shelter."
	if tired {
		opts = append(opts, gatherConsumableRemedies(snap, actorID)...)
	}
	if len(opts) == 0 {
		return nil
	}

	// Nearest first; ties (and the no-distance inns) broken by label for
	// deterministic output.
	sort.SliceStable(opts, func(i, j int) bool {
		if opts[i].sortKey != opts[j].sortKey {
			return opts[i].sortKey < opts[j].sortKey
		}
		if opts[i].Label != opts[j].Label {
			return opts[i].Label < opts[j].Label
		}
		return opts[i].sourceKey < opts[j].sourceKey
	})
	return &RecoveryOptionsView{Options: opts}
}

// gatherFreeRestSpots returns a "rest" option for each village object that
// eases tiredness on arrival (e.g. a shade tree's negative-tiredness
// refresh), skipping objects whose finite supply is exhausted. Distance and
// direction are computed in INTERNAL-GRID TILE space: actor CurrentX/Y are
// already padded tile indices, while VillageObject X/Y are world pixels, so
// the object is converted to the same tile space via WorldToTile (the
// conversion pathfinding and the structure anchors use) before measuring.
// This also drives the §8 nearest-object selection.
func gatherFreeRestSpots(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) []RecoveryOption {
	ax := float64(actorSnap.Pos.X)
	ay := float64(actorSnap.Pos.Y)
	var out []RecoveryOption
	for _, obj := range snap.VillageObjects {
		if obj == nil {
			continue
		}
		mag := tirednessRefreshMagnitude(obj)
		if mag <= 0 {
			continue
		}
		// Actor coords are padded internal-grid tiles; object coords are world
		// pixels. Convert the object to the same tile space before measuring —
		// subtracting pixels from tiles is off by ~TileSize (the HOME-297 unit
		// bug ZBBS-WORK flagged 2026-05-23). WorldToTile applies the same Pad
		// offset CurrentX already carries, so the two are directly comparable.
		objTile := obj.Pos.Tile()
		tx := float64(objTile.X)
		ty := float64(objTile.Y)
		dx := tx - ax
		dy := ty - ay
		distTiles := math.Sqrt(dx*dx + dy*dy)
		out = append(out, RecoveryOption{
			Kind:      "rest",
			Label:     objectLabel(obj),
			Magnitude: mag,
			CostText:  "free",
			Distance:  qualitativeDistance(distTiles),
			Direction: cardinalDirection(ax, ay, tx, ty),
			sortKey:   distTiles,
			sourceKey: string(obj.ID),
		})
	}
	return out
}

// tirednessRefreshMagnitude returns the positive tiredness eased by arriving
// at obj — the negated arrival decrement plus any dwell delta — or 0 if the
// object doesn't ease tiredness or its finite supply is exhausted.
func tirednessRefreshMagnitude(obj *sim.VillageObject) int {
	for _, r := range obj.Refreshes {
		if r == nil || r.Attribute != recoveryTirednessNeed {
			continue
		}
		if r.IsFinite() && r.AvailableQuantity != nil && *r.AvailableQuantity <= 0 {
			continue
		}
		mag := -r.Amount // Amount is the negative arrival decrement
		if r.DwellDelta != nil {
			mag += -*r.DwellDelta
		}
		if mag < 0 {
			mag = 0
		}
		return mag
	}
	return 0
}

func objectLabel(obj *sim.VillageObject) string {
	if obj.DisplayName != "" {
		return obj.DisplayName
	}
	return "a resting spot"
}

// gatherInnRestSpots returns an "inn" option for each structure that has a
// private bedroom — the same lodging gate DeliverOrder uses. Cost is the
// actor's last-paid nights_stay price with the inn's keeper, else "ask the
// keeper". Distance/direction are omitted: Structure.Position is grid space
// (vs. the pixel space of actors/objects) and inn distance is pure flavor —
// the grid->pixel conversion is an additive follow-on (see HOME-297 design).
func gatherInnRestSpots(snap *sim.Snapshot, actorID sim.ActorID) []RecoveryOption {
	var out []RecoveryOption
	for id, s := range snap.Structures {
		if s == nil || !hasPrivateRoom(s) {
			continue
		}
		keeperID := keeperOf(snap, id)
		if keeperID == "" {
			// No keeper to pay or ask — the "rent a room" cue would be
			// unactionable (the booking is a pay_with_item targeting the
			// keeper). Don't advertise a roomless-of-keeper inn. (code_review)
			continue
		}
		out = append(out, RecoveryOption{
			Kind:      "inn",
			Label:     innLabel(s),
			CostText:  innCostText(snap, actorID, keeperID),
			sortKey:   innSortKey,
			sourceKey: string(id),
		})
	}
	return out
}

// gatherConsumableRemedies returns a "remedy" option per (vendor, item) for
// NPCs who hold a tiredness-easing consumable and have a workplace to buy it
// at. v2 has no standing "vendor" capability — v1's serve-tool attribute is
// gone, and sales run through the buyer's pay_with_item against a co-present
// seller — so vendorship is inferred structurally here: an NPC stationed at a
// WorkStructureID who carries a tiredness item is treated as selling it there.
//
// The cue is surfaced at the vendor's WORKPLACE, not their current location
// (ZBBS-HOME-299 decision): a stable "this is where tea is sold" signal rather
// than a momentary-presence claim. It therefore carries NO transient
// break/sleep/shift gate — the buyer walks to the workplace and availability is
// resolved on arrival by the transaction layer (pay_with_item co-presence +
// AcceptPay's seller-break gate). Distance/direction are omitted for the same
// reason inns omit them (Structure.Position is grid space, not the actor's tile
// space), so remedies park after the distance-bearing free rest spots.
//
// PCs are excluded as vendors — they don't sell through the NPC commerce path.
// A vendor whose WorkStructureID doesn't resolve to a structure in the snapshot
// is skipped: the "buy at X" cue would name an unactionable destination.
func gatherConsumableRemedies(snap *sim.Snapshot, actorID sim.ActorID) []RecoveryOption {
	if len(snap.ItemKinds) == 0 {
		return nil
	}
	var out []RecoveryOption
	for vendorID, vendor := range snap.Actors {
		if vendor == nil || vendorID == actorID || vendor.Kind == sim.KindPC {
			continue
		}
		if vendor.WorkStructureID == "" {
			continue
		}
		st := snap.Structures[vendor.WorkStructureID]
		if st == nil {
			continue
		}
		for kind, qty := range vendor.Inventory {
			if qty <= 0 {
				continue
			}
			mag := tirednessRemedyMagnitude(snap, kind)
			if mag <= 0 {
				continue
			}
			out = append(out, RecoveryOption{
				Kind:      "remedy",
				Label:     remedyStructureLabel(st),
				ItemLabel: itemDisplayLabel(snap, kind),
				Magnitude: mag,
				CostText:  buyerLastPaidText(snap, actorID, vendorID, kind, "ask the seller"),
				sortKey:   innSortKey,
				sourceKey: string(vendorID) + ":" + string(kind),
			})
		}
	}
	return out
}

// tirednessRemedyMagnitude returns the immediate tiredness a unit of kind eases
// per the item catalog, or 0 when the kind is unknown or eases no tiredness on
// the immediate hit. Pure slow-burn items (Immediate==0, dwell-only) are not
// surfaced as "buy and drink now" remedies in the MVP.
//
// First-match is correct: ItemKindDef.Satisfies holds at most one entry per
// attribute (the v1 item_satisfies PK is (item_kind, attribute), enforced at
// load — see ItemKindDef.Satisfies), so there is no second tiredness entry to
// stack or out-rank.
func tirednessRemedyMagnitude(snap *sim.Snapshot, kind sim.ItemKind) int {
	def := snap.ItemKinds[kind]
	if def == nil {
		return 0
	}
	for _, s := range def.Satisfies {
		if s.Attribute == recoveryTirednessNeed {
			return s.Immediate
		}
	}
	return 0
}

// itemDisplayLabel resolves a consumable's human label from the catalog,
// falling back to the raw kind when unknown or unlabeled.
func itemDisplayLabel(snap *sim.Snapshot, kind sim.ItemKind) string {
	if def := snap.ItemKinds[kind]; def != nil && def.DisplayLabel != "" {
		return def.DisplayLabel
	}
	return string(kind)
}

// remedyStructureLabel names the workplace where a remedy is bought, with a
// generic fallback when the structure has no display name.
func remedyStructureLabel(s *sim.Structure) string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return "the shop"
}

func hasPrivateRoom(s *sim.Structure) bool {
	for _, r := range s.Rooms {
		if r != nil && r.Kind == sim.RoomKindPrivate {
			return true
		}
	}
	return false
}

func innLabel(s *sim.Structure) string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return "the inn"
}

// keeperOf returns the actor working at structureID (its keeper), or "" when
// none is present. When multiple actors work there, the lexicographically
// smallest ID is chosen so the resolved keeper — and thus the price-book cost
// text — is deterministic across runs (snap.Actors is a map). (code_review)
func keeperOf(snap *sim.Snapshot, structureID sim.StructureID) sim.ActorID {
	var ids []string
	for id, a := range snap.Actors {
		if a != nil && a.WorkStructureID == structureID {
			ids = append(ids, string(id))
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return sim.ActorID(ids[0])
}

// innCostText renders the actor's last-paid nights_stay price with this
// keeper, else "ask the keeper".
func innCostText(snap *sim.Snapshot, actorID, keeperID sim.ActorID) string {
	return buyerLastPaidText(snap, actorID, keeperID, nightsStayItem, "ask the keeper")
}

// buyerLastPaidText renders "~N coins" from the buyer's most-recent accepted
// price for (seller, item) in the snapshot's PriceBook, else fallback.
// Replicates World.LookupBuyerLastPaid against the snapshot (perception runs
// off the world goroutine, so it must read Snapshot.PriceBook, not the live
// accessor). Price knowledge is per-buyer: a buyer who has never bought this
// item from this seller gets the fallback — patronage earns the number, the
// same convention v1 used for both inns and remedy vendors.
func buyerLastPaidText(snap *sim.Snapshot, buyerID, sellerID sim.ActorID, item sim.ItemKind, fallback string) string {
	if sellerID == "" || snap.PriceBook == nil {
		return fallback
	}
	buf, ok := snap.PriceBook[sim.PriceBookKey{SellerID: sellerID, Item: item}]
	if !ok || buf == nil || buf.Len() == 0 {
		return fallback
	}
	entries := buf.Snapshot() // oldest-first; scan from the end for newest-first
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].BuyerID == buyerID {
			return fmt.Sprintf("~%d coins", entries[i].Amount)
		}
	}
	return fallback
}

// qualitativeDistance maps a tile distance to a benefit-first walk phrase.
func qualitativeDistance(tiles float64) string {
	switch {
	case tiles < 3:
		return "right nearby"
	case tiles < 8:
		return "a short walk"
	case tiles < 20:
		return "a fair walk"
	default:
		return "a long walk"
	}
}

// cardinalDirection returns an 8-point compass bearing from (fromX,fromY) to
// (toX,toY) in a single consistent coordinate space — callers pass tile coords
// (+x east, +y south, the same axis orientation as world pixels). The bearing
// is scale-free, so only the from/to consistency matters. Empty when coincident.
func cardinalDirection(fromX, fromY, toX, toY float64) string {
	dx := toX - fromX
	dy := toY - fromY
	if dx == 0 && dy == 0 {
		return ""
	}
	// Screen/world pixels use +y = south; negating dy converts to math-angle
	// space where +90 degrees is north. So a target with larger Y correctly
	// reads as "south" and smaller Y as "north" in the table below.
	angle := math.Atan2(-dy, dx) * 180 / math.Pi
	if angle < 0 {
		angle += 360
	}
	dirs := []string{"east", "northeast", "north", "northwest", "west", "southwest", "south", "southeast"}
	return dirs[int((angle+22.5)/45)%8]
}

// renderRecoveryOptions writes the "## How you can rest" section. Content-
// gated: nil/empty view writes nothing. Benefit-first bullets.
func renderRecoveryOptions(b *strings.Builder, v *RecoveryOptionsView) {
	if v == nil || len(v.Options) == 0 {
		return
	}
	b.WriteString("## How you can rest\n")
	for _, o := range v.Options {
		b.WriteString("- ")
		b.WriteString(sanitizeInline(o.Label))
		switch o.Kind {
		case "inn":
			b.WriteString(" — rent a room")
		case "remedy":
			fmt.Fprintf(b, " — buy %s", sanitizeInline(o.ItemLabel))
			if o.Magnitude > 0 {
				fmt.Fprintf(b, ", eases tiredness (~%d)", o.Magnitude)
			}
		default:
			if o.Magnitude > 0 {
				fmt.Fprintf(b, " — eases tiredness (~%d)", o.Magnitude)
			}
		}
		if o.CostText != "" {
			fmt.Fprintf(b, ", %s", o.CostText)
		}
		if o.Distance != "" {
			fmt.Fprintf(b, ", %s", o.Distance)
			if o.Direction != "" {
				fmt.Fprintf(b, " %s", o.Direction)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}
