// In-game village editor — catalog sidebar, click-to-place, select & delete

import { Asset, getAssetsByCategory, getCatalogItem } from "./catalog";
import { addObject, getObjects, removeObject } from "./objects";
import { Camera } from "./camera";
import { TILE_SIZE } from "./constants";
import { fetchVillageAgents, setObjectOwner, moveObjectPosition, VillageAgent } from "./village-api";
import { getMapDimensions } from "./map";

type EditorMode = "select" | "place";

export class Editor {
    private canvas: HTMLCanvasElement;
    private camera: Camera;
    private panel: HTMLElement | null = null;
    private active = false;
    private mode: EditorMode = "select";
    private selectedAssetId: string | null = null;
    private selectedObjectId: string | null = null;
    private ghostPos: { x: number; y: number } | null = null;
    private onToggle: ((active: boolean) => void) | null = null;
    private agents: VillageAgent[] = [];

    // Drag-to-move state
    private dragging = false;
    private dragObjectId: string | null = null;
    private dragStartWorld: { x: number; y: number } | null = null;
    private dragOriginalPos: { x: number; y: number } | null = null;
    private edgePanTimer: number | null = null;

    constructor(canvas: HTMLCanvasElement, camera: Camera) {
        this.canvas = canvas;
        this.camera = camera;
    }

    isActive(): boolean {
        return this.active;
    }

    toggle(container: HTMLElement): void {
        if (this.active) {
            this.deactivate();
        } else {
            this.activate(container);
        }
        this.onToggle?.(this.active);
    }

    setToggleCallback(cb: (active: boolean) => void): void {
        this.onToggle = cb;
    }

    private activate(container: HTMLElement): void {
        this.active = true;
        this.mode = "select";
        this.selectedAssetId = null;
        this.selectedObjectId = null;
        this.createPanel(container);
        this.canvas.addEventListener("click", this.handleCanvasClick);
        this.canvas.addEventListener("mousedown", this.handleCanvasMouseDown);
        this.canvas.addEventListener("mousemove", this.handleCanvasMouseMove);
        this.canvas.addEventListener("mouseup", this.handleCanvasMouseUp);
        this.canvas.addEventListener("contextmenu", this.handleRightClick);
        this.canvas.style.cursor = "default";

        // Load agents for owner dropdown
        fetchVillageAgents().then(agents => {
            this.agents = agents;
            this.populateOwnerDropdown();
        });
    }

    private deactivate(): void {
        this.active = false;
        this.selectedAssetId = null;
        this.selectedObjectId = null;
        this.ghostPos = null;
        this.cancelDrag();
        if (this.panel) {
            this.panel.remove();
            this.panel = null;
        }
        this.canvas.removeEventListener("click", this.handleCanvasClick);
        this.canvas.removeEventListener("mousedown", this.handleCanvasMouseDown);
        this.canvas.removeEventListener("mousemove", this.handleCanvasMouseMove);
        this.canvas.removeEventListener("mouseup", this.handleCanvasMouseUp);
        this.canvas.removeEventListener("contextmenu", this.handleRightClick);
        this.canvas.style.cursor = "default";
    }

    // Get the ghost preview position and catalog item for rendering
    getGhost(): { item: ReturnType<typeof getCatalogItem>; x: number; y: number } | null {
        if (!this.active || !this.selectedAssetId || !this.ghostPos) return null;
        const item = getCatalogItem(this.selectedAssetId);
        if (!item) return null;
        return { item, x: this.ghostPos.x, y: this.ghostPos.y };
    }

    // Get selected object ID for highlight rendering
    getSelectedObjectId(): string | null {
        return this.active ? this.selectedObjectId : null;
    }

    private handleCanvasClick = async (e: MouseEvent): Promise<void> => {
        // Don't handle click if we just finished a drag
        if (this.camera.isDragging() || this.dragging) return;
        const world = this.screenToWorld(e);
        if (!world) return;

        if (this.mode === "place" && this.selectedAssetId) {
            // Place the selected asset
            await addObject(this.selectedAssetId, world.x, world.y);
        } else if (this.mode === "select") {
            // Try to select an object near the click
            this.selectedObjectId = this.findObjectAt(world.x, world.y);
            this.updateDeleteButton();
        }
    };

    private handleCanvasMouseDown = (e: MouseEvent): void => {
        if (this.mode !== "select" || !this.selectedObjectId) return;

        const world = this.screenToWorld(e);
        if (!world) return;

        // Check if mousedown is on the currently selected object
        const hitId = this.findObjectAt(world.x, world.y);
        if (hitId !== this.selectedObjectId) return;

        // Start dragging the selected object
        const obj = getObjects().find(o => o.id === this.selectedObjectId);
        if (!obj) return;

        this.dragging = true;
        this.dragObjectId = this.selectedObjectId;
        this.dragStartWorld = { x: world.x, y: world.y };
        this.dragOriginalPos = { x: obj.x, y: obj.y };
        this.canvas.style.cursor = "grabbing";

        // Suppress camera panning while dragging an object
        this.camera.suppressDrag();

        // Start edge-pan timer (checks mouse position each frame)
        this.startEdgePan(e);
    };

    private handleCanvasMouseMove = (e: MouseEvent): void => {
        if (this.dragging && this.dragObjectId && this.dragStartWorld && this.dragOriginalPos) {
            // Update object position based on mouse delta
            const world = this.screenToWorld(e);
            if (!world) return;

            const dx = world.x - this.dragStartWorld.x;
            const dy = world.y - this.dragStartWorld.y;

            const obj = getObjects().find(o => o.id === this.dragObjectId);
            if (obj) {
                obj.x = this.dragOriginalPos.x + dx;
                obj.y = this.dragOriginalPos.y + dy;
            }

            // Update edge-pan based on screen position
            this.updateEdgePan(e);
            return;
        }

        if (this.mode === "place" && this.selectedAssetId) {
            const world = this.screenToWorld(e);
            if (world) {
                this.ghostPos = world;
            }
        }
    };

    private handleCanvasMouseUp = async (_e: MouseEvent): Promise<void> => {
        if (!this.dragging || !this.dragObjectId) return;

        const obj = getObjects().find(o => o.id === this.dragObjectId);
        this.stopEdgePan();
        this.camera.unsuppressDrag();
        this.canvas.style.cursor = "default";

        // Persist the new position to the API
        if (obj) {
            await moveObjectPosition(this.dragObjectId, obj.x, obj.y);
        }

        // Clear drag state after a short delay so the click handler doesn't fire
        const wasDragging = this.dragging;
        this.dragObjectId = null;
        this.dragStartWorld = null;
        this.dragOriginalPos = null;
        if (wasDragging) {
            setTimeout(() => { this.dragging = false; }, 50);
        }
    };

    private handleRightClick = (e: MouseEvent): void => {
        e.preventDefault();
        if (this.dragging) {
            // Cancel drag — restore original position
            if (this.dragObjectId && this.dragOriginalPos) {
                const obj = getObjects().find(o => o.id === this.dragObjectId);
                if (obj) {
                    obj.x = this.dragOriginalPos.x;
                    obj.y = this.dragOriginalPos.y;
                }
            }
            this.cancelDrag();
            return;
        }
        if (this.mode === "place") {
            // Cancel placement
            this.mode = "select";
            this.selectedAssetId = null;
            this.ghostPos = null;
            this.canvas.style.cursor = "default";
            this.clearCatalogSelection();
        }
    };

    private cancelDrag(): void {
        this.dragging = false;
        this.dragObjectId = null;
        this.dragStartWorld = null;
        this.dragOriginalPos = null;
        this.stopEdgePan();
        this.camera.unsuppressDrag();
        this.canvas.style.cursor = "default";
    }

    // Edge panning — scroll the camera when dragging near viewport edges
    private lastScreenPos: { x: number; y: number } | null = null;
    private static EDGE_ZONE = 60; // pixels from edge where panning starts
    private static EDGE_PAN_SPEED = 8; // world pixels per frame at full strength

    private startEdgePan(e: MouseEvent): void {
        this.lastScreenPos = { x: e.clientX, y: e.clientY };
        this.stopEdgePan();
        const tick = () => {
            if (!this.dragging || !this.lastScreenPos || !this.dragStartWorld || !this.dragOriginalPos) return;

            const rect = this.canvas.getBoundingClientRect();
            const sx = this.lastScreenPos.x - rect.left;
            const sy = this.lastScreenPos.y - rect.top;
            const w = this.canvas.width;
            const h = this.canvas.height;

            let panX = 0;
            let panY = 0;
            const zone = Editor.EDGE_ZONE;
            const speed = Editor.EDGE_PAN_SPEED / this.camera.zoom;

            // Calculate pan amount based on distance into the edge zone
            if (sx < zone) {
                panX = -speed * (1 - sx / zone);
            } else if (sx > w - zone) {
                panX = speed * (1 - (w - sx) / zone);
            }
            if (sy < zone) {
                panY = -speed * (1 - sy / zone);
            } else if (sy > h - zone) {
                panY = speed * (1 - (h - sy) / zone);
            }

            if (panX !== 0 || panY !== 0) {
                // Move camera
                this.camera.x += panX;
                this.camera.y += panY;

                // Also shift the drag start so the object follows the pan
                this.dragStartWorld.x -= panX;
                this.dragStartWorld.y -= panY;

                // Recalculate object position
                const world = this.camera.screenToWorld(sx, sy, w, h);
                const dx = world.x - this.dragStartWorld.x;
                const dy = world.y - this.dragStartWorld.y;
                const obj = getObjects().find(o => o.id === this.dragObjectId);
                if (obj) {
                    obj.x = this.dragOriginalPos.x + dx;
                    obj.y = this.dragOriginalPos.y + dy;
                }
            }

            this.edgePanTimer = requestAnimationFrame(tick);
        };
        this.edgePanTimer = requestAnimationFrame(tick);
    }

    private updateEdgePan(e: MouseEvent): void {
        this.lastScreenPos = { x: e.clientX, y: e.clientY };
    }

    private stopEdgePan(): void {
        if (this.edgePanTimer !== null) {
            cancelAnimationFrame(this.edgePanTimer);
            this.edgePanTimer = null;
        }
        this.lastScreenPos = null;
    }

    private screenToWorld(e: MouseEvent): { x: number; y: number } | null {
        const rect = this.canvas.getBoundingClientRect();
        const screenX = e.clientX - rect.left;
        const screenY = e.clientY - rect.top;
        const world = this.camera.screenToWorld(screenX, screenY, this.canvas.width, this.canvas.height);

        // Clamp to map bounds
        const dims = getMapDimensions();
        const maxX = dims.width * TILE_SIZE;
        const maxY = dims.height * TILE_SIZE;
        if (world.x < 0 || world.x > maxX || world.y < 0 || world.y > maxY) {
            return null;
        }
        return world;
    }

    private findObjectAt(wx: number, wy: number): string | null {
        const objects = getObjects();
        const SCALE = 2;
        let closest: { id: string; dist: number } | null = null;

        for (const obj of objects) {
            const item = getCatalogItem(obj.assetId, obj.currentState);
            if (!item) continue;

            const destW = item.srcW * SCALE;
            const destH = item.srcH * SCALE;
            const drawX = obj.x - destW * item.anchorX;
            const drawY = obj.y - destH * item.anchorY;

            if (wx >= drawX && wx <= drawX + destW && wy >= drawY && wy <= drawY + destH) {
                const cx = drawX + destW / 2;
                const cy = drawY + destH / 2;
                const dist = Math.sqrt((wx - cx) ** 2 + (wy - cy) ** 2);
                if (!closest || dist < closest.dist) {
                    closest = { id: obj.id, dist };
                }
            }
        }

        return closest?.id ?? null;
    }

    private createPanel(container: HTMLElement): void {
        const panel = document.createElement("div");
        panel.className = "editor-panel";
        panel.innerHTML = `
            <div class="panel-header">
                <h2>Village Editor</h2>
                <button class="panel-close" id="editor-close">&times;</button>
            </div>
            <div class="editor-tools">
                <button class="editor-tool-btn active" id="tool-select">Select</button>
                <button class="editor-tool-btn" id="tool-delete" disabled>Delete</button>
            </div>
            <div class="editor-selection" id="editor-selection" style="display:none">
                <div class="catalog-label">Selected Object</div>
                <div class="selection-info" id="selection-info"></div>
                <div class="field-group">
                    <label>Owner</label>
                    <select id="owner-select">
                        <option value="">— none —</option>
                    </select>
                </div>
            </div>
            <div class="editor-catalog" id="editor-catalog"></div>
        `;
        container.appendChild(panel);
        this.panel = panel;

        // Close button
        panel.querySelector("#editor-close")!.addEventListener("click", () => {
            this.toggle(container);
        });

        // Select tool
        panel.querySelector("#tool-select")!.addEventListener("click", () => {
            this.mode = "select";
            this.selectedAssetId = null;
            this.ghostPos = null;
            this.canvas.style.cursor = "default";
            this.clearCatalogSelection();
        });

        // Delete button
        panel.querySelector("#tool-delete")!.addEventListener("click", async () => {
            if (this.selectedObjectId) {
                await removeObject(this.selectedObjectId);
                this.selectedObjectId = null;
                this.updateDeleteButton();
            }
        });

        // Build catalog
        this.buildCatalog();
    }

    private buildCatalog(): void {
        const catalogEl = this.panel?.querySelector("#editor-catalog");
        if (!catalogEl) return;

        const categories: Array<{ id: Asset["category"]; label: string }> = [
            { id: "tree", label: "Trees" },
            { id: "nature", label: "Nature" },
            { id: "structure", label: "Structures" },
            { id: "prop", label: "Props" },
        ];

        for (const cat of categories) {
            const items = getAssetsByCategory(cat.id);
            if (items.length === 0) continue;

            const section = document.createElement("div");
            section.className = "catalog-section";
            section.innerHTML = `<div class="catalog-label">${cat.label}</div>`;

            const grid = document.createElement("div");
            grid.className = "catalog-grid";

            for (const asset of items) {
                // Resolve the default state sprite for the thumbnail
                const item = getCatalogItem(asset.id);
                if (!item) continue;

                const cell = document.createElement("div");
                cell.className = "catalog-item";
                cell.dataset.assetId = asset.id;
                cell.title = asset.name;

                // Create a canvas thumbnail
                const thumb = document.createElement("canvas");
                thumb.width = 48;
                thumb.height = 48;
                const tctx = thumb.getContext("2d")!;
                tctx.imageSmoothingEnabled = false;

                const img = new Image();
                img.src = item.sheet;
                img.onload = () => {
                    // Scale to fit in 48x48 while maintaining aspect ratio
                    const scale = Math.min(48 / item.srcW, 48 / item.srcH);
                    const dw = item.srcW * scale;
                    const dh = item.srcH * scale;
                    const dx = (48 - dw) / 2;
                    const dy = (48 - dh) / 2;
                    tctx.drawImage(img, item.srcX, item.srcY, item.srcW, item.srcH, dx, dy, dw, dh);
                };

                cell.appendChild(thumb);

                const label = document.createElement("span");
                label.className = "catalog-item-name";
                label.textContent = asset.name;
                cell.appendChild(label);

                cell.addEventListener("click", () => {
                    this.selectAsset(asset.id);
                });

                grid.appendChild(cell);
            }

            section.appendChild(grid);
            catalogEl.appendChild(section);
        }
    }

    private selectAsset(id: string): void {
        this.mode = "place";
        this.selectedAssetId = id;
        this.selectedObjectId = null;
        this.canvas.style.cursor = "crosshair";

        // Highlight in catalog
        this.clearCatalogSelection();
        const cell = this.panel?.querySelector(`[data-asset-id="${id}"]`);
        cell?.classList.add("selected");

        this.updateDeleteButton();
    }

    private clearCatalogSelection(): void {
        this.panel?.querySelectorAll(".catalog-item.selected").forEach(el => {
            el.classList.remove("selected");
        });
    }

    private updateDeleteButton(): void {
        const btn = this.panel?.querySelector("#tool-delete") as HTMLButtonElement | null;
        if (btn) {
            btn.disabled = !this.selectedObjectId;
        }
        this.updateSelectionPanel();
    }

    private updateSelectionPanel(): void {
        const selPanel = this.panel?.querySelector("#editor-selection") as HTMLElement | null;
        if (!selPanel) return;

        if (!this.selectedObjectId) {
            selPanel.style.display = "none";
            return;
        }

        const obj = getObjects().find(o => o.id === this.selectedObjectId);
        if (!obj) {
            selPanel.style.display = "none";
            return;
        }

        const item = getCatalogItem(obj.assetId, obj.currentState);
        const info = selPanel.querySelector("#selection-info") as HTMLElement;
        info.textContent = item?.name || obj.assetId;

        const select = selPanel.querySelector("#owner-select") as HTMLSelectElement;
        select.value = obj.owner || "";

        selPanel.style.display = "";
    }

    private populateOwnerDropdown(): void {
        const select = this.panel?.querySelector("#owner-select") as HTMLSelectElement | null;
        if (!select) return;

        // Keep the "none" option, add agents
        select.innerHTML = '<option value="">— none —</option>';
        for (const agent of this.agents) {
            const opt = document.createElement("option");
            opt.value = agent.llmMemoryAgent;
            opt.textContent = agent.name + (agent.isVirtual ? " (VA)" : "");
            select.appendChild(opt);
        }

        // Handle owner change
        select.onchange = async () => {
            if (!this.selectedObjectId) return;
            const owner = select.value || null;
            const ok = await setObjectOwner(this.selectedObjectId, owner);
            if (ok) {
                // Update local state
                const obj = getObjects().find(o => o.id === this.selectedObjectId);
                if (obj) obj.owner = owner;
            }
        };
    }

    // Draw editor overlays (ghost preview, selection highlight)
    renderOverlay(ctx: CanvasRenderingContext2D): void {
        if (!this.active) return;

        // Ghost preview
        const ghost = this.getGhost();
        if (ghost && ghost.item) {
            const SCALE = 2;
            const destW = ghost.item.srcW * SCALE;
            const destH = ghost.item.srcH * SCALE;
            const drawX = ghost.x - destW * ghost.item.anchorX;
            const drawY = ghost.y - destH * ghost.item.anchorY;

            const img = getSheetImage(ghost.item.sheet);
            if (img?.complete && img.naturalWidth > 0) {
                ctx.globalAlpha = 0.5;
                ctx.drawImage(
                    img,
                    ghost.item.srcX, ghost.item.srcY, ghost.item.srcW, ghost.item.srcH,
                    drawX, drawY, destW, destH
                );
                ctx.globalAlpha = 1.0;
            }
        }

        // Selection highlight
        if (this.selectedObjectId) {
            const objects = getObjects();
            const obj = objects.find(o => o.id === this.selectedObjectId);
            if (obj) {
                const item = getCatalogItem(obj.assetId, obj.currentState);
                if (item) {
                    const SCALE = 2;
                    const destW = item.srcW * SCALE;
                    const destH = item.srcH * SCALE;
                    const drawX = obj.x - destW * item.anchorX;
                    const drawY = obj.y - destH * item.anchorY;

                    ctx.strokeStyle = "#ffcc00";
                    ctx.lineWidth = 2 / this.camera.zoom;
                    ctx.setLineDash([6 / this.camera.zoom, 3 / this.camera.zoom]);
                    ctx.strokeRect(drawX, drawY, destW, destH);
                    ctx.setLineDash([]);
                }
            }
        }
    }
}

// Shared image cache (same as renderer)
const sheetCache = new Map<string, HTMLImageElement>();

function getSheetImage(src: string): HTMLImageElement | undefined {
    let img = sheetCache.get(src);
    if (!img) {
        img = new Image();
        img.src = src;
        sheetCache.set(src, img);
    }
    return img;
}
