-- LLM-290 down: restore the phantom 'coin' item_kind in its minted shape.
--
-- The up migration deleted a discovery-minted kind (mintDiscoveredKind,
-- item_discovery.go): display_label = the key with underscores as spaces,
-- category 'unknown', no satisfies / recipe / capabilities / price. That is
-- the complete row — a mint writes nothing else — so this restore is exact.
-- Only 'coin' is restored ('coins' plural was included in the up's guard
-- range but did not exist live 2026-07-06). ON CONFLICT DO NOTHING: if a
-- real coin item has since been authored under the same name, keep it.

BEGIN;

INSERT INTO item_kind (name, display_label, category, sort_order)
VALUES ('coin', 'coin', 'unknown', 0)
ON CONFLICT (name) DO NOTHING;

COMMIT;
