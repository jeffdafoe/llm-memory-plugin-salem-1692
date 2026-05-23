package sim

// AttributeDefinition is one entry of the attribute_definition catalog — the
// vocabulary of attributes (roles / behaviors) an NPC can be assigned. v2
// surfaces it as reference state on World.AttributeDefinitions, loaded once at
// startup and read directly off *World by the editor's "add attribute"
// dropdown (GET /api/village/npc-behaviors). Same lifecycle as Asset / Sprite:
// no checkpoint path — admin edits write the underlying table and the world
// rebuilds the map wholesale via LoadAll (on the future SIGHUP hot-reload).
//
// Only the actor-assignable subset is loaded — scope IN ('actor','both');
// object-only definitions are excluded at load time, matching v1's
// handleListNPCBehaviors query. The endpoint keeps the historical "behaviors"
// label for URL/wire-format stability (the legacy npc_behavior allowlist table
// was retired in ZBBS-113); the data is the attribute catalog.
//
// Slug is the attribute_definition PK (actor_attribute.slug references it).
// DisplayName is the human-readable label the editor renders. The richer
// columns (description, tools, instructions, behaviors) aren't needed by the
// read surface, so they're left unloaded.
type AttributeDefinition struct {
	Slug        string
	DisplayName string
}
