// Sprite loader — loads tileset images and provides tile extraction

export class Tileset {
    private image: HTMLImageElement;
    private loaded: boolean = false;
    private srcTileSize: number;

    constructor(src: string, srcTileSize: number) {
        this.srcTileSize = srcTileSize;
        this.image = new Image();
        this.image.src = src;
        this.image.onload = () => {
            this.loaded = true;
        };
    }

    isLoaded(): boolean {
        return this.loaded;
    }

    // Draw a tile at a world position, scaling from srcTileSize to destSize.
    // overlap adds extra pixels to the destination to prevent sub-pixel gaps between tiles.
    draw(ctx: CanvasRenderingContext2D, col: number, row: number, worldX: number, worldY: number, destSize: number, overlap: number = 0): void {
        if (!this.loaded) {
            return;
        }
        ctx.drawImage(
            this.image,
            col * this.srcTileSize, row * this.srcTileSize,
            this.srcTileSize, this.srcTileSize,
            worldX, worldY,
            destSize + overlap, destSize + overlap
        );
    }
}
