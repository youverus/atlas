import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands inside the Go package that embeds it, so `go build` stays a
// single command: the assets are ordinary files by the time the compiler looks
// for them. Nothing here runs during a Go build.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/httpapi/web/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    // `vite dev` talks to a running atlas-desktop through this proxy, so the
    // frontend can be worked on without rebuilding the binary.
    proxy: {
      "/api": {
        target: "http://127.0.0.1:7788",
        changeOrigin: true,
      },
    },
  },
});
