import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// During `npm run dev`, proxy /api to the local Go server so the SPA and API
// share an origin. In production the Go binary serves the built dist itself.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
