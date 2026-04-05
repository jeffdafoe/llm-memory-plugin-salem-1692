// Wang terrain indices — must match the Mana Seed wang tile definitions
// These are the 6 ground textures in the wang tileset
export const WangTerrain = {
    DIRT: 1,
    LIGHT_GRASS: 2,
    DARK_GRASS: 3,
    COBBLESTONE: 4,
    SHALLOW_WATER: 5,
    DEEP_WATER: 6,
} as const;

export type WangTerrainType = typeof WangTerrain[keyof typeof WangTerrain];

// Logical terrain types used in map generation (mapped to wang indices for rendering)
export const Terrain = {
    GRASS: WangTerrain.LIGHT_GRASS,
    GRASS_DARK: WangTerrain.DARK_GRASS,
    DIRT_PATH: WangTerrain.DIRT,
    STONE: WangTerrain.COBBLESTONE,
    WATER: WangTerrain.SHALLOW_WATER,
    DEEP_WATER: WangTerrain.DEEP_WATER,
} as const;
