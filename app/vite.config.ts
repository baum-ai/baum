import { defineConfig } from "vite";
import tailwindcss from "@tailwindcss/vite";
export default defineConfig({
  // The server serves the app under a secret /s/<namespace>/ path, so built
  // asset URLs resolve against the page rather than the origin root.
  base: "./",
  plugins: [tailwindcss()],
  build: { outDir: "../internal/localapp/dist", emptyOutDir: true, sourcemap: false },
  resolve: { dedupe: ["react", "react-dom"] },
});
