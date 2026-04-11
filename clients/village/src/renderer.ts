import { TILE_SIZE, WANG_SRC_SIZE, TOP_BAR_HEIGHT } from "./constants";
import { WangTerrainType, WangTerrain } from "./terrain";
import { Camera } from "./camera";
import { Tileset } from "./sprites";
import { getMapDimensions } from "./map";
import { WANG_LOOKUP } from "./wang-lookup";
import { getObjectsSortedByDepth, getObjects } from "./objects";
import { getCatalogItem } from "./catalog";
import { Editor } from "./editor";
import { VillageAgent } from "./village-api";

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

// Sprite sheet image cache — shared across all objects using the same sheet
const sheetCache = new Map<string, HTMLImageElement>();

function getSheet(src: string): HTMLImageElement {
    let img = sheetCache.get(src);
    if (!img) {
        img = new Image();
        img.src = src;
        sheetCache.set(src, img);
    }
    return img;
}

export class Renderer {
    private canvas: HTMLCanvasElement;
    private ctx: CanvasRenderingContext2D;
    private camera: Camera;
    private map: WangTerrainType[][];
    private wangTileset: Tileset;
    private sparkleTileset: Tileset;
    private editor: Editor | null = null;
    private agents: VillageAgent[] = [];
    private tooltipState: (() => { mouse: { x: number; y: number } | null; agents: VillageAgent[]; editorActive: boolean }) | null = null;

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

    setEditor(editor: Editor): void {
        this.editor = editor;
    }

    setAgents(agents: VillageAgent[]): void {
        this.agents = agents;
    }

    setTooltipState(fn: () => { mouse: { x: number; y: number } | null; agents: VillageAgent[]; editorActive: boolean }): void {
        this.tooltipState = fn;
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

        // Draw visible terrain tiles
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

        // Draw objects — sorted by Y for depth ordering
        this.renderObjects(ctx, viewLeft, viewTop, viewRight, viewBottom);

        // Draw outdoor agent dots
        this.renderAgentDots(ctx, viewLeft, viewTop, viewRight, viewBottom);

        // Draw editor overlays (ghost preview, selection highlight)
        this.editor?.renderOverlay(ctx);

        // Draw canvas-based tooltip (in screen space)
        this.renderTooltip(ctx);
    }

    private renderTooltip(ctx: CanvasRenderingContext2D): void {
        if (!this.tooltipState) return;
        const state = this.tooltipState();
        if (!state.mouse || state.editorActive) return;

        const world = this.camera.screenToWorld(state.mouse.x, state.mouse.y, this.canvas.width, this.canvas.height);
        const agents = state.agents;

        // Check outdoor agent dots
        let lines: string[] = [];
        const DOT_RADIUS = 8;
        for (const agent of agents) {
            if (agent.locationType !== "outdoor" || agent.locationX == null || agent.locationY == null) continue;
            const dx = world.x - agent.locationX;
            const dy = world.y - agent.locationY;
            if (dx * dx + dy * dy < DOT_RADIUS * DOT_RADIUS) {
                lines = [agent.name];
                break;
            }
        }

        // Check objects
        if (lines.length === 0) {
            const SCALE = 2;
            for (const obj of getObjects()) {
                const item = getCatalogItem(obj.catalogId);
                if (!item) continue;
                const destW = item.srcW * SCALE;
                const destH = item.srcH * SCALE;
                const drawX = obj.x - destW * item.anchorX;
                const drawY = obj.y - destH * item.anchorY;
                if (world.x >= drawX && world.x <= drawX + destW &&
                    world.y >= drawY && world.y <= drawY + destH) {
                    lines.push(item.name);
                    if (obj.owner) {
                        const ownerAgent = agents.find(a => a.llmMemoryAgent === obj.owner);
                        lines.push("Owner: " + (ownerAgent?.name || obj.owner));
                    }
                    const inside = agents.filter(a =>
                        a.locationType === "inside" && a.locationObjectId === obj.id
                    );
                    if (inside.length > 0) {
                        lines.push("Inside: " + inside.map(a => a.name).join(", "));
                    }
                    break;
                }
            }
        }

        if (lines.length === 0) return;

        // Draw in screen space
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.font = "12px Georgia, serif";
        const padding = 6;
        const lineHeight = 16;
        const maxWidth = Math.max(...lines.map(l => ctx.measureText(l).width));
        const boxW = maxWidth + padding * 2;
        const boxH = lines.length * lineHeight + padding * 2;
        let tx = state.mouse.x + 14;
        let ty = state.mouse.y - 10;
        // Keep on screen
        if (tx + boxW > this.canvas.width) tx = state.mouse.x - boxW - 4;
        if (ty + boxH > this.canvas.height) ty = this.canvas.height - boxH;
        if (ty < 0) ty = 0;

        ctx.fillStyle = "rgba(26, 26, 16, 0.92)";
        ctx.strokeStyle = "#4a4a30";
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.roundRect(tx, ty, boxW, boxH, 3);
        ctx.fill();
        ctx.stroke();

        ctx.fillStyle = "#d4c5a0";
        for (let i = 0; i < lines.length; i++) {
            ctx.fillText(lines[i], tx + padding, ty + padding + (i + 1) * lineHeight - 4);
        }
    }

    private renderAgentDots(
        ctx: CanvasRenderingContext2D,
        viewLeft: number, viewTop: number,
        viewRight: number, viewBottom: number
    ): void {
        const DOT_RADIUS = 6;
        for (const agent of this.agents) {
            if (agent.locationType !== "outdoor" || agent.locationX == null || agent.locationY == null) continue;

            const x = agent.locationX;
            const y = agent.locationY;

            // Frustum cull
            if (x < viewLeft - DOT_RADIUS || x > viewRight + DOT_RADIUS) continue;
            if (y < viewTop - DOT_RADIUS || y > viewBottom + DOT_RADIUS) continue;

            // Draw dot with outline
            ctx.beginPath();
            ctx.arc(x, y, DOT_RADIUS, 0, Math.PI * 2);
            ctx.fillStyle = agent.isVirtual ? "#c8b87a" : "#7ab8c8";
            ctx.fill();
            ctx.strokeStyle = "#1a1a10";
            ctx.lineWidth = 2;
            ctx.stroke();
        }
    }

    private renderObjects(
        ctx: CanvasRenderingContext2D,
        viewLeft: number, viewTop: number,
        viewRight: number, viewBottom: number
    ): void {
        const sorted = getObjectsSortedByDepth();
        const SCALE = 2; // 16px native → 32px render

        for (const obj of sorted) {
            const item = getCatalogItem(obj.catalogId);
            if (!item) continue;

            const sheet = getSheet(item.sheet);
            if (!sheet.complete || sheet.naturalWidth === 0) continue;

            // Destination size (scaled 2x from native 16px art)
            const destW = item.srcW * SCALE;
            const destH = item.srcH * SCALE;

            // Position: anchor point is at (obj.x, obj.y)
            const drawX = obj.x - destW * item.anchorX;
            const drawY = obj.y - destH * item.anchorY;

            // Frustum cull
            if (drawX + destW < viewLeft || drawX > viewRight) continue;
            if (drawY + destH < viewTop || drawY > viewBottom) continue;

            ctx.drawImage(
                sheet,
                item.srcX, item.srcY, item.srcW, item.srcH,
                drawX, drawY, destW, destH
            );
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
    private getCornerTerrain(tileX: number, tileY: number, dx: number, dy: number): WangTerrainType {
        const dims = getMapDimensions();

        // The 4 tiles that share this corner
        const tiles: WangTerrainType[] = [];

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
