// Package httpapi is the v2 engine's client-facing read surface. Handlers
// close over a *sim.World and read the lock-free published snapshot
// (world.Published()); no command channel is on the read path. The wire DTOs
// here are v2-native — shaped by sim.Snapshot, not v1's DB-era JSON — and are
// the single source of truth documented in the shared contract note
// shared/notes/codebase/salem-engine-v2/client-contract (consumed by the
// Godot client).
package httpapi

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ContractVersion is the monotonic version of the whole read API. The Godot
// client embeds the version it was built against and fails loudly on a
// mismatch. Bump ONLY on a breaking change (rename/remove/retype a field,
// change the WS envelope); additive new optional fields do not bump it.
const ContractVersion = 1

// WorldStateDTO is the GET /api/village/world response — coarse world state
// for the client's top bar / lighting. The carrier of ContractVersion.
type WorldStateDTO struct {
	ContractVersion int       `json:"contract_version"`
	Phase           string    `json:"phase"` // "day" | "night"
	Tick            uint64    `json:"tick"`
	Now             time.Time `json:"now"`
	Weather         string    `json:"weather"`
	Atmosphere      string    `json:"atmosphere"`
}

// AgentDTO is one actor in the GET /api/village/agents response.
//
// No sprite reference yet: v2 sim.Actor / ActorSnapshot carry no sprite field
// (v1 joined a sprite catalog the v2 model hasn't ported). Flagged in the
// contract note as a known gap — the client renders a placeholder until a v2
// sprite field or asset-based convention lands.
type AgentDTO struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name"`
	Kind              string `json:"kind"`  // npc_stateful | npc_shared | pc | decorative
	State             string `json:"state"` // idle | walking | conversing | ...
	Role              string `json:"role,omitempty"`
	X                 int    `json:"x"` // tile coordinate (actors move on the integer grid)
	Y                 int    `json:"y"`
	InsideStructureID string `json:"inside_structure_id,omitempty"`
	CurrentHuddleID   string `json:"current_huddle_id,omitempty"`
}

// ObjectDTO is one placed village object in the GET /api/village/objects
// response. In v2 a building is both a village_object AND a sim.Structure
// sharing an ID (the shared-identity bridge); this surfaces the village_object
// half — position, asset, visual state.
type ObjectDTO struct {
	ID           string   `json:"id"`
	AssetID      string   `json:"asset_id"`
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	CurrentState string   `json:"current_state,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

// actorKindString maps the internal ActorKind enum to its stable wire form.
// A new enum value renders as "unknown" rather than leaking the int.
func actorKindString(k sim.ActorKind) string {
	switch k {
	case sim.KindNPCStateful:
		return "npc_stateful"
	case sim.KindNPCShared:
		return "npc_shared"
	case sim.KindPC:
		return "pc"
	case sim.KindDecorative:
		return "decorative"
	default:
		return "unknown"
	}
}
