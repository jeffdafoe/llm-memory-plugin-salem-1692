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
// Every route requires a valid salem-realm session token (auth); an optional
// event hub (SetEventsHub) adds the authenticated WS /events push channel.
type Server struct {
	world *sim.World
	auth  Authenticator
	hub   *Hub
}

// NewServer builds a Server for w, authenticating every route via auth. Panics
// on a nil world or nil Authenticator — both are wiring bugs, and a nil auth
// would silently leave the read surface open.
func NewServer(w *sim.World, auth Authenticator) *Server {
	if w == nil {
		panic("httpapi: NewServer requires a non-nil world")
	}
	if auth == nil {
		panic("httpapi: NewServer requires a non-nil Authenticator")
	}
	return &Server{world: w, auth: auth}
}

// SetEventsHub attaches the WS event hub. When set, Handler exposes the
// GET /api/village/events WebSocket route. The hub must already be Subscribed
// to the world and have its Run goroutine started (both happen at wiring time,
// before world.Run). Wired separately from NewServer so the read-only REST
// surface can stand up without a hub (e.g. existing tests).
//
// MUST be called before Handler and before serving requests — it mutates s
// without synchronization, so calling it concurrently with Handler or a live
// handler races. The intended wiring sets it once during startup.
func (s *Server) SetEventsHub(h *Hub) {
	s.hub = h
}

// Handler returns the read-surface routes: the static-render read set
// (world / agents / objects off the published snapshot; terrain / assets /
// sprites off *sim.World reference state), plus the WS /events push channel
// when an event hub is attached via SetEventsHub. Every route requires a valid
// salem-realm session token — REST via requireAuth (Bearer header), WS via its
// own ?token= verify. Write routes (POST) run the mutation through the world
// command channel — see write_handlers.go.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Every REST read is wrapped in requireAuth (Bearer token → verify →
	// salem-realm gate). The WS /events handler does its own ?token= verify
	// (browsers can't set WS handshake headers), before the upgrade.
	mux.HandleFunc("GET /api/village/world", s.requireAuth(s.handleWorld))
	mux.HandleFunc("GET /api/village/agents", s.requireAuth(s.handleAgents))
	mux.HandleFunc("GET /api/village/objects", s.requireAuth(s.handleObjects))
	mux.HandleFunc("GET /api/village/terrain", s.requireAuth(s.handleTerrain))
	mux.HandleFunc("GET /api/village/assets", s.requireAuth(s.handleAssets))
	mux.HandleFunc("GET /api/village/sprites", s.requireAuth(s.handleSprites))
	mux.HandleFunc("GET /api/village/npc-behaviors", s.requireAuth(s.handleNPCBehaviors))
	// Static editor allowlists (vocabulary the editor's tag dropdowns render).
	// Hardcoded reference data — no World map, no DB; see catalog_tags.go.
	mux.HandleFunc("GET /api/village/object-tags", s.requireAuth(s.handleObjectTags))
	mux.HandleFunc("GET /api/assets/state-tags", s.requireAuth(s.handleStateTags))
	// PC bootstrap read. POST to match the v1 verb + the client, but it's a
	// pure snapshot read (no command channel) — see pc_me.go.
	mux.HandleFunc("POST /api/village/pc/me", s.requireAuth(s.handlePCMe))
	// Write routes — same requireAuth gate; the mutation runs through the
	// world command channel (see write_handlers.go).
	mux.HandleFunc("POST /api/village/pc/move", s.requireAuth(s.handlePCMove))
	mux.HandleFunc("POST /api/village/pc/speak", s.requireAuth(s.handlePCSpeak))
	mux.HandleFunc("POST /api/village/pc/pay", s.requireAuth(s.handlePCPay))
	// Admin write routes — requireAuth PLUS an in-command admin gate (the
	// caller's actor must have admin = true; see adminCommand in
	// write_handlers.go). Distinct from pc/* whose ownership is structural.
	mux.HandleFunc("POST /api/village/admin/phase", s.requireAuth(s.handleAdminPhase))
	mux.HandleFunc("POST /api/village/admin/object/move", s.requireAuth(s.handleAdminObjectMove))
	mux.HandleFunc("POST /api/village/admin/object/delete", s.requireAuth(s.handleAdminObjectDelete))
	mux.HandleFunc("POST /api/village/admin/object/set-state", s.requireAuth(s.handleAdminObjectSetState))
	mux.HandleFunc("POST /api/village/admin/object/set-owner", s.requireAuth(s.handleAdminObjectSetOwner))
	mux.HandleFunc("POST /api/village/admin/object/set-loiter-offset", s.requireAuth(s.handleAdminObjectSetLoiterOffset))
	mux.HandleFunc("POST /api/village/admin/object/set-entry-policy", s.requireAuth(s.handleAdminObjectSetEntryPolicy))
	mux.HandleFunc("POST /api/village/admin/object/set-display-name", s.requireAuth(s.handleAdminObjectSetDisplayName))
	mux.HandleFunc("POST /api/village/admin/object/add-tag", s.requireAuth(s.handleAdminObjectAddTag))
	mux.HandleFunc("POST /api/village/admin/object/remove-tag", s.requireAuth(s.handleAdminObjectRemoveTag))
	mux.HandleFunc("POST /api/village/admin/object/set-refresh", s.requireAuth(s.handleAdminObjectSetRefresh))
	if s.hub != nil {
		mux.HandleFunc("GET /api/village/events", s.handleEvents)
	}
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
	writeJSON(w, objectsFromSnapshot(snap, s.world.Assets))
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

// handleNPCBehaviors serves the actor-assignable attribute catalog (the
// editor's "add attribute" dropdown source). Reads world.AttributeDefinitions
// directly — same lock-free reference-state posture and same SIGHUP invariant
// as handleSprites/handleAssets.
func (s *Server) handleNPCBehaviors(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, npcBehaviorsFromCatalog(s.world.AttributeDefinitions))
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
			LLMAgent:          a.LLMAgent,
			X:                 a.Pos.X,
			Y:                 a.Pos.Y,
			Facing:            normalizeFacing(a.Facing),
			InsideStructureID: string(a.InsideStructureID),
			CurrentHuddleID:   string(a.CurrentHuddleID),
			Sprite:            resolveAgentSprite(a.SpriteID, sprites),
			Attributes:        a.AttributeSlugs,
			HomeStructureID:   string(a.HomeStructureID),
			WorkStructureID:   string(a.WorkStructureID),
			ScheduleStartMin:  a.ScheduleStartMin,
			ScheduleEndMin:    a.ScheduleEndMin,
			SocialTag:         a.SocialTag,
			SocialStartMin:    a.SocialStartMin,
			SocialEndMin:      a.SocialEndMin,
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
// The asset catalog (immutable reference state off *sim.World, same posture as
// agentsFromSnapshot's sprite map) is passed in to resolve each object's
// effective loiter offset; sim.EffectiveLoiterOffset is nil-asset-safe, so a
// dangling asset_id falls back to the raw override (or zero) without breaking
// the build (ZBBS-HOME-289).
func objectsFromSnapshot(s *sim.Snapshot, assets map[sim.AssetID]*sim.Asset) []ObjectDTO {
	out := make([]ObjectDTO, 0, len(s.VillageObjects))
	for id, o := range s.VillageObjects {
		if o == nil {
			continue
		}
		effX, effY := sim.EffectiveLoiterOffset(o, assets[o.AssetID])
		dto := ObjectDTO{
			ID:                     string(id),
			AssetID:                string(o.AssetID),
			X:                      o.Pos.X,
			Y:                      o.Pos.Y,
			CurrentState:           o.CurrentState,
			DisplayName:            o.DisplayName,
			Tags:                   o.Tags,
			Owner:                  string(o.OwnerActorID),
			PlacedBy:               o.PlacedBy,
			EntryPolicy:            string(o.EntryPolicy),
			LoiterOffsetX:          o.LoiterOffsetX,
			LoiterOffsetY:          o.LoiterOffsetY,
			EffectiveLoiterOffsetX: effX,
			EffectiveLoiterOffsetY: effY,
		}
		// Noticeboard content (ZBBS-HOME-291): a board with authored prose
		// carries its current text + posted-at; everything else omits both.
		if nc := s.NoticeboardContent[id]; nc != nil {
			dto.ContentText = nc.Text
			postedAt := nc.PostedAt
			dto.ContentPostedAt = &postedAt
		}
		out = append(out, dto)
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

// npcBehaviorsFromCatalog maps the attribute-definition catalog to a DTO slice,
// sorted by display name so the editor dropdown is predictable (mirrors v1's
// ORDER BY display_name) and the response is deterministic for tests. Ties on
// display name fall back to slug for a total order.
func npcBehaviorsFromCatalog(defs map[string]*sim.AttributeDefinition) []NPCBehaviorDTO {
	out := make([]NPCBehaviorDTO, 0, len(defs))
	for _, d := range defs {
		if d == nil {
			continue
		}
		out = append(out, NPCBehaviorDTO{Slug: d.Slug, DisplayName: d.DisplayName})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Slug < out[j].Slug
	})
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
