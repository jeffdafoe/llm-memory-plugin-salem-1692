import { Terrain, WangTerrainType } from "./terrain";
import { getConfig } from "./config";

// Seeded pseudo-random for deterministic map generation
function seededRandom(seed: number): () => number {
    let s = seed;
    return () => {
        s = (s * 16807 + 0) % 2147483647;
        return s / 2147483647;
    };
}

// Total map dimensions in tiles
export function getMapDimensions(): { width: number; height: number } {
    const config = getConfig();
    return {
        width: config.mapWidth,
        height: config.mapHeight,
    };
}

// Generate the village map as a 2D array of wang terrain indices.
// At 32px tiles, 64x48 = 2048x1536px world space.
export function createMap(): WangTerrainType[][] {
    const config = getConfig();
    const width = config.mapWidth;
    const height = config.mapHeight;
    const rand = seededRandom(42);
    const map: WangTerrainType[][] = [];

    // Fill with light grass — mix in dark grass for variety
    for (let y = 0; y < height; y++) {
        map[y] = [];
        for (let x = 0; x < width; x++) {
            const r = rand();
            if (r < 0.12) {
                map[y][x] = Terrain.GRASS_DARK;
            } else {
                map[y][x] = Terrain.GRASS;
            }
        }
    }

    // Roads — 3 tiles wide with slight curve
    const midY = Math.floor(height / 2);
    const midX = Math.floor(width / 2);

    // Horizontal road
    for (let x = 0; x < width; x++) {
        const curve = Math.floor(Math.sin(x * 0.1) * 1);
        for (let dy = -1; dy <= 1; dy++) {
            const ry = midY + curve + dy;
            if (ry >= 0 && ry < height) {
                map[ry][x] = Terrain.DIRT_PATH;
            }
        }
    }

    // Vertical road
    for (let y = 0; y < height; y++) {
        const curve = Math.floor(Math.sin(y * 0.08) * 1);
        for (let dx = -1; dx <= 1; dx++) {
            const rx = midX + curve + dx;
            if (rx >= 0 && rx < width) {
                map[y][rx] = Terrain.DIRT_PATH;
            }
        }
    }

    // River along the eastern side — 3 tiles wide, sinuous
    const riverBaseX = Math.floor(width * 0.78);
    for (let y = 0; y < height; y++) {
        const riverX = riverBaseX + Math.floor(Math.sin(y * 0.15) * 2);
        for (let dx = 0; dx < 3; dx++) {
            const rx = riverX + dx;
            if (rx >= 0 && rx < width) {
                // Deep water in center, shallow at edges
                if (dx === 1) {
                    map[y][rx] = Terrain.DEEP_WATER;
                } else {
                    map[y][rx] = Terrain.WATER;
                }
            }
        }
    }

    // Bridge crossing — keep water tiles here, the bridge sprite goes on top

    // Forest clusters — dark grass patches (trees will be objects later)
    const forestAreas = [
        { cx: 8, cy: 6, r: 6 },
        { cx: width - 8, cy: 6, r: 5 },
        { cx: 6, cy: height - 8, r: 5 },
        { cx: Math.floor(width * 0.6), cy: Math.floor(height * 0.72), r: 4 },
        { cx: 4, cy: Math.floor(height * 0.4), r: 3 },
    ];
    for (const area of forestAreas) {
        for (let dy = -area.r; dy <= area.r; dy++) {
            for (let dx = -area.r; dx <= area.r; dx++) {
                const tx = area.cx + dx;
                const ty = area.cy + dy;
                const dist = Math.sqrt(dx * dx + dy * dy);
                const noise = rand() * 2;
                if (tx >= 0 && tx < width && ty >= 0 && ty < height && dist < area.r + noise - 1) {
                    if (map[ty][tx] === Terrain.GRASS || map[ty][tx] === Terrain.GRASS_DARK) {
                        map[ty][tx] = Terrain.GRASS_DARK;
                    }
                }
            }
        }
    }

    // Cobblestone town square at the crossroads
    for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
            if (dx * dx + dy * dy <= 5) {
                const sx = midX + dx;
                const sy = midY + dy;
                if (sx >= 0 && sx < width && sy >= 0 && sy < height) {
                    map[sy][sx] = Terrain.STONE;
                }
            }
        }
    }

    // Dense dark grass border around map edges — thick forest boundary
    const borderRand = seededRandom(99);
    const borderDepth = 8;
    for (let y = 0; y < height; y++) {
        for (let x = 0; x < width; x++) {
            const distLeft = x;
            const distRight = width - 1 - x;
            const distTop = y;
            const distBottom = height - 1 - y;
            const distEdge = Math.min(distLeft, distRight, distTop, distBottom);

            if (distEdge < borderDepth) {
                const prob = 1 - (distEdge / borderDepth);
                if (borderRand() < prob * 0.9) {
                    map[y][x] = Terrain.GRASS_DARK;
                }
            }
        }
    }

    return map;
}
