// Object catalog — defines all placeable items with their sprite info

export interface CatalogItem {
    id: string;
    name: string;
    category: "tree" | "nature" | "structure" | "prop";
    sheet: string;        // path to spritesheet (relative to /assets/)
    srcX: number;         // source rect in sheet
    srcY: number;
    srcW: number;
    srcH: number;
    // Anchor point (0-1 normalized) — where the object "stands" on the map.
    // Default is bottom-center (0.5, 1.0) which works for most objects.
    anchorX: number;
    anchorY: number;
    // Which render layer: "objects" draws behind characters, "above" draws on top
    layer: "objects" | "above";
}

const MS = "/assets/tilesets/mana-seed";

export const CATALOG: CatalogItem[] = [
    // === Trees (80x112 each, 3 in sheet) ===
    {
        id: "tree-maple", name: "Maple Tree", category: "tree",
        sheet: `${MS}/summer-forest/summer sheets/summer trees 80x112.png`,
        srcX: 0, srcY: 0, srcW: 80, srcH: 112,
        anchorX: 0.5, anchorY: 0.93, layer: "objects",
    },
    {
        id: "tree-chestnut", name: "Chestnut Tree", category: "tree",
        sheet: `${MS}/summer-forest/summer sheets/summer trees 80x112.png`,
        srcX: 80, srcY: 0, srcW: 80, srcH: 112,
        anchorX: 0.5, anchorY: 0.93, layer: "objects",
    },
    {
        id: "tree-birch", name: "Birch Tree", category: "tree",
        sheet: `${MS}/summer-forest/summer sheets/summer trees 80x112.png`,
        srcX: 160, srcY: 0, srcW: 80, srcH: 112,
        anchorX: 0.5, anchorY: 0.93, layer: "objects",
    },

    // === Small nature objects (32x32 each, 7 in sheet) ===
    {
        id: "bush", name: "Bush", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 0, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "bush-berries", name: "Berry Bush", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 32, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "rock-small", name: "Small Rock", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 64, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "rock-water", name: "River Rock", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 96, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "stump", name: "Tree Stump", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 128, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "log-pile", name: "Log Pile", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 160, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "bush-small", name: "Small Bush", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 32x32.png`,
        srcX: 192, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },

    // === Medium nature objects (48x32 each) ===
    {
        id: "stump-big", name: "Big Stump", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 48x32.png`,
        srcX: 0, srcY: 0, srcW: 48, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "fallen-log", name: "Fallen Log", category: "nature",
        sheet: `${MS}/summer-forest/summer sheets/summer 48x32.png`,
        srcX: 48, srcY: 0, srcW: 48, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },

    // === Bridge ===
    {
        id: "bridge", name: "Bridge", category: "structure",
        sheet: `${MS}/summer-forest/extras/bonus bridge.png`,
        srcX: 0, srcY: 0, srcW: 64, srcH: 48,
        anchorX: 0.5, anchorY: 0.7, layer: "objects",
    },

    // === Village accessories (32x32 grid, 128x160 sheet = 4 cols × 5 rows) ===
    {
        id: "barrel", name: "Barrel", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 32x32.png`,
        srcX: 0, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "barrel-open", name: "Open Barrel", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 32x32.png`,
        srcX: 32, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "wood-pile", name: "Wood Pile", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 32x32.png`,
        srcX: 64, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "wood-shelter", name: "Wood Shelter", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 32x32.png`,
        srcX: 96, srcY: 0, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "crate", name: "Crate", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 32x32.png`,
        srcX: 0, srcY: 32, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "millstone", name: "Millstone", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 32x32.png`,
        srcX: 64, srcY: 32, srcW: 32, srcH: 32,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },

    // === Village accessories — wells and large objects (48x80 each) ===
    {
        id: "well-empty", name: "Well (Empty)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 0, srcY: 0, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "well-bucket", name: "Well (Bucket)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 48, srcY: 0, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "well-roof", name: "Well (Roofed)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 96, srcY: 0, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "well-wishing", name: "Wishing Well", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 144, srcY: 0, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "shop-front", name: "Shop Front", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 192, srcY: 0, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },

    // === Market stalls and wagons (80x96 each, 3 cols × 4 rows) ===
    {
        id: "stall-wood", name: "Market Stall (Wood)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 0, srcY: 0, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "stall-tiled", name: "Market Stall (Tiled)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 80, srcY: 0, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "stall-fancy", name: "Market Stall (Fancy)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 160, srcY: 0, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "stall-closed-wood", name: "Closed Stall (Wood)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 0, srcY: 96, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "stall-closed-tiled", name: "Closed Stall (Tiled)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 80, srcY: 96, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "stall-closed-fancy", name: "Closed Stall (Fancy)", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 160, srcY: 96, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "wagon", name: "Wagon", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 80, srcY: 288, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },
    {
        id: "wagon-covered", name: "Covered Wagon", category: "structure",
        sheet: `${MS}/village-accessories/village accessories 80x96.png`,
        srcX: 160, srcY: 288, srcW: 80, srcH: 96,
        anchorX: 0.5, anchorY: 0.85, layer: "objects",
    },

    // === Lamp posts (48x80 grid, rows 1-3) ===
    {
        id: "lamppost", name: "Lamp Post", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 240, srcY: 80, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.9, layer: "objects",
    },
    {
        id: "lamppost-sign", name: "Lamp Post (Sign)", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 336, srcY: 80, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.9, layer: "objects",
    },
    {
        id: "lamppost-banner", name: "Lamp Post (Banner)", category: "prop",
        sheet: `${MS}/village-accessories/village accessories 48x80.png`,
        srcX: 432, srcY: 80, srcW: 48, srcH: 80,
        anchorX: 0.5, anchorY: 0.9, layer: "objects",
    },
];

// Quick lookup by ID
const catalogMap = new Map<string, CatalogItem>();
for (const item of CATALOG) {
    catalogMap.set(item.id, item);
}

export function getCatalogItem(id: string): CatalogItem | undefined {
    return catalogMap.get(id);
}

export function getCatalogByCategory(category: CatalogItem["category"]): CatalogItem[] {
    return CATALOG.filter(item => item.category === category);
}
