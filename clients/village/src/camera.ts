import { MIN_ZOOM, MAX_ZOOM, ZOOM_SPEED, DEFAULT_ZOOM, TILE_SIZE } from "./constants";
import { getMapDimensions } from "./map";

export class Camera {
    // Camera position in world pixels (center of viewport)
    x: number;
    y: number;
    zoom: number;

    // Drag state
    private dragging = false;
    private dragStartX = 0;
    private dragStartY = 0;
    private cameraStartX = 0;
    private cameraStartY = 0;

    constructor() {
        const dims = getMapDimensions();
        this.x = (dims.width * TILE_SIZE) / 2;
        this.y = (dims.height * TILE_SIZE) / 2;
        this.zoom = DEFAULT_ZOOM;
    }

    recenter(): void {
        const dims = getMapDimensions();
        this.x = (dims.width * TILE_SIZE) / 2;
        this.y = (dims.height * TILE_SIZE) / 2;
    }

    clamp(_canvasWidth: number, _canvasHeight: number): void {
        const dims = getMapDimensions();
        const worldW = dims.width * TILE_SIZE;
        const worldH = dims.height * TILE_SIZE;

        const margin = 4 * TILE_SIZE;

        const minX = -margin;
        const maxX = worldW + margin;
        const minY = -margin;
        const maxY = worldH + margin;

        if (this.x < minX) {
            this.x = minX;
        }
        if (this.x > maxX) {
            this.x = maxX;
        }
        if (this.y < minY) {
            this.y = minY;
        }
        if (this.y > maxY) {
            this.y = maxY;
        }
    }

    attach(canvas: HTMLCanvasElement): void {
        canvas.addEventListener("mousedown", (e) => this.onMouseDown(e));
        canvas.addEventListener("mousemove", (e) => this.onMouseMove(e, canvas));
        canvas.addEventListener("mouseup", () => this.onMouseUp());
        canvas.addEventListener("mouseleave", () => this.onMouseUp());
        canvas.addEventListener("wheel", (e) => this.onWheel(e, canvas), { passive: false });
    }

    apply(ctx: CanvasRenderingContext2D, canvasWidth: number, canvasHeight: number): void {
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.translate(canvasWidth / 2, canvasHeight / 2);
        ctx.scale(this.zoom, this.zoom);
        ctx.translate(-this.x, -this.y);
    }

    private onMouseDown(e: MouseEvent): void {
        this.dragging = true;
        this.dragStartX = e.clientX;
        this.dragStartY = e.clientY;
        this.cameraStartX = this.x;
        this.cameraStartY = this.y;
    }

    private onMouseMove(e: MouseEvent, canvas: HTMLCanvasElement): void {
        if (!this.dragging) {
            return;
        }
        const dx = (e.clientX - this.dragStartX) / this.zoom;
        const dy = (e.clientY - this.dragStartY) / this.zoom;
        this.x = this.cameraStartX - dx;
        this.y = this.cameraStartY - dy;
        this.clamp(canvas.width, canvas.height);
    }

    private onMouseUp(): void {
        this.dragging = false;
    }

    private onWheel(e: WheelEvent, canvas: HTMLCanvasElement): void {
        e.preventDefault();

        const rect = canvas.getBoundingClientRect();
        const mouseScreenX = e.clientX - rect.left;
        const mouseScreenY = e.clientY - rect.top;
        const worldX = this.x + (mouseScreenX - canvas.width / 2) / this.zoom;
        const worldY = this.y + (mouseScreenY - canvas.height / 2) / this.zoom;

        if (e.deltaY < 0) {
            this.zoom = Math.min(MAX_ZOOM, this.zoom + ZOOM_SPEED);
        } else {
            this.zoom = Math.max(MIN_ZOOM, this.zoom - ZOOM_SPEED);
        }

        this.x = worldX - (mouseScreenX - canvas.width / 2) / this.zoom;
        this.y = worldY - (mouseScreenY - canvas.height / 2) / this.zoom;
        this.clamp(canvas.width, canvas.height);
    }
}
