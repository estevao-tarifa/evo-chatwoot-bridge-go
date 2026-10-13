import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { defineConfig } from "vite";

// Dev-only proxy so `npm run dev` can talk to a locally running bridge without
// CORS. Production uses window.__BRIDGE_CONFIG__.apiBaseUrl (or same-origin).
const devProxyTarget =
  process.env.VITE_API_PROXY_TARGET ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
    },
  },
  build: {
    outDir: path.resolve(import.meta.dirname, "dist"),
    emptyOutDir: true,
  },
  server: {
    host: true,
    port: 5173,
    proxy: {
      "/api": { target: devProxyTarget, changeOrigin: true },
    },
  },
});
