package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Server serves the read surface for one world. It holds the *sim.World and
// reads world.Published() per request — lock-free, no command channel. Safe
// for concurrent requests: every handler only reads the immutable snapshot.
type Server struct {
	world *sim.World
}

// NewServer builds a Server for w. Panics on nil w — a wiring bug.
func NewServer(w *sim.World) *Server {
	if w == nil {
		panic("httpapi: NewServer requires a non-nil world")
	}
	return &Server{world: w}
}

// Handler returns the read-surface routes. Slice 2 phases 1-2 — the full
// static-render read set: world / agents / objects (per-tick, off the published
// snapshot) plus terrain / assets (reference state, off *sim.World). The WS
// /events endpoint and write routes land in later phases. Reads are
// unauthenticated during the validation phase; auth middleware ports with the
// write routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/village/world", s.handleWorld)
	mux.HandleFunc("GET /api/village/agents", s.handleAgents)
	mux.HandleFunc("GET /api/village/objects", s.handleObjects)
	mux.HandleFunc("GET /api/village/terrain", s.handleTerrain)
	mux.HandleFunc("GET /api/village/assets", s.handleAssets)
	mux.HandleFunc("GET /api/village/sprites", s.handleSprites)
	return mux
}

func (s *Server) handleWorld(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, worldStateFromSnapshot(snap))
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, agentsFromSnapshot(snap, s.world.Sprites))
}

func (s *Server) handleObjects(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, objectsFromSnapshot(snap))
}

// handleTerrain serves the terrain grid. Unlike the per-tick handlers above it
// reads world.Terrain directly — reference state loaded once at startup and
// never written by the engine loop, so the read is lock-free with no snapshot
// or command channel. The DTO aliases Terrain.Data (no defensive copy) because
// that immutability holds.
//
// INVARIANT for the future SIGHUP hot-reload (not yet wired — cmd/engine
// registers only SIGINT/SIGTERM): a reload MUST publish a NEW *sim.Terrain /
// asset map and swap it atomically (e.g. atomic.Pointer, the way world.Publish
// works for per-tick state). It must NOT mutate the existing Terrain.Data slice
// or world.Assets map in place — these handlers read them concurrently without
// synchronization, so an in-place mutation would race.
func (s *Server) handleTerrain(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, terrainDTO(s.world.Terrain))
}

// handleAssets serves the asset catalog. Reads world.Assets directly — same
// lock-free reference-state posture and same SIGHUP invariant as handleTerrain.
func (s *Server) handleAssets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, assetsFromCatalog(s.world.Assets))
}

// handleSprites serves the raw character-sprite catalog (the editor's sprite
// picker source). Reads world.Sprites directly — same lock-free reference-
// state posture and same SIGHUP invariant as handleTerrain/handleAssets.
func (s *Server) handleSprites(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, spritesFromCatalog(s.world.Sprites))
}

// worldStateFromSnapshot maps the snapshot's world-level state to the wire DTO.
func worldStateFromSnapshot(s *sim.Snapshot) WorldStateDTO {
	return WorldStateDTO{
		ContractVersion: ContractVersion,
		Phase:           string(s.Phase),
		Tick:            s.AtTick,
		Now:             s.Environment.Now,
		Weather:         s.Environment.Weather,
		Atmosphere:      s.Environment.Atmosphere,
	}
}

// agentsFromSnapshot maps every actor to an AgentDTO, sorted by ID so the
// response is deterministic (stable client diffs + testable). The sprite
// catalog (reference state off *sim.World) is passed in so each agent's
// SpriteID can be resolved + inlined; a nil/absent catalog or a dangling
// SpriteID simply leaves Sprite unset (the client renders a placeholder).
func agentsFromSnapshot(s *sim.Snapshot, sprites map[sim.SpriteID]*sim.Sprite) []AgentDTO {
	out := make([]AgentDTO, 0, len(s.Actors))
	for id, a := range s.Actors {
		out = append(out, AgentDTO{
			ID:                string(id),
			DisplayName:       a.DisplayName,
			Kind:              actorKindString(a.Kind),
			State:             string(a.State),
			Role:              a.Role,
			X:                 a.CurrentX,
			Y:                 a.CurrentY,
			Facing:            normalizeFacing(a.Facing),
			InsideStructureID: string(a.InsideStructureID),
			CurrentHuddleID:   string(a.CurrentHuddleID),
			Sprite:            resolveAgentSprite(a.SpriteID, sprites),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// normalizeFacing coalesces an unset Facing to "south" so every agent emits a
// valid facing regardless of origin. A pg-loaded actor's facing column is NOT
// NULL (default 'south'); an in-memory-spawned actor may have an empty Facing.
// Without this the wire would carry "south" for the former and omit it for the
// latter — an origin-dependent inconsistency. Non-empty values pass through
// unchanged (the write path validates the enum; this read path is display-only
// and never the source of a bad stored value).
func normalizeFacing(facing string) string {
	if facing == "" {
		return "south"
	}
	return facing
}

// resolveAgentSprite looks up spriteID in the catalog and maps it to the
// inline render DTO. Returns nil when the actor has no sprite (empty ID) or
// the ID doesn't resolve (dangling ref) — Sprite is omitempty, so the field
// is simply absent on the wire and the client falls back to a placeholder.
func resolveAgentSprite(spriteID sim.SpriteID, sprites map[sim.SpriteID]*sim.Sprite) *AgentSpriteDTO {
	if spriteID == "" || sprites == nil {
		return nil
	}
	sp := sprites[spriteID]
	if sp == nil {
		return nil
	}
	return &AgentSpriteDTO{
		ID:          string(sp.ID),
		Name:        sp.Name,
		Sheet:       sp.Sheet,
		FrameWidth:  sp.FrameWidth,
		FrameHeight: sp.FrameHeight,
		Animations:  spriteAnimationsDTO(sp.Animations),
	}
}

// objectsFromSnapshot maps every village object to an ObjectDTO, sorted by ID.
func objectsFromSnapshot(s *sim.Snapshot) []ObjectDTO {
	out := make([]ObjectDTO, 0, len(s.VillageObjects))
	for id, o := range s.VillageObjects {
		if o == nil {
			continue
		}
		out = append(out, ObjectDTO{
			ID:           string(id),
			AssetID:      string(o.AssetID),
			X:            o.X,
			Y:            o.Y,
			CurrentState: o.CurrentState,
			DisplayName:  o.DisplayName,
			Tags:         o.Tags,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// terrainDTO maps the terrain reference state to the wire DTO. Grid dimensions
// come from the canonical sim constants (the loaded blob is validated to be
// exactly MapW*MapH at load, so they always agree). A nil Terrain (terrain
// absent / not loaded) yields the metadata header with an empty Data string —
// the client decodes that to a zero-length grid and renders nothing.
func terrainDTO(t *sim.Terrain) TerrainDTO {
	dto := TerrainDTO{
		ContractVersion: ContractVersion,
		MapW:            sim.MapW,
		MapH:            sim.MapH,
		PadX:            sim.PadX,
		PadY:            sim.PadY,
		TileSize:        int(sim.TileSize),
	}
	if t != nil {
		dto.Data = base64.StdEncoding.EncodeToString(t.Data)
	}
	return dto
}

// assetsFromCatalog maps the asset catalog to a DTO slice, sorted by ID so the
// response is deterministic (stable client diffs + testable).
func assetsFromCatalog(assets map[sim.AssetID]*sim.Asset) []AssetDTO {
	out := make([]AssetDTO, 0, len(assets))
	for id, a := range assets {
		if a == nil {
			continue
		}
		out = append(out, assetDTO(id, a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// assetDTO maps one catalog asset to its render-graph DTO.
func assetDTO(id sim.AssetID, a *sim.Asset) AssetDTO {
	dto := AssetDTO{
		ID:                string(id),
		Name:              a.Name,
		Category:          a.Category,
		DefaultState:      a.DefaultState,
		AnchorX:           a.AnchorX,
		AnchorY:           a.AnchorY,
		Layer:             a.Layer,
		ZIndex:            a.ZIndex,
		VisibleWhenInside: a.VisibleWhenInside,
		StandOffsetX:      a.StandOffsetX,
		StandOffsetY:      a.StandOffsetY,
		DoorOffsetX:       a.DoorOffsetX,
		DoorOffsetY:       a.DoorOffsetY,
		Footprint: FootprintDTO{
			Left:   a.FootprintLeft,
			Right:  a.FootprintRight,
			Top:    a.FootprintTop,
			Bottom: a.FootprintBottom,
		},
		FitsSlot: a.FitsSlot,
		States:   assetStatesDTO(a.States),
		Slots:    assetSlotsDTO(a.Slots),
	}
	if a.Pack != nil {
		dto.Pack = &TilesetPackDTO{
			ID:   a.Pack.ID,
			Name: a.Pack.Name,
			URL:  a.Pack.URL,
		}
	}
	return dto
}

// assetStatesDTO maps an asset's states. Always returns a non-nil slice — the
// states field is required on the wire (no omitempty), so an asset with no
// states serializes as [] rather than null.
func assetStatesDTO(states []sim.AssetState) []AssetStateDTO {
	out := make([]AssetStateDTO, 0, len(states))
	for i := range states {
		st := &states[i]
		d := AssetStateDTO{
			State:      st.State,
			Sheet:      st.Sheet,
			SrcX:       st.SrcX,
			SrcY:       st.SrcY,
			SrcW:       st.SrcW,
			SrcH:       st.SrcH,
			FrameCount: st.FrameCount,
			FrameRate:  st.FrameRate,
			Tags:       st.Tags,
		}
		if st.Light != nil {
			d.Light = &AssetLightDTO{
				Color:            st.Light.Color,
				Radius:           st.Light.Radius,
				Energy:           st.Light.Energy,
				OffsetX:          st.Light.OffsetX,
				OffsetY:          st.Light.OffsetY,
				FlickerAmplitude: st.Light.FlickerAmplitude,
				FlickerPeriodMs:  st.Light.FlickerPeriodMs,
			}
		}
		out = append(out, d)
	}
	return out
}

// assetSlotsDTO maps an asset's attachment slots. Returns nil when there are
// none — slots carries omitempty, so it's absent on the wire for slot-less
// assets (the common case).
func assetSlotsDTO(slots []sim.AssetSlot) []AssetSlotDTO {
	if len(slots) == 0 {
		return nil
	}
	out := make([]AssetSlotDTO, 0, len(slots))
	for i := range slots {
		out = append(out, AssetSlotDTO{
			SlotName: slots[i].SlotName,
			OffsetX:  slots[i].OffsetX,
			OffsetY:  slots[i].OffsetY,
		})
	}
	return out
}

// spritesFromCatalog maps the sprite catalog to a DTO slice, sorted by ID so
// the response is deterministic (stable client diffs + testable).
func spritesFromCatalog(sprites map[sim.SpriteID]*sim.Sprite) []SpriteDTO {
	out := make([]SpriteDTO, 0, len(sprites))
	for id, sp := range sprites {
		if sp == nil {
			continue
		}
		out = append(out, spriteDTO(id, sp))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// spriteDTO maps one catalog sprite to its render DTO.
func spriteDTO(id sim.SpriteID, sp *sim.Sprite) SpriteDTO {
	dto := SpriteDTO{
		ID:          string(id),
		Name:        sp.Name,
		Sheet:       sp.Sheet,
		FrameWidth:  sp.FrameWidth,
		FrameHeight: sp.FrameHeight,
		Animations:  spriteAnimationsDTO(sp.Animations),
	}
	if sp.Pack != nil {
		dto.Pack = &TilesetPackDTO{
			ID:   sp.Pack.ID,
			Name: sp.Pack.Name,
			URL:  sp.Pack.URL,
		}
	}
	return dto
}

// spriteAnimationsDTO maps a sprite's animation rows. Always returns a
// non-nil slice — animations is required on the wire (no omitempty), so a
// sprite with no animation rows serializes as [] rather than null.
func spriteAnimationsDTO(anims []sim.SpriteAnimation) []SpriteAnimationDTO {
	out := make([]SpriteAnimationDTO, 0, len(anims))
	for i := range anims {
		out = append(out, SpriteAnimationDTO{
			Direction:  anims[i].Direction,
			Animation:  anims[i].Animation,
			RowIndex:   anims[i].RowIndex,
			FrameCount: anims[i].FrameCount,
			FrameRate:  anims[i].FrameRate,
		})
	}
	return out
}

// writeJSON encodes v as the JSON response body. A late encode error (after
// the 200 header is sent) can't be recovered into a status code, so it's
// logged — the client sees a truncated body and re-syncs via its next fetch.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
