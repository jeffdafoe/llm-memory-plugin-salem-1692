// Village configuration — loaded from localStorage, editable via admin panel

const CONFIG_KEY = "village_config";

export interface VillageConfig {
    mapWidth: number;
    mapHeight: number;
}

const DEFAULTS: VillageConfig = {
    mapWidth: 80,
    mapHeight: 45,
};

let current: VillageConfig = load();

function load(): VillageConfig {
    try {
        const stored = localStorage.getItem(CONFIG_KEY);
        if (stored) {
            const parsed = JSON.parse(stored);
            return { ...DEFAULTS, ...parsed };
        }
    } catch {
        // ignore
    }
    return { ...DEFAULTS };
}

export function getConfig(): VillageConfig {
    return current;
}

export function saveConfig(config: VillageConfig): void {
    current = { ...config };
    localStorage.setItem(CONFIG_KEY, JSON.stringify(current));
}
