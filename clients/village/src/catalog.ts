// Asset catalog — fetched from the server API at startup.
// Each asset has one or more visual states (e.g. open/closed for stalls).

export interface AssetState {
    state: string;
    sheet: string;
    srcX: number;
    srcY: number;
    srcW: number;
    srcH: number;
}

export interface TilesetPack {
    id: string;
    name: string;
    url: string | null;
}

export interface Asset {
    id: string;
    name: string;
    category: "tree" | "nature" | "structure" | "prop";
    defaultState: string;
    anchorX: number;
    anchorY: number;
    layer: "objects" | "above";
    pack: TilesetPack | null;
    states: AssetState[];
}

// CatalogItem is the resolved sprite info for a specific asset+state combo.
// Used by the renderer and editor — they don't need to know about states,
// just "give me the sprite for this asset in this state."
export interface CatalogItem {
    id: string;
    name: string;
    category: "tree" | "nature" | "structure" | "prop";
    sheet: string;
    srcX: number;
    srcY: number;
    srcW: number;
    srcH: number;
    anchorX: number;
    anchorY: number;
    layer: "objects" | "above";
}

let assets: Asset[] = [];
const assetMap = new Map<string, Asset>();

// Load asset catalog from the API. Must be called before rendering starts.
export async function loadCatalog(): Promise<void> {
    try {
        const resp = await fetch("/api/assets");
        if (!resp.ok) {
            throw new Error(`HTTP ${resp.status}`);
        }
        const data = await resp.json();
        assets = data.map((a: any) => ({
            id: a.id,
            name: a.name,
            category: a.category,
            defaultState: a.default_state,
            anchorX: a.anchor_x,
            anchorY: a.anchor_y,
            layer: a.layer,
            pack: a.pack ? { id: a.pack.id, name: a.pack.name, url: a.pack.url } : null,
            states: (a.states || []).map((s: any) => ({
                state: s.state,
                sheet: s.sheet,
                srcX: s.src_x,
                srcY: s.src_y,
                srcW: s.src_w,
                srcH: s.src_h,
            })),
        }));
        assetMap.clear();
        for (const asset of assets) {
            assetMap.set(asset.id, asset);
        }
    } catch (err) {
        console.error("Failed to load asset catalog:", err);
        // Renderer will show nothing if catalog is empty
    }
}

// Get the full asset definition (including all states)
export function getAsset(id: string): Asset | undefined {
    return assetMap.get(id);
}

// Get all assets
export function getAssets(): Asset[] {
    return assets;
}

// Resolve a CatalogItem for a specific asset+state combination.
// This is the main function used by the renderer — it returns the sprite
// info needed to draw the object in its current state.
export function getCatalogItem(assetId: string, state?: string): CatalogItem | undefined {
    const asset = assetMap.get(assetId);
    if (!asset) return undefined;

    // Find the requested state, or fall back to default, or first available
    const targetState = state || asset.defaultState;
    let stateInfo = asset.states.find(s => s.state === targetState);
    if (!stateInfo && asset.states.length > 0) {
        stateInfo = asset.states[0];
    }
    if (!stateInfo) return undefined;

    return {
        id: asset.id,
        name: asset.name,
        category: asset.category,
        sheet: stateInfo.sheet,
        srcX: stateInfo.srcX,
        srcY: stateInfo.srcY,
        srcW: stateInfo.srcW,
        srcH: stateInfo.srcH,
        anchorX: asset.anchorX,
        anchorY: asset.anchorY,
        layer: asset.layer,
    };
}

// Get assets filtered by category (used by the editor palette)
export function getAssetsByCategory(category: Asset["category"]): Asset[] {
    return assets.filter(a => a.category === category);
}
