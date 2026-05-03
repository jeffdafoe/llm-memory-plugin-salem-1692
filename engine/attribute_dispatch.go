package main

// Attribute dispatch — runs deterministic behavior specs for actors.
//
// Every actor_attribute assignment links an actor to an attribute_definition
// row. The definition carries a JSONB array of behavior specs:
//
//   [{"type": "<handler-slug>", "params": { ... }}, ...]
//
// At dispatch time, the engine looks up the specs for an actor (or for a
// specific slug), then for each spec invokes a registered Go handler keyed
// by spec.Type. Handlers wrap the existing route-walking primitives in
// npc_behaviors.go (startLamplighterRouteForNPC, startRotationRouteForNPC).
//
// This replaces the legacy switch on actor.behavior. The set of handler
// types is closed at build time (added by editing this file); attribute
// definitions are open at run time (added by INSERT into the registry
// table). New roles can be created by inserting a new attribute_definition
// row that references existing handlers, no code change required.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// behaviorSpec is one entry in attribute_definition.behaviors.
type behaviorSpec struct {
	Type   string                 `json:"type"`
	Params map[string]interface{} `json:"params"`
}

// behaviorHandler executes a single behavior spec for one NPC. Returns
// the number of route stops queued (0 means the trigger fired but had
// nothing to do, e.g. all lamps already matched the phase target).
type behaviorHandler func(ctx context.Context, app *App, npc *behaviorNPC, params map[string]interface{}) (int, error)

// behaviorHandlers is the closed-set registry of handler types referenced
// from attribute_definition.behaviors. Engine startup logs unknown types
// found in the registry so missing handlers surface immediately rather
// than failing silently at first dispatch.
var behaviorHandlers = map[string]behaviorHandler{
	"lamp_route":     handleLampRoute,
	"rotation_route": handleRotationRoute,
	// "worker" is a discoverable marker for the worker scheduler
	// (loadWorkerRows in npc_scheduler.go). The actual home/work walks
	// are driven by the scheduler tick, not by the per-NPC behavior
	// dispatcher — so this handler is a no-op. Registered here so the
	// dispatcher doesn't log "unknown behavior type" when it iterates
	// the behaviors of a tavernkeeper/blacksmith/herbalist/merchant.
	"worker": handleWorkerNoop,
}

// findActorIDsWithAttribute returns every actor.id holding the given
// attribute slug. Empty slice when no actor carries it.
func (app *App) findActorIDsWithAttribute(ctx context.Context, slug string) ([]string, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT actor_id FROM actor_attribute WHERE slug = $1 ORDER BY actor_id`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// loadBehaviorSpecsForActor returns every behavior spec the actor would
// receive, flattened across all of their attributes. Order: definition
// slug ascending, then spec array order within each definition. A bad
// JSON blob in any one definition logs and skips that row rather than
// failing the whole load.
func (app *App) loadBehaviorSpecsForActor(ctx context.Context, actorID string) ([]behaviorSpec, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT d.slug, d.behaviors
		  FROM actor_attribute a
		  JOIN attribute_definition d ON d.slug = a.slug
		 WHERE a.actor_id = $1
		 ORDER BY a.slug
	`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var specs []behaviorSpec
	for rows.Next() {
		var slug string
		var raw []byte
		if err := rows.Scan(&slug, &raw); err != nil {
			return nil, err
		}
		var defSpecs []behaviorSpec
		if err := json.Unmarshal(raw, &defSpecs); err != nil {
			log.Printf("attribute_dispatch: bad behaviors JSON for slug=%s actor=%s: %v", slug, actorID, err)
			continue
		}
		specs = append(specs, defSpecs...)
	}
	return specs, nil
}

// firstAttributeSlugForActor returns the lexicographically-first attribute
// slug carried by the actor (or "" when they have none). Used by handlers
// and admin endpoints that need a slug-shaped label for logging or for
// pre-attribute-system response payloads.
func (app *App) firstAttributeSlugForActor(ctx context.Context, actorID string) (string, error) {
	var slug string
	err := app.DB.QueryRow(ctx,
		`SELECT slug FROM actor_attribute WHERE actor_id = $1 ORDER BY slug LIMIT 1`,
		actorID).Scan(&slug)
	if err != nil {
		// pgx returns pgx.ErrNoRows when there's no match; treating any
		// error as "no slug" keeps the caller path simple. Real DB
		// errors are rare enough at this read path that masking them
		// here doesn't hide a meaningful failure.
		return "", nil
	}
	return slug, nil
}

// handleLampRoute fires the lamplighter route for an NPC. Phase-aware:
// at night, walks lamps to night-active; at day, walks them to day-active.
// Mirrors the prior dispatchBehaviorForNPC logic: try the opposite-phase
// target first to avoid silent no-ops when lamps already match the
// current phase, fall back to the current-phase target.
//
// Params unused — phase is a global. Kept on the signature for handler
// uniformity.
func handleLampRoute(ctx context.Context, app *App, npc *behaviorNPC, params map[string]interface{}) (int, error) {
	phase, err := app.currentWorldPhase(ctx)
	if err != nil {
		return 0, err
	}
	first, second := "night-active", "day-active"
	if phase == "night" {
		first, second = "day-active", "night-active"
	}
	n, err := app.startLamplighterRouteForNPC(ctx, npc, first)
	if err != nil || n > 0 {
		return n, err
	}
	return app.startLamplighterRouteForNPC(ctx, npc, second)
}

// handleRotationRoute walks rotation-tagged states for the NPC. Params:
//
//   domain_tag (string, required) — the asset_state_tag value to filter on.
//   label      (string, required) — log/route-label string for traces.
//
// Both params come from the registry; the actor-side actor_attribute.params
// is reserved for per-assignment overrides (none yet).
func handleRotationRoute(ctx context.Context, app *App, npc *behaviorNPC, params map[string]interface{}) (int, error) {
	domainTag, _ := params["domain_tag"].(string)
	label, _ := params["label"].(string)
	if domainTag == "" {
		return 0, fmt.Errorf("rotation_route: missing domain_tag in params")
	}
	if label == "" {
		label = domainTag
	}
	return app.startRotationRouteForNPC(ctx, npc, domainTag, label)
}

// handleWorkerNoop is the per-NPC dispatcher entry for the worker
// behavior marker. The worker scheduler tick (loadWorkerRows + the
// home/work walk loop) is the actual driver — this handler exists only
// so dispatchBehaviorForNPC doesn't log unknown-type warnings when it
// iterates a tavernkeeper's or blacksmith's behaviors JSONB.
func handleWorkerNoop(ctx context.Context, app *App, npc *behaviorNPC, params map[string]interface{}) (int, error) {
	return 0, nil
}
