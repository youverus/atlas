import { defineConfig } from "vite";
import type { Plugin } from "vite";
import react from "@vitejs/plugin-react";

// KaTeX ships three font formats for each of its twenty families: woff2, woff
// and ttf, chained in one `src:` per @font-face. Every engine this window runs
// in — WKWebView here, Chromium for `vite dev` — reads woff2, so the other two
// are 876 KB of files that would be committed into dist and embedded in the
// binary to be fetched by nobody. Dropping them leaves the 296 KB actually used.
function katexWoff2Only(): Plugin {
  return {
    name: "atlas:katex-woff2-only",
    enforce: "pre",
    transform(code, id) {
      if (!id.includes("/katex/") || !id.endsWith(".css")) return null;
      const trimmed = code.replace(
        /,url\([^)]*\.woff\) format\("woff"\)|,url\([^)]*\.ttf\) format\("truetype"\)/g,
        "",
      );
      return trimmed === code ? null : { code: trimmed, map: null };
    },
  };
}

// The build lands inside the Go package that embeds it, so `go build` stays a
// single command: the assets are ordinary files by the time the compiler looks
// for them. Nothing here runs during a Go build.
export default defineConfig({
  plugins: [react(), katexWoff2Only()],
  build: {
    outDir: "../internal/httpapi/web/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
    // KaTeX is 278 KB of this bundle and any answer containing a formula needs
    // it, so it is not split out: a second chunk would make the first formula
    // arrive a frame after the text around it. The api contract layer is a
    // shared chunk since the desktop pet's page imports it too, which pushes
    // the main chunk just past 800 KB; 950 keeps the build quiet.
    chunkSizeWarningLimit: 950,
    // Two entries: the main window and the desktop pet's tiny companion page
    // (/pet.html), which the shell loads into a second frameless window.
    rollupOptions: {
      input: {
        main: "index.html",
        pet: "pet.html",
      },
    },
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
