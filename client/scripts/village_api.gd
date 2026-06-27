extends Node
## VillageApi — the v2 engine read-API adapter and the single seam between this
## Godot client and the v2 `/api/village/*` surface.
##
## Everything that differs between the old v1 engine and the v2 engine lives
## here in one place instead of being scattered through world.gd:
##   - the API base URL,
##   - the tile -> world-pixel coordinate conversion (v2 places actors on an
##     integer tile grid; the client renders in world pixels),
##   - the contract_version compatibility check,
##   - translating the leaner v2 wire DTOs into the field shape the existing
##     renderer (world.gd) already consumes.
##
## Registered as an autoload (see project.godot) so any script can reach it as
## `VillageApi`.

## The contract_version this client build targets. The server stamps a matching
## int on GET /api/village/world and on the WS hello frame; on a mismatch we
## fail loud rather than render stale/garbage. Additive server fields do NOT
## bump the version, so an exact-equality check is correct. Keep this in lockstep
## with the server's httpapi.ContractVersion on any breaking read-contract change.
const CONTRACT_VERSION: int = 1

## The engine's locomotion tick interval in seconds — the authoritative actor
## advances one tile per this interval (engine sim.LocomotionTickInterval =
## 2/3 sec, i.e. 1.5 tiles/sec, restoring v1's defaultNPCSpeed of 48 px/s at
## tile_size=32). The client paces its walk animation at the same rate so
## visual arrival lines up with the authoritative npc_arrived. Must stay in
## lockstep with the engine const; the contract is documented at both ends
## (ZBBS-WORK-341).
const LOCOMOTION_TICK_SECONDS: float = 2.0 / 3.0

## World-pixel walk speed (px/sec) that matches the engine's 1-tile-per-tick
## rate. Used to drive the per-frame walk interpolation. Derives from
## LOCOMOTION_TICK_SECONDS so any future cadence change updates here in one
## place.
func walk_speed_px_per_s() -> float:
    return float(tile_size) / LOCOMOTION_TICK_SECONDS

## Engine grid geometry. The v2 engine places actors on an internal tile grid
## where world (0,0) maps to internal tile (pad_x, pad_y) and one tile spans
## tile_size world pixels. These defaults mirror the engine constants
## (engine/sim/pathfind.go padX=60 / padY=112, tile size 32) and the values
## world.gd already uses; refresh_geometry() re-seeds them from the terrain DTO
## (which carries pad/tile_size) so the conversion can't drift if the engine
## ever changes the grid.
var pad_x: int = 60
var pad_y: int = 112
var tile_size: int = 32

## Engine base URL, resolved the same way world.gd / event_client.gd do: the
## page origin on web, a fixed host otherwise.
var api_base: String = ""

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        api_base = "http://zbbs.local"

## Convert an engine internal-grid tile coordinate to a world-pixel position
## (the tile's center). This is the ONE place the tile->pixel mapping lives;
## both the agent REST baseline and the WS walk path run through it, so they
## can never disagree.
func tile_to_world(tile_x: int, tile_y: int) -> Vector2:
    return Vector2(
        float(tile_x - pad_x) * tile_size + tile_size / 2.0,
        float(tile_y - pad_y) * tile_size + tile_size / 2.0,
    )

## Inverse of tile_to_world: the engine internal-grid (PADDED) tile whose cell
## contains a world-pixel position. Outbound wire payloads (pc/move) and the
## inbound tile_to_world both live here so the two directions can never disagree.
## floor(px/tile_size) is the unpadded cell; + pad lifts it into the engine's
## padded grid-index space — the canonical wire unit. Do NOT use world.gd's
## world_to_tile (unpadded) to build wire coords.
func world_to_tile_padded(world_pos: Vector2) -> Vector2i:
    return Vector2i(
        int(floor(world_pos.x / float(tile_size))) + pad_x,
        int(floor(world_pos.y / float(tile_size))) + pad_y,
    )

## Re-seed the grid geometry from the terrain DTO header (which carries pad_x /
## pad_y / tile_size). Safe to call as part of the terrain load; until then the
## engine-matching defaults above are used.
func refresh_geometry(terrain_meta: Dictionary) -> void:
    pad_x = int(terrain_meta.get("pad_x", pad_x))
    pad_y = int(terrain_meta.get("pad_y", pad_y))
    # Guard tile_size > 0: a zero/garbage value from a malformed DTO would
    # divide-by-zero in world_to_tile_padded and collapse tile_to_world. Keep
    # the prior (engine-matching) value rather than poison the grid.
    var ts: int = int(terrain_meta.get("tile_size", tile_size))
    if ts > 0:
        tile_size = ts

## Compare a server-reported contract_version against this client build. Returns
## true when compatible; on a mismatch logs loudly and returns false so the
## caller can refuse to render ("client out of date") instead of drawing
## garbage from a contract it doesn't understand.
func check_contract_version(server_version: int) -> bool:
    if server_version == CONTRACT_VERSION:
        return true
    push_error(
        "VillageApi: contract_version mismatch — client built for %d, server reports %d. Refusing to render (client out of date)."
        % [CONTRACT_VERSION, server_version]
    )
    return false

## Translate one v2 AgentDTO (an element of GET /api/village/agents) into the
## field shape world.gd's NPC renderer (_render_npc) consumes.
##
## The v2 DTO is leaner than v1's /npcs row and uses tile coordinates; this
## normalizes both seams:
##   - x / y (internal-grid tiles)        -> current_x / current_y (world pixels)
##   - inside_structure_id presence       -> the `inside` bool the renderer wants
##   - sprite / display_name / facing / llm_memory_agent / kind / state / role
##     pass through unchanged (sprite is already inlined in the render subset).
##
## The editor/HUD config fields (attributes, home/work bindings, schedule
## windows) are now carried by AgentDTO (ZBBS-HOME-290) and passed
## through here so the existing _place_npc meta-setters + editor panels pick
## them up. The schedule *_minute fields are forwarded RAW (null
## preserved, not coerced) because _place_npc gates "set vs inherit dawn/dusk"
## on null. Needs (hunger/thirst/tiredness, ZBBS-HOME-462) and coins (LLM-70) are
## carried too — the editor's per-NPC readout renders them; live updates arrive via
## the npc_needs_changed / npc_coins_changed WS events (apply_npc_needs_changed,
## apply_npc_coins_changed).
func normalize_agent(dto: Dictionary) -> Dictionary:
    var inside_structure_id: String = str(dto.get("inside_structure_id", ""))
    var world_pos := tile_to_world(int(dto.get("x", 0)), int(dto.get("y", 0)))
    var attributes = dto.get("attributes", [])
    var out := {
        "id": str(dto.get("id", "")),
        "display_name": str(dto.get("display_name", "")),
        "kind": str(dto.get("kind", "")),
        "state": str(dto.get("state", "")),
        "role": str(dto.get("role", "")),
        "facing": str(dto.get("facing", "south")),
        "llm_memory_agent": str(dto.get("llm_memory_agent", "")),
        "current_x": world_pos.x,
        "current_y": world_pos.y,
        "inside": inside_structure_id != "",
        "inside_structure_id": inside_structure_id,
        "current_huddle_id": str(dto.get("current_huddle_id", "")),
        "attributes": attributes if attributes is Array else [],
        "home_structure_id": str(dto.get("home_structure_id", "")),
        "work_structure_id": str(dto.get("work_structure_id", "")),
        # Raw / null-preserving: _place_npc reads null as "inherit dawn/dusk".
        "schedule_start_minute": dto.get("schedule_start_minute", null),
        "schedule_end_minute": dto.get("schedule_end_minute", null),
        # Live needs (ZBBS-HOME-462) — the editor needs readout reads these off the
        # agent row; _place_npc seeds them as container meta. Coerced to int (default
        # 0) since a decorative / sprite-less actor may omit them.
        "hunger": int(dto.get("hunger", 0)),
        "thirst": int(dto.get("thirst", 0)),
        "tiredness": int(dto.get("tiredness", 0)),
        # Coins (LLM-70) — the villager-row purse readout. LLM-70 added coins to the
        # DTO and the renderer but not to this seam, so _render_npc never saw it and
        # every row read 0; forwarded here. Live updates arrive via the
        # npc_coins_changed WS event (apply_npc_coins_changed, LLM-71).
        "coins": int(dto.get("coins", 0)),
    }
    # Sprite is already inlined on the v2 DTO in the exact render subset the
    # renderer expects (sheet / frame_width / frame_height / id / name /
    # animations) — pass it through. Absent for actors with no sprite.
    if dto.get("sprite", null) != null:
        out["sprite"] = dto["sprite"]
    return out
