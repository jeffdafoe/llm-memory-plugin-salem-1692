extends Node
## Terrain types — the ground ids stored in village_terrain. The ground pack
## (terrain_pack.gd) maps each id to its tiles.

const DIRT: int = 1
const LIGHT_GRASS: int = 2
const DARK_GRASS: int = 3
const COBBLESTONE: int = 4
const SHALLOW_WATER: int = 5
const DEEP_WATER: int = 6

# Logical terrain aliases
const GRASS: int = LIGHT_GRASS
const GRASS_DARK: int = DARK_GRASS
const DIRT_PATH: int = DIRT
const STONE: int = COBBLESTONE
const WATER: int = SHALLOW_WATER
