import { defineConfig } from "vite";

export default defineConfig({
    root: ".",
    build: {
        outDir: "dist",
        emptyOutDir: true
    },
    server: {
        port: 4300,
        proxy: {
            "/llm": {
                target: "https://llm-memory.net",
                changeOrigin: true,
                rewrite: (path: string) => path.replace(/^\/llm/, "/v1"),
            }
        }
    }
});
