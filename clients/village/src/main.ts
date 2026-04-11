import { verifyToken } from "./auth";
import { showLoginScreen } from "./ui";
import { createTopBar } from "./topbar";
import { showConfigPanel } from "./panels";
import { Camera } from "./camera";
import { Renderer } from "./renderer";
import { createMap } from "./map";
import { loadObjects, clearObjects } from "./objects";
import { Editor } from "./editor";
import { fetchVillageMe, fetchVillageAgents, VillageAgent } from "./village-api";

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

    // Track mouse position for canvas-rendered tooltip
    let mouseScreen: { x: number; y: number } | null = null;
    canvasEl.addEventListener("mousemove", (e) => {
        const rect = canvasEl.getBoundingClientRect();
        mouseScreen = { x: e.clientX - rect.left, y: e.clientY - rect.top };
    });
    canvasEl.addEventListener("mouseleave", () => {
        mouseScreen = null;
    });
    // Pass state to renderer for canvas-drawn tooltips
    renderer.setTooltipState(() => ({
        mouse: mouseScreen,
        agents: currentAgents,
        editorActive: editor.isActive(),
    }));

    function frame(): void {
        renderer.render();
        requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
}

init();
