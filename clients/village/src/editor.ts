// In-game village editor — catalog sidebar, click-to-place, select & delete

import { CATALOG, CatalogItem, getCatalogItem } from "./catalog";
import { addObject, getObjects, removeObject } from "./objects";
import { Camera } from "./camera";
import { TILE_SIZE } from "./constants";
import { fetchVillageAgents, setObjectOwner, VillageAgent } from "./village-api";
import { getMapDimensions } from "./map";

type EditorMode = "select" | "place";

export class Editor {
    private canvas: HTMLCanvasElement;
    private camera: Camera;
    private panel: HTMLElement | null = null;
    private active = false;
    private mode: EditorMode = "select";
    private selectedCatalogId: string | null = null;
    private selectedObjectId: string | null = null;
    private ghostPos: { x: number; y: number } | null = null;
    private onToggle: ((active: boolean) => void) | null = null;
    private agents: VillageAgent[] = [];

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
        this.selectedCatalogId = null;
        this.selectedObjectId = null;
        this.createPanel(container);
        this.canvas.addEventListener("click", this.handleCanvasClick);
        this.canvas.addEventListener("mousemove", this.handleCanvasMouseMove);
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
        this.selectedCatalogId = null;
        this.selectedObjectId = null;
        this.ghostPos = null;
        if (this.panel) {
            this.panel.remove();
            this.panel = null;
        }
        this.canvas.removeEventListener("click", this.handleCanvasClick);
        this.canvas.removeEventListener("mousemove", this.handleCanvasMouseMove);
        this.canvas.removeEventListener("contextmenu", this.handleRightClick);
        this.canvas.style.cursor = "default";
    }

    // Get the ghost preview position and catalog item for rendering
    getGhost(): { item: CatalogItem; x: number; y: number } | null {
        if (!this.active || !this.selectedCatalogId || !this.ghostPos) return null;
        const item = getCatalogItem(this.selectedCatalogId);
        if (!item) return null;
        return { item, x: this.ghostPos.x, y: this.ghostPos.y };
    }

    // Get selected object ID for highlight rendering
    getSelectedObjectId(): string | null {
        return this.active ? this.selectedObjectId : null;
    }

    private handleCanvasClick = async (e: MouseEvent): Promise<void> => {
        if (this.camera.isDragging()) return;
        const world = this.screenToWorld(e);
        if (!world) return;

        if (this.mode === "place" && this.selectedCatalogId) {
            // Place the selected catalog item
            await addObject(this.selectedCatalogId, world.x, world.y);
        } else if (this.mode === "select") {
            // Try to select an object near the click
            this.selectedObjectId = this.findObjectAt(world.x, world.y);
            this.updateDeleteButton();
        }
    };

    private handleCanvasMouseMove = (e: MouseEvent): void => {
        if (this.mode === "place" && this.selectedCatalogId) {
            const world = this.screenToWorld(e);
            if (world) {
                this.ghostPos = world;
            }
        }
    };

    private handleRightClick = (e: MouseEvent): void => {
        e.preventDefault();
        if (this.mode === "place") {
            // Cancel placement
            this.mode = "select";
            this.selectedCatalogId = null;
            this.ghostPos = null;
            this.canvas.style.cursor = "default";
            this.clearCatalogSelection();
        }
    };

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
            const item = getCatalogItem(obj.catalogId);
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
            this.selectedCatalogId = null;
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

        const categories: Array<{ id: CatalogItem["category"]; label: string }> = [
            { id: "tree", label: "Trees" },
            { id: "nature", label: "Nature" },
            { id: "structure", label: "Structures" },
            { id: "prop", label: "Props" },
        ];

        for (const cat of categories) {
            const items = CATALOG.filter(i => i.category === cat.id);
            if (items.length === 0) continue;

            const section = document.createElement("div");
            section.className = "catalog-section";
            section.innerHTML = `<div class="catalog-label">${cat.label}</div>`;

            const grid = document.createElement("div");
            grid.className = "catalog-grid";

            for (const item of items) {
                const cell = document.createElement("div");
                cell.className = "catalog-item";
                cell.dataset.catalogId = item.id;
                cell.title = item.name;

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
                label.textContent = item.name;
                cell.appendChild(label);

                cell.addEventListener("click", () => {
                    this.selectCatalogItem(item.id);
                });

                grid.appendChild(cell);
            }

            section.appendChild(grid);
            catalogEl.appendChild(section);
        }
    }

    private selectCatalogItem(id: string): void {
        this.mode = "place";
        this.selectedCatalogId = id;
        this.selectedObjectId = null;
        this.canvas.style.cursor = "crosshair";

        // Highlight in catalog
        this.clearCatalogSelection();
        const cell = this.panel?.querySelector(`[data-catalog-id="${id}"]`);
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

        const item = getCatalogItem(obj.catalogId);
        const info = selPanel.querySelector("#selection-info") as HTMLElement;
        info.textContent = item?.name || obj.catalogId;

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
        if (ghost) {
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
                const item = getCatalogItem(obj.catalogId);
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
