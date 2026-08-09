import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/ui-v2/",
  plugins: [react()],
  build: {
    outDir: "../internal/api/static/ui-v2",
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
        manualChunks: {
          tanstack: ["@tanstack/react-query", "@tanstack/react-table", "@tanstack/react-virtual"],
          motion: ["framer-motion"]
        }
      }
    }
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/ws": {
        target: "ws://127.0.0.1:8080",
        ws: true
      }
    }
  }
});
