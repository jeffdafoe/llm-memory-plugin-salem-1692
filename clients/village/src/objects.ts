// Placed objects — stores what's on the map, loaded from server API

import { getConfig } from "./config";
import { getToken } from "./auth";

export interface PlacedObject {
    id: string;
    catalogId: string;
    x: number;   // world pixel position (anchor point)
    y: number;
    owner?: string | null;
}

let objects: PlacedObject[] = [];
let loaded = false;

export function getObjects(): PlacedObject[] {
    return objects;
}

// Fetch all objects from the server
export async function loadObjects(): Promise<void> {
    try {
        const resp = await apiFetch("/api/village/objects");
        if (resp.ok) {
            const data = await resp.json();
            if (data.length > 0) {
                objects = data.map((o: any) => ({
                    id: o.id,
                    catalogId: o.catalog_id,
                    x: o.x,
                    y: o.y,
                    owner: o.owner,
                }));
                loaded = true;
                return;
            }
            // Empty DB — generate initial village
        }
    } catch {
        // API unavailable — fall through to generate locally
    }

    if (!loaded) {
        await generateInitialVillage();
        loaded = true;
    }
}

export async function addObject(catalogId: string, x: number, y: number): Promise<PlacedObject> {
    // Try API first
    try {
        const resp = await apiFetch("/api/village/objects", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ catalog_id: catalogId, x, y }),
        });
        if (resp.ok) {
            const data = await resp.json();
            const obj: PlacedObject = {
                id: data.id,
                catalogId: data.catalog_id,
                x: data.x,
                y: data.y,
                owner: data.owner,
            };
            objects.push(obj);
            return obj;
        }
    } catch {
        // Fall through to local
    }

    // Local fallback
    const obj: PlacedObject = {
        id: `local_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`,
        catalogId,
        x,
        y,
    };
    objects.push(obj);
    return obj;
}

export async function removeObject(id: string): Promise<void> {
    // Try API first
    try {
        const resp = await apiFetch(`/api/village/objects/${id}`, { method: "DELETE" });
        if (resp.ok || resp.status === 404) {
            objects = objects.filter(o => o.id !== id);
            return;
        }
    } catch {
        // Fall through to local
    }
    objects = objects.filter(o => o.id !== id);
}

export function clearObjects(): void {
    objects = [];
    loaded = false;
}

// Get objects sorted by Y position for depth-correct rendering
export function getObjectsSortedByDepth(): PlacedObject[] {
    return [...objects].sort((a, b) => a.y - b.y);
}

// API helper with auth token
function apiFetch(url: string, init?: RequestInit): Promise<Response> {
    const token = getToken();
    const headers: Record<string, string> = {
        ...(init?.headers as Record<string, string> || {}),
    };
    if (token) {
        headers["Authorization"] = `Bearer ${token}`;
    }
    return fetch(url, { ...init, headers });
}

// Populate the village with initial objects (used when API has no data or is unavailable)
async function generateInitialVillage(): Promise<void> {
    const config = getConfig();
    const TILE = 32;
    const w = config.mapWidth;
    const h = config.mapHeight;
    const midX = Math.floor(w / 2);
    const midY = Math.floor(h / 2);
    const riverBaseX = Math.floor(w * 0.78);

    let seed = 137;
    function rand(): number {
        seed = (seed * 16807 + 0) % 2147483647;
        return seed / 2147483647;
    }

    // Collect all objects to bulk-create
    const batch: Array<{ catalog_id: string; x: number; y: number }> = [];

    function place(catalogId: string, tileX: number, tileY: number): void {
        batch.push({ catalog_id: catalogId, x: tileX * TILE + TILE / 2, y: tileY * TILE + TILE / 2 });
    }

    function placeRandom(catalogId: string, tileX: number, tileY: number): void {
        const ox = (rand() - 0.5) * TILE * 0.6;
        const oy = (rand() - 0.5) * TILE * 0.4;
        batch.push({ catalog_id: catalogId, x: tileX * TILE + TILE / 2 + ox, y: tileY * TILE + TILE / 2 + oy });
    }

    const treeTypes = ["tree-maple", "tree-chestnut", "tree-birch"];

    // Forest clusters
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

    // Border trees
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
        if (rand() < 0.4 && Math.abs(y - midY) > 3) {
            const tree = treeTypes[Math.floor(rand() * treeTypes.length)];
            placeRandom(tree, w - 2 - Math.floor(rand() * 2), y);
        }
    }

    // Scattered bushes and rocks
    for (let i = 0; i < 20; i++) {
        const x = 3 + Math.floor(rand() * (w - 6));
        const y = 3 + Math.floor(rand() * (h - 6));
        if (Math.abs(x - midX) < 4 && Math.abs(y - midY) < 4) continue;
        if (Math.abs(y - midY) < 2) continue;
        if (Math.abs(x - midX) < 2) continue;
        if (Math.abs(x - riverBaseX) < 4) continue;

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

    // Town square
    place("well-roof", midX, midY - 2);
    place("stall-tiled", midX - 4, midY - 1);
    place("stall-wood", midX + 4, midY - 1);
    place("lamppost", midX - 2, midY + 2);
    place("lamppost-sign", midX + 2, midY + 2);

    placeRandom("barrel", midX - 5, midY);
    placeRandom("crate", midX - 5, midY + 1);
    placeRandom("barrel", midX + 5, midY);
    placeRandom("wood-pile", midX + 5, midY + 1);

    place("wagon-covered", midX - 8, midY);

    // Bridge
    const bridgeY = midY + Math.floor(Math.sin(riverBaseX * 0.1) * 1);
    const bridgeX = riverBaseX + Math.floor(Math.sin(bridgeY * 0.15) * 2) + 1;
    batch.push({ catalog_id: "bridge", x: bridgeX * TILE + TILE / 2, y: bridgeY * TILE + TILE });

    // Try bulk create via API
    try {
        const resp = await apiFetch("/api/village/objects/bulk", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ objects: batch }),
        });
        if (resp.ok) {
            const data = await resp.json();
            objects = data.map((o: any) => ({
                id: o.id,
                catalogId: o.catalog_id,
                x: o.x,
                y: o.y,
                owner: o.owner,
            }));
            return;
        }
    } catch {
        // API unavailable — store locally
    }

    // Local fallback
    for (const item of batch) {
        objects.push({
            id: `local_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`,
            catalogId: item.catalog_id,
            x: item.x,
            y: item.y,
        });
    }
}
