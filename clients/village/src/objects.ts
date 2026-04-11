// Placed objects — stores what's on the map and handles persistence

import { getConfig } from "./config";

export interface PlacedObject {
    id: string;
    catalogId: string;
    x: number;   // world pixel position (anchor point)
    y: number;
}

const STORAGE_KEY = "village_objects";
const VILLAGE_VERSION = 10; // bump this to regenerate the initial village
let objects: PlacedObject[] = [];
let nextId = 1;

export function getObjects(): PlacedObject[] {
    return objects;
}

export function addObject(catalogId: string, x: number, y: number): PlacedObject {
    const obj: PlacedObject = {
        id: `obj_${nextId++}`,
        catalogId,
        x,
        y,
    };
    objects.push(obj);
    saveObjects();
    return obj;
}

export function removeObject(id: string): void {
    objects = objects.filter(o => o.id !== id);
    saveObjects();
}

export function clearObjects(): void {
    objects = [];
    nextId = 1;
    saveObjects();
}

function saveObjects(): void {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ objects, nextId, version: VILLAGE_VERSION }));
}

export function loadObjects(): void {
    try {
        const stored = localStorage.getItem(STORAGE_KEY);
        if (stored) {
            const data = JSON.parse(stored);
            if (data.version === VILLAGE_VERSION) {
                objects = data.objects || [];
                nextId = data.nextId || 1;
                return;
            }
            // Version mismatch — regenerate
        }
    } catch {
        // ignore
    }
    // No saved data or outdated version — generate initial village
    generateInitialVillage();
}

// Get objects sorted by Y position for depth-correct rendering
export function getObjectsSortedByDepth(): PlacedObject[] {
    return [...objects].sort((a, b) => a.y - b.y);
}

// Populate the village with initial objects to make it look alive
function generateInitialVillage(): void {
    const config = getConfig();
    const TILE = 32; // render tile size
    const w = config.mapWidth;
    const h = config.mapHeight;
    const midX = Math.floor(w / 2);
    const midY = Math.floor(h / 2);
    const riverBaseX = Math.floor(w * 0.78);

    // Seeded random for deterministic placement
    let seed = 137;
    function rand(): number {
        seed = (seed * 16807 + 0) % 2147483647;
        return seed / 2147483647;
    }

    // Helper: add object at tile position (converts to world pixels)
    function place(catalogId: string, tileX: number, tileY: number): void {
        addObject(catalogId, tileX * TILE + TILE / 2, tileY * TILE + TILE / 2);
    }

    // Helper: add with sub-tile offset for natural variation
    function placeRandom(catalogId: string, tileX: number, tileY: number): void {
        const ox = (rand() - 0.5) * TILE * 0.6;
        const oy = (rand() - 0.5) * TILE * 0.4;
        addObject(catalogId, tileX * TILE + TILE / 2 + ox, tileY * TILE + TILE / 2 + oy);
    }

    const treeTypes = ["tree-maple", "tree-chestnut", "tree-birch"];

    // Forest clusters — place trees in the dark grass areas
    const forestAreas = [
        { cx: 8, cy: 6, r: 5 },
        { cx: w - 8, cy: 6, r: 4 },
        { cx: 6, cy: h - 8, r: 4 },
        { cx: Math.floor(w * 0.6), cy: Math.floor(h * 0.72), r: 3 },
        { cx: 4, cy: Math.floor(h * 0.4), r: 2 },
    ];

    for (const area of forestAreas) {
        for (let dy = -area.r; dy <= area.r; dy += 2) {
            for (let dx = -area.r; dx <= area.r; dx += 2) {
                const dist = Math.sqrt(dx * dx + dy * dy);
                if (dist < area.r && rand() < 0.7) {
                    const tx = area.cx + dx;
                    const ty = area.cy + dy;
                    if (tx >= 1 && tx < w - 1 && ty >= 1 && ty < h - 1) {
                        const tree = treeTypes[Math.floor(rand() * treeTypes.length)];
                        placeRandom(tree, tx, ty);
                    }
                }
            }
        }
    }

    // Border trees — along the map edges
    for (let x = 1; x < w - 1; x += 3) {
        if (rand() < 0.6) {
            const tree = treeTypes[Math.floor(rand() * treeTypes.length)];
            placeRandom(tree, x, 1 + Math.floor(rand() * 2));
        }
        if (rand() < 0.6) {
            const tree = treeTypes[Math.floor(rand() * treeTypes.length)];
            placeRandom(tree, x, h - 2 - Math.floor(rand() * 2));
        }
    }
    for (let y = 3; y < h - 3; y += 3) {
        if (rand() < 0.5) {
            const tree = treeTypes[Math.floor(rand() * treeTypes.length)];
            placeRandom(tree, 1 + Math.floor(rand() * 2), y);
        }
        // Skip trees on the right border near the river
        if (rand() < 0.4 && Math.abs(y - midY) > 3) {
            const tree = treeTypes[Math.floor(rand() * treeTypes.length)];
            placeRandom(tree, w - 2 - Math.floor(rand() * 2), y);
        }
    }

    // Scattered bushes and rocks
    for (let i = 0; i < 20; i++) {
        const x = 3 + Math.floor(rand() * (w - 6));
        const y = 3 + Math.floor(rand() * (h - 6));
        // Don't place on roads, water, or town square
        if (Math.abs(x - midX) < 4 && Math.abs(y - midY) < 4) continue;
        if (Math.abs(y - midY) < 2) continue; // horizontal road
        if (Math.abs(x - midX) < 2) continue; // vertical road
        if (Math.abs(x - riverBaseX) < 4) continue; // river

        const r = rand();
        if (r < 0.3) placeRandom("bush", x, y);
        else if (r < 0.5) placeRandom("bush-small", x, y);
        else if (r < 0.7) placeRandom("rock-small", x, y);
        else if (r < 0.85) placeRandom("stump", x, y);
        else placeRandom("log-pile", x, y);
    }

    // River rocks
    for (let y = 2; y < h - 2; y += 4) {
        if (rand() < 0.4) {
            const rx = riverBaseX + Math.floor(Math.sin(y * 0.15) * 2);
            placeRandom("rock-water", rx - 1, y);
        }
    }

    // Town square — well, market stalls, lamp posts
    place("well-roof", midX, midY - 2);
    place("stall-tiled", midX - 4, midY - 1);
    place("stall-wood", midX + 4, midY - 1);
    place("lamppost", midX - 2, midY + 2);
    place("lamppost-sign", midX + 2, midY + 2);

    // Barrels and crates near market stalls
    placeRandom("barrel", midX - 5, midY);
    placeRandom("crate", midX - 5, midY + 1);
    placeRandom("barrel", midX + 5, midY);
    placeRandom("wood-pile", midX + 5, midY + 1);

    // Wagon on the road
    place("wagon-covered", midX - 8, midY);

    // Bridge over the river (sprite on top of water tiles)
    const bridgeY = midY + Math.floor(Math.sin(riverBaseX * 0.1) * 1);
    const bridgeX = riverBaseX + Math.floor(Math.sin(bridgeY * 0.15) * 2) + 1;
    // Offset down slightly so the arch sits centered on the water
    addObject("bridge", bridgeX * TILE + TILE / 2, bridgeY * TILE + TILE);

    saveObjects();
}
