import { cpSync, mkdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { desktopDevelopment } from "./vite.development.ts";

// The same build supplies the daemon app and Tauri's own screens: setup, the
// tray panel, and the connecting screen the window opens on, which becomes
// Tauri's index.html. The connecting screen and the tray panel are taken out
// of the daemon's copy.
function desktopBundle(): Plugin {
  let output = "";
  return {
    name: "desktop-bundle",
    apply: "build",
    configResolved(config) {
      output = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      const destination = resolve(import.meta.dirname, "../desktop/dist");
      rmSync(destination, { recursive: true, force: true });
      mkdirSync(destination, { recursive: true });
      cpSync(output, destination, { recursive: true });
      renameSync(join(destination, "connecting.html"), join(destination, "index.html"));
      rmSync(join(output, "connecting.html"));
      rmSync(join(output, "tray.html"));
    },
  };
}

// Vite empties the output directory first; this puts back the tracked, empty
// .gitkeep that keeps the directory, which go:embed needs, in a checkout
// without a build.
function keepPlaceholder(): Plugin {
  let outDir = "";
  return {
    name: "keep-placeholder",
    apply: "build",
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      writeFileSync(join(outDir, ".gitkeep"), "");
    },
  };
}

export default defineConfig({
  // The server serves the app under a secret /s/<namespace>/ path, so built
  // asset URLs resolve against the page rather than the origin root.
  base: "./",
  plugins: [
    react(),
    tailwindcss(),
    keepPlaceholder(),
    desktopBundle(),
    ...(process.env.BAUM_DESKTOP_DEV_DIR
      ? [desktopDevelopment(process.env.BAUM_DESKTOP_DEV_DIR)]
      : []),
  ],
  build: {
    assetsInlineLimit: 0,
    outDir: "../internal/localapp/dist",
    emptyOutDir: true,
    sourcemap: false,
    rolldownOptions: {
      input: {
        app: resolve(import.meta.dirname, "index.html"),
        setup: resolve(import.meta.dirname, "setup.html"),
        connecting: resolve(import.meta.dirname, "connecting.html"),
        tray: resolve(import.meta.dirname, "tray.html"),
      },
    },
  },
  resolve: { dedupe: ["react", "react-dom"] },
});
