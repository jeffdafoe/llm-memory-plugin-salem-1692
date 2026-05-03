package main

// Per-actor tool selection — layers role-specific tools and instructions
// on top of the universal baseline returned by agentToolSpec.
//
// Tool resolution per tick:
//
//   buildAgentTools(actor)
//     = agentToolSpec() (universal baseline)
//     + roleToolRegistry entries for every slug listed by every
//       attribute_definition.tools the actor's attributes reference,
//       deduped against the baseline (baseline always wins on conflict).
//
// roleToolRegistry is the closed-set catalog of role-only tools. Adding
// a new role tool means: (1) add its agentToolDef here, (2) add the
// dispatch case in executeAgentCommit, (3) reference its slug from one
// or more attribute_definition rows.
//
// Universal tools live inline in agentToolSpec for now — moving the
// whole catalog to this file is a future cleanup that doesn't block
// chip-driven role tools landing.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// roleToolRegistry holds tools that are ONLY available to actors whose
// attribute_definition.tools array names them. Universal tools are not
// here; they live in agentToolSpec.
var roleToolRegistry = map[string]agentToolDef{
	"serve": {
		Name: "serve",
		Description: "Give goods from your stock to one or more people present, FREELY and with no expectation of payment. Use this for samples, complimentary drinks, charity, on-the-house pours, gifts to friends or guests in distress. Decrements your inventory by qty per recipient. With consume_now=true (the default for food and drink) the recipients eat or drink the gift on the spot, dropping their matching need. With consume_now=false the goods go into the recipients' own inventories to take away. SALES ARE INITIATED BY THE BUYER via pay() — never use serve to fulfill a sale. The customer's pay() is atomic: stock decrements, goods or consumption land, coins move, all in one transaction. To opt into the gift semantics and confirm no payment is expected, you MUST set gift=true; without it the call rejects.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"recipients": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Display names of the people you're gifting to. Must be present in the same room as you. One or more.",
				},
				"item": map[string]interface{}{
					"type":        "string",
					"description": "Item kind from your stock. Must match a row in your 'Items you can sell' / inventory readout.",
				},
				"qty": map[string]interface{}{
					"type":        "integer",
					"description": "Quantity per recipient. Defaults to 1.",
				},
				"consume_now": map[string]interface{}{
					"type":        "boolean",
					"description": "True (default for food and drink at your place) — recipients eat/drink the gift immediately, need drops. False — items go into recipients' inventories to carry away. Non-portable items (stew) reject consume_now=false.",
				},
				"gift": map[string]interface{}{
					"type":        "boolean",
					"description": "MUST be true. Confirms this is a free gift with no payment expected. Sales go through the buyer's pay() instead. Default false rejects.",
				},
			},
			"required": []string{"recipients", "item", "gift"},
		},
	},
}

// buildAgentTools returns the per-actor tool list: the universal baseline
// from agentToolSpec, plus any role-specific tools the actor's attributes
// declare. Unknown slugs (registry miss) are logged and skipped — keeps
// adding new attribute rows non-fatal even when the matching tool hasn't
// shipped.
func (app *App) buildAgentTools(ctx context.Context, actorID string) []agentToolDef {
	baseline := agentToolSpec()
	out := make([]agentToolDef, 0, len(baseline)+len(roleToolRegistry))
	seen := make(map[string]bool, len(baseline)+len(roleToolRegistry))
	for _, def := range baseline {
		out = append(out, def)
		seen[def.Name] = true
	}
	roleSlugs, err := app.loadToolSlugsForActor(ctx, actorID)
	if err != nil {
		log.Printf("buildAgentTools: load tool slugs for actor %s: %v", actorID, err)
		return out
	}
	for _, slug := range roleSlugs {
		if seen[slug] {
			continue
		}
		def, ok := roleToolRegistry[slug]
		if !ok {
			log.Printf("buildAgentTools: unknown role tool slug %q in attribute_definition for actor %s — skipping", slug, actorID)
			continue
		}
		out = append(out, def)
		seen[slug] = true
	}
	return out
}

// loadToolSlugsForActor returns every tool slug the actor's attributes
// declare, flattened across all of their attribute_definition.tools
// arrays. Order: attribute slug ASC, then array order within each.
// A bad JSON blob in any one definition logs and skips that row.
func (app *App) loadToolSlugsForActor(ctx context.Context, actorID string) ([]string, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT d.slug, d.tools
		  FROM actor_attribute a
		  JOIN attribute_definition d ON d.slug = a.slug
		 WHERE a.actor_id = $1
		 ORDER BY a.slug
	`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var attrSlug string
		var raw []byte
		if err := rows.Scan(&attrSlug, &raw); err != nil {
			return nil, err
		}
		var arr []string
		if err := json.Unmarshal(raw, &arr); err != nil {
			log.Printf("loadToolSlugsForActor: bad tools JSON for attribute %s actor %s: %v", attrSlug, actorID, err)
			continue
		}
		slugs = append(slugs, arr...)
	}
	return slugs, nil
}

// actorIsVendor returns true when the actor holds any role attribute
// that declares the 'serve' tool — today that's blacksmith, herbalist,
// merchant, and tavernkeeper. Used by the perception builder (ZBBS-114)
// to relabel the inventory line as "Items you can sell" for vendors,
// reinforcing the role-prompt grounding rule against off-list offers.
// Errors during slug load are treated as not-a-vendor — the relabel
// is purely informational; falling back to "Your inventory" is safe.
func (app *App) actorIsVendor(ctx context.Context, actorID string) bool {
	slugs, err := app.loadToolSlugsForActor(ctx, actorID)
	if err != nil {
		return false
	}
	for _, s := range slugs {
		if s == "serve" {
			return true
		}
	}
	return false
}

// loadInstructionsForActor returns a single perception-section string
// composed from every attribute_definition.instructions row the actor
// holds. Empty string when the actor has no attributes carrying
// non-empty instructions. Format:
//
//   Your roles:
//   <DisplayName> — <instruction text>
//   <DisplayName> — <instruction text>
//
// Order: attribute slug ASC. Each instruction is a single block — the
// engine doesn't try to merge or rewrite, so the text written into
// attribute_definition is what the model sees verbatim under its
// own role's display name.
func (app *App) loadInstructionsForActor(ctx context.Context, actorID string) (string, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT d.display_name, d.instructions
		  FROM actor_attribute a
		  JOIN attribute_definition d ON d.slug = a.slug
		 WHERE a.actor_id = $1
		   AND d.instructions <> ''
		 ORDER BY a.slug
	`, actorID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var displayName, instructions string
		if err := rows.Scan(&displayName, &instructions); err != nil {
			continue
		}
		entries = append(entries, fmt.Sprintf("%s — %s", displayName, instructions))
	}
	if len(entries) == 0 {
		return "", nil
	}
	return "Your roles:\n" + strings.Join(entries, "\n"), nil
}
