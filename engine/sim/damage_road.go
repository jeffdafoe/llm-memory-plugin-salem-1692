package sim

// damage_road.go — public works slice 3 (LLM-677): a tree comes down across a
// road, and stays there until a hand clears it.
//
// Unlike a well or a business, the damaged thing does not exist before the
// break: the break PLACES a road obstacle — one of the catalog's fallen-tree
// assets, picked at random — and the repair DELETES it. The obstacle is the
// damage: it carries TagRoadObstacle, DamagedAt is stamped as it lands, and
// PublicWorksKind reads it as PublicWorksRoad, so every public-works surface
// (bounty, cue, boards, ticker, PC thought) covers it through the same loop.
//
// The effect is the detour. Both assets are obstacles with a footprint the
// width and height of the drawn log (migration LLM-677-road-damage), and the
// walk grid is rebuilt from live objects on every locomotion tick, so walkers
// route around the log on the next tick with no pathfinder change. Nobody is
// slowed; they walk the long way.
//
// Where it lands (roadObstacleSites): the log must CUT a road — the road tiles
// on its sides no longer join through road once it is down — while open ground
// all around it keeps a way past. That takes north-south roads up to three tiles
// wide, east-west roads up to two, and bends alike (the art lies on a slant).
// Water anywhere near rules a site out, so a bridge never gets one; so does a
// building, fence or any other obstacle, or an object already standing there.
// The site must be within damageSiteLandmarkTiles of a named building — the
// obstacle is named by it ("Fallen log by the Cole Residence") and lies where
// people walk.

import (
	"errors"
	"log"
	"strings"
	"time"
)

// ErrNoRoadSite — no road tile qualifies for an obstacle just now, or the
// catalog carries neither fallen-tree asset.
var ErrNoRoadSite = errors.New("no road site can take a fallen tree just now")

// PublicWorksRoad is the damage kind of a road obstacle.
const PublicWorksRoad = "road"

// TagRoadObstacle marks a placed road obstacle — the object that IS the road's
// damage. Instance tag, checkpointed with the placement.
const TagRoadObstacle = "road_obstacle"

// roadObstacleAsset is one fallen-tree asset a road break may place, with the
// name the placement carries (the catalog names are the editor's).
type roadObstacleAsset struct {
	ID   AssetID
	Name string
}

// The catalog's fallen trees. Their ids are fixed in the live catalog; a
// catalog without them (a test world, a fresh DB) places nothing.
const (
	FallenLogAssetID  AssetID = "1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5"
	FallenTreeAssetID AssetID = "6e894192-421d-45a0-b67c-d34d8f0f5885"
)

// roadObstacleAssets are the fallen trees a road break picks from at random.
var roadObstacleAssets = []roadObstacleAsset{
	{ID: FallenLogAssetID, Name: "Fallen log"},
	{ID: FallenTreeAssetID, Name: "Fallen tree"},
}

// Defaults for the live-tunable road damage settings.
const (
	// DefaultRoadDamageChancePermille is the daily chance, per thousand, that a
	// tree comes down across a road. 0 disables the daily roll.
	DefaultRoadDamageChancePermille = 20
	// DefaultRoadDamageStormChancePermille is the chance, per thousand, that a
	// tree comes down when a storm starts — storms are what fell trees.
	DefaultRoadDamageStormChancePermille = 300
	// DefaultRoadDamageMinGapHours is the quiet time after a clearing before
	// another tree may come down.
	DefaultRoadDamageMinGapHours = 24
	// DefaultPublicWorksRoadBounty is what the chest pays to clear a road.
	DefaultPublicWorksRoadBounty = 10
	// DefaultPublicWorksRoadRepairSeconds is how long clearing a road takes.
	DefaultPublicWorksRoadRepairSeconds = 3600
)

// roadObstacleClearance is how many tiles of open, dry, walkable ground a site
// needs on every side of the log's footprint — the room the detour walks
// through, and the distance kept from buildings, fences and water.
const roadObstacleClearance = 2

// IsRoadObstacle reports whether o is a placed road obstacle. Nil-safe.
func (o *VillageObject) IsRoadObstacle() bool {
	return o != nil && o.HasTag(TagRoadObstacle)
}

// AtRoadObstacle reports whether an actor at pos stands at the obstacle: within
// LoiterAttributionTiles of its loiter pin, the tile move_to walks a hand to
// (south of the log). Pure, so StartRepair and the cue agree.
func AtRoadObstacle(pos TilePos, obj *VillageObject, asset *Asset) bool {
	if obj == nil || asset == nil {
		return false
	}
	return pos.Chebyshev(computeLoiterTile(obj, asset)) <= LoiterAttributionTiles
}

// rollRoadDamage rolls once for a tree coming down across a road. The guards
// are the other kinds': chance 0 is off, one obstacle at a time, and none
// within the min gap of the last clearing. Returns the placed obstacle, or nil.
func rollRoadDamage(w *World, trigger string, permille int, rng DamageRoller, now time.Time) *VillageObject {
	if w == nil || permille <= 0 || rng == nil {
		return nil
	}
	if anyDamaged(w, PublicWorksRoad) {
		return nil
	}
	if gap, last := w.Settings.RoadDamageMinGapHours, w.Environment.LastRoadRepairAt; gap > 0 && !last.IsZero() && now.Sub(last) < time.Duration(gap)*time.Hour {
		return nil
	}
	if rng.Float64() >= float64(permille)/1000 {
		return nil
	}
	obj, err := placeRoadObstacle(w, trigger, rng, now)
	if err != nil {
		log.Printf("sim/damage: a tree should have come down (%s) but %v", trigger, err)
		return nil
	}
	return obj
}

// placeRoadObstacle brings a tree down across a road now: picks a fallen-tree
// asset and a qualifying site at random, places it named by its landmark, and
// damages it — which stamps DamagedAt, logs, emits ObjectDamaged and reposts
// the boards.
func placeRoadObstacle(w *World, trigger string, rng DamageRoller, now time.Time) (*VillageObject, error) {
	var choices []roadObstacleAsset
	for _, c := range roadObstacleAssets {
		if a := w.Assets[c.ID]; a != nil && a.IsObstacle {
			choices = append(choices, c)
		}
	}
	if len(choices) == 0 {
		return nil, ErrNoRoadSite
	}
	choice := choices[pickIndex(rng, len(choices))]
	asset := w.Assets[choice.ID]
	grid, err := buildWalkGrid(w)
	if err != nil {
		return nil, err
	}
	sites := roadObstacleSites(w, grid, asset)
	if len(sites) == 0 {
		return nil, ErrNoRoadSite
	}
	site := sites[pickIndex(rng, len(sites))]
	obj := placeVillageObject(w, choice.ID, asset, TileToWorld(site), "", "", asset.DefaultState)
	obj.Tags = []string{TagRoadObstacle}
	obj.DisplayName = choice.Name
	w.emit(&VillageObjectDisplayNameChanged{ObjectID: obj.ID, DisplayName: obj.DisplayName, At: now})
	damageObject(w, obj, trigger, now)
	return obj, nil
}

// pickIndex turns one roll into an index in [0, n).
func pickIndex(rng DamageRoller, n int) int {
	i := int(rng.Float64() * float64(n))
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// roadObstacleSites lists every anchor tile where asset may come down across a
// road, in row-major order. A site qualifies when:
//
//   - the anchor tile is road (dirt or cobblestone);
//   - the footprint plus roadObstacleClearance on every side is walkable in the
//     current grid and dry — open ground for the detour, and nothing built,
//     fenced or wet close by (a bridge is over water, so it never qualifies);
//   - no other placed object stands on the footprint;
//   - no actor stands on the footprint (nobody is felled);
//   - the log cuts the road: the road tiles bordering the footprint fall into
//     at least two groups that no longer join through road inside the
//     clearance box;
//   - a named building is within damageSiteLandmarkTiles.
//
// The whole clearance box is walkable in the grid, so a way past the log
// always exists.
func roadObstacleSites(w *World, grid *WalkGrid, asset *Asset) []TilePos {
	if w.Terrain == nil || len(w.Terrain.Data) != MapW*MapH || asset == nil {
		return nil
	}
	isRoad := func(x, y int) bool {
		if x < 0 || x >= MapW || y < 0 || y >= MapH {
			return false
		}
		b := w.Terrain.Data[y*MapW+x]
		return b == TerrainDirt || b == TerrainCobblestone
	}
	isOpen := func(x, y int) bool {
		if !grid.CanWalk(x, y) {
			return false
		}
		b := w.Terrain.Data[y*MapW+x]
		return b != TerrainShallowWater && b != TerrainDeepWater
	}
	occupied := make(map[TilePos]struct{})
	for _, o := range w.VillageObjects {
		if o != nil && o.AttachedTo == "" {
			occupied[o.Pos.Tile()] = struct{}{}
		}
	}
	for _, a := range w.Actors {
		if a != nil {
			occupied[a.Pos] = struct{}{}
		}
	}
	landmarks := namedStructureTiles(w)

	var out []TilePos
	for y := 0; y < MapH; y++ {
		for x := 0; x < MapW; x++ {
			if !isRoad(x, y) {
				continue
			}
			fx0, fx1 := x-asset.FootprintLeft, x+asset.FootprintRight
			fy0, fy1 := y-asset.FootprintTop, y+asset.FootprintBottom
			bx0, bx1 := fx0-roadObstacleClearance, fx1+roadObstacleClearance
			by0, by1 := fy0-roadObstacleClearance, fy1+roadObstacleClearance
			inFootprint := func(tx, ty int) bool { return tx >= fx0 && tx <= fx1 && ty >= fy0 && ty <= fy1 }
			inBox := func(tx, ty int) bool { return tx >= bx0 && tx <= bx1 && ty >= by0 && ty <= by1 }
			if !boxAll(bx0, bx1, by0, by1, isOpen) {
				continue
			}
			if footprintOccupied(fx0, fx1, fy0, fy1, occupied) {
				continue
			}
			if roadGroupsAround(fx0, fx1, fy0, fy1, inFootprint, inBox, isRoad) < 2 {
				continue
			}
			if !landmarkWithin(TilePos{X: x, Y: y}, landmarks, damageSiteLandmarkTiles) {
				continue
			}
			out = append(out, TilePos{X: x, Y: y})
		}
	}
	return out
}

// boxAll reports whether ok holds for every tile of the box.
func boxAll(x0, x1, y0, y1 int, ok func(x, y int) bool) bool {
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if !ok(tx, ty) {
				return false
			}
		}
	}
	return true
}

// footprintOccupied reports whether any footprint tile holds an object anchor
// or an actor.
func footprintOccupied(x0, x1, y0, y1 int, occupied map[TilePos]struct{}) bool {
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if _, ok := occupied[TilePos{X: tx, Y: ty}]; ok {
				return true
			}
		}
	}
	return false
}

var cardinalSteps = [4]TilePos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}}

// roadGroupsAround counts the groups the road bordering the footprint falls
// into once the footprint is blocked: road tiles next to the footprint, joined
// through road tiles inside the box but outside the footprint. Two or more
// means the log cuts the road.
func roadGroupsAround(fx0, fx1, fy0, fy1 int, inFootprint, inBox func(x, y int) bool, isRoad func(x, y int) bool) int {
	seen := make(map[TilePos]struct{})
	groups := 0
	for ty := fy0 - 1; ty <= fy1+1; ty++ {
		for tx := fx0 - 1; tx <= fx1+1; tx++ {
			start := TilePos{X: tx, Y: ty}
			if inFootprint(tx, ty) || !isRoad(tx, ty) {
				continue
			}
			if _, done := seen[start]; done {
				continue
			}
			touches := false
			for _, d := range cardinalSteps {
				if inFootprint(tx+d.X, ty+d.Y) {
					touches = true
					break
				}
			}
			if !touches {
				continue
			}
			groups++
			queue := []TilePos{start}
			seen[start] = struct{}{}
			for len(queue) > 0 {
				c := queue[0]
				queue = queue[1:]
				for _, d := range cardinalSteps {
					n := TilePos{X: c.X + d.X, Y: c.Y + d.Y}
					if _, done := seen[n]; done || !inBox(n.X, n.Y) || inFootprint(n.X, n.Y) || !isRoad(n.X, n.Y) {
						continue
					}
					seen[n] = struct{}{}
					queue = append(queue, n)
				}
			}
		}
	}
	return groups
}

// namedStructureTiles returns the anchor tile of every named building — the
// landmarks DamageSiteLabel names a site by.
func namedStructureTiles(w *World) []TilePos {
	var out []TilePos
	for id, o := range w.VillageObjects {
		if o == nil || o.DisplayName == "" {
			continue
		}
		if _, ok := w.Structures[StructureID(id)]; ok {
			out = append(out, o.Pos.Tile())
		}
	}
	return out
}

// landmarkWithin reports whether any landmark is within tiles of p.
func landmarkWithin(p TilePos, landmarks []TilePos, tiles int) bool {
	for _, l := range landmarks {
		if p.Chebyshev(l) <= tiles {
			return true
		}
	}
	return false
}

// removeRoadObstacle clears a road: deletes the obstacle and stamps the road
// kind's min-gap anchor. Reports whether it was removed. DeleteVillageObject
// fails only for a missing object or a structure — an obstacle this code
// placed is neither, but a structure tagged road_obstacle by hand would be —
// and then nothing changes: the road stays blocked and the repair has not
// landed.
func removeRoadObstacle(w *World, obj *VillageObject, now time.Time) bool {
	if _, err := DeleteVillageObject(obj.ID).Fn(w); err != nil {
		log.Printf("sim/damage: removing road obstacle %s: %v", obj.ID, err)
		return false
	}
	w.Environment.LastRoadRepairAt = now
	return true
}

// roadObstacleFact is the "what is broken" opening for a road obstacle — "A
// fallen log lies across the road by the Cole Residence".
func roadObstacleFact(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, assets map[AssetID]*Asset, obj *VillageObject) string {
	name := "fallen tree"
	if obj.DisplayName != "" {
		name = strings.ToLower(obj.DisplayName)
	}
	fact := "A " + name + " lies across the road"
	if landmark := damageSiteLandmark(objects, structures, obj); landmark != "" {
		fact += " by " + WithDefiniteArticle(landmark)
	}
	return fact
}

// ForceRoadDamage is the operator control that brings a tree down now: no roll
// and no guards but a free site. trigger is DamageTriggerForce or
// DamageTriggerStorm. Returns the placed obstacle's id.
func ForceRoadDamage(trigger string, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		obj, err := placeRoadObstacle(w, trigger, rng, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		return obj.ID, nil
	}}
}
