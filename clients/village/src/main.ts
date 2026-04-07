import { verifyToken } from "./auth";
import { showLoginScreen } from "./ui";
import { createTopBar } from "./topbar";
import { showConfigPanel } from "./panels";
import { Camera } from "./camera";
import { Renderer } from "./renderer";
import { createMap } from "./map";

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
                    // Config changed — regenerate map and recenter camera
                    const newMap = createMap();
                    renderer.setMap(newMap);
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

    camera.attach(canvasEl);

    function handleResize(): void {
        renderer.resize();
    }
    window.addEventListener("resize", handleResize);
    handleResize();

    function frame(): void {
        renderer.render();
        requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
}

init();
