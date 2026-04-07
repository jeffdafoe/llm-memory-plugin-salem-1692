// Side panels (config, etc.)

import { getConfig, saveConfig } from "./config";

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

    const body = document.createElement("div");
    body.className = "panel-body";

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

    panel.appendChild(body);
    container.appendChild(panel);

    return panel;
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
