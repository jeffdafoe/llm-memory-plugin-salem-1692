import { TILE_SIZE, WANG_SRC_SIZE, TOP_BAR_HEIGHT } from "./constants";
import { WangTerrainType, WangTerrain } from "./terrain";
import { Camera } from "./camera";
import { Tileset } from "./sprites";
import { getMapDimensions } from "./map";
import { WANG_LOOKUP } from "./wang-lookup";

// Background color — earthy brown for the wilderness outside the map
const BG_COLOR = "#3e3222";

// Water sparkle animation — 4 frames at 16x16, 3 rows of variants
const SPARKLE_FRAMES = 4;
const SPARKLE_ROWS = 3;
const SPARKLE_INTERVAL = 250; // ms per frame

// Fallback colors when wang tileset hasn't loaded yet
const TERRAIN_COLORS: Record<number, string> = {
    1: "#8b7355",  // dirt
    2: "#4a7c2e",  // light grass
    3: "#3d6b24",  // dark grass
    4: "#7a7a7a",  // cobblestone
    5: "#3a7ab5",  // shallow water
    6: "#2e5984",  // deep water
};

export class Renderer {
    private canvas: HTMLCanvasElement;
    private ctx: CanvasRenderingContext2D;
    private camera: Camera;
    private map: WangTerrainType[][];
    private wangTileset: Tileset;
    private sparkleTileset: Tileset;

    constructor(canvas: HTMLCanvasElement, camera: Camera, map: WangTerrainType[][]) {
        this.canvas = canvas;
        this.ctx = canvas.getContext("2d")!;
        this.camera = camera;
        this.map = map;
        // Wang tiles sheet is 16px native — we scale to 32px when drawing
        this.wangTileset = new Tileset("/assets/tilesets/wang.png", WANG_SRC_SIZE);
        this.sparkleTileset = new Tileset("/assets/tilesets/water-sparkles.png", WANG_SRC_SIZE);
        this.ctx.imageSmoothingEnabled = false;
    }

    setMap(map: WangTerrainType[][]): void {
        this.map = map;
    }

    resize(): void {
        this.canvas.width = window.innerWidth;
        this.canvas.height = window.innerHeight - TOP_BAR_HEIGHT;
        this.ctx.imageSmoothingEnabled = false;
    }

    render(): void {
        const ctx = this.ctx;
        const w = this.canvas.width;
        const h = this.canvas.height;
        const dims = getMapDimensions();
        const mapWidth = dims.width;
        const mapHeight = dims.height;

        // Clear with background color
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.fillStyle = BG_COLOR;
        ctx.fillRect(0, 0, w, h);

        // Clamp and apply camera
        this.camera.clamp(w, h);
        this.camera.apply(ctx, w, h);
        ctx.imageSmoothingEnabled = false;

        // Compute visible tile range
        const viewLeft = this.camera.x - w / (2 * this.camera.zoom);
        const viewTop = this.camera.y - h / (2 * this.camera.zoom);
        const viewRight = this.camera.x + w / (2 * this.camera.zoom);
        const viewBottom = this.camera.y + h / (2 * this.camera.zoom);

        const startCol = Math.max(0, Math.floor(viewLeft / TILE_SIZE));
        const endCol = Math.min(mapWidth - 1, Math.ceil(viewRight / TILE_SIZE));
        const startRow = Math.max(0, Math.floor(viewTop / TILE_SIZE));
        const endRow = Math.min(mapHeight - 1, Math.ceil(viewBottom / TILE_SIZE));

        // Draw visible tiles
        for (let y = startRow; y <= endRow; y++) {
            for (let x = startCol; x <= endCol; x++) {
                if (y >= this.map.length || x >= this.map[y].length) {
                    continue;
                }

                const worldX = x * TILE_SIZE;
                const worldY = y * TILE_SIZE;

                if (this.wangTileset.isLoaded()) {
                    this.drawWangTile(ctx, x, y, worldX, worldY);
                } else {
                    // Fallback to colored rectangles
                    const terrain = this.map[y][x];
                    ctx.fillStyle = TERRAIN_COLORS[terrain] || "#ff00ff";
                    ctx.fillRect(worldX, worldY, TILE_SIZE + 1, TILE_SIZE + 1);
                }

                // Water sparkle overlay
                const terrain = this.map[y][x];
                if ((terrain === WangTerrain.SHALLOW_WATER || terrain === WangTerrain.DEEP_WATER) && this.sparkleTileset.isLoaded()) {
                    // Pick animation frame based on time, with per-tile offset so they don't all sync
                    const time = performance.now();
                    const tileOffset = ((x * 3) + (y * 7)) % SPARKLE_FRAMES;
                    const frame = (Math.floor(time / SPARKLE_INTERVAL) + tileOffset) % SPARKLE_FRAMES;
                    // Pick a sparkle row variant based on tile position
                    const row = ((x * 5) + (y * 11)) % SPARKLE_ROWS;
                    ctx.globalAlpha = 0.6;
                    this.sparkleTileset.draw(ctx, frame, row, worldX, worldY, TILE_SIZE);
                    ctx.globalAlpha = 1.0;
                }
            }
        }
    }

    // Look up and draw the correct wang tile based on corner terrain types.
    // Each tile's 4 corners are determined by the terrain of the tile itself
    // and its 3 neighbors that share that corner.
    private drawWangTile(ctx: CanvasRenderingContext2D, tileX: number, tileY: number, worldX: number, worldY: number): void {
        const tl = this.getCornerTerrain(tileX, tileY, -1, -1); // top-left corner
        const tr = this.getCornerTerrain(tileX, tileY, 1, -1);  // top-right corner
        const br = this.getCornerTerrain(tileX, tileY, 1, 1);   // bottom-right corner
        const bl = this.getCornerTerrain(tileX, tileY, -1, 1);  // bottom-left corner

        const key = `${tl},${tr},${br},${bl}`;
        const tiles = WANG_LOOKUP[key];

        if (tiles && tiles.length > 0) {
            // Pick a variant deterministically based on position
            const hash = ((tileX * 7) + (tileY * 13)) & 0xFFFF;
            const tile = tiles[hash % tiles.length];
            // Draw 1px larger to prevent sub-pixel seams between adjacent tiles
            this.wangTileset.draw(ctx, tile.col, tile.row, worldX, worldY, TILE_SIZE, 1);
        } else {
            // No wang tile for this combo — fall back to solid fill
            const terrain = this.map[tileY][tileX];
            ctx.fillStyle = TERRAIN_COLORS[terrain] || "#ff00ff";
            ctx.fillRect(worldX, worldY, TILE_SIZE, TILE_SIZE);
        }
    }

    // Get the terrain type for a corner of a tile.
    // A corner is shared by 4 tiles. The corner terrain is the terrain
    // of the tile at (tileX + dx, tileY + dy) offset, where dx/dy indicate
    // which corner (-1/-1 = top-left, +1/-1 = top-right, etc.).
    // For wang corner tiles, each corner samples from 4 neighboring tiles
    // and picks the dominant terrain. But the simpler (and correct for Mana Seed)
    // approach: the corner terrain equals the terrain of the tile that the
    // corner is "inside" of — i.e. the tile at the diagonal offset.
    //
    // Actually, for Tiled's corner wang tiles, each corner of a tile gets
    // a terrain value. The convention is:
    // - TL corner of tile (x,y) = terrain at (x,y) if homogeneous,
    //   but for transitions, the corner is the terrain that "owns" that corner.
    //
    // The simplest working approach: each corner takes the terrain from
    // the diagonal neighbor. If that neighbor is off-map, use the tile's own terrain.
    private getCornerTerrain(tileX: number, tileY: number, dx: number, dy: number): WangTerrainType {
        const dims = getMapDimensions();

        // The 4 tiles that share this corner
        const tiles: WangTerrainType[] = [];

        // Which tiles share a corner depends on which corner we're looking at.
        // For the top-left corner of tile (x,y), the 4 tiles sharing it are:
        //   (x,y), (x-1,y), (x,y-1), (x-1,y-1)
        // For top-right: (x,y), (x+1,y), (x,y-1), (x+1,y-1)
        // etc.
        //
        // The offsets for the 4 tiles sharing a corner:
        const ox = dx < 0 ? -1 : 0;
        const oy = dy < 0 ? -1 : 0;

        for (let cy = 0; cy <= 1; cy++) {
            for (let cx = 0; cx <= 1; cx++) {
                const nx = tileX + ox + cx;
                const ny = tileY + oy + cy;
                if (nx >= 0 && nx < dims.width && ny >= 0 && ny < dims.height) {
                    tiles.push(this.map[ny][nx]);
                }
            }
        }

        // Pick the most common terrain among the sharing tiles
        // This gives smooth transitions — the majority terrain "wins" each corner
        if (tiles.length === 0) {
            return this.map[tileY][tileX];
        }

        // Count occurrences
        const counts = new Map<WangTerrainType, number>();
        for (const t of tiles) {
            counts.set(t, (counts.get(t) || 0) + 1);
        }

        // Return the terrain with the highest count
        let best = tiles[0];
        let bestCount = 0;
        for (const [terrain, count] of counts) {
            if (count > bestCount) {
                bestCount = count;
                best = terrain;
            }
        }

        return best;
    }

}
