// Side panels (config, asset reference)

import { getConfig, saveConfig } from "./config";
import { getAssets, Asset } from "./catalog";

type CloseCallback = () => void;
type ConfigChangeCallback = () => void;

export function showConfigPanel(
    container: HTMLElement,
    onClose: CloseCallback,
    onChange: ConfigChangeCallback
): HTMLElement {
    const panel = document.createElement("div");
    panel.className = "side-panel";

    const header = document.createElement("div");
    header.className = "panel-header";

    const title = document.createElement("h2");
    title.textContent = "Configuration";
    header.appendChild(title);

    const closeBtn = document.createElement("button");
    closeBtn.textContent = "X";
    closeBtn.className = "panel-close";
    closeBtn.addEventListener("click", () => {
        panel.remove();
        onClose();
    });
    header.appendChild(closeBtn);
    panel.appendChild(header);

    // Tab bar
    const tabs = document.createElement("div");
    tabs.className = "panel-tabs";

    const configTab = document.createElement("button");
    configTab.className = "panel-tab active";
    configTab.textContent = "Settings";
    tabs.appendChild(configTab);

    const assetsTab = document.createElement("button");
    assetsTab.className = "panel-tab";
    assetsTab.textContent = "Assets";
    tabs.appendChild(assetsTab);

    panel.appendChild(tabs);

    // Tab content containers
    const configBody = document.createElement("div");
    configBody.className = "panel-body";
    buildConfigBody(configBody, onChange);
    panel.appendChild(configBody);

    const assetsBody = document.createElement("div");
    assetsBody.className = "panel-body panel-body-assets";
    assetsBody.style.display = "none";
    buildAssetsBody(assetsBody);
    panel.appendChild(assetsBody);

    // Tab switching
    configTab.addEventListener("click", () => {
        configTab.classList.add("active");
        assetsTab.classList.remove("active");
        configBody.style.display = "";
        assetsBody.style.display = "none";
        panel.classList.remove("side-panel-wide");
    });

    assetsTab.addEventListener("click", () => {
        assetsTab.classList.add("active");
        configTab.classList.remove("active");
        assetsBody.style.display = "";
        configBody.style.display = "none";
        panel.classList.add("side-panel-wide");
    });

    container.appendChild(panel);
    return panel;
}

function buildConfigBody(body: HTMLElement, onChange: ConfigChangeCallback): void {
    const config = getConfig();

    // Map width
    const widthGroup = createNumberField("Map width (tiles)", config.mapWidth, 16, 256);
    body.appendChild(widthGroup.element);

    // Map height
    const heightGroup = createNumberField("Map height (tiles)", config.mapHeight, 16, 256);
    body.appendChild(heightGroup.element);

    // Apply button
    const applyBtn = document.createElement("button");
    applyBtn.textContent = "Apply";
    applyBtn.className = "panel-btn";
    applyBtn.addEventListener("click", () => {
        saveConfig({
            mapWidth: widthGroup.getValue(),
            mapHeight: heightGroup.getValue(),
        });
        onChange();
    });
    body.appendChild(applyBtn);
}

function buildAssetsBody(body: HTMLElement): void {
    const assets = getAssets();

    if (assets.length === 0) {
        body.innerHTML = '<div class="assets-empty">No assets loaded</div>';
        return;
    }

    // Stats
    let totalStates = 0;
    for (const asset of assets) {
        totalStates += asset.states.length;
    }
    const stats = document.createElement("div");
    stats.className = "assets-stats";
    stats.textContent = `${assets.length} assets, ${totalStates} states`;
    body.appendChild(stats);

    // Group by category
    const categories: Record<string, Asset[]> = {};
    const categoryOrder = ["tree", "nature", "structure", "prop"];
    const categoryLabels: Record<string, string> = {
        tree: "Trees",
        nature: "Nature",
        structure: "Structures",
        prop: "Props",
    };

    for (const asset of assets) {
        if (!categories[asset.category]) {
            categories[asset.category] = [];
        }
        categories[asset.category].push(asset);
    }

    for (const catId of categoryOrder) {
        const catAssets = categories[catId];
        if (!catAssets || catAssets.length === 0) continue;

        const section = document.createElement("div");
        section.className = "assets-category";

        const catHeader = document.createElement("div");
        catHeader.className = "assets-category-header";
        catHeader.textContent = categoryLabels[catId] || catId;
        section.appendChild(catHeader);

        const grid = document.createElement("div");
        grid.className = "assets-grid";

        for (const asset of catAssets) {
            const card = document.createElement("div");
            card.className = "asset-card";

            const nameEl = document.createElement("div");
            nameEl.className = "asset-card-name";
            nameEl.textContent = asset.name;
            card.appendChild(nameEl);

            const idEl = document.createElement("div");
            idEl.className = "asset-card-id";
            idEl.textContent = asset.id;
            card.appendChild(idEl);

            // Pack info
            if (asset.pack) {
                const packEl = document.createElement("div");
                packEl.className = "asset-card-pack";
                if (asset.pack.url) {
                    const link = document.createElement("a");
                    link.href = asset.pack.url;
                    link.target = "_blank";
                    link.rel = "noopener";
                    link.textContent = asset.pack.name;
                    packEl.appendChild(link);
                } else {
                    packEl.textContent = asset.pack.name;
                }
                card.appendChild(packEl);
            }

            // State sprites
            const statesEl = document.createElement("div");
            statesEl.className = "asset-card-states";

            for (const state of asset.states) {
                const stateEl = document.createElement("div");
                stateEl.className = "asset-card-state";
                if (state.state === asset.defaultState) {
                    stateEl.classList.add("state-default");
                }

                const canvas = document.createElement("canvas");
                drawStateSprite(canvas, state);
                stateEl.appendChild(canvas);

                const label = document.createElement("div");
                label.className = "asset-state-label";
                label.textContent = state.state;
                stateEl.appendChild(label);

                statesEl.appendChild(stateEl);
            }

            card.appendChild(statesEl);
            grid.appendChild(card);
        }

        section.appendChild(grid);
        body.appendChild(section);
    }
}

// Draw a sprite state thumbnail on a canvas
const sheetCache = new Map<string, HTMLImageElement>();

function drawStateSprite(canvas: HTMLCanvasElement, state: { sheet: string; srcX: number; srcY: number; srcW: number; srcH: number }): void {
    const maxThumb = 64;
    const scale = Math.min(2, maxThumb / state.srcW, maxThumb / state.srcH);
    const w = Math.round(state.srcW * scale);
    const h = Math.round(state.srcH * scale);
    canvas.width = w;
    canvas.height = h;

    const ctx = canvas.getContext("2d")!;
    ctx.imageSmoothingEnabled = false;

    let img = sheetCache.get(state.sheet);
    if (!img) {
        img = new Image();
        img.src = state.sheet;
        sheetCache.set(state.sheet, img);
    }

    function draw() {
        ctx.clearRect(0, 0, w, h);
        ctx.drawImage(img!,
            state.srcX, state.srcY, state.srcW, state.srcH,
            0, 0, w, h
        );
    }

    if (img.complete && img.naturalWidth > 0) {
        draw();
    } else {
        img.addEventListener("load", draw);
    }
}

interface NumberField {
    element: HTMLElement;
    getValue: () => number;
}

function createNumberField(label: string, value: number, min: number, max: number): NumberField {
    const group = document.createElement("div");
    group.className = "field-group";

    const labelEl = document.createElement("label");
    labelEl.textContent = label;
    group.appendChild(labelEl);

    const input = document.createElement("input");
    input.type = "number";
    input.value = String(value);
    input.min = String(min);
    input.max = String(max);
    group.appendChild(input);

    return {
        element: group,
        getValue: () => {
            const v = parseInt(input.value, 10);
            if (isNaN(v)) {
                return value;
            }
            return Math.max(min, Math.min(max, v));
        },
    };
}
