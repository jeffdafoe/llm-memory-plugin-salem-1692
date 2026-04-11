import { verifyToken } from "./auth";
import { showLoginScreen } from "./ui";
import { createTopBar } from "./topbar";
import { showConfigPanel } from "./panels";
import { Camera } from "./camera";
import { Renderer } from "./renderer";
import { createMap } from "./map";
import { loadObjects, clearObjects, getObjects } from "./objects";
import { Editor } from "./editor";
import { fetchVillageMe, fetchVillageAgents, VillageAgent } from "./village-api";
import { getCatalogItem } from "./catalog";

const app = document.getElementById("app")!;

async function init(): Promise<void> {
    const valid = await verifyToken();

    if (valid) {
        startGame();
    } else {
        showLoginScreen(app, () => {
            startGame();
        });
    }
}

function startGame(): void {
    app.innerHTML = "";

    let activePanel: string | null = null;
    let panelEl: HTMLElement | null = null;

    // Top bar
    createTopBar(app, (panel) => {
        if (panel === "editor") {
            editor.toggle(app);
            return;
        }

        if (activePanel === panel) {
            // Close current panel
            if (panelEl) {
                panelEl.remove();
                panelEl = null;
            }
            activePanel = null;
            return;
        }
        // Close any existing panel
        if (panelEl) {
            panelEl.remove();
            panelEl = null;
        }
        activePanel = panel;

        if (panel === "config") {
            panelEl = showConfigPanel(
                app,
                () => {
                    activePanel = null;
                    panelEl = null;
                },
                () => {
                    // Config changed — regenerate map, re-populate objects, recenter
                    const newMap = createMap();
                    renderer.setMap(newMap);
                    clearObjects();
                    loadObjects();
                    camera.recenter();
                    renderer.resize();
                }
            );
        }
    });

    // Canvas
    const canvasEl = document.createElement("canvas");
    canvasEl.id = "village";
    app.appendChild(canvasEl);

    const camera = new Camera();
    const map = createMap();
    const renderer = new Renderer(canvasEl, camera, map);
    const editor = new Editor(canvasEl, camera);

    // Pass editor to renderer for overlay drawing
    renderer.setEditor(editor);

    // Update Edit button style when editor toggles
    editor.setToggleCallback((active) => {
        const btn = document.getElementById("editor-toggle-btn");
        if (btn) {
            btn.classList.toggle("editor-active", active);
        }
    });

    // Load placed objects (or generate initial village)
    loadObjects();

    // Check if user has editor permissions
    fetchVillageMe().then(me => {
        if (me?.canEdit) {
            const btn = document.getElementById("editor-toggle-btn");
            if (btn) btn.style.display = "";
        }
    });

    // Fetch agents and refresh periodically (every 30s)
    let currentAgents: VillageAgent[] = [];
    async function refreshAgents(): Promise<void> {
        currentAgents = await fetchVillageAgents();
        renderer.setAgents(currentAgents);
    }
    refreshAgents();
    setInterval(refreshAgents, 30000);

    camera.attach(canvasEl);

    function handleResize(): void {
        renderer.resize();
    }
    window.addEventListener("resize", handleResize);
    handleResize();

    // Tooltip for object info on hover
    const tooltip = document.createElement("div");
    tooltip.className = "village-tooltip";
    app.appendChild(tooltip);

    canvasEl.addEventListener("mousemove", (e) => {
        if (editor.isActive()) {
            tooltip.style.display = "none";
            return;
        }
        const rect = canvasEl.getBoundingClientRect();
        const screenX = e.clientX - rect.left;
        const screenY = e.clientY - rect.top;
        const world = camera.screenToWorld(screenX, screenY, canvasEl.width, canvasEl.height);

        // Check if hovering an outdoor agent dot
        const DOT_RADIUS = 8;
        let tooltipText = "";
        for (const agent of currentAgents) {
            if (agent.locationType !== "outdoor" || agent.locationX == null || agent.locationY == null) continue;
            const dx = world.x - agent.locationX;
            const dy = world.y - agent.locationY;
            if (dx * dx + dy * dy < DOT_RADIUS * DOT_RADIUS) {
                tooltipText = agent.name;
                break;
            }
        }

        // Check if hovering an object (building/structure)
        if (!tooltipText) {
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
                    const parts: string[] = [item.name];
                    if (obj.owner) parts.push(`Owner: ${obj.owner}`);
                    // List agents inside this object
                    const inside = currentAgents.filter(a =>
                        a.locationType === "inside" && a.locationObjectId === obj.id
                    );
                    if (inside.length > 0) {
                        parts.push("Inside: " + inside.map(a => a.name).join(", "));
                    }
                    if (parts.length > 1 || inside.length > 0) {
                        tooltipText = parts.join(" — ");
                    }
                    break;
                }
            }
        }

        if (tooltipText) {
            tooltip.textContent = tooltipText;
            tooltip.style.display = "";
            tooltip.style.left = (e.clientX + 12) + "px";
            tooltip.style.top = (e.clientY - 8) + "px";
        } else {
            tooltip.style.display = "none";
        }
    });

    canvasEl.addEventListener("mouseleave", () => {
        tooltip.style.display = "none";
    });

    function frame(): void {
        renderer.render();
        requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
}

init();
