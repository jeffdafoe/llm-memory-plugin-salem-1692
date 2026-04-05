// Top navigation bar

import { getAgentName, clearSession } from "./auth";
import { TOP_BAR_HEIGHT } from "./constants";

type PanelCallback = (panel: string | null) => void;

export function createTopBar(container: HTMLElement, onPanel: PanelCallback): HTMLElement {
    const bar = document.createElement("div");
    bar.className = "top-bar";
    bar.style.height = TOP_BAR_HEIGHT + "px";

    // Left side — title
    const left = document.createElement("div");
    left.className = "top-bar-left";
    left.textContent = "Salem — 1692";
    bar.appendChild(left);

    // Right side — buttons
    const right = document.createElement("div");
    right.className = "top-bar-right";

    // Config button
    const configBtn = document.createElement("button");
    configBtn.textContent = "Config";
    configBtn.className = "top-bar-btn";
    configBtn.addEventListener("click", () => {
        onPanel("config");
    });
    right.appendChild(configBtn);

    // Agent name display
    const agentName = getAgentName();
    if (agentName) {
        const nameSpan = document.createElement("span");
        nameSpan.className = "top-bar-agent";
        nameSpan.textContent = agentName;
        right.appendChild(nameSpan);
    }

    // Logout button
    const logoutBtn = document.createElement("button");
    logoutBtn.textContent = "Logout";
    logoutBtn.className = "top-bar-btn";
    logoutBtn.addEventListener("click", () => {
        clearSession();
        window.location.reload();
    });
    right.appendChild(logoutBtn);

    bar.appendChild(right);
    container.appendChild(bar);

    return bar;
}
